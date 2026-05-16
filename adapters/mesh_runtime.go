package adapters

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

type MeshBinary struct {
	Name     string
	MIMEType string
	Data     []byte
}

type MeshRequest struct {
	JobID        string
	Prompt       string
	InputImage   *MeshBinary
	Quality      string
	TextureStyle string
	OutputFormat string
	PollInterval time.Duration
}

type MeshArtifact struct {
	Name     string
	MIMEType string
	Data     []byte
	Kind     string
}

type MeshResult struct {
	Status          string
	ProviderJobID   string
	RemoteStatus    string
	PrimaryData     []byte
	PrimaryMIMEType string
	PrimaryFilename string
	PreviewData     []byte
	PreviewMIMEType string
	PreviewFilename string
	Artifacts       []MeshArtifact
	RawBody         string
	Error           string
}

func ExecuteMesh(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	switch normalizedAdapterName(model) {
	case "meshy_mesh":
		return executeMeshyMesh(ctx, model, req)
	case "tripo_mesh":
		return executeConfiguredMultipartMesh(ctx, model, req, meshAdapterDefaults{
			submitPath:       "/v2/openapi/task",
			pollPathTemplate: "/v2/openapi/task/{job_id}",
			jobIDPath:        "data.task_id",
			statusPath:       "data.status",
			successValue:     "succeeded",
			errorPath:        "data.message",
			resultURLPath:    "data.result_url",
			previewURLPath:   "data.preview_url",
			promptFieldName:  "prompt",
			fileFieldName:    "file",
			qualityFieldName: "quality",
			formatFieldName:  "format",
			modelFieldName:   "model_version",
		})
	case "hyper3d_mesh":
		return executeHyper3DMesh(ctx, model, req)
	default:
		return MeshResult{}, fmt.Errorf("adapter %q is not a mesh-generation adapter", strings.TrimSpace(model.Adapter))
	}
}

type meshAdapterDefaults struct {
	submitPath       string
	pollPathTemplate string
	jobIDPath        string
	statusPath       string
	successValue     string
	errorPath        string
	resultURLPath    string
	previewURLPath   string
	promptFieldName  string
	fileFieldName    string
	qualityFieldName string
	formatFieldName  string
	modelFieldName   string
}

func executeConfiguredMultipartMesh(ctx context.Context, model ModelConfig, req MeshRequest, defs meshAdapterDefaults) (MeshResult, error) {
	prepared := model
	authType := normalizedAuthType(prepared.AuthType, "bearer")
	prepared.AuthType = authType
	if authType == "bearer" || authType == "header_key" {
		prepared.APIKey = resolveConfiguredAPIKey(prepared)
		if strings.TrimSpace(prepared.APIKey) == "" {
			return MeshResult{}, errors.New("missing API key for this model")
		}
	}
	if strings.TrimSpace(prepared.APIPath) == "" && strings.TrimSpace(defs.submitPath) != "" {
		prepared.APIPath = defs.submitPath
	}
	endpoint := strings.TrimSpace(modelEndpoint(prepared))
	if endpoint == "" {
		return MeshResult{}, errors.New("missing mesh submit endpoint")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		_ = writer.WriteField(providerOptionString(prepared, "mesh_prompt_field", defs.promptFieldName), prompt)
	}
	if strings.TrimSpace(prepared.ModelName) != "" && defs.modelFieldName != "" {
		_ = writer.WriteField(providerOptionString(prepared, "mesh_model_field", defs.modelFieldName), strings.TrimSpace(prepared.ModelName))
	}
	if val := strings.TrimSpace(req.Quality); val != "" && defs.qualityFieldName != "" {
		_ = writer.WriteField(providerOptionString(prepared, "mesh_quality_field", defs.qualityFieldName), val)
	}
	if val := strings.TrimSpace(req.OutputFormat); val != "" && defs.formatFieldName != "" {
		_ = writer.WriteField(providerOptionString(prepared, "mesh_format_field", defs.formatFieldName), val)
	}
	if req.InputImage != nil && len(req.InputImage.Data) > 0 {
		filename := req.InputImage.Name
		if strings.TrimSpace(filename) == "" {
			filename = "input" + extensionForMIME(defaultMeshMIME(req.InputImage.Name, req.InputImage.MIMEType))
		}
		part, err := writer.CreateFormFile(providerOptionString(prepared, "mesh_image_field", defs.fileFieldName), filepath.Base(filename))
		if err != nil {
			return MeshResult{}, err
		}
		if _, err := part.Write(req.InputImage.Data); err != nil {
			return MeshResult{}, err
		}
	}
	for k, v := range providerOptionMap(prepared, "mesh_submit_fields") {
		_ = writer.WriteField(strings.TrimSpace(k), asString(v))
	}
	if err := writer.Close(); err != nil {
		return MeshResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return MeshResult{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, err := doAdapterRequest(request, modelTimeout(prepared, 20*time.Minute))
	if err != nil {
		return MeshResult{}, err
	}
	if statusCode >= 300 {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("mesh submit returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("mesh submit response was not valid JSON")
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, providerOptionString(prepared, "mesh_job_id_path", defs.jobIDPath))))
	if jobID == "" {
		return MeshResult{RawBody: string(respBody)}, errors.New("mesh submit response did not include a job id")
	}
	return pollMeshResult(ctx, prepared, req, jobID, string(respBody), defs)
}

func executeMeshyMesh(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	if req.InputImage != nil && len(req.InputImage.Data) > 0 {
		return executeMeshyImageTo3D(ctx, model, req)
	}
	return executeMeshyTextTo3D(ctx, model, req)
}

func executeMeshyTextTo3D(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	prepared := model
	prepared.AuthType = normalizedAuthType(prepared.AuthType, "bearer")
	prepared.APIKey = resolveConfiguredAPIKey(prepared)
	if strings.TrimSpace(prepared.APIKey) == "" {
		return MeshResult{}, errors.New("missing API key for this model")
	}
	if strings.TrimSpace(prepared.APIPath) == "" {
		prepared.APIPath = "/openapi/v2/text-to-3d"
	}
	body := map[string]any{"mode": "preview", "prompt": strings.TrimSpace(req.Prompt)}
	if strings.TrimSpace(prepared.ModelName) != "" {
		body["model"] = strings.TrimSpace(prepared.ModelName)
	}
	if strings.TrimSpace(req.OutputFormat) != "" {
		body["target_formats"] = []string{strings.TrimSpace(req.OutputFormat)}
	} else if formatCSV := providerOptionString(prepared, "mesh_target_formats", ""); formatCSV != "" {
		parts := strings.Split(formatCSV, ",")
		targets := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				targets = append(targets, part)
			}
		}
		if len(targets) > 0 {
			body["target_formats"] = targets
		}
	}
	for k, v := range providerOptionMap(prepared, "mesh_submit_fields") {
		body[k] = v
	}
	payload, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, modelEndpoint(prepared), bytes.NewReader(payload))
	if err != nil {
		return MeshResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, err := doAdapterRequest(request, modelTimeout(prepared, 20*time.Minute))
	if err != nil {
		return MeshResult{}, err
	}
	if statusCode >= 300 {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("mesh submit returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("mesh submit response was not valid JSON")
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, "result")))
	if jobID == "" {
		jobID = strings.TrimSpace(asString(lookupPath(submitPayload, "id")))
	}
	if jobID == "" {
		return MeshResult{RawBody: string(respBody)}, errors.New("mesh submit response did not include a job id")
	}
	return pollMeshResult(ctx, prepared, req, jobID, string(respBody), meshAdapterDefaults{
		pollPathTemplate: "/openapi/v2/text-to-3d/{job_id}",
		statusPath:       "status",
		successValue:     "succeeded",
		errorPath:        "message",
		resultURLPath:    "model_urls.glb",
		previewURLPath:   "thumbnail_url",
	})
}

func executeMeshyImageTo3D(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	prepared := model
	prepared.AuthType = normalizedAuthType(prepared.AuthType, "bearer")
	prepared.APIKey = resolveConfiguredAPIKey(prepared)
	if strings.TrimSpace(prepared.APIKey) == "" {
		return MeshResult{}, errors.New("missing API key for this model")
	}
	prepared.APIPath = "/openapi/v1/image-to-3d"
	imageDataURI := "data:" + defaultMeshMIME(req.InputImage.Name, req.InputImage.MIMEType) + ";base64," + base64.StdEncoding.EncodeToString(req.InputImage.Data)
	body := map[string]any{"image_url": imageDataURI}
	if strings.TrimSpace(req.Prompt) != "" {
		body["prompt"] = strings.TrimSpace(req.Prompt)
	}
	if strings.TrimSpace(prepared.ModelName) != "" {
		body["model"] = strings.TrimSpace(prepared.ModelName)
	}
	if strings.TrimSpace(req.OutputFormat) != "" {
		body["target_formats"] = []string{strings.TrimSpace(req.OutputFormat)}
	}
	for k, v := range providerOptionMap(prepared, "mesh_submit_fields") {
		body[k] = v
	}
	payload, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, modelEndpoint(prepared), bytes.NewReader(payload))
	if err != nil {
		return MeshResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, err := doAdapterRequest(request, modelTimeout(prepared, 20*time.Minute))
	if err != nil {
		return MeshResult{}, err
	}
	if statusCode >= 300 {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("mesh submit returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("mesh submit response was not valid JSON")
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, "result")))
	if jobID == "" {
		jobID = strings.TrimSpace(asString(lookupPath(submitPayload, "id")))
	}
	if jobID == "" {
		return MeshResult{RawBody: string(respBody)}, errors.New("mesh submit response did not include a job id")
	}
	return pollMeshResult(ctx, prepared, req, jobID, string(respBody), meshAdapterDefaults{
		pollPathTemplate: "/openapi/v1/image-to-3d/{job_id}",
		statusPath:       "status",
		successValue:     "succeeded",
		errorPath:        "message",
		resultURLPath:    "model_urls.glb",
		previewURLPath:   "thumbnail_url",
	})
}

func executeHyper3DMesh(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	prepared := model
	prepared.AuthType = normalizedAuthType(prepared.AuthType, "bearer")
	prepared.APIKey = resolveConfiguredAPIKey(prepared)
	if strings.TrimSpace(prepared.APIKey) == "" {
		return MeshResult{}, errors.New("missing API key for this model")
	}
	if strings.TrimSpace(prepared.APIPath) == "" {
		prepared.APIPath = "/v2/rodin"
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if strings.TrimSpace(req.Prompt) != "" {
		_ = writer.WriteField("prompt", strings.TrimSpace(req.Prompt))
	}
	if strings.TrimSpace(prepared.ModelName) != "" {
		_ = writer.WriteField("model", strings.TrimSpace(prepared.ModelName))
	}
	formatValue := strings.TrimSpace(req.OutputFormat)
	if formatValue == "" {
		formatValue = providerOptionString(prepared, "mesh_default_geometry_file_format", "glb")
	}
	if formatValue != "" {
		_ = writer.WriteField("geometry_file_format", formatValue)
	}
	_ = writer.WriteField("preview_render", providerOptionString(prepared, "mesh_preview_render", "true"))
	if req.InputImage != nil && len(req.InputImage.Data) > 0 {
		part, err := writer.CreateFormFile("images", filepath.Base(req.InputImage.Name))
		if err != nil {
			return MeshResult{}, err
		}
		if _, err := part.Write(req.InputImage.Data); err != nil {
			return MeshResult{}, err
		}
	}
	for k, v := range providerOptionMap(prepared, "mesh_submit_fields") {
		_ = writer.WriteField(strings.TrimSpace(k), asString(v))
	}
	if err := writer.Close(); err != nil {
		return MeshResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, modelEndpoint(prepared), &body)
	if err != nil {
		return MeshResult{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, err := doAdapterRequest(request, modelTimeout(prepared, 20*time.Minute))
	if err != nil {
		return MeshResult{}, err
	}
	if statusCode >= 300 {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("mesh submit returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("mesh submit response was not valid JSON")
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, "job_id")))
	if jobID == "" {
		jobID = strings.TrimSpace(asString(lookupPath(submitPayload, "id")))
	}
	if jobID == "" {
		return MeshResult{RawBody: string(respBody)}, errors.New("mesh submit response did not include a job id")
	}
	return pollHyper3DResult(ctx, prepared, req, jobID, string(respBody))
}

func pollMeshResult(ctx context.Context, model ModelConfig, req MeshRequest, jobID, raw string, defs meshAdapterDefaults) (MeshResult, error) {
	pollEvery := req.PollInterval
	if pollEvery <= 0 {
		pollEvery = 8 * time.Second
	}
	lastBody := raw
	statusPath := providerOptionString(model, "mesh_status_path", defs.statusPath)
	errorPath := providerOptionString(model, "mesh_error_path", defs.errorPath)
	successValue := strings.ToLower(strings.TrimSpace(providerOptionString(model, "mesh_success_value", defs.successValue)))
	failedValues := strings.Split(strings.ToLower(strings.TrimSpace(providerOptionString(model, "mesh_failure_values", "failed,error,cancelled,canceled,stopped"))), ",")
	for {
		if ctx.Err() != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody, Status: "stopped"}, ctx.Err()
		}
		pollURL := buildVideoPollURL(model, jobID, providerOptionString(model, "mesh_poll_path_template", defs.pollPathTemplate))
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		applyModelHeaders(pollReq, model)
		pollBody, pollStatus, pollCode, err := doAdapterRequest(pollReq, modelTimeout(model, 20*time.Minute))
		if err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		lastBody = string(pollBody)
		if pollCode >= 300 {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("mesh polling returned %s: %s", pollStatus, strings.TrimSpace(lastBody))
		}
		var payload any
		if err := json.Unmarshal(pollBody, &payload); err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("mesh poll response was not valid JSON")
		}
		remoteStatus := strings.ToLower(strings.TrimSpace(asString(lookupPath(payload, statusPath))))
		if successValue != "" && remoteStatus == successValue {
			return fetchMeshArtifacts(ctx, model, jobID, remoteStatus, payload, providerOptionString(model, "mesh_result_url_path", defs.resultURLPath), providerOptionString(model, "mesh_preview_url_path", defs.previewURLPath), lastBody)
		}
		for _, failed := range failedValues {
			failed = strings.TrimSpace(failed)
			if failed != "" && remoteStatus == failed {
				errText := strings.TrimSpace(asString(lookupPath(payload, errorPath)))
				if errText == "" {
					errText = remoteStatus
				}
				return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody, Error: errText, Status: "failed"}, errors.New(errText)
			}
		}
		if strings.TrimSpace(asString(lookupPath(payload, providerOptionString(model, "mesh_result_url_path", defs.resultURLPath)))) != "" {
			return fetchMeshArtifacts(ctx, model, jobID, remoteStatus, payload, providerOptionString(model, "mesh_result_url_path", defs.resultURLPath), providerOptionString(model, "mesh_preview_url_path", defs.previewURLPath), lastBody)
		}
		time.Sleep(pollEvery)
	}
}

func pollHyper3DResult(ctx context.Context, model ModelConfig, req MeshRequest, jobID, raw string) (MeshResult, error) {
	pollEvery := req.PollInterval
	if pollEvery <= 0 {
		pollEvery = 8 * time.Second
	}
	lastBody := raw
	for {
		if ctx.Err() != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody, Status: "stopped"}, ctx.Err()
		}
		pollURL := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/") + "/v2/status/" + jobID
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		applyModelHeaders(pollReq, model)
		pollBody, pollStatus, pollCode, err := doAdapterRequest(pollReq, modelTimeout(model, 20*time.Minute))
		if err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		lastBody = string(pollBody)
		if pollCode >= 300 {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("mesh polling returned %s: %s", pollStatus, strings.TrimSpace(lastBody))
		}
		var payload any
		if err := json.Unmarshal(pollBody, &payload); err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("mesh poll response was not valid JSON")
		}
		statusValue := strings.ToLower(strings.TrimSpace(asString(lookupPath(payload, "status"))))
		if statusValue == "completed" || statusValue == "success" || statusValue == "succeeded" {
			break
		}
		if statusValue == "failed" || statusValue == "error" || statusValue == "cancelled" || statusValue == "canceled" || statusValue == "stopped" {
			errText := strings.TrimSpace(asString(lookupPath(payload, "message")))
			if errText == "" {
				errText = statusValue
			}
			return MeshResult{ProviderJobID: jobID, RemoteStatus: statusValue, RawBody: lastBody, Error: errText, Status: "failed"}, errors.New(errText)
		}
		time.Sleep(pollEvery)
	}
	downloadURL := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/") + "/v2/download"
	payload, _ := json.Marshal(map[string]any{"job_id": jobID})
	reqDL, err := http.NewRequestWithContext(ctx, http.MethodPost, downloadURL, bytes.NewReader(payload))
	if err != nil {
		return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, err
	}
	reqDL.Header.Set("Content-Type", "application/json")
	applyModelHeaders(reqDL, model)
	body, status, code, err := doAdapterRequest(reqDL, modelTimeout(model, 20*time.Minute))
	if err != nil {
		return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, err
	}
	if code >= 300 {
		return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("mesh download listing returned %s: %s", status, strings.TrimSpace(string(body)))
	}
	var payloadAny any
	if err := json.Unmarshal(body, &payloadAny); err != nil {
		return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("mesh download response was not valid JSON")
	}
	return fetchMeshArtifacts(ctx, model, jobID, "completed", payloadAny, "model_urls.glb", "thumbnail_url", string(body))
}

func fetchMeshArtifacts(ctx context.Context, model ModelConfig, jobID, remoteStatus string, pollPayload any, resultURLPath, previewURLPath, raw string) (MeshResult, error) {
	primaryURL := strings.TrimSpace(asString(lookupPath(pollPayload, resultURLPath)))
	previewURL := strings.TrimSpace(asString(lookupPath(pollPayload, previewURLPath)))
	if primaryURL == "" {
		if modelURLs, ok := lookupPath(pollPayload, "model_urls").(map[string]any); ok {
			for _, ext := range []string{"glb", "fbx", "obj", "usdz", "stl", "zip"} {
				if u := strings.TrimSpace(asString(modelURLs[ext])); u != "" {
					primaryURL = u
					resultURLPath = "model_urls." + ext
					break
				}
			}
		}
	}
	if primaryURL == "" {
		return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, Status: "completed"}, errors.New("mesh job completed but no model URL was returned")
	}
	artifacts := []MeshArtifact{}
	primaryArtifact, err := downloadMeshArtifact(ctx, model, primaryURL, "primary", "model")
	if err != nil {
		return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw}, err
	}
	artifacts = append(artifacts, primaryArtifact)
	result := MeshResult{
		Status:          "completed",
		ProviderJobID:   jobID,
		RemoteStatus:    remoteStatus,
		PrimaryData:     primaryArtifact.Data,
		PrimaryMIMEType: primaryArtifact.MIMEType,
		PrimaryFilename: primaryArtifact.Name,
		Artifacts:       append([]MeshArtifact{}, artifacts...),
		RawBody:         raw,
	}
	if previewURL != "" {
		previewArtifact, err := downloadMeshArtifact(ctx, model, previewURL, "preview", "preview")
		if err == nil {
			result.PreviewData = previewArtifact.Data
			result.PreviewMIMEType = previewArtifact.MIMEType
			result.PreviewFilename = previewArtifact.Name
			result.Artifacts = append(result.Artifacts, previewArtifact)
		}
	}
	return result, nil
}

func downloadMeshArtifact(ctx context.Context, model ModelConfig, urlValue, kind, fallbackName string) (MeshArtifact, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, urlValue, nil)
	if err != nil {
		return MeshArtifact{}, err
	}
	applyModelHeaders(req, model)
	body, status, code, err := doAdapterRequest(req, modelTimeout(model, 20*time.Minute))
	if err != nil {
		return MeshArtifact{}, err
	}
	if code >= 300 {
		return MeshArtifact{}, fmt.Errorf("mesh artifact download returned %s", status)
	}
	mimeType := http.DetectContentType(body)
	name := fallbackName + extensionForMIME(mimeType)
	if parsed := strings.TrimSpace(filepath.Base(strings.Split(urlValue, "?")[0])); parsed != "." && parsed != "" && parsed != "/" {
		name = parsed
	}
	return MeshArtifact{Name: sanitizeMeshFilename(name), MIMEType: mimeType, Data: body, Kind: kind}, nil
}

func defaultMeshMIME(name, supplied string) string {
	if strings.TrimSpace(supplied) != "" {
		return supplied
	}
	if ext := strings.TrimSpace(filepath.Ext(name)); ext != "" {
		if guessed := mime.TypeByExtension(ext); guessed != "" {
			return guessed
		}
	}
	return "application/octet-stream"
}

func sanitizeMeshFilename(name string) string {
	clean := strings.TrimSpace(filepath.Base(name))
	clean = strings.ReplaceAll(clean, "\x00", "")
	if clean == "" || clean == "." || clean == string(filepath.Separator) {
		return "artifact.bin"
	}
	clean = strings.ReplaceAll(clean, " ", "_")
	return clean
}
