package adapters

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type anthropicMessagesAdapter struct{}

type anthropicMessageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
}

type anthropicMessageContent struct {
	Type   string                  `json:"type"`
	Text   string                  `json:"text,omitempty"`
	Source *anthropicMessageSource `json:"source,omitempty"`
	Title  string                  `json:"title,omitempty"`
}

type anthropicMessage struct {
	Role    string                    `json:"role"`
	Content []anthropicMessageContent `json:"content"`
}

type anthropicMessagesRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream"`
}

type anthropicMessagesResponse struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Execute sends one AgentGO request to the Anthropic Messages API.
func (anthropicMessagesAdapter) Execute(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	prepared, err := prepareAnthropicModel(model)
	if err != nil {
		return Response{}, err
	}
	endpoint := strings.TrimSpace(modelEndpoint(prepared))
	if endpoint == "" {
		endpoint = "https://api.anthropic.com/v1/messages"
	}
	apiModel := strings.TrimSpace(prepared.ModelName)
	if apiModel == "" {
		return Response{}, errors.New("model name is required")
	}
	payload := anthropicMessagesRequest{Model: apiModel, System: strings.TrimSpace(req.Instructions), Messages: buildAnthropicMessages(req.Messages), MaxTokens: anthropicMaxTokens(prepared), Temperature: anthropicTemperature(prepared), Stream: false}
	if len(payload.Messages) == 0 {
		return Response{}, errors.New("request contained no messages")
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
	request.Header.Set("anthropic-version", anthropicVersion(prepared))
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, headers, err := doAdapterRequestWithHeaders(request, modelTimeout(prepared, 10*time.Minute))
	if err != nil {
		return Response{}, err
	}
	if statusCode >= 300 {
		return Response{}, fmt.Errorf("Anthropic returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	response, err := parseAnthropicResponse(respBody, headers.Get("Content-Type"), prepared)
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

// prepareAnthropicModel fills in Anthropic auth defaults before one request is sent.
func prepareAnthropicModel(model ModelConfig) (ModelConfig, error) {
	prepared := model
	authType := normalizedAuthType(prepared.AuthType, "header_key")
	prepared.AuthType = authType
	if authType == "bearer" || authType == "header_key" {
		prepared.APIKey = resolveConfiguredAPIKey(prepared)
		if strings.TrimSpace(prepared.APIKey) == "" {
			prepared.APIKey = strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
		}
		if strings.TrimSpace(prepared.APIKey) == "" {
			return prepared, errors.New("missing API key for this model")
		}
	}
	if strings.TrimSpace(prepared.AuthHeader) == "" && authType == "header_key" {
		prepared.AuthHeader = "x-api-key"
	}
	return prepared, nil
}

// anthropicVersion uses a saved custom header when present and falls back to the stable public API version.
func anthropicVersion(model ModelConfig) string {
	for key, value := range model.Headers {
		if strings.EqualFold(strings.TrimSpace(key), "anthropic-version") && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "2023-06-01"
}

// buildAnthropicMessages converts AgentGO messages into the user and assistant blocks Anthropic expects.
func buildAnthropicMessages(messages []Message) []anthropicMessage {
	result := make([]anthropicMessage, 0, len(messages))
	for _, message := range messages {
		parts := normalizeMessageParts(message)
		if len(parts) == 0 {
			continue
		}
		content := make([]anthropicMessageContent, 0, len(parts))
		for _, part := range parts {
			switch strings.ToLower(strings.TrimSpace(part.Kind)) {
			case "text":
				if strings.TrimSpace(part.Text) == "" {
					continue
				}
				content = append(content, anthropicMessageContent{Type: "text", Text: strings.TrimSpace(part.Text)})
			case "image":
				if len(part.Data) == 0 {
					continue
				}
				mimeType := strings.TrimSpace(part.MIMEType)
				if mimeType == "" {
					mimeType = "image/png"
				}
				content = append(content, anthropicMessageContent{Type: "image", Source: &anthropicMessageSource{Type: "base64", MediaType: mimeType, Data: base64.StdEncoding.EncodeToString(part.Data)}})
			case "file":
				if len(part.Data) == 0 {
					continue
				}
				mimeType := strings.TrimSpace(part.MIMEType)
				if mimeType == "" {
					mimeType = "application/pdf"
				}
				title := strings.TrimSpace(part.Name)
				if title == "" {
					title = strings.TrimSpace(part.RelPath)
				}
				content = append(content, anthropicMessageContent{Type: "document", Title: title, Source: &anthropicMessageSource{Type: "base64", MediaType: mimeType, Data: base64.StdEncoding.EncodeToString(part.Data)}})
			}
		}
		if len(content) == 0 {
			continue
		}
		role := normalizeAnthropicRole(message.Role)
		result = append(result, anthropicMessage{Role: role, Content: content})
	}
	return result
}

// normalizeAnthropicRole keeps only Anthropic's supported message roles and maps the rest to user.
func normalizeAnthropicRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "assistant" {
		return "assistant"
	}
	return "user"
}

// anthropicMaxTokens chooses a safe output cap when the saved model config leaves it blank.
func anthropicMaxTokens(model ModelConfig) int {
	if model.MaxOutputTokens > 0 {
		return model.MaxOutputTokens
	}
	return 4096
}

// anthropicTemperature keeps zero as omitted while still allowing positive temperature overrides.
func anthropicTemperature(model ModelConfig) *float64 {
	if model.RequestDefaults.Temperature <= 0 {
		return nil
	}
	value := model.RequestDefaults.Temperature
	return &value
}

func parseAnthropicResponse(respBody []byte, contentType string, model ModelConfig) (Response, error) {
	response, err := parseMultipartCapableResponse(respBody, contentType, model, parseAnthropicResponseText)
	if err == nil {
		return response, nil
	}
	return parseAnthropicStructuredResponse(respBody, model)
}

func parseAnthropicStructuredResponse(respBody []byte, model ModelConfig) (Response, error) {
	var parsed any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("unable to parse Anthropic response json: %w", err)
	}
	if message := strings.TrimSpace(asString(lookupPath(parsed, "error.message"))); message != "" {
		return Response{}, errors.New(message)
	}
	response := Response{Text: extractAnthropicStructuredText(parsed), RawBody: string(respBody)}
	if media, ok, err := extractAnthropicStructuredMedia(parsed, model); err != nil {
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

func parseAnthropicResponseText(respBody []byte) (string, error) {
	var parsed anthropicMessagesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unable to parse Anthropic response json: %w", err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return "", errors.New(strings.TrimSpace(parsed.Error.Message))
	}
	text := extractAnthropicText(parsed)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("response contained no output text")
	}
	return text, nil
}

// extractAnthropicText joins all returned Anthropic text blocks into one plain text result.
func extractAnthropicText(resp anthropicMessagesResponse) string {
	parts := make([]string, 0, len(resp.Content))
	for _, item := range resp.Content {
		if item.Type == "text" && strings.TrimSpace(item.Text) != "" {
			parts = append(parts, strings.TrimSpace(item.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractAnthropicStructuredText(parsed any) string {
	content, _ := lookupPath(parsed, "content").([]any)
	parts := make([]string, 0, len(content))
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(asString(block["type"])), "text") {
			if text := strings.TrimSpace(asString(block["text"])); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractAnthropicStructuredMedia(parsed any, model ModelConfig) (Response, bool, error) {
	return extractAnthropicMediaValue(parsed, model)
}

func extractAnthropicMediaValue(value any, model ModelConfig) (Response, bool, error) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if media, ok, err := extractAnthropicMediaValue(item, model); ok || err != nil {
				return media, ok, err
			}
		}
	case map[string]any:
		if media, ok, err := extractAnthropicMediaMap(typed, model); ok || err != nil {
			return media, ok, err
		}
		for _, nested := range typed {
			if media, ok, err := extractAnthropicMediaValue(nested, model); ok || err != nil {
				return media, ok, err
			}
		}
	}
	return Response{}, false, nil
}

func extractAnthropicMediaMap(block map[string]any, model ModelConfig) (Response, bool, error) {
	hintedName := anthropicHintedFileName(block)
	hintedMIME := anthropicHintedMIME(block)
	if source, ok := block["source"].(map[string]any); ok {
		if media, ok, err := extractAnthropicMediaSource(source, hintedName, hintedMIME, model); ok || err != nil {
			return media, ok, err
		}
	}
	if fileID := strings.TrimSpace(asString(block["file_id"])); fileID != "" {
		media, err := downloadAnthropicFileByID(model, fileID, hintedName, hintedMIME)
		if err != nil {
			return Response{}, false, err
		}
		return media, true, nil
	}
	if data := strings.TrimSpace(asString(block["data"])); data != "" {
		decoded, resolvedMIME, err := decodeResponseFileBase64(data, hintedMIME)
		if err != nil {
			return Response{}, false, err
		}
		name := defaultResponseFileName(hintedName, resolvedMIME, "anthropic_output")
		return Response{FileData: decoded, FileName: name, FileMIMEType: defaultResponseMIME(resolvedMIME, name)}, true, nil
	}
	if fileURL := strings.TrimSpace(asString(block["url"])); fileURL != "" && providerOptionBool(model, "download_response_file_url", true) {
		media, err := downloadResponseFileURL(fileURL, modelTimeout(model, 2*time.Minute))
		if err != nil {
			return Response{}, false, err
		}
		if strings.TrimSpace(hintedName) != "" {
			media.FileName = defaultResponseFileName(hintedName, defaultString(hintedMIME, media.FileMIMEType), "anthropic_output")
		}
		if strings.TrimSpace(hintedMIME) != "" {
			media.FileMIMEType = defaultResponseMIME(hintedMIME, media.FileName)
		}
		return media, true, nil
	}
	return Response{}, false, nil
}

func extractAnthropicMediaSource(source map[string]any, hintedName, hintedMIME string, model ModelConfig) (Response, bool, error) {
	hintedName = defaultString(hintedName, anthropicHintedFileName(source))
	hintedMIME = defaultString(hintedMIME, anthropicHintedMIME(source))
	if fileID := strings.TrimSpace(asString(source["file_id"])); fileID != "" {
		media, err := downloadAnthropicFileByID(model, fileID, hintedName, hintedMIME)
		if err != nil {
			return Response{}, false, err
		}
		return media, true, nil
	}
	if data := strings.TrimSpace(asString(source["data"])); data != "" {
		decoded, resolvedMIME, err := decodeResponseFileBase64(data, hintedMIME)
		if err != nil {
			return Response{}, false, err
		}
		name := defaultResponseFileName(hintedName, resolvedMIME, "anthropic_output")
		return Response{FileData: decoded, FileName: name, FileMIMEType: defaultResponseMIME(resolvedMIME, name)}, true, nil
	}
	if fileURL := strings.TrimSpace(asString(source["url"])); fileURL != "" && providerOptionBool(model, "download_response_file_url", true) {
		media, err := downloadResponseFileURL(fileURL, modelTimeout(model, 2*time.Minute))
		if err != nil {
			return Response{}, false, err
		}
		if strings.TrimSpace(hintedName) != "" {
			media.FileName = defaultResponseFileName(hintedName, defaultString(hintedMIME, media.FileMIMEType), "anthropic_output")
		}
		if strings.TrimSpace(hintedMIME) != "" {
			media.FileMIMEType = defaultResponseMIME(hintedMIME, media.FileName)
		}
		return media, true, nil
	}
	return Response{}, false, nil
}

func anthropicHintedFileName(value map[string]any) string {
	for _, key := range []string{"filename", "file_name", "title", "name"} {
		if text := strings.TrimSpace(asString(value[key])); text != "" {
			return text
		}
	}
	return ""
}

func anthropicHintedMIME(value map[string]any) string {
	for _, key := range []string{"mime_type", "media_type", "content_type"} {
		if text := strings.TrimSpace(asString(value[key])); text != "" {
			return text
		}
	}
	return ""
}

func downloadAnthropicFileByID(model ModelConfig, fileID, hintedName, hintedMIME string) (Response, error) {
	if strings.TrimSpace(fileID) == "" {
		return Response{}, errors.New("missing Anthropic file_id")
	}
	baseURL, err := anthropicFilesBaseURL(model)
	if err != nil {
		return Response{}, err
	}
	downloadURL := strings.TrimRight(baseURL, "/") + "/v1/files/" + url.PathEscape(strings.TrimSpace(fileID)) + "/content"
	request, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return Response{}, err
	}
	applyModelHeaders(request, model)
	request.Header.Set("anthropic-version", anthropicVersion(model))
	request.Header.Set("anthropic-beta", mergeAnthropicBetaHeaders(request.Header.Get("anthropic-beta"), providerOptionString(model, "anthropic_files_beta", "files-api-2025-04-14")))
	body, status, statusCode, headers, err := doAdapterRequestWithHeaders(request, modelTimeout(model, 2*time.Minute))
	if err != nil {
		return Response{}, err
	}
	if statusCode >= 300 {
		return Response{}, fmt.Errorf("Anthropic file download returned %s for %s", status, strings.TrimSpace(fileID))
	}
	fileName := defaultString(anthropicResponseFileName(headers), hintedName)
	mimeType := defaultString(strings.TrimSpace(headers.Get("Content-Type")), hintedMIME)
	resolvedName := defaultResponseFileName(fileName, mimeType, strings.TrimSpace(fileID))
	return Response{FileData: append([]byte(nil), body...), FileName: resolvedName, FileMIMEType: defaultResponseMIME(mimeType, resolvedName)}, nil
}

func anthropicFilesBaseURL(model ModelConfig) (string, error) {
	if override := strings.TrimSpace(providerOptionString(model, "anthropic_files_base_url", "")); override != "" {
		return strings.TrimRight(override, "/"), nil
	}
	if base := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/"); base != "" {
		if parsed, err := url.Parse(base); err == nil && parsed != nil {
			parsed.Path = strings.TrimSuffix(parsed.Path, "/v1/messages")
			parsed.Path = strings.TrimSuffix(parsed.Path, "/messages")
			parsed.Path = strings.TrimSuffix(parsed.Path, "/v1")
			parsed.RawQuery = ""
			parsed.Fragment = ""
			return strings.TrimRight(parsed.String(), "/"), nil
		}
	}
	endpoint := strings.TrimSpace(modelEndpoint(model))
	if endpoint == "" {
		return "https://api.anthropic.com", nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid Anthropic endpoint for file download: %s", endpoint)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/v1/messages")
	parsed.Path = strings.TrimSuffix(parsed.Path, "/messages")
	parsed.Path = strings.TrimSuffix(parsed.Path, "/v1")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func anthropicResponseFileName(headers http.Header) string {
	for _, value := range headers.Values("Content-Disposition") {
		if mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(value)); err == nil {
			if strings.EqualFold(strings.TrimSpace(mediaType), "attachment") || strings.EqualFold(strings.TrimSpace(mediaType), "inline") {
				for _, key := range []string{"filename*", "filename"} {
					if candidate := strings.TrimSpace(params[key]); candidate != "" {
						if strings.HasPrefix(strings.ToLower(candidate), "utf-8''") {
							candidate = candidate[7:]
							if decoded, err := url.QueryUnescape(candidate); err == nil && strings.TrimSpace(decoded) != "" {
								candidate = decoded
							}
						}
						candidate = strings.Trim(candidate, "\"")
						if candidate != "" {
							return candidate
						}
					}
				}
			}
		}
	}
	return ""
}

func mergeAnthropicBetaHeaders(existing, required string) string {
	seen := map[string]bool{}
	parts := make([]string, 0, 2)
	appendValues := func(raw string) {
		for _, item := range strings.Split(raw, ",") {
			clean := strings.TrimSpace(item)
			if clean == "" || seen[strings.ToLower(clean)] {
				continue
			}
			seen[strings.ToLower(clean)] = true
			parts = append(parts, clean)
		}
	}
	appendValues(existing)
	appendValues(required)
	return strings.Join(parts, ",")
}
