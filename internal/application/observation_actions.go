package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrObservationActionInvalid = errors.New("observation action is invalid")

// ObservationActionRevalidator performs only the fresh Pod identity check
// required after a decision. Log and optional data-source reads are absent
// from this port and remain Tool-owned.
type ObservationActionRevalidator interface {
	RevalidateObservationAction(context.Context, domain.ObservationActionPlan) error
}

// PrepareObservationAction applies hard admission and permission routing before
// the Tool performs even its safe target-preparation GET.
func (coordinator *ApprovalCoordinator) PrepareObservationAction(
	ctx context.Context,
	preflight domain.ObservationActionPreflight,
) error {
	if coordinator == nil || ctx == nil || ctx.Err() != nil || preflight.Validate() != nil ||
		!coordinator.observationPreflightAdmitted(preflight) {
		return toolAuthorizationError{class: domain.SafeErrorClassPolicyDenied}
	}
	policy, _, err := coordinator.PrepareActionPolicy(
		preflight.SessionID, preflight.Scope.Snapshot(), preflight.Operation,
		preflight.Effect(), domain.RiskReview, true,
	)
	if err != nil {
		return toolAuthorizationFailure(ctx, err)
	}
	if policy.Generation != preflight.PolicyGeneration {
		return toolAuthorizationError{class: domain.SafeErrorClassStaleScope}
	}
	return nil
}

// AuthorizeObservationAction routes the exact target-bound plan through the
// shared automatic, Session-rule, Reviewer, or human lifecycle and blocks the
// Tool until durable consumption or safe closure.
func (coordinator *ApprovalCoordinator) AuthorizeObservationAction(
	ctx context.Context,
	plan domain.ObservationActionPlan,
) (domain.ActionEnvelope, error) {
	if coordinator == nil || ctx == nil || coordinator.observations == nil || plan.Validate() != nil ||
		!coordinator.observationPlanAdmitted(plan) {
		return domain.ActionEnvelope{}, toolAuthorizationError{class: domain.SafeErrorClassPolicyDenied}
	}
	preflight := observationPreflightForPlan(plan)
	policy, _, err := coordinator.PrepareActionPolicy(
		plan.SessionID, plan.Scope.Snapshot(), plan.Operation, preflight.Effect(), domain.RiskReview, true,
	)
	if err != nil {
		return domain.ActionEnvelope{}, toolAuthorizationFailure(ctx, err)
	}
	if policy.Generation != plan.PolicyGeneration {
		return domain.ActionEnvelope{}, toolAuthorizationError{class: domain.SafeErrorClassStaleScope}
	}
	intent, err := plan.Intent(policy.Profile)
	if err != nil {
		return domain.ActionEnvelope{}, toolAuthorizationError{class: domain.SafeErrorClassPolicyDenied}
	}
	completion := make(chan toolAuthorizationResult, 1)
	return coordinator.authorizeToolAction(ctx, plan.RunID, plan.SessionID, intent, trackedAction{
		kind: trackedActionObservation, observation: plan, authorizationCompletion: completion,
	})
}

func observationPreflightForPlan(plan domain.ObservationActionPlan) domain.ObservationActionPreflight {
	preflight := domain.ObservationActionPreflight{
		RunID: plan.RunID, SessionID: plan.SessionID, Scope: plan.Scope,
		PolicyGeneration: plan.PolicyGeneration, Operation: plan.Operation,
	}
	parameters := plan.Parameters.Observation
	switch parameters.Kind {
	case domain.ActionObservationPrometheus:
		preflight.SourceKind, preflight.OriginHash, preflight.QueryID = domain.DataSourcePrometheus, plan.OriginHash, parameters.QueryID
	case domain.ActionObservationLoki:
		preflight.SourceKind, preflight.OriginHash, preflight.QueryID = domain.DataSourceLoki, plan.OriginHash, parameters.QueryID
	}
	return preflight
}

func (coordinator *ApprovalCoordinator) observationPreflightAdmitted(preflight domain.ObservationActionPreflight) bool {
	if coordinator == nil || preflight.Validate() != nil || coordinator.observationPolicy.Validate() != nil {
		return false
	}
	if preflight.SourceKind == "" {
		return true
	}
	policy, ok := coordinator.observationPolicy.Resolve(preflight.SourceKind)
	return ok && domain.ActionDigest(policy.OriginHash).Equal(preflight.OriginHash) && policy.Allows(preflight.QueryID)
}

func (coordinator *ApprovalCoordinator) observationPlanAdmitted(plan domain.ObservationActionPlan) bool {
	if plan.Validate() != nil {
		return false
	}
	preflight := observationPreflightForPlan(plan)
	if !coordinator.observationPreflightAdmitted(preflight) {
		return false
	}
	if preflight.SourceKind == "" {
		return true
	}
	policy, ok := coordinator.observationPolicy.Resolve(preflight.SourceKind)
	return ok && plan.Limits.Timeout == policy.RequestTimeout
}

// RecordObservationOutcome durably appends one content-free terminal result.
// Its persistence context is independently bounded so cancellation cannot
// erase the record of an already-consumed or attempted action.
func (coordinator *ApprovalCoordinator) RecordObservationOutcome(
	ctx context.Context,
	envelope domain.ActionEnvelope,
	outcome domain.ObservationActionOutcome,
) error {
	if coordinator == nil || ctx == nil || envelope.Validate() != nil || outcome.Validate(envelope.Intent) != nil {
		return toolAuthorizationError{class: domain.SafeErrorClassInvalidInput}
	}
	id, err := coordinator.auditIDs.NewAuditEventID()
	if err != nil || !id.Valid() {
		return toolAuthorizationError{class: domain.SafeErrorClassInternal}
	}
	event, err := observationOutcomeAudit(id, envelope, outcome, coordinator.now())
	if err != nil || coordinator.persistActionResultAudit(event) != nil {
		return toolAuthorizationError{class: domain.SafeErrorClassPersistenceUnavailable}
	}
	return nil
}

func observationOutcomeAudit(
	id domain.AuditEventID,
	envelope domain.ActionEnvelope,
	outcome domain.ObservationActionOutcome,
	occurredAt time.Time,
) (domain.AuditEvent, error) {
	if !id.Valid() || envelope.Validate() != nil || outcome.Validate(envelope.Intent) != nil ||
		!validCoordinatorTime(occurredAt) || occurredAt.Before(envelope.RequestedAt) {
		return domain.AuditEvent{}, ErrObservationActionInvalid
	}
	eventType, auditOutcome, detail := domain.AuditEventWriteVerified, domain.AuditOutcomeSuccess, "observation_succeeded"
	switch outcome.State {
	case domain.ObservationActionNotAttempted:
		eventType, auditOutcome, detail = domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeDenied, "observation_not_attempted"
	case domain.ObservationActionFailed:
		eventType, auditOutcome, detail = domain.AuditEventWriteAttempted, domain.AuditOutcomeFailure, "observation_failed"
	}
	operation, policyVersion := string(envelope.Intent.Operation), envelope.Intent.PolicyVersion
	count := int64(outcome.ResultBytes)
	details := domain.AuditDetails{
		Operation: &operation, DetailCode: &detail, PolicyVersion: &policyVersion, Count: &count,
	}
	if outcome.ErrorClass.Valid() {
		class := outcome.ErrorClass
		details.ErrorClass = &class
	}
	sessionID, runID := envelope.SessionID, envelope.RunID
	scope, subject := envelope.Intent.Scope, envelope.Intent.Target.Resource
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, RunID: &runID, Type: eventType,
		Actor: domain.AuditActorSystem, Outcome: auditOutcome, Scope: &scope, Subject: &subject,
		Details: details, CorrelationID: string(envelope.RequestID), IntegrityHash: string(envelope.Digest),
		OccurredAt: occurredAt,
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrObservationActionInvalid
	}
	return event, nil
}
