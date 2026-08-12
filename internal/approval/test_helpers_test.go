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
	mu      sync.Mutex
	calls   int
	intents []domain.OperationIntent
	err     error
}

func (executor *fakeRestartExecutor) ExecuteApprovedRestart(
	ctx context.Context,
	intent domain.OperationIntent,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.calls++
	executor.intents = append(executor.intents, intent)
	return executor.err
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
		Clock:    clock,
		Nonces:   &sequenceNonceSource{values: nonceValues},
		Executor: executor,
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
