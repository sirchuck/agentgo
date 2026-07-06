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
	JobID               string
	Prompt              string
	InputImage          *MeshBinary
	ReferenceImages     []MeshBinary
	Quality             string
	TextureStyle        string
	OutputFormat        string
	MeshMode            string
	SourceProviderJobID string
	PollInterval        time.Duration
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
	TaskType        string
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
		return executeTripoMesh(ctx, model, req)
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

func executeTripoMesh(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	prepared := model
	prepared.AuthType = normalizedAuthType(prepared.AuthType, "bearer")
	prepared.APIKey = resolveConfiguredAPIKey(prepared)
	if strings.TrimSpace(prepared.APIKey) == "" {
		return MeshResult{}, errors.New("missing API key for this model")
	}
	if strings.TrimSpace(prepared.BaseURL) == "" {
		prepared.BaseURL = "https://api.tripo3d.ai"
	}
	if strings.TrimSpace(prepared.APIPath) == "" {
		prepared.APIPath = "/v2/openapi/task"
	}
	endpoint := strings.TrimSpace(modelEndpoint(prepared))
	if endpoint == "" {
		return MeshResult{}, errors.New("missing Tripo task endpoint")
	}
	body := map[string]any{}
	if req.InputImage != nil && len(req.InputImage.Data) > 0 {
		fileToken, err := uploadTripoMeshImage(ctx, prepared, *req.InputImage)
		if err != nil {
			return MeshResult{}, err
		}
		body["type"] = "image_to_model"
		body["file"] = map[string]any{
			"type":       tripoFileType(req.InputImage.Name, req.InputImage.MIMEType),
			"file_token": fileToken,
		}
	} else {
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			return MeshResult{}, errors.New("prompt is required for Tripo text-to-model jobs")
		}
		body["type"] = "text_to_model"
		body["prompt"] = prompt
	}
	if strings.TrimSpace(prepared.ModelName) != "" {
		body["model_version"] = strings.TrimSpace(prepared.ModelName)
	}
	if q := tripoQualityValue(req.Quality); q != "" {
		body["texture_quality"] = q
		body["geometry_quality"] = q
	}
	for k, v := range providerOptionMap(prepared, "mesh_submit_fields") {
		body[k] = v
	}
	payload, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
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
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("Tripo task submit returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("Tripo task submit response was not valid JSON")
	}
	if errText := tripoAPIError(submitPayload); errText != "" {
		return MeshResult{RawBody: string(respBody), Error: errText}, errors.New(errText)
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, "data.task_id")))
	if jobID == "" {
		jobID = strings.TrimSpace(asString(lookupPath(submitPayload, "task_id")))
	}
	if jobID == "" {
		return MeshResult{RawBody: string(respBody)}, errors.New("Tripo task submit response did not include a task id")
	}
	return pollTripoMeshResult(ctx, prepared, req, jobID, string(respBody))
}

func uploadTripoMeshImage(ctx context.Context, model ModelConfig, image MeshBinary) (string, error) {
	if len(image.Data) == 0 {
		return "", errors.New("missing Tripo image data")
	}
	uploadPath := providerOptionString(model, "tripo_upload_path", "")
	if uploadPath == "" {
		uploadPath = providerOptionString(model, "mesh_upload_path", "/v2/openapi/upload")
	}
	endpoint := tripoEndpoint(model, uploadPath)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filename := strings.TrimSpace(image.Name)
	if filename == "" {
		filename = "input." + tripoFileType(image.Name, image.MIMEType)
	}
	part, err := writer.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(image.Data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	applyModelHeaders(request, model)
	respBody, status, statusCode, err := doAdapterRequest(request, modelTimeout(model, 20*time.Minute))
	if err != nil {
		return "", err
	}
	if statusCode >= 300 {
		return "", fmt.Errorf("Tripo image upload returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var uploadPayload any
	if err := json.Unmarshal(respBody, &uploadPayload); err != nil {
		return "", fmt.Errorf("Tripo image upload response was not valid JSON")
	}
	if errText := tripoAPIError(uploadPayload); errText != "" {
		return "", errors.New(errText)
	}
	for _, path := range []string{"data.file_token", "data.image_token", "data.token", "file_token", "image_token", "token"} {
		if token := strings.TrimSpace(asString(lookupPath(uploadPayload, path))); token != "" {
			return token, nil
		}
	}
	return "", errors.New("Tripo image upload response did not include a file token")
}

func pollTripoMeshResult(ctx context.Context, model ModelConfig, req MeshRequest, jobID, raw string) (MeshResult, error) {
	pollEvery := req.PollInterval
	if pollEvery <= 0 {
		pollEvery = 8 * time.Second
	}
	lastBody := raw
	for {
		if ctx.Err() != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody, Status: "stopped"}, ctx.Err()
		}
		pollURL := tripoEndpoint(model, "/v2/openapi/task/"+jobID)
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
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("Tripo task polling returned %s: %s", pollStatus, strings.TrimSpace(lastBody))
		}
		var payload any
		if err := json.Unmarshal(pollBody, &payload); err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("Tripo task poll response was not valid JSON")
		}
		if errText := tripoAPIError(payload); errText != "" {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody, Error: errText, Status: "failed"}, errors.New(errText)
		}
		remoteStatus := strings.ToLower(strings.TrimSpace(asString(lookupPath(payload, "data.status"))))
		if remoteStatus == "" {
			remoteStatus = strings.ToLower(strings.TrimSpace(asString(lookupPath(payload, "status"))))
		}
		switch remoteStatus {
		case "success", "succeeded", "completed":
			return fetchTripoMeshArtifacts(ctx, model, jobID, remoteStatus, payload, lastBody)
		case "failed", "error", "cancelled", "canceled", "banned", "expired", "unknown":
			errText := strings.TrimSpace(asString(lookupPath(payload, "data.message")))
			if errText == "" {
				errText = strings.TrimSpace(asString(lookupPath(payload, "message")))
			}
			if errText == "" {
				errText = remoteStatus
			}
			return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody, Error: errText, Status: "failed"}, errors.New(errText)
		}
		time.Sleep(pollEvery)
	}
}

func fetchTripoMeshArtifacts(ctx context.Context, model ModelConfig, jobID, remoteStatus string, pollPayload any, raw string) (MeshResult, error) {
	result := MeshResult{Status: "completed", ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw}
	modelFields := []struct {
		path string
		kind string
		name string
	}{
		{path: "data.output.pbr_model", kind: "primary", name: "pbr_model"},
		{path: "data.output.model", kind: "model", name: "model"},
		{path: "data.output.base_model", kind: "base_model", name: "base_model"},
		{path: "output.pbr_model", kind: "primary", name: "pbr_model"},
		{path: "output.model", kind: "model", name: "model"},
		{path: "output.base_model", kind: "base_model", name: "base_model"},
	}
	seen := map[string]bool{}
	for _, field := range modelFields {
		urlValue := strings.TrimSpace(asString(lookupPath(pollPayload, field.path)))
		if urlValue == "" || seen[urlValue] {
			continue
		}
		seen[urlValue] = true
		artifact, err := downloadMeshArtifact(ctx, model, urlValue, field.kind, field.name)
		if err != nil {
			if result.PrimaryData == nil {
				return result, err
			}
			continue
		}
		result.Artifacts = append(result.Artifacts, artifact)
		if result.PrimaryData == nil {
			result.PrimaryData = artifact.Data
			result.PrimaryMIMEType = artifact.MIMEType
			result.PrimaryFilename = artifact.Name
		}
	}
	if result.PrimaryData == nil {
		return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, Status: "completed"}, errors.New("Tripo task completed but no model URL was returned")
	}
	for _, path := range []string{"data.output.rendered_image", "output.rendered_image"} {
		previewURL := strings.TrimSpace(asString(lookupPath(pollPayload, path)))
		if previewURL == "" {
			continue
		}
		previewArtifact, err := downloadMeshArtifact(ctx, model, previewURL, "preview", "preview")
		if err == nil {
			result.PreviewData = previewArtifact.Data
			result.PreviewMIMEType = previewArtifact.MIMEType
			result.PreviewFilename = previewArtifact.Name
			result.Artifacts = append(result.Artifacts, previewArtifact)
		}
		break
	}
	return result, nil
}

func tripoEndpoint(model ModelConfig, path string) string {
	base := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
	if base == "" {
		base = "https://api.tripo3d.ai"
	}
	cleanPath := "/" + strings.TrimLeft(strings.TrimSpace(path), "/")
	if strings.HasSuffix(base, "/v2/openapi") && strings.HasPrefix(cleanPath, "/v2/openapi/") {
		cleanPath = strings.TrimPrefix(cleanPath, "/v2/openapi")
	}
	return base + cleanPath
}

func tripoFileType(name, mimeType string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if ext == "jpeg" {
		return "jpg"
	}
	if ext == "jpg" || ext == "png" || ext == "webp" {
		return ext
	}
	mediaType := strings.ToLower(strings.TrimSpace(mimeType))
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil && strings.TrimSpace(parsed) != "" {
		mediaType = strings.ToLower(strings.TrimSpace(parsed))
	}
	switch mediaType {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	default:
		return "jpg"
	}
}

func tripoQualityValue(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return ""
	case "high", "hd", "detailed", "detail":
		return "detailed"
	case "standard", "normal", "default":
		return "standard"
	default:
		return strings.TrimSpace(value)
	}
}

func tripoAPIError(payload any) string {
	codeValue := strings.TrimSpace(asString(lookupPath(payload, "code")))
	if codeValue == "" || codeValue == "0" {
		return ""
	}
	for _, path := range []string{"message", "msg", "data.message", "error", "data.error"} {
		if msg := strings.TrimSpace(asString(lookupPath(payload, path))); msg != "" {
			return msg
		}
	}
	return "Tripo API returned code " + codeValue
}

func executeMeshyMesh(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	mode := strings.ToLower(strings.TrimSpace(req.MeshMode))
	if mode == "refine" || strings.TrimSpace(req.SourceProviderJobID) != "" {
		return executeMeshyTextRefine(ctx, model, req)
	}
	if req.InputImage != nil && len(req.InputImage.Data) > 0 {
		if len(req.ReferenceImages) > 0 {
			return executeMeshyMultiImageTo3D(ctx, model, req)
		}
		return executeMeshyImageTo3D(ctx, model, req)
	}
	return executeMeshyTextTo3D(ctx, model, req)
}

func prepareMeshyModel(model ModelConfig, defaultPath string) (ModelConfig, error) {
	prepared := model
	prepared.AuthType = normalizedAuthType(prepared.AuthType, "bearer")
	prepared.APIKey = resolveConfiguredAPIKey(prepared)
	if strings.TrimSpace(prepared.APIKey) == "" {
		return prepared, errors.New("missing API key for this model")
	}
	if strings.TrimSpace(prepared.BaseURL) == "" {
		prepared.BaseURL = "https://api.meshy.ai"
	}
	if strings.TrimSpace(prepared.APIPath) == "" || strings.Contains(strings.ToLower(strings.TrimSpace(prepared.APIPath)), "/text-to-3d") && defaultPath != "/openapi/v2/text-to-3d" {
		prepared.APIPath = defaultPath
	}
	return prepared, nil
}

func meshyTargetFormats(model ModelConfig, req MeshRequest) []string {
	source := strings.TrimSpace(req.OutputFormat)
	if source == "" {
		source = providerOptionString(model, "mesh_target_formats", "glb")
	}
	parts := strings.Split(source, ",")
	targets := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		clean := strings.ToLower(strings.TrimSpace(part))
		if clean == "" || seen[clean] {
			continue
		}
		targets = append(targets, clean)
		seen[clean] = true
	}
	if len(targets) == 0 {
		targets = []string{"glb"}
	}
	return targets
}

func meshyImageDataURI(image MeshBinary) string {
	return "data:" + defaultMeshMIME(image.Name, image.MIMEType) + ";base64," + base64.StdEncoding.EncodeToString(image.Data)
}

func addMeshyCommonOptions(body map[string]any, prepared ModelConfig, req MeshRequest) {
	if strings.TrimSpace(prepared.ModelName) != "" {
		body["ai_model"] = strings.TrimSpace(prepared.ModelName)
	}
	if targets := meshyTargetFormats(prepared, req); len(targets) > 0 {
		body["target_formats"] = targets
	}
	for k, v := range providerOptionMap(prepared, "mesh_submit_fields") {
		body[k] = v
	}
}

func submitMeshyTask(ctx context.Context, prepared ModelConfig, body map[string]any, submitKind string) (string, string, error) {
	payload, _ := json.Marshal(body)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, modelEndpoint(prepared), bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/json")
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, err := doAdapterRequest(request, modelTimeout(prepared, 20*time.Minute))
	if err != nil {
		return "", string(respBody), err
	}
	if statusCode >= 300 {
		return "", string(respBody), fmt.Errorf("%s submit returned %s: %s", submitKind, status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return "", string(respBody), fmt.Errorf("%s submit response was not valid JSON", submitKind)
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, "result")))
	if jobID == "" {
		jobID = strings.TrimSpace(asString(lookupPath(submitPayload, "id")))
	}
	if jobID == "" {
		return "", string(respBody), errors.New("mesh submit response did not include a job id")
	}
	return jobID, string(respBody), nil
}

func executeMeshyTextTo3D(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	prepared, err := prepareMeshyModel(model, "/openapi/v2/text-to-3d")
	if err != nil {
		return MeshResult{}, err
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return MeshResult{}, errors.New("prompt is required for Meshy text-to-3D preview jobs")
	}
	body := map[string]any{"mode": "preview", "prompt": prompt}
	addMeshyCommonOptions(body, prepared, req)
	jobID, raw, err := submitMeshyTask(ctx, prepared, body, "Meshy text-to-3D preview")
	if err != nil {
		return MeshResult{RawBody: raw}, err
	}
	result, err := pollMeshResult(ctx, prepared, req, jobID, raw, meshAdapterDefaults{
		pollPathTemplate: "/openapi/v2/text-to-3d/{job_id}",
		statusPath:       "status",
		successValue:     "succeeded",
		errorPath:        "task_error.message",
		resultURLPath:    "model_urls.glb",
		previewURLPath:   "thumbnail_url",
	})
	if result.TaskType == "" {
		result.TaskType = "text-to-3d-preview"
	}
	return result, err
}

func executeMeshyTextRefine(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	prepared, err := prepareMeshyModel(model, "/openapi/v2/text-to-3d")
	if err != nil {
		return MeshResult{}, err
	}
	previewTaskID := strings.TrimSpace(req.SourceProviderJobID)
	if previewTaskID == "" {
		return MeshResult{}, errors.New("preview task id is required for Meshy refine jobs")
	}
	body := map[string]any{
		"mode":            "refine",
		"preview_task_id": previewTaskID,
		"enable_pbr":      true,
	}
	addMeshyCommonOptions(body, prepared, req)
	jobID, raw, err := submitMeshyTask(ctx, prepared, body, "Meshy text-to-3D refine")
	if err != nil {
		return MeshResult{RawBody: raw}, err
	}
	result, err := pollMeshResult(ctx, prepared, req, jobID, raw, meshAdapterDefaults{
		pollPathTemplate: "/openapi/v2/text-to-3d/{job_id}",
		statusPath:       "status",
		successValue:     "succeeded",
		errorPath:        "task_error.message",
		resultURLPath:    "model_urls.glb",
		previewURLPath:   "thumbnail_url",
	})
	if result.TaskType == "" {
		result.TaskType = "text-to-3d-refine"
	}
	return result, err
}

func executeMeshyImageTo3D(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	prepared, err := prepareMeshyModel(model, "/openapi/v1/image-to-3d")
	if err != nil {
		return MeshResult{}, err
	}
	if req.InputImage == nil || len(req.InputImage.Data) == 0 {
		return MeshResult{}, errors.New("image data is required for Meshy image-to-3D jobs")
	}
	body := map[string]any{
		"image_url":      meshyImageDataURI(*req.InputImage),
		"should_texture": true,
	}
	if strings.TrimSpace(req.Prompt) != "" {
		body["texture_prompt"] = strings.TrimSpace(req.Prompt)
	}
	addMeshyCommonOptions(body, prepared, req)
	jobID, raw, err := submitMeshyTask(ctx, prepared, body, "Meshy image-to-3D")
	if err != nil {
		return MeshResult{RawBody: raw}, err
	}
	result, err := pollMeshResult(ctx, prepared, req, jobID, raw, meshAdapterDefaults{
		pollPathTemplate: "/openapi/v1/image-to-3d/{job_id}",
		statusPath:       "status",
		successValue:     "succeeded",
		errorPath:        "task_error.message",
		resultURLPath:    "model_urls.glb",
		previewURLPath:   "thumbnail_url",
	})
	if result.TaskType == "" {
		result.TaskType = "image-to-3d"
	}
	return result, err
}

func executeMeshyMultiImageTo3D(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	prepared, err := prepareMeshyModel(model, "/openapi/v1/multi-image-to-3d")
	if err != nil {
		return MeshResult{}, err
	}
	if req.InputImage == nil || len(req.InputImage.Data) == 0 {
		return MeshResult{}, errors.New("primary/front image is required for Meshy multi-image-to-3D jobs")
	}
	imageURLs := []string{meshyImageDataURI(*req.InputImage)}
	for _, ref := range req.ReferenceImages {
		if len(ref.Data) > 0 {
			imageURLs = append(imageURLs, meshyImageDataURI(ref))
		}
	}
	if len(imageURLs) > 4 {
		imageURLs = imageURLs[:4]
	}
	body := map[string]any{
		"image_urls":     imageURLs,
		"should_texture": true,
		"enable_pbr":     providerOptionBool(prepared, "meshy_enable_pbr", false),
	}
	if strings.TrimSpace(req.Prompt) != "" {
		body["texture_prompt"] = strings.TrimSpace(req.Prompt)
	}
	addMeshyCommonOptions(body, prepared, req)
	jobID, raw, err := submitMeshyTask(ctx, prepared, body, "Meshy multi-image-to-3D")
	if err != nil {
		return MeshResult{RawBody: raw}, err
	}
	result, err := pollMeshResult(ctx, prepared, req, jobID, raw, meshAdapterDefaults{
		pollPathTemplate: "/openapi/v1/multi-image-to-3d/{job_id}",
		statusPath:       "status",
		successValue:     "succeeded",
		errorPath:        "task_error.message",
		resultURLPath:    "model_urls.glb",
		previewURLPath:   "thumbnail_url",
	})
	if result.TaskType == "" {
		result.TaskType = "multi-image-to-3d"
	}
	return result, err
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
		taskType := strings.TrimSpace(asString(lookupPath(payload, "type")))
		if successValue != "" && remoteStatus == successValue {
			result, err := fetchMeshArtifacts(ctx, model, jobID, remoteStatus, payload, providerOptionString(model, "mesh_result_url_path", defs.resultURLPath), providerOptionString(model, "mesh_preview_url_path", defs.previewURLPath), lastBody)
			result.TaskType = taskType
			return result, err
		}
		for _, failed := range failedValues {
			failed = strings.TrimSpace(failed)
			if failed != "" && remoteStatus == failed {
				errText := strings.TrimSpace(asString(lookupPath(payload, errorPath)))
				if errText == "" {
					errText = strings.TrimSpace(asString(lookupPath(payload, "task_error.message")))
				}
				if errText == "" {
					errText = strings.TrimSpace(asString(lookupPath(payload, "message")))
				}
				if errText == "" {
					errText = remoteStatus
				}
				return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, TaskType: taskType, RawBody: lastBody, Error: errText, Status: "failed"}, errors.New(errText)
			}
		}
		if strings.TrimSpace(asString(lookupPath(payload, providerOptionString(model, "mesh_result_url_path", defs.resultURLPath)))) != "" {
			result, err := fetchMeshArtifacts(ctx, model, jobID, remoteStatus, payload, providerOptionString(model, "mesh_result_url_path", defs.resultURLPath), providerOptionString(model, "mesh_preview_url_path", defs.previewURLPath), lastBody)
			result.TaskType = taskType
			return result, err
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
