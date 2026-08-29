package domain

import (
	"strings"
	"testing"
	"time"
)

func TestToolInvocationValidationCoversClosedCatalogLifecycleAndBounds(t *testing.T) {
	startedAt := time.UnixMilli(10).UTC()
	finishedAt := time.UnixMilli(11).UTC()
	purpose := "Inspect the projected Pod status."
	summary := "The projected status was collected."
	invocation := ToolInvocation{
		ID:              "00000000-0000-7000-8000-000000001001",
		RunID:           "00000000-0000-7000-8000-000000001002",
		Sequence:        1,
		Name:            ToolNameGetResource,
		Version:         "tool-v1",
		Purpose:         &purpose,
		Scope:           ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 2},
		ArgumentsJSON:   `{"kind":"Pod","name":"sample-pod"}`,
		ArgumentsDigest: SHA256Hex(`{"kind":"Pod","name":"sample-pod"}`),
		Status:          ToolInvocationStatusSucceeded,
		ResultSummary:   &summary,
		ReturnedBytes:   128,
		EvidenceCount:   1,
		StartedAt:       &startedAt,
		FinishedAt:      &finishedAt,
	}
	if err := invocation.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	errorClass := SafeErrorClassPermissionDenied
	safeError := "The admitted read was not permitted."
	denied := invocation
	denied.Status = ToolInvocationStatusDenied
	denied.ErrorClass = &errorClass
	denied.SafeError = &safeError
	denied.ResultSummary = nil
	denied.ReturnedBytes = 0
	denied.EvidenceCount = 0
	if err := denied.Validate(); err != nil {
		t.Fatalf("Validate(denied) error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ToolInvocation)
	}{
		{name: "unknown Tool", mutate: func(value *ToolInvocation) { value.Name = "read_secret" }},
		{name: "scope in arguments", mutate: func(value *ToolInvocation) {
			value.ArgumentsJSON = `{"context":"other","name":"sample-pod"}`
			value.ArgumentsDigest = SHA256Hex(value.ArgumentsJSON)
		}},
		{name: "credential field in arguments", mutate: func(value *ToolInvocation) {
			value.ArgumentsJSON = `{"api_key":"blocked"}`
			value.ArgumentsDigest = SHA256Hex(value.ArgumentsJSON)
		}},
		{name: "kubeconfig field in arguments", mutate: func(value *ToolInvocation) {
			value.ArgumentsJSON = `{"kubeconfig":"blocked"}`
			value.ArgumentsDigest = SHA256Hex(value.ArgumentsJSON)
		}},
		{name: "noncanonical arguments", mutate: func(value *ToolInvocation) {
			value.ArgumentsJSON = `{ "name": "sample-pod" }`
			value.ArgumentsDigest = SHA256Hex(value.ArgumentsJSON)
		}},
		{name: "unordered arguments", mutate: func(value *ToolInvocation) {
			value.ArgumentsJSON = `{"name":"sample-pod","kind":"Pod"}`
			value.ArgumentsDigest = SHA256Hex(value.ArgumentsJSON)
		}},
		{name: "duplicate arguments", mutate: func(value *ToolInvocation) {
			value.ArgumentsJSON = `{"kind":"Pod","name":"first","name":"second"}`
			value.ArgumentsDigest = SHA256Hex(value.ArgumentsJSON)
		}},
		{name: "digest mismatch", mutate: func(value *ToolInvocation) { value.ArgumentsDigest = strings.Repeat("0", 64) }},
		{name: "oversized purpose", mutate: func(value *ToolInvocation) {
			text := strings.Repeat("p", maxToolPurposeBytes+1)
			value.Purpose = &text
		}},
		{name: "success with raw-sized summary", mutate: func(value *ToolInvocation) {
			text := strings.Repeat("s", maxSafeSummaryBytes+1)
			value.ResultSummary = &text
		}},
		{name: "terminal without finish", mutate: func(value *ToolInvocation) { value.FinishedAt = nil }},
		{name: "success with error", mutate: func(value *ToolInvocation) {
			value.ErrorClass = &errorClass
			value.SafeError = &safeError
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := invocation
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestToolResultResourceSummariesAreLocalTypedListPresentationData(t *testing.T) {
	t.Parallel()
	observed := time.UnixMilli(20).UTC()
	scope := ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 2}
	reference := ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: scope.Namespace, Name: "sample-pod"}
	evidence := Evidence{
		ID: "00000000-0000-7000-8000-000000001011", RunID: "00000000-0000-7000-8000-000000001012",
		InvocationID: "00000000-0000-7000-8000-000000001013", Category: EvidenceCategoryResourceStatus,
		Scope: scope, Resource: reference, Fact: "Pod sample-pod was observed; phase Running; ready 1 of 1.",
		Fingerprint: SHA256Hex("safe-status"), ObservedAt: observed,
	}
	result := ToolResult{
		InvocationID: evidence.InvocationID, Name: ToolNameListResources, Version: "tool-v1", Scope: scope,
		ObservedAt: observed, Status: ToolResultStatusSuccess, DataJSON: `{"items":[]}`,
		Evidence: []Evidence{evidence},
		ResourceSummaries: []ResourceSummary{{
			Reference: reference,
			Status:    ResourceStatus{Phase: "Running", Ready: Count(1), Desired: Count(1)},
		}},
		Truncation: ToolResultTruncation{ReturnedCount: 1},
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("ToolResult.Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ToolResult)
	}{
		{name: "wrong Tool", mutate: func(value *ToolResult) { value.Name = ToolNameGetResource }},
		{name: "missing Evidence", mutate: func(value *ToolResult) { value.Evidence = nil }},
		{name: "wrong category", mutate: func(value *ToolResult) {
			value.Evidence = append([]Evidence(nil), value.Evidence...)
			value.Evidence[0].Category = EvidenceCategoryCondition
		}},
		{name: "wrong resource", mutate: func(value *ToolResult) {
			value.ResourceSummaries = append([]ResourceSummary(nil), value.ResourceSummaries...)
			value.ResourceSummaries[0].Reference.Name = "other-pod"
		}},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			value := result
			value.Evidence = append([]Evidence(nil), result.Evidence...)
			value.ResourceSummaries = append([]ResourceSummary(nil), result.ResourceSummaries...)
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("ToolResult.Validate() error = nil")
			}
		})
	}
}

func TestModelRequestMetadataValidationExcludesBodiesAndBoundsFields(t *testing.T) {
	startedAt := time.UnixMilli(20).UTC()
	finishedAt := time.UnixMilli(21).UTC()
	endpointHash := SHA256Hex("https://model.invalid")
	requestID := "request-identifier"
	responseHash := SHA256Hex("validated-response")
	inputTokens := int64(10)
	outputTokens := int64(20)
	latency := int64(1)
	request := ModelRequestMetadata{
		ID:                  "00000000-0000-7000-8000-000000001101",
		RunID:               "00000000-0000-7000-8000-000000001102",
		Sequence:            1,
		ProviderKind:        ModelProviderOpenAICompatible,
		EndpointOriginHash:  &endpointHash,
		Model:               "test-model",
		Status:              ModelRequestStatusSucceeded,
		ProviderRequestID:   &requestID,
		PromptVersion:       "prompt-v1",
		PromptFingerprint:   SHA256Hex("safe-prompt-derivative"),
		ResponseFingerprint: &responseHash,
		InputTokens:         &inputTokens,
		OutputTokens:        &outputTokens,
		LatencyMilliseconds: &latency,
		StartedAt:           startedAt,
		FinishedAt:          &finishedAt,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ModelRequestMetadata)
	}{
		{name: "provider", mutate: func(value *ModelRequestMetadata) { value.ProviderKind = "auto_detect" }},
		{name: "sequence", mutate: func(value *ModelRequestMetadata) { value.Sequence = maxModelRequests + 1 }},
		{name: "model", mutate: func(value *ModelRequestMetadata) { value.Model = strings.Repeat("m", maxModelIdentifierBytes+1) }},
		{name: "request identifier", mutate: func(value *ModelRequestMetadata) {
			text := strings.Repeat("r", maxProviderRequestIDBytes+1)
			value.ProviderRequestID = &text
		}},
		{name: "response fingerprint", mutate: func(value *ModelRequestMetadata) {
			text := "raw response body"
			value.ResponseFingerprint = &text
		}},
		{name: "negative usage", mutate: func(value *ModelRequestMetadata) {
			negative := int64(-1)
			value.InputTokens = &negative
		}},
		{name: "terminal time", mutate: func(value *ModelRequestMetadata) { value.FinishedAt = nil }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := request
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}
