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
	InputImagePath         string               `json:"inputImagePath,omitempty"`
	ReferenceImagePaths    []string             `json:"referenceImagePaths,omitempty"`
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
	case "meshy_mesh", "tripo_mesh", "hyper3d_mesh":
		return true
	default:
		return false
	}
}

func modelSupportsMeshMultiImage(model ModelConfig) bool {
	adapter := normalizedMeshAdapterName(model.Adapter)
	if model.MeshGeneration {
		return model.MeshMultiImage || adapter == "tripo_mesh" || adapter == "meshy_mesh"
	}
	return adapter == "tripo_mesh" || adapter == "meshy_mesh"
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
	go a.executeMeshJobAsync(projectName, model, jobID, record, nil, nil, "mesh:"+jobID)
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
	if prompt == "" && !modelSupportsMeshPromptOnly(model) {
		http.Error(w, "this model requires a prompt", http.StatusBadRequest)
		return
	}
	job, err := a.createManualMeshJob(projectName, model, prompt, quality, outputFormat, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, meshJobCreateResponse{OK: true, Job: job})
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

func (a *App) createManualMeshJob(projectName string, model ModelConfig, prompt, quality, outputFormat string, r *http.Request) (meshJobRecord, error) {
	jobID := buildMeshJobID()
	record, jobRoot, err := a.initMeshJobRecord(projectName, model, jobID, prompt, "manual", quality, outputFormat)
	if err != nil {
		return meshJobRecord{}, err
	}
	inputPath, inputMeta, err := saveMultipartMeshInput(r, "inputImage", jobRoot, projectName, jobID, "input", modelSupportsMeshImageInput(model))
	if err != nil {
		return meshJobRecord{}, err
	}
	referencePaths, referenceInputs, err := saveMultipartMeshReferenceInputs(r, jobRoot, projectName, jobID, modelSupportsMeshMultiImage(model))
	if err != nil {
		return meshJobRecord{}, err
	}
	record.InputImagePath = inputPath
	record.ReferenceImagePaths = append([]string(nil), referencePaths...)
	record.MeshMode = inferMeshMode(model, record.InputImagePath, record.ReferenceImagePaths)
	if err := writeMeshJobRecord(meshJobMetaPath(jobRoot), record); err != nil {
		return meshJobRecord{}, err
	}
	go a.executeMeshJobAsync(projectName, model, jobID, record, inputMeta, referenceInputs, "mesh:"+jobID)
	return record, nil
}

func (a *App) executeMeshJobAsync(projectName string, model ModelConfig, jobID string, record meshJobRecord, inputMeta *stagedMeshInput, referenceInputs []stagedMeshInput, cancelKey string) {
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
	for idx, artifact := range result.Artifacts {
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
