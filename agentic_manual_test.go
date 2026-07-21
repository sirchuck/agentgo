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

func postAgenticManualDecision(t *testing.T, app *App, handler http.HandlerFunc, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(data)))
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func TestManualApproveExecutesStoredCommandNotBrowserReplacement(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "echo-mode"}})
	stored := agenticHelperCommand(helperName)
	stored.Purpose = "Run the server-stored command"
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, stored); err != nil {
		t.Fatal(err)
	}
	rr := postAgenticManualDecision(t, app, app.handleAgenticManualApprove, map[string]any{
		"taskId":  task.SessionID,
		"command": map[string]any{"type": "shell", "script": "malicious browser replacement"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response agenticManualDecisionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Execution == nil || response.Execution.Command.Executable != stored.Executable {
		t.Fatalf("execution=%+v", response.Execution)
	}
	if strings.Contains(response.Prompt, "malicious browser replacement") || !strings.Contains(response.Prompt, "stdout-start") {
		t.Fatalf("prompt did not contain stored command result: %q", response.Prompt)
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCommand != nil || loaded.Status != agenticWorkspaceStatusNeedsUserInput {
		t.Fatalf("task after approval=%+v", loaded)
	}
}

func TestManualDenyExecutesNothingAndReturnsPromptHandoff(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "exit-mode"}})
	command := agenticHelperCommand(helperName)
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, command); err != nil {
		t.Fatal(err)
	}
	rr := postAgenticManualDecision(t, app, app.handleAgenticManualDeny, map[string]any{"taskId": task.SessionID})
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response agenticManualDecisionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Execution != nil || response.Decision != "denied" || !strings.Contains(response.Prompt, "was not executed") {
		t.Fatalf("response=%+v", response)
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCommand != nil || loaded.Status != agenticWorkspaceStatusNeedsUserInput {
		t.Fatalf("task after denial=%+v", loaded)
	}
}

func TestManualStopCancelsProcessTreeAndPreservesWorkspace(t *testing.T) {
	app, task, staged, helperName := newAgenticExecutionFixture(t, nil)
	sentinel := filepath.Join(staged, "child-survived.txt")
	if err := app.saveTerminalEnvironment("Demo", terminalEnvironmentFile{Variables: []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "spawn-mode"}, {Name: "AGENTGO_SENTINEL", Value: sentinel}}}); err != nil {
		t.Fatal(err)
	}
	command := agenticHelperCommand(helperName)
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, command); err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		resultCh <- postAgenticManualDecision(t, app, app.handleAgenticManualApprove, map[string]any{"taskId": task.SessionID})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !app.agenticManualCommandRunning("Demo", task.SessionID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !app.agenticManualCommandRunning("Demo", task.SessionID) {
		t.Fatal("Manual command never entered running state")
	}
	if _, err := app.stopAgenticManualCommand("Demo", task.SessionID, "Terminal Stop test"); err != nil {
		t.Fatal(err)
	}
	select {
	case rr := <-resultCh:
		if rr.Code != http.StatusOK {
			t.Fatalf("approve status=%d body=%s", rr.Code, rr.Body.String())
		}
		var response agenticManualDecisionResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Execution == nil || !response.Execution.Cancelled {
			t.Fatalf("execution=%+v", response.Execution)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Manual approval handler did not return after Stop")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("descendant process survived Stop; stat err=%v", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("staged workspace was not preserved: %v", err)
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != agenticWorkspaceStatusInterrupted || !loaded.Incomplete {
		t.Fatalf("task after Stop=%+v", loaded)
	}
}

func TestWorkspaceReviewActionsBlockedWhileManualCommandRuns(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "base.txt", []byte("base"))
	task, staged, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "base.txt", []byte("changed"))
	key := agenticManualCancelKey(task.SessionID)
	app.mu.Lock()
	app.setActiveCancelLocked(key, "Demo", key, func() {})
	app.mu.Unlock()
	defer func() {
		app.mu.Lock()
		app.clearActiveCancelLocked(key, key)
		app.mu.Unlock()
	}()
	if _, err := app.mergeAgenticWorkspaceChange("Demo", task.SessionID, "base.txt"); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("merge error=%v", err)
	}
	if _, err := app.rejectAgenticWorkspaceChange("Demo", task.SessionID, "base.txt"); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("reject error=%v", err)
	}
	if _, err := app.discardAgenticWorkspaceTask("Demo", task.SessionID); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("discard error=%v", err)
	}
}

func TestEmergencyStopCancelsRegisteredManualCommand(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, nil)
	command := agenticHelperCommand(helperName)
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, command); err != nil {
		t.Fatal(err)
	}
	ctx, cancel, _, err := app.beginAgenticManualExecution("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	rr := httptest.NewRecorder()
	app.handleStop(rr, httptest.NewRequest(http.MethodPost, "/api/stop", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Emergency Stop did not cancel Manual command context")
	}
}

func TestWorkspaceReviewActionsBlockedWhileBuilderContinuationRuns(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "base.txt", []byte("base"))
	task, staged, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeFull, 50)
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "base.txt", []byte("changed"))
	key := agenticBuilderCancelKey(task.SessionID)
	app.mu.Lock()
	app.setActiveCancelLocked(key, "Demo", key, func() {})
	app.mu.Unlock()
	defer func() {
		app.mu.Lock()
		app.clearActiveCancelLocked(key, key)
		app.mu.Unlock()
	}()
	if _, err := app.mergeAgenticWorkspaceChange("Demo", task.SessionID, "base.txt"); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("merge error=%v", err)
	}
	if _, err := app.rejectAgenticWorkspaceChange("Demo", task.SessionID, "base.txt"); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("reject error=%v", err)
	}
	if _, err := app.discardAgenticWorkspaceTask("Demo", task.SessionID); err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("discard error=%v", err)
	}
}
