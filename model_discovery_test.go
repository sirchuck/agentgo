package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildModelDiscoverySpecUsesProviderSpecificEndpoints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		provider string
		adapter  string
		baseURL  string
		wantURL  string
		wantKind string
		manual   bool
	}{
		{name: "ollama root", provider: "ollama", adapter: "ollama_generate", baseURL: "http://127.0.0.1:11434", wantURL: "http://127.0.0.1:11434/api/tags", wantKind: "ollama"},
		{name: "ollama api base", provider: "ollama", adapter: "ollama_generate", baseURL: "http://127.0.0.1:11434/api", wantURL: "http://127.0.0.1:11434/api/tags", wantKind: "ollama"},
		{name: "openai root", provider: "openai", adapter: "openai_responses", baseURL: "https://api.openai.test", wantURL: "https://api.openai.test/v1/models", wantKind: "common"},
		{name: "openai versioned base", provider: "openai", adapter: "openai_responses", baseURL: "https://api.openai.test/v1", wantURL: "https://api.openai.test/v1/models", wantKind: "common"},
		{name: "openrouter api base", provider: "openrouter", adapter: "openai_compatible", baseURL: "https://openrouter.test/api", wantURL: "https://openrouter.test/api/v1/models", wantKind: "common"},
		{name: "generic openai compatible", provider: "custom", adapter: "openai_compatible", baseURL: "https://local.test/v1", wantURL: "https://local.test/v1/models", wantKind: "common"},
		{name: "gemini root", provider: "google", adapter: "gemini_generate_content", baseURL: "https://generativelanguage.test", wantURL: "https://generativelanguage.test/v1beta/models?pageSize=1000", wantKind: "gemini"},
		{name: "gemini versioned base", provider: "google", adapter: "gemini_generate_content", baseURL: "https://generativelanguage.test/v1beta", wantURL: "https://generativelanguage.test/v1beta/models?pageSize=1000", wantKind: "gemini"},
		{name: "deepseek root", provider: "deepseek", adapter: "openai_compatible", baseURL: "https://api.deepseek.test", wantURL: "https://api.deepseek.test/models", wantKind: "common"},
		{name: "fully custom", provider: "custom", adapter: "custom_json", baseURL: "https://custom.test", manual: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := buildModelDiscoverySpec(tt.provider, tt.adapter, tt.baseURL)
			if err != nil {
				t.Fatalf("buildModelDiscoverySpec: %v", err)
			}
			if spec.Manual != tt.manual {
				t.Fatalf("Manual = %v, want %v", spec.Manual, tt.manual)
			}
			if tt.manual {
				if !strings.Contains(spec.Message, "Enter the model name manually") {
					t.Fatalf("manual message = %q", spec.Message)
				}
				return
			}
			if spec.Endpoint != tt.wantURL {
				t.Fatalf("Endpoint = %q, want %q", spec.Endpoint, tt.wantURL)
			}
			if spec.Kind != tt.wantKind {
				t.Fatalf("Kind = %q, want %q", spec.Kind, tt.wantKind)
			}
		})
	}
}

func TestDiscoverOllamaModelsParsesInstalledModels(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/tags" {
			t.Fatalf("request = %s %s, want GET /api/tags", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-AgentGO-Test"); got != "present" {
			t.Fatalf("custom header = %q, want present", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen2.5:7b","details":{"parameter_size":"7.6B","quantization_level":"Q4_K_M"}},{"name":"gpt-oss:20b","details":{"parameter_size":"20.9B","family":"gptoss"}}]}`))
	}))
	defer server.Close()

	result, err := discoverProviderModels(context.Background(), modelDiscoveryRequest{
		Provider: "ollama",
		Adapter:  "ollama_generate",
		BaseURL:  server.URL,
		AuthType: "none",
		Headers:  map[string]string{"X-AgentGO-Test": "present"},
	})
	if err != nil {
		t.Fatalf("discoverProviderModels: %v", err)
	}
	if len(result.Models) != 2 {
		t.Fatalf("models = %#v, want 2", result.Models)
	}
	if result.Models[0].ModelName != "gpt-oss:20b" || result.Models[1].ModelName != "qwen2.5:7b" {
		t.Fatalf("sorted model names = %#v", result.Models)
	}
	if !strings.Contains(result.Models[0].Notes, "20.9B") {
		t.Fatalf("Ollama model notes = %q", result.Models[0].Notes)
	}
}

func TestDiscoverOpenAICompatibleModelsUsesCurrentBearerAndParses(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer unsaved-key" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"beta-model","owned_by":"local"},{"id":"alpha-model","owned_by":"local"}]}`))
	}))
	defer server.Close()

	result, err := discoverProviderModels(context.Background(), modelDiscoveryRequest{
		Provider: "custom",
		Adapter:  "openai_compatible",
		BaseURL:  server.URL,
		AuthType: "bearer",
		APIKey:   "unsaved-key",
	})
	if err != nil {
		t.Fatalf("discoverProviderModels: %v", err)
	}
	if len(result.Models) != 2 || result.Models[0].ModelName != "alpha-model" || result.Models[1].ModelName != "beta-model" {
		t.Fatalf("models = %#v", result.Models)
	}
}

func TestDiscoverGeminiModelsFiltersUnsupportedMethodsAndUsesAPIKeyHeader(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" || r.URL.Query().Get("pageSize") != "1000" {
			t.Fatalf("request URL = %q", r.URL.String())
		}
		if got := r.Header.Get("x-goog-api-key"); got != "gemini-key" {
			t.Fatalf("x-goog-api-key = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"models/gemini-good","displayName":"Gemini Good","supportedGenerationMethods":["generateContent"],"outputTokenLimit":8192},{"name":"models/embedding-only","supportedGenerationMethods":["embedContent"]}]}`))
	}))
	defer server.Close()

	result, err := discoverProviderModels(context.Background(), modelDiscoveryRequest{
		Provider:   "google",
		Adapter:    "gemini_generate_content",
		BaseURL:    server.URL,
		AuthType:   "header_key",
		AuthHeader: "x-goog-api-key",
		APIKey:     "gemini-key",
	})
	if err != nil {
		t.Fatalf("discoverProviderModels: %v", err)
	}
	if len(result.Models) != 1 {
		t.Fatalf("models = %#v, want one generateContent model", result.Models)
	}
	if got := result.Models[0]; got.ModelName != "gemini-good" || got.Label != "Gemini Good" || got.MaxOutputTokens != 8192 {
		t.Fatalf("model = %#v", got)
	}
}

func TestApplySavedDiscoverySecretsPreservesUnsavedValuesAndClearFlags(t *testing.T) {
	t.Parallel()
	saved := ModelConfig{
		APIKey:    "saved-key",
		APIKeyEnv: "SAVED_ENV",
		APIPass:   "saved-pass",
		Headers:   map[string]string{"X-Saved": "yes"},
	}

	req := modelDiscoveryRequest{APIKey: "unsaved-key", APIPass: "unsaved-pass", Headers: map[string]string{"X-New": "yes"}, HeadersPresent: true}
	applySavedDiscoverySecrets(&req, &saved)
	if req.APIKey != "unsaved-key" || req.APIPass != "unsaved-pass" || req.Headers["X-New"] != "yes" {
		t.Fatalf("unsaved values were not preserved: %#v", req)
	}
	if req.APIKeyEnv != "" {
		t.Fatalf("APIKeyEnv = %q, want current blank field preserved", req.APIKeyEnv)
	}

	cleared := modelDiscoveryRequest{ClearAPIKey: true, ClearAPIPass: true, ClearHeaders: true}
	applySavedDiscoverySecrets(&cleared, &saved)
	if cleared.APIKey != "" || cleared.APIPass != "" || len(cleared.Headers) != 0 {
		t.Fatalf("clear flags restored saved secrets: %#v", cleared)
	}
}

func TestHandleModelDiscoveryUsesSavedSecretWithoutPersistingLookupChanges(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer saved-key" {
			t.Fatalf("Authorization = %q, want saved key", got)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	}))
	defer server.Close()

	app := &App{cfg: AppConfig{Models: []ModelConfig{{ID: 25, APIKey: "saved-key", Headers: map[string]string{"X-Saved": "yes"}}}}}
	body := `{"modelId":"25","provider":"custom","adapter":"openai_compatible","base_url":` + stringMustJSON(server.URL) + `,"auth_type":"bearer"}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/models/discover", strings.NewReader(body))
	app.handleModelDiscovery(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response modelDiscoveryResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Models) != 1 || response.Models[0].ModelName != "test-model" {
		t.Fatalf("response = %#v", response)
	}
	if got := app.cfg.Models[0].APIKey; got != "saved-key" {
		t.Fatalf("saved config changed during lookup: %q", got)
	}
}

func TestValidateDiscoveryBaseURLRejectsUnsafeOrInvalidValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "localhost:11434", "file:///tmp/models", "http://user:pass@example.test"} {
		if _, err := validateDiscoveryBaseURL(value); err == nil {
			t.Fatalf("validateDiscoveryBaseURL(%q) unexpectedly succeeded", value)
		}
	}
}

func stringMustJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
