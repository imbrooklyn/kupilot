package kube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

func TestRemediatorPreparesEveryTypedActionFromFreshObjectsWithoutMutation(t *testing.T) {
	tests := []struct {
		name      string
		operation domain.ActionOperation
		target    domain.ResourceRef
		replicas  int64
		revision  int64
		all       bool
		wantRisk  domain.RiskClass
	}{
		{name: "scale up one", operation: domain.ActionOperationScaleWorkload, target: remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), replicas: 3, wantRisk: domain.RiskReview},
		{name: "rollback", operation: domain.ActionOperationRollbackDeployment, target: remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), revision: 2, wantRisk: domain.RiskCritical},
		{name: "owned Pod delete", operation: domain.ActionOperationDeleteOwnedPod, target: remediationCandidate("v1", "Pod", "team-a", "delete-pod"), wantRisk: domain.RiskReview},
		{name: "cordon", operation: domain.ActionOperationCordonNode, target: remediationCandidate("v1", "Node", "", "worker-a"), wantRisk: domain.RiskReview},
		{name: "uncordon", operation: domain.ActionOperationUncordonNode, target: remediationCandidate("v1", "Node", "", "worker-b"), wantRisk: domain.RiskReview},
		{name: "drain", operation: domain.ActionOperationDrainNode, target: remediationCandidate("v1", "Node", "", "worker-a"), all: true, wantRisk: domain.RiskCritical},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := remediationObjects(t)
			if test.operation == domain.ActionOperationDrainNode {
				objects = drainObjects(t)
			}
			remediator, closeClient, fakeClient := newFakeRemediator(t, objects...)
			defer closeClient()
			request := remediationRequest(test.operation, test.target, test.replicas, test.revision, test.all)
			plan, err := remediator.PrepareRemediationAction(context.Background(), request)
			if err != nil || plan.Validate() != nil || plan.Risk() != test.wantRisk || plan.Target.Resource.UID == "" || plan.Target.Resource.ResourceVersion == "" {
				t.Fatalf("PrepareRemediationAction() = %#v/%v validation=%v risk=%s", plan, err, plan.Validate(), plan.Risk())
			}
			if test.operation == domain.ActionOperationDrainNode && (plan.TargetSet.Validate() != nil || plan.Limits.MaximumItems != 2) {
				t.Fatalf("drain target set = %#v limits=%#v", plan.TargetSet.Members(), plan.Limits)
			}
			assertNoRemediationMutation(t, fakeClient.Actions())
		})
	}
}

func TestRemediatorRevalidatesExactRBACAndTargetBeforeOneScaleAttempt(t *testing.T) {
	remediator, closeClient, fakeClient := newFakeRemediator(t, remediationObjects(t)...)
	defer closeClient()
	plan, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
		domain.ActionOperationScaleWorkload, remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), 3, 0, false,
	))
	if err != nil {
		t.Fatal(err)
	}

	var reviews []authorizationv1.ResourceAttributes
	fakeClient.PrependReactor("create", "selfsubjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
		created := action.(clienttesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
		reviews = append(reviews, *created.Spec.ResourceAttributes)
		return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	if err := remediator.RevalidateRemediationAction(context.Background(), plan); err != nil {
		t.Fatalf("RevalidateRemediationAction() error = %v", err)
	}
	wantReview := authorizationv1.ResourceAttributes{Group: "apps", Version: "v1", Resource: "deployments", Subresource: "scale", Verb: "update", Namespace: "team-a", Name: "sample"}
	if !reflect.DeepEqual(reviews, []authorizationv1.ResourceAttributes{wantReview}) {
		t.Fatalf("SSAR attributes = %#v, want %#v", reviews, wantReview)
	}

	fakeClient.PrependReactor("update", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "scale" {
			return false, nil, nil
		}
		scale := action.(clienttesting.UpdateAction).GetObject().(*autoscalingv1.Scale).DeepCopy()
		scale.ResourceVersion = "12"
		return true, scale, nil
	})
	fakeClient.ClearActions()
	attempt, err := remediator.ExecuteRemediationAction(context.Background(), plan)
	if err != nil || attempt.State != domain.RemediationAccepted || attempt.AttemptedCount != 1 || countRemediationMutations(fakeClient.Actions()) != 1 {
		t.Fatalf("ExecuteRemediationAction() = %#v/%v actions=%#v", attempt, err, fakeClient.Actions())
	}

	t.Run("RBAC denial makes zero mutation calls", func(t *testing.T) {
		denied, closeDenied, deniedClient := newFakeRemediator(t, remediationObjects(t)...)
		defer closeDenied()
		deniedPlan, prepareErr := denied.PrepareRemediationAction(context.Background(), remediationRequest(
			domain.ActionOperationScaleWorkload, remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), 3, 0, false,
		))
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		deniedClient.ClearActions()
		if err := denied.RevalidateRemediationAction(context.Background(), deniedPlan); err == nil {
			t.Fatal("RBAC denial was accepted")
		}
		assertNoRemediationMutation(t, deniedClient.Actions())
	})

	t.Run("changed resource version invalidates before mutation", func(t *testing.T) {
		changed, closeChanged, changedClient := newFakeRemediator(t, remediationObjects(t)...)
		defer closeChanged()
		changedClient.PrependReactor("create", "selfsubjectaccessreviews", func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
		})
		changedPlan, prepareErr := changed.PrepareRemediationAction(context.Background(), remediationRequest(
			domain.ActionOperationScaleWorkload, remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), 3, 0, false,
		))
		if prepareErr != nil {
			t.Fatal(prepareErr)
		}
		current, getErr := changedClient.AppsV1().Deployments("team-a").Get(context.Background(), "sample", metav1.GetOptions{})
		if getErr != nil {
			t.Fatal(getErr)
		}
		current.ResourceVersion = "changed"
		if _, updateErr := changedClient.AppsV1().Deployments("team-a").Update(context.Background(), current, metav1.UpdateOptions{}); updateErr != nil {
			t.Fatal(updateErr)
		}
		changedClient.ClearActions()
		if err := changed.RevalidateRemediationAction(context.Background(), changedPlan); err == nil {
			t.Fatal("changed target was accepted")
		}
		assertNoRemediationMutation(t, changedClient.Actions())
	})
}

func TestRemediatorUsesExactRBACForEveryTypedOperationAndDenialMakesZeroMutationCalls(t *testing.T) {
	tests := []struct {
		name      string
		operation domain.ActionOperation
		target    domain.ResourceRef
		replicas  int64
		revision  int64
		all       bool
		want      []authorizationv1.ResourceAttributes
	}{
		{name: "scale", operation: domain.ActionOperationScaleWorkload, target: remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), replicas: 3, want: []authorizationv1.ResourceAttributes{{Group: "apps", Version: "v1", Resource: "deployments", Subresource: "scale", Verb: "update", Namespace: "team-a", Name: "sample"}}},
		{name: "rollback", operation: domain.ActionOperationRollbackDeployment, target: remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), revision: 2, want: []authorizationv1.ResourceAttributes{{Group: "apps", Version: "v1", Resource: "deployments", Verb: "update", Namespace: "team-a", Name: "sample"}}},
		{name: "Pod delete", operation: domain.ActionOperationDeleteOwnedPod, target: remediationCandidate("v1", "Pod", "team-a", "delete-pod"), want: []authorizationv1.ResourceAttributes{{Version: "v1", Resource: "pods", Verb: "delete", Namespace: "team-a", Name: "delete-pod"}}},
		{name: "cordon", operation: domain.ActionOperationCordonNode, target: remediationCandidate("v1", "Node", "", "worker-a"), want: []authorizationv1.ResourceAttributes{{Version: "v1", Resource: "nodes", Verb: "patch", Name: "worker-a"}}},
		{name: "uncordon", operation: domain.ActionOperationUncordonNode, target: remediationCandidate("v1", "Node", "", "worker-b"), want: []authorizationv1.ResourceAttributes{{Version: "v1", Resource: "nodes", Verb: "patch", Name: "worker-b"}}},
		{name: "drain", operation: domain.ActionOperationDrainNode, target: remediationCandidate("v1", "Node", "", "worker-a"), all: true, want: []authorizationv1.ResourceAttributes{
			{Version: "v1", Resource: "nodes", Verb: "patch", Name: "worker-a"},
			{Version: "v1", Resource: "pods", Subresource: "eviction", Verb: "create", Namespace: "team-a"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := remediationObjects(t)
			if test.operation == domain.ActionOperationDrainNode {
				objects = drainObjects(t)
			}
			remediator, closeClient, fakeClient := newFakeRemediator(t, objects...)
			defer closeClient()
			plan, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
				test.operation, test.target, test.replicas, test.revision, test.all,
			))
			if err != nil {
				t.Fatal(err)
			}
			var got []authorizationv1.ResourceAttributes
			fakeClient.PrependReactor("create", "selfsubjectaccessreviews", func(action clienttesting.Action) (bool, runtime.Object, error) {
				created := action.(clienttesting.CreateAction).GetObject().(*authorizationv1.SelfSubjectAccessReview)
				got = append(got, *created.Spec.ResourceAttributes)
				return true, &authorizationv1.SelfSubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
			})
			fakeClient.ClearActions()
			if err := remediator.RevalidateRemediationAction(context.Background(), plan); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("RBAC attributes = %#v, want %#v", got, test.want)
			}
			assertNoRemediationMutation(t, fakeClient.Actions())

			denied, closeDenied, deniedClient := newFakeRemediator(t, objects...)
			defer closeDenied()
			deniedPlan, err := denied.PrepareRemediationAction(context.Background(), remediationRequest(
				test.operation, test.target, test.replicas, test.revision, test.all,
			))
			if err != nil {
				t.Fatal(err)
			}
			deniedClient.ClearActions()
			if err := denied.RevalidateRemediationAction(context.Background(), deniedPlan); err == nil {
				t.Fatal("RBAC denial was accepted")
			}
			assertNoRemediationMutation(t, deniedClient.Actions())
		})
	}
}

func TestDrainPreparationRejectsUnsafeOrMutableSets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]runtime.Object) []runtime.Object
	}{
		{name: "current Namespace scope", mutate: func(objects []runtime.Object) []runtime.Object { return objects }},
		{name: "DaemonSet Pod", mutate: func(objects []runtime.Object) []runtime.Object {
			pod := drainPod().DeepCopy()
			pod.OwnerReferences[0].Kind = "DaemonSet"
			return replaceObject(objects, "drain-pod", pod)
		}},
		{name: "static Pod", mutate: func(objects []runtime.Object) []runtime.Object {
			pod := drainPod().DeepCopy()
			pod.Annotations = map[string]string{corev1.MirrorPodAnnotationKey: "mirror"}
			return replaceObject(objects, "drain-pod", pod)
		}},
		{name: "local data", mutate: func(objects []runtime.Object) []runtime.Object {
			pod := drainPod().DeepCopy()
			pod.Spec.Volumes = []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
			return replaceObject(objects, "drain-pod", pod)
		}},
		{name: "inline CSI data", mutate: func(objects []runtime.Object) []runtime.Object {
			pod := drainPod().DeepCopy()
			pod.Spec.Volumes = []corev1.Volume{{Name: "inline", VolumeSource: corev1.VolumeSource{CSI: &corev1.CSIVolumeSource{Driver: "storage.example.test"}}}}
			return replaceObject(objects, "drain-pod", pod)
		}},
		{name: "GitRepo data", mutate: func(objects []runtime.Object) []runtime.Object {
			pod := drainPod().DeepCopy()
			pod.Spec.Volumes = []corev1.Volume{{Name: "repository", VolumeSource: corev1.VolumeSource{GitRepo: &corev1.GitRepoVolumeSource{Repository: "https://example.test/repository"}}}}
			return replaceObject(objects, "drain-pod", pod)
		}},
		{name: "blocked PDB", mutate: func(objects []runtime.Object) []runtime.Object {
			for _, object := range objects {
				if pdb, ok := object.(*policyv1.PodDisruptionBudget); ok {
					pdb.Status.DisruptionsAllowed = 0
				}
			}
			return objects
		}},
		{name: "deleting PDB", mutate: func(objects []runtime.Object) []runtime.Object {
			for _, object := range objects {
				if pdb, ok := object.(*policyv1.PodDisruptionBudget); ok {
					deleting := metav1.NewTime(time.UnixMilli(1_500).UTC())
					pdb.DeletionTimestamp = &deleting
				}
			}
			return objects
		}},
		{name: "deleting Node", mutate: func(objects []runtime.Object) []runtime.Object {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a", UID: "node-a-uid", ResourceVersion: "11"}}
			deleting := metav1.NewTime(time.UnixMilli(1_500).UTC())
			node.DeletionTimestamp = &deleting
			return replaceObject(objects, "worker-a", node)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := test.mutate(drainObjects(t))
			remediator, closeClient, fakeClient := newFakeRemediator(t, objects...)
			defer closeClient()
			all := test.name != "current Namespace scope"
			_, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
				domain.ActionOperationDrainNode, remediationCandidate("v1", "Node", "", "worker-a"), 0, 0, all,
			))
			if err == nil {
				t.Fatal("unsafe drain set was admitted")
			}
			assertNoRemediationMutation(t, fakeClient.Actions())
		})
	}
}

func TestRemediationPreparationRejectsDeletingPrimaryTargets(t *testing.T) {
	tests := []struct {
		name      string
		operation domain.ActionOperation
		target    domain.ResourceRef
		replicas  int64
		revision  int64
		mutate    func([]runtime.Object) []runtime.Object
	}{
		{name: "scale Deployment", operation: domain.ActionOperationScaleWorkload, target: remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), replicas: 3, mutate: func(objects []runtime.Object) []runtime.Object {
			deployment := remediationDeployment().DeepCopy()
			deleting := metav1.NewTime(time.UnixMilli(1_500).UTC())
			deployment.DeletionTimestamp = &deleting
			return replaceObject(objects, "sample", deployment)
		}},
		{name: "rollback source", operation: domain.ActionOperationRollbackDeployment, target: remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), revision: 2, mutate: func(objects []runtime.Object) []runtime.Object {
			source := remediationRollbackSource().DeepCopy()
			deleting := metav1.NewTime(time.UnixMilli(1_500).UTC())
			source.DeletionTimestamp = &deleting
			return replaceObject(objects, "sample-old", source)
		}},
		{name: "Node scheduling", operation: domain.ActionOperationCordonNode, target: remediationCandidate("v1", "Node", "", "worker-a"), mutate: func(objects []runtime.Object) []runtime.Object {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a", UID: "node-a-uid", ResourceVersion: "11"}}
			deleting := metav1.NewTime(time.UnixMilli(1_500).UTC())
			node.DeletionTimestamp = &deleting
			return replaceObject(objects, "worker-a", node)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := test.mutate(remediationObjects(t))
			remediator, closeClient, fakeClient := newFakeRemediator(t, objects...)
			defer closeClient()
			_, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
				test.operation, test.target, test.replicas, test.revision, false,
			))
			if err == nil {
				t.Fatal("deleting target was admitted")
			}
			assertNoRemediationMutation(t, fakeClient.Actions())
		})
	}
}

func TestDrainStopsAfterCordonWhenApprovedPodSetChanges(t *testing.T) {
	remediator, closeClient, fakeClient := newFakeRemediator(t, drainObjects(t)...)
	defer closeClient()
	plan, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
		domain.ActionOperationDrainNode, remediationCandidate("v1", "Node", "", "worker-a"), 0, 0, true,
	))
	if err != nil {
		t.Fatal(err)
	}
	fakeClient.PrependReactor("patch", "nodes", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name: "worker-a", UID: "node-a-uid", ResourceVersion: "12",
		}, Spec: corev1.NodeSpec{Unschedulable: true}}, nil
	})
	newPod := drainPod().DeepCopy()
	newPod.Name, newPod.UID, newPod.ResourceVersion = "new-after-approval", "new-pod-uid", "1"
	fakeClient.PrependReactor("list", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		list := action.(clienttesting.ListAction)
		if got := list.GetListRestrictions().Fields.String(); got != "spec.nodeName=worker-a" {
			t.Fatalf("drain recheck field selector = %q", got)
		}
		return true, &corev1.PodList{Items: []corev1.Pod{*drainPod(), *newPod}}, nil
	})
	fakeClient.ClearActions()
	attempt, err := executeDrain(context.Background(), &ClientBundle{typed: fakeClient}, plan)
	if err == nil || attempt.State != domain.RemediationFailed || attempt.AcceptedCount != 1 ||
		attempt.AttemptedCount != 1 || attempt.ErrorClass != domain.SafeErrorClassConflict {
		t.Fatalf("changed-set drain = %#v/%v", attempt, err)
	}
	if mutations := countRemediationMutations(fakeClient.Actions()); mutations != 1 {
		t.Fatalf("changed-set mutations = %d, want only the cordon: %#v", mutations, fakeClient.Actions())
	}
	for _, action := range fakeClient.Actions() {
		if action.GetSubresource() == "eviction" {
			t.Fatalf("changed-set drain attempted eviction: %#v", action)
		}
	}
}

func TestRollbackPreparationRejectsPausedDeploymentWithoutMutation(t *testing.T) {
	objects := remediationObjects(t)
	paused := remediationDeployment().DeepCopy()
	paused.Spec.Paused = true
	objects = replaceObject(objects, "sample", paused)
	remediator, closeClient, fakeClient := newFakeRemediator(t, objects...)
	defer closeClient()
	_, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
		domain.ActionOperationRollbackDeployment,
		remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), 0, 2, false,
	))
	if err == nil {
		t.Fatal("paused Deployment rollback was admitted")
	}
	assertNoRemediationMutation(t, fakeClient.Actions())
}

func TestRollbackPreparationRejectsCurrentOrFutureRevisionWithoutMutation(t *testing.T) {
	for _, revision := range []int64{3, 4} {
		t.Run(fmt.Sprintf("revision_%d", revision), func(t *testing.T) {
			remediator, closeClient, fakeClient := newFakeRemediator(t, remediationObjects(t)...)
			defer closeClient()
			_, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
				domain.ActionOperationRollbackDeployment,
				remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), 0, revision, false,
			))
			if err == nil {
				t.Fatalf("revision %d was admitted as a prior revision", revision)
			}
			assertNoRemediationMutation(t, fakeClient.Actions())
		})
	}
}

func TestTypedRemediationUsesExactClientGoRequestsAndBodies(t *testing.T) {
	type recordedRequest struct {
		method, path, query, contentType, body string
	}
	var mu sync.Mutex
	requests := make([]recordedRequest, 0, 8)
	deployment := remediationDeployment()
	source := remediationRollbackSource()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		requests = append(requests, recordedRequest{request.Method, request.URL.EscapedPath(), request.URL.RawQuery, request.Header.Get("Content-Type"), string(body)})
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.EscapedPath() {
		case "PUT /apis/apps/v1/namespaces/team-a/deployments/sample/scale":
			writeJSON(t, writer, &autoscalingv1.Scale{TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v1", Kind: "Scale"}, ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "team-a", UID: "deployment-uid", ResourceVersion: "12"}, Spec: autoscalingv1.ScaleSpec{Replicas: 3}})
		case "GET /apis/apps/v1/namespaces/team-a/replicasets/sample-old":
			writeJSON(t, writer, source)
		case "GET /apis/apps/v1/namespaces/team-a/deployments/sample":
			writeJSON(t, writer, deployment)
		case "PUT /apis/apps/v1/namespaces/team-a/deployments/sample":
			updated := deployment.DeepCopy()
			updated.ResourceVersion = "12"
			writeJSON(t, writer, updated)
		case "DELETE /api/v1/namespaces/team-a/pods/delete-pod", "POST /api/v1/namespaces/team-a/pods/drain-pod/eviction":
			writeJSON(t, writer, &metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusSuccess})
		case "PATCH /api/v1/nodes/worker-a":
			writeJSON(t, writer, &corev1.Node{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: "worker-a", UID: "node-a-uid", ResourceVersion: "12"}, Spec: corev1.NodeSpec{Unschedulable: true}})
		case "PATCH /api/v1/nodes/worker-b":
			writeJSON(t, writer, &corev1.Node{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Node"}, ObjectMeta: metav1.ObjectMeta{Name: "worker-b", UID: "node-b-uid", ResourceVersion: "13"}})
		case "GET /api/v1/pods":
			writeJSON(t, writer, &corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{*drainPod()}})
		default:
			http.Error(writer, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, UserAgent: DefaultUserAgent, ContentConfig: rest.ContentConfig{
		ContentType: runtime.ContentTypeJSON, AcceptContentTypes: runtime.ContentTypeJSON,
	}})
	if err != nil {
		t.Fatal(err)
	}
	bundle := &ClientBundle{typed: client}

	fingerprint, err := deploymentTemplateFingerprint(deployment.Spec.Template)
	if err != nil {
		t.Fatal(err)
	}
	scalePlan := domain.RemediationActionPlan{Target: domain.ActionTarget{Resource: remediationReference("apps/v1", "Deployment", "team-a", "sample", "deployment-uid", "11")}, Parameters: domain.ActionParameters{ReplicaTarget: 3}}
	if _, err := executeScale(context.Background(), bundle, scalePlan); err != nil {
		t.Fatal(err)
	}
	sourceFingerprint, err := rollbackTemplateFingerprint(source.Spec.Template)
	if err != nil {
		t.Fatal(err)
	}
	rollbackSet, err := domain.NewRemediationTargetSet([]domain.RemediationPlanMember{{Role: domain.RemediationMemberRollbackSource, Resource: remediationReference("apps/v1", "ReplicaSet", "team-a", "sample-old", "old-rs-uid", "8"), Fingerprint: domain.ActionDigest(sourceFingerprint), Revision: 2}})
	if err != nil {
		t.Fatal(err)
	}
	rollbackPlan := domain.RemediationActionPlan{Target: domain.ActionTarget{Resource: remediationReference("apps/v1", "Deployment", "team-a", "sample", "deployment-uid", "11"), Fingerprint: fingerprint}, TargetSet: rollbackSet}
	if _, err := executeRollback(context.Background(), bundle, rollbackPlan); err != nil {
		t.Fatal(err)
	}
	deletePlan := domain.RemediationActionPlan{Target: domain.ActionTarget{Resource: remediationReference("v1", "Pod", "team-a", "delete-pod", "delete-pod-uid", "21")}, Parameters: domain.ActionParameters{GracePeriodSeconds: domain.RemediationGracePeriodSeconds}}
	if _, err := executePodDelete(context.Background(), bundle, deletePlan); err != nil {
		t.Fatal(err)
	}
	nodePlan := domain.RemediationActionPlan{Target: domain.ActionTarget{Resource: remediationReference("v1", "Node", "", "worker-a", "node-a-uid", "11")}, Parameters: domain.ActionParameters{Unschedulable: true}}
	if _, err := executeNodeScheduling(context.Background(), bundle, nodePlan); err != nil {
		t.Fatal(err)
	}
	uncordonPlan := domain.RemediationActionPlan{Target: domain.ActionTarget{Resource: remediationReference("v1", "Node", "", "worker-b", "node-b-uid", "12")}, Parameters: domain.ActionParameters{Unschedulable: false}}
	if _, err := executeNodeScheduling(context.Background(), bundle, uncordonPlan); err != nil {
		t.Fatal(err)
	}
	drainFingerprint, err := podIdentityFingerprint(drainPod())
	if err != nil {
		t.Fatal(err)
	}
	drainSet, err := domain.NewRemediationTargetSet([]domain.RemediationPlanMember{{Role: domain.RemediationMemberDrainPod, Resource: remediationReference("v1", "Pod", "team-a", "drain-pod", "drain-pod-uid", "22"), Fingerprint: domain.ActionDigest(drainFingerprint), Order: 1}})
	if err != nil {
		t.Fatal(err)
	}
	drainPlan := domain.RemediationActionPlan{Target: nodePlan.Target, Parameters: domain.ActionParameters{GracePeriodSeconds: domain.RemediationGracePeriodSeconds}, TargetSet: drainSet}
	if _, err := executeDrain(context.Background(), bundle, drainPlan); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	got := append([]recordedRequest(nil), requests...)
	mu.Unlock()
	want := []struct{ method, path string }{
		{"PUT", "/apis/apps/v1/namespaces/team-a/deployments/sample/scale"},
		{"GET", "/apis/apps/v1/namespaces/team-a/replicasets/sample-old"},
		{"GET", "/apis/apps/v1/namespaces/team-a/deployments/sample"},
		{"PUT", "/apis/apps/v1/namespaces/team-a/deployments/sample"},
		{"DELETE", "/api/v1/namespaces/team-a/pods/delete-pod"},
		{"PATCH", "/api/v1/nodes/worker-a"},
		{"PATCH", "/api/v1/nodes/worker-b"},
		{"PATCH", "/api/v1/nodes/worker-a"},
		{"GET", "/api/v1/pods"},
		{"POST", "/api/v1/namespaces/team-a/pods/drain-pod/eviction"},
	}
	if len(got) != len(want) {
		t.Fatalf("request count = %d, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		wantQuery := ""
		if want[index].path == "/api/v1/pods" {
			wantQuery = "fieldSelector=spec.nodeName%3Dworker-a&limit=129"
		}
		if got[index].method != want[index].method || got[index].path != want[index].path || got[index].query != wantQuery {
			t.Errorf("request %d = %#v, want %s %s query %q", index, got[index], want[index].method, want[index].path, wantQuery)
		}
	}
	assertScaleRequestBody(t, got[0].body)
	assertRollbackRequestBody(t, got[3].body, rollbackDeploymentTemplate(source.Spec.Template))
	assertDeleteOptions(t, got[4].body, "delete-pod-uid", "21")
	if got[5].contentType != "application/merge-patch+json" || got[5].body != `{"metadata":{"uid":"node-a-uid","resourceVersion":"11"},"spec":{"unschedulable":true}}` {
		t.Errorf("Node patch = %q %q", got[5].contentType, got[5].body)
	}
	if got[6].contentType != "application/merge-patch+json" || got[6].body != `{"metadata":{"uid":"node-b-uid","resourceVersion":"12"},"spec":{"unschedulable":false}}` {
		t.Errorf("Node uncordon patch = %q %q", got[6].contentType, got[6].body)
	}
	assertEvictionBody(t, got[9].body, "drain-pod", "drain-pod-uid", "22")
}

func TestRemediatorVerifiesEveryTypedOperationFromFreshBoundedReads(t *testing.T) {
	tests := []struct {
		name      string
		operation domain.ActionOperation
		target    domain.ResourceRef
		replicas  int64
		revision  int64
		all       bool
		prepare   func(*testing.T, *kubernetesfake.Clientset, domain.RemediationActionPlan)
	}{
		{name: "scale", operation: domain.ActionOperationScaleWorkload, target: remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), replicas: 3, prepare: func(t *testing.T, client *kubernetesfake.Clientset, plan domain.RemediationActionPlan) {
			client.PrependReactor("get", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
				if action.GetSubresource() != "scale" {
					return false, nil, nil
				}
				return true, &autoscalingv1.Scale{ObjectMeta: metav1.ObjectMeta{
					Name: plan.Target.Resource.Name, Namespace: plan.Target.Resource.Namespace, UID: "deployment-uid", ResourceVersion: "12",
				}, Spec: autoscalingv1.ScaleSpec{Replicas: 3}, Status: autoscalingv1.ScaleStatus{Replicas: 3}}, nil
			})
		}},
		{name: "rollback", operation: domain.ActionOperationRollbackDeployment, target: remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), revision: 2, prepare: func(t *testing.T, client *kubernetesfake.Clientset, _ domain.RemediationActionPlan) {
			deployment := remediationDeployment().DeepCopy()
			deployment.ResourceVersion, deployment.Generation = "12", 6
			deployment.Spec.Template = rollbackDeploymentTemplate(remediationRollbackSource().Spec.Template)
			deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: 6, Replicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}
			if _, err := client.AppsV1().Deployments("team-a").Update(context.Background(), deployment, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "Pod delete", operation: domain.ActionOperationDeleteOwnedPod, target: remediationCandidate("v1", "Pod", "team-a", "delete-pod"), prepare: func(t *testing.T, client *kubernetesfake.Clientset, _ domain.RemediationActionPlan) {
			if err := client.CoreV1().Pods("team-a").Delete(context.Background(), "delete-pod", metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "cordon", operation: domain.ActionOperationCordonNode, target: remediationCandidate("v1", "Node", "", "worker-a"), prepare: func(t *testing.T, client *kubernetesfake.Clientset, _ domain.RemediationActionPlan) {
			setNodeSchedulingForVerification(t, client, "worker-a", true)
		}},
		{name: "uncordon", operation: domain.ActionOperationUncordonNode, target: remediationCandidate("v1", "Node", "", "worker-b"), prepare: func(t *testing.T, client *kubernetesfake.Clientset, _ domain.RemediationActionPlan) {
			setNodeSchedulingForVerification(t, client, "worker-b", false)
		}},
		{name: "drain", operation: domain.ActionOperationDrainNode, target: remediationCandidate("v1", "Node", "", "worker-a"), all: true, prepare: func(t *testing.T, client *kubernetesfake.Clientset, _ domain.RemediationActionPlan) {
			setNodeSchedulingForVerification(t, client, "worker-a", true)
			if err := client.CoreV1().Pods("team-a").Delete(context.Background(), "drain-pod", metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := remediationObjects(t)
			if test.operation == domain.ActionOperationDrainNode {
				objects = drainObjects(t)
			}
			remediator, closeClient, fakeClient := newFakeRemediator(t, objects...)
			defer closeClient()
			plan, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
				test.operation, test.target, test.replicas, test.revision, test.all,
			))
			if err != nil {
				t.Fatal(err)
			}
			test.prepare(t, fakeClient, plan)
			fakeClient.ClearActions()
			state, err := remediator.VerifyRemediationAction(context.Background(), plan)
			if err != nil || state != domain.RemediationVerified {
				t.Fatalf("VerifyRemediationAction() = %s/%v actions=%#v", state, err, fakeClient.Actions())
			}
		})
	}
}

func TestRollbackVerificationRequiresDesiredReplicaAvailability(t *testing.T) {
	remediator, closeClient, fakeClient := newFakeRemediator(t, remediationObjects(t)...)
	defer closeClient()
	plan, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
		domain.ActionOperationRollbackDeployment,
		remediationCandidate("apps/v1", "Deployment", "team-a", "sample"), 0, 2, false,
	))
	if err != nil {
		t.Fatal(err)
	}
	deployment := remediationDeployment().DeepCopy()
	deployment.ResourceVersion, deployment.Generation = "12", 6
	deployment.Spec.Template = rollbackDeploymentTemplate(remediationRollbackSource().Spec.Template)
	deployment.Status = appsv1.DeploymentStatus{ObservedGeneration: 6, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1}
	if _, err := fakeClient.AppsV1().Deployments("team-a").Update(context.Background(), deployment, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	fakeClient.ClearActions()
	state, err := remediator.VerifyRemediationAction(context.Background(), plan)
	if err == nil || state != domain.RemediationVerificationFailed {
		t.Fatalf("under-scaled rollback verification = %s/%v", state, err)
	}
}

func TestDrainVerificationRejectsAnyPodAddedAfterApproval(t *testing.T) {
	remediator, closeClient, fakeClient := newFakeRemediator(t, drainObjects(t)...)
	defer closeClient()
	plan, err := remediator.PrepareRemediationAction(context.Background(), remediationRequest(
		domain.ActionOperationDrainNode, remediationCandidate("v1", "Node", "", "worker-a"), 0, 0, true,
	))
	if err != nil {
		t.Fatal(err)
	}
	setNodeSchedulingForVerification(t, fakeClient, "worker-a", true)
	if err := fakeClient.CoreV1().Pods("team-a").Delete(context.Background(), "drain-pod", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	newPod := drainPod().DeepCopy()
	newPod.Name, newPod.UID, newPod.ResourceVersion = "new-after-approval", "new-pod-uid", "1"
	if _, err := fakeClient.CoreV1().Pods("team-a").Create(context.Background(), newPod, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	fakeClient.ClearActions()
	state, err := remediator.VerifyRemediationAction(context.Background(), plan)
	if err == nil || state != domain.RemediationVerificationFailed {
		t.Fatalf("new-Pod drain verification = %s/%v", state, err)
	}
}

func setNodeSchedulingForVerification(t *testing.T, client *kubernetesfake.Clientset, name string, unschedulable bool) {
	t.Helper()
	node, err := client.CoreV1().Nodes().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.ResourceVersion += "-verified"
	node.Spec.Unschedulable = unschedulable
	if _, err := client.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func newFakeRemediator(t *testing.T, objects ...runtime.Object) (*Remediator, func(), *kubernetesfake.Clientset) {
	t.Helper()
	gateway, initialClient, fakeClient := newFakeGateway(t, "team-a", objects...)
	binding, err := NewToolScopeBinding(gateway)
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.InvalidateScope(7); err != nil {
		t.Fatal(err)
	}
	client, err := binding.Create(context.Background(), "selected")
	if err != nil {
		t.Fatal(err)
	}
	remediator, err := NewRemediator(binding, func() time.Time { return time.UnixMilli(2_000).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	return remediator, func() { client.Close(); initialClient.Close() }, fakeClient
}

func remediationRequest(operation domain.ActionOperation, target domain.ResourceRef, replicas, revision int64, all bool) application.RemediationProposalRequest {
	access := domain.NamespaceAccessCurrent
	if all {
		access = domain.NamespaceAccessAll
	}
	return application.RemediationProposalRequest{
		RunID: "00000000-0000-7000-8000-000000000101", SessionID: "00000000-0000-7000-8000-000000000102",
		Scope:            domain.ClusterScope{Context: "selected", Namespace: "team-a", NamespaceAccess: access, Generation: 7, ActivatedAt: time.UnixMilli(1_000).UTC()},
		PolicyGeneration: 3, Operation: operation, Target: target, ReplicaTarget: replicas, Revision: revision,
		ReasonSummary: "Apply one exact supervised typed operation.",
	}
}

func remediationCandidate(apiVersion, kind, namespace, name string) domain.ResourceRef {
	return domain.ResourceRef{APIVersion: apiVersion, Kind: kind, Namespace: namespace, Name: name}
}

func remediationObjects(t *testing.T) []runtime.Object {
	t.Helper()
	return []runtime.Object{
		remediationDeployment(), remediationRollbackSource(), remediationControllerReplicaSet(), deletePod(), drainPod(),
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a", UID: "node-a-uid", ResourceVersion: "11"}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-b", UID: "node-b-uid", ResourceVersion: "12"}, Spec: corev1.NodeSpec{Unschedulable: true}},
		&policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: "sample-pdb", Namespace: "team-a", UID: "pdb-uid", ResourceVersion: "31"}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "drain"}}}, Status: policyv1.PodDisruptionBudgetStatus{DisruptionsAllowed: 1}},
	}
}

func drainObjects(t *testing.T) []runtime.Object {
	t.Helper()
	objects := remediationObjects(t)
	result := make([]runtime.Object, 0, len(objects)-1)
	for _, object := range objects {
		accessor, ok := object.(metav1.Object)
		if ok && accessor.GetName() == "delete-pod" {
			continue
		}
		result = append(result, object)
	}
	return result
}

func remediationDeployment() *appsv1.Deployment {
	replicas := int32(2)
	return &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Name: "sample", Namespace: "team-a", UID: "deployment-uid", ResourceVersion: "11", Generation: 5, Annotations: map[string]string{"deployment.kubernetes.io/revision": "3"}}, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sample"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "sample"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.invalid/sample:new"}}}}}}
}

func remediationRollbackSource() *appsv1.ReplicaSet {
	controller := true
	return &appsv1.ReplicaSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSet"}, ObjectMeta: metav1.ObjectMeta{Name: "sample-old", Namespace: "team-a", UID: "old-rs-uid", ResourceVersion: "8", Labels: map[string]string{"app": "sample"}, Annotations: map[string]string{"deployment.kubernetes.io/revision": "2"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "sample", UID: "deployment-uid", Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sample"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "sample", appsv1.DefaultDeploymentUniqueLabelKey: "controller-owned-hash"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.invalid/sample:old"}}}}}}
}

func remediationControllerReplicaSet() *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "pod-owner", Namespace: "team-a", UID: "pod-owner-uid", ResourceVersion: "18"}}
}

func deletePod() *corev1.Pod {
	controller := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "delete-pod", Namespace: "team-a", UID: "delete-pod-uid", ResourceVersion: "21", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "pod-owner", UID: "pod-owner-uid", Controller: &controller}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
}

func drainPod() *corev1.Pod {
	controller := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "drain-pod", Namespace: "team-a", UID: "drain-pod-uid", ResourceVersion: "22", Labels: map[string]string{"app": "drain"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "drain-owner", UID: "drain-owner-uid", Controller: &controller}}}, Spec: corev1.PodSpec{NodeName: "worker-a", Containers: []corev1.Container{{Name: "app"}}}}
}

func replaceObject(objects []runtime.Object, name string, replacement runtime.Object) []runtime.Object {
	result := make([]runtime.Object, 0, len(objects))
	for _, object := range objects {
		accessor, ok := object.(metav1.Object)
		if ok && accessor.GetName() == name {
			result = append(result, replacement)
		} else {
			result = append(result, object)
		}
	}
	return result
}

func countRemediationMutations(actions []clienttesting.Action) int {
	count := 0
	for _, action := range actions {
		if action.GetResource().Resource == "selfsubjectaccessreviews" {
			continue
		}
		switch action.GetVerb() {
		case "create", "update", "patch", "delete":
			count++
		}
	}
	return count
}

func assertNoRemediationMutation(t *testing.T, actions []clienttesting.Action) {
	t.Helper()
	if count := countRemediationMutations(actions); count != 0 {
		t.Fatalf("Kubernetes mutation calls = %d, want 0: %#v", count, actions)
	}
}

func writeJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode fixture response: %v", err)
	}
}

func assertScaleRequestBody(t *testing.T, body string) {
	t.Helper()
	var scale autoscalingv1.Scale
	if err := json.Unmarshal([]byte(body), &scale); err != nil || scale.Name != "sample" || scale.Namespace != "team-a" ||
		string(scale.UID) != "deployment-uid" || scale.ResourceVersion != "11" || scale.Spec.Replicas != 3 {
		t.Fatalf("scale request body = %q / %#v / %v", body, scale, err)
	}
}

func assertRollbackRequestBody(t *testing.T, body string, want corev1.PodTemplateSpec) {
	t.Helper()
	var deployment appsv1.Deployment
	if err := json.Unmarshal([]byte(body), &deployment); err != nil || deployment.ResourceVersion != "11" || !reflect.DeepEqual(deployment.Spec.Template, want) {
		t.Fatalf("rollback request body = %q / %#v / %v", body, deployment.Spec.Template, err)
	}
	if _, present := deployment.Spec.Template.Labels[appsv1.DefaultDeploymentUniqueLabelKey]; present {
		t.Fatal("rollback request copied the controller-owned Pod-template hash")
	}
}

func assertDeleteOptions(t *testing.T, body, uid, resourceVersion string) {
	t.Helper()
	var options metav1.DeleteOptions
	if err := json.Unmarshal([]byte(body), &options); err != nil || options.GracePeriodSeconds == nil || *options.GracePeriodSeconds != domain.RemediationGracePeriodSeconds || options.Preconditions == nil || options.Preconditions.UID == nil || string(*options.Preconditions.UID) != uid || options.Preconditions.ResourceVersion == nil || *options.Preconditions.ResourceVersion != resourceVersion {
		t.Fatalf("delete options = %q / %#v / %v", body, options, err)
	}
}

func assertEvictionBody(t *testing.T, body, name, uid, resourceVersion string) {
	t.Helper()
	var eviction policyv1.Eviction
	if err := json.Unmarshal([]byte(body), &eviction); err != nil || eviction.Name != name || eviction.DeleteOptions == nil {
		t.Fatalf("eviction = %q / %#v / %v", body, eviction, err)
	}
	encoded, err := json.Marshal(eviction.DeleteOptions)
	if err != nil {
		t.Fatal(err)
	}
	assertDeleteOptions(t, string(encoded), uid, resourceVersion)
}

func TestRemediationAttemptClassifiesCancellationAndUnavailableAsAmbiguous(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "transport", err: errors.New("synthetic transport failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			if errors.Is(test.err, context.DeadlineExceeded) {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.UnixMilli(1))
				defer cancel()
			}
			attempt, _ := failedRemediationAttempt(ctx, test.err, 1)
			if attempt.State != domain.RemediationUnknown || attempt.AttemptedCount != 1 {
				t.Fatalf("ambiguous attempt = %#v", attempt)
			}
		})
	}
}

func TestScaleCancellationDuringRequestIsAmbiguousAndNeverRetried(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodPut || request.URL.EscapedPath() != "/apis/apps/v1/namespaces/team-a/deployments/sample/scale" {
			http.Error(writer, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = io.Copy(io.Discard, request.Body)
		_ = request.Body.Close()
		close(started)
		<-request.Context().Done()
		close(finished)
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, UserAgent: DefaultUserAgent, ContentConfig: rest.ContentConfig{
		ContentType: runtime.ContentTypeJSON, AcceptContentTypes: runtime.ContentTypeJSON,
	}})
	if err != nil {
		t.Fatal(err)
	}
	bundle := &ClientBundle{typed: client}
	plan := domain.RemediationActionPlan{
		Target:     domain.ActionTarget{Resource: remediationReference("apps/v1", "Deployment", "team-a", "sample", "deployment-uid", "11")},
		Parameters: domain.ActionParameters{ReplicaTarget: 3},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		attempt domain.RemediationAttempt
		err     error
	}
	completed := make(chan result, 1)
	go func() {
		attempt, executeErr := executeScale(ctx, bundle, plan)
		completed <- result{attempt: attempt, err: executeErr}
	}()
	<-started
	cancel()
	got := <-completed
	<-finished
	if got.err == nil || got.attempt.State != domain.RemediationUnknown || got.attempt.AttemptedCount != 1 || calls != 1 {
		t.Fatalf("cancelled scale = %#v/%v calls=%d", got.attempt, got.err, calls)
	}
}
