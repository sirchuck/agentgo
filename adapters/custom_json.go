package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type customJSONAdapter struct{}

func (customJSONAdapter) Execute(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	endpoint := strings.TrimSpace(modelEndpoint(model))
	if endpoint == "" {
		return Response{}, errors.New("missing custom json endpoint")
	}
	payload, err := buildProviderAwareJSONPayload(model, req)
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
	applyModelHeaders(request, model)
	respBody, status, statusCode, headers, err := doAdapterRequestWithHeaders(request, modelTimeout(model, 10*time.Minute))
	if err != nil {
		return Response{}, err
	}
	if statusCode >= 300 {
		return Response{}, fmt.Errorf("custom json endpoint returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	response, err := parseMultipartCapableResponse(respBody, headers.Get("Content-Type"), model, func(body []byte) (string, error) {
		text := extractCustomResponseText(body, model)
		if strings.TrimSpace(text) == "" {
			return "", nil
		}
		return text, nil
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

func buildProviderAwareJSONPayload(model ModelConfig, req Request) (map[string]any, error) {
	if isSeedreamModel(model) {
		return buildSeedreamJSONPayload(model, req)
	}
	return buildCustomJSONPayload(model, req), nil
}

func buildCustomJSONPayload(model ModelConfig, req Request) map[string]any {
	payload := map[string]any{}
	if extra := providerOptionMap(model, "extra_body"); len(extra) > 0 {
		for key, value := range extra {
			payload[key] = value
		}
	}
	promptField := providerOptionString(model, "prompt_field", "prompt")
	instructionsField := providerOptionString(model, "instructions_field", "instructions")
	modelField := providerOptionString(model, "model_field", "model")
	messagesField := providerOptionString(model, "messages_field", "messages")
	filesField := providerOptionString(model, "files_field", "files")
	metadataField := providerOptionString(model, "metadata_field", "metadata")
	binaryMode := binaryEncodingMode(model)
	if strings.TrimSpace(promptField) != "" {
		setNestedField(payload, promptField, flattenMessages(req.Messages))
	}
	if strings.TrimSpace(req.Instructions) != "" && strings.TrimSpace(instructionsField) != "" {
		setNestedField(payload, instructionsField, strings.TrimSpace(req.Instructions))
	}
	if strings.TrimSpace(model.ModelName) != "" && strings.TrimSpace(modelField) != "" {
		setNestedField(payload, modelField, strings.TrimSpace(model.ModelName))
	}
	if strings.TrimSpace(messagesField) != "" {
		setNestedField(payload, messagesField, buildCustomJSONMessages(req.Messages, binaryMode))
	}
	if strings.TrimSpace(filesField) != "" {
		setNestedField(payload, filesField, buildCustomJSONFiles(req.Messages, binaryMode))
	}
	if strings.TrimSpace(metadataField) != "" {
		setNestedField(payload, metadataField, map[string]any{"expect_json": req.ExpectJSON, "binary_encoding": binaryMode})
	}
	return payload
}

func isSeedreamModel(model ModelConfig) bool {
	provider := strings.ToLower(strings.TrimSpace(model.Provider))
	base := strings.ToLower(strings.TrimSpace(model.BaseURL))
	name := strings.ToLower(strings.TrimSpace(model.ModelName))
	label := strings.ToLower(strings.TrimSpace(model.Label))
	if provider == "seedream" {
		return true
	}
	identity := strings.Join([]string{provider, base, name, label}, " ")
	return strings.Contains(identity, "seedream") || strings.Contains(identity, "modelark") || strings.Contains(identity, "byteplus")
}

func buildSeedreamJSONPayload(model ModelConfig, req Request) (map[string]any, error) {
	prompt := seedreamPrompt(req, model)
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("Seedream requires a non-empty prompt")
	}
	payload := map[string]any{}
	if extra := providerOptionMap(model, "extra_body"); len(extra) > 0 {
		for key, value := range extra {
			payload[key] = value
		}
	}
	payload[providerOptionString(model, "model_field", "model")] = strings.TrimSpace(model.ModelName)
	payload[providerOptionString(model, "prompt_field", "prompt")] = prompt
	payload[providerOptionString(model, "response_format_field", "response_format")] = providerOptionString(model, "response_format", "url")
	if size := strings.TrimSpace(providerOptionString(model, "size", "adaptive")); size != "" {
		payload[providerOptionString(model, "size_field", "size")] = size
	}
	if negative := strings.TrimSpace(providerOptionString(model, "negative_prompt", "")); negative != "" {
		payload[providerOptionString(model, "negative_prompt_field", "negative_prompt")] = negative
	}
	if aspectRatio := strings.TrimSpace(providerOptionString(model, "aspect_ratio", "")); aspectRatio != "" {
		payload[providerOptionString(model, "aspect_ratio_field", "aspect_ratio")] = aspectRatio
	}
	for _, key := range []string{"seed", "guidance_scale", "watermark", "n", "num_images"} {
		if value, ok := providerOptionValue(model, key); ok {
			payload[providerOptionString(model, key+"_field", key)] = value
		}
	}
	images := collectSeedreamImages(req, model)
	if len(images) == 1 {
		payload[providerOptionString(model, "image_field", "image")] = images[0]
	} else if len(images) > 1 {
		payload[providerOptionString(model, "image_field", "image")] = images
	}
	for key, value := range providerOptionMap(model, "extra_fields") {
		cleanKey := strings.TrimSpace(key)
		if cleanKey == "" || value == nil {
			continue
		}
		payload[cleanKey] = value
	}
	return payload, nil
}

func seedreamPrompt(req Request, model ModelConfig) string {
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

func collectSeedreamImages(req Request, model ModelConfig) []string {
	parts := collectMultipartMediaParts(req, "image")
	if len(parts) == 0 {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(providerOptionString(model, "seedream_image_encoding", "base64")))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part.Data) == 0 {
			continue
		}
		switch mode {
		case "data_url":
			out = append(out, encodedBinaryData("data_url", part.MIMEType, part.Data))
		default:
			out = append(out, encodedBinaryData("base64", part.MIMEType, part.Data))
		}
	}
	return out
}
