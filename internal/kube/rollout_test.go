package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
)

func TestDeploymentRolloutObserverReportsProgressThenVerifiedSuccess(t *testing.T) {
	deployment := rolloutTestDeployment(12, 3, 0, 1)
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	responses := []*appsv1.Deployment{
		rolloutTestDeployment(12, 3, 0, 1),
		rolloutTestDeployment(12, 3, 3, 3),
	}
	responses[1].Status.ObservedGeneration = 12
	index := 0
	fakeClient.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		response := responses[min(index, len(responses)-1)].DeepCopy()
		index++
		return true, response, nil
	})
	clock := newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC())
	observer := newTestDeploymentRolloutObserver(t, restarter.binding, RolloutObserverConfig{}, clock)
	sink := &recordingRolloutSink{}

	result, err := observer.ObserveRestartRollout(context.Background(), rolloutTestRequest(), sink)
	if err != nil {
		t.Fatalf("ObserveRestartRollout() error = %v", err)
	}
	if result.State != application.RestartRolloutSucceeded || result.Final.State != application.RestartRolloutSucceeded ||
		result.Final.ObservedGeneration != 12 || result.Final.UpdatedReplicas != 3 ||
		result.Final.AvailableReplicas != 3 || result.ObservationCount != 2 {
		t.Fatalf("rollout result = %#v", result)
	}
	progress := sink.Snapshot()
	if len(progress) != 1 || progress[0].State != application.RestartRolloutProgress ||
		progress[0].ObservedGeneration != 11 || progress[0].UpdatedReplicas != 0 ||
		progress[0].AvailableReplicas != 1 {
		t.Fatalf("progress observations = %#v", progress)
	}
	assertRolloutReadActions(t, fakeClient.Actions(), 2)
}

func TestDeploymentRolloutObserverClassifiesTerminalFailures(t *testing.T) {
	tests := []struct {
		name      string
		condition appsv1.DeploymentCondition
		want      application.RestartRolloutFailureCode
	}{
		{
			name: "progress deadline exceeded",
			condition: appsv1.DeploymentCondition{
				Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse,
				Reason: "ProgressDeadlineExceeded",
			},
			want: application.RestartRolloutFailureProgressDeadline,
		},
		{
			name: "replica failure",
			condition: appsv1.DeploymentCondition{
				Type: appsv1.DeploymentReplicaFailure, Status: corev1.ConditionTrue,
				Reason: "FailedCreate",
			},
			want: application.RestartRolloutFailureReplica,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deployment := rolloutTestDeployment(12, 3, 1, 1)
			deployment.Status.ObservedGeneration = 12
			deployment.Status.Conditions = []appsv1.DeploymentCondition{test.condition}
			restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
			defer closeClient()
			observer := newTestDeploymentRolloutObserver(
				t, restarter.binding, RolloutObserverConfig{}, newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC()),
			)
			sink := &recordingRolloutSink{}

			result, err := observer.ObserveRestartRollout(context.Background(), rolloutTestRequest(), sink)
			if err != nil {
				t.Fatalf("ObserveRestartRollout() error = %v", err)
			}
			if result.State != application.RestartRolloutFailed || result.Final.FailureCode != test.want ||
				len(sink.Snapshot()) != 0 {
				t.Fatalf("rollout result/progress = %#v/%#v", result, sink.Snapshot())
			}
			assertRolloutReadActions(t, fakeClient.Actions(), 1)
		})
	}
}

func TestDeploymentRolloutObserverTimesOutWithoutRetryingAnyWrite(t *testing.T) {
	deployment := rolloutTestDeployment(12, 2, 1, 0)
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	clock := newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC())
	observer := newTestDeploymentRolloutObserver(t, restarter.binding, RolloutObserverConfig{
		Timeout: 4 * time.Second, PollInterval: 2 * time.Second,
	}, clock)
	sink := &recordingRolloutSink{}

	result, err := observer.ObserveRestartRollout(context.Background(), rolloutTestRequestWithReplicas(2), sink)
	if err != nil {
		t.Fatalf("ObserveRestartRollout() error = %v", err)
	}
	if result.State != application.RestartRolloutTimedOut || result.ObservationCount != 2 ||
		result.Final.State != application.RestartRolloutProgress {
		t.Fatalf("rollout result = %#v", result)
	}
	if got := len(sink.Snapshot()); got != 1 {
		t.Fatalf("deduplicated progress count = %d, want 1", got)
	}
	if waits := clock.Waits(); !reflect.DeepEqual(waits, []time.Duration{2 * time.Second, 2 * time.Second}) {
		t.Fatalf("clock waits = %v", waits)
	}
	assertRolloutReadActions(t, fakeClient.Actions(), 2)
	if writes := countRestartActions(fakeClient.Actions(), "patch"); writes != 0 {
		t.Fatalf("rollout timeout PATCH actions = %d, want 0", writes)
	}
}

func TestDeploymentRolloutObserverDoesNotAcceptTerminalReadAtDeadline(t *testing.T) {
	deployment := rolloutTestDeployment(12, 1, 1, 1)
	deployment.Status.ObservedGeneration = 12
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	clock := newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC())
	fakeClient.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		clock.advance(4 * time.Second)
		return true, deployment.DeepCopy(), nil
	})
	observer := newTestDeploymentRolloutObserver(t, restarter.binding, RolloutObserverConfig{
		Timeout: 4 * time.Second, PollInterval: 2 * time.Second,
	}, clock)

	result, err := observer.ObserveRestartRollout(
		context.Background(), rolloutTestRequestWithReplicas(1), &recordingRolloutSink{},
	)
	if err != nil || result.State != application.RestartRolloutTimedOut ||
		result.Final != (application.RestartRolloutObservation{}) || result.ObservationCount != 0 {
		t.Fatalf("deadline result/error = %#v/%v", result, err)
	}
	assertRolloutReadActions(t, fakeClient.Actions(), 1)
	if writes := countRestartActions(fakeClient.Actions(), "patch"); writes != 0 {
		t.Fatalf("deadline PATCH actions = %d, want 0", writes)
	}
}

func TestDeploymentRolloutObserverClassifiesFinalInFlightRequestAsRolloutTimeout(t *testing.T) {
	deployment := rolloutTestDeployment(12, 1, 0, 0)
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	clock := newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC())
	reads := 0
	fakeClient.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		reads++
		if reads == 1 {
			return true, deployment.DeepCopy(), nil
		}
		clock.advance(2 * time.Second)
		return true, nil, context.DeadlineExceeded
	})
	observer := newTestDeploymentRolloutObserver(t, restarter.binding, RolloutObserverConfig{
		Timeout: 4 * time.Second, PollInterval: 2 * time.Second,
	}, clock)

	result, err := observer.ObserveRestartRollout(
		context.Background(), rolloutTestRequestWithReplicas(1), &recordingRolloutSink{},
	)
	if err != nil || result.State != application.RestartRolloutTimedOut || result.ObservationCount != 1 {
		t.Fatalf("in-flight deadline result/error = %#v/%v", result, err)
	}
	assertRolloutReadActions(t, fakeClient.Actions(), 2)
	if writes := countRestartActions(fakeClient.Actions(), "patch"); writes != 0 {
		t.Fatalf("in-flight deadline PATCH actions = %d, want 0", writes)
	}
}

func TestDeploymentRolloutObserverClassifiesInitialInFlightRequestAsRolloutTimeout(t *testing.T) {
	deployment := rolloutTestDeployment(12, 1, 0, 0)
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	clock := newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC())
	fakeClient.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		clock.advance(2 * time.Second)
		return true, nil, context.DeadlineExceeded
	})
	observer := newTestDeploymentRolloutObserver(t, restarter.binding, RolloutObserverConfig{
		Timeout: 2 * time.Second, PollInterval: 2 * time.Second,
	}, clock)

	result, err := observer.ObserveRestartRollout(
		context.Background(), rolloutTestRequestWithReplicas(1), &recordingRolloutSink{},
	)
	if err != nil || result.State != application.RestartRolloutTimedOut || result.ObservationCount != 0 ||
		result.Final != (application.RestartRolloutObservation{}) {
		t.Fatalf("initial in-flight deadline result/error = %#v/%v", result, err)
	}
	assertRolloutReadActions(t, fakeClient.Actions(), 1)
	if writes := countRestartActions(fakeClient.Actions(), "patch"); writes != 0 {
		t.Fatalf("initial in-flight deadline PATCH actions = %d, want 0", writes)
	}
}

func TestDeploymentRolloutObserverClassifiesOwningDeadlineAtObserverBoundaryAsTimeout(t *testing.T) {
	deployment := rolloutTestDeployment(12, 1, 0, 0)
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	clock := newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC())
	clock.waitErr = context.DeadlineExceeded
	observer := newTestDeploymentRolloutObserver(t, restarter.binding, RolloutObserverConfig{
		Timeout: 4 * time.Second, PollInterval: 4 * time.Second,
	}, clock)

	result, err := observer.ObserveRestartRollout(
		context.Background(), rolloutTestRequestWithReplicas(1), &recordingRolloutSink{},
	)
	if err != nil || result.State != application.RestartRolloutTimedOut || result.ObservationCount != 1 {
		t.Fatalf("owning deadline result/error = %#v/%v", result, err)
	}
	assertRolloutReadActions(t, fakeClient.Actions(), 1)
	if writes := countRestartActions(fakeClient.Actions(), "patch"); writes != 0 {
		t.Fatalf("owning deadline PATCH actions = %d, want 0", writes)
	}
}

func TestDeploymentRolloutObserverHonorsCancellationAndScopeInvalidation(t *testing.T) {
	t.Run("cancel while waiting", func(t *testing.T) {
		deployment := rolloutTestDeployment(12, 1, 0, 0)
		restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
		defer closeClient()
		ctx, cancel := context.WithCancel(context.Background())
		clock := newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC())
		clock.afterWait = cancel
		observer := newTestDeploymentRolloutObserver(t, restarter.binding, RolloutObserverConfig{}, clock)

		result, err := observer.ObserveRestartRollout(ctx, rolloutTestRequestWithReplicas(1), &recordingRolloutSink{})
		if !errors.Is(err, context.Canceled) || result.State != "" {
			t.Fatalf("cancelled result/error = %#v/%v", result, err)
		}
		assertRolloutReadActions(t, fakeClient.Actions(), 1)
	})

	t.Run("scope changes during read", func(t *testing.T) {
		deployment := rolloutTestDeployment(12, 1, 0, 0)
		restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
		defer closeClient()
		fakeClient.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
			if err := restarter.binding.InvalidateScope(8); err != nil {
				t.Fatalf("InvalidateScope() error = %v", err)
			}
			return true, deployment.DeepCopy(), nil
		})
		observer := newTestDeploymentRolloutObserver(
			t, restarter.binding, RolloutObserverConfig{}, newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC()),
		)

		result, err := observer.ObserveRestartRollout(context.Background(), rolloutTestRequestWithReplicas(1), &recordingRolloutSink{})
		assertRestartSafeClass(t, err, domain.SafeErrorClassStaleScope)
		if result.State != "" {
			t.Fatalf("stale-scope result = %#v", result)
		}
		assertRolloutReadActions(t, fakeClient.Actions(), 1)
	})
}

func TestDeploymentRolloutObserverDefaultsAndTighteningMatchFixture(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "testdata", "kube", "rollout-policy.json"))
	if err != nil {
		t.Fatalf("os.ReadFile(rollout policy fixture) error = %v", err)
	}
	var fixture struct {
		SchemaVersion            int   `json:"schema_version"`
		TimeoutMilliseconds      int64 `json:"timeout_milliseconds"`
		PollIntervalMilliseconds int64 `json:"poll_interval_milliseconds"`
		MaximumObservations      int   `json:"maximum_observations"`
	}
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatalf("json.Unmarshal(rollout policy fixture) error = %v", err)
	}
	if fixture.SchemaVersion != 1 || time.Duration(fixture.TimeoutMilliseconds)*time.Millisecond != DefaultRolloutTimeout ||
		time.Duration(fixture.PollIntervalMilliseconds)*time.Millisecond != DefaultRolloutPollInterval ||
		fixture.MaximumObservations != MaxRolloutObservations ||
		MaxRolloutObservations != int(DefaultRolloutTimeout/DefaultRolloutPollInterval) {
		t.Fatalf("rollout policy fixture = %#v", fixture)
	}

	deployment := rolloutTestDeployment(12, 1, 1, 1)
	restarter, closeClient, _ := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	clock := newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC())
	defaults := newTestDeploymentRolloutObserver(t, restarter.binding, RolloutObserverConfig{}, clock)
	if defaults.timeout != DefaultRolloutTimeout || defaults.pollInterval != DefaultRolloutPollInterval {
		t.Fatalf("default timeout/poll = %v/%v", defaults.timeout, defaults.pollInterval)
	}
	if _, err := newDeploymentRolloutObserver(restarter.binding, RolloutObserverConfig{
		Timeout: 30 * time.Second, PollInterval: 5 * time.Second,
	}, clock); err != nil {
		t.Fatalf("tightened policy error = %v", err)
	}
	for _, invalid := range []RolloutObserverConfig{
		{Timeout: DefaultRolloutTimeout + time.Millisecond},
		{PollInterval: DefaultRolloutPollInterval - time.Millisecond},
		{Timeout: time.Second, PollInterval: DefaultRolloutPollInterval},
		{Timeout: 30 * time.Second, PollInterval: 31 * time.Second},
	} {
		if _, err := newDeploymentRolloutObserver(restarter.binding, invalid, clock); err == nil {
			t.Fatalf("rollout observer accepted expanding/invalid policy %#v", invalid)
		}
	}
}

func TestDeploymentRolloutObserverRejectsInvalidInputBeforeKubernetesRead(t *testing.T) {
	deployment := rolloutTestDeployment(12, 1, 1, 1)
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	observer := newTestDeploymentRolloutObserver(
		t, restarter.binding, RolloutObserverConfig{}, newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC()),
	)
	if _, err := observer.ObserveRestartRollout(context.Background(), application.RestartRolloutRequest{}, &recordingRolloutSink{}); err == nil {
		t.Fatal("ObserveRestartRollout(invalid request) error = nil")
	}
	if _, err := observer.ObserveRestartRollout(context.Background(), rolloutTestRequest(), nil); err == nil {
		t.Fatal("ObserveRestartRollout(nil sink) error = nil")
	}
	if actions := fakeClient.Actions(); len(actions) != 0 {
		t.Fatalf("invalid rollout Kubernetes actions = %#v", actions)
	}
}

func TestDeploymentRolloutObserverUsesOnlyExactBoundedGETsOnTheWire(t *testing.T) {
	deployment := rolloutTestDeployment(12, 1, 1, 1)
	deployment.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}
	deployment.Status.ObservedGeneration = 12
	requests := make(chan restartHTTPRequest, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "request body unavailable", http.StatusInternalServerError)
			return
		}
		requests <- restartHTTPRequest{
			method: request.Method, path: request.URL.EscapedPath(), query: request.URL.RawQuery,
			contentType: request.Header.Get("Content-Type"), body: string(body),
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(deployment); err != nil {
			t.Errorf("encode Kubernetes response: %v", err)
		}
	}))
	defer server.Close()

	path := writeNamespacedKubeconfig(t, server.URL, testServerCAData(server), "team-a")
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	gateway, err := NewGateway(factory)
	if err != nil {
		t.Fatalf("NewGateway() error = %v", err)
	}
	binding, err := NewToolScopeBinding(gateway)
	if err != nil {
		t.Fatalf("NewToolScopeBinding() error = %v", err)
	}
	if err := binding.InvalidateScope(7); err != nil {
		t.Fatalf("InvalidateScope() error = %v", err)
	}
	client, err := binding.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer client.Close()
	observer := newTestDeploymentRolloutObserver(
		t, binding, RolloutObserverConfig{}, newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC()),
	)
	result, err := observer.ObserveRestartRollout(
		context.Background(), rolloutTestRequestWithReplicas(1), &recordingRolloutSink{},
	)
	if err != nil || result.State != application.RestartRolloutSucceeded {
		t.Fatalf("ObserveRestartRollout() result/error = %#v/%v", result, err)
	}

	request := <-requests
	const resourcePath = "/apis/apps/v1/namespaces/team-a/deployments/sample-deployment"
	if request.method != http.MethodGet || request.path != resourcePath || request.query != "timeout=10s" ||
		request.contentType != "" || request.body != "" {
		t.Fatalf("rollout GET request = %#v", request)
	}
	select {
	case extra := <-requests:
		t.Fatalf("unexpected extra Kubernetes request = %#v", extra)
	default:
	}
}

func TestDeploymentRolloutObserverStopsWhenProgressSinkFails(t *testing.T) {
	deployment := rolloutTestDeployment(12, 1, 0, 0)
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	observer := newTestDeploymentRolloutObserver(
		t, restarter.binding, RolloutObserverConfig{}, newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC()),
	)
	wantErr := errors.New("synthetic progress sink failure")
	result, err := observer.ObserveRestartRollout(
		context.Background(), rolloutTestRequestWithReplicas(1), &recordingRolloutSink{err: wantErr},
	)
	if !errors.Is(err, wantErr) || result.State != "" {
		t.Fatalf("progress sink result/error = %#v/%v", result, err)
	}
	assertRolloutReadActions(t, fakeClient.Actions(), 1)
}

func TestDeploymentRolloutObserverReportsPermissionLossWithoutAnotherWrite(t *testing.T) {
	deployment := rolloutTestDeployment(12, 1, 0, 0)
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
	defer closeClient()
	fakeClient.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "deployments"},
			"sample-deployment",
			errors.New("synthetic rollout permission denial"),
		)
	})
	observer := newTestDeploymentRolloutObserver(
		t, restarter.binding, RolloutObserverConfig{}, newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC()),
	)

	result, err := observer.ObserveRestartRollout(
		context.Background(), rolloutTestRequestWithReplicas(1), &recordingRolloutSink{},
	)
	assertRestartSafeClass(t, err, domain.SafeErrorClassPermissionDenied)
	if result.State != "" {
		t.Fatalf("permission-loss result = %#v", result)
	}
	assertRolloutReadActions(t, fakeClient.Actions(), 1)
	if writes := countRestartActions(fakeClient.Actions(), "patch"); writes != 0 {
		t.Fatalf("permission-loss PATCH actions = %d, want 0", writes)
	}
}

func TestDeploymentRolloutObserverFailsClosedWhenTargetChangesAfterPatch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*appsv1.Deployment)
		want   application.RestartRolloutFailureCode
	}{
		{name: "replaced", mutate: func(value *appsv1.Deployment) {
			value.UID = "replacement-uid"
			value.Generation = 1
			value.Status.ObservedGeneration = 0
		}, want: application.RestartRolloutFailureTargetReplaced},
		{name: "changed", mutate: func(value *appsv1.Deployment) { value.Generation = 13 }, want: application.RestartRolloutFailureTargetChanged},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deployment := rolloutTestDeployment(12, 1, 0, 0)
			test.mutate(deployment)
			restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, time.UnixMilli(1_700_000_000_000).UTC(), deployment)
			defer closeClient()
			observer := newTestDeploymentRolloutObserver(
				t, restarter.binding, RolloutObserverConfig{}, newFakeRolloutClock(time.UnixMilli(1_700_000_000_100).UTC()),
			)
			result, err := observer.ObserveRestartRollout(
				context.Background(), rolloutTestRequestWithReplicas(1), &recordingRolloutSink{},
			)
			if err != nil || result.State != application.RestartRolloutFailed || result.Final.FailureCode != test.want {
				t.Fatalf("changed target result/error = %#v/%v", result, err)
			}
			assertRolloutReadActions(t, fakeClient.Actions(), 1)
		})
	}
}

func newTestDeploymentRolloutObserver(
	t *testing.T,
	binding *ToolScopeBinding,
	config RolloutObserverConfig,
	clock rolloutClock,
) *DeploymentRolloutObserver {
	t.Helper()
	observer, err := newDeploymentRolloutObserver(binding, config, clock)
	if err != nil {
		t.Fatalf("newDeploymentRolloutObserver() error = %v", err)
	}
	return observer
}

func rolloutTestRequest() application.RestartRolloutRequest {
	return rolloutTestRequestWithReplicas(3)
}

func rolloutTestRequestWithReplicas(replicas int64) application.RestartRolloutRequest {
	return application.RestartRolloutRequest{
		Scope:          domain.ScopeSnapshot{Context: "selected", Namespace: "team-a", Generation: 7},
		DeploymentName: "sample-deployment", DeploymentUID: "deployment-uid",
		TargetGeneration: 12, TargetReplicas: replicas,
	}
}

func rolloutTestDeployment(generation, replicas, updated, available int64) *appsv1.Deployment {
	replicaCount := int32(replicas)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sample-deployment", Namespace: "team-a", UID: "deployment-uid",
			ResourceVersion: "rollout-rv", Generation: generation,
		},
		Spec: appsv1.DeploymentSpec{Replicas: &replicaCount},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: generation - 1,
			UpdatedReplicas:    int32(updated), AvailableReplicas: int32(available),
		},
	}
}

func assertRolloutReadActions(t *testing.T, actions []clienttesting.Action, want int) {
	t.Helper()
	if len(actions) != want {
		t.Fatalf("rollout Kubernetes actions = %d, want %d: %#v", len(actions), want, actions)
	}
	for _, action := range actions {
		assertRestartAction(t, action, "get")
		if action.GetSubresource() != "" {
			t.Fatalf("rollout GET subresource = %q, want empty", action.GetSubresource())
		}
	}
}

type recordingRolloutSink struct {
	mu           sync.Mutex
	observations []application.RestartRolloutObservation
	err          error
}

func (sink *recordingRolloutSink) PublishRestartRolloutProgress(
	_ context.Context,
	observation application.RestartRolloutObservation,
) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.err != nil {
		return sink.err
	}
	sink.observations = append(sink.observations, observation)
	return nil
}

func (sink *recordingRolloutSink) Snapshot() []application.RestartRolloutObservation {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]application.RestartRolloutObservation(nil), sink.observations...)
}

type fakeRolloutClock struct {
	mu        sync.Mutex
	now       time.Time
	waits     []time.Duration
	afterWait func()
	waitErr   error
}

func newFakeRolloutClock(now time.Time) *fakeRolloutClock {
	return &fakeRolloutClock{now: now}
}

func (clock *fakeRolloutClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeRolloutClock) Wait(ctx context.Context, duration time.Duration) error {
	clock.mu.Lock()
	clock.waits = append(clock.waits, duration)
	clock.now = clock.now.Add(duration)
	after := clock.afterWait
	waitErr := clock.waitErr
	clock.mu.Unlock()
	if after != nil {
		after()
	}
	if waitErr != nil {
		return waitErr
	}
	return ctx.Err()
}

func (clock *fakeRolloutClock) Waits() []time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return append([]time.Duration(nil), clock.waits...)
}

func (clock *fakeRolloutClock) advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}
