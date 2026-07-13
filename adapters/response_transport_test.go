package adapters

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveResponseMediaIgnoresEmptyOptionalMetadataPaths(t *testing.T) {
	imageData := []byte{0xff, 0xd8, 0xff, 0xd9}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(imageData)
	}))
	defer server.Close()

	payload, err := json.Marshal(map[string]any{
		"data":  []any{map[string]any{"url": server.URL + "/generated/potato.jpeg"}},
		"usage": map[string]any{"cost_in_usd_ticks": 600000000},
	})
	if err != nil {
		t.Fatal(err)
	}

	response, err := resolveResponseMedia(payload, ModelConfig{ProviderOptions: map[string]any{}})
	if err != nil {
		t.Fatalf("resolveResponseMedia failed: %v", err)
	}
	if response.FileName != "potato.jpeg" {
		t.Fatalf("FileName = %q, want potato.jpeg", response.FileName)
	}
	if response.FileMIMEType != "image/jpeg" {
		t.Fatalf("FileMIMEType = %q, want image/jpeg", response.FileMIMEType)
	}
	if !bytes.Equal(response.FileData, imageData) {
		t.Fatalf("FileData = %v, want %v", response.FileData, imageData)
	}
}
