package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func postAgenticRecovery(t *testing.T, app *App, taskID, action string) (*httptest.ResponseRecorder, agenticRecoveryResponse) {
	t.Helper()
	body, _ := json.Marshal(agenticRecoveryRequest{TaskID: taskID, Action: action})
	rr := httptest.NewRecorder()
	app.handleAgenticRecovery(rr, httptest.NewRequest(http.MethodPost, "/api/work-mode/agentic-recovery", strings.NewReader(string(body))))
	var response agenticRecoveryResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
	}
	return rr, response
}

func TestRecoveryMarksPreRestartActiveTaskInterruptedAndPreservesStagedWork(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "base.txt", []byte("canonical"))
	task, staged, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeFull, 50)
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "base.txt", []byte("staged"))
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, workModeAgenticCommand{Type: "direct", Executable: "go", Args: []string{"version"}, WorkingDirectory: ".", Purpose: "test"}); err != nil {
		t.Fatal(err)
	}
	app.startedAt = time.Now().UTC().Add(time.Second)
	tasks, err := app.listRecoverableAgenticTasks("Demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != agenticWorkspaceStatusInterrupted || !tasks[0].Incomplete {
		t.Fatalf("tasks=%+v", tasks)
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PendingCommand != nil || loaded.Status != agenticWorkspaceStatusInterrupted {
		t.Fatalf("loaded=%+v", loaded)
	}
	if got := string(readAgenticWorkspaceTestFile(t, staged, "base.txt")); got != "staged" {
		t.Fatalf("staged file=%q", got)
	}
	if got := string(readAgenticWorkspaceTestFile(t, canonical, "base.txt")); got != "canonical" {
		t.Fatalf("canonical file=%q", got)
	}
}

func TestRecoveryReviewNeverResumesOrMerges(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "base.txt", []byte("canonical"))
	task, staged, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeFull, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "base.txt", []byte("staged"))
	if _, err := app.interruptAgenticWorkspaceTask("Demo", task.SessionID, "restart"); err != nil {
		t.Fatal(err)
	}
	rr, response := postAgenticRecovery(t, app, task.SessionID, agenticRecoveryActionReview)
	if rr.Code != http.StatusOK || response.Workspace == nil {
		t.Fatalf("status=%d response=%+v body=%s", rr.Code, response, rr.Body.String())
	}
	if response.Workspace.Status != agenticWorkspaceStatusInterrupted || response.Workspace.PendingCount != 1 {
		t.Fatalf("workspace=%+v", response.Workspace)
	}
	if got := string(readAgenticWorkspaceTestFile(t, canonical, "base.txt")); got != "canonical" {
		t.Fatalf("review merged canonical file: %q", got)
	}
}

func TestRecoveryRejectsDeferredOrContinueNewActions(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, staged, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeFull, 7)
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "base.txt", []byte("staged"))
	if _, err := app.interruptAgenticWorkspaceTask("Demo", task.SessionID, "restart"); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"continue_new", "not_now"} {
		rr, _ := postAgenticRecovery(t, app, task.SessionID, action)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "unknown agentic recovery action") {
			t.Fatalf("action=%s status=%d body=%s", action, rr.Code, rr.Body.String())
		}
		if _, err := os.Stat(staged); err != nil {
			t.Fatalf("action %s changed preserved task: %v", action, err)
		}
	}
}

func TestNewTaskBlockedUntilInterruptedTaskResolved(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, _, err := app.startOrLoadAgenticWorkspaceTask("Demo", workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.interruptAgenticWorkspaceTask("Demo", task.SessionID, "restart"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := app.startOrLoadAgenticWorkspaceTask("Demo", workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50}); !errors.Is(err, errAgenticUnresolvedTaskExists) {
		t.Fatalf("new task err=%v", err)
	}
	if _, _, _, err := app.startOrLoadAgenticWorkspaceTask("Demo", workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50, TaskID: task.SessionID}); !errors.Is(err, errAgenticUnresolvedTaskExists) {
		t.Fatalf("interrupted resume err=%v", err)
	}
	if _, err := app.discardAgenticWorkspaceTask("Demo", task.SessionID); err != nil {
		t.Fatal(err)
	}
	fresh, _, started, err := app.startOrLoadAgenticWorkspaceTask("Demo", workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50})
	if err != nil || !started || fresh.SessionID == task.SessionID {
		t.Fatalf("fresh=%+v started=%v err=%v", fresh, started, err)
	}
}

func TestRecoveryDiscardDeletesOnlySelectedLegacyTask(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	first, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	second, secondStaged, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.interruptAgenticWorkspaceTask("Demo", first.SessionID, "restart"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.interruptAgenticWorkspaceTask("Demo", second.SessionID, "restart"); err != nil {
		t.Fatal(err)
	}
	if rr, _ := postAgenticRecovery(t, app, first.SessionID, agenticRecoveryActionDiscard); rr.Code != http.StatusOK {
		t.Fatalf("discard status=%d body=%s", rr.Code, rr.Body.String())
	}
	firstRoot, _ := app.agenticWorkspaceTaskRoot("Demo", first.SessionID)
	if _, err := os.Stat(firstRoot); !os.IsNotExist(err) {
		t.Fatalf("discarded task still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(secondStaged)); err != nil {
		t.Fatalf("other task was removed: %v", err)
	}
	tasks, err := app.listRecoverableAgenticTasks("Demo")
	if err != nil || len(tasks) != 1 || tasks[0].TaskID != second.SessionID {
		t.Fatalf("remaining tasks=%+v err=%v", tasks, err)
	}
}

func TestRecoveryReviewCancelsActiveBuilderCall(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeFull, 50)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := make(chan struct{})
	key := agenticBuilderCancelKey(task.SessionID)
	app.mu.Lock()
	app.setActiveCancelLocked(key, "Demo", key, func() { close(cancelled) })
	app.mu.Unlock()
	rr, _ := postAgenticRecovery(t, app, task.SessionID, agenticRecoveryActionReview)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("recovery did not cancel active Builder call")
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != agenticWorkspaceStatusInterrupted {
		t.Fatalf("task status=%s", loaded.Status)
	}
}

func TestExistingTaskCannotContinueWhileAnotherLegacyTaskIsUnresolved(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	first, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.interruptAgenticWorkspaceTask("Demo", first.SessionID, "legacy unresolved task"); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = app.startOrLoadAgenticWorkspaceTask("Demo", workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50, TaskID: second.SessionID})
	if !errors.Is(err, errAgenticUnresolvedTaskExists) || !strings.Contains(err.Error(), first.SessionID) {
		t.Fatalf("continuation with another unresolved task err=%v", err)
	}
}
