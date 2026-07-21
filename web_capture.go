package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"agentgo/adapters"
)

const (
	workModeURLCaptureMaxURLs       = 10
	workModeURLCaptureMaxHTMLBytes  = 10_000_000
	workModeURLCaptureMaxTextBytes  = 650_000
	workModeURLCaptureMaxTotalText  = 1_800_000
	workModeURLCaptureMaxImageBytes = 900_000
	workModeURLCaptureMaxImages     = 6
	workModeURLCaptureMaxTotalImage = 5_400_000
	workModeURLCaptureTimeout       = 30 * time.Second
	workModeURLBrowserTimeout       = 35 * time.Second
	workModeURLCaptureUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	workModeWebshotRootDir          = "tmp/webshots"
)

var (
	workModeURLScreenshotFileWait     = 2 * time.Second
	workModeURLScreenshotPollInterval = 100 * time.Millisecond
	workModeWebshotCaptureMu          sync.Mutex
)

type workModeURLCaptureRequest struct {
	URL                  string `json:"url"`
	IncludeMetadata      bool   `json:"includeMetadata"`
	IncludeText          bool   `json:"includeText"`
	IncludeScreenshot    bool   `json:"includeScreenshot"`
	IncludeTranscript    bool   `json:"includeTranscript"`
	IncludeVisualSamples bool   `json:"includeVisualSamples"`
	WebshotTimestamp     string `json:"webshotTimestamp,omitempty"`
	WebshotIndex         int    `json:"webshotIndex,omitempty"`
}

type workModeURLCaptureMetadata struct {
	Title        string `json:"title,omitempty"`
	Description  string `json:"description,omitempty"`
	SiteName     string `json:"siteName,omitempty"`
	Author       string `json:"author,omitempty"`
	Language     string `json:"language,omitempty"`
	Published    string `json:"published,omitempty"`
	CanonicalURL string `json:"canonicalUrl,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	RetrievedAt  string `json:"retrievedAt,omitempty"`
}

type workModeURLCaptureImage struct {
	Name       string `json:"name"`
	Label      string `json:"label,omitempty"`
	MIMEType   string `json:"mime_type"`
	Data       string `json:"data"`
	SizeBytes  int64  `json:"size_bytes"`
	StoredName string `json:"stored_name,omitempty"`
	StoredPath string `json:"stored_path,omitempty"`
}

type workModeWebshotWorkspace struct {
	Directory      string
	WorkPathPrefix string
	Persistent     bool
	Timestamp      string
	Index          int
	Logf           func(format string, args ...any)
}

type workModeURLCaptureInput struct {
	RequestedURL string                     `json:"requestedUrl"`
	FinalURL     string                     `json:"finalUrl,omitempty"`
	Kind         string                     `json:"kind,omitempty"`
	Metadata     workModeURLCaptureMetadata `json:"metadata,omitempty"`
	PageText     string                     `json:"pageText,omitempty"`
	Transcript   string                     `json:"transcript,omitempty"`
	Images       []workModeURLCaptureImage  `json:"images,omitempty"`
	Warnings     []string                   `json:"warnings,omitempty"`
	Errors       []string                   `json:"errors,omitempty"`
}

type workModeURLCaptureResponse struct {
	Capture workModeURLCaptureInput `json:"capture"`
}

type workModeURLCapabilitiesResponse struct {
	BrowserAvailable bool `json:"browserAvailable"`
}

type fetchedWebResource struct {
	RequestedURL string
	FinalURL     string
	ContentType  string
	Body         []byte
	Status       int
}

type htmlCaptureNode struct {
	Tag      string
	Attrs    map[string]string
	Parent   *htmlCaptureNode
	Children []*htmlCaptureNode
	Text     strings.Builder
}

type htmlCaptureDocument struct {
	Root     *htmlCaptureNode
	Metadata workModeURLCaptureMetadata
}

func (a *App) handleWorkModeURLCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, err := findAgentGOBrowserExecutable()
	writeJSON(w, http.StatusOK, workModeURLCapabilitiesResponse{BrowserAvailable: err == nil})
}

func (a *App) handleWorkModeURLCapture(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req workModeURLCaptureRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1_000_000)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	parsed, err := url.Parse(req.URL)
	if err != nil || parsed == nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		http.Error(w, "AgentGO can collect only complete http:// or https:// URLs.", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), workModeURLCaptureTimeout+(2*workModeURLBrowserTimeout))
	defer cancel()
	var workspace *workModeWebshotWorkspace
	if req.IncludeScreenshot {
		projectName, projectErr := a.requireActiveProject()
		if projectErr != nil {
			http.Error(w, "Select an active project before capturing a webpage screenshot.", http.StatusBadRequest)
			return
		}
		resolved, workspaceErr := a.workModeWebshotWorkspace(projectName)
		if workspaceErr != nil {
			http.Error(w, workspaceErr.Error(), http.StatusInternalServerError)
			return
		}
		resolved.Timestamp = normalizeWorkModeWebshotTimestamp(req.WebshotTimestamp, time.Now())
		resolved.Index = req.WebshotIndex
		workspace = &resolved
	}
	capture := captureWorkModeURLWithWorkspace(ctx, req, workspace)
	writeJSON(w, http.StatusOK, workModeURLCaptureResponse{Capture: capture})
}

func captureWorkModeURL(ctx context.Context, req workModeURLCaptureRequest) workModeURLCaptureInput {
	return captureWorkModeURLWithWorkspace(ctx, req, nil)
}

func captureWorkModeURLWithWorkspace(ctx context.Context, req workModeURLCaptureRequest, workspace *workModeWebshotWorkspace) workModeURLCaptureInput {
	capture := workModeURLCaptureInput{
		RequestedURL: strings.TrimSpace(req.URL),
		Kind:         "webpage",
		Warnings:     workModeURLRiskWarnings(req.URL),
	}
	if isYouTubeURL(req.URL) {
		capture.Kind = "youtube"
	}

	needPage := req.IncludeMetadata || req.IncludeText || req.IncludeTranscript || req.IncludeVisualSamples
	var resource fetchedWebResource
	var doc htmlCaptureDocument
	browserFallbackTried := false
	browserContentUsed := false

	useRenderedDOM := func(rendered []byte, reason, contentType string) bool {
		if len(rendered) == 0 {
			return false
		}
		if blockReason := detectWorkModeURLBlockPage(rendered); blockReason != "" {
			capture.Errors = append(capture.Errors, workModeURLBlockPageError(blockReason))
			return false
		}
		renderedDoc, err := parseHTMLCaptureDocument(rendered, capture.FinalURL, contentType)
		if err != nil {
			capture.Warnings = append(capture.Warnings, "AgentGO rendered a browser copy, but could not parse it: "+shortError(err))
			return false
		}
		doc = renderedDoc
		resource = fetchedWebResource{
			RequestedURL: capture.RequestedURL,
			FinalURL:     capture.FinalURL,
			ContentType:  firstNonEmpty(contentType, "text/html"),
			Body:         rendered,
			Status:       http.StatusOK,
		}
		capture.PageText = ""
		browserContentUsed = true
		if strings.TrimSpace(reason) != "" {
			capture.Warnings = append(capture.Warnings, "AgentGO used browser rendering because "+strings.TrimSpace(reason)+".")
		}
		return true
	}

	tryBrowserFallback := func(reason string) bool {
		if browserFallbackTried {
			return false
		}
		browserFallbackTried = true
		rendered, err := renderWorkModeURLDOM(ctx, capture.FinalURL)
		if err != nil {
			capture.Warnings = append(capture.Warnings, "AgentGO could not render a browser fallback: "+shortError(err))
			return false
		}
		return useRenderedDOM(rendered, reason, resource.ContentType)
	}

	if req.IncludeScreenshot {
		browserFallbackTried = true
		browserPass, err := captureWorkModeURLBrowserPass(ctx, capture.RequestedURL, workspace)
		if err != nil {
			capture.Errors = append(capture.Errors, "Could not capture the webpage in Chromium: "+shortError(err))
		} else if browserPass.BlockReason != "" {
			capture.Errors = append(capture.Errors, workModeURLBlockPageError(browserPass.BlockReason))
		} else {
			capture.FinalURL = firstNonEmpty(browserPass.FinalURL, capture.RequestedURL)
			if warning := redirectHostWarning(req.URL, capture.FinalURL); warning != "" {
				capture.Warnings = append(capture.Warnings, warning)
			}
			if needPage {
				useRenderedDOM(browserPass.DOM, "the selected screenshot requires a rendered page", "text/html")
			}
			if browserPass.Image != nil {
				capture.Images = append(capture.Images, *browserPass.Image)
			}
		}
	} else if needPage {
		fetched, fetchErr := fetchWorkModeURL(ctx, req.URL, workModeURLCaptureMaxHTMLBytes)
		resource = fetched
		capture.FinalURL = firstNonEmpty(fetched.FinalURL, capture.RequestedURL)
		if warning := redirectHostWarning(req.URL, capture.FinalURL); warning != "" {
			capture.Warnings = append(capture.Warnings, warning)
		}

		fetchIssue := ""
		if blockReason := detectWorkModeURLBlockPage(fetched.Body); blockReason != "" {
			fetchIssue = "the lightweight request received a " + blockReason
		} else if fetchErr != nil {
			fetchIssue = "the lightweight request failed: " + shortError(fetchErr)
		} else if looksLikeHTML(fetched.ContentType, fetched.Body) {
			parsedDoc, parseErr := parseHTMLCaptureDocument(fetched.Body, capture.FinalURL, fetched.ContentType)
			if parseErr != nil {
				fetchIssue = "the fetched HTML could not be parsed: " + shortError(parseErr)
			} else {
				doc = parsedDoc
			}
		} else if req.IncludeText {
			capture.PageText = truncateUTF8Bytes(strings.TrimSpace(string(fetched.Body)), workModeURLCaptureMaxTextBytes)
		}

		if fetchIssue != "" {
			if !tryBrowserFallback(fetchIssue) {
				if blockReason := detectWorkModeURLBlockPage(fetched.Body); blockReason != "" {
					capture.Errors = append(capture.Errors, workModeURLBlockPageError(blockReason))
				} else if fetchErr != nil {
					capture.Errors = append(capture.Errors, "Could not retrieve the webpage: "+shortError(fetchErr))
				} else {
					capture.Errors = append(capture.Errors, "Could not use the webpage response: "+fetchIssue)
				}
			}
		}
	}

	if capture.FinalURL == "" {
		capture.FinalURL = capture.RequestedURL
	}

	applyRequestedDocumentData := func() {
		if req.IncludeMetadata {
			capture.Metadata = doc.Metadata
			capture.Metadata.ContentType = firstNonEmpty(capture.Metadata.ContentType, resource.ContentType)
			capture.Metadata.RetrievedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if req.IncludeText && capture.PageText == "" && doc.Root != nil {
			capture.PageText = extractReadableHTMLText(doc.Root, capture.FinalURL, workModeURLCaptureMaxTextBytes)
		}
	}
	applyRequestedDocumentData()

	if !req.IncludeScreenshot && !browserContentUsed && !browserFallbackTried {
		needsRenderedFallback := false
		reason := ""
		if req.IncludeText && len(strings.TrimSpace(capture.PageText)) < 180 {
			needsRenderedFallback = true
			reason = "the lightweight response exposed little readable text"
		}
		if req.IncludeMetadata && !hasMeaningfulURLCaptureMetadata(capture.Metadata) {
			needsRenderedFallback = true
			if reason == "" {
				reason = "the lightweight response exposed no useful page metadata"
			}
		}
		if needsRenderedFallback {
			originalDoc := doc
			originalResource := resource
			originalText := capture.PageText
			originalMetadata := capture.Metadata
			if tryBrowserFallback(reason) {
				applyRequestedDocumentData()
			} else {
				doc = originalDoc
				resource = originalResource
				capture.PageText = originalText
				capture.Metadata = originalMetadata
			}
		}
	}

	if req.IncludeText && strings.TrimSpace(capture.PageText) == "" {
		capture.Errors = append(capture.Errors, "AgentGO could not find readable page text.")
	}
	if req.IncludeTranscript && capture.Kind == "youtube" {
		if len(resource.Body) == 0 {
			capture.Errors = append(capture.Errors, "Could not collect the YouTube transcript because the video page was unavailable.")
		} else if transcript, err := captureYouTubeTranscript(ctx, resource.Body); err != nil {
			capture.Errors = append(capture.Errors, "Could not collect the YouTube transcript: "+shortError(err))
		} else {
			capture.Transcript = truncateUTF8Bytes(transcript, workModeURLCaptureMaxTextBytes)
		}
	}
	if req.IncludeVisualSamples && capture.Kind == "youtube" {
		samples, warnings, err := captureYouTubeVisualSamples(ctx, resource.Body, capture.FinalURL)
		capture.Warnings = append(capture.Warnings, warnings...)
		if err != nil {
			capture.Errors = append(capture.Errors, "Could not collect YouTube visual samples: "+shortError(err))
		} else {
			capture.Images = append(capture.Images, samples...)
		}
	}
	capture.Warnings = uniqueNonEmptyStrings(capture.Warnings)
	capture.Errors = uniqueNonEmptyStrings(capture.Errors)
	return capture
}

func hasMeaningfulURLCaptureMetadata(meta workModeURLCaptureMetadata) bool {
	return strings.TrimSpace(firstNonEmpty(meta.Title, meta.Description, meta.SiteName, meta.Author, meta.Published, meta.Language)) != ""
}

func workModeURLBlockPageError(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "website security block page"
	}
	return "Website preview was blocked by the website's security service (" + reason + "). AgentGO did not send the block page as website content."
}

func detectWorkModeURLBlockPage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	text := strings.ToLower(string(body[:minInt(len(body), 2_000_000)]))
	text = compactWhitespace(html.UnescapeString(text))
	contains := func(parts ...string) bool {
		for _, part := range parts {
			if !strings.Contains(text, part) {
				return false
			}
		}
		return true
	}
	switch {
	case contains("sorry, you have been blocked", "cloudflare ray id"):
		return "Cloudflare block page"
	case contains("you are unable to access", "cloudflare ray id"):
		return "Cloudflare block page"
	case contains("attention required", "cloudflare"):
		return "Cloudflare challenge page"
	case contains("just a moment", "cf-chl-") || contains("checking your browser", "cloudflare"):
		return "Cloudflare browser challenge"
	case contains("enable javascript and cookies to continue", "cloudflare"):
		return "Cloudflare browser challenge"
	case contains("access denied", "reference #"):
		return "access-denied page"
	case contains("request unsuccessful", "incapsula incident id"):
		return "Imperva security block page"
	default:
		return ""
	}
}

func mergeURLCaptureMetadata(primary, secondary workModeURLCaptureMetadata) workModeURLCaptureMetadata {
	primary.Title = firstNonEmpty(primary.Title, secondary.Title)
	primary.Description = firstNonEmpty(primary.Description, secondary.Description)
	primary.SiteName = firstNonEmpty(primary.SiteName, secondary.SiteName)
	primary.Author = firstNonEmpty(primary.Author, secondary.Author)
	primary.Language = firstNonEmpty(primary.Language, secondary.Language)
	primary.Published = firstNonEmpty(primary.Published, secondary.Published)
	primary.CanonicalURL = firstNonEmpty(primary.CanonicalURL, secondary.CanonicalURL)
	primary.ContentType = firstNonEmpty(primary.ContentType, secondary.ContentType)
	primary.RetrievedAt = firstNonEmpty(primary.RetrievedAt, secondary.RetrievedAt)
	return primary
}

func fetchWorkModeURL(ctx context.Context, rawURL string, maxBytes int64) (fetchedWebResource, error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Timeout: workModeURLCaptureTimeout,
		Jar:     jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fetchedWebResource{}, err
	}
	req.Header.Set("User-Agent", workModeURLCaptureUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain;q=0.9,*/*;q=0.7")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := client.Do(req)
	if err != nil {
		return fetchedWebResource{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return fetchedWebResource{}, err
	}
	if int64(len(data)) > maxBytes {
		return fetchedWebResource{}, fmt.Errorf("response exceeded %d MB", maxBytes/(1024*1024))
	}
	finalURL := rawURL
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}
	resource := fetchedWebResource{
		RequestedURL: rawURL,
		FinalURL:     finalURL,
		ContentType:  strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]),
		Body:         data,
		Status:       resp.StatusCode,
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return resource, fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return resource, nil
}

func looksLikeHTML(contentType string, body []byte) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if strings.Contains(contentType, "html") || strings.Contains(contentType, "xhtml") {
		return true
	}
	prefix := strings.ToLower(strings.TrimSpace(string(body[:minInt(len(body), 512)])))
	return strings.Contains(prefix, "<!doctype html") || strings.Contains(prefix, "<html") || strings.Contains(prefix, "<head")
}

func extractHTMLCaptureMetadata(root *htmlCaptureNode, baseURL string) workModeURLCaptureMetadata {
	meta := workModeURLCaptureMetadata{}
	values := map[string]string{}
	walkHTMLCaptureNodes(root, func(node *htmlCaptureNode) bool {
		if node.Tag == "html" && meta.Language == "" {
			meta.Language = strings.TrimSpace(node.Attrs["lang"])
		}
		if node.Tag == "title" && meta.Title == "" {
			meta.Title = compactWhitespace(nodeVisibleText(node))
		}
		if node.Tag == "meta" {
			key := strings.ToLower(strings.TrimSpace(firstNonEmpty(node.Attrs["property"], node.Attrs["name"], node.Attrs["itemprop"])))
			content := strings.TrimSpace(node.Attrs["content"])
			if key != "" && content != "" && values[key] == "" {
				values[key] = content
			}
		}
		if node.Tag == "link" {
			rel := strings.ToLower(strings.TrimSpace(node.Attrs["rel"]))
			if strings.Contains(rel, "canonical") && meta.CanonicalURL == "" {
				meta.CanonicalURL = resolveWebURL(baseURL, node.Attrs["href"])
			}
		}
		return true
	})
	meta.Title = firstNonEmpty(values["og:title"], values["twitter:title"], meta.Title)
	meta.Description = firstNonEmpty(values["og:description"], values["twitter:description"], values["description"])
	meta.SiteName = firstNonEmpty(values["og:site_name"], values["application-name"])
	meta.Author = firstNonEmpty(values["author"], values["article:author"])
	meta.Published = firstNonEmpty(values["article:published_time"], values["date"], values["datepublished"])
	meta.Language = firstNonEmpty(values["og:locale"], meta.Language)
	meta.CanonicalURL = firstNonEmpty(meta.CanonicalURL, resolveWebURL(baseURL, values["og:url"]), baseURL)
	return meta
}

func walkHTMLCaptureNodes(node *htmlCaptureNode, visit func(*htmlCaptureNode) bool) {
	if node == nil || !visit(node) {
		return
	}
	for _, child := range node.Children {
		walkHTMLCaptureNodes(child, visit)
	}
}

func nodeVisibleText(node *htmlCaptureNode) string {
	if node == nil || isHTMLCaptureHidden(node) || isIgnoredHTMLCaptureTag(node.Tag) {
		return ""
	}
	parts := []string{}
	if text := compactWhitespace(node.Text.String()); text != "" {
		parts = append(parts, text)
	}
	for _, child := range node.Children {
		if text := nodeVisibleText(child); text != "" {
			parts = append(parts, text)
		}
	}
	return compactWhitespace(strings.Join(parts, " "))
}

func isHTMLCaptureHidden(node *htmlCaptureNode) bool {
	if node == nil {
		return false
	}
	if _, ok := node.Attrs["hidden"]; ok {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(node.Attrs["aria-hidden"]), "true") {
		return true
	}
	style := strings.ToLower(strings.ReplaceAll(node.Attrs["style"], " ", ""))
	if strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") {
		return true
	}
	if node.Tag == "input" && strings.EqualFold(strings.TrimSpace(node.Attrs["type"]), "hidden") {
		return true
	}
	return false
}

func isIgnoredHTMLCaptureTag(tag string) bool {
	switch strings.ToLower(tag) {
	case "script", "style", "noscript", "svg", "canvas", "template", "iframe", "object", "embed", "head":
		return true
	default:
		return false
	}
}

func extractReadableHTMLText(root *htmlCaptureNode, baseURL string, maxBytes int) string {
	candidate := bestHTMLCaptureCandidate(root)
	if candidate == nil {
		candidate = root
	}
	blocks := []string{}
	links := []string{}
	seenLinks := map[string]bool{}
	var render func(*htmlCaptureNode)
	render = func(node *htmlCaptureNode) {
		if node == nil || isHTMLCaptureHidden(node) || isIgnoredHTMLCaptureTag(node.Tag) || isBoilerplateHTMLCaptureNode(node) {
			return
		}
		tag := strings.ToLower(node.Tag)
		text := compactWhitespace(nodeVisibleText(node))
		switch tag {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			if text != "" {
				level := 1
				if len(tag) == 2 {
					if parsed, err := strconv.Atoi(tag[1:]); err == nil {
						level = parsed
					}
				}
				blocks = appendCleanBlock(blocks, strings.Repeat("#", level)+" "+text)
			}
			return
		case "p":
			if text != "" {
				blocks = appendCleanBlock(blocks, text)
			}
			return
		case "li":
			if text != "" {
				blocks = appendCleanBlock(blocks, "- "+text)
			}
			return
		case "blockquote":
			if text != "" {
				blocks = appendCleanBlock(blocks, "> "+text)
			}
			return
		case "pre", "code":
			if text != "" {
				blocks = appendCleanBlock(blocks, "```\n"+text+"\n```")
			}
			return
		case "tr":
			cells := []string{}
			for _, child := range node.Children {
				if child.Tag == "td" || child.Tag == "th" {
					if value := compactWhitespace(nodeVisibleText(child)); value != "" {
						cells = append(cells, value)
					}
				}
			}
			if len(cells) > 0 {
				blocks = appendCleanBlock(blocks, "| "+strings.Join(cells, " | ")+" |")
			}
			return
		case "figcaption", "caption":
			if text != "" {
				blocks = appendCleanBlock(blocks, "Caption: "+text)
			}
			return
		case "button", "label":
			if text != "" && len(text) <= 180 {
				blocks = appendCleanBlock(blocks, text)
			}
		case "a":
			href := resolveWebURL(baseURL, node.Attrs["href"])
			if text != "" && href != "" && !seenLinks[href] && len(links) < 50 {
				seenLinks[href] = true
				links = append(links, text+" → "+href)
			}
		}
		for _, child := range node.Children {
			render(child)
		}
	}
	render(candidate)
	if len(links) > 0 {
		blocks = append(blocks, "## Relevant links", strings.Join(links, "\n"))
	}
	text := strings.TrimSpace(strings.Join(blocks, "\n\n"))
	if text == "" {
		text = compactWhitespace(nodeVisibleText(candidate))
	}
	return truncateUTF8Bytes(text, maxBytes)
}

func appendCleanBlock(blocks []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return blocks
	}
	if len(blocks) > 0 && blocks[len(blocks)-1] == value {
		return blocks
	}
	return append(blocks, value)
}

func bestHTMLCaptureCandidate(root *htmlCaptureNode) *htmlCaptureNode {
	var best *htmlCaptureNode
	bestScore := -1.0
	walkHTMLCaptureNodes(root, func(node *htmlCaptureNode) bool {
		if node == nil || isHTMLCaptureHidden(node) || isIgnoredHTMLCaptureTag(node.Tag) || isBoilerplateHTMLCaptureNode(node) {
			return false
		}
		switch node.Tag {
		case "main", "article", "section", "div", "body":
		default:
			return true
		}
		text := nodeVisibleText(node)
		textLen := len([]rune(text))
		if textLen < 100 {
			return true
		}
		linkLen := htmlCaptureLinkTextLength(node)
		linkDensity := float64(linkLen) / float64(maxInt(textLen, 1))
		punctuation := strings.Count(text, ".") + strings.Count(text, "?") + strings.Count(text, "!") + strings.Count(text, ",")
		blocks := htmlCaptureBlockCount(node)
		score := float64(textLen) + float64(punctuation*18) + float64(blocks*45)
		switch node.Tag {
		case "main":
			score += 2200
		case "article":
			score += 1700
		case "section":
			score += 250
		case "body":
			score += 50
		}
		identity := strings.ToLower(node.Attrs["id"] + " " + node.Attrs["class"] + " " + node.Attrs["role"])
		if containsAny(identity, []string{"article", "content", "main", "post", "entry", "story", "comment", "thread", "discussion"}) {
			score += 600
		}
		if containsAny(identity, []string{"nav", "sidebar", "footer", "header", "menu", "advert", "cookie", "modal", "login", "signup", "related", "recommend"}) {
			score -= 1800
		}
		score *= 1 - minFloat(linkDensity*0.82, 0.82)
		if score > bestScore {
			bestScore = score
			best = node
		}
		return true
	})
	return best
}

func htmlCaptureLinkTextLength(node *htmlCaptureNode) int {
	total := 0
	walkHTMLCaptureNodes(node, func(child *htmlCaptureNode) bool {
		if child.Tag == "a" {
			total += len([]rune(nodeVisibleText(child)))
			return false
		}
		return !isHTMLCaptureHidden(child) && !isIgnoredHTMLCaptureTag(child.Tag)
	})
	return total
}

func htmlCaptureBlockCount(node *htmlCaptureNode) int {
	count := 0
	walkHTMLCaptureNodes(node, func(child *htmlCaptureNode) bool {
		switch child.Tag {
		case "p", "li", "blockquote", "pre", "tr", "h1", "h2", "h3", "h4", "h5", "h6":
			count++
		}
		return !isHTMLCaptureHidden(child) && !isIgnoredHTMLCaptureTag(child.Tag)
	})
	return count
}

func isBoilerplateHTMLCaptureNode(node *htmlCaptureNode) bool {
	if node == nil {
		return false
	}
	tag := strings.ToLower(node.Tag)
	switch tag {
	case "nav", "footer", "aside":
		return true
	}
	identity := strings.ToLower(node.Attrs["id"] + " " + node.Attrs["class"] + " " + node.Attrs["role"])
	return containsAny(identity, []string{"cookie-banner", "cookie-consent", "advertisement", "ad-container", "site-nav", "sidebar", "modal-backdrop"})
}

func renderWorkModeURLDOM(ctx context.Context, rawURL string) ([]byte, error) {
	browser, err := findAgentGOBrowserExecutable()
	if err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp("", "agentgo-browser-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	browserCtx, cancel := context.WithTimeout(ctx, workModeURLBrowserTimeout)
	defer cancel()
	args := browserCommonArgsForExecutable(browser, tmpDir)
	args = append(args, "--window-size=1440,1200", "--timeout=6000", "--dump-dom", rawURL)
	outputPath := filepath.Join(tmpDir, "rendered.html")
	logPath := filepath.Join(tmpDir, "browser.log")
	outputFile, err := os.Create(outputPath)
	if err != nil {
		return nil, err
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		_ = outputFile.Close()
		return nil, err
	}
	cmd := exec.Command(browser, args...)
	cmd.Stdout = outputFile
	cmd.Stderr = logFile
	runErr := runBrowserCommand(browserCtx, cmd)
	_ = outputFile.Close()
	_ = logFile.Close()
	if runErr != nil {
		detailBytes, _ := os.ReadFile(logPath)
		detail := strings.TrimSpace(string(detailBytes))
		if detail != "" {
			return nil, fmt.Errorf("headless browser DOM capture failed: %w (%s)", runErr, detail)
		}
		return nil, fmt.Errorf("headless browser DOM capture failed: %w", runErr)
	}
	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, err
	}
	if len(output) == 0 {
		return nil, errors.New("headless browser returned an empty DOM")
	}
	if len(output) > workModeURLCaptureMaxHTMLBytes {
		return nil, errors.New("rendered DOM exceeded the capture limit")
	}
	return output, nil
}

type workModeURLBrowserPassResult struct {
	DOM         []byte
	FinalURL    string
	Image       *workModeURLCaptureImage
	BlockReason string
}

type workModeURLBrowserPassAttempt struct {
	Mode          string
	RunErr        error
	ScreenshotErr error
	Log           string
	DOM           []byte
	ProducedDOM   bool
	ProducedImage bool
}

func captureWorkModeURLBrowserPass(ctx context.Context, rawURL string, workspace *workModeWebshotWorkspace) (workModeURLBrowserPassResult, error) {
	browser, err := findAgentGOBrowserExecutable()
	if err != nil {
		return workModeURLBrowserPassResult{}, err
	}

	var resolved workModeWebshotWorkspace
	var cleanupDirectory string
	if workspace == nil {
		cleanupDirectory, err = os.MkdirTemp("", "agentgo-browser-pass-")
		if err != nil {
			return workModeURLBrowserPassResult{}, err
		}
		defer os.RemoveAll(cleanupDirectory)
		resolved = workModeWebshotWorkspace{Directory: cleanupDirectory}
	} else {
		resolved = *workspace
	}
	resolved.Directory = strings.TrimSpace(resolved.Directory)
	if resolved.Directory == "" {
		return workModeURLBrowserPassResult{}, errors.New("webpage browser workspace is not configured")
	}
	resolved.Directory, err = filepath.Abs(resolved.Directory)
	if err != nil {
		return workModeURLBrowserPassResult{}, err
	}
	if err := os.MkdirAll(resolved.Directory, 0o755); err != nil {
		return workModeURLBrowserPassResult{}, err
	}

	workModeWebshotCaptureMu.Lock()
	defer workModeWebshotCaptureMu.Unlock()

	storedName, outputPath, err := nextWorkModeWebshotPath(resolved.Directory, resolved.Timestamp, resolved.Index, time.Now())
	if err != nil {
		return workModeURLBrowserPassResult{}, err
	}
	workDir, err := os.MkdirTemp(resolved.Directory, "browser-pass-work-")
	if err != nil {
		return workModeURLBrowserPassResult{}, err
	}
	defer os.RemoveAll(workDir)

	retainOutput := false
	defer func() {
		if !retainOutput {
			_ = os.Remove(outputPath)
		}
	}()
	if resolved.Logf != nil {
		resolved.Logf("Capturing Work Mode webpage once with browser %s using AgentGO work/tmp workspace %s", browser, outputPath)
	}

	newModeResult := runWorkModeURLBrowserPassAttempt(ctx, browser, rawURL, workDir, outputPath, "--headless=new", "new")
	selected := newModeResult
	if (!newModeResult.ProducedDOM || !newModeResult.ProducedImage) && ctx.Err() == nil {
		_ = os.Remove(outputPath)
		legacyResult := runWorkModeURLBrowserPassAttempt(ctx, browser, rawURL, workDir, outputPath, "--headless", "legacy")
		selected = legacyResult
		if !legacyResult.ProducedDOM || !legacyResult.ProducedImage {
			_ = os.Remove(outputPath)
			return workModeURLBrowserPassResult{}, browserPassAttemptFailure(newModeResult, legacyResult)
		}
	} else if !newModeResult.ProducedDOM || !newModeResult.ProducedImage {
		_ = os.Remove(outputPath)
		return workModeURLBrowserPassResult{}, browserPassAttemptFailure(newModeResult)
	}

	result := workModeURLBrowserPassResult{
		DOM:      selected.DOM,
		FinalURL: rawURL,
	}
	if blockReason := detectWorkModeURLBlockPage(selected.DOM); blockReason != "" {
		result.BlockReason = blockReason
		return result, nil
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		return workModeURLBrowserPassResult{}, fmt.Errorf("browser produced a screenshot path that could not be read: %w", err)
	}
	compressed, err := compressCapturedImage(data, workModeURLCaptureMaxImageBytes)
	if err != nil {
		return workModeURLBrowserPassResult{}, err
	}
	host := safeCaptureFilePart(hostnameFromURL(rawURL))
	if host == "" {
		host = "webpage"
	}
	imageData := workModeURLCaptureImage{
		Name:      host + "-screenshot.jpg",
		Label:     "Webpage screenshot",
		MIMEType:  "image/jpeg",
		Data:      base64.StdEncoding.EncodeToString(compressed),
		SizeBytes: int64(len(compressed)),
	}
	if resolved.Persistent {
		imageData.StoredName = storedName
		imageData.StoredPath = filepath.ToSlash(filepath.Join(filepath.FromSlash(resolved.WorkPathPrefix), storedName))
		retainOutput = true
	}
	result.Image = &imageData
	return result, nil
}

func runWorkModeURLBrowserPassAttempt(ctx context.Context, browser, rawURL, tmpDir, outputPath, headlessArg, modeLabel string) workModeURLBrowserPassAttempt {
	result := workModeURLBrowserPassAttempt{Mode: modeLabel}
	browserCtx, cancel := context.WithTimeout(ctx, workModeURLBrowserTimeout)
	defer cancel()

	profileDir := filepath.Join(tmpDir, "profile-"+modeLabel)
	args := browserCommonArgsWithHeadlessForExecutable(browser, profileDir, headlessArg)
	args = append(args,
		"--window-size=1440,1200",
		"--hide-scrollbars",
		"--timeout=6000",
		"--run-all-compositor-stages-before-draw",
		"--dump-dom",
		"--screenshot="+outputPath,
		rawURL,
	)
	domPath := filepath.Join(tmpDir, "rendered-"+modeLabel+".html")
	logPath := filepath.Join(tmpDir, "browser-pass-"+modeLabel+".log")
	domFile, err := os.Create(domPath)
	if err != nil {
		result.RunErr = err
		return result
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		_ = domFile.Close()
		result.RunErr = err
		return result
	}
	cmd := exec.Command(browser, args...)
	cmd.Stdout = domFile
	cmd.Stderr = logFile
	result.RunErr = runBrowserCommand(browserCtx, cmd)
	_ = domFile.Close()
	_ = logFile.Close()

	detailBytes, _ := os.ReadFile(logPath)
	result.Log = strings.TrimSpace(string(detailBytes))
	if domBytes, readErr := os.ReadFile(domPath); readErr == nil && len(domBytes) > 0 && len(domBytes) <= workModeURLCaptureMaxHTMLBytes {
		result.DOM = domBytes
		result.ProducedDOM = true
	}
	result.ScreenshotErr = waitForStableNonEmptyFile(ctx, outputPath, workModeURLScreenshotFileWait, workModeURLScreenshotPollInterval)
	result.ProducedImage = result.ScreenshotErr == nil
	return result
}

func browserPassAttemptFailure(attempts ...workModeURLBrowserPassAttempt) error {
	parts := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		details := make([]string, 0, 4)
		if attempt.RunErr != nil {
			details = append(details, "process error: "+attempt.RunErr.Error())
		}
		if !attempt.ProducedDOM {
			details = append(details, "browser did not return rendered DOM")
		}
		if attempt.ScreenshotErr != nil {
			details = append(details, "screenshot check: "+attempt.ScreenshotErr.Error())
		}
		if attempt.Log != "" {
			details = append(details, "browser output: "+attempt.Log)
		}
		if len(details) == 0 {
			details = append(details, "browser did not complete the combined DOM and screenshot capture")
		}
		parts = append(parts, attempt.Mode+" headless attempt: "+strings.Join(details, "; "))
	}
	return errors.New("browser exited without completing a single-pass webpage capture. " + strings.Join(parts, " | "))
}

func (a *App) workModeWebshotWorkspace(projectName string) (workModeWebshotWorkspace, error) {
	projectName = strings.TrimSpace(projectName)
	if !isValidProjectName(projectName) {
		return workModeWebshotWorkspace{}, errors.New("invalid project for webpage screenshot capture")
	}
	relDir := filepath.Join(filepath.FromSlash(workModeWebshotRootDir), projectName)
	fullDir, err := safeJoin(a.cfg.WorkRoot, relDir)
	if err != nil {
		return workModeWebshotWorkspace{}, err
	}
	fullDir, err = filepath.Abs(fullDir)
	if err != nil {
		return workModeWebshotWorkspace{}, err
	}
	if err := os.MkdirAll(fullDir, 0o755); err != nil {
		return workModeWebshotWorkspace{}, err
	}
	return workModeWebshotWorkspace{
		Directory:      fullDir,
		WorkPathPrefix: filepath.ToSlash(relDir),
		Persistent:     true,
		Logf: func(format string, args ...any) {
			a.logf("system", "info", format, args...)
		},
	}, nil
}

func workModeWebshotRoot(workRoot string) (string, error) {
	root, err := safeJoin(workRoot, filepath.FromSlash(workModeWebshotRootDir))
	if err != nil {
		return "", err
	}
	return filepath.Abs(root)
}

func resetWorkModeWebshotWorkspace(workRoot string) error {
	root, err := workModeWebshotRoot(workRoot)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	return os.MkdirAll(root, 0o755)
}

func (a *App) clearWorkModeWebshots(reason string) {
	if err := resetWorkModeWebshotWorkspace(a.cfg.WorkRoot); err != nil {
		a.logf("system", "warn", "Could not clear AgentGO webshots during %s: %v", strings.TrimSpace(reason), err)
		return
	}
	a.logf("system", "info", "Cleared AgentGO webshots during %s", strings.TrimSpace(reason))
}

func captureWorkModeURLScreenshot(ctx context.Context, rawURL string) (workModeURLCaptureImage, error) {
	tmpDir, err := os.MkdirTemp("", "agentgo-shot-")
	if err != nil {
		return workModeURLCaptureImage{}, err
	}
	defer os.RemoveAll(tmpDir)
	return captureWorkModeURLScreenshotInWorkspace(ctx, rawURL, workModeWebshotWorkspace{Directory: tmpDir})
}

func captureWorkModeURLScreenshotInWorkspace(ctx context.Context, rawURL string, workspace workModeWebshotWorkspace) (workModeURLCaptureImage, error) {
	browser, err := findAgentGOBrowserExecutable()
	if err != nil {
		return workModeURLCaptureImage{}, err
	}
	workspace.Directory = strings.TrimSpace(workspace.Directory)
	if workspace.Directory == "" {
		return workModeURLCaptureImage{}, errors.New("webpage screenshot workspace is not configured")
	}
	workspace.Directory, err = filepath.Abs(workspace.Directory)
	if err != nil {
		return workModeURLCaptureImage{}, err
	}
	if err := os.MkdirAll(workspace.Directory, 0o755); err != nil {
		return workModeURLCaptureImage{}, err
	}
	workModeWebshotCaptureMu.Lock()
	defer workModeWebshotCaptureMu.Unlock()
	storedName, outputPath, err := nextWorkModeWebshotPath(workspace.Directory, workspace.Timestamp, workspace.Index, time.Now())
	if err != nil {
		return workModeURLCaptureImage{}, err
	}
	workDir, err := os.MkdirTemp(workspace.Directory, "browser-work-")
	if err != nil {
		return workModeURLCaptureImage{}, err
	}
	defer os.RemoveAll(workDir)
	retainOutput := false
	defer func() {
		if !retainOutput {
			_ = os.Remove(outputPath)
		}
	}()
	if workspace.Logf != nil {
		workspace.Logf("Capturing Work Mode webshot with browser %s using AgentGO work/tmp workspace %s", browser, outputPath)
	}
	newModeResult := runWorkModeURLScreenshotAttempt(ctx, browser, rawURL, workDir, outputPath, "--headless=new", "new")
	if !newModeResult.ProducedFile && ctx.Err() == nil {
		_ = os.Remove(outputPath)
		legacyResult := runWorkModeURLScreenshotAttempt(ctx, browser, rawURL, workDir, outputPath, "--headless", "legacy")
		if !legacyResult.ProducedFile {
			_ = os.Remove(outputPath)
			return workModeURLCaptureImage{}, screenshotAttemptFailure(newModeResult, legacyResult)
		}
	} else if !newModeResult.ProducedFile {
		_ = os.Remove(outputPath)
		return workModeURLCaptureImage{}, screenshotAttemptFailure(newModeResult)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return workModeURLCaptureImage{}, fmt.Errorf("browser produced a screenshot path that could not be read: %w", err)
	}
	compressed, err := compressCapturedImage(data, workModeURLCaptureMaxImageBytes)
	if err != nil {
		return workModeURLCaptureImage{}, err
	}
	host := safeCaptureFilePart(hostnameFromURL(rawURL))
	if host == "" {
		host = "webpage"
	}
	result := workModeURLCaptureImage{
		Name:      host + "-screenshot.jpg",
		Label:     "Webpage screenshot",
		MIMEType:  "image/jpeg",
		Data:      base64.StdEncoding.EncodeToString(compressed),
		SizeBytes: int64(len(compressed)),
	}
	if workspace.Persistent {
		result.StoredName = storedName
		result.StoredPath = filepath.ToSlash(filepath.Join(filepath.FromSlash(workspace.WorkPathPrefix), storedName))
		retainOutput = true
	}
	return result, nil
}

func normalizeWorkModeWebshotTimestamp(value string, fallback time.Time) string {
	value = strings.TrimSpace(value)
	if matched, _ := regexp.MatchString(`^\d{8}-\d{6}$`, value); matched {
		return value
	}
	return fallback.Format("20060102-150405")
}

func nextWorkModeWebshotPath(directory, requestedTimestamp string, requestedIndex int, now time.Time) (string, string, error) {
	stamp := normalizeWorkModeWebshotTimestamp(requestedTimestamp, now)
	startIndex := requestedIndex
	if startIndex < 1 {
		startIndex = 1
	}
	if startIndex > 9999 {
		startIndex = 9999
	}
	for index := startIndex; index <= 9999; index++ {
		name := fmt.Sprintf("AgentGO-webshot-%s-%d.png", stamp, index)
		path := filepath.Join(directory, name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return name, path, nil
		} else if err != nil {
			return "", "", err
		}
	}
	return "", "", errors.New("could not allocate a unique AgentGO webshot filename")
}

type workModeURLScreenshotAttempt struct {
	Mode         string
	RunErr       error
	WaitErr      error
	Log          string
	ProducedFile bool
}

func runWorkModeURLScreenshotAttempt(ctx context.Context, browser, rawURL, tmpDir, outputPath, headlessArg, modeLabel string) workModeURLScreenshotAttempt {
	result := workModeURLScreenshotAttempt{Mode: modeLabel}
	browserCtx, cancel := context.WithTimeout(ctx, workModeURLBrowserTimeout)
	defer cancel()
	args := browserCommonArgsWithHeadless(filepath.Join(tmpDir, "profile-"+modeLabel), headlessArg)
	args = append(args,
		"--window-size=1440,1200",
		"--hide-scrollbars",
		"--virtual-time-budget=4500",
		"--run-all-compositor-stages-before-draw",
		"--screenshot="+outputPath,
		rawURL,
	)
	logPath := filepath.Join(tmpDir, "browser-"+modeLabel+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		result.RunErr = err
		return result
	}
	cmd := exec.Command(browser, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	result.RunErr = runBrowserCommand(browserCtx, cmd)
	_ = logFile.Close()
	detailBytes, _ := os.ReadFile(logPath)
	result.Log = strings.TrimSpace(string(detailBytes))
	if result.RunErr != nil {
		if info, statErr := os.Stat(outputPath); statErr == nil && !info.IsDir() && info.Size() > 0 {
			result.WaitErr = waitForStableNonEmptyFile(ctx, outputPath, workModeURLScreenshotFileWait, workModeURLScreenshotPollInterval)
			result.ProducedFile = result.WaitErr == nil
		}
		return result
	}
	result.WaitErr = waitForStableNonEmptyFile(ctx, outputPath, workModeURLScreenshotFileWait, workModeURLScreenshotPollInterval)
	result.ProducedFile = result.WaitErr == nil
	return result
}

func waitForStableNonEmptyFile(ctx context.Context, path string, timeout, interval time.Duration) error {
	if timeout <= 0 {
		timeout = time.Second
	}
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var previousSize int64 = -1
	stableChecks := 0
	check := func() bool {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Size() <= 0 {
			previousSize = -1
			stableChecks = 0
			return false
		}
		if info.Size() == previousSize {
			stableChecks++
		} else {
			previousSize = info.Size()
			stableChecks = 1
		}
		return stableChecks >= 2
	}
	if check() {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("browser exited without producing a stable nonempty screenshot")
		case <-ticker.C:
			if check() {
				return nil
			}
		}
	}
}

func screenshotAttemptFailure(attempts ...workModeURLScreenshotAttempt) error {
	parts := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		details := make([]string, 0, 3)
		if attempt.RunErr != nil {
			details = append(details, "process error: "+attempt.RunErr.Error())
		}
		if attempt.WaitErr != nil {
			details = append(details, "file check: "+attempt.WaitErr.Error())
		}
		if attempt.Log != "" {
			details = append(details, "browser output: "+attempt.Log)
		}
		if len(details) == 0 {
			details = append(details, "browser exited without creating the screenshot file")
		}
		parts = append(parts, attempt.Mode+" headless attempt: "+strings.Join(details, "; "))
	}
	return errors.New("browser exited without producing a screenshot. " + strings.Join(parts, " | "))
}

func browserCommonArgs(profileDir string) []string {
	return browserCommonArgsWithHeadless(profileDir, "--headless=new")
}

func browserCommonArgsForExecutable(browser, profileDir string) []string {
	return browserCommonArgsWithHeadlessForExecutable(browser, profileDir, "--headless=new")
}

func browserCommonArgsWithHeadless(profileDir, headlessArg string) []string {
	return browserCommonArgsWithHeadlessForExecutable("", profileDir, headlessArg)
}

func browserCommonArgsWithHeadlessForExecutable(browser, profileDir, headlessArg string) []string {
	if strings.TrimSpace(headlessArg) == "" {
		headlessArg = "--headless=new"
	}
	args := []string{
		headlessArg,
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--disable-crash-reporter",
		"--disable-breakpad",
		"--no-first-run",
		"--no-default-browser-check",
		"--lang=en-US",
		"--user-data-dir=" + profileDir,
	}
	if userAgent := versionMatchedBrowserUserAgent(browser); userAgent != "" {
		args = append(args, "--user-agent="+userAgent)
	}
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		args = append(args, "--no-sandbox")
	}
	return args
}

func versionMatchedBrowserUserAgent(browser string) string {
	browser = strings.TrimSpace(browser)
	if browser == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, browser, "--version").CombinedOutput()
	if err != nil || len(output) == 0 {
		return ""
	}
	versionMatch := regexp.MustCompile(`(?i)(?:chrome|chromium|edge)[^0-9]*([0-9]+)(?:\.[0-9]+){0,3}`).FindStringSubmatch(string(output))
	if len(versionMatch) < 2 {
		return ""
	}
	major := versionMatch[1]
	platform := "X11; Linux x86_64"
	switch runtime.GOOS {
	case "windows":
		platform = "Windows NT 10.0; Win64; x64"
	case "darwin":
		platform = "Macintosh; Intel Mac OS X 10_15_7"
	}
	userAgent := "Mozilla/5.0 (" + platform + ") AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + major + ".0.0.0 Safari/537.36"
	browserIdentity := strings.ToLower(browser + " " + string(output))
	if strings.Contains(browserIdentity, "edge") || strings.Contains(browserIdentity, "msedge") {
		userAgent += " Edg/" + major + ".0.0.0"
	}
	return userAgent
}

func findAgentGOBrowserExecutable() (string, error) {
	for _, value := range []string{os.Getenv("AGENTGO_BROWSER_PATH"), os.Getenv("CHROME_PATH"), os.Getenv("EDGE_PATH")} {
		if value = strings.TrimSpace(value); value != "" {
			if info, err := os.Stat(value); err == nil && !info.IsDir() {
				return value, nil
			}
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "microsoft-edge", "msedge"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	candidates := []string{}
	if runtime.GOOS == "windows" {
		for _, root := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if root == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(root, "Microsoft", "Edge", "Application", "msedge.exe"),
			)
		}
	} else if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("Chrome, Edge, or Chromium was not found; set AGENTGO_BROWSER_PATH to enable rendered screenshots")
}

func compressCapturedImage(data []byte, maxBytes int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("captured image format could not be decoded")
	}
	for _, maxDimension := range []int{1200, 1000, 820, 680} {
		resized := resizeImageNearest(img, maxDimension)
		for _, quality := range []int{78, 68, 58, 48, 38} {
			var out bytes.Buffer
			if err := jpeg.Encode(&out, resized, &jpeg.Options{Quality: quality}); err != nil {
				return nil, err
			}
			if out.Len() <= maxBytes {
				return out.Bytes(), nil
			}
		}
	}
	return nil, fmt.Errorf("captured image could not be compressed below %d KB", maxBytes/1024)
}

func resizeImageNearest(src image.Image, maxDimension int) image.Image {
	bounds := src.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= maxDimension && height <= maxDimension {
		return flattenImageOnWhite(src)
	}
	scale := float64(maxDimension) / float64(maxInt(width, height))
	targetWidth := maxInt(1, int(float64(width)*scale))
	targetHeight := maxInt(1, int(float64(height)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := 0; y < targetHeight; y++ {
		sy := bounds.Min.Y + y*height/targetHeight
		for x := 0; x < targetWidth; x++ {
			sx := bounds.Min.X + x*width/targetWidth
			dst.Set(x, y, blendOnWhite(src.At(sx, sy)))
		}
	}
	return dst
}

func flattenImageOnWhite(src image.Image) image.Image {
	bounds := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := 0; y < bounds.Dy(); y++ {
		for x := 0; x < bounds.Dx(); x++ {
			dst.Set(x, y, blendOnWhite(src.At(bounds.Min.X+x, bounds.Min.Y+y)))
		}
	}
	return dst
}

func blendOnWhite(value color.Color) color.Color {
	r, g, b, a := value.RGBA()
	if a == 0xffff {
		return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}
	}
	alpha := float64(a) / 65535.0
	blend := func(component uint32) uint8 {
		v := float64(component>>8)*alpha + 255*(1-alpha)
		if v < 0 {
			v = 0
		}
		if v > 255 {
			v = 255
		}
		return uint8(v)
	}
	return color.RGBA{blend(r), blend(g), blend(b), 255}
}

func isYouTubeURL(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be"
}

func youtubeVideoID(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	if host == "youtu.be" {
		return strings.Trim(strings.Split(strings.Trim(parsed.Path, "/"), "/")[0], " ")
	}
	if id := strings.TrimSpace(parsed.Query().Get("v")); id != "" {
		return id
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) >= 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func captureYouTubeTranscript(ctx context.Context, pageHTML []byte) (string, error) {
	tracksRaw, err := extractJSONValueAfterMarker(pageHTML, `"captionTracks":`)
	if err != nil {
		tracksRaw, err = extractJSONValueAfterMarker(pageHTML, `captionTracks`)
		if err != nil {
			return "", errors.New("no public caption track was found")
		}
	}
	var tracks []struct {
		BaseURL      string `json:"baseUrl"`
		LanguageCode string `json:"languageCode"`
		Kind         string `json:"kind"`
		Name         struct {
			SimpleText string `json:"simpleText"`
		} `json:"name"`
	}
	if err := json.Unmarshal(tracksRaw, &tracks); err != nil || len(tracks) == 0 {
		return "", errors.New("caption track data could not be decoded")
	}
	sort.SliceStable(tracks, func(i, j int) bool {
		iEnglish := strings.HasPrefix(strings.ToLower(tracks[i].LanguageCode), "en")
		jEnglish := strings.HasPrefix(strings.ToLower(tracks[j].LanguageCode), "en")
		if iEnglish != jEnglish {
			return iEnglish
		}
		iManual := !strings.EqualFold(tracks[i].Kind, "asr")
		jManual := !strings.EqualFold(tracks[j].Kind, "asr")
		return iManual && !jManual
	})
	captionURL := strings.TrimSpace(tracks[0].BaseURL)
	if captionURL == "" {
		return "", errors.New("caption track did not include a download URL")
	}
	parsed, err := url.Parse(captionURL)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("fmt", "json3")
	parsed.RawQuery = query.Encode()
	resource, err := fetchWorkModeURL(ctx, parsed.String(), 5_000_000)
	if err != nil {
		return "", err
	}
	if transcript, err := parseYouTubeJSON3Transcript(resource.Body); err == nil && strings.TrimSpace(transcript) != "" {
		return transcript, nil
	}
	return parseYouTubeXMLTranscript(resource.Body)
}

func parseYouTubeJSON3Transcript(data []byte) (string, error) {
	var payload struct {
		Events []struct {
			StartMS int64 `json:"tStartMs"`
			Segs    []struct {
				Text string `json:"utf8"`
			} `json:"segs"`
		} `json:"events"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	lines := []string{}
	for _, event := range payload.Events {
		parts := []string{}
		for _, segment := range event.Segs {
			if text := compactWhitespace(html.UnescapeString(segment.Text)); text != "" {
				parts = append(parts, text)
			}
		}
		text := compactWhitespace(strings.Join(parts, " "))
		if text == "" {
			continue
		}
		line := fmt.Sprintf("[%s] %s", formatTranscriptTime(event.StartMS), text)
		if len(lines) == 0 || lines[len(lines)-1] != line {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "", errors.New("caption track was empty")
	}
	return strings.Join(lines, "\n"), nil
}

func parseYouTubeXMLTranscript(data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = false
	lines := []string{}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		start, ok := token.(xml.StartElement)
		if !ok || strings.ToLower(start.Name.Local) != "text" {
			continue
		}
		startSeconds := 0.0
		for _, attr := range start.Attr {
			if attr.Name.Local == "start" {
				startSeconds, _ = strconv.ParseFloat(attr.Value, 64)
			}
		}
		var text string
		if err := decoder.DecodeElement(&text, &start); err != nil {
			continue
		}
		text = compactWhitespace(html.UnescapeString(text))
		if text != "" {
			lines = append(lines, fmt.Sprintf("[%s] %s", formatTranscriptTime(int64(startSeconds*1000)), text))
		}
	}
	if len(lines) == 0 {
		return "", errors.New("caption track was empty")
	}
	return strings.Join(lines, "\n"), nil
}

func formatTranscriptTime(milliseconds int64) string {
	seconds := milliseconds / 1000
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	if hours > 0 {
		return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
	}
	return fmt.Sprintf("%02d:%02d", minutes, secs)
}

func captureYouTubeVisualSamples(ctx context.Context, pageHTML []byte, rawURL string) ([]workModeURLCaptureImage, []string, error) {
	warnings := []string{}
	storyboardRaw, err := extractJSONValueAfterMarker(pageHTML, `"storyboards":`)
	if err == nil {
		var storyboards any
		if json.Unmarshal(storyboardRaw, &storyboards) == nil {
			if spec := findNestedString(storyboards, "spec"); spec != "" {
				if urls := youtubeStoryboardURLs(spec); len(urls) > 0 {
					images := []workModeURLCaptureImage{}
					for index, imageURL := range urls {
						resource, fetchErr := fetchWorkModeURL(ctx, imageURL, 3_000_000)
						if fetchErr != nil {
							continue
						}
						compressed, compressErr := compressCapturedImage(resource.Body, workModeURLCaptureMaxImageBytes)
						if compressErr != nil {
							continue
						}
						images = append(images, workModeURLCaptureImage{
							Name:      fmt.Sprintf("youtube-visual-sample-%d.jpg", index+1),
							Label:     fmt.Sprintf("YouTube storyboard sample %d", index+1),
							MIMEType:  "image/jpeg",
							Data:      base64.StdEncoding.EncodeToString(compressed),
							SizeBytes: int64(len(compressed)),
						})
					}
					if len(images) > 0 {
						return images, warnings, nil
					}
				}
			}
		}
	}
	videoID := youtubeVideoID(rawURL)
	if videoID == "" {
		return nil, warnings, errors.New("video ID could not be identified")
	}
	thumbnailURL := "https://i.ytimg.com/vi/" + url.PathEscape(videoID) + "/hqdefault.jpg"
	resource, err := fetchWorkModeURL(ctx, thumbnailURL, 3_000_000)
	if err != nil {
		return nil, warnings, err
	}
	compressed, err := compressCapturedImage(resource.Body, workModeURLCaptureMaxImageBytes)
	if err != nil {
		return nil, warnings, err
	}
	warnings = append(warnings, "Timed YouTube storyboard samples were unavailable, so AgentGO attached the public video thumbnail instead.")
	return []workModeURLCaptureImage{{
		Name:      "youtube-thumbnail.jpg",
		Label:     "YouTube video thumbnail",
		MIMEType:  "image/jpeg",
		Data:      base64.StdEncoding.EncodeToString(compressed),
		SizeBytes: int64(len(compressed)),
	}}, warnings, nil
}

func youtubeStoryboardURLs(spec string) []string {
	parts := strings.Split(spec, "|")
	if len(parts) < 2 {
		return nil
	}
	base := parts[0]
	levelIndex := -1
	var fields []string
	for index := len(parts) - 1; index >= 1; index-- {
		candidate := strings.Split(parts[index], "#")
		if len(candidate) < 6 {
			continue
		}
		count, errCount := strconv.Atoi(candidate[2])
		cols, errCols := strconv.Atoi(candidate[3])
		rows, errRows := strconv.Atoi(candidate[4])
		if errCount == nil && errCols == nil && errRows == nil && count > 0 && cols > 0 && rows > 0 {
			levelIndex = index - 1
			fields = candidate
			break
		}
	}
	if levelIndex < 0 {
		return nil
	}
	count, _ := strconv.Atoi(fields[2])
	cols, _ := strconv.Atoi(fields[3])
	rows, _ := strconv.Atoi(fields[4])
	tileCount := (count + cols*rows - 1) / (cols * rows)
	if tileCount <= 0 {
		return nil
	}
	indices := []int{0}
	if tileCount > 2 {
		indices = append(indices, tileCount/2)
	}
	if tileCount > 1 {
		indices = append(indices, tileCount-1)
	}
	seen := map[int]bool{}
	urls := []string{}
	for _, index := range indices {
		if seen[index] {
			continue
		}
		seen[index] = true
		value := strings.ReplaceAll(base, "$L", strconv.Itoa(levelIndex))
		value = strings.ReplaceAll(value, "$N", strconv.Itoa(index))
		if len(fields) > 7 && fields[7] != "" && !strings.Contains(value, "sigh=") {
			separator := "?"
			if strings.Contains(value, "?") {
				separator = "&"
			}
			value += separator + "sigh=" + url.QueryEscape(fields[7])
		}
		urls = append(urls, value)
	}
	return urls
}

func findNestedString(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if direct, ok := typed[key].(string); ok && strings.TrimSpace(direct) != "" {
			return direct
		}
		for _, child := range typed {
			if found := findNestedString(child, key); found != "" {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findNestedString(child, key); found != "" {
				return found
			}
		}
	}
	return ""
}

func extractJSONValueAfterMarker(data []byte, marker string) ([]byte, error) {
	text := string(data)
	index := strings.Index(text, marker)
	if index < 0 {
		return nil, errors.New("marker not found")
	}
	index += len(marker)
	for index < len(text) && (text[index] == ' ' || text[index] == '\t' || text[index] == '\r' || text[index] == '\n' || text[index] == ':') {
		index++
	}
	if index >= len(text) {
		return nil, errors.New("JSON value not found")
	}
	start := text[index]
	if start != '[' && start != '{' && start != '"' {
		return nil, errors.New("unsupported JSON value")
	}
	if start == '"' {
		inEscape := false
		for i := index + 1; i < len(text); i++ {
			if inEscape {
				inEscape = false
				continue
			}
			if text[i] == '\\' {
				inEscape = true
				continue
			}
			if text[i] == '"' {
				return []byte(text[index : i+1]), nil
			}
		}
		return nil, errors.New("unterminated JSON string")
	}
	open := start
	close := byte('}')
	if open == '[' {
		close = ']'
	}
	depth := 0
	inString := false
	escaped := false
	for i := index; i < len(text); i++ {
		char := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == '"' {
				inString = false
			}
			continue
		}
		if char == '"' {
			inString = true
			continue
		}
		if char == open {
			depth++
		} else if char == close {
			depth--
			if depth == 0 {
				return []byte(text[index : i+1]), nil
			}
		}
	}
	return nil, errors.New("unterminated JSON value")
}

func buildWorkModeURLCaptureMessage(captures []workModeURLCaptureInput, profile adapters.TransportProfile) (adapters.Message, []string, error) {
	message := adapters.Message{Role: "user", Parts: []adapters.Part{}}
	if len(captures) == 0 {
		return message, nil, nil
	}
	if len(captures) > workModeURLCaptureMaxURLs {
		return message, nil, fmt.Errorf("URL capture limit exceeded: %d > %d", len(captures), workModeURLCaptureMaxURLs)
	}
	warnings := []string{}
	totalText := 0
	imageCount := 0
	imageBytes := 0
	for index, capture := range captures {
		requested := strings.TrimSpace(capture.RequestedURL)
		if requested == "" {
			continue
		}
		parts := []string{
			fmt.Sprintf("WEB URL CAPTURE %d", index+1),
			"Requested URL: " + requested,
		}
		if finalURL := strings.TrimSpace(capture.FinalURL); finalURL != "" && finalURL != requested {
			parts = append(parts, "Final URL: "+finalURL)
		}
		if capture.Kind != "" {
			parts = append(parts, "Type: "+capture.Kind)
		}
		metadataLines := []string{}
		appendMetadataLine := func(label, value string) {
			if value = strings.TrimSpace(value); value != "" {
				metadataLines = append(metadataLines, label+": "+value)
			}
		}
		appendMetadataLine("Title", capture.Metadata.Title)
		appendMetadataLine("Description", capture.Metadata.Description)
		appendMetadataLine("Site", capture.Metadata.SiteName)
		appendMetadataLine("Author", capture.Metadata.Author)
		appendMetadataLine("Language", capture.Metadata.Language)
		appendMetadataLine("Published", capture.Metadata.Published)
		appendMetadataLine("Canonical URL", capture.Metadata.CanonicalURL)
		appendMetadataLine("Content type", capture.Metadata.ContentType)
		appendMetadataLine("Retrieved", capture.Metadata.RetrievedAt)
		if len(metadataLines) > 0 {
			parts = append(parts, "", "PAGE METADATA:", strings.Join(metadataLines, "\n"))
		}
		if text := strings.TrimSpace(capture.PageText); text != "" {
			text = truncateUTF8Bytes(text, workModeURLCaptureMaxTextBytes)
			parts = append(parts, "", "CLEANED READABLE PAGE TEXT:", text)
		}
		if transcript := strings.TrimSpace(capture.Transcript); transcript != "" {
			transcript = truncateUTF8Bytes(transcript, workModeURLCaptureMaxTextBytes)
			parts = append(parts, "", "YOUTUBE TRANSCRIPT:", transcript)
		}
		if len(capture.Errors) > 0 {
			parts = append(parts, "", "CAPTURE NOTES:", strings.Join(capture.Errors, "\n"))
		}
		textPart := strings.TrimSpace(strings.Join(parts, "\n"))
		totalText += len([]byte(textPart))
		if totalText > workModeURLCaptureMaxTotalText {
			return message, warnings, fmt.Errorf("combined URL text exceeds %d KB", workModeURLCaptureMaxTotalText/1024)
		}
		message.Parts = append(message.Parts, buildTextPart(textPart))
		for _, imageData := range capture.Images {
			if imageCount >= workModeURLCaptureMaxImages {
				warnings = append(warnings, "AgentGO skipped additional URL images after the six-image URL capture limit.")
				break
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(imageData.Data))
			if err != nil {
				warnings = append(warnings, "AgentGO skipped an invalid URL capture image: "+imageData.Name)
				continue
			}
			if len(decoded) > workModeURLCaptureMaxImageBytes || imageBytes+len(decoded) > workModeURLCaptureMaxTotalImage {
				warnings = append(warnings, "AgentGO skipped a URL capture image that exceeded the URL image budget: "+imageData.Name)
				continue
			}
			if !profile.AdapterCapabilities.SupportsImageIn || !profile.EffectiveCapabilities.SupportsImageIn {
				warnings = append(warnings, "The selected Builder does not accept images, so AgentGO omitted captured screenshots and visual samples.")
				break
			}
			name := filepath.Base(strings.TrimSpace(imageData.Name))
			if name == "." || name == "" {
				name = fmt.Sprintf("url-capture-%d.jpg", imageCount+1)
			}
			label := strings.TrimSpace(imageData.Label)
			if label == "" {
				label = "URL capture image"
			}
			message.Parts = append(message.Parts,
				buildTextPart(label+": "+name+"\n(Attached below as user-selected URL context.)"),
				adapters.Part{Kind: "image", Name: name, RelPath: "url-capture/" + name, MIMEType: firstNonEmpty(strings.TrimSpace(imageData.MIMEType), "image/jpeg"), Data: decoded},
			)
			imageCount++
			imageBytes += len(decoded)
		}
	}
	return message, uniqueNonEmptyStrings(warnings), nil
}

func workModeURLCaptureWarningMessage(warnings []string) string {
	warnings = uniqueNonEmptyStrings(warnings)
	if len(warnings) == 0 {
		return ""
	}
	return "URL capture note:\n- " + strings.Join(warnings, "\n- ")
}

func workModeURLRiskWarnings(rawURL string) []string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return nil
	}
	warnings := []string{}
	if parsed.Scheme == "http" {
		warnings = append(warnings, "This URL uses an unencrypted HTTP connection.")
	}
	if parsed.User != nil {
		warnings = append(warnings, "This URL contains embedded credentials.")
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if ip := net.ParseIP(host); ip != nil {
		warnings = append(warnings, "This URL uses a numeric IP address.")
		if ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			warnings = append(warnings, "This URL points to a local or private-network address.")
		}
	} else if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		warnings = append(warnings, "This URL points to a local-network name.")
	}
	if port := parsed.Port(); port != "" {
		standard := (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443")
		if !standard {
			warnings = append(warnings, "This URL uses a non-standard network port ("+port+").")
		}
	}
	return warnings
}

func redirectHostWarning(requestedURL, finalURL string) string {
	requested, err1 := url.Parse(requestedURL)
	final, err2 := url.Parse(finalURL)
	if err1 != nil || err2 != nil {
		return ""
	}
	requestedHost := strings.ToLower(strings.TrimPrefix(requested.Hostname(), "www."))
	finalHost := strings.ToLower(strings.TrimPrefix(final.Hostname(), "www."))
	if requestedHost != "" && finalHost != "" && requestedHost != finalHost {
		return "This URL redirected from " + requestedHost + " to " + finalHost + "."
	}
	return ""
}

func resolveWebURL(baseURL, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if baseURL != "" {
		if base, baseErr := url.Parse(baseURL); baseErr == nil {
			parsed = base.ResolveReference(parsed)
		}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ""
	}
	return parsed.String()
}

func hostnameFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
}

var captureFilePartPattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safeCaptureFilePart(value string) string {
	value = strings.Trim(captureFilePartPattern.ReplaceAllString(value, "-"), "-._")
	if len(value) > 80 {
		value = value[:80]
	}
	return value
}

func truncateUTF8Bytes(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if maxBytes <= 0 || len([]byte(value)) <= maxBytes {
		return value
	}
	data := []byte(value)
	data = data[:maxBytes]
	for len(data) > 0 && (data[len(data)-1]&0xC0) == 0x80 {
		data = data[:len(data)-1]
	}
	return strings.TrimSpace(string(data)) + "\n\n[AgentGO truncated this capture to fit the URL context limit.]"
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func containsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func uniqueNonEmptyStrings(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func shortError(err error) string {
	if err == nil {
		return "unknown error"
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 240 {
		value = value[:240] + "…"
	}
	return value
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
