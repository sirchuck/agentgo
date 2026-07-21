package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	agenticAuditFileName       = "audit.jsonl"
	agenticLiveStreamMaxBytes  = 256 * 1024
	agenticLiveStreamMaxEvents = 600
)

const (
	agenticAuditKindAIRequest       = "ai_request"
	agenticAuditKindProtocolError   = "protocol_error"
	agenticAuditKindApproval        = "approval"
	agenticAuditKindExecutionStatus = "execution_status"
	agenticAuditKindExecutionResult = "execution_result"
	agenticAuditKindWorkspace       = "workspace"
	agenticAuditKindStop            = "stop"
	agenticAuditKindTokenUsage      = "token_usage"
)

type agenticAuditExecution struct {
	Status          string                 `json:"status"`
	Command         workModeAgenticCommand `json:"command"`
	StartedAt       string                 `json:"startedAt,omitempty"`
	FinishedAt      string                 `json:"finishedAt,omitempty"`
	DurationMillis  int64                  `json:"durationMillis,omitempty"`
	ExitCode        *int                   `json:"exitCode,omitempty"`
	OutputExcerpt   string                 `json:"outputExcerpt,omitempty"`
	OutputTruncated bool                   `json:"outputTruncated,omitempty"`
	Error           string                 `json:"error,omitempty"`
	BlockedPath     string                 `json:"blockedPath,omitempty"`
	TimedOut        bool                   `json:"timedOut,omitempty"`
	Cancelled       bool                   `json:"cancelled,omitempty"`
}

type agenticAuditWorkspace struct {
	Status        string `json:"status,omitempty"`
	Incomplete    bool   `json:"incomplete,omitempty"`
	AddedCount    int    `json:"addedCount,omitempty"`
	ModifiedCount int    `json:"modifiedCount,omitempty"`
	DeletedCount  int    `json:"deletedCount,omitempty"`
	BinaryCount   int    `json:"binaryCount,omitempty"`
	PendingCount  int    `json:"pendingCount,omitempty"`
	ConflictCount int    `json:"conflictCount,omitempty"`
	Resolved      bool   `json:"resolved,omitempty"`
	Deleted       bool   `json:"deleted,omitempty"`
}

type agenticAuditRecord struct {
	ID         int64                   `json:"id"`
	Timestamp  string                  `json:"timestamp"`
	RunNumber  int                     `json:"runNumber,omitempty"`
	Mode       string                  `json:"mode,omitempty"`
	Kind       string                  `json:"kind"`
	Level      string                  `json:"level,omitempty"`
	Status     string                  `json:"status,omitempty"`
	Message    string                  `json:"message,omitempty"`
	Decision   string                  `json:"decision,omitempty"`
	Command    *workModeAgenticCommand `json:"command,omitempty"`
	Execution  *agenticAuditExecution  `json:"execution,omitempty"`
	Workspace  *agenticAuditWorkspace  `json:"workspace,omitempty"`
	TokenUsage *agenticTaskTokenUsage  `json:"tokenUsage,omitempty"`
	Path       string                  `json:"path,omitempty"`
	Detail     string                  `json:"detail,omitempty"`
}

type agenticLiveEvent struct {
	ID        int64  `json:"id"`
	Timestamp string `json:"timestamp"`
	Kind      string `json:"kind"`
	Stream    string `json:"stream,omitempty"`
	Status    string `json:"status,omitempty"`
	Text      string `json:"text,omitempty"`
}

type agenticLiveStream struct {
	NextID int64
	Bytes  int
	Events []agenticLiveEvent
}

type agenticAuditPollResponse struct {
	TaskID       string                  `json:"taskId"`
	RunNumber    int                     `json:"runNumber,omitempty"`
	Records      []agenticAuditRecord    `json:"records"`
	Events       []agenticLiveEvent      `json:"events"`
	RecordCursor int64                   `json:"recordCursor"`
	StreamCursor int64                   `json:"streamCursor"`
	StreamReset  bool                    `json:"streamReset,omitempty"`
	Workspace    *agenticWorkspaceReview `json:"workspace,omitempty"`
}

var agenticAuditFileMu sync.Mutex

func agenticAuditPath(taskRoot string) string {
	return filepath.Join(taskRoot, agenticAuditFileName)
}

func agenticAuditStreamKey(projectName, taskID string) string {
	return strings.TrimSpace(projectName) + "\x00" + strings.TrimSpace(taskID)
}

func auditExecutionSummary(result agenticExecutionResult) *agenticAuditExecution {
	return &agenticAuditExecution{
		Status: result.Status, Command: result.Command, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
		DurationMillis: result.DurationMillis, ExitCode: result.ExitCode, OutputExcerpt: result.AIOutputExcerpt,
		OutputTruncated: result.AIOutputTruncated || result.Stdout.Truncated || result.Stderr.Truncated,
		Error:           result.Error, BlockedPath: result.BlockedPath, TimedOut: result.TimedOut, Cancelled: result.Cancelled,
	}
}

func auditWorkspaceSummary(review agenticWorkspaceReview) *agenticAuditWorkspace {
	return &agenticAuditWorkspace{
		Status: review.Status, Incomplete: review.Incomplete, AddedCount: review.AddedCount,
		ModifiedCount: review.ModifiedCount, DeletedCount: review.DeletedCount, BinaryCount: review.BinaryCount,
		PendingCount: review.PendingCount, ConflictCount: review.ConflictCount, Resolved: review.Resolved, Deleted: review.Deleted,
	}
}

func readAgenticAuditRecordsUnlocked(taskRoot string) ([]agenticAuditRecord, error) {
	file, err := os.Open(agenticAuditPath(taskRoot))
	if errors.Is(err, os.ErrNotExist) {
		return []agenticAuditRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := json.NewDecoder(bufio.NewReader(file))
	out := []agenticAuditRecord{}
	for {
		var record agenticAuditRecord
		if err := decoder.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("invalid agentic audit trail: %w", err)
		}
		out = append(out, record)
	}
	return out, nil
}

func readAgenticAuditRecords(taskRoot string) ([]agenticAuditRecord, error) {
	agenticAuditFileMu.Lock()
	defer agenticAuditFileMu.Unlock()
	return readAgenticAuditRecordsUnlocked(taskRoot)
}

func (a *App) redactAgenticAuditRecord(projectName string, record agenticAuditRecord) agenticAuditRecord {
	configured := []terminalEnvironment{}
	if file, err := a.loadTerminalEnvironment(projectName); err == nil {
		configured = append(configured, file.Variables...)
	}
	record.Message = redactTerminalEnvironmentValues(record.Message, configured)
	record.Detail = redactTerminalEnvironmentValues(record.Detail, configured)
	record.Path = redactTerminalEnvironmentValues(record.Path, configured)
	if record.Command != nil {
		command := redactAgenticCommand(*record.Command, configured)
		record.Command = &command
	}
	if record.Execution != nil {
		execution := *record.Execution
		execution.Command = redactAgenticCommand(execution.Command, configured)
		execution.OutputExcerpt = redactTerminalEnvironmentValues(execution.OutputExcerpt, configured)
		execution.Error = redactTerminalEnvironmentValues(execution.Error, configured)
		execution.BlockedPath = redactTerminalEnvironmentValues(execution.BlockedPath, configured)
		record.Execution = &execution
	}
	return record
}

func (a *App) appendAgenticAuditRecord(projectName, taskID string, record agenticAuditRecord) (agenticAuditRecord, error) {
	agenticAuditFileMu.Lock()
	defer agenticAuditFileMu.Unlock()
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return agenticAuditRecord{}, err
	}
	task, err := loadAgenticWorkspaceTask(taskRoot)
	if err != nil {
		return agenticAuditRecord{}, err
	}
	if task.ProjectName != projectName {
		return agenticAuditRecord{}, errors.New("agentic workspace belongs to another project")
	}
	records, err := readAgenticAuditRecordsUnlocked(taskRoot)
	if err != nil {
		return agenticAuditRecord{}, err
	}
	record.ID = 1
	if len(records) > 0 {
		record.ID = records[len(records)-1].ID + 1
	}
	if record.Timestamp == "" {
		record.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if record.RunNumber == 0 {
		record.RunNumber = task.RunNumber
	}
	if record.Mode == "" {
		record.Mode = task.Mode
	}
	record = a.redactAgenticAuditRecord(projectName, record)
	file, err := os.OpenFile(agenticAuditPath(taskRoot), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return agenticAuditRecord{}, err
	}
	encoder := json.NewEncoder(file)
	writeErr := encoder.Encode(record)
	closeErr := file.Close()
	if writeErr != nil {
		return agenticAuditRecord{}, writeErr
	}
	if closeErr != nil {
		return agenticAuditRecord{}, closeErr
	}
	return record, os.Chmod(agenticAuditPath(taskRoot), 0o600)
}

func (a *App) appendAgenticLiveEvent(projectName, taskID string, event agenticLiveEvent) agenticLiveEvent {
	configured := []terminalEnvironment{}
	if file, err := a.loadTerminalEnvironment(projectName); err == nil {
		configured = append(configured, file.Variables...)
	}
	event.Text = redactTerminalEnvironmentValues(event.Text, configured)
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	key := agenticAuditStreamKey(projectName, taskID)
	a.agenticAuditMu.Lock()
	defer a.agenticAuditMu.Unlock()
	if a.agenticStreams == nil {
		a.agenticStreams = map[string]*agenticLiveStream{}
	}
	stream := a.agenticStreams[key]
	if stream == nil {
		stream = &agenticLiveStream{}
		a.agenticStreams[key] = stream
	}
	stream.NextID++
	event.ID = stream.NextID
	stream.Events = append(stream.Events, event)
	stream.Bytes += len(event.Text)
	for len(stream.Events) > agenticLiveStreamMaxEvents || stream.Bytes > agenticLiveStreamMaxBytes {
		if len(stream.Events) == 0 {
			break
		}
		stream.Bytes -= len(stream.Events[0].Text)
		stream.Events = stream.Events[1:]
	}
	return event
}

func (a *App) appendAgenticExecutionLiveEvent(projectName, taskID string, event agenticExecutionLiveEvent) {
	if event.Kind == "output" {
		const chunkSize = 8 * 1024
		text := event.Text
		for len(text) > 0 {
			take := chunkSize
			if len(text) < take {
				take = len(text)
			}
			a.appendAgenticLiveEvent(projectName, taskID, agenticLiveEvent{Timestamp: event.Timestamp, Kind: event.Kind, Stream: event.Stream, Status: event.Status, Text: text[:take]})
			text = text[take:]
		}
		return
	}
	live := agenticLiveEvent{Timestamp: event.Timestamp, Kind: event.Kind, Stream: event.Stream, Status: event.Status, Text: event.Text}
	a.appendAgenticLiveEvent(projectName, taskID, live)
	if event.Kind == "status" {
		_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{
			Kind: agenticAuditKindExecutionStatus, Status: event.Status, Message: event.Text,
			Level: func() string {
				if event.Status == agenticExecutionStatusFailed || event.Status == agenticExecutionStatusTimedOut || event.Status == agenticExecutionStatusCancelled || event.Status == agenticExecutionStatusBlocked || event.Status == agenticExecutionStatusStartError {
					return "error"
				}
				return "info"
			}(),
		})
	}
}

func (a *App) agenticLiveEventsAfter(projectName, taskID string, after int64) ([]agenticLiveEvent, int64, bool) {
	key := agenticAuditStreamKey(projectName, taskID)
	a.agenticAuditMu.Lock()
	defer a.agenticAuditMu.Unlock()
	stream := a.agenticStreams[key]
	if stream == nil {
		return []agenticLiveEvent{}, after, false
	}
	reset := len(stream.Events) > 0 && after > 0 && after < stream.Events[0].ID-1
	out := []agenticLiveEvent{}
	for _, event := range stream.Events {
		if event.ID > after {
			out = append(out, event)
		}
	}
	return out, stream.NextID, reset
}

func (a *App) clearAgenticLiveStream(projectName, taskID string) {
	a.agenticAuditMu.Lock()
	delete(a.agenticStreams, agenticAuditStreamKey(projectName, taskID))
	a.agenticAuditMu.Unlock()
}

func parseAgenticAuditCursor(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (a *App) handleAgenticAuditPoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	projectName, err := a.requireActiveProject()
	if err != nil {
		http.Error(w, "Select an active project first.", http.StatusBadRequest)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("taskId"))
	task, _, err := a.loadAgenticWorkspaceTask(projectName, taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	taskRoot, _ := a.agenticWorkspaceTaskRoot(projectName, taskID)
	records, err := readAgenticAuditRecords(taskRoot)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	after := parseAgenticAuditCursor(r.URL.Query().Get("after"))
	filtered := make([]agenticAuditRecord, 0, len(records))
	recordCursor := after
	for _, record := range records {
		if record.ID > after {
			filtered = append(filtered, record)
		}
		if record.ID > recordCursor {
			recordCursor = record.ID
		}
	}
	streamAfter := parseAgenticAuditCursor(r.URL.Query().Get("streamAfter"))
	events, streamCursor, streamReset := a.agenticLiveEventsAfter(projectName, taskID, streamAfter)
	review, reviewErr := a.buildAgenticWorkspaceReview(projectName, taskID)
	var reviewPtr *agenticWorkspaceReview
	if reviewErr == nil {
		reviewPtr = &review
	}
	writeJSON(w, http.StatusOK, agenticAuditPollResponse{TaskID: taskID, RunNumber: task.RunNumber, Records: filtered, Events: events, RecordCursor: recordCursor, StreamCursor: streamCursor, StreamReset: streamReset, Workspace: reviewPtr})
}
