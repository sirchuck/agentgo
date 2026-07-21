package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	agenticSemiDecisionAuto           = "auto"
	agenticSemiDecisionAllowOnce      = "allow_once"
	agenticSemiDecisionAllowSession   = "allow_session"
	agenticSemiDecisionAddWhitelist   = "add_whitelist"
	agenticSemiDecisionDeny           = "deny"
	agenticSemiAuthorizationBuiltIn   = "built_in"
	agenticSemiAuthorizationWhitelist = "whitelist"
	agenticSemiAuthorizationSession   = "session"
	agenticSemiAuthorizationUser      = "user"
	agenticSemiAuthorizationDenied    = "denied"
	agenticSemiAuthorizationNeedsUser = "approval_required"
)

type agenticSemiAuthorization struct {
	Allowed bool   `json:"allowed"`
	Source  string `json:"source"`
	Rule    string `json:"rule,omitempty"`
}

type agenticSemiDecisionRequest struct {
	TaskID   string `json:"taskId"`
	Decision string `json:"decision"`
}

type agenticSemiDecisionResponse struct {
	TaskID            string                  `json:"taskId"`
	Decision          string                  `json:"decision"`
	Authorization     string                  `json:"authorization"`
	Prompt            string                  `json:"prompt"`
	Message           string                  `json:"message"`
	AutoContinue      bool                    `json:"autoContinue"`
	Command           workModeAgenticCommand  `json:"command"`
	Execution         *agenticExecutionResult `json:"execution,omitempty"`
	Workspace         agenticWorkspaceReview  `json:"workspace"`
	WhitelistUpdated  bool                    `json:"whitelistUpdated,omitempty"`
	SessionAuthorized bool                    `json:"sessionAuthorized,omitempty"`
}

func normalizeAgenticExecutable(value string) string {
	clean := filepath.ToSlash(strings.TrimSpace(value))
	if runtime.GOOS == "windows" {
		clean = strings.ToLower(clean)
		clean = strings.TrimSuffix(clean, ".exe")
	}
	return clean
}

func normalizeAgenticExecutableBase(value string) string {
	clean := normalizeAgenticExecutable(value)
	base := path.Base(clean)
	if runtime.GOOS == "windows" {
		base = strings.TrimSuffix(base, ".exe")
	}
	return base
}

func terminalWhitelistTokenMatch(patternValue, actual string, caseInsensitive bool) bool {
	patternValue = strings.TrimSpace(patternValue)
	if caseInsensitive {
		patternValue = strings.ToLower(patternValue)
		actual = strings.ToLower(actual)
	}
	if patternValue == actual {
		return true
	}
	if !strings.ContainsAny(patternValue, "*?") {
		return false
	}
	expression := regexp.QuoteMeta(patternValue)
	expression = strings.ReplaceAll(expression, `\*`, `.*`)
	expression = strings.ReplaceAll(expression, `\?`, `.`)
	matched, err := regexp.MatchString(`^`+expression+`$`, actual)
	return err == nil && matched
}

func terminalWhitelistExecutableMatch(patternValue, actual string) bool {
	patternValue = normalizeAgenticExecutable(patternValue)
	actual = normalizeAgenticExecutable(actual)
	return terminalWhitelistTokenMatch(patternValue, actual, runtime.GOOS == "windows")
}

func terminalWhitelistArgsMatch(patterns, actual []string) bool {
	if len(patterns) != len(actual) {
		return false
	}
	for i := range patterns {
		if !terminalWhitelistTokenMatch(patterns[i], actual[i], false) {
			return false
		}
	}
	return true
}

func terminalWhitelistRuleEnabled(rule terminalWhitelistRule) bool {
	return rule.Enabled == nil || *rule.Enabled
}

func terminalWhitelistRuleMatches(rule terminalWhitelistRule, command workModeAgenticCommand) bool {
	if !terminalWhitelistRuleEnabled(rule) {
		return false
	}
	ruleType := strings.ToLower(strings.TrimSpace(rule.Type))
	command = normalizeWorkModeAgenticCommand(command)
	if ruleType != command.Type {
		return false
	}
	switch ruleType {
	case workModeAgenticCommandDirect:
		return terminalWhitelistExecutableMatch(rule.Executable, command.Executable) && terminalWhitelistArgsMatch(rule.Args, command.Args)
	case workModeAgenticCommandShell:
		return terminalWhitelistTokenMatch(rule.Script, command.Script, false)
	default:
		return false
	}
}

func terminalWhitelistRuleLabel(rule terminalWhitelistRule, index int) string {
	if description := strings.TrimSpace(rule.Description); description != "" {
		return description
	}
	return fmt.Sprintf("project whitelist rule %d", index+1)
}

func (a *App) loadActiveTerminalWhitelist(projectName string) (terminalWhitelistFile, error) {
	if err := a.ensureProjectTerminalConfig(projectName); err != nil {
		return terminalWhitelistFile{}, err
	}
	currentPath, _ := a.terminalProjectConfigPath(projectName, terminalWhitelistFilename)
	data, err := os.ReadFile(currentPath)
	if err != nil {
		return terminalWhitelistFile{}, err
	}
	file, validation := validateTerminalWhitelistJSON(string(data))
	if validation.Valid {
		return file, nil
	}
	fallbackPath, _ := a.terminalProjectConfigPath(projectName, terminalWhitelistLastValidFilename)
	fallback, err := os.ReadFile(fallbackPath)
	if err != nil {
		return terminalWhitelistFile{}, errors.New("the project whitelist is invalid and no last-valid whitelist is available")
	}
	file, validation = validateTerminalWhitelistJSON(string(fallback))
	if !validation.Valid {
		return terminalWhitelistFile{}, errors.New("the project whitelist and last-valid whitelist are both invalid")
	}
	return file, nil
}

func conservativeBuiltInAgenticCommand(command workModeAgenticCommand) (bool, string) {
	command = normalizeWorkModeAgenticCommand(command)
	if command.Type != workModeAgenticCommandDirect {
		return false, ""
	}
	rawExecutable := filepath.ToSlash(strings.TrimSpace(command.Executable))
	if rawExecutable == "" || strings.Contains(rawExecutable, "/") || rawExecutable == "." || rawExecutable == ".." {
		return false, ""
	}
	executable := strings.ToLower(normalizeAgenticExecutableBase(rawExecutable))
	args := command.Args
	if len(args) == 0 {
		switch executable {
		case "pwd", "whoami":
			return true, executable
		}
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version" || args[0] == "version") {
		switch executable {
		case "go", "git", "node", "npm", "python", "python3", "java", "javac", "rustc", "cargo", "dotnet":
			return true, executable + " version"
		}
	}
	if executable == "go" && len(args) >= 2 && args[0] == "env" {
		allowed := map[string]bool{"GOOS": true, "GOARCH": true, "GOVERSION": true, "GOROOT": true, "GOPATH": true, "GOMOD": true, "GOWORK": true}
		for _, arg := range args[1:] {
			if !allowed[arg] {
				return false, ""
			}
		}
		return true, "go env safe fields"
	}
	if executable == "git" && len(args) >= 1 {
		switch args[0] {
		case "status":
			for _, arg := range args[1:] {
				if arg == "--short" || arg == "-s" || arg == "--porcelain" || arg == "--porcelain=v1" || arg == "--porcelain=v2" || arg == "--branch" || arg == "-b" || strings.HasPrefix(arg, "--untracked-files=") || strings.HasPrefix(arg, "--ignored=") {
					continue
				}
				return false, ""
			}
			return true, "git status"
		case "rev-parse":
			allowed := map[string]bool{"--show-toplevel": true, "--is-inside-work-tree": true, "--show-prefix": true, "--show-cdup": true, "--git-dir": true, "HEAD": true}
			if len(args) < 2 {
				return false, ""
			}
			for _, arg := range args[1:] {
				if !allowed[arg] {
					return false, ""
				}
			}
			return true, "git rev-parse"
		}
	}
	if executable == "ls" {
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") && arg != "-l" && arg != "-a" && arg != "-la" && arg != "-al" && arg != "-1" && arg != "-h" && arg != "-lh" && arg != "-hl" {
				return false, ""
			}
		}
		return true, "list staged files"
	}
	if executable == "cat" || executable == "head" || executable == "tail" || executable == "wc" {
		return true, "read staged files"
	}
	return false, ""
}

func agenticSemiCommandKey(command workModeAgenticCommand) string {
	command = normalizeWorkModeAgenticCommand(command)
	data, _ := json.Marshal(command)
	return string(data)
}

func agenticSemiAllowanceMapKey(projectName, taskID string) string {
	return strings.TrimSpace(projectName) + "\x00" + strings.TrimSpace(taskID)
}

func (a *App) agenticSemiSessionAllows(projectName, taskID string, command workModeAgenticCommand) bool {
	key := agenticSemiAllowanceMapKey(projectName, taskID)
	commandKey := agenticSemiCommandKey(command)
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.agenticSemiAllowances != nil && a.agenticSemiAllowances[key] != nil && a.agenticSemiAllowances[key][commandKey]
}

func (a *App) addAgenticSemiSessionAllowance(projectName, taskID string, command workModeAgenticCommand) {
	key := agenticSemiAllowanceMapKey(projectName, taskID)
	commandKey := agenticSemiCommandKey(command)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.agenticSemiAllowances == nil {
		a.agenticSemiAllowances = map[string]map[string]bool{}
	}
	if a.agenticSemiAllowances[key] == nil {
		a.agenticSemiAllowances[key] = map[string]bool{}
	}
	a.agenticSemiAllowances[key][commandKey] = true
}

func (a *App) clearAgenticSemiSessionAllowances(projectName, taskID string) {
	key := agenticSemiAllowanceMapKey(projectName, taskID)
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.agenticSemiAllowances != nil {
		delete(a.agenticSemiAllowances, key)
	}
}

func (a *App) authorizeAgenticSemiCommand(projectName, taskID string, command workModeAgenticCommand) (agenticSemiAuthorization, error) {
	if allowed, rule := conservativeBuiltInAgenticCommand(command); allowed {
		return agenticSemiAuthorization{Allowed: true, Source: agenticSemiAuthorizationBuiltIn, Rule: rule}, nil
	}
	if a.agenticSemiSessionAllows(projectName, taskID, command) {
		return agenticSemiAuthorization{Allowed: true, Source: agenticSemiAuthorizationSession, Rule: "session allowance"}, nil
	}
	file, err := a.loadActiveTerminalWhitelist(projectName)
	if err != nil {
		return agenticSemiAuthorization{}, err
	}
	for i, rule := range file.Rules {
		if terminalWhitelistRuleMatches(rule, command) {
			return agenticSemiAuthorization{Allowed: true, Source: agenticSemiAuthorizationWhitelist, Rule: terminalWhitelistRuleLabel(rule, i)}, nil
		}
	}
	return agenticSemiAuthorization{Allowed: false, Source: agenticSemiAuthorizationNeedsUser}, nil
}

func exactTerminalWhitelistRule(command workModeAgenticCommand) terminalWhitelistRule {
	command = normalizeWorkModeAgenticCommand(command)
	rule := terminalWhitelistRule{Type: command.Type, Description: "Added from Semi-mode approval"}
	if command.Type == workModeAgenticCommandShell {
		rule.Script = command.Script
	} else {
		rule.Executable = command.Executable
		rule.Args = append([]string{}, command.Args...)
	}
	return rule
}

func (a *App) addAgenticCommandToWhitelist(projectName string, command workModeAgenticCommand) error {
	file, err := a.loadActiveTerminalWhitelist(projectName)
	if err != nil {
		return err
	}
	candidate := exactTerminalWhitelistRule(command)
	for _, rule := range file.Rules {
		if terminalWhitelistRuleMatches(rule, command) && terminalWhitelistRuleMatches(candidate, command) {
			return nil
		}
	}
	file.Rules = append(file.Rules, candidate)
	content := string(prettyTerminalJSON(file))
	validation, active, err := a.saveTerminalWhitelist(projectName, content, true)
	if err != nil {
		return err
	}
	if !validation.Valid || !active {
		return errors.New("the exact command rule could not be activated in the project whitelist")
	}
	return nil
}

func agenticSemiContinuationPrompt(task agenticWorkspaceTask, decision string, command workModeAgenticCommand, execution *agenticExecutionResult) string {
	return buildAgenticCommandResultPrompt("AGENTGO SEMI TERMINAL RESULT", decision, &task, command, execution, true)
}

func (a *App) finishAgenticSemiExecution(projectName, taskID string, result agenticExecutionResult) (agenticWorkspaceReview, error) {
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
	summary := "The Semi-mode command finished. Its result is ready for automatic Builder continuation."
	if result.Cancelled || result.TimedOut {
		status = agenticWorkspaceStatusInterrupted
		summary = "The active Semi-mode command was interrupted. Partial staged files and available output were preserved."
		a.clearAgenticSemiSessionAllowances(projectName, taskID)
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

func (a *App) executeAgenticSemiStoredCommand(projectName, taskID, authorization, decision string) (workModeAgenticCommand, agenticExecutionResult, agenticWorkspaceReview, error) {
	ctx, cancel, command, err := a.beginAgenticManualExecution(projectName, taskID)
	if err != nil {
		return workModeAgenticCommand{}, agenticExecutionResult{}, agenticWorkspaceReview{}, err
	}
	defer cancel()
	message := "AgentGO authorized the stored Semi-mode command."
	if authorization == agenticSemiAuthorizationUser {
		message = "The user approved the stored Semi-mode command request."
	}
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindApproval, Decision: decision, Status: "approved", Message: message, Detail: authorization, Command: cloneWorkModeAgenticCommand(&command)})
	options := defaultAgenticExecutionOptions()
	options.OnEvent = func(event agenticExecutionLiveEvent) {
		a.appendAgenticExecutionLiveEvent(projectName, taskID, event)
	}
	result := a.executeAgenticCommand(ctx, agenticExecutionRequest{ProjectName: projectName, TaskID: taskID, Command: command}, options)
	level := "info"
	if result.Status != agenticExecutionStatusCompleted {
		level = "error"
	}
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{Kind: agenticAuditKindExecutionResult, Status: result.Status, Level: level, Message: "The authorized Semi-mode command finished.", Execution: auditExecutionSummary(result)})
	review, err := a.finishAgenticSemiExecution(projectName, taskID, result)
	return command, result, review, err
}

func normalizeAgenticSemiDecision(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case agenticSemiDecisionAuto:
		return agenticSemiDecisionAuto
	case agenticSemiDecisionAllowOnce:
		return agenticSemiDecisionAllowOnce
	case agenticSemiDecisionAllowSession:
		return agenticSemiDecisionAllowSession
	case agenticSemiDecisionAddWhitelist:
		return agenticSemiDecisionAddWhitelist
	case agenticSemiDecisionDeny:
		return agenticSemiDecisionDeny
	default:
		return ""
	}
}

func (a *App) handleAgenticSemiDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req agenticSemiDecisionRequest
	if !decodeAgenticWorkspaceJSON(w, r, &req) {
		return
	}
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.Decision = normalizeAgenticSemiDecision(req.Decision)
	if req.Decision == "" {
		http.Error(w, "unknown Semi-mode approval decision", http.StatusBadRequest)
		return
	}
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
	if task.Mode != workModeAgenticModeSemi {
		http.Error(w, "the staged task is not using Semi mode", http.StatusConflict)
		return
	}
	if req.Decision == agenticSemiDecisionAuto {
		command, loadErr := a.loadAgenticPendingCommand(projectName, req.TaskID)
		if loadErr != nil {
			http.Error(w, loadErr.Error(), http.StatusConflict)
			return
		}
		authorization, authorizationErr := a.authorizeAgenticSemiCommand(projectName, req.TaskID, command)
		if authorizationErr != nil {
			http.Error(w, authorizationErr.Error(), http.StatusInternalServerError)
			return
		}
		if !authorization.Allowed {
			http.Error(w, "the stored command is not automatically authorized in Semi mode", http.StatusConflict)
			return
		}
		storedCommand, result, review, executionErr := a.executeAgenticSemiStoredCommand(projectName, req.TaskID, authorization.Source, authorization.Source)
		if executionErr != nil {
			http.Error(w, executionErr.Error(), http.StatusConflict)
			return
		}
		autoContinue := !result.Cancelled && !result.TimedOut
		message := "Automatically authorized Semi-mode command finished. Its result will return automatically to the Builder."
		if !autoContinue {
			message = "The automatically authorized Semi-mode command was interrupted. Partial staged files and available output were preserved."
		}
		prompt := agenticSemiContinuationPrompt(task, "Automatically authorized ("+authorization.Source+")", storedCommand, &result)
		if _, saveErr := a.saveAgenticLatestCommandResult(projectName, req.TaskID, prompt); saveErr != nil {
			http.Error(w, "Could not preserve Semi-mode command continuity: "+saveErr.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, agenticSemiDecisionResponse{TaskID: req.TaskID, Decision: req.Decision, Authorization: authorization.Source, Prompt: prompt, Message: message, AutoContinue: autoContinue, Command: storedCommand, Execution: &result, Workspace: review})
		return
	}
	if req.Decision == agenticSemiDecisionDeny {
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
		_, _ = a.appendAgenticAuditRecord(projectName, req.TaskID, agenticAuditRecord{Kind: agenticAuditKindApproval, Decision: agenticSemiDecisionDeny, Status: "denied", Level: "warning", Message: "The user denied the stored Semi-mode command request. Nothing was executed.", Command: cloneWorkModeAgenticCommand(&command)})
		if _, err := a.clearAgenticPendingCommand(projectName, req.TaskID, agenticWorkspaceStatusNeedsUserInput, "The user denied the requested Semi-mode command. The denial will return automatically to the Builder.", true); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		review, err := a.buildAgenticWorkspaceReview(projectName, req.TaskID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		prompt := agenticSemiContinuationPrompt(task, "Denied", command, nil)
		if _, saveErr := a.saveAgenticLatestCommandResult(projectName, req.TaskID, prompt); saveErr != nil {
			http.Error(w, "Could not preserve Semi-mode denial continuity: "+saveErr.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, agenticSemiDecisionResponse{TaskID: req.TaskID, Decision: req.Decision, Authorization: agenticSemiAuthorizationDenied, Prompt: prompt, Message: "Semi-mode command denied. Nothing executed; the denial will return automatically to the Builder.", AutoContinue: true, Command: command, Workspace: review})
		return
	}
	command, err := a.loadAgenticPendingCommand(projectName, req.TaskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	response := agenticSemiDecisionResponse{TaskID: req.TaskID, Decision: req.Decision, Authorization: agenticSemiAuthorizationUser, AutoContinue: true}
	switch req.Decision {
	case agenticSemiDecisionAllowSession:
		a.addAgenticSemiSessionAllowance(projectName, req.TaskID, command)
		response.SessionAuthorized = true
	case agenticSemiDecisionAddWhitelist:
		if err := a.addAgenticCommandToWhitelist(projectName, command); err != nil {
			http.Error(w, "could not add command to whitelist: "+err.Error(), http.StatusInternalServerError)
			return
		}
		response.WhitelistUpdated = true
	}
	storedCommand, result, review, err := a.executeAgenticSemiStoredCommand(projectName, req.TaskID, agenticSemiAuthorizationUser, req.Decision)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	response.Command = storedCommand
	response.Execution = &result
	response.Workspace = review
	response.Prompt = agenticSemiContinuationPrompt(task, "Approved ("+strings.ReplaceAll(req.Decision, "_", " ")+")", storedCommand, &result)
	if _, saveErr := a.saveAgenticLatestCommandResult(projectName, req.TaskID, response.Prompt); saveErr != nil {
		http.Error(w, "Could not preserve Semi-mode command continuity: "+saveErr.Error(), http.StatusInternalServerError)
		return
	}
	response.Message = "Approved Semi-mode command finished. Its result will return automatically to the Builder."
	if result.Cancelled || result.TimedOut {
		response.AutoContinue = false
		response.Message = "The approved Semi-mode command was interrupted. Partial staged files and available output were preserved."
	}
	writeJSON(w, http.StatusOK, response)
}
