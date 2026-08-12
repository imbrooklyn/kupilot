package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

func TestRestartDeploymentProposalBridgeCreatesOnlyPendingApproval(t *testing.T) {
	target := testRestartProposalTarget()
	sink := &fakeRestartProposalSink{}
	sink.submit = func(_ context.Context, runID domain.AgentRunID, sessionID domain.SessionID, sequence int64, intent domain.OperationIntent) (domain.ApprovalRequest, error) {
		if runID != target.RunID || sessionID != target.SessionID || sequence != target.Sequence {
			t.Fatalf("proposal identity = %s/%s/%d", runID, sessionID, sequence)
		}
		if intent.Scope != target.Scope || intent.DeploymentName != target.DeploymentName ||
			intent.DeploymentUID != target.DeploymentUID || intent.TemplateFingerprint != target.TemplateFingerprint ||
			intent.DeploymentGeneration != target.DeploymentGeneration || intent.ReasonSummary != "Restart after diagnosis." {
			t.Fatalf("proposal intent = %#v", intent)
		}
		return pendingProposalRequest(t, runID, sessionID, intent), nil
	}
	bridge, err := NewRestartDeploymentProposalBridge(sink, security.NewRedactor())
	if err != nil {
		t.Fatalf("NewRestartDeploymentProposalBridge() error = %v", err)
	}
	specification := bridge.Specification()
	if specification.Name != RestartDeploymentProposalName || specification.Version != RestartDeploymentProposalVersion ||
		!json.Valid([]byte(specification.InputSchemaJSON)) || strings.Contains(specification.InputSchemaJSON, "patch") ||
		strings.Contains(specification.InputSchemaJSON, "yaml") {
		t.Fatalf("proposal specification = %#v", specification)
	}
	result, err := bridge.Propose(context.Background(), target, `{"reason":"Restart after diagnosis."}`)
	if err != nil {
		t.Fatalf("Propose() error = %v", err)
	}
	if sink.calls != 1 || result.State != domain.ApprovalStatePending || !result.RequestID.Valid() ||
		!result.Digest.Valid() || !result.ExpiresAt.After(result.RequestedAt) {
		t.Fatalf("proposal calls/result = %d/%#v", sink.calls, result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal(result) error = %v", err)
	}
	if bytes.Contains(encoded, bytes.Repeat([]byte{0x61}, domain.ApprovalNonceBytes)) ||
		strings.Contains(strings.ToLower(string(encoded)), `"state":"executed"`) ||
		strings.Contains(strings.ToLower(string(encoded)), `"success":`) {
		t.Fatalf("model result disclosed authority or execution claim: %s", encoded)
	}
}

func TestRestartDeploymentProposalBridgeRequiresSinkAndTextPolicy(t *testing.T) {
	if _, err := NewRestartDeploymentProposalBridge(nil, security.NewRedactor()); !errors.Is(err, ErrInvalidRestartDeploymentProposalBridge) {
		t.Fatalf("NewRestartDeploymentProposalBridge(nil sink) error = %v", err)
	}
	if _, err := NewRestartDeploymentProposalBridge(&fakeRestartProposalSink{}, nil); !errors.Is(err, ErrInvalidRestartDeploymentProposalBridge) {
		t.Fatalf("NewRestartDeploymentProposalBridge(nil text) error = %v", err)
	}
}

func TestRestartDeploymentProposalBridgeSanitizesReasonBeforePersistence(t *testing.T) {
	target := testRestartProposalTarget()
	canary := strings.Repeat("proposal-secret-canary", 2)
	rawReason := "Restart \x1b[31mafter diagnosis.\x1b[0m " + "password" + "=" + canary
	encoded, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: rawReason})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	sink := &fakeRestartProposalSink{submit: func(
		_ context.Context,
		runID domain.AgentRunID,
		sessionID domain.SessionID,
		_ int64,
		intent domain.OperationIntent,
	) (domain.ApprovalRequest, error) {
		if intent.ReasonSummary != "Restart after diagnosis. [REDACTED]" ||
			strings.Contains(intent.ReasonSummary, canary) || strings.ContainsRune(intent.ReasonSummary, '\x1b') {
			t.Fatalf("sanitized reason = %q", intent.ReasonSummary)
		}
		return pendingProposalRequest(t, runID, sessionID, intent), nil
	}}
	bridge, err := NewRestartDeploymentProposalBridge(sink, security.NewRedactor())
	if err != nil {
		t.Fatalf("NewRestartDeploymentProposalBridge() error = %v", err)
	}
	if _, err := bridge.Propose(context.Background(), target, string(encoded)); err != nil {
		t.Fatalf("Propose() error = %v", err)
	}
	if sink.calls != 1 {
		t.Fatalf("sink calls = %d, want 1", sink.calls)
	}
}

func TestRestartDeploymentProposalBridgeBlocksHighRiskReasonBeforePersistence(t *testing.T) {
	target := testRestartProposalTarget()
	highRisk := strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ")
	encoded, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: highRisk})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	sink := &fakeRestartProposalSink{}
	bridge, err := NewRestartDeploymentProposalBridge(sink, security.NewRedactor())
	if err != nil {
		t.Fatalf("NewRestartDeploymentProposalBridge() error = %v", err)
	}
	if _, err := bridge.Propose(context.Background(), target, string(encoded)); !errors.Is(err, ErrRestartDeploymentProposalDenied) {
		t.Fatalf("Propose() error = %v", err)
	}
	if sink.calls != 0 {
		t.Fatalf("sink calls = %d, want 0", sink.calls)
	}
}

func TestRestartDeploymentProposalBridgeRejectsUnboundOrNonStrictInputBeforeSink(t *testing.T) {
	validTarget := testRestartProposalTarget()
	tests := []struct {
		name   string
		target RestartDeploymentProposalTarget
		input  string
	}{
		{name: "unknown patch", target: validTarget, input: `{"reason":"Restart.","patch":{}}`},
		{name: "yaml", target: validTarget, input: `{"reason":"kind: Deployment\\nspec: {}"}`},
		{name: "duplicate reason", target: validTarget, input: `{"reason":"First.","reason":"Second."}`},
		{name: "wrong type", target: validTarget, input: `{"reason":7}`},
		{name: "missing reason", target: validTarget, input: `{}`},
		{name: "trailing data", target: validTarget, input: `{"reason":"Restart."}{}`},
		{name: "overlong reason", target: validTarget, input: `{"reason":"` + strings.Repeat("x", domain.MaxApprovalReasonSummaryBytes+1) + `"}`},
		{name: "oversize envelope", target: validTarget, input: strings.Repeat(" ", maxRestartDeploymentProposalInputBytes+1)},
		{name: "invalid target", target: RestartDeploymentProposalTarget{}, input: `{"reason":"Restart."}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := &fakeRestartProposalSink{}
			bridge, err := NewRestartDeploymentProposalBridge(sink, security.NewRedactor())
			if err != nil {
				t.Fatalf("NewRestartDeploymentProposalBridge() error = %v", err)
			}
			if _, err := bridge.Propose(context.Background(), test.target, test.input); !errors.Is(err, ErrRestartDeploymentProposalDenied) {
				t.Fatalf("Propose() error = %v", err)
			}
			if sink.calls != 0 {
				t.Fatalf("sink calls = %d, want 0", sink.calls)
			}
		})
	}
}

func TestRestartDeploymentProposalBridgeFailsClosedOnCancellationAndNonPendingResult(t *testing.T) {
	target := testRestartProposalTarget()
	t.Run("cancelled", func(t *testing.T) {
		sink := &fakeRestartProposalSink{}
		bridge, _ := NewRestartDeploymentProposalBridge(sink, security.NewRedactor())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := bridge.Propose(ctx, target, `{"reason":"Restart."}`); !errors.Is(err, context.Canceled) {
			t.Fatalf("Propose(cancelled) error = %v", err)
		}
		if sink.calls != 0 {
			t.Fatalf("sink calls = %d, want 0", sink.calls)
		}
	})
	t.Run("approved result", func(t *testing.T) {
		sink := &fakeRestartProposalSink{submit: func(_ context.Context, runID domain.AgentRunID, sessionID domain.SessionID, _ int64, intent domain.OperationIntent) (domain.ApprovalRequest, error) {
			request := pendingProposalRequest(t, runID, sessionID, intent)
			request.State = domain.ApprovalStateApproved
			request.StateReason = domain.ApprovalReasonUserApproved
			request.StateChangedAt = request.RequestedAt.Add(time.Second)
			return request, nil
		}}
		bridge, _ := NewRestartDeploymentProposalBridge(sink, security.NewRedactor())
		if _, err := bridge.Propose(context.Background(), target, `{"reason":"Restart."}`); !errors.Is(err, ErrRestartDeploymentProposalDenied) {
			t.Fatalf("Propose(approved result) error = %v", err)
		}
		if sink.calls != 1 {
			t.Fatalf("sink calls = %d, want 1", sink.calls)
		}
	})
}

type fakeRestartProposalSink struct {
	calls  int
	submit func(context.Context, domain.AgentRunID, domain.SessionID, int64, domain.OperationIntent) (domain.ApprovalRequest, error)
}

func (sink *fakeRestartProposalSink) SubmitRestartDeploymentProposal(
	ctx context.Context,
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	sequence int64,
	intent domain.OperationIntent,
) (domain.ApprovalRequest, error) {
	sink.calls++
	if sink.submit == nil {
		return domain.ApprovalRequest{}, ErrRestartDeploymentProposalDenied
	}
	return sink.submit(ctx, runID, sessionID, sequence, intent)
}

func testRestartProposalTarget() RestartDeploymentProposalTarget {
	return RestartDeploymentProposalTarget{
		RunID:          "00000000-0000-7000-8000-000000008001",
		SessionID:      "00000000-0000-7000-8000-000000008002",
		Sequence:       11,
		Scope:          domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		DeploymentName: "sample-deployment", DeploymentUID: "sample-deployment-uid",
		TemplateFingerprint: strings.Repeat("b", 64), DeploymentGeneration: 9,
	}
}

func pendingProposalRequest(
	t *testing.T,
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	intent domain.OperationIntent,
) domain.ApprovalRequest {
	t.Helper()
	nonce, err := domain.NewApprovalNonce(bytes.Repeat([]byte{0x61}, domain.ApprovalNonceBytes))
	if err != nil {
		t.Fatalf("NewApprovalNonce() error = %v", err)
	}
	requestedAt := time.UnixMilli(1_700_000_400_000).UTC()
	request := domain.ApprovalRequest{
		ID: "00000000-0000-7000-8000-000000008003", RunID: runID, SessionID: sessionID,
		Intent: intent, Nonce: nonce, State: domain.ApprovalStatePending,
		RequestedAt: requestedAt, ExpiresAt: requestedAt.Add(domain.ApprovalExecutionTTL), StateChangedAt: requestedAt,
	}
	request.Digest, err = approval.OperationDigest(request)
	if err != nil || request.Validate() != nil {
		t.Fatalf("pending request error/request = %v/%#v", err, request)
	}
	return request
}
