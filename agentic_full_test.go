package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func convertAgenticFixtureToFull(t *testing.T, app *App, task agenticWorkspaceTask, maxRuns int) agenticWorkspaceTask {
	t.Helper()
	root, err := app.agenticWorkspaceTaskRoot("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	task.Mode = workModeAgenticModeFull
	task.MaxRuns = maxRuns
	if task.MaxRuns <= 0 {
		task.MaxRuns = 50
	}
	if err := saveAgenticWorkspaceTask(root, task); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	return loaded
}

func postAgenticFullExecute(t *testing.T, app *App, taskID string) (*httptest.ResponseRecorder, agenticFullExecuteResponse) {
	t.Helper()
	body, _ := json.Marshal(agenticFullExecuteRequest{TaskID: taskID})
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/agentic-command/full-execute", strings.NewReader(string(body)))
	rr := httptest.NewRecorder()
	app.handleAgenticFullExecute(rr, req)
	var response agenticFullExecuteResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
	}
	return rr, response
}

func TestFullExecuteRunsStoredCommandAndReturnsAutomaticContinuation(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "echo-mode"}})
	task = convertAgenticFixtureToFull(t, app, task, 3)
	command := agenticHelperCommand(helperName)
	command.Purpose = "Run the exact Full-mode stored request"
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, command); err != nil {
		t.Fatal(err)
	}
	rr, response := postAgenticFullExecute(t, app, task.SessionID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if response.Execution == nil || response.Execution.Status != agenticExecutionStatusCompleted || !response.AutoContinue || response.MaxRunsHit {
		t.Fatalf("response=%+v", response)
	}
	if !strings.Contains(response.Prompt, "AGENTGO FULL TERMINAL RESULT") || !strings.Contains(response.Prompt, "Run: 1 of 3") {
		t.Fatalf("continuation prompt=%q", response.Prompt)
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCommand != nil || loaded.Status != agenticWorkspaceStatusNeedsUserInput {
		t.Fatalf("task after full execution=%+v", loaded)
	}
	if !strings.Contains(loaded.LatestCommandResult, "AGENTGO FULL TERMINAL RESULT") || !strings.Contains(loaded.LatestCommandResult, "Execution status: completed") {
		t.Fatalf("latest command result was not persisted: %q", loaded.LatestCommandResult)
	}
}

func TestFullMaximumRunsStopsAfterCurrentRun(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "echo-mode"}})
	task = convertAgenticFixtureToFull(t, app, task, 1)
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, agenticHelperCommand(helperName)); err != nil {
		t.Fatal(err)
	}
	rr, response := postAgenticFullExecute(t, app, task.SessionID)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if response.AutoContinue || !response.MaxRunsHit || response.Workspace.Status != agenticWorkspaceStatusMaximumRuns {
		t.Fatalf("response=%+v", response)
	}
	if response.Prompt != "" || !strings.Contains(response.Message, "Maximum Runs") {
		t.Fatalf("prompt=%q message=%q", response.Prompt, response.Message)
	}
}

func TestFullNonzeroExitContinuesButCancellationStops(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "exit-mode"}})
	task = convertAgenticFixtureToFull(t, app, task, 3)
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, agenticHelperCommand(helperName)); err != nil {
		t.Fatal(err)
	}
	rr, response := postAgenticFullExecute(t, app, task.SessionID)
	if rr.Code != http.StatusOK || response.Execution == nil || response.Execution.Status != agenticExecutionStatusFailed || !response.AutoContinue {
		t.Fatalf("status=%d response=%+v body=%s", rr.Code, response, rr.Body.String())
	}

	spawnApp, spawnTask, _, spawnHelper := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "spawn-mode"}})
	spawnTask = convertAgenticFixtureToFull(t, spawnApp, spawnTask, 3)
	if _, err := spawnApp.saveAgenticPendingCommand("Demo", spawnTask.SessionID, agenticHelperCommand(spawnHelper)); err != nil {
		t.Fatal(err)
	}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr, _ := postAgenticFullExecute(t, spawnApp, spawnTask.SessionID)
		result <- rr
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !spawnApp.agenticManualCommandRunning("Demo", spawnTask.SessionID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !spawnApp.agenticManualCommandRunning("Demo", spawnTask.SessionID) {
		t.Fatal("Full command did not enter running state")
	}
	if _, err := spawnApp.stopAgenticManualCommand("Demo", spawnTask.SessionID, "Full Stop test"); err != nil {
		t.Fatal(err)
	}
	select {
	case rr := <-result:
		if rr.Code != http.StatusOK {
			t.Fatalf("full handler status=%d body=%s", rr.Code, rr.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Full handler did not return after Stop")
	}
	loaded, _, err := spawnApp.loadAgenticWorkspaceTask("Demo", spawnTask.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != agenticWorkspaceStatusInterrupted || !loaded.Incomplete {
		t.Fatalf("task after stop=%+v", loaded)
	}
}

func TestHandleWorkModeSendFullReturnsAutomaticExecutionHandoff(t *testing.T) {
	var instructions string
	responseText := agenticEnvelope(workModeAgenticStatusRunCommand, workModeAgenticCommand{
		Type: workModeAgenticCommandDirect, Executable: "go", Args: []string{"version"}, WorkingDirectory: ".", Purpose: "Read Go version",
	}, "Work started; next command requested.", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		instructions, _ = payload["instructions"].(string)
		writeJSON(w, http.StatusOK, map[string]any{"text": responseText})
	}))
	defer server.Close()
	app := newAgenticHandlerTestApp(t, server.URL)
	body, _ := json.Marshal(map[string]any{"modelId": "1", "prompt": "Check Go.", "agentic": map[string]any{"enabled": true, "mode": "full", "maxRuns": 4}})
	rr := httptest.NewRecorder()
	app.handleWorkModeSend(rr, httptest.NewRequest(http.MethodPost, "/api/work-mode/send", strings.NewReader(string(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response workModeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Agentic == nil || !response.Agentic.AutoExecute || response.Agentic.ApprovalRequired || response.Agentic.Mode != workModeAgenticModeFull || response.Agentic.Workspace == nil {
		t.Fatalf("agentic=%+v", response.Agentic)
	}
	if response.Agentic.Workspace.MaxRuns != 4 || response.Agentic.Workspace.RunNumber != 1 {
		t.Fatalf("workspace=%+v", response.Agentic.Workspace)
	}
	if !strings.Contains(instructions, "Full mode is selected") || !strings.Contains(instructions, "current Full-mode limit is 4") || strings.Contains(strings.ToLower(instructions), "phase 6") {
		t.Fatalf("instructions=%s", instructions)
	}
}

func TestHandleWorkModeSendRejectsFullContinuationAfterMaximumRuns(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeFull, 1)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := app.agenticWorkspaceTaskRoot("Demo", task.SessionID)
	task.Status = agenticWorkspaceStatusMaximumRuns
	if err := saveAgenticWorkspaceTask(root, task); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = app.startOrLoadAgenticWorkspaceTask("Demo", workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeFull, MaxRuns: 1, TaskID: task.SessionID})
	if !errors.Is(err, errAgenticUnresolvedTaskExists) {
		t.Fatalf("err=%v", err)
	}
}
