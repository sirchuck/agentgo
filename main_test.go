package main

import (
	"encoding/json"
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
