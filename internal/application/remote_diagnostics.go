package application

import (
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrRemoteDiagnosticActionInvalid = errors.New("remote diagnostic action is invalid")

const remoteDiagnosticPodAuditEventCount = 5

// NewRemoteDiagnosticActionIntent stamps one normalized remote diagnostic
// plan with the current deterministic permission snapshot. It performs no I/O
// and creates no authority by itself.
func NewRemoteDiagnosticActionIntent(
	policy PermissionPolicy,
	plan domain.RemoteDiagnosticActionPlan,
) (domain.ActionIntent, error) {
	if policy.Validate() != nil || plan.Validate() != nil || policy.Generation != plan.PolicyGeneration {
		return domain.ActionIntent{}, ErrRemoteDiagnosticActionInvalid
	}
	intent := domain.ActionIntent{
		Operation:              plan.Operation,
		OperationSchemaVersion: plan.Operation.SchemaVersion(),
		PolicyVersion:          domain.ActionPolicyVersion,
		PermissionProfile:      policy.Profile,
		Risk:                   plan.Risk,
		Effect:                 plan.Effect,
		PolicyGeneration:       plan.PolicyGeneration,
		Scope:                  plan.Scope.Snapshot(),
		NamespaceAccess:        plan.Scope.NamespaceAccess,
		Target:                 plan.Target,
		Parameters:             plan.Parameters,
		Stdin:                  false,
		TTY:                    false,
		Shell:                  false,
		DataCategories:         plan.DataCategories,
		AllowedSinks:           plan.AllowedSinks,
		NetworkEffects:         plan.NetworkEffects,
		NetworkDestinationHash: plan.NetworkDestinationHash,
		Limits:                 plan.Limits,
		VerificationPlanID:     plan.VerificationPlan,
		ReasonSummary:          plan.ReasonSummary,
		RiskSummary:            plan.RiskSummary,
	}
	if intent.Validate() != nil || !plan.MatchesIntent(intent) {
		return domain.ActionIntent{}, ErrRemoteDiagnosticActionInvalid
	}
	return intent, nil
}

// EvaluateRemoteDiagnosticPermission applies the common profile matrix to an
// already-normalized plan. The plan fixes risk and effect; caller text cannot
// lower either value.
func EvaluateRemoteDiagnosticPermission(
	policy PermissionPolicy,
	plan domain.RemoteDiagnosticActionPlan,
	enabled bool,
) PermissionEvaluation {
	if plan.Validate() != nil {
		return PermissionEvaluation{Disposition: domain.ReviewDispositionDeny, ReasonCode: "invalid_policy"}
	}
	return EvaluatePermission(policy, PermissionEvaluationInput{
		Operation: plan.Operation, Effect: plan.Effect, Risk: plan.Risk,
		CapabilityAdmitted: true, CapabilityEnabled: enabled,
	})
}

// NewRemoteDiagnosticActionEnvelope creates the exact digest-bound envelope
// used by an approval implementation. It does not imply that the request was
// approved, durably consumed, or executed.
func NewRemoteDiagnosticActionEnvelope(
	requestID domain.ApprovalID,
	policy PermissionPolicy,
	plan domain.RemoteDiagnosticActionPlan,
	requestedAt time.Time,
) (domain.ActionEnvelope, error) {
	intent, err := NewRemoteDiagnosticActionIntent(policy, plan)
	if err != nil {
		return domain.ActionEnvelope{}, err
	}
	envelope, err := domain.NewActionEnvelope(requestID, plan.SessionID, plan.RunID, intent, requestedAt)
	if err != nil || envelope.Validate() != nil || !plan.MatchesIntent(envelope.Intent) {
		return domain.ActionEnvelope{}, ErrRemoteDiagnosticActionInvalid
	}
	return envelope, nil
}

// NewRemoteDiagnosticOutcomeAudit builds a bounded content-free action audit.
// It deliberately contains no command output, file data, Pod log, image text,
// network address, raw error, or vendor response.
func NewRemoteDiagnosticOutcomeAudit(
	id domain.AuditEventID,
	envelope domain.ActionEnvelope,
	outcome domain.RemoteDiagnosticOutcome,
	occurredAt time.Time,
) (domain.AuditEvent, error) {
	if !id.Valid() || envelope.Validate() != nil || outcome.Validate(envelope.Intent.Operation) != nil ||
		!validCoordinatorTime(occurredAt) || occurredAt.Before(envelope.RequestedAt) {
		return domain.AuditEvent{}, ErrRemoteDiagnosticActionInvalid
	}
	eventType := domain.AuditEventWriteVerified
	auditOutcome := domain.AuditOutcomeSuccess
	if outcome.State == domain.RemoteDiagnosticOutcomeUnknown || outcome.CleanupState == domain.DiagnosticPodCleanupUnknown {
		eventType = domain.AuditEventWriteOutcomeUnknown
		auditOutcome = domain.AuditOutcomeUnknown
	} else if outcome.State == domain.RemoteDiagnosticOutcomeFailed {
		eventType = domain.AuditEventWriteVerificationFailed
		auditOutcome = domain.AuditOutcomeFailure
	}
	operation := string(envelope.Intent.Operation)
	policyVersion := envelope.Intent.PolicyVersion
	detail := string(outcome.State)
	if envelope.Intent.Operation == domain.ActionOperationDiagnosticPod {
		detail += "_cleanup_" + string(outcome.CleanupState)
	}
	count := int64(outcome.OutputBytes)
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
		Actor: domain.AuditActorSystem, Outcome: auditOutcome,
		Scope: &scope, Subject: &subject, Details: details,
		CorrelationID: string(envelope.RequestID), IntegrityHash: string(envelope.Digest),
		OccurredAt: occurredAt,
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrRemoteDiagnosticActionInvalid
	}
	return event, nil
}

// NewRemoteDiagnosticOutcomeAudits returns one atomic content-free result set.
// Exec and file reads use one aggregate event. A diagnostic Pod uses one event
// for create, wait, log, delete, and cleanup in that fixed order.
func NewRemoteDiagnosticOutcomeAudits(
	ids []domain.AuditEventID,
	envelope domain.ActionEnvelope,
	outcome domain.RemoteDiagnosticOutcome,
	occurredAt time.Time,
) ([]domain.AuditEvent, error) {
	want := 1
	if envelope.Intent.Operation == domain.ActionOperationDiagnosticPod {
		want = remoteDiagnosticPodAuditEventCount
	}
	if len(ids) != want {
		return nil, ErrRemoteDiagnosticActionInvalid
	}
	seen := make(map[domain.AuditEventID]struct{}, len(ids))
	for _, id := range ids {
		if !id.Valid() {
			return nil, ErrRemoteDiagnosticActionInvalid
		}
		if _, exists := seen[id]; exists {
			return nil, ErrRemoteDiagnosticActionInvalid
		}
		seen[id] = struct{}{}
	}
	if want == 1 {
		event, err := NewRemoteDiagnosticOutcomeAudit(ids[0], envelope, outcome, occurredAt)
		if err != nil {
			return nil, err
		}
		return []domain.AuditEvent{event}, nil
	}
	if envelope.Validate() != nil || outcome.Validate(envelope.Intent.Operation) != nil ||
		!validCoordinatorTime(occurredAt) || occurredAt.Before(envelope.RequestedAt) {
		return nil, ErrRemoteDiagnosticActionInvalid
	}
	phases := []struct {
		name  string
		state domain.DiagnosticPodPhaseState
		count *int64
	}{
		{name: "create", state: outcome.Lifecycle.Create},
		{name: "wait", state: outcome.Lifecycle.Wait},
		{name: "log", state: outcome.Lifecycle.Log, count: remoteDiagnosticAuditCount(outcome.OutputBytes)},
		{name: "delete", state: outcome.Lifecycle.Delete},
		{name: "cleanup", state: diagnosticCleanupAuditPhase(outcome.CleanupState)},
	}
	events := make([]domain.AuditEvent, len(phases))
	for index, phase := range phases {
		event, err := newRemoteDiagnosticPhaseAudit(ids[index], envelope, outcome, phase.name, phase.state, index+1, phase.count, occurredAt)
		if err != nil {
			return nil, err
		}
		events[index] = event
	}
	return events, nil
}

func remoteDiagnosticAuditCount(value int) *int64 {
	count := int64(value)
	return &count
}

func diagnosticCleanupAuditPhase(state domain.DiagnosticPodCleanupState) domain.DiagnosticPodPhaseState {
	switch state {
	case domain.DiagnosticPodCleanupVerified:
		return domain.DiagnosticPodPhaseCompleted
	case domain.DiagnosticPodCleanupUnknown:
		return domain.DiagnosticPodPhaseUnknown
	default:
		return domain.DiagnosticPodPhaseNotAttempted
	}
}

func newRemoteDiagnosticPhaseAudit(
	id domain.AuditEventID,
	envelope domain.ActionEnvelope,
	outcome domain.RemoteDiagnosticOutcome,
	phase string,
	state domain.DiagnosticPodPhaseState,
	sequence int,
	count *int64,
	occurredAt time.Time,
) (domain.AuditEvent, error) {
	eventType, auditOutcome := domain.AuditEventWriteVerified, domain.AuditOutcomeSuccess
	switch state {
	case domain.DiagnosticPodPhaseCompleted:
	case domain.DiagnosticPodPhaseFailed:
		eventType, auditOutcome = domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeFailure
	case domain.DiagnosticPodPhaseUnknown:
		eventType, auditOutcome = domain.AuditEventWriteOutcomeUnknown, domain.AuditOutcomeUnknown
	case domain.DiagnosticPodPhaseNotAttempted:
		eventType, auditOutcome = domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeDenied
	default:
		return domain.AuditEvent{}, ErrRemoteDiagnosticActionInvalid
	}
	operation, policyVersion := string(envelope.Intent.Operation), envelope.Intent.PolicyVersion
	detail := "diagnostic_" + phase + "_" + string(state)
	details := domain.AuditDetails{Operation: &operation, Sequence: &sequence, DetailCode: &detail, PolicyVersion: &policyVersion, Count: count}
	if state != domain.DiagnosticPodPhaseCompleted && outcome.ErrorClass.Valid() {
		class := outcome.ErrorClass
		details.ErrorClass = &class
	}
	sessionID, runID := envelope.SessionID, envelope.RunID
	scope, subject := envelope.Intent.Scope, envelope.Intent.Target.Resource
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, RunID: &runID, Type: eventType,
		Actor: domain.AuditActorSystem, Outcome: auditOutcome,
		Scope: &scope, Subject: &subject, Details: details,
		CorrelationID: string(envelope.RequestID), IntegrityHash: string(envelope.Digest),
		OccurredAt: occurredAt,
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrRemoteDiagnosticActionInvalid
	}
	return event, nil
}
