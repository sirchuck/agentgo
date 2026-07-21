package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const (
	terminalWhitelistFilename          = "whitelistcmds.json"
	terminalWhitelistBackupFilename    = "whitelistcmds.json.backup"
	terminalWhitelistLastValidFilename = "whitelistcmds.valid.json"
	terminalEnvironmentFilename        = "terminalenv.json"
	terminalConfigSchemaVersion        = 1
)

type terminalWhitelistFile struct {
	SchemaVersion int                     `json:"schema_version"`
	Rules         []terminalWhitelistRule `json:"rules"`
}

type terminalWhitelistRule struct {
	Type        string   `json:"type"`
	Executable  string   `json:"executable,omitempty"`
	Args        []string `json:"args,omitempty"`
	Script      string   `json:"script,omitempty"`
	Description string   `json:"description,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
}

type terminalEnvironmentFile struct {
	SchemaVersion int                   `json:"schema_version"`
	Variables     []terminalEnvironment `json:"variables"`
}

type terminalEnvironment struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type terminalConfigValidation struct {
	Valid    bool     `json:"valid"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type terminalWhitelistSaveRequest struct {
	Content string `json:"content"`
	Force   bool   `json:"force"`
}

type terminalEnvironmentUpsertRequest struct {
	OriginalName  string `json:"originalName,omitempty"`
	Name          string `json:"name"`
	Value         string `json:"value,omitempty"`
	Description   string `json:"description,omitempty"`
	PreserveValue bool   `json:"preserveValue,omitempty"`
}

type terminalEnvironmentNameRequest struct {
	Name string `json:"name"`
}

var terminalEnvironmentNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func defaultTerminalWhitelistFile() terminalWhitelistFile {
	return terminalWhitelistFile{SchemaVersion: terminalConfigSchemaVersion, Rules: []terminalWhitelistRule{}}
}

func defaultTerminalEnvironmentFile() terminalEnvironmentFile {
	return terminalEnvironmentFile{SchemaVersion: terminalConfigSchemaVersion, Variables: []terminalEnvironment{}}
}

func prettyTerminalJSON(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return append(data, '\n')
}

func (a *App) terminalProjectConfigPath(projectName, filename string) (string, error) {
	root, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", err
	}
	return safeJoin(root, filename)
}

func ensurePrivateConfigFile(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		return os.Chmod(path, 0o600)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (a *App) ensureProjectTerminalConfig(projectName string) error {
	whitelistPath, err := a.terminalProjectConfigPath(projectName, terminalWhitelistFilename)
	if err != nil {
		return err
	}
	if err := ensurePrivateConfigFile(whitelistPath, prettyTerminalJSON(defaultTerminalWhitelistFile())); err != nil {
		return err
	}
	validPath, err := a.terminalProjectConfigPath(projectName, terminalWhitelistLastValidFilename)
	if err != nil {
		return err
	}
	if err := ensurePrivateConfigFile(validPath, prettyTerminalJSON(defaultTerminalWhitelistFile())); err != nil {
		return err
	}
	envPath, err := a.terminalProjectConfigPath(projectName, terminalEnvironmentFilename)
	if err != nil {
		return err
	}
	return ensurePrivateConfigFile(envPath, prettyTerminalJSON(defaultTerminalEnvironmentFile()))
}

func validateTerminalWhitelistJSON(content string) (terminalWhitelistFile, terminalConfigValidation) {
	validation := terminalConfigValidation{Valid: true}
	var file terminalWhitelistFile
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &file); err != nil {
		validation.Valid = false
		validation.Errors = append(validation.Errors, "Invalid JSON: "+err.Error())
		return file, validation
	}
	if file.SchemaVersion != terminalConfigSchemaVersion {
		validation.Errors = append(validation.Errors, fmt.Sprintf("schema_version must be %d", terminalConfigSchemaVersion))
	}
	seen := map[string]int{}
	for i, rule := range file.Rules {
		label := fmt.Sprintf("Rule %d", i+1)
		typeName := strings.ToLower(strings.TrimSpace(rule.Type))
		switch typeName {
		case "direct":
			if strings.TrimSpace(rule.Executable) == "" {
				validation.Errors = append(validation.Errors, label+": executable is required for direct rules")
			}
			if strings.TrimSpace(rule.Script) != "" {
				validation.Warnings = append(validation.Warnings, label+": script is ignored for a direct rule")
			}
		case "shell":
			if strings.TrimSpace(rule.Script) == "" {
				validation.Errors = append(validation.Errors, label+": script is required for shell rules")
			}
			validation.Warnings = append(validation.Warnings, label+": shell rules can authorize pipes, redirects, substitutions, and chained commands")
		default:
			validation.Errors = append(validation.Errors, label+`: type must be "direct" or "shell"`)
		}
		keyBytes, _ := json.Marshal(rule)
		key := string(keyBytes)
		if previous, ok := seen[key]; ok {
			validation.Warnings = append(validation.Warnings, fmt.Sprintf("%s duplicates rule %d", label, previous))
		} else {
			seen[key] = i + 1
		}
		if strings.ContainsAny(strings.TrimSpace(rule.Executable), "*?") || strings.ContainsAny(rule.Script, "*?") {
			validation.Warnings = append(validation.Warnings, label+": wildcard authorization appears unusually broad")
		}
		for _, token := range append(append([]string{}, rule.Args...), rule.Script) {
			clean := strings.TrimSpace(token)
			if clean == "" {
				continue
			}
			if filepath.IsAbs(clean) || strings.HasPrefix(filepath.ToSlash(clean), "../") || strings.Contains(filepath.ToSlash(clean), "/../") {
				validation.Warnings = append(validation.Warnings, label+": contains a path that may target outside the project workspace")
				break
			}
		}
	}
	if len(validation.Errors) > 0 {
		validation.Valid = false
	}
	return file, validation
}

func (a *App) loadTerminalWhitelistContent(projectName string) (string, terminalConfigValidation, bool, error) {
	if err := a.ensureProjectTerminalConfig(projectName); err != nil {
		return "", terminalConfigValidation{}, false, err
	}
	path, _ := a.terminalProjectConfigPath(projectName, terminalWhitelistFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", terminalConfigValidation{}, false, err
	}
	_, validation := validateTerminalWhitelistJSON(string(data))
	if validation.Valid {
		return string(data), validation, false, nil
	}
	fallbackPath, _ := a.terminalProjectConfigPath(projectName, terminalWhitelistLastValidFilename)
	fallback, fallbackErr := os.ReadFile(fallbackPath)
	if fallbackErr == nil {
		_, fallbackValidation := validateTerminalWhitelistJSON(string(fallback))
		if fallbackValidation.Valid {
			return string(data), validation, true, nil
		}
	}
	return string(data), validation, false, nil
}

func backupConfigFile(path, backupPath string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := atomicWriteFile(backupPath, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(backupPath, 0o600)
}

func (a *App) saveTerminalWhitelist(projectName, content string, force bool) (terminalConfigValidation, bool, error) {
	if err := a.ensureProjectTerminalConfig(projectName); err != nil {
		return terminalConfigValidation{}, false, err
	}
	_, validation := validateTerminalWhitelistJSON(content)
	if len(validation.Errors) > 0 && !force {
		return validation, false, nil
	}
	if len(validation.Warnings) > 0 && !force {
		return validation, false, nil
	}
	path, _ := a.terminalProjectConfigPath(projectName, terminalWhitelistFilename)
	backupPath, _ := a.terminalProjectConfigPath(projectName, terminalWhitelistBackupFilename)
	if err := backupConfigFile(path, backupPath); err != nil {
		return validation, false, err
	}
	if err := atomicWriteFile(path, []byte(content), 0o600); err != nil {
		return validation, false, err
	}
	_ = os.Chmod(path, 0o600)
	active := validation.Valid
	if active {
		validPath, _ := a.terminalProjectConfigPath(projectName, terminalWhitelistLastValidFilename)
		if err := atomicWriteFile(validPath, []byte(content), 0o600); err != nil {
			return validation, false, err
		}
		_ = os.Chmod(validPath, 0o600)
	}
	return validation, active, nil
}

func (a *App) loadTerminalEnvironment(projectName string) (terminalEnvironmentFile, error) {
	if err := a.ensureProjectTerminalConfig(projectName); err != nil {
		return terminalEnvironmentFile{}, err
	}
	path, _ := a.terminalProjectConfigPath(projectName, terminalEnvironmentFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		return terminalEnvironmentFile{}, err
	}
	var file terminalEnvironmentFile
	if err := json.Unmarshal(data, &file); err != nil {
		return terminalEnvironmentFile{}, err
	}
	if file.SchemaVersion != terminalConfigSchemaVersion {
		return terminalEnvironmentFile{}, fmt.Errorf("terminal environment schema_version must be %d", terminalConfigSchemaVersion)
	}
	if file.Variables == nil {
		file.Variables = []terminalEnvironment{}
	}
	return file, nil
}

func validateTerminalEnvironmentVariable(variable terminalEnvironment) error {
	variable.Name = strings.TrimSpace(variable.Name)
	if !terminalEnvironmentNameRE.MatchString(variable.Name) {
		return errors.New("variable name must begin with a letter or underscore and contain only letters, numbers, and underscores")
	}
	if strings.ContainsRune(variable.Value, '\x00') {
		return errors.New("variable value cannot contain a null byte")
	}
	return nil
}

func (a *App) saveTerminalEnvironment(projectName string, file terminalEnvironmentFile) error {
	file.SchemaVersion = terminalConfigSchemaVersion
	sort.Slice(file.Variables, func(i, j int) bool {
		return strings.ToUpper(file.Variables[i].Name) < strings.ToUpper(file.Variables[j].Name)
	})
	seen := map[string]bool{}
	for i := range file.Variables {
		file.Variables[i].Name = strings.TrimSpace(file.Variables[i].Name)
		file.Variables[i].Description = strings.TrimSpace(file.Variables[i].Description)
		if err := validateTerminalEnvironmentVariable(file.Variables[i]); err != nil {
			return fmt.Errorf("%s: %w", file.Variables[i].Name, err)
		}
		key := strings.ToUpper(file.Variables[i].Name)
		if seen[key] {
			return fmt.Errorf("duplicate environment variable %q", file.Variables[i].Name)
		}
		seen[key] = true
	}
	path, err := a.terminalProjectConfigPath(projectName, terminalEnvironmentFilename)
	if err != nil {
		return err
	}
	if err := atomicWriteFile(path, prettyTerminalJSON(file), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func maskedTerminalEnvironmentValue(value string) string {
	if value == "" {
		return "(empty)"
	}
	return "••••••••"
}

func terminalEnvironmentPublicList(file terminalEnvironmentFile) []map[string]any {
	out := make([]map[string]any, 0, len(file.Variables))
	for _, variable := range file.Variables {
		out = append(out, map[string]any{
			"name": variable.Name, "description": variable.Description,
			"maskedValue": maskedTerminalEnvironmentValue(variable.Value), "hasValue": variable.Value != "",
		})
	}
	return out
}

func sensitiveEnvironmentName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	for _, marker := range []string{"API_KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "PRIVATE_KEY", "CREDENTIAL", "AUTH"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return strings.HasPrefix(upper, "AWS_") || strings.HasPrefix(upper, "AZURE_") || strings.HasPrefix(upper, "GOOGLE_")
}

func terminalEnvironmentNameAllowed(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	allowed := map[string]bool{
		"PATH": true, "PATHEXT": true, "SYSTEMROOT": true, "WINDIR": true, "COMSPEC": true,
		"HOME": true, "USERPROFILE": true, "HOMEDRIVE": true, "HOMEPATH": true,
		"TMP": true, "TEMP": true, "TMPDIR": true,
		"LANG": true, "LANGUAGE": true, "LC_ALL": true, "TERM": true, "COLORTERM": true,
		"SHELL": true, "USER": true, "USERNAME": true, "LOGNAME": true, "OS": true,
		"NUMBER_OF_PROCESSORS": true, "PROCESSOR_ARCHITECTURE": true, "PROCESSOR_IDENTIFIER": true,
		"GOROOT": true, "GOPATH": true, "GOENV": true, "GOFLAGS": true, "GO111MODULE": true,
		"GOTOOLCHAIN": true, "CGO_ENABLED": true, "CC": true, "CXX": true, "PKG_CONFIG_PATH": true,
		"CARGO_HOME": true, "RUSTUP_HOME": true, "RUSTUP_TOOLCHAIN": true,
		"JAVA_HOME": true, "JDK_HOME": true, "MAVEN_HOME": true, "M2_HOME": true,
		"GRADLE_HOME": true, "GRADLE_USER_HOME": true,
		"NODE_PATH": true, "NPM_CONFIG_PREFIX": true,
		"PYTHONHOME": true, "PYTHONPATH": true, "VIRTUAL_ENV": true,
		"DOTNET_ROOT": true, "MSBUILD_EXE_PATH": true,
	}
	return allowed[upper] || strings.HasPrefix(upper, "LC_")
}

func sanitizedTerminalEnvironment(base []string, configured []terminalEnvironment) []string {
	values := map[string]string{}
	actualNames := map[string]string{}
	for _, item := range base {
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		upper := strings.ToUpper(name)
		if !terminalEnvironmentNameAllowed(name) || sensitiveEnvironmentName(name) {
			continue
		}
		values[upper] = value
		actualNames[upper] = name
	}
	for _, variable := range configured {
		upper := strings.ToUpper(strings.TrimSpace(variable.Name))
		if upper == "" {
			continue
		}
		values[upper] = variable.Value
		actualNames[upper] = variable.Name
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, actualNames[key]+"="+values[key])
	}
	return out
}

func redactTerminalEnvironmentValues(text string, configured []terminalEnvironment) string {
	redacted := text
	values := make([]string, 0, len(configured))
	for _, variable := range configured {
		if variable.Value != "" {
			values = append(values, variable.Value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		redacted = strings.ReplaceAll(redacted, value, "[REDACTED]")
	}
	return redacted
}

func (a *App) requireTerminalConfigProject() (string, error) {
	project, err := a.requireActiveProject()
	if err != nil {
		return "", errors.New("Select an active project first.")
	}
	if err := a.ensureProjectTerminalConfig(project); err != nil {
		return "", err
	}
	return project, nil
}

func (a *App) handleWorkModeTerminalWhitelist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project, err := a.requireTerminalConfigProject()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	content, validation, usingFallback, err := a.loadTerminalWhitelistContent(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "content": content, "validation": validation, "usingLastValid": usingFallback})
}

func (a *App) handleWorkModeTerminalWhitelistValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req terminalWhitelistSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	_, validation := validateTerminalWhitelistJSON(req.Content)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "validation": validation})
}

func (a *App) handleWorkModeTerminalWhitelistSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project, err := a.requireTerminalConfigProject()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req terminalWhitelistSaveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	validation, active, err := a.saveTerminalWhitelist(project, req.Content, req.Force)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	requiresForce := !req.Force && (len(validation.Errors) > 0 || len(validation.Warnings) > 0)
	message := "Whitelist saved and active."
	if requiresForce {
		message = "Review validation results before saving."
	} else if !active {
		message = "Saved, but not active. AgentGO is continuing to use the last valid whitelist."
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": !requiresForce, "active": active, "requiresForce": requiresForce, "validation": validation, "message": message})
}

func (a *App) handleWorkModeTerminalEnvironment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project, err := a.requireTerminalConfigProject()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, err := a.loadTerminalEnvironment(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "variables": terminalEnvironmentPublicList(file)})
}

func (a *App) handleWorkModeTerminalEnvironmentUpsert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project, err := a.requireTerminalConfigProject()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req terminalEnvironmentUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(req.Name)
	candidate := terminalEnvironment{Name: name, Value: req.Value, Description: req.Description}
	if err := validateTerminalEnvironmentVariable(candidate); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	file, err := a.loadTerminalEnvironment(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	original := strings.TrimSpace(req.OriginalName)
	found := -1
	for i, variable := range file.Variables {
		if strings.EqualFold(variable.Name, original) || (original == "" && strings.EqualFold(variable.Name, name)) {
			found = i
			break
		}
	}
	for i, variable := range file.Variables {
		if i != found && strings.EqualFold(variable.Name, name) {
			http.Error(w, "an environment variable with that name already exists", http.StatusConflict)
			return
		}
	}
	if found >= 0 {
		if req.PreserveValue {
			candidate.Value = file.Variables[found].Value
		}
		file.Variables[found] = candidate
	} else {
		file.Variables = append(file.Variables, candidate)
	}
	if err := a.saveTerminalEnvironment(project, file); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleWorkModeTerminalEnvironmentDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project, err := a.requireTerminalConfigProject()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req terminalEnvironmentNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	file, err := a.loadTerminalEnvironment(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	filtered := file.Variables[:0]
	removed := false
	for _, variable := range file.Variables {
		if strings.EqualFold(variable.Name, strings.TrimSpace(req.Name)) {
			removed = true
			continue
		}
		filtered = append(filtered, variable)
	}
	if !removed {
		http.Error(w, "environment variable not found", http.StatusNotFound)
		return
	}
	file.Variables = filtered
	if err := a.saveTerminalEnvironment(project, file); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *App) handleWorkModeTerminalEnvironmentReveal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project, err := a.requireTerminalConfigProject()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var req terminalEnvironmentNameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	file, err := a.loadTerminalEnvironment(project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, variable := range file.Variables {
		if strings.EqualFold(variable.Name, strings.TrimSpace(req.Name)) {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "name": variable.Name, "value": variable.Value})
			return
		}
	}
	http.Error(w, "environment variable not found", http.StatusNotFound)
}

// terminalConfigPlatformLabel is used by tests and future execution wiring.
func terminalConfigPlatformLabel() string { return runtime.GOOS + "/" + runtime.GOARCH }
