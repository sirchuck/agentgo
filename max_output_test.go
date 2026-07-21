package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadKnownModelMaxOutputCatalogAndResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model_names.json")
	payload := `{"models":[{"provider":"openai","adapter":"openai_responses","model_name":"gpt-test","max_output_tokens":64000}]}`
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := loadKnownModelMaxOutputCatalog(path)
	if err != nil {
		t.Fatal(err)
	}
	automatic := resolveModelMaxOutputTokens(ModelConfig{Provider: "openai", Adapter: "openai_responses", ModelName: "gpt-test"}, catalog)
	if automatic.Mode != "automatic" || automatic.Source != "known_model_maximum" || automatic.EffectiveTokens != 64000 {
		t.Fatalf("automatic resolution = %#v", automatic)
	}
	custom := resolveModelMaxOutputTokens(ModelConfig{Provider: "openai", Adapter: "openai_responses", ModelName: "gpt-test", MaxOutputTokens: 12000}, catalog)
	if custom.Mode != "custom" || custom.Source != "custom_guardrail" || custom.EffectiveTokens != 12000 {
		t.Fatalf("custom resolution = %#v", custom)
	}
}

func TestResolveModelMaxOutputTokensProviderManagedAndRequiredFallback(t *testing.T) {
	catalog := knownModelMaxOutputCatalog{byExactKey: map[string]int{}, byAdapterKey: map[string]int{}}
	openAI := resolveModelMaxOutputTokens(ModelConfig{Provider: "openai", Adapter: "openai_responses", ModelName: "unknown"}, catalog)
	if openAI.Source != "provider_managed" || openAI.EffectiveTokens != 0 {
		t.Fatalf("OpenAI resolution = %#v", openAI)
	}
	claude := resolveModelMaxOutputTokens(ModelConfig{Provider: "anthropic", Adapter: "anthropic_messages", ModelName: "unknown"}, catalog)
	if claude.Source != "provider_required_fallback" || claude.EffectiveTokens != anthropicAutomaticMaxOutputTokens {
		t.Fatalf("Anthropic resolution = %#v", claude)
	}
}

func TestResolveModelMaxOutputTokensIgnoresMediaEndpoints(t *testing.T) {
	catalog := knownModelMaxOutputCatalog{byExactKey: map[string]int{}, byAdapterKey: map[string]int{}}
	for _, model := range []ModelConfig{
		{Provider: "openai", Adapter: "openai_responses", ModelName: "gpt-image-1", MaxOutputTokens: 9999},
		{Provider: "google", Adapter: "gemini_generate_content", ModelName: "gemini-2.5-flash-image", MaxOutputTokens: 9999},
		{Provider: "google", Adapter: "gemini_generate_content", ModelName: "gemini-video", VideoGeneration: true, MaxOutputTokens: 9999},
		{Provider: "fal", Adapter: "fal_mesh", ModelName: "mesh", MeshGeneration: true, MaxOutputTokens: 9999},
	} {
		got := resolveModelMaxOutputTokens(model, catalog)
		if got.Supported || got.EffectiveTokens != 0 || got.SavedTokens != 0 || got.Source != "unsupported" {
			t.Fatalf("media resolution = %#v for %#v", got, model)
		}
	}
}

func TestAdapterModelConfigUsesResolvedMaximumWithoutMutatingSavedModel(t *testing.T) {
	catalog := knownModelMaxOutputCatalog{
		byExactKey:   map[string]int{},
		byAdapterKey: map[string]int{maxOutputAdapterKey("gemini_generate_content", "gemini-test"): 65536},
	}
	app := &App{knownModelMaxOutputCatalog: catalog}
	model := ModelConfig{Provider: "google", Adapter: "gemini_generate_content", ModelName: "gemini-test"}
	got := app.adapterModelConfig(model)
	if got.MaxOutputTokens != 65536 {
		t.Fatalf("adapter maximum = %d, want 65536", got.MaxOutputTokens)
	}
	if model.MaxOutputTokens != 0 {
		t.Fatalf("saved model mutated to %d", model.MaxOutputTokens)
	}
}

func TestSanitizeModelDefinitionReportsAutomaticMaximum(t *testing.T) {
	catalog := knownModelMaxOutputCatalog{
		byExactKey:   map[string]int{},
		byAdapterKey: map[string]int{maxOutputAdapterKey("openai_responses", "gpt-test"): 32768},
	}
	app := &App{knownModelMaxOutputCatalog: catalog}
	view := app.sanitizeModelDefinition(ModelConfig{ID: 7, Label: "Test", Provider: "openai", Adapter: "openai_responses", ModelName: "gpt-test"})
	if view.MaxOutputMode != "automatic" || view.KnownMaxOutputTokens != 32768 || view.EffectiveMaxOutputTokens != 32768 || view.MaxOutputSource != "known_model_maximum" || !view.SupportsMaxOutputTokens {
		t.Fatalf("definition view = %#v", view)
	}
}

func TestHandleModelMaxOutputTokensRejectsMediaGuardrail(t *testing.T) {
	app := &App{
		cfg:                AppConfig{Models: []ModelConfig{{ID: 1, Label: "Image", Provider: "openai", Adapter: "openai_responses", ModelName: "gpt-image-1"}}},
		modelsPath:         filepath.Join(t.TempDir(), "models.json"),
		modelSchemaVersion: 1,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/models/max-output-tokens", strings.NewReader(`{"models":[{"modelId":"1","maxOutputTokens":32000}]}`))
	rr := httptest.NewRecorder()
	app.handleModelMaxOutputTokens(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(strings.ToLower(rr.Body.String()), "does not support") {
		t.Fatalf("error = %q", rr.Body.String())
	}
}
