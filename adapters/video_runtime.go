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
	JobID           string
	Prompt          string
	StartFrame      *VideoBinary
	EndFrame        *VideoBinary
	ReferenceImages []VideoBinary
	Duration        string
	AspectRatio     string
	Resolution      string
	OutputFormat    string
	FPS             string
	Quality         string
	PollInterval    time.Duration
	PollUpdate      func(VideoProgress)
}

type VideoProgress struct {
	Status            string
	ProviderJobID     string
	RemoteStatus      string
	PollCount         int
	LastPollAt        string
	SubmitRequestURL  string
	SubmitRequestBody string
	SubmitRawBody     string
	PollRequestURL    string
	PollRequestBody   string
	LastPollRawBody   string
	Error             string
}

type VideoResult struct {
	Status                   string
	ProviderJobID            string
	RemoteStatus             string
	VideoData                []byte
	VideoMIMEType            string
	VideoFilename            string
	VideoSourceURI           string
	VideoDownloadContentType string
	RawBody                  string
	SubmitRequestURL         string
	SubmitRequestBody        string
	SubmitRawBody            string
	PollRequestURL           string
	PollRequestBody          string
	LastPollRawBody          string
	PollCount                int
	LastPollAt               string
	Error                    string
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
			videoURLPath:       "response.generateVideoResponse.generatedSamples.0.video.uri",
			videoMimePath:      "response.generateVideoResponse.generatedSamples.0.video.mimeType",
			submitBodyTemplate: map[string]any{"instances": []any{map[string]any{}}, "parameters": map[string]any{}},
			promptFieldPath:    "instances.0.prompt",
			startFieldPath:     "instances.0.image.inlineData.data",
			startMimeFieldPath: "instances.0.image.inlineData.mimeType",
			endFieldPath:       "instances.0.lastFrame.inlineData.data",
			endMimeFieldPath:   "instances.0.lastFrame.inlineData.mimeType",
			refFieldPath:       "instances.0.referenceImages",
			durationFieldPath:  "parameters.durationSeconds",
			aspectFieldPath:    "parameters.aspectRatio",
			resFieldPath:       "parameters.resolution",
		})
	case "vertex_veo_video":
		return executeVertexVeoVideo(ctx, model, req)
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
	refFieldPath       string
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

func reportVideoProgress(req VideoRequest, progress VideoProgress) {
	if req.PollUpdate != nil {
		req.PollUpdate(progress)
	}
}

func setVideoRequestField(root map[string]any, path string, value any) {
	clean := strings.TrimSpace(path)
	if clean == "" || root == nil || value == nil {
		return
	}
	parts := strings.Split(clean, ".")
	updated := setVideoRequestPath(root, parts, value)
	if mapped, ok := updated.(map[string]any); ok {
		replacement := make(map[string]any, len(mapped))
		for key, child := range mapped {
			replacement[key] = child
		}
		for key := range root {
			delete(root, key)
		}
		for key, child := range replacement {
			root[key] = child
		}
	}
}

func setVideoRequestPath(current any, parts []string, value any) any {
	if len(parts) == 0 {
		return value
	}
	segment := strings.TrimSpace(parts[0])
	if segment == "" {
		return current
	}
	if idx, err := strconv.Atoi(segment); err == nil && idx >= 0 {
		var arr []any
		if typed, ok := current.([]any); ok && typed != nil {
			arr = typed
		}
		for len(arr) <= idx {
			arr = append(arr, nil)
		}
		arr[idx] = setVideoRequestPath(arr[idx], parts[1:], value)
		return arr
	}
	mapped, _ := current.(map[string]any)
	if mapped == nil {
		mapped = map[string]any{}
	}
	mapped[segment] = setVideoRequestPath(mapped[segment], parts[1:], value)
	return mapped
}

func coerceJSONScalar(value string) any {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return value
	}
	if i, err := strconv.Atoi(clean); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(clean, 64); err == nil {
		return f
	}
	return value
}

func sanitizedVideoRequestBody(body map[string]any) string {
	if body == nil {
		return ""
	}
	payload, err := json.MarshalIndent(sanitizeVideoDiagnosticValue(body, "", nil), "", "  ")
	if err != nil {
		return ""
	}
	return string(payload)
}

func sanitizeVideoDiagnosticValue(value any, key string, parent map[string]any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, child := range typed {
			out[childKey] = sanitizeVideoDiagnosticValue(child, childKey, typed)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for idx, child := range typed {
			out[idx] = sanitizeVideoDiagnosticValue(child, key, parent)
		}
		return out
	case string:
		if shouldRedactVideoBase64Field(key, typed) {
			mimeType := "binary"
			if parent != nil {
				if val := strings.TrimSpace(asString(parent["mimeType"])); val != "" {
					mimeType = val
				} else if val := strings.TrimSpace(asString(parent["mime_type"])); val != "" {
					mimeType = val
				}
			}
			return fmt.Sprintf("[base64 %s, approx %d bytes]", mimeType, approximateBase64DecodedBytes(typed))
		}
		return typed
	default:
		return typed
	}
}

func shouldRedactVideoBase64Field(key, value string) bool {
	cleanKey := strings.ToLower(strings.TrimSpace(key))
	if len(strings.TrimSpace(value)) < 96 {
		return false
	}
	switch cleanKey {
	case "bytesbase64encoded", "data", "base64", "b64_json", "content":
		return looksLikeBase64(value)
	default:
		return false
	}
}

func looksLikeBase64(value string) bool {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return false
	}
	if strings.Contains(clean, " ") || strings.Contains(clean, "\t") || strings.Contains(clean, "\n") || strings.Contains(clean, "\r") {
		return false
	}
	for _, ch := range clean {
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '+' || ch == '/' || ch == '=' || ch == '-' || ch == '_' {
			continue
		}
		return false
	}
	return true
}

func approximateBase64DecodedBytes(value string) int {
	clean := strings.TrimSpace(value)
	if decoded, err := base64.StdEncoding.DecodeString(clean); err == nil {
		return len(decoded)
	}
	padding := 0
	for strings.HasSuffix(clean, "=") {
		padding++
		clean = strings.TrimSuffix(clean, "=")
	}
	return len(clean)*3/4 - padding
}

func sanitizeDiagnosticURL(raw string) string {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return ""
	}
	parsed, err := url.Parse(clean)
	if err != nil || parsed == nil {
		return clean
	}
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "credential") || strings.Contains(lower, "auth") {
			query.Set(key, "[redacted]")
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func buildGenericVideoReferenceImages(images []VideoBinary) []any {
	refs := []any{}
	for _, image := range images {
		if len(refs) >= 3 {
			break
		}
		if len(image.Data) == 0 {
			continue
		}
		refs = append(refs, map[string]any{
			"image": map[string]any{
				"inlineData": map[string]any{
					"mimeType": defaultVideoMIME(image.Name, image.MIMEType),
					"data":     base64.StdEncoding.EncodeToString(image.Data),
				},
			},
			"referenceType": "asset",
		})
	}
	return refs
}

func buildVertexVideoReferenceImages(images []VideoBinary) []any {
	refs := []any{}
	for _, image := range images {
		if len(refs) >= 3 {
			break
		}
		if len(image.Data) == 0 {
			continue
		}
		refs = append(refs, map[string]any{
			"image": map[string]any{
				"bytesBase64Encoded": base64.StdEncoding.EncodeToString(image.Data),
				"mimeType":           defaultVideoMIME(image.Name, image.MIMEType),
			},
			"referenceType": "asset",
		})
	}
	return refs
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
		setVideoRequestField(body, providerOptionString(prepared, "video_prompt_path", defs.promptFieldPath), prompt)
	}
	if strings.TrimSpace(prepared.ModelName) != "" {
		field := providerOptionString(prepared, "video_model_path", defs.modelFieldPath)
		if strings.TrimSpace(field) != "" {
			setVideoRequestField(body, field, strings.TrimSpace(prepared.ModelName))
		}
	}
	if req.StartFrame != nil && len(req.StartFrame.Data) > 0 {
		setVideoRequestField(body, providerOptionString(prepared, "video_start_frame_path", defs.startFieldPath), base64.StdEncoding.EncodeToString(req.StartFrame.Data))
		setVideoRequestField(body, providerOptionString(prepared, "video_start_frame_mime_path", defs.startMimeFieldPath), defaultVideoMIME(req.StartFrame.Name, req.StartFrame.MIMEType))
	}
	if req.EndFrame != nil && len(req.EndFrame.Data) > 0 {
		setVideoRequestField(body, providerOptionString(prepared, "video_end_frame_path", defs.endFieldPath), base64.StdEncoding.EncodeToString(req.EndFrame.Data))
		setVideoRequestField(body, providerOptionString(prepared, "video_end_frame_mime_path", defs.endMimeFieldPath), defaultVideoMIME(req.EndFrame.Name, req.EndFrame.MIMEType))
	}
	if refs := buildGenericVideoReferenceImages(req.ReferenceImages); len(refs) > 0 {
		if field := providerOptionString(prepared, "video_reference_images_path", defs.refFieldPath); field != "" {
			setVideoRequestField(body, field, refs)
		}
	}
	if val := strings.TrimSpace(req.Duration); val != "" {
		field := providerOptionString(prepared, "video_duration_path", defs.durationFieldPath)
		if field != "" {
			setVideoRequestField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.AspectRatio); val != "" {
		field := providerOptionString(prepared, "video_aspect_ratio_path", defs.aspectFieldPath)
		if field != "" {
			setVideoRequestField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.Resolution); val != "" {
		field := providerOptionString(prepared, "video_resolution_path", defs.resFieldPath)
		if field != "" {
			setVideoRequestField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.OutputFormat); val != "" {
		field := providerOptionString(prepared, "video_output_format_path", defs.formatFieldPath)
		if field != "" {
			setVideoRequestField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.FPS); val != "" {
		field := providerOptionString(prepared, "video_fps_path", defs.fpsFieldPath)
		if field != "" {
			setVideoRequestField(body, field, val)
		}
	}
	if val := strings.TrimSpace(req.Quality); val != "" {
		field := providerOptionString(prepared, "video_quality_path", defs.qualityFieldPath)
		if field != "" {
			setVideoRequestField(body, field, val)
		}
	}
	submitRequestBody := sanitizedVideoRequestBody(body)
	submitRequestURL := sanitizeDiagnosticURL(endpoint)
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
		return VideoResult{SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody}, err
	}
	submitRaw := string(respBody)
	if statusCode >= 300 {
		return VideoResult{RawBody: submitRaw, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw}, fmt.Errorf("video submit returned %s: %s", status, strings.TrimSpace(submitRaw))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return VideoResult{RawBody: submitRaw, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw}, fmt.Errorf("video submit response was not valid JSON")
	}
	jobID := strings.TrimSpace(asString(lookupPath(submitPayload, providerOptionString(prepared, "video_job_id_path", defs.jobIDPath))))
	if jobID == "" {
		if uri := strings.TrimSpace(asString(lookupPath(submitPayload, providerOptionString(prepared, "video_operation_name_path", defs.jobIDPath)))); uri != "" {
			jobID = uri
		}
	}
	if jobID == "" {
		return VideoResult{RawBody: submitRaw, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw}, errors.New("video submit response did not include a job id")
	}
	reportVideoProgress(req, VideoProgress{Status: "running", ProviderJobID: jobID, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw})
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

	lastBody := submitRaw
	pollCount := 0
	lastPollAt := ""
	resultWithProgress := func(result VideoResult) VideoResult {
		result.PollCount = pollCount
		result.LastPollAt = lastPollAt
		result.SubmitRequestURL = submitRequestURL
		result.SubmitRequestBody = submitRequestBody
		result.SubmitRawBody = submitRaw
		result.LastPollRawBody = lastBody
		if result.RawBody == "" {
			result.RawBody = lastBody
		}
		return result
	}
	for {
		if ctx.Err() != nil {
			return resultWithProgress(VideoResult{ProviderJobID: jobID, RawBody: lastBody, Status: "stopped"}), ctx.Err()
		}
		pollURL := buildVideoPollURL(prepared, jobID, pollPathTemplate)
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return resultWithProgress(VideoResult{ProviderJobID: jobID, RawBody: lastBody}), err
		}
		applyModelHeaders(pollReq, prepared)
		pollBody, pollStatus, pollCode, err := doAdapterRequest(pollReq, modelTimeout(prepared, 20*time.Minute))
		pollCount++
		lastPollAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			return resultWithProgress(VideoResult{ProviderJobID: jobID, RawBody: lastBody}), err
		}
		lastBody = string(pollBody)
		if pollCode >= 300 {
			reportVideoProgress(req, VideoProgress{Status: "running", ProviderJobID: jobID, PollCount: pollCount, LastPollAt: lastPollAt, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw, LastPollRawBody: lastBody, Error: strings.TrimSpace(lastBody)})
			return resultWithProgress(VideoResult{ProviderJobID: jobID, RawBody: lastBody}), fmt.Errorf("video polling returned %s: %s", pollStatus, strings.TrimSpace(lastBody))
		}
		var pollPayload any
		if err := json.Unmarshal(pollBody, &pollPayload); err != nil {
			reportVideoProgress(req, VideoProgress{Status: "running", ProviderJobID: jobID, PollCount: pollCount, LastPollAt: lastPollAt, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw, LastPollRawBody: lastBody, Error: "video poll response was not valid JSON"})
			return resultWithProgress(VideoResult{ProviderJobID: jobID, RawBody: lastBody}), fmt.Errorf("video poll response was not valid JSON")
		}
		remoteStatusRaw := lookupPath(pollPayload, statusPath)
		remoteStatus := strings.ToLower(strings.TrimSpace(asString(remoteStatusRaw)))
		reportVideoProgress(req, VideoProgress{Status: "running", ProviderJobID: jobID, RemoteStatus: remoteStatus, PollCount: pollCount, LastPollAt: lastPollAt, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw, LastPollRawBody: lastBody})
		if successValue == "true" || successValue == "false" {
			if b, ok := remoteStatusRaw.(bool); ok {
				if fmt.Sprint(b) == successValue {
					result, err := fetchVideoResult(ctx, prepared, jobID, remoteStatus, pollPayload, videoURLPath, videoMimePath, lastBody)
					return resultWithProgress(result), err
				}
				if !b {
					time.Sleep(pollEvery)
					continue
				}
			}
		}
		if remoteStatus != "" {
			if remoteStatus == successValue {
				result, err := fetchVideoResult(ctx, prepared, jobID, remoteStatus, pollPayload, videoURLPath, videoMimePath, lastBody)
				return resultWithProgress(result), err
			}
			for _, failed := range failedValues {
				if strings.TrimSpace(failed) != "" && remoteStatus == strings.TrimSpace(failed) {
					errText := strings.TrimSpace(asString(lookupPath(pollPayload, errorPath)))
					if errText == "" {
						errText = remoteStatus
					}
					return resultWithProgress(VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: lastBody, Error: errText, Status: "failed"}), errors.New(errText)
				}
			}
		}
		if payloadMap, ok := pollPayload.(map[string]any); ok {
			if done, ok := payloadMap["done"].(bool); ok && done && successValue != "true" {
				result, err := fetchVideoResult(ctx, prepared, jobID, remoteStatus, pollPayload, videoURLPath, videoMimePath, lastBody)
				return resultWithProgress(result), err
			}
		}
		time.Sleep(pollEvery)
	}
}

func fetchVideoResult(ctx context.Context, model ModelConfig, jobID, remoteStatus string, pollPayload any, videoURLPath, videoMimePath, raw string) (VideoResult, error) {
	videoURL := strings.TrimSpace(asString(lookupPath(pollPayload, videoURLPath)))
	videoMime := strings.TrimSpace(asString(lookupPath(pollPayload, videoMimePath)))
	if videoURL == "" {
		for _, fallback := range []string{
			"response.generateVideoResponse.generatedSamples.0.video.uri",
			"response.generatedVideos.0.video.uri",
			"response.generated_videos.0.video.uri",
			"response.videos.0.gcsUri",
			"response.videos.0.uri",
			"response.videos.0.video.uri",
		} {
			videoURL = strings.TrimSpace(asString(lookupPath(pollPayload, fallback)))
			if videoURL != "" {
				break
			}
		}
	}
	if videoMime == "" {
		for _, fallback := range []string{
			"response.generateVideoResponse.generatedSamples.0.video.mimeType",
			"response.generatedVideos.0.video.mimeType",
			"response.generated_videos.0.video.mimeType",
			"response.videos.0.mimeType",
			"response.videos.0.video.mimeType",
		} {
			videoMime = strings.TrimSpace(asString(lookupPath(pollPayload, fallback)))
			if videoMime != "" {
				break
			}
		}
	}
	if videoURL == "" {
		if b64 := strings.TrimSpace(asString(lookupPath(pollPayload, "response.videos.0.bytesBase64Encoded"))); b64 != "" {
			body, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, Status: "completed"}, err
			}
			if videoMime == "" {
				videoMime = http.DetectContentType(body)
			}
			filename := providerOptionString(model, "video_result_filename", "")
			if strings.TrimSpace(filename) == "" {
				filename = "output" + extensionForMIME(videoMime)
			}
			return VideoResult{Status: "completed", ProviderJobID: jobID, RemoteStatus: remoteStatus, VideoData: body, VideoMIMEType: videoMime, VideoFilename: filename, RawBody: raw}, nil
		}
		return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, Status: "completed"}, errors.New("video job completed but no video URL was returned")
	}
	downloadURL := normalizeVideoDownloadURL(videoURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw}, err
	}
	applyModelHeaders(req, model)
	body, status, statusCode, headers, err := doAdapterRequestWithHeaders(req, modelTimeout(model, 20*time.Minute))
	if err != nil {
		return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, VideoSourceURI: sanitizeDiagnosticURL(videoURL)}, err
	}
	if statusCode >= 300 {
		return VideoResult{ProviderJobID: jobID, RemoteStatus: remoteStatus, RawBody: raw, VideoSourceURI: sanitizeDiagnosticURL(videoURL)}, fmt.Errorf("video download returned %s", status)
	}
	downloadContentType := strings.TrimSpace(headers.Get("Content-Type"))
	if videoMime == "" {
		videoMime = strings.TrimSpace(downloadContentType)
	}
	if videoMime == "" || strings.EqualFold(videoMime, "application/octet-stream") {
		videoMime = http.DetectContentType(body)
	}
	filename := providerOptionString(model, "video_result_filename", "")
	if strings.TrimSpace(filename) == "" {
		filename = filenameFromVideoURL(videoURL)
	}
	if strings.TrimSpace(filename) == "" {
		filename = "output" + extensionForMIME(videoMime)
	}
	return VideoResult{
		Status:                   "completed",
		ProviderJobID:            jobID,
		RemoteStatus:             remoteStatus,
		VideoData:                body,
		VideoMIMEType:            videoMime,
		VideoFilename:            filename,
		VideoSourceURI:           sanitizeDiagnosticURL(videoURL),
		VideoDownloadContentType: downloadContentType,
		RawBody:                  raw,
	}, nil
}

func buildVertexVeoSubmitBody(model ModelConfig, req VideoRequest) map[string]any {
	body := map[string]any{
		"instances":  []any{map[string]any{}},
		"parameters": map[string]any{},
	}
	if extra := providerOptionMap(model, "extra_body"); len(extra) > 0 {
		for k, v := range extra {
			body[k] = deepCloneAny(v)
		}
	}
	instance := ensureFirstObjectInArray(body, "instances")
	parameters := ensureObjectField(body, "parameters")
	prompt := strings.TrimSpace(req.Prompt)
	if prompt != "" {
		setVideoRequestField(body, providerOptionString(model, "video_prompt_path", "instances.0.prompt"), prompt)
		if strings.TrimSpace(asString(lookupPath(body, "instances.0.prompt"))) == "" {
			instance["prompt"] = prompt
		}
	}
	if req.StartFrame != nil && len(req.StartFrame.Data) > 0 {
		mimeType := defaultVideoMIME(req.StartFrame.Name, req.StartFrame.MIMEType)
		encoded := base64.StdEncoding.EncodeToString(req.StartFrame.Data)
		setVideoRequestField(body, providerOptionString(model, "video_start_frame_path", "instances.0.image.bytesBase64Encoded"), encoded)
		setVideoRequestField(body, providerOptionString(model, "video_start_frame_mime_path", "instances.0.image.mimeType"), mimeType)
		if strings.TrimSpace(asString(lookupPath(body, "instances.0.image.bytesBase64Encoded"))) == "" {
			image := ensureObjectField(instance, "image")
			image["bytesBase64Encoded"] = encoded
			image["mimeType"] = mimeType
		}
	}
	if req.EndFrame != nil && len(req.EndFrame.Data) > 0 {
		mimeType := defaultVideoMIME(req.EndFrame.Name, req.EndFrame.MIMEType)
		encoded := base64.StdEncoding.EncodeToString(req.EndFrame.Data)
		setVideoRequestField(body, providerOptionString(model, "video_end_frame_path", "instances.0.lastFrame.bytesBase64Encoded"), encoded)
		setVideoRequestField(body, providerOptionString(model, "video_end_frame_mime_path", "instances.0.lastFrame.mimeType"), mimeType)
		if strings.TrimSpace(asString(lookupPath(body, "instances.0.lastFrame.bytesBase64Encoded"))) == "" {
			lastFrame := ensureObjectField(instance, "lastFrame")
			lastFrame["bytesBase64Encoded"] = encoded
			lastFrame["mimeType"] = mimeType
		}
	}
	if refs := buildVertexVideoReferenceImages(req.ReferenceImages); len(refs) > 0 {
		setVideoRequestField(body, providerOptionString(model, "video_reference_images_path", "instances.0.referenceImages"), refs)
		if existing, ok := lookupPath(body, "instances.0.referenceImages").([]any); !ok || len(existing) == 0 {
			instance["referenceImages"] = refs
		}
	}
	duration := firstNonEmpty(req.Duration, model.VideoDuration, providerOptionString(model, "default_duration_seconds", ""), "4")
	if duration != "" {
		setVideoRequestField(body, providerOptionString(model, "video_duration_path", "parameters.durationSeconds"), coerceJSONScalar(duration))
	}
	aspectRatio := firstNonEmpty(req.AspectRatio, model.VideoAspectRatio, providerOptionString(model, "default_aspect_ratio", ""), "16:9")
	if aspectRatio != "" {
		setVideoRequestField(body, providerOptionString(model, "video_aspect_ratio_path", "parameters.aspectRatio"), aspectRatio)
	}
	resolution := firstNonEmpty(req.Resolution, model.VideoResolution, providerOptionString(model, "default_resolution", ""), "720p")
	if resolution != "" {
		setVideoRequestField(body, providerOptionString(model, "video_resolution_path", "parameters.resolution"), resolution)
	}
	if val := strings.TrimSpace(firstProviderOptionString(model, "video_storage_uri", "storage_uri", "vertex_storage_uri")); val != "" {
		setVideoRequestField(body, providerOptionString(model, "video_storage_uri_path", "parameters.storageUri"), val)
	}
	if lookupPath(body, "parameters.sampleCount") == nil {
		parameters["sampleCount"] = 1
	}
	ensureFirstObjectInArray(body, "instances")
	ensureObjectField(body, "parameters")
	return body
}

func ensureObjectField(parent map[string]any, key string) map[string]any {
	if parent == nil {
		return map[string]any{}
	}
	if existing, ok := parent[key].(map[string]any); ok && existing != nil {
		return existing
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

func ensureFirstObjectInArray(parent map[string]any, key string) map[string]any {
	if parent == nil {
		return map[string]any{}
	}
	var arr []any
	if existing, ok := parent[key].([]any); ok && existing != nil {
		arr = existing
	}
	if len(arr) == 0 {
		arr = []any{map[string]any{}}
	}
	first, ok := arr[0].(map[string]any)
	if !ok || first == nil {
		first = map[string]any{}
		arr[0] = first
	}
	parent[key] = arr
	return first
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if clean := strings.TrimSpace(value); clean != "" {
			return clean
		}
	}
	return ""
}

func validateVertexVeoSubmitBody(body map[string]any) error {
	instances, _ := lookupPath(body, "instances").([]any)
	if len(instances) == 0 {
		return errors.New("Vertex Veo request was not sent because AgentGO did not build any instances from the prompt/start frame")
	}
	instance, _ := instances[0].(map[string]any)
	if instance == nil {
		return errors.New("Vertex Veo request was not sent because AgentGO built an invalid first instance")
	}
	if strings.TrimSpace(asString(instance["prompt"])) != "" {
		return nil
	}
	if strings.TrimSpace(asString(lookupPath(instance, "image.bytesBase64Encoded"))) != "" {
		return nil
	}
	if strings.TrimSpace(asString(lookupPath(instance, "lastFrame.bytesBase64Encoded"))) != "" {
		return nil
	}
	if refs, ok := lookupPath(instance, "referenceImages").([]any); ok && len(refs) > 0 {
		return nil
	}
	return errors.New("Vertex Veo request was not sent because AgentGO did not attach a prompt, start frame, end frame, or reference image to instances[0]")
}

func executeVertexVeoVideo(ctx context.Context, model ModelConfig, req VideoRequest) (VideoResult, error) {
	prepared := model
	prepared.AuthType = normalizedAuthType(prepared.AuthType, "google_adc")
	switch prepared.AuthType {
	case "google_adc", "adc":
		var err error
		prepared, err = applyGoogleADC(ctx, prepared)
		if err != nil {
			return VideoResult{}, err
		}
	case "bearer", "header_key":
		prepared.APIKey = resolveConfiguredAPIKey(prepared)
		if strings.TrimSpace(prepared.APIKey) == "" {
			return VideoResult{}, errors.New("missing Vertex bearer token for this model")
		}
	}
	if strings.TrimSpace(prepared.BaseURL) == "" {
		prepared.BaseURL = "https://us-central1-aiplatform.googleapis.com"
	}
	if strings.TrimSpace(prepared.APIPath) == "" {
		prepared.APIPath = "/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:predictLongRunning"
	}
	submitURL, err := vertexVeoURL(ctx, prepared, "predictLongRunning")
	if err != nil {
		return VideoResult{}, err
	}
	body := buildVertexVeoSubmitBody(prepared, req)
	submitRequestBody := sanitizedVideoRequestBody(body)
	submitRequestURL := sanitizeDiagnosticURL(submitURL)
	if err := validateVertexVeoSubmitBody(body); err != nil {
		return VideoResult{SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return VideoResult{SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody}, err
	}
	submitReq, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, bytes.NewReader(payload))
	if err != nil {
		return VideoResult{SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody}, err
	}
	submitReq.Header.Set("Content-Type", "application/json")
	applyModelHeaders(submitReq, prepared)
	respBody, status, statusCode, err := doAdapterRequest(submitReq, modelTimeout(prepared, 20*time.Minute))
	if err != nil {
		return VideoResult{SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody}, err
	}
	submitRaw := string(respBody)
	if statusCode >= 300 {
		return VideoResult{RawBody: submitRaw, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw}, fmt.Errorf("video submit returned %s: %s", status, strings.TrimSpace(submitRaw))
	}
	var submitPayload any
	if err := json.Unmarshal(respBody, &submitPayload); err != nil {
		return VideoResult{RawBody: submitRaw, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw}, errors.New("video submit response was not valid JSON")
	}
	operationName := strings.TrimSpace(asString(lookupPath(submitPayload, providerOptionString(prepared, "video_job_id_path", "name"))))
	if operationName == "" {
		return VideoResult{RawBody: submitRaw, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw}, errors.New("video submit response did not include an operation name")
	}
	reportVideoProgress(req, VideoProgress{Status: "running", ProviderJobID: operationName, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw})
	pollURL, err := vertexVeoURL(ctx, prepared, "fetchPredictOperation")
	if err != nil {
		return VideoResult{ProviderJobID: operationName, RawBody: submitRaw, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw}, err
	}
	pollEvery := req.PollInterval
	if pollEvery <= 0 {
		pollEvery = 10 * time.Second
	}
	lastBody := submitRaw
	pollCount := 0
	lastPollAt := ""
	resultWithProgress := func(result VideoResult) VideoResult {
		result.ProviderJobID = operationName
		result.PollCount = pollCount
		result.LastPollAt = lastPollAt
		result.SubmitRequestURL = submitRequestURL
		result.SubmitRequestBody = submitRequestBody
		result.SubmitRawBody = submitRaw
		result.LastPollRawBody = lastBody
		if result.RawBody == "" {
			result.RawBody = lastBody
		}
		return result
	}
	for {
		if ctx.Err() != nil {
			return resultWithProgress(VideoResult{Status: "stopped", RawBody: lastBody}), ctx.Err()
		}
		pollPayload, err := json.Marshal(map[string]any{"operationName": operationName})
		if err != nil {
			return resultWithProgress(VideoResult{RawBody: lastBody}), err
		}
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodPost, pollURL, bytes.NewReader(pollPayload))
		if err != nil {
			return resultWithProgress(VideoResult{RawBody: lastBody}), err
		}
		pollReq.Header.Set("Content-Type", "application/json")
		applyModelHeaders(pollReq, prepared)
		pollBody, pollStatus, pollCode, err := doAdapterRequest(pollReq, modelTimeout(prepared, 20*time.Minute))
		pollCount++
		lastPollAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			return resultWithProgress(VideoResult{RawBody: lastBody}), err
		}
		lastBody = string(pollBody)
		if pollCode >= 300 {
			reportVideoProgress(req, VideoProgress{Status: "running", ProviderJobID: operationName, PollCount: pollCount, LastPollAt: lastPollAt, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw, LastPollRawBody: lastBody, Error: strings.TrimSpace(lastBody)})
			return resultWithProgress(VideoResult{RawBody: lastBody}), fmt.Errorf("video polling returned %s: %s", pollStatus, strings.TrimSpace(lastBody))
		}
		var response any
		if err := json.Unmarshal(pollBody, &response); err != nil {
			return resultWithProgress(VideoResult{RawBody: lastBody}), errors.New("video poll response was not valid JSON")
		}
		remoteStatus := strings.TrimSpace(asString(lookupPath(response, providerOptionString(prepared, "video_status_path", "metadata.state"))))
		if remoteStatus == "" {
			if done, _ := lookupPath(response, "done").(bool); done {
				remoteStatus = "done"
			}
		}
		reportVideoProgress(req, VideoProgress{Status: "running", ProviderJobID: operationName, RemoteStatus: strings.ToLower(remoteStatus), PollCount: pollCount, LastPollAt: lastPollAt, SubmitRequestURL: submitRequestURL, SubmitRequestBody: submitRequestBody, SubmitRawBody: submitRaw, LastPollRawBody: lastBody})
		if errMsg := strings.TrimSpace(asString(lookupPath(response, providerOptionString(prepared, "video_error_path", "error.message")))); errMsg != "" {
			return resultWithProgress(VideoResult{Status: "failed", RemoteStatus: strings.ToLower(remoteStatus), RawBody: lastBody, Error: errMsg}), errors.New(errMsg)
		}
		if done, _ := lookupPath(response, "done").(bool); done {
			result, err := fetchVideoResult(ctx, prepared, operationName, strings.ToLower(remoteStatus), response, providerOptionString(prepared, "video_result_url_path", "response.videos.0.gcsUri"), providerOptionString(prepared, "video_result_mime_path", "response.videos.0.mimeType"), lastBody)
			return resultWithProgress(result), err
		}
		time.Sleep(pollEvery)
	}
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

func firstProviderOptionString(model ModelConfig, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(providerOptionString(model, key, "")); value != "" {
			return value
		}
	}
	return ""
}

func vertexVeoURL(ctx context.Context, model ModelConfig, operation string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
	if base == "" {
		base = "https://us-central1-aiplatform.googleapis.com"
	}
	path := strings.TrimSpace(model.APIPath)
	if path == "" {
		path = "/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:predictLongRunning"
	}
	if operation == "fetchPredictOperation" {
		if idx := strings.LastIndex(path, ":"); idx >= 0 {
			path = path[:idx] + ":fetchPredictOperation"
		} else {
			path += ":fetchPredictOperation"
		}
	}
	project := firstProviderOptionString(model, "vertex_project_id", "project_id", "project")
	if project == "" {
		project = googleADCProjectID(ctx, model)
	}
	location := firstProviderOptionString(model, "vertex_location", "location", "region")
	if location == "" {
		location = "us-central1"
	}
	path = strings.ReplaceAll(path, "{project}", project)
	path = strings.ReplaceAll(path, "{project_id}", project)
	path = strings.ReplaceAll(path, "{location}", location)
	path = strings.ReplaceAll(path, "{region}", location)
	path = strings.ReplaceAll(path, "{model}", strings.TrimSpace(model.ModelName))
	if strings.Contains(path, "{project}") || strings.Contains(path, "{project_id}") || strings.Contains(path, "//") || strings.Contains(path, "/projects//") {
		return "", errors.New("Vertex Veo needs a Google Cloud Project ID. Add provider_options.project_id, set GOOGLE_CLOUD_PROJECT, or use Google ADC credentials that include a project_id/quota_project_id")
	}
	if strings.Contains(path, "{model}") || strings.TrimSpace(model.ModelName) == "" {
		return "", errors.New("Vertex Veo requires a model name")
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, nil
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path, nil
}

func normalizeVideoDownloadURL(raw string) string {
	clean := strings.TrimSpace(raw)
	if !strings.HasPrefix(clean, "gs://") {
		return clean
	}
	withoutScheme := strings.TrimPrefix(clean, "gs://")
	parts := strings.SplitN(withoutScheme, "/", 2)
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return clean
	}
	bucket := url.PathEscape(parts[0])
	object := ""
	if len(parts) > 1 {
		segments := strings.Split(parts[1], "/")
		for i, segment := range segments {
			segments[i] = url.PathEscape(segment)
		}
		object = "/" + strings.Join(segments, "/")
	}
	return "https://storage.googleapis.com/" + bucket + object
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

func filenameFromVideoURL(raw string) string {
	clean := strings.TrimSpace(raw)
	if clean == "" {
		return ""
	}
	pathValue := clean
	if parsed, err := url.Parse(clean); err == nil && parsed != nil && strings.TrimSpace(parsed.Path) != "" {
		pathValue = parsed.Path
	}
	name := strings.TrimSpace(filepath.Base(pathValue))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return ""
	}
	return name
}

func extensionForMIME(contentType string) string {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if parsed, _, err := mime.ParseMediaType(mediaType); err == nil && strings.TrimSpace(parsed) != "" {
		mediaType = strings.ToLower(strings.TrimSpace(parsed))
	}
	switch mediaType {
	case "video/mp4", "application/mp4":
		return ".mp4"
	}
	if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
		return exts[0]
	}
	switch mediaType {
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
