package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	workModeAgenticStatusRunCommand     = "run_command"
	workModeAgenticStatusComplete       = "complete"
	workModeAgenticStatusNeedsUserInput = "needs_user_input"
	workModeAgenticStatusProtocolError  = "protocol_error"

	workModeAgenticCommandDirect = "direct"
	workModeAgenticCommandShell  = "shell"

	workModeAgenticModeFull   = "full"
	workModeAgenticModeSemi   = "semi"
	workModeAgenticModeManual = "manual"

	agenticWorkspaceMaxFacts     = 64
	agenticWorkspaceFactNameMax  = 160
	agenticWorkspaceFactValueMax = 1000
	agenticWorkspaceJSONMaxBytes = 32768
)

type workModeAgenticRequest struct {
	Enabled      bool   `json:"enabled,omitempty"`
	Mode         string `json:"mode,omitempty"`
	MaxRuns      int    `json:"maxRuns,omitempty"`
	TaskID       string `json:"taskId,omitempty"`
	Continuation bool   `json:"continuation,omitempty"`
}

type workModeAgenticCommand struct {
	Type             string   `json:"type"`
	Executable       string   `json:"executable"`
	Args             []string `json:"args"`
	Script           string   `json:"script"`
	WorkingDirectory string   `json:"working_directory"`
	Purpose          string   `json:"purpose"`
}

type workModeAgenticEnvironmentDescriptor struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type workModeAgenticResult struct {
	Enabled            bool                                   `json:"enabled"`
	Mode               string                                 `json:"mode,omitempty"`
	Status             string                                 `json:"status"`
	DryRun             bool                                   `json:"dryRun"`
	Paused             bool                                   `json:"paused"`
	ApprovalRequired   bool                                   `json:"approvalRequired,omitempty"`
	Message            string                                 `json:"message,omitempty"`
	Command            *workModeAgenticCommand                `json:"command,omitempty"`
	Summary            string                                 `json:"summary,omitempty"`
	Question           string                                 `json:"question,omitempty"`
	ValidationErrors   []string                               `json:"validationErrors,omitempty"`
	RawResponse        string                                 `json:"rawResponse,omitempty"`
	Environment        []workModeAgenticEnvironmentDescriptor `json:"environment,omitempty"`
	Workspace          *agenticWorkspaceReview                `json:"workspace,omitempty"`
	Authorization      string                                 `json:"authorization,omitempty"`
	ApprovalOptions    []string                               `json:"approvalOptions,omitempty"`
	AutoExecute        bool                                   `json:"autoExecute,omitempty"`
	AutoContinue       bool                                   `json:"autoContinue,omitempty"`
	ContinuationPrompt string                                 `json:"continuationPrompt,omitempty"`
	Execution          *agenticExecutionResult                `json:"execution,omitempty"`
}

func enforceWorkModeAgenticContextIsolation(req *workModeRequest, model *ModelConfig) {
	if req == nil || model == nil || !req.Agentic.Enabled {
		return
	}
	disabled := false
	req.IncludeRoleContext = &disabled
	req.ResponseMode = "auto"
	req.UseMemory = false
	req.MemoryName = ""
	req.MemoryContent = nil
	req.UseAgentGOStyling = false
	model.UseUggPrompt = false
}

func normalizeWorkModeAgenticRequest(req workModeAgenticRequest) (workModeAgenticRequest, error) {
	if !req.Enabled {
		return workModeAgenticRequest{}, nil
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	switch mode {
	case workModeAgenticModeFull, workModeAgenticModeSemi, workModeAgenticModeManual:
	default:
		return workModeAgenticRequest{}, fmt.Errorf("unknown agentic terminal mode %q", req.Mode)
	}
	maxRuns := req.MaxRuns
	if maxRuns <= 0 {
		maxRuns = 50
	}
	taskID := strings.TrimSpace(req.TaskID)
	if taskID != "" && !validAgenticWorkspaceSessionID(taskID) {
		return workModeAgenticRequest{}, errors.New("invalid agentic workspace task id")
	}
	return workModeAgenticRequest{Enabled: true, Mode: mode, MaxRuns: maxRuns, TaskID: taskID, Continuation: req.Continuation}, nil
}

func newAgenticEnvironmentWorkspace() map[string]any {
	return map[string]any{"facts": []any{}}
}

func normalizeAgenticEnvironmentWorkspace(workspace map[string]any) map[string]any {
	if len(workspace) == 0 {
		return newAgenticEnvironmentWorkspace()
	}
	return workspace
}

func workModeAgenticWorkspaceJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"facts": map[string]any{
				"type":     "array",
				"maxItems": agenticWorkspaceMaxFacts,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":  map[string]any{"type": "string", "maxLength": agenticWorkspaceFactNameMax},
						"value": map[string]any{"type": "string", "maxLength": agenticWorkspaceFactValueMax},
					},
					"required":             []string{"name", "value"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"facts"},
		"additionalProperties": false,
	}
}

func validateAgenticEnvironmentWorkspace(workspace map[string]any) error {
	if workspace == nil {
		return errors.New("workspace is required and must be a JSON object")
	}
	data, err := json.Marshal(workspace)
	if err != nil {
		return fmt.Errorf("workspace must be valid JSON: %w", err)
	}
	if len(data) > agenticWorkspaceJSONMaxBytes {
		return fmt.Errorf("workspace must be at most %d bytes", agenticWorkspaceJSONMaxBytes)
	}
	if len(workspace) == 0 {
		return nil
	}
	if len(workspace) != 1 {
		return errors.New("workspace must contain only the facts array")
	}
	if _, ok := workspace["facts"]; !ok {
		return errors.New("workspace.facts is required")
	}
	var shaped struct {
		Facts []map[string]any `json:"facts"`
	}
	if err := json.Unmarshal(data, &shaped); err != nil {
		return fmt.Errorf("workspace.facts must be an array of objects: %w", err)
	}
	if shaped.Facts == nil {
		return errors.New("workspace.facts must be an array")
	}
	if len(shaped.Facts) > agenticWorkspaceMaxFacts {
		return fmt.Errorf("workspace.facts must contain at most %d entries", agenticWorkspaceMaxFacts)
	}
	seen := map[string]bool{}
	for index, fact := range shaped.Facts {
		if len(fact) != 2 {
			return fmt.Errorf("workspace.facts[%d] must contain only name and value", index)
		}
		nameValue, nameOK := fact["name"].(string)
		valueValue, valueOK := fact["value"].(string)
		if !nameOK || !valueOK {
			return fmt.Errorf("workspace.facts[%d] name and value must be strings", index)
		}
		name := strings.TrimSpace(nameValue)
		if name == "" {
			return fmt.Errorf("workspace.facts[%d].name is required", index)
		}
		if len([]rune(name)) > agenticWorkspaceFactNameMax {
			return fmt.Errorf("workspace.facts[%d].name must be at most %d characters", index, agenticWorkspaceFactNameMax)
		}
		if len([]rune(valueValue)) > agenticWorkspaceFactValueMax {
			return fmt.Errorf("workspace.facts[%d].value must be at most %d characters", index, agenticWorkspaceFactValueMax)
		}
		if seen[name] {
			return fmt.Errorf("workspace.facts contains duplicate name %q", name)
		}
		seen[name] = true
	}
	return nil
}

func workModeAgenticCommandJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"type":              map[string]any{"type": "string", "enum": []string{"", workModeAgenticCommandDirect, workModeAgenticCommandShell}},
			"executable":        map[string]any{"type": "string"},
			"args":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"script":            map[string]any{"type": "string"},
			"working_directory": map[string]any{"type": "string"},
			"purpose":           map[string]any{"type": "string"},
		},
		"required":             []string{"type", "executable", "args", "script", "working_directory", "purpose"},
		"additionalProperties": false,
	}
}

func workModeAgenticJSONSchema() map[string]any {
	schema := cloneAnyMap(workModeJSONSchema())
	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		schema["properties"] = props
	}
	props["agentic_status"] = map[string]any{"type": "string", "enum": []string{workModeAgenticStatusRunCommand, workModeAgenticStatusComplete, workModeAgenticStatusNeedsUserInput}}
	props["command"] = workModeAgenticCommandJSONSchema()
	props["summary"] = map[string]any{"type": "string", "maxLength": agenticProgressSummaryLimit}
	props["question"] = map[string]any{"type": "string"}
	props["workspace"] = workModeAgenticWorkspaceJSONSchema()
	required, _ := schema["required"].([]string)
	required = append(required, "agentic_status", "command", "summary", "question", "workspace")
	schema["required"] = required
	return schema
}

func terminalEnvironmentAgenticDescriptors(file terminalEnvironmentFile) []workModeAgenticEnvironmentDescriptor {
	out := make([]workModeAgenticEnvironmentDescriptor, 0, len(file.Variables))
	for _, variable := range file.Variables {
		name := strings.TrimSpace(variable.Name)
		if name == "" {
			continue
		}
		out = append(out, workModeAgenticEnvironmentDescriptor{Name: name, Description: strings.TrimSpace(variable.Description)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func workModeAgenticEnvironmentPrompt(environment []workModeAgenticEnvironmentDescriptor) string {
	if len(environment) == 0 {
		return "- No project environment variables are configured."
	}
	lines := []string{"- The following environment-variable names are available to launched programs. Values are protected and are never provided to you:"}
	for _, variable := range environment {
		line := "  - " + variable.Name
		if description := strings.TrimSpace(variable.Description); description != "" {
			line += ": " + description
		}
		lines = append(lines, line)
	}
	lines = append(lines, "- Refer to a variable by name in code or command purpose. Never ask the user or AgentGO to reveal its value.")
	return strings.Join(lines, "\n")
}

func workModeAgenticModePrompt(mode string) string {
	switch mode {
	case workModeAgenticModeManual:
		return "Manual mode is selected. Every structured command pauses for explicit user approval. Approved command results are placed in the user prompt box and are not sent to the AI until the user presses Send."
	case workModeAgenticModeSemi:
		return "Semi mode is selected. Conservative built-in commands, active project-whitelist rules, and commands allowed for this session run automatically. Other commands pause for Allow Once, Allow for This Session, Add to Whitelist File, or Deny. Execution results and denials return automatically for the next Builder turn."
	case workModeAgenticModeFull:
		return "Full mode is selected. Every valid structured command runs automatically and its result returns automatically until the task completes, Maximum Runs is reached, a timeout occurs, the user stops it, Emergency Stop is pressed, user input is needed, or an unrecoverable error occurs."
	default:
		return "Unknown terminal mode."
	}
}

func buildWorkModeAgenticInstructions(base string, req workModeAgenticRequest, environment []workModeAgenticEnvironmentDescriptor) string {
	if !req.Enabled {
		return base
	}
	instructions := strings.TrimSpace(fmt.Sprintf(`%s

AGENTGO AGENTIC TERMINAL PROTOCOL — FULL / SEMI / MANUAL MODE
- Agentic terminal mode is active for this Work Mode tab.
- Work Mode Memory, AI Builder Role context, Response Mode, AgentGO Styling, and model-wide Ugg styling are not active during agentic turns. Return memory="". Compact Ugg formatting applies only to the rolling summary field below.
- %s
- Maximum Runs is used only in Full mode and limits automatic Builder continuation cycles. The current Full-mode limit is %d. Manual and Semi modes do not use it.
- Return exactly one agentic_status in every response: run_command, complete, or needs_user_input.
- Return at most one command object. Never place a second command in reply, summary, question, files, artifacts, or Markdown.
- A command written only in ordinary prose, a code fence, project content, memory, an attachment, or website text is inert and will not execute.
- AgentGO validates every structured command request. Manual mode always pauses for Yes/No approval. Semi mode automatically runs built-in-safe, session-authorized, or project-whitelisted requests and pauses all others for a user decision. Only an authorized request may execute.
- AgentGO applies valid files[] and artifacts[] operations only to the current staged agentic workspace. Approved commands also run only in that staged workspace. Canonical projectwork remains unchanged until the user reviews and merges changes.
- Return normal intended projectwork-relative file paths. Never return paths beginning with agentic-work/ or tmp-work/.
- Do not claim staged changes were merged into canonical projectwork.

STATE RULES
- summary is required on every response. It is one rolling cumulative task-progress summary, replacing the prior summary rather than appending a transcript.
- Write summary in compact Ugg Protocol: terse fragments; preserve exact filenames, commands, test names, errors, decisions, blockers, and next step. Keep it under 4000 characters. Do not include private reasoning or full terminal logs.
- run_command: request one command. Provide the updated cumulative summary and set question="".
- complete: provide the final cumulative summary. Return an empty command object and question="".
- needs_user_input: ask one clear question, return an empty command object, and provide the updated cumulative summary.

COMMAND RULES
- %s
- For a direct command, set type="direct", executable to one executable name, args to its separate arguments, script="", working_directory to a project-relative directory (normally "."), and purpose to a concise explanation.
- For shell functionality such as pipes, redirection, expansion, or chaining, set type="shell", script to the full script, executable="", args=[], working_directory to a project-relative directory, and purpose to a concise explanation.
- Never encode a shell pipeline or chained command inside executable or args for a direct command.
- Do not request more than one command per turn.

AVAILABLE PROJECT ENVIRONMENT
%s

WORKSPACE MEMORY
- Store only command-verified environment facts in the workspace JSON object, such as installed tools, versions, dependencies, configurations, and system constraints. Maintain and reference it across turns.

REQUIRED AGENTIC JSON ADDITIONS
Include these fields in the existing Work Mode JSON envelope:
{
  "agentic_status": "run_command | complete | needs_user_input",
  "command": {
    "type": "direct | shell",
    "executable": "",
    "args": [],
    "script": "",
    "working_directory": ".",
    "purpose": ""
  },
  "summary": "",
  "question": "",
  "workspace": {"facts": []}
}
Use empty strings/arrays in command when agentic_status is complete or needs_user_input.`, strings.TrimSpace(base), workModeAgenticModePrompt(req.Mode), req.MaxRuns, agenticExecutionEnvironmentInstruction(), workModeAgenticEnvironmentPrompt(environment)))
	return instructions
}

func commandLooksEmpty(command workModeAgenticCommand) bool {
	return strings.TrimSpace(command.Type) == "" && strings.TrimSpace(command.Executable) == "" && len(command.Args) == 0 && strings.TrimSpace(command.Script) == "" && strings.TrimSpace(command.WorkingDirectory) == "" && strings.TrimSpace(command.Purpose) == ""
}

func normalizeWorkModeAgenticCommand(command workModeAgenticCommand) workModeAgenticCommand {
	command.Type = strings.ToLower(strings.TrimSpace(command.Type))
	command.Executable = strings.TrimSpace(command.Executable)
	command.Script = strings.TrimSpace(command.Script)
	command.WorkingDirectory = strings.TrimSpace(command.WorkingDirectory)
	command.Purpose = strings.TrimSpace(command.Purpose)
	if command.Args == nil {
		command.Args = []string{}
	}
	return command
}

func validateWorkModeAgenticResponse(resp workModeAIResponse) []string {
	status := strings.ToLower(strings.TrimSpace(resp.AgenticStatus))
	command := normalizeWorkModeAgenticCommand(resp.AgenticCommand)
	errorsOut := []string{}
	if err := validateAgenticEnvironmentWorkspace(resp.AgenticWorkspace); err != nil {
		errorsOut = append(errorsOut, err.Error())
	}
	if len([]rune(strings.TrimSpace(resp.AgenticSummary))) > agenticProgressSummaryLimit {
		errorsOut = append(errorsOut, fmt.Sprintf("summary must be at most %d characters", agenticProgressSummaryLimit))
	}
	switch status {
	case workModeAgenticStatusRunCommand:
		if strings.TrimSpace(resp.AgenticSummary) == "" {
			errorsOut = append(errorsOut, "summary is required when agentic_status is run_command")
		}
		if strings.TrimSpace(resp.AgenticQuestion) != "" {
			errorsOut = append(errorsOut, "question must be empty when agentic_status is run_command")
		}
		if command.WorkingDirectory == "" {
			errorsOut = append(errorsOut, "command.working_directory is required")
		}
		if command.Purpose == "" {
			errorsOut = append(errorsOut, "command.purpose is required")
		}
		switch command.Type {
		case workModeAgenticCommandDirect:
			if command.Executable == "" {
				errorsOut = append(errorsOut, "command.executable is required for a direct command")
			}
			if command.Script != "" {
				errorsOut = append(errorsOut, "command.script must be empty for a direct command")
			}
		case workModeAgenticCommandShell:
			if command.Script == "" {
				errorsOut = append(errorsOut, "command.script is required for a shell command")
			}
			if command.Executable != "" || len(command.Args) != 0 {
				errorsOut = append(errorsOut, "command.executable and command.args must be empty for a shell command")
			}
		default:
			errorsOut = append(errorsOut, "command.type must be direct or shell")
		}
	case workModeAgenticStatusComplete:
		if strings.TrimSpace(resp.AgenticSummary) == "" {
			errorsOut = append(errorsOut, "summary is required when agentic_status is complete")
		}
		if strings.TrimSpace(resp.AgenticQuestion) != "" {
			errorsOut = append(errorsOut, "question must be empty when agentic_status is complete")
		}
		if !commandLooksEmpty(command) {
			errorsOut = append(errorsOut, "command must be empty when agentic_status is complete")
		}
	case workModeAgenticStatusNeedsUserInput:
		if strings.TrimSpace(resp.AgenticQuestion) == "" {
			errorsOut = append(errorsOut, "question is required when agentic_status is needs_user_input")
		}
		if strings.TrimSpace(resp.AgenticSummary) == "" {
			errorsOut = append(errorsOut, "summary is required when agentic_status is needs_user_input")
		}
		if !commandLooksEmpty(command) {
			errorsOut = append(errorsOut, "command must be empty when agentic_status is needs_user_input")
		}
	default:
		errorsOut = append(errorsOut, "agentic_status must be run_command, complete, or needs_user_input")
	}
	return errorsOut
}

func workModeAgenticDryRunResult(req workModeAgenticRequest, resp workModeAIResponse, environment []workModeAgenticEnvironmentDescriptor) workModeAgenticResult {
	command := normalizeWorkModeAgenticCommand(resp.AgenticCommand)
	result := workModeAgenticResult{
		Enabled:     true,
		Mode:        req.Mode,
		Status:      strings.ToLower(strings.TrimSpace(resp.AgenticStatus)),
		DryRun:      false,
		Paused:      true,
		Summary:     strings.TrimSpace(resp.AgenticSummary),
		Question:    strings.TrimSpace(resp.AgenticQuestion),
		Environment: append([]workModeAgenticEnvironmentDescriptor{}, environment...),
	}
	if result.Status == workModeAgenticStatusRunCommand {
		result.Command = &command
		result.ApprovalRequired = true
		switch req.Mode {
		case workModeAgenticModeFull:
			result.Message = "AgentGO validated the AI command request. Full mode will authorize and execute the stored request automatically."
		case workModeAgenticModeSemi:
			result.Message = "AgentGO validated the AI command request. Semi-mode authorization is being evaluated."
		default:
			result.Message = "AgentGO validated the AI command request. Manual approval is required before anything executes."
		}
	} else if result.Status == workModeAgenticStatusComplete {
		result.Message = "The AI marked the agentic task complete. Review staged changes before merging."
	} else if result.Status == workModeAgenticStatusNeedsUserInput {
		result.Message = "The AI paused the agentic task for user input."
	}
	return result
}

func workModeAgenticProtocolErrorResult(req workModeAgenticRequest, environment []workModeAgenticEnvironmentDescriptor, raw string, errs ...error) workModeAgenticResult {
	messages := []string{}
	for _, err := range errs {
		if err != nil && strings.TrimSpace(err.Error()) != "" {
			messages = append(messages, strings.TrimSpace(err.Error()))
		}
	}
	if len(messages) == 0 {
		messages = append(messages, "The AI response did not match the active AgentGO agentic protocol.")
	}
	return workModeAgenticResult{
		Enabled:          true,
		Mode:             req.Mode,
		Status:           workModeAgenticStatusProtocolError,
		DryRun:           true,
		Paused:           true,
		Message:          "AgentGO interrupted the staged agentic task because the AI response was malformed or ambiguous. Nothing was executed or merged.",
		ValidationErrors: messages,
		RawResponse:      previewForLog(strings.TrimSpace(raw), 65536),
		Environment:      append([]workModeAgenticEnvironmentDescriptor{}, environment...),
	}
}

func validateWorkModeAgenticRequestCompatibility(req workModeRequest, normalized workModeAgenticRequest) error {
	if !normalized.Enabled {
		return nil
	}
	if req.ObserverReview {
		return errors.New("Terminal access is unavailable while AI Observer is active. Turn Observer off before starting an agentic terminal session.")
	}
	return nil
}

func strictWorkModeAgenticJSONCandidate(raw string) (string, error) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if trimmed == "" {
		return "", errors.New("empty agentic response")
	}
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return "", errors.New("agentic response must be one raw JSON object with no prose or Markdown outside it")
	}
	if !json.Valid([]byte(trimmed)) {
		if err := explainInvalidJSON(trimmed); err != nil {
			return "", fmt.Errorf("invalid agentic JSON: %w", err)
		}
		return "", errors.New("invalid agentic JSON")
	}
	return trimmed, nil
}

func requireWorkModeAgenticJSONShape(raw string) error {
	jsonText, err := strictWorkModeAgenticJSONCandidate(raw)
	if err != nil {
		return err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonText), &object); err != nil {
		return err
	}
	allowedTop := map[string]bool{
		"reply": true, "files": true, "artifacts": true, "memory": true, "warnings": true,
		"agentic_status": true, "command": true, "summary": true, "question": true, "workspace": true,
	}
	for _, required := range []string{"reply", "files", "artifacts", "memory", "warnings", "agentic_status", "command", "summary", "question", "workspace"} {
		if _, ok := object[required]; !ok {
			return fmt.Errorf("missing required agentic response field %q", required)
		}
	}
	for field := range object {
		if !allowedTop[field] {
			return fmt.Errorf("unsupported agentic response field %q", field)
		}
	}
	var workspaceObject map[string]any
	if err := json.Unmarshal(object["workspace"], &workspaceObject); err != nil {
		return fmt.Errorf("workspace must be an object: %w", err)
	}
	if err := validateAgenticEnvironmentWorkspace(workspaceObject); err != nil {
		return err
	}
	var commandObject map[string]json.RawMessage
	if err := json.Unmarshal(object["command"], &commandObject); err != nil {
		return fmt.Errorf("command must be an object: %w", err)
	}
	allowedCommand := map[string]bool{"type": true, "executable": true, "args": true, "script": true, "working_directory": true, "purpose": true}
	for _, required := range []string{"type", "executable", "args", "script", "working_directory", "purpose"} {
		if _, ok := commandObject[required]; !ok {
			return fmt.Errorf("missing required command field %q", required)
		}
	}
	for field := range commandObject {
		if !allowedCommand[field] {
			return fmt.Errorf("unsupported command field %q", field)
		}
	}
	return nil
}

func parseAndValidateWorkModeAgenticResponse(raw string) (workModeAIResponse, []string, error) {
	if err := requireWorkModeAgenticJSONShape(raw); err != nil {
		return workModeAIResponse{}, nil, err
	}
	parsed, err := parseWorkModeAIResponse(raw)
	if err != nil {
		return workModeAIResponse{}, nil, err
	}
	validationErrors := validateWorkModeAgenticResponse(parsed)
	return parsed, validationErrors, nil
}
