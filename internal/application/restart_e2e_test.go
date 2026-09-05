package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestRestartApprovalEndToEndWriteActionMatrix(t *testing.T) {
	t.Run("reject", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		request := fixture.submit(t, 100)
		result, err := fixture.coordinator.Decide(
			context.Background(), approvalDecisionCommand(UICommandRejectAction, request, 100, 1),
		)
		if err != nil || result.State != domain.ApprovalStateRejected || fixture.executor.calls != 0 ||
			fixture.rollout.calls != 0 || fixture.persistence.consumes != 0 {
			t.Fatalf("reject result/error/write/rollout/consume = %#v/%v/%d/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls, fixture.persistence.consumes)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		request := fixture.submit(t, 101)
		fixture.clock.set(request.ExpiresAt)
		result, err := fixture.coordinator.Decide(
			context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 101, 2),
		)
		if !errors.Is(err, ErrApprovalExpired) || result.State != domain.ApprovalStateExpired ||
			fixture.executor.calls != 0 || fixture.rollout.calls != 0 || fixture.persistence.consumes != 0 {
			t.Fatalf("expiry result/error/write/rollout/consume = %#v/%v/%d/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls, fixture.persistence.consumes)
		}
	})

	t.Run("replayed decision", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		request := fixture.submit(t, 112)
		command := approvalDecisionCommand(UICommandApproveAction, request, 112, 112)
		approved, err := fixture.coordinator.Decide(context.Background(), command)
		if err != nil || approved.State != domain.ApprovalStateApproved {
			t.Fatalf("initial approval result/error = %#v/%v", approved, err)
		}
		replayed, err := fixture.coordinator.Decide(context.Background(), command)
		if !errors.Is(err, ErrApprovalInvalidated) || replayed.State != domain.ApprovalStateInvalidated ||
			fixture.executor.calls != 0 || fixture.rollout.calls != 0 || fixture.persistence.consumes != 0 {
			t.Fatalf("replay result/error/write/rollout/consume = %#v/%v/%d/%d/%d", replayed, err, fixture.executor.calls, fixture.rollout.calls, fixture.persistence.consumes)
		}
	})

	t.Run("process restart invalidation", func(t *testing.T) {
		old := newApprovalCoordinatorFixture(t)
		request := old.submit(t, 113)
		command := approvalDecisionCommand(UICommandApproveAction, request, 113, 113)
		approved, err := old.coordinator.Decide(context.Background(), command)
		if err != nil || approved.State != domain.ApprovalStateApproved {
			t.Fatalf("pre-restart approval result/error = %#v/%v", approved, err)
		}
		stored, err := approval.NewStoredRequest(old.persistence.lastCreated)
		if err != nil {
			t.Fatalf("approval.NewStoredRequest() error = %v", err)
		}
		old.persistence.recoverable = []approval.StoredRequest{stored}
		restartedExecutor := &fakeApprovalExecutor{}
		restartedRollout := newFakeApprovalRolloutObserver(old.clock.Now().Add(2 * time.Millisecond))
		restartedIDs := &approvalCoordinatorIDs{}
		restartedService, err := approval.NewService(approval.ServiceConfig{
			Clock: old.clock, Nonces: approvalNonceSource{value: 0x73}, Store: old.persistence,
			AuditIDs: restartedIDs,
		})
		if err != nil {
			t.Fatalf("approval.NewService(restarted) error = %v", err)
		}
		restartedPermissions, err := NewPermissionManager(PermissionPolicy{
			Profile: domain.PermissionProfileAsk, Generation: 1,
		})
		if err != nil {
			t.Fatalf("NewPermissionManager(restarted) error = %v", err)
		}
		if err := restartedPermissions.BindSession(old.sessionID); err != nil {
			t.Fatalf("BindSession(restarted) error = %v", err)
		}
		restarted, err := NewApprovalCoordinator(ApprovalCoordinatorConfig{
			Service: restartedService, Persistence: old.persistence, ResultAudits: old.persistence,
			Scope: old.scope, ApprovalIDs: restartedIDs, AuditIDs: restartedIDs,
			UIEvents: &fakeApprovalUIEvents{}, Rollout: restartedRollout, Now: old.clock.Now,
			RestartRevalidator: restartedExecutor, RestartExecutor: restartedExecutor,
			Permissions: restartedPermissions, Reviews: old.persistence,
		})
		if err != nil {
			t.Fatalf("NewApprovalCoordinator(restarted) error = %v", err)
		}
		if err := restarted.Recover(context.Background()); err != nil {
			t.Fatalf("Recover() error = %v", err)
		}
		if len(old.persistence.recovered) != 1 ||
			old.persistence.recovered[0].After.StateReason != domain.ApprovalReasonProcessRestarted {
			t.Fatalf("restart recovery transitions = %#v", old.persistence.recovered)
		}
		if _, err := restarted.ConsumeApprovedRestart(context.Background(), command); !errors.Is(err, ErrApprovalUnavailable) {
			t.Fatalf("restarted ConsumeApprovedRestart() error = %v", err)
		}
		if old.executor.calls != 0 || restartedExecutor.calls != 0 || restartedRollout.calls != 0 {
			t.Fatalf("old/restarted writes/rollout = %d/%d/%d, want 0/0/0", old.executor.calls, restartedExecutor.calls, restartedRollout.calls)
		}
	})

	t.Run("approved success", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		result, err := approveAndConsumeRestart(t, fixture, 102)
		if err != nil || result.Execution == nil || result.Execution.State != UIRestartRolloutSucceeded ||
			fixture.executor.calls != 1 || fixture.rollout.calls != 1 || fixture.persistence.consumes != 1 {
			t.Fatalf("success result/error/write/rollout/consume = %#v/%v/%d/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls, fixture.persistence.consumes)
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
			[]string{"patch_accepted", "rollout_progress", "rollout_succeeded"})
		assertRestartUIStates(t, fixture.ui.events, []UIRestartExecutionState{
			UIRestartPatchAccepted, UIRestartRolloutProgress, UIRestartRolloutSucceeded,
		})
		_, replayErr := fixture.coordinator.ConsumeApprovedRestart(
			context.Background(), approvalDecisionCommand(UICommandApproveAction, fixture.persistence.lastCreated, 102, 102),
		)
		if !errors.Is(replayErr, ErrApprovalUnavailable) || fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("replay error/write/rollout = %v/%d/%d, want unavailable/1/1", replayErr, fixture.executor.calls, fixture.rollout.calls)
		}
	})

	for _, test := range []struct {
		name       string
		class      domain.SafeErrorClass
		wantDetail string
	}{
		{name: "object changed", class: domain.SafeErrorClassConflict},
		{name: "RBAC forbidden before write", class: domain.SafeErrorClassPermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			fixture.executor.revalidateErr = classifiedApprovalTestError{class: test.class, message: "synthetic safe revalidation failure"}
			result, err := approveAndConsumeRestart(t, fixture, 103)
			if !errors.Is(err, ErrApprovalInvalidated) || result.State != domain.ApprovalStateInvalidated ||
				fixture.executor.calls != 0 || fixture.rollout.calls != 0 || fixture.persistence.consumes != 0 {
				t.Fatalf("pre-write denial result/error/write/rollout/consume = %#v/%v/%d/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls, fixture.persistence.consumes)
			}
		})
	}

	t.Run("Context switch before write", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		request := fixture.submit(t, 120)
		command := approvalDecisionCommand(UICommandApproveAction, request, 120, 120)
		approved, err := fixture.coordinator.Decide(context.Background(), command)
		if err != nil || approved.State != domain.ApprovalStateApproved {
			t.Fatalf("pre-switch approval result/error = %#v/%v", approved, err)
		}
		fixture.scope.scope.Context = "other-context"
		fixture.scope.scope.Generation++
		result, err := fixture.coordinator.ConsumeApprovedRestart(context.Background(), command)
		if !errors.Is(err, ErrApprovalInvalidated) || result.State != domain.ApprovalStateInvalidated ||
			fixture.executor.calls != 0 || fixture.rollout.calls != 0 || fixture.persistence.consumes != 0 {
			t.Fatalf("pre-write Context switch result/error/write/rollout/consume = %#v/%v/%d/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls, fixture.persistence.consumes)
		}
	})

	for _, test := range []struct {
		name  string
		class domain.SafeErrorClass
	}{
		{name: "RBAC forbidden PATCH", class: domain.SafeErrorClassPermissionDenied},
		{name: "PATCH conflict", class: domain.SafeErrorClassConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			fixture.executor.err = classifiedApprovalTestError{class: test.class, message: "synthetic safe PATCH rejection"}
			result, err := approveAndConsumeRestart(t, fixture, 104)
			if !errors.Is(err, ErrApprovalExecutionFailed) || result.Execution == nil ||
				result.Execution.State != UIRestartPatchFailed || result.Execution.ErrorClass != test.class ||
				fixture.executor.calls != 1 || fixture.rollout.calls != 0 || fixture.persistence.consumes != 1 {
				t.Fatalf("PATCH rejection result/error/write/rollout/consume = %#v/%v/%d/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls, fixture.persistence.consumes)
			}
			assertRestartAuditDetails(t, fixture.persistence.writeResultAudits, []string{"patch_failed"})
		})
	}

	t.Run("pre-write audit failure", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.persistence.consumeErr = errors.New("synthetic pre-write audit failure")
		result, err := approveAndConsumeRestart(t, fixture, 105)
		if !errors.Is(err, ErrApprovalPersistenceUnavailable) || result.State != domain.ApprovalStateInvalidated ||
			fixture.executor.calls != 0 || fixture.rollout.calls != 0 || fixture.persistence.consumes != 0 {
			t.Fatalf("pre-write audit result/error/write/rollout/consume = %#v/%v/%d/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls, fixture.persistence.consumes)
		}
	})

	for _, test := range []struct {
		name       string
		state      RestartRolloutState
		failure    RestartRolloutFailureCode
		wantState  UIRestartExecutionState
		wantErr    error
		wantDetail string
	}{
		{
			name: "rollout failure", state: RestartRolloutFailed,
			failure: RestartRolloutFailureProgressDeadline, wantState: UIRestartRolloutFailed,
			wantErr: ErrApprovalRolloutFailed, wantDetail: "rollout_failed",
		},
		{
			name: "rollout timeout", state: RestartRolloutTimedOut,
			wantState: UIRestartRolloutTimedOut, wantErr: ErrApprovalRolloutTimedOut,
			wantDetail: "rollout_timed_out",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			fixture.rollout.state, fixture.rollout.failureCode = test.state, test.failure
			result, err := approveAndConsumeRestart(t, fixture, 106)
			if !errors.Is(err, test.wantErr) || result.Execution == nil || result.Execution.State != test.wantState ||
				fixture.executor.calls != 1 || fixture.rollout.calls != 1 || fixture.persistence.consumes != 1 {
				t.Fatalf("rollout result/error/write/rollout/consume = %#v/%v/%d/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls, fixture.persistence.consumes)
			}
			assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
				[]string{"patch_accepted", "rollout_progress", test.wantDetail})
		})
	}

	t.Run("rollout timeout before first completed read", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.rollout.noObservations = true
		result, err := approveAndConsumeRestart(t, fixture, 118)
		if !errors.Is(err, ErrApprovalRolloutTimedOut) || result.Execution == nil ||
			result.Execution.State != UIRestartRolloutTimedOut || result.Execution.TargetGeneration != 0 ||
			result.Execution.TargetReplicas != 0 || fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("zero-observation timeout result/error/write/rollout = %#v/%v/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls)
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
			[]string{"patch_accepted", "rollout_timed_out"})
		assertRestartUIStates(t, fixture.ui.events, []UIRestartExecutionState{
			UIRestartPatchAccepted, UIRestartRolloutTimedOut,
		})
	})

	t.Run("Context switch during rollout", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.rollout.beforeProgress = func() {
			fixture.scope.scope.Generation++
		}
		result, err := approveAndConsumeRestart(t, fixture, 107)
		if !errors.Is(err, ErrApprovalRolloutUnavailable) || result.Execution == nil ||
			result.Execution.State != UIRestartRolloutUnavailable || result.Execution.ErrorClass != domain.SafeErrorClassStaleScope ||
			fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("Context switch result/error/write/rollout = %#v/%v/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls)
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
			[]string{"patch_accepted", "rollout_unavailable"})
	})

	t.Run("Context switch before terminal acceptance", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.rollout.beforeReturn = func() {
			fixture.scope.scope.Generation++
		}
		result, err := approveAndConsumeRestart(t, fixture, 117)
		if !errors.Is(err, ErrApprovalRolloutUnavailable) || result.Execution == nil ||
			result.Execution.State != UIRestartRolloutUnavailable || result.Execution.ErrorClass != domain.SafeErrorClassStaleScope ||
			fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("terminal Context switch result/error/write/rollout = %#v/%v/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls)
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
			[]string{"patch_accepted", "rollout_progress", "rollout_unavailable"})
	})

	t.Run("rollout cancellation", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.rollout.err = context.Canceled
		result, err := approveAndConsumeRestart(t, fixture, 115)
		if !errors.Is(err, ErrApprovalRolloutUnavailable) || result.Execution == nil ||
			result.Execution.State != UIRestartRolloutUnavailable || result.Execution.ErrorClass != domain.SafeErrorClassCancelled ||
			fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("rollout cancellation result/error/write/rollout = %#v/%v/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls)
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
			[]string{"patch_accepted", "rollout_unavailable"})
	})

	t.Run("result audit failure", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.persistence.writeResultFailures = maxApprovalResultAuditAttempts
		result, err := approveAndConsumeRestart(t, fixture, 108)
		if !errors.Is(err, ErrApprovalResultAuditUnavailable) || result.Execution == nil ||
			result.Execution.State != UIRestartResultAuditFailed || fixture.persistence.writeResultCalls != 3 ||
			fixture.executor.calls != 1 || fixture.rollout.calls != 0 {
			t.Fatalf("result audit result/error/audit calls/write/rollout = %#v/%v/%d/%d/%d", result, err, fixture.persistence.writeResultCalls, fixture.executor.calls, fixture.rollout.calls)
		}
	})

	t.Run("terminal result audit failure", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.persistence.writeResultFailAfter = 2
		result, err := approveAndConsumeRestart(t, fixture, 114)
		if !errors.Is(err, ErrApprovalResultAuditUnavailable) || result.Execution == nil ||
			result.Execution.State != UIRestartResultAuditFailed || fixture.persistence.writeResultCalls != 5 ||
			fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("terminal audit result/error/audit calls/write/rollout = %#v/%v/%d/%d/%d", result, err, fixture.persistence.writeResultCalls, fixture.executor.calls, fixture.rollout.calls)
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
			[]string{"patch_accepted", "rollout_progress"})
		assertRestartUIStates(t, fixture.ui.events, []UIRestartExecutionState{
			UIRestartPatchAccepted, UIRestartRolloutProgress, UIRestartResultAuditFailed,
		})
	})

	t.Run("transient result audit failure", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.persistence.writeResultFailures = maxApprovalResultAuditAttempts - 1
		result, err := approveAndConsumeRestart(t, fixture, 110)
		if err != nil || result.Execution == nil || result.Execution.State != UIRestartRolloutSucceeded ||
			fixture.persistence.writeResultCalls != 5 || fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("transient audit result/error/audit calls/write/rollout = %#v/%v/%d/%d/%d", result, err, fixture.persistence.writeResultCalls, fixture.executor.calls, fixture.rollout.calls)
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
			[]string{"patch_accepted", "rollout_progress", "rollout_succeeded"})
	})

	t.Run("scope changes after PATCH dispatch", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.executor.err = classifiedApprovalTestError{
			class: domain.SafeErrorClassStaleScope, message: "synthetic post-dispatch scope change",
		}
		result, err := approveAndConsumeRestart(t, fixture, 111)
		if !errors.Is(err, ErrApprovalPatchOutcomeUnknown) || result.Execution == nil ||
			result.Execution.State != UIRestartPatchOutcomeUnknown || fixture.executor.calls != 1 || fixture.rollout.calls != 0 {
			t.Fatalf("post-dispatch scope result/error/write/rollout = %#v/%v/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls)
		}
	})

	t.Run("ambiguous PATCH sink canary", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		const canary = "raw-vendor-write-canary-7f88"
		fixture.executor.err = classifiedApprovalTestError{class: domain.SafeErrorClassTimeout, message: canary}
		result, err := approveAndConsumeRestart(t, fixture, 109)
		if !errors.Is(err, ErrApprovalPatchOutcomeUnknown) || result.Execution == nil ||
			result.Execution.State != UIRestartPatchOutcomeUnknown || fixture.executor.calls != 1 || fixture.rollout.calls != 0 {
			t.Fatalf("ambiguous PATCH result/error/write/rollout = %#v/%v/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls)
		}
		for _, sink := range []any{
			result, fixture.persistence.lastConsumed, fixture.persistence.lastConsumeAudit,
			fixture.persistence.writeResultAudits, fixture.ui.events, err,
		} {
			if strings.Contains(fmt.Sprintf("%v", sink), canary) {
				t.Fatalf("raw vendor canary reached a result, audit, UI, or returned error sink: %T", sink)
			}
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits, []string{"patch_outcome_unknown"})
	})

	t.Run("rollout error sink canary", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		const canary = "raw-vendor-rollout-canary-98ac"
		fixture.rollout.err = classifiedApprovalTestError{class: domain.SafeErrorClassUnavailable, message: canary}
		result, err := approveAndConsumeRestart(t, fixture, 116)
		if !errors.Is(err, ErrApprovalRolloutUnavailable) || result.Execution == nil ||
			result.Execution.State != UIRestartRolloutUnavailable || result.Execution.ErrorClass != domain.SafeErrorClassUnavailable ||
			fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("rollout error result/error/write/rollout = %#v/%v/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls)
		}
		for _, sink := range []any{result, fixture.persistence.writeResultAudits, fixture.ui.events, err} {
			if strings.Contains(fmt.Sprintf("%v", sink), canary) {
				t.Fatalf("raw rollout canary reached a result, audit, UI, or returned error sink: %T", sink)
			}
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
			[]string{"patch_accepted", "rollout_unavailable"})
	})

	t.Run("PATCH response metadata sink canary", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		const freshCanary = "fresh-resource-version-canary-41dd"
		const resultCanary = "result-resource-version-canary-61ab"
		fixture.intent.Target.Resource.ResourceVersion = freshCanary
		fixture.executor.revalidateRV = freshCanary
		fixture.executor.resultRV = resultCanary
		result, err := approveAndConsumeRestart(t, fixture, 121)
		if err != nil || result.Execution == nil || result.Execution.State != UIRestartRolloutSucceeded ||
			fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("metadata canary result/error/write/rollout = %#v/%v/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls)
		}
		for _, sink := range []any{result, fixture.ui.events, err} {
			formatted := fmt.Sprintf("%v", sink)
			if strings.Contains(formatted, freshCanary) || strings.Contains(formatted, resultCanary) {
				t.Fatalf("resource-version canary reached a result, UI, or returned error sink: %T", sink)
			}
		}
		if !strings.Contains(fmt.Sprintf("%v", fixture.persistence.lastConsumed), freshCanary) {
			t.Fatal("the exact approved target resource version was not retained in durable authority metadata")
		}
		for _, sink := range []any{fixture.persistence.lastConsumed, fixture.persistence.lastConsumeAudit, fixture.persistence.writeResultAudits} {
			if strings.Contains(fmt.Sprintf("%v", sink), resultCanary) {
				t.Fatalf("unvalidated PATCH response resource version reached durable metadata: %T", sink)
			}
		}
	})

	t.Run("noncanonical first rollout projection", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		fixture.rollout.progressNumber = 2
		result, err := approveAndConsumeRestart(t, fixture, 119)
		if !errors.Is(err, ErrApprovalRolloutUnavailable) || result.Execution == nil ||
			result.Execution.State != UIRestartRolloutUnavailable ||
			result.Execution.ErrorClass != domain.SafeErrorClassInvalidExternalResponse ||
			fixture.executor.calls != 1 || fixture.rollout.calls != 1 {
			t.Fatalf("noncanonical rollout result/error/write/rollout = %#v/%v/%d/%d", result, err, fixture.executor.calls, fixture.rollout.calls)
		}
		assertRestartAuditDetails(t, fixture.persistence.writeResultAudits,
			[]string{"patch_accepted", "rollout_unavailable"})
	})
}

func TestRestartRolloutContractsRejectCrossRequestAndConflatedPatchProjections(t *testing.T) {
	request := RestartRolloutRequest{
		Scope:          domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
		DeploymentName: "sample-deployment", DeploymentUID: "sample-deployment-uid",
		TargetGeneration: 9, TargetReplicas: 3,
	}
	observation := RestartRolloutObservation{
		Scope: request.Scope, DeploymentName: request.DeploymentName, DeploymentUID: request.DeploymentUID,
		DeploymentGeneration: 9, TargetGeneration: request.TargetGeneration,
		ObservedGeneration: 8, UpdatedReplicas: 2, AvailableReplicas: 1,
		TargetReplicas: request.TargetReplicas, ObservationNumber: 1,
		State: RestartRolloutProgress, ObservedAt: time.UnixMilli(1_700_000_700_001).UTC(),
	}
	result := RestartRolloutResult{
		State: RestartRolloutTimedOut, Final: observation, ObservationCount: 1,
		StartedAt: time.UnixMilli(1_700_000_700_000).UTC(), FinishedAt: observation.ObservedAt,
	}
	if result.ValidateFor(request) != nil {
		t.Fatal("valid request-bound rollout result was rejected")
	}
	mutations := []func(*RestartRolloutObservation){
		func(value *RestartRolloutObservation) { value.Scope.Generation++ },
		func(value *RestartRolloutObservation) { value.DeploymentName = "other-deployment" },
		func(value *RestartRolloutObservation) { value.DeploymentUID = "other-uid" },
		func(value *RestartRolloutObservation) { value.TargetGeneration++ },
		func(value *RestartRolloutObservation) { value.TargetReplicas++ },
	}
	for index, mutate := range mutations {
		changed := result
		mutate(&changed.Final)
		if changed.ValidateFor(request) == nil {
			t.Fatalf("cross-request rollout mutation %d was accepted", index)
		}
	}
	invalidShapes := []func(*RestartRolloutObservation){
		func(value *RestartRolloutObservation) { value.ObservedGeneration = value.DeploymentGeneration + 1 },
		func(value *RestartRolloutObservation) { value.DeploymentGeneration++ },
		func(value *RestartRolloutObservation) { value.ObservationNumber = MaxRestartRolloutObservations + 1 },
		func(value *RestartRolloutObservation) {
			value.State = RestartRolloutFailed
			value.FailureCode = RestartRolloutFailureProgressDeadline
		},
	}
	for index, mutate := range invalidShapes {
		changed := observation
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatalf("invalid rollout shape %d was accepted", index)
		}
	}

	patchEvent := UIRestartExecution{
		RequestID: "00000000-0000-7000-8000-000000008801",
		RunID:     "00000000-0000-7000-8000-000000008802", ScopeGeneration: 7,
		Sequence: 2, EventIndex: 1, Digest: domain.ApprovalDigest(fmt.Sprintf("%064d", 9)),
		State: UIRestartPatchAccepted, TargetGeneration: 9, TargetReplicas: 3,
	}
	if patchEvent.Validate() != nil {
		t.Fatal("valid PATCH acceptance event was rejected")
	}
	patchEvent.UpdatedReplicas = 1
	if patchEvent.Validate() == nil {
		t.Fatal("PATCH acceptance event carried rollout progress")
	}
	patchEvent.UpdatedReplicas = 0
	patchEvent.EventIndex = MaxRestartRolloutObservations + 3
	if patchEvent.Validate() == nil {
		t.Fatal("restart execution event exceeded the fixed event ceiling")
	}
}

func approveAndConsumeRestart(
	t *testing.T,
	fixture *approvalCoordinatorFixture,
	sequence int64,
) (UIApprovalResult, error) {
	t.Helper()
	request := fixture.submit(t, sequence)
	command := approvalDecisionCommand(UICommandApproveAction, request, sequence, uint64(sequence))
	approved, err := fixture.coordinator.Decide(context.Background(), command)
	if err != nil || approved.State != domain.ApprovalStateApproved || fixture.executor.calls != 0 {
		t.Fatalf("Decide(approve) result/error/write = %#v/%v/%d", approved, err, fixture.executor.calls)
	}
	return fixture.coordinator.ConsumeApprovedRestart(context.Background(), command)
}

func assertRestartAuditDetails(t *testing.T, events []domain.AuditEvent, want []string) {
	t.Helper()
	got := make([]string, 0, len(events))
	for _, event := range events {
		if event.Validate() != nil || event.Details.DetailCode == nil {
			t.Fatalf("invalid write result audit = %#v", event)
		}
		detail := *event.Details.DetailCode
		got = append(got, detail)
		wantType, wantOutcome, wantCount, wantErrorClass := restartAuditShape(detail)
		if event.Type != wantType || event.Outcome != wantOutcome ||
			(event.Details.Count != nil) != wantCount ||
			(event.Details.ErrorClass != nil) != wantErrorClass {
			t.Fatalf("write result audit shape for %q = %#v", detail, event)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("write result audit details = %v, want %v", got, want)
	}
}

func restartAuditShape(detail string) (domain.AuditEventType, domain.AuditOutcome, bool, bool) {
	switch detail {
	case "patch_accepted":
		return domain.AuditEventWriteAttempted, domain.AuditOutcomeSuccess, false, false
	case "patch_failed":
		return domain.AuditEventWriteAttempted, domain.AuditOutcomeFailure, false, true
	case "patch_outcome_unknown":
		return domain.AuditEventWriteOutcomeUnknown, domain.AuditOutcomeUnknown, false, true
	case "rollout_progress":
		return domain.AuditEventWriteAttempted, domain.AuditOutcomeSuccess, true, false
	case "rollout_succeeded":
		return domain.AuditEventWriteVerified, domain.AuditOutcomeSuccess, true, false
	case "rollout_timed_out":
		return domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeUnknown, true, false
	case "rollout_failed":
		return domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeFailure, true, false
	case "rollout_unavailable":
		return domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeUnknown, false, true
	default:
		return "", "", false, false
	}
}

func assertRestartUIStates(t *testing.T, events []UIEvent, want []UIRestartExecutionState) {
	t.Helper()
	got := make([]UIRestartExecutionState, 0, len(want))
	for _, event := range events {
		if event.Kind == UIEventRestartExecution {
			got = append(got, event.RestartExecution.State)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("restart UI states = %v, want %v", got, want)
	}
}

type classifiedApprovalTestError struct {
	class   domain.SafeErrorClass
	message string
}

func (err classifiedApprovalTestError) Error() string                { return err.message }
func (err classifiedApprovalTestError) Class() domain.SafeErrorClass { return err.class }
