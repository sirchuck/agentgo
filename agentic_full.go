package main

import (
	"fmt"
	"net/http"
	"strings"
)

const agenticFullAuthorization = "full"

type agenticFullExecuteRequest struct {
	TaskID string `json:"taskId"`
}

type agenticFullExecuteResponse struct {
	TaskID       string                  `json:"taskId"`
	Prompt       string                  `json:"prompt,omitempty"`
	Message      string                  `json:"message"`
	AutoContinue bool                    `json:"autoContinue"`
	MaxRunsHit   bool                    `json:"maxRunsHit,omitempty"`
	Command      workModeAgenticCommand  `json:"command"`
	Execution    *agenticExecutionResult `json:"execution,omitempty"`
	Workspace    agenticWorkspaceReview  `json:"workspace"`
}

func agenticFullContinuationPrompt(task agenticWorkspaceTask, command workModeAgenticCommand, execution agenticExecutionResult) string {
	return buildAgenticCommandResultPrompt("AGENTGO FULL TERMINAL RESULT", "", &task, command, &execution, true)
}

func agenticFullExecutionCanContinue(result agenticExecutionResult) bool {
	switch result.Status {
	case agenticExecutionStatusCompleted, agenticExecutionStatusFailed:
		return true
	default:
		return false
	}
}

func (a *App) finishAgenticFullExecution(projectName, taskID string, result agenticExecutionResult) (agenticWorkspaceTask, agenticWorkspaceReview, bool, error) {
	agenticManualDecisionMu.Lock()
	defer agenticManualDecisionMu.Unlock()

	key := agenticManualCancelKey(taskID)
	a.mu.Lock()
	a.clearActiveCancelLocked(key, key)
	a.mu.Unlock()

	task, _, err := a.loadAgenticWorkspaceTask(projectName, taskID)
	if err != nil {
		return agenticWorkspaceTask{}, agenticWorkspaceReview{}, false, err
	}

	autoContinue := agenticFullExecutionCanContinue(result)
	status := agenticWorkspaceStatusNeedsUserInput
	summary := "The Full-mode command finished. Its result is ready for automatic Builder continuation."
	if result.TimedOut {
		autoContinue = false
		status = agenticWorkspaceStatusInterrupted
		summary = "The Full-mode command reached the twenty-minute timeout. Partial staged files and available output were preserved; automatic continuation stopped."
	} else if result.Cancelled {
		autoContinue = false
		status = agenticWorkspaceStatusInterrupted
		summary = "The Full-mode command was cancelled. Partial staged files and available output were preserved; automatic continuation stopped."
	} else if !autoContinue {
		status = agenticWorkspaceStatusInterrupted
		summary = "The Full-mode command encountered an unrecoverable execution error. Partial staged files and available output were preserved; automatic continuation stopped."
	} else if task.RunNumber >= task.MaxRuns {
		autoContinue = false
		status = agenticWorkspaceStatusMaximumRuns
		summary = fmt.Sprintf("Full mode reached Maximum Runs (%d). Automatic continuation stopped; staged files remain available for review.", task.MaxRuns)
	}
	if task.Status == agenticWorkspaceStatusInterrupted {
		autoContinue = false
		status = task.Status
		summary = task.Summary
	}
	task, err = a.clearAgenticPendingCommand(projectName, taskID, status, summary, true)
	if err != nil {
		return agenticWorkspaceTask{}, agenticWorkspaceReview{}, false, err
	}
	review, err := a.buildAgenticWorkspaceReview(projectName, taskID)
	return task, review, autoContinue, err
}

func (a *App) executeAgenticFullStoredCommand(projectName, taskID string) (workModeAgenticCommand, agenticExecutionResult, agenticWorkspaceTask, agenticWorkspaceReview, bool, error) {
	ctx, cancel, command, err := a.beginAgenticManualExecution(projectName, taskID)
	if err != nil {
		return workModeAgenticCommand{}, agenticExecutionResult{}, agenticWorkspaceTask{}, agenticWorkspaceReview{}, false, err
	}
	defer cancel()

	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{
		Kind: agenticAuditKindApproval, Decision: agenticFullAuthorization, Status: "approved",
		Message: "Full mode automatically authorized the stored structured command request.",
		Command: cloneWorkModeAgenticCommand(&command),
	})
	options := defaultAgenticExecutionOptions()
	options.OnEvent = func(event agenticExecutionLiveEvent) {
		a.appendAgenticExecutionLiveEvent(projectName, taskID, event)
	}
	result := a.executeAgenticCommand(ctx, agenticExecutionRequest{ProjectName: projectName, TaskID: taskID, Command: command}, options)
	level := "info"
	if result.Status != agenticExecutionStatusCompleted {
		level = "error"
	}
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{
		Kind: agenticAuditKindExecutionResult, Status: result.Status, Level: level,
		Message: "The automatically authorized Full-mode command finished.", Execution: auditExecutionSummary(result),
	})
	task, review, autoContinue, err := a.finishAgenticFullExecution(projectName, taskID, result)
	return command, result, task, review, autoContinue, err
}

func (a *App) handleAgenticFullExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticFullExecuteRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	req.TaskID = strings.TrimSpace(req.TaskID)
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	task, _, err := a.loadAgenticWorkspaceTask(projectName, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if task.Mode != workModeAgenticModeFull {
		http.Error(w, "the staged task is not using Full mode", http.StatusConflict)
		return
	}
	command, result, task, review, autoContinue, err := a.executeAgenticFullStoredCommand(projectName, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	maxRunsHit := task.Status == agenticWorkspaceStatusMaximumRuns
	message := "Full-mode command finished. Its result will return automatically to the Builder."
	if maxRunsHit || !autoContinue {
		message = task.Summary
	}
	resultPrompt := agenticFullContinuationPrompt(task, command, result)
	if _, saveErr := a.saveAgenticLatestCommandResult(projectName, req.TaskID, resultPrompt); saveErr != nil {
		http.Error(w, "Could not preserve Full-mode command continuity: "+saveErr.Error(), http.StatusInternalServerError)
		return
	}
	response := agenticFullExecuteResponse{
		TaskID: req.TaskID, Message: message, AutoContinue: autoContinue, MaxRunsHit: maxRunsHit,
		Command: command, Execution: &result, Workspace: review,
	}
	if autoContinue {
		response.Prompt = resultPrompt
	}
	writeJSON(w, http.StatusOK, response)
}
