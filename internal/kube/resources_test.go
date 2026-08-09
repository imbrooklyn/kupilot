package kube

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"
)

func TestResourceServiceUsesOnlyFixedTypedGetAndListActions(t *testing.T) {
	canary := strings.Repeat("s", 37) + "-generated"
	controller := true
	objects := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:            "sample-pod",
				Namespace:       "team-a",
				UID:             types.UID("pod-uid"),
				ResourceVersion: "pod-version",
				Labels:          map[string]string{"prohibited": canary},
				Annotations:     map[string]string{"prohibited": canary},
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion: "apps/v1",
					Kind:       "StatefulSet",
					Name:       "sample-statefulset",
					UID:        types.UID("statefulset-uid"),
					Controller: &controller,
				}},
			},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name: "app",
					Env:  []corev1.EnvVar{{Name: "PROHIBITED", Value: canary}},
				}},
				Volumes: []corev1.Volume{{
					Name: "private",
					VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
						SecretName: canary,
					}},
				}},
			},
			Status: corev1.PodStatus{
				Phase: corev1.PodRunning,
				ContainerStatuses: []corev1.ContainerStatus{{
					Name:  "app",
					Ready: false,
					State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: canary}},
				}},
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-deployment", Namespace: "team-a", UID: types.UID("deployment-uid")},
			Spec:       appsv1.DeploymentSpec{Replicas: int32Pointer(3)},
			Status: appsv1.DeploymentStatus{
				ReadyReplicas:     1,
				AvailableReplicas: 1,
				Conditions: []appsv1.DeploymentCondition{{
					Type:    appsv1.DeploymentAvailable,
					Status:  corev1.ConditionFalse,
					Reason:  "MinimumReplicasUnavailable",
					Message: canary,
				}},
			},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-replicaset", Namespace: "team-a", UID: types.UID("replicaset-uid")},
			Spec:       appsv1.ReplicaSetSpec{Replicas: int32Pointer(2)},
			Status:     appsv1.ReplicaSetStatus{ReadyReplicas: 1, AvailableReplicas: 1},
		},
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-job", Namespace: "team-a", UID: types.UID("job-uid")},
			Spec:       batchv1.JobSpec{Completions: int32Pointer(1)},
			Status: batchv1.JobStatus{
				Active: 0,
				Failed: 1,
				Conditions: []batchv1.JobCondition{{
					Type:    batchv1.JobFailed,
					Status:  corev1.ConditionTrue,
					Reason:  "BackoffLimitExceeded",
					Message: canary,
				}},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-service", Namespace: "team-a", UID: types.UID("service-uid")},
			Spec: corev1.ServiceSpec{
				Type:     corev1.ServiceTypeClusterIP,
				Selector: map[string]string{"prohibited": canary},
			},
		},
	}
	gateway, client, fakeClient := newFakeGateway(t, "team-a", objects...)
	defer client.Close()
	scope := liveScope("team-a")

	tests := []struct {
		kind       domain.ResourceKind
		name       string
		group      string
		version    string
		resource   string
		wantPhase  string
		wantReason string
	}{
		{kind: domain.ResourceKindPod, name: "sample-pod", version: "v1", resource: "pods", wantPhase: "Running", wantReason: "CrashLoopBackOff"},
		{kind: domain.ResourceKindDeployment, name: "sample-deployment", group: "apps", version: "v1", resource: "deployments", wantReason: "MinimumReplicasUnavailable"},
		{kind: domain.ResourceKindReplicaSet, name: "sample-replicaset", group: "apps", version: "v1", resource: "replicasets"},
		{kind: domain.ResourceKindJob, name: "sample-job", group: "batch", version: "v1", resource: "jobs", wantPhase: "Failed", wantReason: "BackoffLimitExceeded"},
		{kind: domain.ResourceKindService, name: "sample-service", version: "v1", resource: "services"},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			fakeClient.ClearActions()
			reference := domain.ResourceRef{
				APIVersion: test.kind.APIVersion(),
				Kind:       string(test.kind),
				Namespace:  scope.Namespace,
				Name:       test.name,
			}
			summary, err := gateway.GetResource(context.Background(), client, scope, reference)
			if err != nil {
				t.Fatalf("GetResource() error = %v", err)
			}
			assertSingleClientAction(t, fakeClient.Actions(), "get", test.group, test.version, test.resource, scope.Namespace)
			if summary.Reference.Name != test.name || summary.Reference.Namespace != scope.Namespace || summary.Reference.Kind != string(test.kind) {
				t.Fatalf("GetResource() reference = %#v", summary.Reference)
			}
			if summary.Status.Phase != test.wantPhase || summary.Status.Reason != test.wantReason {
				t.Fatalf("GetResource() status = %#v, want phase %q reason %q", summary.Status, test.wantPhase, test.wantReason)
			}
			assertNoCanary(t, summary, canary)

			fakeClient.ClearActions()
			list, err := gateway.ListResources(context.Background(), client, scope, test.kind, 20)
			if err != nil {
				t.Fatalf("ListResources() error = %v", err)
			}
			assertSingleClientAction(t, fakeClient.Actions(), "list", test.group, test.version, test.resource, scope.Namespace)
			listAction, ok := fakeClient.Actions()[0].(clienttesting.ListAction)
			if !ok {
				t.Fatalf("list action type = %T", fakeClient.Actions()[0])
			}
			if got := listAction.GetListRestrictions().Labels.String(); got != "" {
				t.Fatalf("direct LIST label selector = %q, want empty", got)
			}
			if got := listAction.GetListRestrictions().Fields.String(); got != "" {
				t.Fatalf("direct LIST field selector = %q, want empty", got)
			}
			if len(list.Items) != 1 || list.Items[0].Reference.Name != test.name {
				t.Fatalf("ListResources() = %#v", list)
			}
			assertNoCanary(t, list, canary)
		})
	}

	fakeClient.ClearActions()
	pod, err := gateway.GetResource(context.Background(), client, scope, domain.ResourceRef{
		APIVersion: "v1",
		Kind:       "Pod",
		Namespace:  scope.Namespace,
		Name:       "sample-pod",
	})
	if err != nil {
		t.Fatalf("StatefulSet owner Pod GetResource() error = %v", err)
	}
	if len(pod.Owners) != 1 || pod.Owners[0].Kind != "StatefulSet" || !pod.Owners[0].ReferenceOnly {
		t.Fatalf("Pod owner projection = %#v", pod.Owners)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", scope.Namespace)
}

func TestResourceServiceDeniesSecretUnknownCrossNamespaceAndAllNamespaceBeforeAction(t *testing.T) {
	gateway, client, fakeClient := newFakeGateway(t, "team-a")
	defer client.Close()
	validScope := liveScope("team-a")

	tests := []struct {
		name      string
		operation func() error
		class     ErrorClass
		code      string
	}{
		{
			name: "Secret get",
			operation: func() error {
				_, err := gateway.GetResource(context.Background(), client, validScope, domain.ResourceRef{
					APIVersion: "v1", Kind: "Secret", Namespace: "team-a", Name: "sample-secret",
				})
				return err
			},
			class: ClassPolicyDenied,
			code:  "kubernetes_resource_kind_denied",
		},
		{
			name: "unknown Kind list",
			operation: func() error {
				_, err := gateway.ListResources(context.Background(), client, validScope, domain.ResourceKind("Widget"), 20)
				return err
			},
			class: ClassPolicyDenied,
			code:  "kubernetes_resource_kind_denied",
		},
		{
			name: "EndpointSlice direct list",
			operation: func() error {
				_, err := gateway.ListResources(context.Background(), client, validScope, domain.ResourceKind("EndpointSlice"), 20)
				return err
			},
			class: ClassPolicyDenied,
			code:  "kubernetes_resource_kind_denied",
		},
		{
			name: "StatefulSet direct list",
			operation: func() error {
				_, err := gateway.ListResources(context.Background(), client, validScope, domain.ResourceKind("StatefulSet"), 20)
				return err
			},
			class: ClassPolicyDenied,
			code:  "kubernetes_resource_kind_denied",
		},
		{
			name: "cross-Namespace get",
			operation: func() error {
				_, err := gateway.GetResource(context.Background(), client, validScope, domain.ResourceRef{
					APIVersion: "v1", Kind: "Pod", Namespace: "team-b", Name: "sample-pod",
				})
				return err
			},
			class: ClassPolicyDenied,
			code:  "kubernetes_cross_namespace_denied",
		},
		{
			name: "cross-Context list",
			operation: func() error {
				scope := validScope
				scope.Context = "other-context"
				_, err := gateway.ListResources(context.Background(), client, scope, domain.ResourceKindPod, 20)
				return err
			},
			class: ClassPolicyDenied,
			code:  "kubernetes_cross_context_denied",
		},
		{
			name: "all-Namespace list",
			operation: func() error {
				scope := validScope
				scope.Namespace = ""
				_, err := gateway.ListResources(context.Background(), client, scope, domain.ResourceKindPod, 20)
				return err
			},
			class: ClassPolicyDenied,
			code:  "kubernetes_all_namespaces_denied",
		},
		{
			name: "limit over maximum",
			operation: func() error {
				_, err := gateway.ListResources(context.Background(), client, validScope, domain.ResourceKindPod, domain.MaxResourceSummaries+1)
				return err
			},
			class: ClassInvalidInput,
			code:  "kubernetes_resource_limit_invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeClient.ClearActions()
			err := test.operation()
			assertKubeSafeError(t, err, test.class, test.code)
			if actions := fakeClient.Actions(); len(actions) != 0 {
				t.Fatalf("denied operation performed %d Kubernetes actions, want 0", len(actions))
			}
		})
	}
}

func TestResourceServiceClassifiesCancellationAndTimeoutWithoutRawErrors(t *testing.T) {
	gateway, client, fakeClient := newFakeGateway(t, "team-a")
	defer client.Close()
	reference := domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"}
	tests := []struct {
		name  string
		raw   error
		class ErrorClass
		code  string
	}{
		{name: "cancelled", raw: context.Canceled, class: ClassCancelled, code: "kubernetes_request_cancelled"},
		{name: "timeout", raw: context.DeadlineExceeded, class: ClassTimeout, code: "kubernetes_request_timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeClient.ClearActions()
			fakeClient.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, test.raw
			})
			_, err := gateway.GetResource(context.Background(), client, liveScope("team-a"), reference)
			assertKubeSafeError(t, err, test.class, test.code)
			assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-a")
		})
	}
}

func TestEndpointSliceCountsRemainInternalAddressFreeAndSelectorBound(t *testing.T) {
	canary := strings.Repeat("a", 41) + "-generated"
	ready := true
	notReady := false
	objects := []runtime.Object{
		&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "sample-slice",
				Namespace: "team-a",
				Labels:    map[string]string{discoveryv1.LabelServiceName: "sample-service"},
			},
			AddressType: discoveryv1.AddressTypeIPv4,
			Endpoints: []discoveryv1.Endpoint{
				{Addresses: []string{canary}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
				{Addresses: []string{canary}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}},
				{Addresses: []string{canary}},
			},
		},
	}
	gateway, client, fakeClient := newFakeGateway(t, "team-a", objects...)
	defer client.Close()
	counts, err := gateway.countServiceEndpoints(context.Background(), client, liveScope("team-a"), "sample-service")
	if err != nil {
		t.Fatalf("countServiceEndpoints() error = %v", err)
	}
	if counts.Ready != 2 || counts.NotReady != 1 {
		t.Fatalf("endpoint counts = %#v, want ready=2 notReady=1", counts)
	}
	assertNoCanary(t, counts, canary)
	assertSingleClientAction(t, fakeClient.Actions(), "list", "discovery.k8s.io", "v1", "endpointslices", "team-a")
	action, ok := fakeClient.Actions()[0].(clienttesting.ListAction)
	if !ok {
		t.Fatalf("EndpointSlice action type = %T, want ListAction", fakeClient.Actions()[0])
	}
	if got := action.GetListRestrictions().Labels.String(); got != discoveryv1.LabelServiceName+"=sample-service" {
		t.Fatalf("EndpointSlice selector = %q", got)
	}
}

func liveScope(namespace string) domain.ClusterScope {
	return domain.ClusterScope{
		Context:     "selected",
		Namespace:   namespace,
		Generation:  1,
		ActivatedAt: time.UnixMilli(1).UTC(),
	}
}

func int32Pointer(value int32) *int32 {
	return &value
}

func assertSingleClientAction(t *testing.T, actions []clienttesting.Action, verb, group, version, resource, namespace string) {
	t.Helper()
	if len(actions) != 1 {
		t.Fatalf("Kubernetes actions = %d, want 1: %#v", len(actions), actions)
	}
	action := actions[0]
	wantResource := schema.GroupVersionResource{Group: group, Version: version, Resource: resource}
	if action.GetVerb() != verb || action.GetResource() != wantResource || action.GetNamespace() != namespace {
		t.Fatalf("action = %s %s namespace %q, want %s %s namespace %q", action.GetVerb(), action.GetResource(), action.GetNamespace(), verb, wantResource, namespace)
	}
}

func TestResourceSummaryBoundsKeepFiftyItems(t *testing.T) {
	objects := make([]runtime.Object, 0, domain.MaxResourceSummaries+5)
	for index := 0; index < domain.MaxResourceSummaries+5; index++ {
		objects = append(objects, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("sample-pod-%02d", index), Namespace: "team-a"},
		})
	}
	gateway, client, _ := newFakeGateway(t, "team-a", objects...)
	defer client.Close()
	list, err := gateway.ListResources(context.Background(), client, liveScope("team-a"), domain.ResourceKindPod, domain.MaxResourceSummaries)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(list.Items) != domain.MaxResourceSummaries || !list.Truncated {
		t.Fatalf("ListResources() count/truncated = %d/%v, want %d/true", len(list.Items), list.Truncated, domain.MaxResourceSummaries)
	}
}
