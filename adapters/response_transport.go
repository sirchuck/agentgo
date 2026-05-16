package adapters

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"time"
)

func doAdapterRequestWithHeaders(request *http.Request, timeout time.Duration) ([]byte, string, int, http.Header, error) {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(request)
	if err != nil {
		return nil, "", 0, nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", 0, nil, err
	}
	return respBody, resp.Status, resp.StatusCode, resp.Header.Clone(), nil
}

func parseMultipartCapableResponse(respBody []byte, contentType string, model ModelConfig, textExtractor func([]byte) (string, error)) (Response, error) {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return parseStructuredMediaResponse(respBody, model, textExtractor)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return Response{}, errors.New("multipart response is missing a boundary")
	}
	preferredTextPart := strings.TrimSpace(providerOptionString(model, "response_text_part", "metadata"))
	preferredFilePart := strings.TrimSpace(providerOptionString(model, "response_file_part", "file"))
	reader := multipart.NewReader(bytes.NewReader(respBody), boundary)
	var response Response
	var firstText string
	var preferredText string
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Response{}, err
		}
		fieldName := strings.TrimSpace(part.FormName())
		fileName := strings.TrimSpace(part.FileName())
		mimeType := strings.TrimSpace(part.Header.Get("Content-Type"))
		data, err := io.ReadAll(part)
		_ = part.Close()
		if err != nil {
			return Response{}, err
		}
		if mimeType == "" && fileName != "" {
			mimeType = strings.TrimSpace(mime.TypeByExtension(filepath.Ext(fileName)))
		}
		isTextLike := fileName == "" && (mimeType == "" || strings.HasPrefix(strings.ToLower(mimeType), "text/") || strings.EqualFold(mimeType, "application/json"))
		if isTextLike {
			textValue, _ := textExtractor(data)
			textValue = strings.TrimSpace(textValue)
			if textValue == "" {
				textValue = extractCustomResponseText(data, model)
			}
			if textValue == "" {
				textValue = strings.TrimSpace(string(data))
			}
			if textValue != "" {
				if firstText == "" {
					firstText = textValue
				}
				if preferredText == "" && fieldName == preferredTextPart {
					preferredText = textValue
				}
			}
			continue
		}
		isPreferredFile := preferredFilePart != "" && fieldName == preferredFilePart
		if len(data) > 0 && (response.FileData == nil || isPreferredFile) {
			response.FileData = append([]byte(nil), data...)
			response.FileName = defaultResponseFileName(fileName, mimeType, "response_output")
			response.FileMIMEType = defaultResponseMIME(mimeType, response.FileName)
			if isPreferredFile {
				preferredFilePart = ""
			}
		}
	}
	response.Text = strings.TrimSpace(defaultString(preferredText, firstText))
	response.RawBody = response.Text
	if strings.TrimSpace(response.Text) == "" && len(response.FileData) == 0 {
		return Response{}, errors.New("response contained no text or binary file")
	}
	return response, nil
}

func parseStructuredMediaResponse(respBody []byte, model ModelConfig, textExtractor func([]byte) (string, error)) (Response, error) {
	text := ""
	if textExtractor != nil {
		if extracted, err := textExtractor(respBody); err == nil {
			text = strings.TrimSpace(extracted)
		}
	}
	if text == "" {
		text = strings.TrimSpace(extractCustomResponseText(respBody, model))
	}
	response := Response{Text: text, RawBody: string(respBody)}
	resolved, err := resolveResponseMedia(respBody, model)
	if err != nil {
		return Response{}, err
	}
	if len(resolved.FileData) > 0 {
		response.FileData = resolved.FileData
		response.FileName = resolved.FileName
		response.FileMIMEType = resolved.FileMIMEType
	}
	if strings.TrimSpace(response.Text) == "" && len(response.FileData) == 0 {
		return Response{}, errors.New("response contained no text or binary file")
	}
	return response, nil
}

func resolveResponseMedia(respBody []byte, model ModelConfig) (Response, error) {
	var parsed any
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return Response{}, nil
	}
	fileName := strings.TrimSpace(asString(lookupPath(parsed, providerOptionString(model, "response_file_name_path", ""))))
	mimeType := strings.TrimSpace(asString(lookupPath(parsed, providerOptionString(model, "response_file_mime_path", ""))))
	if base64Data := extractResponseFileBase64(parsed, model); base64Data != "" {
		decoded, resolvedMIME, err := decodeResponseFileBase64(base64Data, mimeType)
		if err != nil {
			return Response{}, err
		}
		return Response{FileData: decoded, FileName: defaultResponseFileName(fileName, resolvedMIME, "response_output"), FileMIMEType: defaultResponseMIME(resolvedMIME, fileName)}, nil
	}
	fileURL := extractResponseFileURL(parsed, model)
	if fileURL == "" {
		return Response{}, nil
	}
	if isDataURL(fileURL) {
		decoded, resolvedMIME, err := decodeResponseFileBase64(fileURL, mimeType)
		if err != nil {
			return Response{}, err
		}
		return Response{FileData: decoded, FileName: defaultResponseFileName(fileName, resolvedMIME, "response_output"), FileMIMEType: defaultResponseMIME(resolvedMIME, fileName)}, nil
	}
	if !providerOptionBool(model, "download_response_file_url", true) {
		return Response{}, nil
	}
	downloaded, err := downloadResponseFileURL(fileURL, modelTimeout(model, 2*time.Minute))
	if err != nil {
		return Response{}, err
	}
	resolvedName := defaultString(fileName, downloaded.FileName)
	resolvedMIME := defaultString(mimeType, downloaded.FileMIMEType)
	return Response{FileData: downloaded.FileData, FileName: defaultResponseFileName(resolvedName, resolvedMIME, "response_output"), FileMIMEType: defaultResponseMIME(resolvedMIME, resolvedName)}, nil
}

func extractResponseFileURL(parsed any, model ModelConfig) string {
	paths := []string{}
	if preferred := strings.TrimSpace(providerOptionString(model, "response_file_url_path", "")); preferred != "" {
		paths = append(paths, preferred)
	}
	paths = append(paths,
		"data.0.url",
		"data.0.image_url",
		"images.0.url",
		"images.0.image_url",
		"output.0.url",
		"output.url",
		"result.url",
		"url",
	)
	for _, lookup := range paths {
		if value := strings.TrimSpace(asString(lookupPath(parsed, lookup))); value != "" {
			return value
		}
	}
	return ""
}

func extractResponseFileBase64(parsed any, model ModelConfig) string {
	paths := []string{}
	if preferred := strings.TrimSpace(providerOptionString(model, "response_file_base64_path", "")); preferred != "" {
		paths = append(paths, preferred)
	}
	paths = append(paths,
		"data.0.b64_json",
		"data.0.base64",
		"data.0.data",
		"image.base64",
		"image.data",
		"output.0.b64_json",
	)
	for _, lookup := range paths {
		if value := strings.TrimSpace(asString(lookupPath(parsed, lookup))); value != "" {
			return value
		}
	}
	return ""
}

func isDataURL(value string) bool {
	clean := strings.TrimSpace(value)
	return strings.HasPrefix(strings.ToLower(clean), "data:") && strings.Contains(clean, ";base64,")
}

func decodeResponseFileBase64(value, fallbackMIME string) ([]byte, string, error) {
	clean := strings.TrimSpace(value)
	resolvedMIME := strings.TrimSpace(fallbackMIME)
	if isDataURL(clean) {
		comma := strings.Index(clean, ",")
		if comma <= 0 {
			return nil, "", errors.New("response data url is malformed")
		}
		header := clean[:comma]
		payload := clean[comma+1:]
		if strings.HasPrefix(strings.ToLower(header), "data:") {
			header = header[5:]
		}
		if semi := strings.Index(header, ";"); semi >= 0 {
			if strings.TrimSpace(header[:semi]) != "" {
				resolvedMIME = strings.TrimSpace(header[:semi])
			}
		}
		clean = payload
	}
	compact := strings.Join(strings.Fields(clean), "")
	decoded, err := base64.StdEncoding.DecodeString(compact)
	if err != nil {
		if fallback, fallbackErr := base64.RawStdEncoding.DecodeString(compact); fallbackErr == nil {
			decoded = fallback
		} else {
			return nil, "", fmt.Errorf("response file base64 decode failed: %w", err)
		}
	}
	return decoded, resolvedMIME, nil
}

func downloadResponseFileURL(rawURL string, timeout time.Duration) (Response, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil || parsed.Scheme == "" || parsed.Host == "" {
		return Response{}, fmt.Errorf("invalid response file url: %s", strings.TrimSpace(rawURL))
	}
	request, err := http.NewRequest(http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Response{}, err
	}
	body, _, statusCode, headers, err := doAdapterRequestWithHeaders(request, timeout)
	if err != nil {
		return Response{}, err
	}
	if statusCode >= 300 {
		return Response{}, fmt.Errorf("response file download failed with status %d", statusCode)
	}
	fileName := strings.TrimSpace(path.Base(parsed.Path))
	mimeType := strings.TrimSpace(headers.Get("Content-Type"))
	return Response{FileData: append([]byte(nil), body...), FileName: defaultResponseFileName(fileName, mimeType, "response_output"), FileMIMEType: defaultResponseMIME(mimeType, fileName)}, nil
}

func defaultResponseFileName(name, mimeType, fallbackBase string) string {
	clean := strings.TrimSpace(name)
	if clean != "" {
		clean = path.Base(clean)
		clean = strings.TrimSpace(clean)
	}
	if clean == "." || clean == "/" || clean == "" {
		clean = fallbackBase
	}
	if ext := strings.TrimSpace(filepath.Ext(clean)); ext == "" {
		if guessed := extensionForMIME(mimeType); strings.TrimSpace(guessed) != "" {
			clean += guessed
		}
	}
	return clean
}

func defaultResponseMIME(mimeType, fileName string) string {
	clean := strings.TrimSpace(mimeType)
	if clean != "" {
		if mediaType, _, err := mime.ParseMediaType(clean); err == nil && strings.TrimSpace(mediaType) != "" {
			return strings.TrimSpace(mediaType)
		}
		return clean
	}
	if guessed := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName)))); guessed != "" {
		if mediaType, _, err := mime.ParseMediaType(guessed); err == nil && strings.TrimSpace(mediaType) != "" {
			return strings.TrimSpace(mediaType)
		}
		return guessed
	}
	return "application/octet-stream"
}
