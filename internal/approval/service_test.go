package approval

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestApprovalServiceLegalLifecycleTransitions(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()
	tests := []struct {
		name       string
		apply      func(*testing.T, *Service, *fakeClock, domain.ApprovalRequest) domain.ApprovalRequest
		wantState  domain.ApprovalState
		writeCount int
	}{
		{
			name: "approve",
			apply: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) domain.ApprovalRequest {
				t.Helper()
				updated, decision, err := service.Decide(context.Background(), approveCommand(request))
				if err != nil {
					t.Fatalf("Decide(approve) error = %v", err)
				}
				if decision.Choice != domain.ApprovalDecisionApprove || decision.Actor != domain.ApprovalActorLocalUser {
					t.Fatalf("approved decision = %#v", decision)
				}
				return updated
			},
			wantState: domain.ApprovalStateApproved,
		},
		{
			name: "reject by default",
			apply: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) domain.ApprovalRequest {
				t.Helper()
				command := approveCommand(request)
				command.Choice = domain.ApprovalDecisionChoice(0)
				updated, decision, err := service.Decide(context.Background(), command)
				if err != nil {
					t.Fatalf("Decide(default) error = %v", err)
				}
				if decision.Choice != domain.ApprovalDecisionReject {
					t.Fatalf("default ApprovalDecision choice = %q, want reject", decision.Choice)
				}
				return updated
			},
			wantState: domain.ApprovalStateRejected,
		},
		{
			name: "expire",
			apply: func(t *testing.T, service *Service, clock *fakeClock, request domain.ApprovalRequest) domain.ApprovalRequest {
				t.Helper()
				clock.Set(request.ExpiresAt)
				updated, err := service.Expire(context.Background(), request.ID)
				if err != nil {
					t.Fatalf("Expire() error = %v", err)
				}
				return updated
			},
			wantState: domain.ApprovalStateExpired,
		},
		{
			name: "cancel",
			apply: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) domain.ApprovalRequest {
				t.Helper()
				updated, err := service.Cancel(context.Background(), request.ID, domain.ApprovalReasonUserCancelled)
				if err != nil {
					t.Fatalf("Cancel() error = %v", err)
				}
				return updated
			},
			wantState: domain.ApprovalStateCancelled,
		},
		{
			name: "consume",
			apply: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) domain.ApprovalRequest {
				t.Helper()
				approved, _, err := service.Decide(context.Background(), approveCommand(request))
				if err != nil {
					t.Fatalf("Decide(approve) error = %v", err)
				}
				updated, err := service.Consume(context.Background(), consumeCommand(approved))
				if err != nil {
					t.Fatalf("Consume() error = %v", err)
				}
				return updated
			},
			wantState:  domain.ApprovalStateConsumed,
			writeCount: 1,
		},
	}
	for index, current := range tests {
		index, current := index, current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			service, clock, executor := newTestService(t, baseTime, testNonce(t, byte(index+1)))
			request, err := service.Request(context.Background(), testRequestCommand())
			if err != nil {
				t.Fatalf("Request() error = %v", err)
			}
			updated := current.apply(t, service, clock, request)
			if err := updated.Validate(); err != nil {
				t.Fatalf("updated ApprovalRequest.Validate() error = %v", err)
			}
			if updated.State != current.wantState || updated.State.AuditEventType() != current.wantState.AuditEventType() {
				t.Fatalf("updated state/event = %q/%q, want %q/%q", updated.State, updated.State.AuditEventType(), current.wantState, current.wantState.AuditEventType())
			}
			if executor.WriteCount() != current.writeCount {
				t.Fatalf("fake executor write count = %d, want %d", executor.WriteCount(), current.writeCount)
			}
			intents := executor.Intents()
			if current.writeCount == 1 && (len(intents) != 1 || intents[0] != request.Intent) {
				t.Fatalf("fake executor intents = %#v, want exact approved intent %#v", intents, request.Intent)
			}
		})
	}
}

func TestApprovalTTLUsesExactSixtySecondBoundary(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()
	tests := []struct {
		name      string
		advance   time.Duration
		wantState domain.ApprovalState
		wantCode  domain.ApprovalErrorCode
	}{
		{name: "immediately before", advance: domain.ApprovalExecutionTTL - time.Millisecond, wantState: domain.ApprovalStateApproved},
		{name: "at boundary", advance: domain.ApprovalExecutionTTL, wantState: domain.ApprovalStateExpired, wantCode: domain.ApprovalErrorCodeExpired},
		{name: "after boundary", advance: domain.ApprovalExecutionTTL + time.Millisecond, wantState: domain.ApprovalStateExpired, wantCode: domain.ApprovalErrorCodeExpired},
	}
	for index, current := range tests {
		index, current := index, current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			service, clock, executor := newTestService(t, baseTime, testNonce(t, byte(index+11)))
			request, err := service.Request(context.Background(), testRequestCommand())
			if err != nil {
				t.Fatalf("Request() error = %v", err)
			}
			if !request.ExpiresAt.Equal(request.RequestedAt.Add(60 * time.Second)) {
				t.Fatalf("expiry = %s, requested = %s", request.ExpiresAt, request.RequestedAt)
			}
			clock.Set(baseTime.Add(current.advance))
			updated, _, err := service.Decide(context.Background(), approveCommand(request))
			if current.wantCode == "" {
				if err != nil {
					t.Fatalf("Decide() error = %v", err)
				}
			} else {
				requireApprovalErrorCode(t, err, current.wantCode)
			}
			if updated.State != current.wantState {
				t.Fatalf("state = %q, want %q", updated.State, current.wantState)
			}
			if executor.WriteCount() != 0 {
				t.Fatalf("fake executor write count = %d, want 0", executor.WriteCount())
			}
		})
	}
}

func TestApprovalTTLBoundaryPrecedesCancelReplayAndConsume(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()
	tests := []struct {
		name    string
		prepare func(*testing.T, *Service, domain.ApprovalRequest) domain.ApprovalRequest
		apply   func(*Service, domain.ApprovalRequest) (domain.ApprovalRequest, error)
	}{
		{
			name: "cancel pending",
			prepare: func(_ *testing.T, _ *Service, request domain.ApprovalRequest) domain.ApprovalRequest {
				return request
			},
			apply: func(service *Service, request domain.ApprovalRequest) (domain.ApprovalRequest, error) {
				return service.Cancel(context.Background(), request.ID, domain.ApprovalReasonUserCancelled)
			},
		},
		{
			name: "consume pending",
			prepare: func(_ *testing.T, _ *Service, request domain.ApprovalRequest) domain.ApprovalRequest {
				return request
			},
			apply: func(service *Service, request domain.ApprovalRequest) (domain.ApprovalRequest, error) {
				return service.Consume(context.Background(), consumeCommand(request))
			},
		},
		{
			name: "duplicate approved decision",
			prepare: func(t *testing.T, service *Service, request domain.ApprovalRequest) domain.ApprovalRequest {
				t.Helper()
				approved, _, err := service.Decide(context.Background(), approveCommand(request))
				if err != nil {
					t.Fatalf("Decide() error = %v", err)
				}
				return approved
			},
			apply: func(service *Service, request domain.ApprovalRequest) (domain.ApprovalRequest, error) {
				updated, _, err := service.Decide(context.Background(), approveCommand(request))
				return updated, err
			},
		},
	}
	for index, current := range tests {
		index, current := index, current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			service, clock, executor := newTestService(t, baseTime, testNonce(t, byte(index+21)))
			request, err := service.Request(context.Background(), testRequestCommand())
			if err != nil {
				t.Fatalf("Request() error = %v", err)
			}
			request = current.prepare(t, service, request)
			clock.Set(request.ExpiresAt)
			updated, err := current.apply(service, request)
			requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeExpired)
			if updated.State != domain.ApprovalStateExpired || executor.WriteCount() != 0 {
				t.Fatalf("boundary state/write count = %q/%d, want expired/0", updated.State, executor.WriteCount())
			}
		})
	}
}

func TestApprovalDenialsInvalidateAuthorityAndNeverInvokeExecutor(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()
	tests := []struct {
		name      string
		attempt   func(*testing.T, *Service, *fakeClock, domain.ApprovalRequest) error
		wantState domain.ApprovalState
		wantCode  domain.ApprovalErrorCode
	}{
		{
			name: "not approved",
			attempt: func(_ *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) error {
				_, err := service.Consume(context.Background(), consumeCommand(request))
				return err
			},
			wantState: domain.ApprovalStatePending,
			wantCode:  domain.ApprovalErrorCodeInvalidTransition,
		},
		{
			name: "expired approval",
			attempt: func(t *testing.T, service *Service, clock *fakeClock, request domain.ApprovalRequest) error {
				t.Helper()
				approved, _, err := service.Decide(context.Background(), approveCommand(request))
				if err != nil {
					t.Fatalf("Decide() error = %v", err)
				}
				clock.Set(request.ExpiresAt)
				_, err = service.Consume(context.Background(), consumeCommand(approved))
				return err
			},
			wantState: domain.ApprovalStateExpired,
			wantCode:  domain.ApprovalErrorCodeExpired,
		},
		{
			name: "nonce mismatch",
			attempt: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) error {
				t.Helper()
				command := approveCommand(request)
				command.Nonce = testNonce(t, 90)
				_, _, err := service.Decide(context.Background(), command)
				return err
			},
			wantState: domain.ApprovalStateInvalidated,
			wantCode:  domain.ApprovalErrorCodeNonceMismatch,
		},
		{
			name: "nonce mismatch before consume",
			attempt: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) error {
				t.Helper()
				approved, _, err := service.Decide(context.Background(), approveCommand(request))
				if err != nil {
					t.Fatalf("Decide() error = %v", err)
				}
				command := consumeCommand(approved)
				command.Nonce = testNonce(t, 89)
				_, err = service.Consume(context.Background(), command)
				return err
			},
			wantState: domain.ApprovalStateInvalidated,
			wantCode:  domain.ApprovalErrorCodeNonceMismatch,
		},
		{
			name: "shown digest mismatch",
			attempt: func(_ *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) error {
				command := approveCommand(request)
				command.ShownDigest = domain.ApprovalDigest(domain.SHA256Hex("different-visible-operation"))
				_, _, err := service.Decide(context.Background(), command)
				return err
			},
			wantState: domain.ApprovalStateInvalidated,
			wantCode:  domain.ApprovalErrorCodeDigestMismatch,
		},
		{
			name: "shown digest mismatch before consume",
			attempt: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) error {
				t.Helper()
				approved, _, err := service.Decide(context.Background(), approveCommand(request))
				if err != nil {
					t.Fatalf("Decide() error = %v", err)
				}
				command := consumeCommand(approved)
				command.ShownDigest = domain.ApprovalDigest(domain.SHA256Hex("different-consumed-operation"))
				_, err = service.Consume(context.Background(), command)
				return err
			},
			wantState: domain.ApprovalStateInvalidated,
			wantCode:  domain.ApprovalErrorCodeDigestMismatch,
		},
		{
			name: "scope changed before decision",
			attempt: func(_ *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) error {
				command := approveCommand(request)
				command.CurrentScope.Generation++
				_, _, err := service.Decide(context.Background(), command)
				return err
			},
			wantState: domain.ApprovalStateInvalidated,
			wantCode:  domain.ApprovalErrorCodeStaleScope,
		},
		{
			name: "scope changed before consume",
			attempt: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) error {
				t.Helper()
				approved, _, err := service.Decide(context.Background(), approveCommand(request))
				if err != nil {
					t.Fatalf("Decide() error = %v", err)
				}
				command := consumeCommand(approved)
				command.CurrentScope.Namespace = "other-namespace"
				_, err = service.Consume(context.Background(), command)
				return err
			},
			wantState: domain.ApprovalStateInvalidated,
			wantCode:  domain.ApprovalErrorCodeStaleScope,
		},
		{
			name: "duplicate decision",
			attempt: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) error {
				t.Helper()
				if _, _, err := service.Decide(context.Background(), approveCommand(request)); err != nil {
					t.Fatalf("first Decide() error = %v", err)
				}
				_, _, err := service.Decide(context.Background(), approveCommand(request))
				return err
			},
			wantState: domain.ApprovalStateInvalidated,
			wantCode:  domain.ApprovalErrorCodeDecisionReplayed,
		},
	}
	for index, current := range tests {
		index, current := index, current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			service, clock, executor := newTestService(t, baseTime, testNonce(t, byte(index+31)))
			request, err := service.Request(context.Background(), testRequestCommand())
			if err != nil {
				t.Fatalf("Request() error = %v", err)
			}
			err = current.attempt(t, service, clock, request)
			requireApprovalErrorCode(t, err, current.wantCode)
			snapshot, ok := service.Snapshot(request.ID)
			if !ok || snapshot.State != current.wantState {
				t.Fatalf("snapshot = %#v/%t, want state %q", snapshot, ok, current.wantState)
			}
			if err := snapshot.Validate(); err != nil {
				t.Fatalf("denied ApprovalRequest.Validate() error = %v", err)
			}
			if executor.WriteCount() != 0 {
				t.Fatalf("fake executor write count = %d, want 0", executor.WriteCount())
			}
		})
	}
}

func TestOldNonceCannotAuthorizeAnotherRequest(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()
	oldNonce := testNonce(t, 61)
	newNonce := testNonce(t, 62)
	service, _, executor := newTestService(t, baseTime, oldNonce, newNonce)
	first, err := service.Request(context.Background(), testRequestCommand())
	if err != nil {
		t.Fatalf("Request(first) error = %v", err)
	}
	secondCommand := testRequestCommand()
	secondCommand.ID = "00000000-0000-7000-8000-000000003021"
	second, err := service.Request(context.Background(), secondCommand)
	if err != nil {
		t.Fatalf("Request(second) error = %v", err)
	}
	command := approveCommand(second)
	command.Nonce = first.Nonce
	updated, _, err := service.Decide(context.Background(), command)
	requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeNonceMismatch)
	if updated.State != domain.ApprovalStateInvalidated || executor.WriteCount() != 0 {
		t.Fatalf("old nonce result/write count = %q/%d", updated.State, executor.WriteCount())
	}
}

func TestProcessRestartDoesNotRestoreApprovalAuthority(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()
	oldService, _, oldExecutor := newTestService(t, baseTime, testNonce(t, 66))
	request, err := oldService.Request(context.Background(), testRequestCommand())
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	approved, _, err := oldService.Decide(context.Background(), approveCommand(request))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}

	restartedService, _, restartedExecutor := newTestService(t, baseTime, testNonce(t, 67))
	_, err = restartedService.Consume(context.Background(), consumeCommand(approved))
	requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeRequestNotFound)
	if oldExecutor.WriteCount() != 0 || restartedExecutor.WriteCount() != 0 {
		t.Fatalf("old/restarted fake executor write count = %d/%d, want 0/0", oldExecutor.WriteCount(), restartedExecutor.WriteCount())
	}
}

func TestApprovalConsumeIsSingleUseUnderConcurrency(t *testing.T) {
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()
	service, _, executor := newTestService(t, baseTime, testNonce(t, 71))
	request, err := service.Request(context.Background(), testRequestCommand())
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	approved, _, err := service.Decide(context.Background(), approveCommand(request))
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}

	const attempts = 16
	start := make(chan struct{})
	results := make(chan error, attempts)
	var group sync.WaitGroup
	for range attempts {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, consumeErr := service.Consume(context.Background(), consumeCommand(approved))
			results <- consumeErr
		}()
	}
	close(start)
	group.Wait()
	close(results)

	successes := 0
	for consumeErr := range results {
		if consumeErr == nil {
			successes++
			continue
		}
		requireApprovalErrorCode(t, consumeErr, domain.ApprovalErrorCodeInvalidTransition)
	}
	if successes != 1 || executor.WriteCount() != 1 {
		t.Fatalf("consume successes/write count = %d/%d, want 1/1", successes, executor.WriteCount())
	}
	snapshot, ok := service.Snapshot(request.ID)
	if !ok || snapshot.State != domain.ApprovalStateConsumed {
		t.Fatalf("final snapshot = %#v/%t", snapshot, ok)
	}
}

func TestApprovalCancellationAndExecutorFailureFailClosedWithoutRetry(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()

	t.Run("cancelled context before consume", func(t *testing.T) {
		service, _, executor := newTestService(t, baseTime, testNonce(t, 81))
		request, err := service.Request(context.Background(), testRequestCommand())
		if err != nil {
			t.Fatalf("Request() error = %v", err)
		}
		approved, _, err := service.Decide(context.Background(), approveCommand(request))
		if err != nil {
			t.Fatalf("Decide() error = %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		updated, err := service.Consume(ctx, consumeCommand(approved))
		requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeCancelled)
		if updated.State != domain.ApprovalStateCancelled || executor.WriteCount() != 0 {
			t.Fatalf("cancelled consume state/write count = %q/%d", updated.State, executor.WriteCount())
		}
	})

	t.Run("executor failure is consumed and not retried", func(t *testing.T) {
		service, _, executor := newTestService(t, baseTime, testNonce(t, 82))
		executor.err = errSyntheticExecution
		request, err := service.Request(context.Background(), testRequestCommand())
		if err != nil {
			t.Fatalf("Request() error = %v", err)
		}
		approved, _, err := service.Decide(context.Background(), approveCommand(request))
		if err != nil {
			t.Fatalf("Decide() error = %v", err)
		}
		updated, err := service.Consume(context.Background(), consumeCommand(approved))
		requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeExecutorFailed)
		if updated.State != domain.ApprovalStateConsumed || executor.WriteCount() != 1 {
			t.Fatalf("failed executor state/write count = %q/%d", updated.State, executor.WriteCount())
		}
		_, err = service.Consume(context.Background(), consumeCommand(approved))
		requireApprovalErrorCode(t, err, domain.ApprovalErrorCodeInvalidTransition)
		if executor.WriteCount() != 1 {
			t.Fatalf("fake executor was retried: write count = %d", executor.WriteCount())
		}
	})
}

func TestApprovalTerminatedStateMethodsNeverReverseStateOrInvokeExecutor(t *testing.T) {
	t.Parallel()
	baseTime := time.UnixMilli(1_700_000_000_000).UTC()
	tests := []struct {
		name      string
		terminate func(*testing.T, *Service, *fakeClock, domain.ApprovalRequest) domain.ApprovalRequest
	}{
		{name: "rejected", terminate: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) domain.ApprovalRequest {
			command := approveCommand(request)
			command.Choice = domain.ApprovalDecisionReject
			updated, _, err := service.Decide(context.Background(), command)
			if err != nil {
				t.Fatalf("Decide(reject) error = %v", err)
			}
			return updated
		}},
		{name: "expired", terminate: func(t *testing.T, service *Service, clock *fakeClock, request domain.ApprovalRequest) domain.ApprovalRequest {
			clock.Set(request.ExpiresAt)
			updated, err := service.Expire(context.Background(), request.ID)
			if err != nil {
				t.Fatalf("Expire() error = %v", err)
			}
			return updated
		}},
		{name: "cancelled", terminate: func(t *testing.T, service *Service, _ *fakeClock, request domain.ApprovalRequest) domain.ApprovalRequest {
			updated, err := service.Cancel(context.Background(), request.ID, domain.ApprovalReasonProcessRestarted)
			if err != nil {
				t.Fatalf("Cancel() error = %v", err)
			}
			return updated
		}},
	}
	for index, current := range tests {
		index, current := index, current
		t.Run(current.name, func(t *testing.T) {
			t.Parallel()
			service, clock, executor := newTestService(t, baseTime, testNonce(t, byte(index+91)))
			request, err := service.Request(context.Background(), testRequestCommand())
			if err != nil {
				t.Fatalf("Request() error = %v", err)
			}
			terminated := current.terminate(t, service, clock, request)
			originalState := terminated.State
			if _, _, err := service.Decide(context.Background(), approveCommand(request)); err == nil {
				t.Fatal("Decide() on terminated request error = nil")
			}
			if _, err := service.Consume(context.Background(), consumeCommand(request)); err == nil {
				t.Fatal("Consume() on terminated request error = nil")
			}
			snapshot, ok := service.Snapshot(request.ID)
			if !ok || snapshot.State != originalState || executor.WriteCount() != 0 {
				t.Fatalf("terminated snapshot/write count = %#v/%t/%d", snapshot, ok, executor.WriteCount())
			}
		})
	}
}
