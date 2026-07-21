package main

import (
	"testing"

	"agentgo/adapters"
)

func agenticTokenTestPayload(text string) adapterRequestPayload {
	return adapterRequestPayload{
		Instructions: "System instructions",
		Messages:     []adapters.Message{{Role: "user", Text: text}},
		ExpectJSON:   true,
	}
}

func TestAgenticTokenUsageUsesProviderReportedCountsAndPersists(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeFull, 50)
	if err != nil {
		t.Fatal(err)
	}
	response := adapters.Response{Text: "done", TokenUsage: adapters.TokenUsage{InputTokens: 1200, OutputTokens: 300, InputReported: true, OutputReported: true}}
	usage, err := app.recordAgenticBuilderCallUsage("Demo", task.SessionID, agenticTokenTestPayload("ignored estimate"), &response)
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 1200 || usage.OutputTokens != 300 || usage.TotalTokens != 1500 || usage.BuilderCalls != 1 || usage.ReportedCalls != 1 || usage.Estimated {
		t.Fatalf("usage=%+v", usage)
	}
	loaded, _, err := app.loadAgenticWorkspaceTask("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TokenUsage != usage {
		t.Fatalf("persisted=%+v want=%+v", loaded.TokenUsage, usage)
	}
	review, err := app.buildAgenticWorkspaceReview("Demo", task.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if review.TokenUsage.TotalTokens != 1500 || review.TokenUsage.BuilderCalls != 1 {
		t.Fatalf("review usage=%+v", review.TokenUsage)
	}
	taskRoot, _ := app.agenticWorkspaceTaskRoot("Demo", task.SessionID)
	records, err := readAgenticAuditRecords(taskRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Kind != agenticAuditKindTokenUsage || records[0].TokenUsage == nil || records[0].TokenUsage.TotalTokens != 1500 {
		t.Fatalf("audit records=%+v", records)
	}
}

func TestAgenticTokenUsageMixedReportedAndEstimatedCounts(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	task, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeSemi, 50)
	if err != nil {
		t.Fatal(err)
	}
	first := adapters.Response{Text: "short output", TokenUsage: adapters.TokenUsage{InputTokens: 900, InputReported: true}}
	usage, err := app.recordAgenticBuilderCallUsage("Demo", task.SessionID, agenticTokenTestPayload("first request"), &first)
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 900 || usage.OutputTokens != estimateTextTokens(first.Text) || !usage.Estimated || usage.ReportedCalls != 0 || usage.EstimatedCalls != 1 || usage.InputEstimated || !usage.OutputEstimated {
		t.Fatalf("first usage=%+v", usage)
	}
	second := adapters.Response{Text: "ignored", TokenUsage: adapters.TokenUsage{InputTokens: 100, OutputTokens: 50, InputReported: true, OutputReported: true}}
	usage, err = app.recordAgenticBuilderCallUsage("Demo", task.SessionID, agenticTokenTestPayload("second request"), &second)
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 1000 || usage.OutputTokens != estimateTextTokens(first.Text)+50 || usage.BuilderCalls != 2 || usage.ReportedCalls != 1 || usage.EstimatedCalls != 1 || !usage.Estimated {
		t.Fatalf("mixed usage=%+v", usage)
	}
}

func TestAgenticTokenUsageEstimatesFailedCallAndResetsForNewTask(t *testing.T) {
	app := newAgenticWorkspaceTestApp(t)
	firstTask, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	payload := agenticTokenTestPayload("A failed request still counts as one Builder call.")
	usage, err := app.recordAgenticBuilderCallUsage("Demo", firstTask.SessionID, payload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if usage.BuilderCalls != 1 || usage.InputTokens != estimateAdapterPayloadTokens(payload) || usage.OutputTokens != 0 || !usage.Estimated || usage.EstimatedCalls != 1 {
		t.Fatalf("failed-call usage=%+v", usage)
	}
	secondTask, _, err := app.startAgenticWorkspaceTask("Demo", workModeAgenticModeManual, 50)
	if err != nil {
		t.Fatal(err)
	}
	if secondTask.TokenUsage.TotalTokens != 0 || secondTask.TokenUsage.BuilderCalls != 0 || secondTask.TokenUsage.Estimated {
		t.Fatalf("new task did not reset usage: %+v", secondTask.TokenUsage)
	}
}
