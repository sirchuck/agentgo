package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func testStructuredOutputApp(t *testing.T, model ModelConfig) *App {
	t.Helper()
	return &App{
		cfg:                AppConfig{Models: []ModelConfig{model}},
		modelsPath:         filepath.Join(t.TempDir(), "models.json"),
		modelSchemaVersion: 1,
		modelTopID:         model.ID,
	}
}

func TestContainsHarmonyTemplateMarkers(t *testing.T) {
	if !containsHarmonyTemplateMarkers(`{{ <|start|>assistant<|channel|>final<|message|> }}`) {
		t.Fatal("expected Harmony template detection")
	}
	if containsHarmonyTemplateMarkers(`ordinary chat template`) {
		t.Fatal("ordinary template must not be detected as Harmony")
	}
}

func TestAutomaticStructuredOutputUsesHarmonyTemplateWithoutProbe(t *testing.T) {
	var generateCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"template": `<|start|><|channel|><|message|>`, "model_info": map[string]any{"name": "test"}})
		case "/api/generate":
			generateCalls.Add(1)
			http.Error(w, "probe should not run", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	model := ModelConfig{ID: 1, Label: "Harmony", Provider: "ollama", Adapter: "ollama_generate", BaseURL: server.URL, APIPath: "/api/generate", ModelName: "gpt-oss:test", AuthType: "none", StructuredOutputCompatibility: structuredOutputCompatibilityAutomatic}
	app := testStructuredOutputApp(t, model)
	resolved, err := app.resolveWorkModeStructuredOutputCompatibility(context.Background(), model)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !resolved.PromptEnforcedJSON {
		t.Fatal("Harmony template should resolve to prompt-enforced JSON")
	}
	if generateCalls.Load() != 0 {
		t.Fatalf("Harmony template should avoid a probe, got %d generate calls", generateCalls.Load())
	}
	cached := app.cfg.Models[0].StructuredOutputAutomaticResolution
	if cached == nil || cached.EffectiveMode != structuredOutputCompatibilityPromptJSON || cached.DetectionSource != "ollama_template" {
		t.Fatalf("unexpected cached resolution: %#v", cached)
	}
}

func TestAutomaticStructuredOutputCachesNativeSchemaProbe(t *testing.T) {
	var generateCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"template": "ordinary", "model_info": map[string]any{"revision": 1}})
		case "/api/generate":
			generateCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"response": `{"probe":"ok"}`, "done": true, "eval_count": 4})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	model := ModelConfig{ID: 2, Label: "Standard", Provider: "ollama", Adapter: "ollama_generate", BaseURL: server.URL, APIPath: "/api/generate", ModelName: "standard:test", AuthType: "none", StructuredOutputCompatibility: structuredOutputCompatibilityAutomatic}
	app := testStructuredOutputApp(t, model)
	resolved, err := app.resolveWorkModeStructuredOutputCompatibility(context.Background(), model)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if resolved.PromptEnforcedJSON || resolved.StrictStructuredOutput == nil || !*resolved.StrictStructuredOutput {
		t.Fatal("valid native probe should resolve to provider schema")
	}
	resolved, err = app.resolveWorkModeStructuredOutputCompatibility(context.Background(), model)
	if err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
	if generateCalls.Load() != 1 {
		t.Fatalf("expected one probe across two calls, got %d", generateCalls.Load())
	}
}

func TestAutomaticStructuredOutputFallsBackToPromptJSON(t *testing.T) {
	var generateCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			_ = json.NewEncoder(w).Encode(map[string]any{"template": "ordinary", "model_info": map[string]any{"revision": 1}})
		case "/api/generate":
			generateCalls.Add(1)
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if _, hasFormat := payload["format"]; hasFormat {
				_ = json.NewEncoder(w).Encode(map[string]any{"response": "", "done": true, "eval_count": 12})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"response": `{"probe":"ok"}`, "done": true, "eval_count": 4})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	model := ModelConfig{ID: 3, Label: "Fallback", Provider: "ollama", Adapter: "ollama_generate", BaseURL: server.URL, APIPath: "/api/generate", ModelName: "fallback:test", AuthType: "none", StructuredOutputCompatibility: structuredOutputCompatibilityAutomatic}
	app := testStructuredOutputApp(t, model)
	resolved, err := app.resolveWorkModeStructuredOutputCompatibility(context.Background(), model)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !resolved.PromptEnforcedJSON {
		t.Fatal("empty native response should fall back to prompt-enforced JSON")
	}
	if generateCalls.Load() != 2 {
		t.Fatalf("expected native and prompt probes, got %d calls", generateCalls.Load())
	}
}
