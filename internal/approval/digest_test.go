package approval

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestCanonicalOperationAndDigestLockedVector(t *testing.T) {
	t.Parallel()
	request := digestFixtureRequest(t)
	wantCanonical := "kupilot.approval.operation-digest.v1\n" +
		"request_id=36:00000000-0000-7000-8000-000000003001\n" +
		"session_id=36:00000000-0000-7000-8000-000000003003\n" +
		"run_id=36:00000000-0000-7000-8000-000000003002\n" +
		"operation=18:restart_deployment\n" +
		"operation_schema_version=21:restart_deployment/v1\n" +
		"policy_version=30:restart-deployment-approval/v1\n" +
		"scope_context=12:test-context\n" +
		"scope_namespace=14:test-namespace\n" +
		"scope_generation=1:7\n" +
		"target_api_version=7:apps/v1\n" +
		"target_kind=10:Deployment\n" +
		"target_namespace=14:test-namespace\n" +
		"deployment_name=17:sample-deployment\n" +
		"deployment_uid=14:deployment-uid\n" +
		"template_fingerprint=64:df9381f3e70df4545a9a983f4e91b11942c43f2c3dc69b4d6d2e1c2ed2e5fd87\n" +
		"deployment_generation=2:11\n" +
		"reason_summary=24:Restart after diagnosis.\n" +
		"risk_summary=80:Restarting the Deployment replaces Pods and may temporarily reduce availability.\n" +
		"requested_at_ms=13:1700000000123\n" +
		"expires_at_ms=13:1700000060123\n"

	canonical, err := CanonicalOperation(request)
	if err != nil {
		t.Fatalf("CanonicalOperation() error = %v", err)
	}
	if string(canonical) != wantCanonical {
		t.Fatalf("CanonicalOperation() = %q, want %q", canonical, wantCanonical)
	}
	digest, err := OperationDigest(request)
	if err != nil {
		t.Fatalf("OperationDigest() error = %v", err)
	}
	const wantDigest = domain.ApprovalDigest("b7c0aab6f0a4206709e01a6141697613d10fee2277cd4af69447255c7078a62a")
	if digest != wantDigest {
		t.Fatalf("OperationDigest() = %q, want %q", digest, wantDigest)
	}
	if !digest.Equal(wantDigest) || !digest.Valid() {
		t.Fatal("locked digest did not validate or compare equal")
	}
}

func TestOperationDigestChangesForEveryMutableBoundField(t *testing.T) {
	t.Parallel()
	base := digestFixtureRequest(t)
	baseDigest, err := OperationDigest(base)
	if err != nil {
		t.Fatalf("OperationDigest(base) error = %v", err)
	}
	mutations := []struct {
		name   string
		mutate func(*domain.ApprovalRequest)
	}{
		{name: "request identity", mutate: func(value *domain.ApprovalRequest) { value.ID = "00000000-0000-7000-8000-000000003011" }},
		{name: "session identity", mutate: func(value *domain.ApprovalRequest) { value.SessionID = "00000000-0000-7000-8000-000000003013" }},
		{name: "run identity", mutate: func(value *domain.ApprovalRequest) { value.RunID = "00000000-0000-7000-8000-000000003012" }},
		{name: "scope context", mutate: func(value *domain.ApprovalRequest) { value.Intent.Scope.Context = "other-context" }},
		{name: "scope namespace", mutate: func(value *domain.ApprovalRequest) {
			value.Intent.Scope.Namespace = "other-namespace"
		}},
		{name: "scope generation", mutate: func(value *domain.ApprovalRequest) { value.Intent.Scope.Generation++ }},
		{name: "deployment name", mutate: func(value *domain.ApprovalRequest) { value.Intent.DeploymentName = "other-deployment" }},
		{name: "deployment uid", mutate: func(value *domain.ApprovalRequest) { value.Intent.DeploymentUID = "other-deployment-uid" }},
		{name: "template fingerprint", mutate: func(value *domain.ApprovalRequest) {
			value.Intent.TemplateFingerprint = domain.SHA256Hex("changed-template")
		}},
		{name: "deployment generation", mutate: func(value *domain.ApprovalRequest) { value.Intent.DeploymentGeneration++ }},
		{name: "reason summary", mutate: func(value *domain.ApprovalRequest) { value.Intent.ReasonSummary = "A different bounded reason." }},
		{name: "time window", mutate: func(value *domain.ApprovalRequest) {
			value.RequestedAt = value.RequestedAt.Add(time.Millisecond)
			value.ExpiresAt = value.ExpiresAt.Add(time.Millisecond)
		}},
	}
	for _, current := range mutations {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			mutated := base
			current.mutate(&mutated)
			got, err := OperationDigest(mutated)
			if err != nil {
				t.Fatalf("OperationDigest(mutated) error = %v", err)
			}
			if got.Equal(baseDigest) {
				t.Fatalf("mutating %s preserved digest %q", current.name, got)
			}
		})
	}
}

func TestOperationIntentIsClosedAndContainsNoArbitraryPayload(t *testing.T) {
	t.Parallel()
	intentType := reflect.TypeOf(domain.OperationIntent{})
	wantFields := []string{
		"Operation",
		"Scope",
		"DeploymentName",
		"DeploymentUID",
		"TemplateFingerprint",
		"DeploymentGeneration",
		"PolicyVersion",
		"ReasonSummary",
	}
	if intentType.NumField() != len(wantFields) {
		t.Fatalf("OperationIntent field count = %d, want %d", intentType.NumField(), len(wantFields))
	}
	for index, want := range wantFields {
		field := intentType.Field(index)
		if field.Name != want {
			t.Fatalf("OperationIntent field[%d] = %q, want %q", index, field.Name, want)
		}
		if field.Type.Kind() == reflect.Map || field.Type.Kind() == reflect.Slice || field.Type.Kind() == reflect.Interface {
			t.Fatalf("OperationIntent.%s admits an open payload type %s", field.Name, field.Type)
		}
	}

	tests := []struct {
		name   string
		mutate func(*domain.OperationIntent)
	}{
		{name: "unknown operation", mutate: func(value *domain.OperationIntent) { value.Operation = "delete" }},
		{name: "policy version", mutate: func(value *domain.OperationIntent) { value.PolicyVersion = "user-selected" }},
		{name: "missing uid", mutate: func(value *domain.OperationIntent) { value.DeploymentUID = "" }},
		{name: "invalid fingerprint", mutate: func(value *domain.OperationIntent) { value.TemplateFingerprint = "template" }},
		{name: "zero deployment generation", mutate: func(value *domain.OperationIntent) { value.DeploymentGeneration = 0 }},
		{name: "unsafe reason", mutate: func(value *domain.OperationIntent) { value.ReasonSummary = "approve\nnow" }},
		{name: "oversized reason", mutate: func(value *domain.OperationIntent) {
			value.ReasonSummary = strings.Repeat("r", domain.MaxApprovalReasonSummaryBytes+1)
		}},
	}
	for _, current := range tests {
		current := current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			value := testIntent()
			current.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("OperationIntent.Validate() error = nil")
			}
		})
	}
}

func TestApprovalNonceIsOpaqueStableAndComparedByValue(t *testing.T) {
	t.Parallel()
	left := testNonce(t, 1)
	same := testNonce(t, 1)
	different := testNonce(t, 2)
	if !left.Valid() || !left.Equal(same) || left.Equal(different) {
		t.Fatal("ApprovalNonce validity or equality contract failed")
	}
	if left.Hash() != same.Hash() || left.Hash() == different.Hash() || !left.Hash().Valid() {
		t.Fatal("ApprovalNonce hash contract failed")
	}
	if rendered := fmt.Sprintf("%v %+v %#v", left, left, left); strings.Contains(rendered, "1 2 3") || rendered != "[approval nonce] [approval nonce] [approval nonce]" {
		t.Fatalf("ApprovalNonce rendered raw value: %q", rendered)
	}
	encoded, err := json.Marshal(left)
	if err != nil {
		t.Fatalf("json.Marshal(ApprovalNonce) error = %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("json.Marshal(ApprovalNonce) = %s, want opaque object", encoded)
	}
	if _, err := domain.NewApprovalNonce(make([]byte, domain.ApprovalNonceBytes)); err == nil {
		t.Fatal("NewApprovalNonce(all zero) error = nil")
	}
	if hash := (domain.ApprovalNonce{}).Hash(); hash != "" || hash.Valid() {
		t.Fatalf("invalid ApprovalNonce.Hash() = %q", hash)
	}
}

func digestFixtureRequest(t *testing.T) domain.ApprovalRequest {
	t.Helper()
	requestedAt := time.UnixMilli(1_700_000_000_123).UTC()
	return domain.ApprovalRequest{
		ID:             "00000000-0000-7000-8000-000000003001",
		RunID:          "00000000-0000-7000-8000-000000003002",
		SessionID:      "00000000-0000-7000-8000-000000003003",
		Intent:         testIntent(),
		Nonce:          testNonce(t, 1),
		State:          domain.ApprovalStatePending,
		RequestedAt:    requestedAt,
		ExpiresAt:      requestedAt.Add(domain.ApprovalExecutionTTL),
		StateChangedAt: requestedAt,
	}
}
