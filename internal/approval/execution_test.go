package approval

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestApprovalConsumeRequiresDurableApprovalFreshTargetAndPreWriteAudit(t *testing.T) {
	tests := []struct {
		name             string
		prepare          func(*executionFixture)
		wantState        domain.ApprovalState
		wantRevalidates  int
		wantAuditCommits int
		wantWrites       int
		wantCode         domain.ApprovalErrorCode
	}{
		{
			name: "durable approval mismatch",
			prepare: func(fixture *executionFixture) {
				fixture.store.verifyErr = ErrStoredApprovalConflict
			},
			wantState: domain.ApprovalStateInvalidated, wantCode: domain.ApprovalErrorCodeInternal,
		},
		{
			name: "scope changed before fresh read",
			prepare: func(fixture *executionFixture) {
				fixture.scope.Set(domain.ClusterScope{})
			},
			wantState: domain.ApprovalStateInvalidated, wantCode: domain.ApprovalErrorCodeStaleScope,
		},
		{
			name: "fresh read forbidden",
			prepare: func(fixture *executionFixture) {
				fixture.target.revalidateErr = errors.New("synthetic forbidden read")
			},
			wantState: domain.ApprovalStateInvalidated, wantRevalidates: 1, wantCode: domain.ApprovalErrorCodeExecutorFailed,
		},
		{
			name: "uid changed",
			prepare: func(fixture *executionFixture) {
				fixture.target.observation.DeploymentUID = "changed-uid"
			},
			wantState: domain.ApprovalStateInvalidated, wantRevalidates: 1, wantCode: domain.ApprovalErrorCodeInvalidIntent,
		},
		{
			name: "template changed",
			prepare: func(fixture *executionFixture) {
				fixture.target.observation.TemplateFingerprint = domain.SHA256Hex("changed-template")
			},
			wantState: domain.ApprovalStateInvalidated, wantRevalidates: 1, wantCode: domain.ApprovalErrorCodeInvalidIntent,
		},
		{
			name: "generation changed",
			prepare: func(fixture *executionFixture) {
				fixture.target.observation.DeploymentGeneration++
			},
			wantState: domain.ApprovalStateInvalidated, wantRevalidates: 1, wantCode: domain.ApprovalErrorCodeInvalidIntent,
		},
		{
			name: "scope changed during fresh read",
			prepare: func(fixture *executionFixture) {
				fixture.target.afterRevalidate = func() { fixture.scope.Set(domain.ClusterScope{}) }
			},
			wantState: domain.ApprovalStateInvalidated, wantRevalidates: 1, wantCode: domain.ApprovalErrorCodeStaleScope,
		},
		{
			name: "approval expires during fresh read",
			prepare: func(fixture *executionFixture) {
				fixture.target.afterRevalidate = func() { fixture.clock.Set(fixture.approved.ExpiresAt) }
			},
			wantState: domain.ApprovalStateExpired, wantRevalidates: 1, wantCode: domain.ApprovalErrorCodeExpired,
		},
		{
			name: "pre-write audit failure",
			prepare: func(fixture *executionFixture) {
				fixture.store.consumeErr = errors.New("synthetic audit failure")
			},
			wantState: domain.ApprovalStateInvalidated, wantRevalidates: 1, wantCode: domain.ApprovalErrorCodeInternal,
		},
		{
			name: "scope changed after audit commit",
			prepare: func(fixture *executionFixture) {
				fixture.store.afterConsume = func() { fixture.scope.Set(domain.ClusterScope{}) }
			},
			wantState: domain.ApprovalStateConsumed, wantRevalidates: 1, wantAuditCommits: 1,
			wantCode: domain.ApprovalErrorCodeStaleScope,
		},
		{
			name:      "approved success",
			prepare:   func(*executionFixture) {},
			wantState: domain.ApprovalStateConsumed, wantRevalidates: 1, wantAuditCommits: 1, wantWrites: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t)
			test.prepare(fixture)
			updated, err := fixture.service.Consume(context.Background(), consumeCommand(fixture.approved))
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("Consume() error = %v", err)
				}
			} else {
				requireApprovalErrorCode(t, err, test.wantCode)
			}
			if updated.State != test.wantState {
				t.Fatalf("state = %q, want %q", updated.State, test.wantState)
			}
			wantAttempt := RestartDeploymentNotAttempted
			if test.wantWrites == 1 && test.wantCode == "" {
				wantAttempt = RestartDeploymentPatchAccepted
			}
			if updated.Attempt.State != wantAttempt || updated.Attempt.Validate() != nil {
				t.Fatalf("attempt = %#v, want state %q", updated.Attempt, wantAttempt)
			}
			if fixture.target.RevalidateCount() != test.wantRevalidates ||
				fixture.store.CommitCount() != test.wantAuditCommits || fixture.target.WriteCount() != test.wantWrites {
				t.Fatalf(
					"revalidates/audit commits/writes = %d/%d/%d, want %d/%d/%d",
					fixture.target.RevalidateCount(), fixture.store.CommitCount(), fixture.target.WriteCount(),
					test.wantRevalidates, test.wantAuditCommits, test.wantWrites,
				)
			}
		})
	}
}

func TestApprovalConsumeUsesStatusOnlyFreshResourceVersionAndRejectsReplay(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.target.observation.ResourceVersion = "status-only-rv-19"

	updated, err := fixture.service.Consume(context.Background(), consumeCommand(fixture.approved))
	if err != nil || updated.State != domain.ApprovalStateConsumed {
		t.Fatalf("Consume() = %#v/%v", updated, err)
	}
	executions := fixture.target.Executions()
	if len(executions) != 1 || executions[0].Observation().ResourceVersion != "status-only-rv-19" {
		t.Fatalf("executions = %#v", executions)
	}
	if fixture.store.lastAudit.Type != domain.AuditEventWriteIntent ||
		fixture.store.lastAudit.Outcome != domain.AuditOutcomeSuccess ||
		fixture.store.lastConsumed.State != domain.ApprovalStateConsumed {
		t.Fatalf("pre-write audit/consumed state = %#v/%#v", fixture.store.lastAudit, fixture.store.lastConsumed)
	}

	_, err = fixture.service.Consume(context.Background(), consumeCommand(fixture.approved))
	requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeInvalidTransition)
	if fixture.target.RevalidateCount() != 1 || fixture.store.CommitCount() != 1 || fixture.target.WriteCount() != 1 {
		t.Fatalf("replay revalidates/commits/writes = %d/%d/%d, want 1/1/1",
			fixture.target.RevalidateCount(), fixture.store.CommitCount(), fixture.target.WriteCount())
	}
}

func TestApprovalConsumeRejectsChangedDurablePolicyAndCanonicalParametersBeforeKubernetesRead(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*StoredRequest)
	}{
		{name: "policy", mutate: func(request *StoredRequest) {
			request.Intent.PolicyVersion = "restart-deployment-approval/changed"
		}},
		{name: "reason", mutate: func(request *StoredRequest) {
			request.Intent.ReasonSummary = "Changed canonical reason."
		}},
		{name: "target name", mutate: func(request *StoredRequest) {
			request.Intent.DeploymentName = "changed-deployment"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t)
			test.mutate(&fixture.store.approved)
			updated, err := fixture.service.Consume(context.Background(), consumeCommand(fixture.approved))
			requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeInternal)
			if updated.State != domain.ApprovalStateInvalidated || fixture.target.RevalidateCount() != 0 ||
				fixture.store.CommitCount() != 0 || fixture.target.WriteCount() != 0 {
				t.Fatalf("state/revalidates/commits/writes = %q/%d/%d/%d, want invalidated/0/0/0",
					updated.State, fixture.target.RevalidateCount(), fixture.store.CommitCount(), fixture.target.WriteCount())
			}
		})
	}
}

func TestApprovalConsumeUnclassifiedExecutorFailureIsUnknownAndNeverRetries(t *testing.T) {
	fixture := newExecutionFixture(t)
	fixture.target.executeErr = errors.New("synthetic unclassified executor failure")

	updated, err := fixture.service.Consume(context.Background(), consumeCommand(fixture.approved))
	requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeExecutorFailed)
	if updated.State != domain.ApprovalStateConsumed || updated.Attempt.State != RestartDeploymentPatchUnknown ||
		updated.Attempt.ErrorClass != domain.SafeErrorClassInternal || fixture.target.WriteCount() != 1 || fixture.store.CommitCount() != 1 {
		t.Fatalf("first attempt state/writes/commits = %q/%d/%d", updated.State, fixture.target.WriteCount(), fixture.store.CommitCount())
	}
	_, err = fixture.service.Consume(context.Background(), consumeCommand(fixture.approved))
	requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeInvalidTransition)
	if fixture.target.WriteCount() != 1 || fixture.target.RevalidateCount() != 1 {
		t.Fatalf("replayed executor failure writes/revalidates = %d/%d, want 1/1", fixture.target.WriteCount(), fixture.target.RevalidateCount())
	}
}

func TestApprovalConsumeTreatsInvalidSuccessfulExecutorProjectionAsUnknown(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RestartDeploymentResult)
	}{
		{name: "unchanged resource version", mutate: func(result *RestartDeploymentResult) {
			result.ResourceVersion = result.PreviousResourceVersion
		}},
		{name: "skipped generation", mutate: func(result *RestartDeploymentResult) {
			result.TargetGeneration++
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newExecutionFixture(t)
			fixture.target.resultMutate = test.mutate
			result, err := fixture.service.Consume(context.Background(), consumeCommand(fixture.approved))
			requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeExecutorFailed)
			if result.State != domain.ApprovalStateConsumed ||
				result.Attempt.State != RestartDeploymentPatchUnknown ||
				result.Attempt.ErrorClass != domain.SafeErrorClassInvalidExternalResponse ||
				fixture.target.WriteCount() != 1 || fixture.store.CommitCount() != 1 {
				t.Fatalf("invalid result/attempt/writes/commits = %#v/%#v/%d/%d", result, result.Attempt, fixture.target.WriteCount(), fixture.store.CommitCount())
			}
			_, replayErr := fixture.service.Consume(context.Background(), consumeCommand(fixture.approved))
			requireApprovalErrorCode(t, replayErr, domain.ApprovalErrorCodeInvalidTransition)
			if fixture.target.WriteCount() != 1 {
				t.Fatalf("invalid result replay writes = %d, want 1", fixture.target.WriteCount())
			}
		})
	}
}

type executionFixture struct {
	service  *Service
	approved domain.ApprovalRequest
	store    *fakePreWriteStore
	target   *fakeRestartTarget
	scope    *fakeExecutionScope
	clock    *fakeClock
}

func newExecutionFixture(t *testing.T) *executionFixture {
	t.Helper()
	now := time.UnixMilli(1_700_000_000_000).UTC()
	store := &fakePreWriteStore{}
	target := &fakeRestartTarget{observation: RestartDeploymentObservation{
		Scope:          domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		DeploymentName: "sample-deployment", DeploymentUID: "deployment-uid",
		TemplateFingerprint: domain.SHA256Hex("projected-pod-template"), DeploymentGeneration: 11,
		ResourceVersion: "fresh-rv-18",
	}}
	scope := &fakeExecutionScope{scope: domain.ClusterScope{
		Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7, ActivatedAt: now,
	}}
	clock := &fakeClock{now: now}
	service, err := NewService(ServiceConfig{
		Clock: clock, Nonces: &sequenceNonceSource{values: []domain.ApprovalNonce{testNonce(t, 0x55)}},
		Store: store, Scope: scope, Revalidator: target, Executor: target,
		AuditIDs: fixedApprovalAuditIDs{},
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	request, err := service.Request(context.Background(), testRequestCommand())
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	approved, decision, err := service.Decide(context.Background(), approveCommand(request))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	stored, err := NewStoredRequest(approved)
	if err != nil {
		t.Fatalf("NewStoredRequest() error = %v", err)
	}
	storedDecision, err := NewStoredDecision(decision)
	if err != nil {
		t.Fatalf("NewStoredDecision() error = %v", err)
	}
	store.approved = stored
	store.decision = storedDecision
	return &executionFixture{service: service, approved: approved, store: store, target: target, scope: scope, clock: clock}
}

type fakePreWriteStore struct {
	mu           sync.Mutex
	approved     StoredRequest
	decision     StoredDecision
	verifyErr    error
	consumeErr   error
	commits      int
	afterConsume func()
	lastConsumed StoredRequest
	lastAudit    domain.AuditEvent
}

func (store *fakePreWriteStore) VerifyApproved(_ context.Context, request StoredRequest, decision StoredDecision) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.verifyErr != nil {
		return store.verifyErr
	}
	if !sameStoredIdentity(store.approved, request) || store.approved.State != request.State ||
		store.approved.StateReason != request.StateReason || store.decision != decision {
		return ErrStoredApprovalConflict
	}
	return nil
}

func (store *fakePreWriteStore) ConsumeWithAudit(
	_ context.Context,
	expected StoredRequest,
	expectedDecision StoredDecision,
	consumed StoredRequest,
	audit domain.AuditEvent,
) error {
	store.mu.Lock()
	if store.consumeErr != nil {
		store.mu.Unlock()
		return store.consumeErr
	}
	if !sameStoredIdentity(store.approved, expected) || expected.State != domain.ApprovalStateApproved ||
		store.decision != expectedDecision ||
		consumed.State != domain.ApprovalStateConsumed || consumed.ValidateAudit(audit) != nil {
		store.mu.Unlock()
		return ErrStoredApprovalConflict
	}
	store.commits++
	store.lastConsumed = consumed
	store.lastAudit = audit
	store.approved = consumed
	after := store.afterConsume
	store.mu.Unlock()
	if after != nil {
		after()
	}
	return nil
}

func (store *fakePreWriteStore) CommitCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.commits
}

type fakeRestartTarget struct {
	mu              sync.Mutex
	observation     RestartDeploymentObservation
	revalidateErr   error
	executeErr      error
	resultMutate    func(*RestartDeploymentResult)
	revalidates     int
	writes          int
	executions      []RestartDeploymentExecution
	afterRevalidate func()
}

func (target *fakeRestartTarget) RevalidateApprovedRestart(
	ctx context.Context,
	_ domain.OperationIntent,
) (RestartDeploymentObservation, error) {
	if err := ctx.Err(); err != nil {
		return RestartDeploymentObservation{}, err
	}
	target.mu.Lock()
	target.revalidates++
	observation, err, after := target.observation, target.revalidateErr, target.afterRevalidate
	target.mu.Unlock()
	if after != nil {
		after()
	}
	return observation, err
}

func (target *fakeRestartTarget) ExecuteApprovedRestart(
	ctx context.Context,
	execution RestartDeploymentExecution,
) (RestartDeploymentResult, error) {
	if err := ctx.Err(); err != nil {
		return RestartDeploymentResult{}, err
	}
	target.mu.Lock()
	defer target.mu.Unlock()
	target.writes++
	target.executions = append(target.executions, execution)
	if target.executeErr != nil {
		return RestartDeploymentResult{}, target.executeErr
	}
	result := RestartDeploymentResult{
		Scope:                   execution.Observation().Scope,
		DeploymentName:          execution.Observation().DeploymentName,
		DeploymentUID:           execution.Observation().DeploymentUID,
		PreviousResourceVersion: execution.Observation().ResourceVersion,
		ResourceVersion:         "post-restart-rv-19",
		TargetGeneration:        execution.Observation().DeploymentGeneration + 1,
		TargetReplicas:          1,
		RestartedAt:             time.UnixMilli(1_700_000_000_001).UTC(),
	}
	if target.resultMutate != nil {
		target.resultMutate(&result)
	}
	return result, nil
}

func (target *fakeRestartTarget) RevalidateCount() int {
	target.mu.Lock()
	defer target.mu.Unlock()
	return target.revalidates
}

func (target *fakeRestartTarget) WriteCount() int {
	target.mu.Lock()
	defer target.mu.Unlock()
	return target.writes
}

func (target *fakeRestartTarget) Executions() []RestartDeploymentExecution {
	target.mu.Lock()
	defer target.mu.Unlock()
	return append([]RestartDeploymentExecution(nil), target.executions...)
}

type fakeExecutionScope struct {
	mu    sync.Mutex
	scope domain.ClusterScope
}

func (scope *fakeExecutionScope) CurrentScope() (domain.ClusterScope, bool) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	return scope.scope, scope.scope.Validate() == nil
}

func (scope *fakeExecutionScope) Set(value domain.ClusterScope) {
	scope.mu.Lock()
	scope.scope = value
	scope.mu.Unlock()
}

type fixedApprovalAuditIDs struct{}

func (fixedApprovalAuditIDs) NewAuditEventID() (domain.AuditEventID, error) {
	return "00000000-0000-7000-8000-000000003099", nil
}
