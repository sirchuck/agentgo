package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaPromptEnforcedJSONOmitsNativeFormat(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"{\"probe\":\"ok\"}","done":true}`))
	}))
	defer server.Close()

	strict := false
	_, err := Execute(context.Background(), ModelConfig{
		Provider:               "ollama",
		Adapter:                "ollama_generate",
		BaseURL:                server.URL,
		APIPath:                "/api/generate",
		ModelName:              "test",
		AuthType:               "none",
		StrictStructuredOutput: &strict,
		PromptEnforcedJSON:     true,
	}, Request{ExpectJSON: true, JSONSchema: map[string]any{"type": "object"}, Messages: []Message{{Role: "user", Text: "probe"}}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := payload["format"]; ok {
		t.Fatalf("prompt-enforced JSON must omit Ollama format, got %#v", payload["format"])
	}
}

func TestOpenAICompatibleProviderSchemaUsesResponseFormat(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"probe\":\"ok\"}"}}]}`))
	}))
	defer server.Close()

	strict := true
	_, err := Execute(context.Background(), ModelConfig{
		Provider:               "llama.cpp",
		Adapter:                "openai_compatible",
		BaseURL:                server.URL,
		APIPath:                "/v1/chat/completions",
		ModelName:              "test",
		AuthType:               "none",
		StrictStructuredOutput: &strict,
	}, Request{ExpectJSON: true, JSONSchema: map[string]any{"type": "object", "properties": map[string]any{"probe": map[string]any{"type": "string"}}}, Messages: []Message{{Role: "user", Text: "probe"}}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	format, ok := payload["response_format"].(map[string]any)
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("expected json_schema response_format, got %#v", payload["response_format"])
	}
}

func TestOpenAICompatiblePromptEnforcedJSONOmitsResponseFormat(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"probe\":\"ok\"}"}}]}`))
	}))
	defer server.Close()

	strict := false
	_, err := Execute(context.Background(), ModelConfig{
		Provider:               "llama.cpp",
		Adapter:                "openai_compatible",
		BaseURL:                server.URL,
		APIPath:                "/v1/chat/completions",
		ModelName:              "test",
		AuthType:               "none",
		StrictStructuredOutput: &strict,
		PromptEnforcedJSON:     true,
	}, Request{ExpectJSON: true, JSONSchema: map[string]any{"type": "object"}, Messages: []Message{{Role: "user", Text: "probe"}}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if _, ok := payload["response_format"]; ok {
		t.Fatalf("prompt-enforced JSON must omit response_format, got %#v", payload["response_format"])
	}
}
