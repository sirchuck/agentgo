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
	"os"
	"strings"
	"time"
)

type geminiGenerateContentAdapter struct{}

type geminiInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type geminiContentPart struct {
	Text       string            `json:"text,omitempty"`
	InlineData *geminiInlineData `json:"inlineData,omitempty"`
}

type geminiContent struct {
	Role  string              `json:"role,omitempty"`
	Parts []geminiContentPart `json:"parts"`
}

type geminiGenerationConfig struct {
	ResponseMIMEType   string         `json:"responseMimeType,omitempty"`
	ResponseSchema     map[string]any `json:"responseSchema,omitempty"`
	ResponseModalities []string       `json:"responseModalities,omitempty"`
	ImageConfig        map[string]any `json:"imageConfig,omitempty"`
	MaxOutputTokens    int            `json:"maxOutputTokens,omitempty"`
	Temperature        *float64       `json:"temperature,omitempty"`
}

type geminiGenerateContentRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiContent          `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiGenerateContentResponse struct {
	Candidates []struct {
		Content struct {
			Role  string              `json:"role"`
			Parts []geminiContentPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code,omitempty"`
		Message string `json:"message"`
		Status  string `json:"status,omitempty"`
	} `json:"error,omitempty"`
}

// Execute sends one AgentGO request to the Gemini generateContent endpoint.
func (geminiGenerateContentAdapter) Execute(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	prepared, err := prepareGeminiModel(model)
	if err != nil {
		return Response{}, err
	}
	endpoint := strings.TrimSpace(modelEndpoint(prepared))
	if endpoint == "" {
		apiModel := strings.TrimSpace(prepared.ModelName)
		if apiModel == "" {
			apiModel = "gemini-2.5-flash"
		}
		endpoint = "https://generativelanguage.googleapis.com/v1beta/models/" + apiModel + ":generateContent"
	}
	payload := geminiGenerateContentRequest{
		Contents:          buildGeminiContents(req.Messages),
		SystemInstruction: buildGeminiSystemInstruction(req.Instructions),
		GenerationConfig:  buildGeminiGenerationConfig(prepared, req),
	}
	if len(payload.Contents) == 0 {
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
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, headers, err := doAdapterRequestWithHeaders(request, modelTimeout(prepared, 10*time.Minute))
	if err != nil {
		return Response{}, err
	}
	if statusCode >= 300 {
		return Response{}, fmt.Errorf("Gemini returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	response, err := parseGeminiResponse(respBody, headers.Get("Content-Type"), prepared)
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

// prepareGeminiModel fills in Gemini auth defaults before one request is sent.
func prepareGeminiModel(model ModelConfig) (ModelConfig, error) {
	prepared := model
	authType := normalizedAuthType(prepared.AuthType, "header_key")
	prepared.AuthType = authType
	if authType == "bearer" || authType == "header_key" {
		prepared.APIKey = resolveConfiguredAPIKey(prepared)
		if strings.TrimSpace(prepared.APIKey) == "" {
			prepared.APIKey = strings.TrimSpace(os.Getenv("GEMINI_API_KEY"))
		}
		if strings.TrimSpace(prepared.APIKey) == "" {
			prepared.APIKey = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
		}
		if strings.TrimSpace(prepared.APIKey) == "" {
			return prepared, errors.New("missing API key for this model")
		}
	}
	if strings.TrimSpace(prepared.AuthHeader) == "" && authType == "header_key" {
		prepared.AuthHeader = "x-goog-api-key"
	}
	return prepared, nil
}

// buildGeminiContents converts AgentGO messages into Gemini content blocks with supported roles.
func buildGeminiContents(messages []Message) []geminiContent {
	contents := make([]geminiContent, 0, len(messages))
	for _, message := range messages {
		parts := normalizeMessageParts(message)
		if len(parts) == 0 {
			continue
		}
		contentParts := make([]geminiContentPart, 0, len(parts))
		for _, part := range parts {
			switch strings.ToLower(strings.TrimSpace(part.Kind)) {
			case "text":
				if strings.TrimSpace(part.Text) == "" {
					continue
				}
				contentParts = append(contentParts, geminiContentPart{Text: strings.TrimSpace(part.Text)})
			case "image", "audio", "video", "file":
				if len(part.Data) == 0 {
					continue
				}
				mimeType := strings.TrimSpace(part.MIMEType)
				if mimeType == "" {
					switch strings.ToLower(strings.TrimSpace(part.Kind)) {
					case "image":
						mimeType = "image/png"
					case "audio":
						mimeType = "audio/mpeg"
					case "video":
						mimeType = "video/mp4"
					default:
						mimeType = "application/octet-stream"
					}
				}
				contentParts = append(contentParts, geminiContentPart{InlineData: &geminiInlineData{MIMEType: mimeType, Data: base64.StdEncoding.EncodeToString(part.Data)}})
			}
		}
		if len(contentParts) == 0 {
			continue
		}
		contents = append(contents, geminiContent{Role: normalizeGeminiRole(message.Role), Parts: contentParts})
	}
	return contents
}

// normalizeGeminiRole maps AgentGO message roles to the user and model roles Gemini accepts.
func normalizeGeminiRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "assistant" || role == "model" {
		return "model"
	}
	return "user"
}

// buildGeminiSystemInstruction wraps the assembled instruction text into Gemini's systemInstruction field.
func buildGeminiSystemInstruction(instructions string) *geminiContent {
	instructions = strings.TrimSpace(instructions)
	if instructions == "" {
		return nil
	}
	return &geminiContent{Parts: []geminiContentPart{{Text: instructions}}}
}

// buildGeminiGenerationConfig copies the small set of v1 generation controls AgentGO supports.
func buildGeminiGenerationConfig(model ModelConfig, req Request) *geminiGenerationConfig {
	config := &geminiGenerationConfig{}
	if req.ExpectJSON {
		config.ResponseMIMEType = "application/json"
	}
	if shouldUseStrictStructuredOutput(model, req) {
		config.ResponseMIMEType = "application/json"
		config.ResponseSchema = strictJSONSchema(req.JSONSchema)
	}
	if override := strings.TrimSpace(providerOptionString(model, "response_mime_type", "")); override != "" {
		config.ResponseMIMEType = override
	}
	if modalities := providerOptionStringList(model, "response_modalities"); len(modalities) > 0 {
		config.ResponseModalities = append([]string(nil), modalities...)
	}
	if imageConfig := providerOptionMap(model, "image_config"); len(imageConfig) > 0 {
		config.ImageConfig = imageConfig
	}
	if model.MaxOutputTokens > 0 {
		config.MaxOutputTokens = model.MaxOutputTokens
	}
	if model.RequestDefaults.Temperature > 0 {
		value := model.RequestDefaults.Temperature
		config.Temperature = &value
	}
	if config.ResponseMIMEType == "" && len(config.ResponseSchema) == 0 && len(config.ResponseModalities) == 0 && len(config.ImageConfig) == 0 && config.MaxOutputTokens == 0 && config.Temperature == nil {
		return nil
	}
	return config
}

func parseGeminiResponse(respBody []byte, contentType string, model ModelConfig) (Response, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err == nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return parseMultipartCapableResponse(respBody, contentType, model, parseGeminiResponseText)
	}
	return parseGeminiStructuredResponse(respBody)
}

func parseGeminiStructuredResponse(respBody []byte) (Response, error) {
	var parsed geminiGenerateContentResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("unable to parse Gemini response json: %w", err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return Response{}, errors.New(strings.TrimSpace(parsed.Error.Message))
	}
	text := extractGeminiText(parsed)
	fileData, fileMIMEType, err := extractGeminiBinaryData(parsed)
	if err != nil {
		return Response{}, err
	}
	if strings.TrimSpace(text) == "" && len(fileData) == 0 {
		return Response{}, errors.New("response contained no output text or binary data")
	}
	response := Response{Text: text, RawBody: string(respBody)}
	if len(fileData) > 0 {
		response.FileData = fileData
		response.FileMIMEType = fileMIMEType
		response.FileName = geminiDefaultFileName(fileMIMEType)
	}
	return response, nil
}

func parseGeminiResponseText(respBody []byte) (string, error) {
	response, err := parseGeminiStructuredResponse(respBody)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(response.Text) == "" {
		return "", errors.New("response contained no output text")
	}
	return response.Text, nil
}

// extractGeminiText joins all candidate text parts from one Gemini response into plain text.
func extractGeminiText(resp geminiGenerateContentResponse) string {
	parts := make([]string, 0)
	for _, candidate := range resp.Candidates {
		for _, part := range candidate.Content.Parts {
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			parts = append(parts, strings.TrimSpace(part.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func extractGeminiBinaryData(resp geminiGenerateContentResponse) ([]byte, string, error) {
	for _, candidate := range resp.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData == nil {
				continue
			}
			encoded := strings.TrimSpace(part.InlineData.Data)
			if encoded == "" {
				continue
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, "", fmt.Errorf("unable to decode Gemini inline binary data: %w", err)
			}
			mimeType := strings.TrimSpace(part.InlineData.MIMEType)
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			return decoded, mimeType, nil
		}
	}
	return nil, "", nil
}

func geminiDefaultFileName(mimeType string) string {
	clean := strings.TrimSpace(mimeType)
	if clean != "" {
		if exts, err := mime.ExtensionsByType(clean); err == nil && len(exts) > 0 {
			for _, ext := range exts {
				if strings.TrimSpace(ext) == "" {
					continue
				}
				return "gemini_output" + ext
			}
		}
	}
	switch strings.ToLower(clean) {
	case "image/png":
		return "gemini_output.png"
	case "image/jpeg":
		return "gemini_output.jpg"
	case "image/webp":
		return "gemini_output.webp"
	default:
		return "gemini_output.bin"
	}
}
