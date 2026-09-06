package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestObservationHumanApprovalReleasesOneConsumedEnvelopeAndContentFreeOutcome(t *testing.T) {
	fixture := newSupervisedRemoteDiagnosticFixture(t, PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 3}, nil)
	revalidator := &recordingObservationRevalidator{}
	fixture.supervisor.observations = revalidator
	plan := applicationObservationPlan(t, domain.ActionObservationPodLog)
	if err := fixture.supervisor.PrepareObservationAction(context.Background(), observationPreflightForPlan(plan)); err != nil {
		t.Fatalf("PrepareObservationAction() error = %v", err)
	}

	result := make(chan toolAuthorizationResult, 1)
	go func() {
		envelope, err := fixture.supervisor.AuthorizeObservationAction(context.Background(), plan)
		result <- toolAuthorizationResult{envelope: envelope, err: err}
	}()
	requested := receiveSupervisionEvent(t, fixture.events)
	if requested.Kind != UIEventApprovalRequested || requested.Approval == nil || requested.Approval.Validate() != nil ||
		requested.Approval.Operation != domain.ActionOperationLogsCurrent ||
		!strings.Contains(requested.Approval.ParameterSummary, `container="app"`) ||
		!strings.Contains(requested.Approval.EffectSummary, "sensitive_read") {
		t.Fatalf("observation approval request = %#v", requested)
	}
	select {
	case early := <-result:
		t.Fatalf("authority returned before a decision: %#v", early)
	default:
	}
	command := approvalDecisionCommand(UICommandApproveAction, fixture.persistence.lastCreated, requested.Sequence, 61)
	if approved, err := fixture.supervisor.Decide(context.Background(), command); err != nil || approved.State != domain.ApprovalStateApproved {
		t.Fatalf("Decide() = %#v/%v", approved, err)
	}
	consumed, err := fixture.supervisor.ConsumeApprovedAction(context.Background(), command)
	if err != nil || consumed.Validate() != nil || consumed.ActionExecution == nil || consumed.ActionExecution.Authorization == nil {
		t.Fatalf("ConsumeApprovedAction() = %#v/%v", consumed, err)
	}
	closed := receiveSupervisionEvent(t, fixture.events)
	if closed.Kind != UIEventApprovalClosed || closed.ApprovalResult == nil || closed.ApprovalResult.State != domain.ApprovalStateConsumed {
		t.Fatalf("observation approval close = %#v", closed)
	}
	authority := receiveRemoteAuthorization(t, result)
	if authority.err != nil || authority.envelope.Validate() != nil || !plan.MatchesIntent(authority.envelope.Intent) ||
		fixture.persistence.consumes != 1 || revalidator.calls != 1 {
		t.Fatalf("observation authority = %#v/%v consumes/revalidates=%d/%d", authority.envelope, authority.err, fixture.persistence.consumes, revalidator.calls)
	}
	if err := fixture.supervisor.RecordObservationOutcome(context.Background(), authority.envelope, domain.ObservationActionOutcome{
		State: domain.ObservationActionSucceeded, ResultItems: 1, ResultLines: 2, ResultBytes: 17,
	}); err != nil {
		t.Fatalf("RecordObservationOutcome() error = %v", err)
	}
	if len(fixture.persistence.writeResultAudits) != 1 {
		t.Fatalf("outcome audits = %d, want one", len(fixture.persistence.writeResultAudits))
	}
	audit := fixture.persistence.writeResultAudits[0]
	auditDetails, marshalErr := json.Marshal(audit.Details)
	if audit.Details.Count == nil || *audit.Details.Count != 17 || audit.Details.DetailCode == nil ||
		*audit.Details.DetailCode != "observation_succeeded" || marshalErr != nil || strings.Contains(string(auditDetails), "synthetic-log-canary") {
		t.Fatalf("content-free observation audit = %#v", audit)
	}
}

func TestObservationFullAccessAndReviewerRoutesKeepActorIdentity(t *testing.T) {
	t.Run("explicit full access", func(t *testing.T) {
		policy := PermissionPolicy{Profile: domain.PermissionProfileFullAccess, Generation: 3, FullAccessAllowed: true, HighRiskAcknowledged: true}
		fixture := newSupervisedRemoteDiagnosticFixture(t, policy, nil)
		revalidator := &recordingObservationRevalidator{}
		fixture.supervisor.observations = revalidator
		fixture.supervisor.observationPolicy = applicationObservabilityCatalog(t)
		plan := applicationObservationPlan(t, domain.ActionObservationPrometheus)
		if err := fixture.supervisor.PrepareObservationAction(context.Background(), observationPreflightForPlan(plan)); err != nil {
			t.Fatalf("PrepareObservationAction() error = %v", err)
		}
		envelope, err := fixture.supervisor.AuthorizeObservationAction(context.Background(), plan)
		if err != nil || envelope.Validate() != nil || revalidator.calls != 1 || fixture.persistence.consumes != 1 ||
			fixture.persistence.lastDecision.Actor != domain.ApprovalActorPermissionPolicy {
			t.Fatalf("automatic authority = %#v/%v revalidates=%d consumes=%d decision=%#v", envelope, err, revalidator.calls, fixture.persistence.consumes, fixture.persistence.lastDecision)
		}
		closed := receiveSupervisionEvent(t, fixture.events)
		if closed.Kind != UIEventApprovalClosed || closed.ApprovalResult == nil || closed.ApprovalResult.State != domain.ApprovalStateConsumed {
			t.Fatalf("automatic close = %#v", closed)
		}
	})

	t.Run("Reviewer", func(t *testing.T) {
		transport := &recordingActionReviewer{result: reviewerResult(agent.ReviewerDecisionApprove, domain.RiskReview)}
		fixture := newSupervisedRemoteDiagnosticFixture(t, PermissionPolicy{Profile: domain.PermissionProfileAutoReview, Generation: 3}, transport)
		revalidator := &recordingObservationRevalidator{}
		fixture.supervisor.observations = revalidator
		plan := applicationObservationPlan(t, domain.ActionObservationPodLog)
		envelope, err := fixture.supervisor.AuthorizeObservationAction(context.Background(), plan)
		if err != nil || envelope.Validate() != nil || transport.calls != 1 || revalidator.calls != 1 ||
			fixture.persistence.lastDecision.Actor != domain.ApprovalActorReviewer ||
			fixture.persistence.lastDecision.Actor == domain.ApprovalActorLocalUser {
			t.Fatalf("Reviewer authority = %#v/%v calls/revalidates=%d/%d decision=%#v", envelope, err, transport.calls, revalidator.calls, fixture.persistence.lastDecision)
		}
		for _, want := range []UIReviewerState{UIReviewerReviewing, UIReviewerApproved} {
			event := receiveSupervisionEvent(t, fixture.events)
			if event.Kind != UIEventReviewerState || event.Reviewer == nil || event.Reviewer.Status.State != want {
				t.Fatalf("Reviewer event = %#v, want %s", event, want)
			}
		}
		closed := receiveSupervisionEvent(t, fixture.events)
		if closed.Kind != UIEventApprovalClosed || closed.ApprovalResult == nil || closed.ApprovalResult.State != domain.ApprovalStateConsumed {
			t.Fatalf("Reviewer close = %#v", closed)
		}
	})
}

func TestObservationPreflightDenialsPerformNoApprovalOrTargetRevalidation(t *testing.T) {
	tests := []struct {
		name       string
		policy     PermissionPolicy
		kind       domain.ActionObservationKind
		configure  bool
		generation domain.PolicyGeneration
		want       domain.SafeErrorClass
	}{
		{name: "read-only network egress", policy: PermissionPolicy{Profile: domain.PermissionProfileReadOnly, Generation: 3}, kind: domain.ActionObservationPrometheus, configure: true, generation: 3, want: domain.SafeErrorClassPolicyDenied},
		{name: "disabled source", policy: PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 3}, kind: domain.ActionObservationPrometheus, generation: 3, want: domain.SafeErrorClassPolicyDenied},
		{name: "stale generation", policy: PermissionPolicy{Profile: domain.PermissionProfileAsk, Generation: 3}, kind: domain.ActionObservationPodLog, generation: 2, want: domain.SafeErrorClassStaleScope},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSupervisedRemoteDiagnosticFixture(t, test.policy, nil)
			revalidator := &recordingObservationRevalidator{}
			fixture.supervisor.observations = revalidator
			if test.configure {
				fixture.supervisor.observationPolicy = applicationObservabilityCatalog(t)
			}
			plan := applicationObservationPlan(t, test.kind)
			plan.PolicyGeneration = test.generation
			preflight := observationPreflightForPlan(plan)
			err := fixture.supervisor.PrepareObservationAction(context.Background(), preflight)
			var classified interface{ Class() domain.SafeErrorClass }
			if !errors.As(err, &classified) || classified.Class() != test.want || fixture.persistence.creates != 0 ||
				fixture.persistence.consumes != 0 || revalidator.calls != 0 {
				t.Fatalf("denial = %v class=%v creates/consumes/revalidates=%d/%d/%d", err, classified, fixture.persistence.creates, fixture.persistence.consumes, revalidator.calls)
			}
		})
	}
}

type recordingObservationRevalidator struct {
	calls int
	err   error
	plan  domain.ObservationActionPlan
}

func (revalidator *recordingObservationRevalidator) RevalidateObservationAction(_ context.Context, plan domain.ObservationActionPlan) error {
	revalidator.calls++
	revalidator.plan = plan
	return revalidator.err
}

func applicationObservationPlan(t *testing.T, kind domain.ActionObservationKind) domain.ObservationActionPlan {
	t.Helper()
	parameters := domain.ActionObservationParameters{Kind: kind, WindowSeconds: 300}
	limits := domain.ActionLimits{MaximumBytes: 4096, MaximumOutput: 4096}
	origin := domain.ActionDigest("")
	subresource := ""
	switch kind {
	case domain.ActionObservationPodLog:
		parameters.Container, parameters.TailLines = "app", 20
		limits.Timeout, limits.MaximumItems, limits.MaximumLines = domain.ObservationKubernetesTimeout, 1, 20
		subresource = "log"
	case domain.ActionObservationPrometheus:
		parameters.QueryID, parameters.StepSeconds, parameters.SeriesLimit = domain.QueryPrometheusPodCPUUsage, 30, 2
		limits.Timeout, limits.MaximumItems = 5*time.Second, 2
		origin = domain.ActionDigest(strings.Repeat("a", 64))
	case domain.ActionObservationLoki:
		parameters.QueryID, parameters.Search, parameters.LineLimit = domain.QueryLokiPodLogs, "error", 20
		limits.Timeout, limits.MaximumItems, limits.MaximumLines = 5*time.Second, 2, 20
		origin = domain.ActionDigest(strings.Repeat("b", 64))
	default:
		t.Fatalf("unsupported observation kind %q", kind)
	}
	typed := domain.ActionParameters{Kind: domain.ActionParametersObservation, Observation: parameters}
	return domain.ObservationActionPlan{
		RunID: "00000000-0000-7000-8000-000000078001", SessionID: "00000000-0000-7000-8000-000000078002",
		Scope: remoteDiagnosticGateScope(), PolicyGeneration: 3, Operation: operationForObservationTest(kind),
		Target:     domain.ActionTarget{Resource: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod", UID: "pod-uid", ResourceVersion: "21"}, Subresource: subresource, Fingerprint: string(typed.Digest())},
		Parameters: typed, Limits: limits, OriginHash: origin, ReasonSummary: "Read one exact bounded observation.",
	}
}

func operationForObservationTest(kind domain.ActionObservationKind) domain.ActionOperation {
	switch kind {
	case domain.ActionObservationPodLog:
		return domain.ActionOperationLogsCurrent
	case domain.ActionObservationPrometheus:
		return domain.ActionOperationPrometheusQuery
	case domain.ActionObservationLoki:
		return domain.ActionOperationLokiQuery
	default:
		return ""
	}
}

func applicationObservabilityCatalog(t *testing.T) domain.ObservabilityPolicyCatalog {
	t.Helper()
	catalog, err := domain.NewObservabilityPolicyCatalog(
		domain.DataSourcePolicy{Kind: domain.DataSourcePrometheus, OriginHash: strings.Repeat("a", 64), Queries: []domain.ObservabilityQueryID{domain.QueryPrometheusPodCPUUsage}, RequestTimeout: 5 * time.Second},
		domain.DataSourcePolicy{Kind: domain.DataSourceLoki, OriginHash: strings.Repeat("b", 64), Queries: []domain.ObservabilityQueryID{domain.QueryLokiPodLogs}, RequestTimeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatalf("NewObservabilityPolicyCatalog() error = %v", err)
	}
	return catalog
}
