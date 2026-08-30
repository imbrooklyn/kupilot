package kube

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
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
	if len(pod.Owners) != 1 || pod.Owners[0].Kind != "StatefulSet" || pod.Owners[0].ReferenceOnly {
		t.Fatalf("Pod owner projection = %#v", pod.Owners)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", scope.Namespace)
}

func TestExpandedOperationalKindsUseFixedTypedReadsAndSafeProjections(t *testing.T) {
	canary := strings.Repeat("prohibited-source-", 3)
	suspended := true
	ingressClass := canary
	storageClass := canary
	objects := []runtime.Object{
		&corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "team-b", Annotations: map[string]string{"generated.invalid/value": canary}},
			Status:     corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
		},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "worker-a", Annotations: map[string]string{"generated.invalid/value": canary}},
			Spec:       corev1.NodeSpec{ProviderID: canary, Taints: []corev1.Taint{{Key: "generated.invalid/value", Value: canary}}},
			Status: corev1.NodeStatus{
				Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: canary}},
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
			},
		},
		&corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-claim", Namespace: "team-a", Annotations: map[string]string{"generated.invalid/value": canary}},
			Spec: corev1.PersistentVolumeClaimSpec{
				StorageClassName: &storageClass, VolumeName: canary, AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			},
			Status: corev1.PersistentVolumeClaimStatus{Phase: corev1.ClaimBound},
		},
		&corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-volume", Annotations: map[string]string{"generated.invalid/value": canary}},
			Spec: corev1.PersistentVolumeSpec{
				StorageClassName: canary,
				PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
					Driver: canary, VolumeHandle: canary, VolumeAttributes: map[string]string{"generated.invalid/value": canary},
				}},
			},
			Status: corev1.PersistentVolumeStatus{Phase: corev1.VolumeBound},
		},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-config", Namespace: "team-a", Annotations: map[string]string{"generated.invalid/value": canary}},
			Data:       map[string]string{"generated.invalid/value": canary},
			BinaryData: map[string][]byte{"generated.invalid/binary": []byte(canary)},
		},
		&appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-statefulset", Namespace: "team-a", Annotations: map[string]string{"generated.invalid/value": canary}},
			Spec: appsv1.StatefulSetSpec{
				Replicas: int32Pointer(3),
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: "app", Env: []corev1.EnvVar{{Name: "GENERATED_VALUE", Value: canary}},
				}}}},
			},
			Status: appsv1.StatefulSetStatus{CurrentReplicas: 2, ReadyReplicas: 2, AvailableReplicas: 2},
		},
		&appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-daemonset", Namespace: "team-a", Annotations: map[string]string{"generated.invalid/value": canary}},
			Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "agent", Env: []corev1.EnvVar{{Name: "GENERATED_VALUE", Value: canary}},
			}}}}},
			Status: appsv1.DaemonSetStatus{DesiredNumberScheduled: 3, NumberReady: 2, NumberAvailable: 2},
		},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-cronjob", Namespace: "team-a", Annotations: map[string]string{"generated.invalid/value": canary}},
			Spec: batchv1.CronJobSpec{
				Schedule: canary, Suspend: &suspended,
				JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "job", Env: []corev1.EnvVar{{Name: "GENERATED_VALUE", Value: canary}}}},
				}}}},
			},
			Status: batchv1.CronJobStatus{Active: []corev1.ObjectReference{{Name: canary}}},
		},
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-ingress", Namespace: "team-a", Annotations: map[string]string{"generated.invalid/value": canary}},
			Spec: networkingv1.IngressSpec{
				IngressClassName: &ingressClass,
				TLS:              []networkingv1.IngressTLS{{SecretName: canary}},
				Rules:            []networkingv1.IngressRule{{Host: canary}},
			},
			Status: networkingv1.IngressStatus{LoadBalancer: networkingv1.IngressLoadBalancerStatus{
				Ingress: []networkingv1.IngressLoadBalancerIngress{{IP: canary, Hostname: canary}},
			}},
		},
		&autoscalingv2.HorizontalPodAutoscaler{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-hpa", Namespace: "team-a", Annotations: map[string]string{"generated.invalid/value": canary}},
			Spec: autoscalingv2.HorizontalPodAutoscalerSpec{ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1", Kind: "Deployment", Name: canary,
			}},
			Status: autoscalingv2.HorizontalPodAutoscalerStatus{CurrentReplicas: 2, DesiredReplicas: 3},
		},
		&policyv1.PodDisruptionBudget{
			ObjectMeta: metav1.ObjectMeta{Name: "sample-pdb", Namespace: "team-a", Annotations: map[string]string{"generated.invalid/value": canary}},
			Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"generated.invalid/value": canary,
			}}},
			Status: policyv1.PodDisruptionBudgetStatus{DesiredHealthy: 3, CurrentHealthy: 2, DisruptionsAllowed: 1},
		},
	}
	gateway, client, fakeClient := newFakeGateway(t, "team-a", objects...)
	defer client.Close()
	scope := liveScope("team-a")

	tests := []struct {
		kind      domain.ResourceKind
		name      string
		namespace string
		group     string
		version   string
		resource  string
		status    domain.ResourceStatus
	}{
		{domain.ResourceKindNamespace, "team-b", "", "", "v1", "namespaces", domain.ResourceStatus{Phase: "Active"}},
		{domain.ResourceKindNode, "worker-a", "", "", "v1", "nodes", domain.ResourceStatus{Phase: "Ready", Ready: domain.Count(1), Desired: domain.Count(1)}},
		{domain.ResourceKindPersistentVolumeClaim, "sample-claim", "team-a", "", "v1", "persistentvolumeclaims", domain.ResourceStatus{Phase: "Bound"}},
		{domain.ResourceKindPersistentVolume, "sample-volume", "", "", "v1", "persistentvolumes", domain.ResourceStatus{Phase: "Bound"}},
		{domain.ResourceKindConfigMap, "sample-config", "team-a", "", "v1", "configmaps", domain.ResourceStatus{}},
		{domain.ResourceKindStatefulSet, "sample-statefulset", "team-a", "apps", "v1", "statefulsets", domain.ResourceStatus{Desired: domain.Count(3), Ready: domain.Count(2), Available: domain.Count(2), Succeeded: domain.Count(2)}},
		{domain.ResourceKindDaemonSet, "sample-daemonset", "team-a", "apps", "v1", "daemonsets", domain.ResourceStatus{Desired: domain.Count(3), Ready: domain.Count(2), Available: domain.Count(2)}},
		{domain.ResourceKindCronJob, "sample-cronjob", "team-a", "batch", "v1", "cronjobs", domain.ResourceStatus{Phase: "Suspended", Active: domain.Count(1)}},
		{domain.ResourceKindIngress, "sample-ingress", "team-a", "networking.k8s.io", "v1", "ingresses", domain.ResourceStatus{}},
		{domain.ResourceKindHorizontalPodAutoscaler, "sample-hpa", "team-a", "autoscaling", "v2", "horizontalpodautoscalers", domain.ResourceStatus{Desired: domain.Count(3), Ready: domain.Count(2)}},
		{domain.ResourceKindPodDisruptionBudget, "sample-pdb", "team-a", "policy", "v1", "poddisruptionbudgets", domain.ResourceStatus{Desired: domain.Count(3), Ready: domain.Count(2), Available: domain.Count(1)}},
	}
	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			fakeClient.ClearActions()
			reference := domain.ResourceRef{
				APIVersion: test.kind.APIVersion(), Kind: string(test.kind), Namespace: test.namespace, Name: test.name,
			}
			summary, err := gateway.GetResource(context.Background(), client, scope, reference)
			if err != nil || summary.Validate() != nil {
				t.Fatalf("GetResource() summary/error = %#v/%v, validation = %v", summary, err, summary.Validate())
			}
			assertSingleClientAction(t, fakeClient.Actions(), "get", test.group, test.version, test.resource, test.namespace)
			if summary.Status != test.status {
				t.Fatalf("GetResource() status = %#v, want %#v", summary.Status, test.status)
			}
			assertNoCanary(t, summary, canary)

			fakeClient.ClearActions()
			list, err := gateway.ListResources(context.Background(), client, scope, test.kind, 17)
			if err != nil || list.Validate() != nil {
				t.Fatalf("ListResources() list/error = %#v/%v, validation = %v", list, err, list.Validate())
			}
			assertSingleClientAction(t, fakeClient.Actions(), "list", test.group, test.version, test.resource, test.namespace)
			if len(list.Items) != 1 || list.Items[0].Reference != summary.Reference || list.Items[0].Status != test.status {
				t.Fatalf("ListResources() = %#v, want one matching summary", list)
			}
			assertNoCanary(t, list, canary)
		})
	}
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
			name: "cross-Namespace get",
			operation: func() error {
				_, err := gateway.GetResource(context.Background(), client, validScope, domain.ResourceRef{
					APIVersion: "v1", Kind: "Pod", Namespace: "team-b", Name: "sample-pod",
				})
				return err
			},
			class: ClassPolicyDenied,
			code:  "kubernetes_namespace_access_denied",
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
		Context:         "selected",
		Namespace:       namespace,
		NamespaceAccess: domain.NamespaceAccessCurrent,
		Generation:      1,
		ActivatedAt:     time.UnixMilli(1).UTC(),
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
