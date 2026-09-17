package main

import (
	"agentgo/adapters"
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSlowHTTPRequestWarningThresholdUsesWorkModeAllowance(t *testing.T) {
	if got := slowHTTPRequestWarningThreshold("/api/work-mode/send", http.MethodPost); got != 60*time.Second {
		t.Fatalf("Work Mode slow threshold = %s, want 60s", got)
	}
	if got := slowHTTPRequestWarningThreshold("/api/work-mode/url/capture", http.MethodPost); got != 120*time.Second {
		t.Fatalf("URL capture slow threshold = %s, want 120s", got)
	}
	if got := slowHTTPRequestWarningThreshold("/api/work-mode/send", http.MethodGet); got != 2*time.Second {
		t.Fatalf("GET Work Mode slow threshold = %s, want 2s", got)
	}
	if got := slowHTTPRequestWarningThreshold("/api/projects", http.MethodPost); got != 2*time.Second {
		t.Fatalf("normal route slow threshold = %s, want 2s", got)
	}
}

func TestDefaultAgentGOStylingKeepsCorrectionInstructionNearTop(t *testing.T) {
	instructions := buildWorkModeInstructions(
		ModelConfig{UseUggPrompt: true},
		false,
		false,
		"auto",
		nil,
		defaultAgentGOStylingPrompt,
		"",
	)
	correction := `Important: Whenever you return or reuse any user-provided prose or code, always wrap only the changes or modifications you make in <span class="agentgo-code-change">...</span>. Always apply this rule so users can quickly identify your edits, whether inside a <pre><code> block or in ordinary prose outside a code block.`
	if count := strings.Count(instructions, correction); count != 1 {
		t.Fatalf("correction instruction count = %d, want 1\n%s", count, instructions)
	}
	applyIndex := strings.Index(instructions, `Only apply AgentGO Styling Protocol to the user-facing "reply" field`)
	correctionIndex := strings.Index(instructions, correction)
	compactIndex := strings.Index(instructions, "Use compact, safe HTML for rendered chat replies.")
	if applyIndex < 0 || correctionIndex <= applyIndex || compactIndex <= correctionIndex {
		t.Fatalf("correction instruction is not in the requested styling position: apply=%d correction=%d compact=%d", applyIndex, correctionIndex, compactIndex)
	}
}

func TestDefaultAgentGOStylingPositionIsPreservedForObserverWorker(t *testing.T) {
	instructions := workModeObserverReviewWorkerInstructions(
		ModelConfig{UseUggPrompt: true},
		false,
		true,
		"auto",
		nil,
		defaultAgentGOStylingPrompt,
		"",
	)
	correction := `Important: Whenever you return or reuse any user-provided prose or code, always wrap only the changes or modifications you make in <span class="agentgo-code-change">...</span>. Always apply this rule so users can quickly identify your edits, whether inside a <pre><code> block or in ordinary prose outside a code block.`
	if count := strings.Count(instructions, correction); count != 1 {
		t.Fatalf("Observer Worker correction instruction count = %d, want 1\n%s", count, instructions)
	}
	if strings.Index(instructions, correction) > strings.Index(instructions, "OBSERVER REVIEW MODE") {
		t.Fatalf("default styling prompt moved outside its requested position:\n%s", instructions)
	}
}

func TestAgentGOStylingCorrectionInstructionNotAddedWhenStylingDisabled(t *testing.T) {
	instructions := buildWorkModeInstructions(ModelConfig{UseUggPrompt: true}, false, false, "auto", nil, "", "")
	if strings.Contains(instructions, `Important: Whenever you return or reuse any user-provided prose or code, always wrap only the changes or modifications you make in <span class="agentgo-code-change">...</span>. Always apply this rule so users can quickly identify your edits, whether inside a <pre><code> block or in ordinary prose outside a code block.`) {
		t.Fatalf("AgentGO styling correction instruction was added while styling disabled:\n%s", instructions)
	}
}

func TestWorkModeStructuredOutputPromptMatchesEffectiveCompatibilityMode(t *testing.T) {
	nativeInstructions := buildWorkModeInstructions(ModelConfig{}, false, false, "auto", nil, "", "")
	for _, required := range []string{
		`The provider enforces AgentGO's Work Mode JSON schema.`,
		`For ordinary replies, explanations, summaries, code examples, and read-only questions, leave "files" and "artifacts" empty.`,
		`Use artifacts only for requested binary/file outputs, with base64 data.`,
	} {
		if !strings.Contains(nativeInstructions, required) {
			t.Fatalf("native-schema Work Mode instructions missing %q:\n%s", required, nativeInstructions)
		}
	}
	for _, redundant := range []string{
		`Return strict valid JSON according to RFC 8259.`,
		`Use this compact response shape:`,
		`{"reply":"User-facing response or summary of actions here."`,
	} {
		if strings.Contains(nativeInstructions, redundant) {
			t.Fatalf("native-schema Work Mode instructions retained redundant prompt text %q:\n%s", redundant, nativeInstructions)
		}
	}

	promptInstructions := buildWorkModeInstructions(ModelConfig{PromptEnforcedJSON: true}, false, false, "auto", nil, "", "")
	for _, required := range []string{
		`CRITICAL JSON CONTRACT`,
		`Return strict valid JSON according to RFC 8259.`,
		`Use this compact response shape:`,
		`{"reply":"User-facing response or summary of actions here.","files":[],"artifacts":[],"memory":"","warnings":[]}`,
	} {
		if !strings.Contains(promptInstructions, required) {
			t.Fatalf("prompt-enforced Work Mode instructions missing %q:\n%s", required, promptInstructions)
		}
	}

	properties := workModeJSONSchema()["properties"].(map[string]any)
	files := properties["files"].(map[string]any)
	fileItems := files["items"].(map[string]any)
	fileProperties := fileItems["properties"].(map[string]any)
	action := fileProperties["action"].(map[string]any)
	actions, ok := action["enum"].([]string)
	if !ok || len(actions) != 2 || actions[0] != "create" || actions[1] != "overwrite" {
		t.Fatalf("normal Work Mode actions = %#v, want create/overwrite only", action["enum"])
	}
	artifacts := properties["artifacts"].(map[string]any)
	items := artifacts["items"].(map[string]any)
	artifactProperties := items["properties"].(map[string]any)
	encoding := artifactProperties["encoding"].(map[string]any)
	enum, ok := encoding["enum"].([]string)
	if encoding["type"] != "string" || !ok || len(enum) != 1 || enum[0] != "base64" {
		t.Fatalf("artifact encoding schema = %#v, want base64 only", encoding)
	}
}

func TestParseWorkModeAdapterResponseIgnoresUnreferencedInvalidArtifact(t *testing.T) {
	raw := `{"reply":"ok","files":[],"artifacts":[{"id":"hallucinated","encoding":"utf-8","mime_type":"text/plain","data":"not used"}],"memory":"","warnings":[]}`
	parsed, err := parseWorkModeAdapterResponse(adapters.Response{Text: raw}, t.TempDir(), "Demo")
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(parsed.Artifacts) != 0 {
		t.Fatalf("unreferenced artifacts were retained: %#v", parsed.Artifacts)
	}
	if len(parsed.Warnings) != 0 {
		t.Fatalf("successful normalization leaked into AI warnings: %#v", parsed.Warnings)
	}
	if !strings.Contains(strings.Join(parsed.Normalizations, "\n"), "ignored 1 unreferenced artifact") {
		t.Fatalf("missing ignored-artifact normalization: %#v", parsed.Normalizations)
	}
}

func TestParseWorkModeAdapterResponseNormalizesReferencedPlainTextArtifact(t *testing.T) {
	raw := `{"reply":"saved","files":[{"path":"notes.txt","action":"create","content":"","artifact_ref":"note"}],"artifacts":[{"id":"note","encoding":"utf-8","mime_type":"text/plain","data":"hello"}],"memory":"","warnings":[]}`
	parsed, err := parseWorkModeAdapterResponse(adapters.Response{Text: raw}, t.TempDir(), "Demo")
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(parsed.Artifacts) != 1 || parsed.Artifacts[0].Encoding != "base64" {
		t.Fatalf("artifact was not normalized: %#v", parsed.Artifacts)
	}
	decoded, err := decodeBuilderArtifactData(parsed.Artifacts[0].Data)
	if err != nil || string(decoded) != "hello" {
		t.Fatalf("normalized artifact data = %q err=%v", string(decoded), err)
	}
	if !strings.Contains(strings.Join(parsed.Normalizations, "\n"), "normalized plain-text artifact encoding to base64") {
		t.Fatalf("missing artifact-encoding normalization detail: %#v", parsed.Normalizations)
	}
}

func TestWorkModeDiagnosticSanitizationRedactsConfiguredAndNamedSecrets(t *testing.T) {
	model := ModelConfig{APIKey: "configured-secret-key", APIPass: "configured-password"}
	raw := `Bearer abcdefghijklmnop {"api_key":"inline-api-key","password":"inline-password"} configured-secret-key configured-password`
	clean := sanitizeWorkModeDiagnosticText(model, raw)
	for _, secret := range []string{"abcdefghijklmnop", "inline-api-key", "inline-password", "configured-secret-key", "configured-password"} {
		if strings.Contains(clean, secret) {
			t.Fatalf("diagnostic leaked %q: %s", secret, clean)
		}
	}
	if strings.Count(clean, "[REDACTED]") < 5 {
		t.Fatalf("expected redaction markers: %s", clean)
	}
}

func TestModelMaxOutputEditorUsesStructuredResponsiveLayout(t *testing.T) {
	raw, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	template := string(raw)
	for _, required := range []string{
		`class="model-max-output-options"`,
		`data-model-max-option="automatic"`,
		`data-model-max-option="custom"`,
		`id="modelMaxCustomRow"`,
		`class="model-max-output-custom-copy"`,
		`customRow?.classList.toggle('is-disabled', !custom.checked)`,
	} {
		if !strings.Contains(template, required) {
			t.Fatalf("model maximum output layout missing %q", required)
		}
	}
}

func TestWorkModeFailureDiagnosticsAreCopyableAndAddedFromErrorResponse(t *testing.T) {
	raw, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	template := string(raw)
	for _, required := range []string{
		`parsed.diagnostic && typeof appendDiagnosticsEntry === 'function'`,
		`data-diag-copy-field`,
		`Sanitized outbound request`,
		`entry.detailsLabel || 'Details'`,
		`diagnosticsEntries.findIndex(item => item.id === normalized.id)`,
	} {
		if !strings.Contains(template, required) {
			t.Fatalf("Work Mode diagnostics UI missing %q", required)
		}
	}
}

func TestWorkModeNormalizationUIUsesDiagnosticsSubtleNoteAndPlainTextWarnings(t *testing.T) {
	raw, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	template := string(raw)
	for _, required := range []string{
		`parsed.diagnostics.forEach(entry => appendDiagnosticsEntry(entry))`,
		`Response normalized by AgentGO`,
		`class="work-mode-normalized-note"`,
		`function workModeWarningPlainText(value)`,
		`doc.querySelectorAll('script,style,template,noscript')`,
		`warnings:Array.isArray(data.warnings) ? data.warnings : [], normalized:!!data.normalized`,
		`warnings: Array.isArray(data.warnings) ? data.warnings : [], normalized: !!data.normalized`,
	} {
		if !strings.Contains(template, required) {
			t.Fatalf("Work Mode normalization UI missing %q", required)
		}
	}
}

func TestOllamaContextGuidanceIsInlineAndAdapterSpecific(t *testing.T) {
	raw, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	template := string(raw)
	for _, required := range []string{
		`Known adapters use built-in transport rules. Provider Options remain optional JSON overrides.`,
		`id="modelOllamaContextHint" hidden`,
		`For smaller local models, limited context can be a problem. With Ollama, try increasing`,
		`<code>8192</code>`,
		`<code>16384</code>`,
		`<code>32768</code>`,
		`<code>65536</code>`,
		`"num_ctx": 16384`,
		`"num_predict": 2048`,
		`ollamaContextHint.hidden = String(adapter || '').toLowerCase() !== 'ollama_generate'`,
		`case 'ollama_generate':
      return '{}';`,
	} {
		if !strings.Contains(template, required) {
			t.Fatalf("Ollama context guidance missing %q", required)
		}
	}
	for _, required := range []string{
		`.model-ollama-context-hint pre{`,
		`background:#475569`,
		`color:#FFFFFF`,
	} {
		if !strings.Contains(template, required) {
			t.Fatalf("Ollama context example styling missing %q", required)
		}
	}
	if strings.Contains(template, `or Ollama context size. Larger context windows may require more system memory.`) {
		t.Fatal("Provider Options popup still contains the old Ollama context explanation")
	}
	ollamaHintStart := strings.Index(template, `id="modelOllamaContextHint"`)
	if ollamaHintStart < 0 {
		t.Fatal("Ollama context hint not found")
	}
	ollamaHintEnd := strings.Index(template[ollamaHintStart:], `</div>`)
	if ollamaHintEnd < 0 {
		t.Fatal("Ollama context hint end not found")
	}
	if strings.Contains(template[ollamaHintStart:ollamaHintStart+ollamaHintEnd], `temperature`) {
		t.Fatal("general Ollama context example must not set a temperature default")
	}
}

func TestModelNamePickerUsesLiveProviderDiscovery(t *testing.T) {
	raw, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	template := string(raw)
	mainRaw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main source: %v", err)
	}
	for _, required := range []string{
		`/api/models/discover`,
		`Select a Provider and Base URL so AgentGO can fetch the available models.`,
		`Fetching available models…`,
		`Could not connect to the provider.`,
		`The provider reported no available models.`,
		`Search available models`,
		`Enter model name manually…`,
		`function collectModelDiscoveryPayload()`,
		`headers_present: !!headersRaw && !deleteHeaders`,
		`api('/api/models/discover', { method: 'POST', body: JSON.stringify(payload) })`,
		`data-model-name-manual`,
	} {
		if !strings.Contains(template+string(mainRaw), required) {
			t.Fatalf("live model discovery UI missing %q", required)
		}
	}
	missingSetup := `showKnownModelNamePicker(popover, anchor, '<p class="model-name-picker-status">Select a Provider and Base URL so AgentGO can fetch the available models.</p>');`
	if !strings.Contains(template, missingSetup) {
		t.Fatal("missing-provider state should show only the setup guidance")
	}
}

func TestWorkModeSettingsIntroAndDismissibleStageStatus(t *testing.T) {
	raw, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	template := string(raw)
	mainRaw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main source: %v", err)
	}
	source := template + string(mainRaw)
	for _, required := range []string{
		`AgentGO Work Mode</strong> — an AI Builder, optional Observer, and project file manager. Use the file manager to select existing files or right-click to create files and folders for your AI to work on.`,
		`function renderWorkModeStatusMessage(content, dismissible = true)`,
		`data-work-status-dismiss`,
		`aria-label="Hide status"`,
		`function dismissWorkModeStatus()`,
		"statusItems.join(separatorHTML)}</span>`, { dismissible: false });",
		`stageMessage: String(state.stageMessage || '')`,
		`if (normalized.status === 'running' && normalized.stageMessage) return normalized.stageMessage;`,
		`Sending response for second-pass polish...`,
		`Sending response to Observer...`,
		`Finalizing response...`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Work Mode settings or staged status UI missing %q", required)
		}
	}
	for _, removed := range []string{
		`Memory uses tokens faster because AgentGO reminds the AI what you discussed.`,
		`Enable Roles to apply this AI Builder's configured role.`,
	} {
		if strings.Contains(template, removed) {
			t.Fatalf("old Work Mode settings preamble still contains %q", removed)
		}
	}
}

func TestWorkModeSessionStageTracksMatchingExecution(t *testing.T) {
	state := newWorkModeSessionState("demo", workModeRoleSelection{}, false, 3, "test")
	state.ExecutionID = "run-1"
	if state.StageMessage != "Sending Work Mode prompt..." {
		t.Fatalf("initial stage = %q", state.StageMessage)
	}
	app := &App{workModeSessionsByProject: map[string]workModeSessionState{"demo": state}}
	updated := app.updateWorkModeSessionStage("demo", "run-1", "Sending response for second-pass polish...", workModeCallOwnerWorker)
	if updated.StageMessage != "Sending response for second-pass polish..." || updated.ActiveCallOwner != workModeCallOwnerWorker {
		t.Fatalf("updated session = %#v", updated)
	}
	unchanged := app.updateWorkModeSessionStage("demo", "different-run", "wrong stage", workModeCallOwnerObserver)
	if unchanged.StageMessage != updated.StageMessage || unchanged.ActiveCallOwner != updated.ActiveCallOwner {
		t.Fatalf("mismatched execution changed session: %#v", unchanged)
	}
}

func TestWorkModeResponseDiagnosticSanitizationOmitsMalformedArtifactData(t *testing.T) {
	raw := `{"artifacts":[{"id":"x","encoding":"base64","data":"private-payload"}],`
	clean := sanitizeWorkModeResponseDiagnostic(ModelConfig{}, raw)
	if strings.Contains(clean, "private-payload") {
		t.Fatalf("malformed response diagnostic leaked artifact data: %s", clean)
	}
	if !strings.Contains(clean, "omitted from diagnostic") {
		t.Fatalf("malformed response diagnostic did not mark omission: %s", clean)
	}
}

func TestLiveFreezeDiagnosticEligibilityTargetsFastStateReads(t *testing.T) {
	for _, path := range []string{"/api/healthz", "/api/logs", "/api/risk", "/api/wave-state", "/api/deaddrop/status", "/api/projects", "/api/models", "/api/context-files"} {
		if !liveFreezeDiagnosticEligible(path, http.MethodGet) {
			t.Fatalf("expected live freeze diagnostics for GET %s", path)
		}
	}
	if liveFreezeDiagnosticEligible("/api/work-mode/send", http.MethodPost) {
		t.Fatal("long-running Work Mode send must not use the 10-second freeze watchdog")
	}
}

func TestCaptureLiveFreezeDiagnosticWritesRequestAndGoroutineEvidence(t *testing.T) {
	root := t.TempDir()
	app := &App{
		cfg:                      AppConfig{WorkRoot: root},
		startedAt:                time.Now().Add(-time.Minute),
		httpActiveByRoute:        map[string]int{"/api/wave-state": 1},
		httpActiveRequestDetails: map[uint64]httpActiveRequestInfo{},
		httpConns:                map[net.Conn]http.ConnState{},
	}
	timing := newHTTPRequestTiming()
	timing.lockWaits["app.wave_state"] = 12 * time.Second
	info := httpActiveRequestInfo{
		ID:       7,
		Method:   http.MethodGet,
		Path:     "/api/wave-state",
		Remote:   "10.0.2.2:63054",
		Started:  time.Now().Add(-12 * time.Second),
		Watchdog: true,
	}
	app.httpActiveRequestDetails[info.ID] = info
	path, err := app.captureLiveFreezeDiagnostic(info, timing)
	if err != nil {
		t.Fatalf("capture live freeze diagnostic: %v", err)
	}
	data, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatalf("read live freeze diagnostic: %v", err)
	}
	text := string(data)
	for _, required := range []string{
		"AgentGO live freeze diagnostic",
		"request_id=7",
		"path=/api/wave-state",
		"phase=handler_before_response_write",
		"app.wave_state=12s",
		"=== HTTP STATE ===",
		"activeRequestDetails",
		"=== GOROUTINES ===",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("live freeze diagnostic missing %q", required)
		}
	}
}

func TestWaveStateRecordsApplicationLockWaits(t *testing.T) {
	app := &App{
		activeProjectName:       "Work",
		waveStatusByProject:     map[string]waveStatusState{},
		waveExecutionsByProject: map[string]waveExecutionState{},
	}
	timing := newHTTPRequestTiming()
	req := httptest.NewRequest(http.MethodGet, "/api/wave-state", nil)
	req = req.WithContext(context.WithValue(req.Context(), httpRequestTimingContextKey{}, timing))
	rec := httptest.NewRecorder()
	app.handleWaveState(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("wave-state status = %d, want 200", rec.Code)
	}
	snapshot := timing.snapshot()
	for _, name := range []string{"app.active_project", "app.wave_state"} {
		if _, ok := snapshot.LockWaits[name]; !ok {
			t.Fatalf("wave-state timing did not record %s", name)
		}
	}
}

func TestNormalizeWorkModeWarningsTrimsAndDeduplicates(t *testing.T) {
	got := normalizeWorkModeWarnings([]string{"  Token prices can change.  ", "", "Token prices can change.", "Model availability can change."})
	want := []string{"Token prices can change.", "Model availability can change."}
	if len(got) != len(want) {
		t.Fatalf("warnings = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("warnings[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLogAIResponseWarningNamesModelSource(t *testing.T) {
	app := &App{}
	app.logAIResponseWarning(ModelConfig{ID: 23, Label: "Grok4.5"}, "  Token prices can change.  ")
	if len(app.logs) != 1 {
		t.Fatalf("log count = %d, want 1", len(app.logs))
	}
	entry := app.logs[0]
	if entry.Level != "WARN" {
		t.Fatalf("level = %q, want WARN", entry.Level)
	}
	if entry.Source != "Source: Grok4.5 (23)" {
		t.Fatalf("source = %q", entry.Source)
	}
	if entry.Message != "Token prices can change." {
		t.Fatalf("message = %q", entry.Message)
	}
}

func TestCurrentXAIKnownModelNames(t *testing.T) {
	data, err := os.ReadFile("model_names.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Models []struct {
			Provider     string   `json:"provider"`
			Adapter      string   `json:"adapter"`
			ModelName    string   `json:"model_name"`
			Capabilities []string `json:"capabilities"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("model_names.json is invalid: %v", err)
	}
	entries := map[string]struct {
		adapter string
		caps    map[string]bool
	}{}
	for _, model := range catalog.Models {
		if model.Provider != "xai" {
			continue
		}
		caps := map[string]bool{}
		for _, capability := range model.Capabilities {
			caps[capability] = true
		}
		entries[model.ModelName] = struct {
			adapter string
			caps    map[string]bool
		}{adapter: model.Adapter, caps: caps}
	}
	for modelName, adapter := range map[string]string{
		"grok-4.5":                     "xai_responses",
		"grok-4.5-latest":              "xai_responses",
		"grok-4.3":                     "xai_responses",
		"grok-4.20-0309-reasoning":     "xai_responses",
		"grok-4.20-0309-non-reasoning": "xai_responses",
		"grok-4.20-multi-agent-0309":   "xai_responses",
		"grok-build-0.1":               "xai_responses",
		"grok-imagine-image":           "xai_image",
		"grok-imagine-image-quality":   "xai_image",
		"grok-imagine-video":           "xai_video",
		"grok-imagine-video-1.5":       "xai_video",
	} {
		entry, ok := entries[modelName]
		if !ok {
			t.Fatalf("missing xAI known model %q", modelName)
		}
		if entry.adapter != adapter {
			t.Fatalf("%s adapter = %q, want %q", modelName, entry.adapter, adapter)
		}
	}
	if _, ok := entries["grok-build-latest"]; ok {
		t.Fatal("grok-build-latest should not remain in the known-model picker")
	}
	if _, ok := entries["grok-3"]; ok {
		t.Fatal("retired grok-3 should not appear in the known-model picker")
	}
	if entries["grok-imagine-video-1.5"].caps["text_to_video"] {
		t.Fatal("grok-imagine-video-1.5 must not advertise text_to_video")
	}
	if !entries["grok-imagine-video-1.5"].caps["image_to_video"] {
		t.Fatal("grok-imagine-video-1.5 should advertise image_to_video")
	}
}

func TestNormalizeWorkModeMaxPasses(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "default on zero", in: 0, want: 3},
		{name: "default on negative", in: -4, want: 3},
		{name: "keeps one", in: 1, want: 1},
		{name: "keeps middle", in: 42, want: 42},
		{name: "caps at 100", in: 101, want: 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeWorkModeMaxPasses(tt.in); got != tt.want {
				t.Fatalf("normalizeWorkModeMaxPasses(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseWorkModeObserverResponseRequiresExplicitHasInput(t *testing.T) {
	valid, err := parseWorkModeObserverResponse(`{"reply":"Looks good","has_input":false,"recommendations":["  ","Ship it"]}`)
	if err != nil {
		t.Fatalf("valid observer response failed: %v", err)
	}
	if valid.HasInput {
		t.Fatalf("HasInput = true, want false")
	}
	if len(valid.Recommendations) != 1 || valid.Recommendations[0] != "Ship it" {
		t.Fatalf("recommendations were not trimmed correctly: %#v", valid.Recommendations)
	}

	if _, err := parseWorkModeObserverResponse(`{"reply":"No flag"}`); err == nil || !strings.Contains(err.Error(), "has_input") {
		t.Fatalf("missing has_input error = %v, want has_input error", err)
	}
	if _, err := parseWorkModeObserverResponse(`{"reply":"Wrong type","has_input":"false"}`); err == nil || !strings.Contains(err.Error(), "must be a boolean") {
		t.Fatalf("string has_input error = %v, want boolean type error", err)
	}
}

func TestNormalizeLiteralLineBreaksInJSONStringValues(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		want        string
		wantChanged bool
	}{
		{
			name:        "literal line feed in string",
			raw:         "{\"reply\":\"line one\nline two\",\"files\":[]}",
			want:        `{"reply":"line one\nline two","files":[]}`,
			wantChanged: true,
		},
		{
			name:        "literal CRLF in string",
			raw:         "{\"reply\":\"line one\r\nline two\",\"files\":[]}",
			want:        `{"reply":"line one\r\nline two","files":[]}`,
			wantChanged: true,
		},
		{
			name:        "pretty JSON line breaks remain unchanged",
			raw:         "{\n  \"reply\": \"ok\",\n  \"files\": []\n}",
			want:        "{\n  \"reply\": \"ok\",\n  \"files\": []\n}",
			wantChanged: false,
		},
		{
			name:        "already escaped line break remains unchanged",
			raw:         `{"reply":"line one\nline two","files":[]}`,
			want:        `{"reply":"line one\nline two","files":[]}`,
			wantChanged: false,
		},
		{
			name:        "ambiguous escaped physical line break is not normalized",
			raw:         "{\"reply\":\"line one\\\nline two\",\"files\":[]}",
			want:        "{\"reply\":\"line one\\\nline two\",\"files\":[]}",
			wantChanged: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := normalizeLiteralLineBreaksInJSONStringValues(tt.raw)
			if got != tt.want {
				t.Fatalf("normalized = %q, want %q", got, tt.want)
			}
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChanged)
			}
		})
	}
}

func TestLiteralLineBreakNormalizationSilentlyRestoresWorkModeJSON(t *testing.T) {
	workerRaw := "{\"reply\":\"first line\nsecond line\",\"files\":[],\"artifacts\":[],\"memory\":\"\",\"warnings\":[]}"
	workerNormalized, changed := normalizeLiteralLineBreaksInJSONStringValues(workerRaw)
	if !changed {
		t.Fatal("worker response line break was not normalized")
	}
	worker, err := parseWorkModeAIResponse(workerNormalized)
	if err != nil {
		t.Fatalf("normalized worker response did not parse: %v", err)
	}
	if worker.Reply != "first line\nsecond line" {
		t.Fatalf("worker reply = %q", worker.Reply)
	}

	observerRaw := "{\"reply\":\"first line\nsecond line\",\"has_input\":false,\"recommendations\":[],\"warnings\":[]}"
	observerNormalized, changed := normalizeLiteralLineBreaksInJSONStringValues(observerRaw)
	if !changed {
		t.Fatal("observer response line break was not normalized")
	}
	observer, err := parseWorkModeObserverResponse(observerNormalized)
	if err != nil {
		t.Fatalf("normalized observer response did not parse: %v", err)
	}
	if observer.Reply != "first line\nsecond line" {
		t.Fatalf("observer reply = %q", observer.Reply)
	}
}

func TestParseOrRepairSilentlyNormalizesOnlyLiteralLineBreaks(t *testing.T) {
	app := &App{}
	workerRaw := "{\"reply\":\"first line\nsecond line\",\"files\":[],\"artifacts\":[],\"memory\":\"\",\"warnings\":[]}"
	worker, workerResp, err := app.parseOrRepairWorkModeAdapterResponse(
		context.Background(),
		ModelConfig{},
		adapterRequestPayload{},
		adapters.Response{Text: workerRaw},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("worker silent normalization failed: %v", err)
	}
	if worker.RepairDetail != nil {
		t.Fatalf("worker normalization unexpectedly produced repair details: %#v", worker.RepairDetail)
	}
	if workerResp.Text == workerRaw || !json.Valid([]byte(workerResp.Text)) {
		t.Fatalf("worker response was not returned as normalized JSON: %q", workerResp.Text)
	}

	observerRaw := "{\"reply\":\"first line\nsecond line\",\"has_input\":false,\"recommendations\":[],\"warnings\":[]}"
	observer, observerResp, err := app.parseOrRepairWorkModeObserverResponse(
		context.Background(),
		ModelConfig{},
		adapterRequestPayload{},
		adapters.Response{Text: observerRaw},
	)
	if err != nil {
		t.Fatalf("observer silent normalization failed: %v", err)
	}
	if observer.RepairDetail != nil {
		t.Fatalf("observer normalization unexpectedly produced repair details: %#v", observer.RepairDetail)
	}
	if observerResp.Text == observerRaw || !json.Valid([]byte(observerResp.Text)) {
		t.Fatalf("observer response was not returned as normalized JSON: %q", observerResp.Text)
	}
}

func TestLiteralLineBreakNormalizationDoesNotHideOtherJSONErrors(t *testing.T) {
	raw := "{\"reply\":\"first line\nsecond line\",\"files\":[],}"
	normalized, changed := normalizeLiteralLineBreaksInJSONStringValues(raw)
	if !changed {
		t.Fatal("line break was not normalized")
	}
	if _, err := parseWorkModeAIResponse(normalized); err == nil {
		t.Fatal("normalization must not make unrelated malformed JSON silently valid")
	}
}

func TestRequireWorkModeJSONBoolFieldForWorkerReviewComplete(t *testing.T) {
	if err := requireWorkModeJSONBoolField(`{"reply":"draft","files":[],"review_complete":true}`, "review_complete"); err != nil {
		t.Fatalf("review_complete bool was rejected: %v", err)
	}
	if err := requireWorkModeJSONBoolField(`{"reply":"draft","files":[]}`, "review_complete"); err == nil || !strings.Contains(err.Error(), "review_complete") {
		t.Fatalf("missing review_complete error = %v, want review_complete error", err)
	}
	if err := requireWorkModeJSONBoolField(`{"reply":"draft","files":[],"review_complete":"true"}`, "review_complete"); err == nil || !strings.Contains(err.Error(), "must be a boolean") {
		t.Fatalf("string review_complete error = %v, want boolean type error", err)
	}
}

func TestParseWorkModeAIResponsePreservesMemoryForNonStringValues(t *testing.T) {
	tests := []struct {
		name       string
		memoryJSON string
		wantIssue  bool
	}{
		{name: "string", memoryJSON: `"# Durable memory"`, wantIssue: false},
		{name: "blank string", memoryJSON: `""`, wantIssue: false},
		{name: "null", memoryJSON: `null`, wantIssue: false},
		{name: "empty array", memoryJSON: `[]`, wantIssue: true},
		{name: "object", memoryJSON: `{}`, wantIssue: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{"reply":"ok","files":[],"artifacts":[],"memory":` + tt.memoryJSON + `,"warnings":[]}`
			parsed, err := parseWorkModeAIResponse(raw)
			if err != nil {
				t.Fatalf("parseWorkModeAIResponse: %v", err)
			}
			if tt.memoryJSON == `"# Durable memory"` && parsed.Memory != "# Durable memory" {
				t.Fatalf("memory = %q", parsed.Memory)
			}
			if tt.wantIssue != (parsed.MemoryIssue != "") {
				t.Fatalf("memory issue = %q, wantIssue=%v", parsed.MemoryIssue, tt.wantIssue)
			}
			if tt.wantIssue && parsed.Memory != "" {
				t.Fatalf("invalid memory should be ignored, got %q", parsed.Memory)
			}
		})
	}
}

func TestWorkModeMemoryUpdateWarnsOnlyForMalformedValues(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantMemory  string
		wantWarning bool
	}{
		{name: "missing", raw: `{"reply":"ok","files":[],"artifacts":[],"warnings":[]}`},
		{name: "null", raw: `{"reply":"ok","files":[],"artifacts":[],"memory":null,"warnings":[]}`},
		{name: "empty", raw: `{"reply":"ok","files":[],"artifacts":[],"memory":"","warnings":[]}`},
		{name: "text", raw: `{"reply":"ok","files":[],"artifacts":[],"memory":"# Durable memory","warnings":[]}`, wantMemory: "# Durable memory"},
		{name: "array", raw: `{"reply":"ok","files":[],"artifacts":[],"memory":[],"warnings":[]}`, wantWarning: true},
		{name: "object", raw: `{"reply":"ok","files":[],"artifacts":[],"memory":{},"warnings":[]}`, wantWarning: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := parseWorkModeAIResponse(tt.raw)
			if err != nil {
				t.Fatalf("parseWorkModeAIResponse: %v", err)
			}
			memory, warning := workModeMemoryUpdate(parsed)
			if memory != tt.wantMemory {
				t.Fatalf("memory = %q, want %q", memory, tt.wantMemory)
			}
			if tt.wantWarning != (warning != "") {
				t.Fatalf("warning = %q, wantWarning=%v", warning, tt.wantWarning)
			}
		})
	}
}

func TestParseWorkModeAIResponseAcceptsCaseInsensitiveJSONFence(t *testing.T) {
	for _, language := range []string{"json", "Json", "JSON"} {
		raw := "```" + language + "\n{\"reply\":\"ok\",\"files\":[],\"artifacts\":[],\"memory\":\"\",\"warnings\":[]}\n```"
		parsed, err := parseWorkModeAIResponse(raw)
		if err != nil {
			t.Fatalf("%s fence failed: %v", language, err)
		}
		if parsed.Reply != "ok" {
			t.Fatalf("%s reply = %q", language, parsed.Reply)
		}
	}
}

func TestWriteWorkModeJSONErrorIncludesOriginalAndRepairResponses(t *testing.T) {
	rr := httptest.NewRecorder()
	writeWorkModeJSONError(rr, &workModeJSONRepairError{
		OriginalParseError: "original bad",
		OriginalResponse:   "original raw",
		RepairParseError:   "repair bad",
		RepairResponse:     "repair raw",
	}, "repair raw", diagnosticsEntry{}, nil)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", rr.Code)
	}
	var got workModeJSONErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got.OriginalParseError != "original bad" || got.OriginalResponse != "original raw" || got.RepairParseError != "repair bad" || got.RepairResponse != "repair raw" {
		t.Fatalf("unexpected structured error: %#v", got)
	}
}

func TestAppEffectiveModelMaxOutputTokensUsesAutomaticMaximum(t *testing.T) {
	catalog := knownModelMaxOutputCatalog{
		byExactKey: map[string]int{},
		byAdapterKey: map[string]int{
			maxOutputAdapterKey("openai_responses", "gpt-4.1-mini"): 32768,
		},
	}
	app := &App{knownModelMaxOutputCatalog: catalog}
	if got := app.effectiveModelMaxOutputTokens(ModelConfig{Adapter: "openai_responses", ModelName: "gpt-4.1-mini"}); got != 32768 {
		t.Fatalf("known OpenAI maximum = %d, want 32768", got)
	}
	if got := app.effectiveModelMaxOutputTokens(ModelConfig{Adapter: "anthropic_messages", ModelName: "unknown-claude"}); got != anthropicAutomaticMaxOutputTokens {
		t.Fatalf("unknown Anthropic maximum = %d, want %d", got, anthropicAutomaticMaxOutputTokens)
	}
	if got := app.effectiveModelMaxOutputTokens(ModelConfig{Provider: "anthropic", Adapter: "anthropic_messages", MaxOutputTokens: 12000}); got != 12000 {
		t.Fatalf("custom Anthropic guardrail = %d, want 12000", got)
	}
	if got := app.effectiveModelMaxOutputTokens(ModelConfig{Adapter: "openai_responses", ModelName: "unknown-openai"}); got != 0 {
		t.Fatalf("unknown OpenAI maximum = %d, want provider-managed 0", got)
	}
}

func TestHandleModelMaxOutputTokensPersistsModelSpecificDefaults(t *testing.T) {
	modelsPath := filepath.Join(t.TempDir(), "models.json")
	app := &App{
		cfg: AppConfig{Models: []ModelConfig{
			{ID: 1, Label: "Claude Sonnet", Provider: "anthropic", Adapter: "anthropic", MaxOutputTokens: 0},
			{ID: 2, Label: "Claude Observer", Provider: "anthropic", Adapter: "anthropic", MaxOutputTokens: 12000},
		}},
		modelsPath:         modelsPath,
		modelSchemaVersion: 1,
	}
	body := `{"models":[{"modelId":"1","maxOutputTokens":32000},{"modelId":"2","maxOutputTokens":24000}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/max-output-tokens", strings.NewReader(body))
	rr := httptest.NewRecorder()

	app.handleModelMaxOutputTokens(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if app.cfg.Models[0].MaxOutputTokens != 32000 || app.cfg.Models[1].MaxOutputTokens != 24000 {
		t.Fatalf("model values not updated: %#v", app.cfg.Models)
	}
	persisted, err := os.ReadFile(modelsPath)
	if err != nil {
		t.Fatalf("read persisted models: %v", err)
	}
	if !bytes.Contains(persisted, []byte(`"max_output_tokens": 32000`)) || !bytes.Contains(persisted, []byte(`"max_output_tokens": 24000`)) {
		t.Fatalf("persisted model defaults missing: %s", persisted)
	}
}

func TestBuildWorkModeTranscriptZIPIncludesOfflineBrandingAndAssets(t *testing.T) {
	assetsDir := t.TempDir()
	for _, name := range []string{"frostcandy_logo_font.png", "agentgo_logo.png", "fc_agentgo_yetti.png"} {
		if err := os.WriteFile(filepath.Join(assetsDir, name), []byte("asset-"+name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	req := workModeTranscriptExportRequest{
		Title:         "Demo Transcript",
		ProjectName:   "Camera Project",
		ExportedAt:    "2026-07-15T12:00:00Z",
		BuilderLabel:  "Claude Sonnet",
		BuilderID:     "claude-sonnet-4",
		ObserverLabel: "Gemini Pro",
		ObserverID:    "gemini-2.5-pro",
		BodyHTML: `<article class="transcript-message ai"><div class="work-mode-message-body"><pre><code>&lt;script onload="demo()"&gt;
<span class="agentgo-code-change">let changed = true;</span></code></pre><img src="assets/transcript-image-001.png" alt="Example"></div></article>`,
		TranscriptJSON: json.RawMessage(`{"messages":[{"role":"ai","text":"ok"}]}`),
		Assets:         []workModeTranscriptAsset{{Path: "assets/transcript-image-001.png", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("image"))}},
	}
	archive, filename, err := buildWorkModeTranscriptZIP(req, assetsDir)
	if err != nil {
		t.Fatalf("build transcript zip: %v", err)
	}
	if !strings.HasPrefix(filename, "AgentGO-transcript-camera-project-") || !strings.HasSuffix(filename, ".zip") {
		t.Fatalf("unexpected filename %q", filename)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open transcript zip: %v", err)
	}
	files := map[string][]byte{}
	for _, entry := range zr.File {
		rc, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name] = data
	}
	for _, name := range []string{"index.html", "transcript.json", "assets/frostcandy_logo_font.png", "assets/agentgo_logo.png", "assets/fc_agentgo_yetti.png", "assets/transcript-image-001.png"} {
		if _, ok := files[name]; !ok {
			t.Fatalf("transcript zip missing %s", name)
		}
	}
	index := string(files["index.html"])
	for _, required := range []string{
		`href="https://agentgo.frostcandy.com" target="_blank"`,
		`<img src="assets/frostcandy_logo_font.png" alt="FrostCandy">`,
		`<img src="assets/agentgo_logo.png" alt="AgentGO">`,
		`<h1>AgentGO Work-Mode Transcript</h1>`,
		`AI Builder: Claude Sonnet (claude-sonnet-4) - Observer: Gemini Pro (gemini-2.5-pro)`,
		`Project: Camera Project · Exported 2026-07-15T12:00:00Z`,
		`.transcript-brand{display:flex;flex-direction:column;align-items:center;gap:8px;margin:0 auto 20px`,
		`.transcript-yeti{display:block;width:min(180px,45vw);height:auto;margin:20px auto 0}`,
		`assets/transcript-image-001.png`,
		`&lt;script onload="demo()"&gt;`,
		`<span class="agentgo-code-change">let changed = true;</span>`,
		`.agentgo-code-change{color:#FFE27C}`,
	} {
		if !strings.Contains(index, required) {
			t.Fatalf("transcript index missing %q", required)
		}
	}
}

func TestBuildWorkModeTranscriptZIPRejectsExecutableHTML(t *testing.T) {
	assetsDir := t.TempDir()
	for _, name := range []string{"frostcandy_logo_font.png", "agentgo_logo.png", "fc_agentgo_yetti.png"} {
		if err := os.WriteFile(filepath.Join(assetsDir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := buildWorkModeTranscriptZIP(workModeTranscriptExportRequest{
		BodyHTML:       `<article><script>alert(1)</script></article>`,
		TranscriptJSON: json.RawMessage(`{"messages":[]}`),
	}, assetsDir)
	if err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe transcript error = %v", err)
	}
}

func TestHandleTTSConfigAddsMissingVoiceField(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.json")
	initial := `{"agentgo_file":"config","file_version":1,"bind_host":"0.0.0.0","http_port":5226,"https_port":5227,"tls_cert_file":"","tls_key_file":"","work_root":"work","max_response_history":50,"risk_mode_max_iterations":10,"outfit_run_retention":50,"auto_merge_single_builder_waves":true,"prompt_version":1,"wiretap":{"max_wiretap_entries":200,"default_runtime_slice_entries":75,"max_runtime_slice_entries":150}}`
	if err := os.WriteFile(configPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: AppConfig{WorkRoot: "work"}, configPath: configPath}
	req := httptest.NewRequest(http.MethodPost, "/api/config/tts", strings.NewReader(`{"tts_voice":"Test Female Voice"}`))
	rr := httptest.NewRecorder()
	app.handleTTSConfig(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("saved config is not json: %v", err)
	}
	if got, ok := saved["tts_voice"].(string); !ok || got != "Test Female Voice" {
		t.Fatalf("saved tts_voice = %#v, want Test Female Voice", saved["tts_voice"])
	}
}

func TestWorkModeApplyFileOpsToTmpWorkRoutesDraftsAndRejectsNestedTmpWork(t *testing.T) {
	projectworkRoot := t.TempDir()
	projectName := "demo"
	if err := os.MkdirAll(filepath.Join(projectworkRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectworkRoot, "src", "app.js"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, skipped, blocked, diffs, err := workModeApplyFileOpsToTmpWork(projectworkRoot, projectName, []builderFileOp{
		{Path: "index.html", Action: "create", Content: "<h1>Draft</h1>"},
		{Path: "src/app.js", Action: "overwrite", Content: "draft"},
		{Path: "tmp-work/nested.js", Action: "create", Content: "bad"},
	}, nil, ProjectLimits{MaxFiles: 10, MaxFileSizeKB: 64, MaxPayloadKB: 256})
	if err != nil {
		t.Fatalf("workModeApplyFileOpsToTmpWork failed: %v", err)
	}
	if got := string(mustReadFileForTest(t, filepath.Join(projectworkRoot, "src", "app.js"))); got != "old" {
		t.Fatalf("real project file changed to %q, want old", got)
	}
	if got := string(mustReadFileForTest(t, filepath.Join(projectworkRoot, workModeTmpWorkDirName, "src", "app.js"))); got != "draft" {
		t.Fatalf("tmp-work overwrite = %q, want draft", got)
	}
	if got := string(mustReadFileForTest(t, filepath.Join(projectworkRoot, workModeTmpWorkDirName, "index.html"))); got != "<h1>Draft</h1>" {
		t.Fatalf("tmp-work create = %q", got)
	}
	if _, err := os.Stat(filepath.Join(projectworkRoot, workModeTmpWorkDirName, workModeTmpWorkDirName, "nested.js")); !os.IsNotExist(err) {
		t.Fatalf("nested tmp-work file exists or stat failed unexpectedly: %v", err)
	}
	if !containsTestString(changed, "tmp-work/index.html") || !containsTestString(changed, "tmp-work/src/app.js") {
		t.Fatalf("changed paths = %#v, want tmp-work/index.html and tmp-work/src/app.js", changed)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "tmp-work/nested.js") {
		t.Fatalf("skipped = %#v, want nested tmp-work rejection", skipped)
	}
	if len(blocked) != 1 || blocked[0].Path != "tmp-work/nested.js" {
		t.Fatalf("blocked = %#v, want nested tmp-work blocked output", blocked)
	}
	if len(diffs) == 0 {
		t.Fatalf("expected diffs for draft writes")
	}
}

func TestMergeTmpWorkFileOverwritesTargetAndRemovesDraft(t *testing.T) {
	workRoot := t.TempDir()
	projectworkRoot := filepath.Join(workRoot, "projects", "demo", "projectwork")
	if err := os.MkdirAll(filepath.Join(projectworkRoot, workModeTmpWorkDirName, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectworkRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectworkRoot, "src", "app.js"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectworkRoot, workModeTmpWorkDirName, "src", "app.js"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{cfg: AppConfig{WorkRoot: workRoot}}
	sourcePath, targetPath, err := app.mergeTmpWorkFile("demo", "src/app.js")
	if err != nil {
		t.Fatalf("mergeTmpWorkFile failed: %v", err)
	}
	if sourcePath != "projects/demo/projectwork/tmp-work/src/app.js" {
		t.Fatalf("sourcePath = %q", sourcePath)
	}
	if targetPath != "projects/demo/projectwork/src/app.js" {
		t.Fatalf("targetPath = %q", targetPath)
	}
	if got := string(mustReadFileForTest(t, filepath.Join(projectworkRoot, "src", "app.js"))); got != "draft" {
		t.Fatalf("merged target = %q, want draft", got)
	}
	if _, err := os.Stat(filepath.Join(projectworkRoot, workModeTmpWorkDirName, "src", "app.js")); !os.IsNotExist(err) {
		t.Fatalf("tmp-work draft still exists or stat failed unexpectedly: %v", err)
	}
}

func mustReadFileForTest(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func containsTestString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestDefaultProjectLimitsUseHighOutputDefaults(t *testing.T) {
	got := defaultProjectLimits()
	want := ProjectLimits{MaxFiles: 50, MaxFileSizeKB: 50 * 1024, MaxPayloadKB: 100 * 1024}
	if got != want {
		t.Fatalf("defaultProjectLimits() = %#v, want %#v", got, want)
	}
}

func TestValidateProjectLimitsRejectsTotalBelowSingleFile(t *testing.T) {
	if err := validateProjectLimits(ProjectLimits{MaxFiles: 50, MaxFileSizeKB: 2048, MaxPayloadKB: 1024}); err == nil {
		t.Fatal("validateProjectLimits accepted total output below single-file output")
	}
	if err := validateProjectLimits(ProjectLimits{MaxFiles: 50, MaxFileSizeKB: 2048, MaxPayloadKB: 4096}); err != nil {
		t.Fatalf("validateProjectLimits rejected valid limits: %v", err)
	}
}

func TestEnsureModelProjectScaffoldCreatesActiveProjectLayout(t *testing.T) {
	workRoot := t.TempDir()
	app := &App{cfg: AppConfig{WorkRoot: workRoot}}
	model := ModelConfig{WorkDir: "i22_grok_4_5"}

	if err := app.ensureModelProjectScaffold(model, "Work"); err != nil {
		t.Fatalf("ensureModelProjectScaffold failed: %v", err)
	}

	base := filepath.Join(workRoot, model.WorkDir, "Work")
	for _, dir := range []string{
		filepath.Join(base, "project"),
		filepath.Join(base, "meta"),
		filepath.Join(base, "meta", "reviews"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("expected scaffold directory %s: %v", dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("scaffold path %s is not a directory", dir)
		}
	}

	for _, file := range []string{
		filepath.Join(base, "meta", "user_context.json"),
		filepath.Join(base, "meta", "ai_context.json"),
		filepath.Join(base, "meta", "reviewer_context.json"),
		filepath.Join(base, "meta", chatMemoryFileName),
	} {
		if info, err := os.Stat(file); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("expected scaffold file %s: info=%v err=%v", file, info, err)
		}
	}
}

func TestHandleCreateModelInitializesActiveProjectScaffold(t *testing.T) {
	workRoot := t.TempDir()
	app := &App{
		cfg:                AppConfig{WorkRoot: workRoot},
		modelsPath:         filepath.Join(t.TempDir(), "models.json"),
		modelSchemaVersion: 1,
		toggles:            map[string]bool{},
		activeProjectName:  "Work",
	}
	body := `{"label":"Grok 4.5","provider":"xai","adapter":"xai_imagine","model_name":"grok-imagine-image","base_url":"https://api.x.ai","api_path":"/v1/images/generations","auth_type":"bearer"}`
	req := httptest.NewRequest(http.MethodPost, "/api/models/create", strings.NewReader(body))
	rr := httptest.NewRecorder()

	app.handleCreateModel(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if len(app.cfg.Models) != 1 {
		t.Fatalf("created models = %d, want 1", len(app.cfg.Models))
	}
	model := app.cfg.Models[0]
	for _, path := range []string{
		filepath.Join(workRoot, model.WorkDir, "Work", "project"),
		filepath.Join(workRoot, model.WorkDir, "Work", "meta"),
		filepath.Join(workRoot, model.WorkDir, "Work", "meta", "reviews"),
	} {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			t.Fatalf("expected active-project scaffold directory %s: info=%v err=%v", path, info, err)
		}
	}
}

func TestResolveWorkModeRequestMemoryUsesProjectContinuedMemory(t *testing.T) {
	metaRoot := t.TempDir()
	continuedRoot := t.TempDir()
	continuedPath := filepath.Join(continuedRoot, workModeContinuedMemoryFilename)
	if err := os.WriteFile(continuedPath, []byte("project continued memory"), 0o600); err != nil {
		t.Fatal(err)
	}
	staleBrowserMemory := "stale browser memory must not replace the server file"
	data, name, writePath, persist, err := resolveWorkModeRequestMemory(metaRoot, continuedPath, workModeRequest{
		UseMemory:     true,
		MemoryContent: &staleBrowserMemory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "project continued memory" {
		t.Fatalf("memory = %q, want project continued memory", got)
	}
	if name != "" || writePath != continuedPath || !persist {
		t.Fatalf("continued memory resolved as name=%q path=%q persist=%v", name, writePath, persist)
	}
	if err := writeWorkModeRequestMemory(writePath, persist, "updated continued memory"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(continuedPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(updated); got != "updated continued memory" {
		t.Fatalf("continued memory = %q", got)
	}
}

func TestResolveWorkModeRequestMemorySharesNamedFile(t *testing.T) {
	metaRoot := t.TempDir()
	memoriesRoot, err := workModeMemoriesRoot(metaRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(memoriesRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	displayName, fileName, err := normalizeWorkModeMemoryFileName("Shared Task")
	if err != nil {
		t.Fatal(err)
	}
	memoryPath := filepath.Join(memoriesRoot, fileName)
	if err := os.WriteFile(memoryPath, []byte("shared version one"), 0o644); err != nil {
		t.Fatal(err)
	}
	continuedPath := filepath.Join(t.TempDir(), workModeContinuedMemoryFilename)
	if err := os.WriteFile(continuedPath, []byte("continued"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, name, writePath, persist, err := resolveWorkModeRequestMemory(metaRoot, continuedPath, workModeRequest{
		UseMemory:  true,
		MemoryName: displayName,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "shared version one" {
		t.Fatalf("memory = %q", got)
	}
	if name != displayName || writePath != memoryPath || !persist {
		t.Fatalf("named memory resolved as name=%q path=%q persist=%v", name, writePath, persist)
	}
	if err := writeWorkModeRequestMemory(writePath, persist, "shared version two"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(updated); got != "shared version two" {
		t.Fatalf("named memory = %q, want latest update", got)
	}
}

func TestWorkModeTemplateContainsConversationTabPatchControls(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		`id="workModeTabList"`,
		`id="workModeAddTabBtn"`,
		`data-work-tab-close`,
		`data-work-memory-cancel`,
		`function syncNamedWorkModeMemory`,
		`memoryName`,
		`memoryContent`,
		`sessionOnly: true`,
		`max-height:calc(100vh - 120px)`,
		`padding-bottom:24px`,
		`restoreWorkModeReplyScrolling`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Work Mode template missing %q", required)
		}
	}
}

func TestRevision421StylingPromptClarifications(t *testing.T) {
	for _, required := range []string{
		"Use <pre><code> for copy/paste blocks with real newlines; use <br> only when it is literal code being demonstrated.",
		"Use safe HTML tags instead of Markdown formatting inside replies.",
	} {
		if !strings.Contains(defaultAgentGOStylingPrompt, required) {
			t.Fatalf("default styling prompt missing %q", required)
		}
	}
	for _, unwanted := range []string{
		"Favor HTML tags over boring markdown symbols",
		"Use <pre><code> for copy/paste blocks.",
	} {
		if strings.Contains(defaultAgentGOStylingPrompt, unwanted) {
			t.Fatalf("default styling prompt retained old wording %q", unwanted)
		}
	}
}

func TestWorkModeClearsInvalidArtifactRefWhenInlineContentExists(t *testing.T) {
	raw := `{"reply":"ok","files":[{"path":"code_snippets/php_code_snippet.txt","action":"create","content":"complete inline text","artifact_ref":"php_code_snippet.txt"}],"artifacts":[],"memory":"","warnings":[]}`
	parsed, err := parseWorkModeAdapterResponse(adapters.Response{Text: raw}, t.TempDir(), "Demo")
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if len(parsed.Files) != 1 || parsed.Files[0].ArtifactRef != "" {
		t.Fatalf("invalid artifact_ref was not cleared: %#v", parsed.Files)
	}
	if !strings.Contains(strings.Join(parsed.Normalizations, "\n"), "cleared invalid artifact_ref") {
		t.Fatalf("missing normalization note: %#v", parsed.Normalizations)
	}
}

func TestWorkModeRejectsMissingArtifactWhenInlineContentIsEmpty(t *testing.T) {
	raw := `{"reply":"ok","files":[{"path":"output.bin","action":"create","content":"","artifact_ref":"missing"}],"artifacts":[],"memory":"","warnings":[]}`
	_, err := parseWorkModeAdapterResponse(adapters.Response{Text: raw}, t.TempDir(), "Demo")
	if err == nil || !strings.Contains(err.Error(), "does not match a returned artifact") {
		t.Fatalf("expected missing artifact error, got %v", err)
	}
}

func TestWorkModeModelCorrectionsAreBuilderOnly(t *testing.T) {
	corrections := "- Leave artifact_ref empty when inline content is present."
	instructions := buildWorkModeInstructions(ModelConfig{}, false, false, "auto", nil, "", corrections)
	if !strings.Contains(instructions, "MODEL CORRECTIONS (WORK MODE BUILDER ONLY)") || !strings.Contains(instructions, corrections) {
		t.Fatalf("builder instructions missing corrections:\n%s", instructions)
	}
	observerInstructions := workModeObserverInstructions(ModelConfig{})
	if strings.Contains(observerInstructions, corrections) || strings.Contains(observerInstructions, "MODEL CORRECTIONS (WORK MODE BUILDER ONLY)") {
		t.Fatalf("Observer instructions unexpectedly include Builder corrections:\n%s", observerInstructions)
	}
}

func TestWorkModeCorrectionFailureSummaryIncludesAutomaticRepair(t *testing.T) {
	summary := workModeCorrectionFailureSummary(nil, &workModeJSONRepairDetail{OriginalParseError: "invalid character after top-level value"})
	if !strings.Contains(summary, "automatically repaired invalid Work Mode JSON") || !strings.Contains(summary, "invalid character") {
		t.Fatalf("repair summary = %q", summary)
	}
}

func TestWorkModeCorrectionCandidateSanitizesSecretsAndArtifactData(t *testing.T) {
	model := ModelConfig{ID: 4, Label: "Qwen", APIKey: "secret-token-123"}
	raw := `{"artifacts":[{"id":"sample","encoding":"base64","data":"sensitive-artifact-data"}],"files":[]}`
	candidate := newWorkModeCorrectionCandidate(model, "Invalid JSON", "Return valid JSON.", "authorization=secret-token-123", raw, diagnosticsEntry{ID: "diag-1"})
	if candidate == nil {
		t.Fatal("expected correction candidate")
	}
	combined := candidate.Issue + candidate.Expected + candidate.Failure + candidate.ResponseExcerpt
	if strings.Contains(combined, "secret-token-123") || strings.Contains(combined, "sensitive-artifact-data") {
		t.Fatalf("candidate leaked sensitive data: %#v", candidate)
	}
	if !strings.Contains(candidate.Failure, "[REDACTED]") || !strings.Contains(candidate.ResponseExcerpt, "omitted from diagnostic") {
		t.Fatalf("candidate was not sanitized as expected: %#v", candidate)
	}
}

func TestModelCorrectionsHandlerSavesPerConfiguredModel(t *testing.T) {
	root := t.TempDir()
	app := &App{cfg: AppConfig{WorkRoot: root, Models: []ModelConfig{{ID: 4, Label: "Qwen", WorkDir: "i4_qwen"}, {ID: 5, Label: "Qwen Copy", WorkDir: "i5_qwen"}}}}
	body := strings.NewReader(`{"modelId":"4","content":"- Use files only for durable file work."}`)
	req := httptest.NewRequest(http.MethodPost, "/api/model-corrections", body)
	rr := httptest.NewRecorder()
	app.handleModelCorrections(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("save status = %d: %s", rr.Code, rr.Body.String())
	}
	path := filepath.Join(root, "i4_qwen", modelCorrectionsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read correction file: %v", err)
	}
	if string(data) != "- Use files only for durable file work.\n" {
		t.Fatalf("saved content = %q", string(data))
	}
	if got := app.loadModelCorrections(app.cfg.Models[0]); got != "- Use files only for durable file work." {
		t.Fatalf("loaded corrections = %q", got)
	}
	if got := app.loadModelCorrections(app.cfg.Models[1]); got != "" {
		t.Fatalf("corrections leaked to another configured model: %q", got)
	}
}

func TestRevision421TemplateIncludesModelCorrectionsWorkflow(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		`data-model-action="corrections"`,
		`id="modelCorrectionsModal"`,
		`Suggest a correction for this AI`,
		`/api/model-corrections/draft`,
		`They apply whenever this configured model is used as an AI Builder in Work Mode.`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("template missing %q", required)
		}
	}
}

func TestRevision422CorrectionModalStacksAboveWorkModeBelowEmergencyStop(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		`.work-mode-overlay{--work-pink:#f5a6d7;--work-pink-strong:#d14ba0;--work-blue:#4aa3ff;--work-yellow:#ffe27c;position:fixed;inset:0;z-index:2100;`,
		`.model-corrections-modal{position:fixed;inset:0;z-index:2190;`,
		`z-index: 2220;`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("correction modal stacking missing %q", required)
		}
	}
}

func TestRevision422CorrectionActionsUseAdaptiveRows(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("templates", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, required := range []string{
		`class="work-mode-normalization-row"`,
		`class="work-mode-correction-footer`,
		`function renderWorkModeNormalizationRow(normalized, candidate, messageIndex)`,
		`function renderWorkModeCorrectionFooter(candidate, messageIndex, variant = '')`,
		`title="Suggest a correction for this AI">Suggest correction</button>`,
		`renderWorkModeCorrectionFooter(msg.correction, index, 'warning')`,
		`renderWorkModeCorrectionFooter(msg.correction, index, 'agentgo')`,
		`${actions}<div class="work-mode-message-ai-content"><div>${text}</div>${normalized}${warnings}${correction}${repairs}${inlineFiles}${files}</div>${toggle}`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("adaptive correction layout missing %q", required)
		}
	}
}

func TestWorkModeSmallModelPolishPayloadAvoidsNativeSchemaDuplication(t *testing.T) {
	raw := `{"reply":"plain","files":[],"artifacts":[],"memory":"","warnings":[]}`
	userRequest := "Create the requested project file."
	payload := buildWorkModeSmallModelPolishPayload(false, "CUSTOM SAVED STYLING", workModeJSONSchema(), userRequest, raw)
	for _, required := range []string{
		"AGENTGO SMALL MODEL POLISH PASS",
		`Revise only the "reply" field`,
		"AGENTGO STYLING INSTRUCTIONS:\nCUSTOM SAVED STYLING",
		`Return only one complete AgentGO Work Mode JSON object.`,
	} {
		if !strings.Contains(payload.Instructions, required) {
			t.Fatalf("native polish instructions missing %q:\n%s", required, payload.Instructions)
		}
	}
	for _, duplicated := range []string{"REQUIRED AGENTGO WORK MODE JSON SCHEMA:", `"required": [`, "PROMPT-ENFORCED JSON CONTRACT:"} {
		if strings.Contains(payload.Instructions, duplicated) {
			t.Fatalf("native polish instructions duplicated schema/contract %q:\n%s", duplicated, payload.Instructions)
		}
	}
	if len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || !strings.Contains(payload.Messages[0].Text, "ORIGINAL USER REQUEST:\n"+userRequest) || !strings.Contains(payload.Messages[0].Text, "AI BUILDER RESPONSE TO POLISH:\n"+raw) {
		t.Fatalf("polish response message=%+v", payload.Messages)
	}
	if !payload.ExpectJSON || payload.JSONSchema == nil {
		t.Fatalf("polish payload does not retain provider schema: %+v", payload)
	}
}

func TestWorkModeSmallModelPolishPayloadKeepsPromptEnforcedContract(t *testing.T) {
	payload := buildWorkModeSmallModelPolishPayload(true, "CUSTOM SAVED STYLING", workModeJSONSchema(), "Fix it.", `{"reply":"plain","files":[],"artifacts":[],"memory":"","warnings":[]}`)
	for _, required := range []string{
		"PROMPT-ENFORCED JSON CONTRACT:",
		"CRITICAL JSON CONTRACT",
		`{"reply":"User-facing response or summary of actions here.","files":[],"artifacts":[],"memory":"","warnings":[]}`,
	} {
		if !strings.Contains(payload.Instructions, required) {
			t.Fatalf("prompt-enforced polish instructions missing %q:\n%s", required, payload.Instructions)
		}
	}
	if strings.Contains(payload.Instructions, "REQUIRED AGENTGO WORK MODE JSON SCHEMA:") {
		t.Fatalf("prompt-enforced polish instructions duplicated the full schema:\n%s", payload.Instructions)
	}
}

func TestWorkModeHTMLReplySettingAndHiddenPolishPassWiring(t *testing.T) {
	raw, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	template := string(raw)
	for _, required := range []string{
		`id="workModeAllowHTMLRepliesToggle"`,
		`Allow AI to send HTML in replies`,
		`allowAIHTMLReplies: !!settings.allow_ai_html_replies`,
		`smallModelPolishPass: false`,
		`smallModelPolishPass: false,`,
		`function saveWorkModeAllowAIHTMLReplies(enabled)`,
	} {
		if !strings.Contains(template, required) {
			t.Fatalf("HTML reply/hidden polish wiring missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`id="workModeSmallModelPolishToggle"`,
		`<strong>Small Model Polish Pass</strong>`,
		`workModeSettings.smallModelPolishPass = !!e.target.checked`,
	} {
		if strings.Contains(template, forbidden) {
			t.Fatalf("hidden Small Model Polish Pass UI still contains %q", forbidden)
		}
	}
}

func TestPreserveWorkModeSmallModelPolishDecisionsChangesOnlyReply(t *testing.T) {
	original := `{"reply":"plain","files":[{"path":"notes.txt","action":"create","content":"keep","artifact_ref":""}],"artifacts":[],"memory":"keep memory","warnings":["keep warning"]}`
	polished := `{"reply":"<p>styled</p>","files":[],"artifacts":[{"id":"new","encoding":"base64","mime_type":"text/plain","data":"bmV3"}],"memory":"changed","warnings":[],"extra":"not allowed"}`
	preserved, ok := preserveWorkModeSmallModelPolishDecisions(original, polished, workModeJSONSchema())
	if !ok {
		t.Fatal("valid responses were not merged")
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(preserved), &result); err != nil {
		t.Fatalf("decode merged response: %v", err)
	}
	var reply string
	if err := json.Unmarshal(result["reply"], &reply); err != nil || reply != "<p>styled</p>" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	for _, key := range []string{"files", "artifacts", "memory", "warnings"} {
		var originalValue map[string]json.RawMessage
		if err := json.Unmarshal([]byte(original), &originalValue); err != nil {
			t.Fatal(err)
		}
		if string(result[key]) != string(originalValue[key]) {
			t.Fatalf("%s changed: got=%s want=%s", key, result[key], originalValue[key])
		}
	}
	if _, exists := result["extra"]; exists {
		t.Fatalf("polish-added field was retained: %s", preserved)
	}
}

func TestPreserveWorkModeSmallModelPolishDecisionsAllowsStructuralRepair(t *testing.T) {
	original := `{"reply":"plain","files":"not-an-array","artifacts":[],"memory":"","warnings":[],"unexpected":"remove"}`
	polished := `{"reply":"<p>styled</p>","files":[],"artifacts":[],"memory":"","warnings":[]}`
	preserved, ok := preserveWorkModeSmallModelPolishDecisions(original, polished, workModeJSONSchema())
	if !ok {
		t.Fatal("valid polish result was not retained")
	}
	parsed, err := parseWorkModeAIResponse(preserved)
	if err != nil {
		t.Fatalf("structural repair was lost: %v\n%s", err, preserved)
	}
	if parsed.Reply != "<p>styled</p>" || len(parsed.Files) != 0 {
		t.Fatalf("parsed=%+v", parsed)
	}
	if strings.Contains(preserved, "unexpected") || strings.Contains(preserved, "not-an-array") {
		t.Fatalf("invalid original structure was restored: %s", preserved)
	}
}
