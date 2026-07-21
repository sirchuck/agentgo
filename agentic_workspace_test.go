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
)

func newAgenticWorkspaceTestApp(t *testing.T) *App {
	t.Helper()
	app := &App{
		cfg:                       AppConfig{WorkRoot: t.TempDir()},
		activeProjectName:         "Demo",
		activeCancels:             map[string]activeCancelEntry{},
		workModeSessionsByProject: map[string]workModeSessionState{},
	}
	if err := app.ensureProjectScaffold("Demo"); err != nil {
		t.Fatalf("ensure scaffold: %v", err)
	}
	return app
}

func writeAgenticWorkspaceTestFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	target, err := safeJoin(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFileUnderRoot(root, target, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAgenticWorkspaceTestFile(t *testing.T, root, rel string) []byte {
	t.Helper()
	target, err := safeJoin(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAgenticWorkspaceStartsFromFreshCanonicalCopyAndReusesTask(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "main.txt", []byte("baseline"))
	writeAgenticWorkspaceTestFile(t, canonical, "tmp-work/draft.txt", []byte("unmerged"))

	task, staged, started, err := app.startOrLoadAgenticWorkspaceTask("Demo", workModeAgenticRequest{Enabled: true, Mode: "manual", MaxRuns: 50})
	if err != nil || !started {
		t.Fatalf("start task started=%v err=%v", started, err)
	}
	if got := string(readAgenticWorkspaceTestFile(t, staged, "main.txt")); got != "baseline" {
		t.Fatalf("staged baseline=%q", got)
	}
	if _, err := os.Stat(filepath.Join(staged, "tmp-work", "draft.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tmp-work draft copied into agentic baseline: %v", err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "main.txt", []byte("staged"))
	if got := string(readAgenticWorkspaceTestFile(t, canonical, "main.txt")); got != "baseline" {
		t.Fatalf("canonical changed before merge: %q", got)
	}

	loaded, reusedRoot, started, err := app.startOrLoadAgenticWorkspaceTask("Demo", workModeAgenticRequest{Enabled: true, Mode: "manual", MaxRuns: 50, TaskID: task.SessionID})
	if err != nil || started {
		t.Fatalf("reuse started=%v err=%v", started, err)
	}
	if loaded.SessionID != task.SessionID || reusedRoot != staged {
		t.Fatalf("reused task=%q root=%q, want task=%q root=%q", loaded.SessionID, reusedRoot, task.SessionID, staged)
	}
	if got := string(readAgenticWorkspaceTestFile(t, reusedRoot, "main.txt")); got != "staged" {
		t.Fatalf("continuation did not reuse staged work: %q", got)
	}
	if err := app.saveAgenticWorkspaceDecision("Demo", task.SessionID, "main.txt", agenticWorkspaceDecisionRejected); err != nil {
		t.Fatal(err)
	}
	if _, err := app.reactivateAgenticWorkspaceTask("Demo", task.SessionID); err != nil {
		t.Fatal(err)
	}
	review, err := app.buildAgenticWorkspaceReview("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if review.PendingCount != 1 || len(review.Changes) != 1 || review.Changes[0].Decision != agenticWorkspaceDecisionPending {
		t.Fatalf("continuation did not reset stale review decisions: %+v", review)
	}
}

func TestAgenticWorkspaceComparisonDetectsAddedModifiedDeletedBinaryAndCleanBaseline(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "same.txt", []byte("same"))
	writeAgenticWorkspaceTestFile(t, canonical, "modify.txt", []byte("old"))
	writeAgenticWorkspaceTestFile(t, canonical, "delete.txt", []byte("remove"))

	task, staged, err := app.startAgenticWorkspaceTask("Demo", "manual", 50)
	if err != nil {
		t.Fatal(err)
	}
	clean, err := app.buildAgenticWorkspaceReview("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(clean.Changes) != 0 || clean.UnchangedCount != 3 {
		t.Fatalf("clean review=%+v", clean)
	}

	writeAgenticWorkspaceTestFile(t, staged, "modify.txt", []byte("new"))
	writeAgenticWorkspaceTestFile(t, staged, "added.bin", []byte{0, 1, 2, 3})
	if err := os.Remove(filepath.Join(staged, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	review, err := app.buildAgenticWorkspaceReview("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if review.AddedCount != 1 || review.ModifiedCount != 1 || review.DeletedCount != 1 || review.BinaryCount != 1 || review.UnchangedCount != 1 || review.PendingCount != 3 {
		t.Fatalf("review counts=%+v", review)
	}
	kinds := map[string]string{}
	for _, change := range review.Changes {
		kinds[change.Path] = change.Kind
	}
	if kinds["added.bin"] != agenticWorkspaceChangeAdded || kinds["modify.txt"] != agenticWorkspaceChangeModified || kinds["delete.txt"] != agenticWorkspaceChangeDeleted {
		t.Fatalf("change kinds=%v", kinds)
	}
}

func TestAgenticWorkspaceMergeRejectAndAutomaticCleanup(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "modify.txt", []byte("old"))
	writeAgenticWorkspaceTestFile(t, canonical, "delete.txt", []byte("remove"))
	task, staged, err := app.startAgenticWorkspaceTask("Demo", "manual", 50)
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "modify.txt", []byte("new"))
	writeAgenticWorkspaceTestFile(t, staged, "added.txt", []byte("added"))
	if err := os.Remove(filepath.Join(staged, "delete.txt")); err != nil {
		t.Fatal(err)
	}

	review, err := app.mergeAgenticWorkspaceChange("Demo", task.SessionID, "modify.txt")
	if err != nil || review.PendingCount != 2 {
		t.Fatalf("merge one review=%+v err=%v", review, err)
	}
	if got := string(readAgenticWorkspaceTestFile(t, canonical, "modify.txt")); got != "new" {
		t.Fatalf("merged content=%q", got)
	}
	review, err = app.rejectAgenticWorkspaceChange("Demo", task.SessionID, "added.txt")
	if err != nil || review.PendingCount != 1 {
		t.Fatalf("reject review=%+v err=%v", review, err)
	}
	if _, err := os.Stat(filepath.Join(canonical, "added.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected file appeared in canonical: %v", err)
	}
	review, err = app.mergeAgenticWorkspaceChange("Demo", task.SessionID, "delete.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !review.Resolved || !review.Deleted || review.PendingCount != 0 {
		t.Fatalf("resolved review=%+v", review)
	}
	if _, err := os.Stat(filepath.Join(canonical, "delete.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical deletion not merged: %v", err)
	}
	taskRoot, _ := app.agenticWorkspaceTaskRoot("Demo", task.SessionID)
	if _, err := os.Stat(taskRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resolved staged task still exists: %v", err)
	}
}

func TestAgenticWorkspaceMergeAllAndConflictProtection(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "a.txt", []byte("a0"))
	writeAgenticWorkspaceTestFile(t, canonical, "b.txt", []byte("b0"))
	task, staged, err := app.startAgenticWorkspaceTask("Demo", "manual", 50)
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "a.txt", []byte("a1"))
	writeAgenticWorkspaceTestFile(t, staged, "b.txt", []byte("b1"))
	writeAgenticWorkspaceTestFile(t, canonical, "b.txt", []byte("external"))

	review, err := app.buildAgenticWorkspaceReview("Demo", task.SessionID)
	if err != nil || review.ConflictCount != 1 {
		t.Fatalf("conflict review=%+v err=%v", review, err)
	}
	if _, err := app.mergeAllAgenticWorkspaceChanges("Demo", task.SessionID); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("merge-all conflict error=%v", err)
	}
	if got := string(readAgenticWorkspaceTestFile(t, canonical, "a.txt")); got != "a0" {
		t.Fatalf("merge-all partially applied before conflict: %q", got)
	}

	writeAgenticWorkspaceTestFile(t, canonical, "b.txt", []byte("b0"))
	review, err = app.mergeAllAgenticWorkspaceChanges("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !review.Resolved || !review.Deleted {
		t.Fatalf("merge-all review=%+v", review)
	}
	if string(readAgenticWorkspaceTestFile(t, canonical, "a.txt")) != "a1" || string(readAgenticWorkspaceTestFile(t, canonical, "b.txt")) != "b1" {
		t.Fatal("merge-all did not update canonical files")
	}
}

func TestAgenticWorkspaceDiscardAndCleanCompletionCleanup(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "a.txt", []byte("a"))
	task, staged, err := app.startAgenticWorkspaceTask("Demo", "manual", 50)
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "a.txt", []byte("changed"))
	discarded, err := app.discardAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil || !discarded.Deleted || !discarded.Resolved {
		t.Fatalf("discarded=%+v err=%v", discarded, err)
	}
	if got := string(readAgenticWorkspaceTestFile(t, canonical, "a.txt")); got != "a" {
		t.Fatalf("discard changed canonical: %q", got)
	}

	cleanTask, _, err := app.startAgenticWorkspaceTask("Demo", "manual", 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.updateAgenticWorkspaceTask("Demo", cleanTask.SessionID, agenticWorkspaceStatusAwaitingReview, false, "done", "Task done.", nil); err != nil {
		t.Fatal(err)
	}
	cleanReview, err := app.buildAgenticWorkspaceReview("Demo", cleanTask.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	cleanReview, err = app.cleanupAgenticWorkspaceWhenNoChanges("Demo", cleanTask.SessionID, cleanReview)
	if err != nil || !cleanReview.Deleted || !cleanReview.Resolved {
		t.Fatalf("clean review=%+v err=%v", cleanReview, err)
	}
}

func TestAgenticWorkspaceInterruptPreservesCompletedReviewState(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "a.txt", []byte("a"))
	task, staged, err := app.startAgenticWorkspaceTask("Demo", "manual", 50)
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "a.txt", []byte("changed"))
	if _, err := app.updateAgenticWorkspaceTask("Demo", task.SessionID, agenticWorkspaceStatusAwaitingReview, false, "complete", "Task complete.", nil); err != nil {
		t.Fatal(err)
	}
	review, err := app.interruptAgenticWorkspaceTask("Demo", task.SessionID, "terminal off")
	if err != nil {
		t.Fatal(err)
	}
	if review.Incomplete || review.Status != agenticWorkspaceStatusAwaitingReview || review.Summary != "complete" {
		t.Fatalf("completed review was incorrectly marked interrupted: %+v", review)
	}
}

func TestHandleWorkModeSendStagesAgenticFilesWithoutChangingCanonicalAndReusesTask(t *testing.T) {
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		payload := map[string]any{
			"reply": "staged", "artifacts": []any{}, "memory": "", "warnings": []any{},
			"agentic_status": "complete", "command": emptyAgenticCommand(), "summary": "done", "question": "", "workspace": map[string]any{},
		}
		if call == 1 {
			payload["agentic_status"] = "needs_user_input"
			payload["summary"] = "main.txt staged; waiting for next instruction."
			payload["question"] = "Continue with the staged task?"
		}
		if call == 1 {
			payload["files"] = []any{
				map[string]any{"path": "main.txt", "action": "overwrite", "content": "first", "artifact_ref": ""},
				map[string]any{"path": "agentic-work/escape.txt", "action": "create", "content": "blocked", "artifact_ref": ""},
			}
		} else {
			payload["files"] = []any{map[string]any{"path": "main.txt", "action": "delete", "content": "", "artifact_ref": ""}}
		}
		data, _ := json.Marshal(payload)
		writeJSON(w, http.StatusOK, map[string]any{
			"text":  string(data),
			"usage": map[string]any{"prompt_tokens": 100 + call, "completion_tokens": 20 + call},
		})
	}))
	defer server.Close()

	app := newAgenticHandlerTestApp(t, server.URL)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "main.txt", []byte("baseline"))

	send := func(taskID string) workModeResponse {
		body, _ := json.Marshal(map[string]any{
			"modelId": "1", "prompt": "Edit main.txt", "selectedFiles": []string{"main.txt"},
			"agentic": map[string]any{"enabled": true, "mode": "manual", "maxRuns": 50, "taskId": taskID},
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
		return response
	}

	first := send("")
	if first.Agentic == nil || first.Agentic.Workspace == nil || first.Agentic.Workspace.TaskID == "" {
		t.Fatalf("first agentic response=%+v", first.Agentic)
	}
	taskID := first.Agentic.Workspace.TaskID
	if len(first.SkippedFiles) != 1 || !strings.Contains(first.SkippedFiles[0], "reserved AgentGO workspace paths") {
		t.Fatalf("reserved path was not blocked: %+v", first.SkippedFiles)
	}
	if got := string(readAgenticWorkspaceTestFile(t, canonical, "main.txt")); got != "baseline" {
		t.Fatalf("canonical changed after staged response: %q", got)
	}
	_, firstStaged, err := app.loadAgenticWorkspaceTask("Demo", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(firstStaged, "agentic-work", "escape.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reserved path appeared in staged workspace: %v", err)
	}
	second := send(taskID)
	if second.Agentic == nil || second.Agentic.Workspace == nil || second.Agentic.Workspace.TaskID != taskID {
		t.Fatalf("second workspace=%+v", second.Agentic)
	}
	_, staged, err := app.loadAgenticWorkspaceTask("Demo", taskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(staged, "main.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("agentic delete was not staged: %v", err)
	}
	if second.Agentic.Workspace.DeletedCount != 1 || second.Agentic.Workspace.PendingCount != 1 {
		t.Fatalf("staged deletion review=%+v", second.Agentic.Workspace)
	}
	if usage := second.Agentic.Workspace.TokenUsage; usage.InputTokens != 203 || usage.OutputTokens != 43 || usage.TotalTokens != 246 || usage.BuilderCalls != 2 || usage.ReportedCalls != 2 || usage.Estimated {
		t.Fatalf("provider-reported task usage=%+v", usage)
	}
	if got := string(readAgenticWorkspaceTestFile(t, canonical, "main.txt")); got != "baseline" {
		t.Fatalf("canonical changed after staged deletion: %q", got)
	}
}
