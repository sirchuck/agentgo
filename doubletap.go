package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentgo/adapters"
)

const (
	doubleTapFileName       = "DoubleTap.json"
	doubleTapArchiveDirName = "doubletap"
	doubleTapMinCount       = 2
	doubleTapMaxCount       = 20
	doubleTapDefaultCount   = 4
)

type doubleTapDocument struct {
	AgentGOFile    string           `json:"agentgo_file"`
	FileVersion    int              `json:"file_version"`
	Project        string           `json:"project"`
	RunID          string           `json:"run_id"`
	OriginalPrompt string           `json:"original_prompt"`
	CountTotal     int              `json:"count_total"`
	CallsUsed      int              `json:"calls_used"`
	Status         string           `json:"status"`
	StartedAt      string           `json:"started_at"`
	UpdatedAt      string           `json:"updated_at"`
	FinalAnswer    string           `json:"final_answer,omitempty"`
	ArchivePath    string           `json:"archive_path,omitempty"`
	Events         []doubleTapEvent `json:"events"`
}

type doubleTapEvent struct {
	CallNumber           int      `json:"call_number"`
	Wave                 int      `json:"wave"`
	Role                 string   `json:"role"`
	ModelID              string   `json:"model_id"`
	ModelLabel           string   `json:"model_label"`
	StartedAt            string   `json:"started_at"`
	FinishedAt           string   `json:"finished_at"`
	ElapsedSeconds       float64  `json:"elapsed_seconds"`
	Summary              string   `json:"summary,omitempty"`
	SupportedClaims      []string `json:"supported_claims,omitempty"`
	InferredClaims       []string `json:"inferred_claims,omitempty"`
	SpeculativeClaims    []string `json:"speculative_claims,omitempty"`
	QuestionableClaims   []string `json:"questionable_claims,omitempty"`
	RemovedClaims        []string `json:"removed_claims,omitempty"`
	SourceReferences     []string `json:"source_references,omitempty"`
	RecommendedDirection string   `json:"recommended_direction,omitempty"`
	NextQuestions        []string `json:"next_questions,omitempty"`
	FinalAnswer          string   `json:"final_answer,omitempty"`
	Notes                string   `json:"notes,omitempty"`
}

type doubleTapAIResponse struct {
	AgentGOTool string        `json:"agentgo_tool,omitempty"`
	ToolVersion int           `json:"tool_version,omitempty"`
	Memo        doubleTapMemo `json:"memo,omitempty"`
	FinalAnswer string        `json:"final_answer,omitempty"`
	Notes       string        `json:"notes,omitempty"`
}

type doubleTapMemo struct {
	Summary              string   `json:"summary,omitempty"`
	SupportedClaims      []string `json:"supported_claims,omitempty"`
	InferredClaims       []string `json:"inferred_claims,omitempty"`
	SpeculativeClaims    []string `json:"speculative_claims,omitempty"`
	QuestionableClaims   []string `json:"questionable_claims,omitempty"`
	RemovedClaims        []string `json:"removed_claims,omitempty"`
	SourceReferences     []string `json:"source_references,omitempty"`
	RecommendedDirection string   `json:"recommended_direction,omitempty"`
	NextQuestions        []string `json:"next_questions,omitempty"`
}

func normalizeDoubleTapCountServer(value int) int {
	if value < doubleTapMinCount {
		return doubleTapDefaultCount
	}
	if value > doubleTapMaxCount {
		return doubleTapMaxCount
	}
	return value
}

func (a *App) doubleTapPath(projectName string) (string, error) {
	root, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, doubleTapFileName), nil
}

func (a *App) doubleTapArchiveDir(projectName string) (string, error) {
	root, err := a.projectSettingsDir(projectName)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, doubleTapArchiveDirName), nil
}

func writeDoubleTapDocument(path string, doc doubleTapDocument) error {
	doc.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(path, append(data, '\n'), 0o644)
}

func doubleTapExecutionIDLooksLikeDoubleTap(executionID string) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(executionID)), "-doubletap-")
}

func isDoubleTapStoppedError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "canceled") || strings.Contains(text, "cancelled") || strings.Contains(text, "stale doubletap execution") || strings.Contains(text, "execution stopped")
}

func (a *App) recordDoubleTapStopped(projectName, executionID, reason string) {
	projectName = strings.TrimSpace(projectName)
	executionID = strings.TrimSpace(executionID)
	if projectName == "" || executionID == "" || !doubleTapExecutionIDLooksLikeDoubleTap(executionID) {
		return
	}
	if strings.TrimSpace(reason) == "" {
		reason = "DoubleTap stopped."
	}
	path, err := a.doubleTapPath(projectName)
	if err != nil {
		a.logf("system", "warn", "DoubleTap stop state could not resolve path for project %s: %v", projectName, err)
		return
	}
	var doc doubleTapDocument
	if data, readErr := os.ReadFile(path); readErr == nil {
		_ = json.Unmarshal(data, &doc)
	}
	if strings.TrimSpace(doc.RunID) != "" && strings.TrimSpace(doc.RunID) != executionID {
		return
	}
	if strings.TrimSpace(doc.RunID) == "" {
		now := time.Now().UTC().Format(time.RFC3339)
		doc = doubleTapDocument{AgentGOFile: agentGOToolDoubleTap, FileVersion: agentGOToolVersion, Project: projectName, RunID: executionID, Status: "running", StartedAt: now, UpdatedAt: now, Events: []doubleTapEvent{}}
	}
	if strings.EqualFold(strings.TrimSpace(doc.Status), "stopped") || strings.EqualFold(strings.TrimSpace(doc.Status), "complete") {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	doc.Status = "stopped"
	doc.Events = append(doc.Events, doubleTapEvent{CallNumber: doc.CallsUsed + 1, Wave: -1, Role: "stopped", ModelLabel: "AgentGO", StartedAt: now, FinishedAt: now, Summary: "DoubleTap stopped before final answer.", Notes: reason})
	if err := writeDoubleTapDocument(path, doc); err != nil {
		a.logf("system", "warn", "DoubleTap stop state could not be written for project %s: %v", projectName, err)
	}
}

func (a *App) stopDoubleTapRun(projectName, executionID string, doc doubleTapDocument, reason string) {
	if strings.TrimSpace(reason) == "" {
		reason = "DoubleTap stopped."
	}
	if strings.TrimSpace(doc.RunID) != "" {
		path, pathErr := a.doubleTapPath(projectName)
		if pathErr == nil {
			if !strings.EqualFold(strings.TrimSpace(doc.Status), "stopped") && !strings.EqualFold(strings.TrimSpace(doc.Status), "complete") {
				now := time.Now().UTC().Format(time.RFC3339)
				doc.Status = "stopped"
				doc.Events = append(doc.Events, doubleTapEvent{CallNumber: doc.CallsUsed + 1, Wave: -1, Role: "stopped", ModelLabel: "AgentGO", StartedAt: now, FinishedAt: now, Summary: "DoubleTap stopped before final answer.", Notes: reason})
				_ = writeDoubleTapDocument(path, doc)
			}
		} else {
			a.recordDoubleTapStopped(projectName, executionID, reason)
		}
	} else {
		a.recordDoubleTapStopped(projectName, executionID, reason)
	}
	state, _ := a.currentWaveExecution(projectName)
	a.mu.Lock()
	a.clearWaveExecutionLocked(projectName)
	if strings.TrimSpace(state.ExecutionID) == "" {
		state = waveExecutionState{ProjectName: projectName, ExecutionID: executionID, CurrentPromptSource: "doubletap"}
	}
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, state.CurrentWave, "stopped", "DoubleTap Stopped", "doubletap", 0))
	a.mu.Unlock()
	a.logf("system", "warn", "DoubleTap stopped for project %s: %s", projectName, reason)
}

func (a *App) startDoubleTapExecutionForCurrentConfig(projectName string, req executeRequest, source executionSourceInfo, builders []ModelConfig, skipped []string) (executeResponse, error, int) {
	if source.TriggerType != "manual" {
		return executeResponse{}, errors.New("DoubleTap can only run from manual Execute Prompt for now."), http.StatusBadRequest
	}
	rootPrompt := strings.TrimSpace(req.Prompt)
	if rootPrompt == "" {
		rootPrompt = strings.TrimSpace(normalizeWavePromptMap(req.WavePrompts)[0])
	}
	if rootPrompt == "" {
		for _, prompt := range normalizeWavePromptMap(req.WavePrompts) {
			if trimmed := strings.TrimSpace(prompt); trimmed != "" {
				rootPrompt = trimmed
				break
			}
		}
	}
	if rootPrompt == "" {
		return executeResponse{}, errors.New("Prompt is required before executing with DoubleTap."), http.StatusBadRequest
	}
	if req.CypherEnabled || req.WireTapEnabled {
		return executeResponse{}, errors.New("Only one deep mode can be active. Disable Cypher or WireTap before running DoubleTap."), http.StatusBadRequest
	}
	if len(req.ContextFiles) > 0 || len(req.TemporaryAttachments) > 0 || waveContextFileMapHasFiles(normalizeWaveContextFileMap(req.WaveContextFiles)) {
		return executeResponse{}, errors.New("DoubleTap cannot run with selected context files or temporary attachments."), http.StatusBadRequest
	}
	if req.LoopCount > 0 {
		return executeResponse{}, errors.New("DoubleTap disables normal Loops. Set Loops to 0 before running."), http.StatusBadRequest
	}
	a.mu.RLock()
	reviewerID := strings.TrimSpace(a.reviewerID)
	riskEnabled := a.riskModeEnabled
	a.mu.RUnlock()
	if reviewerID != "" {
		return executeResponse{}, errors.New("DoubleTap cannot run while an Observer/Reviewer is active."), http.StatusBadRequest
	}
	if riskEnabled {
		return executeResponse{}, errors.New("DoubleTap cannot run with Risk Mode. Disable Risk Mode before running DoubleTap."), http.StatusBadRequest
	}
	if _, err := uniformMediaGenerationKind(builders); err != nil {
		return executeResponse{}, err, http.StatusBadRequest
	}
	for _, model := range builders {
		if modelIsVideoGeneration(model) || modelIsMeshGeneration(model) {
			return executeResponse{}, errors.New("DoubleTap can only run with normal text Builder models."), http.StatusBadRequest
		}
	}
	waves := buildExecutionWaves(builders)
	if len(waves) == 0 {
		return executeResponse{}, errors.New("Activate at least one Builder AI before executing with DoubleTap."), http.StatusBadRequest
	}
	for _, wave := range waves {
		if len(wave.BuilderIDs) != 1 {
			return executeResponse{}, fmt.Errorf("DoubleTap allows only one active Builder AI per wave. Wave %d has %d active Builders.", wave.Number, len(wave.BuilderIDs)), http.StatusBadRequest
		}
	}
	count := normalizeDoubleTapCountServer(req.DoubleTapCount)
	schedule := buildDoubleTapSchedule(waves, count)
	if len(schedule) == 0 {
		return executeResponse{}, errors.New("DoubleTap could not build a call schedule."), http.StatusInternalServerError
	}
	modelByID := map[string]ModelConfig{}
	for _, model := range builders {
		modelByID[modelIDString(model.ID)] = model
	}
	for _, wave := range schedule {
		if len(wave.BuilderIDs) == 0 {
			return executeResponse{}, errors.New("DoubleTap schedule contains an empty wave."), http.StatusInternalServerError
		}
		if _, ok := modelByID[wave.BuilderIDs[0]]; !ok {
			return executeResponse{}, fmt.Errorf("DoubleTap could not find Builder for wave %d.", wave.Number), http.StatusInternalServerError
		}
	}
	executionID := fmt.Sprintf("%s-doubletap-%d", projectName, time.Now().UTC().UnixNano())
	state := waveExecutionState{ProjectName: projectName, ExecutionID: executionID, RootPrompt: rootPrompt, ContextFiles: []string{}, TemporaryAttachments: []temporaryAttachmentInput{}, WavePrompts: map[int]string{}, WaveContextFiles: map[int][]string{}, WaveMediaInputRoles: map[int]map[string]string{}, Waves: schedule, CurrentIndex: 0, CurrentWave: schedule[0].Number, CurrentPromptSource: "doubletap", CurrentContextFilesUsed: 0, LoopCount: 0, LoopsRemaining: 0, CycleNumber: 1, AwaitingMerge: false, StartedAt: time.Now().UTC().Format(time.RFC3339)}
	a.clearPendingMergeState(projectName)
	a.clearLastMergedFiles(projectName)
	a.mu.Lock()
	a.setWaveExecutionLocked(projectName, state)
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, schedule[0].Number, "running", withWaveProgress("DoubleTap Running", 0, len(schedule)), "doubletap", 0))
	a.mu.Unlock()
	started := []string{schedule[0].BuilderLabels[0]}
	queued := make([]string, 0, len(schedule)-1)
	for _, wave := range schedule[1:] {
		if len(wave.BuilderLabels) > 0 {
			queued = append(queued, fmt.Sprintf("%s (Wave %d)", wave.BuilderLabels[0], wave.Number))
		}
	}
	a.logf("system", "info", "DoubleTap started for project %s. count=%d first_wave=%d calls=%d", projectName, count, schedule[0].Number, len(schedule))
	go a.runDoubleTapExecution(projectName, executionID, rootPrompt, count, schedule, modelByID)
	return executeResponse{Started: started, Skipped: skipped, WaveStarted: schedule[0].Number, TotalWaves: len(schedule), RemainingWaves: remainingWaveNumbers(schedule, 1), QueuedBuilders: queued, ContextFilesUsed: 0}, nil, http.StatusOK
}

func waveContextFileMapHasFiles(values map[int][]string) bool {
	for _, list := range values {
		if len(list) > 0 {
			return true
		}
	}
	return false
}

func buildDoubleTapSchedule(waves []executionWave, count int) []executionWave {
	if len(waves) == 0 || count <= 0 {
		return nil
	}
	schedule := make([]executionWave, 0, count)
	for i := 0; i < count; i++ {
		if i == count-1 {
			schedule = append(schedule, cloneExecutionWave(waves[0]))
			continue
		}
		schedule = append(schedule, cloneExecutionWave(waves[i%len(waves)]))
	}
	return schedule
}

func cloneExecutionWave(wave executionWave) executionWave {
	return executionWave{Number: wave.Number, BuilderIDs: append([]string(nil), wave.BuilderIDs...), BuilderLabels: append([]string(nil), wave.BuilderLabels...)}
}

func (a *App) runDoubleTapExecution(projectName, executionID, userPrompt string, count int, schedule []executionWave, modelByID map[string]ModelConfig) {
	runID := strings.TrimSpace(executionID)
	now := time.Now().UTC().Format(time.RFC3339)
	doc := doubleTapDocument{AgentGOFile: agentGOToolDoubleTap, FileVersion: agentGOToolVersion, Project: projectName, RunID: runID, OriginalPrompt: strings.TrimSpace(userPrompt), CountTotal: count, CallsUsed: 0, Status: "running", StartedAt: now, UpdatedAt: now, Events: []doubleTapEvent{}}
	doubleTapPath, err := a.doubleTapPath(projectName)
	if err != nil {
		a.failDoubleTapRun(projectName, executionID, doc, err)
		return
	}
	if err := writeDoubleTapDocument(doubleTapPath, doc); err != nil {
		a.failDoubleTapRun(projectName, executionID, doc, fmt.Errorf("could not write DoubleTap.json: %w", err))
		return
	}
	singleWorker := len(modelByID) == 1
	for i, wave := range schedule {
		if !a.isWaveExecutionCurrent(projectName, executionID) {
			a.stopDoubleTapRun(projectName, executionID, doc, "Emergency Stop triggered or DoubleTap execution was replaced.")
			return
		}
		modelID := ""
		if len(wave.BuilderIDs) > 0 {
			modelID = wave.BuilderIDs[0]
		}
		model, ok := modelByID[modelID]
		if !ok {
			a.failDoubleTapRun(projectName, executionID, doc, fmt.Errorf("DoubleTap Builder not found for wave %d", wave.Number))
			return
		}
		role := doubleTapRoleForCall(wave.Number, i, singleWorker)
		finalCall := i == len(schedule)-1
		if finalCall {
			role = "final_answer"
		}
		liveState := waveExecutionState{ProjectName: projectName, ExecutionID: executionID, RootPrompt: userPrompt, ContextFiles: []string{}, TemporaryAttachments: []temporaryAttachmentInput{}, WavePrompts: map[int]string{}, WaveContextFiles: map[int][]string{}, WaveMediaInputRoles: map[int]map[string]string{}, Waves: schedule, CurrentIndex: i, CurrentWave: wave.Number, CurrentPromptSource: "doubletap", CurrentContextFilesUsed: 0, LoopCount: 0, LoopsRemaining: 0, CycleNumber: 1, AwaitingMerge: false, StartedAt: doc.StartedAt}
		detail := "DoubleTap Thinking"
		if role == "critic_refiner" {
			detail = "DoubleTap Critiquing"
		}
		if finalCall {
			detail = "DoubleTap Answering"
		}
		a.mu.Lock()
		a.setWaveExecutionLocked(projectName, liveState)
		a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, liveState, wave.Number, "running", withWaveProgress(detail, i, len(schedule)), "doubletap", 0))
		a.mu.Unlock()
		a.logf(modelID, "info", "DoubleTap call %d/%d: Wave %d / %s / %s started for project %s", i+1, len(schedule), wave.Number, role, model.Label, projectName)
		started := time.Now().UTC()
		event, callErr := a.runDoubleTapModelCall(projectName, executionID, model, userPrompt, doc, i+1, wave.Number, role, finalCall)
		finished := time.Now().UTC()
		event.CallNumber = i + 1
		event.Wave = wave.Number
		event.Role = role
		event.ModelID = modelID
		event.ModelLabel = model.Label
		event.StartedAt = started.Format(time.RFC3339)
		event.FinishedAt = finished.Format(time.RFC3339)
		event.ElapsedSeconds = finished.Sub(started).Seconds()
		if callErr != nil {
			if strings.TrimSpace(event.StartedAt) != "" || strings.TrimSpace(event.FinishedAt) != "" || strings.TrimSpace(event.Summary) != "" || strings.TrimSpace(event.Notes) != "" {
				doc.Events = append(doc.Events, event)
				doc.CallsUsed = len(doc.Events)
			}
			if isDoubleTapStoppedError(callErr) {
				a.stopDoubleTapRun(projectName, executionID, doc, callErr.Error())
				return
			}
			doc.Status = "failed"
			_ = writeDoubleTapDocument(doubleTapPath, doc)
			a.failDoubleTapRun(projectName, executionID, doc, callErr)
			return
		}
		doc.Events = append(doc.Events, event)
		doc.CallsUsed = len(doc.Events)
		if finalCall {
			doc.FinalAnswer = strings.TrimSpace(event.FinalAnswer)
		}
		if err := writeDoubleTapDocument(doubleTapPath, doc); err != nil {
			a.failDoubleTapRun(projectName, executionID, doc, fmt.Errorf("could not update DoubleTap.json: %w", err))
			return
		}
		a.logf(modelID, "info", "DoubleTap call %d/%d: Wave %d / %s / %s completed in %.1fs", i+1, len(schedule), wave.Number, role, model.Label, event.ElapsedSeconds)
	}
	doc.Status = "complete"
	archivePath, archiveErr := a.writeDoubleTapArchive(projectName, doc)
	if archiveErr != nil {
		a.failDoubleTapRun(projectName, executionID, doc, fmt.Errorf("DoubleTap completed, but archive write failed: %w", archiveErr))
		return
	}
	doc.ArchivePath = filepath.ToSlash(archivePath)
	_ = writeDoubleTapDocument(doubleTapPath, doc)
	finalState := waveExecutionState{ProjectName: projectName, ExecutionID: executionID, RootPrompt: userPrompt, Waves: schedule, CurrentIndex: len(schedule) - 1, CurrentWave: schedule[len(schedule)-1].Number, CurrentPromptSource: "doubletap", StartedAt: doc.StartedAt}
	a.mu.Lock()
	a.clearWaveExecutionLocked(projectName)
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, finalState, finalState.CurrentWave, "complete", withWaveProgress("DoubleTap Complete", len(schedule)-1, len(schedule)), "doubletap", 0))
	a.mu.Unlock()
	a.logf("system", "info", "DoubleTap complete for project %s. Archive: %s", projectName, filepath.ToSlash(archivePath))
	if strings.TrimSpace(doc.FinalAnswer) != "" {
		a.logf("system", "info", "DoubleTap final answer:\n%s", strings.TrimSpace(doc.FinalAnswer))
	}
}

func doubleTapRoleForCall(waveNumber, callIndex int, singleWorker bool) string {
	if singleWorker {
		if callIndex%2 == 0 {
			return "thinker"
		}
		return "critic_refiner"
	}
	if normalizeRunOrder(waveNumber)%2 == 0 {
		return "thinker"
	}
	return "critic_refiner"
}

func (a *App) failDoubleTapRun(projectName, executionID string, doc doubleTapDocument, err error) {
	if err == nil {
		err = errors.New("DoubleTap failed")
	}
	if strings.TrimSpace(doc.RunID) != "" {
		doc.Status = "failed"
		if path, pathErr := a.doubleTapPath(projectName); pathErr == nil {
			_ = writeDoubleTapDocument(path, doc)
		}
	}
	state, _ := a.currentWaveExecution(projectName)
	a.mu.Lock()
	a.clearWaveExecutionLocked(projectName)
	if strings.TrimSpace(state.ExecutionID) == "" {
		state = waveExecutionState{ProjectName: projectName, ExecutionID: executionID, CurrentPromptSource: "doubletap"}
	}
	a.setWaveStatusLocked(projectName, waveStatusFromExecution(projectName, state, state.CurrentWave, "error", "DoubleTap Failed", "doubletap", 0))
	a.mu.Unlock()
	a.logf("system", "error", "DoubleTap failed for project %s: %v", projectName, err)
}

func (a *App) runDoubleTapModelCall(projectName, executionID string, model ModelConfig, userPrompt string, doc doubleTapDocument, callNumber, waveNumber int, role string, finalCall bool) (doubleTapEvent, error) {
	modelID := modelIDString(model.ID)
	event := doubleTapEvent{}
	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	a.setActiveCancelLocked(modelID, projectName, executionID, cancel)
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.clearActiveCancelLocked(modelID, executionID)
		a.mu.Unlock()
	}()
	docJSON, _ := json.MarshalIndent(doc, "", "  ")
	payload, err := buildDoubleTapRequestPayload(a.cfg, model, userPrompt, string(docJSON), role, finalCall)
	if err != nil {
		return event, err
	}
	baseEntry := diagnosticsEntry{Mode: "doubletap", Target: model.Label, ModelID: modelID, ModelLabel: model.Label, Project: projectName, Prompt: strings.TrimSpace(userPrompt), WaveNumber: waveNumber, PromptSource: "doubletap", ContextFilesUsed: 0, StatusMessage: fmt.Sprintf("DoubleTap call %d as %s", callNumber, role), SystemPrompt: strings.TrimSpace(payload.Instructions)}
	a.publishDiagnostics(baseEntry.withStage("Assembled"))
	a.publishDiagnostics(baseEntry.withStage("Sent"))
	resp, err := a.executeAdapterResponse(ctx, model, payload)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return event, fmt.Errorf("DoubleTap request canceled: %w", context.Canceled)
		}
		a.publishDiagnostics(baseEntry.withStage("Failed").withReason(err.Error()))
		return event, err
	}
	responsePreview := formatBuilderDiagnosticsResponse(resp)
	a.publishDiagnostics(baseEntry.withStage("Response Received").withResponse(responsePreview))
	if !a.isWaveExecutionCurrent(projectName, executionID) {
		return event, errors.New("stale DoubleTap execution")
	}
	parsed, parseErr := parseDoubleTapAIResponse(strings.TrimSpace(resp.Text), finalCall)
	if parseErr != nil {
		a.publishDiagnostics(baseEntry.withStage("Failed").withResponse(responsePreview).withReason(parseErr.Error()))
		return event, fmt.Errorf("invalid DoubleTap response: %w", parseErr)
	}
	if finalCall {
		event.FinalAnswer = strings.TrimSpace(parsed.FinalAnswer)
		event.Summary = "Final answer generated."
	} else {
		event.Summary = strings.TrimSpace(parsed.Memo.Summary)
		event.SupportedClaims = normalizeStringSlice(parsed.Memo.SupportedClaims)
		event.InferredClaims = normalizeStringSlice(parsed.Memo.InferredClaims)
		event.SpeculativeClaims = normalizeStringSlice(parsed.Memo.SpeculativeClaims)
		event.QuestionableClaims = normalizeStringSlice(parsed.Memo.QuestionableClaims)
		event.RemovedClaims = normalizeStringSlice(parsed.Memo.RemovedClaims)
		event.SourceReferences = normalizeStringSlice(parsed.Memo.SourceReferences)
		event.RecommendedDirection = strings.TrimSpace(parsed.Memo.RecommendedDirection)
		event.NextQuestions = normalizeStringSlice(parsed.Memo.NextQuestions)
	}
	event.Notes = strings.TrimSpace(parsed.Notes)
	a.publishDiagnostics(baseEntry.withStage("Parsed").withResponse(responsePreview).withStatusMessage("DoubleTap response parsed."))
	return event, nil
}

func buildDoubleTapRequestPayload(cfg AppConfig, model ModelConfig, userPrompt, doubleTapJSON, role string, finalCall bool) (adapterRequestPayload, error) {
	baseInstructions, err := loadBuilderSystemPrompt(cfg, model.PromptMode)
	if err != nil {
		return adapterRequestPayload{}, err
	}
	baseInstructions = appendModelUggProtocol(baseInstructions, model)
	instructions := strings.TrimSpace(baseInstructions + "\n\n" + doubleTapInstructions(role, finalCall))
	messages := []adapters.Message{
		buildTextMessage("user", "ORIGINAL USER PROMPT:\n"+strings.TrimSpace(userPrompt)),
		buildTextMessage("user", "CURRENT AGENTGO DoubleTap.json:\n```json\n"+strings.TrimSpace(doubleTapJSON)+"\n```"),
	}
	return adapterRequestPayload{Instructions: instructions, Messages: messages, ExpectJSON: true, JSONSchema: doubleTapJSONSchema(finalCall)}, nil
}

func doubleTapInstructions(role string, finalCall bool) string {
	if finalCall {
		return `AGENTGO DOUBLETAP FINAL ANSWER MODE
You are the final-answer Builder. Use the original user prompt and the current DoubleTap.json reasoning memos to produce the strongest direct answer you can.
Do not expose hidden chain-of-thought. You may cite or summarize useful memo points, confidence tags, questionable/removed claims, and caveats when they matter.
Trust supported claims most; use inferred claims carefully; caveat speculative claims; ignore removed claims; do not rely on questionable claims unless you explicitly explain the uncertainty.
Return JSON only, with final_answer containing the complete user-facing answer.`
	}
	if role == "critic_refiner" {
		return `AGENTGO DOUBLETAP CRITIC / REFINER MODE
Do not answer the user yet. Review the original prompt and the current DoubleTap.json memos.
Think about the thinking: find weak assumptions, hallucinated claims, missing evidence, contradictions, overreach, and better framing. If no questionable issues remain, add a refined memo that strengthens the best answer direction.
Return a concise structured memo only. Do not reveal raw hidden chain-of-thought. Use compressed reasoning summaries and claim/evidence status tags.
Use supported_claims for well-grounded points, inferred_claims for reasonable implications, speculative_claims for uncertain ideas, questionable_claims for claims that need caution, and removed_claims for claims that should be ignored by the final-answer Builder.
Return JSON only.`
	}
	return `AGENTGO DOUBLETAP THINKER MODE
Do not answer the user yet. Analyze how to answer the original prompt and write a compact reasoning memo for the next AI Builder.
If prior memos include questionable claims, revisit them before adding new thinking: keep, revise, or remove them. Include source references only when they are available from the prompt or generally reliable model knowledge; mark unverifiable items as inferred or speculative.
Do not reveal raw hidden chain-of-thought. Return only a useful compressed memo: assumptions, strongest approaches, risks, missing evidence, and recommended answer direction.
Use supported_claims for well-grounded points, inferred_claims for reasonable implications, speculative_claims for uncertain ideas, questionable_claims for claims that need caution, and removed_claims for claims that should be ignored by the final-answer Builder.
Return JSON only.`
}

func doubleTapJSONSchema(finalCall bool) map[string]any {
	if finalCall {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"agentgo_tool": map[string]any{"type": "string"},
				"tool_version": map[string]any{"type": "integer"},
				"final_answer": map[string]any{"type": "string"},
				"notes":        map[string]any{"type": "string"},
			},
			"required":             []string{"final_answer"},
			"additionalProperties": false,
		}
	}
	memoProps := map[string]any{
		"summary":               map[string]any{"type": "string"},
		"supported_claims":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"inferred_claims":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"speculative_claims":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"questionable_claims":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"removed_claims":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"source_references":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"recommended_direction": map[string]any{"type": "string"},
		"next_questions":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"agentgo_tool": map[string]any{"type": "string"},
			"tool_version": map[string]any{"type": "integer"},
			"memo":         map[string]any{"type": "object", "properties": memoProps, "required": []string{"summary"}, "additionalProperties": false},
			"notes":        map[string]any{"type": "string"},
		},
		"required":             []string{"memo"},
		"additionalProperties": false,
	}
}

func parseDoubleTapAIResponse(raw string, finalCall bool) (doubleTapAIResponse, error) {
	var parsed doubleTapAIResponse
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsed, errors.New("empty response")
	}
	jsonText, err := cypherJSONTextFromModelResponse(raw)
	if err != nil {
		if finalCall {
			parsed.FinalAnswer = raw
			return parsed, nil
		}
		return parsed, err
	}
	if err := json.Unmarshal([]byte(jsonText), &parsed); err != nil {
		return parsed, err
	}
	if finalCall {
		if strings.TrimSpace(parsed.FinalAnswer) == "" {
			return parsed, errors.New("final_answer is required")
		}
		return parsed, nil
	}
	if strings.TrimSpace(parsed.Memo.Summary) == "" {
		return parsed, errors.New("memo.summary is required")
	}
	return parsed, nil
}

func normalizeStringSlice(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		key := strings.ToLower(clean)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, clean)
	}
	return out
}

func (a *App) writeDoubleTapArchive(projectName string, doc doubleTapDocument) (string, error) {
	dir, err := a.doubleTapArchiveDir(projectName)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	timestamp := time.Now().UTC().Format("20060102_150405")
	archivePath := filepath.Join(dir, "DoubleTap_"+timestamp+".md")
	content := renderDoubleTapMarkdown(doc)
	if err := atomicWriteFile(archivePath, []byte(content), 0o644); err != nil {
		return "", err
	}
	return archivePath, nil
}

func renderDoubleTapMarkdown(doc doubleTapDocument) string {
	var b strings.Builder
	events := append([]doubleTapEvent(nil), doc.Events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].CallNumber < events[j].CallNumber })

	b.WriteString("# DoubleTap Run\n\n")
	b.WriteString(fmt.Sprintf("- **Project:** `%s`\n", markdownInline(doc.Project)))
	b.WriteString(fmt.Sprintf("- **Run ID:** `%s`\n", markdownInline(doc.RunID)))
	b.WriteString(fmt.Sprintf("- **Status:** %s\n", markdownInline(doc.Status)))
	b.WriteString(fmt.Sprintf("- **Started:** %s\n", markdownInline(doc.StartedAt)))
	b.WriteString(fmt.Sprintf("- **Updated:** %s\n", markdownInline(doc.UpdatedAt)))
	b.WriteString(fmt.Sprintf("- **Builder calls used:** %d / %d\n\n", doc.CallsUsed, doc.CountTotal))

	b.WriteString("## User Prompt\n\n")
	b.WriteString(markdownBlock(doc.OriginalPrompt))
	b.WriteString("\n")

	b.WriteString("## Run Timeline\n\n")
	b.WriteString("| Call | Wave | Role | Model | Time | Summary |\n")
	b.WriteString("|---:|---:|---|---|---:|---|\n")
	if len(events) == 0 {
		b.WriteString("| — | — | — | — | — | No DoubleTap events recorded. |\n")
	} else {
		for _, event := range events {
			wave := fmt.Sprintf("%d", event.Wave)
			if event.Wave < 0 {
				wave = "—"
			}
			call := fmt.Sprintf("%d", event.CallNumber)
			if event.CallNumber <= 0 {
				call = "—"
			}
			model := event.ModelLabel
			if strings.TrimSpace(model) == "" {
				model = event.ModelID
			}
			b.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s | %s |\n",
				markdownTableCell(call),
				markdownTableCell(wave),
				markdownTableCell(event.Role),
				markdownTableCell(model),
				markdownTableCell(formatDoubleTapDuration(event.ElapsedSeconds)),
				markdownTableCell(firstDoubleTapNonEmpty(event.Summary, event.Notes, event.FinalAnswer)),
			))
		}
	}
	b.WriteString("\n")

	b.WriteString("## Thinking Memos and Critiques\n\n")
	memoCount := 0
	for _, event := range events {
		if event.Role == "final_answer" || event.Role == "stopped" {
			continue
		}
		memoCount++
		b.WriteString(fmt.Sprintf("### Call %d — Wave %d — %s — %s\n\n", event.CallNumber, event.Wave, markdownInline(event.ModelLabel), markdownInline(event.Role)))
		b.WriteString(fmt.Sprintf("- **Elapsed:** %s\n", formatDoubleTapDuration(event.ElapsedSeconds)))
		if strings.TrimSpace(event.Summary) != "" {
			b.WriteString("\n**Summary**\n\n")
			b.WriteString(markdownBlock(event.Summary))
			b.WriteString("\n")
		}
		writeMarkdownList(&b, "Supported Claims", event.SupportedClaims)
		writeMarkdownList(&b, "Inferred Claims", event.InferredClaims)
		writeMarkdownList(&b, "Speculative Claims", event.SpeculativeClaims)
		writeMarkdownList(&b, "Questionable Claims", event.QuestionableClaims)
		writeMarkdownList(&b, "Removed Claims", event.RemovedClaims)
		writeMarkdownList(&b, "Source References", event.SourceReferences)
		if strings.TrimSpace(event.RecommendedDirection) != "" {
			b.WriteString("**Recommended Direction**\n\n")
			b.WriteString(markdownBlock(event.RecommendedDirection))
			b.WriteString("\n")
		}
		writeMarkdownList(&b, "Next Questions", event.NextQuestions)
		if strings.TrimSpace(event.Notes) != "" {
			b.WriteString("**Notes**\n\n")
			b.WriteString(markdownBlock(event.Notes))
			b.WriteString("\n")
		}
	}
	if memoCount == 0 {
		b.WriteString("_No pre-final thinking memo was recorded._\n\n")
	}

	if stopNote := doubleTapStopNote(events); stopNote != "" {
		b.WriteString("## Stop Note\n\n")
		b.WriteString(markdownBlock(stopNote))
		b.WriteString("\n")
	}

	b.WriteString("## Final Answer\n\n")
	if strings.TrimSpace(doc.FinalAnswer) != "" {
		b.WriteString(strings.TrimSpace(doc.FinalAnswer))
		b.WriteString("\n")
	} else {
		b.WriteString("_No final answer was recorded._\n")
	}
	return b.String()
}

func formatDoubleTapDuration(seconds float64) string {
	if seconds <= 0 {
		return "—"
	}
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	mins := int(seconds) / 60
	secs := int(seconds) % 60
	return fmt.Sprintf("%dm %02ds", mins, secs)
}

func firstDoubleTapNonEmpty(values ...string) string {
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean != "" {
			if len(clean) > 140 {
				return clean[:140] + "…"
			}
			return clean
		}
	}
	return "—"
}

func markdownTableCell(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return "—"
	}
	clean = strings.ReplaceAll(clean, "|", "\\|")
	clean = strings.ReplaceAll(clean, "\r\n", " ")
	clean = strings.ReplaceAll(clean, "\n", " ")
	return clean
}

func doubleTapStopNote(events []doubleTapEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Role != "stopped" {
			continue
		}
		return firstDoubleTapNonEmpty(events[i].Notes, events[i].Summary)
	}
	return ""
}

func writeMarkdownList(b *strings.Builder, title string, items []string) {
	items = normalizeStringSlice(items)
	if len(items) == 0 {
		return
	}
	b.WriteString("**" + title + "**\n\n")
	for _, item := range items {
		b.WriteString("- " + strings.ReplaceAll(strings.TrimSpace(item), "\n", " ") + "\n")
	}
	b.WriteString("\n")
}

func markdownInline(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "`", "'")
}

func markdownBlock(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return ""
	}
	return clean + "\n"
}
