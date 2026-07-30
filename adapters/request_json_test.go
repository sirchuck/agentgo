package adapters

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMarshalProviderRequestJSONKeepsPromptHTMLLiteral(t *testing.T) {
	body, err := marshalProviderRequestJSON(map[string]string{"instructions": `<h3 style="color:#2b6cb0">Title</h3>`})
	if err != nil {
		t.Fatalf("marshal provider request: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, `<h3 style=\"color:#2b6cb0\">Title</h3>`) {
		t.Fatalf("provider request did not retain literal prompt HTML: %s", text)
	}
	if strings.Contains(text, `\u003c`) || strings.Contains(text, `\u003e`) {
		t.Fatalf("provider request still HTML-escaped prompt characters: %s", text)
	}
}

func TestOllamaRequestSerializationKeepsSystemPromptHTMLLiteral(t *testing.T) {
	var rawBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		rawBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"response":"ok","done":true}`))
	}))
	defer server.Close()

	_, err := Execute(context.Background(), ModelConfig{
		Provider:  "ollama",
		Adapter:   "ollama_generate",
		BaseURL:   server.URL,
		APIPath:   "/api/generate",
		ModelName: "test",
		AuthType:  "none",
	}, Request{Instructions: `<h3 style="color:#2b6cb0">Title</h3>`, Messages: []Message{{Role: "user", Text: "hello"}}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(rawBody, `<h3 style=\"color:#2b6cb0\">Title</h3>`) {
		t.Fatalf("Ollama request did not retain literal prompt HTML: %s", rawBody)
	}
	if strings.Contains(rawBody, `\u003c`) || strings.Contains(rawBody, `\u003e`) {
		t.Fatalf("Ollama request still HTML-escaped prompt characters: %s", rawBody)
	}
}
