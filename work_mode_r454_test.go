package main

import (
	"os"
	"strings"
	"testing"

	"agentgo/adapters"
)

func TestR454ClaudeModelEditorDefaultsToProviderSchema(t *testing.T) {
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	start := strings.Index(html, "function buildDefaultResponseCapabilities(provider, adapter) {")
	if start < 0 {
		t.Fatal("buildDefaultResponseCapabilities not found")
	}
	endRel := strings.Index(html[start:], "function normalizePromptMode(")
	if endRel < 0 {
		t.Fatal("could not isolate buildDefaultResponseCapabilities")
	}
	functionText := html[start : start+endRel]
	for _, required := range []string{
		"normalizedProvider === 'claude'",
		"normalizedAdapter === 'anthropic_messages'",
		"? 'provider_schema'",
		": 'automatic'",
	} {
		if !strings.Contains(functionText, required) {
			t.Fatalf("Claude structured-output default is missing %q:\n%s", required, functionText)
		}
	}
	if !strings.Contains(html, "select.value = buildDefaultResponseCapabilities(provider, adapter).structured_output_compatibility;") {
		t.Fatal("provider/adapter changes do not apply provider-specific structured-output defaults")
	}
	if strings.Contains(html, "if (force) select.value = 'automatic';") {
		t.Fatal("model editor still hard-resets Structured Output Compatibility to Automatic")
	}
}

func TestR454NormalizationDiagnosticsPreserveProviderRawEvidence(t *testing.T) {
	model := ModelConfig{
		Provider:  "claude",
		Adapter:   "anthropic_messages",
		ModelName: "claude-opus-test",
		APIKey:    "super-secret-key",
	}
	finalResponse := adapters.Response{
		Text:    `{"reply":"Recovered answer","files":[],"artifacts":[],"memory":"","warnings":[]}`,
		RawBody: `{"content":[{"type":"tool_use","input":{"reply":"Recovered answer"}}],"api_key":"super-secret-key"}`,
	}
	originalText := `</antml:parameter><parameter name="warnings">[]`
	originalRaw := `{"content":[{"type":"text","text":"</antml:parameter>"}],"api_key":"super-secret-key"}`

	responseDiagnostic := formatWorkModeNormalizationResponseDiagnostic(model, finalResponse, originalText, originalRaw)
	for _, required := range []string{
		`"original_extracted_response"`,
		`"original_provider_raw_body"`,
		`"final_extracted_response"`,
		`"final_provider_raw_body"`,
		`antml:parameter`,
		`Recovered answer`,
		`[REDACTED]`,
	} {
		if !strings.Contains(responseDiagnostic, required) {
			t.Fatalf("normalization response diagnostic missing %q:\n%s", required, responseDiagnostic)
		}
	}
	if strings.Contains(responseDiagnostic, "super-secret-key") {
		t.Fatal("normalization response diagnostic leaked API key")
	}

	parsed := workModeAIResponse{Reply: "Recovered answer", Files: []builderFileOp{}, Artifacts: []builderArtifact{}, Memory: "", Warnings: []string{}}
	repair := &workModeJSONRepairDetail{
		OriginalParseError: "invalid character '<'",
		OriginalResponse:   originalText,
		RepairResponse:     finalResponse.Text,
	}
	details := formatWorkModeNormalizationDiagnostic(model, []string{"AgentGO repaired the response."}, repair, parsed)
	for _, required := range []string{
		`"normalizations"`,
		`"automatic_json_repair"`,
		`"final_parsed_response"`,
		`Recovered answer`,
		`invalid character`,
	} {
		if !strings.Contains(details, required) {
			t.Fatalf("normalization details missing %q:\n%s", required, details)
		}
	}
}

func TestR454RepairFailureDiagnosticsIncludeRawProviderBodies(t *testing.T) {
	model := ModelConfig{Provider: "claude", Adapter: "anthropic_messages", ModelName: "claude-opus-test"}
	repairErr := &workModeJSONRepairError{
		OriginalParseError: "invalid original JSON",
		OriginalResponse:   "broken extracted response",
		OriginalRawBody:    `{"content":[{"type":"text","text":"broken"}]}`,
		RepairParseError:   "invalid repaired JSON",
		RepairResponse:     "broken repair response",
		RepairRawBody:      `{"content":[{"type":"text","text":"repair broken"}]}`,
	}
	details := formatWorkModeRepairFailureDiagnostic(model, repairErr)
	for _, required := range []string{
		`"automatic_json_repair_failure"`,
		`"original_provider_raw_body"`,
		`"repair_provider_raw_body"`,
		`broken extracted response`,
		`repair broken`,
	} {
		if !strings.Contains(details, required) {
			t.Fatalf("repair failure diagnostic missing %q:\n%s", required, details)
		}
	}
}
