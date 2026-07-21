package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgenticAuditPersistsRunNumberAndRedactsSecrets(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, err := app.projectWorkRoot("Demo")
	if err != nil {
		t.Fatal(err)
	}
	writeAgenticWorkspaceTestFile(t, canonical, "base.txt", []byte("base"))
	if err := app.ensureProjectTerminalConfig("Demo"); err != nil {
		t.Fatal(err)
	}
	if err := app.saveTerminalEnvironment("Demo", terminalEnvironmentFile{Variables: []terminalEnvironment{{Name: "TOKEN", Value: "audit-secret-value"}}}); err != nil {
		t.Fatal(err)
	}
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	command := workModeAgenticCommand{Type: workModeAgenticCommandShell, Script: "echo audit-secret-value", WorkingDirectory: ".", Purpose: "Use audit-secret-value"}
	record, err := app.appendAgenticAuditRecord("Demo", task.SessionID, agenticAuditRecord{Kind: agenticAuditKindAIRequest, Status: workModeAgenticStatusRunCommand, Message: "audit-secret-value", Command: &command})
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != 1 || record.RunNumber != 1 || strings.Contains(record.Message, "audit-secret-value") || !strings.Contains(record.Message, "[REDACTED]") {
		t.Fatalf("record=%+v", record)
	}
	if record.Command == nil || strings.Contains(record.Command.Script, "audit-secret-value") || strings.Contains(record.Command.Purpose, "audit-secret-value") {
		t.Fatalf("redacted command=%+v", record.Command)
	}
	taskRoot, _ := app.agenticWorkspaceTaskRoot("Demo", task.SessionID)
	records, err := readAgenticAuditRecords(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != 1 || records[0].RunNumber != 1 {
		t.Fatalf("records=%+v", records)
	}
	if _, err := app.reactivateAgenticWorkspaceTask("Demo", task.SessionID); err != nil {
		t.Fatal(err)
	}
	record, err = app.appendAgenticAuditRecord("Demo", task.SessionID, agenticAuditRecord{Kind: agenticAuditKindWorkspace, Status: "turn_started"})
	if err != nil {
		t.Fatal(err)
	}
	if record.ID != 2 || record.RunNumber != 2 {
		t.Fatalf("continued record=%+v", record)
	}
}

func TestAgenticStreamingRedactsSecretAcrossWrites(t *testing.T) {
	collector := newAgenticOutputCollector(4096)
	events := []agenticExecutionLiveEvent{}
	writer := collector.writerWithEvents(agenticExecutionStreamStdout, []terminalEnvironment{{Name: "TOKEN", Value: "split-secret-value"}}, func(event agenticExecutionLiveEvent) {
		events = append(events, event)
	})
	_, _ = writer.Write([]byte("before split-"))
	_, _ = writer.Write([]byte("secret-value after"))
	writer.flush()
	var combined strings.Builder
	for _, event := range events {
		combined.WriteString(event.Text)
	}
	text := combined.String()
	if strings.Contains(text, "split-secret-value") || !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, "before ") || !strings.Contains(text, " after") {
		t.Fatalf("streamed=%q events=%+v", text, events)
	}
}

func TestAgenticLiveStreamIsBoundedAndCursorBased(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	for i := 0; i < agenticLiveStreamMaxEvents+100; i++ {
		app.appendAgenticLiveEvent("Demo", "task-1", agenticLiveEvent{Kind: "output", Stream: "stdout", Text: strings.Repeat("x", 1024)})
	}
	events, cursor, reset := app.agenticLiveEventsAfter("Demo", "task-1", 1)
	if cursor <= int64(agenticLiveStreamMaxEvents) || !reset {
		t.Fatalf("cursor=%d reset=%v", cursor, reset)
	}
	if len(events) > agenticLiveStreamMaxEvents {
		t.Fatalf("too many events retained: %d", len(events))
	}
	total := 0
	for _, event := range events {
		total += len(event.Text)
	}
	if total > agenticLiveStreamMaxBytes {
		t.Fatalf("retained bytes=%d", total)
	}
}

func TestAgenticAuditPollReturnsOnlyRequestedTask(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "base.txt", []byte("base"))
	taskOne, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	taskTwo, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = app.appendAgenticAuditRecord("Demo", taskOne.SessionID, agenticAuditRecord{Kind: agenticAuditKindWorkspace, Message: "task one"})
	_, _ = app.appendAgenticAuditRecord("Demo", taskTwo.SessionID, agenticAuditRecord{Kind: agenticAuditKindWorkspace, Message: "task two"})
	app.appendAgenticLiveEvent("Demo", taskOne.SessionID, agenticLiveEvent{Kind: "output", Stream: "stdout", Text: "one"})
	app.appendAgenticLiveEvent("Demo", taskTwo.SessionID, agenticLiveEvent{Kind: "output", Stream: "stdout", Text: "two"})
	req := httptest.NewRequest(http.MethodGet, "/api/work-mode/agentic-audit?taskId="+taskOne.SessionID+"&after=0&streamAfter=0", nil)
	rr := httptest.NewRecorder()
	app.handleAgenticAuditPoll(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var response agenticAuditPollResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.TaskID != taskOne.SessionID || len(response.Records) != 1 || response.Records[0].Message != "task one" || len(response.Events) != 1 || response.Events[0].Text != "one" {
		t.Fatalf("response=%+v", response)
	}
}

func TestDiscardReturnsAuditBeforeWorkspaceDeletion(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	canonical, _ := app.projectWorkRoot("Demo")
	writeAgenticWorkspaceTestFile(t, canonical, "base.txt", []byte("base"))
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = app.appendAgenticAuditRecord("Demo", task.SessionID, agenticAuditRecord{Kind: agenticAuditKindAIRequest, Message: "request"})
	review, err := app.discardAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !review.Deleted || len(review.AuditRecords) < 2 || review.AuditRecords[len(review.AuditRecords)-1].Status != "discarded" {
		t.Fatalf("review=%+v", review)
	}
	if _, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID); err == nil {
		t.Fatal("discarded task remained loadable")
	}
}
