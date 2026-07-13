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
		"decoder.innerHTML = source.replace(/<\\/textarea/gi, '&lt;/textarea')",
		"el.textContent = decodeAgentGOCodeText(node)",
		"const text = pre ? String(pre.textContent || '') : ''",
	} {
		if !strings.Contains(templateText, required) {
			t.Fatalf("Work Mode code rendering regression guard missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"hasElementChild ? (node.innerHTML || '') : (node.textContent || '')",
		"const text = pre ? String(pre.innerHTML || '') : ''",
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

func TestAgentGOStylingPaletteAndWorkModeWebshotUI(t *testing.T) {
	promptBytes, err := os.ReadFile("system_prompts/agentgo_styling.txt")
	if err != nil {
		t.Fatalf("read styling prompt: %v", err)
	}
	prompt := strings.TrimSpace(string(promptBytes))
	if prompt != strings.TrimSpace(defaultAgentGOStylingPrompt) {
		t.Fatal("built-in styling prompt and system_prompts/agentgo_styling.txt are out of sync")
	}
	for _, required := range []string{"green #7EE7A8", "yellow #FFE27C", "orange #FF9900", "red #FF9696", `<span style="color:#RRGGBB">`} {
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
		"ImgCompress: On",
		"work-mode-status-memory-toggle.is-off",
		"work-mode-settings-status-line",
		".work-mode-message-actions{display:inline-flex;align-items:center;gap:7px;margin-top:8px;vertical-align:middle}",
		".work-mode-message-collapse-btn{margin-top:8px;margin-right:7px;",
		"height:30px;line-height:1;padding:0 10px;cursor:pointer;transform-origin:center;vertical-align:middle",
		"(&nbsp;<span class=\"work-mode-input-size\">${escapeHtml(sizeCompact)}</span>&nbsp;/&nbsp;~${escapeHtml(tokenCompact)}&nbsp;,&nbsp;${summary.files}&nbsp;)",
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
}
