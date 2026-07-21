package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
)

const anthropicAutomaticMaxOutputTokens = 128000

type knownModelMaxOutputCatalog struct {
	byExactKey   map[string]int
	byAdapterKey map[string]int
}

type maxOutputResolution struct {
	Supported       bool
	Mode            string
	SavedTokens     int
	KnownTokens     int
	EffectiveTokens int
	Source          string
}

type knownModelMaxOutputFile struct {
	Models []struct {
		Provider        string `json:"provider"`
		Adapter         string `json:"adapter"`
		ModelName       string `json:"model_name"`
		MaxOutputTokens int    `json:"max_output_tokens"`
	} `json:"models"`
}

func loadKnownModelMaxOutputCatalog(filename string) (knownModelMaxOutputCatalog, error) {
	catalog := knownModelMaxOutputCatalog{byExactKey: map[string]int{}, byAdapterKey: map[string]int{}}
	data, err := os.ReadFile(filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return catalog, nil
		}
		return catalog, err
	}
	var payload knownModelMaxOutputFile
	if err := json.Unmarshal(data, &payload); err != nil {
		return catalog, err
	}
	for _, item := range payload.Models {
		if item.MaxOutputTokens <= 0 {
			continue
		}
		provider := normalizeMaxOutputProvider(item.Provider)
		adapter := normalizeMaxOutputAdapter(item.Adapter, provider)
		modelName := normalizeMaxOutputModelName(item.ModelName)
		if adapter == "" || modelName == "" {
			continue
		}
		catalog.byAdapterKey[maxOutputAdapterKey(adapter, modelName)] = item.MaxOutputTokens
		if provider != "" {
			catalog.byExactKey[maxOutputExactKey(provider, adapter, modelName)] = item.MaxOutputTokens
		}
	}
	return catalog, nil
}

func normalizeMaxOutputProvider(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "chatgpt":
		return "openai"
	case "anthropic":
		return "claude"
	case "gemini":
		return "google"
	case "grok":
		return "xai"
	default:
		return value
	}
}

func normalizeMaxOutputAdapter(value, provider string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "anthropic", "claude":
		return "anthropic_messages"
	case "gemini", "google":
		return "gemini_generate_content"
	case "ollama":
		return "ollama_generate"
	case "openai", "responses":
		return "openai_responses"
	case "xai", "grok":
		return "xai_responses"
	}
	if value != "" {
		return value
	}
	switch normalizeMaxOutputProvider(provider) {
	case "openai":
		return "openai_responses"
	case "claude":
		return "anthropic_messages"
	case "google":
		return "gemini_generate_content"
	case "ollama":
		return "ollama_generate"
	case "xai":
		return "xai_responses"
	default:
		return ""
	}
}

func normalizeMaxOutputModelName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func maxOutputExactKey(provider, adapter, modelName string) string {
	return normalizeMaxOutputProvider(provider) + "\x00" + normalizeMaxOutputAdapter(adapter, provider) + "\x00" + normalizeMaxOutputModelName(modelName)
}

func maxOutputAdapterKey(adapter, modelName string) string {
	return normalizeMaxOutputAdapter(adapter, "") + "\x00" + normalizeMaxOutputModelName(modelName)
}

func (catalog knownModelMaxOutputCatalog) lookup(model ModelConfig) int {
	adapter := normalizeMaxOutputAdapter(model.Adapter, model.Provider)
	modelName := normalizeMaxOutputModelName(model.ModelName)
	if adapter == "" || modelName == "" {
		return 0
	}
	if value := catalog.byExactKey[maxOutputExactKey(model.Provider, adapter, modelName)]; value > 0 {
		return value
	}
	return catalog.byAdapterKey[maxOutputAdapterKey(adapter, modelName)]
}

func supportsSharedMaxOutputTokens(model ModelConfig) bool {
	if model.VideoGeneration || model.MeshGeneration {
		return false
	}
	adapter := normalizeMaxOutputAdapter(model.Adapter, model.Provider)
	identity := normalizeMaxOutputModelName(model.ModelName)
	if adapter == "openai_responses" && (strings.Contains(identity, "gpt-image") || strings.Contains(identity, "chatgpt-image")) {
		return false
	}
	if adapter == "gemini_generate_content" && (strings.Contains(identity, "flash-image") || strings.Contains(identity, "image-preview") || strings.Contains(identity, "nano-banana")) {
		return false
	}
	switch adapter {
	case "openai_responses", "openai_compatible", "xai_responses", "anthropic_messages", "gemini_generate_content", "ollama_generate":
		return true
	default:
		return false
	}
}

func resolveModelMaxOutputTokens(model ModelConfig, catalog knownModelMaxOutputCatalog) maxOutputResolution {
	resolution := maxOutputResolution{Supported: supportsSharedMaxOutputTokens(model), Mode: "automatic", SavedTokens: model.MaxOutputTokens, Source: "unsupported"}
	if !resolution.Supported {
		resolution.SavedTokens = 0
		return resolution
	}
	if model.MaxOutputTokens > 0 {
		resolution.Mode = "custom"
		resolution.EffectiveTokens = model.MaxOutputTokens
		resolution.Source = "custom_guardrail"
		return resolution
	}
	resolution.KnownTokens = catalog.lookup(model)
	if resolution.KnownTokens > 0 {
		resolution.EffectiveTokens = resolution.KnownTokens
		resolution.Source = "known_model_maximum"
		return resolution
	}
	if normalizeMaxOutputAdapter(model.Adapter, model.Provider) == "anthropic_messages" {
		// Anthropic's Messages API requires max_tokens. Unknown custom Claude
		// model names use a high documented fallback; known models use cataloged
		// maxima and users can always choose a lower custom guardrail.
		resolution.EffectiveTokens = anthropicAutomaticMaxOutputTokens
		resolution.Source = "provider_required_fallback"
		return resolution
	}
	resolution.Source = "provider_managed"
	return resolution
}
