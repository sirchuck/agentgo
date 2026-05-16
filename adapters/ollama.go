package adapters

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type ollamaGenerateAdapter struct{}

type ollamaGenerateRequest struct {
	Model   string         `json:"model"`
	Prompt  string         `json:"prompt,omitempty"`
	System  string         `json:"system,omitempty"`
	Images  []string       `json:"images,omitempty"`
	Format  any            `json:"format,omitempty"`
	Stream  bool           `json:"stream"`
	Options map[string]any `json:"options,omitempty"`
}

func ollamaRequestOptions(model ModelConfig) map[string]any {
	options := map[string]any{}
	if nested := providerOptionMap(model, "options"); len(nested) > 0 {
		for key, value := range nested {
			options[key] = value
		}
	}
	for _, key := range []string{"num_ctx", "temperature", "top_k", "top_p", "repeat_penalty", "repeat_last_n", "num_predict", "seed", "min_p", "tfs_z", "num_gpu"} {
		if _, exists := options[key]; exists {
			continue
		}
		if value, ok := ollamaTopLevelProviderOption(model, key); ok {
			options[key] = value
		}
	}
	if _, exists := options["temperature"]; !exists && model.RequestDefaults.Temperature > 0 {
		options["temperature"] = model.RequestDefaults.Temperature
	}
	if len(options) == 0 {
		return nil
	}
	return options
}

func ollamaTopLevelProviderOption(model ModelConfig, key string) (any, bool) {
	if model.ProviderOptions == nil {
		return nil, false
	}
	value, ok := model.ProviderOptions[key]
	if !ok || value == nil {
		return nil, false
	}
	return value, true
}

func ollamaStrictStructuredOutputEnabled(model ModelConfig) bool {
	if model.StrictStructuredOutput != nil {
		return *model.StrictStructuredOutput
	}
	return normalizedAdapterName(model) == "ollama_generate"
}

type ollamaGenerateResponse struct {
	Model              string `json:"model"`
	CreatedAt          string `json:"created_at"`
	Response           string `json:"response"`
	Done               bool   `json:"done"`
	DoneReason         string `json:"done_reason,omitempty"`
	Context            []int  `json:"context,omitempty"`
	TotalDuration      int64  `json:"total_duration,omitempty"`
	LoadDuration       int64  `json:"load_duration,omitempty"`
	PromptEvalCount    int    `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64  `json:"prompt_eval_duration,omitempty"`
	EvalCount          int    `json:"eval_count,omitempty"`
	EvalDuration       int64  `json:"eval_duration,omitempty"`
	Error              string `json:"error,omitempty"`
}

func parseOllamaResponseText(respBody []byte) (string, error) {
	var parsed ollamaGenerateResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("unable to parse Ollama response json: %w", err)
	}
	if strings.TrimSpace(parsed.Error) != "" {
		return "", errors.New(strings.TrimSpace(parsed.Error))
	}
	text := strings.TrimSpace(parsed.Response)
	if text == "" {
		return "", errors.New("response contained no output text")
	}
	return text, nil
}

func collectOllamaImages(messages []Message) []string {
	images := []string{}
	for _, message := range messages {
		for _, part := range normalizeMessageParts(message) {
			if strings.ToLower(strings.TrimSpace(part.Kind)) != "image" || len(part.Data) == 0 {
				continue
			}
			images = append(images, base64.StdEncoding.EncodeToString(part.Data))
		}
	}
	if len(images) == 0 {
		return nil
	}
	return images
}

func flattenMessagesForOllama(messages []Message, nativeImages bool) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		messageParts := normalizeMessageParts(message)
		if len(messageParts) == 0 {
			continue
		}
		segments := make([]string, 0, len(messageParts))
		for _, part := range messageParts {
			if nativeImages && strings.EqualFold(strings.TrimSpace(part.Kind), "image") && len(part.Data) > 0 {
				continue
			}
			text := strings.TrimSpace(partTextFallback(part))
			if text == "" {
				continue
			}
			segments = append(segments, text)
		}
		if len(segments) == 0 {
			continue
		}
		parts = append(parts, strings.Join(segments, "\n\n"))
	}
	return strings.Join(parts, "\n\n")
}

// Execute sends one AgentGO request to an Ollama generate endpoint, including native images when present.
func (ollamaGenerateAdapter) Execute(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	endpoint := strings.TrimSpace(modelEndpoint(model))
	if endpoint == "" {
		endpoint = "http://127.0.0.1:11434/api/generate"
	}
	apiModel := strings.TrimSpace(model.ModelName)
	if apiModel == "" {
		apiModel = "phi3.5:latest"
	}
	var format any
	if req.ExpectJSON {
		if ollamaStrictStructuredOutputEnabled(model) && len(req.JSONSchema) > 0 {
			format = req.JSONSchema
		} else {
			format = "json"
		}
	}
	images := collectOllamaImages(req.Messages)
	payload := ollamaGenerateRequest{
		Model:   apiModel,
		Prompt:  flattenMessagesForOllama(req.Messages, len(images) > 0),
		System:  strings.TrimSpace(req.Instructions),
		Images:  images,
		Format:  format,
		Stream:  false,
		Options: ollamaRequestOptions(model),
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
		return Response{}, fmt.Errorf("Ollama returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	response, err := parseMultipartCapableResponse(respBody, headers.Get("Content-Type"), model, parseOllamaResponseText)
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
