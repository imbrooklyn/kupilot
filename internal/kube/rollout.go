package kube

import (
	"context"
	"errors"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// DefaultRolloutTimeout is the non-expandable post-PATCH observation window.
	DefaultRolloutTimeout = 90 * time.Second
	// DefaultRolloutPollInterval is the fastest admitted polling cadence.
	DefaultRolloutPollInterval = 2 * time.Second
	// MaxRolloutObservations bounds GET calls for the default policy.
	MaxRolloutObservations = int(application.MaxRestartRolloutObservations)
)

// RolloutObserverConfig may only shorten the window or reduce polling
// frequency. Zero values select the fixed defaults.
type RolloutObserverConfig struct {
	Timeout      time.Duration
	PollInterval time.Duration
}

// DeploymentRolloutObserver performs bounded exact Deployment GETs. It owns no
// goroutine and never exposes a Watch, informer, client-go value, or write.
type DeploymentRolloutObserver struct {
	binding      *ToolScopeBinding
	timeout      time.Duration
	pollInterval time.Duration
	clock        rolloutClock
}

var _ application.RestartRolloutObserver = (*DeploymentRolloutObserver)(nil)

type rolloutClock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}

type realRolloutClock struct{}

func (realRolloutClock) Now() time.Time {
	return time.Now().UTC().Truncate(time.Millisecond)
}

func (realRolloutClock) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// NewDeploymentRolloutObserver constructs the fixed bounded read adapter.
func NewDeploymentRolloutObserver(
	binding *ToolScopeBinding,
	config RolloutObserverConfig,
) (*DeploymentRolloutObserver, error) {
	return newDeploymentRolloutObserver(binding, config, realRolloutClock{})
}

func newDeploymentRolloutObserver(
	binding *ToolScopeBinding,
	config RolloutObserverConfig,
	clock rolloutClock,
) (*DeploymentRolloutObserver, error) {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = DefaultRolloutTimeout
	}
	pollInterval := config.PollInterval
	if pollInterval == 0 {
		pollInterval = DefaultRolloutPollInterval
	}
	if binding == nil || binding.gateway == nil || clock == nil ||
		timeout < DefaultRolloutPollInterval || timeout > DefaultRolloutTimeout ||
		pollInterval < DefaultRolloutPollInterval || pollInterval > timeout ||
		!validRolloutClockTime(clock.Now()) {
		return nil, newKubeSafeError(
			ClassConfigurationInvalid,
			"kubernetes_rollout_policy_invalid",
			"create_deployment_rollout_observer",
			"The Deployment rollout observation policy is invalid.",
		)
	}
	return &DeploymentRolloutObserver{
		binding: binding, timeout: timeout, pollInterval: pollInterval, clock: clock,
	}, nil
}

// ObserveRestartRollout observes one exact post-PATCH generation. Progress is
// emitted only when its bounded numeric projection changes.
func (observer *DeploymentRolloutObserver) ObserveRestartRollout(
	ctx context.Context,
	request application.RestartRolloutRequest,
	sink application.RestartRolloutProgressSink,
) (application.RestartRolloutResult, error) {
	if ctx == nil || observer == nil || observer.binding == nil || observer.clock == nil ||
		request.Validate() != nil || sink == nil {
		return application.RestartRolloutResult{}, rolloutInputError()
	}
	if err := ctx.Err(); err != nil {
		return application.RestartRolloutResult{}, classifyContextError(err, "observe_restart_deployment_rollout")
	}
	startedAt := observer.clock.Now().UTC().Truncate(time.Millisecond)
	if !validRolloutClockTime(startedAt) {
		return application.RestartRolloutResult{}, rolloutClockError()
	}
	deadline := startedAt.Add(observer.timeout)
	var (
		last         application.RestartRolloutObservation
		lastProgress application.RestartRolloutObservation
		haveLast     bool
		haveProgress bool
	)
	for observationNumber := int64(1); observationNumber <= int64(MaxRolloutObservations); observationNumber++ {
		now := observer.clock.Now().UTC().Truncate(time.Millisecond)
		if !validRolloutClockTime(now) || now.Before(startedAt) {
			return application.RestartRolloutResult{}, rolloutClockError()
		}
		if !now.Before(deadline) {
			return rolloutTimeoutResult(startedAt, now, last, request)
		}
		observation, err := observer.readObservation(ctx, request, observationNumber, deadline.Sub(now))
		if err != nil {
			finishedReadAt := observer.clock.Now().UTC().Truncate(time.Millisecond)
			if errors.Is(err, context.DeadlineExceeded) && validRolloutClockTime(finishedReadAt) &&
				!finishedReadAt.Before(deadline) {
				return rolloutTimeoutResult(startedAt, finishedReadAt, last, request)
			}
			return application.RestartRolloutResult{}, err
		}
		finishedReadAt := observer.clock.Now().UTC().Truncate(time.Millisecond)
		if !validRolloutClockTime(finishedReadAt) || finishedReadAt.Before(startedAt) {
			return application.RestartRolloutResult{}, rolloutClockError()
		}
		if !finishedReadAt.Before(deadline) {
			return rolloutTimeoutResult(startedAt, finishedReadAt, last, request)
		}
		last, haveLast = observation, true
		if observation.State == application.RestartRolloutSucceeded || observation.State == application.RestartRolloutFailed {
			result := application.RestartRolloutResult{
				State: observation.State, Final: observation, ObservationCount: observationNumber,
				StartedAt: startedAt, FinishedAt: finishedReadAt,
			}
			if result.ValidateFor(request) != nil {
				return application.RestartRolloutResult{}, rolloutProjectionError()
			}
			return result, nil
		}
		if !haveProgress || rolloutProgressChanged(lastProgress, observation) {
			if err := sink.PublishRestartRolloutProgress(ctx, observation); err != nil {
				return application.RestartRolloutResult{}, err
			}
			lastProgress, haveProgress = observation, true
		}
		if err := ctx.Err(); err != nil {
			return application.RestartRolloutResult{}, classifyContextError(err, "observe_restart_deployment_rollout")
		}
		now = observer.clock.Now().UTC().Truncate(time.Millisecond)
		if !validRolloutClockTime(now) || now.Before(startedAt) {
			return application.RestartRolloutResult{}, rolloutClockError()
		}
		remaining := deadline.Sub(now)
		if remaining <= 0 {
			return rolloutTimeoutResult(startedAt, now, last, request)
		}
		wait := min(observer.pollInterval, remaining)
		if err := observer.clock.Wait(ctx, wait); err != nil {
			finishedWaitAt := observer.clock.Now().UTC().Truncate(time.Millisecond)
			if errors.Is(err, context.DeadlineExceeded) && validRolloutClockTime(finishedWaitAt) &&
				!finishedWaitAt.Before(deadline) {
				return rolloutTimeoutResult(startedAt, finishedWaitAt, last, request)
			}
			if ctx.Err() != nil {
				return application.RestartRolloutResult{}, classifyContextError(ctx.Err(), "observe_restart_deployment_rollout")
			}
			return application.RestartRolloutResult{}, rolloutClockError()
		}
	}
	if !haveLast {
		return application.RestartRolloutResult{}, rolloutClockError()
	}
	finishedAt := observer.clock.Now().UTC().Truncate(time.Millisecond)
	return rolloutTimeoutResult(startedAt, finishedAt, last, request)
}

func (observer *DeploymentRolloutObserver) readObservation(
	ctx context.Context,
	request application.RestartRolloutRequest,
	observationNumber int64,
	remaining time.Duration,
) (application.RestartRolloutObservation, error) {
	bundle, client, err := observer.capture(request.Scope)
	if err != nil {
		return application.RestartRolloutObservation{}, err
	}
	requestLimit := min(DefaultRequestTimeout, remaining)
	readContext, cancel := context.WithTimeout(ctx, requestLimit)
	object, rawErr := bundle.typed.AppsV1().Deployments(request.Scope.Namespace).Get(
		readContext,
		request.DeploymentName,
		metav1.GetOptions{},
	)
	cancel()
	if ctx.Err() != nil {
		return application.RestartRolloutObservation{}, classifyContextError(ctx.Err(), "observe_restart_deployment_rollout")
	}
	if rawErr != nil {
		return application.RestartRolloutObservation{}, classifyKubernetesError("observe_restart_deployment_rollout", rawErr)
	}
	if !observer.bindingCurrent(client, request.Scope) {
		return application.RestartRolloutObservation{}, rolloutStaleScopeError()
	}
	observedAt := observer.clock.Now().UTC().Truncate(time.Millisecond)
	observation, err := projectRolloutObservation(request, object, observationNumber, observedAt)
	if err != nil {
		return application.RestartRolloutObservation{}, err
	}
	return observation, nil
}

func (observer *DeploymentRolloutObserver) capture(
	scope domain.ScopeSnapshot,
) (*ClientBundle, application.ScopeClient, error) {
	if observer == nil || observer.binding == nil || observer.binding.gateway == nil ||
		scope.Validate() != nil || !domain.ValidContextName(scope.Context) ||
		!domain.ValidNamespaceName(scope.Namespace) || scope.Generation < 1 {
		return nil, nil, rolloutInputError()
	}
	observer.binding.mu.Lock()
	defer observer.binding.mu.Unlock()
	client := observer.binding.client
	if client == nil || observer.binding.generation != scope.Generation || client.Context().Name != scope.Context {
		return nil, nil, rolloutStaleScopeError()
	}
	bundle, err := observer.binding.gateway.clientBundle(client, "observe_restart_deployment_rollout")
	if err != nil {
		return nil, nil, err
	}
	return bundle, client, nil
}

func (observer *DeploymentRolloutObserver) bindingCurrent(
	client application.ScopeClient,
	scope domain.ScopeSnapshot,
) bool {
	if observer == nil || observer.binding == nil || client == nil {
		return false
	}
	observer.binding.mu.Lock()
	defer observer.binding.mu.Unlock()
	return observer.binding.client == client && observer.binding.generation == scope.Generation &&
		client.Context().Name == scope.Context
}

func projectRolloutObservation(
	request application.RestartRolloutRequest,
	object *appsv1.Deployment,
	observationNumber int64,
	observedAt time.Time,
) (application.RestartRolloutObservation, error) {
	if object == nil || object.APIVersion != "" && object.APIVersion != domain.RestartDeploymentTargetAPIVersion ||
		object.Kind != "" && object.Kind != domain.RestartDeploymentTargetKind ||
		object.Namespace != request.Scope.Namespace || object.Name != request.DeploymentName ||
		object.UID == "" || object.Generation < 1 || object.Status.ObservedGeneration < 0 ||
		object.Status.ObservedGeneration > object.Generation ||
		object.Status.UpdatedReplicas < 0 || object.Status.AvailableReplicas < 0 {
		return application.RestartRolloutObservation{}, rolloutProjectionError()
	}
	targetReplicas := int64(1)
	if object.Spec.Replicas != nil {
		targetReplicas = int64(*object.Spec.Replicas)
	}
	sameTarget := string(object.UID) == request.DeploymentUID
	if sameTarget && (object.Generation < request.TargetGeneration ||
		object.Generation == request.TargetGeneration && targetReplicas != request.TargetReplicas) {
		return application.RestartRolloutObservation{}, rolloutProjectionError()
	}
	observation := application.RestartRolloutObservation{
		Scope: request.Scope, DeploymentName: object.Name, DeploymentUID: request.DeploymentUID,
		DeploymentGeneration: object.Generation, TargetGeneration: request.TargetGeneration,
		ObservedGeneration: object.Status.ObservedGeneration,
		UpdatedReplicas:    int64(object.Status.UpdatedReplicas), AvailableReplicas: int64(object.Status.AvailableReplicas),
		TargetReplicas: request.TargetReplicas, ObservationNumber: observationNumber,
		State: application.RestartRolloutProgress, ObservedAt: observedAt,
	}
	switch {
	case string(object.UID) != request.DeploymentUID:
		observation.State = application.RestartRolloutFailed
		observation.FailureCode = application.RestartRolloutFailureTargetReplaced
	case object.Generation > request.TargetGeneration:
		observation.State = application.RestartRolloutFailed
		observation.FailureCode = application.RestartRolloutFailureTargetChanged
	case object.Status.ObservedGeneration >= request.TargetGeneration && deploymentProgressDeadlineExceeded(object.Status.Conditions):
		observation.State = application.RestartRolloutFailed
		observation.FailureCode = application.RestartRolloutFailureProgressDeadline
	case object.Status.ObservedGeneration >= request.TargetGeneration && deploymentReplicaFailure(object.Status.Conditions):
		observation.State = application.RestartRolloutFailed
		observation.FailureCode = application.RestartRolloutFailureReplica
	case object.Status.ObservedGeneration >= request.TargetGeneration &&
		int64(object.Status.UpdatedReplicas) >= request.TargetReplicas &&
		int64(object.Status.AvailableReplicas) >= request.TargetReplicas:
		observation.State = application.RestartRolloutSucceeded
	}
	if observation.ValidateFor(request) != nil {
		return application.RestartRolloutObservation{}, rolloutProjectionError()
	}
	return observation, nil
}

func deploymentProgressDeadlineExceeded(conditions []appsv1.DeploymentCondition) bool {
	for _, condition := range conditions {
		if condition.Type == appsv1.DeploymentProgressing && condition.Status == corev1.ConditionFalse &&
			condition.Reason == "ProgressDeadlineExceeded" {
			return true
		}
	}
	return false
}

func deploymentReplicaFailure(conditions []appsv1.DeploymentCondition) bool {
	for _, condition := range conditions {
		if condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func rolloutProgressChanged(
	previous application.RestartRolloutObservation,
	current application.RestartRolloutObservation,
) bool {
	return previous.DeploymentGeneration != current.DeploymentGeneration ||
		previous.ObservedGeneration != current.ObservedGeneration ||
		previous.UpdatedReplicas != current.UpdatedReplicas ||
		previous.AvailableReplicas != current.AvailableReplicas ||
		previous.TargetReplicas != current.TargetReplicas
}

func rolloutTimeoutResult(
	startedAt time.Time,
	finishedAt time.Time,
	last application.RestartRolloutObservation,
	request application.RestartRolloutRequest,
) (application.RestartRolloutResult, error) {
	result := application.RestartRolloutResult{
		State: application.RestartRolloutTimedOut, Final: last,
		StartedAt: startedAt, FinishedAt: finishedAt,
	}
	if last != (application.RestartRolloutObservation{}) {
		result.ObservationCount = last.ObservationNumber
	}
	if result.ValidateFor(request) != nil {
		return application.RestartRolloutResult{}, rolloutProjectionError()
	}
	return result, nil
}

func validRolloutClockTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.UnixMilli() >= 0 &&
		value.Equal(time.UnixMilli(value.UnixMilli()).UTC())
}

func rolloutInputError() *SafeError {
	return newKubeSafeError(
		ClassInvalidInput,
		"kubernetes_rollout_request_invalid",
		"observe_restart_deployment_rollout",
		"The Deployment rollout observation request is invalid.",
	)
}

func rolloutStaleScopeError() *SafeError {
	return newKubeSafeError(
		ClassStaleScope,
		"kubernetes_rollout_scope_stale",
		"observe_restart_deployment_rollout",
		"The Kubernetes scope changed during Deployment rollout observation.",
	)
}

func rolloutProjectionError() *SafeError {
	return newKubeSafeError(
		ClassInvalidExternalResponse,
		"kubernetes_rollout_response_invalid",
		"observe_restart_deployment_rollout",
		"Kubernetes returned an invalid Deployment rollout response.",
	)
}

func rolloutClockError() *SafeError {
	return newKubeSafeError(
		ClassInternal,
		"kubernetes_rollout_clock_invalid",
		"observe_restart_deployment_rollout",
		"The Deployment rollout observation clock failed safely.",
	)
}
