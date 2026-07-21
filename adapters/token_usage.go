package adapters

import (
	"encoding/json"
	"math"
)

// extractReportedTokenUsage recognizes the token-usage fields returned by the
// text providers AgentGO supports. Unknown/custom providers simply return no
// reported counts and AgentGO can use its bounded local estimate instead.
func extractReportedTokenUsage(body []byte) TokenUsage {
	var root map[string]any
	if len(body) == 0 || json.Unmarshal(body, &root) != nil {
		return TokenUsage{}
	}

	// Gemini generateContent usage metadata. totalTokenCount includes thinking
	// tokens, so use total minus prompt when both are available.
	if usage, ok := root["usageMetadata"].(map[string]any); ok {
		input, inputOK := nonNegativeJSONInt(usage["promptTokenCount"])
		total, totalOK := nonNegativeJSONInt(usage["totalTokenCount"])
		output, outputOK := 0, false
		if totalOK && inputOK && total >= input {
			output, outputOK = total-input, true
		} else {
			candidates, candidatesOK := nonNegativeJSONInt(usage["candidatesTokenCount"])
			thoughts, thoughtsOK := nonNegativeJSONInt(usage["thoughtsTokenCount"])
			if candidatesOK || thoughtsOK {
				output, outputOK = candidates+thoughts, true
			}
		}
		return TokenUsage{InputTokens: input, OutputTokens: output, InputReported: inputOK, OutputReported: outputOK}
	}

	// Ollama generate/chat response counts.
	if input, inputOK := nonNegativeJSONInt(root["prompt_eval_count"]); inputOK {
		output, outputOK := nonNegativeJSONInt(root["eval_count"])
		return TokenUsage{InputTokens: input, OutputTokens: output, InputReported: true, OutputReported: outputOK}
	}

	usage, ok := root["usage"].(map[string]any)
	if !ok {
		return TokenUsage{}
	}

	// OpenAI Responses and Anthropic Messages both use input_tokens and
	// output_tokens. Anthropic cache counts are separate and are included in the
	// task total because they still represent provider-processed input tokens.
	if input, inputOK := nonNegativeJSONInt(usage["input_tokens"]); inputOK {
		if cached, ok := nonNegativeJSONInt(usage["cache_creation_input_tokens"]); ok {
			input += cached
		}
		if cached, ok := nonNegativeJSONInt(usage["cache_read_input_tokens"]); ok {
			input += cached
		}
		output, outputOK := nonNegativeJSONInt(usage["output_tokens"])
		return TokenUsage{InputTokens: input, OutputTokens: output, InputReported: true, OutputReported: outputOK}
	}

	// OpenAI-compatible Chat Completions shape.
	input, inputOK := nonNegativeJSONInt(usage["prompt_tokens"])
	output, outputOK := nonNegativeJSONInt(usage["completion_tokens"])
	return TokenUsage{InputTokens: input, OutputTokens: output, InputReported: inputOK, OutputReported: outputOK}
}

func nonNegativeJSONInt(value any) (int, bool) {
	var number int64
	switch typed := value.(type) {
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < 0 || math.Trunc(typed) != typed || typed > float64(math.MaxInt) {
			return 0, false
		}
		number = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed < 0 {
			return 0, false
		}
		number = parsed
	case int:
		if typed < 0 {
			return 0, false
		}
		return typed, true
	case int64:
		if typed < 0 || typed > int64(math.MaxInt) {
			return 0, false
		}
		number = typed
	default:
		return 0, false
	}
	if number > int64(math.MaxInt) {
		return 0, false
	}
	return int(number), true
}
