package main

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkModeTerminalInfoHandler(t *testing.T) {
	root := t.TempDir()
	app := &App{cfg: AppConfig{WorkRoot: root}}
	workspace := filepath.Join(root, "projects", "Demo", "projectwork")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/work-mode/terminal/info?project=Demo", nil)
	rr := httptest.NewRecorder()
	app.handleWorkModeTerminalInfo(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got workModeTerminalInfoResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(got.Computer) == "" {
		t.Fatal("computer name is empty")
	}
	if strings.TrimSpace(got.OperatingSystem) == "" {
		t.Fatal("operating system is empty")
	}
	if strings.TrimSpace(got.Architecture) == "" {
		t.Fatal("architecture is empty")
	}
	if got.ProjectWorkspace != workspace {
		t.Fatalf("project workspace = %q, want %q", got.ProjectWorkspace, workspace)
	}
	if strings.TrimSpace(got.VirtualEnvironment) == "" {
		t.Fatal("virtual environment status is empty")
	}
}

func TestWorkModeTerminalInfoRejectsInvalidProject(t *testing.T) {
	app := &App{cfg: AppConfig{WorkRoot: t.TempDir()}}
	req := httptest.NewRequest(http.MethodGet, "/api/work-mode/terminal/info?project=../escape", nil)
	rr := httptest.NewRecorder()
	app.handleWorkModeTerminalInfo(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestWorkModeTerminalPhaseSixTemplateControls(t *testing.T) {
	path := filepath.Join("templates", "index.html")
	if _, err := template.ParseFiles(path); err != nil {
		t.Fatalf("parse Work Mode template: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, required := range []string{
		`class="work-mode-heading-line"`,
		`id="workModeTerminalBtn"`,
		`id="workModeTerminalPanel"`,
		`id="workModeTerminalSafetyLayer"`,
		`id="workModeTerminalSafetyCheck"`,
		`id="workModeTerminalSafetyCancelBtn"`,
		`id="workModeTerminalSafetyContinueBtn"`,
		`id="workModeTerminalSettingsBtn"`,
		`id="workModeTerminalEnvBtn"`,
		`id="workModeTerminalToggleBtn"`,
		`id="workModeTerminalTokenUsage"`,
		`TASK TOKENS: 0`,
		`normalizeWorkModeAgenticTokenUsage`,
		`renderWorkModeAgenticTokenUsage`,
		`showWorkModeAgenticTokenUsageDetails`,
		`Task tokens: ${usage.estimated ? '~' : ''}${usage.totalTokens.toLocaleString()} (input ${usage.inputEstimated ? '~' : ''}`,
		`data-terminal-mode="full"`,
		`data-terminal-mode="semi"`,
		`data-terminal-mode="manual"`,
		`is-terminal-owner`,
		`Close Work Mode Tab?`,
		`Turn AI Observer off before enabling terminal access.`,
		`Full / Semi / Manual`,
		`Full mode can continue command-and-result turns automatically`,
		`Do not provide sensitive shared folders or credentials.`,
		`Restrict VM access to the host and private-network ranges with firewall rules.`,
		`Expose only the specific ports needed to reach AgentGO or administer the VM.`,
		`Avoid bridged networking unless direct LAN access is intentionally required.`,
		`VirtualBox block guest access to local network while allowing internet`,
		`Agentic context:</strong> During agentic work, AgentGO temporarily ignores Work Mode Memory, AI Builder Role, Response Mode, AgentGO Styling, and model-wide Ugg styling.`,
		`Compact Ugg formatting remains active only for the internal task-progress summary.`,
		`Saved settings are unchanged and remain available in normal Work Mode.`,
		`includeRoleContext: agenticActive ? false : workModeSettings.includeRoleContext !== false`,
		`useMemory: agenticActive ? false : memoryEnabled`,
		`responseMode: agenticActive ? 'auto' : normalizeResponseMode(workModeSettings.responseMode)`,
		`agentGOStyling: agenticActive ? false : workModeSettings.agentGOStyling !== false`,
		`includeRoleContext:false`,
		`useMemory:false`,
		`responseMode:'auto'`,
		`agentGOStyling:false`,
		`Terminal is already active in the yellow conversation tab. Only one tab can control Terminal at a time.`,
		`Terminal access is enabled for this tab. No agentic task is currently running.`,
		`Agentic task completed. Staged changes are awaiting review. Terminal access remains enabled.`,
		`agentic: agenticActive ?`,
		`renderWorkModeAgenticDryRunResult`,
		`id="workModeAgenticReview"`,
		`id="workModeManualApproval"`,
		`id="workModeTerminalStopBtn"`,
		`data-agentic-approve`,
		`data-agentic-deny`,
		`/api/work-mode/agentic-command/approve`,
		`/api/work-mode/agentic-command/deny`,
		`/api/work-mode/agentic-command/semi-decision`,
		`data-agentic-semi-decision="allow_once"`,
		`data-agentic-semi-decision="allow_session"`,
		`data-agentic-semi-decision="add_whitelist"`,
		`continueWorkModeSemiAgentic`,
		`runWorkModeSemiAutomaticCommand`,
		`runWorkModeFullAutomaticCommand`,
		`continueWorkModeFullAgentic`,
		`agenticContinuationPrompt`,
		`continuation:agenticContinuation`,
		`continuation:true`,
		`/api/work-mode/agentic-command/full-execute`,
		`id="workModeAgenticRecoveryModal"`,
		`data-agentic-recovery="review"`,
		`data-agentic-recovery="discard"`,
		`/api/work-mode/agentic-recovery`,
		`/api/work-mode/agentic-disconnect`,
		`window.addEventListener('pagehide'`,
		`const wasSending = !!tab.sending;`,
		`const wasSending = !!owner.sending;`,
		`Could not stop terminal command.`,
		`/api/work-mode/agentic-command/stop`,
		`/api/work-mode/agentic-audit`,
		`Agentic Terminal Audit Trail`,
		`pollWorkModeAgenticAudit`,
		`/api/work-mode/agentic-work/merge`,
		`/api/work-mode/agentic-work/merge-all`,
		`/api/work-mode/agentic-work/reject`,
		`/api/work-mode/agentic-work/discard`,
		`/api/work-mode/agentic-work/interrupt`,
		`Semi command paused for a decision in the terminal panel.`,
		`id="workModeTerminalWhitelistModal"`,
		`id="workModeTerminalEnvModal"`,
		`/api/work-mode/terminal/whitelist`,
		`/api/work-mode/terminal/environment`,
		`/api/work-mode/terminal/info`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("Work Mode terminal Phase 6 template missing %q", required)
		}
	}
	for _, forbidden := range []string{
		`/api/work-mode/terminal/execute`,
		`exec.Command(`,
		`Phase 6 Full / Semi / Manual`,
		`Phase 6 runs Full-mode command/result continuation automatically`,
		`data-agentic-recovery="not_now"`,
		`data-agentic-recovery="continue_new"`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("Phase 5 template unexpectedly contains forbidden execution wiring %q", forbidden)
		}
	}
}
