package application

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// ErrInvalidRestartRollout reports a malformed or cross-request rollout value.
var ErrInvalidRestartRollout = errors.New("restart rollout data is invalid")

// MaxRestartRolloutObservations is the non-expandable Application acceptance
// ceiling for one restart verification.
const MaxRestartRolloutObservations int64 = 45

// RestartRolloutState is a closed verification state. Patch acceptance is
// tracked separately by the approval execution result.
type RestartRolloutState string

const (
	RestartRolloutProgress  RestartRolloutState = "rollout_progress"
	RestartRolloutSucceeded RestartRolloutState = "rollout_succeeded"
	RestartRolloutTimedOut  RestartRolloutState = "rollout_timed_out"
	RestartRolloutFailed    RestartRolloutState = "rollout_failed"
)

func (state RestartRolloutState) validObservationState() bool {
	return state == RestartRolloutProgress || state == RestartRolloutSucceeded || state == RestartRolloutFailed
}

// RestartRolloutFailureCode is code-defined and never carries a Kubernetes
// condition message or another external value.
type RestartRolloutFailureCode string

const (
	RestartRolloutFailureProgressDeadline RestartRolloutFailureCode = "progress_deadline_exceeded"
	RestartRolloutFailureReplica          RestartRolloutFailureCode = "replica_failure"
	RestartRolloutFailureTargetReplaced   RestartRolloutFailureCode = "target_replaced"
	RestartRolloutFailureTargetChanged    RestartRolloutFailureCode = "target_changed"
)

func (code RestartRolloutFailureCode) valid() bool {
	switch code {
	case RestartRolloutFailureProgressDeadline,
		RestartRolloutFailureReplica,
		RestartRolloutFailureTargetReplaced,
		RestartRolloutFailureTargetChanged:
		return true
	default:
		return false
	}
}

// RestartRolloutRequest is the exact post-PATCH target returned by the fixed
// executor. Timing policy is deliberately absent and cannot be model-selected.
type RestartRolloutRequest struct {
	Scope            domain.ScopeSnapshot
	DeploymentName   string
	DeploymentUID    string
	TargetGeneration int64
	TargetReplicas   int64
}

// Validate checks the project-owned target without Kubernetes vendor values.
func (request RestartRolloutRequest) Validate() error {
	reference := domain.ResourceRef{
		APIVersion: domain.RestartDeploymentTargetAPIVersion,
		Kind:       domain.RestartDeploymentTargetKind,
		Namespace:  request.Scope.Namespace,
		Name:       request.DeploymentName,
		UID:        request.DeploymentUID,
	}
	if request.Scope.Validate() != nil || !domain.ValidContextName(request.Scope.Context) ||
		!domain.ValidNamespaceName(request.Scope.Namespace) || request.Scope.Generation < 1 ||
		reference.Validate() != nil || request.TargetGeneration < 1 ||
		request.TargetReplicas < 0 || request.TargetReplicas > math.MaxInt32 {
		return ErrInvalidRestartRollout
	}
	return nil
}

// RestartRolloutObservation is one bounded Deployment status projection.
type RestartRolloutObservation struct {
	Scope                domain.ScopeSnapshot
	DeploymentName       string
	DeploymentUID        string
	DeploymentGeneration int64
	TargetGeneration     int64
	ObservedGeneration   int64
	UpdatedReplicas      int64
	AvailableReplicas    int64
	TargetReplicas       int64
	ObservationNumber    int64
	State                RestartRolloutState
	FailureCode          RestartRolloutFailureCode
	ObservedAt           time.Time
}

// Validate checks exact identity, non-negative counts, fixed failure codes,
// and state semantics without accepting Kubernetes condition text.
func (observation RestartRolloutObservation) Validate() error {
	request := RestartRolloutRequest{
		Scope: observation.Scope, DeploymentName: observation.DeploymentName,
		DeploymentUID: observation.DeploymentUID, TargetGeneration: observation.TargetGeneration,
		TargetReplicas: observation.TargetReplicas,
	}
	if request.Validate() != nil || observation.DeploymentGeneration < 1 ||
		observation.ObservedGeneration < 0 || observation.ObservedGeneration > observation.DeploymentGeneration ||
		observation.UpdatedReplicas < 0 ||
		observation.AvailableReplicas < 0 || observation.UpdatedReplicas > math.MaxInt32 ||
		observation.AvailableReplicas > math.MaxInt32 || observation.ObservationNumber < 1 ||
		observation.ObservationNumber > MaxRestartRolloutObservations || !observation.State.validObservationState() ||
		!validRolloutTime(observation.ObservedAt) {
		return ErrInvalidRestartRollout
	}
	if observation.State == RestartRolloutFailed {
		if !observation.FailureCode.valid() || !observation.validFailureShape() {
			return ErrInvalidRestartRollout
		}
	} else if observation.FailureCode != "" {
		return ErrInvalidRestartRollout
	}
	if observation.State == RestartRolloutProgress && observation.DeploymentGeneration != observation.TargetGeneration {
		return ErrInvalidRestartRollout
	}
	if observation.State == RestartRolloutSucceeded &&
		(observation.DeploymentGeneration != observation.TargetGeneration ||
			observation.ObservedGeneration < observation.TargetGeneration ||
			observation.UpdatedReplicas < observation.TargetReplicas ||
			observation.AvailableReplicas < observation.TargetReplicas) {
		return ErrInvalidRestartRollout
	}
	return nil
}

func (observation RestartRolloutObservation) validFailureShape() bool {
	switch observation.FailureCode {
	case RestartRolloutFailureProgressDeadline, RestartRolloutFailureReplica:
		return observation.DeploymentGeneration == observation.TargetGeneration &&
			observation.ObservedGeneration >= observation.TargetGeneration
	case RestartRolloutFailureTargetChanged:
		return observation.DeploymentGeneration > observation.TargetGeneration
	case RestartRolloutFailureTargetReplaced:
		return true
	default:
		return false
	}
}

// ValidateFor binds one adapter projection back to the exact post-PATCH
// request. The observer cannot substitute another scope, target, generation,
// or replica goal.
func (observation RestartRolloutObservation) ValidateFor(request RestartRolloutRequest) error {
	if request.Validate() != nil || observation.Validate() != nil ||
		observation.Scope != request.Scope || observation.DeploymentName != request.DeploymentName ||
		observation.DeploymentUID != request.DeploymentUID ||
		observation.TargetGeneration != request.TargetGeneration ||
		observation.TargetReplicas != request.TargetReplicas {
		return ErrInvalidRestartRollout
	}
	return nil
}

// RestartRolloutResult is one terminal bounded observation result. A timeout
// retains the last progress projection and never claims PATCH failure.
type RestartRolloutResult struct {
	State            RestartRolloutState
	Final            RestartRolloutObservation
	ObservationCount int64
	StartedAt        time.Time
	FinishedAt       time.Time
}

// Validate checks terminal-state and observation consistency.
func (result RestartRolloutResult) Validate() error {
	if !validRolloutTime(result.StartedAt) || !validRolloutTime(result.FinishedAt) ||
		result.FinishedAt.Before(result.StartedAt) {
		return ErrInvalidRestartRollout
	}
	if result.State == RestartRolloutTimedOut && result.ObservationCount == 0 {
		if result.Final != (RestartRolloutObservation{}) {
			return ErrInvalidRestartRollout
		}
		return nil
	}
	if result.Final.Validate() != nil || result.ObservationCount != result.Final.ObservationNumber ||
		result.Final.ObservedAt.Before(result.StartedAt) || result.Final.ObservedAt.After(result.FinishedAt) {
		return ErrInvalidRestartRollout
	}
	switch result.State {
	case RestartRolloutSucceeded, RestartRolloutFailed:
		if result.Final.State != result.State {
			return ErrInvalidRestartRollout
		}
	case RestartRolloutTimedOut:
		if result.Final.State != RestartRolloutProgress {
			return ErrInvalidRestartRollout
		}
	default:
		return ErrInvalidRestartRollout
	}
	return nil
}

// ValidateFor binds a terminal result to the exact immutable observation
// request in addition to checking its closed state shape.
func (result RestartRolloutResult) ValidateFor(request RestartRolloutRequest) error {
	if request.Validate() != nil || result.Validate() != nil ||
		result.ObservationCount > 0 && result.Final.ValidateFor(request) != nil {
		return ErrInvalidRestartRollout
	}
	return nil
}

// RestartRolloutProgressSink accepts only changed progress projections. An
// error stops observation so a required result-audit failure is visible.
type RestartRolloutProgressSink interface {
	PublishRestartRolloutProgress(context.Context, RestartRolloutObservation) error
}

// RestartRolloutObserver is the Application-owned narrow read capability
// implemented by the Kubernetes adapter.
type RestartRolloutObserver interface {
	ObserveRestartRollout(context.Context, RestartRolloutRequest, RestartRolloutProgressSink) (RestartRolloutResult, error)
}

func validRolloutTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0 &&
		value.Equal(time.UnixMilli(value.UnixMilli()).UTC())
}
