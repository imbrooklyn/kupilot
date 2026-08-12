package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clienttesting "k8s.io/client-go/testing"
)

func TestDeploymentRestarterUsesFreshResourceVersionAndFixedPatch(t *testing.T) {
	now := time.Date(2026, time.August, 12, 9, 10, 11, 123_456_789, time.FixedZone("fixture", 8*60*60))
	wantRestartedAt := time.Date(2026, time.August, 12, 1, 10, 11, 123_000_000, time.UTC)
	deployment := restartTestDeployment("17")
	deployment.Spec.Template.Annotations = map[string]string{
		"example.invalid/preserved": "safe-value",
		"kupilot.io/restartedAt":    "2026-08-12T08:00:00Z",
	}
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, now, deployment)
	defer closeClient()
	fakeClient.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		response := deployment.DeepCopy()
		response.ResourceVersion = "18"
		response.Generation = 12
		return true, response, nil
	})
	approved := deployment.DeepCopy()
	approved.ResourceVersion = "16"
	intent := restartTestIntent(t, approved)

	observation, err := restarter.RevalidateApprovedRestart(context.Background(), intent)
	if err != nil {
		t.Fatalf("RevalidateApprovedRestart() error = %v", err)
	}
	execution, err := approval.NewRestartDeploymentExecution(intent, observation)
	if err != nil {
		t.Fatalf("NewRestartDeploymentExecution() error = %v", err)
	}
	result, err := restarter.ExecuteApprovedRestart(context.Background(), execution)
	if err != nil {
		t.Fatalf("ExecuteApprovedRestart() error = %v", err)
	}
	if result.Validate() != nil || result.RestartedAt != wantRestartedAt || result.PreviousResourceVersion != "17" ||
		result.TargetGeneration != 12 || result.TargetReplicas != 1 {
		t.Fatalf("restart result = %#v", result)
	}

	actions := fakeClient.Actions()
	if len(actions) != 2 {
		t.Fatalf("Kubernetes actions = %d, want GET then PATCH", len(actions))
	}
	assertRestartAction(t, actions[0], "get")
	assertRestartAction(t, actions[1], "patch")
	patchAction, ok := actions[1].(clienttesting.PatchAction)
	if !ok {
		t.Fatalf("patch action type = %T", actions[1])
	}
	if patchAction.GetPatchType() != types.MergePatchType {
		t.Fatalf("patch type = %q, want %q", patchAction.GetPatchType(), types.MergePatchType)
	}
	const wantPatch = `{"metadata":{"resourceVersion":"17"},"spec":{"template":{"metadata":{"annotations":{"kupilot.io/restartedAt":"2026-08-12T01:10:11.123Z"}}}}}`
	if got := string(patchAction.GetPatch()); got != wantPatch {
		t.Fatalf("patch = %s, want %s", got, wantPatch)
	}
	assertFixedRestartPatchShape(t, patchAction.GetPatch())
}

func TestDeploymentRestarterRejectsInvalidSuccessfulPatchProjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*appsv1.Deployment)
	}{
		{name: "unchanged resource version", mutate: func(value *appsv1.Deployment) { value.ResourceVersion = "17" }},
		{name: "unchanged generation", mutate: func(value *appsv1.Deployment) { value.Generation = 11 }},
		{name: "skipped generation", mutate: func(value *appsv1.Deployment) { value.Generation = 13 }},
		{name: "negative target replicas", mutate: func(value *appsv1.Deployment) {
			replicas := int32(-1)
			value.Spec.Replicas = &replicas
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deployment := restartTestDeployment("17")
			restarter, closeClient, fakeClient := newFakeDeploymentRestarter(
				t, time.Date(2026, time.August, 12, 9, 10, 11, 0, time.UTC), deployment,
			)
			defer closeClient()
			fakeClient.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
				response := deployment.DeepCopy()
				response.ResourceVersion = "18"
				response.Generation = 12
				test.mutate(response)
				return true, response, nil
			})
			intent := restartTestIntent(t, deployment)
			observation, err := restarter.RevalidateApprovedRestart(context.Background(), intent)
			if err != nil {
				t.Fatalf("RevalidateApprovedRestart() error = %v", err)
			}
			execution, err := approval.NewRestartDeploymentExecution(intent, observation)
			if err != nil {
				t.Fatalf("NewRestartDeploymentExecution() error = %v", err)
			}
			_, err = restarter.ExecuteApprovedRestart(context.Background(), execution)
			assertRestartSafeClass(t, err, domain.SafeErrorClassInvalidExternalResponse)
			if gets, writes := countRestartActions(fakeClient.Actions(), "get"), countRestartActions(fakeClient.Actions(), "patch"); gets != 1 || writes != 1 {
				t.Fatalf("GET/PATCH actions = %d/%d, want 1/1", gets, writes)
			}
		})
	}
}

func TestDeploymentRestarterSendsOnlyTheFixedWirePatch(t *testing.T) {
	now := time.Date(2026, time.August, 12, 9, 10, 11, 123_000_000, time.UTC)
	deployment := restartTestDeployment("17")
	deployment.TypeMeta = metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}
	requests := make(chan restartHTTPRequest, 4)
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
		response := deployment.DeepCopy()
		if request.Method == http.MethodPatch {
			response.ResourceVersion = "18"
			response.Generation = 12
		}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(response); err != nil {
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
	restarter, err := NewDeploymentRestarter(binding, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewDeploymentRestarter() error = %v", err)
	}
	intent := restartTestIntent(t, deployment)
	observation, err := restarter.RevalidateApprovedRestart(context.Background(), intent)
	if err != nil {
		t.Fatalf("RevalidateApprovedRestart() error = %v", err)
	}
	execution, err := approval.NewRestartDeploymentExecution(intent, observation)
	if err != nil {
		t.Fatalf("NewRestartDeploymentExecution() error = %v", err)
	}
	if _, err := restarter.ExecuteApprovedRestart(context.Background(), execution); err != nil {
		t.Fatalf("ExecuteApprovedRestart() error = %v", err)
	}

	const resourcePath = "/apis/apps/v1/namespaces/team-a/deployments/sample-deployment"
	get := <-requests
	if get.method != http.MethodGet || get.path != resourcePath || get.query != "timeout=10s" || get.body != "" {
		t.Fatalf("fresh GET request = %#v", get)
	}
	patch := <-requests
	const wantPatch = `{"metadata":{"resourceVersion":"17"},"spec":{"template":{"metadata":{"annotations":{"kupilot.io/restartedAt":"2026-08-12T09:10:11.123Z"}}}}}`
	if patch.method != http.MethodPatch || patch.path != resourcePath || patch.query != "timeout=10s" ||
		patch.contentType != string(types.MergePatchType) || patch.body != wantPatch {
		t.Fatalf("PATCH request = %#v, want exact fixed merge patch", patch)
	}
	select {
	case extra := <-requests:
		t.Fatalf("unexpected extra Kubernetes request = %#v", extra)
	default:
	}
}

func TestDeploymentRestarterRejectsChangedTargetBeforePatch(t *testing.T) {
	now := time.Date(2026, time.August, 12, 9, 10, 11, 0, time.UTC)
	approved := restartTestDeployment("16")
	intent := restartTestIntent(t, approved)
	tests := []struct {
		name   string
		mutate func(*appsv1.Deployment)
	}{
		{name: "uid", mutate: func(value *appsv1.Deployment) { value.UID = types.UID("changed-uid") }},
		{name: "template", mutate: func(value *appsv1.Deployment) {
			value.Spec.Template.Spec.Containers[0].Image = "registry.invalid/sample:v2"
		}},
		{name: "restart annotation", mutate: func(value *appsv1.Deployment) {
			value.Spec.Template.Annotations = map[string]string{"kupilot.io/restartedAt": "2026-08-12T08:00:00Z"}
		}},
		{name: "generation", mutate: func(value *appsv1.Deployment) { value.Generation++ }},
		{name: "namespace", mutate: func(value *appsv1.Deployment) { value.Namespace = "other-namespace" }},
		{name: "name", mutate: func(value *appsv1.Deployment) { value.Name = "other-deployment" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fresh := approved.DeepCopy()
			fresh.ResourceVersion = "17"
			test.mutate(fresh)
			restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, now, approved)
			defer closeClient()
			fakeClient.PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, fresh, nil
			})
			if _, err := restarter.RevalidateApprovedRestart(context.Background(), intent); err == nil {
				t.Fatal("RevalidateApprovedRestart(changed target) error = nil")
			}
			if writes := countRestartActions(fakeClient.Actions(), "patch"); writes != 0 {
				t.Fatalf("PATCH actions = %d, want 0", writes)
			}
		})
	}
}

func TestDeploymentRestarterRejectsScopeChangeBeforePatch(t *testing.T) {
	now := time.Date(2026, time.August, 12, 9, 10, 11, 0, time.UTC)
	deployment := restartTestDeployment("17")
	restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, now, deployment)
	defer closeClient()
	intent := restartTestIntent(t, deployment)
	wrongContext := intent
	wrongContext.Scope.Context = "other-context"
	_, err := restarter.RevalidateApprovedRestart(context.Background(), wrongContext)
	assertRestartSafeClass(t, err, domain.SafeErrorClassStaleScope)
	if actions := fakeClient.Actions(); len(actions) != 0 {
		t.Fatalf("wrong Context Kubernetes actions = %d, want 0", len(actions))
	}
	observation, err := restarter.RevalidateApprovedRestart(context.Background(), intent)
	if err != nil {
		t.Fatalf("RevalidateApprovedRestart() error = %v", err)
	}
	execution, err := approval.NewRestartDeploymentExecution(intent, observation)
	if err != nil {
		t.Fatalf("NewRestartDeploymentExecution() error = %v", err)
	}
	if err := restarter.binding.InvalidateScope(8); err != nil {
		t.Fatalf("InvalidateScope() error = %v", err)
	}
	_, err = restarter.ExecuteApprovedRestart(context.Background(), execution)
	assertRestartSafeClass(t, err, domain.SafeErrorClassStaleScope)
	if gets, writes := countRestartActions(fakeClient.Actions(), "get"), countRestartActions(fakeClient.Actions(), "patch"); gets != 1 || writes != 0 {
		t.Fatalf("GET/PATCH actions = %d/%d, want 1/0", gets, writes)
	}
}

func TestDeploymentRestarterHonorsCancellationAndTimeoutBeforeKubernetesIO(t *testing.T) {
	tests := []struct {
		name      string
		context   func() (context.Context, context.CancelFunc)
		wantClass domain.SafeErrorClass
	}{
		{name: "cancelled", context: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		}, wantClass: domain.SafeErrorClassCancelled},
		{name: "deadline", context: func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Unix(0, 0))
		}, wantClass: domain.SafeErrorClassTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deployment := restartTestDeployment("17")
			restarter, closeClient, fakeClient := newFakeDeploymentRestarter(
				t, time.Date(2026, time.August, 12, 9, 10, 11, 0, time.UTC), deployment,
			)
			defer closeClient()
			ctx, cancel := test.context()
			defer cancel()
			_, err := restarter.RevalidateApprovedRestart(ctx, restartTestIntent(t, deployment))
			assertRestartSafeClass(t, err, test.wantClass)
			if actions := fakeClient.Actions(); len(actions) != 0 {
				t.Fatalf("Kubernetes actions = %d, want 0", len(actions))
			}
		})
	}
}

func TestDeploymentRestarterHandlesForbiddenAndConflictWithoutRetry(t *testing.T) {
	now := time.Date(2026, time.August, 12, 9, 10, 11, 0, time.UTC)
	tests := []struct {
		name       string
		verb       string
		failure    error
		wantClass  domain.SafeErrorClass
		wantGets   int
		wantWrites int
	}{
		{
			name: "fresh get forbidden", verb: "get",
			failure:   apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "sample-deployment", errors.New("synthetic denial")),
			wantClass: domain.SafeErrorClassPermissionDenied, wantGets: 1,
		},
		{
			name: "patch forbidden", verb: "patch",
			failure:   apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "sample-deployment", errors.New("synthetic denial")),
			wantClass: domain.SafeErrorClassPermissionDenied, wantGets: 1, wantWrites: 1,
		},
		{
			name: "patch conflict", verb: "patch",
			failure:   apierrors.NewConflict(schema.GroupResource{Group: "apps", Resource: "deployments"}, "sample-deployment", errors.New("synthetic conflict")),
			wantClass: domain.SafeErrorClassConflict, wantGets: 1, wantWrites: 1,
		},
		{
			name: "patch timeout", verb: "patch", failure: context.DeadlineExceeded,
			wantClass: domain.SafeErrorClassTimeout, wantGets: 1, wantWrites: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deployment := restartTestDeployment("17")
			restarter, closeClient, fakeClient := newFakeDeploymentRestarter(t, now, deployment)
			defer closeClient()
			fakeClient.PrependReactor(test.verb, "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, test.failure
			})
			intent := restartTestIntent(t, deployment)
			observation, err := restarter.RevalidateApprovedRestart(context.Background(), intent)
			if test.verb == "get" {
				assertRestartSafeClass(t, err, test.wantClass)
			} else {
				if err != nil {
					t.Fatalf("RevalidateApprovedRestart() error = %v", err)
				}
				execution, createErr := approval.NewRestartDeploymentExecution(intent, observation)
				if createErr != nil {
					t.Fatalf("NewRestartDeploymentExecution() error = %v", createErr)
				}
				_, err = restarter.ExecuteApprovedRestart(context.Background(), execution)
				assertRestartSafeClass(t, err, test.wantClass)
			}
			if gets := countRestartActions(fakeClient.Actions(), "get"); gets != test.wantGets {
				t.Fatalf("GET actions = %d, want %d", gets, test.wantGets)
			}
			if writes := countRestartActions(fakeClient.Actions(), "patch"); writes != test.wantWrites {
				t.Fatalf("PATCH actions = %d, want %d", writes, test.wantWrites)
			}
		})
	}
}

func TestRestartExecutionContractContainsNoCallerSelectedWritePayload(t *testing.T) {
	typeOf := reflect.TypeOf(approval.RestartDeploymentExecution{})
	for index := 0; index < typeOf.NumField(); index++ {
		field := typeOf.Field(index)
		if field.IsExported() {
			t.Fatalf("RestartDeploymentExecution exposes field %s", field.Name)
		}
		for _, forbidden := range []string{"Patch", "YAML", "Annotation", "Timestamp", "Time", "ResourceVersion"} {
			if field.Name == forbidden {
				t.Fatalf("RestartDeploymentExecution exposes caller-selected %s", field.Name)
			}
		}
	}
}

type restartHTTPRequest struct {
	method      string
	path        string
	query       string
	contentType string
	body        string
}

func newFakeDeploymentRestarter(
	t *testing.T,
	now time.Time,
	objects ...runtime.Object,
) (*DeploymentRestarter, func(), *clienttesting.Fake) {
	t.Helper()
	base, initialClient, fakeClient := newFakeGateway(t, "team-a", objects...)
	binding, err := NewToolScopeBinding(base)
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
	restarter, err := NewDeploymentRestarter(binding, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewDeploymentRestarter() error = %v", err)
	}
	closeClient := func() {
		client.Close()
		initialClient.Close()
	}
	return restarter, closeClient, &fakeClient.Fake
}

func restartTestDeployment(resourceVersion string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sample-deployment", Namespace: "team-a", UID: types.UID("deployment-uid"),
			ResourceVersion: resourceVersion, Generation: 11,
		},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "sample"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "sample"}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "registry.invalid/sample:v1"}}},
			},
		},
	}
}

func restartTestIntent(t *testing.T, deployment *appsv1.Deployment) domain.OperationIntent {
	t.Helper()
	fingerprint, err := deploymentTemplateFingerprint(deployment.Spec.Template)
	if err != nil {
		t.Fatalf("deploymentTemplateFingerprint() error = %v", err)
	}
	return domain.OperationIntent{
		Operation:      domain.ApprovalOperationRestartDeployment,
		Scope:          domain.ScopeSnapshot{Context: "selected", Namespace: "team-a", Generation: 7},
		DeploymentName: deployment.Name, DeploymentUID: string(deployment.UID),
		TemplateFingerprint: fingerprint, DeploymentGeneration: deployment.Generation,
		PolicyVersion: domain.RestartDeploymentApprovalPolicyVersion,
		ReasonSummary: "Restart after the bounded diagnosis.",
	}
}

func assertRestartAction(t *testing.T, action clienttesting.Action, verb string) {
	t.Helper()
	if action.GetVerb() != verb || action.GetResource().Group != "apps" || action.GetResource().Version != "v1" ||
		action.GetResource().Resource != "deployments" || action.GetNamespace() != "team-a" {
		t.Fatalf("action = %#v, want %s apps/v1 deployments in team-a", action, verb)
	}
}

func countRestartActions(actions []clienttesting.Action, verb string) int {
	count := 0
	for _, action := range actions {
		if action.GetVerb() == verb && action.GetResource().Group == "apps" && action.GetResource().Resource == "deployments" {
			count++
		}
	}
	return count
}

func assertRestartSafeClass(t *testing.T, err error, want domain.SafeErrorClass) {
	t.Helper()
	classified, ok := err.(interface{ Class() domain.SafeErrorClass })
	if !ok || classified.Class() != want {
		t.Fatalf("error = %v (%T), want class %q", err, err, want)
	}
}

func assertFixedRestartPatchShape(t *testing.T, patch []byte) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(patch, &top); err != nil {
		t.Fatalf("json.Unmarshal(patch) error = %v", err)
	}
	if !reflect.DeepEqual(sortedRawKeys(top), []string{"metadata", "spec"}) {
		t.Fatalf("patch top-level keys = %v", sortedRawKeys(top))
	}
}

func sortedRawKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	if len(keys) == 2 && keys[0] > keys[1] {
		keys[0], keys[1] = keys[1], keys[0]
	}
	return keys
}
