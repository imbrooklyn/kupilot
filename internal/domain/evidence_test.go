package domain

import (
	"strings"
	"testing"
	"time"
)

func TestEvidenceValidationAndDetailState(t *testing.T) {
	sourcePath := "status.containerStatuses[0].state.waiting.reason"
	severity := EvidenceSeverityWarning
	evidence := Evidence{
		ID:           "00000000-0000-7000-8000-000000001201",
		RunID:        "00000000-0000-7000-8000-000000001202",
		InvocationID: "00000000-0000-7000-8000-000000001203",
		Category:     EvidenceCategoryContainerState,
		Scope:        ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 3},
		Resource: ResourceRef{
			APIVersion:      "v1",
			Kind:            "Pod",
			Namespace:       "test-namespace",
			Name:            "sample-pod",
			ResourceVersion: "12",
		},
		Fact:           "The projected container state is waiting.",
		SourcePath:     &sourcePath,
		Severity:       &severity,
		RedactionCount: 1,
		Fingerprint:    SHA256Hex("normalized-evidence"),
		ObservedAt:     time.UnixMilli(30).UTC(),
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := evidence.DetailState(); got != EvidenceDetailAvailable {
		t.Fatalf("DetailState() = %q, want %q", got, EvidenceDetailAvailable)
	}
	truncated := evidence
	truncated.Truncated = true
	if got := truncated.DetailState(); got != EvidenceDetailPartial {
		t.Fatalf("truncated DetailState() = %q, want %q", got, EvidenceDetailPartial)
	}

	tests := []struct {
		name   string
		mutate func(*Evidence)
	}{
		{name: "unknown category", mutate: func(value *Evidence) { value.Category = "raw_object" }},
		{name: "scope mismatch", mutate: func(value *Evidence) { value.Resource.Namespace = "other-namespace" }},
		{name: "oversized fact", mutate: func(value *Evidence) { value.Fact = strings.Repeat("f", maxEvidenceFactBytes+1) }},
		{name: "oversized source path", mutate: func(value *Evidence) {
			text := strings.Repeat("p", maxEvidenceSourcePathBytes+1)
			value.SourcePath = &text
		}},
		{name: "negative redactions", mutate: func(value *Evidence) { value.RedactionCount = -1 }},
		{name: "invalid fingerprint", mutate: func(value *Evidence) { value.Fingerprint = "raw-value" }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := evidence
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}
