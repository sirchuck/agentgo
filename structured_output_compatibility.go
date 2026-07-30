package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agentgo/adapters"
)

const (
	structuredOutputCompatibilityAutomatic      = "automatic"
	structuredOutputCompatibilityProviderSchema = "provider_schema"
	structuredOutputCompatibilityPromptJSON     = "prompt_json"
)

type structuredOutputAutoResolution struct {
	EffectiveMode   string `json:"effective_mode"`
	DetectionSource string `json:"detection_source,omitempty"`
	RuntimeKey      string `json:"runtime_key"`
	DetectedAt      string `json:"detected_at,omitempty"`
}

type structuredOutputMetadata struct {
	RuntimeKey      string
	HarmonyDetected bool
	DetectionSource string
}

func normalizeStructuredOutputCompatibility(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case structuredOutputCompatibilityProviderSchema, "standard", "standard_provider_schema", "provider", "schema":
		return structuredOutputCompatibilityProviderSchema
	case structuredOutputCompatibilityPromptJSON, "prompt_enforced_json", "prompt-enforced-json", "prompt":
		return structuredOutputCompatibilityPromptJSON
	default:
		return structuredOutputCompatibilityAutomatic
	}
}

func normalizeStoredStructuredOutputCompatibility(value string, legacyStrict *bool) string {
	if strings.TrimSpace(value) != "" {
		return normalizeStructuredOutputCompatibility(value)
	}
	if legacyStrict != nil && !*legacyStrict {
		return structuredOutputCompatibilityPromptJSON
	}
	return structuredOutputCompatibilityAutomatic
}

func normalizeStructuredOutputEffectiveMode(value string) string {
	switch normalizeStructuredOutputCompatibility(value) {
	case structuredOutputCompatibilityProviderSchema:
		return structuredOutputCompatibilityProviderSchema
	case structuredOutputCompatibilityPromptJSON:
		return structuredOutputCompatibilityPromptJSON
	default:
		return ""
	}
}

func legacyStrictStructuredOutputForCompatibility(mode, adapter string) *bool {
	if !adapterSupportsStrictStructuredOutput(adapter) {
		return nil
	}
	value := normalizeStructuredOutputCompatibility(mode) != structuredOutputCompatibilityPromptJSON
	return &value
}

func cloneStructuredOutputAutoResolution(src *structuredOutputAutoResolution) *structuredOutputAutoResolution {
	if src == nil {
		return nil
	}
	copyValue := *src
	return &copyValue
}

func applyStructuredOutputEffectiveMode(model ModelConfig, mode string) ModelConfig {
	mode = normalizeStructuredOutputEffectiveMode(mode)
	model.PromptEnforcedJSON = mode == structuredOutputCompatibilityPromptJSON
	strict := mode != structuredOutputCompatibilityPromptJSON
	model.StrictStructuredOutput = &strict
	return model
}

func structuredOutputRuntimeIdentityChanged(before, after ModelConfig) bool {
	return normalizeStructuredOutputCompatibility(before.StructuredOutputCompatibility) != normalizeStructuredOutputCompatibility(after.StructuredOutputCompatibility) ||
		!strings.EqualFold(strings.TrimSpace(before.Provider), strings.TrimSpace(after.Provider)) ||
		!strings.EqualFold(strings.TrimSpace(before.Adapter), strings.TrimSpace(after.Adapter)) ||
		strings.TrimSpace(before.BaseURL) != strings.TrimSpace(after.BaseURL) ||
		strings.TrimSpace(before.APIPath) != strings.TrimSpace(after.APIPath) ||
		strings.TrimSpace(before.ModelName) != strings.TrimSpace(after.ModelName)
}

func isLocalStructuredOutputEndpoint(model ModelConfig) bool {
	parsed, err := url.Parse(strings.TrimSpace(model.BaseURL))
	if err != nil || parsed == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "localhost" || host == "host.docker.internal" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func shouldAutomaticallyDetectStructuredOutput(model ModelConfig) bool {
	if !adapterSupportsStrictStructuredOutput(model.Adapter) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(model.Adapter), "ollama_generate") || strings.EqualFold(strings.TrimSpace(model.Provider), "ollama") {
		return true
	}
	return isLocalStructuredOutputEndpoint(model)
}

func structuredOutputStaticRuntimeKey(model ModelConfig) string {
	identity := strings.Join([]string{
		strings.ToLower(strings.TrimSpace(model.Provider)),
		strings.ToLower(strings.TrimSpace(model.Adapter)),
		strings.TrimSpace(model.BaseURL),
		strings.TrimSpace(model.APIPath),
		strings.TrimSpace(model.ModelName),
	}, "\n")
	return structuredOutputHash(identity)
}

func structuredOutputHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func containsHarmonyTemplateMarkers(raw string) bool {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded) == nil {
		raw = structuredOutputMetadataText(decoded)
	}
	lower := strings.ToLower(raw)
	markers := 0
	for _, marker := range []string{"<|start|>", "<|channel|>", "<|message|>"} {
		if strings.Contains(lower, marker) {
			markers++
		}
	}
	return markers >= 2
}

func structuredOutputMetadataText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		parts := make([]string, 0, len(typed))
		for _, child := range typed {
			if text := structuredOutputMetadataText(child); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case []any:
		parts := make([]string, 0, len(typed))
		for _, child := range typed {
			if text := structuredOutputMetadataText(child); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func (a *App) resolveWorkModeStructuredOutputCompatibility(ctx context.Context, model ModelConfig) (ModelConfig, error) {
	selection := normalizeStructuredOutputCompatibility(model.StructuredOutputCompatibility)
	model.StructuredOutputCompatibility = selection
	if selection == structuredOutputCompatibilityProviderSchema || selection == structuredOutputCompatibilityPromptJSON {
		return applyStructuredOutputEffectiveMode(model, selection), nil
	}
	if !shouldAutomaticallyDetectStructuredOutput(model) {
		return applyStructuredOutputEffectiveMode(model, structuredOutputCompatibilityProviderSchema), nil
	}

	a.structuredOutputDetectionMu.Lock()
	defer a.structuredOutputDetectionMu.Unlock()

	// A previous request may have resolved and persisted this model while this request waited.
	a.mu.RLock()
	for i := range a.cfg.Models {
		if a.cfg.Models[i].ID == model.ID {
			model = a.cfg.Models[i]
			break
		}
	}
	a.mu.RUnlock()
	if normalizeStructuredOutputCompatibility(model.StructuredOutputCompatibility) != structuredOutputCompatibilityAutomatic {
		return applyStructuredOutputEffectiveMode(model, model.StructuredOutputCompatibility), nil
	}

	metadata, err := a.inspectStructuredOutputMetadata(ctx, model)
	if err != nil {
		return model, err
	}
	if cached := model.StructuredOutputAutomaticResolution; cached != nil &&
		normalizeStructuredOutputEffectiveMode(cached.EffectiveMode) != "" &&
		strings.TrimSpace(cached.RuntimeKey) == metadata.RuntimeKey {
		return applyStructuredOutputEffectiveMode(model, cached.EffectiveMode), nil
	}

	effectiveMode := ""
	detectionSource := metadata.DetectionSource
	if metadata.HarmonyDetected {
		effectiveMode = structuredOutputCompatibilityPromptJSON
		if detectionSource == "" {
			detectionSource = "provider_template"
		}
	} else {
		nativeOK, nativeErr := a.runStructuredOutputCompatibilityProbe(ctx, model, structuredOutputCompatibilityProviderSchema)
		if nativeErr == nil && nativeOK {
			effectiveMode = structuredOutputCompatibilityProviderSchema
			detectionSource = "native_schema_probe"
		} else {
			if nativeErr != nil && !isStructuredOutputCompatibilityFailure(nativeErr) {
				return model, fmt.Errorf("structured-output compatibility probe failed: %w", nativeErr)
			}
			promptOK, promptErr := a.runStructuredOutputCompatibilityProbe(ctx, model, structuredOutputCompatibilityPromptJSON)
			if promptErr != nil {
				return model, fmt.Errorf("native schema was incompatible and the prompt-enforced JSON probe also failed: %w", promptErr)
			}
			if !promptOK {
				return model, errors.New("AgentGO could not determine a working structured-output mode. Select Standard provider schema or Prompt-enforced JSON compatibility in Model Setup")
			}
			effectiveMode = structuredOutputCompatibilityPromptJSON
			detectionSource = "prompt_json_fallback_probe"
		}
	}

	resolution := &structuredOutputAutoResolution{
		EffectiveMode:   effectiveMode,
		DetectionSource: detectionSource,
		RuntimeKey:      metadata.RuntimeKey,
		DetectedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	if err := a.persistStructuredOutputAutoResolution(model.ID, resolution); err != nil {
		return model, err
	}
	model.StructuredOutputAutomaticResolution = cloneStructuredOutputAutoResolution(resolution)
	a.logf(modelIDString(model.ID), "info", "Structured Output Automatic resolved to %s via %s", effectiveMode, detectionSource)
	return applyStructuredOutputEffectiveMode(model, effectiveMode), nil
}

func (a *App) persistStructuredOutputAutoResolution(modelID int, resolution *structuredOutputAutoResolution) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.cfg.Models {
		if a.cfg.Models[i].ID != modelID {
			continue
		}
		if normalizeStructuredOutputCompatibility(a.cfg.Models[i].StructuredOutputCompatibility) != structuredOutputCompatibilityAutomatic {
			return nil
		}
		a.cfg.Models[i].StructuredOutputAutomaticResolution = cloneStructuredOutputAutoResolution(resolution)
		a.cfg.Models[i].UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		return a.persistModelsLocked()
	}
	return errors.New("could not cache structured-output compatibility for an unknown model")
}

func (a *App) inspectStructuredOutputMetadata(ctx context.Context, model ModelConfig) (structuredOutputMetadata, error) {
	metadata := structuredOutputMetadata{RuntimeKey: structuredOutputStaticRuntimeKey(model)}
	adapter := strings.ToLower(strings.TrimSpace(model.Adapter))
	provider := strings.ToLower(strings.TrimSpace(model.Provider))
	var raw []byte
	var source string
	var err error
	switch {
	case adapter == "ollama_generate" || provider == "ollama":
		raw, err = fetchOllamaStructuredOutputMetadata(ctx, model)
		source = "ollama_template"
	case isLocalStructuredOutputEndpoint(model):
		raw, err = fetchLocalCompatibleStructuredOutputMetadata(ctx, model)
		source = "local_provider_template"
	}
	if err != nil {
		return metadata, err
	}
	if len(raw) == 0 {
		return metadata, nil
	}
	metadata.RuntimeKey = structuredOutputHash(metadata.RuntimeKey + "\n" + string(raw))
	metadata.HarmonyDetected = containsHarmonyTemplateMarkers(string(raw))
	if metadata.HarmonyDetected {
		metadata.DetectionSource = source
	}
	return metadata, nil
}

func fetchOllamaStructuredOutputMetadata(ctx context.Context, model ModelConfig) ([]byte, error) {
	endpoint := appendDiscoveryPath(strings.TrimSpace(model.BaseURL), "/api/show", "/api")
	body, _ := json.Marshal(map[string]string{"model": strings.TrimSpace(model.ModelName)})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	applyModelDiscoveryHeaders(request, structuredOutputDiscoveryRequest(model))
	return readOptionalStructuredOutputMetadataResponse(request)
}

func fetchLocalCompatibleStructuredOutputMetadata(ctx context.Context, model ModelConfig) ([]byte, error) {
	parsed, err := url.Parse(strings.TrimSpace(model.BaseURL))
	if err != nil || parsed == nil || parsed.Host == "" {
		return nil, nil
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(strings.ToLower(cleanPath), "/v1") {
		cleanPath = strings.TrimSuffix(cleanPath, "/v1")
	}
	parsed.Path = strings.TrimRight(cleanPath, "/") + "/props"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	applyModelDiscoveryHeaders(request, structuredOutputDiscoveryRequest(model))
	return readOptionalStructuredOutputMetadataResponse(request)
}

func structuredOutputDiscoveryRequest(model ModelConfig) modelDiscoveryRequest {
	return modelDiscoveryRequest{
		Provider:   model.Provider,
		Adapter:    model.Adapter,
		BaseURL:    model.BaseURL,
		AuthType:   model.AuthType,
		AuthHeader: model.AuthHeader,
		APIUser:    model.APIUser,
		APIPass:    model.APIPass,
		APIKey:     model.APIKey,
		APIKeyEnv:  model.APIKeyEnv,
		Headers:    cloneStringMap(model.Headers),
	}
}

func readOptionalStructuredOutputMetadataResponse(request *http.Request) ([]byte, error) {
	client := &http.Client{Timeout: modelDiscoveryTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("could not inspect provider model metadata: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, modelDiscoveryMaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > modelDiscoveryMaxBodyBytes {
		return nil, errors.New("provider model metadata was too large")
	}
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return nil, nil
	}
	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("provider model metadata returned %s", response.Status)
	}
	return body, nil
}

func structuredOutputProbeSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"probe": map[string]any{"type": "string", "enum": []string{"ok"}},
		},
		"required":             []string{"probe"},
		"additionalProperties": false,
	}
}

func (a *App) runStructuredOutputCompatibilityProbe(ctx context.Context, model ModelConfig, mode string) (bool, error) {
	probeModel := applyStructuredOutputEffectiveMode(model, mode)
	response, err := adapters.Execute(ctx, a.adapterModelConfig(probeModel), adapters.Request{
		Instructions: "Return exactly one JSON object matching the supplied schema. Return no markdown or extra text.",
		Messages: []adapters.Message{{
			Role: "user",
			Text: `Return {"probe":"ok"}.`,
		}},
		ExpectJSON: true,
		JSONSchema: structuredOutputProbeSchema(),
	})
	if err != nil {
		return false, err
	}
	candidate, err := workModeJSONCandidate(response.Text)
	if err != nil {
		return false, nil
	}
	var parsed struct {
		Probe string `json:"probe"`
	}
	if json.Unmarshal([]byte(candidate), &parsed) != nil {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(parsed.Probe), "ok"), nil
}

func isStructuredOutputCompatibilityFailure(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"response contained no output",
		"unable to parse",
		"returned 400",
		"returned 404",
		"returned 405",
		"returned 415",
		"returned 422",
		"response_format",
		"json schema",
		"structured output",
	} {
		if strings.Contains(text, fragment) {
			return true
		}
	}
	return false
}
