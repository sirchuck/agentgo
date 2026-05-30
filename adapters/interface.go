package adapters

import "context"

// RequestDefaults keeps provider-neutral request defaults used by adapter configs.
type RequestDefaults struct {
	Temperature float64 `json:"temperature,omitempty"`
}

// ModelConfig holds the connection settings an adapter needs to call a model.
type ModelCapabilities struct {
	SupportsTextIn    bool `json:"supports_text_in"`
	SupportsImageIn   bool `json:"supports_image_in"`
	SupportsAudioIn   bool `json:"supports_audio_in"`
	SupportsVideoIn   bool `json:"supports_video_in"`
	SupportsFileIn    bool `json:"supports_file_in"`
	SupportsBinaryOut bool `json:"supports_binary_out"`
}

type TransportProfile struct {
	Adapter               string
	CapabilityMode        string
	AdapterCapabilities   ModelCapabilities
	ModelCapabilities     ModelCapabilities
	EffectiveCapabilities ModelCapabilities
}

// ModelConfig holds the connection settings an adapter needs to call a model.
type ModelConfig struct {
	Label                  string            `json:"label"`
	StrictStructuredOutput *bool             `json:"strict_structured_output,omitempty"`
	VideoGeneration        bool              `json:"video_generation,omitempty"`
	VideoPromptOnly        bool              `json:"video_prompt_only,omitempty"`
	VideoStartFrame        bool              `json:"video_start_frame,omitempty"`
	VideoEndFrame          bool              `json:"video_end_frame,omitempty"`
	VideoIngredients       bool              `json:"video_ingredients,omitempty"`
	VideoDuration          string            `json:"video_duration,omitempty"`
	VideoAspectRatio       string            `json:"video_aspect_ratio,omitempty"`
	VideoResolution        string            `json:"video_resolution,omitempty"`
	VideoOutputFormat      string            `json:"video_output_format,omitempty"`
	VideoFPS               string            `json:"video_fps,omitempty"`
	VideoQuality           string            `json:"video_quality,omitempty"`
	MeshGeneration         bool              `json:"mesh_generation,omitempty"`
	MeshPromptOnly         bool              `json:"mesh_prompt_only,omitempty"`
	MeshImageInput         bool              `json:"mesh_image_input,omitempty"`
	MeshMultiImage         bool              `json:"mesh_multi_image,omitempty"`
	MeshQuality            string            `json:"mesh_quality,omitempty"`
	MeshOutputFormat       string            `json:"mesh_output_format,omitempty"`
	Provider               string            `json:"provider"`
	Adapter                string            `json:"adapter"`
	APIUser                string            `json:"api_user"`
	APIPass                string            `json:"api_pass"`
	APIKey                 string            `json:"api_key"`
	APIKeyEnv              string            `json:"api_key_env,omitempty"`
	AuthType               string            `json:"auth_type"`
	AuthHeader             string            `json:"auth_header"`
	BaseURL                string            `json:"base_url"`
	APIPath                string            `json:"api_path"`
	ModelName              string            `json:"model_name"`
	Headers                map[string]string `json:"headers"`
	MaxOutputTokens        int               `json:"max_output_tokens,omitempty"`
	TimeoutSeconds         int               `json:"timeout_seconds,omitempty"`
	RequestDefaults        RequestDefaults   `json:"request_defaults"`
	ProviderOptions        map[string]any    `json:"provider_options"`
	CapabilityMode         string            `json:"capability_mode,omitempty"`
	Capabilities           ModelCapabilities `json:"capabilities,omitempty"`
}

// Part carries one provider-neutral text or binary request part.
type Part struct {
	Kind     string
	Name     string
	RelPath  string
	MIMEType string
	Text     string
	Data     []byte
}

// Message carries one assembled request message from AgentGO into an adapter.
type Message struct {
	Role  string
	Text  string
	Parts []Part
}

// Request is the provider-neutral execution payload built by AgentGO.
type Request struct {
	Instructions string
	Messages     []Message
	ExpectJSON   bool
	JSONSchema   map[string]any
}

// Response is the provider-neutral result returned by an adapter.
type Response struct {
	Text         string
	RawBody      string
	Status       string
	StatusCode   int
	FileData     []byte
	FileName     string
	FileMIMEType string
}

// Adapter defines the one execution entry point every provider implementation must support.
type Adapter interface {
	Execute(ctx context.Context, model ModelConfig, req Request) (Response, error)
}

// Execute routes a model request to the adapter named in the model config.
func Execute(ctx context.Context, model ModelConfig, req Request) (Response, error) {
	adapter, err := selectAdapter(model)
	if err != nil {
		return Response{}, err
	}
	return adapter.Execute(ctx, model, req)
}
