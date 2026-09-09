package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func r453Envelope(reply string) string {
	data, _ := json.Marshal(map[string]any{
		"reply":     reply,
		"files":     []any{},
		"artifacts": []any{},
		"memory":    "",
		"warnings":  []any{},
	})
	return string(data)
}

func assertR453EnvelopeReply(t *testing.T, raw, want string) {
	t.Helper()
	var parsed struct {
		Reply string `json:"reply"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("response text is not the structured envelope: %v\n%s", err, raw)
	}
	if parsed.Reply != want {
		t.Fatalf("reply = %q, want %q; raw=%s", parsed.Reply, want, raw)
	}
}

func TestR453AnthropicProviderSchemaExtractsToolUseInputBeforeRawFallback(t *testing.T) {
	body := []byte(`{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_test","name":"agentgo_work_mode_response","input":{"reply":"anthropic","files":[],"artifacts":[],"memory":"","warnings":[]}}]}`)
	response, err := parseAnthropicResponse(body, "application/json", ModelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	assertR453EnvelopeReply(t, response.Text, "anthropic")
	if strings.Contains(response.Text, `"type":"message"`) || strings.Contains(response.Text, `"tool_use"`) {
		t.Fatalf("Anthropic outer response leaked into Work Mode text: %s", response.Text)
	}
}

func TestR453AnthropicProviderSchemaRequestAndResponseRoundTrip(t *testing.T) {
	var requestPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Fatalf("decode Anthropic request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_test","name":"agentgo_work_mode_response","input":{"reply":"round-trip","files":[],"artifacts":[],"memory":"","warnings":[]}}]}`))
	}))
	defer server.Close()

	strict := true
	response, err := Execute(context.Background(), ModelConfig{
		Provider:               "claude",
		Adapter:                "anthropic_messages",
		BaseURL:                server.URL,
		APIPath:                "/v1/messages",
		ModelName:              "claude-test",
		AuthType:               "none",
		StrictStructuredOutput: &strict,
	}, Request{
		Instructions: "Return the AgentGO envelope.",
		Messages:     []Message{{Role: "user", Text: "hello"}},
		ExpectJSON:   true,
		JSONSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"reply":     map[string]any{"type": "string"},
				"files":     map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"artifacts": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"memory":    map[string]any{"type": "string"},
				"warnings":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertR453EnvelopeReply(t, response.Text, "round-trip")
	tools, ok := requestPayload["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("Anthropic provider-schema request missing structured tool: %#v", requestPayload["tools"])
	}
	choice, ok := requestPayload["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "tool" || choice["name"] != "agentgo_work_mode_response" {
		t.Fatalf("Anthropic provider-schema tool_choice = %#v", requestPayload["tool_choice"])
	}
}

func TestR453OtherProviderSchemaParsersDoNotUseOuterRawResponse(t *testing.T) {
	openAIEnvelope := r453Envelope("openai")
	xAIEnvelope := r453Envelope("xai")
	geminiEnvelope := r453Envelope("gemini")
	ollamaEnvelope := r453Envelope("ollama")
	compatibleEnvelope := r453Envelope("compatible")

	openAIBody, _ := json.Marshal(map[string]any{
		"status": "completed",
		"output": []any{map[string]any{
			"type":    "message",
			"content": []any{map[string]any{"type": "output_text", "text": openAIEnvelope}},
		}},
	})
	xAIBody, _ := json.Marshal(map[string]any{
		"status": "completed",
		"output": []any{map[string]any{
			"type":    "message",
			"content": []any{map[string]any{"type": "output_text", "text": xAIEnvelope}},
		}},
	})
	geminiBody, _ := json.Marshal(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": []any{map[string]any{"text": geminiEnvelope}}},
		}},
	})
	ollamaBody, _ := json.Marshal(map[string]any{"response": ollamaEnvelope, "done": true})
	compatibleBody, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": compatibleEnvelope}}}})

	tests := []struct {
		name string
		want string
		run  func() (Response, error)
	}{
		{name: "openai_responses", want: "openai", run: func() (Response, error) { return parseOpenAIResponse(openAIBody, "application/json", ModelConfig{}) }},
		{name: "xai_responses_shared_openai_parser", want: "xai", run: func() (Response, error) { return parseOpenAIResponse(xAIBody, "application/json", ModelConfig{}) }},
		{name: "gemini_generate_content", want: "gemini", run: func() (Response, error) { return parseGeminiResponse(geminiBody, "application/json", ModelConfig{}) }},
		{name: "ollama_generate", want: "ollama", run: func() (Response, error) {
			return parseMultipartCapableResponse(ollamaBody, "application/json", ModelConfig{}, parseOllamaResponseText)
		}},
		{name: "openai_compatible_chat", want: "compatible", run: func() (Response, error) {
			return parseOpenAICompatibleChatResponse(compatibleBody, "application/json", ModelConfig{})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response, err := tt.run()
			if err != nil {
				t.Fatal(err)
			}
			assertR453EnvelopeReply(t, response.Text, tt.want)
		})
	}
}
