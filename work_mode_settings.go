package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	workModeSettingsFilename        = "workmodesettings.json"
	workModeContinuedMemoryFilename = "continuedmemory.md"
	workModeSettingsSchemaVersion   = 1
	workModeMemoryDefaultOff        = "off"
	workModeMemoryDefaultNew        = "new"
	workModeMemoryDefaultContinue   = "continue"
	workModeMemoryDefaultNamed      = "named"
)

type workModeSettingsFile struct {
	SchemaVersion        int    `json:"schema_version"`
	MemoryDefault        string `json:"memory_default"`
	DefaultMemoryFile    string `json:"default_memory_file,omitempty"`
	DefaultMemoryModelID string `json:"default_memory_model_id,omitempty"`
}

type workModeSettingsResponse struct {
	Settings         workModeSettingsFile `json:"settings"`
	Saved            []workModeMemoryFile `json:"saved"`
	ContinuedExists  bool                 `json:"continuedExists"`
	ContinuedBytes   int64                `json:"continuedBytes,omitempty"`
	ContinuedContent string               `json:"continuedContent,omitempty"`
	Message          string               `json:"message,omitempty"`
}

type workModeSettingsSaveRequest struct {
	ModelID           string `json:"modelId,omitempty"`
	MemoryDefault     string `json:"memoryDefault"`
	DefaultMemoryFile string `json:"defaultMemoryFile,omitempty"`
}

func defaultWorkModeSettingsFile() workModeSettingsFile {
	return workModeSettingsFile{SchemaVersion: workModeSettingsSchemaVersion, MemoryDefault: workModeMemoryDefaultOff}
}

func normalizeWorkModeMemoryDefault(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case workModeMemoryDefaultNew:
		return workModeMemoryDefaultNew
	case workModeMemoryDefaultContinue:
		return workModeMemoryDefaultContinue
	case workModeMemoryDefaultNamed:
		return workModeMemoryDefaultNamed
	default:
		return workModeMemoryDefaultOff
	}
}

func (a *App) workModeSettingsPath(projectName string) (string, error) {
	root, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", err
	}
	return safeJoin(root, workModeSettingsFilename)
}

func (a *App) workModeContinuedMemoryPath(projectName string) (string, error) {
	root, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", err
	}
	return safeJoin(root, workModeContinuedMemoryFilename)
}

func (a *App) ensureProjectWorkModeSettings(projectName string) error {
	settingsPath, err := a.workModeSettingsPath(projectName)
	if err != nil {
		return err
	}
	if err := ensurePrivateConfigFile(settingsPath, prettyTerminalJSON(defaultWorkModeSettingsFile())); err != nil {
		return err
	}
	continuedPath, err := a.workModeContinuedMemoryPath(projectName)
	if err != nil {
		return err
	}
	if err := ensurePrivateConfigFile(continuedPath, []byte("")); err != nil {
		return err
	}
	return nil
}

func (a *App) loadWorkModeSettings(projectName string) (workModeSettingsFile, error) {
	if err := a.ensureProjectWorkModeSettings(projectName); err != nil {
		return workModeSettingsFile{}, err
	}
	path, _ := a.workModeSettingsPath(projectName)
	data, err := os.ReadFile(path)
	if err != nil {
		return workModeSettingsFile{}, err
	}
	settings := defaultWorkModeSettingsFile()
	if err := json.Unmarshal(data, &settings); err != nil {
		return workModeSettingsFile{}, err
	}
	settings.SchemaVersion = workModeSettingsSchemaVersion
	settings.MemoryDefault = normalizeWorkModeMemoryDefault(settings.MemoryDefault)
	settings.DefaultMemoryFile = strings.TrimSpace(settings.DefaultMemoryFile)
	settings.DefaultMemoryModelID = strings.TrimSpace(settings.DefaultMemoryModelID)
	if settings.MemoryDefault != workModeMemoryDefaultNamed {
		settings.DefaultMemoryFile = ""
		settings.DefaultMemoryModelID = ""
	}
	return settings, nil
}

func (a *App) saveWorkModeSettings(projectName string, settings workModeSettingsFile) error {
	if err := a.ensureProjectWorkModeSettings(projectName); err != nil {
		return err
	}
	settings.SchemaVersion = workModeSettingsSchemaVersion
	settings.MemoryDefault = normalizeWorkModeMemoryDefault(settings.MemoryDefault)
	settings.DefaultMemoryFile = strings.TrimSpace(settings.DefaultMemoryFile)
	settings.DefaultMemoryModelID = strings.TrimSpace(settings.DefaultMemoryModelID)
	if settings.MemoryDefault != workModeMemoryDefaultNamed {
		settings.DefaultMemoryFile = ""
		settings.DefaultMemoryModelID = ""
	}
	path, _ := a.workModeSettingsPath(projectName)
	if err := atomicWriteFile(path, prettyTerminalJSON(settings), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func (a *App) validateNamedWorkModeDefault(projectName, modelID, rawName string) (string, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", errors.New("activate exactly one Builder AI before selecting a named default memory")
	}
	_, metaRoot, err := a.workModeMemoryMetaRoot(modelID, projectName)
	if err != nil {
		return "", err
	}
	displayName, fileName, err := normalizeWorkModeMemoryFileName(rawName)
	if err != nil {
		return "", err
	}
	memoriesRoot, err := workModeMemoriesRoot(metaRoot)
	if err != nil {
		return "", err
	}
	full, err := safeJoin(memoriesRoot, fileName)
	if err != nil {
		return "", errors.New("invalid memory file")
	}
	if _, err := os.Stat(full); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("saved memory file was not found")
		}
		return "", err
	}
	return displayName, nil
}

func (a *App) workModeSettingsResponse(projectName, modelID, message string) (workModeSettingsResponse, error) {
	settings, err := a.loadWorkModeSettings(projectName)
	if err != nil {
		return workModeSettingsResponse{}, err
	}
	modelID = strings.TrimSpace(modelID)
	if settings.MemoryDefault == workModeMemoryDefaultNamed {
		if settings.DefaultMemoryModelID != modelID {
			// Named memories remain Builder-specific. Preserve the configured default
			// so it is available when that Builder is active again, but apply Off to
			// this Builder rather than silently loading another AI's memory file.
			settings = defaultWorkModeSettingsFile()
			message = "The configured named default belongs to a different Builder. Memory will start Off for this Builder."
		} else if _, err := a.validateNamedWorkModeDefault(projectName, modelID, settings.DefaultMemoryFile); err != nil {
			settings = defaultWorkModeSettingsFile()
			message = "The configured default memory file was not found. AgentGO Memory Default was reset to Off."
			if err := a.saveWorkModeSettings(projectName, settings); err != nil {
				return workModeSettingsResponse{}, err
			}
		}
	}

	saved := []workModeMemoryFile{}
	if modelID != "" {
		if _, metaRoot, resolveErr := a.workModeMemoryMetaRoot(modelID, projectName); resolveErr == nil {
			saved, err = listWorkModeMemoryFiles(metaRoot)
			if err != nil {
				return workModeSettingsResponse{}, err
			}
		}
	}
	continuedPath, err := a.workModeContinuedMemoryPath(projectName)
	if err != nil {
		return workModeSettingsResponse{}, err
	}
	continuedData, readErr := os.ReadFile(continuedPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return workModeSettingsResponse{}, readErr
	}
	return workModeSettingsResponse{
		Settings:         settings,
		Saved:            saved,
		ContinuedExists:  strings.TrimSpace(string(continuedData)) != "",
		ContinuedBytes:   int64(len(continuedData)),
		ContinuedContent: string(continuedData),
		Message:          message,
	}, nil
}

func (a *App) handleWorkModeSettings(w http.ResponseWriter, r *http.Request) {
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		modelID := strings.TrimSpace(r.URL.Query().Get("modelId"))
		resp, err := a.workModeSettingsResponse(projectName, modelID, "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	case http.MethodPost:
		if !a.requireActiveSessionMatch(w, r) {
			return
		}
		var req workModeSettingsSaveRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		settings := defaultWorkModeSettingsFile()
		settings.MemoryDefault = normalizeWorkModeMemoryDefault(req.MemoryDefault)
		if settings.MemoryDefault == workModeMemoryDefaultNamed {
			displayName, err := a.validateNamedWorkModeDefault(projectName, req.ModelID, req.DefaultMemoryFile)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			settings.DefaultMemoryFile = displayName
			settings.DefaultMemoryModelID = strings.TrimSpace(req.ModelID)
		}
		if err := a.saveWorkModeSettings(projectName, settings); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp, err := a.workModeSettingsResponse(projectName, req.ModelID, "AgentGO Memory Default saved.")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, resp)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) clearContinuedWorkModeMemory(projectName string) error {
	if err := a.ensureProjectWorkModeSettings(projectName); err != nil {
		return err
	}
	path, err := a.workModeContinuedMemoryPath(projectName)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, []byte(""), 0o600)
}

func (a *App) readContinuedWorkModeMemory(projectName string) ([]byte, string, error) {
	if err := a.ensureProjectWorkModeSettings(projectName); err != nil {
		return nil, "", err
	}
	path, err := a.workModeContinuedMemoryPath(projectName)
	if err != nil {
		return nil, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, "", err
	}
	return data, path, nil
}

func writeWorkModeMemoryFile(path, content string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	perm := os.FileMode(0o644)
	if filepath.Base(path) == workModeContinuedMemoryFilename {
		perm = 0o600
	}
	return atomicWriteFile(path, []byte(content), perm)
}
