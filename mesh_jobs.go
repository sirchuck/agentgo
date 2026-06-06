package main

import (
	"agentgo/adapters"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type meshArtifactRecord struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Kind    string `json:"kind,omitempty"`
	MIME    string `json:"mime,omitempty"`
	BlobURL string `json:"blobUrl,omitempty"`
}

type meshJobRecord struct {
	JobID                  string               `json:"jobId"`
	ProjectName            string               `json:"projectName"`
	ModelID                string               `json:"modelId"`
	ModelLabel             string               `json:"modelLabel"`
	Provider               string               `json:"provider"`
	Adapter                string               `json:"adapter"`
	ModelName              string               `json:"modelName"`
	Status                 string               `json:"status"`
	ProviderJobID          string               `json:"providerJobId,omitempty"`
	RemoteStatus           string               `json:"remoteStatus,omitempty"`
	ProviderTaskType       string               `json:"providerTaskType,omitempty"`
	MeshMode               string               `json:"meshMode,omitempty"`
	RefinedFromJobID       string               `json:"refinedFromJobId,omitempty"`
	RefinedFromProviderID  string               `json:"refinedFromProviderId,omitempty"`
	RefinedJobID           string               `json:"refinedJobId,omitempty"`
	Prompt                 string               `json:"prompt"`
	PromptSource           string               `json:"promptSource,omitempty"`
	Quality                string               `json:"quality,omitempty"`
	OutputFormat           string               `json:"outputFormat,omitempty"`
	MeshSettings           map[string]any       `json:"meshSettings,omitempty"`
	InputImagePath         string               `json:"inputImagePath,omitempty"`
	ReferenceImagePaths    []string             `json:"referenceImagePaths,omitempty"`
	NamedImagePaths        map[string]string    `json:"namedImagePaths,omitempty"`
	PrimaryModelPath       string               `json:"primaryModelPath,omitempty"`
	PrimaryProjectworkPath string               `json:"primaryProjectworkPath,omitempty"`
	PreviewImagePath       string               `json:"previewImagePath,omitempty"`
	ArtifactBundleRoot     string               `json:"artifactBundleRoot,omitempty"`
	ProjectworkBundleRoot  string               `json:"projectworkBundleRoot,omitempty"`
	MetadataPath           string               `json:"metadataPath,omitempty"`
	SourceContextFiles     []string             `json:"sourceContextFiles,omitempty"`
	Artifacts              []meshArtifactRecord `json:"artifacts,omitempty"`
	PromotionState         string               `json:"promotionState,omitempty"`
	Error                  string               `json:"error,omitempty"`
	CreatedAt              string               `json:"createdAt"`
	UpdatedAt              string               `json:"updatedAt"`
	CompletedAt            string               `json:"completedAt,omitempty"`
	PromotedAt             string               `json:"promotedAt,omitempty"`
}

type meshJobCreateResponse struct {
	OK  bool          `json:"ok"`
	Job meshJobRecord `json:"job"`
}

type stagedMeshInput struct {
	Path     string
	Name     string
	MIMEType string
	Data     []byte
}

func normalizedMeshAdapterName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "meshy_mesh":
		return "meshy_mesh"
	case "tripo_mesh":
		return "tripo_mesh"
	case "hyper3d_mesh":
		return "hyper3d_mesh"
	case "fal_mesh":
		return "fal_mesh"
	default:
		return ""
	}
}

func modelIsMeshGeneration(model ModelConfig) bool {
	if model.MeshGeneration {
		return true
	}
	return normalizedMeshAdapterName(model.Adapter) != ""
}

func modelSupportsMeshPromptOnly(model ModelConfig) bool {
	if model.MeshGeneration {
		return model.MeshPromptOnly
	}
	return true
}

func modelSupportsMeshImageInput(model ModelConfig) bool {
	if model.MeshGeneration {
		return model.MeshImageInput
	}
	switch normalizedMeshAdapterName(model.Adapter) {
	case "meshy_mesh", "tripo_mesh", "hyper3d_mesh", "fal_mesh":
		return true
	default:
		return false
	}
}

func modelSupportsMeshMultiImage(model ModelConfig) bool {
	adapter := normalizedMeshAdapterName(model.Adapter)
	if model.MeshGeneration {
		return model.MeshMultiImage || adapter == "tripo_mesh" || adapter == "meshy_mesh" || adapter == "fal_mesh"
	}
	return adapter == "tripo_mesh" || adapter == "meshy_mesh" || adapter == "fal_mesh"
}

func meshRequestHasAnyUserInput(r *http.Request, prompt string) bool {
	if strings.TrimSpace(prompt) != "" {
		return true
	}
	if r == nil || r.MultipartForm == nil {
		return false
	}
	for _, field := range []string{"inputImage", "backViewImage", "leftViewImage", "rightViewImage", "topViewImage", "bottomViewImage", "leftFrontViewImage", "rightFrontViewImage"} {
		for _, header := range r.MultipartForm.File[field] {
			if header == nil {
				continue
			}
			if strings.TrimSpace(header.Filename) != "" || header.Size > 0 {
				return true
			}
		}
	}
	return false
}

func waveIncludesMeshGeneration(builders []ModelConfig) bool {
	for _, model := range builders {
		if modelIsMeshGeneration(model) {
			return true
		}
	}
	return false
}

func (a *App) meshJobsRoot(projectName string) (string, error) {
	if !isValidProjectName(projectName) {
		return "", errors.New("invalid project name")
	}
	return safeJoin(a.cfg.WorkRoot, "projects", projectName, "mesh_jobs")
}

func (a *App) meshJobRoot(projectName, jobID string) (string, error) {
	root, err := a.meshJobsRoot(projectName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(jobID) == "" {
		return "", errors.New("job id is required")
	}
	return safeJoin(root, jobID)
}

func meshJobMetaPath(jobRoot string) string { return filepath.Join(jobRoot, "meta", "job.json") }
func buildMeshJobID() string                { return "mj_" + time.Now().UTC().Format("2006-01-02T15-04-05Z") }

func writeMeshJobRecord(path string, record meshJobRecord) error {
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if record.CreatedAt == "" {
		record.CreatedAt = record.UpdatedAt
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readMeshJobRecord(path string) (meshJobRecord, error) {
	var record meshJobRecord
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	for i := range record.Artifacts {
		if record.Artifacts[i].Path != "" && record.Artifacts[i].BlobURL == "" {
			record.Artifacts[i].BlobURL = buildBlobURL(record.Artifacts[i].Path)
		}
	}
	return record, nil
}

func (a *App) listMeshJobRecords(projectName, modelID string) ([]meshJobRecord, error) {
	root, err := a.meshJobsRoot(projectName)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []meshJobRecord{}, nil
		}
		return nil, err
	}
	out := make([]meshJobRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := readMeshJobRecord(meshJobMetaPath(filepath.Join(root, entry.Name())))
		if err != nil {
			continue
		}
		if strings.TrimSpace(modelID) != "" && strings.TrimSpace(record.ModelID) != strings.TrimSpace(modelID) {
			continue
		}
		out = append(out, record)
	}
	sort.Slice(out, func(i, j int) bool { return strings.TrimSpace(out[i].CreatedAt) > strings.TrimSpace(out[j].CreatedAt) })
	return out, nil
}

func (a *App) handleMeshJobDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	jobID := strings.TrimSpace(r.URL.Query().Get("jobId"))
	if jobID == "" {
		http.Error(w, "jobId is required", http.StatusBadRequest)
		return
	}
	jobRoot, err := a.meshJobRoot(projectName, jobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	record, err := readMeshJobRecord(meshJobMetaPath(jobRoot))
	if err != nil {
		http.Error(w, "mesh job not found", http.StatusNotFound)
		return
	}
	if _, err := os.Stat(jobRoot); err != nil {
		http.Error(w, "mesh job folder not found", http.StatusNotFound)
		return
	}
	label := sanitizeImportedFilename(record.ModelLabel)
	if label == "" {
		label = "mesh_job"
	}
	filename := fmt.Sprintf("%s_%s.zip", label, jobID)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	if err := buildProjectZip(jobRoot, w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}

func (a *App) handleMeshJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		projectName, err := a.requireActiveProject()
		if err != nil {
			http.Error(w, "Select an active project first.", http.StatusBadRequest)
			return
		}
		modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
		jobs, err := a.listMeshJobRecords(projectName, modelID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
	case http.MethodPost:
		a.handleCreateMeshJob(w, r)
	case http.MethodDelete:
		a.handleDeleteMeshJob(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDeleteMeshJob(w http.ResponseWriter, r *http.Request) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	jobID := strings.TrimSpace(r.URL.Query().Get("jobId"))
	if jobID == "" {
		var req struct {
			JobID string `json:"jobId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		jobID = strings.TrimSpace(req.JobID)
	}
	if jobID == "" {
		http.Error(w, "jobId is required", http.StatusBadRequest)
		return
	}
	jobRoot, err := a.meshJobRoot(projectName, jobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	record, err := readMeshJobRecord(meshJobMetaPath(jobRoot))
	if err != nil {
		http.Error(w, "mesh job not found", http.StatusNotFound)
		return
	}
	if meshJobStatusIsActive(record.Status) {
		http.Error(w, "Stop or wait for this mesh job before deleting its local record.", http.StatusConflict)
		return
	}
	if err := os.RemoveAll(jobRoot); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleMeshJobRefine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	var req struct {
		JobID string `json:"jobId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	sourceJobID := strings.TrimSpace(req.JobID)
	if sourceJobID == "" {
		http.Error(w, "jobId is required", http.StatusBadRequest)
		return
	}
	sourceRoot, err := a.meshJobRoot(projectName, sourceJobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sourceRecord, err := readMeshJobRecord(meshJobMetaPath(sourceRoot))
	if err != nil {
		http.Error(w, "mesh job not found", http.StatusNotFound)
		return
	}
	if !meshJobCanRefine(sourceRecord) {
		http.Error(w, "this mesh job is not a completed Meshy text preview that can be refined", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(sourceRecord.ModelID) == "" {
		http.Error(w, "source mesh job is missing model id", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(sourceRecord.ModelID)
	if !ok {
		http.Error(w, "source mesh model is no longer configured", http.StatusNotFound)
		return
	}
	if strings.ToLower(strings.TrimSpace(model.Adapter)) != "meshy_mesh" {
		http.Error(w, "source mesh model is not configured for Meshy refinement", http.StatusBadRequest)
		return
	}
	jobID := buildMeshJobID()
	record, jobRoot, err := a.initMeshJobRecord(projectName, model, jobID, sourceRecord.Prompt, "manual_refine", sourceRecord.Quality, sourceRecord.OutputFormat)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	record.MeshMode = "refine"
	record.RefinedFromJobID = sourceRecord.JobID
	record.RefinedFromProviderID = sourceRecord.ProviderJobID
	if err := writeMeshJobRecord(meshJobMetaPath(jobRoot), record); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sourceRecord.RefinedJobID = jobID
	if err := writeMeshJobRecord(meshJobMetaPath(sourceRoot), sourceRecord); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	go a.executeMeshJobAsync(projectName, model, jobID, record, nil, nil, nil, "mesh:"+jobID)
	writeJSON(w, http.StatusOK, meshJobCreateResponse{OK: true, Job: record})
}

func (a *App) handleCreateMeshJob(w http.ResponseWriter, r *http.Request) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		http.Error(w, "invalid multipart upload", http.StatusBadRequest)
		return
	}
	modelID := strings.TrimSpace(r.FormValue("modelId"))
	prompt := strings.TrimSpace(r.FormValue("prompt"))
	quality := strings.TrimSpace(r.FormValue("quality"))
	outputFormat := strings.TrimSpace(r.FormValue("outputFormat"))
	meshSettings, err := parseMeshSettingsFormValue(r.FormValue("meshSettings"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if modelID == "" {
		http.Error(w, "modelId is required", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(modelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	if !modelIsMeshGeneration(model) {
		http.Error(w, "selected model is not configured for mesh generation", http.StatusBadRequest)
		return
	}
	if !meshRequestHasAnyUserInput(r, prompt) {
		http.Error(w, "Add a prompt or at least one image before creating a 3D mesh job.", http.StatusBadRequest)
		return
	}
	job, err := a.createManualMeshJob(projectName, model, prompt, quality, outputFormat, meshSettings, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, meshJobCreateResponse{OK: true, Job: job})
}

func parseMeshSettingsFormValue(raw string) (map[string]any, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(trimmed), &input); err != nil {
		return nil, errors.New("meshSettings must be valid JSON")
	}
	allowed := map[string]bool{
		"fal_textured_mesh":   true,
		"fal_enable_pbr":      true,
		"fal_enable_geometry": true,
		"fal_generate_type":   true,
		"fal_face_count":      true,
		"fal_texture_size":    true,
		"fal_resolution":      true,
		"tripo_quality":       true,
	}
	out := map[string]any{}
	for key, value := range input {
		key = strings.TrimSpace(key)
		if !allowed[key] {
			continue
		}
		switch typed := value.(type) {
		case bool:
			if typed {
				out[key] = typed
			}
		case string:
			if trimmedValue := strings.TrimSpace(typed); trimmedValue != "" {
				out[key] = trimmedValue
			}
		case float64:
			out[key] = typed
		case int:
			out[key] = typed
		case json.Number:
			out[key] = typed.String()
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (a *App) initMeshJobRecord(projectName string, model ModelConfig, jobID, prompt, promptSource, quality, outputFormat string) (meshJobRecord, string, error) {
	jobRoot, err := a.meshJobRoot(projectName, jobID)
	if err != nil {
		return meshJobRecord{}, "", err
	}
	for _, dir := range []string{"inputs", "artifacts", filepath.Join("meta")} {
		if err := os.MkdirAll(filepath.Join(jobRoot, dir), 0o755); err != nil {
			return meshJobRecord{}, "", err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	record := meshJobRecord{
		JobID:              jobID,
		ProjectName:        projectName,
		ModelID:            modelIDString(model.ID),
		ModelLabel:         model.Label,
		Provider:           model.Provider,
		Adapter:            model.Adapter,
		ModelName:          model.ModelName,
		Status:             "accepted",
		Prompt:             strings.TrimSpace(prompt),
		PromptSource:       strings.TrimSpace(promptSource),
		Quality:            strings.TrimSpace(quality),
		OutputFormat:       strings.TrimSpace(outputFormat),
		CreatedAt:          now,
		UpdatedAt:          now,
		PromotionState:     "not_promoted",
		MetadataPath:       filepath.ToSlash(filepath.Join("projects", projectName, "mesh_jobs", jobID, "meta", "job.json")),
		ArtifactBundleRoot: filepath.ToSlash(filepath.Join("projects", projectName, "mesh_jobs", jobID, "artifacts")),
	}
	return record, jobRoot, nil
}

func saveMultipartMeshInput(r *http.Request, field, jobRoot, projectName, jobID, prefix string, allowed bool) (string, *stagedMeshInput, error) {
	file, header, err := r.FormFile(field)
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) || strings.Contains(strings.ToLower(err.Error()), "no such file") {
			return "", nil, nil
		}
		return "", nil, err
	}
	defer file.Close()
	if !allowed {
		return "", nil, fmt.Errorf("%s is not supported by this model", field)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", nil, err
	}
	if len(data) == 0 {
		return "", nil, nil
	}
	contentType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return "", nil, fmt.Errorf("%s must be an image upload", field)
	}
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 {
			ext = exts[0]
		}
	}
	if ext == "" {
		ext = ".png"
	}
	rel := filepath.ToSlash(filepath.Join("projects", projectName, "mesh_jobs", jobID, "inputs", prefix+ext))
	full := filepath.Join(jobRoot, "inputs", prefix+ext)
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", nil, err
	}
	return rel, &stagedMeshInput{Path: rel, Name: filepath.Base(rel), MIMEType: contentType, Data: data}, nil
}

func saveMultipartMeshReferenceInputs(r *http.Request, jobRoot, projectName, jobID string, allowed bool) ([]string, []stagedMeshInput, error) {
	fields := []struct {
		name   string
		prefix string
	}{
		{name: "backViewImage", prefix: "back_view"},
		{name: "leftViewImage", prefix: "left_view"},
		{name: "rightViewImage", prefix: "right_view"},
	}
	paths := []string{}
	inputs := []stagedMeshInput{}
	for _, field := range fields {
		path, input, err := saveMultipartMeshInput(r, field.name, jobRoot, projectName, jobID, field.prefix, allowed)
		if err != nil {
			return nil, nil, err
		}
		if input != nil {
			paths = append(paths, path)
			inputs = append(inputs, *input)
		}
	}
	return paths, inputs, nil
}

func saveMultipartMeshNamedInputs(r *http.Request, jobRoot, projectName, jobID string, allowed bool) (map[string]string, map[string]stagedMeshInput, error) {
	fields := []struct {
		formName string
		key      string
		prefix   string
	}{
		{formName: "topViewImage", key: "top", prefix: "top_view"},
		{formName: "bottomViewImage", key: "bottom", prefix: "bottom_view"},
		{formName: "leftFrontViewImage", key: "left_front", prefix: "left_front_view"},
		{formName: "rightFrontViewImage", key: "right_front", prefix: "right_front_view"},
	}
	paths := map[string]string{}
	inputs := map[string]stagedMeshInput{}
	for _, field := range fields {
		path, input, err := saveMultipartMeshInput(r, field.formName, jobRoot, projectName, jobID, field.prefix, allowed)
		if err != nil {
			return nil, nil, err
		}
		if input != nil {
			paths[field.key] = path
			inputs[field.key] = *input
		}
	}
	if len(paths) == 0 {
		paths = nil
	}
	if len(inputs) == 0 {
		inputs = nil
	}
	return paths, inputs, nil
}

func inferMeshMode(model ModelConfig, inputPath string, referencePaths []string) string {
	switch normalizedMeshAdapterName(model.Adapter) {
	case "meshy_mesh":
		if strings.TrimSpace(inputPath) == "" {
			return "preview"
		}
		if len(referencePaths) > 0 {
			return "multi_image"
		}
		return "image"
	case "tripo_mesh":
		if strings.TrimSpace(inputPath) == "" {
			return "text"
		}
		if len(referencePaths) > 0 {
			return "multiview_staged"
		}
		return "image"
	case "fal_mesh":
		if strings.TrimSpace(inputPath) == "" {
			return "text"
		}
		if len(referencePaths) > 0 {
			return "multi_view"
		}
		return "image"
	default:
		if strings.TrimSpace(inputPath) != "" {
			return "image"
		}
		return "text"
	}
}

func meshJobStatusIsActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "accepted", "running", "queued", "submitted", "processing":
		return true
	default:
		return false
	}
}

func meshJobCanRefine(record meshJobRecord) bool {
	if strings.ToLower(strings.TrimSpace(record.Adapter)) != "meshy_mesh" {
		return false
	}
	if strings.ToLower(strings.TrimSpace(record.Status)) != "completed" {
		return false
	}
	if strings.TrimSpace(record.ProviderJobID) == "" || strings.TrimSpace(record.RefinedJobID) != "" {
		return false
	}
	if strings.TrimSpace(record.InputImagePath) != "" || len(record.ReferenceImagePaths) > 0 {
		return false
	}
	mode := strings.ToLower(strings.TrimSpace(record.MeshMode))
	taskType := strings.ToLower(strings.TrimSpace(record.ProviderTaskType))
	return mode == "" || mode == "preview" || strings.Contains(taskType, "preview")
}

func (a *App) createManualMeshJob(projectName string, model ModelConfig, prompt, quality, outputFormat string, meshSettings map[string]any, r *http.Request) (meshJobRecord, error) {
	jobID := buildMeshJobID()
	record, jobRoot, err := a.initMeshJobRecord(projectName, model, jobID, prompt, "manual", quality, outputFormat)
	if err != nil {
		return meshJobRecord{}, err
	}
	inputPath, inputMeta, err := saveMultipartMeshInput(r, "inputImage", jobRoot, projectName, jobID, "input", true)
	if err != nil {
		return meshJobRecord{}, err
	}
	referencePaths, referenceInputs, err := saveMultipartMeshReferenceInputs(r, jobRoot, projectName, jobID, true)
	if err != nil {
		return meshJobRecord{}, err
	}
	namedPaths, namedInputs, err := saveMultipartMeshNamedInputs(r, jobRoot, projectName, jobID, true)
	if err != nil {
		return meshJobRecord{}, err
	}
	record.InputImagePath = inputPath
	record.ReferenceImagePaths = append([]string(nil), referencePaths...)
	if len(namedPaths) > 0 {
		record.NamedImagePaths = namedPaths
	}
	if len(meshSettings) > 0 {
		record.MeshSettings = meshSettings
	}
	record.MeshMode = inferMeshMode(model, record.InputImagePath, append(record.ReferenceImagePaths, namedImagePathValues(record.NamedImagePaths)...))
	if err := writeMeshJobRecord(meshJobMetaPath(jobRoot), record); err != nil {
		return meshJobRecord{}, err
	}
	go a.executeMeshJobAsync(projectName, model, jobID, record, inputMeta, referenceInputs, namedInputs, "mesh:"+jobID)
	return record, nil
}

func cloneMeshSettingsMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func namedImagePathValues(paths map[string]string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, value := range paths {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	return out
}

func (a *App) executeMeshJobAsync(projectName string, model ModelConfig, jobID string, record meshJobRecord, inputMeta *stagedMeshInput, referenceInputs []stagedMeshInput, namedInputs map[string]stagedMeshInput, cancelKey string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.setActiveCancelLocked(cancelKey, projectName, jobID, cancel)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.clearActiveCancelLocked(cancelKey, jobID)
		a.mu.Unlock()
	}()
	jobRoot, err := a.meshJobRoot(projectName, jobID)
	if err != nil {
		return
	}
	record.Status = "running"
	record.Error = ""
	_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
	meshReq := adapters.MeshRequest{
		JobID:               jobID,
		Prompt:              record.Prompt,
		Settings:            cloneMeshSettingsMap(record.MeshSettings),
		Quality:             record.Quality,
		OutputFormat:        record.OutputFormat,
		MeshMode:            record.MeshMode,
		SourceProviderJobID: record.RefinedFromProviderID,
	}
	if inputMeta != nil {
		meshReq.InputImage = &adapters.MeshBinary{Name: inputMeta.Name, MIMEType: inputMeta.MIMEType, Data: inputMeta.Data}
	}
	for _, ref := range referenceInputs {
		if len(ref.Data) > 0 {
			meshReq.ReferenceImages = append(meshReq.ReferenceImages, adapters.MeshBinary{Name: ref.Name, MIMEType: ref.MIMEType, Data: ref.Data})
		}
	}
	if len(namedInputs) > 0 {
		meshReq.NamedImages = map[string]adapters.MeshBinary{}
		for key, ref := range namedInputs {
			if len(ref.Data) > 0 {
				meshReq.NamedImages[key] = adapters.MeshBinary{Name: ref.Name, MIMEType: ref.MIMEType, Data: ref.Data}
			}
		}
	}
	result, err := adapters.ExecuteMesh(ctx, toAdapterModelConfig(model), meshReq)
	record.ProviderJobID = result.ProviderJobID
	record.RemoteStatus = result.RemoteStatus
	record.ProviderTaskType = result.TaskType
	if strings.TrimSpace(record.MeshMode) == "" {
		record.MeshMode = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(result.TaskType)), "text-to-3d-"), "-")
	}
	if err != nil {
		if ctx.Err() != nil {
			record.Status = "stopped"
			record.Error = "stopped locally"
		} else {
			record.Status = "failed"
			record.Error = strings.TrimSpace(err.Error())
		}
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
		return
	}
	if err := a.persistMeshArtifacts(projectName, jobID, &record, result); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
		return
	}
	record.Status = "completed"
	record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	record.PromotionState = "job_saved"
	record.PromotedAt = record.CompletedAt
	record.Error = ""
	if err := writeJobProviderResponse(jobRoot, result.RawBody); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
		return
	}
	_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
}

func (a *App) persistMeshArtifacts(projectName, jobID string, record *meshJobRecord, result adapters.MeshResult) error {
	artifactsDir := filepath.ToSlash(filepath.Join("projects", projectName, "mesh_jobs", jobID, "artifacts"))
	artifacts := normalizeLinkedMeshArtifactNames(result.Artifacts)
	for idx, artifact := range artifacts {
		name := sanitizeImportedFilename(artifact.Name)
		if name == "" {
			if artifact.Kind == "preview" {
				name = fmt.Sprintf("preview_%02d%s", idx+1, extForMIMEOrDefault(artifact.MIMEType, ".png"))
			} else {
				name = fmt.Sprintf("artifact_%02d%s", idx+1, extForMIMEOrDefault(artifact.MIMEType, ".bin"))
			}
		}
		rel := filepath.ToSlash(filepath.Join(artifactsDir, name))
		full, err := safeJoin(a.cfg.WorkRoot, rel)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, artifact.Data, 0o644); err != nil {
			return err
		}
		record.Artifacts = append(record.Artifacts, meshArtifactRecord{Name: name, Path: rel, Kind: artifact.Kind, MIME: artifact.MIMEType, BlobURL: buildBlobURL(rel)})
		if record.PrimaryModelPath == "" && artifact.Kind != "preview" {
			record.PrimaryModelPath = rel
		}
		if artifact.Kind == "preview" && record.PreviewImagePath == "" {
			record.PreviewImagePath = rel
		}
	}
	if record.PrimaryModelPath == "" && result.PrimaryData != nil {
		name := sanitizeImportedFilename(result.PrimaryFilename)
		if name == "" {
			name = "model" + extForMIMEOrDefault(result.PrimaryMIMEType, ".glb")
		}
		rel := filepath.ToSlash(filepath.Join(artifactsDir, name))
		full, err := safeJoin(a.cfg.WorkRoot, rel)
		if err != nil {
			return err
		}
		if err := os.WriteFile(full, result.PrimaryData, 0o644); err != nil {
			return err
		}
		record.PrimaryModelPath = rel
		record.Artifacts = append(record.Artifacts, meshArtifactRecord{Name: name, Path: rel, Kind: "primary", MIME: result.PrimaryMIMEType, BlobURL: buildBlobURL(rel)})
	}
	if record.PreviewImagePath == "" && len(result.PreviewData) > 0 {
		name := sanitizeImportedFilename(result.PreviewFilename)
		if name == "" {
			name = "preview" + extForMIMEOrDefault(result.PreviewMIMEType, ".png")
		}
		rel := filepath.ToSlash(filepath.Join(artifactsDir, name))
		full, err := safeJoin(a.cfg.WorkRoot, rel)
		if err != nil {
			return err
		}
		if err := os.WriteFile(full, result.PreviewData, 0o644); err != nil {
			return err
		}
		record.PreviewImagePath = rel
		record.Artifacts = append(record.Artifacts, meshArtifactRecord{Name: name, Path: rel, Kind: "preview", MIME: result.PreviewMIMEType, BlobURL: buildBlobURL(rel)})
	}
	return nil
}

func normalizeLinkedMeshArtifactNames(artifacts []adapters.MeshArtifact) []adapters.MeshArtifact {
	out := append([]adapters.MeshArtifact(nil), artifacts...)
	mtlRef := ""
	textureRef := ""
	for _, artifact := range out {
		name := strings.ToLower(strings.TrimSpace(artifact.Name))
		switch filepath.Ext(name) {
		case ".obj":
			if ref := firstOBJMaterialLibraryName(string(artifact.Data)); ref != "" {
				mtlRef = ref
			}
		case ".mtl":
			if ref := firstMTLTextureName(string(artifact.Data)); ref != "" {
				textureRef = ref
			}
		}
	}
	if mtlRef != "" {
		applyLinkedMeshArtifactName(out, "material", ".mtl", mtlRef)
	}
	if textureRef != "" {
		applyLinkedMeshArtifactName(out, "texture", "", textureRef)
	}
	return out
}

func applyLinkedMeshArtifactName(artifacts []adapters.MeshArtifact, kind, ext, targetName string) {
	cleanTarget := sanitizeImportedFilename(targetName)
	if cleanTarget == "" {
		return
	}
	matches := []int{}
	for idx, artifact := range artifacts {
		if kind != "" && strings.EqualFold(strings.TrimSpace(artifact.Kind), kind) {
			matches = append(matches, idx)
			continue
		}
		if ext != "" && strings.EqualFold(filepath.Ext(artifact.Name), ext) {
			matches = append(matches, idx)
		}
	}
	if len(matches) == 1 {
		artifacts[matches[0]].Name = cleanTarget
	}
}

func firstOBJMaterialLibraryName(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmed), "mtllib ") {
			return meshLinkedFilenameFromDirective(strings.TrimSpace(trimmed[len("mtllib "):]))
		}
	}
	return ""
}

func firstMTLTextureName(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if strings.HasPrefix(lower, "map_kd ") || strings.HasPrefix(lower, "map_ks ") || strings.HasPrefix(lower, "map_bump ") || strings.HasPrefix(lower, "bump ") {
			return meshLinkedFilenameFromDirective(strings.TrimSpace(trimmed[strings.Index(trimmed, " ")+1:]))
		}
	}
	return ""
}

func meshLinkedFilenameFromDirective(value string) string {
	parts := strings.Fields(strings.TrimSpace(value))
	if len(parts) == 0 {
		return ""
	}
	// MTL map directives can include option flags before the filename. The
	// filename is conventionally the final token.
	return filepath.Base(parts[len(parts)-1])
}

func extForMIMEOrDefault(contentType, fallback string) string {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil && strings.TrimSpace(parsed) != "" {
		mediaType = strings.ToLower(strings.TrimSpace(parsed))
	}
	if mediaType == "video/mp4" || mediaType == "application/mp4" {
		return ".mp4"
	}
	if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 && strings.TrimSpace(exts[0]) != "" {
		return exts[0]
	}
	return fallback
}

func (a *App) buildWaveMeshInput(projectName string, contextFiles []string) (*stagedMeshInput, []string, error) {
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return nil, nil, err
	}
	used := []string{}
	for _, rel := range normalizeRelativePaths(contextFiles) {
		full, err := safeJoin(projectworkRoot, rel)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		contentType := detectContentType(rel, data)
		if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
			continue
		}
		return &stagedMeshInput{Path: filepath.ToSlash(rel), Name: filepath.Base(rel), MIMEType: contentType, Data: data}, append(used, filepath.ToSlash(rel)), nil
	}
	return nil, used, nil
}

func (a *App) stageWaveMeshInput(projectName, jobID, prefix string, input *stagedMeshInput) (string, error) {
	if input == nil || len(input.Data) == 0 {
		return "", nil
	}
	ext := strings.ToLower(filepath.Ext(input.Name))
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(input.MIMEType); len(exts) > 0 {
			ext = exts[0]
		}
	}
	if ext == "" {
		ext = ".png"
	}
	rel := filepath.ToSlash(filepath.Join("projects", projectName, "mesh_jobs", jobID, "inputs", prefix+ext))
	full, err := safeJoin(a.cfg.WorkRoot, rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, input.Data, 0o644); err != nil {
		return "", err
	}
	return rel, nil
}

func (a *App) runMeshModelRequest(model ModelConfig, projectName, executionID, prompt string, contextFiles []string) modelRunResult {
	result := modelRunResult{ModelID: modelIDString(model.ID), ModelLabel: model.Label}
	inputImage, usedContext, err := a.buildWaveMeshInput(projectName, contextFiles)
	if err != nil {
		result.Err = err
		return result
	}
	jobID := buildMeshJobID()
	record, jobRoot, err := a.initMeshJobRecord(projectName, model, jobID, prompt, "wave", model.MeshQuality, model.MeshOutputFormat)
	if err != nil {
		result.Err = err
		return result
	}
	record.SourceContextFiles = append([]string(nil), usedContext...)
	record.InputImagePath, _ = a.stageWaveMeshInput(projectName, jobID, "input", inputImage)
	record.MeshMode = inferMeshMode(model, record.InputImagePath, nil)
	if err := writeMeshJobRecord(meshJobMetaPath(jobRoot), record); err != nil {
		result.Err = err
		return result
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.setActiveCancelLocked(modelIDString(model.ID), projectName, executionID, cancel)
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.clearActiveCancelLocked(modelIDString(model.ID), executionID); a.mu.Unlock() }()
	record.Status = "running"
	_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
	meshReq := adapters.MeshRequest{JobID: jobID, Prompt: prompt, Quality: record.Quality, OutputFormat: record.OutputFormat, MeshMode: record.MeshMode}
	if inputImage != nil {
		meshReq.InputImage = &adapters.MeshBinary{Name: inputImage.Name, MIMEType: inputImage.MIMEType, Data: inputImage.Data}
	}
	meshResult, err := adapters.ExecuteMesh(ctx, toAdapterModelConfig(model), meshReq)
	record.ProviderJobID = meshResult.ProviderJobID
	record.RemoteStatus = meshResult.RemoteStatus
	record.ProviderTaskType = meshResult.TaskType
	if strings.TrimSpace(record.MeshMode) == "" {
		record.MeshMode = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(meshResult.TaskType)), "text-to-3d-"), "-")
	}
	if err != nil {
		if ctx.Err() != nil {
			record.Status = "stopped"
			record.Error = "stopped locally"
		} else {
			record.Status = "failed"
			record.Error = strings.TrimSpace(err.Error())
		}
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
		result.Err = errors.New(record.Error)
		return result
	}
	if err := a.persistMeshArtifacts(projectName, jobID, &record, meshResult); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
		result.Err = err
		return result
	}
	record.Status = "completed"
	record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	record.PromotionState = "job_saved"
	record.PromotedAt = record.CompletedAt
	record.Error = ""
	if err := writeJobProviderResponse(jobRoot, meshResult.RawBody); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
		result.Err = err
		return result
	}
	_ = writeMeshJobRecord(meshJobMetaPath(jobRoot), record)
	result.Valid = true
	return result
}

func (a *App) stageMeshArtifactsToProjectwork(model ModelConfig, projectName, jobRoot string, record *meshJobRecord, result adapters.MeshResult) error {
	if record == nil {
		return errors.New("missing mesh job record")
	}
	src := filepath.Join(jobRoot, "artifacts")
	targetRel, targetFull, err := a.nextMediaProjectworkOutputRoot(projectName, model)
	if err != nil {
		return err
	}
	if _, err := syncDirContents(src, targetFull); err != nil {
		return err
	}
	record.ProjectworkBundleRoot = targetRel
	record.PromotionState = "auto_saved"
	record.PromotedAt = time.Now().UTC().Format(time.RFC3339)
	if record.PrimaryModelPath != "" {
		record.PrimaryProjectworkPath = filepath.ToSlash(filepath.Join(targetRel, filepath.Base(record.PrimaryModelPath)))
	}
	if strings.TrimSpace(result.RawBody) != "" {
		if err := os.WriteFile(filepath.Join(targetFull, "provider_response.json"), []byte(result.RawBody), 0o644); err != nil {
			return err
		}
	}
	return writeProjectworkJSON(filepath.Join(targetFull, "job.json"), record)
}

func (a *App) stageMeshArtifactsForMerge(model ModelConfig, projectName, jobID, jobRoot string) (int, error) {
	projectRoot, _, err := a.projectPaths(model, projectName)
	if err != nil {
		return 0, err
	}
	src := filepath.Join(jobRoot, "artifacts")
	target, err := safeJoin(projectRoot, filepath.ToSlash(filepath.Join("mesh_output", jobID)))
	if err != nil {
		return 0, err
	}
	if err := os.RemoveAll(target); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return 0, err
	}
	return syncDirContents(src, target)
}

func (a *App) handleMeshJobPromote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	var req struct {
		JobID string `json:"jobId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	jobID := strings.TrimSpace(req.JobID)
	if jobID == "" {
		http.Error(w, "jobId is required", http.StatusBadRequest)
		return
	}
	jobRoot, err := a.meshJobRoot(projectName, jobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	record, err := readMeshJobRecord(meshJobMetaPath(jobRoot))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if record.Status != "completed" {
		http.Error(w, "mesh job is not completed yet", http.StatusBadRequest)
		return
	}
	src := filepath.Join(jobRoot, "artifacts")
	if _, err := os.Stat(src); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	record.PromotionState = "job_saved"
	record.PromotedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeMeshJobRecord(meshJobMetaPath(jobRoot), record); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if record.ModelID != "" {
		a.setPendingMergeCount(projectName, record.ModelID, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": record})
}
