package main

import (
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestWorkModeHTMLPreviewInstructionsAreConditional(t *testing.T) {
	const base = "BASE WORK MODE INSTRUCTIONS"
	if got := appendWorkModeHTMLPreviewInstructions(base, false); got != base {
		t.Fatalf("disabled instructions = %q", got)
	}
	got := appendWorkModeHTMLPreviewInstructions(base, true)
	for _, required := range []string{
		base,
		"HTML PREVIEW RESPONSES",
		`you may return one complete HTML`,
		`Put only the complete HTML document in "reply"`,
		`Do not substitute a reply preview for the requested`,
	} {
		if !strings.Contains(got, required) {
			t.Fatalf("HTML preview instructions missing %q:\n%s", required, got)
		}
	}
}

func TestWorkModeHTMLReplySettingPersistsIndependently(t *testing.T) {
	app := newWorkModeSettingsTestApp(t)
	settings, err := app.loadWorkModeSettings("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if settings.AllowAIHTMLReplies {
		t.Fatal("HTML replies defaulted on")
	}
	settings.MemoryDefault = workModeMemoryDefaultContinue
	settings.AllowAIHTMLReplies = true
	if err := app.saveWorkModeSettings("Demo", settings); err != nil {
		t.Fatal(err)
	}

	session, err := app.rotateActiveSession("Demo")
	if err != nil {
		t.Fatal(err)
	}
	memoryDefault := workModeMemoryDefaultOff
	body, _ := json.Marshal(workModeSettingsSaveRequest{ModelID: "1", MemoryDefault: &memoryDefault})
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/settings", strings.NewReader(string(body)))
	req.Header.Set(activeSessionHeader, session.SessionID)
	rr := httptest.NewRecorder()
	app.handleWorkModeSettings(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("memory save status=%d body=%s", rr.Code, rr.Body.String())
	}
	persisted, err := app.loadWorkModeSettings("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.AllowAIHTMLReplies || persisted.MemoryDefault != workModeMemoryDefaultOff {
		t.Fatalf("memory save changed HTML setting: %#v", persisted)
	}

	disabled := false
	body, _ = json.Marshal(workModeSettingsSaveRequest{ModelID: "1", AllowAIHTMLReplies: &disabled})
	req = httptest.NewRequest(http.MethodPost, "/api/work-mode/settings", strings.NewReader(string(body)))
	req.Header.Set(activeSessionHeader, session.SessionID)
	rr = httptest.NewRecorder()
	app.handleWorkModeSettings(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("HTML save status=%d body=%s", rr.Code, rr.Body.String())
	}
	persisted, err = app.loadWorkModeSettings("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.AllowAIHTMLReplies || persisted.MemoryDefault != workModeMemoryDefaultOff {
		t.Fatalf("HTML save changed unrelated settings: %#v", persisted)
	}
}

func TestHandleWorkModeSendInjectsHTMLPreviewPromptOnlyWhenAllowed(t *testing.T) {
	instructions := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode provider request: %v", err)
		}
		instructions = append(instructions, strings.TrimSpace(stringValue(payload["instructions"])))
		writeJSON(w, http.StatusOK, map[string]any{"text": `{"reply":"ok","files":[],"artifacts":[],"memory":"","warnings":[]}`})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	for _, allowed := range []bool{false, true} {
		settings, err := app.loadWorkModeSettings("Demo")
		if err != nil {
			t.Fatal(err)
		}
		settings.AllowAIHTMLReplies = allowed
		if err := app.saveWorkModeSettings("Demo", settings); err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]any{"modelId": "1", "prompt": "Build a preview."})
		rr := httptest.NewRecorder()
		app.handleWorkModeSend(rr, httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body))))
		if rr.Code != http.StatusOK {
			t.Fatalf("allowed=%v status=%d body=%s", allowed, rr.Code, rr.Body.String())
		}
	}
	if len(instructions) != 2 {
		t.Fatalf("captured instructions=%d", len(instructions))
	}
	if strings.Contains(instructions[0], "HTML PREVIEW RESPONSES") {
		t.Fatalf("disabled call received HTML preview instructions:\n%s", instructions[0])
	}
	if !strings.Contains(instructions[1], "HTML PREVIEW RESPONSES") {
		t.Fatalf("enabled call missing HTML preview instructions:\n%s", instructions[1])
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func TestWorkModeHTMLPreviewTemplateSecurityHooks(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		`function workModeReplyContainsPreviewableHTML(value)`,
		`View Source`,
		`Safe Preview`,
		`Run Interactive…`,
		`sandbox="${sandbox}"`,
		`const sandbox = interactive ? 'allow-scripts' : '';`,
		`connect-src 'none'`,
		`form-action 'none'`,
		`base-uri 'none'`,
		`referrerpolicy="no-referrer"`,
		`External network access`,
		`function createAgentGOWorkModeRequestID()`,
		`X-AgentGO-Request-ID`,
		`{ label: 'Request ID'`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("HTML preview template missing %q", required)
		}
	}
	if strings.Contains(source, `allow-same-origin`) {
		t.Fatal("interactive HTML preview grants same-origin access")
	}
}

type failingWorkModeWriter struct {
	header http.Header
}

func (w *failingWorkModeWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}
func (w *failingWorkModeWriter) WriteHeader(int)           {}
func (w *failingWorkModeWriter) Write([]byte) (int, error) { return 0, errors.New("socket closed") }

func TestWorkModeResponseWriteTrackerCapturesWriteFailure(t *testing.T) {
	tracker := &workModeResponseWriteTracker{ResponseWriter: &failingWorkModeWriter{}}
	if _, err := tracker.Write([]byte(`{"ok":true}`)); err == nil {
		t.Fatal("write unexpectedly succeeded")
	}
	if tracker.writeErr == nil || !strings.Contains(tracker.writeErr.Error(), "socket closed") || tracker.status != http.StatusOK {
		t.Fatalf("tracker=%+v", tracker)
	}
}

func TestWorkModeRecoveryUsesClientRequestID(t *testing.T) {
	app := newWorkModeSettingsTestApp(t)
	const requestID = "wm-client-test-123"
	handler := app.withWorkModeRecovery("/api/work-mode/send", func(w http.ResponseWriter, r *http.Request) {
		if got := workModeRequestIDFromRequest(r); got != requestID {
			t.Fatalf("request context ID=%q want=%q", got, requestID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(`{}`))
	req.Header.Set("X-AgentGO-Request-ID", requestID)
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-AgentGO-Request-ID"); got != requestID {
		t.Fatalf("response request ID=%q want=%q", got, requestID)
	}
}

func TestRenderedAgentGOJavaScriptParses(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is unavailable")
	}
	tmpl, err := template.ParseFiles(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	var rendered strings.Builder
	data := homeData{
		HTTPPort:               5226,
		HTTPSPort:              5227,
		TTSVoiceJSON:           template.JS(`""`),
		VoiceStartCommandJSON:  template.JS(`"AgentGO Start"`),
		VoiceSendCommandJSON:   template.JS(`"AgentGO Send"`),
		VoiceRepeatCommandJSON: template.JS(`"AgentGO Repeat"`),
		VoiceReadCommandJSON:   template.JS(`"AgentGO Read"`),
	}
	if err := tmpl.Execute(&rendered, data); err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?s)<script([^>]*)>(.*?)</script>`)
	matches := re.FindAllStringSubmatch(rendered.String(), -1)
	checked := 0
	for _, match := range matches {
		if strings.Contains(strings.ToLower(match[1]), "src=") || strings.TrimSpace(match[2]) == "" {
			continue
		}
		path := filepath.Join(t.TempDir(), "agentgo-inline.js")
		if err := os.WriteFile(path, []byte(match[2]), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("node", "--check", path)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("inline JavaScript syntax failed: %v\n%s", err, output)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no inline JavaScript blocks were checked")
	}
}
