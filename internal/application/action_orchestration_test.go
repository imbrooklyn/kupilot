package application

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestApprovalReviewerRecommendationMatrixIsFailClosed(t *testing.T) {
	tests := []struct {
		name            string
		result          agent.ReviewerResult
		err             error
		wantReview      ActionReviewDisposition
		wantErrorClass  domain.SafeErrorClass
		wantChoice      domain.ApprovalDecisionChoice
		wantResolve     int
		wantConsume     int
		wantExecute     int
		wantHumanDialog bool
		wantStates      []UIReviewerState
	}{
		{
			name: "approve", result: reviewerResult(agent.ReviewerDecisionApprove, domain.RiskReview),
			wantReview: ActionReviewApprove, wantChoice: domain.ApprovalDecisionApprove,
			wantResolve: 1, wantConsume: 1, wantExecute: 1,
			wantStates: []UIReviewerState{UIReviewerReviewing, UIReviewerApproved},
		},
		{
			name: "deny", result: reviewerResult(agent.ReviewerDecisionDeny, domain.RiskReview),
			wantReview: ActionReviewDeny, wantChoice: domain.ApprovalDecisionReject, wantResolve: 1,
			wantStates: []UIReviewerState{UIReviewerReviewing, UIReviewerDenied},
		},
		{
			name: "escalate", result: reviewerResult(agent.ReviewerDecisionEscalateToUser, domain.RiskReview),
			wantReview: ActionReviewEscalate, wantHumanDialog: true,
			wantStates: []UIReviewerState{UIReviewerReviewing, UIReviewerEscalated},
		},
		{
			name: "risk lowering", result: reviewerResult(agent.ReviewerDecisionApprove, domain.RiskSafe),
			wantReview: ActionReviewFailed, wantErrorClass: domain.SafeErrorClassInvalidExternalResponse,
			wantHumanDialog: true,
			wantStates:      []UIReviewerState{UIReviewerReviewing, UIReviewerEscalated},
		},
		{
			name: "timeout", err: context.DeadlineExceeded,
			wantReview: ActionReviewTimedOut, wantErrorClass: domain.SafeErrorClassTimeout,
			wantHumanDialog: true,
			wantStates:      []UIReviewerState{UIReviewerReviewing, UIReviewerTimedOut, UIReviewerEscalated},
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			transport := &recordingActionReviewer{result: current.result, err: current.err}
			binding := acceptedReviewerBinding(t, fixture.clock.Now, transport, 2)
			configureAutoReview(t, fixture, binding)

			request, err := fixture.coordinator.SubmitRestartDeploymentProposal(
				context.Background(), fixture.runID, fixture.sessionID, 71, fixture.intent,
			)
			if err != nil || request.State != domain.ApprovalStatePending {
				t.Fatalf("SubmitRestartDeploymentProposal() = %#v/%v", request, err)
			}
			if transport.calls != 1 || transport.request.Validate() != nil ||
				!strings.Contains(transport.request.NormalizedAction, "risk=6:review") ||
				!strings.Contains(transport.request.PolicyFacts, "reviewer_cannot_lower_risk=true") {
				t.Fatalf("reviewer calls/request = %d/%#v", transport.calls, transport.request)
			}
			if len(fixture.persistence.actionReviews) != 1 {
				t.Fatalf("action review records = %#v", fixture.persistence.actionReviews)
			}
			review := fixture.persistence.actionReviews[0]
			if review.Disposition != current.wantReview || review.ErrorClass != current.wantErrorClass ||
				review.ApprovalID != request.ID || review.PolicyGeneration != fixture.intent.PolicyGeneration {
				t.Fatalf("review record = %#v", review)
			}
			if fixture.persistence.resolves != current.wantResolve || fixture.persistence.consumes != current.wantConsume ||
				fixture.executor.calls != current.wantExecute {
				t.Fatalf("resolve/consume/execute = %d/%d/%d", fixture.persistence.resolves, fixture.persistence.consumes, fixture.executor.calls)
			}
			if current.wantResolve != 0 && (fixture.persistence.lastDecision.Choice != current.wantChoice ||
				fixture.persistence.lastDecision.Actor != domain.ApprovalActorReviewer ||
				fixture.persistence.lastDecision.Disposition != domain.ReviewDispositionReviewer ||
				fixture.persistence.lastDecision.ReviewerProfile != binding.Profile ||
				fixture.persistence.lastDecision.ReviewerOriginHash != binding.Privacy.OriginHash()) {
				t.Fatalf("durable Application decision = %#v", fixture.persistence.lastDecision)
			}
			if got := countUIEvents(fixture.ui.events, UIEventApprovalRequested); (got == 1) != current.wantHumanDialog {
				t.Fatalf("human approval dialogs = %d, want visible=%t", got, current.wantHumanDialog)
			}
			if states := reviewerUIStates(fixture.ui.events); !slices.Equal(states, current.wantStates) {
				t.Fatalf("Reviewer UI states = %v, want %v", states, current.wantStates)
			}
		})
	}
}

func TestApprovalReviewerMissingConsentAndMissingBindingMakeZeroModelCalls(t *testing.T) {
	for _, current := range []struct {
		name    string
		binding func(*testing.T, *approvalCoordinatorFixture, *recordingActionReviewer) *ReviewerModelBinding
	}{
		{
			name: "missing consent",
			binding: func(t *testing.T, fixture *approvalCoordinatorFixture, transport *recordingActionReviewer) *ReviewerModelBinding {
				return reviewerBinding(t, fixture.clock.Now, transport, 1, false)
			},
		},
		{name: "missing reviewer", binding: func(*testing.T, *approvalCoordinatorFixture, *recordingActionReviewer) *ReviewerModelBinding {
			return nil
		}},
	} {
		t.Run(current.name, func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			transport := &recordingActionReviewer{result: reviewerResult(agent.ReviewerDecisionApprove, domain.RiskReview)}
			binding := current.binding(t, fixture, transport)
			configureAutoReview(t, fixture, binding)
			request, err := fixture.coordinator.SubmitRestartDeploymentProposal(
				context.Background(), fixture.runID, fixture.sessionID, 72, fixture.intent,
			)
			if err != nil || request.State != domain.ApprovalStatePending || transport.calls != 0 ||
				fixture.persistence.resolves != 0 || fixture.persistence.consumes != 0 || fixture.executor.calls != 0 ||
				len(fixture.persistence.actionReviews) != 0 || countUIEvents(fixture.ui.events, UIEventApprovalRequested) != 1 {
				t.Fatalf("request/error/calls/resolves/consumes/execute/reviews/events = %#v/%v/%d/%d/%d/%d/%d/%#v",
					request, err, transport.calls, fixture.persistence.resolves, fixture.persistence.consumes,
					fixture.executor.calls, len(fixture.persistence.actionReviews), fixture.ui.events)
			}
			if binding != nil {
				snapshot := binding.Budget.Snapshot()
				if snapshot.Calls != 0 || snapshot.CostUnits != 0 {
					t.Fatalf("unconsented Reviewer budget = %#v", snapshot)
				}
			}
		})
	}
}

func TestApprovalReviewerBudgetOneOverEscalatesWithoutAnotherModelOrExecutorCall(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	transport := &recordingActionReviewer{result: reviewerResult(agent.ReviewerDecisionDeny, domain.RiskReview)}
	binding := acceptedReviewerBinding(t, fixture.clock.Now, transport, 1)
	configureAutoReview(t, fixture, binding)
	if _, err := fixture.coordinator.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 73, fixture.intent,
	); err != nil {
		t.Fatalf("first proposal error = %v", err)
	}
	if _, err := fixture.coordinator.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 74, fixture.intent,
	); err != nil {
		t.Fatalf("one-over proposal error = %v", err)
	}
	if transport.calls != 1 || fixture.persistence.resolves != 1 || fixture.persistence.consumes != 0 ||
		fixture.executor.calls != 0 || len(fixture.persistence.actionReviews) != 2 ||
		fixture.persistence.actionReviews[1].Disposition != ActionReviewFailed ||
		fixture.persistence.actionReviews[1].ErrorClass != domain.SafeErrorClassBudgetExhausted ||
		countUIEvents(fixture.ui.events, UIEventApprovalRequested) != 1 {
		t.Fatalf("calls/resolves/consumes/execute/reviews/events = %d/%d/%d/%d/%#v/%#v",
			transport.calls, fixture.persistence.resolves, fixture.persistence.consumes,
			fixture.executor.calls, fixture.persistence.actionReviews, fixture.ui.events)
	}
}

func TestApprovalReviewerPersistenceFailureInvalidatesWithoutDecisionOrExecution(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	transport := &recordingActionReviewer{result: reviewerResult(agent.ReviewerDecisionApprove, domain.RiskReview)}
	binding := acceptedReviewerBinding(t, fixture.clock.Now, transport, 1)
	configureAutoReview(t, fixture, binding)
	fixture.persistence.actionReviewErr = errors.New("synthetic action review persistence failure")

	request, err := fixture.coordinator.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 75, fixture.intent,
	)
	if !errors.Is(err, ErrApprovalPersistenceUnavailable) || request.ID == "" ||
		transport.calls != 1 || fixture.persistence.actionReviewCalls != 1 ||
		len(fixture.persistence.actionReviews) != 0 || fixture.persistence.resolves != 0 ||
		fixture.persistence.consumes != 0 || fixture.executor.revalidates != 0 || fixture.executor.calls != 0 ||
		fixture.persistence.closes != 1 || fixture.persistence.lastClosed.State != domain.ApprovalStateInvalidated ||
		fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonPolicyChanged {
		t.Fatalf("request/error/reviewer/review-write/reviews/resolve/consume/revalidate/execute/close/closed = %#v/%v/%d/%d/%d/%d/%d/%d/%d/%d/%#v",
			request, err, transport.calls, fixture.persistence.actionReviewCalls,
			len(fixture.persistence.actionReviews), fixture.persistence.resolves, fixture.persistence.consumes,
			fixture.executor.revalidates, fixture.executor.calls, fixture.persistence.closes,
			fixture.persistence.lastClosed)
	}
}

func TestPolicyGenerationChangeCancelsReviewerAndRejectsLateResult(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	transport := &blockingActionReviewer{started: make(chan struct{})}
	binding := acceptedReviewerBinding(t, fixture.clock.Now, transport, 1)
	configureAutoReview(t, fixture, binding)
	type submitResult struct {
		request domain.ApprovalRequest
		err     error
	}
	completed := make(chan submitResult, 1)
	go func() {
		request, err := fixture.coordinator.SubmitRestartDeploymentProposal(
			context.Background(), fixture.runID, fixture.sessionID, 76, fixture.intent,
		)
		completed <- submitResult{request: request, err: err}
	}()
	<-transport.started

	if err := fixture.coordinator.permissions.InvalidateOriginPolicy(context.Background()); err != nil {
		t.Fatalf("InvalidateOriginPolicy() error = %v", err)
	}
	result := <-completed
	if !errors.Is(result.err, ErrApprovalUnavailable) || result.request.ID == "" || transport.calls != 1 ||
		fixture.persistence.resolves != 0 || fixture.persistence.consumes != 0 ||
		fixture.executor.revalidates != 0 || fixture.executor.calls != 0 || fixture.persistence.closes != 1 ||
		fixture.persistence.lastClosed.State != domain.ApprovalStateInvalidated ||
		fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonPolicyChanged ||
		len(fixture.persistence.actionReviews) != 1 ||
		fixture.persistence.actionReviews[0].Disposition != ActionReviewCancelled ||
		fixture.persistence.actionReviews[0].ErrorClass != domain.SafeErrorClassCancelled {
		t.Fatalf("result/reviewer/resolve/consume/revalidate/execute/close/closed/reviews = %#v/%d/%d/%d/%d/%d/%d/%#v/%#v",
			result, transport.calls, fixture.persistence.resolves, fixture.persistence.consumes,
			fixture.executor.revalidates, fixture.executor.calls, fixture.persistence.closes,
			fixture.persistence.lastClosed, fixture.persistence.actionReviews)
	}
	if status := fixture.coordinator.permissions.Status(fixture.clock.Now()); status.PolicyGeneration != 3 || !status.Healthy {
		t.Fatalf("permission status after reviewer cancellation = %#v", status)
	}
}

func TestLocalPermissionDenialPrecedesTargetPreparationReviewerAndPersistence(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	transport := &recordingActionReviewer{result: reviewerResult(agent.ReviewerDecisionApprove, domain.RiskReview)}
	binding := acceptedReviewerBinding(t, fixture.clock.Now, transport, 1)
	fixture.coordinator.reviewer = binding
	if err := fixture.coordinator.permissions.Reconfigure(
		context.Background(), PermissionChangeActorLocalUser,
		PermissionPolicy{Profile: domain.PermissionProfileReadOnly},
	); err != nil {
		t.Fatalf("Reconfigure(read-only) error = %v", err)
	}
	_, _, err := fixture.coordinator.PrepareRestartActionPolicy(fixture.sessionID, fixture.intent.Scope)
	if !errors.Is(err, ErrPermissionDenied) || transport.calls != 0 || fixture.persistence.creates != 0 ||
		fixture.persistence.resolves != 0 || fixture.persistence.consumes != 0 || fixture.executor.revalidates != 0 ||
		fixture.executor.calls != 0 || len(fixture.persistence.actionReviews) != 0 {
		t.Fatalf("denial/calls/create/resolve/consume/revalidate/execute/reviews = %v/%d/%d/%d/%d/%d/%d/%d",
			err, transport.calls, fixture.persistence.creates, fixture.persistence.resolves,
			fixture.persistence.consumes, fixture.executor.revalidates, fixture.executor.calls,
			len(fixture.persistence.actionReviews))
	}
}

func TestRestartSubmissionRejectsNamespacePolicyMismatchBeforePersistenceOrReview(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	transport := &recordingActionReviewer{result: reviewerResult(agent.ReviewerDecisionApprove, domain.RiskReview)}
	fixture.coordinator.reviewer = acceptedReviewerBinding(t, fixture.clock.Now, transport, 1)
	intent := fixture.intent
	intent.NamespaceAccess = domain.NamespaceAccessAll

	_, err := fixture.coordinator.SubmitRestartDeploymentProposal(
		context.Background(), fixture.runID, fixture.sessionID, 70, intent,
	)
	if !errors.Is(err, ErrApprovalInvalidated) || fixture.persistence.creates != 0 ||
		fixture.persistence.actionReviewCalls != 0 || transport.calls != 0 ||
		fixture.executor.revalidates != 0 || fixture.executor.calls != 0 {
		t.Fatalf("submission/create/reviewer-write/reviewer/revalidate/execute = %v/%d/%d/%d/%d/%d",
			err, fixture.persistence.creates, fixture.persistence.actionReviewCalls, transport.calls,
			fixture.executor.revalidates, fixture.executor.calls)
	}
}

func TestReadOnlyDenialSkipsTrustedKubernetesProposalPreparation(t *testing.T) {
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
	if err := fixture.coordinator.permissions.Reconfigure(
		context.Background(), PermissionChangeActorLocalUser,
		PermissionPolicy{Profile: domain.PermissionProfileReadOnly},
	); err != nil {
		t.Fatalf("Reconfigure(read-only) error = %v", err)
	}
	state := &activeRun{
		run:    domain.AgentRun{ID: fixture.runID, SessionID: fixture.sessionID, Scope: fixture.intent.Scope},
		bridge: &eventBridge{runID: fixture.runID, scopeGeneration: fixture.intent.Scope.Generation},
		diagnosis: &domain.Diagnosis{RecommendedActions: []domain.RecommendedAction{{
			Operation: domain.ActionOperationRestartDeployment,
			Target: &domain.ResourceRef{
				APIVersion: domain.RestartDeploymentTargetAPIVersion, Kind: domain.RestartDeploymentTargetKind,
				Namespace: fixture.intent.Scope.Namespace, Name: fixture.intent.Target.Resource.Name,
			},
			Action: fixture.intent.ReasonSummary, Risk: domain.RestartDeploymentRiskSummary,
		}}},
	}
	if err := outer.prepareRestartProposal(context.Background(), state); !errors.Is(err, ErrPermissionDenied) ||
		preparer.calls != 0 || fixture.persistence.creates != 0 || fixture.persistence.actionReviewCalls != 0 ||
		fixture.executor.revalidates != 0 || fixture.executor.calls != 0 {
		t.Fatalf("denial/preparer/create/reviewer/revalidate/execute = %v/%d/%d/%d/%d/%d",
			err, preparer.calls, fixture.persistence.creates, fixture.persistence.actionReviewCalls,
			fixture.executor.revalidates, fixture.executor.calls)
	}
}

func TestPermissionAndActionStatusIsLocalAndContentFree(t *testing.T) {
	fixture := newApprovalCoordinatorFixture(t)
	request := fixture.submit(t, 77)
	before := struct {
		creates, resolves, closes, consumes, reviews, revalidates, executes int
	}{
		fixture.persistence.creates, fixture.persistence.resolves, fixture.persistence.closes,
		fixture.persistence.consumes, fixture.persistence.actionReviewCalls,
		fixture.executor.revalidates, fixture.executor.calls,
	}
	permission, action := fixture.coordinator.Status()
	after := struct {
		creates, resolves, closes, consumes, reviews, revalidates, executes int
	}{
		fixture.persistence.creates, fixture.persistence.resolves, fixture.persistence.closes,
		fixture.persistence.consumes, fixture.persistence.actionReviewCalls,
		fixture.executor.revalidates, fixture.executor.calls,
	}
	if before != after || permission.Profile != domain.PermissionProfileAsk || !permission.Healthy ||
		action == nil || action.RequestID != request.ID || action.Operation != domain.ActionOperationRestartDeployment ||
		action.Risk != domain.RiskReview || action.Route != domain.ReviewDispositionHuman ||
		action.State != domain.ApprovalStatePending || action.PolicyGeneration != fixture.intent.PolicyGeneration ||
		action.ScopeGeneration != fixture.intent.Scope.Generation || action.Reviewing {
		t.Fatalf("before/after/permission/action = %#v/%#v/%#v/%#v", before, after, permission, action)
	}
}

type recordingActionReviewer struct {
	calls       int
	request     agent.ReviewerRequest
	reservation agent.CallReservation
	result      agent.ReviewerResult
	err         error
}

type blockingActionReviewer struct {
	calls   int
	started chan struct{}
}

func (reviewer *blockingActionReviewer) Review(
	ctx context.Context,
	_ agent.ReviewerRequest,
	_ agent.CallReservation,
) (agent.ReviewerResult, error) {
	reviewer.calls++
	close(reviewer.started)
	<-ctx.Done()
	return agent.ReviewerResult{}, ctx.Err()
}

func (reviewer *recordingActionReviewer) Review(
	_ context.Context,
	request agent.ReviewerRequest,
	reservation agent.CallReservation,
) (agent.ReviewerResult, error) {
	reviewer.calls++
	reviewer.request = request
	reviewer.reservation = reservation
	return reviewer.result, reviewer.err
}

func reviewerResult(decision agent.ReviewerDecision, risk domain.RiskClass) agent.ReviewerResult {
	return agent.ReviewerResult{Decision: decision, Risk: risk, Rationale: "The exact bounded action matches the supplied policy facts."}
}

func acceptedReviewerBinding(
	t *testing.T,
	now func() time.Time,
	transport ActionReviewer,
	callLimit int,
) *ReviewerModelBinding {
	t.Helper()
	return reviewerBinding(t, now, transport, callLimit, true)
}

func reviewerBinding(
	t *testing.T,
	now func() time.Time,
	transport ActionReviewer,
	callLimit int,
	accepted bool,
) *ReviewerModelBinding {
	t.Helper()
	privacy, err := NewPrivacyManager(PrivacyManagerConfig{
		Store: &privacyTestStore{}, Role: domain.ModelRoleApprovalReviewer,
		Origin: "https://reviewer.example", PolicyVersion: PrivacyPolicyVersion, Now: now,
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	if accepted {
		review, reviewErr := privacy.Review(context.Background())
		if reviewErr != nil {
			t.Fatalf("Privacy Review() error = %v", reviewErr)
		}
		if _, decisionErr := privacy.Decide(context.Background(), PrivacyActionAccept, review.Revision, nil); decisionErr != nil {
			t.Fatalf("Privacy Decide() error = %v", decisionErr)
		}
	}
	limits, err := agent.ReviewerBudgetLimitsForProfile(agent.BudgetProfileCompact)
	if err != nil || callLimit < 1 || callLimit > limits.Calls {
		t.Fatalf("ReviewerBudgetLimitsForProfile() = %#v/%v", limits, err)
	}
	limits.Calls = callLimit
	limits.CostUnits = callLimit
	budget, err := agent.NewReviewerBudget(limits)
	if err != nil {
		t.Fatalf("NewReviewerBudget() error = %v", err)
	}
	return &ReviewerModelBinding{
		Profile: "approval_reviewer", Model: "fixture-reviewer", Available: transport != nil,
		Privacy: privacy, Budget: budget, Transport: transport,
	}
}

func configureAutoReview(t *testing.T, fixture *approvalCoordinatorFixture, binding *ReviewerModelBinding) {
	t.Helper()
	if err := fixture.coordinator.permissions.Reconfigure(
		context.Background(), PermissionChangeActorLocalUser,
		PermissionPolicy{Profile: domain.PermissionProfileAutoReview},
	); err != nil {
		t.Fatalf("Reconfigure(auto-review) error = %v", err)
	}
	status := fixture.coordinator.permissions.Status(fixture.clock.Now())
	fixture.intent.PermissionProfile = status.Profile
	fixture.intent.PolicyGeneration = status.PolicyGeneration
	fixture.coordinator.reviewer = binding
}

func countUIEvents(events []UIEvent, kind UIEventKind) int {
	count := 0
	for _, event := range events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func reviewerUIStates(events []UIEvent) []UIReviewerState {
	states := make([]UIReviewerState, 0, len(events))
	for _, event := range events {
		if event.Kind == UIEventReviewerState && event.Reviewer != nil {
			states = append(states, event.Reviewer.Status.State)
		}
	}
	return states
}
