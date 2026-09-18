package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestR462WorkModeReplyFadeUsesMask(t *testing.T) {
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)

	checks := []string{
		".work-mode-stage:not(.is-review-mode){-webkit-mask-image:linear-gradient(to bottom,#000 0,#000 calc(100% - 28px),transparent 100%)",
		"mask-image:linear-gradient(to bottom,#000 0,#000 calc(100% - 28px),transparent 100%)",
		"-webkit-mask-repeat:no-repeat;mask-repeat:no-repeat",
	}
	for _, check := range checks {
		if !strings.Contains(html, check) {
			t.Fatalf("r462 reply-content mask missing %q", check)
		}
	}

	if strings.Contains(html, ".work-mode-prompt-wrap::after") {
		t.Fatal("legacy prompt-wrap dark gradient overlay still present")
	}
	if strings.Contains(html, "rgba(8,10,15,.90) 20px") {
		t.Fatal("legacy dark fade paint still present")
	}
}

func TestR462RevisionLabels(t *testing.T) {
	data, err := os.ReadFile("version.json")
	if err != nil {
		t.Fatal(err)
	}
	var version struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(data, &version); err != nil {
		t.Fatal(err)
	}
	if version.Revision != "r462" {
		t.Fatalf("revision = %q, want r462", version.Revision)
	}

	helper, err := os.ReadFile("cmd/kokoro-helper/main.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(helper), `const helperVersion = "agentgo-kokoro-helper-r462"`) {
		t.Fatal("Kokoro helper revision label was not bumped to r462")
	}
}
