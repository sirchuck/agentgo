package main

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	agenticRecoveryActionReview  = "review"
	agenticRecoveryActionDiscard = "discard"
)

type agenticRecoveryTask struct {
	TaskID     string                  `json:"taskId"`
	Mode       string                  `json:"mode"`
	Status     string                  `json:"status"`
	Incomplete bool                    `json:"incomplete"`
	Summary    string                  `json:"summary,omitempty"`
	RunNumber  int                     `json:"runNumber,omitempty"`
	MaxRuns    int                     `json:"maxRuns,omitempty"`
	UpdatedAt  string                  `json:"updatedAt,omitempty"`
	Workspace  *agenticWorkspaceReview `json:"workspace,omitempty"`
}

type agenticRecoveryResponse struct {
	Tasks     []agenticRecoveryTask   `json:"tasks"`
	Workspace *agenticWorkspaceReview `json:"workspace,omitempty"`
	Message   string                  `json:"message,omitempty"`
}

type agenticRecoveryRequest struct {
	TaskID string `json:"taskId"`
	Action string `json:"action"`
}

func agenticTaskNeedsRecovery(review agenticWorkspaceReview) bool {
	if review.Deleted || review.Resolved {
		return false
	}
	if review.PendingCount > 0 || review.Incomplete {
		return true
	}
	switch review.Status {
	case agenticWorkspaceStatusInterrupted, agenticWorkspaceStatusMaximumRuns, agenticWorkspaceStatusAwaitingReview, agenticWorkspaceStatusPrepared:
		return true
	default:
		return false
	}
}

func parseAgenticTaskTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return parsed
}

func (a *App) markRestartedAgenticTaskInterrupted(projectName string, task agenticWorkspaceTask) (agenticWorkspaceTask, error) {
	if !parseAgenticTaskTime(task.UpdatedAt).Before(a.startedAt.UTC()) {
		return task, nil
	}
	switch task.Status {
	case agenticWorkspaceStatusActive, agenticWorkspaceStatusAwaitingCommand, agenticWorkspaceStatusNeedsUserInput:
		summary := "AgentGO restarted while this agentic task was unfinished. Automatic continuation was not resumed; staged files and audit data were preserved."
		return a.clearAgenticPendingCommand(projectName, task.SessionID, agenticWorkspaceStatusInterrupted, summary, true)
	default:
		return task, nil
	}
}

func (a *App) listRecoverableAgenticTasks(projectName string) ([]agenticRecoveryTask, error) {
	root, err := a.agenticWorkspaceProjectRoot(projectName)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return []agenticRecoveryTask{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []agenticRecoveryTask{}
	for _, entry := range entries {
		if !entry.IsDir() || !validAgenticWorkspaceSessionID(entry.Name()) {
			continue
		}
		taskRoot := filepath.Join(root, entry.Name())
		task, err := loadAgenticWorkspaceTask(taskRoot)
		if err != nil || task.ProjectName != projectName {
			continue
		}
		task, _ = a.markRestartedAgenticTaskInterrupted(projectName, task)
		review, err := a.buildAgenticWorkspaceReview(projectName, task.SessionID)
		if err != nil || !agenticTaskNeedsRecovery(review) {
			continue
		}
		copyReview := review
		out = append(out, agenticRecoveryTask{
			TaskID: task.SessionID, Mode: task.Mode, Status: review.Status, Incomplete: review.Incomplete,
			Summary: review.Summary, RunNumber: review.RunNumber, MaxRuns: review.MaxRuns,
			UpdatedAt: review.UpdatedAt, Workspace: &copyReview,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, nil
}

func agenticRecoveryShouldInterrupt(task agenticWorkspaceTask) bool {
	switch task.Status {
	case agenticWorkspaceStatusActive, agenticWorkspaceStatusAwaitingCommand, agenticWorkspaceStatusNeedsUserInput, agenticWorkspaceStatusPrepared:
		return true
	default:
		return false
	}
}

func (a *App) stopAgenticTaskForRecovery(projectName string, task agenticWorkspaceTask) (agenticWorkspaceTask, error) {
	if !agenticRecoveryShouldInterrupt(task) && !a.agenticBuilderCallRunning(projectName, task.SessionID) && !a.agenticManualCommandRunning(projectName, task.SessionID) {
		return task, nil
	}
	reason := "Agentic recovery was opened for this unfinished task. Automatic continuation and active processes were stopped; staged files and audit data were preserved."
	if _, err := a.stopAgenticManualCommand(projectName, task.SessionID, reason); err != nil {
		return agenticWorkspaceTask{}, err
	}
	updated, _, err := a.loadAgenticWorkspaceTask(projectName, task.SessionID)
	return updated, err
}

func normalizeAgenticRecoveryAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case agenticRecoveryActionReview:
		return agenticRecoveryActionReview
	case agenticRecoveryActionDiscard:
		return agenticRecoveryActionDiscard
	default:
		return ""
	}
}

func (a *App) handleAgenticRecovery(w http.ResponseWriter, r *http.Request) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodGet {
		tasks, err := a.listRecoverableAgenticTasks(projectName)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, agenticRecoveryResponse{Tasks: tasks})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticRecoveryRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.Action = normalizeAgenticRecoveryAction(req.Action)
	if req.Action == "" {
		http.Error(w, "unknown agentic recovery action", http.StatusBadRequest)
		return
	}
	task, _, err := a.loadAgenticWorkspaceTask(projectName, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	task, err = a.stopAgenticTaskForRecovery(projectName, task)
	if err != nil {
		http.Error(w, "Could not stop unfinished agentic work for recovery: "+err.Error(), http.StatusConflict)
		return
	}
	switch req.Action {
	case agenticRecoveryActionReview:
		review, err := a.buildAgenticWorkspaceReview(projectName, task.SessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, agenticRecoveryResponse{Workspace: &review, Message: "Interrupted staged changes are ready for review. Nothing was resumed or merged."})
	case agenticRecoveryActionDiscard:
		review, err := a.discardAgenticWorkspaceTask(projectName, task.SessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		writeJSON(w, http.StatusOK, agenticRecoveryResponse{Workspace: &review, Message: "Interrupted staged task discarded."})
	}
}

func (a *App) handleAgenticDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticWorkspaceTaskRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	req.TaskID = strings.TrimSpace(req.TaskID)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "The Work Mode browser session disconnected. Automatic continuation stopped and staged files were preserved."
	}
	review, err := a.stopAgenticManualCommand(projectName, req.TaskID, reason)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, review)
}
