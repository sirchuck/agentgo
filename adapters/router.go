package adapters

import (
	"fmt"
	"strings"
)

// selectAdapter chooses the backend adapter implementation for one saved model.
func selectAdapter(model ModelConfig) (Adapter, error) {
	switch normalizedAdapterName(model) {
	case "openai_responses":
		return openAIResponsesAdapter{}, nil
	case "ollama_generate":
		return ollamaGenerateAdapter{}, nil
	case "anthropic_messages":
		return anthropicMessagesAdapter{}, nil
	case "gemini_generate_content":
		return geminiGenerateContentAdapter{}, nil
	case "openai_compatible":
		return openAICompatibleAdapter{}, nil
	case "custom_json":
		return customJSONAdapter{}, nil
	case "custom_multipart":
		return customMultipartAdapter{}, nil
	case "veo_video", "kling_video", "sora_video", "comfyui_ltx_video":
		return nil, fmt.Errorf("adapter %q is a video-generation adapter and must use the dedicated video runtime", strings.TrimSpace(model.Adapter))
	case "meshy_mesh", "tripo_mesh", "hyper3d_mesh":
		return nil, fmt.Errorf("adapter %q is a mesh-generation adapter and must use the dedicated mesh runtime", strings.TrimSpace(model.Adapter))
	default:
		return nil, fmt.Errorf("adapter %q is not implemented yet", strings.TrimSpace(model.Adapter))
	}
}

func normalizeCapabilityMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "manual":
		return "manual"
	default:
		return "auto"
	}
}

func baseCapabilities() ModelCapabilities {
	return ModelCapabilities{SupportsTextIn: true, SupportsBinaryOut: true}
}

func mergeCapabilities(base, extra ModelCapabilities) ModelCapabilities {
	base.SupportsTextIn = true
	base.SupportsImageIn = base.SupportsImageIn || extra.SupportsImageIn
	base.SupportsAudioIn = base.SupportsAudioIn || extra.SupportsAudioIn
	base.SupportsVideoIn = base.SupportsVideoIn || extra.SupportsVideoIn
	base.SupportsFileIn = base.SupportsFileIn || extra.SupportsFileIn
	base.SupportsBinaryOut = base.SupportsBinaryOut || extra.SupportsBinaryOut
	return base
}

func capabilityIdentity(model ModelConfig) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(model.Provider)),
		strings.ToLower(strings.TrimSpace(model.Adapter)),
		strings.ToLower(strings.TrimSpace(model.ModelName)),
		strings.ToLower(strings.TrimSpace(model.Label)),
	}
	return strings.Join(parts, " ")
}

func containsAny(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if needle != "" && strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

func knownModelCapabilityHints(model ModelConfig) ModelCapabilities {
	identity := capabilityIdentity(model)
	adapter := normalizedAdapterName(model)
	hints := ModelCapabilities{SupportsTextIn: true}
	if identity == "" {
		return hints
	}
	if adapter == "openai_compatible" || adapter == "ollama_generate" {
		if containsAny(identity,
			"gpt-4.1", "gpt-4o", "gpt-4-turbo", "gpt-4-vision", "gpt-4.5", "gpt-5",
			"gemini", "claude-3", "claude 3", "claude-4", "claude 4",
			"llava", "bakllava", "moondream", "minicpm-v", "minicpmv",
			"qwen2.5-vl", "qwen-vl", "internvl", "pixtral",
			"gemma-3", "gemma3", "llama-3.2-vision", "llama3.2-vision", "llama vision") {
			hints.SupportsImageIn = true
		}
	}
	return hints
}

// AdapterDefaultCapabilities reports the built-in transport defaults for one adapter family.
func AdapterDefaultCapabilities(model ModelConfig) ModelCapabilities {
	caps := baseCapabilities()
	switch normalizedAdapterName(model) {
	case "openai_responses":
		caps.SupportsImageIn = true
		caps.SupportsFileIn = true
	case "gemini_generate_content":
		caps.SupportsImageIn = true
		caps.SupportsAudioIn = true
		caps.SupportsVideoIn = true
		caps.SupportsFileIn = true
	case "anthropic_messages":
		caps.SupportsImageIn = true
		caps.SupportsFileIn = true
	case "custom_json", "custom_multipart":
		caps.SupportsImageIn = true
		caps.SupportsAudioIn = true
		caps.SupportsVideoIn = true
		caps.SupportsFileIn = true
	case "veo_video", "kling_video", "sora_video", "comfyui_ltx_video":
		caps.SupportsImageIn = true
	case "meshy_mesh", "tripo_mesh", "hyper3d_mesh":
		caps.SupportsImageIn = true
	case "ollama_generate", "openai_compatible":
		// Auto mode stays conservative unless the known model name indicates image support.
	}
	return mergeCapabilities(caps, knownModelCapabilityHints(model))
}

// ResolveTransportProfile combines adapter defaults with any per-model overrides.
func ResolveTransportProfile(model ModelConfig) TransportProfile {
	adapterCaps := AdapterDefaultCapabilities(model)
	mode := normalizeCapabilityMode(model.CapabilityMode)
	modelCaps := model.Capabilities
	if !modelCaps.SupportsTextIn {
		modelCaps.SupportsTextIn = true
	}
	if mode != "manual" {
		modelCaps = adapterCaps
	}
	effective := adapterCaps
	if mode == "manual" {
		effective = ModelCapabilities{
			SupportsTextIn:    true,
			SupportsImageIn:   adapterCaps.SupportsImageIn && modelCaps.SupportsImageIn,
			SupportsAudioIn:   adapterCaps.SupportsAudioIn && modelCaps.SupportsAudioIn,
			SupportsVideoIn:   adapterCaps.SupportsVideoIn && modelCaps.SupportsVideoIn,
			SupportsFileIn:    adapterCaps.SupportsFileIn && modelCaps.SupportsFileIn,
			SupportsBinaryOut: adapterCaps.SupportsBinaryOut && modelCaps.SupportsBinaryOut,
		}
	}
	return TransportProfile{
		Adapter:               normalizedAdapterName(model),
		CapabilityMode:        mode,
		AdapterCapabilities:   adapterCaps,
		ModelCapabilities:     modelCaps,
		EffectiveCapabilities: effective,
	}
}

// SupportsNativeImages reports whether the selected model should receive native image parts.
func SupportsNativeImages(model ModelConfig) bool {
	return ResolveTransportProfile(model).EffectiveCapabilities.SupportsImageIn
}

// normalizedAdapterName falls back to provider defaults while the adapter rollout is still in progress.
func normalizedAdapterName(model ModelConfig) string {
	name := strings.ToLower(strings.TrimSpace(model.Adapter))
	if name != "" {
		return name
	}
	switch strings.ToLower(strings.TrimSpace(model.Provider)) {
	case "openai", "chatgpt":
		return "openai_responses"
	case "ollama":
		return "ollama_generate"
	case "openrouter", "custom", "openai_compatible", "deepseek", "xai", "grok", "mistral":
		return "openai_compatible"
	case "anthropic", "claude":
		return "anthropic_messages"
	case "google", "gemini":
		return "gemini_generate_content"
	case "ideogram":
		return "custom_multipart"
	case "seedream":
		return "custom_json"
	default:
		return name
	}
}
