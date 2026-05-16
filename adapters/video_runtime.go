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
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type VideoBinary struct {
	Name     string
	MIMEType string
	Data     []byte
}

type VideoRequest struct {
	JobID        string
	Prompt       string
	StartFrame   *VideoBinary
	EndFrame     *VideoBinary
	Duration     string
	AspectRatio  string
	Resolution   string
	OutputFormat string
	FPS          string
	Quality      string
	PollInterval time.Duration
}

type VideoResult struct {
	Status        string
	ProviderJobID string
	RemoteStatus  string
	VideoData     []byte
	VideoMIMEType string
	VideoFilename string
	RawBody       string
	Error         string
}

func ExecuteVideo(ctx context.Context, model ModelConfig, req VideoRequest) (VideoResult, error) {
	switch normalizedAdapterName(model) {
	case "veo_video":
		return executeConfiguredVideo(ctx, model, req, videoAdapterDefaults{
			submitPath:         "/v1beta/models/{model}:predictLongRunning",
			pollPathTemplate:   "{job_id}",
			jobIDPath:          "name",
			statusPath:         "done",
			successValue:       "true",
			errorPath:          "error.message",
			videoURLPath:       "response.generatedVideos.0.video.uri",
			videoMimePath:      "response.generatedVideos.0.video.mimeType",
			submitBodyTemplate: map[string]any{"instances": []any{map[string]any{}}, "parameters": map[string]any{}},
			promptFieldPath:    "instances.0.prompt",
			startFieldPath:     "instances.0.image.bytesBase64Encoded",
			startMimeFieldPath: "instances.0.image.mimeType",
			endFieldPath:       "instances.0.lastFrame.bytesBase64Encoded",
			endMimeFieldPath:   "instances.0.lastFrame.mimeType",
			durationFieldPath:  "parameters.durationSeconds",
			aspectFieldPath:    "parameters.aspectRatio",
			resFieldPath:       "parameters.resolution",
		})
	case "kling_video":
		return executeConfiguredVideo(ctx, model, req, videoAdapterDefaults{
			submitPath:         "/v1/videos/generations",
			pollPathTemplate:   "/v1/videos/generations/{job_id}",
			jobIDPath:          "data.id",
			statusPath:         "data.status",
			successValue:       "succeeded",
			errorPath:          "data.error.message",
			videoURLPath:       "data.output.url",
			videoMimePath:      "data.output.mime_type",
			submitBodyTemplate: map[string]any{},
			promptFieldPath:    "prompt",
			startFieldPath:     "start_frame.data",
			startMimeFieldPath: "start_frame.mime_type",
			endFieldPath:       "end_frame.data",
			endMimeFieldPath:   "end_frame.mime_type",
			durationFieldPath:  "duration",
			aspectFieldPath:    "aspect_ratio",
			resFieldPath:       "resolution",
			modelFieldPath:     "model",
		})
	case "sora_video":
		return executeSoraVideo(ctx, model, req)
	case "comfyui_ltx_video":
		return executeComfyUILTXVideo(ctx, model, req)
	default:
		return VideoResult{}, fmt.Errorf("adapter %q is not a video-generation adapter", strings.TrimSpace(model.Adapter))
	}
}

type videoAdapterDefaults struct {
	submitPath         string
	pollPathTemplate   string
	jobIDPath          string
	statusPath         string
	successValue       string
	errorPath          string
	videoURLPath       string
	videoMimePath      string
	submitBodyTemplate map[string]any
	promptFieldPath    string
	startFieldPath     string
	startMimeFieldPath string
	endFieldPath       string
	endMimeFieldPath   string
	durationFieldPath  string
	aspectFieldPath    string
	resFieldPath       string
	formatFieldPath    string
	fpsFieldPath       string
	qualityFieldPath   string
	modelFieldPath     string
}

func executeComfyUILTXVideo(ctx context.Context, model ModelConfig, req VideoRequest) (VideoResult, error) {
	prepared := model
	prepared.AuthType = normalizedAuthType(prepared.AuthType, "none")
	if strings.TrimSpace(prepared.BaseURL) == "" {
		prepared.BaseURL = "http://127.0.0.1:8188"
	}
	if strings.TrimSpace(prepared.APIPath) == "" {
		prepared.APIPath = "/prompt"
	}
	workflow, err := buildComfyWorkflow(prepared)
	if err != nil {
		return VideoResult{}, err
	}
	if req.StartFrame != nil && len(req.StartFrame.Data) > 0 {
		filename, err := uploadComfyUIImage(ctx, prepared, req.StartFrame)
		if err != nil {
			return VideoResult{}, err
		}
		workflow = applyComfyPlaceholders(workflow, map[string]string{
			"__AGENTGO_START_IMAGE__": filename,
			"__AGENTGO_IMAGE__":       filename,
			"__AGENTGO_INPUT_IMAGE__": filename,
		})
		setComfyNodeInput(workflow, providerOptionString(prepared, "image_node_id", ""), providerOptionString(prepared, "image_input_name", "image"), filename)
	}
	workflow = applyComfyPlaceholders(workflow, map[string]string{
		"__AGENTGO_PROMPT__":        strings.TrimSpace(req.Prompt),
		"__AGENTGO_JOB_ID__":        strings.TrimSpace(req.JobID),
		"__AGENTGO_PREFIX__":        strings.TrimSpace(req.JobID),
		"__AGENTGO_DURATION__":      strings.TrimSpace(req.Duration),
		"__AGENTGO_ASPECT_RATIO__":  strings.TrimSpace(req.AspectRatio),
		"__AGENTGO_RESOLUTION__":    strings.TrimSpace(req.Resolution),
		"__AGENTGO_OUTPUT_FORMAT__": strings.TrimSpace(req.OutputFormat),
		"__AGENTGO_FPS__":           strings.TrimSpace(req.FPS),
		"__AGENTGO_QUALITY__":       strings.TrimSpace(req.Quality),
	})
	setComfyNodeInput(workflow, providerOptionString(prepared, "prompt_node_id", ""), providerOptionString(prepared, "prompt_input_name", "text"), strings.TrimSpace(req.Prompt))
	if strings.TrimSpace(req.Duration) != "" {
		setComfyNodeInput(workflow, providerOptionString(prepared, "duration_node_id", ""), providerOptionString(prepared, "duration_input_name", "seconds"), strings.TrimSpace(req.Duration))
	}
	if strings.TrimSpace(req.AspectRatio) != "" {
		setComfyNodeInput(workflow, providerOptionString(prepared, "aspect_ratio_node_id", ""), providerOptionString(prepared, "aspect_ratio_input_name", "aspect_ratio"), strings.TrimSpace(req.AspectRatio))
	}
	if strings.TrimSpace(req.Resolution) != "" {
		setComfyNodeInput(workflow, providerOptionString(prepared, "resolution_node_id", ""), providerOptionString(prepared, "resolution_input_name", "resolution"), strings.TrimSpace(req.Resolution))
	}
	if strings.TrimSpace(req.OutputFormat) != "" {
		setComfyNodeInput(workflow, providerOptionString(prepared, "output_format_node_id", ""), providerOptionString(prepared, "output_format_input_name", "format"), strings.TrimSpace(req.OutputFormat))
	}
	if strings.TrimSpace(req.FPS) != "" {
		setComfyNodeInput(workflow, providerOptionString(prepared, "fps_node_id", ""), providerOptionString(prepared, "fps_input_name", "fps"), strings.TrimSpace(req.FPS))
	}
	if strings.TrimSpace(req.Quality) != "" {
		setComfyNodeInput(workflow, providerOptionString(prepared, "quality_node_id", ""), providerOptionString(prepared, "quality_input_name", "quality"), strings.TrimSpace(req.Quality))
	}

	clientID := strings.TrimSpace(req.JobID)
	if clientID == "" {
		clientID = fmt.Sprintf("agentgo-%d", time.Now().UTC().UnixNano())
	}
	payloadMap := map[string]any{"prompt": workflow, "client_id": clientID}
	if front := strings.TrimSpace(providerOptionString(prepared, "comfy_front", "")); front != "" {
		payloadMap["front"] = front
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return VideoResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, modelEndpoint(prepared), bytes.NewReader(payload))
	if err != nil {
		return VideoResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, err := doAdapterRequest(request, modelTimeout(prepared, 60*time.Minute))
	if err != nil {
		return VideoResult{}, err
	}
	if statusCode >= 300 {
		return VideoResult{RawBody: string(respBody)}, fmt.Errorf("video submit returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return VideoResult{RawBody: string(respBody)}, fmt.Errorf("video submit response was not valid JSON")
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, providerOptionString(prepared, "video_job_id_path", "prompt_id"))))
	if jobID == "" {
		return VideoResult{RawBody: string(respBody)}, errors.New("video submit response did not include a prompt_id")
	}
	pollEvery := req.PollInterval
	if pollEvery <= 0 {
		pollEvery = 4 * time.Second
	}
	lastBody := string(respBody)
	for {
		if ctx.Err() != nil {
			_ = interruptComfyUI(context.Background(), prepared)
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody, Status: "stopped"}, ctx.Err()
		}
		historyURL := buildVideoPollURL(prepared, jobID, providerOptionString(prepared, "video_poll_path_template", "/history/{job_id}"))
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, historyURL, nil)
		if err != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		applyModelHeaders(pollReq, prepared)
		pollBody, pollStatus, pollCode, err := doAdapterRequest(pollReq, modelTimeout(prepared, 60*time.Minute))
		if err != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		lastBody = string(pollBody)
		if pollCode >= 300 {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("video polling returned %s: %s", pollStatus, strings.TrimSpace(lastBody))
		}
		var historyPayload any
		if err := json.Unmarshal(pollBody, &historyPayload); err != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("video poll response was not valid JSON")
		}
		entry := lookupPath(historyPayload, jobID)
		if entry == nil {
			time.Sleep(pollEvery)
			continue
		}
		remoteStatus := strings.ToLower(strings.TrimSpace(asString(lookupPath(entry, providerOptionString(prepared, "comfy_history_status_path", "status.status_str")))))
		if remoteStatus == "" {
			if completed, ok := lookupPath(entry, "status.completed").(bool); ok && completed {
				remoteStatus = "completed"
			}
		}
		if strings.Contains(remoteStatus, "error") || strings.Contains(remoteStatus, "fail") {
			errText := strings.TrimSpace(asString(lookupPath(entry, providerOptionString(prepared, "comfy_history_error_path", "status.messages.0.1"))))
			if errText == "" {
				errText = strings.TrimSpace(asString(lookupPath(entry, providerOptionString(prepared, "comfy_history_error_path_alt", "status.messages.0.message"))))
			}
			if errText == "" {
				errText = remoteStatus
			}
			return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody, Error: errText, Status: "failed"}, errors.New(errText)
		}
		file, err := firstComfyOutputFile(entry)
		if err == nil {
			data, mimeType, filename, err := downloadComfyOutputFile(ctx, prepared, file)
			if err != nil {
				return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody}, err
			}
			return VideoResult{Status: "completed", ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody, VideoData: data, VideoMIMEType: mimeType, VideoFilename: filename}, nil
		}
		time.Sleep(pollEvery)
	}
}

func buildComfyWorkflow(model ModelConfig) (map[string]any, error) {
	if workflowObject := providerOptionMap(model, "workflow"); len(workflowObject) > 0 {
		cloned, _ := deepCloneAny(workflowObject).(map[string]any)
		if cloned != nil {
			return cloned, nil
		}
	}
	if raw := providerOptionString(model, "workflow_json", ""); strings.TrimSpace(raw) != "" {
		var workflow map[string]any
		if err := json.Unmarshal([]byte(raw), &workflow); err != nil {
			return nil, fmt.Errorf("workflow_json is not valid JSON: %w", err)
		}
		return workflow, nil
	}
	return nil, errors.New("provider_options.workflow or provider_options.workflow_json is required for ComfyUI LTX")
}

func applyComfyPlaceholders(value any, replacements map[string]string) map[string]any {
	mapped, _ := replaceComfyPlaceholders(value, replacements).(map[string]any)
	if mapped == nil {
		mapped = map[string]any{}
	}
	return mapped
}

func replaceComfyPlaceholders(value any, replacements map[string]string) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, child := range typed {
			out[k] = replaceComfyPlaceholders(child, replacements)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = replaceComfyPlaceholders(child, replacements)
		}
		return out
	case string:
		updated := typed
		for key, replacement := range replacements {
			updated = strings.ReplaceAll(updated, key, replacement)
		}
		return updated
	default:
		return typed
	}
}

func setComfyNodeInput(workflow map[string]any, nodeID, inputName string, value any) {
	nodeID = strings.TrimSpace(nodeID)
	inputName = strings.TrimSpace(inputName)
	if workflow == nil || nodeID == "" || inputName == "" || value == nil {
		return
	}
	node, ok := workflow[nodeID].(map[string]any)
	if !ok || node == nil {
		return
	}
	inputs, ok := node["inputs"].(map[string]any)
	if !ok || inputs == nil {
		inputs = map[string]any{}
		node["inputs"] = inputs
	}
	inputs[inputName] = value
}

func uploadComfyUIImage(ctx context.Context, model ModelConfig, file *VideoBinary) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	filename := defaultVideoFilename(file.Name, ".png")
	part, err := writer.CreateFormFile("image", filepath.Base(filename))
	if err != nil {
		return "", err
	}
	if _, err := part.Write(file.Data); err != nil {
		return "", err
	}
	_ = writer.WriteField("type", providerOptionString(model, "comfy_upload_type", "input"))
	_ = writer.WriteField("overwrite", providerOptionString(model, "comfy_upload_overwrite", "true"))
	if subfolder := strings.TrimSpace(providerOptionString(model, "comfy_upload_subfolder", "")); subfolder != "" {
		_ = writer.WriteField("subfolder", subfolder)
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	uploadURL := buildVideoPollURL(model, "", providerOptionString(model, "comfy_upload_path", "/upload/image"))
	uploadReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		return "", err
	}
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	applyModelHeaders(uploadReq, model)
	respBody, status, statusCode, err := doAdapterRequest(uploadReq, modelTimeout(model, 20*time.Minute))
	if err != nil {
		return "", err
	}
	if statusCode >= 300 {
		return "", fmt.Errorf("image upload returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var payload map[string]any
	if err := json.Unmarshal(respBody, &payload); err == nil {
		if name := strings.TrimSpace(asString(payload["name"])); name != "" {
			return name, nil
		}
		if name := strings.TrimSpace(asString(payload["filename"])); name != "" {
			return name, nil
		}
	}
	return filepath.Base(filename), nil
}

func interruptComfyUI(ctx context.Context, model ModelConfig) error {
	interruptURL := buildVideoPollURL(model, "", providerOptionString(model, "comfy_interrupt_path", "/interrupt"))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, interruptURL, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	applyModelHeaders(req, model)
	_, _, _, err = doAdapterRequest(req, modelTimeout(model, 10*time.Second))
	return err
}

func firstComfyOutputFile(entry any) (map[string]any, error) {
	outputs, ok := lookupPath(entry, "outputs").(map[string]any)
	if !ok || len(outputs) == 0 {
		return nil, errors.New("history entry does not include outputs yet")
	}
	var fallback map[string]any
	for _, nodeValue := range outputs {
		nodeMap, ok := nodeValue.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range []string{"gifs", "videos", "files", "images"} {
			items, ok := nodeMap[key].([]any)
			if !ok || len(items) == 0 {
				continue
			}
			for _, item := range items {
				file, ok := item.(map[string]any)
				if !ok {
					continue
				}
				name := strings.ToLower(strings.TrimSpace(asString(file["filename"])))
				if strings.HasSuffix(name, ".mp4") || strings.HasSuffix(name, ".mov") || strings.HasSuffix(name, ".webm") || strings.HasSuffix(name, ".gif") || strings.HasSuffix(name, ".mkv") || key == "gifs" || key == "videos" {
					return file, nil
				}
				if fallback == nil {
					fallback = file
				}
			}
		}
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, errors.New("no output files found in ComfyUI history")
}

func downloadComfyOutputFile(ctx context.Context, model ModelConfig, file map[string]any) ([]byte, string, string, error) {
	filename := strings.TrimSpace(asString(file["filename"]))
	if filename == "" {
		return nil, "", "", errors.New("ComfyUI output file is missing filename")
	}
	params := url.Values{}
	params.Set("filename", filename)
	if subfolder := strings.TrimSpace(asString(file["subfolder"])); subfolder != "" {
		params.Set("subfolder", subfolder)
	}
	if folderType := strings.TrimSpace(asString(file["type"])); folderType != "" {
		params.Set("type", folderType)
	}
	viewURL := buildVideoPollURL(model, "", providerOptionString(model, "comfy_view_path", "/view"))
	if strings.Contains(viewURL, "?") {
		viewURL += "&" + params.Encode()
	} else {
		viewURL += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, viewURL, nil)
	if err != nil {
		return nil, "", "", err
	}
	applyModelHeaders(req, model)
	body, status, statusCode, err := doAdapterRequest(req, modelTimeout(model, 20*time.Minute))
	if err != nil {
		return nil, "", "", err
	}
	if statusCode >= 300 {
		return nil, "", "", fmt.Errorf("video download returned %s", status)
	}
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	if mimeType == "" {
		mimeType = http.DetectContentType(body)
	}
	return body, mimeType, filepath.Base(filename), nil
}

func executeSoraVideo(ctx context.Context, model ModelConfig, req VideoRequest) (VideoResult, error) {
	prepared := model
	authType := normalizedAuthType(prepared.AuthType, "bearer")
	prepared.AuthType = authType
	if authType == "bearer" || authType == "header_key" {
		prepared.APIKey = resolveConfiguredAPIKey(prepared)
		if strings.TrimSpace(prepared.APIKey) == "" {
			return VideoResult{}, errors.New("missing API key for this model")
		}
	}
	if strings.TrimSpace(prepared.APIPath) == "" {
		prepared.APIPath = "/v1/videos"
	}
	endpoint := strings.TrimSpace(modelEndpoint(prepared))
	if endpoint == "" {
		return VideoResult{}, errors.New("missing Sora submit endpoint")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("prompt", strings.TrimSpace(req.Prompt))
	modelName := strings.TrimSpace(prepared.ModelName)
	if modelName == "" {
		modelName = providerOptionString(prepared, "video_model_name", "sora-2")
	}
	_ = writer.WriteField("model", modelName)
	if seconds := normalizeSoraSeconds(req.Duration); seconds != "" {
		_ = writer.WriteField("seconds", seconds)
	}
	if size := normalizeSoraSize(req.AspectRatio, req.Resolution); size != "" {
		_ = writer.WriteField("size", size)
	}
	if req.StartFrame != nil && len(req.StartFrame.Data) > 0 {
		part, err := writer.CreateFormFile("input_reference", filepath.Base(defaultVideoFilename(req.StartFrame.Name, ".png")))
		if err != nil {
			return VideoResult{}, err
		}
		if _, err := part.Write(req.StartFrame.Data); err != nil {
			return VideoResult{}, err
		}
	}
	for key, value := range providerOptionMap(prepared, "extra_fields") {
		if strings.TrimSpace(key) == "" || value == nil {
			continue
		}
		_ = writer.WriteField(strings.TrimSpace(key), strings.TrimSpace(fmt.Sprint(value)))
	}
	if err := writer.Close(); err != nil {
		return VideoResult{}, err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return VideoResult{}, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, err := doAdapterRequest(request, modelTimeout(prepared, 20*time.Minute))
	if err != nil {
		return VideoResult{}, err
	}
	if statusCode >= 300 {
		return VideoResult{RawBody: string(respBody)}, fmt.Errorf("video submit returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return VideoResult{RawBody: string(respBody)}, fmt.Errorf("video submit response was not valid JSON")
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, "id")))
	if jobID == "" {
		return VideoResult{RawBody: string(respBody)}, errors.New("video submit response did not include a job id")
	}
	pollEvery := req.PollInterval
	if pollEvery <= 0 {
		pollEvery = 8 * time.Second
	}
	lastBody := string(respBody)
	for {
		if ctx.Err() != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody, Status: "stopped"}, ctx.Err()
		}
		pollURL := buildVideoPollURL(prepared, jobID, providerOptionString(prepared, "video_poll_path_template", "/v1/videos/{job_id}"))
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		applyModelHeaders(pollReq, prepared)
		pollBody, pollStatus, pollCode, err := doAdapterRequest(pollReq, modelTimeout(prepared, 20*time.Minute))
		if err != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		lastBody = string(pollBody)
		if pollCode >= 300 {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("video polling returned %s: %s", pollStatus, strings.TrimSpace(lastBody))
		}
		var pollPayload any
		if err := json.Unmarshal(pollBody, &pollPayload); err != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("video poll response was not valid JSON")
		}
		remoteStatus := strings.ToLower(strings.TrimSpace(asString(lookupPath(pollPayload, "status"))))
		switch remoteStatus {
		case "completed":
			contentURL := buildVideoPollURL(prepared, jobID, providerOptionString(prepared, "video_content_path_template", "/v1/videos/{job_id}/content?variant=video"))
			contentReq, err := http.NewRequestWithContext(ctx, http.MethodGet, contentURL, nil)
			if err != nil {
				return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody}, err
			}
			applyModelHeaders(contentReq, prepared)
			contentBody, contentStatus, contentCode, err := doAdapterRequest(contentReq, modelTimeout(prepared, 20*time.Minute))
			if err != nil {
				return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody}, err
			}
			if contentCode >= 300 {
				return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody}, fmt.Errorf("video download returned %s", contentStatus)
			}
			videoMime := http.DetectContentType(contentBody)
			filename := providerOptionString(prepared, "video_result_filename", "sora_output"+extensionForMIME(videoMime))
			return VideoResult{Status: "completed", ProviderJobID: jobID, RemoteStatus: remoteStatus, VideoData: contentBody, VideoMIMEType: videoMime, VideoFilename: filename, RawBody: lastBody}, nil
		case "failed":
			errText := strings.TrimSpace(asString(lookupPath(pollPayload, "error.message")))
			if errText == "" {
				errText = "video generation failed"
			}
			return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody, Error: errText, Status: "failed"}, errors.New(errText)
		case "queued", "in_progress", "":
			time.Sleep(pollEvery)
			continue
		default:
			time.Sleep(pollEvery)
			continue
		}
	}
}

func normalizeSoraSeconds(raw string) string {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return ""
	}
	clean = strings.TrimSuffix(clean, "s")
	clean = strings.TrimSpace(clean)
	switch clean {
	case "4", "8", "12":
		return clean
	}
	if n, err := strconv.Atoi(clean); err == nil {
		if n <= 4 {
			return "4"
		}
		if n <= 8 {
			return "8"
		}
		return "12"
	}
	return ""
}

func normalizeSoraSize(aspectRatio, resolution string) string {
	allowed := map[string]bool{"720x1280": true, "1280x720": true, "1024x1792": true, "1792x1024": true, "1080x1920": true, "1920x1080": true}
	for _, candidate := range []string{strings.TrimSpace(resolution), strings.TrimSpace(aspectRatio)} {
		c := strings.ToLower(strings.TrimSpace(candidate))
		if allowed[c] {
			return c
		}
	}
	switch strings.TrimSpace(strings.ToLower(aspectRatio)) {
	case "9:16", "portrait":
		return "720x1280"
	case "16:9", "landscape":
		return "1280x720"
	}
	return ""
}

func defaultVideoFilename(name, fallbackExt string) string {
	base := strings.TrimSpace(filepath.Base(name))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "upload" + fallbackExt
	}
	if filepath.Ext(base) == "" {
		return base + fallbackExt
	}
	return base
}

func executeConfiguredVideo(ctx context.Context, model ModelConfig, req VideoRequest, defs videoAdapterDefaults) (VideoResult, error) {
	prepared := model
	authType := normalizedAuthType(prepared.AuthType, "bearer")
	prepared.AuthType = authType
	if authType == "bearer" || authType == "header_key" {
		prepared.APIKey = resolveConfiguredAPIKey(prepared)
		if strings.TrimSpace(prepared.APIKey) == "" {
			return VideoResult{}, errors.New("missing API key for this model")
		}
	}
	endpoint := strings.TrimSpace(modelEndpoint(prepared))
	if endpoint == "" && strings.TrimSpace(defs.submitPath) == "" {
		return VideoResult{}, errors.New("missing video submit endpoint")
	}
	if strings.TrimSpace(prepared.APIPath) == "" && strings.TrimSpace(defs.submitPath) != "" {
		prepared.APIPath = defs.submitPath
		endpoint = strings.TrimSpace(modelEndpoint(prepared))
	}
	body := map[string]any{}
	for k, v := range defs.submitBodyTemplate {
		body[k] = deepCloneAny(v)
	}
	if extra := providerOptionMap(prepared, "extra_body"); len(extra) > 0 {
		for k, v := range extra {
			body[k] = v
		}
	}
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		setNestedField(body, providerOptionString(prepared, "video_prompt_path", defs.promptFieldPath), prompt)
	}
	if strings.TrimSpace(prepared.ModelName) != "" {
		field := providerOptionString(prepared, "video_model_path", defs.modelFieldPath)
		if strings.TrimSpace(field) != "" {
			setNestedField(body, field, strings.TrimSpace(prepared.ModelName))
		}
	}
	if req.StartFrame != nil && len(req.StartFrame.Data) > 0 {
		setNestedField(body, providerOptionString(prepared, "video_start_frame_path", defs.startFieldPath), base64.StdEncoding.EncodeToString(req.StartFrame.Data))
		setNestedField(body, providerOptionString(prepared, "video_start_frame_mime_path", defs.startMimeFieldPath), defaultVideoMIME(req.StartFrame.Name, req.StartFrame.MIMEType))
	}
	if req.EndFrame != nil && len(req.EndFrame.Data) > 0 {
		setNestedField(body, providerOptionString(prepared, "video_end_frame_path", defs.endFieldPath), base64.StdEncoding.EncodeToString(req.EndFrame.Data))
		setNestedField(body, providerOptionString(prepared, "video_end_frame_mime_path", defs.endMimeFieldPath), defaultVideoMIME(req.EndFrame.Name, req.EndFrame.MIMEType))
	}
	if val := strings.TrimSpace(req.Duration); val != "" {
		field := providerOptionString(prepared, "video_duration_path", defs.durationFieldPath)
		if field != "" {
			setNestedField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.AspectRatio); val != "" {
		field := providerOptionString(prepared, "video_aspect_ratio_path", defs.aspectFieldPath)
		if field != "" {
			setNestedField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.Resolution); val != "" {
		field := providerOptionString(prepared, "video_resolution_path", defs.resFieldPath)
		if field != "" {
			setNestedField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.OutputFormat); val != "" {
		field := providerOptionString(prepared, "video_output_format_path", defs.formatFieldPath)
		if field != "" {
			setNestedField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.FPS); val != "" {
		field := providerOptionString(prepared, "video_fps_path", defs.fpsFieldPath)
		if field != "" {
			setNestedField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.Quality); val != "" {
		field := providerOptionString(prepared, "video_quality_path", defs.qualityFieldPath)
		if field != "" {
			setNestedField(body, field, val)
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return VideoResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return VideoResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	applyModelHeaders(request, prepared)
	respBody, status, statusCode, err := doAdapterRequest(request, modelTimeout(prepared, 20*time.Minute))
	if err != nil {
		return VideoResult{}, err
	}
	if statusCode >= 300 {
		return VideoResult{RawBody: string(respBody)}, fmt.Errorf("video submit returned %s: %s", status, strings.TrimSpace(string(respBody)))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return VideoResult{RawBody: string(respBody)}, fmt.Errorf("video submit response was not valid JSON")
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, providerOptionString(prepared, "video_job_id_path", defs.jobIDPath))))
	if jobID == "" {
		if uri := strings.TrimSpace(asString(lookupPath(submitPayload, providerOptionString(prepared, "video_operation_name_path", defs.jobIDPath)))); uri != "" {
			jobID = uri
		}
	}
	if jobID == "" {
		return VideoResult{RawBody: string(respBody)}, errors.New("video submit response did not include a job id")
	}
	pollEvery := req.PollInterval
	if pollEvery <= 0 {
		pollEvery = 8 * time.Second
	}
	pollPathTemplate := providerOptionString(prepared, "video_poll_path_template", defs.pollPathTemplate)
	videoURLPath := providerOptionString(prepared, "video_result_url_path", defs.videoURLPath)
	videoMimePath := providerOptionString(prepared, "video_result_mime_path", defs.videoMimePath)
	statusPath := providerOptionString(prepared, "video_status_path", defs.statusPath)
	errorPath := providerOptionString(prepared, "video_error_path", defs.errorPath)
	successValue := strings.ToLower(strings.TrimSpace(providerOptionString(prepared, "video_success_value", defs.successValue)))
	failedValues := strings.Split(strings.ToLower(strings.TrimSpace(providerOptionString(prepared, "video_failure_values", "failed,error,cancelled,canceled,stopped"))), ",")

	lastBody := string(respBody)
	for {
		if ctx.Err() != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody, Status: "stopped"}, ctx.Err()
		}
		pollURL := buildVideoPollURL(prepared, jobID, pollPathTemplate)
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		applyModelHeaders(pollReq, prepared)
		pollBody, pollStatus, pollCode, err := doAdapterRequest(pollReq, modelTimeout(prepared, 20*time.Minute))
		if err != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, err
		}
		lastBody = string(pollBody)
		if pollCode >= 300 {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("video polling returned %s: %s", pollStatus, strings.TrimSpace(lastBody))
		}
		var pollPayload any
		if err := json.Unmarshal(pollBody, &pollPayload); err != nil {
			return VideoResult{ProviderJobID: jobID, RawBody: lastBody}, fmt.Errorf("video poll response was not valid JSON")
		}
		remoteStatusRaw := lookupPath(pollPayload, statusPath)
		remoteStatus := strings.ToLower(strings.TrimSpace(asString(remoteStatusRaw)))
		if successValue == "true" || successValue == "false" {
			if b, ok := remoteStatusRaw.(bool); ok {
				if fmt.Sprint(b) == successValue {
					return fetchVideoResult(ctx, prepared, jobID, remoteStatus, pollPayload, videoURLPath, videoMimePath, lastBody)
				}
				if !b {
					time.Sleep(pollEvery)
					continue
				}
			}
		}
		if remoteStatus != "" {
			if remoteStatus == successValue {
				return fetchVideoResult(ctx, prepared, jobID, remoteStatus, pollPayload, videoURLPath, videoMimePath, lastBody)
			}
			for _, failed := range failedValues {
				if strings.TrimSpace(failed) != "" && remoteStatus == strings.TrimSpace(failed) {
					errText := strings.TrimSpace(asString(lookupPath(pollPayload, errorPath)))
					if errText == "" {
						errText = remoteStatus
					}
					return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody, Error: errText, Status: "failed"}, errors.New(errText)
				}
			}
		}
		if payloadMap, ok := pollPayload.(map[string]any); ok {
			if done, ok := payloadMap["done"].(bool); ok && done && successValue != "true" {
				return fetchVideoResult(ctx, prepared, jobID, remoteStatus, pollPayload, videoURLPath, videoMimePath, lastBody)
			}
		}
		time.Sleep(pollEvery)
	}
}

func fetchVideoResult(ctx context.Context, model ModelConfig, jobID, remoteStatus string, pollPayload any, videoURLPath, videoMimePath, raw string) (VideoResult, error) {
	videoURL := strings.TrimSpace(asString(lookupPath(pollPayload, videoURLPath)))
	videoMime := strings.TrimSpace(asString(lookupPath(pollPayload, videoMimePath)))
	if videoURL == "" {
		videoURL = strings.TrimSpace(asString(lookupPath(pollPayload, "response.generated_videos.0.video.uri")))
	}
	if videoURL == "" {
		return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, Status: "completed"}, errors.New("video job completed but no video URL was returned")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, videoURL, nil)
	if err != nil {
		return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw}, err
	}
	applyModelHeaders(req, model)
	body, status, statusCode, err := doAdapterRequest(req, modelTimeout(model, 20*time.Minute))
	if err != nil {
		return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw}, err
	}
	if statusCode >= 300 {
		return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw}, fmt.Errorf("video download returned %s", status)
	}
	if videoMime == "" {
		videoMime = http.DetectContentType(body)
	}
	filename := providerOptionString(model, "video_result_filename", "")
	if strings.TrimSpace(filename) == "" {
		filename = "output" + extensionForMIME(videoMime)
	}
	return VideoResult{
		Status:        "completed",
		ProviderJobID: jobID,
		RemoteStatus:  remoteStatus,
		VideoData:     body,
		VideoMIMEType: videoMime,
		VideoFilename: filename,
		RawBody:       raw,
	}, nil
}

func buildVideoPollURL(model ModelConfig, jobID, pollTemplate string) string {
	clean := strings.TrimSpace(pollTemplate)
	if clean == "" {
		clean = "{job_id}"
	}
	clean = strings.ReplaceAll(clean, "{job_id}", jobID)
	if strings.HasPrefix(clean, "http://") || strings.HasPrefix(clean, "https://") {
		return clean
	}
	base := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
	if strings.HasPrefix(clean, "/") {
		return base + clean
	}
	if base == "" {
		return clean
	}
	return base + "/" + clean
}

func deepCloneAny(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, child := range typed {
			out[k] = deepCloneAny(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = deepCloneAny(child)
		}
		return out
	default:
		return typed
	}
}

func asString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func extensionForMIME(contentType string) string {
	if exts, _ := mime.ExtensionsByType(strings.TrimSpace(contentType)); len(exts) > 0 {
		return exts[0]
	}
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	case "video/webm":
		return ".webm"
	default:
		return filepath.Ext(contentType)
	}
}

func defaultVideoMIME(name, explicit string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	if guessed := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); guessed != "" {
		return guessed
	}
	return "image/png"
}
