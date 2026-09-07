package application

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestApprovalCoordinatorPersistsProposalBeforePublishingDialog(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request, err := fixture.coordinator.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 12, fixture.intent,
	)
	if err != nil {
		t.Fatalf("SubmitRestartDeploymentProposal() error = %v", err)
	}
	if request.State != domain.ApprovalStatePending || fixture.persistence.creates != 1 || len(fixture.ui.events) != 1 {
		t.Fatalf("request/create/events = %#v/%d/%d", request, fixture.persistence.creates, len(fixture.ui.events))
	}
	event := fixture.ui.events[0]
	if event.Validate() != nil || event.Kind != UIEventApprovalRequested || event.Approval == nil ||
		event.Approval.RequestID != request.ID || event.Approval.Digest != request.Digest ||
		event.Approval.ReasonSummary != fixture.intent.ReasonSummary || event.Approval.RiskSummary != domain.RestartDeploymentRiskSummary {
		t.Fatalf("approval UI event = %#v", event)
	}
	if fixture.persistence.lastCreateAudit.Validate() != nil ||
		fixture.persistence.lastCreateAudit.Type != domain.AuditEventApprovalRequested ||
		fixture.persistence.lastCreateAudit.IntegrityHash != string(request.Digest) {
		t.Fatalf("request audit = %#v", fixture.persistence.lastCreateAudit)
	}
	if fixture.executor.calls != 0 {
		t.Fatalf("fake executor calls = %d, want 0", fixture.executor.calls)
	}
}

func TestCoordinatorPermissionCommandsAreLocalTypedAndGenerationBound(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	outer := &Coordinator{
		approvals: fixture.coordinator, runResourcePolicies: coordinatorResourcePolicies{},
		budgetLimits: agent.DefaultRunBudgetLimits(), now: fixture.clock.Now,
	}
	changedContext, cancelChanged := context.WithCancel(context.Background())
	defer cancelChanged()
	outer.active = &activeRun{cancel: cancelChanged}
	before := struct {
		creates, resolves, closes, consumes, reviews, revalidates, executes int
	}{
		fixture.persistence.creates, fixture.persistence.resolves, fixture.persistence.closes,
		fixture.persistence.consumes, fixture.persistence.actionReviewCalls,
		fixture.executor.revalidates, fixture.executor.calls,
	}
	shown, err := outer.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandShowPermissions, RequestID: 901,
	})
	after := struct {
		creates, resolves, closes, consumes, reviews, revalidates, executes int
	}{
		fixture.persistence.creates, fixture.persistence.resolves, fixture.persistence.closes,
		fixture.persistence.consumes, fixture.persistence.actionReviewCalls,
		fixture.executor.revalidates, fixture.executor.calls,
	}
	if err != nil || shown.Validate() != nil || before != after || shown.Permissions == nil ||
		shown.Permissions.Permission.Profile != domain.PermissionProfileAsk ||
		shown.Permissions.Permission.PolicyGeneration != 1 || shown.Permissions.Changed || shown.Permissions.RuleCreated {
		t.Fatalf("show permissions = %#v/%v, effects %#v/%#v", shown, err, before, after)
	}

	changed, err := outer.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandChangePermission, RequestID: 902, ExpectedPolicyGeneration: 1,
		PermissionProfile: domain.PermissionProfileReadOnly,
	})
	if err != nil || changed.Validate() != nil || changed.Permissions == nil || !changed.Permissions.Changed ||
		changed.Permissions.RuleCreated || changed.Permissions.Permission.Profile != domain.PermissionProfileReadOnly ||
		changed.Permissions.Permission.PolicyGeneration != 2 {
		t.Fatalf("change permission = %#v/%v", changed, err)
	}
	if fixture.persistence.creates != before.creates || fixture.persistence.resolves != before.resolves ||
		fixture.persistence.consumes != before.consumes || fixture.persistence.actionReviewCalls != before.reviews ||
		fixture.executor.revalidates != before.revalidates || fixture.executor.calls != before.executes {
		t.Fatal("permission change invoked a proposal, Reviewer, target validation, or executor")
	}
	select {
	case <-changedContext.Done():
	default:
		t.Fatal("successful permission change did not cancel the old run")
	}
	staleContext, cancelStale := context.WithCancel(context.Background())
	defer cancelStale()
	outer.active = &activeRun{cancel: cancelStale}
	if _, err := outer.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandChangePermission, RequestID: 903, ExpectedPolicyGeneration: 1,
		PermissionProfile: domain.PermissionProfileAsk,
	}); !errors.Is(err, ErrPermissionStale) {
		t.Fatalf("stale permission change error = %v", err)
	}
	select {
	case <-staleContext.Done():
		t.Fatal("stale permission change cancelled current-generation work")
	default:
	}
	if snapshot := fixture.coordinator.UIPermissionSnapshot(); snapshot.Profile != domain.PermissionProfileReadOnly ||
		snapshot.PolicyGeneration != 2 || !snapshot.Healthy {
		t.Fatalf("permission snapshot = %#v", snapshot)
	}
}

func TestCoordinatorPermissionPostCommitFailureStillCancelsOldRun(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	fixture.submit(t, 17)
	fixture.persistence.closeErr = errors.New("synthetic policy invalidation persistence failure")
	outer := &Coordinator{
		approvals: fixture.coordinator, runResourcePolicies: coordinatorResourcePolicies{},
		budgetLimits: agent.DefaultRunBudgetLimits(), now: fixture.clock.Now,
	}
	runContext, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	outer.active = &activeRun{cancel: cancelRun}

	_, err := outer.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandChangePermission, RequestID: 903, ExpectedPolicyGeneration: 1,
		PermissionProfile: domain.PermissionProfileReadOnly,
	})
	if !errors.Is(err, ErrPermissionUnavailable) {
		t.Fatalf("post-commit permission change error = %v", err)
	}
	select {
	case <-runContext.Done():
	default:
		t.Fatal("post-commit permission failure did not cancel old-generation work")
	}
	status, action := fixture.coordinator.Status()
	if status.PolicyGeneration != 2 || status.Healthy || action != nil || fixture.executor.calls != 0 {
		t.Fatalf("post-commit status/action/executor = %#v/%#v/%d", status, action, fixture.executor.calls)
	}
}

func TestCoordinatorSessionRuleCommandInvalidatesCurrentActionBeforeFutureMatch(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 18)
	outer := &Coordinator{
		approvals: fixture.coordinator, runResourcePolicies: coordinatorResourcePolicies{},
		budgetLimits: agent.DefaultRunBudgetLimits(), now: fixture.clock.Now,
	}
	runContext, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	outer.active = &activeRun{cancel: cancelRun}
	command := approvalDecisionCommand(UICommandCreateSessionRule, request, 18, 904)
	outcome, err := outer.ExecuteUICommand(context.Background(), command)
	if err != nil || outcome.Validate() != nil || outcome.Permissions == nil || !outcome.Permissions.RuleCreated ||
		outcome.Permissions.Changed || outcome.Permissions.Permission.PolicyGeneration != 2 ||
		len(outcome.Permissions.Permission.SessionRules) != 1 || outcome.Permissions.Action != nil {
		t.Fatalf("create Session rule = %#v/%v", outcome, err)
	}
	if fixture.persistence.closes != 1 || fixture.persistence.lastClosed.ID != request.ID ||
		fixture.persistence.lastClosed.State != domain.ApprovalStateInvalidated ||
		fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonPolicyChanged || fixture.executor.calls != 0 {
		t.Fatalf("source action close/executor = %#v/%d", fixture.persistence.lastClosed, fixture.executor.calls)
	}
	select {
	case <-runContext.Done():
	default:
		t.Fatal("successful Session rule creation did not cancel old-generation work")
	}
	statusRule := outcome.Permissions.Permission.SessionRules[0]
	if statusRule.Scope != request.Intent.Scope || statusRule.NamespaceAccess != request.Intent.NamespaceAccess ||
		statusRule.PolicyGeneration != 2 || statusRule.ParameterSummary == "" ||
		statusRule.Effect != request.Intent.Effect || statusRule.Risk != domain.RiskReview ||
		statusRule.DataCategories != request.Intent.DataCategories || statusRule.AllowedSinks != request.Intent.AllowedSinks ||
		statusRule.NetworkEffects != request.Intent.NetworkEffects || statusRule.Limits != request.Intent.Limits ||
		statusRule.CreatedAtMillis <= 0 || statusRule.ExpiresAtMillis <= statusRule.CreatedAtMillis {
		t.Fatalf("Session rule status omitted an exact binding: %#v", statusRule)
	}

	fixture.intent.PolicyGeneration = 2
	future, err := fixture.coordinator.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 19, fixture.intent,
	)
	if err != nil || future.State != domain.ApprovalStatePending || fixture.persistence.lastDecision.Actor != domain.ApprovalActorSessionRule ||
		fixture.persistence.lastDecision.Disposition != domain.ReviewDispositionAutomatic || fixture.executor.calls != 1 {
		t.Fatalf("future exact Session-rule match = %#v/%v decision=%#v executor=%d", future, err, fixture.persistence.lastDecision, fixture.executor.calls)
	}
}

func TestSessionRuleCreationExcludesConcurrentActionDecision(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 20)
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	fixture.identifiers.permissionRuleStarted = started
	fixture.identifiers.permissionRuleRelease = release
	ruleResult := make(chan error, 1)
	go func() {
		ruleResult <- fixture.coordinator.CreateSessionRule(
			context.Background(), approvalDecisionCommand(UICommandCreateSessionRule, request, 20, 905),
		)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Session rule reservation")
	}
	if result, err := fixture.coordinator.Decide(
		context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 20, 906),
	); !errors.Is(err, ErrApprovalUnavailable) || result != (UIApprovalResult{}) {
		t.Fatalf("concurrent action decision = %#v/%v", result, err)
	}
	if fixture.persistence.resolves != 0 || fixture.persistence.consumes != 0 || fixture.executor.calls != 0 {
		t.Fatalf("concurrent decision side effects = resolves %d consumes %d executor %d",
			fixture.persistence.resolves, fixture.persistence.consumes, fixture.executor.calls)
	}
	close(release)
	released = true
	select {
	case err := <-ruleResult:
		if err != nil {
			t.Fatalf("CreateSessionRule() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Session rule creation")
	}
	if fixture.persistence.consumes != 0 || fixture.executor.calls != 0 {
		t.Fatalf("Session rule creation executed source action: consumes %d executor %d",
			fixture.persistence.consumes, fixture.executor.calls)
	}
}

func TestFailedSessionRulePreparationReleasesLocalReservation(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 21)
	fixture.identifiers.permissionRuleErr = errors.New("synthetic permission rule identifier failure")
	if err := fixture.coordinator.CreateSessionRule(
		context.Background(), approvalDecisionCommand(UICommandCreateSessionRule, request, 21, 907),
	); !errors.Is(err, ErrPermissionRuleInvalid) {
		t.Fatalf("CreateSessionRule() error = %v", err)
	}
	result, err := fixture.coordinator.Decide(
		context.Background(), approvalDecisionCommand(UICommandRejectAction, request, 21, 908),
	)
	if err != nil || result.State != domain.ApprovalStateRejected || fixture.persistence.consumes != 0 || fixture.executor.calls != 0 {
		t.Fatalf("decision after failed rule preparation = %#v/%v consumes=%d executor=%d",
			result, err, fixture.persistence.consumes, fixture.executor.calls)
	}
}

func TestApprovalCoordinatorApprovalPersistsDecisionWithoutExecutionAndRejectsReplay(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 20)
	command := approvalDecisionCommand(UICommandApproveAction, request, 20, 101)
	result, err := fixture.coordinator.Decide(context.Background(), command)
	if err != nil {
		t.Fatalf("Decide(approve) error = %v", err)
	}
	if result.Validate() != nil || result.State != domain.ApprovalStateApproved || fixture.persistence.resolves != 1 {
		t.Fatalf("approve result/resolves = %#v/%d", result, fixture.persistence.resolves)
	}
	if fixture.persistence.lastDecision.Choice != domain.ApprovalDecisionApprove ||
		fixture.persistence.lastResolveAudit.Type != domain.AuditEventApprovalApproved {
		t.Fatalf("approved decision/audit = %#v/%#v", fixture.persistence.lastDecision, fixture.persistence.lastResolveAudit)
	}
	if fixture.executor.calls != 0 {
		t.Fatalf("fake executor calls = %d, want 0", fixture.executor.calls)
	}
	replayed, err := fixture.coordinator.Decide(context.Background(), command)
	if !errors.Is(err, ErrApprovalInvalidated) || replayed.State != domain.ApprovalStateInvalidated ||
		replayed.StateReason != domain.ApprovalReasonDecisionReplayed {
		t.Fatalf("Decide(replay) error = %v", err)
	}
	if fixture.persistence.resolves != 1 || fixture.persistence.closes != 1 || fixture.executor.calls != 0 {
		t.Fatalf("replay resolves/closes/executor = %d/%d/%d", fixture.persistence.resolves, fixture.persistence.closes, fixture.executor.calls)
	}
}

func TestApprovalCoordinatorConsumesApprovedRestartAfterDurableDecision(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 26)
	command := approvalDecisionCommand(UICommandApproveAction, request, 26, 109)
	approved, err := fixture.coordinator.Decide(context.Background(), command)
	if err != nil || approved.State != domain.ApprovalStateApproved || fixture.executor.calls != 0 {
		t.Fatalf("Decide() result/error/executor = %#v/%v/%d", approved, err, fixture.executor.calls)
	}
	consumed, err := fixture.coordinator.ConsumeApprovedRestart(context.Background(), command)
	if err != nil || consumed.State != domain.ApprovalStateConsumed ||
		consumed.StateReason != domain.ApprovalReasonConsumed || fixture.persistence.consumes != 1 ||
		fixture.persistence.lastConsumeAudit.Type != domain.AuditEventWriteIntent ||
		fixture.executor.revalidates != 1 || fixture.executor.calls != 1 {
		t.Fatalf(
			"ConsumeApprovedRestart() result/error/consumes/audit/revalidates/executor = %#v/%v/%d/%#v/%d/%d",
			consumed, err, fixture.persistence.consumes, fixture.persistence.lastConsumeAudit,
			fixture.executor.revalidates, fixture.executor.calls,
		)
	}
	if _, err := fixture.coordinator.ConsumeApprovedRestart(context.Background(), command); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("ConsumeApprovedRestart(replay) error = %v", err)
	}
	if fixture.persistence.consumes != 1 || fixture.executor.calls != 1 {
		t.Fatalf("replay consumes/executor = %d/%d, want 1/1", fixture.persistence.consumes, fixture.executor.calls)
	}
}

func TestApprovalCoordinatorPreWriteAuditFailureClosesWithoutExecution(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 27)
	command := approvalDecisionCommand(UICommandApproveAction, request, 27, 110)
	if _, err := fixture.coordinator.Decide(context.Background(), command); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	fixture.persistence.consumeErr = errors.New("synthetic pre-write audit failure")
	result, err := fixture.coordinator.ConsumeApprovedRestart(context.Background(), command)
	if !errors.Is(err, ErrApprovalPersistenceUnavailable) || result.State != domain.ApprovalStateInvalidated ||
		fixture.persistence.consumes != 0 || fixture.executor.calls != 0 {
		t.Fatalf("audit failure result/error/consumes/executor = %#v/%v/%d/%d",
			result, err, fixture.persistence.consumes, fixture.executor.calls)
	}
}

func TestApprovalCoordinatorScopeInvalidationDuringRevalidationPreventsWrite(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 28)
	command := approvalDecisionCommand(UICommandApproveAction, request, 28, 111)
	if _, err := fixture.coordinator.Decide(context.Background(), command); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	fixture.executor.revalidateStarted = started
	fixture.executor.revalidateRelease = release
	type consumeOutcome struct {
		result UIApprovalResult
		err    error
	}
	completed := make(chan consumeOutcome, 1)
	go func() {
		result, err := fixture.coordinator.ConsumeApprovedRestart(context.Background(), command)
		completed <- consumeOutcome{result: result, err: err}
	}()
	<-started
	if err := fixture.coordinator.InvalidateScope(8); err != nil {
		t.Fatalf("InvalidateScope() error = %v", err)
	}
	close(release)
	outcome := <-completed
	if !errors.Is(outcome.err, ErrApprovalUnavailable) || outcome.result.RequestID != "" ||
		fixture.persistence.closes != 1 || fixture.persistence.lastClosed.State != domain.ApprovalStateInvalidated ||
		fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonScopeChanged || len(fixture.ui.events) != 2 ||
		fixture.persistence.consumes != 0 || fixture.executor.calls != 0 {
		t.Fatalf("result/error/closes/closed/events/consumes/writes = %#v/%v/%d/%#v/%d/%d/%d, want unavailable/1/scope-invalidated/2/0/0",
			outcome.result, outcome.err, fixture.persistence.closes, fixture.persistence.lastClosed,
			len(fixture.ui.events), fixture.persistence.consumes, fixture.executor.calls)
	}
}

func TestCoordinatorApprovalCommandReturnsTerminalRestartAttempt(t *testing.T) {
	tests := []struct {
		name       string
		executeErr error
	}{
		{name: "request accepted"},
		{name: "request failed", executeErr: errors.New("synthetic write failure")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			fixture.executor.err = test.executeErr
			request := fixture.submit(t, 29)
			command := approvalDecisionCommand(UICommandApproveAction, request, 29, 112)
			outer, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(
				context.Context,
				agent.RunInput,
				agent.EventSink,
			) agent.RunOutcome {
				return agent.RunOutcome{}
			}))
			outer.approvals = fixture.coordinator
			outcome, err := outer.ExecuteUICommand(context.Background(), command)
			if err != nil || outcome.Approval == nil || outcome.Approval.State != domain.ApprovalStateConsumed ||
				fixture.persistence.consumes != 1 || fixture.executor.calls != 1 {
				t.Fatalf("outcome/error/consumes/writes = %#v/%v/%d/%d, want consumed/nil/1/1",
					outcome, err, fixture.persistence.consumes, fixture.executor.calls)
			}
		})
	}
}

func TestApprovalCoordinatorRejectionPersistsTerminalDecisionWithoutExecution(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 21)
	result, err := fixture.coordinator.Decide(
		context.Background(), approvalDecisionCommand(UICommandRejectAction, request, 21, 107),
	)
	if err != nil || result.State != domain.ApprovalStateRejected ||
		result.StateReason != domain.ApprovalReasonUserRejected || fixture.persistence.resolves != 1 ||
		fixture.persistence.lastDecision.Choice != domain.ApprovalDecisionReject ||
		fixture.persistence.lastResolveAudit.Type != domain.AuditEventApprovalRejected ||
		fixture.executor.calls != 0 {
		t.Fatalf("reject result/error/decision/audit/executor = %#v/%v/%#v/%#v/%d", result, err, fixture.persistence.lastDecision, fixture.persistence.lastResolveAudit, fixture.executor.calls)
	}
	if _, err := fixture.coordinator.Decide(
		context.Background(), approvalDecisionCommand(UICommandRejectAction, request, 21, 108),
	); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("Decide(rejected replay) error = %v", err)
	}
}

func TestApprovalCoordinatorApprovedNotExecutedStillExpiresAndInvalidates(t *testing.T) {
	tests := []struct {
		name string
		act  func(*testing.T, *approvalCoordinatorFixture, domain.ApprovalRequest)
		want domain.ApprovalStateReason
	}{
		{
			name: "expiry",
			act: func(t *testing.T, fixture *approvalCoordinatorFixture, request domain.ApprovalRequest) {
				fixture.clock.set(request.ExpiresAt)
				result, err := fixture.coordinator.Expire(context.Background(), request.ID)
				if err != nil || result.State != domain.ApprovalStateExpired {
					t.Fatalf("Expire() result/error = %#v/%v", result, err)
				}
			},
			want: domain.ApprovalReasonTTLExpired,
		},
		{
			name: "run cancellation",
			act: func(t *testing.T, fixture *approvalCoordinatorFixture, request domain.ApprovalRequest) {
				if err := fixture.coordinator.CancelRun(context.Background(), request.RunID); err != nil {
					t.Fatalf("CancelRun() error = %v", err)
				}
			},
			want: domain.ApprovalReasonRunCancelled,
		},
		{
			name: "scope invalidation",
			act: func(t *testing.T, fixture *approvalCoordinatorFixture, _ domain.ApprovalRequest) {
				if err := fixture.coordinator.InvalidateScope(8); err != nil {
					t.Fatalf("InvalidateScope() error = %v", err)
				}
			},
			want: domain.ApprovalReasonScopeChanged,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			request := fixture.submit(t, 25)
			approved, err := fixture.coordinator.Decide(
				context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 25, 106),
			)
			if err != nil || approved.State != domain.ApprovalStateApproved {
				t.Fatalf("Decide() result/error = %#v/%v", approved, err)
			}
			test.act(t, fixture, request)
			if fixture.persistence.lastCloseExpected != domain.ApprovalStateApproved ||
				fixture.persistence.lastClosed.StateReason != test.want || fixture.executor.calls != 0 {
				t.Fatalf("close expected/request/executor = %s/%#v/%d", fixture.persistence.lastCloseExpected, fixture.persistence.lastClosed, fixture.executor.calls)
			}
		})
	}
}

func TestCoordinatorProposalBridgeRequiresExactActiveRunBinding(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	outer, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(
		context.Context,
		agent.RunInput,
		agent.EventSink,
	) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	if _, err := outer.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 2, fixture.intent,
	); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("disabled proposal error = %v", err)
	}
	outer.approvals = fixture.coordinator
	if _, err := outer.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 2, fixture.intent,
	); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("inactive proposal error = %v", err)
	}
	bridge, err := newEventBridge(fixture.runID, fixture.intent.Scope.Generation, fixture.intent.PolicyGeneration, fixture.ui)
	if err != nil {
		t.Fatalf("newEventBridge() error = %v", err)
	}
	bridge.started = true
	bridge.sequence = 1
	outer.active = &activeRun{run: domain.AgentRun{
		ID: fixture.runID, SessionID: fixture.sessionID, Scope: fixture.intent.Scope,
	}, bridge: bridge}
	otherSession := domain.SessionID("00000000-0000-7000-8000-000000008199")
	if _, err := outer.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, otherSession, 2, fixture.intent,
	); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("session mismatch error = %v", err)
	}
	staleIntent := fixture.intent
	staleIntent.Scope.Generation++
	if _, err := outer.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 2, staleIntent,
	); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("scope mismatch error = %v", err)
	}
	if _, err := outer.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 3, fixture.intent,
	); !errors.Is(err, ErrApprovalUnavailable) {
		t.Fatalf("sequence mismatch error = %v", err)
	}
	outer.deletingSession = fixture.sessionID
	if _, err := outer.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 2, fixture.intent,
	); !errors.Is(err, ErrApprovalUnavailable) || fixture.persistence.creates != 0 {
		t.Fatalf("lifecycle operation proposal error/creates = %v/%d", err, fixture.persistence.creates)
	}
	outer.deletingSession = ""
	outer.operations = 1
	request, err := outer.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 2, fixture.intent,
	)
	outer.operations = 0
	if err != nil || request.State != domain.ApprovalStatePending || fixture.persistence.creates != 1 ||
		fixture.executor.calls != 0 || bridge.sequence != 2 {
		t.Fatalf("bound proposal request/error/create/executor/sequence = %#v/%v/%d/%d/%d", request, err, fixture.persistence.creates, fixture.executor.calls, bridge.sequence)
	}
}

func TestCoordinatorTurnsTypedSuggestionIntoApprovalOnlyAfterTrustedPreparation(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	outer, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(
		context.Context,
		agent.RunInput,
		agent.EventSink,
	) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	preparer := &fakeRestartProposalPreparer{prepared: fixture.intent.Target}
	outer.approvals = fixture.coordinator
	outer.restartProposals = preparer
	bridge, err := newEventBridge(fixture.runID, fixture.intent.Scope.Generation, fixture.intent.PolicyGeneration, fixture.ui)
	if err != nil {
		t.Fatalf("newEventBridge() error = %v", err)
	}
	bridge.started = true
	bridge.sequence = 8
	diagnosis := domain.Diagnosis{RecommendedActions: []domain.RecommendedAction{{
		Operation: domain.ApprovalOperationRestartDeployment,
		Target: &domain.ResourceRef{
			APIVersion: domain.RestartDeploymentTargetAPIVersion,
			Kind:       domain.RestartDeploymentTargetKind,
			Namespace:  fixture.intent.Scope.Namespace,
			Name:       fixture.intent.Target.Resource.Name,
		},
		Action: fixture.intent.ReasonSummary,
		Risk:   domain.RestartDeploymentRiskSummary,
	}}}
	state := &activeRun{
		run: domain.AgentRun{
			ID: fixture.runID, SessionID: fixture.sessionID, Scope: fixture.intent.Scope,
		},
		bridge: bridge, diagnosis: &diagnosis,
	}
	if err := outer.prepareRestartProposal(context.Background(), state); err != nil {
		t.Fatalf("prepareRestartProposal() error = %v", err)
	}
	if preparer.calls != 1 || preparer.target.UID != "" || preparer.target.ResourceVersion != "" ||
		preparer.scope != fixture.intent.Scope || preparer.reason != fixture.intent.ReasonSummary {
		t.Fatalf("trusted preparer calls/input = %d/%#v/%#v/%q", preparer.calls, preparer.scope, preparer.target, preparer.reason)
	}
	if fixture.persistence.creates != 1 || len(fixture.ui.events) != 1 ||
		fixture.ui.events[0].Kind != UIEventApprovalRequested || bridge.sequence != 9 || fixture.executor.calls != 0 {
		t.Fatalf("creates/events/sequence/writes = %d/%#v/%d/%d, want 1/approval/9/0",
			fixture.persistence.creates, fixture.ui.events, bridge.sequence, fixture.executor.calls)
	}
}

func TestCoordinatorTerminalEventCancelsUnexecutedApproval(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	clock := newCoordinatorClock()
	var outer *Coordinator
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatalf("NewEventPublisher() error = %v", err)
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
			t.Fatalf("Publish(started) error = %v", err)
		}
		intent := fixture.intent
		intent.Scope = input.Scope().Snapshot()
		intent.Target.Resource.Namespace = input.Scope().Namespace
		intent.PolicyGeneration = fixture.coordinator.permissions.Status(fixture.clock.Now()).PolicyGeneration
		if _, err := outer.SubmitRestartDeploymentProposal(
			ctx, input.RunID(), input.SessionID(), 2, intent,
		); err != nil {
			t.Fatalf("SubmitRestartDeploymentProposal() error = %v", err)
		}
		class := domain.SafeErrorClassInternal
		if _, err := publisher.Publish(ctx, agent.RunEvent{
			Kind: agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{
				Class: class, SafeMessage: "The AgentRun failed safely.",
			},
		}); err != nil {
			t.Fatalf("Publish(failed) error = %v", err)
		}
		return agent.RunOutcome{
			Status: domain.AgentRunStatusFailed, ErrorClass: &class,
			SafeMessage: "The AgentRun failed safely.",
		}
	})
	var scope *coordinatorScope
	outer, _, scope, _ = newCoordinatorHarness(t, clock, runner)
	outer.approvals = fixture.coordinator
	fixture.scope.scope = scope.scope
	session := createCoordinatorSession(t, outer)
	outer.runResourcePolicies = coordinatorResourcePoliciesAt{
		generation: fixture.coordinator.UIPermissionSnapshot().PolicyGeneration,
	}
	runID, err := outer.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected Deployment.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	if _, err := outer.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	if fixture.persistence.lastCloseExpected != domain.ApprovalStatePending ||
		fixture.persistence.lastClosed.State != domain.ApprovalStateCancelled ||
		fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonRunCancelled ||
		fixture.executor.calls != 0 {
		t.Fatalf("terminal close expected/request/executor = %s/%#v/%d", fixture.persistence.lastCloseExpected, fixture.persistence.lastClosed, fixture.executor.calls)
	}
}

type coordinatorResourcePoliciesAt struct {
	generation domain.PolicyGeneration
}

func (source coordinatorResourcePoliciesAt) ResourcePolicySnapshot(ctx context.Context) (domain.ResourcePolicyCatalog, domain.PolicyGeneration, bool) {
	return domain.DefaultResourcePolicyCatalog(), source.generation, ctx != nil && ctx.Err() == nil && source.generation.Valid()
}

func (source coordinatorResourcePoliciesAt) CurrentPolicyGeneration(ctx context.Context, generation domain.PolicyGeneration) bool {
	return ctx != nil && ctx.Err() == nil && generation == source.generation
}

func TestApprovalCoordinatorFailureExpiryScopeAndCancellationNeverExecute(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *approvalCoordinatorFixture)
	}{
		{name: "proposal persistence failure", run: func(t *testing.T, fixture *approvalCoordinatorFixture) {
			fixture.persistence.createErr = errors.New("synthetic storage failure")
			if _, err := fixture.coordinator.SubmitRestartDeploymentProposal(context.Background(), fixture.runID, fixture.sessionID, 30, fixture.intent); !errors.Is(err, ErrApprovalPersistenceUnavailable) {
				t.Fatalf("proposal error = %v", err)
			}
			if len(fixture.ui.events) != 0 {
				t.Fatalf("UI events = %d, want 0", len(fixture.ui.events))
			}
		}},
		{name: "decision persistence failure", run: func(t *testing.T, fixture *approvalCoordinatorFixture) {
			request := fixture.submit(t, 31)
			fixture.persistence.resolveErr = errors.New("synthetic audit failure")
			if _, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 31, 102)); !errors.Is(err, ErrApprovalPersistenceUnavailable) {
				t.Fatalf("Decide() error = %v", err)
			}
			if snapshot, ok := fixture.service.Snapshot(request.ID); !ok || snapshot.State != domain.ApprovalStateCancelled {
				t.Fatalf("in-memory state after persistence failure = %#v/%v", snapshot, ok)
			}
		}},
		{name: "expired", run: func(t *testing.T, fixture *approvalCoordinatorFixture) {
			request := fixture.submit(t, 32)
			fixture.clock.set(request.ExpiresAt)
			result, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 32, 103))
			if !errors.Is(err, ErrApprovalExpired) || result.State != domain.ApprovalStateExpired || fixture.persistence.closes != 1 {
				t.Fatalf("expired result/error/closes = %#v/%v/%d", result, err, fixture.persistence.closes)
			}
		}},
		{name: "scope changed", run: func(t *testing.T, fixture *approvalCoordinatorFixture) {
			request := fixture.submit(t, 33)
			fixture.scope.scope.Generation++
			result, err := fixture.coordinator.Decide(context.Background(), approvalDecisionCommand(UICommandApproveAction, request, 33, 104))
			if !errors.Is(err, ErrApprovalInvalidated) || result.State != domain.ApprovalStateInvalidated ||
				result.StateReason != domain.ApprovalReasonScopeChanged || fixture.persistence.closes != 1 {
				t.Fatalf("stale result/error/closes = %#v/%v/%d", result, err, fixture.persistence.closes)
			}
		}},
		{name: "run cancelled", run: func(t *testing.T, fixture *approvalCoordinatorFixture) {
			request := fixture.submit(t, 34)
			if err := fixture.coordinator.CancelRun(context.Background(), request.RunID); err != nil {
				t.Fatalf("CancelRun() error = %v", err)
			}
			if fixture.persistence.lastClosed.State != domain.ApprovalStateCancelled ||
				fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonRunCancelled || len(fixture.ui.events) != 2 {
				t.Fatalf("cancelled request/events = %#v/%d", fixture.persistence.lastClosed, len(fixture.ui.events))
			}
		}},
		{name: "scope generation invalidated", run: func(t *testing.T, fixture *approvalCoordinatorFixture) {
			fixture.submit(t, 35)
			if err := fixture.coordinator.InvalidateScope(8); err != nil {
				t.Fatalf("InvalidateScope() error = %v", err)
			}
			if fixture.persistence.lastClosed.State != domain.ApprovalStateInvalidated ||
				fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonScopeChanged || len(fixture.ui.events) != 2 {
				t.Fatalf("invalidated request/events = %#v/%d", fixture.persistence.lastClosed, len(fixture.ui.events))
			}
		}},
		{name: "scope invalidation persistence failure", run: func(t *testing.T, fixture *approvalCoordinatorFixture) {
			fixture.submit(t, 36)
			fixture.persistence.closeErr = errors.New("synthetic close failure")
			if err := fixture.coordinator.InvalidateScope(8); !errors.Is(err, ErrApprovalPersistenceUnavailable) {
				t.Fatalf("InvalidateScope() error = %v", err)
			}
			if len(fixture.ui.events) != 2 {
				t.Fatalf("UI events = %d, want request and close", len(fixture.ui.events))
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			test.run(t, fixture)
			if fixture.executor.calls != 0 {
				t.Fatalf("fake executor calls = %d, want 0", fixture.executor.calls)
			}
		})
	}
}

func TestApprovalCoordinatorDecisionProofMismatchIsPersistedAndCleared(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 40)
	command := approvalDecisionCommand(UICommandApproveAction, request, 40, 105)
	command.ApprovalDigest = domain.ApprovalDigest(fmt.Sprintf("%064d", 7))
	result, err := fixture.coordinator.Decide(context.Background(), command)
	if !errors.Is(err, ErrApprovalInvalidated) || result.State != domain.ApprovalStateInvalidated ||
		result.StateReason != domain.ApprovalReasonDigestMismatch || fixture.persistence.closes != 1 {
		t.Fatalf("mismatch result/error/closes = %#v/%v/%d", result, err, fixture.persistence.closes)
	}
	if fixture.executor.calls != 0 {
		t.Fatalf("fake executor calls = %d, want 0", fixture.executor.calls)
	}
}

func TestApprovalCoordinatorRecoveryInvalidatesPendingAndApprovedWithoutExecution(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	pending := storedApprovalForRecovery(t, fixture, "00000000-0000-7000-8000-000000008201", domain.ApprovalStatePending)
	approved := storedApprovalForRecovery(t, fixture, "00000000-0000-7000-8000-000000008202", domain.ApprovalStateApproved)
	fixture.persistence.recoverable = []approval.StoredRequest{pending, approved}
	fixture.clock.set(pending.RequestedAt.Add(5 * time.Second))
	if err := fixture.coordinator.Recover(context.Background()); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if len(fixture.persistence.recovered) != 2 {
		t.Fatalf("recovered transitions = %d, want 2", len(fixture.persistence.recovered))
	}
	for _, transition := range fixture.persistence.recovered {
		if transition.Validate() != nil || transition.After.State != domain.ApprovalStateCancelled ||
			transition.After.StateReason != domain.ApprovalReasonProcessRestarted {
			t.Fatalf("recovery transition = %#v", transition)
		}
	}
	if fixture.executor.calls != 0 || len(fixture.ui.events) != 0 {
		t.Fatalf("recovery executor/UI = %d/%d", fixture.executor.calls, len(fixture.ui.events))
	}
}

func TestCoordinatorStartupRecoveryFailsClosedBeforeRunMaintenance(t *testing.T) {
	tests := []struct {
		name       string
		recoverErr error
		wantErr    bool
		wantCalls  int
	}{
		{name: "success", wantCalls: 1},
		{name: "approval audit failure", recoverErr: errors.New("synthetic recovery audit failure"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			pending := storedApprovalForRecovery(t, fixture, "00000000-0000-7000-8000-000000008203", domain.ApprovalStatePending)
			fixture.persistence.recoverable = []approval.StoredRequest{pending}
			fixture.persistence.recoveryErr = test.recoverErr
			fixture.clock.set(pending.RequestedAt.Add(time.Second))
			outer, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(
				context.Context,
				agent.RunInput,
				agent.EventSink,
			) agent.RunOutcome {
				return agent.RunOutcome{}
			}))
			maintenance := new(recordingStartupMaintenance)
			outer.approvals = fixture.coordinator
			outer.startup = maintenance
			err := outer.prepareStartup(context.Background())
			if test.wantErr != errors.Is(err, ErrPersistenceUnavailable) {
				t.Fatalf("prepareStartup() error = %v", err)
			}
			if maintenance.recoveryCalls != test.wantCalls || maintenance.retentionCalls != test.wantCalls ||
				fixture.executor.calls != 0 {
				t.Fatalf("startup recovery/retention/executor calls = %d/%d/%d", maintenance.recoveryCalls, maintenance.retentionCalls, fixture.executor.calls)
			}
			if !test.wantErr {
				if len(fixture.persistence.recovered) != 1 {
					t.Fatalf("recovered transitions = %d, want 1", len(fixture.persistence.recovered))
				}
				if err := outer.prepareStartup(context.Background()); err != nil || maintenance.recoveryCalls != 1 {
					t.Fatalf("repeated prepareStartup() error/calls = %v/%d", err, maintenance.recoveryCalls)
				}
			}
		})
	}
}

type approvalCoordinatorFixture struct {
	coordinator *ApprovalCoordinator
	service     *approval.Service
	persistence *fakeApprovalPersistence
	ui          *fakeApprovalUIEvents
	executor    *fakeApprovalExecutor
	rollout     *fakeApprovalRolloutObserver
	clock       *approvalCoordinatorClock
	scope       *fakeApprovalCurrentScope
	identifiers *approvalCoordinatorIDs
	runID       domain.AgentRunID
	sessionID   domain.SessionID
	intent      domain.OperationIntent
}

type fakeRestartProposalPreparer struct {
	calls    int
	scope    domain.ScopeSnapshot
	target   domain.ResourceRef
	reason   string
	prepared domain.ActionTarget
	err      error
}

func (preparer *fakeRestartProposalPreparer) PrepareRestartDeploymentProposal(
	_ context.Context,
	scope domain.ScopeSnapshot,
	target domain.ResourceRef,
	reason string,
) (domain.ActionTarget, error) {
	preparer.calls++
	preparer.scope = scope
	preparer.target = target
	preparer.reason = reason
	return preparer.prepared, preparer.err
}

func newApprovalCoordinatorFixture(t *testing.T) *approvalCoordinatorFixture {
	t.Helper()
	clock := &approvalCoordinatorClock{now: time.UnixMilli(1_700_000_500_000).UTC()}
	runID := domain.AgentRunID("00000000-0000-7000-8000-000000008101")
	sessionID := domain.SessionID("00000000-0000-7000-8000-000000008102")
	executor := &fakeApprovalExecutor{}
	scope := &fakeApprovalCurrentScope{scope: domain.ClusterScope{
		Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7,
		ActivatedAt: clock.now,
	}}
	persistence := &fakeApprovalPersistence{}
	ui := &fakeApprovalUIEvents{}
	rollout := newFakeApprovalRolloutObserver(clock.now.Add(2 * time.Millisecond))
	ids := &approvalCoordinatorIDs{}
	service, err := approval.NewService(approval.ServiceConfig{
		Clock: clock, Nonces: approvalNonceSource{value: 0x71},
		Store: persistence, AuditIDs: ids,
	})
	if err != nil {
		t.Fatalf("approval.NewService() error = %v", err)
	}
	policy := PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 1}
	permissions, err := NewPermissionManager(policy)
	if err != nil {
		t.Fatalf("NewPermissionManager() error = %v", err)
	}
	if err := permissions.BindSession(sessionID); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}
	coordinator, err := NewApprovalCoordinator(ApprovalCoordinatorConfig{
		Service: service, Persistence: persistence, ResultAudits: persistence, Scope: scope,
		ApprovalIDs: ids, AuditIDs: ids, UIEvents: ui, Rollout: rollout, Now: clock.Now,
		RestartRevalidator: executor, RestartExecutor: executor, Observations: fakeObservationRevalidator{},
		ObservationPolicy: domain.DisabledObservabilityPolicyCatalog(), Permissions: permissions, Reviews: persistence,
	})
	if err != nil {
		t.Fatalf("NewApprovalCoordinator() error = %v", err)
	}
	intent, err := NewRestartDeploymentActionIntent(
		policy,
		scope.scope,
		domain.ActionTarget{Resource: domain.ResourceRef{
			APIVersion: domain.RestartDeploymentTargetAPIVersion, Kind: domain.RestartDeploymentTargetKind,
			Namespace: scope.scope.Namespace, Name: "sample-deployment", UID: "sample-deployment-uid",
			ResourceVersion: "fresh-resource-version",
		}, Fingerprint: fmt.Sprintf("%064d", 8), Generation: 8},
		"Restart after the bounded diagnosis.",
	)
	if err != nil {
		t.Fatalf("NewRestartDeploymentActionIntent() error = %v", err)
	}
	return &approvalCoordinatorFixture{
		coordinator: coordinator, service: service, persistence: persistence, ui: ui,
		executor: executor, rollout: rollout, clock: clock, scope: scope, identifiers: ids,
		runID: runID, sessionID: sessionID, intent: intent,
	}
}

type fakeObservationRevalidator struct{}

func (fakeObservationRevalidator) RevalidateObservationAction(context.Context, domain.ObservationActionPlan) error {
	return nil
}

func (fixture *approvalCoordinatorFixture) submit(t *testing.T, sequence int64) domain.ApprovalRequest {
	t.Helper()
	request, err := fixture.coordinator.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, sequence, fixture.intent,
	)
	if err != nil {
		t.Fatalf("SubmitRestartDeploymentProposal() error = %v", err)
	}
	return request
}

func (fixture *approvalCoordinatorFixture) bindSession(t *testing.T, sessionID domain.SessionID) {
	t.Helper()
	if err := fixture.coordinator.BindSessionAuthority(context.Background(), sessionID); err != nil {
		t.Fatalf("BindSessionAuthority() error = %v", err)
	}
	fixture.sessionID = sessionID
	fixture.intent.PolicyGeneration = fixture.coordinator.permissions.Status(fixture.clock.Now()).PolicyGeneration
}

func approvalDecisionCommand(kind UICommandKind, request domain.ApprovalRequest, sequence int64, requestID uint64) UICommand {
	return UICommand{
		Kind: kind, RequestID: requestID, RunID: request.RunID,
		ExpectedScopeGeneration:  request.Intent.Scope.Generation,
		ExpectedPolicyGeneration: request.Intent.PolicyGeneration,
		ApprovalID:               request.ID, ApprovalDigest: request.Digest, ApprovalNonce: request.Nonce,
		ApprovalSequence: sequence,
	}
}

func storedApprovalForRecovery(
	t *testing.T,
	fixture *approvalCoordinatorFixture,
	id domain.ApprovalID,
	state domain.ApprovalState,
) approval.StoredRequest {
	t.Helper()
	nonce, err := domain.NewApprovalNonce(bytes.Repeat([]byte{0x72}, domain.ApprovalNonceBytes))
	if err != nil {
		t.Fatalf("NewApprovalNonce() error = %v", err)
	}
	requestedAt := time.UnixMilli(1_700_000_500_000).UTC()
	request := domain.ApprovalRequest{
		ID: id, RunID: fixture.runID, SessionID: fixture.sessionID, Intent: fixture.intent,
		Nonce: nonce, State: state, RequestedAt: requestedAt,
		ExpiresAt: requestedAt.Add(domain.ApprovalExecutionTTL), StateChangedAt: requestedAt,
	}
	if state == domain.ApprovalStateApproved {
		request.StateReason = domain.ApprovalReasonUserApproved
		request.StateChangedAt = requestedAt.Add(time.Second)
	}
	request.Digest, err = approval.OperationDigest(request)
	if err != nil {
		t.Fatalf("OperationDigest() error = %v", err)
	}
	stored, err := approval.NewStoredRequest(request)
	if err != nil {
		t.Fatalf("NewStoredRequest() error = %v", err)
	}
	return stored
}

type approvalCoordinatorClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *approvalCoordinatorClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *approvalCoordinatorClock) set(value time.Time) {
	clock.mu.Lock()
	clock.now = value
	clock.mu.Unlock()
}

type approvalNonceSource struct{ value byte }

func (source approvalNonceSource) NewNonce(context.Context) (domain.ApprovalNonce, error) {
	return domain.NewApprovalNonce(bytes.Repeat([]byte{source.value}, domain.ApprovalNonceBytes))
}

type fakeApprovalExecutor struct {
	calls             int
	revalidates       int
	err               error
	revalidateErr     error
	revalidateRV      string
	resultRV          string
	revalidateStarted chan struct{}
	revalidateRelease <-chan struct{}
}

type fakeApprovalRolloutObserver struct {
	calls          int
	requests       []RestartRolloutRequest
	observedAt     time.Time
	state          RestartRolloutState
	failureCode    RestartRolloutFailureCode
	err            error
	noObservations bool
	progressNumber int64
	beforeProgress func()
	beforeReturn   func()
}

func newFakeApprovalRolloutObserver(observedAt time.Time) *fakeApprovalRolloutObserver {
	return &fakeApprovalRolloutObserver{observedAt: observedAt, state: RestartRolloutSucceeded}
}

func (observer *fakeApprovalRolloutObserver) ObserveRestartRollout(
	ctx context.Context,
	request RestartRolloutRequest,
	sink RestartRolloutProgressSink,
) (RestartRolloutResult, error) {
	observer.calls++
	observer.requests = append(observer.requests, request)
	if err := ctx.Err(); err != nil {
		return RestartRolloutResult{}, err
	}
	if observer.err != nil {
		return RestartRolloutResult{}, observer.err
	}
	if observer.noObservations {
		return RestartRolloutResult{
			State:     RestartRolloutTimedOut,
			StartedAt: observer.observedAt.Add(-time.Millisecond), FinishedAt: observer.observedAt,
		}, nil
	}
	if observer.beforeProgress != nil {
		observer.beforeProgress()
	}
	progressNumber := observer.progressNumber
	if progressNumber == 0 {
		progressNumber = 1
	}
	progress := RestartRolloutObservation{
		Scope: request.Scope, DeploymentName: request.DeploymentName, DeploymentUID: request.DeploymentUID,
		DeploymentGeneration: request.TargetGeneration, TargetGeneration: request.TargetGeneration,
		ObservedGeneration: request.TargetGeneration - 1, TargetReplicas: request.TargetReplicas,
		ObservationNumber: progressNumber, State: RestartRolloutProgress, ObservedAt: observer.observedAt,
	}
	if err := sink.PublishRestartRolloutProgress(ctx, progress); err != nil {
		return RestartRolloutResult{}, err
	}
	final := progress
	final.ObservationNumber = progressNumber + 1
	final.ObservedAt = observer.observedAt.Add(time.Millisecond)
	result := RestartRolloutResult{
		State: observer.state, ObservationCount: final.ObservationNumber,
		StartedAt: observer.observedAt.Add(-time.Millisecond), FinishedAt: final.ObservedAt,
	}
	switch observer.state {
	case RestartRolloutSucceeded:
		final.State = RestartRolloutSucceeded
		final.ObservedGeneration = request.TargetGeneration
		final.UpdatedReplicas = request.TargetReplicas
		final.AvailableReplicas = request.TargetReplicas
	case RestartRolloutFailed:
		final.State = RestartRolloutFailed
		final.FailureCode = observer.failureCode
		final.ObservedGeneration = request.TargetGeneration
	case RestartRolloutTimedOut:
		final.State = RestartRolloutProgress
	}
	result.Final = final
	if observer.beforeReturn != nil {
		observer.beforeReturn()
	}
	return result, nil
}

func (executor *fakeApprovalExecutor) RevalidateApprovedRestart(
	ctx context.Context,
	intent domain.OperationIntent,
) (approval.RestartDeploymentObservation, error) {
	executor.revalidates++
	if executor.revalidateErr != nil {
		return approval.RestartDeploymentObservation{}, executor.revalidateErr
	}
	if executor.revalidateStarted != nil {
		close(executor.revalidateStarted)
	}
	if executor.revalidateRelease != nil {
		select {
		case <-executor.revalidateRelease:
		case <-ctx.Done():
			return approval.RestartDeploymentObservation{}, ctx.Err()
		}
	}
	resourceVersion := executor.revalidateRV
	if resourceVersion == "" {
		resourceVersion = "fresh-resource-version"
	}
	return approval.RestartDeploymentObservation{
		Scope: intent.Scope, DeploymentName: intent.Target.Resource.Name, DeploymentUID: intent.Target.Resource.UID,
		TemplateFingerprint: intent.Target.Fingerprint, DeploymentGeneration: intent.Target.Generation,
		ResourceVersion: resourceVersion,
	}, nil
}

func (executor *fakeApprovalExecutor) ExecuteApprovedRestart(
	_ context.Context,
	execution approval.RestartDeploymentExecution,
) (approval.RestartDeploymentResult, error) {
	executor.calls++
	if executor.err != nil {
		return approval.RestartDeploymentResult{}, executor.err
	}
	resourceVersion := executor.resultRV
	if resourceVersion == "" {
		resourceVersion = "post-restart-rv-19"
	}
	return approval.RestartDeploymentResult{
		Scope:                   execution.Observation().Scope,
		DeploymentName:          execution.Observation().DeploymentName,
		DeploymentUID:           execution.Observation().DeploymentUID,
		PreviousResourceVersion: execution.Observation().ResourceVersion,
		ResourceVersion:         resourceVersion,
		TargetGeneration:        execution.Observation().DeploymentGeneration + 1,
		TargetReplicas:          1,
		RestartedAt:             time.UnixMilli(1_700_000_500_001).UTC(),
	}, nil
}

type approvalCoordinatorIDs struct {
	next                  int
	permissionRuleStarted chan<- struct{}
	permissionRuleRelease <-chan struct{}
	permissionRuleErr     error
}

func (source *approvalCoordinatorIDs) nextID() string {
	source.next++
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", 8_300+source.next)
}

func (source *approvalCoordinatorIDs) NewApprovalID() (domain.ApprovalID, error) {
	return domain.ApprovalID(source.nextID()), nil
}

func (source *approvalCoordinatorIDs) NewAuditEventID() (domain.AuditEventID, error) {
	return domain.AuditEventID(source.nextID()), nil
}

func (source *approvalCoordinatorIDs) NewModelRequestID() (domain.ModelRequestID, error) {
	return domain.ModelRequestID(source.nextID()), nil
}

func (source *approvalCoordinatorIDs) NewPermissionRuleID() (domain.PermissionRuleID, error) {
	if source.permissionRuleStarted != nil {
		close(source.permissionRuleStarted)
	}
	if source.permissionRuleRelease != nil {
		<-source.permissionRuleRelease
	}
	if source.permissionRuleErr != nil {
		return "", source.permissionRuleErr
	}
	return domain.PermissionRuleID(source.nextID()), nil
}

type fakeApprovalCurrentScope struct{ scope domain.ClusterScope }

func (scope *fakeApprovalCurrentScope) CurrentScope() (domain.ClusterScope, bool) {
	return scope.scope, scope.scope.Validate() == nil
}

type fakeApprovalUIEvents struct {
	events []UIEvent
	err    error
}

func (sink *fakeApprovalUIEvents) PublishUIEvent(_ context.Context, event UIEvent) error {
	if sink.err != nil {
		return sink.err
	}
	sink.events = append(sink.events, event)
	return nil
}

type fakeApprovalPersistence struct {
	creates, resolves, closes, consumes                      int
	createErr, resolveErr, closeErr, consumeErr, recoveryErr error
	actionReviewErr                                          error
	lastCreated                                              domain.ApprovalRequest
	lastClosed                                               domain.ApprovalRequest
	lastCloseExpected                                        domain.ApprovalState
	lastDecision                                             domain.ApprovalDecision
	lastCreateAudit                                          domain.AuditEvent
	lastResolveAudit                                         domain.AuditEvent
	lastConsumed                                             approval.StoredRequest
	lastConsumeAudit                                         domain.AuditEvent
	recoverable                                              []approval.StoredRequest
	recovered                                                []approval.RecoveryTransition
	writeResultAudits                                        []domain.AuditEvent
	writeResultCalls                                         int
	writeResultFailures                                      int
	writeResultFailAfter                                     int
	actionReviews                                            []ActionReviewRecord
	actionReviewCalls                                        int
	afterResolve                                             func()
	afterConsume                                             func()
}

func (persistence *fakeApprovalPersistence) AppendActionReview(_ context.Context, record ActionReviewRecord) error {
	persistence.actionReviewCalls++
	if persistence.actionReviewErr != nil {
		return persistence.actionReviewErr
	}
	if record.Validate() != nil {
		return errors.New("synthetic invalid action review")
	}
	persistence.actionReviews = append(persistence.actionReviews, record)
	return nil
}

func (persistence *fakeApprovalPersistence) AppendWriteResult(_ context.Context, event domain.AuditEvent) error {
	persistence.writeResultCalls++
	if persistence.writeResultFailures > 0 {
		persistence.writeResultFailures--
		return errors.New("synthetic write result audit failure")
	}
	if persistence.writeResultFailAfter > 0 && persistence.writeResultCalls > persistence.writeResultFailAfter {
		return errors.New("synthetic write result audit failure")
	}
	if event.Validate() != nil {
		return errors.New("synthetic invalid write result audit")
	}
	persistence.writeResultAudits = append(persistence.writeResultAudits, event)
	return nil
}

func (persistence *fakeApprovalPersistence) AppendWriteResults(_ context.Context, events []domain.AuditEvent) error {
	persistence.writeResultCalls++
	if persistence.writeResultFailures > 0 {
		persistence.writeResultFailures--
		return errors.New("synthetic write result audit failure")
	}
	if persistence.writeResultFailAfter > 0 && persistence.writeResultCalls > persistence.writeResultFailAfter {
		return errors.New("synthetic write result audit failure")
	}
	if len(events) < 1 || len(events) > 5 {
		return errors.New("synthetic invalid write result audit set")
	}
	for _, event := range events {
		if event.Validate() != nil {
			return errors.New("synthetic invalid write result audit")
		}
	}
	persistence.writeResultAudits = append(persistence.writeResultAudits, events...)
	return nil
}

func (persistence *fakeApprovalPersistence) VerifyApproved(
	_ context.Context,
	request approval.StoredRequest,
	decision approval.StoredDecision,
) error {
	stored, err := approval.NewStoredRequest(persistence.lastCreated)
	if err != nil {
		return err
	}
	storedDecision, err := approval.NewStoredDecision(persistence.lastDecision)
	if err != nil {
		return err
	}
	if stored != request || storedDecision != decision {
		return approval.ErrStoredApprovalConflict
	}
	return nil
}

func (persistence *fakeApprovalPersistence) ConsumeWithAudit(
	_ context.Context,
	expected approval.StoredRequest,
	expectedDecision approval.StoredDecision,
	consumed approval.StoredRequest,
	audit domain.AuditEvent,
) error {
	if persistence.consumeErr != nil {
		return persistence.consumeErr
	}
	stored, err := approval.NewStoredRequest(persistence.lastCreated)
	storedDecision, decisionErr := approval.NewStoredDecision(persistence.lastDecision)
	if err != nil || decisionErr != nil || stored != expected || storedDecision != expectedDecision ||
		consumed.State != domain.ApprovalStateConsumed ||
		consumed.ValidateAudit(audit) != nil {
		return approval.ErrStoredApprovalConflict
	}
	persistence.consumes++
	persistence.lastConsumed = consumed
	persistence.lastConsumeAudit = audit
	if persistence.afterConsume != nil {
		persistence.afterConsume()
	}
	return nil
}

func (persistence *fakeApprovalPersistence) CreateWithAudit(_ context.Context, request domain.ApprovalRequest, audit domain.AuditEvent) error {
	persistence.creates++
	persistence.lastCreated, persistence.lastCreateAudit = request, audit
	return persistence.createErr
}

func (persistence *fakeApprovalPersistence) ResolveWithAudit(_ context.Context, _ domain.ApprovalState, request domain.ApprovalRequest, decision domain.ApprovalDecision, audit domain.AuditEvent) error {
	persistence.resolves++
	persistence.lastCreated, persistence.lastDecision, persistence.lastResolveAudit = request, decision, audit
	if persistence.afterResolve != nil {
		persistence.afterResolve()
	}
	return persistence.resolveErr
}

func (persistence *fakeApprovalPersistence) CloseWithAudit(_ context.Context, expected domain.ApprovalState, request domain.ApprovalRequest, _ domain.AuditEvent) error {
	persistence.closes++
	persistence.lastCloseExpected = expected
	persistence.lastClosed = request
	return persistence.closeErr
}

func (persistence *fakeApprovalPersistence) Get(context.Context, domain.ApprovalID) (approval.StoredRequest, *approval.StoredDecision, error) {
	return approval.StoredRequest{}, nil, approval.ErrStoredApprovalNotFound
}

func (persistence *fakeApprovalPersistence) ListRecoverable(context.Context, int) ([]approval.StoredRequest, error) {
	result := append([]approval.StoredRequest(nil), persistence.recoverable...)
	persistence.recoverable = nil
	return result, nil
}

func (persistence *fakeApprovalPersistence) RecoverWithAudits(_ context.Context, transitions []approval.RecoveryTransition) error {
	persistence.recovered = append([]approval.RecoveryTransition(nil), transitions...)
	return persistence.recoveryErr
}
