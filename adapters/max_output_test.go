package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIResponsesTextRequestIncludesMaxOutputTokens(t *testing.T) {
	payload := buildOpenAIResponsesRequest(ModelConfig{Adapter: "openai_responses", ModelName: "gpt-test", MaxOutputTokens: 32000}, Request{Messages: []Message{{Role: "user", Text: "hello"}}}, "gpt-test")
	if payload.MaxOutputTokens != 32000 {
		t.Fatalf("max_output_tokens = %d, want 32000", payload.MaxOutputTokens)
	}
}

func TestOpenAIResponsesImageRequestOmitsMaxOutputTokens(t *testing.T) {
	payload := buildOpenAIResponsesRequest(ModelConfig{Provider: "openai", Adapter: "openai_responses", ModelName: "gpt-image-1", MaxOutputTokens: 32000}, Request{Messages: []Message{{Role: "user", Text: "create an image"}}}, "gpt-image-1")
	if payload.MaxOutputTokens != 0 {
		t.Fatalf("image max_output_tokens = %d, want 0", payload.MaxOutputTokens)
	}
	if len(payload.Tools) == 0 {
		t.Fatal("expected image-generation tool")
	}
}

func TestAnthropicMaxTokensUsesResolvedOrAutomaticMaximum(t *testing.T) {
	if got := anthropicMaxTokens(ModelConfig{MaxOutputTokens: 64000}); got != 64000 {
		t.Fatalf("resolved max_tokens = %d, want 64000", got)
	}
	if got := anthropicMaxTokens(ModelConfig{}); got != 128000 {
		t.Fatalf("automatic fallback = %d, want 128000", got)
	}
}

func TestOllamaSharedMaxOutputOverridesAdvancedNumPredict(t *testing.T) {
	options := ollamaRequestOptions(ModelConfig{
		MaxOutputTokens: 32000,
		ProviderOptions: map[string]any{
			"options":     map[string]any{"num_ctx": float64(8192), "num_predict": float64(123)},
			"num_predict": float64(456),
		},
	})
	if got := int(options["num_predict"].(int)); got != 32000 {
		t.Fatalf("num_predict = %d, want 32000", got)
	}
	if got := int(options["num_ctx"].(float64)); got != 8192 {
		t.Fatalf("num_ctx = %d, want 8192", got)
	}
}

func TestOllamaAutomaticModeDoesNotReuseRawNumPredict(t *testing.T) {
	options := ollamaRequestOptions(ModelConfig{ProviderOptions: map[string]any{"options": map[string]any{"num_ctx": float64(8192), "num_predict": float64(123)}}})
	if _, exists := options["num_predict"]; exists {
		t.Fatalf("raw num_predict must not override Automatic Maximum: %#v", options)
	}
}

func TestGeminiGenerationConfigUsesMaxOutputTokens(t *testing.T) {
	config := buildGeminiGenerationConfig(ModelConfig{MaxOutputTokens: 65536}, Request{})
	if config == nil || config.MaxOutputTokens != 65536 {
		t.Fatalf("Gemini config = %#v", config)
	}
}

func TestOpenAICompatibleChatUsesMaxTokens(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	_, err := executeOpenAICompatibleChat(context.Background(), ModelConfig{ModelName: "compatible-test", MaxOutputTokens: 48000}, Request{Messages: []Message{{Role: "user", Text: "hello"}}}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got := int(requestBody["max_tokens"].(float64)); got != 48000 {
		t.Fatalf("max_tokens = %d, want 48000", got)
	}
}
