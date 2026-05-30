package main

import (
	"agentgo/adapters"
	"bytes"
	"context"
	"encoding/binary"
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

type videoJobRecord struct {
	JobID                 string            `json:"jobId"`
	ProjectName           string            `json:"projectName"`
	ModelID               string            `json:"modelId"`
	ModelLabel            string            `json:"modelLabel"`
	Provider              string            `json:"provider"`
	Adapter               string            `json:"adapter"`
	ModelName             string            `json:"modelName"`
	Status                string            `json:"status"`
	ProviderJobID         string            `json:"providerJobId,omitempty"`
	ProviderOperationName string            `json:"providerOperationName,omitempty"`
	RemoteStatus          string            `json:"remoteStatus,omitempty"`
	PollCount             int               `json:"pollCount,omitempty"`
	LastPollAt            string            `json:"lastPollAt,omitempty"`
	ProviderDiagnostics   map[string]string `json:"providerDiagnostics,omitempty"`
	Prompt                string            `json:"prompt"`
	PromptSource          string            `json:"promptSource,omitempty"`
	Duration              string            `json:"duration,omitempty"`
	AspectRatio           string            `json:"aspectRatio,omitempty"`
	Resolution            string            `json:"resolution,omitempty"`
	OutputFormat          string            `json:"outputFormat,omitempty"`
	FPS                   string            `json:"fps,omitempty"`
	Quality               string            `json:"quality,omitempty"`
	StartFramePath        string            `json:"startFramePath,omitempty"`
	EndFramePath          string            `json:"endFramePath,omitempty"`
	ReferenceImagePaths   []string          `json:"referenceImagePaths,omitempty"`
	ArtifactVideoPath     string            `json:"artifactVideoPath,omitempty"`
	ProjectworkVideoPath  string            `json:"projectworkVideoPath,omitempty"`
	ThumbnailPath         string            `json:"thumbnailPath,omitempty"`
	MetadataPath          string            `json:"metadataPath,omitempty"`
	SourceContextFiles    []string          `json:"sourceContextFiles,omitempty"`
	PromotionState        string            `json:"promotionState,omitempty"`
	Error                 string            `json:"error,omitempty"`
	CreatedAt             string            `json:"createdAt"`
	UpdatedAt             string            `json:"updatedAt"`
	CompletedAt           string            `json:"completedAt,omitempty"`
	PromotedAt            string            `json:"promotedAt,omitempty"`
}

type videoJobResponse struct {
	videoJobRecord
	ArtifactBlobURL        string   `json:"artifactBlobUrl,omitempty"`
	ProjectworkBlobURL     string   `json:"projectworkBlobUrl,omitempty"`
	StartFrameBlobURL      string   `json:"startFrameBlobUrl,omitempty"`
	EndFrameBlobURL        string   `json:"endFrameBlobUrl,omitempty"`
	ReferenceImageBlobURLs []string `json:"referenceImageBlobUrls,omitempty"`
}

type videoJobCreateResponse struct {
	OK  bool             `json:"ok"`
	Job videoJobResponse `json:"job"`
}

type stagedVideoInput struct {
	Path     string
	Name     string
	MIMEType string
	Data     []byte
}

const (
	defaultVideoJobDuration   = "8"
	defaultVideoJobAspect     = "16:9"
	defaultVideoJobResolution = "720p"
)

type videoOutputFileInfo struct {
	OriginalFilename    string
	OriginalExtension   string
	OriginalMIME        string
	DownloadContentType string
	DetectedContainer   string
	DetectedBrands      []string
	FinalFilename       string
	FinalExtension      string
	MP4Compatible       bool
	Decision            string
}

func resolvedVideoJobSettings(model ModelConfig, duration, aspectRatio, resolution string) (string, string, string) {
	return firstNonEmpty(duration, model.VideoDuration, defaultVideoJobDuration),
		firstNonEmpty(aspectRatio, model.VideoAspectRatio, defaultVideoJobAspect),
		firstNonEmpty(resolution, model.VideoResolution, defaultVideoJobResolution)
}

func (a *App) persistVideoModelSettings(modelID, duration, aspectRatio, resolution string) (ModelConfig, error) {
	cleanID := strings.TrimSpace(modelID)
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.cfg.Models {
		if modelIDString(a.cfg.Models[i].ID) != cleanID {
			continue
		}
		changed := false
		if strings.TrimSpace(duration) != "" && strings.TrimSpace(a.cfg.Models[i].VideoDuration) != strings.TrimSpace(duration) {
			a.cfg.Models[i].VideoDuration = strings.TrimSpace(duration)
			changed = true
		}
		if strings.TrimSpace(aspectRatio) != "" && strings.TrimSpace(a.cfg.Models[i].VideoAspectRatio) != strings.TrimSpace(aspectRatio) {
			a.cfg.Models[i].VideoAspectRatio = strings.TrimSpace(aspectRatio)
			changed = true
		}
		if strings.TrimSpace(resolution) != "" && strings.TrimSpace(a.cfg.Models[i].VideoResolution) != strings.TrimSpace(resolution) {
			a.cfg.Models[i].VideoResolution = strings.TrimSpace(resolution)
			changed = true
		}
		if changed {
			a.cfg.Models[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if err := a.persistModelsLocked(); err != nil {
				return ModelConfig{}, err
			}
		}
		return a.cfg.Models[i], nil
	}
	return ModelConfig{}, errors.New("unknown model")
}

func normalizeVideoMediaType(contentType string) string {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil && strings.TrimSpace(parsed) != "" {
		mediaType = strings.ToLower(strings.TrimSpace(parsed))
	}
	return mediaType
}

func inspectMP4FamilyContainer(data []byte) (bool, string, []string) {
	if len(data) < 12 {
		return false, "", nil
	}
	limit := len(data)
	if limit > 4096 {
		limit = 4096
	}
	idx := bytes.Index(data[:limit], []byte("ftyp"))
	if idx < 4 {
		return false, "", nil
	}
	boxStart := idx - 4
	boxSize := int(binary.BigEndian.Uint32(data[boxStart:idx]))
	boxEnd := boxStart + boxSize
	if boxSize < 16 || boxEnd > limit {
		boxEnd = limit
	}
	brands := []string{}
	appendBrand := func(raw []byte) {
		if len(raw) != 4 {
			return
		}
		brand := string(raw)
		if strings.TrimSpace(brand) == "" {
			return
		}
		for _, existing := range brands {
			if existing == brand {
				return
			}
		}
		brands = append(brands, brand)
	}
	if idx+8 <= boxEnd {
		appendBrand(data[idx+4 : idx+8])
	}
	for pos := idx + 12; pos+4 <= boxEnd; pos += 4 {
		appendBrand(data[pos : pos+4])
	}
	return true, "ISO BMFF / ftyp", brands
}

func mp4CompatibleBrand(brands []string) bool {
	known := map[string]bool{
		"isom": true, "iso2": true, "iso3": true, "iso4": true, "iso5": true, "iso6": true,
		"mp41": true, "mp42": true, "avc1": true, "avc3": true, "hvc1": true, "hev1": true,
		"dash": true, "M4V ": true, "M4A ": true, "f4v ": true, "F4V ": true, "f4p ": true,
	}
	for _, brand := range brands {
		if known[brand] {
			return true
		}
	}
	return false
}

func replaceVideoExtension(name, ext string) string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		clean = "video"
	}
	current := filepath.Ext(clean)
	if current == "" {
		return clean + ext
	}
	return strings.TrimSuffix(clean, current) + ext
}

func resolveVideoOutputFilename(result adapters.VideoResult, fallbackBase string) (string, videoOutputFileInfo) {
	info := videoOutputFileInfo{
		OriginalFilename:    sanitizeImportedFilename(result.VideoFilename),
		OriginalMIME:        strings.TrimSpace(result.VideoMIMEType),
		DownloadContentType: strings.TrimSpace(result.VideoDownloadContentType),
	}
	if info.OriginalFilename == "" || info.OriginalFilename == "downloaded_file" {
		info.OriginalFilename = strings.TrimSpace(fallbackBase) + extForMIMEOrDefault(firstNonEmpty(info.OriginalMIME, info.DownloadContentType), ".mp4")
	}
	if info.OriginalFilename == "" || info.OriginalFilename == "downloaded_file" {
		info.OriginalFilename = "video.mp4"
	}
	info.OriginalExtension = strings.ToLower(filepath.Ext(info.OriginalFilename))
	mediaType := normalizeVideoMediaType(firstNonEmpty(info.OriginalMIME, info.DownloadContentType))
	hasFTYP, container, brands := inspectMP4FamilyContainer(result.VideoData)
	info.DetectedContainer = container
	info.DetectedBrands = brands
	switch {
	case mediaType == "video/mp4" || mediaType == "application/mp4":
		info.MP4Compatible = true
		info.Decision = "content type is MP4"
	case info.OriginalExtension == ".mp4":
		info.MP4Compatible = true
		info.Decision = "filename extension is MP4"
	case info.OriginalExtension == ".f4v" && hasFTYP:
		info.MP4Compatible = true
		info.Decision = "F4V file uses MP4-family ftyp container"
	case hasFTYP && mp4CompatibleBrand(brands):
		info.MP4Compatible = true
		info.Decision = "detected MP4-compatible ftyp brands"
	default:
		info.Decision = "preserved original extension"
	}
	info.FinalFilename = info.OriginalFilename
	if info.MP4Compatible {
		info.FinalFilename = replaceVideoExtension(info.OriginalFilename, ".mp4")
	}
	info.FinalFilename = sanitizeImportedFilename(info.FinalFilename)
	if info.FinalFilename == "" {
		info.FinalFilename = "video.mp4"
	}
	info.FinalExtension = strings.ToLower(filepath.Ext(info.FinalFilename))
	return info.FinalFilename, info
}

func applyVideoFileDiagnostics(record *videoJobRecord, result adapters.VideoResult, info videoOutputFileInfo) {
	if record == nil {
		return
	}
	setVideoDiagnostic(record, "video_original_filename", info.OriginalFilename)
	setVideoDiagnostic(record, "video_original_extension", info.OriginalExtension)
	setVideoDiagnostic(record, "video_result_mime_type", info.OriginalMIME)
	setVideoDiagnostic(record, "video_download_content_type", info.DownloadContentType)
	setVideoDiagnostic(record, "video_source_uri", result.VideoSourceURI)
	setVideoDiagnostic(record, "video_detected_container", info.DetectedContainer)
	if len(info.DetectedBrands) > 0 {
		setVideoDiagnostic(record, "video_detected_brands", strings.Join(info.DetectedBrands, ", "))
	}
	setVideoDiagnostic(record, "video_mp4_normalization", fmt.Sprintf("mp4_compatible=%t; decision=%s; final_filename=%s; final_extension=%s", info.MP4Compatible, info.Decision, info.FinalFilename, info.FinalExtension))
}

func normalizedVideoAdapterName(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "veo_video":
		return "veo_video"
	case "vertex_veo_video":
		return "vertex_veo_video"
	case "kling_video":
		return "kling_video"
	case "sora_video":
		return "sora_video"
	case "comfyui_ltx_video":
		return "comfyui_ltx_video"
	default:
		return ""
	}
}

func modelIsVideoGeneration(model ModelConfig) bool {
	if model.VideoGeneration {
		return true
	}
	return normalizedVideoAdapterName(model.Adapter) != ""
}

func modelSupportsVideoPromptOnly(model ModelConfig) bool {
	if model.VideoGeneration {
		return model.VideoPromptOnly
	}
	return true
}

func modelSupportsVideoStartFrame(model ModelConfig) bool {
	if model.VideoGeneration {
		return model.VideoStartFrame
	}
	switch normalizedVideoAdapterName(model.Adapter) {
	case "veo_video", "vertex_veo_video", "kling_video", "sora_video", "comfyui_ltx_video":
		return true
	default:
		return false
	}
}

func modelSupportsVideoEndFrame(model ModelConfig) bool {
	if model.VideoGeneration {
		return model.VideoEndFrame
	}
	switch normalizedVideoAdapterName(model.Adapter) {
	case "veo_video", "vertex_veo_video", "kling_video":
		return true
	default:
		return false
	}
}

func modelSupportsVideoIngredients(model ModelConfig) bool {
	if model.VideoGeneration {
		return model.VideoIngredients
	}
	switch normalizedVideoAdapterName(model.Adapter) {
	case "veo_video", "vertex_veo_video":
		return isKnownGoogleVeoIngredientsModel(model.ModelName)
	default:
		return false
	}
}

func isKnownGoogleVeoIngredientsModel(modelName string) bool {
	clean := strings.ToLower(strings.TrimSpace(modelName))
	if clean == "" || strings.Contains(clean, "lite") {
		return false
	}
	return strings.Contains(clean, "veo-3.1")
}

func waveIncludesVideoGeneration(builders []ModelConfig) bool {
	for _, model := range builders {
		if modelIsVideoGeneration(model) {
			return true
		}
	}
	return false
}

func (a *App) videoJobsRoot(projectName string) (string, error) {
	if !isValidProjectName(projectName) {
		return "", errors.New("invalid project name")
	}
	return safeJoin(a.cfg.WorkRoot, "projects", projectName, "video_jobs")
}

func (a *App) videoJobRoot(projectName, jobID string) (string, error) {
	root, err := a.videoJobsRoot(projectName)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(jobID) == "" {
		return "", errors.New("job id is required")
	}
	return safeJoin(root, jobID)
}

func videoJobMetaPath(jobRoot string) string { return filepath.Join(jobRoot, "meta", "job.json") }

func buildVideoJobID() string { return "vj_" + time.Now().UTC().Format("2006-01-02T15-04-05Z") }

func writeVideoJobRecord(path string, record videoJobRecord) error {
	record.MetadataPath = filepath.ToSlash(strings.TrimSpace(record.MetadataPath))
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

func readVideoJobRecord(path string) (videoJobRecord, error) {
	var record videoJobRecord
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	return record, nil
}

func videoDiagnosticText(raw string) string {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return ""
	}
	const maxDiagnosticChars = 200000
	if len(clean) > maxDiagnosticChars {
		return clean[:maxDiagnosticChars] + "\n...[truncated by AgentGO]"
	}
	return clean
}

func setVideoDiagnostic(record *videoJobRecord, key, value string) {
	if record == nil {
		return
	}
	value = videoDiagnosticText(value)
	if value == "" {
		return
	}
	if record.ProviderDiagnostics == nil {
		record.ProviderDiagnostics = map[string]string{}
	}
	record.ProviderDiagnostics[key] = value
}

func applyVideoProgressToRecord(record *videoJobRecord, progress adapters.VideoProgress) {
	if record == nil {
		return
	}
	if strings.TrimSpace(progress.Status) != "" {
		record.Status = strings.TrimSpace(progress.Status)
	}
	if strings.TrimSpace(progress.ProviderJobID) != "" {
		record.ProviderJobID = strings.TrimSpace(progress.ProviderJobID)
		record.ProviderOperationName = strings.TrimSpace(progress.ProviderJobID)
	}
	if strings.TrimSpace(progress.RemoteStatus) != "" {
		record.RemoteStatus = strings.TrimSpace(progress.RemoteStatus)
	}
	if progress.PollCount > 0 {
		record.PollCount = progress.PollCount
	}
	if strings.TrimSpace(progress.LastPollAt) != "" {
		record.LastPollAt = strings.TrimSpace(progress.LastPollAt)
	}
	setVideoDiagnostic(record, "submit_request_url", progress.SubmitRequestURL)
	setVideoDiagnostic(record, "submit_request_body", progress.SubmitRequestBody)
	setVideoDiagnostic(record, "submit_response", progress.SubmitRawBody)
	setVideoDiagnostic(record, "poll_request_url", progress.PollRequestURL)
	setVideoDiagnostic(record, "poll_request_body", progress.PollRequestBody)
	setVideoDiagnostic(record, "last_poll_response", progress.LastPollRawBody)
	setVideoDiagnostic(record, "provider_error", progress.Error)
}

func applyVideoResultToRecord(record *videoJobRecord, result adapters.VideoResult) {
	if record == nil {
		return
	}
	if strings.TrimSpace(result.ProviderJobID) != "" {
		record.ProviderJobID = strings.TrimSpace(result.ProviderJobID)
		record.ProviderOperationName = strings.TrimSpace(result.ProviderJobID)
	}
	if strings.TrimSpace(result.RemoteStatus) != "" {
		record.RemoteStatus = strings.TrimSpace(result.RemoteStatus)
	}
	if result.PollCount > 0 {
		record.PollCount = result.PollCount
	}
	if strings.TrimSpace(result.LastPollAt) != "" {
		record.LastPollAt = strings.TrimSpace(result.LastPollAt)
	}
	setVideoDiagnostic(record, "submit_request_url", result.SubmitRequestURL)
	setVideoDiagnostic(record, "submit_request_body", result.SubmitRequestBody)
	setVideoDiagnostic(record, "submit_response", result.SubmitRawBody)
	setVideoDiagnostic(record, "poll_request_url", result.PollRequestURL)
	setVideoDiagnostic(record, "poll_request_body", result.PollRequestBody)
	setVideoDiagnostic(record, "last_poll_response", result.LastPollRawBody)
	setVideoDiagnostic(record, "raw_response", result.RawBody)
	setVideoDiagnostic(record, "provider_error", result.Error)
}

func (a *App) listVideoJobRecords(projectName, modelID string) ([]videoJobRecord, error) {
	root, err := a.videoJobsRoot(projectName)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []videoJobRecord{}, nil
		}
		return nil, err
	}
	out := make([]videoJobRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta := videoJobMetaPath(filepath.Join(root, entry.Name()))
		record, err := readVideoJobRecord(meta)
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

func (a *App) videoJobResponseForRecord(record videoJobRecord) videoJobResponse {
	resp := videoJobResponse{videoJobRecord: record}
	if record.ArtifactVideoPath != "" {
		resp.ArtifactBlobURL = buildBlobURL(record.ArtifactVideoPath)
	}
	if record.ProjectworkVideoPath != "" {
		resp.ProjectworkBlobURL = buildBlobURL(record.ProjectworkVideoPath)
	}
	if record.StartFramePath != "" {
		resp.StartFrameBlobURL = buildBlobURL(record.StartFramePath)
	}
	if record.EndFramePath != "" {
		resp.EndFrameBlobURL = buildBlobURL(record.EndFramePath)
	}
	for _, path := range record.ReferenceImagePaths {
		if strings.TrimSpace(path) != "" {
			resp.ReferenceImageBlobURLs = append(resp.ReferenceImageBlobURLs, buildBlobURL(path))
		}
	}
	return resp
}

func (a *App) handleVideoJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		projectName, err := a.requireActiveProject()
		if err != nil {
			http.Error(w, "Select an active project first.", http.StatusBadRequest)
			return
		}
		modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
		jobs, err := a.listVideoJobRecords(projectName, modelID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp := make([]videoJobResponse, 0, len(jobs))
		for _, record := range jobs {
			resp = append(resp, a.videoJobResponseForRecord(record))
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": resp})
	case http.MethodPost:
		a.handleCreateVideoJob(w, r)
	case http.MethodDelete:
		a.handleDeleteVideoJob(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDeleteVideoJob(w http.ResponseWriter, r *http.Request) {
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
	jobRoot, err := a.videoJobRoot(projectName, jobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	record, err := readVideoJobRecord(videoJobMetaPath(jobRoot))
	if err != nil {
		http.Error(w, "video job not found", http.StatusNotFound)
		return
	}
	status := strings.ToLower(strings.TrimSpace(record.Status))
	if status == "accepted" || status == "running" || status == "queued" || status == "submitted" || status == "processing" {
		http.Error(w, "Stop or wait for this video job before deleting its local record.", http.StatusConflict)
		return
	}
	if err := os.RemoveAll(jobRoot); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleCreateVideoJob(w http.ResponseWriter, r *http.Request) {
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
	duration := strings.TrimSpace(r.FormValue("duration"))
	aspectRatio := strings.TrimSpace(r.FormValue("aspectRatio"))
	resolution := strings.TrimSpace(r.FormValue("resolution"))
	outputFormat := strings.TrimSpace(r.FormValue("outputFormat"))
	fps := strings.TrimSpace(r.FormValue("fps"))
	quality := strings.TrimSpace(r.FormValue("quality"))
	if modelID == "" {
		http.Error(w, "modelId is required", http.StatusBadRequest)
		return
	}
	model, ok := a.findModel(modelID)
	if !ok {
		http.Error(w, "unknown model", http.StatusNotFound)
		return
	}
	if !modelIsVideoGeneration(model) {
		http.Error(w, "selected model is not configured for video generation", http.StatusBadRequest)
		return
	}
	if prompt == "" && !modelSupportsVideoPromptOnly(model) {
		http.Error(w, "this model requires a prompt", http.StatusBadRequest)
		return
	}
	duration, aspectRatio, resolution = resolvedVideoJobSettings(model, duration, aspectRatio, resolution)
	model, err = a.persistVideoModelSettings(modelID, duration, aspectRatio, resolution)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	job, err := a.createManualVideoJob(projectName, model, prompt, duration, aspectRatio, resolution, outputFormat, fps, quality, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, videoJobCreateResponse{OK: true, Job: a.videoJobResponseForRecord(job)})
}

func (a *App) createManualVideoJob(projectName string, model ModelConfig, prompt, duration, aspectRatio, resolution, outputFormat, fps, quality string, r *http.Request) (videoJobRecord, error) {
	jobID := buildVideoJobID()
	record, jobRoot, err := a.initVideoJobRecord(projectName, model, jobID, prompt, "manual", duration, aspectRatio, resolution, outputFormat, fps, quality)
	if err != nil {
		return videoJobRecord{}, err
	}
	startPath, startMeta, err := saveMultipartVideoInput(r, "startFrame", jobRoot, projectName, jobID, "start", modelSupportsVideoStartFrame(model))
	if err != nil {
		return videoJobRecord{}, err
	}
	endPath, endMeta, err := saveMultipartVideoInput(r, "endFrame", jobRoot, projectName, jobID, "end", modelSupportsVideoEndFrame(model))
	if err != nil {
		return videoJobRecord{}, err
	}
	referencePaths, referenceMetas, err := saveMultipartVideoReferenceInputs(r, jobRoot, projectName, jobID, modelSupportsVideoIngredients(model))
	if err != nil {
		return videoJobRecord{}, err
	}
	record.StartFramePath = startPath
	record.EndFramePath = endPath
	record.ReferenceImagePaths = referencePaths
	record.MetadataPath = filepath.ToSlash(filepath.Join("projects", projectName, "video_jobs", jobID, "meta", "job.json"))
	if err := writeVideoJobRecord(videoJobMetaPath(jobRoot), record); err != nil {
		return videoJobRecord{}, err
	}
	go a.executeVideoJobAsync(projectName, model, jobID, record, startMeta, endMeta, referenceMetas, "video:"+jobID)
	return record, nil
}

func saveMultipartVideoReferenceInputs(r *http.Request, jobRoot, projectName, jobID string, allowed bool) ([]string, []stagedVideoInput, error) {
	paths := []string{}
	metas := []stagedVideoInput{}
	for i := 0; i < 3; i++ {
		field := fmt.Sprintf("referenceImage%d", i)
		path, meta, err := saveMultipartVideoInput(r, field, jobRoot, projectName, jobID, fmt.Sprintf("reference_%d", i+1), allowed)
		if err != nil {
			return nil, nil, err
		}
		if meta == nil {
			continue
		}
		paths = append(paths, path)
		metas = append(metas, *meta)
	}
	return paths, metas, nil
}

func saveMultipartVideoInput(r *http.Request, field, jobRoot, projectName, jobID, prefix string, allowed bool) (string, *stagedVideoInput, error) {
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
	rel := filepath.ToSlash(filepath.Join("projects", projectName, "video_jobs", jobID, "inputs", prefix+ext))
	full := filepath.Join(jobRoot, "inputs", prefix+ext)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", nil, err
	}
	return rel, &stagedVideoInput{Path: rel, Name: filepath.Base(rel), MIMEType: contentType, Data: data}, nil
}

func (a *App) initVideoJobRecord(projectName string, model ModelConfig, jobID, prompt, promptSource, duration, aspectRatio, resolution, outputFormat, fps, quality string) (videoJobRecord, string, error) {
	jobRoot, err := a.videoJobRoot(projectName, jobID)
	if err != nil {
		return videoJobRecord{}, "", err
	}
	if err := os.MkdirAll(filepath.Join(jobRoot, "inputs"), 0o755); err != nil {
		return videoJobRecord{}, "", err
	}
	if err := os.MkdirAll(filepath.Join(jobRoot, "artifacts"), 0o755); err != nil {
		return videoJobRecord{}, "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	record := videoJobRecord{
		JobID:          jobID,
		ProjectName:    projectName,
		ModelID:        modelIDString(model.ID),
		ModelLabel:     model.Label,
		Provider:       model.Provider,
		Adapter:        model.Adapter,
		ModelName:      model.ModelName,
		Status:         "accepted",
		Prompt:         strings.TrimSpace(prompt),
		PromptSource:   strings.TrimSpace(promptSource),
		Duration:       strings.TrimSpace(duration),
		AspectRatio:    strings.TrimSpace(aspectRatio),
		Resolution:     strings.TrimSpace(resolution),
		OutputFormat:   strings.TrimSpace(outputFormat),
		FPS:            strings.TrimSpace(fps),
		Quality:        strings.TrimSpace(quality),
		CreatedAt:      now,
		UpdatedAt:      now,
		PromotionState: "not_promoted",
		MetadataPath:   filepath.ToSlash(filepath.Join("projects", projectName, "video_jobs", jobID, "meta", "job.json")),
	}
	return record, jobRoot, nil
}

func (a *App) executeVideoJobAsync(projectName string, model ModelConfig, jobID string, record videoJobRecord, startMeta, endMeta *stagedVideoInput, referenceMetas []stagedVideoInput, cancelKey string) {
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.setActiveCancelLocked(cancelKey, projectName, jobID, cancel)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.clearActiveCancelLocked(cancelKey, jobID)
		a.mu.Unlock()
	}()
	jobRoot, err := a.videoJobRoot(projectName, jobID)
	if err != nil {
		return
	}
	record.Status = "running"
	record.Error = ""
	_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
	req := adapters.VideoRequest{JobID: jobID, Prompt: record.Prompt, Duration: record.Duration, AspectRatio: record.AspectRatio, Resolution: record.Resolution, OutputFormat: record.OutputFormat, FPS: record.FPS, Quality: record.Quality}
	if startMeta != nil {
		req.StartFrame = &adapters.VideoBinary{Name: startMeta.Name, MIMEType: startMeta.MIMEType, Data: startMeta.Data}
	}
	if endMeta != nil {
		req.EndFrame = &adapters.VideoBinary{Name: endMeta.Name, MIMEType: endMeta.MIMEType, Data: endMeta.Data}
	}
	for _, meta := range referenceMetas {
		if len(meta.Data) > 0 {
			req.ReferenceImages = append(req.ReferenceImages, adapters.VideoBinary{Name: meta.Name, MIMEType: meta.MIMEType, Data: meta.Data})
		}
	}
	req.PollUpdate = func(progress adapters.VideoProgress) {
		applyVideoProgressToRecord(&record, progress)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
	}
	result, err := adapters.ExecuteVideo(ctx, toAdapterModelConfig(model), req)
	applyVideoResultToRecord(&record, result)
	if err != nil {
		if ctx.Err() != nil {
			record.Status = "stopped"
			record.Error = "stopped locally"
		} else {
			record.Status = "failed"
			record.Error = strings.TrimSpace(err.Error())
		}
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		a.logf(modelIDString(model.ID), "warn", "Video job %s ended with status=%s error=%s", jobID, record.Status, record.Error)
		return
	}
	artifactName, fileInfo := resolveVideoOutputFilename(result, "video")
	applyVideoFileDiagnostics(&record, result, fileInfo)
	artifactPath := filepath.ToSlash(filepath.Join("projects", projectName, "video_jobs", jobID, "artifacts", artifactName))
	artifactFull, err := safeJoin(a.cfg.WorkRoot, artifactPath)
	if err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		return
	}
	if err := os.MkdirAll(filepath.Dir(artifactFull), 0o755); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		return
	}
	if err := os.WriteFile(artifactFull, result.VideoData, 0o644); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		return
	}
	record.Status = "completed"
	record.ArtifactVideoPath = artifactPath
	record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	record.Error = ""
	if err := a.stageVideoArtifactsToProjectwork(model, projectName, &record, result, artifactName); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		return
	}
	_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
	a.logf(modelIDString(model.ID), "info", "Completed video job %s for project %s and saved output to %s", jobID, projectName, record.ProjectworkVideoPath)
}

func (a *App) buildWaveVideoInputs(projectName string, contextFiles []string, mediaInputRoles map[string]string) (*stagedVideoInput, *stagedVideoInput, []string, error) {
	projectworkRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return nil, nil, nil, err
	}
	orderedPaths := []string{}
	itemsByPath := map[string]*stagedVideoInput{}
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
		normalized := filepath.ToSlash(rel)
		itemsByPath[normalized] = &stagedVideoInput{Path: normalized, Name: filepath.Base(rel), MIMEType: contentType, Data: data}
		orderedPaths = append(orderedPaths, normalized)
	}
	if len(orderedPaths) == 0 {
		return nil, nil, nil, nil
	}
	used := []string{}
	usedSet := map[string]bool{}
	appendUsed := func(path string) {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" || usedSet[path] {
			return
		}
		usedSet[path] = true
		used = append(used, path)
	}
	var startFrame *stagedVideoInput
	var endFrame *stagedVideoInput
	for _, path := range orderedPaths {
		role := normalizeMediaInputRole(mediaInputRoles[path])
		switch role {
		case "start_frame":
			if startFrame == nil {
				startFrame = itemsByPath[path]
				appendUsed(path)
			}
		case "end_frame":
			if endFrame == nil && (startFrame == nil || startFrame.Path != path) {
				endFrame = itemsByPath[path]
				appendUsed(path)
			}
		}
	}
	for _, path := range orderedPaths {
		item := itemsByPath[path]
		if startFrame == nil {
			startFrame = item
			appendUsed(path)
			continue
		}
		if endFrame == nil && startFrame.Path != path {
			endFrame = item
			appendUsed(path)
			break
		}
	}
	return startFrame, endFrame, used, nil
}

func (a *App) stageWaveVideoInput(projectName, jobID, prefix string, input *stagedVideoInput) (string, error) {
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
	rel := filepath.ToSlash(filepath.Join("projects", projectName, "video_jobs", jobID, "inputs", prefix+ext))
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

func (a *App) runVideoModelRequest(model ModelConfig, projectName, executionID, prompt string, contextFiles []string, mediaInputRoles map[string]string) modelRunResult {
	result := modelRunResult{ModelID: modelIDString(model.ID), ModelLabel: model.Label}
	startFrame, endFrame, usedContext, err := a.buildWaveVideoInputs(projectName, contextFiles, mediaInputRoles)
	if err != nil {
		result.Err = err
		return result
	}
	jobID := buildVideoJobID()
	duration, aspectRatio, resolution := resolvedVideoJobSettings(model, model.VideoDuration, model.VideoAspectRatio, model.VideoResolution)
	record, jobRoot, err := a.initVideoJobRecord(projectName, model, jobID, prompt, "wave", duration, aspectRatio, resolution, model.VideoOutputFormat, model.VideoFPS, model.VideoQuality)
	if err != nil {
		result.Err = err
		return result
	}
	record.SourceContextFiles = append([]string(nil), usedContext...)
	record.StartFramePath, _ = a.stageWaveVideoInput(projectName, jobID, "start", startFrame)
	record.EndFramePath, _ = a.stageWaveVideoInput(projectName, jobID, "end", endFrame)
	if err := writeVideoJobRecord(videoJobMetaPath(jobRoot), record); err != nil {
		result.Err = err
		return result
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.setActiveCancelLocked(modelIDString(model.ID), projectName, executionID, cancel)
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.clearActiveCancelLocked(modelIDString(model.ID), executionID)
		a.mu.Unlock()
	}()
	record.Status = "running"
	_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
	videoReq := adapters.VideoRequest{JobID: jobID, Prompt: prompt, Duration: record.Duration, AspectRatio: record.AspectRatio, Resolution: record.Resolution, OutputFormat: record.OutputFormat, FPS: record.FPS, Quality: record.Quality}
	if startFrame != nil {
		videoReq.StartFrame = &adapters.VideoBinary{Name: startFrame.Name, MIMEType: startFrame.MIMEType, Data: startFrame.Data}
	}
	if endFrame != nil {
		videoReq.EndFrame = &adapters.VideoBinary{Name: endFrame.Name, MIMEType: endFrame.MIMEType, Data: endFrame.Data}
	}
	videoReq.PollUpdate = func(progress adapters.VideoProgress) {
		applyVideoProgressToRecord(&record, progress)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
	}
	videoResult, err := adapters.ExecuteVideo(ctx, toAdapterModelConfig(model), videoReq)
	applyVideoResultToRecord(&record, videoResult)
	if err != nil {
		if ctx.Err() != nil {
			record.Status = "stopped"
			record.Error = "stopped locally"
		} else {
			record.Status = "failed"
			record.Error = strings.TrimSpace(err.Error())
		}
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		result.Err = errors.New(record.Error)
		return result
	}
	artifactName, fileInfo := resolveVideoOutputFilename(videoResult, "video")
	applyVideoFileDiagnostics(&record, videoResult, fileInfo)
	artifactPath := filepath.ToSlash(filepath.Join("projects", projectName, "video_jobs", jobID, "artifacts", artifactName))
	artifactFull, err := safeJoin(a.cfg.WorkRoot, artifactPath)
	if err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		result.Err = err
		return result
	}
	if err := os.MkdirAll(filepath.Dir(artifactFull), 0o755); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		result.Err = err
		return result
	}
	if err := os.WriteFile(artifactFull, videoResult.VideoData, 0o644); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		result.Err = err
		return result
	}
	record.Status = "completed"
	record.ArtifactVideoPath = artifactPath
	record.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	record.Error = ""
	if err := a.stageVideoArtifactsToProjectwork(model, projectName, &record, videoResult, artifactName); err != nil {
		record.Status = "failed"
		record.Error = err.Error()
		_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
		result.Err = err
		return result
	}
	_ = writeVideoJobRecord(videoJobMetaPath(jobRoot), record)
	result.Valid = true
	return result
}

func (a *App) stageVideoArtifactsToProjectwork(model ModelConfig, projectName string, record *videoJobRecord, result adapters.VideoResult, preferredName string) error {
	if record == nil {
		return errors.New("missing video job record")
	}
	name := sanitizeImportedFilename(preferredName)
	if name == "" {
		var info videoOutputFileInfo
		name, info = resolveVideoOutputFilename(result, "video")
		applyVideoFileDiagnostics(record, result, info)
	}
	data := result.VideoData
	if len(data) == 0 && strings.TrimSpace(record.ArtifactVideoPath) != "" {
		artifactFull, err := safeJoin(a.cfg.WorkRoot, record.ArtifactVideoPath)
		if err != nil {
			return err
		}
		data, err = os.ReadFile(artifactFull)
		if err != nil {
			return err
		}
	}
	if len(data) == 0 {
		return errors.New("video job completed without video data")
	}
	targetRel, targetFull, err := a.nextMediaProjectworkOutputRoot(projectName, model)
	if err != nil {
		return err
	}
	targetFile, err := safeJoin(targetFull, name)
	if err != nil {
		return err
	}
	if err := os.WriteFile(targetFile, data, 0o644); err != nil {
		return err
	}
	record.ProjectworkVideoPath = filepath.ToSlash(filepath.Join(targetRel, name))
	record.PromotionState = "auto_saved"
	record.PromotedAt = time.Now().UTC().Format(time.RFC3339)
	if strings.TrimSpace(result.RawBody) != "" {
		if err := os.WriteFile(filepath.Join(targetFull, "provider_response.json"), []byte(result.RawBody), 0o644); err != nil {
			return err
		}
	}
	return writeProjectworkJSON(filepath.Join(targetFull, "job.json"), record)
}

func (a *App) handleVideoJobPromote(w http.ResponseWriter, r *http.Request) {
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
	req.JobID = strings.TrimSpace(req.JobID)
	if req.JobID == "" {
		http.Error(w, "jobId is required", http.StatusBadRequest)
		return
	}
	jobRoot, err := a.videoJobRoot(projectName, req.JobID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	record, err := readVideoJobRecord(videoJobMetaPath(jobRoot))
	if err != nil {
		http.Error(w, "video job not found", http.StatusNotFound)
		return
	}
	if record.ArtifactVideoPath == "" || record.Status != "completed" {
		http.Error(w, "video job is not ready to promote", http.StatusBadRequest)
		return
	}
	src, err := safeJoin(a.cfg.WorkRoot, record.ArtifactVideoPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ext := strings.ToLower(filepath.Ext(src))
	if ext == "" {
		ext = ".mp4"
	}
	model, ok := a.findModel(record.ModelID)
	if !ok {
		http.Error(w, "video job model not found", http.StatusNotFound)
		return
	}
	targetRelRoot, targetRoot, err := a.nextMediaProjectworkOutputRoot(projectName, model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	targetName := sanitizeImportedFilename(filepath.Base(src))
	if targetName == "" {
		targetName = "video" + ext
	}
	targetFull := filepath.Join(targetRoot, targetName)
	if err := os.WriteFile(targetFull, data, 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	record.ProjectworkVideoPath = filepath.ToSlash(filepath.Join(targetRelRoot, targetName))
	record.PromotionState = "auto_saved"
	record.PromotedAt = time.Now().UTC().Format(time.RFC3339)
	if err := writeVideoJobRecord(videoJobMetaPath(jobRoot), record); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if record.ModelID != "" {
		a.setPendingMergeCount(projectName, record.ModelID, 0)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": a.videoJobResponseForRecord(record), "projectworkPath": record.ProjectworkVideoPath})
}
