package adapters

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func providerOptionString(model ModelConfig, key, fallback string) string {
	if model.ProviderOptions == nil {
		return fallback
	}
	value, ok := model.ProviderOptions[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return fallback
		}
		return strings.TrimSpace(typed)
	default:
		text := strings.TrimSpace(fmt.Sprint(typed))
		if text == "" {
			return fallback
		}
		return text
	}
}

func providerOptionBool(model ModelConfig, key string, fallback bool) bool {
	if model.ProviderOptions == nil {
		return fallback
	}
	value, ok := model.ProviderOptions[key]
	if !ok || value == nil {
		return fallback
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	case float64:
		return typed != 0
	case int:
		return typed != 0
	}
	return fallback
}

func providerOptionOptionalBool(model ModelConfig, key string) (bool, bool) {
	if model.ProviderOptions == nil {
		return false, false
	}
	value, ok := model.ProviderOptions[key]
	if !ok || value == nil {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	case float64:
		return typed != 0, true
	case int:
		return typed != 0, true
	}
	return false, false
}

func providerOptionValue(model ModelConfig, key string) (any, bool) {
	if model.ProviderOptions == nil {
		return nil, false
	}
	value, ok := model.ProviderOptions[key]
	if !ok || value == nil {
		return nil, false
	}
	return value, true
}

func providerOptionMap(model ModelConfig, key string) map[string]any {
	if model.ProviderOptions == nil {
		return nil
	}
	value, ok := model.ProviderOptions[key]
	if !ok || value == nil {
		return nil
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(object))
	for k, v := range object {
		cloned[k] = v
	}
	return cloned
}

func providerOptionStringList(model ModelConfig, key string) []string {
	if model.ProviderOptions == nil {
		return nil
	}
	value, ok := model.ProviderOptions[key]
	if !ok || value == nil {
		return nil
	}
	appendClean := func(out []string, raw string) []string {
		clean := strings.TrimSpace(raw)
		if clean == "" {
			return out
		}
		return append(out, clean)
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = appendClean(out, item)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = appendClean(out, fmt.Sprint(item))
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		clean := strings.TrimSpace(typed)
		if clean == "" {
			return nil
		}
		parts := strings.Split(clean, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			out = appendClean(out, part)
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		clean := strings.TrimSpace(fmt.Sprint(typed))
		if clean == "" {
			return nil
		}
		return []string{clean}
	}
}

func setNestedField(root map[string]any, path string, value any) {
	clean := strings.TrimSpace(path)
	if clean == "" || root == nil || value == nil {
		return
	}
	parts := strings.Split(clean, ".")
	current := root
	for idx, part := range parts {
		key := strings.TrimSpace(part)
		if key == "" {
			return
		}
		if idx == len(parts)-1 {
			current[key] = value
			return
		}
		next, ok := current[key].(map[string]any)
		if !ok || next == nil {
			next = map[string]any{}
			current[key] = next
		}
		current = next
	}
}

func lookupPath(value any, path string) any {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return value
	}
	current := value
	for _, raw := range strings.Split(clean, ".") {
		segment := strings.TrimSpace(raw)
		if segment == "" {
			return nil
		}
		switch typed := current.(type) {
		case map[string]any:
			current = typed[segment]
		case []any:
			idx, err := strconv.Atoi(segment)
			if err != nil || idx < 0 || idx >= len(typed) {
				return nil
			}
			current = typed[idx]
		default:
			return nil
		}
	}
	return current
}

func extractTextValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			text := extractTextValue(item)
			if text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]any:
		if text, ok := typed["text"]; ok {
			if normalized := extractTextValue(text); normalized != "" {
				return normalized
			}
		}
		if content, ok := typed["content"]; ok {
			if normalized := extractTextValue(content); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

func extractCustomResponseText(respBody []byte, model ModelConfig) string {
	trimmed := strings.TrimSpace(string(respBody))
	if trimmed == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return trimmed
	}
	preferred := providerOptionString(model, "response_text_path", "")
	paths := []string{}
	if preferred != "" {
		paths = append(paths, preferred)
	}
	paths = append(paths,
		"text",
		"response.text",
		"result",
		"output_text",
		"choices.0.message.content",
		"choices.0.text",
		"content.0.text",
	)
	for _, path := range paths {
		if text := extractTextValue(lookupPath(parsed, path)); text != "" {
			return text
		}
	}
	return trimmed
}

func binaryEncodingMode(model ModelConfig) string {
	switch strings.ToLower(strings.TrimSpace(providerOptionString(model, "binary_encoding", "base64"))) {
	case "data_url":
		return "data_url"
	default:
		return "base64"
	}
}

func encodedBinaryData(mode, mimeType string, data []byte) string {
	encoded := base64.StdEncoding.EncodeToString(data)
	if mode == "data_url" {
		mimeType = strings.TrimSpace(mimeType)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		return "data:" + mimeType + ";base64," + encoded
	}
	return encoded
}

func buildCustomJSONMessages(messages []Message, binaryMode string) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, message := range messages {
		parts := normalizeMessageParts(message)
		if len(parts) == 0 {
			continue
		}
		item := map[string]any{
			"role": defaultString(strings.TrimSpace(message.Role), "user"),
		}
		content := make([]map[string]any, 0, len(parts))
		for _, part := range parts {
			entry := map[string]any{"type": strings.TrimSpace(part.Kind)}
			switch strings.ToLower(strings.TrimSpace(part.Kind)) {
			case "text":
				entry["text"] = strings.TrimSpace(part.Text)
			case "image", "audio", "video", "file":
				entry["name"] = strings.TrimSpace(part.Name)
				entry["rel_path"] = strings.TrimSpace(part.RelPath)
				entry["mime_type"] = strings.TrimSpace(part.MIMEType)
				entry["encoding"] = binaryMode
				entry["data"] = encodedBinaryData(binaryMode, part.MIMEType, part.Data)
				entry["size_bytes"] = len(part.Data)
			}
			content = append(content, entry)
		}
		item["content"] = content
		out = append(out, item)
	}
	return out
}

func buildCustomJSONFiles(messages []Message, binaryMode string) []map[string]any {
	files := []map[string]any{}
	for _, message := range messages {
		for _, part := range normalizeMessageParts(message) {
			switch strings.ToLower(strings.TrimSpace(part.Kind)) {
			case "image", "audio", "video", "file":
				files = append(files, map[string]any{
					"kind":       strings.TrimSpace(part.Kind),
					"name":       strings.TrimSpace(part.Name),
					"rel_path":   strings.TrimSpace(part.RelPath),
					"mime_type":  strings.TrimSpace(part.MIMEType),
					"encoding":   binaryMode,
					"data":       encodedBinaryData(binaryMode, part.MIMEType, part.Data),
					"size_bytes": len(part.Data),
				})
			}
		}
	}
	return files
}

func buildMultipartMetadata(req Request) map[string]any {
	return map[string]any{
		"expect_json":   req.ExpectJSON,
		"message_count": len(req.Messages),
	}
}

func buildMultipartMessages(req Request) []map[string]any {
	out := make([]map[string]any, 0, len(req.Messages))
	for _, message := range req.Messages {
		parts := normalizeMessageParts(message)
		if len(parts) == 0 {
			continue
		}
		item := map[string]any{"role": defaultString(strings.TrimSpace(message.Role), "user")}
		content := make([]map[string]any, 0, len(parts))
		for _, part := range parts {
			entry := map[string]any{"type": strings.TrimSpace(part.Kind)}
			switch strings.ToLower(strings.TrimSpace(part.Kind)) {
			case "text":
				entry["text"] = strings.TrimSpace(part.Text)
			case "image", "audio", "video", "file":
				entry["name"] = strings.TrimSpace(part.Name)
				entry["rel_path"] = strings.TrimSpace(part.RelPath)
				entry["mime_type"] = strings.TrimSpace(part.MIMEType)
				entry["size_bytes"] = len(part.Data)
			}
			content = append(content, entry)
		}
		item["content"] = content
		out = append(out, item)
	}
	return out
}
