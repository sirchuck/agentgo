package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestR453FinalOutputRequirementStaysLast(t *testing.T) {
	instructions := buildWorkModeInstructions(ModelConfig{}, false, false, "", nil, "AGENTGO STYLING TEST", "MODEL CORRECTION TEST")
	instructions = appendWorkModeHTMLPreviewInstructions(instructions, true)
	instructions = appendWorkModeFinalOutputRequirement(instructions)
	if !strings.HasSuffix(strings.TrimSpace(instructions), strings.TrimSpace(workModeFinalOutputRequirementInstruction)) {
		t.Fatalf("final output requirement is not last:\n%s", instructions)
	}
	for _, required := range []string{
		"FINAL OUTPUT REQUIREMENT",
		`one valid AgentGO Work Mode JSON object`,
		`inside the JSON "reply" string`,
		`before or after the JSON object`,
	} {
		if !strings.Contains(instructions, required) {
			t.Fatalf("missing final Builder JSON reminder %q", required)
		}
	}

	polish := buildWorkModeSmallModelPolishPayload(false, "STYLE", workModeJSONSchema(), "hello", `{"reply":"ok","files":[],"artifacts":[],"memory":"","warnings":[]}`)
	if !strings.HasSuffix(strings.TrimSpace(polish.Instructions), strings.TrimSpace(workModeFinalOutputRequirementInstruction)) {
		t.Fatalf("small-model polish instructions do not end with final JSON reminder:\n%s", polish.Instructions)
	}
}

func TestR453HTMLPreviewIgnoresCodeExamples(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	sourceBytes, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(sourceBytes)
	start := strings.Index(source, "function workModeReplyHTMLDetectionText(value)")
	if start < 0 {
		t.Fatal("workModeReplyHTMLDetectionText is missing")
	}
	endRel := strings.Index(source[start:], "function workModeHTMLPreviewMode(value)")
	if endRel < 0 {
		t.Fatal("could not isolate HTML preview detector functions")
	}
	snippet := source[start : start+endRel]
	checks := `
const fence = String.fromCharCode(96, 96, 96);
const cases = [
  ["<p>Before</p><pre><code><input type=\"text\"><button>Go</button></code></pre><p>After</p>", false, "pre/code HTML example"],
  ["<p>Before</p><code><button>Go</button></code><p>After</p>", false, "inline code HTML example"],
  [fence + "html\n<form><input></form>\n" + fence, false, "markdown fenced HTML example"],
  ["<p>Before</p><input type=\"text\"><p>After</p>", true, "real active input"],
  ["<p>Before</p><pre><code><button>example</button></code></pre><form><input></form>", true, "active HTML outside code"],
  ["<!doctype html><html><body><p>Page</p></body></html>", true, "full HTML document"]
];
for (const [value, expected, label] of cases) {
  const actual = workModeReplyContainsPreviewableHTML(value);
  if (actual !== expected) {
    console.error(label + ': got ' + actual + ', expected ' + expected);
    process.exit(1);
  }
}
`
	path := filepath.Join(t.TempDir(), "r453-html-preview.js")
	if err := os.WriteFile(path, []byte(snippet+checks), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("node", path).CombinedOutput(); err != nil {
		t.Fatalf("HTML preview detector regression: %v\n%s", err, output)
	}
}

func TestR453DefaultClaudeAndGeminiUseProviderSchema(t *testing.T) {
	data, err := os.ReadFile("models.json")
	if err != nil {
		t.Fatal(err)
	}
	var registry ModelRegistry
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, model := range registry.Models {
		switch model.Label {
		case "Claud1", "ClaudeSonnet", "ClaudeOpus", "GEMINI_1":
			seen[model.Label] = true
			if normalizeStructuredOutputCompatibility(model.StructuredOutputCompatibility) != structuredOutputCompatibilityProviderSchema {
				t.Fatalf("%s default structured-output mode = %q, want provider_schema", model.Label, model.StructuredOutputCompatibility)
			}
			if model.StrictStructuredOutput == nil || !*model.StrictStructuredOutput {
				t.Fatalf("%s default strict_structured_output is not true", model.Label)
			}
		case "ChatGPT 4.1 Mini", "Grok4.5":
			if normalizeStructuredOutputCompatibility(model.StructuredOutputCompatibility) != structuredOutputCompatibilityAutomatic {
				t.Fatalf("%s should remain Automatic, got %q", model.Label, model.StructuredOutputCompatibility)
			}
		}
	}
	for _, label := range []string{"Claud1", "ClaudeSonnet", "ClaudeOpus", "GEMINI_1"} {
		if !seen[label] {
			t.Fatalf("default model %s not found", label)
		}
	}
}

func TestR453Revision(t *testing.T) {
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
	if version.Revision != "r453" {
		t.Fatalf("revision = %q, want r453", version.Revision)
	}
}
