package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAgenticExecutionHelperProcess(t *testing.T) {
	mode := os.Getenv("AGENTGO_HELPER_MODE")
	if mode == "" {
		return
	}
	switch mode {
	case "echo-mode":
		cwd, _ := os.Getwd()
		fmt.Printf("stdout-start|secret=%s|inherited=%s|cwd=%s|workspace=%s|temp=%s|stdout-end", os.Getenv("AGENTGO_TEST_SECRET"), os.Getenv("AGENTGO_INHERITED_SECRET"), cwd, os.Getenv("AGENTGO_WORKSPACE"), os.Getenv("TMP"))
		fmt.Fprintf(os.Stderr, "stderr-start|secret=%s|stderr-end", os.Getenv("AGENTGO_TEST_SECRET"))
		os.Exit(0)
	case "shell-mode":
		fmt.Print("shell-ok")
		os.Exit(0)
	case "flood-mode":
		fmt.Print("BEGIN-")
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 16*1024))
		fmt.Print("-END")
		os.Exit(0)
	case "exit-mode":
		fmt.Fprint(os.Stderr, "intentional failure")
		os.Exit(7)
	case "spawn-mode":
		child := exec.Command(os.Args[0], "-test.run=TestAgenticExecutionHelperProcess")
		child.Env = replaceAgenticTestEnvironment(os.Environ(), "AGENTGO_HELPER_MODE", "child-mode")
		if err := child.Start(); err != nil {
			os.Exit(8)
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	case "child-mode":
		time.Sleep(900 * time.Millisecond)
		_ = os.WriteFile(os.Getenv("AGENTGO_SENTINEL"), []byte("child survived"), 0o644)
		os.Exit(0)
	default:
		os.Exit(9)
	}
}

func replaceAgenticTestEnvironment(environment []string, name, value string) []string {
	out := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if ok && strings.EqualFold(key, name) {
			continue
		}
		out = append(out, item)
	}
	return append(out, name+"="+value)
}

func copyAgenticExecutionHelper(t *testing.T, canonical string) string {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "agentgo-exec-helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(canonical, name)
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatal(err)
	}
	return name
}

func newAgenticExecutionFixture(t *testing.T, variables []terminalEnvironment) (*App, agenticWorkspaceTask, string, string) {
	t.Helper()
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	helperName := copyAgenticExecutionHelper(t, canonical)
	if err := app.ensureProjectTerminalConfig("Demo"); err != nil {
		t.Fatal(err)
	}
	if err := app.saveTerminalEnvironment("Demo", terminalEnvironmentFile{Variables: variables}); err != nil {
		t.Fatal(err)
	}
	task, staged, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	return app, task, staged, helperName
}

func agenticHelperCommand(helperName string) workModeAgenticCommand {
	return workModeAgenticCommand{
		Type: workModeAgenticCommandDirect, Executable: "./" + helperName,
		Args:             []string{"-test.run=TestAgenticExecutionHelperProcess"},
		WorkingDirectory: ".", Purpose: "Exercise the isolated Phase 4B command runner.",
	}
}

func TestAgenticExecutionOptionsEnforceHardLimits(t *testing.T) {
	options := normalizeAgenticExecutionOptions(agenticExecutionOptions{Timeout: 24 * time.Hour, OutputLimit: 9 * 1024 * 1024})
	if options.Timeout != 20*time.Minute || options.OutputLimit != 2*1024*1024 {
		t.Fatalf("options=%+v", options)
	}
}

func TestAgenticAIOutputExcerptHonorsLimit(t *testing.T) {
	excerpt, truncated := truncateAgenticHeadTailText(strings.Repeat("a", agenticCommandAIExcerptLimit*2), agenticCommandAIExcerptLimit)
	if !truncated || len(excerpt) > agenticCommandAIExcerptLimit {
		t.Fatalf("truncated=%v length=%d", truncated, len(excerpt))
	}
}

func TestAgenticOutputCollectorRedactsAcrossInterleavedWrites(t *testing.T) {
	collector := newAgenticOutputCollector(4096)
	stdout, stderr := collector.writer(agenticExecutionStreamStdout), collector.writer(agenticExecutionStreamStderr)
	_, _ = stdout.Write([]byte("before split-"))
	_, _ = stderr.Write([]byte("other-stream"))
	_, _ = stdout.Write([]byte("secret-value after"))
	gotOut, gotErr, records := collector.snapshot([]terminalEnvironment{{Name: "SECRET", Value: "split-secret-value"}})
	if strings.Contains(gotOut.Text, "split-secret-value") || !strings.Contains(gotOut.Text, "[REDACTED]") {
		t.Fatalf("stdout=%q", gotOut.Text)
	}
	if gotErr.Text != "other-stream" || len(records) != 2 {
		t.Fatalf("stderr=%q records=%+v", gotErr.Text, records)
	}
}

func TestPrepareAgenticExecutionBlocksVisibleEscapes(t *testing.T) {
	app, task, staged, helperName := newAgenticExecutionFixture(t, nil)
	base := agenticHelperCommand(helperName)
	outside := t.TempDir()
	cases := []struct {
		name    string
		mutate  func(*workModeAgenticCommand)
		blocked string
	}{
		{"working directory", func(c *workModeAgenticCommand) { c.WorkingDirectory = "../../outside" }, "../../outside"},
		{"absolute argument", func(c *workModeAgenticCommand) { c.Args = append(c.Args, filepath.Join(outside, "out.txt")) }, filepath.Join(outside, "out.txt")},
		{"flag traversal", func(c *workModeAgenticCommand) { c.Args = append(c.Args, "--output=../../out.txt") }, "../../out.txt"},
		{"shell path", func(c *workModeAgenticCommand) {
			c.Type = workModeAgenticCommandShell
			c.Executable = ""
			c.Args = nil
			c.Script = "cat " + filepath.ToSlash(filepath.Join(outside, "secret.txt"))
		}, filepath.ToSlash(filepath.Join(outside, "secret.txt"))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := base
			command.Args = append([]string(nil), base.Args...)
			tc.mutate(&command)
			_, err := prepareAgenticExecution(app, agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: command})
			var pathErr *agenticVisiblePathError
			if !errors.As(err, &pathErr) || pathErr.Path != tc.blocked {
				t.Fatalf("error=%v path=%+v", err, pathErr)
			}
		})
	}
	allowedURL := base
	allowedURL.Args = append(allowedURL.Args, "https://example.com/path")
	if _, err := prepareAgenticExecution(app, agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: allowedURL}); err != nil {
		t.Fatalf("URL treated as path: %v", err)
	}
	if err := app.saveTerminalEnvironment("Demo", terminalEnvironmentFile{Variables: []terminalEnvironment{{Name: "OUTSIDE_ROOT", Value: outside}}}); err != nil {
		t.Fatal(err)
	}
	dynamicShell := workModeAgenticCommand{Type: workModeAgenticCommandShell, Script: "cat $OUTSIDE_ROOT/secret.txt", WorkingDirectory: ".", Purpose: "Test shell path variable."}
	_, err := prepareAgenticExecution(app, agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: dynamicShell})
	var dynamicErr *agenticVisiblePathError
	if !errors.As(err, &dynamicErr) || dynamicErr.Path != "$OUTSIDE_ROOT/secret.txt" {
		t.Fatalf("dynamic error=%v path=%+v", err, dynamicErr)
	}
	if runtime.GOOS != "windows" {
		outsideFile := filepath.Join(outside, "outside.txt")
		_ = os.WriteFile(outsideFile, []byte("outside"), 0o644)
		if err := os.Symlink(outsideFile, filepath.Join(staged, "outside-link")); err == nil {
			linked := base
			linked.Args = append(linked.Args, "outside-link")
			_, err = prepareAgenticExecution(app, agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: linked})
			var pathErr *agenticVisiblePathError
			if !errors.As(err, &pathErr) {
				t.Fatalf("symlink error=%v", err)
			}
		}
	}
}

func TestExecuteAgenticCommandUsesStagedWorkspaceAndRedactsEnvironment(t *testing.T) {
	secret, inherited := "phase4b-super-secret-value", "must-not-be-inherited"
	t.Setenv("AGENTGO_INHERITED_SECRET", inherited)
	variables := []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "echo-mode"}, {Name: "AGENTGO_TEST_SECRET", Value: secret}}
	app, task, staged, helperName := newAgenticExecutionFixture(t, variables)
	command := agenticHelperCommand(helperName)
	command.Purpose = "Purpose contains " + secret
	result := app.executeAgenticCommand(context.Background(), agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: command}, agenticExecutionOptions{Timeout: 5 * time.Second, OutputLimit: 32 * 1024, GracePeriod: 100 * time.Millisecond})
	if result.Status != agenticExecutionStatusCompleted || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Stdout.Text, "secret=[REDACTED]") || !strings.Contains(result.Stderr.Text, "secret=[REDACTED]") {
		t.Fatalf("stdout=%q stderr=%q", result.Stdout.Text, result.Stderr.Text)
	}
	if strings.Contains(result.Stdout.Text, inherited) || !strings.Contains(result.Stdout.Text, "inherited=") {
		t.Fatalf("inherited=%q", result.Stdout.Text)
	}
	if !strings.Contains(result.Stdout.Text, "cwd="+staged) || !strings.Contains(result.Stdout.Text, "workspace="+staged) || !strings.Contains(result.Stdout.Text, filepath.Join("runtime", "tmp")) {
		t.Fatalf("paths=%q", result.Stdout.Text)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), inherited) {
		t.Fatal("secret leaked into result")
	}
	canonical, _ := app.projectWorkRoot("Demo")
	if _, err := os.Stat(filepath.Join(canonical, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime in canonical: %v", err)
	}
}

func TestExecuteAgenticCommandSupportsExplicitShell(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "shell-mode"}})
	helper := "./" + helperName
	if runtime.GOOS == "windows" {
		helper = ".\\" + helperName
	}
	result := app.executeAgenticCommand(context.Background(), agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: workModeAgenticCommand{Type: workModeAgenticCommandShell, Script: helper + " -test.run=TestAgenticExecutionHelperProcess", WorkingDirectory: ".", Purpose: "Test shell."}}, agenticExecutionOptions{Timeout: 5 * time.Second, OutputLimit: 4096, GracePeriod: 100 * time.Millisecond})
	if result.Status != agenticExecutionStatusCompleted || !strings.Contains(result.Stdout.Text, "shell-ok") {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecuteAgenticCommandRetainsHeadAndTail(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "flood-mode"}})
	result := app.executeAgenticCommand(context.Background(), agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: agenticHelperCommand(helperName)}, agenticExecutionOptions{Timeout: 5 * time.Second, OutputLimit: 1024, GracePeriod: 100 * time.Millisecond})
	if result.Status != agenticExecutionStatusCompleted || !result.Stdout.Truncated || result.Stdout.RetainedBytes > 1024 || result.Stdout.OmittedBytes <= 0 {
		t.Fatalf("result=%+v", result)
	}
	if !strings.Contains(result.Stdout.Text, "BEGIN-") || !strings.Contains(result.Stdout.Text, "-END") || !strings.Contains(result.Stdout.Text, "AgentGO omitted") {
		t.Fatalf("stdout=%q", result.Stdout.Text)
	}
}

func TestExecuteAgenticCommandReportsNonzeroExit(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "exit-mode"}})
	result := app.executeAgenticCommand(context.Background(), agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: agenticHelperCommand(helperName)}, agenticExecutionOptions{Timeout: 5 * time.Second, OutputLimit: 4096, GracePeriod: 100 * time.Millisecond})
	if result.Status != agenticExecutionStatusFailed || result.ExitCode == nil || *result.ExitCode != 7 || !strings.Contains(result.Stderr.Text, "intentional failure") {
		t.Fatalf("result=%+v", result)
	}
}

func TestExecuteAgenticCommandTimeoutTerminatesProcessTree(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "child-survived.txt")
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "spawn-mode"}, {Name: "AGENTGO_SENTINEL", Value: sentinel}})
	result := app.executeAgenticCommand(context.Background(), agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: agenticHelperCommand(helperName)}, agenticExecutionOptions{Timeout: 120 * time.Millisecond, OutputLimit: 4096, GracePeriod: 120 * time.Millisecond})
	if result.Status != agenticExecutionStatusTimedOut || !result.TimedOut {
		t.Fatalf("result=%+v", result)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child survived: %v", err)
	}
}

func TestExecuteAgenticCommandCancellationTerminatesProcessTree(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "child-survived.txt")
	app, task, _, helperName := newAgenticExecutionFixture(t, []terminalEnvironment{{Name: "AGENTGO_HELPER_MODE", Value: "spawn-mode"}, {Name: "AGENTGO_SENTINEL", Value: sentinel}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	result := app.executeAgenticCommand(ctx, agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: agenticHelperCommand(helperName)}, agenticExecutionOptions{Timeout: 5 * time.Second, OutputLimit: 4096, GracePeriod: 100 * time.Millisecond})
	if result.Status != agenticExecutionStatusCancelled || !result.Cancelled {
		t.Fatalf("result=%+v", result)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("child survived: %v", err)
	}
}

func TestExecuteAgenticCommandBlockedResultDoesNotStart(t *testing.T) {
	app, task, _, helperName := newAgenticExecutionFixture(t, nil)
	command := agenticHelperCommand(helperName)
	command.Args = append(command.Args, "../../escape.txt")
	result := app.executeAgenticCommand(context.Background(), agenticExecutionRequest{ProjectName: "Demo", TaskID: task.SessionID, Command: command}, agenticExecutionOptions{})
	if result.Status != agenticExecutionStatusBlocked || result.BlockedPath != "../../escape.txt" || result.StartedAt != "" {
		t.Fatalf("result=%+v", result)
	}
}

func TestAgenticAIOutputExcerptPrioritizesDiagnosticsWithinLimit(t *testing.T) {
	stdoutText := "BEGIN setup\n" + strings.Repeat("ordinary database row\n", 2000) + "ERROR widget migration failed at widget.rb:42\n" + strings.Repeat("more rows\n", 2000) + "STACK TRACE FINAL FRAME widget.rb:99"
	stderrText := "warning: retrying\nFATAL database unavailable\ntraceback final stderr frame"
	stdout := agenticCapturedOutput{Text: stdoutText, TotalBytes: int64(len(stdoutText)), RetainedBytes: int64(len(stdoutText)), OmittedBytes: 8192, Truncated: true}
	stderr := agenticCapturedOutput{Text: stderrText, TotalBytes: int64(len(stderrText)), RetainedBytes: int64(len(stderrText))}
	excerpt, truncated := buildAgenticAIOutputExcerpt(stdout, stderr)
	if !truncated || len(excerpt) > agenticCommandAIExcerptLimit {
		t.Fatalf("truncated=%v length=%d", truncated, len(excerpt))
	}
	for _, required := range []string{
		"OUTPUT RETENTION", "omitted=8192", "STDERR (prioritized)", "FATAL database unavailable",
		"STDOUT IMPORTANT LINES", "ERROR widget migration failed at widget.rb:42",
		"STDOUT HEAD / TAIL", "BEGIN setup", "STACK TRACE FINAL FRAME widget.rb:99",
	} {
		if !strings.Contains(excerpt, required) {
			t.Fatalf("excerpt missing %q:\n%s", required, excerpt)
		}
	}
}
