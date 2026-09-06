package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type remoteAuthorizationResult = toolAuthorizationResult

func TestRemoteDiagnosticHumanApprovalReleasesOneConsumedEnvelope(t *testing.T) {
	fixture := newSupervisedRemoteDiagnosticFixture(t, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 3}, nil)
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	result := make(chan remoteAuthorizationResult, 1)
	go func() {
		envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan)
		result <- remoteAuthorizationResult{envelope: envelope, err: err}
	}()

	requested := receiveSupervisionEvent(t, fixture.events)
	if requested.Kind != UIEventApprovalRequested || requested.Approval == nil || requested.Approval.Validate() != nil ||
		requested.Approval.Operation != plan.Operation || requested.Approval.ParameterSummary == "" || requested.Approval.EffectSummary == "" {
		t.Fatalf("approval request = %#v", requested)
	}
	if fixture.persistence.consumes != 0 || fixture.revalidator.calls != 0 {
		t.Fatalf("pre-decision consume/revalidate = %d/%d", fixture.persistence.consumes, fixture.revalidator.calls)
	}
	command := approvalDecisionCommand(UICommandApproveAction, fixture.persistence.lastCreated, requested.Sequence, 1)
	approved, err := fixture.supervisor.Decide(context.Background(), command)
	if err != nil || approved.State != domain.ApprovalStateApproved {
		t.Fatalf("Decide() = %#v/%v", approved, err)
	}
	consumed, err := fixture.supervisor.ConsumeApprovedAction(context.Background(), command)
	if err != nil || consumed.Validate() != nil || consumed.ActionExecution == nil || consumed.ActionExecution.Authorization == nil {
		t.Fatalf("ConsumeApprovedAction() = %#v/%v", consumed, err)
	}
	closed := receiveSupervisionEvent(t, fixture.events)
	if closed.Kind != UIEventApprovalClosed || closed.ApprovalResult == nil || closed.ApprovalResult.State != domain.ApprovalStateConsumed {
		t.Fatalf("approval close event = %#v", closed)
	}
	authority := receiveRemoteAuthorization(t, result)
	if authority.err != nil || authority.envelope.Validate() != nil || !plan.MatchesIntent(authority.envelope.Intent) ||
		fixture.persistence.consumes != 1 || fixture.revalidator.calls != 1 {
		t.Fatalf("remote authority = %#v/%v consumes/revalidates=%d/%d", authority.envelope, authority.err, fixture.persistence.consumes, fixture.revalidator.calls)
	}
}

func TestRemoteDiagnosticHumanDenialClosesBeforeReturningZeroAuthority(t *testing.T) {
	fixture := newSupervisedRemoteDiagnosticFixture(t, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 3}, nil)
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	result := make(chan remoteAuthorizationResult, 1)
	go func() {
		envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan)
		result <- remoteAuthorizationResult{envelope: envelope, err: err}
	}()

	requested := receiveSupervisionEvent(t, fixture.events)
	command := approvalDecisionCommand(UICommandRejectAction, fixture.persistence.lastCreated, requested.Sequence, 2)
	denied, err := fixture.supervisor.Decide(context.Background(), command)
	if err != nil || denied.State != domain.ApprovalStateRejected {
		t.Fatalf("Decide(reject) = %#v/%v", denied, err)
	}
	closed := receiveSupervisionEvent(t, fixture.events)
	if closed.Kind != UIEventApprovalClosed || closed.ApprovalResult == nil || closed.ApprovalResult.State != domain.ApprovalStateRejected {
		t.Fatalf("denial close event = %#v", closed)
	}
	authority := receiveRemoteAuthorization(t, result)
	var classified interface{ Class() domain.SafeErrorClass }
	if authority.envelope != (domain.ActionEnvelope{}) || !errors.As(authority.err, &classified) || classified.Class() != domain.SafeErrorClassPolicyDenied ||
		fixture.persistence.consumes != 0 || fixture.revalidator.calls != 0 {
		t.Fatalf("denied authority = %#v/%v consumes/revalidates=%d/%d", authority.envelope, authority.err, fixture.persistence.consumes, fixture.revalidator.calls)
	}
}

func TestRemoteDiagnosticPendingApprovalCancellationUnblocksWithoutAuthority(t *testing.T) {
	fixture := newSupervisedRemoteDiagnosticFixture(t, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 3}, nil)
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan remoteAuthorizationResult, 1)
	go func() {
		envelope, err := fixture.gate.AuthorizeAndConsume(ctx, plan)
		result <- remoteAuthorizationResult{envelope: envelope, err: err}
	}()

	requested := receiveSupervisionEvent(t, fixture.events)
	if requested.Kind != UIEventApprovalRequested {
		t.Fatalf("approval request = %#v", requested)
	}
	cancel()
	closed := receiveSupervisionEvent(t, fixture.events)
	if closed.Kind != UIEventApprovalClosed || closed.ApprovalResult == nil ||
		closed.ApprovalResult.State != domain.ApprovalStateCancelled {
		t.Fatalf("cancel close event = %#v", closed)
	}
	authority := receiveRemoteAuthorization(t, result)
	var classified interface{ Class() domain.SafeErrorClass }
	if authority.envelope != (domain.ActionEnvelope{}) || !errors.As(authority.err, &classified) ||
		classified.Class() != domain.SafeErrorClassCancelled || fixture.persistence.consumes != 0 || fixture.revalidator.calls != 0 {
		t.Fatalf("cancelled authority = %#v/%v consumes/revalidates=%d/%d", authority.envelope, authority.err, fixture.persistence.consumes, fixture.revalidator.calls)
	}
}

func TestRemoteDiagnosticPostConsumeScopeChangeReturnsAuditIdentityWithoutAttemptAuthority(t *testing.T) {
	fixture := newSupervisedRemoteDiagnosticFixture(t, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 3}, nil)
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	result := make(chan remoteAuthorizationResult, 1)
	go func() {
		envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan)
		result <- remoteAuthorizationResult{envelope: envelope, err: err}
	}()

	requested := receiveSupervisionEvent(t, fixture.events)
	command := approvalDecisionCommand(UICommandApproveAction, fixture.persistence.lastCreated, requested.Sequence, 3)
	approved, err := fixture.supervisor.Decide(context.Background(), command)
	if err != nil || approved.State != domain.ApprovalStateApproved {
		t.Fatalf("Decide() = %#v/%v", approved, err)
	}
	fixture.persistence.afterConsume = func() {
		fixture.scope.scope.Generation++
	}
	consumed, err := fixture.supervisor.ConsumeApprovedAction(context.Background(), command)
	if !errors.Is(err, ErrPermissionStale) || consumed.State != domain.ApprovalStateConsumed ||
		consumed.ActionExecution == nil || consumed.ActionExecution.Authorization == nil {
		t.Fatalf("ConsumeApprovedAction() = %#v/%v", consumed, err)
	}
	closed := receiveSupervisionEvent(t, fixture.events)
	if closed.Kind != UIEventApprovalClosed || closed.ApprovalResult == nil || closed.ApprovalResult.State != domain.ApprovalStateConsumed {
		t.Fatalf("consumed close event = %#v", closed)
	}
	authority := receiveRemoteAuthorization(t, result)
	var classified interface{ Class() domain.SafeErrorClass }
	if authority.envelope.Validate() != nil || !plan.MatchesIntent(authority.envelope.Intent) ||
		!errors.As(authority.err, &classified) || classified.Class() != domain.SafeErrorClassStaleScope ||
		fixture.persistence.consumes != 1 || fixture.revalidator.calls != 1 {
		t.Fatalf("stale consumed identity = %#v/%v consumes/revalidates=%d/%d", authority.envelope, authority.err, fixture.persistence.consumes, fixture.revalidator.calls)
	}
}

func TestRemoteDiagnosticCancellationAfterConsumeRetainsAuditIdentity(t *testing.T) {
	fixture := newSupervisedRemoteDiagnosticFixture(t, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 3}, nil)
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodExec, domain.RiskCritical)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan remoteAuthorizationResult, 1)
	go func() {
		envelope, err := fixture.gate.AuthorizeAndConsume(ctx, plan)
		result <- remoteAuthorizationResult{envelope: envelope, err: err}
	}()

	requested := receiveSupervisionEvent(t, fixture.events)
	command := approvalDecisionCommand(UICommandApproveAction, fixture.persistence.lastCreated, requested.Sequence, 4)
	approved, err := fixture.supervisor.Decide(context.Background(), command)
	if err != nil || approved.State != domain.ApprovalStateApproved {
		t.Fatalf("Decide() = %#v/%v", approved, err)
	}
	publishEntered := make(chan struct{})
	releasePublish := make(chan struct{})
	publishReleased := false
	defer func() {
		if !publishReleased {
			close(releasePublish)
		}
	}()
	fixture.eventSink.beforePublish = func(ctx context.Context, event UIEvent) error {
		if event.Kind != UIEventApprovalClosed {
			return nil
		}
		close(publishEntered)
		select {
		case <-releasePublish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	consumeResult := make(chan remoteAuthorizationResult, 1)
	go func() {
		_, consumeErr := fixture.supervisor.ConsumeApprovedAction(context.Background(), command)
		consumeResult <- remoteAuthorizationResult{err: consumeErr}
	}()
	select {
	case <-publishEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for post-consume delivery barrier")
	}
	cancel()
	select {
	case early := <-result:
		t.Fatalf("authorization returned before consumed identity was available: %#v", early)
	default:
	}
	close(releasePublish)
	publishReleased = true
	consumed := receiveRemoteAuthorization(t, consumeResult)
	if consumed.err != nil {
		t.Fatalf("ConsumeApprovedAction() error = %v", consumed.err)
	}
	closed := receiveSupervisionEvent(t, fixture.events)
	if closed.Kind != UIEventApprovalClosed || closed.ApprovalResult == nil || closed.ApprovalResult.State != domain.ApprovalStateConsumed {
		t.Fatalf("consumed close event = %#v", closed)
	}
	authority := receiveRemoteAuthorization(t, result)
	var classified interface{ Class() domain.SafeErrorClass }
	if authority.envelope.Validate() != nil || !plan.MatchesIntent(authority.envelope.Intent) ||
		!errors.As(authority.err, &classified) || classified.Class() != domain.SafeErrorClassCancelled {
		t.Fatalf("cancelled consumed identity = %#v/%v", authority.envelope, authority.err)
	}
}

func TestRemoteDiagnosticReviewerDecisionIsVisibleAndCannotImpersonateHuman(t *testing.T) {
	transport := &recordingActionReviewer{result: reviewerResult(agent.ReviewerDecisionApprove, domain.RiskReview)}
	fixture := newSupervisedRemoteDiagnosticFixture(t, PermissionPolicy{Profile: domain.PermissionProfileAutoReview, Generation: 3}, transport)
	plan := remoteDiagnosticGatePlan(t, domain.ActionOperationPodDiagnostic, domain.RiskReview)
	plan.RiskSummary = domain.PodDiagnosticRiskSummary
	result := make(chan remoteAuthorizationResult, 1)
	go func() {
		envelope, err := fixture.gate.AuthorizeAndConsume(context.Background(), plan)
		result <- remoteAuthorizationResult{envelope: envelope, err: err}
	}()

	reviewing := receiveSupervisionEvent(t, fixture.events)
	approved := receiveSupervisionEvent(t, fixture.events)
	closed := receiveSupervisionEvent(t, fixture.events)
	if reviewing.Kind != UIEventReviewerState || reviewing.Reviewer.Status.State != UIReviewerReviewing ||
		approved.Kind != UIEventReviewerState || approved.Reviewer.Status.State != UIReviewerApproved ||
		approved.Reviewer.Status.Profile != "approval_reviewer" || closed.Kind != UIEventApprovalClosed ||
		closed.ApprovalResult.State != domain.ApprovalStateConsumed || transport.calls != 1 {
		t.Fatalf("Reviewer lifecycle = %#v / %#v / %#v calls=%d", reviewing, approved, closed, transport.calls)
	}
	authority := receiveRemoteAuthorization(t, result)
	if authority.err != nil || authority.envelope.Validate() != nil || fixture.persistence.lastDecision.Actor != domain.ApprovalActorReviewer ||
		fixture.persistence.lastDecision.Disposition != domain.ReviewDispositionReviewer || fixture.revalidator.calls != 1 {
		t.Fatalf("Reviewer authority = %#v/%v decision=%#v", authority.envelope, authority.err, fixture.persistence.lastDecision)
	}
}

func TestConsumedRemoteInvalidationReasonsNeverReleaseAttemptAuthority(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason domain.ApprovalStateReason
		class  domain.SafeErrorClass
	}{
		{domain.ApprovalReasonScopeChanged, domain.SafeErrorClassStaleScope},
		{domain.ApprovalReasonPolicyChanged, domain.SafeErrorClassPolicyDenied},
		{domain.ApprovalReasonTargetChanged, domain.SafeErrorClassConflict},
		{domain.ApprovalReasonRunCancelled, domain.SafeErrorClassCancelled},
	}
	for _, test := range tests {
		var classified interface{ Class() domain.SafeErrorClass }
		err := remoteInvalidationError(test.reason)
		if !errors.As(err, &classified) || classified.Class() != test.class {
			t.Fatalf("remoteInvalidationError(%q) = %v, want %s", test.reason, err, test.class)
		}
	}
}

type supervisedRemoteDiagnosticFixture struct {
	gate        *RemoteDiagnosticActionGate
	supervisor  *ApprovalCoordinator
	persistence *fakeApprovalPersistence
	revalidator *fakeRemoteDiagnosticRevalidator
	scope       *fakeApprovalCurrentScope
	events      <-chan UIEvent
	eventSink   *channelApprovalUIEvents
}

func newSupervisedRemoteDiagnosticFixture(t *testing.T, policy PermissionPolicy, reviewer ActionReviewer) supervisedRemoteDiagnosticFixture {
	t.Helper()
	clock := &approvalCoordinatorClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	persistence := &fakeApprovalPersistence{}
	identifiers := &approvalCoordinatorIDs{}
	service, err := approval.NewService(approval.ServiceConfig{Clock: clock, Nonces: approvalNonceSource{value: 0x73}, Store: persistence, AuditIDs: identifiers})
	if err != nil {
		t.Fatalf("approval.NewService() error = %v", err)
	}
	permissions, err := NewPermissionManager(policy)
	if err != nil {
		t.Fatalf("NewPermissionManager() error = %v", err)
	}
	if err := permissions.BindSession("00000000-0000-7000-8000-000000078002"); err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}
	scope := &fakeApprovalCurrentScope{scope: remoteDiagnosticGateScope()}
	revalidator := &fakeRemoteDiagnosticRevalidator{}
	eventChannel := make(chan UIEvent, 16)
	eventSink := &channelApprovalUIEvents{events: eventChannel}
	restartExecutor := &fakeApprovalExecutor{}
	var binding *ReviewerModelBinding
	if reviewer != nil {
		binding = acceptedReviewerBinding(t, clock.Now, reviewer, 2)
	}
	supervisor, err := NewApprovalCoordinator(ApprovalCoordinatorConfig{
		Service: service, Persistence: persistence, ResultAudits: persistence, Scope: scope,
		ApprovalIDs: identifiers, AuditIDs: identifiers, UIEvents: eventSink,
		Rollout: newFakeApprovalRolloutObserver(clock.Now()), RestartRevalidator: restartExecutor,
		RestartExecutor: restartExecutor, RemoteDiagnostics: revalidator, Observations: fakeObservationRevalidator{},
		ObservationPolicy: domain.DisabledObservabilityPolicyCatalog(), Permissions: permissions,
		Reviewer: binding, Reviews: persistence, Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("NewApprovalCoordinator() error = %v", err)
	}
	gate, err := NewRemoteDiagnosticActionGate(RemoteDiagnosticActionGateConfig{
		Service: service, Persistence: persistence, ResultAudits: persistence, Scope: scope,
		Revalidator: revalidator, Identifiers: identifiers, Permissions: permissions, Supervisor: supervisor,
		PolicyCatalog: remoteDiagnosticGatePolicyCatalog(t), Now: clock.Now, PersistenceTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewRemoteDiagnosticActionGate() error = %v", err)
	}
	return supervisedRemoteDiagnosticFixture{
		gate: gate, supervisor: supervisor, persistence: persistence, revalidator: revalidator,
		scope: scope, events: eventChannel, eventSink: eventSink,
	}
}

type channelApprovalUIEvents struct {
	events        chan<- UIEvent
	beforePublish func(context.Context, UIEvent) error
}

func (sink *channelApprovalUIEvents) PublishUIEvent(ctx context.Context, event UIEvent) error {
	if sink.beforePublish != nil {
		if err := sink.beforePublish(ctx, event); err != nil {
			return err
		}
	}
	select {
	case sink.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func receiveSupervisionEvent(t *testing.T, events <-chan UIEvent) UIEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for supervision event")
		return UIEvent{}
	}
}

func receiveRemoteAuthorization(t *testing.T, results <-chan remoteAuthorizationResult) remoteAuthorizationResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for remote authorization")
		return remoteAuthorizationResult{}
	}
}
