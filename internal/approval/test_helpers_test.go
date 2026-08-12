package approval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var errSyntheticExecution = errors.New("synthetic execution failure")

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Set(value time.Time) {
	clock.mu.Lock()
	clock.now = value
	clock.mu.Unlock()
}

type sequenceNonceSource struct {
	mu     sync.Mutex
	values []domain.ApprovalNonce
	calls  int
	err    error
}

func (source *sequenceNonceSource) NewNonce(ctx context.Context) (domain.ApprovalNonce, error) {
	if err := ctx.Err(); err != nil {
		return domain.ApprovalNonce{}, err
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	if source.err != nil {
		return domain.ApprovalNonce{}, source.err
	}
	if len(source.values) == 0 {
		return domain.ApprovalNonce{}, errors.New("synthetic nonce exhaustion")
	}
	value := source.values[0]
	source.values = source.values[1:]
	return value, nil
}

type fakeRestartExecutor struct {
	mu           sync.Mutex
	calls        int
	intents      []domain.OperationIntent
	executions   []RestartDeploymentExecution
	auditCommits int
	err          error
}

func (executor *fakeRestartExecutor) VerifyApproved(_ context.Context, request StoredRequest, decision StoredDecision) error {
	if request.Validate() != nil || request.State != domain.ApprovalStateApproved || decision.Validate() != nil ||
		decision.RequestID != request.ID || decision.Choice != domain.ApprovalDecisionApprove ||
		!decision.ShownDigest.Equal(request.Digest) || decision.NonceHash != request.NonceHash {
		return ErrStoredApprovalConflict
	}
	return nil
}

func (executor *fakeRestartExecutor) ConsumeWithAudit(
	_ context.Context,
	expected StoredRequest,
	expectedDecision StoredDecision,
	consumed StoredRequest,
	audit domain.AuditEvent,
) error {
	if expected.State != domain.ApprovalStateApproved || consumed.State != domain.ApprovalStateConsumed ||
		expectedDecision.Validate() != nil || expectedDecision.RequestID != expected.ID ||
		expectedDecision.Choice != domain.ApprovalDecisionApprove ||
		!expectedDecision.ShownDigest.Equal(expected.Digest) || expectedDecision.NonceHash != expected.NonceHash ||
		!sameStoredIdentity(expected, consumed) || consumed.ValidateAudit(audit) != nil {
		return ErrStoredApprovalConflict
	}
	executor.mu.Lock()
	executor.auditCommits++
	executor.mu.Unlock()
	return nil
}

func (executor *fakeRestartExecutor) CurrentScope() (domain.ClusterScope, bool) {
	intent := testIntent()
	return domain.ClusterScope{
		Context: intent.Scope.Context, Namespace: intent.Scope.Namespace, Generation: intent.Scope.Generation,
		ActivatedAt: time.UnixMilli(1).UTC(),
	}, true
}

func (executor *fakeRestartExecutor) RevalidateApprovedRestart(
	ctx context.Context,
	intent domain.OperationIntent,
) (RestartDeploymentObservation, error) {
	if err := ctx.Err(); err != nil {
		return RestartDeploymentObservation{}, err
	}
	executor.mu.Lock()
	executor.intents = append(executor.intents, intent)
	executor.mu.Unlock()
	return RestartDeploymentObservation{
		Scope: intent.Scope, DeploymentName: intent.DeploymentName, DeploymentUID: intent.DeploymentUID,
		TemplateFingerprint: intent.TemplateFingerprint, DeploymentGeneration: intent.DeploymentGeneration,
		ResourceVersion: "fresh-resource-version",
	}, nil
}

func (executor *fakeRestartExecutor) ExecuteApprovedRestart(
	ctx context.Context,
	execution RestartDeploymentExecution,
) (RestartDeploymentResult, error) {
	if err := ctx.Err(); err != nil {
		return RestartDeploymentResult{}, err
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.calls++
	executor.executions = append(executor.executions, execution)
	if executor.err != nil {
		return RestartDeploymentResult{}, executor.err
	}
	return RestartDeploymentResult{
		Scope:                   execution.Observation().Scope,
		DeploymentName:          execution.Observation().DeploymentName,
		DeploymentUID:           execution.Observation().DeploymentUID,
		PreviousResourceVersion: execution.Observation().ResourceVersion,
		ResourceVersion:         "post-restart-rv-19",
		TargetGeneration:        execution.Observation().DeploymentGeneration + 1,
		TargetReplicas:          1,
		RestartedAt:             time.UnixMilli(2).UTC(),
	}, nil
}

func (*fakeRestartExecutor) NewAuditEventID() (domain.AuditEventID, error) {
	return "00000000-0000-7000-8000-000000003091", nil
}

func (executor *fakeRestartExecutor) WriteCount() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.calls
}

func (executor *fakeRestartExecutor) Intents() []domain.OperationIntent {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]domain.OperationIntent(nil), executor.intents...)
}

func testNonce(t *testing.T, marker byte) domain.ApprovalNonce {
	t.Helper()
	value := make([]byte, domain.ApprovalNonceBytes)
	for index := range value {
		value[index] = marker + byte(index%7)
	}
	nonce, err := domain.NewApprovalNonce(value)
	if err != nil {
		t.Fatalf("NewApprovalNonce() error = %v", err)
	}
	return nonce
}

func testIntent() domain.OperationIntent {
	return domain.OperationIntent{
		Operation:            domain.ApprovalOperationRestartDeployment,
		Scope:                domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		DeploymentName:       "sample-deployment",
		DeploymentUID:        "deployment-uid",
		TemplateFingerprint:  domain.SHA256Hex("projected-pod-template"),
		DeploymentGeneration: 11,
		PolicyVersion:        domain.RestartDeploymentApprovalPolicyVersion,
		ReasonSummary:        "Restart after diagnosis.",
	}
}

func testRequestCommand() RequestCommand {
	return RequestCommand{
		ID:        "00000000-0000-7000-8000-000000003001",
		RunID:     "00000000-0000-7000-8000-000000003002",
		SessionID: "00000000-0000-7000-8000-000000003003",
		Intent:    testIntent(),
	}
}

func newTestService(t *testing.T, now time.Time, nonceValues ...domain.ApprovalNonce) (*Service, *fakeClock, *fakeRestartExecutor) {
	t.Helper()
	clock := &fakeClock{now: now}
	executor := &fakeRestartExecutor{}
	service, err := NewService(ServiceConfig{
		Clock: clock, Nonces: &sequenceNonceSource{values: nonceValues},
		Store: executor, Scope: executor, Revalidator: executor, Executor: executor, AuditIDs: executor,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, clock, executor
}

func approveCommand(request domain.ApprovalRequest) DecisionCommand {
	return DecisionCommand{
		RequestID:    request.ID,
		Choice:       domain.ApprovalDecisionApprove,
		ShownDigest:  request.ShownDigest(),
		Nonce:        request.Nonce,
		CurrentScope: request.Intent.Scope,
	}
}

func consumeCommand(request domain.ApprovalRequest) ConsumeCommand {
	return ConsumeCommand{
		RequestID:    request.ID,
		ShownDigest:  request.ShownDigest(),
		Nonce:        request.Nonce,
		CurrentScope: request.Intent.Scope,
	}
}

func requireApprovalErrorCode(t *testing.T, err error, want domain.ApprovalErrorCode) {
	t.Helper()
	var approvalError *domain.ApprovalError
	if !errors.As(err, &approvalError) {
		t.Fatalf("error = %v, want ApprovalError code %q", err, want)
	}
	if approvalError.Code() != want {
		t.Fatalf("ApprovalError.Code() = %q, want %q", approvalError.Code(), want)
	}
}
