package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrRemoteDiagnosticGateUnavailable = errors.New("remote diagnostic action gate is unavailable")

type RemoteDiagnosticGateIdentifierSource interface {
	NewApprovalID() (domain.ApprovalID, error)
	NewAuditEventID() (domain.AuditEventID, error)
}

// RemoteDiagnosticResultAudits persists one complete post-attempt audit set
// atomically. Diagnostic Pods use five events so no lifecycle phase can be
// durable without the others.
type RemoteDiagnosticResultAudits interface {
	AppendWriteResults(context.Context, []domain.AuditEvent) error
}

// RemoteDiagnosticActionRevalidator performs the one fresh external target
// check owned by Application after a decision and before durable consumption.
// It returns no Kubernetes object or command output.
type RemoteDiagnosticActionRevalidator interface {
	RevalidateRemoteDiagnosticAction(context.Context, domain.RemoteDiagnosticActionPlan) error
}

// RemoteDiagnosticActionSupervisor is the shared Application-owned human and
// Reviewer route. It blocks the Tool caller until the immutable envelope is
// consumed or safely closed.
type RemoteDiagnosticActionSupervisor interface {
	AuthorizeRemoteDiagnosticAction(context.Context, PermissionPolicy, domain.RemoteDiagnosticActionPlan) (domain.ActionEnvelope, error)
}

// RemoteDiagnosticActionGate applies the generic durable approval lifecycle to
// the S04 operations. Automatic and matching Session-rule routes keep their
// direct durable path; human and Reviewer routes use the shared S05 supervisor.
type RemoteDiagnosticActionGate struct {
	service      ApprovalLifecycle
	persistence  ApprovalPersistence
	resultAudits RemoteDiagnosticResultAudits
	revalidator  RemoteDiagnosticActionRevalidator
	scope        ApprovalCurrentScope
	identifiers  RemoteDiagnosticGateIdentifierSource
	permissions  *PermissionManager
	supervisor   RemoteDiagnosticActionSupervisor
	policies     domain.RemoteDiagnosticsPolicyCatalog
	now          func() time.Time
	timeout      time.Duration
}

type RemoteDiagnosticActionGateConfig struct {
	Service            ApprovalLifecycle
	Persistence        ApprovalPersistence
	ResultAudits       RemoteDiagnosticResultAudits
	Revalidator        RemoteDiagnosticActionRevalidator
	Scope              ApprovalCurrentScope
	Identifiers        RemoteDiagnosticGateIdentifierSource
	Permissions        *PermissionManager
	Supervisor         RemoteDiagnosticActionSupervisor
	PolicyCatalog      domain.RemoteDiagnosticsPolicyCatalog
	Now                func() time.Time
	PersistenceTimeout time.Duration
}

func NewRemoteDiagnosticActionGate(config RemoteDiagnosticActionGateConfig) (*RemoteDiagnosticActionGate, error) {
	if config.Service == nil || config.Persistence == nil || config.ResultAudits == nil || config.Revalidator == nil || config.Scope == nil ||
		config.Identifiers == nil || config.Permissions == nil || config.PolicyCatalog.Validate() != nil || config.Now == nil || !validCoordinatorTime(config.Now()) ||
		config.PersistenceTimeout <= 0 || config.PersistenceTimeout > time.Minute {
		return nil, ErrRemoteDiagnosticGateUnavailable
	}
	return &RemoteDiagnosticActionGate{service: config.Service, persistence: config.Persistence, resultAudits: config.ResultAudits, revalidator: config.Revalidator, scope: config.Scope, identifiers: config.Identifiers, permissions: config.Permissions, supervisor: config.Supervisor, policies: config.PolicyCatalog, now: config.Now, timeout: config.PersistenceTimeout}, nil
}

func (gate *RemoteDiagnosticActionGate) AuthorizeAndConsume(ctx context.Context, plan domain.RemoteDiagnosticActionPlan) (domain.ActionEnvelope, error) {
	if gate == nil || ctx == nil || ctx.Err() != nil || plan.Validate() != nil {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassInvalidInput}
	}
	if !gate.policies.AdmitsAction(plan) {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPolicyDenied}
	}
	current, currentOK := gate.scope.CurrentScope()
	if !currentOK || current != plan.Scope {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassStaleScope}
	}
	policy, preliminary := gate.permissions.EvaluateCatalog(plan.SessionID, PermissionEvaluationInput{Operation: plan.Operation, Effect: plan.Effect, Risk: plan.Risk, CapabilityAdmitted: true, CapabilityEnabled: true})
	if policy.Validate() != nil || preliminary.Validate() != nil || preliminary.Disposition == domain.ReviewDispositionDeny || policy.Generation != plan.PolicyGeneration {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPolicyDenied}
	}
	if preliminary.Disposition == domain.ReviewDispositionHuman || preliminary.Disposition == domain.ReviewDispositionReviewer {
		if gate.supervisor == nil {
			return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPolicyDenied}
		}
		return gate.supervisor.AuthorizeRemoteDiagnosticAction(ctx, policy, plan)
	}
	intent, err := NewRemoteDiagnosticActionIntent(policy, plan)
	if err != nil {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPolicyDenied}
	}
	id, err := gate.identifiers.NewApprovalID()
	if err != nil || !id.Valid() {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassInternal}
	}
	request, err := gate.service.Request(ctx, approval.RequestCommand{ID: id, RunID: plan.RunID, SessionID: plan.SessionID, Intent: intent})
	if err != nil {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassInternal}
	}
	route := gate.permissions.Evaluate(request.ActionEnvelope(), true, true, gate.now())
	if route.Validate() != nil || route.Disposition != domain.ReviewDispositionAutomatic {
		_, _ = gate.service.Cancel(context.Background(), request.ID, domain.ApprovalReasonRunCancelled)
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPolicyDenied}
	}
	requestAudit, err := gate.approvalAudit(request)
	if err != nil || gate.persist(ctx, func(persistContext context.Context) error {
		return gate.persistence.CreateWithAudit(persistContext, request, requestAudit)
	}) != nil {
		_, _ = gate.service.Cancel(context.Background(), request.ID, domain.ApprovalReasonRunCancelled)
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
	}
	actor := domain.ApprovalActorPermissionPolicy
	if route.MatchedRuleID != "" {
		actor = domain.ApprovalActorSessionRule
	}
	updated, decision, err := gate.service.Decide(ctx, approval.DecisionCommand{RequestID: request.ID, Choice: domain.ApprovalDecisionApprove, ShownDigest: request.Digest, Nonce: request.Nonce, CurrentScope: current.Snapshot(), Actor: actor, Disposition: domain.ReviewDispositionAutomatic, RuleID: route.MatchedRuleID})
	if err != nil {
		reason, class := domain.ApprovalReasonPolicyChanged, domain.SafeErrorClassPolicyDenied
		if ctx.Err() != nil {
			reason, class = domain.ApprovalReasonContextCancelled, domain.SafeErrorClassCancelled
		}
		if gate.closePersistedRequest(ctx, request.State, request.ID, reason) != nil {
			return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
		}
		return domain.ActionEnvelope{}, remoteGateError{class: class}
	}
	decisionAudit, err := gate.approvalAudit(updated)
	if err != nil || gate.persist(ctx, func(persistContext context.Context) error {
		return gate.persistence.ResolveWithAudit(persistContext, request.State, updated, decision, decisionAudit)
	}) != nil {
		_ = gate.closePersistedRequest(ctx, request.State, request.ID, domain.ApprovalReasonPolicyChanged)
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
	}
	claim, err := gate.service.Claim(ctx, approval.ConsumeCommand{RequestID: request.ID, ShownDigest: request.Digest, Nonce: request.Nonce, CurrentScope: current.Snapshot()})
	if err != nil || claim.Validate() != nil {
		if gate.closePersistedRequest(ctx, updated.State, request.ID, domain.ApprovalReasonPolicyChanged) != nil {
			return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
		}
		class := domain.SafeErrorClassPolicyDenied
		if ctx.Err() != nil {
			class = domain.SafeErrorClassCancelled
		}
		return domain.ActionEnvelope{}, remoteGateError{class: class}
	}
	current, currentOK = gate.scope.CurrentScope()
	if !currentOK || current != plan.Scope || !plan.MatchesIntent(claim.Request.Intent) {
		if gate.closePersistedRequest(ctx, updated.State, request.ID, domain.ApprovalReasonScopeChanged) != nil {
			return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
		}
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassStaleScope}
	}
	if !gate.permissions.ActionCurrent(claim.Request.ActionEnvelope()) {
		if gate.closePersistedRequest(ctx, updated.State, request.ID, domain.ApprovalReasonPolicyChanged) != nil {
			return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
		}
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPolicyDenied}
	}
	if err := gate.revalidator.RevalidateRemoteDiagnosticAction(ctx, plan); err != nil {
		reason, class := remoteDiagnosticRevalidationFailure(ctx, err)
		if gate.closePersistedRequest(ctx, updated.State, request.ID, reason) != nil {
			return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
		}
		return domain.ActionEnvelope{}, remoteGateError{class: class}
	}
	current, currentOK = gate.scope.CurrentScope()
	if !currentOK || current != plan.Scope || !plan.MatchesIntent(claim.Request.Intent) || !gate.policies.AdmitsAction(plan) || !gate.permissions.ActionCurrent(claim.Request.ActionEnvelope()) {
		reason := domain.ApprovalReasonPolicyChanged
		if !currentOK || current != plan.Scope {
			reason = domain.ApprovalReasonScopeChanged
		}
		if gate.closePersistedRequest(ctx, updated.State, request.ID, reason) != nil {
			return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
		}
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassStaleScope}
	}
	consumed, err := gate.service.CommitConsume(ctx, claim)
	if err != nil || consumed.Validate() != nil || consumed.State != domain.ApprovalStateConsumed {
		if consumed.Validate() == nil && consumed.State == domain.ApprovalStateConsumed {
			return consumed.ActionEnvelope(), remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
		}
		_ = gate.closePersistedRequest(ctx, updated.State, request.ID, domain.ApprovalReasonPolicyChanged)
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
	}
	envelope := consumed.ActionEnvelope()
	if !plan.MatchesIntent(envelope.Intent) {
		return domain.ActionEnvelope{}, remoteGateError{class: domain.SafeErrorClassStaleScope}
	}
	currentTime := gate.now()
	current, currentOK = gate.scope.CurrentScope()
	if !validCoordinatorTime(currentTime) {
		return envelope, remoteGateError{class: domain.SafeErrorClassInternal}
	}
	if !currentTime.Before(envelope.ExpiresAt) {
		return envelope, remoteGateError{class: domain.SafeErrorClassTimeout}
	}
	if !currentOK || current != plan.Scope || !gate.permissions.ActionCurrent(envelope) {
		return envelope, remoteGateError{class: domain.SafeErrorClassStaleScope}
	}
	return envelope, nil
}

func remoteDiagnosticRevalidationFailure(ctx context.Context, err error) (domain.ApprovalStateReason, domain.SafeErrorClass) {
	if ctx != nil && ctx.Err() != nil {
		class := domain.SafeErrorClassCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			class = domain.SafeErrorClassTimeout
		}
		return domain.ApprovalReasonContextCancelled, class
	}
	class := domain.SafeErrorClassConflict
	var classified interface{ Class() domain.SafeErrorClass }
	if errors.As(err, &classified) && classified.Class().Valid() {
		class = classified.Class()
	}
	switch class {
	case domain.SafeErrorClassStaleScope, domain.SafeErrorClassPolicyDenied:
		return domain.ApprovalReasonPolicyChanged, class
	default:
		return domain.ApprovalReasonTargetChanged, class
	}
}

func (gate *RemoteDiagnosticActionGate) RecordOutcome(ctx context.Context, envelope domain.ActionEnvelope, outcome domain.RemoteDiagnosticOutcome) error {
	if gate == nil || ctx == nil || envelope.Validate() != nil || outcome.Validate(envelope.Intent.Operation) != nil ||
		outcome.OutputBytes > envelope.Intent.Limits.MaximumBytes || outcome.OutputLines > envelope.Intent.Limits.MaximumLines ||
		outcome.State == domain.RemoteDiagnosticOutcomeSucceeded && outcome.OutputBytes > envelope.Intent.Limits.MaximumOutput {
		return remoteGateError{class: domain.SafeErrorClassInvalidInput}
	}
	eventCount := 1
	if envelope.Intent.Operation == domain.ActionOperationDiagnosticPod {
		eventCount = remoteDiagnosticPodAuditEventCount
	}
	ids := make([]domain.AuditEventID, eventCount)
	for index := range ids {
		id, err := gate.identifiers.NewAuditEventID()
		if err != nil || !id.Valid() {
			return remoteGateError{class: domain.SafeErrorClassInternal}
		}
		ids[index] = id
	}
	events, err := NewRemoteDiagnosticOutcomeAudits(ids, envelope, outcome, gate.now())
	if err != nil {
		return remoteGateError{class: domain.SafeErrorClassInternal}
	}
	if err := gate.persistOutcome(ctx, func(persistContext context.Context) error {
		return gate.resultAudits.AppendWriteResults(persistContext, events)
	}); err != nil {
		return remoteGateError{class: domain.SafeErrorClassPersistenceUnavailable}
	}
	return nil
}

func (gate *RemoteDiagnosticActionGate) approvalAudit(request domain.ApprovalRequest) (domain.AuditEvent, error) {
	stored, err := approval.NewStoredRequest(request)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	id, err := gate.identifiers.NewAuditEventID()
	if err != nil || !id.Valid() {
		return domain.AuditEvent{}, ErrRemoteDiagnosticGateUnavailable
	}
	eventType, actor, outcome, detail := approvalAuditProjection(stored)
	operation, policyVersion := string(stored.Intent.Operation), stored.Intent.PolicyVersion
	sessionID, runID, scope, subject := stored.SessionID, stored.RunID, stored.Intent.Scope, stored.Intent.Target.Resource
	event := domain.AuditEvent{ID: id, SessionID: &sessionID, RunID: &runID, Type: eventType, Actor: actor, Outcome: outcome, Scope: &scope, Subject: &subject, Details: domain.AuditDetails{Operation: &operation, DetailCode: &detail, PolicyVersion: &policyVersion}, CorrelationID: string(stored.ID), IntegrityHash: string(stored.Digest), OccurredAt: stored.StateChangedAt}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrRemoteDiagnosticGateUnavailable
	}
	return event, nil
}

// closePersistedRequest makes every pre-consumption invalidation durable. It
// uses an independent bounded context because cancellation must remove, rather
// than preserve, pending execution authority.
func (gate *RemoteDiagnosticActionGate) closePersistedRequest(ctx context.Context, expected domain.ApprovalState, requestID domain.ApprovalID, reason domain.ApprovalStateReason) error {
	closeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), gate.timeout)
	defer cancel()
	var updated domain.ApprovalRequest
	var transitionErr error
	if reason.ValidCancellation() {
		updated, transitionErr = gate.service.Cancel(closeContext, requestID, reason)
	} else {
		updated, transitionErr = gate.service.Invalidate(closeContext, requestID, reason)
	}
	if updated.Validate() != nil || !updated.State.Terminated() || updated.State == domain.ApprovalStateConsumed {
		return errors.Join(ErrRemoteDiagnosticGateUnavailable, transitionErr)
	}
	audit, err := gate.approvalAudit(updated)
	if err != nil {
		return errors.Join(ErrRemoteDiagnosticGateUnavailable, transitionErr, err)
	}
	if err := gate.persistence.CloseWithAudit(closeContext, expected, updated, audit); err != nil {
		return errors.Join(ErrRemoteDiagnosticGateUnavailable, transitionErr, err)
	}
	return nil
}

func (gate *RemoteDiagnosticActionGate) persist(ctx context.Context, operation func(context.Context) error) error {
	persistContext, cancel := context.WithTimeout(ctx, gate.timeout)
	defer cancel()
	return operation(persistContext)
}

// persistOutcome gives an already-attempted external operation one independent
// bounded audit window even when the operation context was cancelled.
func (gate *RemoteDiagnosticActionGate) persistOutcome(ctx context.Context, operation func(context.Context) error) error {
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), gate.timeout)
	defer cancel()
	return operation(persistContext)
}

type remoteGateError struct{ class domain.SafeErrorClass }

func (err remoteGateError) Error() string { return "The remote diagnostic action was not authorized." }
func (err remoteGateError) Class() domain.SafeErrorClass {
	if err.class.Valid() {
		return err.class
	}
	return domain.SafeErrorClassInternal
}
