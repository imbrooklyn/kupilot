package application

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrLocalActionInvalid = errors.New("restricted local action is invalid")

// LocalNamespaceResolver performs the one exact Kubernetes Namespace read
// used to bind a local command to the frozen cluster scope. It returns no
// client-go value or credential.
type LocalNamespaceResolver interface {
	ResolveLocalExecutionNamespace(context.Context, domain.ClusterScope) (domain.ResourceRef, error)
}

// LocalExecutableInspector proves the current executable and working-directory
// identities without launching a process.
type LocalExecutableInspector interface {
	Inspect(context.Context, string, string) (domain.LocalExecutionObservation, error)
}

// LocalProcessExecutor is the sole Application-owned local-action process
// port. Its executor implementation owns os/exec for this capability; the
// kubeconfig exec-credential adapter remains a separate Kubernetes exception.
type LocalProcessExecutor interface {
	LocalExecutableInspector
	Execute(context.Context, domain.ActionEnvelope) (domain.LocalCommandResult, error)
}

// LocalActionProposalPreparer exposes only code-owned policy metadata before
// any Kubernetes or filesystem read, then prepares the exact retained plan.
type LocalActionProposalPreparer interface {
	CommandCatalog() domain.LocalCommandPolicyCatalog
	ShellCatalog() domain.LocalShellPolicyCatalog
	CommandPolicy(string) (domain.LocalCommandPolicy, bool)
	ShellPolicy(string) (domain.LocalShellPolicy, bool)
	PrepareCommand(context.Context, domain.AgentRunID, domain.SessionID, domain.ClusterScope, domain.PolicyGeneration, string, string) (domain.LocalCommandActionPlan, error)
	PrepareShell(context.Context, domain.AgentRunID, domain.SessionID, domain.ClusterScope, domain.PolicyGeneration, string, string) (domain.LocalShellActionPlan, error)
}

// LocalActionPreparer owns immutable default-off policy catalogs and combines
// exact Namespace and local filesystem identities into one project plan.
type LocalActionPreparer struct {
	commands  domain.LocalCommandPolicyCatalog
	shells    domain.LocalShellPolicyCatalog
	namespace LocalNamespaceResolver
	inspector LocalExecutableInspector
}

func NewLocalActionPreparer(
	commands domain.LocalCommandPolicyCatalog,
	shells domain.LocalShellPolicyCatalog,
	namespace LocalNamespaceResolver,
	inspector LocalExecutableInspector,
) (*LocalActionPreparer, error) {
	if commands.Validate() != nil || shells.Validate() != nil || namespace == nil || inspector == nil {
		return nil, ErrLocalActionInvalid
	}
	return &LocalActionPreparer{commands: commands, shells: shells, namespace: namespace, inspector: inspector}, nil
}

func (preparer *LocalActionPreparer) CommandCatalog() domain.LocalCommandPolicyCatalog {
	if preparer == nil {
		return domain.DisabledLocalCommandPolicyCatalog()
	}
	catalog, _ := domain.NewLocalCommandPolicyCatalog(preparer.commands.Policies())
	return catalog
}

func (preparer *LocalActionPreparer) ShellCatalog() domain.LocalShellPolicyCatalog {
	if preparer == nil {
		return domain.DisabledLocalShellPolicyCatalog()
	}
	catalog, _ := domain.NewLocalShellPolicyCatalog(preparer.shells.Policies())
	return catalog
}

func (preparer *LocalActionPreparer) CommandPolicy(id string) (domain.LocalCommandPolicy, bool) {
	if preparer == nil {
		return domain.LocalCommandPolicy{}, false
	}
	return preparer.commands.Resolve(id)
}

func (preparer *LocalActionPreparer) ShellPolicy(id string) (domain.LocalShellPolicy, bool) {
	if preparer == nil {
		return domain.LocalShellPolicy{}, false
	}
	return preparer.shells.Resolve(id)
}

func (preparer *LocalActionPreparer) PrepareCommand(
	ctx context.Context,
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	scope domain.ClusterScope,
	policyGeneration domain.PolicyGeneration,
	policyID string,
	purpose string,
) (domain.LocalCommandActionPlan, error) {
	if preparer == nil || ctx == nil || !runID.Valid() || !sessionID.Valid() || scope.Validate() != nil ||
		!policyGeneration.Valid() || !domain.ValidActionReasonSummary(purpose) {
		return domain.LocalCommandActionPlan{}, ErrLocalActionInvalid
	}
	policy, found := preparer.commands.Resolve(policyID)
	if !found {
		return domain.LocalCommandActionPlan{}, ErrPermissionDenied
	}
	arguments, err := policy.RuntimeArguments(scope)
	if err != nil {
		return domain.LocalCommandActionPlan{}, ErrPermissionDenied
	}
	namespace, err := preparer.namespace.ResolveLocalExecutionNamespace(ctx, scope)
	if err != nil {
		return domain.LocalCommandActionPlan{}, err
	}
	observation, err := preparer.inspector.Inspect(ctx, policy.Executable, policy.WorkingDirectory)
	if err != nil {
		return domain.LocalCommandActionPlan{}, err
	}
	plan := domain.LocalCommandActionPlan{
		RunID: runID, SessionID: sessionID, Scope: scope, PolicyGeneration: policyGeneration,
		NamespaceTarget: namespace, Policy: policy, Arguments: arguments, Observation: observation,
		Limits:  domain.ActionLimits{Timeout: policy.Timeout, MaximumItems: 1, MaximumLines: policy.MaxLines, MaximumBytes: policy.MaxBytes, MaximumOutput: policy.MaxBytes},
		Purpose: purpose,
	}
	if plan.Validate() != nil {
		return domain.LocalCommandActionPlan{}, ErrLocalActionInvalid
	}
	return plan, nil
}

func (preparer *LocalActionPreparer) PrepareShell(
	ctx context.Context,
	runID domain.AgentRunID,
	sessionID domain.SessionID,
	scope domain.ClusterScope,
	policyGeneration domain.PolicyGeneration,
	policyID string,
	purpose string,
) (domain.LocalShellActionPlan, error) {
	if preparer == nil || ctx == nil || !runID.Valid() || !sessionID.Valid() || scope.Validate() != nil ||
		!policyGeneration.Valid() || !domain.ValidActionReasonSummary(purpose) {
		return domain.LocalShellActionPlan{}, ErrLocalActionInvalid
	}
	policy, found := preparer.shells.Resolve(policyID)
	if !found {
		return domain.LocalShellActionPlan{}, ErrPermissionDenied
	}
	namespace, err := preparer.namespace.ResolveLocalExecutionNamespace(ctx, scope)
	if err != nil {
		return domain.LocalShellActionPlan{}, err
	}
	observation, err := preparer.inspector.Inspect(ctx, policy.Executable, policy.WorkingDirectory)
	if err != nil {
		return domain.LocalShellActionPlan{}, err
	}
	plan := domain.LocalShellActionPlan{
		RunID: runID, SessionID: sessionID, Scope: scope, PolicyGeneration: policyGeneration,
		NamespaceTarget: namespace, Policy: policy, Observation: observation,
		Limits:  domain.ActionLimits{Timeout: policy.Timeout, MaximumItems: 1, MaximumLines: policy.MaxLines, MaximumBytes: policy.MaxBytes, MaximumOutput: policy.MaxBytes},
		Purpose: purpose,
	}
	if plan.Validate() != nil {
		return domain.LocalShellActionPlan{}, ErrLocalActionInvalid
	}
	return plan, nil
}

// LocalOutcomeAudit excludes argv, environment, cwd, output, command text,
// credentials, and raw errors. Their identity is already covered by the
// envelope digest.
func LocalOutcomeAudit(
	id domain.AuditEventID,
	envelope domain.ActionEnvelope,
	result domain.LocalCommandResult,
	occurredAt time.Time,
) (domain.AuditEvent, error) {
	if !id.Valid() || envelope.Validate() != nil || result.Validate(envelope.Intent.Limits) != nil ||
		!validCoordinatorTime(occurredAt) || occurredAt.Before(envelope.RequestedAt) {
		return domain.AuditEvent{}, ErrLocalActionInvalid
	}
	// A zero process exit establishes only the local process result. It never
	// proves that a kubectl, Helm, Argo CD, or diagnostic destination accepted
	// or completed a remote state change.
	eventType, outcome := domain.AuditEventWriteAttempted, domain.AuditOutcomeSuccess
	if result.State == domain.LocalProcessUnknown {
		eventType, outcome = domain.AuditEventWriteOutcomeUnknown, domain.AuditOutcomeUnknown
	} else if result.State != domain.LocalProcessExited || result.ExitCode != 0 {
		eventType, outcome = domain.AuditEventWriteVerificationFailed, domain.AuditOutcomeFailure
	}
	operation, detail, policy := string(envelope.Intent.Operation), string(result.State), envelope.Intent.PolicyVersion
	count := int64(result.ByteCount)
	details := domain.AuditDetails{Operation: &operation, DetailCode: &detail, PolicyVersion: &policy, Count: &count}
	if result.ErrorClass.Valid() {
		class := result.ErrorClass
		details.ErrorClass = &class
	}
	sessionID, runID := envelope.SessionID, envelope.RunID
	scope, subject := envelope.Intent.Scope, envelope.Intent.Target.Resource
	event := domain.AuditEvent{
		ID: id, SessionID: &sessionID, RunID: &runID, Type: eventType, Actor: domain.AuditActorSystem,
		Outcome: outcome, Scope: &scope, Subject: &subject, Details: details,
		CorrelationID: string(envelope.RequestID), IntegrityHash: string(envelope.Digest), OccurredAt: occurredAt,
	}
	if event.Validate() != nil {
		return domain.AuditEvent{}, ErrLocalActionInvalid
	}
	return event, nil
}
