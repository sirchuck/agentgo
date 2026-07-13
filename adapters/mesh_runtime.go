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
	"os"
	"path/filepath"
	"strconv"
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
	SourceModel         *MeshBinary
	ReferenceImages     []MeshBinary
	NamedImages         map[string]MeshBinary
	Settings            map[string]any
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
	case "fal_mesh":
		return executeFalMesh(ctx, model, req)
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
	qualitySetting := meshRequestSettingString(req, "tripo_quality")
	if qualitySetting == "" {
		qualitySetting = req.Quality
	}
	if q := tripoQualityValue(qualitySetting); q != "" {
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
		artifact, err := downloadMeshArtifact(ctx, model, urlValue, field.kind, field.name, "")
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
		previewArtifact, err := downloadMeshArtifact(ctx, model, previewURL, "preview", "preview", "")
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

func executeFalMesh(ctx context.Context, model ModelConfig, req MeshRequest) (MeshResult, error) {
	prepared := model
	prepared.AuthType = normalizedAuthType(prepared.AuthType, "fal_key")
	prepared.APIKey = resolveConfiguredAPIKey(prepared)
	if strings.TrimSpace(prepared.APIKey) == "" {
		prepared.APIKey = strings.TrimSpace(os.Getenv("FAL_KEY"))
	}
	if strings.TrimSpace(prepared.APIKey) == "" {
		prepared.APIKey = strings.TrimSpace(os.Getenv("FAL_API_KEY"))
	}
	if strings.TrimSpace(prepared.APIKey) == "" {
		return MeshResult{}, errors.New("missing API key for fal.ai mesh model")
	}
	if strings.TrimSpace(prepared.BaseURL) == "" {
		prepared.BaseURL = "https://queue.fal.run"
	}
	body := map[string]any{}
	taskType := "text-to-3d"
	falFamily := falMeshFamily(prepared)
	if falMeshIsMeshyRetexture(prepared) {
		if req.SourceModel == nil || len(req.SourceModel.Data) == 0 {
			return MeshResult{}, errors.New("fal Meshy Retexture requires a source 3D model file")
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" && (req.InputImage == nil || len(req.InputImage.Data) == 0) {
			return MeshResult{}, errors.New("fal Meshy Retexture requires either a style prompt or a style reference image")
		}
		taskType = "retexture"
		body["model_url"] = falBinaryDataURI(*req.SourceModel, defaultMeshMIME(req.SourceModel.Name, req.SourceModel.MIMEType), "application/octet-stream")
		if req.InputImage != nil && len(req.InputImage.Data) > 0 {
			body["image_style_url"] = falBinaryDataURI(*req.InputImage, defaultMeshMIME(req.InputImage.Name, req.InputImage.MIMEType), "image/png")
		} else {
			body["text_style_prompt"] = prompt
		}
	} else if falMeshIsTrellis2Retexture(prepared) {
		if req.SourceModel == nil || len(req.SourceModel.Data) == 0 {
			return MeshResult{}, errors.New("fal Trellis 2 Retexture requires a source 3D model file")
		}
		if req.InputImage == nil || len(req.InputImage.Data) == 0 {
			return MeshResult{}, errors.New("fal Trellis 2 Retexture requires a style reference image")
		}
		taskType = "retexture"
		body["mesh_url"] = falBinaryDataURI(*req.SourceModel, defaultMeshMIME(req.SourceModel.Name, req.SourceModel.MIMEType), "application/octet-stream")
		body["image_url"] = falImageDataURI(*req.InputImage)
	} else if falFamily == "trellis" || falFamily == "trellis2" {
		imageURLs := falMeshInputImageURLs(req)
		if len(imageURLs) == 0 {
			if falFamily == "trellis2" {
				return MeshResult{}, errors.New("fal Trellis 2 requires at least one input image")
			}
			return MeshResult{}, errors.New("fal Trellis requires at least one input image")
		}
		if len(imageURLs) == 1 {
			taskType = "image-to-3d"
			body["image_url"] = imageURLs[0]
		} else {
			taskType = "multi-image-to-3d"
			body["image_urls"] = imageURLs
		}
	} else if falMeshIsHunyuanV2(prepared) {
		if req.InputImage == nil || len(req.InputImage.Data) == 0 {
			return MeshResult{}, errors.New("fal Hunyuan 3D V2 requires a primary input image")
		}
		taskType = "image-to-3d"
		body["input_image_url"] = falImageDataURI(*req.InputImage)
	} else if req.InputImage != nil && len(req.InputImage.Data) > 0 {
		taskType = "image-to-3d"
		body["input_image_url"] = falImageDataURI(*req.InputImage)
		for idx, ref := range req.ReferenceImages {
			if len(ref.Data) == 0 {
				continue
			}
			switch idx {
			case 0:
				body["back_image_url"] = falImageDataURI(ref)
			case 1:
				body["left_image_url"] = falImageDataURI(ref)
			case 2:
				body["right_image_url"] = falImageDataURI(ref)
			}
		}
	} else {
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			return MeshResult{}, errors.New("prompt is required for fal Hunyuan text-to-3D jobs")
		}
		body["prompt"] = prompt
	}
	route := falMeshRoute(prepared, taskType)
	addFalNamedImageInputs(body, prepared, req, route, taskType)
	addFalMeshOptions(body, prepared, req, route)
	endpoint := falQueueEndpoint(prepared, route)
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
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("fal mesh submit returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return MeshResult{RawBody: string(respBody)}, fmt.Errorf("fal mesh submit response was not valid JSON")
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, "request_id")))
	if jobID == "" {
		return MeshResult{RawBody: string(respBody)}, errors.New("fal mesh submit response did not include a request_id")
	}
	statusURL := strings.TrimSpace(asString(lookupPath(submitPayload, "status_url")))
	responseURL := strings.TrimSpace(asString(lookupPath(submitPayload, "response_url")))
	return pollFalMeshResult(ctx, prepared, req, jobID, route, statusURL, responseURL, taskType, string(respBody))
}

func falBinaryDataURI(file MeshBinary, detectedMIME, fallbackMIME string) string {
	mimeType := strings.TrimSpace(detectedMIME)
	if mimeType == "" {
		mimeType = strings.TrimSpace(file.MIMEType)
	}
	if mimeType == "" {
		mimeType = fallbackMIME
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(file.Data)
}

func falImageDataURI(image MeshBinary) string {
	return falBinaryDataURI(image, defaultMeshMIME(image.Name, image.MIMEType), "image/png")
}

func falMeshInputImageURLs(req MeshRequest) []string {
	imageURLs := []string{}
	if req.InputImage != nil && len(req.InputImage.Data) > 0 {
		imageURLs = append(imageURLs, falImageDataURI(*req.InputImage))
	}
	for _, ref := range req.ReferenceImages {
		if len(ref.Data) == 0 {
			continue
		}
		imageURLs = append(imageURLs, falImageDataURI(ref))
	}
	return imageURLs
}

func falMeshRouteValue(model ModelConfig) string {
	route := strings.TrimSpace(model.ModelName)
	if route == "" {
		route = strings.TrimSpace(model.APIPath)
	}
	route = strings.TrimSpace(strings.TrimPrefix(route, strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")))
	return strings.Trim(route, "/")
}

func falMeshRouteContains(model ModelConfig, needle string) bool {
	return strings.Contains(strings.ToLower(falMeshRouteValue(model)), strings.ToLower(needle))
}

func falMeshIsMeshyRetexture(model ModelConfig) bool {
	return falMeshRouteContains(model, "meshy/v5/retexture")
}

func falMeshIsTrellis2Retexture(model ModelConfig) bool {
	return falMeshRouteContains(model, "trellis-2/retexture")
}

func falMeshIsHunyuanV2(model ModelConfig) bool {
	route := strings.ToLower(falMeshRouteValue(model))
	return strings.Contains(route, "hunyuan3d/v2") || strings.Contains(route, "hunyuan-3d/v2")
}

func falMeshIsHunyuanRapid(model ModelConfig, route string) bool {
	clean := strings.ToLower(strings.TrimSpace(route))
	if clean == "" {
		clean = strings.ToLower(falMeshRouteValue(model))
	}
	return strings.Contains(clean, "/rapid/") || strings.Contains(clean, "v3.1/rapid")
}

func falMeshIsHunyuanPro(model ModelConfig, route string) bool {
	clean := strings.ToLower(strings.TrimSpace(route))
	if clean == "" {
		clean = strings.ToLower(falMeshRouteValue(model))
	}
	return strings.Contains(clean, "/pro/") || strings.Contains(clean, "v3.1/pro")
}

func falMeshFamily(model ModelConfig) string {
	route := strings.ToLower(falMeshRouteValue(model))
	if strings.Contains(route, "trellis-2/retexture") {
		return "trellis2_retexture"
	}
	if strings.Contains(route, "meshy/v5/retexture") {
		return "meshy_retexture"
	}
	if strings.Contains(route, "trellis-2") {
		return "trellis2"
	}
	if strings.Contains(route, "trellis") {
		return "trellis"
	}
	return "hunyuan"
}

func falMeshRoute(model ModelConfig, taskType string) string {
	route := falMeshRouteValue(model)
	if route == "" {
		route = "fal-ai/hunyuan3d-v3/text-to-3d"
	}
	family := falMeshFamily(model)
	if family == "trellis" {
		if taskType == "multi-image-to-3d" {
			return "fal-ai/trellis/multi"
		}
		return "fal-ai/trellis"
	}
	if family == "trellis2" {
		return "fal-ai/trellis-2"
	}
	if family == "trellis2_retexture" {
		return "fal-ai/trellis-2/retexture"
	}
	if family == "meshy_retexture" {
		return "fal-ai/meshy/v5/retexture"
	}
	if falMeshIsHunyuanV2(model) {
		return route
	}
	if taskType == "image-to-3d" {
		if strings.Contains(route, "/text-to-3d") {
			route = strings.Replace(route, "/text-to-3d", "/image-to-3d", 1)
		} else if !strings.Contains(route, "/image-to-3d") {
			route = "fal-ai/hunyuan3d-v3/image-to-3d"
		}
	} else {
		if strings.Contains(route, "/image-to-3d") {
			route = strings.Replace(route, "/image-to-3d", "/text-to-3d", 1)
		} else if !strings.Contains(route, "/text-to-3d") {
			route = "fal-ai/hunyuan3d-v3/text-to-3d"
		}
	}
	return route
}

func falQueueEndpoint(model ModelConfig, route string) string {
	base := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
	if base == "" {
		base = "https://queue.fal.run"
	}
	return base + "/" + strings.Trim(route, "/")
}

func addFalNamedImageInputs(body map[string]any, model ModelConfig, req MeshRequest, route, taskType string) {
	if taskType != "image-to-3d" || !falMeshIsHunyuanPro(model, route) || len(req.NamedImages) == 0 {
		return
	}
	for _, field := range []struct {
		key  string
		name string
	}{
		{key: "top", name: "top_image_url"},
		{key: "bottom", name: "bottom_image_url"},
		{key: "left_front", name: "left_front_image_url"},
		{key: "right_front", name: "right_front_image_url"},
	} {
		image, ok := req.NamedImages[field.key]
		if !ok || len(image.Data) == 0 {
			continue
		}
		body[field.name] = falImageDataURI(image)
	}
}

func addFalMeshOptions(body map[string]any, model ModelConfig, req MeshRequest, route string) {
	family := falMeshFamily(model)
	if family == "meshy_retexture" {
		if meshRequestSettingBool(req, "fal_enable_pbr") || providerOptionBool(model, "fal_enable_pbr", false) {
			body["enable_pbr"] = true
		}
		if enableOriginalUV, ok := meshRequestSettingOptionalBool(req, "fal_enable_original_uv"); ok {
			body["enable_original_uv"] = enableOriginalUV
		} else if value, ok := providerOptionOptionalBool(model, "fal_enable_original_uv"); ok {
			body["enable_original_uv"] = value
		}
		if safetyChecker, ok := meshRequestSettingOptionalBool(req, "fal_enable_safety_checker"); ok {
			body["enable_safety_checker"] = safetyChecker
		} else if value, ok := providerOptionOptionalBool(model, "fal_enable_safety_checker"); ok {
			body["enable_safety_checker"] = value
		}
		if seed := strings.TrimSpace(providerOptionString(model, "fal_seed", "")); seed != "" {
			body["seed"] = seed
		}
		for k, v := range providerOptionMap(model, "mesh_submit_fields") {
			body[k] = v
		}
		return
	}
	if family == "trellis2_retexture" {
		if resolution, ok := meshRequestSettingInt(req, "fal_resolution"); ok {
			body["resolution"] = resolution
		}
		if size, ok := meshRequestSettingInt(req, "fal_texture_size"); ok {
			body["texture_size"] = size
		}
		for _, field := range []struct {
			setting string
			bodyKey string
		}{
			{setting: "fal_tex_slat_guidance_strength", bodyKey: "tex_slat_guidance_strength"},
			{setting: "fal_tex_slat_guidance_rescale", bodyKey: "tex_slat_guidance_rescale"},
			{setting: "fal_tex_slat_guidance_interval_start", bodyKey: "tex_slat_guidance_interval_start"},
			{setting: "fal_tex_slat_guidance_interval_end", bodyKey: "tex_slat_guidance_interval_end"},
			{setting: "fal_tex_slat_rescale_t", bodyKey: "tex_slat_rescale_t"},
		} {
			if value, ok := meshRequestSettingFloat(req, field.setting); ok {
				body[field.bodyKey] = value
			}
		}
		if steps, ok := meshRequestSettingInt(req, "fal_tex_slat_sampling_steps"); ok {
			body["tex_slat_sampling_steps"] = steps
		}
		if seed, ok := meshRequestSettingInt(req, "fal_seed"); ok {
			body["seed"] = seed
		} else if seedText := strings.TrimSpace(providerOptionString(model, "fal_seed", "")); seedText != "" {
			body["seed"] = seedText
		}
		for k, v := range providerOptionMap(model, "mesh_submit_fields") {
			body[k] = v
		}
		return
	}
	if family == "trellis" || family == "trellis2" {
		if size, ok := meshRequestSettingInt(req, "fal_texture_size"); ok {
			body["texture_size"] = size
		}
		if family == "trellis2" {
			if resolution, ok := meshRequestSettingInt(req, "fal_resolution"); ok {
				body["resolution"] = resolution
			}
		}
		for k, v := range providerOptionMap(model, "mesh_submit_fields") {
			body[k] = v
		}
		return
	}
	quality := strings.ToLower(strings.TrimSpace(req.Quality))
	if falMeshIsHunyuanV2(model) {
		if meshRequestSettingBool(req, "fal_textured_mesh") || providerOptionBool(model, "fal_textured_mesh", false) {
			body["textured_mesh"] = true
		}
		if value := strings.TrimSpace(providerOptionString(model, "fal_octree_resolution", "")); value != "" {
			body["octree_resolution"] = value
		}
		if value := strings.TrimSpace(providerOptionString(model, "fal_num_inference_steps", "")); value != "" {
			body["num_inference_steps"] = value
		}
		if value := strings.TrimSpace(providerOptionString(model, "fal_guidance_scale", "")); value != "" {
			body["guidance_scale"] = value
		}
	} else if falMeshIsHunyuanRapid(model, route) {
		geometry := meshRequestSettingBool(req, "fal_enable_geometry") || providerOptionBool(model, "fal_enable_geometry", false)
		pbr := meshRequestSettingBool(req, "fal_enable_pbr") || providerOptionBool(model, "fal_enable_pbr", false)
		switch quality {
		case "geometry", "white", "white-model", "white_model":
			geometry = true
		case "pbr", "normal+pbr", "standard+pbr", "high", "detailed":
			pbr = true
		}
		if geometry {
			body["enable_geometry"] = true
		} else if pbr {
			body["enable_pbr"] = true
		}
	} else {
		switch quality {
		case "normal", "standard", "default":
			body["generate_type"] = "Normal"
		case "low", "lowpoly", "low-poly", "low_poly":
			if !falMeshIsHunyuanPro(model, route) {
				body["generate_type"] = "LowPoly"
			}
		case "geometry", "white", "white-model", "white_model":
			body["generate_type"] = "Geometry"
		case "pbr", "normal+pbr", "standard+pbr", "high", "detailed":
			body["generate_type"] = "Normal"
			body["enable_pbr"] = true
		}
		if providerOptionBool(model, "fal_enable_pbr", false) || meshRequestSettingBool(req, "fal_enable_pbr") {
			body["enable_pbr"] = true
		}
		if generateType := strings.TrimSpace(meshRequestSettingString(req, "fal_generate_type")); generateType != "" {
			body["generate_type"] = generateType
		} else if generateType := strings.TrimSpace(providerOptionString(model, "fal_generate_type", "")); generateType != "" {
			body["generate_type"] = generateType
		}
		if polygonType := strings.TrimSpace(providerOptionString(model, "fal_polygon_type", "")); polygonType != "" && !falMeshIsHunyuanPro(model, route) && !falMeshIsHunyuanRapid(model, route) {
			body["polygon_type"] = polygonType
		}
		if faceCount, ok := meshRequestSettingInt(req, "fal_face_count"); ok {
			body["face_count"] = faceCount
		} else if faceCount := strings.TrimSpace(providerOptionString(model, "fal_face_count", "")); faceCount != "" {
			body["face_count"] = faceCount
		}
	}
	if seed := strings.TrimSpace(providerOptionString(model, "fal_seed", "")); seed != "" {
		body["seed"] = seed
	}
	for k, v := range providerOptionMap(model, "mesh_submit_fields") {
		body[k] = v
	}
}

func meshRequestSettingValue(req MeshRequest, key string) (any, bool) {
	if len(req.Settings) == 0 {
		return nil, false
	}
	value, ok := req.Settings[key]
	return value, ok
}

func meshRequestSettingOptionalBool(req MeshRequest, key string) (bool, bool) {
	value, ok := meshRequestSettingValue(req, key)
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	}
	return false, false
}

func meshRequestSettingBool(req MeshRequest, key string) bool {
	value, ok := meshRequestSettingOptionalBool(req, key)
	return ok && value
}

func meshRequestSettingString(req MeshRequest, key string) string {
	value, ok := meshRequestSettingValue(req, key)
	if !ok {
		return ""
	}
	return strings.TrimSpace(asString(value))
}

func meshRequestSettingFloat(req MeshRequest, key string) (float64, bool) {
	value, ok := meshRequestSettingValue(req, key)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		if n, err := typed.Float64(); err == nil {
			return n, true
		}
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0, false
		}
		if n, err := strconv.ParseFloat(text, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func meshRequestSettingInt(req MeshRequest, key string) (int, bool) {
	value, ok := meshRequestSettingValue(req, key)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return int(n), true
		}
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0, false
		}
		var n int
		if _, err := fmt.Sscanf(text, "%d", &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

func pollFalMeshResult(ctx context.Context, model ModelConfig, req MeshRequest, jobID, route, statusURL, responseURL, taskType, raw string) (MeshResult, error) {
	pollEvery := req.PollInterval
	if pollEvery <= 0 {
		pollEvery = 8 * time.Second
	}
	if statusURL == "" {
		statusURL = falQueueEndpoint(model, route) + "/requests/" + jobID + "/status"
	}
	if responseURL == "" {
		responseURL = falQueueEndpoint(model, route) + "/requests/" + jobID + "/response"
	}
	lastBody := raw
	remoteStatus := ""
	for {
		if ctx.Err() != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody, Status: "stopped", TaskType: taskType}, ctx.Err()
		}
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
		if err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody, TaskType: taskType}, err
		}
		applyModelHeaders(pollReq, model)
		pollBody, pollStatus, pollCode, err := doAdapterRequest(pollReq, modelTimeout(model, 20*time.Minute))
		if err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody, TaskType: taskType}, err
		}
		lastBody = string(pollBody)
		if pollCode >= 300 {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody, TaskType: taskType}, fmt.Errorf("fal mesh polling returned %s: %s", pollStatus, strings.TrimSpace(lastBody))
		}
		var payload any
		if err := json.Unmarshal(pollBody, &payload); err != nil {
			return MeshResult{ProviderJobID: jobID, RawBody: lastBody, TaskType: taskType}, fmt.Errorf("fal mesh poll response was not valid JSON")
		}
		remoteStatus = strings.ToLower(strings.TrimSpace(asString(lookupPath(payload, "status"))))
		switch remoteStatus {
		case "completed", "success", "succeeded":
			if errText := falStatusError(payload); errText != "" {
				return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody, Error: errText, Status: "failed", TaskType: taskType}, errors.New(errText)
			}
			return fetchFalMeshArtifacts(ctx, model, jobID, remoteStatus, responseURL, taskType, lastBody)
		case "failed", "error", "cancelled", "canceled":
			errText := falStatusError(payload)
			if errText == "" {
				errText = remoteStatus
			}
			return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody, Error: errText, Status: "failed", TaskType: taskType}, errors.New(errText)
		}
		time.Sleep(pollEvery)
	}
}

func falStatusError(payload any) string {
	for _, path := range []string{"error", "error.message", "error.detail", "error_type", "detail", "message"} {
		if text := strings.TrimSpace(asString(lookupPath(payload, path))); text != "" {
			return text
		}
	}
	return ""
}

func fetchFalMeshArtifacts(ctx context.Context, model ModelConfig, jobID, remoteStatus, responseURL, taskType, raw string) (MeshResult, error) {
	resultReq, err := http.NewRequestWithContext(ctx, http.MethodGet, responseURL, nil)
	if err != nil {
		return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, TaskType: taskType}, err
	}
	applyModelHeaders(resultReq, model)
	body, status, code, err := doAdapterRequest(resultReq, modelTimeout(model, 20*time.Minute))
	if err != nil {
		return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, TaskType: taskType}, err
	}
	resultRaw := string(body)
	if code >= 300 {
		return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: resultRaw, TaskType: taskType}, fmt.Errorf("fal mesh result returned %s: %s", status, strings.TrimSpace(resultRaw))
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: resultRaw, TaskType: taskType}, fmt.Errorf("fal mesh result response was not valid JSON")
	}
	if errText := falStatusError(payload); errText != "" {
		return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: resultRaw, Error: errText, Status: "failed", TaskType: taskType}, errors.New(errText)
	}
	resultPath := "model_glb.url"
	previewPath := "thumbnail.url"
	switch falMeshFamily(model) {
	case "trellis":
		resultPath = "model_mesh.url"
	case "trellis2":
		resultPath = "model_glb.url"
		previewPath = ""
	default:
		if falMeshIsHunyuanV2(model) {
			resultPath = "model_mesh.url"
		}
	}
	result, err := fetchMeshArtifacts(ctx, model, jobID, remoteStatus, payload, resultPath, previewPath, resultRaw)
	if result.TaskType == "" {
		result.TaskType = taskType
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

type meshArtifactCandidate struct {
	URL          string
	Kind         string
	FallbackName string
	ProviderName string
	Path         string
}

func fetchMeshArtifacts(ctx context.Context, model ModelConfig, jobID, remoteStatus string, pollPayload any, resultURLPath, previewURLPath, raw string) (MeshResult, error) {
	candidates := []meshArtifactCandidate{}
	appendMeshArtifactCandidate(&candidates, lookupPath(pollPayload, resultURLPath), resultURLPath, "primary", "model")
	appendMeshArtifactCandidate(&candidates, lookupPath(pollPayload, previewURLPath), previewURLPath, "preview", "preview")
	for _, path := range []string{
		"data.model_mesh.url", "model_mesh.url",
		"data.model_glb.url", "model_glb.url",
		"data.model_obj.url", "model_obj.url",
		"data.model_urls.glb.url", "model_urls.glb.url",
		"data.model_urls.obj.url", "model_urls.obj.url",
		"data.model_urls.fbx.url", "model_urls.fbx.url",
		"data.model_urls.usdz.url", "model_urls.usdz.url",
		"data.model_urls.stl.url", "model_urls.stl.url",
		"data.material_mtl.url", "material_mtl.url",
		"data.texture.url", "texture.url",
		"data.thumbnail.url", "thumbnail.url",
		"data.thumbnail_url", "thumbnail_url",
		"data.preview_url", "preview_url",
	} {
		kind, fallback := meshArtifactKindAndFallback(path, "")
		if path == resultURLPath {
			kind, fallback = "primary", "model"
		}
		if path == previewURLPath {
			kind, fallback = "preview", "preview"
		}
		appendMeshArtifactCandidate(&candidates, lookupPath(pollPayload, path), path, kind, fallback)
	}
	collectMeshArtifactCandidates(pollPayload, "", &candidates)
	candidates = dedupeMeshArtifactCandidates(candidates)
	primaryIndex := firstPrimaryMeshArtifactCandidate(candidates)
	if primaryIndex < 0 {
		return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, Status: "completed"}, errors.New("mesh job completed but no model URL was returned")
	}
	if primaryIndex > 0 {
		candidates[0], candidates[primaryIndex] = candidates[primaryIndex], candidates[0]
	}

	result := MeshResult{
		Status:        "completed",
		ProviderJobID: jobID,
		RemoteStatus:  remoteStatus,
		Artifacts:     []MeshArtifact{},
		RawBody:       raw,
	}
	for idx, candidate := range candidates {
		kind := candidate.Kind
		if idx == 0 {
			kind = "primary"
		} else if kind == "primary" {
			kind = "model"
		}
		artifact, err := downloadMeshArtifact(ctx, model, candidate.URL, kind, candidate.FallbackName, candidate.ProviderName)
		if err != nil {
			if idx == 0 {
				return MeshResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw}, err
			}
			continue
		}
		artifact.Kind = kind
		result.Artifacts = append(result.Artifacts, artifact)
		if idx == 0 {
			result.PrimaryData = artifact.Data
			result.PrimaryMIMEType = artifact.MIMEType
			result.PrimaryFilename = artifact.Name
		}
		if kind == "preview" && len(result.PreviewData) == 0 {
			result.PreviewData = artifact.Data
			result.PreviewMIMEType = artifact.MIMEType
			result.PreviewFilename = artifact.Name
		}
	}
	return result, nil
}

func appendMeshArtifactCandidate(candidates *[]meshArtifactCandidate, value any, path, kind, fallbackName string) {
	urlValue, nameHint := meshArtifactURLAndName(value)
	if urlValue == "" || !isLikelyMeshArtifactURL(path, urlValue) {
		return
	}
	providerName := sanitizeMeshFilename(nameHint)
	if providerName == "artifact.bin" && strings.TrimSpace(nameHint) == "" {
		providerName = ""
	}
	if kind == "" || fallbackName == "" {
		inferredKind, inferredFallback := meshArtifactKindAndFallback(path, urlValue)
		if kind == "" {
			kind = inferredKind
		}
		if fallbackName == "" {
			fallbackName = inferredFallback
		}
	}
	if fallbackName == "" && providerName != "" {
		fallbackName = strings.TrimSuffix(providerName, filepath.Ext(providerName))
	}
	if fallbackName == "" {
		fallbackName = "artifact"
	}
	*candidates = append(*candidates, meshArtifactCandidate{URL: urlValue, Kind: kind, FallbackName: fallbackName, ProviderName: providerName, Path: path})
}

func collectMeshArtifactCandidates(value any, path string, candidates *[]meshArtifactCandidate) {
	switch typed := value.(type) {
	case map[string]any:
		appendMeshArtifactCandidate(candidates, typed, path, "", "")
		for key, child := range typed {
			childPath := strings.TrimSpace(key)
			if path != "" {
				childPath = path + "." + childPath
			}
			if strings.EqualFold(strings.TrimSpace(key), "url") {
				continue
			}
			collectMeshArtifactCandidates(child, childPath, candidates)
		}
	case []any:
		for idx, child := range typed {
			childPath := fmt.Sprintf("%s.%d", path, idx)
			if path == "" {
				childPath = fmt.Sprintf("%d", idx)
			}
			collectMeshArtifactCandidates(child, childPath, candidates)
		}
	case string:
		appendMeshArtifactCandidate(candidates, typed, path, "", "")
	}
}

func meshArtifactURLAndName(value any) (string, string) {
	switch typed := value.(type) {
	case string:
		urlValue := strings.TrimSpace(typed)
		if strings.HasPrefix(strings.ToLower(urlValue), "http://") || strings.HasPrefix(strings.ToLower(urlValue), "https://") {
			return urlValue, ""
		}
	case map[string]any:
		for _, key := range []string{"url", "download_url", "file_url"} {
			urlValue := strings.TrimSpace(asString(typed[key]))
			if strings.HasPrefix(strings.ToLower(urlValue), "http://") || strings.HasPrefix(strings.ToLower(urlValue), "https://") {
				for _, nameKey := range []string{"file_name", "filename", "name"} {
					if name := strings.TrimSpace(asString(typed[nameKey])); name != "" {
						return urlValue, name
					}
				}
				return urlValue, ""
			}
		}
	}
	return "", ""
}

func isLikelyMeshArtifactURL(path, urlValue string) bool {
	lowerPath := strings.ToLower(strings.TrimSpace(path))
	lowerURL := strings.ToLower(strings.TrimSpace(urlValue))
	if lowerURL == "" || !(strings.HasPrefix(lowerURL, "http://") || strings.HasPrefix(lowerURL, "https://")) {
		return false
	}
	for _, bad := range []string{"status_url", "response_url", "cancel_url", "queue_url", "webhook", "request_id", "logs"} {
		if strings.Contains(lowerPath, bad) {
			return false
		}
	}
	for _, token := range []string{"model", "mesh", "glb", "gltf", "obj", "mtl", "texture", "material", "thumbnail", "preview", "image", "fbx", "usdz", "stl", "ply", "zip", "artifact", "file"} {
		if strings.Contains(lowerPath, token) || strings.Contains(lowerURL, "."+token) {
			return true
		}
	}
	return false
}

func meshArtifactKindAndFallback(path, urlValue string) (string, string) {
	lower := strings.ToLower(strings.TrimSpace(path + " " + urlValue))
	switch {
	case strings.Contains(lower, "thumbnail") || strings.Contains(lower, "preview"):
		return "preview", "preview"
	case strings.Contains(lower, "material_mtl") || strings.Contains(lower, ".mtl") || strings.Contains(lower, "model_urls.mtl"):
		return "material", "material"
	case strings.Contains(lower, "texture") || strings.Contains(lower, "albedo") || strings.Contains(lower, "normal") || strings.Contains(lower, "roughness") || strings.Contains(lower, "metallic"):
		return "texture", "texture"
	case strings.Contains(lower, "model") || strings.Contains(lower, "mesh") || strings.Contains(lower, ".glb") || strings.Contains(lower, ".gltf") || strings.Contains(lower, ".obj") || strings.Contains(lower, ".fbx") || strings.Contains(lower, ".usdz") || strings.Contains(lower, ".stl") || strings.Contains(lower, ".ply"):
		return "primary", "model"
	default:
		return "artifact", "artifact"
	}
}

func dedupeMeshArtifactCandidates(candidates []meshArtifactCandidate) []meshArtifactCandidate {
	indexByURL := map[string]int{}
	out := make([]meshArtifactCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate.URL))
		if key == "" {
			continue
		}
		if existingIdx, ok := indexByURL[key]; ok {
			existing := &out[existingIdx]
			// Explicit provider filenames are important for OBJ bundles because
			// OBJ/MTL files reference companion files by exact filename.
			if existing.ProviderName == "" && strings.TrimSpace(candidate.ProviderName) != "" {
				existing.ProviderName = candidate.ProviderName
			}
			if existing.Kind == "artifact" && candidate.Kind != "" && candidate.Kind != "artifact" {
				existing.Kind = candidate.Kind
			}
			if existing.FallbackName == "artifact" && strings.TrimSpace(candidate.FallbackName) != "" {
				existing.FallbackName = candidate.FallbackName
			}
			continue
		}
		indexByURL[key] = len(out)
		out = append(out, candidate)
	}
	return out
}

func firstPrimaryMeshArtifactCandidate(candidates []meshArtifactCandidate) int {
	for idx, candidate := range candidates {
		if candidate.Kind == "primary" {
			return idx
		}
	}
	for idx, candidate := range candidates {
		if candidate.Kind != "preview" {
			return idx
		}
	}
	return -1
}

func downloadMeshArtifact(ctx context.Context, model ModelConfig, urlValue, kind, fallbackName, providerName string) (MeshArtifact, error) {
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
	name := strings.TrimSpace(providerName)
	if name == "" {
		name = meshArtifactFallbackFilename(urlValue, kind, fallbackName, mimeType)
	}
	return MeshArtifact{Name: sanitizeMeshFilename(name), MIMEType: mimeType, Data: body, Kind: kind}, nil
}

func meshArtifactFallbackFilename(urlValue, kind, fallbackName, mimeType string) string {
	parsed := strings.TrimSpace(filepath.Base(strings.Split(urlValue, "?")[0]))
	if parsed == "." || parsed == "/" {
		parsed = ""
	}
	urlExt := strings.ToLower(strings.TrimSpace(filepath.Ext(parsed)))
	if urlExt == "" {
		urlExt = extensionForMIME(mimeType)
	}
	base := strings.TrimSpace(fallbackName)
	if base == "" {
		base = strings.TrimSuffix(parsed, filepath.Ext(parsed))
	}
	if base == "" {
		base = "artifact"
	}
	// OBJ bundles rely on exact companion filenames. CDN object names often
	// include random prefixes even when the provider's OBJ/MTL contents refer to
	// stable names like material.mtl or material.png, so keep logical filenames
	// for linked bundle parts when no explicit provider filename was supplied.
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "material", "texture":
		return base + urlExt
	}
	if parsed != "" {
		return parsed
	}
	return base + urlExt
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
