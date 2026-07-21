package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func agenticEnvelope(status string, command workModeAgenticCommand, summary, question string) string {
	payload := map[string]any{
		"reply":          "Visible reply",
		"files":          []any{},
		"artifacts":      []any{},
		"memory":         "",
		"warnings":       []any{},
		"agentic_status": status,
		"command":        command,
		"summary":        summary,
		"question":       question,
		"workspace":      map[string]any{},
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func emptyAgenticCommand() workModeAgenticCommand {
	return workModeAgenticCommand{Type: "", Executable: "", Args: []string{}, Script: "", WorkingDirectory: "", Purpose: ""}
}

func TestNormalizeWorkModeAgenticRequest(t *testing.T) {
	disabled, err := normalizeWorkModeAgenticRequest(workModeAgenticRequest{})
	if err != nil || disabled.Enabled {
		t.Fatalf("disabled = %+v err=%v", disabled, err)
	}
	full, err := normalizeWorkModeAgenticRequest(workModeAgenticRequest{Enabled: true, Mode: " FULL ", MaxRuns: 0})
	if err != nil || full.Mode != "full" || full.MaxRuns != 50 {
		t.Fatalf("full = %+v err=%v", full, err)
	}
	if _, err := normalizeWorkModeAgenticRequest(workModeAgenticRequest{Enabled: true, Mode: "unknown"}); err == nil {
		t.Fatal("unknown mode was accepted")
	}
}

func TestWorkModeAgenticSchemaIsSeparateFromNormalWorkMode(t *testing.T) {
	normalProps := workModeJSONSchema()["properties"].(map[string]any)
	if _, ok := normalProps["agentic_status"]; ok {
		t.Fatal("normal Work Mode schema unexpectedly contains agentic fields")
	}
	agentic := workModeAgenticJSONSchema()
	props := agentic["properties"].(map[string]any)
	for _, field := range []string{"agentic_status", "command", "summary", "question", "workspace"} {
		if _, ok := props[field]; !ok {
			t.Fatalf("agentic schema missing %s", field)
		}
	}
	workspaceSchema, ok := props["workspace"].(map[string]any)
	if !ok || workspaceSchema["additionalProperties"] != false {
		t.Fatalf("workspace schema must be a strict object: %#v", props["workspace"])
	}
	workspaceProps, _ := workspaceSchema["properties"].(map[string]any)
	factsSchema, ok := workspaceProps["facts"].(map[string]any)
	if !ok || factsSchema["type"] != "array" {
		t.Fatalf("workspace facts schema = %#v", workspaceProps["facts"])
	}
}

func TestAgenticEnvironmentWorkspaceValidation(t *testing.T) {
	valid := map[string]any{"facts": []any{
		map[string]any{"name": "ruby.version", "value": "3.3.1"},
		map[string]any{"name": "puma.installed", "value": "true"},
	}}
	if err := validateAgenticEnvironmentWorkspace(valid); err != nil {
		t.Fatalf("valid workspace rejected: %v", err)
	}
	for name, workspace := range map[string]map[string]any{
		"unknown top-level field": {"tools": map[string]any{}},
		"unknown fact field":      {"facts": []any{map[string]any{"name": "ruby.version", "value": "3.3.1", "source": "ruby --version"}}},
		"duplicate fact":          {"facts": []any{map[string]any{"name": "ruby.version", "value": "3.3.1"}, map[string]any{"name": "ruby.version", "value": "3.3.2"}}},
	} {
		if err := validateAgenticEnvironmentWorkspace(workspace); err == nil {
			t.Fatalf("%s was accepted: %#v", name, workspace)
		}
	}
}

func TestParseAgenticEmptyWorkspaceNormalizesToFactsArray(t *testing.T) {
	parsed, validation, err := parseAndValidateWorkModeAgenticResponse(agenticEnvelope(workModeAgenticStatusComplete, emptyAgenticCommand(), "Finished", ""))
	if err != nil || len(validation) != 0 {
		t.Fatalf("parse validation=%v err=%v", validation, err)
	}
	if _, ok := parsed.AgenticWorkspace["facts"]; !ok {
		t.Fatalf("workspace was not normalized: %#v", parsed.AgenticWorkspace)
	}
}

func TestWorkModeAgenticInstructionsOnlyWhenEnabledAndHideValues(t *testing.T) {
	base := "BASE WORK MODE"
	if got := buildWorkModeAgenticInstructions(base, workModeAgenticRequest{}, nil); got != base {
		t.Fatalf("disabled instructions changed: %q", got)
	}
	environment := []workModeAgenticEnvironmentDescriptor{{Name: "DEPLOY_TOKEN", Description: "Deployment credential"}}
	got := buildWorkModeAgenticInstructions(base, workModeAgenticRequest{Enabled: true, Mode: "manual", MaxRuns: 50}, environment)
	for _, required := range []string{
		"AGENTGO AGENTIC TERMINAL PROTOCOL", "agentic_status", "DEPLOY_TOKEN", "Deployment credential", "FULL / SEMI / MANUAL MODE",
		agenticExecutionEnvironmentInstruction(),
		"Store only command-verified environment facts in the workspace JSON object",
		`"workspace": {"facts": []}`,
	} {
		if !strings.Contains(got, required) {
			t.Fatalf("instructions missing %q", required)
		}
	}
	if strings.Contains(got, "actual-secret-value") {
		t.Fatal("instructions leaked environment value")
	}
	if strings.Contains(strings.ToLower(got), "phase 6") {
		t.Fatal("agentic instructions exposed internal development-phase wording")
	}
}

func TestTerminalEnvironmentAgenticDescriptorsNeverIncludeValues(t *testing.T) {
	file := terminalEnvironmentFile{Variables: []terminalEnvironment{{Name: "PORT", Value: "8080", Description: "Local port"}, {Name: "TOKEN", Value: "actual-secret-value", Description: "Credential"}}}
	descriptors := terminalEnvironmentAgenticDescriptors(file)
	data, err := json.Marshal(descriptors)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "8080") || strings.Contains(text, "actual-secret-value") {
		t.Fatalf("descriptor leaked values: %s", text)
	}
	if !strings.Contains(text, "PORT") || !strings.Contains(text, "TOKEN") {
		t.Fatalf("descriptor missing names: %s", text)
	}
}

func TestParseAndValidateWorkModeAgenticDirectCommand(t *testing.T) {
	raw := agenticEnvelope(workModeAgenticStatusRunCommand, workModeAgenticCommand{
		Type: workModeAgenticCommandDirect, Executable: "go", Args: []string{"test", "./..."}, Script: "", WorkingDirectory: ".", Purpose: "Run tests",
	}, "Work started; next command requested.", "")
	parsed, validation, err := parseAndValidateWorkModeAgenticResponse(raw)
	if err != nil || len(validation) != 0 {
		t.Fatalf("parse validation=%v err=%v", validation, err)
	}
	if parsed.AgenticCommand.Executable != "go" || len(parsed.AgenticCommand.Args) != 2 {
		t.Fatalf("command = %+v", parsed.AgenticCommand)
	}
}

func TestParseAndValidateWorkModeAgenticStates(t *testing.T) {
	complete := agenticEnvelope(workModeAgenticStatusComplete, emptyAgenticCommand(), "Finished", "")
	if _, validation, err := parseAndValidateWorkModeAgenticResponse(complete); err != nil || len(validation) != 0 {
		t.Fatalf("complete validation=%v err=%v", validation, err)
	}
	question := agenticEnvelope(workModeAgenticStatusNeedsUserInput, emptyAgenticCommand(), "Need style choice; no file changes yet.", "Which style?")
	if _, validation, err := parseAndValidateWorkModeAgenticResponse(question); err != nil || len(validation) != 0 {
		t.Fatalf("needs input validation=%v err=%v", validation, err)
	}
}

func TestAgenticProtocolRequiresCumulativeSummaryForEveryState(t *testing.T) {
	cases := []string{
		agenticEnvelope(workModeAgenticStatusRunCommand, workModeAgenticCommand{Type: workModeAgenticCommandDirect, Executable: "go", Args: []string{"test", "./..."}, WorkingDirectory: ".", Purpose: "Run tests"}, "", ""),
		agenticEnvelope(workModeAgenticStatusNeedsUserInput, emptyAgenticCommand(), "", "Which style?"),
		agenticEnvelope(workModeAgenticStatusComplete, emptyAgenticCommand(), "", ""),
	}
	for _, raw := range cases {
		_, validation, err := parseAndValidateWorkModeAgenticResponse(raw)
		if err != nil {
			t.Fatalf("parse error=%v", err)
		}
		if len(validation) == 0 || !strings.Contains(strings.ToLower(strings.Join(validation, " ")), "summary") {
			t.Fatalf("missing summary was accepted: validation=%v", validation)
		}
	}
}

func TestAgenticProtocolRejectsMarkdownOrProseWrappedJSON(t *testing.T) {
	raw := agenticEnvelope(workModeAgenticStatusComplete, emptyAgenticCommand(), "Finished", "")
	for _, wrapped := range []string{"```json\n" + raw + "\n```", "Here is the response:\n" + raw} {
		if _, _, err := parseAndValidateWorkModeAgenticResponse(wrapped); err == nil || !strings.Contains(err.Error(), "raw JSON object") {
			t.Fatalf("wrapped response error = %v", err)
		}
	}
}

func TestAgenticProtocolRejectsMissingOrAmbiguousFields(t *testing.T) {
	missing := `{"reply":"x","files":[],"artifacts":[],"memory":"","warnings":[],"agentic_status":"complete","summary":"done","question":"","workspace":{}}`
	if _, _, err := parseAndValidateWorkModeAgenticResponse(missing); err == nil || !strings.Contains(err.Error(), "command") {
		t.Fatalf("missing command error = %v", err)
	}
	extra := `{"reply":"x","files":[],"artifacts":[],"memory":"","warnings":[],"agentic_status":"complete","command":{"type":"","executable":"","args":[],"script":"","working_directory":"","purpose":"","second_command":"bad"},"summary":"done","question":"","workspace":{}}`
	if _, _, err := parseAndValidateWorkModeAgenticResponse(extra); err == nil || !strings.Contains(err.Error(), "second_command") {
		t.Fatalf("extra command error = %v", err)
	}
	shell := agenticEnvelope(workModeAgenticStatusRunCommand, workModeAgenticCommand{Type: "shell", Executable: "sh", Args: []string{"-c"}, Script: "go test ./...", WorkingDirectory: ".", Purpose: "Run tests"}, "Work started; next command requested.", "")
	if _, validation, err := parseAndValidateWorkModeAgenticResponse(shell); err != nil || len(validation) == 0 {
		t.Fatalf("ambiguous shell validation=%v err=%v", validation, err)
	}
}

func TestAgenticProtocolAllowsPhaseFourCStagedFileOperations(t *testing.T) {
	payload := map[string]any{
		"reply": "x", "files": []any{map[string]any{"path": "main.go", "action": "overwrite", "content": "package main", "artifact_ref": ""}},
		"artifacts": []any{}, "memory": "", "warnings": []any{}, "agentic_status": "complete",
		"command": emptyAgenticCommand(), "summary": "done", "question": "", "workspace": map[string]any{},
	}
	data, _ := json.Marshal(payload)
	parsed, validation, err := parseAndValidateWorkModeAgenticResponse(string(data))
	if err != nil || len(validation) != 0 || len(parsed.Files) != 1 {
		t.Fatalf("parsed files=%d validation=%v err=%v", len(parsed.Files), validation, err)
	}
}

func TestNormalizeWorkModeAgenticRequestPreservesValidTaskID(t *testing.T) {
	normalized, err := normalizeWorkModeAgenticRequest(workModeAgenticRequest{Enabled: true, Mode: "manual", MaxRuns: 50, TaskID: "task-123"})
	if err != nil || normalized.TaskID != "task-123" {
		t.Fatalf("normalized=%+v err=%v", normalized, err)
	}
	if _, err := normalizeWorkModeAgenticRequest(workModeAgenticRequest{Enabled: true, Mode: "manual", TaskID: "../bad"}); err == nil {
		t.Fatal("invalid task id was accepted")
	}
}

func TestAgenticAndObserverAreMutuallyExclusive(t *testing.T) {
	req := workModeRequest{ObserverReview: true}
	err := validateWorkModeAgenticRequestCompatibility(req, workModeAgenticRequest{Enabled: true, Mode: "manual", MaxRuns: 50})
	if err == nil || !strings.Contains(err.Error(), "Observer") {
		t.Fatalf("compatibility error = %v", err)
	}
}

func TestPhaseSixAllowsFullSemiAndManual(t *testing.T) {
	for _, mode := range []string{"full", "semi", "manual"} {
		if err := validateWorkModeAgenticRequestCompatibility(workModeRequest{}, workModeAgenticRequest{Enabled: true, Mode: mode, MaxRuns: 50}); err != nil {
			t.Fatalf("mode %s compatibility error = %v", mode, err)
		}
	}
}

func newAgenticHandlerTestApp(t *testing.T, endpoint string) *App {
	t.Helper()
	workRoot := t.TempDir()
	model := ModelConfig{
		ID:        1,
		Label:     "Test Builder",
		Provider:  "custom",
		Adapter:   "custom_json",
		WorkDir:   "models/test-builder",
		BaseURL:   endpoint,
		ModelName: "test-builder",
		Capabilities: ModelCapabilities{
			SupportsTextIn: true,
		},
	}
	app := &App{
		cfg:                       AppConfig{WorkRoot: workRoot, Models: []ModelConfig{model}},
		toggles:                   map[string]bool{"1": true},
		activeProjectName:         "Demo",
		activeCancels:             map[string]activeCancelEntry{},
		workModeSessionsByProject: map[string]workModeSessionState{},
	}
	if err := app.ensureProjectScaffold("Demo"); err != nil {
		t.Fatalf("ensure project scaffold: %v", err)
	}
	return app
}

func TestHandleWorkModeSendAgenticManualApprovalInjectsProtocolWithoutEnvironmentValues(t *testing.T) {
	var receivedInstructions string
	var receivedMessages string
	responseText := agenticEnvelope(workModeAgenticStatusRunCommand, workModeAgenticCommand{
		Type: workModeAgenticCommandDirect, Executable: "go", Args: []string{"test", "./..."}, Script: "", WorkingDirectory: ".", Purpose: "Run project tests",
	}, "Work started; next command requested.", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		receivedInstructions, _ = payload["instructions"].(string)
		messagesJSON, _ := json.Marshal(payload["messages"])
		receivedMessages = string(messagesJSON)
		writeJSON(w, http.StatusOK, map[string]any{"text": responseText})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	if err := app.saveTerminalEnvironment("Demo", terminalEnvironmentFile{
		SchemaVersion: terminalConfigSchemaVersion,
		Variables:     []terminalEnvironment{{Name: "DEPLOY_TOKEN", Value: "actual-secret-value", Description: "Deployment credential"}},
	}); err != nil {
		t.Fatalf("save terminal environment: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"modelId": "1",
		"prompt":  "Run the project tests.",
		"agentic": map[string]any{"enabled": true, "mode": "manual", "maxRuns": 50},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response workModeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Agentic == nil || response.Agentic.Status != workModeAgenticStatusRunCommand || response.Agentic.DryRun || !response.Agentic.Paused || !response.Agentic.ApprovalRequired {
		t.Fatalf("agentic result=%+v", response.Agentic)
	}
	if response.Agentic.Command == nil || response.Agentic.Command.Executable != "go" {
		t.Fatalf("agentic command=%+v", response.Agentic.Command)
	}
	if response.Agentic.Workspace == nil || response.Agentic.Workspace.PendingCommand == nil || response.Agentic.Workspace.PendingCommand.Executable != "go" {
		t.Fatalf("pending command was not stored in staged task metadata: %+v", response.Agentic.Workspace)
	}
	for _, required := range []string{"AGENTGO AGENTIC TERMINAL PROTOCOL", "DEPLOY_TOKEN", "Deployment credential", "Manual mode is selected"} {
		if !strings.Contains(receivedInstructions, required) {
			t.Fatalf("provider instructions missing %q", required)
		}
	}
	if strings.Contains(receivedInstructions, "actual-secret-value") {
		t.Fatal("provider instructions leaked terminal environment value")
	}
	for _, required := range []string{"AGENTGO AGENTIC TASK CONTINUITY", "ORIGINAL USER TASK", "Run the project tests.", "first turn; no prior progress summary", "no command result yet"} {
		if !strings.Contains(receivedMessages, required) {
			t.Fatalf("provider messages missing %q: %s", required, receivedMessages)
		}
	}
}

func TestHandleWorkModeSendNormalModeDoesNotInjectAgenticProtocol(t *testing.T) {
	var receivedInstructions string
	var receivedMessages string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		receivedInstructions, _ = payload["instructions"].(string)
		messagesJSON, _ := json.Marshal(payload["messages"])
		receivedMessages = string(messagesJSON)
		writeJSON(w, http.StatusOK, map[string]any{"text": `{"reply":"Normal Work Mode reply","files":[],"artifacts":[],"memory":"","warnings":[]}`})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	body, _ := json.Marshal(map[string]any{"modelId": "1", "prompt": "Answer without terminal mode."})
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response workModeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Agentic != nil {
		t.Fatalf("normal Work Mode returned agentic result: %+v", response.Agentic)
	}
	if strings.Contains(receivedInstructions, "AGENTGO AGENTIC TERMINAL PROTOCOL") || strings.Contains(receivedInstructions, "agentic_status") {
		t.Fatal("normal Work Mode received agentic protocol instructions")
	}
	if strings.Contains(receivedMessages, "AGENTGO AGENTIC TASK CONTINUITY") || strings.Contains(receivedMessages, "ROLLING CUMULATIVE PROGRESS SUMMARY") {
		t.Fatal("normal Work Mode received agentic continuity context")
	}
}

func TestHandleWorkModeSendMalformedAgenticResponsePausesWithoutRepairCall(t *testing.T) {
	calls := 0
	valid := agenticEnvelope(workModeAgenticStatusComplete, emptyAgenticCommand(), "Finished", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, http.StatusOK, map[string]any{"text": "```json\n" + valid + "\n```"})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	body, _ := json.Marshal(map[string]any{
		"modelId": "1",
		"prompt":  "Finish the task.",
		"agentic": map[string]any{"enabled": true, "mode": "manual", "maxRuns": 50},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if calls != 1 {
		t.Fatalf("provider calls=%d, want 1 without automatic repair", calls)
	}
	var response workModeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Agentic == nil || response.Agentic.Status != workModeAgenticStatusProtocolError || !response.Agentic.Paused {
		t.Fatalf("agentic result=%+v", response.Agentic)
	}
}

func TestHandleWorkModeSendSemiBuiltInCommandReturnsAutomaticExecutionHandoff(t *testing.T) {
	var receivedInstructions string
	responseText := agenticEnvelope(workModeAgenticStatusRunCommand, workModeAgenticCommand{
		Type: workModeAgenticCommandDirect, Executable: "go", Args: []string{"version"}, Script: "", WorkingDirectory: ".", Purpose: "Read the Go version",
	}, "Work started; next command requested.", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		receivedInstructions, _ = payload["instructions"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"text": responseText})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	body, _ := json.Marshal(map[string]any{
		"modelId": "1", "prompt": "Check the Go version.",
		"agentic": map[string]any{"enabled": true, "mode": "semi", "maxRuns": 50},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response workModeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Agentic == nil || !response.Agentic.AutoExecute || response.Agentic.ApprovalRequired || response.Agentic.Authorization != agenticSemiAuthorizationBuiltIn || response.Agentic.Execution != nil {
		t.Fatalf("agentic=%+v", response.Agentic)
	}
	if response.Agentic.Workspace == nil || response.Agentic.Workspace.Mode != workModeAgenticModeSemi || response.Agentic.Workspace.PendingCommand == nil {
		t.Fatalf("workspace=%+v", response.Agentic.Workspace)
	}
	if !strings.Contains(receivedInstructions, "Semi mode is selected") || strings.Contains(strings.ToLower(receivedInstructions), "phase 6") {
		t.Fatalf("instructions=%s", receivedInstructions)
	}
}

func TestHandleWorkModeSendSemiUnlistedCommandPausesWithFourDecisions(t *testing.T) {
	responseText := agenticEnvelope(workModeAgenticStatusRunCommand, workModeAgenticCommand{
		Type: workModeAgenticCommandDirect, Executable: "go", Args: []string{"test", "./..."}, Script: "", WorkingDirectory: ".", Purpose: "Run tests",
	}, "Work started; next command requested.", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"text": responseText})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	body, _ := json.Marshal(map[string]any{
		"modelId": "1", "prompt": "Run tests.",
		"agentic": map[string]any{"enabled": true, "mode": "semi", "maxRuns": 50},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response workModeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want := []string{agenticSemiDecisionAllowOnce, agenticSemiDecisionAllowSession, agenticSemiDecisionAddWhitelist, agenticSemiDecisionDeny}
	if response.Agentic == nil || response.Agentic.AutoExecute || !response.Agentic.ApprovalRequired || response.Agentic.Authorization != agenticSemiAuthorizationNeedsUser || len(response.Agentic.ApprovalOptions) != len(want) {
		t.Fatalf("agentic=%+v", response.Agentic)
	}
	for i := range want {
		if response.Agentic.ApprovalOptions[i] != want[i] {
			t.Fatalf("approval options=%v want=%v", response.Agentic.ApprovalOptions, want)
		}
	}
}

func TestAgenticProtocolRejectsOversizedCumulativeSummary(t *testing.T) {
	raw := agenticEnvelope(workModeAgenticStatusComplete, emptyAgenticCommand(), strings.Repeat("x", agenticProgressSummaryLimit+1), "")
	_, validation, err := parseAndValidateWorkModeAgenticResponse(raw)
	if err != nil {
		t.Fatalf("parse error=%v", err)
	}
	if len(validation) == 0 || !strings.Contains(strings.Join(validation, " "), "at most 4000 characters") {
		t.Fatalf("oversized summary validation=%v", validation)
	}
}

func TestHandleWorkModeAgenticContinuationUsesStoredContextWithoutDuplicatingTerminalPrompt(t *testing.T) {
	var receivedPrompt string
	responseText := agenticEnvelope(workModeAgenticStatusNeedsUserInput, emptyAgenticCommand(), "Tests pass; waiting for confirmation.", "Should I finalize the task?")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		receivedPrompt, _ = payload["prompt"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"text": responseText})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeFull, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.prepareAgenticTaskContinuity("Demo", task.SessionID, "Fix the widget.", false); err != nil {
		t.Fatal(err)
	}
	terminalResult := "AGENTGO FULL TERMINAL RESULT\nCommand: go test ./...\nExit code: 0"
	if _, err := app.saveAgenticLatestCommandResult("Demo", task.SessionID, terminalResult); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"modelId": "1", "prompt": terminalResult,
		"agentic": map[string]any{"enabled": true, "mode": "full", "maxRuns": 50, "taskId": task.SessionID, "continuation": true},
	})
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, required := range []string{"AGENTGO AGENTIC TASK CONTINUITY", "Fix the widget.", terminalResult, "FINAL USER REQUEST:", "Continue the same staged agentic task using the AgentGO continuity context above."} {
		if !strings.Contains(receivedPrompt, required) {
			t.Fatalf("provider prompt missing %q:\n%s", required, receivedPrompt)
		}
	}
	if strings.Count(receivedPrompt, terminalResult) != 1 {
		t.Fatalf("terminal result duplicated in provider prompt (%d occurrences)", strings.Count(receivedPrompt, terminalResult))
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OriginalPrompt != "Fix the widget." || loaded.LatestUserInstruction != "Fix the widget." {
		t.Fatalf("automatic continuation replaced stored user intent: %+v", loaded)
	}
}

func TestEnforceWorkModeAgenticContextIsolation(t *testing.T) {
	memory := "temporary memory"
	enabled := true
	req := workModeRequest{
		IncludeRoleContext: &enabled,
		ResponseMode:       "skeptic",
		UseMemory:          true,
		MemoryName:         "Project Memory",
		MemoryContent:      &memory,
		UseAgentGOStyling:  true,
		Agentic:            workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50},
	}
	model := ModelConfig{UseUggPrompt: true}
	enforceWorkModeAgenticContextIsolation(&req, &model)
	if req.IncludeRoleContext == nil || *req.IncludeRoleContext {
		t.Fatal("agentic role context was not disabled")
	}
	if req.ResponseMode != "auto" || req.UseMemory || req.MemoryName != "" || req.MemoryContent != nil || req.UseAgentGOStyling {
		t.Fatalf("agentic request settings were not isolated: %+v", req)
	}
	if model.UseUggPrompt {
		t.Fatal("model-wide Ugg remained enabled for agentic request")
	}
}

func TestHandleWorkModeSendAgenticIgnoresNormalWorkModeContextAndPreservesMemory(t *testing.T) {
	var receivedInstructions string
	var receivedMessages string
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(agenticEnvelope(workModeAgenticStatusComplete, emptyAgenticCommand(), "Task finished; tests pass.", "")), &payload); err != nil {
		t.Fatal(err)
	}
	payload["memory"] = "AGENTIC MEMORY MUST NOT BE SAVED"
	responseData, _ := json.Marshal(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var providerPayload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&providerPayload); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		receivedInstructions, _ = providerPayload["instructions"].(string)
		messagesJSON, _ := json.Marshal(providerPayload["messages"])
		receivedMessages = string(messagesJSON)
		writeJSON(w, http.StatusOK, map[string]any{"text": string(responseData)})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	app.cfg.Models[0].UseUggPrompt = true
	_, metaRoot, err := app.projectPaths(app.cfg.Models[0], "Demo")
	if err != nil {
		t.Fatal(err)
	}
	roleSentinel := "ROLE_CONTEXT_SENTINEL_AGENTIC"
	memorySentinel := "MEMORY_SENTINEL_AGENTIC"
	if err := os.WriteFile(filepath.Join(metaRoot, "user_context.json"), []byte(roleSentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	memoryPath := filepath.Join(metaRoot, chatMemoryFileName)
	if err := os.WriteFile(memoryPath, []byte(memorySentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	includeRole := true
	memoryContent := "BROWSER_MEMORY_CONTENT_SENTINEL"
	body, _ := json.Marshal(workModeRequest{
		ModelID:            "1",
		Prompt:             "Fix the widget.",
		IncludeRoleContext: &includeRole,
		ResponseMode:       "skeptic",
		UseMemory:          true,
		MemoryContent:      &memoryContent,
		UseAgentGOStyling:  true,
		Agentic:            workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50},
	})
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, forbidden := range []string{
		roleSentinel,
		memorySentinel,
		memoryContent,
		"ROLE CONTEXT\n- AgentGO included",
		"MEMORY\n- AgentGO included memory.md",
		"RESPONSE MODE",
		"AGENTGO STYLING",
		"SYSTEM INSTRUCTION: UGG PROTOCOL",
		"Only apply AgentGO Styling Protocol",
	} {
		if strings.Contains(receivedInstructions, forbidden) || strings.Contains(receivedMessages, forbidden) {
			t.Fatalf("agentic provider request included disabled context %q\ninstructions:\n%s\nmessages:\n%s", forbidden, receivedInstructions, receivedMessages)
		}
	}
	for _, required := range []string{
		"AGENTGO AGENTIC TERMINAL PROTOCOL",
		"Work Mode Memory, AI Builder Role context, Response Mode, AgentGO Styling, and model-wide Ugg styling are not active",
		"Compact Ugg formatting applies only to the rolling summary field",
		"MODEL USER CONTEXT:",
		"(disabled for this message)",
		"WORK MODE MEMORY:",
	} {
		if !strings.Contains(receivedInstructions+receivedMessages, required) {
			t.Fatalf("agentic provider request missing isolation marker %q", required)
		}
	}
	after, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != memorySentinel {
		t.Fatalf("agentic response changed Work Mode memory: %q", string(after))
	}
	var response workModeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.MemoryUpdated || response.MemoryWarning != "" {
		t.Fatalf("agentic response reported memory activity: updated=%v warning=%q", response.MemoryUpdated, response.MemoryWarning)
	}
}

func TestHandleWorkModeSendNormalModeKeepsConfiguredContextFeatures(t *testing.T) {
	var receivedInstructions string
	var receivedMessages string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var providerPayload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&providerPayload); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		receivedInstructions, _ = providerPayload["instructions"].(string)
		messagesJSON, _ := json.Marshal(providerPayload["messages"])
		receivedMessages = string(messagesJSON)
		writeJSON(w, http.StatusOK, map[string]any{"text": `{"reply":"Normal reply","files":[],"artifacts":[],"memory":"NORMAL MEMORY UPDATED","warnings":[]}`})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	app.cfg.Models[0].UseUggPrompt = true
	_, metaRoot, err := app.projectPaths(app.cfg.Models[0], "Demo")
	if err != nil {
		t.Fatal(err)
	}
	roleSentinel := "ROLE_CONTEXT_SENTINEL_NORMAL"
	memorySentinel := "MEMORY_SENTINEL_NORMAL"
	if err := os.WriteFile(filepath.Join(metaRoot, "user_context.json"), []byte(roleSentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.ensureProjectWorkModeSettings("Demo"); err != nil {
		t.Fatal(err)
	}
	memoryPath, err := app.workModeContinuedMemoryPath("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memoryPath, []byte(memorySentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	includeRole := true
	body, _ := json.Marshal(workModeRequest{
		ModelID:            "1",
		Prompt:             "Review the widget.",
		IncludeRoleContext: &includeRole,
		ResponseMode:       "skeptic",
		UseMemory:          true,
		UseAgentGOStyling:  true,
	})
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	for _, required := range []string{
		roleSentinel,
		memorySentinel,
		"ROLE CONTEXT",
		"MEMORY\n- AgentGO included memory.md",
		"RESPONSE MODE",
		"Challenge weak assumptions",
		"AGENTGO STYLING",
		"Only apply AgentGO Styling Protocol",
		"SYSTEM INSTRUCTION: UGG PROTOCOL",
	} {
		if !strings.Contains(receivedInstructions+receivedMessages, required) {
			t.Fatalf("normal Work Mode provider request missing configured context %q", required)
		}
	}
	after, err := os.ReadFile(memoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != "NORMAL MEMORY UPDATED" {
		t.Fatalf("normal Work Mode memory was not updated: %q", string(after))
	}
}
