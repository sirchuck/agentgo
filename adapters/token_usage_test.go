package adapters

import "testing"

func TestExtractReportedTokenUsage(t *testing.T) {
	tests := []struct {
		name              string
		body              string
		input, output     int
		inputOK, outputOK bool
	}{
		{name: "openai responses", body: `{"usage":{"input_tokens":120,"output_tokens":45,"total_tokens":165}}`, input: 120, output: 45, inputOK: true, outputOK: true},
		{name: "openai chat", body: `{"usage":{"prompt_tokens":80,"completion_tokens":20,"total_tokens":100}}`, input: 80, output: 20, inputOK: true, outputOK: true},
		{name: "anthropic cache", body: `{"usage":{"input_tokens":100,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":25}}`, input: 150, output: 25, inputOK: true, outputOK: true},
		{name: "gemini total includes thoughts", body: `{"usageMetadata":{"promptTokenCount":200,"candidatesTokenCount":50,"thoughtsTokenCount":25,"totalTokenCount":275}}`, input: 200, output: 75, inputOK: true, outputOK: true},
		{name: "gemini fallback parts", body: `{"usageMetadata":{"promptTokenCount":200,"candidatesTokenCount":50,"thoughtsTokenCount":25}}`, input: 200, output: 75, inputOK: true, outputOK: true},
		{name: "ollama", body: `{"prompt_eval_count":64,"eval_count":16}`, input: 64, output: 16, inputOK: true, outputOK: true},
		{name: "unknown", body: `{"answer":"ok"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := extractReportedTokenUsage([]byte(test.body))
			if got.InputTokens != test.input || got.OutputTokens != test.output || got.InputReported != test.inputOK || got.OutputReported != test.outputOK {
				t.Fatalf("usage = %+v, want input=%d output=%d inputOK=%v outputOK=%v", got, test.input, test.output, test.inputOK, test.outputOK)
			}
		})
	}
}
