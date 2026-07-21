package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	agenticManualCancelPrefix  = "agentic-terminal:"
	agenticBuilderCancelPrefix = "agentic-builder:"
)

var agenticManualDecisionMu sync.Mutex

type agenticManualDecisionRequest struct {
	TaskID string `json:"taskId"`
}

type agenticManualDecisionResponse struct {
	TaskID    string                  `json:"taskId"`
	Decision  string                  `json:"decision"`
	Prompt    string                  `json:"prompt"`
	Message   string                  `json:"message"`
	Command   workModeAgenticCommand  `json:"command"`
	Execution *agenticExecutionResult `json:"execution,omitempty"`
	Workspace agenticWorkspaceReview  `json:"workspace"`
}

func cloneWorkModeAgenticCommand(command *workModeAgenticCommand) *workModeAgenticCommand {
	if command == nil {
		return nil
	}
	copy := *command
	copy.Args = append([]string(nil), command.Args...)
	return &copy
}

func agenticManualCancelKey(taskID string) string {
	return agenticManualCancelPrefix + strings.TrimSpace(taskID)
}

func agenticBuilderCancelKey(taskID string) string {
	return agenticBuilderCancelPrefix + strings.TrimSpace(taskID)
}

func (a *App) agenticManualCommandRunning(projectName, taskID string) bool {
	key := agenticManualCancelKey(taskID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	entry, ok := a.activeCancels[key]
	return ok && strings.TrimSpace(entry.ProjectName) == strings.TrimSpace(projectName) && entry.Cancel != nil
}

func (a *App) agenticBuilderCallRunning(projectName, taskID string) bool {
	key := agenticBuilderCancelKey(taskID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	entry, ok := a.activeCancels[key]
	return ok && strings.TrimSpace(entry.ProjectName) == strings.TrimSpace(projectName) && entry.Cancel != nil
}

func (a *App) ensureAgenticTaskIdle(projectName, taskID string) error {
	if a.agenticManualCommandRunning(projectName, taskID) || a.agenticBuilderCallRunning(projectName, taskID) {
		return errors.New("agentic work is still running for this staged task")
	}
	return nil
}

func (a *App) ensureAgenticManualCommandIdle(projectName, taskID string) error {
	if a.agenticManualCommandRunning(projectName, taskID) {
		return errors.New("an agentic terminal command is still running for this staged task")
	}
	return nil
}

func (a *App) saveAgenticPendingCommand(projectName, taskID string, command workModeAgenticCommand) (agenticWorkspaceTask, error) {
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return agenticWorkspaceTask{}, err
	}
	task, err := loadAgenticWorkspaceTask(taskRoot)
	if err != nil {
		return agenticWorkspaceTask{}, err
	}
	if task.ProjectName != projectName {
		return agenticWorkspaceTask{}, errors.New("agentic workspace belongs to another project")
	}
	command = normalizeWorkModeAgenticCommand(command)
	task.PendingCommand = cloneWorkModeAgenticCommand(&command)
	task.PendingCommandAt = time.Now().UTC().Format(time.RFC3339Nano)
	task.Status = agenticWorkspaceStatusAwaitingCommand
	task.Incomplete = true
	switch task.Mode {
	case workModeAgenticModeFull:
		task.Summary = "A structured command request is queued for automatic Full-mode execution."
	case workModeAgenticModeSemi:
		task.Summary = "A structured command request is waiting for Semi-mode authorization."
	default:
		task.Summary = "A structured command request is waiting for Manual approval."
	}
	if err := saveAgenticWorkspaceTask(taskRoot, task); err != nil {
		return agenticWorkspaceTask{}, err
	}
	return loadAgenticWorkspaceTask(taskRoot)
}

func (a *App) clearAgenticPendingCommand(projectName, taskID, status, summary string, incomplete bool) (agenticWorkspaceTask, error) {
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return agenticWorkspaceTask{}, err
	}
	task, err := loadAgenticWorkspaceTask(taskRoot)
	if err != nil {
		return agenticWorkspaceTask{}, err
	}
	if task.ProjectName != projectName {
		return agenticWorkspaceTask{}, errors.New("agentic workspace belongs to another project")
	}
	task.PendingCommand = nil
	task.PendingCommandAt = ""
	if strings.TrimSpace(status) != "" {
		task.Status = strings.TrimSpace(status)
		task.Incomplete = incomplete
		task.Summary = strings.TrimSpace(summary)
	}
	if err := saveAgenticWorkspaceTask(taskRoot, task); err != nil {
		return agenticWorkspaceTask{}, err
	}
	return loadAgenticWorkspaceTask(taskRoot)
}

func (a *App) loadAgenticPendingCommand(projectName, taskID string) (workModeAgenticCommand, error) {
	task, _, err := a.loadAgenticWorkspaceTask(projectName, taskID)
	if err != nil {
		return workModeAgenticCommand{}, err
	}
	if task.Mode != workModeAgenticModeManual && task.Mode != workModeAgenticModeSemi && task.Mode != workModeAgenticModeFull {
		return workModeAgenticCommand{}, errors.New("the staged task is not using an executable terminal mode")
	}
	if task.PendingCommand == nil {
		return workModeAgenticCommand{}, errors.New("no structured command is awaiting authorization")
	}
	return normalizeWorkModeAgenticCommand(*cloneWorkModeAgenticCommand(task.PendingCommand)), nil
}

func agenticManualCommandDisplay(command workModeAgenticCommand) string {
	if command.Type == workModeAgenticCommandShell {
		return command.Script
	}
	return strings.TrimSpace(strings.Join(append([]string{command.Executable}, command.Args...), " "))
}

func agenticManualPrompt(task agenticWorkspaceTask, decision string, command workModeAgenticCommand, execution *agenticExecutionResult) string {
	return buildAgenticCommandResultPrompt("AGENTGO MANUAL TERMINAL RESULT", decision, &task, command, execution, false)
}

func (a *App) beginAgenticManualExecution(projectName, taskID string) (context.Context, context.CancelFunc, workModeAgenticCommand, error) {
	agenticManualDecisionMu.Lock()
	defer agenticManualDecisionMu.Unlock()
	if err := a.ensureAgenticManualCommandIdle(projectName, taskID); err != nil {
		return nil, nil, workModeAgenticCommand{}, err
	}
	command, err := a.loadAgenticPendingCommand(projectName, taskID)
	if err != nil {
		return nil, nil, workModeAgenticCommand{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	key := agenticManualCancelKey(taskID)
	a.mu.Lock()
	a.setActiveCancelLocked(key, projectName, key, cancel)
	a.mu.Unlock()
	return ctx, cancel, command, nil
}

func (a *App) finishAgenticManualExecution(projectName, taskID string, result agenticExecutionResult) (agenticWorkspaceReview, error) {
	agenticManualDecisionMu.Lock()
	defer agenticManualDecisionMu.Unlock()
	key := agenticManualCancelKey(taskID)
	a.mu.Lock()
	a.clearActiveCancelLocked(key, key)
	a.mu.Unlock()

	task, _, err := a.loadAgenticWorkspaceTask(projectName, taskID)
	if err != nil {
		return agenticWorkspaceReview{}, err
	}
	status := agenticWorkspaceStatusNeedsUserInput
	summary := "The approved Manual-mode command finished. Its result is waiting in the user prompt box."
	if result.Cancelled || result.TimedOut {
		status = agenticWorkspaceStatusInterrupted
		summary = "The active Manual-mode command was interrupted. Partial staged files and available output were preserved."
	}
	if task.Status == agenticWorkspaceStatusInterrupted {
		status = task.Status
		summary = task.Summary
	}
	if _, err := a.clearAgenticPendingCommand(projectName, taskID, status, summary, true); err != nil {
		return agenticWorkspaceReview{}, err
	}
	return a.buildAgenticWorkspaceReview(projectName, taskID)
}

func (a *App) stopAgenticManualCommand(projectName, taskID, reason string) (agenticWorkspaceReview, error) {
	agenticManualDecisionMu.Lock()
	defer agenticManualDecisionMu.Unlock()
	key := agenticManualCancelKey(taskID)
	builderKey := agenticBuilderCancelKey(taskID)
	a.mu.Lock()
	for _, cancelKey := range []string{key, builderKey} {
		entry, running := a.activeCancels[cancelKey]
		if running && entry.Cancel != nil {
			entry.Cancel()
		}
	}
	a.mu.Unlock()
	summary := strings.TrimSpace(reason)
	if summary == "" {
		summary = "The Manual-mode terminal command was stopped by the user. Partial staged files were preserved."
	}
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindStop, Status: "terminal_stop", Level: "warning", Message: summary})
	a.clearAgenticSemiSessionAllowances(projectName, taskID)
	if _, err := a.clearAgenticPendingCommand(projectName, taskID, agenticWorkspaceStatusInterrupted, summary, true); err != nil {
		return agenticWorkspaceReview{}, err
	}
	return a.buildAgenticWorkspaceReview(projectName, taskID)
}

func (a *App) handleAgenticManualApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticManualDecisionRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	task, _, err := a.loadAgenticWorkspaceTask(projectName, strings.TrimSpace(req.TaskID))
	if err != nil || task.Mode != workModeAgenticModeManual {
		http.Error(w, "the staged task is not using Manual mode", http.StatusConflict)
		return
	}
	ctx, cancel, command, err := a.beginAgenticManualExecution(projectName, strings.TrimSpace(req.TaskID))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	defer cancel()
	_, _ = a.appendAgenticAuditRecord(projectName, req.TaskID, agenticAuditRecord{Kind: agenticAuditKindApproval, Decision: "approved", Status: "approved", Message: "The user approved the stored Manual-mode command request.", Command: cloneWorkModeAgenticCommand(&command)})
	options := defaultAgenticExecutionOptions()
	options.OnEvent = func(event agenticExecutionLiveEvent) {
		a.appendAgenticExecutionLiveEvent(projectName, req.TaskID, event)
	}
	result := a.executeAgenticCommand(ctx, agenticExecutionRequest{ProjectName: projectName, TaskID: req.TaskID, Command: command}, options)
	_, _ = a.appendAgenticAuditRecord(projectName, req.TaskID, agenticAuditRecord{Kind: agenticAuditKindExecutionResult, Status: result.Status, Level: func() string {
		if result.Status == agenticExecutionStatusCompleted {
			return "info"
		}
		return "error"
	}(), Message: "The approved Manual-mode command finished.", Execution: auditExecutionSummary(result)})
	review, finishErr := a.finishAgenticManualExecution(projectName, req.TaskID, result)
	if finishErr != nil {
		http.Error(w, finishErr.Error(), http.StatusInternalServerError)
		return
	}
	message := "Approved Manual-mode command completed. Its result was placed in the prompt box and was not sent to the AI."
	if result.Cancelled || result.TimedOut {
		message = "The approved Manual-mode command was interrupted. Partial staged files and available output were preserved."
	} else if result.Status != agenticExecutionStatusCompleted {
		message = "The approved Manual-mode command finished with an error. Its result was placed in the prompt box."
	}
	prompt := agenticManualPrompt(task, "Approved", command, &result)
	if _, saveErr := a.saveAgenticLatestCommandResult(projectName, req.TaskID, prompt); saveErr != nil {
		http.Error(w, "Could not preserve Manual-mode command continuity: "+saveErr.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, agenticManualDecisionResponse{TaskID: req.TaskID, Decision: "approved", Prompt: prompt, Message: message, Command: command, Execution: &result, Workspace: review})
}

func (a *App) handleAgenticManualDeny(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticManualDecisionRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	task, _, err := a.loadAgenticWorkspaceTask(projectName, strings.TrimSpace(req.TaskID))
	if err != nil || task.Mode != workModeAgenticModeManual {
		http.Error(w, "the staged task is not using Manual mode", http.StatusConflict)
		return
	}
	agenticManualDecisionMu.Lock()
	defer agenticManualDecisionMu.Unlock()
	if err := a.ensureAgenticManualCommandIdle(projectName, req.TaskID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	command, err := a.loadAgenticPendingCommand(projectName, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	_, _ = a.appendAgenticAuditRecord(projectName, req.TaskID, agenticAuditRecord{Kind: agenticAuditKindApproval, Decision: "denied", Status: "denied", Level: "warning", Message: "The user denied the stored Manual-mode command request. Nothing was executed.", Command: cloneWorkModeAgenticCommand(&command)})
	if _, err := a.clearAgenticPendingCommand(projectName, req.TaskID, agenticWorkspaceStatusNeedsUserInput, "The user denied the requested Manual-mode command. The denial is waiting in the prompt box.", true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	review, err := a.buildAgenticWorkspaceReview(projectName, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	prompt := agenticManualPrompt(task, "Denied", command, nil)
	if _, saveErr := a.saveAgenticLatestCommandResult(projectName, req.TaskID, prompt); saveErr != nil {
		http.Error(w, "Could not preserve Manual-mode denial continuity: "+saveErr.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, agenticManualDecisionResponse{TaskID: req.TaskID, Decision: "denied", Prompt: prompt, Message: "Manual-mode command denied. Nothing was executed; the denial was placed in the prompt box.", Command: command, Workspace: review})
}

func (a *App) handleAgenticManualStop(w http.ResponseWriter, r *http.Request) {
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
	review, err := a.stopAgenticManualCommand(projectName, req.TaskID, req.Reason)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, review)
}
