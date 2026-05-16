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
	"strings"
	"time"
)

type openAIResponsesAdapter struct{}

type openAIMessageContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileID   string `json:"file_id,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	Filename string `json:"filename,omitempty"`
}

type openAIInputMessage struct {
	Role    string                 `json:"role"`
	Content []openAIMessageContent `json:"content"`
}

type openAIResponsesToolChoice struct {
	Type string `json:"type"`
}

type openAIImageGenerationTool struct {
	Type              string `json:"type"`
	Action            string `json:"action,omitempty"`
	Background        string `json:"background,omitempty"`
	Size              string `json:"size,omitempty"`
	Quality           string `json:"quality,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	OutputCompression int    `json:"output_compression,omitempty"`
	InputFidelity     string `json:"input_fidelity,omitempty"`
}

type openAIResponsesRequest struct {
	Model        string                      `json:"model"`
	Instructions string                      `json:"instructions,omitempty"`
	Input        []openAIInputMessage        `json:"input,omitempty"`
	Store        bool                        `json:"store"`
	Text         map[string]any              `json:"text,omitempty"`
	Tools        []openAIImageGenerationTool `json:"tools,omitempty"`
	ToolChoice   *openAIResponsesToolChoice  `json:"tool_choice,omitempty"`
}

type openAIResponsesResponse struct {
	Status string `json:"status"`
	Output []struct {
		ID            string `json:"id,omitempty"`
		Type          string `json:"type"`
		Role          string `json:"role,omitempty"`
		Status        string `json:"status,omitempty"`
		Result        string `json:"result,omitempty"`
		RevisedPrompt string `json:"revised_prompt,omitempty"`
		OutputFormat  string `json:"output_format,omitempty"`
		Content       []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"content,omitempty"`
	} `json:"output"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Execute sends one AgentGO request to the OpenAI Responses API.
func (openAIResponsesAdapter) Execute(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	prepared, err := prepareOpenAIModel(model)
	if err != nil {
		return Response{}, err
	}
	endpoint := modelEndpoint(prepared)
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/responses"
	}
	apiModel := strings.TrimSpace(prepared.ModelName)
	if apiModel == "" {
		apiModel = "gpt-5.4"
	}
	payload := buildOpenAIResponsesRequest(prepared, req, apiModel)
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
	respBody, status, statusCode, headers, err := doAdapterRequestWithHeaders(request, modelTimeout(model, 10*time.Minute))
	if err != nil {
		return Response{}, err
	}
	if statusCode >= 300 {
		return Response{}, fmt.Errorf("OpenAI returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	response, err := parseOpenAIResponse(respBody, headers.Get("Content-Type"), prepared)
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

func buildOpenAIResponsesRequest(model ModelConfig, req Request, apiModel string) openAIResponsesRequest {
	payload := openAIResponsesRequest{
		Model:        apiModel,
		Instructions: strings.TrimSpace(req.Instructions),
		Input:        buildOpenAIInput(req.Messages),
		Store:        false,
	}
	if shouldUseOpenAIImageGeneration(model, apiModel, req) {
		payload.Tools = []openAIImageGenerationTool{buildOpenAIImageGenerationTool(model)}
		payload.ToolChoice = &openAIResponsesToolChoice{Type: "image_generation"}
		return payload
	}
	payload.Text = map[string]any{"format": map[string]any{"type": "text"}}
	return payload
}

func shouldUseOpenAIImageGeneration(model ModelConfig, apiModel string, req Request) bool {
	if providerOptionBool(model, "image_generation_tool", false) || providerOptionBool(model, "openai_image_generation_tool", false) {
		return true
	}
	identity := strings.ToLower(strings.TrimSpace(apiModel))
	if strings.Contains(identity, "gpt-image") || strings.Contains(identity, "chatgpt-image") {
		return true
	}
	if !model.Capabilities.SupportsBinaryOut {
		return false
	}
	return openAIRequestLooksLikeImageGeneration(req)
}

func openAIRequestLooksLikeImageGeneration(req Request) bool {
	text := strings.ToLower(strings.TrimSpace(openAILastUserText(req)))
	if text == "" {
		return false
	}
	imageNouns := []string{"image", "picture", "photo", "photograph", "illustration", "drawing", "artwork", "logo", "icon", "sprite", "thumbnail", "poster", "banner", "cover art", "avatar", "sticker", "meme", "comic", "png", "jpg", "jpeg", "webp", "gif"}
	actionVerbs := []string{"generate", "create", "draw", "make", "produce", "render", "design", "illustrate", "paint", "sketch", "visualize", "return", "output", "save", "build"}
	hasImageNoun := false
	for _, noun := range imageNouns {
		if strings.Contains(text, noun) {
			hasImageNoun = true
			break
		}
	}
	if !hasImageNoun {
		return false
	}
	for _, verb := range actionVerbs {
		if strings.Contains(text, verb) {
			return true
		}
	}
	return strings.Contains(text, "text-to-image") || strings.Contains(text, "image generation")
}

func openAILastUserText(req Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		message := req.Messages[i]
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role != "" && role != "user" {
			continue
		}
		parts := []string{strings.TrimSpace(message.Text)}
		for _, part := range message.Parts {
			if strings.EqualFold(strings.TrimSpace(part.Kind), "text") && strings.TrimSpace(part.Text) != "" {
				parts = append(parts, strings.TrimSpace(part.Text))
			}
		}
		joined := strings.TrimSpace(strings.Join(parts, "\n"))
		if joined != "" {
			return joined
		}
	}
	return ""
}

func buildOpenAIImageGenerationTool(model ModelConfig) openAIImageGenerationTool {
	tool := openAIImageGenerationTool{Type: "image_generation"}
	if value := strings.TrimSpace(providerOptionString(model, "action", "")); value != "" {
		tool.Action = value
	}
	if value := strings.TrimSpace(providerOptionString(model, "background", "")); value != "" {
		tool.Background = value
	}
	if value := strings.TrimSpace(providerOptionString(model, "size", "")); value != "" {
		tool.Size = value
	}
	if value := strings.TrimSpace(providerOptionString(model, "quality", "")); value != "" {
		tool.Quality = value
	}
	if value := strings.TrimSpace(providerOptionString(model, "output_format", "")); value != "" {
		tool.OutputFormat = value
	}
	if value, ok := providerOptionValue(model, "output_compression"); ok {
		switch typed := value.(type) {
		case int:
			if typed > 0 {
				tool.OutputCompression = typed
			}
		case float64:
			if int(typed) > 0 {
				tool.OutputCompression = int(typed)
			}
		}
	}
	if value := strings.TrimSpace(providerOptionString(model, "input_fidelity", "")); value != "" {
		tool.InputFidelity = value
	}
	if tool.Action == "" {
		tool.Action = "auto"
	}
	return tool
}

func parseOpenAIResponse(respBody []byte, contentType string, model ModelConfig) (Response, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err == nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return parseMultipartCapableResponse(respBody, contentType, model, parseOpenAIResponseText)
	}
	return parseOpenAIStructuredResponse(respBody, model)
}

func parseOpenAIStructuredResponse(respBody []byte, model ModelConfig) (Response, error) {
	var parsed openAIResponsesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, fmt.Errorf("unable to parse OpenAI response json: %w", err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return Response{}, errors.New(strings.TrimSpace(parsed.Error.Message))
	}
	response := Response{Text: extractOpenAIOutputText(parsed), RawBody: string(respBody)}
	if imageResponse, ok, err := extractOpenAIImageResult(parsed, model); err != nil {
		return Response{}, err
	} else if ok {
		response.FileData = imageResponse.FileData
		response.FileName = imageResponse.FileName
		response.FileMIMEType = imageResponse.FileMIMEType
	}
	if strings.TrimSpace(response.Text) == "" && len(response.FileData) == 0 {
		return Response{}, errors.New("response contained no output text or binary data")
	}
	return response, nil
}

func parseOpenAIResponseText(respBody []byte) (string, error) {
	var parsed openAIResponsesResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unable to parse OpenAI response json: %w", err)
	}
	if parsed.Error != nil && strings.TrimSpace(parsed.Error.Message) != "" {
		return "", errors.New(strings.TrimSpace(parsed.Error.Message))
	}
	text := extractOpenAIOutputText(parsed)
	if strings.TrimSpace(text) == "" {
		return "", errors.New("response contained no output text")
	}
	return text, nil
}

func extractOpenAIImageResult(resp openAIResponsesResponse, model ModelConfig) (Response, bool, error) {
	for _, item := range resp.Output {
		if strings.TrimSpace(item.Type) != "image_generation_call" {
			continue
		}
		encoded := strings.TrimSpace(item.Result)
		if encoded == "" {
			continue
		}
		format := strings.ToLower(strings.TrimSpace(item.OutputFormat))
		if format == "" {
			format = strings.ToLower(strings.TrimSpace(providerOptionString(model, "output_format", "png")))
		}
		mimeType := openAIImageResultMIME(format)
		decoded, resolvedMIME, err := decodeResponseFileBase64(encoded, mimeType)
		if err != nil {
			return Response{}, false, err
		}
		fileName := strings.TrimSpace(providerOptionString(model, "response_file_name", ""))
		if fileName == "" {
			fileName = defaultResponseFileName("openai_image", resolvedMIME, "openai_image")
		}
		return Response{
			FileData:     decoded,
			FileName:     defaultResponseFileName(fileName, resolvedMIME, "openai_image"),
			FileMIMEType: defaultResponseMIME(resolvedMIME, fileName),
		}, true, nil
	}
	return Response{}, false, nil
}

func openAIImageResultMIME(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg", "jpg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}

// buildOpenAIInput converts AgentGO's assembled messages into the Responses API input shape.
func buildOpenAIInput(messages []Message) []openAIInputMessage {
	input := make([]openAIInputMessage, 0, len(messages))
	for _, message := range messages {
		parts := normalizeMessageParts(message)
		if len(parts) == 0 {
			continue
		}
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "user"
		}
		content := make([]openAIMessageContent, 0, len(parts))
		for _, part := range parts {
			switch strings.ToLower(strings.TrimSpace(part.Kind)) {
			case "text":
				if strings.TrimSpace(part.Text) == "" {
					continue
				}
				content = append(content, openAIMessageContent{Type: "input_text", Text: strings.TrimSpace(part.Text)})
			case "image":
				if len(part.Data) == 0 {
					continue
				}
				mimeType := strings.TrimSpace(part.MIMEType)
				if mimeType == "" {
					mimeType = "image/png"
				}
				dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
				content = append(content, openAIMessageContent{Type: "input_image", ImageURL: dataURL})
			case "audio", "video", "file":
				if len(part.Data) == 0 {
					continue
				}
				mimeType := strings.TrimSpace(part.MIMEType)
				if mimeType == "" {
					mimeType = "application/octet-stream"
				}
				filename := strings.TrimSpace(part.Name)
				if filename == "" {
					filename = strings.TrimSpace(part.RelPath)
				}
				if filename == "" {
					filename = "attachment"
				}
				dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(part.Data)
				content = append(content, openAIMessageContent{Type: "input_file", FileData: dataURL, Filename: filename})
			}
		}
		if len(content) == 0 {
			continue
		}
		input = append(input, openAIInputMessage{Role: role, Content: content})
	}
	return input
}

// extractOpenAIOutputText joins all text segments from one OpenAI response into one string.
func extractOpenAIOutputText(resp openAIResponsesResponse) string {
	var b strings.Builder
	for _, item := range resp.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(content.Text)
			}
		}
	}
	return strings.TrimSpace(b.String())
}
