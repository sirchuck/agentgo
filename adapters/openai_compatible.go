package adapters

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type openAICompatibleAdapter struct{}

type openAICompatibleChatImageURL struct {
	URL string `json:"url"`
}

type openAICompatibleChatContentPart struct {
	Type     string                        `json:"type"`
	Text     string                        `json:"text,omitempty"`
	ImageURL *openAICompatibleChatImageURL `json:"image_url,omitempty"`
}

type openAICompatibleChatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type openAICompatibleChatRequest struct {
	Model       string                        `json:"model"`
	Messages    []openAICompatibleChatMessage `json:"messages"`
	Temperature *float64                      `json:"temperature,omitempty"`
	MaxTokens   int                           `json:"max_tokens,omitempty"`
	Stream      bool                          `json:"stream"`
}

type openAICompatibleChatResponse struct {
	Choices []struct {
		Message struct {
			Content any `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Execute sends one AgentGO request to an OpenAI-compatible endpoint chosen by api_path.
func (openAICompatibleAdapter) Execute(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	prepared, err := prepareOpenAICompatibleModel(model)
	if err != nil {
		return Response{}, err
	}
	endpoint := strings.TrimSpace(modelEndpoint(prepared))
	if endpoint == "" {
		return Response{}, errors.New("missing compatible API endpoint")
	}
	if isChatCompletionsEndpoint(endpoint) {
		return executeOpenAICompatibleChat(ctx, prepared, req, endpoint)
	}
	return executeOpenAICompatibleResponses(ctx, prepared, req, endpoint)
}

// prepareOpenAICompatibleModel fills in auth defaults for OpenRouter and other OpenAI-like endpoints.
func prepareOpenAICompatibleModel(model ModelConfig) (ModelConfig, error) {
	prepared := model
	authType := normalizedAuthType(prepared.AuthType, "bearer")
	prepared.AuthType = authType
	if authType == "bearer" || authType == "header_key" {
		prepared.APIKey = resolveConfiguredAPIKey(prepared)
		if strings.TrimSpace(prepared.APIKey) == "" {
			prepared.APIKey = fallbackCompatibleAPIKey(prepared)
		}
		if strings.TrimSpace(prepared.APIKey) == "" {
			return prepared, errors.New("missing API key for this model")
		}
	}
	return prepared, nil
}

// fallbackCompatibleAPIKey tries provider-specific environment variables when the config key is blank.
func fallbackCompatibleAPIKey(model ModelConfig) string {
	switch strings.ToLower(strings.TrimSpace(model.Provider)) {
	case "openrouter":
		return strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	case "openai", "chatgpt":
		return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	case "deepseek":
		return strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	case "xai", "grok":
		return strings.TrimSpace(os.Getenv("XAI_API_KEY"))
	case "mistral":
		return strings.TrimSpace(os.Getenv("MISTRAL_API_KEY"))
	}
	baseURL := strings.ToLower(strings.TrimSpace(model.BaseURL))
	if strings.Contains(baseURL, "api.openai.com") {
		return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if strings.Contains(baseURL, "api.deepseek.com") {
		return strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	}
	if strings.Contains(baseURL, "api.x.ai") {
		return strings.TrimSpace(os.Getenv("XAI_API_KEY"))
	}
	if strings.Contains(baseURL, "api.mistral.ai") {
		return strings.TrimSpace(os.Getenv("MISTRAL_API_KEY"))
	}
	return ""
}

// isChatCompletionsEndpoint switches the compatible adapter to chat completions when the path asks for it.
func isChatCompletionsEndpoint(endpoint string) bool {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	return strings.Contains(endpoint, "/chat/completions")
}

// executeOpenAICompatibleResponses reuses the Responses API shape for providers that mimic that endpoint.
func executeOpenAICompatibleResponses(ctx context.Context, model ModelConfig, req Request, endpoint string) (Response, error) {
	apiModel := strings.TrimSpace(model.ModelName)
	if apiModel == "" {
		return Response{}, errors.New("model name is required")
	}
	payload := buildOpenAIResponsesRequest(model, req, apiModel)
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	applyModelHeaders(request, model)
	respBody, status, statusCode, headers, err := doAdapterRequestWithHeaders(request, modelTimeout(model, 10*time.Minute))
	if err != nil {
		return Response{}, err
	}
	if statusCode >= 300 {
		return Response{}, fmt.Errorf("compatible responses endpoint returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	response, err := parseOpenAIResponse(respBody, headers.Get("Content-Type"), model)
	if err != nil {
		return Response{}, err
	}
	response.Status = status
	response.StatusCode = statusCode
	if strings.TrimSpace(response.RawBody) == "" {
		response.RawBody = string(respBody)
	}
	return response, nil
}

// executeOpenAICompatibleChat uses chat completions for providers that expose the older OpenAI-style path.
func executeOpenAICompatibleChat(ctx context.Context, model ModelConfig, req Request, endpoint string) (Response, error) {
	apiModel := strings.TrimSpace(model.ModelName)
	if apiModel == "" {
		return Response{}, errors.New("model name is required")
	}
	payload := openAICompatibleChatRequest{
		Model:       apiModel,
		Messages:    buildOpenAICompatibleChatMessages(req),
		Temperature: compatibleTemperature(model),
		MaxTokens:   model.MaxOutputTokens,
		Stream:      false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	applyModelHeaders(request, model)
	respBody, status, statusCode, headers, err := doAdapterRequestWithHeaders(request, modelTimeout(model, 10*time.Minute))
	if err != nil {
		return Response{}, err
	}
	if statusCode >= 300 {
		return Response{}, fmt.Errorf("compatible chat endpoint returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	response, err := parseOpenAICompatibleChatResponse(respBody, headers.Get("Content-Type"), model)
	if err != nil {
		return Response{}, err
	}
	response.Status = status
	response.StatusCode = statusCode
	if strings.TrimSpace(response.RawBody) == "" {
		response.RawBody = string(respBody)
	}
	return response, nil
}

func parseOpenAICompatibleChatResponse(respBody []byte, contentType string, model ModelConfig) (Response, error) {
	response, err := parseMultipartCapableResponse(respBody, contentType, model, parseOpenAICompatibleChatTextFromBody)
	if err == nil {
		return response, nil
	}
	return parseOpenAICompatibleStructuredChatResponse(respBody, model)
}

func parseOpenAICompatibleStructuredChatResponse(respBody []byte, model ModelConfig) (Response, error) {
	var parsed openAICompatibleChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("unable to parse compatible chat json: %w", err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return Response{}, errors.New(strings.TrimSpace(parsed.Error.Message))
	}
	response := Response{Text: extractOpenAICompatibleChatText(parsed), RawBody: string(respBody)}
	if media, ok, err := extractOpenAICompatibleChatMedia(parsed, model); err != nil {
		return Response{}, err
	} else if ok {
		response.FileData = media.FileData
		response.FileName = media.FileName
		response.FileMIMEType = media.FileMIMEType
	}
	if len(response.FileData) == 0 {
		resolved, err := resolveResponseMedia(respBody, model)
		if err != nil {
			return Response{}, err
		}
		if len(resolved.FileData) > 0 {
			response.FileData = resolved.FileData
			response.FileName = resolved.FileName
			response.FileMIMEType = resolved.FileMIMEType
		}
	}
	if strings.TrimSpace(response.Text) == "" && len(response.FileData) == 0 {
		return Response{}, errors.New("response contained no output text or binary data")
	}
	return response, nil
}

func parseOpenAICompatibleChatTextFromBody(respBody []byte) (string, error) {
	var parsed openAICompatibleChatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unable to parse compatible chat json: %w", err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return "", errors.New(strings.TrimSpace(parsed.Error.Message))
	}
	text := extractOpenAICompatibleChatText(parsed)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("response contained no output text")
	}
	return text, nil
}

// buildOpenAICompatibleChatMessages converts the assembled AgentGO request into chat-completions messages.
func buildOpenAICompatibleChatMessages(req Request) []openAICompatibleChatMessage {
	messages := make([]openAICompatibleChatMessage, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.Instructions) != "" {
		messages = append(messages, openAICompatibleChatMessage{Role: "system", Content: strings.TrimSpace(req.Instructions)})
	}
	for _, message := range req.Messages {
		parts := normalizeMessageParts(message)
		if len(parts) == 0 {
			continue
		}
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		textParts := make([]string, 0, len(parts))
		contentParts := make([]openAICompatibleChatContentPart, 0, len(parts))
		hasMedia := false
		for _, part := range parts {
			switch strings.ToLower(strings.TrimSpace(part.Kind)) {
			case "text":
				text := strings.TrimSpace(part.Text)
				if text == "" {
					continue
				}
				textParts = append(textParts, text)
				contentParts = append(contentParts, openAICompatibleChatContentPart{Type: "text", Text: text})
			case "image":
				if len(part.Data) == 0 {
					continue
				}
				mimeType := strings.TrimSpace(part.MIMEType)
				if mimeType == "" {
					mimeType = "image/png"
				}
				dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
				hasMedia = true
				contentParts = append(contentParts, openAICompatibleChatContentPart{Type: "image_url", ImageURL: &openAICompatibleChatImageURL{URL: dataURL}})
			}
		}
		if len(contentParts) == 0 {
			continue
		}
		if hasMedia {
			messages = append(messages, openAICompatibleChatMessage{Role: role, Content: contentParts})
			continue
		}
		messages = append(messages, openAICompatibleChatMessage{Role: role, Content: strings.TrimSpace(strings.Join(textParts, "\n"))})
	}
	return messages
}

// compatibleTemperature keeps zero as omitted while still allowing positive temperature overrides.
func compatibleTemperature(model ModelConfig) *float64 {
	if model.RequestDefaults.Temperature <= 0 {
		return nil
	}
	value := model.RequestDefaults.Temperature
	return &value
}

// extractOpenAICompatibleChatText pulls assistant text from either string or structured chat content.
func extractOpenAICompatibleChatText(resp openAICompatibleChatResponse) string {
	parts := []string{}
	for _, choice := range resp.Choices {
		text := extractCompatibleContentText(choice.Message.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(text))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractOpenAICompatibleChatMedia(resp openAICompatibleChatResponse, model ModelConfig) (Response, bool, error) {
	for _, choice := range resp.Choices {
		resolved, ok, err := extractCompatibleContentMedia(choice.Message.Content, model)
		if err != nil {
			return Response{}, false, err
		}
		if ok {
			return resolved, true, nil
		}
	}
	return Response{}, false, nil
}

func extractCompatibleContentMedia(content any, model ModelConfig) (Response, bool, error) {
	switch value := content.(type) {
	case []any:
		for _, item := range value {
			resolved, ok, err := extractCompatibleContentMedia(item, model)
			if err != nil {
				return Response{}, false, err
			}
			if ok {
				return resolved, true, nil
			}
		}
	case map[string]any:
		if resolved, ok, err := extractCompatibleContentItemMedia(value, model); err != nil || ok {
			return resolved, ok, err
		}
		for _, nested := range value {
			resolved, ok, err := extractCompatibleContentMedia(nested, model)
			if err != nil {
				return Response{}, false, err
			}
			if ok {
				return resolved, true, nil
			}
		}
	}
	return Response{}, false, nil
}

func extractCompatibleContentItemMedia(object map[string]any, model ModelConfig) (Response, bool, error) {
	mimeType := strings.TrimSpace(asString(object["mime_type"]))
	if mimeType == "" {
		mimeType = strings.TrimSpace(asString(object["media_type"]))
	}
	fileName := strings.TrimSpace(asString(object["filename"]))
	if fileName == "" {
		fileName = strings.TrimSpace(asString(object["name"]))
	}
	for _, key := range []string{"b64_json", "base64", "data"} {
		if encoded := strings.TrimSpace(asString(object[key])); encoded != "" {
			decoded, resolvedMIME, err := decodeResponseFileBase64(encoded, defaultString(mimeType, compatibleDefaultImageMIME(model)))
			if err != nil {
				return Response{}, false, err
			}
			return Response{FileData: decoded, FileName: defaultResponseFileName(fileName, resolvedMIME, "compatible_image"), FileMIMEType: defaultResponseMIME(resolvedMIME, fileName)}, true, nil
		}
	}
	if imageURLValue, ok := object["image_url"]; ok {
		if resolved, ok, err := resolveCompatibleMediaURL(imageURLValue, fileName, mimeType, model); err != nil || ok {
			return resolved, ok, err
		}
	}
	if strings.Contains(strings.ToLower(strings.TrimSpace(asString(object["type"]))), "image") {
		if resolved, ok, err := resolveCompatibleMediaURL(object["url"], fileName, mimeType, model); err != nil || ok {
			return resolved, ok, err
		}
	}
	return Response{}, false, nil
}

func resolveCompatibleMediaURL(value any, fileName, mimeType string, model ModelConfig) (Response, bool, error) {
	urlValue := strings.TrimSpace(asString(value))
	if urlValue == "" {
		if nested, ok := value.(map[string]any); ok {
			urlValue = strings.TrimSpace(asString(nested["url"]))
			if mimeType == "" {
				mimeType = strings.TrimSpace(asString(nested["mime_type"]))
			}
			if fileName == "" {
				fileName = strings.TrimSpace(asString(nested["filename"]))
			}
		}
	}
	if urlValue == "" {
		return Response{}, false, nil
	}
	if isDataURL(urlValue) {
		decoded, resolvedMIME, err := decodeResponseFileBase64(urlValue, defaultString(mimeType, compatibleDefaultImageMIME(model)))
		if err != nil {
			return Response{}, false, err
		}
		return Response{FileData: decoded, FileName: defaultResponseFileName(fileName, resolvedMIME, "compatible_image"), FileMIMEType: defaultResponseMIME(resolvedMIME, fileName)}, true, nil
	}
	if !providerOptionBool(model, "download_response_file_url", true) {
		return Response{}, false, nil
	}
	downloaded, err := downloadResponseFileURL(urlValue, modelTimeout(model, 2*time.Minute))
	if err != nil {
		return Response{}, false, err
	}
	resolvedName := defaultString(fileName, downloaded.FileName)
	resolvedMIME := defaultString(mimeType, downloaded.FileMIMEType)
	return Response{FileData: downloaded.FileData, FileName: defaultResponseFileName(resolvedName, resolvedMIME, "compatible_image"), FileMIMEType: defaultResponseMIME(resolvedMIME, resolvedName)}, true, nil
}

func compatibleDefaultImageMIME(model ModelConfig) string {
	return openAIImageResultMIME(providerOptionString(model, "output_format", "png"))
}

// extractCompatibleContentText normalizes string and array content into one plain text block.
func extractCompatibleContentText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			object, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text, _ := object["text"].(string)
			if strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

// doAdapterRequest runs one HTTP request and returns the raw body plus the final status details.
func doAdapterRequest(request *http.Request, timeout time.Duration) ([]byte, string, int, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(request)
	if err != nil {
		return nil, "", 0, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", 0, err
	}
	return respBody, resp.Status, resp.StatusCode, nil
}
