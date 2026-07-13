package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type xaiImagineImageAdapter struct{}

func (xaiImagineImageAdapter) Execute(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	prepared, err := prepareXAIModel(model)
	if err != nil {
		return Response{}, err
	}
	prompt := xaiPrompt(req, prepared)
	if prompt == "" {
		return Response{}, errors.New("xAI image requests require a non-empty prompt")
	}
	images := collectMultipartMediaParts(req, "image")
	operation, err := xaiImageOperation(prepared, images)
	if err != nil {
		return Response{}, err
	}
	endpoint := xaiImageEndpoint(prepared, operation)
	payload, err := buildXAIImagePayload(prepared, prompt, images, operation)
	if err != nil {
		return Response{}, err
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
		return Response{}, fmt.Errorf("xAI image endpoint returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	response, err := parseMultipartCapableResponse(respBody, headers.Get("Content-Type"), prepared, func([]byte) (string, error) {
		return "", nil
	})
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

func prepareXAIModel(model ModelConfig) (ModelConfig, error) {
	prepared := model
	authType := normalizedAuthType(prepared.AuthType, "bearer")
	prepared.AuthType = authType
	if strings.TrimSpace(prepared.BaseURL) == "" {
		prepared.BaseURL = "https://api.x.ai"
	}
	if authType == "bearer" || authType == "header_key" {
		prepared.APIKey = resolveConfiguredAPIKey(prepared)
		if strings.TrimSpace(prepared.APIKey) == "" {
			prepared.APIKey = strings.TrimSpace(os.Getenv("XAI_API_KEY"))
		}
		if strings.TrimSpace(prepared.APIKey) == "" {
			return prepared, errors.New("missing API key for this model")
		}
	}
	return prepared, nil
}

func xaiPrompt(req Request, model ModelConfig) string {
	messagePrompt := strings.TrimSpace(flattenMessages(req.Messages))
	includeInstructions := providerOptionBool(model, "include_instructions_in_prompt", false)
	instructions := strings.TrimSpace(req.Instructions)
	switch {
	case messagePrompt != "" && includeInstructions && instructions != "":
		return strings.TrimSpace(instructions + "\n\n" + messagePrompt)
	case messagePrompt != "":
		return messagePrompt
	default:
		return instructions
	}
}

func xaiImageOperation(model ModelConfig, images []Part) (string, error) {
	operation := strings.ToLower(strings.TrimSpace(providerOptionString(model, "xai_image_operation", "auto")))
	hasImages := len(images) > 0
	switch operation {
	case "", "auto":
		if hasImages {
			return "edit", nil
		}
		return "generate", nil
	case "generate", "generation":
		if hasImages {
			return "", errors.New("xAI image generate mode does not accept source images; use auto/edit instead")
		}
		return "generate", nil
	case "edit", "edits":
		if !hasImages {
			return "", errors.New("xAI image edit mode requires at least one source image")
		}
		return "edit", nil
	default:
		return "", fmt.Errorf("unsupported xAI image operation: %s", operation)
	}
}

func xaiImageEndpoint(model ModelConfig, operation string) string {
	base := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
	apiPath := strings.TrimSpace(model.APIPath)
	if strings.HasPrefix(apiPath, "http://") || strings.HasPrefix(apiPath, "https://") {
		return apiPath
	}
	defaultPath := "/v1/images/generations"
	if operation == "edit" {
		defaultPath = "/v1/images/edits"
	}
	if apiPath == "" {
		return base + defaultPath
	}
	lower := strings.ToLower(apiPath)
	if strings.Contains(lower, "/images/") {
		return base + defaultPath
	}
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	return base + apiPath
}

func buildXAIImagePayload(model ModelConfig, prompt string, images []Part, operation string) (map[string]any, error) {
	payload := map[string]any{}
	for key, value := range providerOptionMap(model, "extra_body") {
		if strings.TrimSpace(key) == "" || value == nil {
			continue
		}
		payload[key] = value
	}
	modelName := strings.TrimSpace(model.ModelName)
	if modelName == "" {
		modelName = providerOptionString(model, "xai_default_image_model", "grok-imagine-image-quality")
	}
	payload[providerOptionString(model, "model_field", "model")] = modelName
	payload[providerOptionString(model, "prompt_field", "prompt")] = prompt
	for _, key := range []string{"n", "num_images", "aspect_ratio", "resolution", "response_format", "quality", "background", "seed", "negative_prompt"} {
		if value, ok := providerOptionValue(model, key); ok {
			payload[providerOptionString(model, key+"_field", key)] = value
		}
	}
	if operation == "edit" {
		if len(images) == 0 {
			return nil, errors.New("xAI image edit mode requires at least one source image")
		}
		if len(images) > 3 {
			return nil, errors.New("xAI image edit mode supports up to 3 source images")
		}
		encoded := make([]map[string]any, 0, len(images))
		for _, part := range images {
			encoded = append(encoded, map[string]any{
				"url":  encodedBinaryData("data_url", part.MIMEType, part.Data),
				"type": providerOptionString(model, "image_type", "image_url"),
			})
		}
		if len(encoded) == 1 {
			payload[providerOptionString(model, "image_field", "image")] = encoded[0]
		} else {
			payload[providerOptionString(model, "images_field", "images")] = encoded
		}
	}
	return payload, nil
}

func normalizeXAIDuration(raw string) (int, bool) {
	clean := strings.TrimSpace(strings.ToLower(raw))
	clean = strings.TrimSuffix(clean, "s")
	if clean == "" {
		return 0, false
	}
	if value, err := strconv.Atoi(clean); err == nil && value > 0 {
		return value, true
	}
	if value, err := strconv.ParseFloat(clean, 64); err == nil && value > 0 {
		return int(value + 0.5), true
	}
	return 0, false
}

func isXAIGrokReferenceVideoModel(modelName string) bool {
	return strings.EqualFold(strings.TrimSpace(modelName), "grok-imagine-video")
}
