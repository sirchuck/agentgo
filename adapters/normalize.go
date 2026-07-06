package adapters

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// modelEndpoint builds the final request URL from a model's base URL and API path.
func modelEndpoint(model ModelConfig) string {
	base := strings.TrimRight(strings.TrimSpace(model.BaseURL), "/")
	path := strings.TrimSpace(model.APIPath)
	if path == "" {
		return base
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	endpoint := base + path
	endpoint = strings.ReplaceAll(endpoint, "{model}", model.ModelName)
	return endpoint
}

// modelTimeout returns the per-model timeout or a safe fallback when none is set.
func modelTimeout(model ModelConfig, fallback time.Duration) time.Duration {
	if model.TimeoutSeconds > 0 {
		return time.Duration(model.TimeoutSeconds) * time.Second
	}
	return fallback
}

// resolveConfiguredAPIKey prefers an explicit environment variable reference and falls back to the saved key.
func resolveConfiguredAPIKey(model ModelConfig) string {
	if envName := strings.TrimSpace(model.APIKeyEnv); envName != "" {
		if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
			return value
		}
	}
	return strings.TrimSpace(model.APIKey)
}

// applyModelHeaders adds auth and custom headers from the saved model config.
func applyModelHeaders(req *http.Request, model ModelConfig) {
	switch strings.TrimSpace(model.AuthType) {
	case "bearer":
		if strings.TrimSpace(model.APIKey) != "" {
			req.Header.Set(defaultString(model.AuthHeader, "Authorization"), "Bearer "+strings.TrimSpace(model.APIKey))
		}
	case "header_key":
		if strings.TrimSpace(model.APIKey) != "" {
			req.Header.Set(defaultString(model.AuthHeader, "x-api-key"), strings.TrimSpace(model.APIKey))
		}
	case "basic":
		if strings.TrimSpace(model.APIUser) != "" || strings.TrimSpace(model.APIPass) != "" {
			req.SetBasicAuth(strings.TrimSpace(model.APIUser), strings.TrimSpace(model.APIPass))
		}
	}
	for k, v := range model.Headers {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			continue
		}
		req.Header.Set(k, v)
	}
}

// prepareOpenAIModel fills in OpenAI auth defaults before a request is sent.
func prepareOpenAIModel(model ModelConfig) (ModelConfig, error) {
	prepared := model
	authType := normalizedAuthType(prepared.AuthType, "bearer")
	prepared.AuthType = authType
	if authType == "bearer" || authType == "header_key" {
		prepared.APIKey = resolveConfiguredAPIKey(prepared)
		if strings.TrimSpace(prepared.APIKey) == "" {
			prepared.APIKey = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
		}
		if strings.TrimSpace(prepared.APIKey) == "" {
			return prepared, errors.New("missing API key for this model")
		}
	}
	return prepared, nil
}

// normalizedAuthType applies a fallback auth type when the config leaves it blank.
func normalizedAuthType(authType, fallback string) string {
	authType = strings.ToLower(strings.TrimSpace(authType))
	if authType == "" {
		return fallback
	}
	return authType
}

// defaultString returns the fallback when a config string is blank.
func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

// normalizeMessageParts preserves multipart content while keeping legacy text-only requests working.
func normalizeMessageParts(message Message) []Part {
	parts := make([]Part, 0, len(message.Parts)+1)
	if strings.TrimSpace(message.Text) != "" {
		parts = append(parts, Part{Kind: "text", Text: strings.TrimSpace(message.Text)})
	}
	for _, part := range message.Parts {
		kind := strings.ToLower(strings.TrimSpace(part.Kind))
		if kind == "" {
			if strings.TrimSpace(part.Text) != "" {
				kind = "text"
			} else if len(part.Data) > 0 {
				kind = "file"
			}
		}
		normalized := part
		normalized.Kind = kind
		switch kind {
		case "text":
			normalized.Text = strings.TrimSpace(part.Text)
			if normalized.Text == "" {
				continue
			}
		case "image", "audio", "video", "file":
			if len(part.Data) == 0 {
				continue
			}
		default:
			if strings.TrimSpace(part.Text) != "" {
				normalized.Kind = "text"
				normalized.Text = strings.TrimSpace(part.Text)
			} else if len(part.Data) > 0 {
				normalized.Kind = "file"
			} else {
				continue
			}
		}
		parts = append(parts, normalized)
	}
	return parts
}

func partLabel(part Part) string {
	if strings.TrimSpace(part.RelPath) != "" {
		return strings.TrimSpace(part.RelPath)
	}
	if strings.TrimSpace(part.Name) != "" {
		return strings.TrimSpace(part.Name)
	}
	return strings.TrimSpace(part.MIMEType)
}

func partTextFallback(part Part) string {
	label := partLabel(part)
	if label == "" {
		label = "binary attachment"
	}
	switch strings.ToLower(strings.TrimSpace(part.Kind)) {
	case "text":
		return strings.TrimSpace(part.Text)
	case "image":
		if strings.TrimSpace(part.MIMEType) != "" {
			return fmt.Sprintf("[image attachment omitted from native transport: %s (%s)]", label, strings.TrimSpace(part.MIMEType))
		}
		return fmt.Sprintf("[image attachment omitted from native transport: %s]", label)
	case "audio":
		if strings.TrimSpace(part.MIMEType) != "" {
			return fmt.Sprintf("[audio attachment omitted from native transport: %s (%s)]", label, strings.TrimSpace(part.MIMEType))
		}
		return fmt.Sprintf("[audio attachment omitted from native transport: %s]", label)
	case "video":
		if strings.TrimSpace(part.MIMEType) != "" {
			return fmt.Sprintf("[video attachment omitted from native transport: %s (%s)]", label, strings.TrimSpace(part.MIMEType))
		}
		return fmt.Sprintf("[video attachment omitted from native transport: %s]", label)
	case "file":
		if strings.TrimSpace(part.MIMEType) != "" {
			return fmt.Sprintf("[file attachment omitted from native transport: %s (%s)]", label, strings.TrimSpace(part.MIMEType))
		}
		return fmt.Sprintf("[file attachment omitted from native transport: %s]", label)
	default:
		return ""
	}
}

// flattenMessages joins AgentGO's assembled messages into one provider-neutral text block.
func flattenMessages(messages []Message) string {
	parts := make([]string, 0, len(messages))
	for _, message := range messages {
		messageParts := normalizeMessageParts(message)
		if len(messageParts) == 0 {
			continue
		}
		segments := make([]string, 0, len(messageParts))
		for _, part := range messageParts {
			text := strings.TrimSpace(partTextFallback(part))
			if text == "" {
				continue
			}
			segments = append(segments, text)
		}
		if len(segments) == 0 {
			continue
		}
		parts = append(parts, strings.Join(segments, "\n\n"))
	}
	return strings.Join(parts, "\n\n")
}
