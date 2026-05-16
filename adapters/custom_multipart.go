package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

type customMultipartAdapter struct{}

func (customMultipartAdapter) Execute(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	if isIdeogramModel(model) {
		return executeIdeogramMultipart(ctx, model, req)
	}
	endpoint := strings.TrimSpace(modelEndpoint(model))
	if endpoint == "" {
		return Response{}, errors.New("missing custom multipart endpoint")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writeCustomMultipartBody(writer, model, req); err != nil {
		return Response{}, err
	}
	if err := writer.Close(); err != nil {
		return Response{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	applyModelHeaders(request, model)
	client := &http.Client{Timeout: modelTimeout(model, 10*time.Minute)}
	resp, err := client.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("custom multipart endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	response, err := parseMultipartCapableResponse(respBody, resp.Header.Get("Content-Type"), model, func(body []byte) (string, error) {
		text := extractCustomResponseText(body, model)
		if strings.TrimSpace(text) == "" {
			return "", nil
		}
		return text, nil
	})
	if err != nil {
		return Response{}, err
	}
	response.Status = resp.Status
	response.StatusCode = resp.StatusCode
	if strings.TrimSpace(response.RawBody) == "" {
		response.RawBody = response.Text
	}
	return response, nil
}

func isIdeogramModel(model ModelConfig) bool {
	provider := strings.ToLower(strings.TrimSpace(model.Provider))
	base := strings.ToLower(strings.TrimSpace(model.BaseURL))
	return provider == "ideogram" || strings.Contains(base, "api.ideogram.ai")
}

func executeIdeogramMultipart(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	operation, primaryImage, extraImages, err := ideogramOperationAndImages(req, model)
	if err != nil {
		return Response{}, err
	}
	endpoint := ideogramEndpoint(model, operation)
	if strings.TrimSpace(endpoint) == "" {
		return Response{}, errors.New("missing Ideogram endpoint")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writeIdeogramMultipartBody(writer, model, req, operation, primaryImage, extraImages); err != nil {
		return Response{}, err
	}
	if err := writer.Close(); err != nil {
		return Response{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	applyModelHeaders(request, model)
	respBody, status, statusCode, headers, err := doAdapterRequestWithHeaders(request, modelTimeout(model, 10*time.Minute))
	if err != nil {
		return Response{}, err
	}
	if statusCode >= 300 {
		return Response{}, fmt.Errorf("Ideogram returned %s: %s", status, strings.TrimSpace(string(respBody)))
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

func ideogramOperationAndImages(req Request, model ModelConfig) (string, *Part, []Part, error) {
	operation := strings.ToLower(strings.TrimSpace(providerOptionString(model, "ideogram_operation", "auto")))
	images := collectMultipartMediaParts(req, "image")
	var primary *Part
	var extras []Part
	if len(images) > 0 {
		primary = &images[0]
		if len(images) > 1 {
			extras = append(extras, images[1:]...)
		}
	}
	switch operation {
	case "", "auto":
		if primary != nil {
			return "remix", primary, extras, nil
		}
		return "generate", nil, nil, nil
	case "generate":
		return "generate", nil, images, nil
	case "remix":
		if primary == nil {
			return "", nil, nil, errors.New("Ideogram remix requires one input image")
		}
		return "remix", primary, extras, nil
	default:
		return "", nil, nil, fmt.Errorf("unsupported Ideogram operation: %s", operation)
	}
}

func collectMultipartMediaParts(req Request, kind string) []Part {
	out := []Part{}
	for _, message := range req.Messages {
		for _, part := range normalizeMessageParts(message) {
			if strings.EqualFold(strings.TrimSpace(part.Kind), kind) && len(part.Data) > 0 {
				out = append(out, part)
			}
		}
	}
	return out
}

func ideogramEndpoint(model ModelConfig, operation string) string {
	base := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
	apiPath := strings.TrimSpace(model.APIPath)
	version := strings.ToLower(strings.TrimSpace(providerOptionString(model, "ideogram_api_version", "v3")))
	if strings.HasPrefix(apiPath, "http://") || strings.HasPrefix(apiPath, "https://") {
		return apiPath
	}
	defaultPath := ideogramDefaultAPIPath(operation, version)
	if apiPath == "" {
		return base + defaultPath
	}
	lowerPath := strings.ToLower(apiPath)
	knownIdeogramPath := strings.Contains(lowerPath, "/generate") || strings.Contains(lowerPath, "/remix") || strings.Contains(lowerPath, "/edit") || strings.Contains(lowerPath, "ideogram-v3")
	if knownIdeogramPath {
		return base + defaultPath
	}
	if !strings.HasPrefix(apiPath, "/") {
		apiPath = "/" + apiPath
	}
	return base + apiPath
}

func ideogramDefaultAPIPath(operation, version string) string {
	if version == "legacy" {
		switch operation {
		case "remix":
			return "/remix"
		default:
			return "/generate"
		}
	}
	switch operation {
	case "remix":
		return "/v1/ideogram-v3/remix"
	default:
		return "/v1/ideogram-v3/generate"
	}
}

func ideogramPrompt(req Request, model ModelConfig) string {
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

func writeIdeogramMultipartBody(writer *multipart.Writer, model ModelConfig, req Request, operation string, primaryImage *Part, extraImages []Part) error {
	prompt := ideogramPrompt(req, model)
	if strings.TrimSpace(prompt) == "" {
		return errors.New("Ideogram requires a non-empty prompt")
	}
	if err := writer.WriteField("prompt", prompt); err != nil {
		return err
	}
	for _, key := range []string{"rendering_speed", "magic_prompt", "negative_prompt", "num_images", "seed", "resolution", "aspect_ratio", "style_type", "style_preset", "image_weight"} {
		value := strings.TrimSpace(providerOptionString(model, key, ""))
		if value == "" {
			continue
		}
		if err := writer.WriteField(key, value); err != nil {
			return err
		}
	}
	for key, value := range providerOptionMap(model, "extra_fields") {
		if strings.TrimSpace(key) == "" || value == nil {
			continue
		}
		if err := writer.WriteField(strings.TrimSpace(key), strings.TrimSpace(fmt.Sprint(value))); err != nil {
			return err
		}
	}
	if operation == "remix" {
		if primaryImage == nil || len(primaryImage.Data) == 0 {
			return errors.New("Ideogram remix requires one input image")
		}
		if err := writeMultipartBinaryPart(writer, "image", *primaryImage); err != nil {
			return err
		}
	}
	for _, part := range extraImages {
		if err := writeMultipartBinaryPart(writer, "style_reference_images", part); err != nil {
			return err
		}
	}
	return nil
}

func writeMultipartBinaryPart(writer *multipart.Writer, fieldName string, part Part) error {
	headers := textproto.MIMEHeader{}
	filename := defaultString(strings.TrimSpace(part.Name), defaultString(strings.TrimSpace(part.RelPath), "attachment"))
	headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, fieldName, filename))
	headers.Set("Content-Type", defaultString(strings.TrimSpace(part.MIMEType), "application/octet-stream"))
	fileWriter, err := writer.CreatePart(headers)
	if err != nil {
		return err
	}
	_, err = fileWriter.Write(part.Data)
	return err
}

func writeCustomMultipartBody(writer *multipart.Writer, model ModelConfig, req Request) error {
	promptField := providerOptionString(model, "prompt_field", "prompt")
	instructionsField := providerOptionString(model, "instructions_field", "instructions")
	modelField := providerOptionString(model, "model_field", "model")
	messagesField := providerOptionString(model, "messages_field", "messages_json")
	metadataField := providerOptionString(model, "metadata_field", "metadata_json")
	fileField := providerOptionString(model, "file_field", "files")
	if strings.TrimSpace(promptField) != "" {
		if err := writer.WriteField(promptField, flattenMessages(req.Messages)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(req.Instructions) != "" && strings.TrimSpace(instructionsField) != "" {
		if err := writer.WriteField(instructionsField, strings.TrimSpace(req.Instructions)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(model.ModelName) != "" && strings.TrimSpace(modelField) != "" {
		if err := writer.WriteField(modelField, strings.TrimSpace(model.ModelName)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(messagesField) != "" {
		encoded, err := json.Marshal(buildMultipartMessages(req))
		if err != nil {
			return err
		}
		if err := writer.WriteField(messagesField, string(encoded)); err != nil {
			return err
		}
	}
	if strings.TrimSpace(metadataField) != "" {
		encoded, err := json.Marshal(buildMultipartMetadata(req))
		if err != nil {
			return err
		}
		if err := writer.WriteField(metadataField, string(encoded)); err != nil {
			return err
		}
	}
	for key, value := range providerOptionMap(model, "extra_fields") {
		if strings.TrimSpace(key) == "" || value == nil {
			continue
		}
		if err := writer.WriteField(strings.TrimSpace(key), strings.TrimSpace(fmt.Sprint(value))); err != nil {
			return err
		}
	}
	for _, message := range req.Messages {
		for _, part := range normalizeMessageParts(message) {
			switch strings.ToLower(strings.TrimSpace(part.Kind)) {
			case "image", "audio", "video", "file":
				if err := writeMultipartBinaryPart(writer, fileField, part); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
