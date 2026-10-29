package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	modelDiscoveryTimeout      = 20 * time.Second
	modelDiscoveryMaxBodyBytes = 8 << 20
	modelDiscoveryMaxModels    = 2000
)

type modelDiscoveryRequest struct {
	ModelID        string            `json:"modelId,omitempty"`
	Provider       string            `json:"provider"`
	Adapter        string            `json:"adapter"`
	BaseURL        string            `json:"base_url"`
	AuthType       string            `json:"auth_type"`
	AuthHeader     string            `json:"auth_header"`
	APIUser        string            `json:"api_user"`
	APIPass        string            `json:"api_pass"`
	APIKey         string            `json:"api_key"`
	APIKeyEnv      string            `json:"api_key_env,omitempty"`
	ClearAPIPass   bool              `json:"clear_api_pass,omitempty"`
	ClearAPIKey    bool              `json:"clear_api_key,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	HeadersPresent bool              `json:"headers_present,omitempty"`
	ClearHeaders   bool              `json:"clear_headers,omitempty"`
}

type discoveredModel struct {
	Provider        string   `json:"provider,omitempty"`
	Adapter         string   `json:"adapter,omitempty"`
	Label           string   `json:"label"`
	ModelName       string   `json:"model_name"`
	Notes           string   `json:"notes,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	// Optional OpenRouter catalog metadata. Missing is never interpreted as unsupported.
	Developer       string            `json:"developer,omitempty"`
	ContextLength   int               `json:"context_length,omitempty"`
	Created         int64             `json:"created,omitempty"`
	InputPrice      *float64          `json:"input_price_per_million,omitempty"`
	OutputPrice     *float64          `json:"output_price_per_million,omitempty"`
	PriceCategory   string            `json:"price_category,omitempty"`
	FeatureStatus   map[string]string `json:"feature_status,omitempty"`
	SupportedParams []string          `json:"supported_parameters,omitempty"`
}

type modelDiscoveryResponse struct {
	Models     []discoveredModel `json:"models"`
	ManualOnly bool              `json:"manual_only,omitempty"`
	Message    string            `json:"message,omitempty"`
}

type modelDiscoverySpec struct {
	Endpoint string
	Kind     string
	Manual   bool
	Message  string
}

type modelDiscoveryClientError struct {
	Code    string
	Message string
	Detail  string
	Status  int
}

func (e *modelDiscoveryClientError) Error() string {
	if e == nil || strings.TrimSpace(e.Message) == "" {
		return "Could not retrieve available models from the provider."
	}
	return strings.TrimSpace(e.Message)
}

func newModelDiscoveryClientError(code, message, detail string, status int) error {
	return &modelDiscoveryClientError{
		Code:    strings.TrimSpace(code),
		Message: strings.TrimSpace(message),
		Detail:  strings.TrimSpace(detail),
		Status:  status,
	}
}

func (a *App) handleModelDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var req modelDiscoveryRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(&req); err != nil {
		http.Error(w, "invalid model-discovery request", http.StatusBadRequest)
		return
	}
	req = normalizeModelDiscoveryRequest(req)
	if req.Provider == "" || req.BaseURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider and base URL are required"})
		return
	}
	var saved *ModelConfig
	if req.ModelID != "" {
		a.mu.RLock()
		for i := range a.cfg.Models {
			if modelIDString(a.cfg.Models[i].ID) == req.ModelID {
				copyModel := a.cfg.Models[i]
				saved = &copyModel
				break
			}
		}
		a.mu.RUnlock()
	}
	applySavedDiscoverySecrets(&req, saved)
	ctx, cancel := context.WithTimeout(r.Context(), modelDiscoveryTimeout)
	defer cancel()
	result, err := discoverProviderModels(ctx, req)
	if err != nil {
		status := http.StatusBadGateway
		payload := map[string]any{"error": userFacingDiscoveryError(err)}
		var clientErr *modelDiscoveryClientError
		if errors.As(err, &clientErr) {
			if clientErr.Status > 0 {
				status = clientErr.Status
			}
			if clientErr.Code != "" {
				payload["code"] = clientErr.Code
			}
			if clientErr.Detail != "" {
				payload["detail"] = clientErr.Detail
			}
		}
		writeJSON(w, status, payload)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// handleOpenRouterModelTest runs only after an explicit user click. It never
// submits project content, stores credentials, or modifies saved model settings.
func (a *App) handleOpenRouterModelTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	defer r.Body.Close()
	var payload struct {
		modelDiscoveryRequest
		ModelName string `json:"model_name"`
		APIPath   string `json:"api_path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid model test request."})
		return
	}
	req := normalizeModelDiscoveryRequest(payload.modelDiscoveryRequest)
	if req.Provider != "openrouter" || req.Adapter != "openai_compatible" || strings.TrimSpace(payload.ModelName) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Select an OpenRouter model first."})
		return
	}
	base, err := validateDiscoveryBaseURL(req.BaseURL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid OpenRouter URL."})
		return
	}
	parsed, _ := url.Parse(base)
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "openrouter.ai") || (parsed.Path != "/api" && parsed.Path != "/api/v1") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Model Test only supports the official OpenRouter endpoint."})
		return
	}
	if req.ModelID != "" {
		a.mu.RLock()
		for i := range a.cfg.Models {
			if modelIDString(a.cfg.Models[i].ID) == req.ModelID {
				copyModel := a.cfg.Models[i]
				applySavedDiscoverySecrets(&req, &copyModel)
				break
			}
		}
		a.mu.RUnlock()
	}
	if resolveModelDiscoveryAPIKey(req) == "" && !discoveryRequestHasExplicitAuthHeader(req) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Enter an OpenRouter API key or configured environment variable to test this model."})
		return
	}
	path := strings.TrimSpace(payload.APIPath)
	if path == "/v1/responses" || path == "/responses" {
		path = "/responses"
	} else if path == "/v1/chat/completions" || path == "/chat/completions" {
		path = "/chat/completions"
	} else {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Model Test supports Chat Completions or Responses API paths."})
		return
	}
	body := map[string]any{"model": strings.TrimSpace(payload.ModelName)}
	if path == "/responses" {
		body["input"] = "Reply with OK only."
		body["max_output_tokens"] = 16
	} else {
		body["messages"] = []map[string]string{{"role": "user", "content": "Reply with OK only."}}
		body["max_tokens"] = 16
	}
	encoded, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	target := "https://openrouter.ai/api/v1" + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Could not prepare model test."})
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	applyModelDiscoveryHeaders(request, req)
	response, err := (&http.Client{Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "OpenRouter model test could not connect."})
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode >= 300 {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("OpenRouter model test failed (HTTP %d). Check model compatibility, credentials, and credits.", response.StatusCode)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "OpenRouter accepted the synthetic 16-token test. This does not verify Work Mode, tools, or structured output.", "status": response.StatusCode})
}

func normalizeModelDiscoveryRequest(req modelDiscoveryRequest) modelDiscoveryRequest {
	req.ModelID = strings.TrimSpace(req.ModelID)
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.Adapter = strings.ToLower(strings.TrimSpace(req.Adapter))
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.AuthType = strings.ToLower(strings.TrimSpace(req.AuthType))
	req.AuthHeader = strings.TrimSpace(req.AuthHeader)
	req.APIUser = strings.TrimSpace(req.APIUser)
	req.APIPass = strings.TrimSpace(req.APIPass)
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.APIKeyEnv = strings.TrimSpace(req.APIKeyEnv)
	cleanHeaders := map[string]string{}
	for key, value := range req.Headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			cleanHeaders[key] = value
		}
	}
	req.Headers = cleanHeaders
	return req
}

func applySavedDiscoverySecrets(req *modelDiscoveryRequest, saved *ModelConfig) {
	if req == nil || saved == nil {
		return
	}
	// Never forward saved credentials from one provider/endpoint to another
	// while editing a model. Explicitly entered unsaved credentials still work.
	if saved.Provider != "" && !strings.EqualFold(strings.TrimSpace(saved.Provider), req.Provider) {
		return
	}
	if saved.Adapter != "" && !strings.EqualFold(strings.TrimSpace(saved.Adapter), req.Adapter) {
		return
	}
	if saved.BaseURL != "" && !strings.EqualFold(strings.TrimRight(strings.TrimSpace(saved.BaseURL), "/"), strings.TrimRight(req.BaseURL, "/")) {
		return
	}
	if req.APIKey == "" && !req.ClearAPIKey {
		req.APIKey = strings.TrimSpace(saved.APIKey)
	}
	if req.APIPass == "" && !req.ClearAPIPass {
		req.APIPass = strings.TrimSpace(saved.APIPass)
	}
	if !req.HeadersPresent && !req.ClearHeaders {
		req.Headers = cloneStringMap(saved.Headers)
	}
}

func discoverProviderModels(ctx context.Context, req modelDiscoveryRequest) (modelDiscoveryResponse, error) {
	spec, err := buildModelDiscoverySpec(req.Provider, req.Adapter, req.BaseURL)
	if err != nil {
		return modelDiscoveryResponse{}, err
	}
	if spec.Manual {
		return modelDiscoveryResponse{Models: []discoveredModel{}, ManualOnly: true, Message: spec.Message}, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.Endpoint, nil)
	if err != nil {
		return modelDiscoveryResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	if req.Provider == "claude" && request.Header.Get("anthropic-version") == "" {
		request.Header.Set("anthropic-version", "2023-06-01")
	}
	applyModelDiscoveryHeaders(request, req)
	client := &http.Client{Timeout: modelDiscoveryTimeout}
	resp, err := client.Do(request)
	if err != nil {
		detail := strings.TrimSpace(err.Error())
		message := "Could not reach the provider. Check the Base URL, network connection, and provider status."
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			message = "The provider model-list request timed out. Check the provider and try again."
		}
		return modelDiscoveryResponse{}, newModelDiscoveryClientError("network_error", message, detail, http.StatusBadGateway)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, modelDiscoveryMaxBodyBytes+1))
	if err != nil {
		return modelDiscoveryResponse{}, newModelDiscoveryClientError("provider_response_error", "Could not read the provider model list.", err.Error(), http.StatusBadGateway)
	}
	if len(body) > modelDiscoveryMaxBodyBytes {
		return modelDiscoveryResponse{}, newModelDiscoveryClientError("provider_response_error", "The provider model list was too large to read safely.", "response exceeded AgentGO model-discovery size limit", http.StatusBadGateway)
	}
	if resp.StatusCode >= 300 {
		detail := providerDiscoveryErrorDetail(resp, body)
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			if resolveModelDiscoveryAPIKey(req) == "" && !discoveryRequestHasExplicitAuthHeader(req) {
				message := "Authentication is required to retrieve available models. Enter an API key or an API key environment variable."
				if req.APIKeyEnv != "" {
					message = fmt.Sprintf("Environment variable %s is not set or is empty. Enter an API key or a working environment variable name.", req.APIKeyEnv)
				}
				return modelDiscoveryResponse{}, newModelDiscoveryClientError("authentication_required", message, detail, http.StatusUnauthorized)
			}
			return modelDiscoveryResponse{}, newModelDiscoveryClientError("authentication_failed", "Authentication failed. The provider rejected the API key or credentials. Check them and try again.", detail, http.StatusUnauthorized)
		case http.StatusTooManyRequests:
			return modelDiscoveryResponse{}, newModelDiscoveryClientError("provider_unavailable", "The provider is rate-limiting model discovery. Try again shortly.", detail, http.StatusBadGateway)
		default:
			if resp.StatusCode >= 500 {
				return modelDiscoveryResponse{}, newModelDiscoveryClientError("provider_unavailable", "The provider is unavailable right now. Try again shortly.", detail, http.StatusBadGateway)
			}
			return modelDiscoveryResponse{}, newModelDiscoveryClientError("provider_error", "The provider rejected the model-list request. Check the provider, Base URL, and authentication settings.", detail, http.StatusBadGateway)
		}
	}
	models, err := parseDiscoveredModels(spec.Kind, req.Provider, req.Adapter, body)
	if err != nil {
		return modelDiscoveryResponse{}, newModelDiscoveryClientError("provider_response_error", "The provider returned a model list AgentGO could not read.", err.Error(), http.StatusBadGateway)
	}
	result := modelDiscoveryResponse{Models: models}
	if len(models) == 0 {
		result.Message = "The provider returned no available models. You can still enter the model name manually."
	}
	return result, nil
}

func discoveryRequestHasExplicitAuthHeader(req modelDiscoveryRequest) bool {
	header := strings.TrimSpace(req.AuthHeader)
	if header == "" {
		switch req.AuthType {
		case "bearer", "fal_key":
			header = "Authorization"
		case "header_key":
			if req.Provider == "google" {
				header = "x-goog-api-key"
			} else {
				header = "x-api-key"
			}
		}
	}
	if header == "" {
		return false
	}
	for key, value := range req.Headers {
		if strings.EqualFold(strings.TrimSpace(key), header) && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func providerDiscoveryErrorDetail(resp *http.Response, body []byte) string {
	if resp == nil {
		return ""
	}
	detail := strings.TrimSpace(string(body))
	if len(detail) > 600 {
		detail = detail[:600] + "…"
	}
	if detail == "" {
		detail = resp.Status
	} else {
		detail = resp.Status + ": " + detail
	}
	return detail
}

func buildModelDiscoverySpec(provider, adapter, rawBaseURL string) (modelDiscoverySpec, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	adapter = strings.ToLower(strings.TrimSpace(adapter))
	base, err := validateDiscoveryBaseURL(rawBaseURL)
	if err != nil {
		return modelDiscoverySpec{}, err
	}
	switch {
	case adapter == "ollama_generate" || provider == "ollama":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/api/tags", "/api"), Kind: "ollama"}, nil
	case provider == "google" && (adapter == "gemini_generate_content" || adapter == "veo_video"):
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/v1beta/models?pageSize=1000", "/v1beta"), Kind: "gemini"}, nil
	case provider == "xai" && adapter == "xai_image":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/v1/image-generation-models", "/v1"), Kind: "common"}, nil
	case provider == "xai" && adapter == "xai_video":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/v1/video-generation-models", "/v1"), Kind: "common"}, nil
	case provider == "xai":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/v1/language-models", "/v1"), Kind: "common"}, nil
	case provider == "deepseek":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/models", ""), Kind: "common"}, nil
	case provider == "openrouter":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/v1/models?output_modalities=all", "/v1"), Kind: "common"}, nil
	case provider == "openai":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/v1/models", "/v1"), Kind: "common"}, nil
	case provider == "claude":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/v1/models", "/v1"), Kind: "common"}, nil
	case provider == "mistral":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/v1/models", "/v1"), Kind: "common"}, nil
	case adapter == "openai_compatible":
		return modelDiscoverySpec{Endpoint: appendDiscoveryPath(base, "/v1/models", "/v1"), Kind: "common"}, nil
	default:
		return modelDiscoverySpec{Manual: true, Message: "Automatic model discovery is not available for this provider and adapter. Enter the model name manually."}, nil
	}
}

func validateDiscoveryBaseURL(raw string) (string, error) {
	clean := strings.TrimSpace(raw)
	parsed, err := url.Parse(clean)
	if err != nil || parsed == nil || parsed.Host == "" {
		return "", errors.New("base URL must be a valid http or https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("base URL must use http or https")
	}
	if parsed.User != nil {
		return "", errors.New("base URL must not contain embedded credentials")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func appendDiscoveryPath(base, wantedPath, versionSuffix string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	pathPart := wantedPath
	query := ""
	if idx := strings.Index(pathPart, "?"); idx >= 0 {
		query = pathPart[idx:]
		pathPart = pathPart[:idx]
	}
	if versionSuffix != "" && strings.HasSuffix(strings.ToLower(base), strings.ToLower(versionSuffix)) && strings.HasPrefix(strings.ToLower(pathPart), strings.ToLower(versionSuffix)+"/") {
		pathPart = strings.TrimPrefix(pathPart, versionSuffix)
	}
	if strings.HasSuffix(strings.ToLower(base), "/api") && strings.HasPrefix(strings.ToLower(pathPart), "/api/") {
		pathPart = strings.TrimPrefix(pathPart, "/api")
	}
	return base + pathPart + query
}

func applyModelDiscoveryHeaders(request *http.Request, req modelDiscoveryRequest) {
	key := resolveModelDiscoveryAPIKey(req)
	switch req.AuthType {
	case "bearer":
		if key != "" {
			header := strings.TrimSpace(req.AuthHeader)
			if header == "" {
				header = "Authorization"
			}
			request.Header.Set(header, "Bearer "+key)
		}
	case "header_key":
		if key != "" {
			header := strings.TrimSpace(req.AuthHeader)
			if header == "" {
				if req.Provider == "google" {
					header = "x-goog-api-key"
				} else if req.Provider == "claude" {
					header = "x-api-key"
				} else {
					header = "x-api-key"
				}
			}
			request.Header.Set(header, key)
		}
	case "fal_key":
		if key != "" {
			header := strings.TrimSpace(req.AuthHeader)
			if header == "" {
				header = "Authorization"
			}
			if !strings.HasPrefix(strings.ToLower(key), "key ") {
				key = "Key " + key
			}
			request.Header.Set(header, key)
		}
	case "basic":
		request.SetBasicAuth(req.APIUser, req.APIPass)
	}
	for header, value := range req.Headers {
		request.Header.Set(header, value)
	}
}

func resolveModelDiscoveryAPIKey(req modelDiscoveryRequest) string {
	if req.APIKeyEnv != "" {
		if value := strings.TrimSpace(os.Getenv(req.APIKeyEnv)); value != "" {
			return value
		}
	}
	if req.APIKey != "" {
		return req.APIKey
	}
	for _, envName := range providerDefaultAPIKeyEnvs(req.Provider) {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return value
		}
	}
	return ""
}

func providerDefaultAPIKeyEnvs(provider string) []string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "openai":
		return []string{"OPENAI_API_KEY"}
	case "claude":
		return []string{"ANTHROPIC_API_KEY"}
	case "google":
		return []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}
	case "xai":
		return []string{"XAI_API_KEY"}
	case "mistral":
		return []string{"MISTRAL_API_KEY"}
	case "deepseek":
		return []string{"DEEPSEEK_API_KEY"}
	case "openrouter":
		return []string{"OPENROUTER_API_KEY"}
	default:
		return nil
	}
}

func parseDiscoveredModels(kind, provider, adapter string, body []byte) ([]discoveredModel, error) {
	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("provider returned invalid JSON: %w", err)
	}
	items := discoveryItems(raw)
	models := make([]discoveredModel, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		entry, ok := normalizeDiscoveredModel(kind, provider, adapter, item)
		if !ok {
			continue
		}
		key := strings.ToLower(entry.ModelName)
		if seen[key] {
			continue
		}
		seen[key] = true
		models = append(models, entry)
		if len(models) >= modelDiscoveryMaxModels {
			break
		}
	}
	sort.Slice(models, func(i, j int) bool {
		return strings.ToLower(models[i].ModelName) < strings.ToLower(models[j].ModelName)
	})
	return models, nil
}

func discoveryItems(raw any) []map[string]any {
	var list []any
	switch value := raw.(type) {
	case []any:
		list = value
	case map[string]any:
		for _, key := range []string{"data", "models"} {
			if candidate, ok := value[key].([]any); ok {
				list = candidate
				break
			}
		}
	}
	items := make([]map[string]any, 0, len(list))
	for _, value := range list {
		if item, ok := value.(map[string]any); ok {
			items = append(items, item)
		}
	}
	return items
}

func normalizeDiscoveredModel(kind, provider, adapter string, item map[string]any) (discoveredModel, bool) {
	modelName := firstDiscoveryString(item, "id", "model", "name", "baseModelId", "base_model_id")
	if kind == "ollama" {
		modelName = firstDiscoveryString(item, "name", "model", "id")
	}
	if kind == "gemini" {
		methods := discoveryStringSlice(item["supportedGenerationMethods"])
		if len(methods) == 0 {
			methods = discoveryStringSlice(item["supported_actions"])
		}
		required := "generateContent"
		if adapter == "veo_video" {
			required = "predictLongRunning"
		}
		if len(methods) > 0 && !containsFold(methods, required) {
			return discoveredModel{}, false
		}
		if baseID := firstDiscoveryString(item, "baseModelId", "base_model_id"); baseID != "" {
			modelName = baseID
		}
	}
	modelName = strings.TrimPrefix(strings.TrimSpace(modelName), "models/")
	if modelName == "" {
		return discoveredModel{}, false
	}
	label := firstDiscoveryString(item, "displayName", "display_name", "label", "name", "id", "model")
	label = strings.TrimPrefix(strings.TrimSpace(label), "models/")
	if label == "" {
		label = modelName
	}
	notes := firstDiscoveryString(item, "description", "owned_by")
	if kind == "ollama" {
		if details, ok := item["details"].(map[string]any); ok {
			parts := []string{}
			for _, key := range []string{"parameter_size", "quantization_level", "family"} {
				if value := firstDiscoveryString(details, key); value != "" {
					parts = append(parts, value)
				}
			}
			if len(parts) > 0 {
				notes = strings.Join(parts, " · ")
			}
		}
	}
	capabilities := discoveryCapabilities(item)
	entry := discoveredModel{
		Provider:        provider,
		Adapter:         adapter,
		Label:           label,
		ModelName:       modelName,
		Notes:           notes,
		MaxOutputTokens: discoveryInt(item, "outputTokenLimit", "output_token_limit", "max_output_tokens", "max_completion_tokens"),
		Capabilities:    capabilities,
	}
	if provider == "openrouter" {
		applyOpenRouterCatalogMetadata(&entry, item)
	}
	return entry, true
}

// applyOpenRouterCatalogMetadata uses the advertised catalog fields as guidance,
// not a guarantee of end-to-end AgentGO compatibility. Unknown fields stay unknown.
func applyOpenRouterCatalogMetadata(entry *discoveredModel, item map[string]any) {
	if entry == nil {
		return
	}
	if slash := strings.IndexByte(entry.ModelName, '/'); slash > 0 {
		entry.Developer = entry.ModelName[:slash]
	}
	entry.ContextLength = discoveryPositiveInt(item["context_length"], 100000000)
	entry.Created = int64(discoveryPositiveInt(item["created"], 9999999999))
	if architecture, ok := item["architecture"].(map[string]any); ok {
		if input, present := architecture["input_modalities"].([]any); present {
			for _, modality := range discoveryStringSlice(input) {
				entry.Capabilities = appendUniqueDiscoveryString(entry.Capabilities, strings.ToLower(modality))
			}
		}
	}
	entry.FeatureStatus = map[string]string{
		"tools": "unknown", "structured_json": "unknown", "vision": "unknown", "text": "unknown",
	}
	if params, present := item["supported_parameters"].([]any); present {
		entry.SupportedParams = discoveryStringSlice(params)
		entry.FeatureStatus["tools"] = capabilityStatusForParams(entry.SupportedParams, "tools")
		// response_format advertises JSON mode, while structured_outputs advertises schema support.
		entry.FeatureStatus["structured_json"] = capabilityStatusForParams(entry.SupportedParams, "structured_outputs", "response_format")
	}
	if architecture, ok := item["architecture"].(map[string]any); ok {
		if modalities, present := architecture["input_modalities"].([]any); present {
			entry.FeatureStatus["vision"] = capabilityStatusForParams(discoveryStringSlice(modalities), "image")
		}
		if modalities, present := architecture["output_modalities"].([]any); present {
			entry.FeatureStatus["text"] = capabilityStatusForParams(discoveryStringSlice(modalities), "text")
		}
	}
	if pricing, ok := item["pricing"].(map[string]any); ok {
		entry.InputPrice = discoveryPricePerMillion(pricing["prompt"])
		entry.OutputPrice = discoveryPricePerMillion(pricing["completion"])
		if entry.InputPrice != nil && entry.OutputPrice != nil {
			if *entry.InputPrice == 0 && *entry.OutputPrice == 0 {
				requestPrice := discoveryPricePerMillion(pricing["request"])
				if requestPrice == nil || *requestPrice == 0 {
					entry.PriceCategory = "free"
				} else {
					entry.PriceCategory = "paid"
				}
			} else {
				entry.PriceCategory = "paid"
			}
		}
	}
}

func discoveryPositiveInt(value any, max int64) int {
	var n int64
	switch v := value.(type) {
	case float64:
		if v < 0 || v > float64(max) {
			return 0
		}
		n = int64(v)
	case json.Number:
		var err error
		n, err = v.Int64()
		if err != nil {
			return 0
		}
	default:
		return 0
	}
	if n <= 0 || n > max {
		return 0
	}
	return int(n)
}
func discoveryPricePerMillion(value any) *float64 {
	var raw string
	switch v := value.(type) {
	case string:
		raw = v
	case float64:
		raw = strconv.FormatFloat(v, 'g', -1, 64)
	default:
		return nil
	}
	valueFloat, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(valueFloat) || math.IsInf(valueFloat, 0) || valueFloat < 0 {
		return nil
	}
	result := valueFloat * 1000000
	if math.IsInf(result, 0) {
		return nil
	}
	return &result
}
func capabilityStatusForParams(params []string, names ...string) string {
	for _, name := range names {
		if containsFold(params, name) {
			return "supported"
		}
	}
	return "unsupported"
}
func appendUniqueDiscoveryString(values []string, needle string) []string {
	if needle == "" || containsFold(values, needle) {
		return values
	}
	return append(values, needle)
}

func discoveryCapabilities(item map[string]any) []string {
	values := []string{}
	for _, key := range []string{"input_modalities", "output_modalities", "modalities"} {
		for _, value := range discoveryStringSlice(item[key]) {
			values = append(values, strings.ToLower(value))
		}
	}
	if caps, ok := item["capabilities"].(map[string]any); ok {
		if value, ok := caps["vision"].(bool); ok && value {
			values = append(values, "image")
		}
	}
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func firstDiscoveryString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := item[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if clean := strings.TrimSpace(typed); clean != "" {
				return clean
			}
		case json.Number:
			return typed.String()
		case float64:
			return strconv.FormatFloat(typed, 'f', -1, 64)
		}
	}
	return ""
}

func discoveryStringSlice(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, value := range list {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			out = append(out, strings.TrimSpace(text))
		}
	}
	return out
}

func discoveryInt(item map[string]any, keys ...string) int {
	for _, key := range keys {
		value, ok := item[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			if typed > 0 && typed <= 2000000 {
				return int(typed)
			}
		case json.Number:
			if parsed, err := strconv.Atoi(typed.String()); err == nil && parsed > 0 && parsed <= 2000000 {
				return parsed
			}
		}
	}
	if top, ok := item["top_provider"].(map[string]any); ok {
		return discoveryInt(top, keys...)
	}
	return 0
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func userFacingDiscoveryError(err error) string {
	if err == nil {
		return "Could not connect to the provider."
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		return "Could not connect to the provider."
	}
	return message
}
