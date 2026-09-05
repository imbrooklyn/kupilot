package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrRemediationActionInvalid = errors.New("typed remediation action is invalid")

// RemediationProposalRequest contains only the model/user-selectable part of
// a typed operation. Kubernetes identity, current values, target sets,
// ceilings, risk, and verification are supplied by deterministic code.
type RemediationProposalRequest struct {
	RunID            domain.AgentRunID
	SessionID        domain.SessionID
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Operation        domain.ActionOperation
	Target           domain.ResourceRef
	ReplicaTarget    int64
	Revision         int64
	ReasonSummary    string
}

func (request RemediationProposalRequest) Validate() error {
	if !request.RunID.Valid() || !request.SessionID.Valid() || request.Scope.Validate() != nil ||
		!request.PolicyGeneration.Valid() || domain.ValidateLiveResourceRef(request.Target) != nil ||
		request.Target.UID != "" || request.Target.ResourceVersion != "" ||
		!domain.ValidActionReasonSummary(request.ReasonSummary) || !request.Scope.AllowsReference(request.Target) {
		return ErrRemediationActionInvalid
	}
	switch request.Operation {
	case domain.ActionOperationScaleWorkload:
		if request.Target.APIVersion != "apps/v1" || request.Target.Kind != "Deployment" && request.Target.Kind != "StatefulSet" ||
			request.Target.Namespace != request.Scope.Namespace || request.ReplicaTarget < 0 || request.ReplicaTarget > int64(^uint32(0)>>1) || request.Revision != 0 {
			return ErrRemediationActionInvalid
		}
	case domain.ActionOperationRollbackDeployment:
		if request.Target.APIVersion != "apps/v1" || request.Target.Kind != "Deployment" || request.Target.Namespace != request.Scope.Namespace ||
			request.ReplicaTarget != 0 || request.Revision < 1 {
			return ErrRemediationActionInvalid
		}
	case domain.ActionOperationDeleteOwnedPod:
		if request.Target.APIVersion != "v1" || request.Target.Kind != "Pod" || request.Target.Namespace != request.Scope.Namespace ||
			request.ReplicaTarget != 0 || request.Revision != 0 {
			return ErrRemediationActionInvalid
		}
	case domain.ActionOperationCordonNode, domain.ActionOperationUncordonNode, domain.ActionOperationDrainNode:
		if request.Target.APIVersion != "v1" || request.Target.Kind != "Node" || request.Target.Namespace != "" ||
			request.ReplicaTarget != 0 || request.Revision != 0 {
			return ErrRemediationActionInvalid
		}
	default:
		return ErrRemediationActionInvalid
	}
	return nil
}

// RemediationProposalPreparer owns the fresh read that constructs a complete,
// immutable target plan before an approval is requested.
type RemediationProposalPreparer interface {
	PrepareRemediationAction(context.Context, RemediationProposalRequest) (domain.RemediationActionPlan, error)
}

// RemediationActionExecutor is the exact typed Kubernetes execution port.
// Application calls Revalidate before durable approval consumption, Execute
// once after the pre-operation audit, and Verify separately.
type RemediationActionExecutor interface {
	RevalidateRemediationAction(context.Context, domain.RemediationActionPlan) error
	ExecuteRemediationAction(context.Context, domain.RemediationActionPlan) (domain.RemediationAttempt, error)
	VerifyRemediationAction(context.Context, domain.RemediationActionPlan) (domain.RemediationVerificationState, error)
}

// NewRemediationActionIntent stamps a fresh plan with the current permission
// profile. It creates no approval or execution authority on its own.
func NewRemediationActionIntent(policy PermissionPolicy, plan domain.RemediationActionPlan) (domain.ActionIntent, error) {
	if policy.Validate() != nil || plan.Validate() != nil || policy.Generation != plan.PolicyGeneration {
		return domain.ActionIntent{}, ErrRemediationActionInvalid
	}
	intent, err := plan.Intent(policy.Profile)
	if err != nil || domain.ValidateRemediationIntent(intent) != nil {
		return domain.ActionIntent{}, ErrRemediationActionInvalid
	}
	return intent, nil
}

// RemediationOutcomeAudit contains only bounded target and outcome metadata.
// Kubernetes object bodies and response bytes are deliberately absent.
func RemediationOutcomeAudit(
	id domain.AuditEventID,
	envelope domain.ActionEnvelope,
	result domain.RemediationResult,
	occurredAt time.Time,
) (domain.AuditEvent, error) {
	if !id.Valid() || envelope.Validate() != nil || domain.ValidateRemediationIntent(envelope.Intent) != nil ||
		result.Validate(envelope.Intent.Limits.MaximumItems) != nil || result.Operation != envelope.Intent.Operation ||
		!validCoordinatorTime(occurredAt) || occurredAt.Before(envelope.RequestedAt) {
		return domain.AuditEvent{}, ErrRemediationActionInvalid
	}
	eventType, outcome := domain.AuditEventWriteVerified, domain.AuditOutcomeSuccess
	if result.Attempt.State == domain.RemediationUnknown {
		eventType, outcome = domain.AuditEventWriteOutcomeUnknown, domain.AuditOutcomeUnknown
	} else if result.Attempt.State == domain.RemediationFailed || result.Attempt.State == domain.RemediationNotAttempted ||
		result.Verification != domain.RemediationVerified {
		eventType, outcome = domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeFailure
	}
	operation := string(envelope.Intent.Operation)
	detail := string(result.Attempt.State) + "_" + string(result.Verification)
	policyVersion := envelope.Intent.PolicyVersion
	count := int64(result.Attempt.AcceptedCount)
	details := domain.AuditDetails{Operation: &operation, DetailCode: &detail, PolicyVersion: &policyVersion, Count: &count}
	class := result.ErrorClass
	if class == "" && result.Attempt.ErrorClass.Valid() {
		class = result.Attempt.ErrorClass
	}
	if class.Valid() {
		details.ErrorClass = &class
	}
	sessionID, runID := envelope.SessionID, envelope.RunID
	scope, subject := envelope.Intent.Scope, envelope.Intent.Target.Resource
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, RunID: &runID, Type: eventType,
		Actor: domain.AuditActorSystem, Outcome: outcome, Scope: &scope, Subject: &subject,
		Details: details, CorrelationID: string(envelope.RequestID), IntegrityHash: string(envelope.Digest),
		OccurredAt: occurredAt,
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrRemediationActionInvalid
	}
	return event, nil
}
