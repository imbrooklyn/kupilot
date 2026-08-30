package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	clienttesting "k8s.io/client-go/testing"
)

func TestToolResourceReaderUsesOneExactGetAndProjectsOnlyAllowlistedFields(t *testing.T) {
	forbiddenCanary := strings.Repeat("forbidden", 5)
	messageCanary := strings.Repeat("message", 5)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "sample-pod",
			Namespace:   "team-a",
			UID:         "generated-pod-uid",
			Generation:  9,
			Annotations: map[string]string{"generated.invalid/annotation": forbiddenCanary},
			Labels: map[string]string{
				"app.kubernetes.io/name": "sample",
				"generated.invalid/data": forbiddenCanary,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "sample-node",
			Containers: []corev1.Container{
				{
					Name:  "app",
					Image: "registry.invalid/sample/app:v1",
					Env:   []corev1.EnvVar{{Name: "GENERATED_VALUE", Value: forbiddenCanary}},
					VolumeMounts: []corev1.VolumeMount{
						{Name: forbiddenCanary, MountPath: "/generated"},
					},
				},
			},
			Volumes: []corev1.Volume{
				{Name: "generated-volume", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: forbiddenCanary}}},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "ProbeFailed", Message: "token=" + messageCanary},
			},
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "app",
					Image:        "registry.invalid/sample/app:v1",
					Ready:        false,
					RestartCount: 3,
					State: corev1.ContainerState{
						Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "generated waiting message"},
					},
				},
			},
		},
	}
	gateway, client, fakeClient := newFakeGateway(t, "team-a", pod)
	defer client.Close()
	scope := liveScope("team-a")
	reader, err := NewToolResourceReader(gateway, client, scope)
	if err != nil {
		t.Fatalf("NewToolResourceReader() error = %v", err)
	}
	observation, err := reader.ReadResource(context.Background(), toolcontract.ResourceReadRequest{
		Scope: scope,
		Reference: domain.ResourceRef{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  "team-a",
			Name:       "sample-pod",
		},
		Detail: toolcontract.ResourceDetailDiagnostic,
	})
	if err != nil {
		t.Fatalf("ReadResource() error = %v", err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-a")
	if observation.Summary.Reference.Name != "sample-pod" || len(observation.Conditions) != 1 || len(observation.Containers) != 1 ||
		observation.Status.NodeName.Value != "sample-node" || observation.Conditions[0].Message.Value != "token="+messageCanary ||
		!observation.Generation.Present || observation.Generation.Value != 9 {
		t.Fatalf("projected observation = %#v", observation)
	}
	if strings.Contains(fmt.Sprintf("%#v", observation), forbiddenCanary) {
		t.Fatal("Tool ResourceReader projection contains a prohibited source canary")
	}
	if observation.LabelCount != 2 || len(observation.Labels) != 1 || observation.Labels[0].Key != "app.kubernetes.io/name" {
		t.Fatalf("label projection = count %d, labels %#v", observation.LabelCount, observation.Labels)
	}
}

func TestToolResourceReaderUsesFixedTypedGetForEveryDirectKind(t *testing.T) {
	tests := []struct {
		kind     domain.ResourceKind
		name     string
		group    string
		resource string
		object   runtime.Object
		check    func(*testing.T, toolcontract.ResourceObservation)
	}{
		{
			kind: domain.ResourceKindDeployment, name: "sample-deployment", group: "apps", resource: "deployments",
			object: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Name: "sample-deployment", Namespace: "team-a"},
				Spec:       appsv1.DeploymentSpec{Replicas: int32Pointer(3), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sample"}}},
				Status: appsv1.DeploymentStatus{
					Replicas: 3, UpdatedReplicas: 2, ReadyReplicas: 1, AvailableReplicas: 1, UnavailableReplicas: 2,
					Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionFalse, Reason: "MinimumReplicasUnavailable"}},
				},
			},
			check: func(t *testing.T, observation toolcontract.ResourceObservation) {
				if len(observation.Conditions) != 1 || !observation.Status.Current.Present || observation.Status.Current.Value != 3 ||
					!observation.Status.Updated.Present || observation.Status.Updated.Value != 2 {
					t.Fatalf("Deployment observation = %#v", observation)
				}
			},
		},
		{
			kind: domain.ResourceKindReplicaSet, name: "sample-replicaset", group: "apps", resource: "replicasets",
			object: &appsv1.ReplicaSet{
				ObjectMeta: metav1.ObjectMeta{Name: "sample-replicaset", Namespace: "team-a"},
				Spec:       appsv1.ReplicaSetSpec{Replicas: int32Pointer(2), Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sample"}}},
				Status: appsv1.ReplicaSetStatus{
					Replicas: 2, ReadyReplicas: 1, AvailableReplicas: 1,
					Conditions: []appsv1.ReplicaSetCondition{{Type: appsv1.ReplicaSetReplicaFailure, Status: corev1.ConditionTrue, Reason: "FailedCreate"}},
				},
			},
			check: func(t *testing.T, observation toolcontract.ResourceObservation) {
				if len(observation.Conditions) != 1 || !observation.Status.Unavailable.Present || observation.Status.Unavailable.Value != 1 {
					t.Fatalf("ReplicaSet observation = %#v", observation)
				}
			},
		},
		{
			kind: domain.ResourceKindJob, name: "sample-job", group: "batch", resource: "jobs",
			object: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{Name: "sample-job", Namespace: "team-a"},
				Spec: batchv1.JobSpec{
					Completions: int32Pointer(1), Parallelism: int32Pointer(1), BackoffLimit: int32Pointer(4),
				},
				Status: batchv1.JobStatus{Failed: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded"}}},
			},
			check: func(t *testing.T, observation toolcontract.ResourceObservation) {
				if len(observation.Conditions) != 1 || !observation.Status.BackoffLimit.Present || observation.Status.BackoffLimit.Value != 4 {
					t.Fatalf("Job observation = %#v", observation)
				}
			},
		},
		{
			kind: domain.ResourceKindService, name: "sample-service", resource: "services",
			object: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Name: "sample-service", Namespace: "team-a"},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeClusterIP, Selector: map[string]string{"app": "sample"},
					Ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 80, TargetPort: intstr.FromInt32(8080)}},
				},
			},
			check: func(t *testing.T, observation toolcontract.ResourceObservation) {
				if len(observation.ServicePorts) != 1 || observation.ServicePorts[0].TargetPort.Value != "8080" ||
					!observation.Status.SelectorKeyCount.Present || observation.Status.SelectorKeyCount.Value != 1 {
					t.Fatalf("Service observation = %#v", observation)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			gateway, client, fakeClient := newFakeGateway(t, "team-a", test.object)
			defer client.Close()
			scope := liveScope("team-a")
			reader, err := NewToolResourceReader(gateway, client, scope)
			if err != nil {
				t.Fatalf("NewToolResourceReader() error = %v", err)
			}
			observation, err := reader.ReadResource(context.Background(), toolcontract.ResourceReadRequest{
				Scope: scope,
				Reference: domain.ResourceRef{
					APIVersion: test.kind.APIVersion(), Kind: string(test.kind), Namespace: "team-a", Name: test.name,
				},
				Detail: toolcontract.ResourceDetailDiagnostic,
			})
			if err != nil {
				t.Fatalf("ReadResource() error = %v", err)
			}
			assertSingleClientAction(t, fakeClient.Actions(), "get", test.group, "v1", test.resource, "team-a")
			if observation.Validate() != nil || observation.Summary.Reference.Kind != string(test.kind) {
				t.Fatalf("observation = %#v, validation = %v", observation, observation.Validate())
			}
			test.check(t, observation)
		})
	}
}

func TestToolResourceReaderListUsesFixedKindNamespaceLimitAndNoSelectors(t *testing.T) {
	podB := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sample-b", Namespace: "team-a"}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	podA := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sample-a", Namespace: "team-a"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	gateway, client, fakeClient := newFakeGateway(t, "team-a", podB, podA)
	defer client.Close()
	scope := liveScope("team-a")
	reader, err := NewToolResourceReader(gateway, client, scope)
	if err != nil {
		t.Fatalf("NewToolResourceReader() error = %v", err)
	}
	result, err := reader.ListResources(context.Background(), toolcontract.ResourceListRequest{
		Scope:     scope,
		Kind:      domain.ResourceKindPod,
		Namespace: "team-a",
		Limit:     17,
	})
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "list", "", "v1", "pods", "team-a")
	action, ok := fakeClient.Actions()[0].(clienttesting.ListAction)
	if !ok {
		t.Fatalf("action type = %T, want ListAction", fakeClient.Actions()[0])
	}
	if action.GetListRestrictions().Labels.String() != "" || action.GetListRestrictions().Fields.String() != "" {
		t.Fatalf("LIST restrictions = %#v", action.GetListRestrictions())
	}
	if len(result.Items) != 2 || result.Items[0].Summary.Reference.Name != "sample-a" || result.Items[1].Summary.Reference.Name != "sample-b" {
		t.Fatalf("ListResources() = %#v", result)
	}
}

func TestToolResourceReaderAllowsExactCrossNamespaceReadOnlyUnderAllPolicy(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "sample-pod", Namespace: "team-b"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	gateway, client, fakeClient := newFakeGateway(t, "team-a", pod)
	defer client.Close()
	scope := liveScope("team-a")
	scope.NamespaceAccess = domain.NamespaceAccessAll
	reader, err := NewToolResourceReader(gateway, client, scope)
	if err != nil {
		t.Fatalf("NewToolResourceReader() error = %v", err)
	}
	observation, err := reader.ReadResource(context.Background(), toolcontract.ResourceReadRequest{
		Scope: scope,
		Reference: domain.ResourceRef{
			APIVersion: "v1", Kind: "Pod", Namespace: "team-b", Name: "sample-pod",
		},
		Detail: toolcontract.ResourceDetailDiagnostic,
	})
	if err != nil {
		t.Fatalf("ReadResource(cross namespace) error = %v", err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-b")
	if observation.Validate() != nil || observation.Summary.Reference.Namespace != "team-b" {
		t.Fatalf("cross-Namespace observation = %#v", observation)
	}
}

func TestToolResourceReaderAllNamespaceListIsExplicitAndSortedByNamespaceThenName(t *testing.T) {
	podTeamB := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sample-a", Namespace: "team-b"}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	podTeamA := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sample-z", Namespace: "team-a"}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	gateway, client, fakeClient := newFakeGateway(t, "team-a", podTeamB, podTeamA)
	defer client.Close()
	scope := liveScope("team-a")
	scope.NamespaceAccess = domain.NamespaceAccessAll
	reader, err := NewToolResourceReader(gateway, client, scope)
	if err != nil {
		t.Fatalf("NewToolResourceReader() error = %v", err)
	}
	result, err := reader.ListResources(context.Background(), toolcontract.ResourceListRequest{
		Scope: scope, Kind: domain.ResourceKindPod, AllNamespaces: true, Limit: 17,
	})
	if err != nil {
		t.Fatalf("ListResources(all namespaces) error = %v", err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "list", "", "v1", "pods", "")
	if len(result.Items) != 2 || result.Items[0].Summary.Reference.Namespace != "team-a" ||
		result.Items[0].Summary.Reference.Name != "sample-z" ||
		result.Items[1].Summary.Reference.Namespace != "team-b" || result.Items[1].Summary.Reference.Name != "sample-a" {
		t.Fatalf("all-Namespace ListResources() = %#v", result)
	}
}

func TestToolResourceReaderDeniesInvalidKindScopeAndCancellationBeforeClientAction(t *testing.T) {
	gateway, client, fakeClient := newFakeGateway(t, "team-a")
	defer client.Close()
	scope := liveScope("team-a")
	reader, err := NewToolResourceReader(gateway, client, scope)
	if err != nil {
		t.Fatalf("NewToolResourceReader() error = %v", err)
	}
	tests := []struct {
		name    string
		context func() context.Context
		request toolcontract.ResourceReadRequest
		class   ErrorClass
	}{
		{
			name:    "Secret",
			context: func() context.Context { return context.Background() },
			request: toolcontract.ResourceReadRequest{
				Scope:     scope,
				Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Secret", Namespace: "team-a", Name: "sample-secret"},
				Detail:    toolcontract.ResourceDetailDiagnostic,
			},
			class: ClassPolicyDenied,
		},
		{
			name:    "unknown Kind",
			context: func() context.Context { return context.Background() },
			request: toolcontract.ResourceReadRequest{
				Scope:     scope,
				Reference: domain.ResourceRef{APIVersion: "generated.invalid/v1", Kind: "Widget", Namespace: "team-a", Name: "sample-widget"},
				Detail:    toolcontract.ResourceDetailDiagnostic,
			},
			class: ClassPolicyDenied,
		},
		{
			name:    "cross Namespace",
			context: func() context.Context { return context.Background() },
			request: toolcontract.ResourceReadRequest{
				Scope:     scope,
				Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-b", Name: "sample-pod"},
				Detail:    toolcontract.ResourceDetailDiagnostic,
			},
			class: ClassPolicyDenied,
		},
		{
			name:    "stale reader scope",
			context: func() context.Context { return context.Background() },
			request: func() toolcontract.ResourceReadRequest {
				changed := scope
				changed.Generation++
				return toolcontract.ResourceReadRequest{
					Scope:     changed,
					Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"},
					Detail:    toolcontract.ResourceDetailDiagnostic,
				}
			}(),
			class: ClassStaleScope,
		},
		{
			name: "cancelled",
			context: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			request: toolcontract.ResourceReadRequest{
				Scope:     scope,
				Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"},
				Detail:    toolcontract.ResourceDetailDiagnostic,
			},
			class: ClassCancelled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeClient.ClearActions()
			_, err := reader.ReadResource(test.context(), test.request)
			var safe *SafeError
			if err == nil || !errors.As(err, &safe) || safe.Class() != test.class {
				t.Fatalf("ReadResource() error = %#v, want class %q", err, test.class)
			}
			if actions := fakeClient.Actions(); len(actions) != 0 {
				t.Fatalf("denied request actions = %#v, want none", actions)
			}
		})
	}
}
