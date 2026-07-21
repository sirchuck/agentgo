package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"agentgo/adapters"
)

func writeFakeBrowserScript(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake browser shell script test is Unix-only")
	}
	path := filepath.Join(t.TempDir(), "fake-browser.sh")
	script := "#!/bin/sh\nset -eu\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake browser: %v", err)
	}
	return path
}

func withFastScreenshotPolling(t *testing.T) {
	t.Helper()
	previousWait := workModeURLScreenshotFileWait
	previousInterval := workModeURLScreenshotPollInterval
	workModeURLScreenshotFileWait = 250 * time.Millisecond
	workModeURLScreenshotPollInterval = 15 * time.Millisecond
	t.Cleanup(func() {
		workModeURLScreenshotFileWait = previousWait
		workModeURLScreenshotPollInterval = previousInterval
	})
}

func TestHTMLCaptureExtractsMetadataAndReadableText(t *testing.T) {
	htmlDoc := `<!doctype html><html lang="en-US"><head>
<title>Fallback title</title>
<meta property="og:title" content="HopeSmart Example">
<meta name="description" content="A useful page description.">
<link rel="canonical" href="/welcome">
</head><body>
<nav><a href="/home">Home</a><a href="/login">Login</a></nav>
<main id="primary-content"><article>
<h1>Welcome to HopeSmart</h1>
<p>This paragraph explains the public purpose of the website in enough detail to be useful.</p>
<ul><li>Choose a legislator.</li><li>Read the current petition.</li></ul>
<p hidden>This hidden sentence must not be sent.</p>
<a href="/learn-more">Learn more</a>
</article></main>
<footer>Copyright boilerplate</footer>
</body></html>`
	doc, err := parseHTMLCaptureDocument([]byte(htmlDoc), "https://hopesmart.com/", "text/html")
	if err != nil {
		t.Fatalf("parseHTMLCaptureDocument: %v", err)
	}
	if doc.Metadata.Title != "HopeSmart Example" {
		t.Fatalf("title = %q", doc.Metadata.Title)
	}
	if doc.Metadata.CanonicalURL != "https://hopesmart.com/welcome" {
		t.Fatalf("canonical URL = %q", doc.Metadata.CanonicalURL)
	}
	text := extractReadableHTMLText(doc.Root, "https://hopesmart.com/", 100_000)
	for _, expected := range []string{"# Welcome to HopeSmart", "Choose a legislator", "Learn more → https://hopesmart.com/learn-more"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("readable text missing %q:\n%s", expected, text)
		}
	}
	for _, unwanted := range []string{"hidden sentence", "Copyright boilerplate"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("readable text unexpectedly contains %q:\n%s", unwanted, text)
		}
	}
}

func TestWorkModeURLRiskWarningsAreNonBlockingDescriptions(t *testing.T) {
	warnings := workModeURLRiskWarnings("http://user:pass@127.0.0.1:8080/private")
	joined := strings.Join(warnings, "\n")
	for _, expected := range []string{"unencrypted", "embedded credentials", "numeric IP", "local or private-network", "non-standard network port"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("warning list missing %q: %v", expected, warnings)
		}
	}
}

func TestYouTubeJSON3Transcript(t *testing.T) {
	payload := `{"events":[{"tStartMs":0,"segs":[{"utf8":"Hello "},{"utf8":"world"}]},{"tStartMs":65000,"segs":[{"utf8":"Second line"}]}]}`
	transcript, err := parseYouTubeJSON3Transcript([]byte(payload))
	if err != nil {
		t.Fatalf("parseYouTubeJSON3Transcript: %v", err)
	}
	if !strings.Contains(transcript, "[00:00] Hello world") || !strings.Contains(transcript, "[01:05] Second line") {
		t.Fatalf("unexpected transcript: %s", transcript)
	}
}

func TestExtractJSONValueAfterMarker(t *testing.T) {
	page := []byte(`<script>var x={"captionTracks":[{"baseUrl":"https://example.com/captions?a=1&b=2","languageCode":"en"}],"after":true};</script>`)
	raw, err := extractJSONValueAfterMarker(page, `"captionTracks":`)
	if err != nil {
		t.Fatalf("extractJSONValueAfterMarker: %v", err)
	}
	if !strings.HasPrefix(string(raw), "[") || !strings.Contains(string(raw), "languageCode") {
		t.Fatalf("unexpected JSON value: %s", raw)
	}
}

func TestStoryboardURLsSelectFirstMiddleLastTiles(t *testing.T) {
	spec := "https://i.ytimg.com/sb/video/storyboard3_L$L/$N.jpg?sqp=x|160#90#250#5#5#1000#default#sig"
	urls := youtubeStoryboardURLs(spec)
	if len(urls) != 3 {
		t.Fatalf("expected 3 storyboard URLs, got %d: %v", len(urls), urls)
	}
	if !strings.Contains(urls[0], "/0.jpg") || !strings.Contains(urls[1], "/5.jpg") || !strings.Contains(urls[2], "/9.jpg") {
		t.Fatalf("unexpected storyboard URLs: %v", urls)
	}
}

func TestBuildURLCaptureMessageIncludesTextAndImage(t *testing.T) {
	profile := adapters.TransportProfile{AdapterCapabilities: adapters.ModelCapabilities{SupportsImageIn: true}, EffectiveCapabilities: adapters.ModelCapabilities{SupportsImageIn: true}}
	imageData := base64.StdEncoding.EncodeToString([]byte("fake-jpeg-data"))
	capture := workModeURLCaptureInput{
		RequestedURL: "https://example.com/article",
		FinalURL:     "https://example.com/article",
		Kind:         "webpage",
		Metadata:     workModeURLCaptureMetadata{Title: "Example Article"},
		PageText:     "Readable article content.",
		Images:       []workModeURLCaptureImage{{Name: "example.jpg", MIMEType: "image/jpeg", Data: imageData, SizeBytes: 14}},
	}
	message, _, err := buildWorkModeURLCaptureMessage([]workModeURLCaptureInput{capture}, profile)
	if err != nil {
		t.Fatalf("buildWorkModeURLCaptureMessage: %v", err)
	}
	if len(message.Parts) < 3 {
		t.Fatalf("expected text plus image parts, got %d", len(message.Parts))
	}
}

func TestCaptureWorkModeURLFromHTMLServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><title>Capture Test</title><meta name="description" content="Test metadata"></head><body><nav>Skip navigation</nav><main><h1>Capture Test</h1><p>This is the main readable page content that AgentGO should collect for the AI. It includes enough explanatory text to represent a realistic article paragraph, exercise the readability-style extraction path, and avoid requiring a headless browser fallback during this deterministic unit test. The cleaned result should preserve this useful material while excluding navigation, scripts, and other boilerplate.</p></main><script>doNotSend()</script></body></html>`))
	}))
	defer server.Close()

	capture := captureWorkModeURL(context.Background(), workModeURLCaptureRequest{
		URL:             server.URL,
		IncludeMetadata: true,
		IncludeText:     true,
	})
	if len(capture.Errors) != 0 {
		t.Fatalf("capture errors: %v", capture.Errors)
	}
	if capture.Metadata.Title != "Capture Test" {
		t.Fatalf("title = %q", capture.Metadata.Title)
	}
	if !strings.Contains(capture.PageText, "main readable page content") {
		t.Fatalf("page text missing main content: %s", capture.PageText)
	}
	if strings.Contains(capture.PageText, "doNotSend") || strings.Contains(capture.PageText, "Skip navigation") {
		t.Fatalf("page text included boilerplate or script: %s", capture.PageText)
	}
}

func TestCaptureWorkModeURLUsesSingleBrowserPassWhenScreenshotSelected(t *testing.T) {
	withFastScreenshotPolling(t)
	pngData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode PNG fixture: %v", err)
	}
	fixture := filepath.Join(t.TempDir(), "fixture.png")
	if err := os.WriteFile(fixture, pngData, 0o644); err != nil {
		t.Fatalf("write PNG fixture: %v", err)
	}
	calls := filepath.Join(t.TempDir(), "browser-calls.txt")
	browser := writeFakeBrowserScript(t, `
if [ "${1:-}" = "--version" ]; then
  echo "Chromium 150.0.0.0"
  exit 0
fi
output=""
target=""
for arg in "$@"; do
  case "$arg" in
    --screenshot=*) output="${arg#--screenshot=}" ;;
    http://*|https://*) target="$arg" ;;
  esac
done
printf '%s\n' "$target" >> "$FAKE_BROWSER_CALLS"
cp "$FAKE_BROWSER_PNG" "$output"
cat <<'EOF'
<!doctype html><html lang="en"><head><title>Rendered Product</title><meta name="description" content="Rendered product metadata"><link rel="canonical" href="https://example.com/products/rendered"></head><body><main><h1>Rendered Product</h1><p>This rendered product page contains enough useful visible text for AgentGO to send to the AI from the same Chromium navigation that produced the screenshot. The browser pass should be used exactly once and the lightweight HTTP fetch should not run first.</p></main></body></html>
EOF
exit 0`)
	t.Setenv("AGENTGO_BROWSER_PATH", browser)
	t.Setenv("FAKE_BROWSER_CALLS", calls)
	t.Setenv("FAKE_BROWSER_PNG", fixture)

	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte("unexpected lightweight fetch"))
	}))
	defer server.Close()

	capture := captureWorkModeURL(context.Background(), workModeURLCaptureRequest{
		URL:               server.URL + "/products/rendered",
		IncludeMetadata:   true,
		IncludeText:       true,
		IncludeScreenshot: true,
	})
	if hits != 0 {
		t.Fatalf("lightweight fetch count = %d, want 0 when screenshot is selected", hits)
	}
	if len(capture.Errors) != 0 {
		t.Fatalf("capture errors: %v", capture.Errors)
	}
	if capture.Metadata.Title != "Rendered Product" || !strings.Contains(capture.PageText, "same Chromium navigation") {
		t.Fatalf("browser DOM was not used for metadata/text: %#v\n%s", capture.Metadata, capture.PageText)
	}
	if len(capture.Images) != 1 || capture.Images[0].Data == "" {
		t.Fatalf("browser screenshot missing: %#v", capture.Images)
	}
	callData, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read browser calls: %v", err)
	}
	if got := strings.Fields(string(callData)); len(got) != 1 || got[0] != server.URL+"/products/rendered" {
		t.Fatalf("browser page navigations = %q, want one", string(callData))
	}
}

func TestCaptureWorkModeURLUsesLightweightFetchForMetadataAndText(t *testing.T) {
	hits := 0
	userAgent := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		userAgent = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Fetch Product</title><meta name="description" content="Fetched product metadata"></head><body><main><h1>Fetch Product</h1><p>This ordinary server-rendered page contains enough useful readable text for AgentGO to complete metadata and text collection using one lightweight HTTP request without starting Chromium or requesting a screenshot.</p></main></body></html>`))
	}))
	defer server.Close()

	capture := captureWorkModeURL(context.Background(), workModeURLCaptureRequest{
		URL:             server.URL + "/product",
		IncludeMetadata: true,
		IncludeText:     true,
	})
	if hits != 1 {
		t.Fatalf("lightweight fetch count = %d, want 1", hits)
	}
	if len(capture.Errors) != 0 {
		t.Fatalf("capture errors: %v", capture.Errors)
	}
	if !strings.HasPrefix(userAgent, "Mozilla/5.0") {
		t.Fatalf("lightweight fetch did not use a browser-shaped user agent: %q", userAgent)
	}
	if capture.Metadata.Title != "Fetch Product" || !strings.Contains(capture.PageText, "lightweight HTTP request") {
		t.Fatalf("unexpected capture: %#v\n%s", capture.Metadata, capture.PageText)
	}
	if len(capture.Images) != 0 {
		t.Fatalf("metadata/text-only capture unexpectedly included images: %#v", capture.Images)
	}
}

func TestCaptureWorkModeURLFallsBackToBrowserAfterBlockedFetch(t *testing.T) {
	browserCalls := filepath.Join(t.TempDir(), "browser-calls.txt")
	browser := writeFakeBrowserScript(t, `
if [ "${1:-}" = "--version" ]; then
  echo "Chromium 150.0.0.0"
  exit 0
fi
target=""
for arg in "$@"; do
  case "$arg" in http://*|https://*) target="$arg" ;; esac
done
printf '%s\n' "$target" >> "$FAKE_BROWSER_CALLS"
cat <<'EOF'
<!doctype html><html><head><title>Browser Fallback Product</title><meta name="description" content="Browser-rendered fallback metadata"></head><body><main><h1>Browser Fallback Product</h1><p>The lightweight request received a security block page, so AgentGO retried once in Chromium and collected this useful rendered page text instead of sending the block response to the AI.</p></main></body></html>
EOF
exit 0`)
	t.Setenv("AGENTGO_BROWSER_PATH", browser)
	t.Setenv("FAKE_BROWSER_CALLS", browserCalls)

	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Sorry, you have been blocked</title></head><body>You are unable to access this website. Cloudflare Ray ID: test123</body></html>`))
	}))
	defer server.Close()

	capture := captureWorkModeURL(context.Background(), workModeURLCaptureRequest{
		URL:             server.URL + "/blocked",
		IncludeMetadata: true,
		IncludeText:     true,
	})
	if hits != 1 {
		t.Fatalf("lightweight fetch count = %d, want 1", hits)
	}
	if len(capture.Errors) != 0 {
		t.Fatalf("browser fallback should recover without errors: %v", capture.Errors)
	}
	if capture.Metadata.Title != "Browser Fallback Product" || !strings.Contains(capture.PageText, "retried once in Chromium") {
		t.Fatalf("browser fallback content missing: %#v\n%s", capture.Metadata, capture.PageText)
	}
	if !strings.Contains(strings.Join(capture.Warnings, " "), "used browser rendering") {
		t.Fatalf("browser fallback warning missing: %v", capture.Warnings)
	}
	callData, err := os.ReadFile(browserCalls)
	if err != nil {
		t.Fatalf("read browser calls: %v", err)
	}
	if got := strings.Fields(string(callData)); len(got) != 1 || got[0] != server.URL+"/blocked" {
		t.Fatalf("browser fallback navigations = %q, want one", string(callData))
	}
}

func TestCaptureWorkModeURLRejectsBrowserBlockPage(t *testing.T) {
	withFastScreenshotPolling(t)
	pngData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode PNG fixture: %v", err)
	}
	fixture := filepath.Join(t.TempDir(), "fixture.png")
	if err := os.WriteFile(fixture, pngData, 0o644); err != nil {
		t.Fatalf("write PNG fixture: %v", err)
	}
	browser := writeFakeBrowserScript(t, `
if [ "${1:-}" = "--version" ]; then
  echo "Chromium 150.0.0.0"
  exit 0
fi
output=""
for arg in "$@"; do
  case "$arg" in --screenshot=*) output="${arg#--screenshot=}" ;; esac
done
cp "$FAKE_BROWSER_PNG" "$output"
cat <<'EOF'
<!doctype html><html><head><title>Sorry, you have been blocked</title></head><body><h1>Sorry, you have been blocked</h1><p>You are unable to access superhivemarket.com</p><footer>Cloudflare Ray ID: test123</footer></body></html>
EOF
exit 0`)
	t.Setenv("AGENTGO_BROWSER_PATH", browser)
	t.Setenv("FAKE_BROWSER_PNG", fixture)

	capture := captureWorkModeURL(context.Background(), workModeURLCaptureRequest{
		URL:               "https://superhivemarket.com/products/auto-rig-pro",
		IncludeMetadata:   true,
		IncludeText:       true,
		IncludeScreenshot: true,
	})
	if len(capture.Errors) == 0 || !strings.Contains(strings.Join(capture.Errors, " "), "Cloudflare block page") {
		t.Fatalf("expected clear Cloudflare block error, got %v", capture.Errors)
	}
	if capture.PageText != "" || hasMeaningfulURLCaptureMetadata(capture.Metadata) || len(capture.Images) != 0 {
		t.Fatalf("block page was treated as valid content: metadata=%#v text=%q images=%d", capture.Metadata, capture.PageText, len(capture.Images))
	}
}

func TestHTMLCaptureToleratesBrowserHTMLThatIsNotXML(t *testing.T) {
	htmlDoc := `<!doctype html><html lang=en><head><title>Loose &amp; Valid HTML</title>
<meta name=description content="A browser-valid page with loose markup">
</head><body><main><h1>Readable page</h1>
<input disabled required>
<script>if (window.innerWidth < 900) { document.body.dataset.small = true; }</script>
<p>This useful paragraph appears after JavaScript containing a less-than sign.</p>
</main></body></html>`
	doc, err := parseHTMLCaptureDocument([]byte(htmlDoc), "https://example.com/", "text/html")
	if err != nil {
		t.Fatalf("parseHTMLCaptureDocument rejected browser-valid HTML: %v", err)
	}
	if doc.Metadata.Title != "Loose & Valid HTML" {
		t.Fatalf("title = %q", doc.Metadata.Title)
	}
	text := extractReadableHTMLText(doc.Root, "https://example.com/", 100_000)
	if !strings.Contains(text, "useful paragraph appears after JavaScript") {
		t.Fatalf("readable text was truncated after script markup: %s", text)
	}
	if strings.Contains(text, "window.innerWidth") {
		t.Fatalf("script text leaked into readable content: %s", text)
	}
}

func TestWorkModeCodeBlocksDecodeEntitiesBeforeDisplayAndCopy(t *testing.T) {
	raw, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	templateText := string(raw)
	for _, required := range []string{
		"function decodeAgentGOCodeText(node)",
		"function isAgentGOCodeChangeMarker(node)",
		"String(node.getAttribute('class') || '').trim() !== 'agentgo-code-change'",
		"Array.from(node.attributes || []).every(attr => String(attr.name || '').toLowerCase() === 'class')",
		"function appendSanitizedAgentGOCodeContent(target, source)",
		"marker.className = 'agentgo-code-change'",
		"marker.textContent = child.textContent || ''",
		"decoder.innerHTML = source.replace(/<\\/textarea/gi, '&lt;/textarea')",
		"if (hasChangeMarker) appendSanitizedAgentGOCodeContent(el, node)",
		"else el.textContent = decodeAgentGOCodeText(node)",
		".work-mode-message.ai pre code .agentgo-code-change{color:#FFE27C}",
		"const text = pre ? String(pre.textContent || '') : ''",
		"const preview = content ? `<pre class=\"work-mode-file-output-pre\">${escapeHtml(content)}</pre>` : ''",
	} {
		if !strings.Contains(templateText, required) {
			t.Fatalf("Work Mode code rendering regression guard missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"hasElementChild ? (node.innerHTML || '') : (node.textContent || '')",
		"const text = pre ? String(pre.innerHTML || '') : ''",
		"marker.innerHTML",
		"sanitizeAgentGOReplyHTML(content)",
	} {
		if strings.Contains(templateText, forbidden) {
			t.Fatalf("Work Mode code rendering still contains unsafe entity-preserving path %q", forbidden)
		}
	}
}

func TestWorkModeURLCapabilitiesReportsMissingBrowser(t *testing.T) {
	t.Setenv("AGENTGO_BROWSER_PATH", "")
	t.Setenv("CHROME_PATH", "")
	t.Setenv("EDGE_PATH", "")
	t.Setenv("PATH", t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/work-mode/url/capabilities", nil)
	recorder := httptest.NewRecorder()
	var app App
	app.handleWorkModeURLCapabilities(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response workModeURLCapabilitiesResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.BrowserAvailable {
		t.Fatal("browserAvailable = true with no configured or discoverable browser")
	}
}

func TestURLReviewTemplateIncludesPlainSendAndConditionalScreenshot(t *testing.T) {
	raw, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	text := string(raw)
	for _, required := range []string{
		"Send with Selected URL Data",
		"Send Prompt as Written",
		"/api/work-mode/url/capabilities",
		"workModeURLBrowserAvailable ? [option('screenshot', 'Send Screenshot')] : []",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("URL review template missing %q", required)
		}
	}
}

func TestCurrentXAIResponsesPickerEntries(t *testing.T) {
	raw, err := os.ReadFile("model_names.json")
	if err != nil {
		t.Fatalf("read model_names.json: %v", err)
	}
	var catalog struct {
		Models []struct {
			Provider  string `json:"provider"`
			Adapter   string `json:"adapter"`
			ModelName string `json:"model_name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("parse model_names.json: %v", err)
	}
	found := map[string]bool{}
	for _, item := range catalog.Models {
		if item.Provider == "xai" && item.Adapter == "xai_responses" {
			found[item.ModelName] = true
		}
	}
	for _, required := range []string{
		"grok-4.5",
		"grok-4.5-latest",
		"grok-4.3",
		"grok-4.20-0309-reasoning",
		"grok-4.20-0309-non-reasoning",
		"grok-4.20-multi-agent-0309",
		"grok-build-0.1",
	} {
		if !found[required] {
			t.Fatalf("xAI Responses picker is missing %q", required)
		}
	}
	for _, retiredOrReplaced := range []string{"grok-3", "grok-build-latest"} {
		if found[retiredOrReplaced] {
			t.Fatalf("xAI Responses picker unexpectedly includes %q", retiredOrReplaced)
		}
	}
}

func TestBuildURLCaptureMessageWarnsWhenBuilderCannotAcceptImages(t *testing.T) {
	profile := adapters.TransportProfile{}
	imageData := base64.StdEncoding.EncodeToString([]byte("fake-jpeg-data"))
	capture := workModeURLCaptureInput{
		RequestedURL: "https://example.com/article",
		Images:       []workModeURLCaptureImage{{Name: "example.jpg", MIMEType: "image/jpeg", Data: imageData}},
	}
	message, warnings, err := buildWorkModeURLCaptureMessage([]workModeURLCaptureInput{capture}, profile)
	if err != nil {
		t.Fatalf("buildWorkModeURLCaptureMessage: %v", err)
	}
	if len(message.Parts) != 1 {
		t.Fatalf("expected only URL text context when images are unsupported, got %d parts", len(message.Parts))
	}
	note := workModeURLCaptureWarningMessage(warnings)
	if !strings.Contains(note, "does not accept images") {
		t.Fatalf("missing unsupported-image note: %q", note)
	}
}

func TestCaptureWorkModeURLScreenshotWaitsForDelayedFile(t *testing.T) {
	withFastScreenshotPolling(t)
	pngData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode PNG fixture: %v", err)
	}
	fixture := filepath.Join(t.TempDir(), "fixture.png")
	if err := os.WriteFile(fixture, pngData, 0o644); err != nil {
		t.Fatalf("write PNG fixture: %v", err)
	}
	calls := filepath.Join(t.TempDir(), "calls.txt")
	browser := writeFakeBrowserScript(t, `
mode="unknown"
output=""
for arg in "$@"; do
  [ "$arg" = "--headless=new" ] && mode="new"
  [ "$arg" = "--headless" ] && mode="legacy"
  case "$arg" in --screenshot=*) output="${arg#--screenshot=}" ;; esac
done
printf '%s\n' "$mode" >> "$FAKE_BROWSER_CALLS"
(sleep 0.06; cp "$FAKE_BROWSER_PNG" "$output") >/dev/null 2>&1 &
echo "scheduled delayed screenshot"
exit 0`)
	t.Setenv("AGENTGO_BROWSER_PATH", browser)
	t.Setenv("FAKE_BROWSER_CALLS", calls)
	t.Setenv("FAKE_BROWSER_PNG", fixture)

	imageData, err := captureWorkModeURLScreenshot(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("captureWorkModeURLScreenshot: %v", err)
	}
	if imageData.MIMEType != "image/jpeg" || imageData.Data == "" {
		t.Fatalf("unexpected screenshot result: %#v", imageData)
	}
	callData, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	if strings.TrimSpace(string(callData)) != "new" {
		t.Fatalf("expected delayed new-headless screenshot without retry, calls = %q", string(callData))
	}
}

func TestCaptureWorkModeURLScreenshotRetriesLegacyHeadless(t *testing.T) {
	withFastScreenshotPolling(t)
	pngData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode PNG fixture: %v", err)
	}
	fixture := filepath.Join(t.TempDir(), "fixture.png")
	if err := os.WriteFile(fixture, pngData, 0o644); err != nil {
		t.Fatalf("write PNG fixture: %v", err)
	}
	calls := filepath.Join(t.TempDir(), "calls.txt")
	browser := writeFakeBrowserScript(t, `
mode="unknown"
output=""
for arg in "$@"; do
  [ "$arg" = "--headless=new" ] && mode="new"
  [ "$arg" = "--headless" ] && mode="legacy"
  case "$arg" in --screenshot=*) output="${arg#--screenshot=}" ;; esac
done
printf '%s\n' "$mode" >> "$FAKE_BROWSER_CALLS"
if [ "$mode" = "new" ]; then
  echo "new mode intentionally produced no file"
  exit 0
fi
cp "$FAKE_BROWSER_PNG" "$output"
echo "legacy mode produced screenshot"
exit 0`)
	t.Setenv("AGENTGO_BROWSER_PATH", browser)
	t.Setenv("FAKE_BROWSER_CALLS", calls)
	t.Setenv("FAKE_BROWSER_PNG", fixture)

	imageData, err := captureWorkModeURLScreenshot(context.Background(), "https://example.com/")
	if err != nil {
		t.Fatalf("captureWorkModeURLScreenshot: %v", err)
	}
	if imageData.Data == "" {
		t.Fatal("screenshot data is empty")
	}
	callData, err := os.ReadFile(calls)
	if err != nil {
		t.Fatalf("read calls: %v", err)
	}
	if strings.TrimSpace(string(callData)) != "new\nlegacy" {
		t.Fatalf("expected new then legacy calls, got %q", string(callData))
	}
}

func TestCaptureWorkModeURLScreenshotMissingFileIncludesBrowserLogs(t *testing.T) {
	withFastScreenshotPolling(t)
	browser := writeFakeBrowserScript(t, `
mode="unknown"
for arg in "$@"; do
  [ "$arg" = "--headless=new" ] && mode="new"
  [ "$arg" = "--headless" ] && mode="legacy"
done
echo "simulated $mode browser output"
exit 0`)
	t.Setenv("AGENTGO_BROWSER_PATH", browser)

	_, err := captureWorkModeURLScreenshot(context.Background(), "https://example.com/")
	if err == nil {
		t.Fatal("expected screenshot failure")
	}
	message := err.Error()
	for _, expected := range []string{
		"browser exited without producing a screenshot",
		"simulated new browser output",
		"simulated legacy browser output",
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("error missing %q: %s", expected, message)
		}
	}
	if strings.Contains(message, "agentgo-shot-") || strings.Contains(message, "capture.png: no such file") {
		t.Fatalf("error leaked temporary path details: %s", message)
	}
}

func TestCaptureWorkModeURLScreenshotPersistsOriginalInWorkspace(t *testing.T) {
	withFastScreenshotPolling(t)
	pngData, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode PNG fixture: %v", err)
	}
	fixture := filepath.Join(t.TempDir(), "fixture.png")
	if err := os.WriteFile(fixture, pngData, 0o644); err != nil {
		t.Fatalf("write PNG fixture: %v", err)
	}
	outputRecord := filepath.Join(t.TempDir(), "output.txt")
	browser := writeFakeBrowserScript(t, `
output=""
for arg in "$@"; do
  case "$arg" in --screenshot=*) output="${arg#--screenshot=}" ;; esac
done
printf '%s' "$output" > "$FAKE_BROWSER_OUTPUT_RECORD"
cp "$FAKE_BROWSER_PNG" "$output"
echo "saved workspace screenshot"
exit 0`)
	t.Setenv("AGENTGO_BROWSER_PATH", browser)
	t.Setenv("FAKE_BROWSER_PNG", fixture)
	t.Setenv("FAKE_BROWSER_OUTPUT_RECORD", outputRecord)

	workRoot := t.TempDir()
	app := &App{cfg: AppConfig{WorkRoot: workRoot}}
	workspace, err := app.workModeWebshotWorkspace("Work")
	if err != nil {
		t.Fatalf("workModeWebshotWorkspace: %v", err)
	}
	workspace.Timestamp = "20260712-055428"
	workspace.Index = 1
	imageData, err := captureWorkModeURLScreenshotInWorkspace(context.Background(), "https://example.com/", workspace)
	if err != nil {
		t.Fatalf("captureWorkModeURLScreenshotInWorkspace: %v", err)
	}
	if imageData.StoredPath == "" || imageData.StoredName == "" {
		t.Fatalf("stored webshot metadata missing: %#v", imageData)
	}
	if matched, _ := regexp.MatchString(`^AgentGO-webshot-20260712-055428-1\.png$`, imageData.StoredName); !matched {
		t.Fatalf("unexpected stored filename: %q", imageData.StoredName)
	}
	expectedRel := filepath.ToSlash(filepath.Join("tmp", "webshots", "Work", imageData.StoredName))
	if imageData.StoredPath != expectedRel {
		t.Fatalf("stored path = %q, want %q", imageData.StoredPath, expectedRel)
	}
	storedPath := filepath.Join(workRoot, filepath.FromSlash(imageData.StoredPath))
	storedData, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatalf("read stored PNG: %v", err)
	}
	if string(storedData) != string(pngData) {
		t.Fatal("stored PNG does not match Chromium output")
	}
	recordedOutput, err := os.ReadFile(outputRecord)
	if err != nil {
		t.Fatalf("read browser output record: %v", err)
	}
	if filepath.Clean(string(recordedOutput)) != filepath.Clean(storedPath) {
		t.Fatalf("browser output path = %q, want %q", string(recordedOutput), storedPath)
	}
	entries, err := os.ReadDir(workspace.Directory)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "browser-work-") {
			t.Fatalf("browser work directory was not cleaned: %s", entry.Name())
		}
	}
}

func TestNextWorkModeWebshotPathUsesSequenceSuffix(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 12, 5, 54, 28, 0, time.Local)
	firstName := "AgentGO-webshot-20260712-055428-1.png"
	if err := os.WriteFile(filepath.Join(dir, firstName), []byte("existing"), 0o644); err != nil {
		t.Fatalf("write existing webshot: %v", err)
	}
	name, path, err := nextWorkModeWebshotPath(dir, "20260712-055428", 1, now)
	if err != nil {
		t.Fatalf("nextWorkModeWebshotPath: %v", err)
	}
	if name != "AgentGO-webshot-20260712-055428-2.png" {
		t.Fatalf("name = %q", name)
	}
	if path != filepath.Join(dir, name) {
		t.Fatalf("path = %q", path)
	}
}

func TestResetWorkModeWebshotWorkspacePreservesOtherTmpContent(t *testing.T) {
	workRoot := t.TempDir()
	webshotDir := filepath.Join(workRoot, "tmp", "webshots", "Work")
	otherDir := filepath.Join(workRoot, "tmp", "other")
	if err := os.MkdirAll(webshotDir, 0o755); err != nil {
		t.Fatalf("mkdir webshot dir: %v", err)
	}
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatalf("mkdir other dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(webshotDir, "old.png"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write old webshot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatalf("write other tmp file: %v", err)
	}
	if err := resetWorkModeWebshotWorkspace(workRoot); err != nil {
		t.Fatalf("resetWorkModeWebshotWorkspace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(webshotDir, "old.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old webshot still exists or unexpected stat error: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(otherDir, "keep.txt")); err != nil || string(data) != "keep" {
		t.Fatalf("other tmp content changed: data=%q err=%v", string(data), err)
	}
	root, err := workModeWebshotRoot(workRoot)
	if err != nil {
		t.Fatalf("workModeWebshotRoot: %v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("webshot root was not recreated: info=%v err=%v", info, err)
	}
}

func TestWorkModeSessionClearRemovesWebshotsWithoutSelectedBuilder(t *testing.T) {
	workRoot := t.TempDir()
	webshotDir := filepath.Join(workRoot, "tmp", "webshots", "Work")
	if err := os.MkdirAll(webshotDir, 0o755); err != nil {
		t.Fatalf("mkdir webshot dir: %v", err)
	}
	oldPath := filepath.Join(webshotDir, "AgentGO-webshot-20260712-055428-1.png")
	if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old webshot: %v", err)
	}
	tempAttachmentDir := filepath.Join(workRoot, "tmp", workModeTempAttachmentsDirName, workModeAttachmentProjectKey("Work"), "attachments", "attachment-1")
	if err := os.MkdirAll(tempAttachmentDir, 0o755); err != nil {
		t.Fatalf("mkdir temporary attachment dir: %v", err)
	}
	tempAttachmentPath := filepath.Join(tempAttachmentDir, "payload.bin")
	if err := os.WriteFile(tempAttachmentPath, []byte("temporary"), 0o600); err != nil {
		t.Fatalf("write temporary attachment: %v", err)
	}
	app := &App{
		cfg:                       AppConfig{WorkRoot: workRoot},
		activeProjectName:         "Work",
		workModeSessionsByProject: map[string]workModeSessionState{"Work": {ProjectName: "Work"}},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/session", strings.NewReader(`{"action":"clear"}`))
	recorder := httptest.NewRecorder()
	app.handleWorkModeSession(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old webshot still exists or unexpected stat error: %v", err)
	}
	if _, err := os.Stat(tempAttachmentPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary attachment still exists or unexpected stat error: %v", err)
	}
	if _, ok := app.workModeSessionsByProject["Work"]; ok {
		t.Fatal("Work Mode session state was not cleared")
	}
}

func TestNormalizeTemporaryAttachmentsHasNoAgentGOSizeOrCountBudget(t *testing.T) {
	largeText := strings.Repeat("x", 300_000)
	items := make([]temporaryAttachmentInput, 0, 10)
	for i := 0; i < 10; i++ {
		items = append(items, temporaryAttachmentInput{
			ID:       fmt.Sprintf("attachment-%d", i),
			Name:     fmt.Sprintf("context-%d.txt", i),
			Kind:     "text",
			MIMEType: "text/plain",
			Text:     largeText,
		})
	}
	clean, err := normalizeTemporaryAttachments(items)
	if err != nil {
		t.Fatalf("normalizeTemporaryAttachments rejected large/many attachments: %v", err)
	}
	if len(clean) != len(items) {
		t.Fatalf("normalized attachment count = %d, want %d", len(clean), len(items))
	}
}

func TestWorkModeTemporaryAttachmentStorePreservesOriginalAndPayload(t *testing.T) {
	workRoot := t.TempDir()
	app := &App{cfg: AppConfig{WorkRoot: workRoot}}
	original := []byte("full-quality-image-data")
	payload := []byte("compressed-image")
	stored, err := app.storeWorkModeTemporaryAttachment("Work", temporaryAttachmentInput{
		ID:                "attachment-1",
		Name:              "reference.jpg",
		Kind:              "image",
		MIMEType:          "image/jpeg",
		Data:              base64.StdEncoding.EncodeToString(payload),
		SizeBytes:         int64(len(payload)),
		OriginalName:      "reference.png",
		OriginalMIMEType:  "image/png",
		OriginalData:      base64.StdEncoding.EncodeToString(original),
		OriginalSizeBytes: int64(len(original)),
	})
	if err != nil {
		t.Fatalf("storeWorkModeTemporaryAttachment: %v", err)
	}
	if stored.SizeBytes != int64(len(payload)) || stored.OriginalSizeBytes != int64(len(original)) {
		t.Fatalf("unexpected stored sizes: %#v", stored)
	}
	loaded, err := app.loadWorkModeTemporaryAttachment("Work", "attachment-1")
	if err != nil {
		t.Fatalf("loadWorkModeTemporaryAttachment: %v", err)
	}
	loadedPayload, _ := base64.StdEncoding.DecodeString(loaded.Data)
	loadedOriginal, _ := base64.StdEncoding.DecodeString(loaded.OriginalData)
	if string(loadedPayload) != string(payload) || string(loadedOriginal) != string(original) {
		t.Fatalf("stored attachment data mismatch: payload=%q original=%q", loadedPayload, loadedOriginal)
	}
}

func TestToastPositionContrastAndKnowledgeToggleLayering(t *testing.T) {
	templateBytes, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	templateText := string(templateBytes)

	toastStart := strings.Index(templateText, ".toast {")
	if toastStart < 0 {
		t.Fatal("toast CSS block not found")
	}
	toastEnd := strings.Index(templateText[toastStart:], ".token-usage-display {")
	if toastEnd < 0 {
		t.Fatal("toast CSS block end not found")
	}
	toastCSS := templateText[toastStart : toastStart+toastEnd]
	for _, required := range []string{
		"top: 20px;",
		"transform: translateX(-50%) translateY(-24px);",
		"pointer-events: none;",
		"background: rgba(17, 24, 39, 0.98);",
		"border-left: 4px solid #f5a6d7;",
		"border-left-color: #ffe27c;",
		"color: #fff7dc;",
	} {
		if !strings.Contains(toastCSS, required) {
			t.Fatalf("toast CSS missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"bottom: 24px;",
		"translateY(20px)",
		"background: rgba(16, 32, 58, 0.94);",
	} {
		if strings.Contains(toastCSS, forbidden) {
			t.Fatalf("toast CSS still contains old styling %q", forbidden)
		}
	}

	knowledgeStart := strings.Index(templateText, ".knowledge-toggle-btn {")
	if knowledgeStart < 0 {
		t.Fatal("knowledge toggle CSS block not found")
	}
	knowledgeEnd := strings.Index(templateText[knowledgeStart:], ".knowledge-toggle-btn:hover")
	if knowledgeEnd < 0 {
		t.Fatal("knowledge toggle CSS block end not found")
	}
	knowledgeCSS := templateText[knowledgeStart : knowledgeStart+knowledgeEnd]
	if !strings.Contains(knowledgeCSS, "z-index: 1600;") {
		t.Fatal("knowledge toggle must use z-index 1600")
	}
	if strings.Contains(knowledgeCSS, "z-index: 2600;") {
		t.Fatal("knowledge toggle still uses z-index 2600")
	}
}

func TestAgentGOStylingPaletteAndWorkModeWebshotUI(t *testing.T) {
	promptBytes, err := os.ReadFile("system_prompts/agentgo_styling.txt")
	if err != nil {
		t.Fatalf("read styling prompt: %v", err)
	}
	prompt := strings.TrimSpace(string(promptBytes))
	if prompt != strings.TrimSpace(defaultAgentGOStylingPrompt) {
		t.Fatal("built-in styling prompt and system_prompts/agentgo_styling.txt are out of sync")
	}
	for _, required := range []string{"green #7EE7A8", "yellow #FFE27C", "orange #FF9900", "red #FF9696", `<span style="color:#RRGGBB">`, `CodeChange Rule - Code change highlighting is required`, `Keep the normal <pre><code> block`, `<span class="agentgo-code-change">...</span>`, `The span belongs inside <pre><code>`, `Use one span per changed or newly inserted line.`, `CodeChange Rule Example:`, `<span class="agentgo-code-change">const timeoutMs = 15000;</span>`} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("styling prompt missing %q", required)
		}
	}

	templateBytes, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	templateText := string(templateBytes)
	for _, required := range []string{
		"property === 'color' && /^#[0-9a-fA-F]{6}$/.test(candidate)",
		`id="workModeWebshotModal"`,
		"collectWorkModeWebshots(options.urlCaptures)",
		"appendWorkModeMessage('user', prompt, { urlCaptures: suppliedURLCaptures, attachments: tempAttachmentSnapshots })",
		"AgentGO auto-compress image uploads",
		"renderWorkModeMessageAttachments(msg.attachments)",
		"ImgPress: On",
		"Image compression can be toggled on or off in settings",
		"RMode: ${mode.label}",
		"Response mode defines how AI will respond to you, set this in settings",
		"work-mode-status-memory-toggle.is-off",
		"work-mode-settings-status-line",
		".work-mode-message-actions{display:inline-flex;align-items:center;gap:7px;margin-top:8px;vertical-align:middle}",
		".work-mode-message-collapse-btn{margin-top:8px;margin-right:7px;",
		"height:30px;line-height:1;padding:0 10px;cursor:pointer;transform-origin:center;vertical-align:middle",
		"const totalFiles = summary.selectedFiles + summary.temporaryFiles",
		"(&nbsp;<span class=\"work-mode-input-size\">${escapeHtml(sizeCompact)}</span>&nbsp;/&nbsp;~${escapeHtml(tokenCompact)}&nbsp;/&nbsp;${totalFiles})",
		"Total files: ${totalFiles}",
		"data-work-max-return",
		"MaxOutput: ${escapeHtml(builderMaxText)}",
		"/api/models/max-output-tokens",
		"Automatic Maximum — Recommended",
		"Custom guardrail",
		`max-output-token-unit">TOKENS`,
		"max_output_mode",
		"provider-managed automatic maximum",
		"AgentGO repaired the AI response",
		"Original AI response",
		"Automatic repair response",
		"work-mode-code-toolbar",
		".work-mode-message.ai .work-mode-code-block pre{max-width:100%;min-width:0;box-sizing:border-box;white-space:pre;overflow-x:auto;overflow-y:auto;overflow-wrap:normal;word-break:normal",
		"data-work-code-copy",
		"data-work-code-wrap",
		"renderWorkModeWrapIcon()",
		"/api/work-mode/attachments",
		"image.stored_path || image.storedPath",
		"buildWorkspaceBlobUrl(storedPath, true)",
		"webshotTimestamp: workModeWebshotTimestamp(Date.now())",
		"webshotIndex: row.options.screenshot ? Number(webshotIndex || 0) : 0",
		"AgentGO-webshot-${workModeWebshotTimestamp(selected.msg.webshotCapturedAt)}-${workModeWebshotPreviewState.webshotIndex + 1}.png",
	} {
		if !strings.Contains(templateText, required) {
			t.Fatalf("Work Mode template missing %q", required)
		}
	}
	if !strings.Contains(templateText, "#workModeMaxTokensModal{z-index:2210}") {
		t.Fatal("Work Mode max-return modal must render above the Work Mode overlay")
	}
	if !strings.Contains(templateText, ".work-mode-max-token-card{width:min(580px,calc(100vw - 36px));height:auto;max-height:min(82vh,720px)}") {
		t.Fatal("Work Mode max-return modal must use a compact, content-driven height")
	}
	if !strings.Contains(templateText, ".work-mode-max-token-body{display:grid;gap:14px;padding:18px 20px 20px;min-height:0;overflow-y:auto;color:#12233e}") {
		t.Fatal("Work Mode max-return modal body must use padded, scroll-safe spacing")
	}
	if strings.Contains(templateText, ".work-mode-max-token-body{display:grid;gap:14px;padding:4px 0}") {
		t.Fatal("Work Mode max-return modal still uses the unpadded body layout")
	}
	if strings.Contains(templateText, "ImgCompress:") || strings.Contains(templateText, "Resp Mode:") || strings.Contains(templateText, ">Max return:") {
		t.Fatal("Work Mode footer still contains the previous long labels")
	}

	for _, forbidden := range []string{
		"AgentGO automatically repaired the provider's Work Mode JSON envelope",
		"Loaded that message into the prompt box.",
		"A blank or 0 value restores",
		"AgentGO fallback",
	} {
		if strings.Contains(templateText, forbidden) {
			t.Fatalf("Work Mode template still contains obsolete message %q", forbidden)
		}
	}
}

func TestWorkModeTranscriptExportAndReconnectUI(t *testing.T) {
	templateBytes, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	templateText := string(templateBytes)
	for _, required := range []string{
		`id="workModeTranscriptDownloadBtn"`,
		`title="Download Full Transcript"`,
		`downloadWorkModeTranscript()`,
		`/api/work-mode/transcript/export`,
		`observerReviewMessages: workModeReviewMessages`,
		`builder: { id: builderId, label: builderLabel }`,
		`observer: observerId ? { id: observerId, label: observerLabel } : null`,
		`title: 'AgentGO Work-Mode Transcript'`,
		`builderLabel,`,
		`observerLabel,`,
		`msg.modelLabel = String(options.modelLabel || getActiveBuilderLabel() || 'AI')`,
		`const aiLabel = String((msg && msg.modelLabel) || getActiveBuilderLabel() || 'AI')`,
		`normalizeWorkModeTranscriptLightText(host);`,
		`node.style.setProperty('color', '#172033'`,
		`if (node.closest('pre,code')) return;`,
		`localizeWorkModeTranscriptImages(host)`,
		`id="agentGOConnectionModal"`,
		`.agentgo-connection-modal{position:fixed;inset:0;z-index:2147483000`,
		`AgentGO will not resend any interrupted prompt or file-changing request.`,
		`Your current browser-side Work Mode progress has not been refreshed or reset.`,
		`id="agentGOConnectionDismissBtn"`,
		`stopWorkModeObserverSessionPolling();`,
		`workModeURLReviewState.controller.abort()`,
		`if (agentGOConnectionState.disconnected) return null;`,
		`if (!(err && err.name === 'AbortError')) noteAgentGOConnectionFailure(url);`,
		`agentGOConnectionState.disconnected = false;`,
		`closeAgentGOConnectionModal();`,
		`dismissAgentGOConnectionLost`,
		`nativeAgentGOFetch('/api/healthz'`,
		`AgentGO request temporarily blocked`,
		`The health check succeeded.`,
		`The health check failed.`,
		`const INITIALIZATION_READ_TIMEOUT_MS = 15000;`,
		`const initializationReadOptions = { timeoutMs: INITIALIZATION_READ_TIMEOUT_MS };`,
		`await loadWaveState(initializationReadOptions).catch(() => {});`,
	} {
		if !strings.Contains(templateText, required) {
			t.Fatalf("Work Mode transcript/reconnect template missing %q", required)
		}
	}
	if strings.Contains(templateText, `z-index:2210;display:none;align-items:center;justify-content:center;padding:18px;background:rgba(5,10,18,.72)`) {
		t.Fatal("connection modal reused a normal Work Mode modal z-index")
	}
	for _, forbidden := range []string{
		`id="agentGOConnectionRetryBtn"`,
		`Try Reconnecting`,
		`startAgentGOConnectionChecks`,
		`tryAgentGOReconnect`,
		`refreshAgentGOStateAfterReconnect`,
		`Connected to AgentGO. State refreshed.`,
		`agentGOConnectionState.retryTimer`,
	} {
		if strings.Contains(templateText, forbidden) {
			t.Fatalf("warning-only connection UI still contains reconnect behavior %q", forbidden)
		}
	}
}

func TestWorkModeMemoryDefaultsAndActiveSessionTemplateHooks(t *testing.T) {
	templateBytes, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	templateText := string(templateBytes)
	for _, required := range []string{
		`AgentGO Memory Default`,
		`id="workModeMemoryDefaultSelect"`,
		`New Memory`,
		`Continue Last Memory`,
		`/api/work-mode/settings`,
		`/api/work-mode/memory/new`,
		`syncContinuedWorkModeMemory`,
		`workModeSettings.useMemory = true;`,
		`let agentGOActiveSessionId = '';`,
		`headers['X-AgentGO-Session-ID'] = agentGOActiveSessionId`,
		`headers.set('X-AgentGO-Session-ID', agentGOActiveSessionId)`,
		`/api/session/claim`,
		`Make This Tab Active`,
		`claimAgentGOSession('fresh')`,
		`claimAgentGOSession('takeover')`,
	} {
		if !strings.Contains(templateText, required) {
			t.Fatalf("B-series template missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`The session memory remains available until the tab closes.`,
		`memoryContent: memoryEnabled && !memoryName ? memoryContent : undefined`,
		`window.sessionStorage.getItem(AGENTGO_SESSION_STORAGE_KEY)`,
		`window.sessionStorage.setItem(AGENTGO_SESSION_STORAGE_KEY`,
	} {
		if strings.Contains(templateText, forbidden) {
			t.Fatalf("B-series template still contains obsolete behavior %q", forbidden)
		}
	}
}

func TestImportRepositoryChoiceUsesReadableCalloutStyling(t *testing.T) {
	templateBytes, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	templateText := string(templateBytes)
	for _, required := range []string{
		"#importModal .import-repository-choice {",
		"background: linear-gradient(180deg, rgba(232,244,255,0.98), rgba(217,235,252,0.96));",
		"#importModal .import-repository-choice-title {",
		"color: #17355f;",
		"#importModal .import-repository-choice label {",
		"color: #405b78;",
	} {
		if !strings.Contains(templateText, required) {
			t.Fatalf("import repository callout styling missing %q", required)
		}
	}
}
