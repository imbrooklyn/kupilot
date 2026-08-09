package domain

import (
	"strings"
	"testing"
	"time"
)

func TestApprovalSchemaRecordIsBoundedButDoesNotExposeExecution(t *testing.T) {
	resolvedAt := time.UnixMilli(71).UTC()
	record := ApprovalSchemaRecord{
		ID:                      "00000000-0000-7000-8000-000000001501",
		RunID:                   "00000000-0000-7000-8000-000000001502",
		SessionID:               "00000000-0000-7000-8000-000000001503",
		Operation:               ApprovalOperationRestartDeployment,
		Scope:                   ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 6},
		Target:                  ResourceRef{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "test-namespace", Name: "sample-deployment", UID: "deployment-uid"},
		CanonicalParametersJSON: `{}`,
		OperationDigest:         SHA256Hex("bound-operation"),
		HumanSummary:            "Restart the selected Deployment.",
		RiskSummary:             "Replacement Pods may be temporarily unavailable.",
		Status:                  ApprovalSchemaStatusRejected,
		PolicyVersion:           "approval-v1",
		RequestedAt:             time.UnixMilli(70).UTC(),
		ExpiresAt:               time.UnixMilli(60_070).UTC(),
		ResolvedAt:              &resolvedAt,
	}
	if err := record.ValidateSchemaShape(); err != nil {
		t.Fatalf("ValidateSchemaShape() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ApprovalSchemaRecord)
	}{
		{name: "operation", mutate: func(value *ApprovalSchemaRecord) { value.Operation = "patch" }},
		{name: "target", mutate: func(value *ApprovalSchemaRecord) { value.Target.Kind = "Pod" }},
		{name: "parameters", mutate: func(value *ApprovalSchemaRecord) { value.CanonicalParametersJSON = `{ "patch": [] }` }},
		{name: "digest", mutate: func(value *ApprovalSchemaRecord) { value.OperationDigest = "mutable" }},
		{name: "summary", mutate: func(value *ApprovalSchemaRecord) { value.HumanSummary = strings.Repeat("h", maxApprovalSummaryBytes+1) }},
		{name: "expiry", mutate: func(value *ApprovalSchemaRecord) { value.ExpiresAt = value.RequestedAt }},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			value := record
			current.mutate(&value)
			if err := value.ValidateSchemaShape(); err == nil {
				t.Fatal("ValidateSchemaShape() error = nil")
			}
		})
	}
}
