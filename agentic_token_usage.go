package main

import (
	"fmt"
	"strings"
	"time"

	"agentgo/adapters"
)

// agenticTaskTokenUsage is persisted with one staged task so usage survives
// refreshes, recovery, Stop, and the final review/audit export.
type agenticTaskTokenUsage struct {
	InputTokens     int    `json:"inputTokens"`
	OutputTokens    int    `json:"outputTokens"`
	TotalTokens     int    `json:"totalTokens"`
	BuilderCalls    int    `json:"builderCalls"`
	ReportedCalls   int    `json:"reportedCalls,omitempty"`
	EstimatedCalls  int    `json:"estimatedCalls,omitempty"`
	InputEstimated  bool   `json:"inputEstimated,omitempty"`
	OutputEstimated bool   `json:"outputEstimated,omitempty"`
	Estimated       bool   `json:"estimated,omitempty"`
	UpdatedAt       string `json:"updatedAt,omitempty"`
}

type agenticBuilderCallUsage struct {
	InputTokens     int
	OutputTokens    int
	InputEstimated  bool
	OutputEstimated bool
}

func normalizeAgenticTaskTokenUsage(usage agenticTaskTokenUsage) agenticTaskTokenUsage {
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}
	if usage.BuilderCalls < 0 {
		usage.BuilderCalls = 0
	}
	if usage.ReportedCalls < 0 {
		usage.ReportedCalls = 0
	}
	if usage.EstimatedCalls < 0 {
		usage.EstimatedCalls = 0
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	usage.Estimated = usage.InputEstimated || usage.OutputEstimated || usage.EstimatedCalls > 0
	return usage
}

func resolveAgenticBuilderCallUsage(payload adapterRequestPayload, response *adapters.Response) agenticBuilderCallUsage {
	call := agenticBuilderCallUsage{
		InputTokens:     estimateAdapterPayloadTokens(payload),
		InputEstimated:  true,
		OutputEstimated: true,
	}
	if response == nil {
		return call
	}
	call.OutputTokens = estimateTextTokens(response.Text)
	if response.TokenUsage.InputReported {
		call.InputTokens = response.TokenUsage.InputTokens
		call.InputEstimated = false
	}
	if response.TokenUsage.OutputReported {
		call.OutputTokens = response.TokenUsage.OutputTokens
		call.OutputEstimated = false
	}
	return call
}

func (a *App) recordAgenticBuilderCallUsage(projectName, taskID string, payload adapterRequestPayload, response *adapters.Response) (agenticTaskTokenUsage, error) {
	task, _, err := a.loadAgenticWorkspaceTask(projectName, taskID)
	if err != nil {
		return agenticTaskTokenUsage{}, err
	}
	taskRoot, err := a.agenticWorkspaceTaskRoot(projectName, taskID)
	if err != nil {
		return agenticTaskTokenUsage{}, err
	}
	call := resolveAgenticBuilderCallUsage(payload, response)
	usage := normalizeAgenticTaskTokenUsage(task.TokenUsage)
	usage.InputTokens += call.InputTokens
	usage.OutputTokens += call.OutputTokens
	usage.BuilderCalls++
	if call.InputEstimated {
		usage.InputEstimated = true
	}
	if call.OutputEstimated {
		usage.OutputEstimated = true
	}
	if call.InputEstimated || call.OutputEstimated {
		usage.EstimatedCalls++
	} else {
		usage.ReportedCalls++
	}
	usage.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	usage = normalizeAgenticTaskTokenUsage(usage)
	task.TokenUsage = usage
	if err := saveAgenticWorkspaceTask(taskRoot, task); err != nil {
		return agenticTaskTokenUsage{}, err
	}

	copyUsage := usage
	message := fmt.Sprintf("Builder call %d token usage recorded: input %d, output %d, total %d.", usage.BuilderCalls, call.InputTokens, call.OutputTokens, call.InputTokens+call.OutputTokens)
	if call.InputEstimated || call.OutputEstimated {
		parts := []string{}
		if call.InputEstimated {
			parts = append(parts, "input")
		}
		if call.OutputEstimated {
			parts = append(parts, "output")
		}
		message += " Estimated component(s): " + strings.Join(parts, ", ") + "."
	}
	_, _ = a.appendAgenticAuditRecord(projectName, taskID, agenticAuditRecord{
		Kind:       agenticAuditKindTokenUsage,
		Status:     "updated",
		Message:    message,
		TokenUsage: &copyUsage,
	})
	return usage, nil
}
