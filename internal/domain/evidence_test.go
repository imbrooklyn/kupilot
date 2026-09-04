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
	partial := evidence
	partial.Partial = true
	if got := partial.DetailState(); got != EvidenceDetailPartial {
		t.Fatalf("partial DetailState() = %q, want %q", got, EvidenceDetailPartial)
	}
	crossNamespace := evidence
	crossNamespace.Resource.Namespace = "other-namespace"
	if err := crossNamespace.Validate(); err != nil {
		t.Fatalf("cross-Namespace Evidence validation = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Evidence)
	}{
		{name: "unknown category", mutate: func(value *Evidence) { value.Category = "raw_object" }},
		{name: "prohibited resource", mutate: func(value *Evidence) { value.Resource.Kind = "Secret" }},
		{name: "oversized fact", mutate: func(value *Evidence) { value.Fact = strings.Repeat("f", maxEvidenceFactBytes+1) }},
		{name: "oversized source path", mutate: func(value *Evidence) {
			text := strings.Repeat("p", maxEvidenceSourcePathBytes+1)
			value.SourcePath = &text
		}},
		{name: "negative redactions", mutate: func(value *Evidence) { value.RedactionCount = -1 }},
		{name: "invalid fingerprint", mutate: func(value *Evidence) { value.Fingerprint = "raw-value" }},
		{name: "negative policy generation without policy", mutate: func(value *Evidence) { value.PolicyGeneration = -1 }},
		{name: "policy generation without policy version", mutate: func(value *Evidence) { value.PolicyGeneration = 1 }},
		{name: "policy version without generation", mutate: func(value *Evidence) { value.PolicyVersion = ResourcePolicyVersion }},
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

func TestEvidenceValidationBindsObservabilityCategoryProvenance(t *testing.T) {
	from := time.UnixMilli(20).UTC()
	through := time.UnixMilli(30).UTC()
	sourcePath := "api/v1/query_range#pod_cpu_usage"
	valid := Evidence{
		ID:           "00000000-0000-7000-8000-000000001211",
		RunID:        "00000000-0000-7000-8000-000000001212",
		InvocationID: "00000000-0000-7000-8000-000000001213",
		Category:     EvidenceCategoryPrometheus,
		Scope:        ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 3},
		Resource: ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod",
		},
		PolicyVersion: ObservabilityPolicyVersion, PolicyGeneration: 7,
		Fact: "The admitted CPU series contains one bounded sample.", SourcePath: &sourcePath,
		SourceOriginHash: SHA256Hex("https://prometheus.example"), Series: "container=app",
		ObservedFrom: &from, ObservedThrough: &through,
		Fingerprint: SHA256Hex("observability-evidence"), ObservedAt: through,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Evidence)
	}{
		{name: "missing source origin", mutate: func(value *Evidence) { value.SourceOriginHash = "" }},
		{name: "missing series", mutate: func(value *Evidence) { value.Series = "" }},
		{name: "missing window", mutate: func(value *Evidence) {
			value.ObservedFrom = nil
			value.ObservedThrough = nil
		}},
		{name: "resource policy", mutate: func(value *Evidence) { value.PolicyVersion = ResourcePolicyVersion }},
		{name: "resource category with source authority", mutate: func(value *Evidence) {
			value.Category = EvidenceCategoryResourceStatus
			value.PolicyVersion = ResourcePolicyVersion
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}
