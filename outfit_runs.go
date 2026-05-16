package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const outfitRunSchemaVersion = 1
const outfitAuthHeader = "X-AgentGO-Outfit-Key"
const canonicalBuilderJSONFilename = "agentgo_builder.json"

type activeOutfitRun struct {
	OutfitID   string
	Project    string
	RunID      string
	RunPath    string
	RecordPath string
}

type outfitRunRecord struct {
	AgentGOFile string                   `json:"agentgo_file,omitempty"`
	FileVersion int                      `json:"file_version,omitempty"`
	Run         outfitRunRecordRun       `json:"run"`
	Outfit      outfitRunRecordOutfit    `json:"outfit"`
	Trigger     outfitRunRecordTrigger   `json:"trigger"`
	Execution   outfitRunRecordExecution `json:"execution"`
	Failure     outfitRunRecordFailure   `json:"failure,omitempty"`
	Delivery    outfitRunRecordDelivery  `json:"delivery"`
	Paths       outfitRunRecordPaths     `json:"paths"`
	Version     outfitRunRecordVersion   `json:"version"`
}

type outfitRunRecordRun struct {
	RunID      string `json:"run_id"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}

type outfitRunRecordOutfit struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Slug                 string            `json:"slug"`
	Project              string            `json:"project"`
	UseCypher            bool              `json:"use_cypher,omitempty"`
	DeliveryMode         string            `json:"delivery_mode"`
	PayloadType          string            `json:"payload_type,omitempty"`
	CallbackURL          string            `json:"callback_url,omitempty"`
	CallbackHeaders      map[string]string `json:"callback_headers,omitempty"`
	HasBearerToken       bool              `json:"has_bearer_token,omitempty"`
	FileSetSelectorType  string            `json:"file_set_selector_type,omitempty"`
	FileSetSelectorValue string            `json:"file_set_selector_value,omitempty"`
}

type outfitRunRecordTrigger struct {
	TriggerType            string `json:"trigger_type"`
	Source                 string `json:"source,omitempty"`
	Input                  any    `json:"input,omitempty"`
	RuntimePromptField     string `json:"runtime_prompt_field,omitempty"`
	RuntimeObjective       string `json:"runtime_objective,omitempty"`
	OriginalObjective      string `json:"original_objective,omitempty"`
	Diagnostics            string `json:"diagnostics,omitempty"`
	DiagnosticsFingerprint string `json:"diagnostics_fingerprint,omitempty"`
	AdditionalContext      string `json:"additional_context,omitempty"`
	PreviousRunID          string `json:"previous_run_id,omitempty"`
	CycleNumber            int    `json:"cycle_number,omitempty"`
	MaxCycles              int    `json:"max_cycles,omitempty"`
	HasRuntimePrompt       bool   `json:"has_runtime_prompt,omitempty"`
	HasDiagnostics         bool   `json:"has_diagnostics,omitempty"`
}

type outfitRunRecordExecution struct {
	WorkflowType          string   `json:"workflow_type,omitempty"`
	ModelsUsed            []string `json:"models_used,omitempty"`
	WaveCount             int      `json:"wave_count,omitempty"`
	SelectedWinnerModelID string   `json:"selected_winner_model_id,omitempty"`
	SelectedWinnerLabel   string   `json:"selected_winner_label,omitempty"`
	OutcomeSummary        string   `json:"outcome_summary,omitempty"`
	AddedFiles            []string `json:"added_files,omitempty"`
	ModifiedFiles         []string `json:"modified_files,omitempty"`
	DeletedFiles          []string `json:"deleted_files,omitempty"`
	ChangedFiles          []string `json:"changed_files,omitempty"`
}

type outfitRunRecordFailure struct {
	Stage          string `json:"stage,omitempty"`
	Reason         string `json:"reason,omitempty"`
	SafeToRetry    bool   `json:"safe_to_retry,omitempty"`
	BuilderError   string `json:"builder_error,omitempty"`
	CypherError    string `json:"cypher_error,omitempty"`
	CallbackError  string `json:"callback_error,omitempty"`
	AutoMergeError string `json:"auto_merge_error,omitempty"`
}

type outfitRunRecordDelivery struct {
	Mode        string `json:"mode"`
	PayloadType string `json:"payload_type,omitempty"`
	Status      string `json:"status,omitempty"`
	Error       string `json:"error,omitempty"`
	DeliveredAt string `json:"delivered_at,omitempty"`
	Target      string `json:"target,omitempty"`
}

type outfitRunRecordPaths struct {
	RunPath         string `json:"run_path"`
	MetaPath        string `json:"meta_path"`
	ProjectworkPath string `json:"projectwork_path"`
	DeadDropPath    string `json:"dead_drop_path,omitempty"`
}

type outfitRunRecordVersion struct {
	AppRevision      string `json:"app_revision"`
	RunSchemaVersion int    `json:"run_schema_version"`
}

type outfitRunIndexEntry struct {
	RunID      string `json:"run_id"`
	OutfitID   string `json:"outfit_id"`
	Project    string `json:"project"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Path       string `json:"path"`
}

type outfitPublicRunAcceptedResponse struct {
	Status    string `json:"status"`
	RunID     string `json:"run_id"`
	OutfitID  string `json:"outfit_id"`
	Project   string `json:"project"`
	CreatedAt string `json:"created_at"`
	RunPath   string `json:"run_path"`
	MetaPath  string `json:"meta_path"`
}

type outfitPublicErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

type outfitRunsListResponse struct {
	OutfitID   string           `json:"outfit_id"`
	OutfitSlug string           `json:"outfit_slug"`
	Project    string           `json:"project,omitempty"`
	Count      int              `json:"count"`
	Runs       []map[string]any `json:"runs"`
}

func (a *App) buildOutfitRunsListResponse(outfit OutfitRecord, statusFilter string, limit int) (outfitRunsListResponse, error) {
	records, err := a.listArchivedOutfitRuns(outfit.ID)
	if err != nil {
		return outfitRunsListResponse{}, err
	}
	runs := []map[string]any{}
	for _, record := range records {
		if statusFilter != "" && record.Run.Status != statusFilter {
			continue
		}
		runs = append(runs, map[string]any{
			"run_id":                  record.Run.RunID,
			"outfit_id":               record.Outfit.ID,
			"outfit_slug":             record.Outfit.Slug,
			"project":                 record.Outfit.Project,
			"status":                  record.Run.Status,
			"created_at":              record.Run.CreatedAt,
			"started_at":              record.Run.StartedAt,
			"finished_at":             record.Run.FinishedAt,
			"workflow_type":           normalizeOutfitExecutionMode(record.Execution.WorkflowType),
			"use_cypher":              record.Outfit.UseCypher,
			"added_files":             record.Execution.AddedFiles,
			"modified_files":          record.Execution.ModifiedFiles,
			"deleted_files":           record.Execution.DeletedFiles,
			"changed_files":           record.Execution.ChangedFiles,
			"changed_file_count":      len(record.Execution.ChangedFiles),
			"deleted_file_count":      len(record.Execution.DeletedFiles),
			"runtime_objective":       record.Trigger.RuntimeObjective,
			"diagnostics_fingerprint": record.Trigger.DiagnosticsFingerprint,
			"previous_run_id":         record.Trigger.PreviousRunID,
			"cycle_number":            record.Trigger.CycleNumber,
			"max_cycles":              record.Trigger.MaxCycles,
			"failure":                 record.Failure,
			"pull_urls":               outfitRunPullURLs(record),
			"delivery": map[string]any{
				"mode":             record.Delivery.Mode,
				"payload_type":     record.Delivery.PayloadType,
				"status":           record.Delivery.Status,
				"error":            record.Delivery.Error,
				"delivered_at":     record.Delivery.DeliveredAt,
				"target":           record.Delivery.Target,
				"run_path":         record.Paths.RunPath,
				"meta_path":        record.Paths.MetaPath,
				"projectwork_path": record.Paths.ProjectworkPath,
				"dead_drop_path":   record.Paths.DeadDropPath,
			},
		})
	}
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return outfitRunsListResponse{OutfitID: outfit.ID, OutfitSlug: outfitSlug(outfit.Name), Project: strings.TrimSpace(outfit.Project), Count: len(runs), Runs: runs}, nil
}

func writeOutfitAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, outfitPublicErrorResponse{Error: strings.TrimSpace(code), Message: strings.TrimSpace(message)})
}

func applyOutfitDeliveryInstructions(outfit OutfitRecord, req executeRequest) executeRequest {
	outfit = normalizeOutfitRecord(outfit)
	if outfit.DeliveryMode != "callback" || outfit.PayloadType != "json" {
		return req
	}
	note := "\n\nDELIVERY CONTRACT: This Outfit expects a final machine-usable JSON payload. Write the final completed JSON result to projectwork/agentgo_builder.json and keep it valid JSON."
	if len(req.WavePrompts) == 0 {
		req.WavePrompts = map[string]string{"0": strings.TrimSpace(note)}
		return req
	}
	for key, value := range req.WavePrompts {
		text := strings.TrimSpace(value)
		if strings.Contains(text, "projectwork/agentgo_builder.json") {
			continue
		}
		if text == "" {
			req.WavePrompts[key] = strings.TrimSpace(note)
		} else {
			req.WavePrompts[key] = text + note
		}
	}
	return req
}

func parseOutfitRouteID(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return ""
	}
	if idx := strings.Index(clean, "_"); idx > 0 {
		clean = clean[:idx]
	}
	if validateOutfitID(clean) != nil {
		return ""
	}
	return clean
}

func activeOutfitBuilderIDs(outfit OutfitRecord) []string {
	ids := []string{}
	for _, model := range outfit.Models {
		if model.Active {
			ids = append(ids, strings.TrimSpace(model.Label))
		}
	}
	return ids
}

func activeOutfitWaveCount(outfit OutfitRecord) int {
	waves := map[int]bool{}
	for _, model := range outfit.Models {
		if model.Active {
			waves[normalizeRunOrder(model.Wave)] = true
		}
	}
	return len(waves)
}

func (a *App) projectOutfitsRoot(project string) (string, error) {
	return safeJoin(a.cfg.WorkRoot, "projects", strings.TrimSpace(project), "outfits")
}

func (a *App) outfitRunGroupRoot(project string, outfit OutfitRecord) (string, error) {
	base, err := a.projectOutfitsRoot(project)
	if err != nil {
		return "", err
	}
	return safeJoin(base, outfit.ID+"_"+outfitSlug(outfit.Name))
}

func (a *App) projectOutfitRunsIndexPath(project string) (string, error) {
	return safeJoin(a.cfg.WorkRoot, "projects", strings.TrimSpace(project), "meta", "outfit_runs.json")
}

func (a *App) readProjectOutfitRunsIndex(project string) ([]outfitRunIndexEntry, error) {
	path, err := a.projectOutfitRunsIndexPath(project)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []outfitRunIndexEntry{}, nil
		}
		return nil, err
	}
	var entries []outfitRunIndexEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return []outfitRunIndexEntry{}, nil
	}
	return entries, nil
}

func (a *App) writeProjectOutfitRunsIndex(project string, entries []outfitRunIndexEntry) error {
	path, err := a.projectOutfitRunsIndexPath(project)
	if err != nil {
		return err
	}
	return writeJSONFileAtomic(path, entries)
}

func (a *App) upsertProjectOutfitRunIndex(project string, entry outfitRunIndexEntry) error {
	entries, err := a.readProjectOutfitRunsIndex(project)
	if err != nil {
		return err
	}
	replaced := false
	for i := range entries {
		if entries[i].RunID == entry.RunID && entries[i].OutfitID == entry.OutfitID {
			entries[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].CreatedAt > entries[j].CreatedAt })
	return a.writeProjectOutfitRunsIndex(project, entries)
}

func (a *App) removeProjectOutfitRunIndex(project, outfitID, runID string) error {
	entries, err := a.readProjectOutfitRunsIndex(project)
	if err != nil {
		return err
	}
	filtered := make([]outfitRunIndexEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.RunID == runID && entry.OutfitID == outfitID {
			continue
		}
		filtered = append(filtered, entry)
	}
	return a.writeProjectOutfitRunsIndex(project, filtered)
}

func (a *App) runIndexEntryFromRecord(record outfitRunRecord) outfitRunIndexEntry {
	return outfitRunIndexEntry{
		RunID:      record.Run.RunID,
		OutfitID:   record.Outfit.ID,
		Project:    record.Outfit.Project,
		Status:     record.Run.Status,
		CreatedAt:  record.Run.CreatedAt,
		StartedAt:  record.Run.StartedAt,
		FinishedAt: record.Run.FinishedAt,
		Path:       record.Paths.RunPath,
	}
}

func (a *App) readOutfitRunRecord(recordPath string) (outfitRunRecord, error) {
	data, err := os.ReadFile(recordPath)
	if err != nil {
		return outfitRunRecord{}, err
	}
	var record outfitRunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return outfitRunRecord{}, err
	}
	return record, nil
}

func (a *App) saveOutfitRunRecord(record outfitRunRecord) error {
	full, err := safeJoin(a.cfg.WorkRoot, record.Paths.MetaPath)
	if err != nil {
		return err
	}
	if err := writeJSONFileAtomic(full, record); err != nil {
		return err
	}
	return a.upsertProjectOutfitRunIndex(record.Outfit.Project, a.runIndexEntryFromRecord(record))
}

func (a *App) createAcceptedOutfitRun(outfit OutfitRecord, source executionSourceInfo, triggerPayload any) (*outfitRunRecord, error) {
	outfit = normalizeOutfitRecord(outfit)
	project := strings.TrimSpace(outfit.Project)
	if project == "" {
		return nil, errors.New("outfit project is required")
	}
	groupRoot, err := a.outfitRunGroupRoot(project, outfit)
	if err != nil {
		return nil, err
	}
	_ = os.MkdirAll(groupRoot, 0o755)
	entries, _ := os.ReadDir(groupRoot)
	nextN := 1
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, "r") {
			continue
		}
		end := strings.Index(name, "_")
		if end <= 1 {
			continue
		}
		if n, convErr := strconv.Atoi(name[1:end]); convErr == nil && n >= nextN {
			nextN = n + 1
		}
	}
	createdAt := time.Now().UTC().Format(time.RFC3339)
	runID := fmt.Sprintf("r%d_%s", nextN, time.Now().UTC().Format("2006-01-02T15-04-05Z"))
	runRoot, err := safeJoin(groupRoot, runID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(runRoot, "meta"), 0o755); err != nil {
		return nil, err
	}
	runtime := parseOutfitRuntimeInput(triggerPayload)
	record := outfitRunRecord{
		AgentGOFile: agentGOFileOutfitRun,
		FileVersion: agentGOFileVersion,
		Run:         outfitRunRecordRun{RunID: runID, Status: "accepted", CreatedAt: createdAt},
		Outfit: outfitRunRecordOutfit{
			ID:                   outfit.ID,
			Name:                 outfit.Name,
			Slug:                 outfitSlug(outfit.Name),
			Project:              project,
			UseCypher:            outfit.UseCypher,
			DeliveryMode:         outfit.DeliveryMode,
			PayloadType:          outfit.PayloadType,
			CallbackURL:          outfit.CallbackURL,
			CallbackHeaders:      outfit.CallbackHeaders,
			HasBearerToken:       strings.TrimSpace(outfit.CallbackBearerToken) != "",
			FileSetSelectorType:  outfit.FileSetSelectorType,
			FileSetSelectorValue: outfit.FileSetSelectorValue,
		},
		Trigger: outfitRunRecordTrigger{
			TriggerType:            strings.TrimSpace(source.TriggerType),
			Source:                 strings.TrimSpace(source.TriggerType),
			Input:                  triggerPayload,
			RuntimePromptField:     runtime.RuntimePromptField,
			RuntimeObjective:       runtime.RuntimePrompt,
			OriginalObjective:      runtime.OriginalObjective,
			Diagnostics:            runtime.DiagnosticsText,
			DiagnosticsFingerprint: runtime.DiagnosticsFingerprint,
			AdditionalContext:      runtime.AdditionalContext,
			PreviousRunID:          runtime.PreviousRunID,
			CycleNumber:            runtime.CycleNumber,
			MaxCycles:              runtime.MaxCycles,
			HasRuntimePrompt:       runtime.HasRuntimePrompt,
			HasDiagnostics:         runtime.HasDiagnostics,
		},
		Execution: outfitRunRecordExecution{WorkflowType: normalizeOutfitExecutionMode(outfit.ExecutionMode), ModelsUsed: activeOutfitBuilderIDs(outfit), WaveCount: activeOutfitWaveCount(outfit)},
		Delivery:  outfitRunRecordDelivery{Mode: outfit.DeliveryMode, PayloadType: outfit.PayloadType, Status: map[bool]string{true: "pending", false: "local_only"}[outfit.DeliveryMode == "callback"], Target: strings.TrimSpace(outfit.CallbackURL)},
		Paths: outfitRunRecordPaths{
			RunPath:         filepath.ToSlash(filepath.Join("projects", project, "outfits", outfit.ID+"_"+outfitSlug(outfit.Name), runID)),
			MetaPath:        filepath.ToSlash(filepath.Join("projects", project, "outfits", outfit.ID+"_"+outfitSlug(outfit.Name), runID, "meta", "run.json")),
			ProjectworkPath: filepath.ToSlash(filepath.Join("projects", project, "outfits", outfit.ID+"_"+outfitSlug(outfit.Name), runID, "projectwork")),
			DeadDropPath:    filepath.ToSlash(filepath.Join("projects", project, "outfits", outfit.ID+"_"+outfitSlug(outfit.Name), runID, "deaddrop")),
		},
		Version: outfitRunRecordVersion{AppRevision: normalizeRevisionLabel(a.release.Revision), RunSchemaVersion: outfitRunSchemaVersion},
	}
	if err := a.saveOutfitRunRecord(record); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.activeOutfitRunsByProject[project] = activeOutfitRun{OutfitID: outfit.ID, Project: project, RunID: runID, RunPath: record.Paths.RunPath, RecordPath: record.Paths.MetaPath}
	a.mu.Unlock()
	return &record, nil
}

func outfitFailureRecord(stage, message string) outfitRunRecordFailure {
	stage = strings.TrimSpace(stage)
	message = strings.TrimSpace(message)
	if stage == "" {
		stage = "workflow"
	}
	failure := outfitRunRecordFailure{Stage: stage, Reason: message, SafeToRetry: true}
	lowerStage := strings.ToLower(stage)
	lowerMessage := strings.ToLower(message)
	switch {
	case strings.Contains(lowerStage, "cypher") || strings.Contains(lowerMessage, "cypher"):
		failure.CypherError = message
	case strings.Contains(lowerStage, "callback"):
		failure.CallbackError = message
	case strings.Contains(lowerStage, "auto_merge") || strings.Contains(lowerStage, "merge"):
		failure.AutoMergeError = message
	case strings.Contains(lowerStage, "builder"):
		failure.BuilderError = message
	}
	if strings.Contains(lowerMessage, "excluded file") || strings.Contains(lowerMessage, "never_send") || strings.Contains(lowerMessage, "forbidden") || strings.Contains(lowerMessage, "invalid outfit api key") {
		failure.SafeToRetry = false
	}
	return failure
}

func (a *App) finalizeAcceptedOutfitRunFailureAt(runRecord *outfitRunRecord, stage, message string) {
	if runRecord == nil {
		return
	}
	runRecord.Run.Status = "failed"
	runRecord.Run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	runRecord.Execution.OutcomeSummary = strings.TrimSpace(message)
	runRecord.Failure = outfitFailureRecord(stage, message)
	runRecord.Delivery.Error = strings.TrimSpace(message)
	_ = a.saveOutfitRunRecord(*runRecord)
	_ = a.enforceOutfitRunRetention(runRecord.Outfit.Project, runRecord.Outfit.ID)
	a.mu.Lock()
	delete(a.activeOutfitRunsByProject, strings.TrimSpace(runRecord.Outfit.Project))
	a.mu.Unlock()
}

func (a *App) markAcceptedOutfitRunRunning(runRecord *outfitRunRecord) {
	if runRecord == nil {
		return
	}
	runRecord.Run.Status = "running"
	runRecord.Run.StartedAt = time.Now().UTC().Format(time.RFC3339)
	_ = a.saveOutfitRunRecord(*runRecord)
}

func (a *App) finalizeAcceptedOutfitRunFailure(runRecord *outfitRunRecord, message string) {
	a.finalizeAcceptedOutfitRunFailureAt(runRecord, "start", message)
}

func (a *App) activeOutfitRun(project string) (activeOutfitRun, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	run, ok := a.activeOutfitRunsByProject[strings.TrimSpace(project)]
	return run, ok
}

func (a *App) loadActiveOutfitRunRecord(project string) (*outfitRunRecord, error) {
	active, ok := a.activeOutfitRun(project)
	if !ok {
		return nil, errors.New("active outfit run not found")
	}
	full, err := safeJoin(a.cfg.WorkRoot, active.RecordPath)
	if err != nil {
		return nil, err
	}
	record, err := a.readOutfitRunRecord(full)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

func (a *App) snapshotProjectworkForRun(record *outfitRunRecord) error {
	src, err := a.projectWorkRoot(record.Outfit.Project)
	if err != nil {
		return err
	}
	dst, err := safeJoin(a.cfg.WorkRoot, record.Paths.ProjectworkPath)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	_, err = copyWorkingTreeInto(src, dst)
	return err
}

func (a *App) snapshotDeadDropForRun(record *outfitRunRecord) error {
	src, err := a.deadDropRoot(record.Outfit.Project)
	if err != nil {
		return err
	}
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	dst, err := safeJoin(a.cfg.WorkRoot, record.Paths.DeadDropPath)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	_, err = copyWorkingTreeInto(src, dst)
	return err
}

func (a *App) finalizeActiveDeadDropOutfitRunCompleted(project, summary string) {
	record, err := a.loadActiveOutfitRunRecord(project)
	if err != nil {
		return
	}
	if normalizeOutfitExecutionMode(record.Execution.WorkflowType) != "deaddrop" {
		return
	}
	if err := a.snapshotDeadDropForRun(record); err != nil {
		a.finalizeActiveDeadDropOutfitRunFailed(project, err.Error())
		return
	}
	record.Run.Status = "completed"
	record.Run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	record.Execution.OutcomeSummary = strings.TrimSpace(summary)
	record.Delivery.Status = "local_only"
	record.Delivery.Error = ""
	if saveErr := a.saveOutfitRunRecord(*record); saveErr == nil {
		_ = a.enforceOutfitRunRetention(record.Outfit.Project, record.Outfit.ID)
	}
	a.mu.Lock()
	delete(a.activeOutfitRunsByProject, strings.TrimSpace(project))
	a.mu.Unlock()
}

func (a *App) finalizeActiveDeadDropOutfitRunFailed(project, message string) {
	record, err := a.loadActiveOutfitRunRecord(project)
	if err != nil {
		return
	}
	if normalizeOutfitExecutionMode(record.Execution.WorkflowType) != "deaddrop" {
		return
	}
	record.Run.Status = "failed"
	record.Run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	record.Execution.OutcomeSummary = strings.TrimSpace(message)
	record.Failure = outfitFailureRecord("deaddrop", message)
	record.Delivery.Error = strings.TrimSpace(message)
	_ = a.saveOutfitRunRecord(*record)
	_ = a.enforceOutfitRunRetention(record.Outfit.Project, record.Outfit.ID)
	a.mu.Lock()
	delete(a.activeOutfitRunsByProject, strings.TrimSpace(project))
	a.mu.Unlock()
}

func (a *App) finalizeActiveOutfitRunCompleted(project, winnerID, winnerLabel, summary string) {
	record, err := a.loadActiveOutfitRunRecord(project)
	if err != nil {
		return
	}
	if err := a.snapshotProjectworkForRun(record); err != nil {
		a.finalizeActiveOutfitRunFailed(project, err.Error())
		return
	}
	record.Run.Status = "completed"
	record.Run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	record.Execution.SelectedWinnerModelID = strings.TrimSpace(winnerID)
	record.Execution.SelectedWinnerLabel = strings.TrimSpace(winnerLabel)
	record.Execution.OutcomeSummary = strings.TrimSpace(summary)
	mergeSummary := a.currentLastMergeSummary(project)
	record.Execution.AddedFiles = normalizeRelativePaths(mergeSummary.Files.Added)
	record.Execution.ModifiedFiles = normalizeRelativePaths(mergeSummary.Files.Modified)
	record.Execution.DeletedFiles = normalizeRelativePaths(mergeSummary.Files.Deleted)
	record.Execution.ChangedFiles = normalizeRelativePaths(a.currentLastMergedFiles(project))
	if len(record.Execution.DeletedFiles) == 0 {
		record.Execution.DeletedFiles = normalizeRelativePaths(a.currentLastMergedDeletedFiles(project))
	}
	if record.Delivery.Mode == "callback" {
		if err := a.deliverCompletedOutfitRun(record); err != nil {
			record.Delivery.Status = "failed"
			record.Delivery.Error = err.Error()
			record.Failure.CallbackError = err.Error()
		}
	} else {
		record.Delivery.Status = "local_only"
	}
	if saveErr := a.saveOutfitRunRecord(*record); saveErr == nil {
		_ = a.enforceOutfitRunRetention(record.Outfit.Project, record.Outfit.ID)
	}
	a.mu.Lock()
	delete(a.activeOutfitRunsByProject, strings.TrimSpace(project))
	a.mu.Unlock()
}

func (a *App) finalizeActiveOutfitRunFailed(project, message string) {
	a.finalizeActiveOutfitRunFailedAt(project, "workflow", message)
}

func (a *App) finalizeActiveOutfitRunFailedAt(project, stage, message string) {
	record, err := a.loadActiveOutfitRunRecord(project)
	if err != nil {
		return
	}
	record.Run.Status = "failed"
	record.Run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	record.Execution.OutcomeSummary = strings.TrimSpace(message)
	record.Failure = outfitFailureRecord(stage, message)
	record.Delivery.Error = strings.TrimSpace(message)
	_ = a.saveOutfitRunRecord(*record)
	_ = a.enforceOutfitRunRetention(record.Outfit.Project, record.Outfit.ID)
	a.mu.Lock()
	delete(a.activeOutfitRunsByProject, strings.TrimSpace(project))
	a.mu.Unlock()
}

func (a *App) activeExternalOutfitRunShouldAutoMerge(project string) bool {
	record, err := a.loadActiveOutfitRunRecord(project)
	if err != nil || record == nil {
		return false
	}
	if strings.TrimSpace(record.Outfit.ID) == "" {
		return false
	}
	source := strings.TrimSpace(record.Trigger.TriggerType)
	return source == "webhook" || source == "timer"
}

func (a *App) finalizeActiveOutfitRunStopped(project, message string) {
	record, err := a.loadActiveOutfitRunRecord(project)
	if err != nil {
		return
	}
	record.Run.Status = "stopped"
	record.Run.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	record.Execution.OutcomeSummary = strings.TrimSpace(message)
	record.Delivery.Error = strings.TrimSpace(message)
	_ = a.saveOutfitRunRecord(*record)
	_ = a.enforceOutfitRunRetention(record.Outfit.Project, record.Outfit.ID)
	a.mu.Lock()
	delete(a.activeOutfitRunsByProject, strings.TrimSpace(project))
	a.mu.Unlock()
}

func (a *App) archivedDeadDropCurrentFile(record outfitRunRecord) (string, string, error) {
	root, err := safeJoin(a.cfg.WorkRoot, record.Paths.DeadDropPath)
	if err != nil {
		return "", "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || !isExactDeadDropCandidateName(entry.Name()) {
			continue
		}
		full, joinErr := safeJoin(root, entry.Name())
		if joinErr != nil {
			return "", "", joinErr
		}
		return full, entry.Name(), nil
	}
	return "", "", errors.New("deaddrop_final_missing")
}

func (a *App) buildPullDeadDropFinalResponse(record outfitRunRecord) ([]byte, string, string, error) {
	if normalizeOutfitExecutionMode(record.Execution.WorkflowType) != "deaddrop" {
		return nil, "", "", errors.New("run_is_not_deaddrop")
	}
	full, name, err := a.archivedDeadDropCurrentFile(record)
	if err != nil {
		return nil, "", "", err
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, "", "", err
	}
	contentType := detectContentType(name, data)
	return data, contentType, name, nil
}

func (a *App) buildPullDeadDropZipResponse(record outfitRunRecord) ([]byte, string, string, error) {
	if normalizeOutfitExecutionMode(record.Execution.WorkflowType) != "deaddrop" {
		return nil, "", "", errors.New("run_is_not_deaddrop")
	}
	root, err := safeJoin(a.cfg.WorkRoot, record.Paths.DeadDropPath)
	if err != nil {
		return nil, "", "", err
	}
	var buf bytes.Buffer
	if err := buildProjectZip(root, &buf); err != nil {
		return nil, "", "", err
	}
	return buf.Bytes(), "application/zip", fmt.Sprintf("%s_deaddrop.zip", record.Run.RunID), nil
}

func (a *App) buildPullProjectResponse(record outfitRunRecord) ([]byte, string, error) {
	if normalizeOutfitExecutionMode(record.Execution.WorkflowType) == "deaddrop" {
		return nil, "", errors.New("deaddrop_pull_not_available_yet")
	}
	if record.Outfit.DeliveryMode == "none" {
		return nil, "", errors.New("no_delivery_mode_selected_in_outfit")
	}
	switch record.Outfit.PayloadType {
	case "json":
		target, err := safeJoin(a.cfg.WorkRoot, record.Paths.ProjectworkPath, canonicalBuilderJSONFilename)
		if err != nil {
			return nil, "", err
		}
		data, err := os.ReadFile(target)
		if err != nil {
			return nil, "", errors.New("json_payload_missing")
		}
		var probe any
		if err := json.Unmarshal(data, &probe); err != nil {
			return nil, "", errors.New("json_payload_invalid")
		}
		return data, "application/json", nil
	case "zip", "pull_urls":
		var buf bytes.Buffer
		src, err := safeJoin(a.cfg.WorkRoot, record.Paths.ProjectworkPath)
		if err != nil {
			return nil, "", err
		}
		if err := buildProjectZip(src, &buf); err != nil {
			return nil, "", err
		}
		return buf.Bytes(), "application/zip", nil
	case "file_set":
		return a.buildMultipartFileSet(record)
	default:
		return nil, "", errors.New("payload_type_not_set")
	}
}

func (a *App) buildPullProjectworkZipResponse(record outfitRunRecord) ([]byte, string, string, error) {
	root, err := safeJoin(a.cfg.WorkRoot, record.Paths.ProjectworkPath)
	if err != nil {
		return nil, "", "", err
	}
	var buf bytes.Buffer
	if err := buildProjectZip(root, &buf); err != nil {
		return nil, "", "", err
	}
	return buf.Bytes(), "application/zip", fmt.Sprintf("%s_projectwork.zip", record.Run.RunID), nil
}

func fileDigestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (a *App) buildChangedFilesManifest(record outfitRunRecord) ([]byte, string, string, error) {
	root, err := safeJoin(a.cfg.WorkRoot, record.Paths.ProjectworkPath)
	if err != nil {
		return nil, "", "", err
	}
	changed := normalizeRelativePaths(record.Execution.ChangedFiles)
	deleted := normalizeRelativePaths(record.Execution.DeletedFiles)
	addedSet := map[string]bool{}
	for _, rel := range normalizeRelativePaths(record.Execution.AddedFiles) {
		addedSet[rel] = true
	}
	modifiedSet := map[string]bool{}
	for _, rel := range normalizeRelativePaths(record.Execution.ModifiedFiles) {
		modifiedSet[rel] = true
	}
	entries := []map[string]any{}
	for _, rel := range changed {
		full, err := safeJoin(root, rel)
		if err != nil {
			return nil, "", "", err
		}
		data, readErr := os.ReadFile(full)
		entry := map[string]any{"path": rel}
		status := "changed"
		if addedSet[rel] {
			status = "added"
		} else if modifiedSet[rel] {
			status = "modified"
		}
		entry["status"] = status
		if readErr == nil {
			entry["size_bytes"] = len(data)
			entry["sha256"] = fileDigestHex(data)
		} else {
			entry["missing"] = true
			entry["error"] = readErr.Error()
		}
		entries = append(entries, entry)
	}
	for _, rel := range deleted {
		entries = append(entries, map[string]any{"path": rel, "status": "deleted", "deleted": true})
	}
	payload := map[string]any{
		"type":            "agentgo_changed_files_manifest",
		"run_id":          record.Run.RunID,
		"outfit_id":       record.Outfit.ID,
		"outfit_name":     record.Outfit.Name,
		"project":         record.Outfit.Project,
		"status":          record.Run.Status,
		"use_cypher":      record.Outfit.UseCypher,
		"changed_files":   changed,
		"deleted_files":   deleted,
		"file_count":      len(changed),
		"deleted_count":   len(deleted),
		"files":           entries,
		"created_at":      record.Run.CreatedAt,
		"finished_at":     record.Run.FinishedAt,
		"previous_run_id": record.Trigger.PreviousRunID,
		"cycle_number":    record.Trigger.CycleNumber,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, "", "", err
	}
	return encoded, "application/json", fmt.Sprintf("%s_changed_files.json", record.Run.RunID), nil
}

func (a *App) buildChangedFilesZipResponse(record outfitRunRecord) ([]byte, string, string, error) {
	root, err := safeJoin(a.cfg.WorkRoot, record.Paths.ProjectworkPath)
	if err != nil {
		return nil, "", "", err
	}
	changed := normalizeRelativePaths(record.Execution.ChangedFiles)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, rel := range changed {
		full, err := safeJoin(root, rel)
		if err != nil {
			_ = zw.Close()
			return nil, "", "", err
		}
		info, statErr := os.Stat(full)
		if statErr != nil || info.IsDir() {
			continue
		}
		fh, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			_ = zw.Close()
			return nil, "", "", err
		}
		file, err := os.Open(full)
		if err != nil {
			_ = zw.Close()
			return nil, "", "", err
		}
		_, copyErr := io.Copy(fh, file)
		closeErr := file.Close()
		if copyErr != nil {
			_ = zw.Close()
			return nil, "", "", copyErr
		}
		if closeErr != nil {
			_ = zw.Close()
			return nil, "", "", closeErr
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", "", err
	}
	return buf.Bytes(), "application/zip", fmt.Sprintf("%s_changed_files.zip", record.Run.RunID), nil
}

func listAllRelativeFiles(root string) ([]string, error) {
	rels := []string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(rels)
	return rels, err
}

func (a *App) selectFileSetRelativePaths(record outfitRunRecord, root string) ([]string, error) {
	switch strings.TrimSpace(record.Outfit.FileSetSelectorType) {
	case "", "all":
		return listAllRelativeFiles(root)
	case "canonical":
		if _, err := os.Stat(filepath.Join(root, canonicalBuilderJSONFilename)); err != nil {
			return nil, errors.New("canonical_file_missing")
		}
		return []string{canonicalBuilderJSONFilename}, nil
	case "path_pattern":
		files, err := listAllRelativeFiles(root)
		if err != nil {
			return nil, err
		}
		matches := []string{}
		pattern := filepath.ToSlash(strings.TrimSpace(record.Outfit.FileSetSelectorValue))
		for _, rel := range files {
			if ok, _ := filepath.Match(pattern, filepath.ToSlash(rel)); ok {
				matches = append(matches, rel)
			}
		}
		return matches, nil
	default:
		return nil, errors.New("file_set_selector_invalid")
	}
}

func (a *App) buildMultipartFileSet(record outfitRunRecord) ([]byte, string, error) {
	root, err := safeJoin(a.cfg.WorkRoot, record.Paths.ProjectworkPath)
	if err != nil {
		return nil, "", err
	}
	rels, err := a.selectFileSetRelativePaths(record, root)
	if err != nil {
		return nil, "", err
	}
	if len(rels) == 0 {
		return nil, "", errors.New("file_set_empty")
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	manifest := map[string]any{"run_id": record.Run.RunID, "outfit_id": record.Outfit.ID, "project": record.Outfit.Project, "files": rels}
	manifestBytes, _ := json.Marshal(manifest)
	mf, err := mw.CreateFormField("manifest")
	if err != nil {
		return nil, "", err
	}
	if _, err := mf.Write(manifestBytes); err != nil {
		return nil, "", err
	}
	for _, rel := range rels {
		full, err := safeJoin(root, rel)
		if err != nil {
			return nil, "", err
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, "", err
		}
		part, err := mw.CreateFormFile(filepath.ToSlash(rel), filepath.Base(rel))
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(data); err != nil {
			return nil, "", err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), mw.FormDataContentType(), nil
}

func outfitRunPublicRoute(record outfitRunRecord) string {
	slug := strings.TrimSpace(record.Outfit.Slug)
	if slug == "" {
		slug = outfitSlug(record.Outfit.Name)
	}
	routeID := strings.TrimSpace(record.Outfit.ID)
	if slug != "" {
		routeID = routeID + "_" + slug
	}
	return "/outfits/" + url.PathEscape(routeID)
}

func outfitRunPullPath(record outfitRunRecord, kind string) string {
	return outfitRunPublicRoute(record) + "/runs/" + url.PathEscape(record.Run.RunID) + "/pull/" + url.PathEscape(strings.TrimSpace(kind))
}

func outfitRunPullURLs(record outfitRunRecord) map[string]string {
	urls := map[string]string{
		"run_record":             outfitRunPullPath(record, "meta"),
		"project":                outfitRunPullPath(record, "project"),
		"projectwork_zip":        outfitRunPullPath(record, "projectwork_zip"),
		"changed_files_manifest": outfitRunPullPath(record, "changed_files_manifest"),
		"changed_files_zip":      outfitRunPullPath(record, "changed_files_zip"),
	}
	if normalizeOutfitExecutionMode(record.Execution.WorkflowType) == "deaddrop" {
		urls["final_file"] = outfitRunPullPath(record, "final_file")
		urls["deaddrop_zip"] = outfitRunPullPath(record, "deaddrop_zip")
	}
	return urls
}

func buildOutfitRunPullURLNotification(record outfitRunRecord) ([]byte, string, error) {
	payload := map[string]any{
		"type":            "agentgo_outfit_run_completed",
		"status":          record.Run.Status,
		"run_id":          record.Run.RunID,
		"outfit_id":       record.Outfit.ID,
		"outfit_name":     record.Outfit.Name,
		"project":         record.Outfit.Project,
		"use_cypher":      record.Outfit.UseCypher,
		"created_at":      record.Run.CreatedAt,
		"started_at":      record.Run.StartedAt,
		"finished_at":     record.Run.FinishedAt,
		"changed_files":   record.Execution.ChangedFiles,
		"deleted_files":   record.Execution.DeletedFiles,
		"previous_run_id": record.Trigger.PreviousRunID,
		"cycle_number":    record.Trigger.CycleNumber,
		"pull_urls":       outfitRunPullURLs(record),
		"summary":         record.Execution.OutcomeSummary,
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, "", err
	}
	return encoded, "application/json", nil
}

func (a *App) deliverCompletedOutfitRun(record *outfitRunRecord) error {
	if strings.TrimSpace(record.Outfit.CallbackURL) == "" {
		return errors.New("callback_url_not_set")
	}
	var payload []byte
	var contentType string
	var err error
	if record.Outfit.PayloadType == "pull_urls" {
		payload, contentType, err = buildOutfitRunPullURLNotification(*record)
	} else {
		payload, contentType, err = a.buildPullProjectResponse(*record)
	}
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, record.Outfit.CallbackURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	for k, v := range record.Outfit.CallbackHeaders {
		req.Header.Set(k, v)
	}
	if outfit, readErr := a.readOutfitRecord(record.Outfit.ID); readErr == nil {
		if token := strings.TrimSpace(outfit.CallbackBearerToken); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		return errors.New(message)
	}
	record.Delivery.Status = "delivered"
	record.Delivery.Error = ""
	record.Delivery.DeliveredAt = time.Now().UTC().Format(time.RFC3339)
	return nil
}

func (a *App) enforceOutfitRunRetention(project, outfitID string) error {
	limit := a.cfg.OutfitRunRetention
	if limit <= 0 {
		return nil
	}
	entries, err := a.readProjectOutfitRunsIndex(project)
	if err != nil {
		return err
	}
	matching := make([]outfitRunIndexEntry, 0)
	for _, entry := range entries {
		if entry.OutfitID == outfitID {
			matching = append(matching, entry)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].CreatedAt < matching[j].CreatedAt })
	for len(matching) > limit {
		oldest := matching[0]
		matching = matching[1:]
		_ = a.deleteArchivedOutfitRun(oldest.OutfitID, oldest.RunID)
	}
	return nil
}

func (a *App) findArchivedOutfitRun(outfitID, runID string) (*outfitRunRecord, error) {
	projectsRoot, err := safeJoin(a.cfg.WorkRoot, "projects")
	if err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(projectsRoot)
	if err != nil {
		return nil, err
	}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		entries, indexErr := a.readProjectOutfitRunsIndex(project.Name())
		if indexErr != nil {
			continue
		}
		for _, entry := range entries {
			if entry.OutfitID != outfitID || entry.RunID != runID {
				continue
			}
			recordPath := filepath.ToSlash(filepath.Join(entry.Path, "meta", "run.json"))
			full, joinErr := safeJoin(a.cfg.WorkRoot, recordPath)
			if joinErr != nil {
				return nil, joinErr
			}
			record, readErr := a.readOutfitRunRecord(full)
			if readErr != nil {
				return nil, readErr
			}
			return &record, nil
		}
	}
	return nil, errors.New("run_not_found")
}

func (a *App) listArchivedOutfitRuns(outfitID string) ([]outfitRunRecord, error) {
	projectsRoot, err := safeJoin(a.cfg.WorkRoot, "projects")
	if err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(projectsRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []outfitRunRecord{}, nil
		}
		return nil, err
	}
	records := []outfitRunRecord{}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		entries, indexErr := a.readProjectOutfitRunsIndex(project.Name())
		if indexErr != nil {
			continue
		}
		for _, entry := range entries {
			if entry.OutfitID != outfitID {
				continue
			}
			recordPath := filepath.ToSlash(filepath.Join(entry.Path, "meta", "run.json"))
			full, joinErr := safeJoin(a.cfg.WorkRoot, recordPath)
			if joinErr != nil {
				continue
			}
			record, readErr := a.readOutfitRunRecord(full)
			if readErr != nil {
				continue
			}
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Run.CreatedAt > records[j].Run.CreatedAt })
	return records, nil
}

func (a *App) deleteArchivedOutfitRun(outfitID, runID string) error {
	record, err := a.findArchivedOutfitRun(outfitID, runID)
	if err != nil {
		return err
	}
	fullRun, err := safeJoin(a.cfg.WorkRoot, record.Paths.RunPath)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(fullRun); err != nil {
		return err
	}
	return a.removeProjectOutfitRunIndex(record.Outfit.Project, outfitID, runID)
}

func readOptionalJSONBody(r *http.Request) any {
	if r.Body == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err == nil {
		return value
	}
	return map[string]any{"raw_body": string(data)}
}

func (a *App) handleAPIOutfitRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	outfitID := strings.TrimSpace(r.URL.Query().Get("id"))
	if outfitID == "" {
		http.Error(w, "outfit id is required", http.StatusBadRequest)
		return
	}
	outfit, err := a.readOutfitRecord(outfitID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	resp, err := a.buildOutfitRunsListResponse(outfit, statusFilter, 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) handleAPIOutfitRunMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	outfitID := strings.TrimSpace(r.URL.Query().Get("id"))
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if outfitID == "" || runID == "" {
		http.Error(w, "outfit id and run id are required", http.StatusBadRequest)
		return
	}
	record, err := a.findArchivedOutfitRun(outfitID, runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, record)
}

func (a *App) handleAPIOutfitRunProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	outfitID := strings.TrimSpace(r.URL.Query().Get("id"))
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if outfitID == "" || runID == "" {
		http.Error(w, "outfit id and run id are required", http.StatusBadRequest)
		return
	}
	record, err := a.findArchivedOutfitRun(outfitID, runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	payload, contentType, err := a.buildPullProjectResponse(*record)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ext := ".bin"
	switch record.Outfit.PayloadType {
	case "json":
		ext = ".json"
	case "zip", "pull_urls":
		ext = ".zip"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s%s"`, record.Run.RunID, ext))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) handleAPIOutfitRunDeadDropFinal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	outfitID := strings.TrimSpace(r.URL.Query().Get("id"))
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if outfitID == "" || runID == "" {
		http.Error(w, "outfit id and run id are required", http.StatusBadRequest)
		return
	}
	record, err := a.findArchivedOutfitRun(outfitID, runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	payload, contentType, fileName, err := a.buildPullDeadDropFinalResponse(*record)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s_%s"`, record.Run.RunID, fileName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) handleAPIOutfitRunDeadDropZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	outfitID := strings.TrimSpace(r.URL.Query().Get("id"))
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if outfitID == "" || runID == "" {
		http.Error(w, "outfit id and run id are required", http.StatusBadRequest)
		return
	}
	record, err := a.findArchivedOutfitRun(outfitID, runID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	payload, contentType, fileName, err := a.buildPullDeadDropZipResponse(*record)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) findArchivedOutfitRunFromQuery(r *http.Request) (*outfitRunRecord, error) {
	outfitID := strings.TrimSpace(r.URL.Query().Get("id"))
	runID := strings.TrimSpace(r.URL.Query().Get("runId"))
	if outfitID == "" || runID == "" {
		return nil, errors.New("outfit id and run id are required")
	}
	return a.findArchivedOutfitRun(outfitID, runID)
}

func (a *App) handleAPIOutfitRunProjectworkZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	record, err := a.findArchivedOutfitRunFromQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	payload, contentType, fileName, err := a.buildPullProjectworkZipResponse(*record)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) handleAPIOutfitRunChangedFilesManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	record, err := a.findArchivedOutfitRunFromQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	payload, contentType, fileName, err := a.buildChangedFilesManifest(*record)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) handleAPIOutfitRunChangedFilesZip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	record, err := a.findArchivedOutfitRunFromQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	payload, contentType, fileName, err := a.buildChangedFilesZipResponse(*record)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (a *App) handleAPIOutfitRunCallbackRetry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID    string `json:"id"`
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ID) == "" || strings.TrimSpace(req.RunID) == "" {
		http.Error(w, "outfit id and run id are required", http.StatusBadRequest)
		return
	}
	record, err := a.findArchivedOutfitRun(strings.TrimSpace(req.ID), strings.TrimSpace(req.RunID))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if record.Delivery.Mode != "callback" {
		http.Error(w, "run was not configured for callback delivery", http.StatusBadRequest)
		return
	}
	if err := a.deliverCompletedOutfitRun(record); err != nil {
		record.Delivery.Status = "failed"
		record.Delivery.Error = err.Error()
		record.Failure.CallbackError = err.Error()
		_ = a.saveOutfitRunRecord(*record)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := a.saveOutfitRunRecord(*record); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "delivered", "runId": record.Run.RunID, "delivered_at": record.Delivery.DeliveredAt})
}

func (a *App) handleAPIOutfitRunDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID    string `json:"id"`
		RunID string `json:"runId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := a.deleteArchivedOutfitRun(strings.TrimSpace(req.ID), strings.TrimSpace(req.RunID)); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "runId": strings.TrimSpace(req.RunID)})
}

func outfitPublicAPIKeyFromRequest(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get(outfitAuthHeader)); value != "" {
		return value
	}
	if value := strings.TrimSpace(r.Header.Get("X-AgentGO-Token")); value != "" {
		return value
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func outfitPublicAPIKeyMatches(r *http.Request, outfit OutfitRecord) bool {
	expected := strings.TrimSpace(outfit.WebhookAPIKey)
	if expected == "" {
		return false
	}
	return outfitPublicAPIKeyFromRequest(r) == expected
}

func (a *App) handleOutfitPublicAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/outfits/"), "/")
	if path == "" {
		writeOutfitAPIError(w, http.StatusNotFound, "route_not_found", "Outfit route not found.")
		return
	}
	parts := strings.Split(path, "/")
	outfitID := parseOutfitRouteID(parts[0])
	if outfitID == "" {
		writeOutfitAPIError(w, http.StatusNotFound, "outfit_not_found", "Outfit not found.")
		return
	}
	outfit, err := a.readOutfitRecord(outfitID)
	if err != nil {
		writeOutfitAPIError(w, http.StatusNotFound, "outfit_not_found", "Outfit not found.")
		return
	}
	if !outfitPublicAPIKeyMatches(r, outfit) {
		writeOutfitAPIError(w, http.StatusUnauthorized, "invalid_outfit_api_key", "Invalid Outfit API key.")
		return
	}
	if len(parts) == 2 && parts[1] == "run" && r.Method == http.MethodPost {
		a.handlePublicOutfitRun(w, r, outfit)
		return
	}
	if len(parts) == 3 && parts[1] == "run" && parts[2] == "deaddrop" && r.Method == http.MethodPost {
		a.handlePublicDeadDropOutfitRun(w, r, outfit)
		return
	}
	if len(parts) >= 2 && parts[1] == "runs" {
		if len(parts) == 2 && r.Method == http.MethodGet {
			a.handlePublicOutfitRunsList(w, r, outfit)
			return
		}
		if len(parts) == 3 && r.Method == http.MethodDelete {
			a.handlePublicOutfitRunDelete(w, outfit, strings.TrimSpace(parts[2]))
			return
		}
		if len(parts) == 5 && parts[3] == "pull" && r.Method == http.MethodGet {
			a.handlePublicOutfitRunPull(w, outfit, strings.TrimSpace(parts[2]), strings.TrimSpace(parts[4]))
			return
		}
	}
	if len(parts) == 4 && parts[1] == "latest" && parts[2] == "pull" && r.Method == http.MethodGet {
		a.handlePublicOutfitLatestPull(w, outfit, strings.TrimSpace(parts[3]))
		return
	}
	writeOutfitAPIError(w, http.StatusNotFound, "route_not_found", "Outfit route not found.")
}

func extractSingleDeadDropUpload(r *http.Request) (string, []byte, map[string]any, error) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		return "", nil, nil, errors.New("invalid multipart form")
	}
	files := []*multipart.FileHeader{}
	for _, group := range r.MultipartForm.File {
		for _, fh := range group {
			files = append(files, fh)
		}
	}
	if len(files) != 1 {
		return "", nil, nil, errors.New("upload exactly one DeadDrop.<ext> file")
	}
	fh := files[0]
	name := strings.TrimSpace(fh.Filename)
	if !isExactDeadDropCandidateName(filepath.Base(name)) {
		return "", nil, nil, errors.New("uploaded file must be named exactly DeadDrop.<ext>")
	}
	file, err := fh.Open()
	if err != nil {
		return "", nil, nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 128<<20))
	if err != nil {
		return "", nil, nil, err
	}
	payload := map[string]any{}
	for key, values := range r.MultipartForm.Value {
		cleanKey := strings.TrimSpace(key)
		if cleanKey == "" || len(values) == 0 {
			continue
		}
		if len(values) == 1 {
			payload[cleanKey] = strings.TrimSpace(values[0])
		} else {
			trimmed := make([]string, 0, len(values))
			for _, value := range values {
				trimmed = append(trimmed, strings.TrimSpace(value))
			}
			payload[cleanKey] = trimmed
		}
	}
	payload["uploaded_file"] = name
	payload["uploaded_bytes"] = len(data)
	return filepath.Base(name), data, payload, nil
}

func (a *App) handlePublicOutfitRun(w http.ResponseWriter, r *http.Request, outfit OutfitRecord) {
	if !outfit.WebhookEnabled {
		writeOutfitAPIError(w, http.StatusForbidden, "webhook_disabled", "Webhook trigger is disabled for this Outfit.")
		return
	}
	payload := readOptionalJSONBody(r)
	_, runErr, status, runRecord := a.startOutfitTriggerRun(outfit, executionSourceInfo{TriggerType: "webhook", OutfitID: outfit.ID, OutfitName: outfit.Name}, "", payload)
	if runErr != nil {
		switch status {
		case http.StatusConflict:
			writeOutfitAPIError(w, http.StatusConflict, "busy", "AgentGO is already running another workflow.")
		case http.StatusBadRequest:
			writeOutfitAPIError(w, http.StatusBadRequest, "bad_request", runErr.Error())
		default:
			writeOutfitAPIError(w, status, "run_failed", runErr.Error())
		}
		return
	}
	if runRecord == nil {
		writeOutfitAPIError(w, http.StatusInternalServerError, "run_not_recorded", "The Outfit run was accepted but no run record was created.")
		return
	}
	writeJSON(w, http.StatusAccepted, outfitPublicRunAcceptedResponse{Status: "accepted", RunID: runRecord.Run.RunID, OutfitID: runRecord.Outfit.ID, Project: runRecord.Outfit.Project, CreatedAt: runRecord.Run.CreatedAt, RunPath: runRecord.Paths.RunPath, MetaPath: runRecord.Paths.MetaPath})
}

func (a *App) handlePublicDeadDropOutfitRun(w http.ResponseWriter, r *http.Request, outfit OutfitRecord) {
	if !outfit.WebhookEnabled {
		writeOutfitAPIError(w, http.StatusForbidden, "webhook_disabled", "Webhook trigger is disabled for this Outfit.")
		return
	}
	if normalizeOutfitExecutionMode(outfit.ExecutionMode) != "deaddrop" {
		writeOutfitAPIError(w, http.StatusBadRequest, "not_deaddrop_outfit", "This Outfit is not a DeadDrop Outfit.")
		return
	}
	if normalizeDeadDropSourcePolicy(outfit.DeadDropSourcePolicy) != "accept_webhook_upload" {
		writeOutfitAPIError(w, http.StatusBadRequest, "invalid_source_policy", "This DeadDrop Outfit is not configured to accept webhook uploads.")
		return
	}
	fileName, data, payload, err := extractSingleDeadDropUpload(r)
	if err != nil {
		writeOutfitAPIError(w, http.StatusBadRequest, "bad_upload", err.Error())
		return
	}
	if _, _, _, err := a.setDeadDropSourceFromData(strings.TrimSpace(outfit.Project), fileName, data); err != nil {
		writeOutfitAPIError(w, http.StatusBadRequest, "deaddrop_seed_failed", err.Error())
		return
	}
	_, runErr, status, runRecord := a.startOutfitTriggerRun(outfit, executionSourceInfo{TriggerType: "webhook", OutfitID: outfit.ID, OutfitName: outfit.Name, DeadDropUploadReady: true}, "", payload)
	if runErr != nil {
		switch status {
		case http.StatusConflict:
			writeOutfitAPIError(w, http.StatusConflict, "busy", "AgentGO is already running another workflow.")
		case http.StatusBadRequest:
			writeOutfitAPIError(w, http.StatusBadRequest, "bad_request", runErr.Error())
		default:
			writeOutfitAPIError(w, status, "run_failed", runErr.Error())
		}
		return
	}
	if runRecord == nil {
		writeOutfitAPIError(w, http.StatusInternalServerError, "run_not_recorded", "The Outfit run was accepted but no run record was created.")
		return
	}
	writeJSON(w, http.StatusAccepted, outfitPublicRunAcceptedResponse{Status: "accepted", RunID: runRecord.Run.RunID, OutfitID: runRecord.Outfit.ID, Project: runRecord.Outfit.Project, CreatedAt: runRecord.Run.CreatedAt, RunPath: runRecord.Paths.RunPath, MetaPath: runRecord.Paths.MetaPath})
}

func (a *App) handlePublicOutfitRunsList(w http.ResponseWriter, r *http.Request, outfit OutfitRecord) {
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, convErr := strconv.Atoi(raw); convErr == nil && n > 0 {
			limit = n
		}
	}
	resp, err := a.buildOutfitRunsListResponse(outfit, statusFilter, limit)
	if err != nil {
		writeOutfitAPIError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *App) handlePublicOutfitLatestPull(w http.ResponseWriter, outfit OutfitRecord, kind string) {
	records, err := a.listArchivedOutfitRuns(outfit.ID)
	if err != nil || len(records) == 0 {
		writeOutfitAPIError(w, http.StatusNotFound, "no_runs_available", "No archived runs are available for this Outfit.")
		return
	}
	a.handlePublicOutfitRunPullResolved(w, records[0], kind)
}

func (a *App) handlePublicOutfitRunPull(w http.ResponseWriter, outfit OutfitRecord, runID, kind string) {
	record, err := a.findArchivedOutfitRun(outfit.ID, runID)
	if err != nil {
		writeOutfitAPIError(w, http.StatusNotFound, "run_not_found", fmt.Sprintf("Run %s was not found for outfit %s.", runID, outfit.ID))
		return
	}
	a.handlePublicOutfitRunPullResolved(w, *record, kind)
}

func (a *App) handlePublicOutfitRunPullResolved(w http.ResponseWriter, record outfitRunRecord, kind string) {
	switch kind {
	case "meta":
		writeJSON(w, http.StatusOK, record)
	case "project":
		payload, contentType, err := a.buildPullProjectResponse(record)
		if err != nil {
			writeOutfitAPIError(w, http.StatusBadRequest, err.Error(), err.Error())
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	case "projectwork_zip":
		payload, contentType, fileName, err := a.buildPullProjectworkZipResponse(record)
		if err != nil {
			writeOutfitAPIError(w, http.StatusBadRequest, err.Error(), err.Error())
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileName))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	case "changed_files_manifest", "changed_files":
		payload, contentType, fileName, err := a.buildChangedFilesManifest(record)
		if err != nil {
			writeOutfitAPIError(w, http.StatusBadRequest, err.Error(), err.Error())
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileName))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	case "changed_files_zip":
		payload, contentType, fileName, err := a.buildChangedFilesZipResponse(record)
		if err != nil {
			writeOutfitAPIError(w, http.StatusBadRequest, err.Error(), err.Error())
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileName))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	case "final_file":
		payload, contentType, fileName, err := a.buildPullDeadDropFinalResponse(record)
		if err != nil {
			writeOutfitAPIError(w, http.StatusBadRequest, err.Error(), err.Error())
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s_%s"`, record.Run.RunID, fileName))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	case "deaddrop_zip":
		payload, contentType, fileName, err := a.buildPullDeadDropZipResponse(record)
		if err != nil {
			writeOutfitAPIError(w, http.StatusBadRequest, err.Error(), err.Error())
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fileName))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	default:
		writeOutfitAPIError(w, http.StatusNotFound, "route_not_found", "Requested pull target was not found.")
	}
}

func (a *App) handlePublicOutfitRunDelete(w http.ResponseWriter, outfit OutfitRecord, runID string) {
	if err := a.deleteArchivedOutfitRun(outfit.ID, runID); err != nil {
		writeOutfitAPIError(w, http.StatusNotFound, "run_not_found", fmt.Sprintf("Run %s was not found for outfit %s.", runID, outfit.ID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "run_id": runID})
}
