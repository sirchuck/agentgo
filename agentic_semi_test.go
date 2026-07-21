package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalWhitelistRuleMatchesExecutableAndArguments(t *testing.T) {
	command := workModeAgenticCommand{Type: "direct", Executable: "go", Args: []string{"test", "./..."}}
	if !terminalWhitelistRuleMatches(terminalWhitelistRule{Type: "direct", Executable: "go", Args: []string{"test", "./..."}}, command) {
		t.Fatal("exact executable-plus-arguments rule did not match")
	}
	if terminalWhitelistRuleMatches(terminalWhitelistRule{Type: "direct", Executable: "go", Args: []string{"test"}}, command) {
		t.Fatal("executable-only/partial argument rule matched a longer command")
	}
	if !terminalWhitelistRuleMatches(terminalWhitelistRule{Type: "direct", Executable: "go", Args: []string{"test", "./*"}}, command) {
		t.Fatal("argument wildcard did not match")
	}
	if terminalWhitelistRuleMatches(terminalWhitelistRule{Type: "direct", Executable: "go", Args: []string{"build", "./..."}}, command) {
		t.Fatal("different arguments matched")
	}
	pathCommand := command
	pathCommand.Executable = "./go"
	if terminalWhitelistRuleMatches(terminalWhitelistRule{Type: "direct", Executable: "go", Args: []string{"test", "./..."}}, pathCommand) {
		t.Fatal("bare executable whitelist rule matched a staged executable path")
	}
	if !terminalWhitelistRuleMatches(terminalWhitelistRule{Type: "shell", Script: "go test *"}, workModeAgenticCommand{Type: "shell", Script: "go test ./..."}) {
		t.Fatal("shell pattern did not match")
	}
	disabled := false
	if terminalWhitelistRuleMatches(terminalWhitelistRule{Type: "direct", Executable: "go", Args: []string{"test", "./..."}, Enabled: &disabled}, command) {
		t.Fatal("disabled rule matched")
	}
}

func TestConservativeBuiltInAgenticCommands(t *testing.T) {
	allowed := []workModeAgenticCommand{
		{Type: "direct", Executable: "go", Args: []string{"version"}},
		{Type: "direct", Executable: "go", Args: []string{"env", "GOOS", "GOARCH"}},
		{Type: "direct", Executable: "git", Args: []string{"status", "--short"}},
		{Type: "direct", Executable: "git", Args: []string{"rev-parse", "--show-toplevel"}},
		{Type: "direct", Executable: "ls", Args: []string{"-la", "."}},
	}
	for _, command := range allowed {
		if ok, _ := conservativeBuiltInAgenticCommand(command); !ok {
			t.Fatalf("safe command rejected: %+v", command)
		}
	}
	blocked := []workModeAgenticCommand{
		{Type: "direct", Executable: "./go", Args: []string{"version"}},
		{Type: "direct", Executable: "tools/go", Args: []string{"version"}},
		{Type: "direct", Executable: "go", Args: []string{"test", "./..."}},
		{Type: "direct", Executable: "git", Args: []string{"clean", "-fdx"}},
		{Type: "shell", Script: "go version && rm -rf ."},
		{Type: "direct", Executable: "find", Args: []string{".", "-exec", "rm", "{}", ";"}},
	}
	for _, command := range blocked {
		if ok, _ := conservativeBuiltInAgenticCommand(command); ok {
			t.Fatalf("unsafe command accepted: %+v", command)
		}
	}
}

func TestSemiAuthorizationUsesBuiltInSessionAndActiveWhitelist(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	builtIn := workModeAgenticCommand{Type: "direct", Executable: "go", Args: []string{"version"}, WorkingDirectory: ".", Purpose: "Read Go version"}
	auth, err := app.authorizeAgenticSemiCommand("Demo", "task-does-not-need-session", builtIn)
	if err != nil || !auth.Allowed || auth.Source != agenticSemiAuthorizationBuiltIn {
		t.Fatalf("built-in authorization=%+v err=%v", auth, err)
	}

	command := workModeAgenticCommand{Type: "direct", Executable: "go", Args: []string{"test", "./..."}, WorkingDirectory: ".", Purpose: "Run tests"}
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeSemi, 50)
	if err != nil {
		t.Fatal(err)
	}
	auth, err = app.authorizeAgenticSemiCommand("Demo", task.SessionID, command)
	if err != nil || auth.Allowed {
		t.Fatalf("unlisted authorization=%+v err=%v", auth, err)
	}
	app.addAgenticSemiSessionAllowance("Demo", task.SessionID, command)
	auth, err = app.authorizeAgenticSemiCommand("Demo", task.SessionID, command)
	if err != nil || !auth.Allowed || auth.Source != agenticSemiAuthorizationSession {
		t.Fatalf("session authorization=%+v err=%v", auth, err)
	}
	other := command
	other.Args = []string{"test", "./pkg"}
	auth, err = app.authorizeAgenticSemiCommand("Demo", task.SessionID, other)
	if err != nil || auth.Allowed {
		t.Fatalf("session allowance incorrectly matched other arguments: %+v err=%v", auth, err)
	}
	app.clearAgenticSemiSessionAllowances("Demo", task.SessionID)
	if app.agenticSemiSessionAllows("Demo", task.SessionID, command) {
		t.Fatal("session allowance survived clear")
	}

	if err := app.addAgenticCommandToWhitelist("Demo", command); err != nil {
		t.Fatal(err)
	}
	auth, err = app.authorizeAgenticSemiCommand("Demo", task.SessionID, command)
	if err != nil || !auth.Allowed || auth.Source != agenticSemiAuthorizationWhitelist {
		t.Fatalf("whitelist authorization=%+v err=%v", auth, err)
	}
	path, _ := app.terminalProjectConfigPath("Demo", terminalWhitelistFilename)
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"executable": "go"`) || !strings.Contains(string(data), `"./..."`) {
		t.Fatalf("whitelist content=%s err=%v", data, err)
	}
}

func postSemiDecision(t *testing.T, app *App, taskID, decision string) (*httptest.ResponseRecorder, agenticSemiDecisionResponse) {
	t.Helper()
	body, _ := json.Marshal(agenticSemiDecisionRequest{TaskID: taskID, Decision: decision})
	req := httptest.NewRequest(http.MethodPost, "/api/work-mode/agentic-command/semi-decision", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	app.handleAgenticSemiDecision(rr, req)
	var response agenticSemiDecisionResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
	}
	return rr, response
}

func TestSemiAutoDecisionExecutesOnlyReauthorizedCommand(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeSemi, 50)
	if err != nil {
		t.Fatal(err)
	}
	command := workModeAgenticCommand{Type: "direct", Executable: "go", Args: []string{"version"}, WorkingDirectory: ".", Purpose: "Read Go version"}
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, command); err != nil {
		t.Fatal(err)
	}
	rr, response := postSemiDecision(t, app, task.SessionID, agenticSemiDecisionAuto)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if response.Execution == nil || response.Execution.Status != agenticExecutionStatusCompleted || !response.AutoContinue || !strings.Contains(response.Prompt, "AGENTGO SEMI TERMINAL RESULT") {
		t.Fatalf("response=%+v", response)
	}

	unsafeTask, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeSemi, 50)
	if err != nil {
		t.Fatal(err)
	}
	unsafe := workModeAgenticCommand{Type: "direct", Executable: "go", Args: []string{"test", "./..."}, WorkingDirectory: ".", Purpose: "Run tests"}
	if _, err := app.saveAgenticPendingCommand("Demo", unsafeTask.SessionID, unsafe); err != nil {
		t.Fatal(err)
	}
	rr, _ = postSemiDecision(t, app, unsafeTask.SessionID, agenticSemiDecisionAuto)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "not automatically authorized") {
		t.Fatalf("unsafe auto status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSemiDenyReturnsAutomaticallyAndExecutesNothing(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	marker := filepath.Join(canonical, "should-not-exist.txt")
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeSemi, 50)
	if err != nil {
		t.Fatal(err)
	}
	command := workModeAgenticCommand{Type: "shell", Script: "echo denied > " + filepath.Base(marker), WorkingDirectory: ".", Purpose: "Should be denied"}
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, command); err != nil {
		t.Fatal(err)
	}
	rr, response := postSemiDecision(t, app, task.SessionID, agenticSemiDecisionDeny)
	if rr.Code != http.StatusOK || !response.AutoContinue || response.Execution != nil || response.Authorization != agenticSemiAuthorizationDenied {
		t.Fatalf("status=%d response=%+v body=%s", rr.Code, response, rr.Body.String())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("denied command changed canonical projectwork: %v", err)
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil || loaded.PendingCommand != nil {
		t.Fatalf("pending command not cleared: %+v err=%v", loaded.PendingCommand, err)
	}
}

func TestSemiSessionAllowancesClearWhenTaskIsInterrupted(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeSemi, 50)
	if err != nil {
		t.Fatal(err)
	}
	command := workModeAgenticCommand{Type: "direct", Executable: "go", Args: []string{"test", "./..."}}
	app.addAgenticSemiSessionAllowance("Demo", task.SessionID, command)
	if _, err := app.interruptAgenticWorkspaceTask("Demo", task.SessionID, "stop"); err != nil {
		t.Fatal(err)
	}
	if app.agenticSemiSessionAllows("Demo", task.SessionID, command) {
		t.Fatal("session allowance remained after interruption")
	}
}

func TestSemiAddToWhitelistDecisionActivatesExactCommand(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeSemi, 50)
	if err != nil {
		t.Fatal(err)
	}
	command := workModeAgenticCommand{Type: "direct", Executable: "go", Args: []string{"env", "GOENV"}, WorkingDirectory: ".", Purpose: "Read the Go environment file location"}
	if allowed, _ := conservativeBuiltInAgenticCommand(command); allowed {
		t.Fatal("test command unexpectedly matched a built-in rule")
	}
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, command); err != nil {
		t.Fatal(err)
	}
	rr, response := postSemiDecision(t, app, task.SessionID, agenticSemiDecisionAddWhitelist)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !response.WhitelistUpdated || response.Execution == nil || !response.AutoContinue {
		t.Fatalf("response=%+v", response)
	}
	auth, err := app.authorizeAgenticSemiCommand("Demo", task.SessionID, command)
	if err != nil || !auth.Allowed || auth.Source != agenticSemiAuthorizationWhitelist {
		t.Fatalf("authorization=%+v err=%v", auth, err)
	}
	file, err := app.loadActiveTerminalWhitelist("Demo")
	if err != nil || len(file.Rules) != 1 || file.Rules[0].Executable != "go" || strings.Join(file.Rules[0].Args, " ") != "env GOENV" {
		t.Fatalf("whitelist=%+v err=%v", file, err)
	}
}

func TestSemiTaskCannotChangeTerminalModeDuringContinuation(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeSemi, 50)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = app.startOrLoadAgenticWorkspaceTask("Demo", workModeAgenticRequest{Enabled: true, Mode: workModeAgenticModeManual, MaxRuns: 50, TaskID: task.SessionID})
	if err == nil || !strings.Contains(err.Error(), "cannot change") {
		t.Fatalf("mode change err=%v", err)
	}
}

func TestSemiSessionAllowanceClearsOnTerminalStop(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeSemi, 50)
	if err != nil {
		t.Fatal(err)
	}
	command := workModeAgenticCommand{Type: "direct", Executable: "go", Args: []string{"test", "./..."}, WorkingDirectory: ".", Purpose: "Run tests"}
	app.addAgenticSemiSessionAllowance("Demo", task.SessionID, command)
	if _, err := app.saveAgenticPendingCommand("Demo", task.SessionID, command); err != nil {
		t.Fatal(err)
	}
	if _, err := app.stopAgenticManualCommand("Demo", task.SessionID, "Terminal Stop test"); err != nil {
		t.Fatal(err)
	}
	if app.agenticSemiSessionAllows("Demo", task.SessionID, command) {
		t.Fatal("session allowance remained after Terminal Stop")
	}
}
