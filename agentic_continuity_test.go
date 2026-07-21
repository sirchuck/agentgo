package main

import (
	"strings"
	"testing"
)

func TestAgenticContinuityPersistsOriginalTaskAndRollingProgress(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "widget.js", []byte("old"))
	task, staged, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeFull, 7)
	if err != nil {
		t.Fatal(err)
	}

	task, err = app.prepareAgenticTaskContinuity("Demo", task.SessionID, "Fix the widget save behavior.", false)
	if err != nil {
		t.Fatal(err)
	}
	if task.OriginalPrompt != "Fix the widget save behavior." || task.LatestUserInstruction != task.OriginalPrompt {
		t.Fatalf("initial continuity=%+v", task)
	}

	workspace := map[string]any{
		"facts": []any{
			map[string]any{"name": "ruby.version", "value": "3.3.1"},
			map[string]any{"name": "puma.version", "value": "6.4.2"},
		},
	}
	if _, err := app.updateAgenticWorkspaceTask("Demo", task.SessionID, agenticWorkspaceStatusNeedsUserInput, true, "Need choice", "Edited widget.js; browser test still failing; next ask user for expected label.", workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := app.saveAgenticLatestCommandResult("Demo", task.SessionID, "Command: npm test\nExit code: 1\nERROR widget label mismatch"); err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, staged, "widget.js", []byte("new"))

	// An automatic continuation prompt contains command output, not a new user instruction.
	task, err = app.prepareAgenticTaskContinuity("Demo", task.SessionID, "AGENTGO FULL TERMINAL RESULT", true)
	if err != nil {
		t.Fatal(err)
	}
	if task.OriginalPrompt != "Fix the widget save behavior." || task.LatestUserInstruction != "Fix the widget save behavior." {
		t.Fatalf("automatic continuation replaced user intent: %+v", task)
	}

	// A real user answer becomes the latest instruction without replacing the original task.
	task, err = app.prepareAgenticTaskContinuity("Demo", task.SessionID, "Use the compact label.", false)
	if err != nil {
		t.Fatal(err)
	}
	if task.OriginalPrompt != "Fix the widget save behavior." || task.LatestUserInstruction != "Use the compact label." {
		t.Fatalf("user answer continuity=%+v", task)
	}
	if !strings.Contains(task.ProgressSummary, "Edited widget.js") || !strings.Contains(task.LatestCommandResult, "widget label mismatch") {
		t.Fatalf("continuity fields were not preserved: %+v", task)
	}

	review, err := app.buildAgenticWorkspaceReview("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	message := buildAgenticTaskContinuityMessage(task, review)
	for _, required := range []string{
		"ORIGINAL USER TASK", "Fix the widget save behavior.",
		"LATEST USER INSTRUCTION", "Use the compact label.",
		"ROLLING CUMULATIVE PROGRESS SUMMARY", "Edited widget.js",
		"ENVIRONMENT WORKSPACE JSON", `"name": "ruby.version"`, `"value": "3.3.1"`, `"name": "puma.version"`,
		"CURRENT STAGED-CHANGE MANIFEST", "MODIFIED: widget.js",
		"LATEST TERMINAL DECISION / COMMAND RESULT", "ERROR widget label mismatch",
		"Mode: full", "Run: 1 of 7",
	} {
		if !strings.Contains(message, required) {
			t.Fatalf("continuity message missing %q:\n%s", required, message)
		}
	}
}

func TestAgenticContinuityTextIsBounded(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("progress-", agenticProgressSummaryLimit)
	updated, err := app.updateAgenticWorkspaceTask("Demo", task.SessionID, agenticWorkspaceStatusActive, true, "working", long, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.ProgressSummary) > agenticProgressSummaryLimit {
		t.Fatalf("progress summary length=%d", len(updated.ProgressSummary))
	}
	updated, err = app.saveAgenticLatestCommandResult("Demo", task.SessionID, strings.Repeat("result-", agenticCommandBuilderResultLimit))
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.LatestCommandResult) > agenticCommandBuilderResultLimit {
		t.Fatalf("latest command result length=%d", len(updated.LatestCommandResult))
	}
}

func TestAgenticBuilderCommandResultIsStructuredAndLimited(t *testing.T) {
	exitCode := 1
	task := agenticWorkspaceTask{RunNumber: 4, MaxRuns: 50}
	command := workModeAgenticCommand{
		Type: workModeAgenticCommandDirect, Executable: "ruby", Args: []string{"large_report.rb"},
		WorkingDirectory: ".", Purpose: strings.Repeat("purpose ", 1000),
	}
	execution := agenticExecutionResult{
		Status: agenticExecutionStatusFailed, DurationMillis: 1200, ExitCode: &exitCode,
		Error:           strings.Repeat("diagnostic ", 1000),
		AIOutputExcerpt: "OUTPUT RETENTION\n" + strings.Repeat("bulk database row\n", 3000) + "\nSTDERR (prioritized)\nERROR final database failure",
	}
	prompt := buildAgenticCommandResultPrompt("AGENTGO FULL TERMINAL RESULT", "automatic Full-mode authorization", &task, command, &execution, true)
	if len(prompt) > agenticCommandBuilderResultLimit {
		t.Fatalf("prompt length=%d, limit=%d", len(prompt), agenticCommandBuilderResultLimit)
	}
	for _, required := range []string{"Run: 4 of 50", "Execution status: failed", "Exit code: 1", "Continue the same staged task automatically"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q", required)
		}
	}
}
