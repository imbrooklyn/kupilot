package tools

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrInvalidObservationActionDependency = errors.New("observation action dependency is invalid")

// ObservationActionGate is the Tool-owned Application port. Prepare is a
// local zero-I/O denial gate; AuthorizeAndConsume returns only one durably
// consumed envelope; RecordOutcome accepts no source content.
type ObservationActionGate interface {
	PrepareObservationAction(context.Context, domain.ObservationActionPreflight) error
	AuthorizeObservationAction(context.Context, domain.ObservationActionPlan) (domain.ActionEnvelope, error)
	RecordObservationOutcome(context.Context, domain.ActionEnvelope, domain.ObservationActionOutcome) error
}

// ObservationTargetRequest is the sole safe Kubernetes metadata read used to
// bind an observation to one live Pod before approval. It cannot request Pod
// content, logs, exec, a selector, or another resource kind.
type ObservationTargetRequest struct {
	Scope              domain.ClusterScope
	PolicyGeneration   domain.PolicyGeneration
	Operation          domain.ActionOperation
	Namespace          string
	PodName            string
	RequestedContainer string
	AllContainers      bool
	IncludeInit        bool
	IncludeEphemeral   bool
}

func (request ObservationTargetRequest) Validate() error {
	reference := domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: request.Namespace, Name: request.PodName}
	if request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() || domain.ValidateLiveResourceRef(reference) != nil ||
		!request.Scope.AllowsReference(reference) || request.RequestedContainer != "" && !domain.ValidResourceName(request.RequestedContainer) {
		return ErrInvalidObservationActionDependency
	}
	switch request.Operation {
	case domain.ActionOperationLogsCurrent, domain.ActionOperationLogsPrevious,
		domain.ActionOperationLogsAllContainers, domain.ActionOperationLogSearch:
		if request.AllContainers && request.RequestedContainer != "" ||
			!request.AllContainers && (request.IncludeInit || request.IncludeEphemeral) {
			return ErrInvalidObservationActionDependency
		}
	case domain.ActionOperationPrometheusQuery, domain.ActionOperationLokiQuery:
		if request.RequestedContainer != "" || request.AllContainers || request.IncludeInit || request.IncludeEphemeral {
			return ErrInvalidObservationActionDependency
		}
	default:
		return ErrInvalidObservationActionDependency
	}
	return nil
}

// ResolvedObservationTarget contains only a live identity and the exact
// default/explicit single-container selection. All-container selection remains
// bound by the Pod resourceVersion plus the envelope's include/limit fields.
type ResolvedObservationTarget struct {
	Reference domain.ResourceRef
	Container string
}

func (target ResolvedObservationTarget) Validate(request ObservationTargetRequest) error {
	if request.Validate() != nil || target.Reference.Validate() != nil || target.Reference.UID == "" || target.Reference.ResourceVersion == "" ||
		target.Reference.APIVersion != "v1" || target.Reference.Kind != "Pod" || target.Reference.Namespace != request.Namespace ||
		target.Reference.Name != request.PodName {
		return ErrInvalidObservationActionDependency
	}
	if request.Operation == domain.ActionOperationPrometheusQuery || request.Operation == domain.ActionOperationLokiQuery || request.AllContainers {
		if target.Container != "" {
			return ErrInvalidObservationActionDependency
		}
		return nil
	}
	if !domain.ValidResourceName(target.Container) || request.RequestedContainer != "" && target.Container != request.RequestedContainer {
		return ErrInvalidObservationActionDependency
	}
	return nil
}

type ObservationTargetResolver interface {
	ResolveObservationTarget(context.Context, ObservationTargetRequest) (ResolvedObservationTarget, error)
}

func observationCallCurrent(
	ctx context.Context,
	call BoundToolCall,
	scope ScopeGuard,
	policy PolicyGenerationGuard,
) bool {
	return ctx != nil && ctx.Err() == nil && scope.Current(ctx, call.Scope()) &&
		policy.CurrentPolicyGeneration(ctx, call.PolicyGeneration())
}

func observationCurrentFailure(ctx context.Context) domain.SafeErrorClass {
	if ctx != nil && ctx.Err() != nil {
		return classifyFailure(ctx, ctx.Err())
	}
	return domain.SafeErrorClassStaleScope
}

func observationEnvelopeCurrent(
	ctx context.Context,
	call BoundToolCall,
	envelope domain.ActionEnvelope,
	now func() time.Time,
	scope ScopeGuard,
	policy PolicyGenerationGuard,
) domain.SafeErrorClass {
	if envelope.Validate() != nil || envelope.Intent.ValidateObservationAction() != nil ||
		envelope.RunID != call.RunID() || envelope.SessionID != call.SessionID() ||
		envelope.Intent.Scope != call.Scope().Snapshot() || envelope.Intent.PolicyGeneration != call.PolicyGeneration() {
		return domain.SafeErrorClassPolicyDenied
	}
	if !observationCallCurrent(ctx, call, scope, policy) {
		return observationCurrentFailure(ctx)
	}
	current := now()
	if !validRequiredUTCTime(current) {
		return domain.SafeErrorClassInternal
	}
	if !current.Before(envelope.ExpiresAt) {
		return domain.SafeErrorClassTimeout
	}
	return ""
}

func failAfterObservationAuthority(
	ctx context.Context,
	call BoundToolCall,
	observed time.Time,
	envelope domain.ActionEnvelope,
	attempted bool,
	items int,
	lines int,
	bytes int,
	truncated bool,
	class domain.SafeErrorClass,
	actions ObservationActionGate,
) ToolResult {
	if !class.Valid() {
		class = domain.SafeErrorClassInternal
	}
	if envelope.Validate() == nil && envelope.Intent.ValidateObservationAction() == nil {
		items = min(max(items, 0), envelope.Intent.Limits.MaximumItems)
		lines = min(max(lines, 0), envelope.Intent.Limits.MaximumLines)
		bytes = min(max(bytes, 0), envelope.Intent.Limits.MaximumBytes)
		state := domain.ObservationActionNotAttempted
		if attempted {
			state = domain.ObservationActionFailed
		}
		outcome := domain.ObservationActionOutcome{
			State: state, ErrorClass: class, ResultItems: items,
			ResultLines: lines, ResultBytes: bytes, Truncated: truncated,
		}
		if actions.RecordObservationOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
	}
	return failedResult(call, observed, class)
}

func recordObservationSuccess(
	ctx context.Context,
	envelope domain.ActionEnvelope,
	items int,
	lines int,
	bytes int,
	truncated bool,
	actions ObservationActionGate,
) error {
	items = min(max(items, 0), envelope.Intent.Limits.MaximumItems)
	lines = min(max(lines, 0), envelope.Intent.Limits.MaximumLines)
	bytes = min(max(bytes, 0), envelope.Intent.Limits.MaximumBytes)
	return actions.RecordObservationOutcome(ctx, envelope, domain.ObservationActionOutcome{
		State: domain.ObservationActionSucceeded, ResultItems: items,
		ResultLines: lines, ResultBytes: bytes, Truncated: truncated,
	})
}
