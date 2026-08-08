package domain

import (
	"testing"
	"time"
)

func TestAgentRunValidationAndTerminalTransition(t *testing.T) {
	startedAt := time.UnixMilli(3).UTC()
	running := AgentRun{
		ID:                 "00000000-0000-7000-8000-000000000201",
		SessionID:          "00000000-0000-7000-8000-000000000202",
		RequestMessageID:   "00000000-0000-7000-8000-000000000203",
		Status:             AgentRunStatusRunning,
		Scope:              ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 1},
		PromptVersion:      "prompt-v1",
		ToolCatalogVersion: "tools-v1",
		StartedAt:          &startedAt,
	}
	if err := running.Validate(); err != nil {
		t.Fatalf("running Validate() error = %v", err)
	}

	finishedAt := startedAt.Add(time.Millisecond)
	completed := running
	completed.Status = AgentRunStatusCompleted
	completed.StepCount = 2
	completed.ToolCallCount = 1
	completed.ModelRequestCount = 2
	completed.FinishedAt = &finishedAt
	if err := completed.Validate(); err != nil {
		t.Fatalf("completed Validate() error = %v", err)
	}
	if err := ValidateAgentRunTransition(running, completed); err != nil {
		t.Fatalf("ValidateAgentRunTransition() error = %v", err)
	}

	changedScope := completed
	changedScope.Scope.Generation++
	if err := ValidateAgentRunTransition(running, changedScope); err == nil {
		t.Fatal("scope-changing transition error = nil")
	}
	resourceMismatch := running
	resourceMismatch.Resource = &ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "other-namespace", Name: "sample-pod"}
	if err := resourceMismatch.Validate(); err == nil {
		t.Fatal("scope-mismatched ResourceRef Validate() error = nil")
	}
	unsupportedResource := running
	unsupportedResource.Resource = &ResourceRef{APIVersion: "v1", Kind: "Secret", Namespace: "test-namespace", Name: "sample-secret"}
	if err := unsupportedResource.Validate(); err == nil {
		t.Fatal("unsupported ResourceRef Validate() error = nil")
	}
	decreased := completed
	decreased.StepCount = -1
	if err := decreased.Validate(); err == nil {
		t.Fatal("negative counter Validate() error = nil")
	}
	if AgentRunStatusCompleted.CanTransitionTo(AgentRunStatusRunning) {
		t.Fatal("terminal status admitted another transition")
	}

	queued := running
	queued.Status = AgentRunStatusQueued
	queued.StartedAt = nil
	if err := queued.Validate(); err != nil {
		t.Fatalf("queued Validate() error = %v", err)
	}
	if err := ValidateAgentRunTransition(queued, running); err != nil {
		t.Fatalf("queued-to-running transition error = %v", err)
	}
}

func TestAgentRunValidationEnforcesRuntimeCeilings(t *testing.T) {
	startedAt := time.UnixMilli(4).UTC()
	base := AgentRun{
		ID:                 "00000000-0000-7000-8000-000000000211",
		SessionID:          "00000000-0000-7000-8000-000000000212",
		RequestMessageID:   "00000000-0000-7000-8000-000000000213",
		Status:             AgentRunStatusRunning,
		Scope:              ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 1},
		PromptVersion:      "prompt-v1",
		ToolCatalogVersion: "tools-v1",
		StartedAt:          &startedAt,
		StepCount:          maxAgentSteps,
		ToolCallCount:      maxToolCalls,
		ModelRequestCount:  maxModelRequests,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("ceiling Validate() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*AgentRun)
	}{
		{name: "steps", mutate: func(value *AgentRun) { value.StepCount++ }},
		{name: "Tool calls", mutate: func(value *AgentRun) { value.ToolCallCount++ }},
		{name: "model requests", mutate: func(value *AgentRun) { value.ModelRequestCount++ }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := base
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}
