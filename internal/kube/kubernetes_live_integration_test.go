//go:build integration

package kube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	kubeIntegrationAuthorization       = "authorized"
	kubeIntegrationNamespace           = "kupilot-integration-v05"
	kubeIntegrationManagedByLabelKey   = "app.kubernetes.io/managed-by"
	kubeIntegrationManagedByLabelValue = "kupilot-integration-v05"
	kubeIntegrationAppPod              = "kupilot-integration-app"
	kubeIntegrationService             = "kupilot-integration-service"
	kubeIntegrationDeployment          = "kupilot-integration-scale"
	kubeIntegrationDiagnosticPod       = "kupilot-diagnostic-v05"
	kubeIntegrationPolicyGeneration    = domain.PolicyGeneration(1)
	kubeIntegrationOperationalCalls    = 224
	kubeIntegrationTotalCalls          = 256
	kubeIntegrationRemoteAttempts      = 2
	kubeIntegrationSuiteTimeout        = 8 * time.Minute
)

// TestKubernetesIntegration uses one explicitly named disposable local
// Context. All objects have deterministic names and labels, and Namespace
// cleanup is retained even after a failed assertion. No live test is reachable
// from an ordinary go test invocation because this file is integration-tagged.
func TestKubernetesIntegration(t *testing.T) {
	if os.Getenv("KUPILOT_INTEGRATION_KUBE_MUTATION") != kubeIntegrationAuthorization {
		t.Skip("BLOCKED Kubernetes integration: set KUPILOT_INTEGRATION_KUBE_MUTATION=authorized only for a disposable cluster")
	}
	contextName := os.Getenv("KUPILOT_INTEGRATION_KUBE_CONTEXT")
	if contextName == "" {
		t.Skip("BLOCKED Kubernetes integration: KUPILOT_INTEGRATION_KUBE_CONTEXT must name one explicit disposable Context")
	}
	image := os.Getenv("KUPILOT_INTEGRATION_KUBE_IMAGE")
	if !domain.ValidPinnedContainerImage(image) {
		t.Skip("BLOCKED Kubernetes integration: KUPILOT_INTEGRATION_KUBE_IMAGE must be one explicit digest-pinned image with sh, sleep, echo, and nc")
	}

	ctx, cancel := context.WithTimeout(context.Background(), kubeIntegrationSuiteTimeout)
	defer cancel()
	loader := NewConfigLoader()
	current, err := loader.CurrentContext(ctx)
	if err != nil {
		t.Skipf("BLOCKED Kubernetes integration: current Context preflight failed safely: %v", err)
	}
	if current.Name != contextName || !current.Current ||
		!strings.HasPrefix(contextName, "k3d-") && !strings.HasPrefix(contextName, "kind-") {
		t.Skipf("BLOCKED Kubernetes integration: explicit Context %q is not the current disposable k3d/kind Context", contextName)
	}
	factory, err := NewClientFactory(loader, ExecCredentialsAllow, 20*time.Second)
	if err != nil {
		t.Fatalf("FAIL Kubernetes preflight: create client factory: %v", err)
	}
	gateway, err := NewGateway(factory)
	if err != nil {
		t.Fatalf("FAIL Kubernetes preflight: create Gateway: %v", err)
	}
	binding, err := NewToolScopeBinding(gateway)
	if err != nil {
		t.Fatalf("FAIL Kubernetes preflight: create Tool binding: %v", err)
	}
	guard := &kubeIntegrationPolicyGuard{}
	guard.current.Store(true)
	if err := binding.ConfigureResourcePolicyGuard(guard); err != nil {
		t.Fatalf("FAIL Kubernetes preflight: configure policy guard: %v", err)
	}
	if err := binding.InvalidateScope(1); err != nil {
		t.Fatalf("FAIL Kubernetes preflight: bind scope generation: %v", err)
	}
	client, err := binding.Create(ctx, contextName)
	if err != nil {
		t.Skipf("BLOCKED Kubernetes integration: the explicit Context could not create a client safely: %v", err)
	}
	t.Cleanup(client.Close)
	owned, ok := client.(*scopeClient)
	if !ok || owned == nil || owned.bundle == nil || owned.bundle.lifecycle == nil {
		t.Fatal("FAIL Kubernetes preflight: the adapter returned no owned client bundle")
	}
	bundle := owned.bundle
	counter := &kubeIntegrationRoundTripper{
		base:               bundle.lifecycle.base,
		operationalCeiling: kubeIntegrationOperationalCalls,
		totalCeiling:       kubeIntegrationTotalCalls,
	}
	bundle.lifecycle.base = counter

	if blocked := preflightKubernetesRBAC(ctx, bundle); blocked != "" {
		t.Skipf("BLOCKED Kubernetes integration: missing exact RBAC for %s", blocked)
	}
	t.Logf(
		"PREFLIGHT PASS Kubernetes: context=%s namespace=%s image_digest=%s operational_calls<=%d cleanup_reserve=%d remote_attempts<=%d elapsed<=%s cost_usd=0",
		contextName, kubeIntegrationNamespace, imageDigestSuffix(image), kubeIntegrationOperationalCalls,
		kubeIntegrationTotalCalls-kubeIntegrationOperationalCalls, kubeIntegrationRemoteAttempts, kubeIntegrationSuiteTimeout,
	)
	if os.Getenv("KUPILOT_INTEGRATION_PREFLIGHT_ONLY") == "1" {
		t.Log("PREFLIGHT ONLY Kubernetes: no fixture was created and this result is not integration PASS evidence")
		return
	}

	if err := prepareIntegrationNamespace(ctx, bundle, counter); err != nil {
		if errors.Is(err, errKubeIntegrationNamespaceForeign) {
			t.Skipf("BLOCKED Kubernetes integration: %v", err)
		}
		t.Fatalf("FAIL Kubernetes fixture Namespace preparation: %v", err)
	}
	cleanupDone := false
	t.Cleanup(func() {
		if cleanupDone {
			return
		}
		counter.cleanup.Store(true)
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cleanupCancel()
		if cleanupErr := deleteIntegrationNamespace(cleanupCtx, bundle); cleanupErr != nil {
			t.Errorf("FAIL Kubernetes cleanup: %v", cleanupErr)
		}
	})

	if err := createIntegrationFixtures(ctx, bundle, image); err != nil {
		t.Fatalf("FAIL Kubernetes fixture creation: %v", err)
	}
	_, err = waitIntegrationPod(ctx, bundle, kubeIntegrationAppPod, 90*time.Second)
	if err != nil {
		t.Fatalf("FAIL Kubernetes workload readiness: %v", err)
	}

	scope := domain.ClusterScope{
		Context: contextName, Namespace: kubeIntegrationNamespace,
		NamespaceAccess: domain.NamespaceAccessCurrent,
		Generation:      1,
		ActivatedAt:     time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond),
	}
	if scope.Validate() != nil {
		t.Fatal("FAIL Kubernetes fixture generated an invalid frozen scope")
	}

	t.Run("broad read and bounded projection", func(t *testing.T) {
		namespaces, readErr := gateway.ListNamespaces(ctx, client, domain.MaxResourceSummaries)
		if readErr != nil || namespaces.Validate() != nil {
			t.Fatalf("ListNamespaces() = %#v/%v", namespaces, readErr)
		}
		request := toolcontract.ResourceListRequest{
			Scope: scope, Kind: domain.ResourceKindPod,
			Namespace: kubeIntegrationNamespace, Limit: 20,
		}
		observed, readErr := binding.ListResources(ctx, request)
		if readErr != nil || observed.Validate(request) != nil || !resourceListContains(observed, kubeIntegrationAppPod) {
			t.Fatalf("ListResources() = count %d contains fixture %t error %v", len(observed.Items), resourceListContains(observed, kubeIntegrationAppPod), readErr)
		}
	})

	resolved, err := binding.ResolvePod(ctx, toolcontract.PodResolveRequest{
		Scope: scope, Namespace: kubeIntegrationNamespace, PodName: kubeIntegrationAppPod, Container: "app",
	})
	if err != nil {
		t.Fatalf("FAIL Kubernetes target resolution: %v", err)
	}

	t.Run("Pod logs", func(t *testing.T) {
		request := toolcontract.PodLogReadRequest{
			Scope: scope, PolicyGeneration: kubeIntegrationPolicyGeneration,
			Pod: resolved.Reference, Namespace: kubeIntegrationNamespace, PodName: kubeIntegrationAppPod, Container: "app",
			TailLines: 20, SinceSeconds: 3600, LimitBytes: 16 * 1024,
		}
		observed, readErr := binding.ReadPodLog(ctx, request)
		if readErr != nil || observed.Validate(request) != nil || observed.Content.Len() == 0 {
			t.Fatalf("ReadPodLog() = bytes %d availability %q error %v", observed.Content.Len(), observed.Availability, readErr)
		}
	})

	t.Run("metrics", func(t *testing.T) {
		request := toolcontract.MetricReadRequest{
			Scope: scope, PolicyGeneration: kubeIntegrationPolicyGeneration,
			Reference: resolved.Reference, MaxContainers: 8, LimitBytes: 32 * 1024,
		}
		observed, readErr := binding.ReadMetrics(ctx, request)
		if readErr != nil {
			t.Skipf("SKIP Kubernetes metrics: Metrics API did not provide an admitted observation: %v", readErr)
		}
		if observed.Validate(request) != nil {
			t.Fatalf("FAIL Kubernetes metrics projection: %#v", observed)
		}
		if observed.Availability == toolcontract.MetricUnavailable || observed.Availability == toolcontract.MetricUnsupported {
			t.Skipf("SKIP Kubernetes metrics: availability=%s", observed.Availability)
		}
	})

	remoteAttempts := 0
	t.Run("Pod Exec", func(t *testing.T) {
		arguments, argumentsErr := domain.NewActionArguments([]string{"kupilot-integration-exec"})
		if argumentsErr != nil {
			t.Fatal(argumentsErr)
		}
		request := toolcontract.RemoteCommandRequest{
			Scope: scope, PolicyGeneration: kubeIntegrationPolicyGeneration,
			Pod: resolved.Reference, Container: "app", Executable: "/bin/echo", Arguments: arguments,
			Timeout: 10 * time.Second, MaxLines: 10, MaxBytes: 4096,
		}
		remoteAttempts++
		observed, executeErr := binding.ExecuteRemoteCommand(ctx, request)
		if executeErr != nil || observed.Validate(request) != nil || !observed.Completed || observed.ExitCode != 0 {
			t.Fatalf("ExecuteRemoteCommand() = completed %t exit %d bytes %d error %v", observed.Completed, observed.ExitCode, observed.ByteCount, executeErr)
		}
	})

	t.Run("Pod Exec cancellation", func(t *testing.T) {
		arguments, argumentsErr := domain.NewActionArguments([]string{"30"})
		if argumentsErr != nil {
			t.Fatal(argumentsErr)
		}
		request := toolcontract.RemoteCommandRequest{
			Scope: scope, PolicyGeneration: kubeIntegrationPolicyGeneration,
			Pod: resolved.Reference, Container: "app", Executable: "/bin/sleep", Arguments: arguments,
			Timeout: 40 * time.Second, MaxLines: 10, MaxBytes: 4096,
		}
		requestCtx, requestCancel := context.WithCancel(ctx)
		result := make(chan error, 1)
		remoteAttempts++
		go func() {
			_, executeErr := binding.ExecuteRemoteCommand(requestCtx, request)
			result <- executeErr
		}()
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			requestCancel()
			t.Fatal("Kubernetes integration suite ended before cancellation")
		case <-timer.C:
			requestCancel()
		}
		if executeErr := <-result; safeErrorClass(executeErr) != domain.SafeErrorClassCancelled {
			t.Fatalf("cancelled ExecuteRemoteCommand() error class = %q, error %v", safeErrorClass(executeErr), executeErr)
		}
	})

	t.Run("namespace permission denial makes zero Kubernetes calls", func(t *testing.T) {
		before := counter.calls.Load()
		_, readErr := binding.ReadResource(ctx, toolcontract.ResourceReadRequest{
			Scope: scope,
			Reference: domain.ResourceRef{
				APIVersion: "v1", Kind: "Pod", Namespace: "outside-integration-scope", Name: kubeIntegrationAppPod,
			},
			Detail: toolcontract.ResourceDetailSummary,
		})
		if safeErrorClass(readErr) != domain.SafeErrorClassPolicyDenied || counter.calls.Load() != before {
			t.Fatalf("policy denial error/calls = %v/%d->%d", readErr, before, counter.calls.Load())
		}
	})

	t.Run("diagnostic Pod cleanup", func(t *testing.T) {
		service, resolveErr := binding.ResolveService(ctx, toolcontract.ServiceResolveRequest{
			Scope: scope, Namespace: kubeIntegrationNamespace, ServiceName: kubeIntegrationService, Port: 8080,
		})
		if resolveErr != nil {
			t.Fatalf("ResolveService() error = %v", resolveErr)
		}
		host := kubeIntegrationService + "." + kubeIntegrationNamespace + ".svc"
		arguments, argumentsErr := domain.NewActionArguments([]string{"-z", "-v", "-w", "5", host, "8080"})
		if argumentsErr != nil {
			t.Fatal(argumentsErr)
		}
		request := toolcontract.DiagnosticPodRequest{
			Scope: scope, PolicyGeneration: kubeIntegrationPolicyGeneration,
			Name: kubeIntegrationDiagnosticPod, Service: service.Reference, Image: image,
			Executable: "/bin/nc", Arguments: arguments, TargetHost: host, TargetPort: 8080,
			Timeout: 12 * time.Second, MaxLines: 20, MaxBytes: 8192,
		}
		dryRunPod, dryRunErr := bundle.typed.CoreV1().Pods(kubeIntegrationNamespace).Create(
			ctx,
			diagnosticPodFor(request),
			metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}, FieldManager: "kupilot"},
		)
		if dryRunErr != nil {
			t.Fatalf("exact diagnostic Pod server dry-run was rejected with reason %q", apierrors.ReasonForError(dryRunErr))
		}
		if !diagnosticPodMatchesRequest(dryRunPod, request) {
			t.Fatal("exact diagnostic Pod server defaults changed the admitted identity")
		}
		observed, runErr := binding.RunDiagnosticPod(ctx, request)
		if runErr != nil || observed.Validate(request) != nil || observed.CleanupState != domain.DiagnosticPodCleanupVerified {
			t.Fatalf("RunDiagnosticPod() = cleanup %q lifecycle %#v error %v", observed.CleanupState, observed.Lifecycle, runErr)
		}
		if _, getErr := bundle.typed.CoreV1().Pods(kubeIntegrationNamespace).Get(ctx, kubeIntegrationDiagnosticPod, metav1.GetOptions{}); !apierrors.IsNotFound(getErr) {
			t.Fatalf("diagnostic Pod remained after verified cleanup: %v", getErr)
		}
	})

	t.Run("typed scale remediation", func(t *testing.T) {
		remediator, createErr := NewRemediator(binding, func() time.Time {
			return time.Now().UTC().Truncate(time.Millisecond)
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		proposal := application.RemediationProposalRequest{
			RunID: "00000000-0000-7000-8000-000000009701", SessionID: "00000000-0000-7000-8000-000000009702",
			Scope: scope, PolicyGeneration: kubeIntegrationPolicyGeneration,
			Operation: domain.ActionOperationScaleWorkload,
			Target: domain.ResourceRef{
				APIVersion: "apps/v1", Kind: "Deployment", Namespace: kubeIntegrationNamespace, Name: kubeIntegrationDeployment,
			},
			ReplicaTarget: 2, ReasonSummary: "Scale the synthetic integration Deployment from one replica to two.",
		}
		plan, prepareErr := remediator.PrepareRemediationAction(ctx, proposal)
		if prepareErr != nil || plan.Validate() != nil || plan.Risk() != domain.RiskReview {
			t.Fatalf("PrepareRemediationAction() = risk %q plan-valid %t error %v", plan.Risk(), plan.Validate() == nil, prepareErr)
		}
		if revalidateErr := remediator.RevalidateRemediationAction(ctx, plan); revalidateErr != nil {
			t.Fatalf("RevalidateRemediationAction() error = %v", revalidateErr)
		}
		attempt, executeErr := remediator.ExecuteRemediationAction(ctx, plan)
		if executeErr != nil || attempt.State != domain.RemediationAccepted || attempt.AttemptedCount != 1 {
			t.Fatalf("ExecuteRemediationAction() = %#v/%v", attempt, executeErr)
		}
		if waitErr := waitIntegrationDeployment(ctx, bundle, 2, 90*time.Second); waitErr != nil {
			t.Fatalf("scaled Deployment readiness: %v", waitErr)
		}
		state, verifyErr := remediator.VerifyRemediationAction(ctx, plan)
		if verifyErr != nil || state != domain.RemediationVerified {
			t.Fatalf("VerifyRemediationAction() = %q/%v", state, verifyErr)
		}
	})

	if remoteAttempts != kubeIntegrationRemoteAttempts {
		t.Fatalf("FAIL Kubernetes remote attempts = %d, want %d", remoteAttempts, kubeIntegrationRemoteAttempts)
	}
	if calls := counter.calls.Load(); calls > kubeIntegrationOperationalCalls {
		t.Fatalf("FAIL Kubernetes operational calls = %d, ceiling %d", calls, kubeIntegrationOperationalCalls)
	}
	counter.cleanup.Store(true)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 90*time.Second)
	err = deleteIntegrationNamespace(cleanupCtx, bundle)
	cleanupCancel()
	if err != nil {
		t.Fatalf("FAIL Kubernetes final cleanup: %v", err)
	}
	cleanupDone = true
	if calls := counter.calls.Load(); calls > kubeIntegrationTotalCalls {
		t.Fatalf("FAIL Kubernetes total calls including cleanup = %d, ceiling %d", calls, kubeIntegrationTotalCalls)
	}
	if t.Failed() {
		t.Logf("FAIL Kubernetes: namespace cleanup completed after one or more required subtests failed; total_calls=%d", counter.calls.Load())
		return
	}
	t.Logf(
		"PASS Kubernetes: remote_attempts=%d broad_read=PASS logs=PASS Pod_Exec=PASS cancellation=PASS permission_denial=PASS diagnostic_cleanup=PASS typed_scale=PASS metrics=see_subtest",
		remoteAttempts,
	)
	t.Logf("PASS Kubernetes cleanup: namespace_deleted=true total_calls=%d ceiling=%d", counter.calls.Load(), kubeIntegrationTotalCalls)
}

var errKubeIntegrationNamespaceForeign = errors.New("the deterministic integration Namespace exists without the suite ownership label")

type kubeIntegrationPolicyGuard struct {
	current atomic.Bool
}

func (guard *kubeIntegrationPolicyGuard) CurrentPolicyGeneration(ctx context.Context, generation domain.PolicyGeneration) bool {
	return guard != nil && ctx != nil && generation == kubeIntegrationPolicyGeneration && guard.current.Load()
}

type kubeIntegrationRoundTripper struct {
	base               http.RoundTripper
	operationalCeiling int64
	totalCeiling       int64
	calls              atomic.Int64
	cleanup            atomic.Bool
}

func (transport *kubeIntegrationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.base == nil || request == nil {
		return nil, errors.New("Kubernetes integration transport is invalid")
	}
	calls := transport.calls.Add(1)
	ceiling := transport.operationalCeiling
	if transport.cleanup.Load() {
		ceiling = transport.totalCeiling
	}
	if calls > ceiling {
		return nil, fmt.Errorf("Kubernetes integration request ceiling %d reached", ceiling)
	}
	return transport.base.RoundTrip(request)
}

func preflightKubernetesRBAC(ctx context.Context, bundle *ClientBundle) string {
	checks := []authorizationv1.ResourceAttributes{
		{Version: "v1", Resource: "namespaces", Verb: "get"},
		{Version: "v1", Resource: "namespaces", Verb: "list"},
		{Version: "v1", Resource: "namespaces", Verb: "create"},
		{Version: "v1", Resource: "namespaces", Verb: "delete"},
		{Version: "v1", Resource: "pods", Verb: "get", Namespace: kubeIntegrationNamespace},
		{Version: "v1", Resource: "pods", Verb: "list", Namespace: kubeIntegrationNamespace},
		{Version: "v1", Resource: "pods", Verb: "create", Namespace: kubeIntegrationNamespace},
		{Version: "v1", Resource: "pods", Verb: "delete", Namespace: kubeIntegrationNamespace},
		{Version: "v1", Resource: "pods", Subresource: "log", Verb: "get", Namespace: kubeIntegrationNamespace},
		{Version: "v1", Resource: "pods", Subresource: "exec", Verb: "create", Namespace: kubeIntegrationNamespace},
		{Version: "v1", Resource: "services", Verb: "get", Namespace: kubeIntegrationNamespace},
		{Version: "v1", Resource: "services", Verb: "create", Namespace: kubeIntegrationNamespace},
		{Group: "apps", Version: "v1", Resource: "deployments", Verb: "get", Namespace: kubeIntegrationNamespace},
		{Group: "apps", Version: "v1", Resource: "deployments", Verb: "create", Namespace: kubeIntegrationNamespace},
		{Group: "apps", Version: "v1", Resource: "deployments", Subresource: "scale", Verb: "update", Namespace: kubeIntegrationNamespace},
	}
	for _, attributes := range checks {
		review, err := bundle.typed.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{
			Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &attributes},
		}, metav1.CreateOptions{})
		if err != nil || review == nil || !review.Status.Allowed || review.Status.Denied {
			group := attributes.Group
			if group == "" {
				group = "core"
			}
			return strings.Join([]string{attributes.Verb, group, attributes.Resource, attributes.Subresource}, "/")
		}
	}
	return ""
}

func prepareIntegrationNamespace(ctx context.Context, bundle *ClientBundle, counter *kubeIntegrationRoundTripper) error {
	namespace, err := bundle.typed.CoreV1().Namespaces().Get(ctx, kubeIntegrationNamespace, metav1.GetOptions{})
	if err == nil {
		if namespace.Labels[kubeIntegrationManagedByLabelKey] != kubeIntegrationManagedByLabelValue {
			return errKubeIntegrationNamespaceForeign
		}
		counter.cleanup.Store(true)
		if deleteErr := deleteIntegrationNamespace(ctx, bundle); deleteErr != nil {
			return deleteErr
		}
		counter.cleanup.Store(false)
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	_, err = bundle.typed.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: kubeIntegrationNamespace,
			Labels: map[string]string{
				kubeIntegrationManagedByLabelKey: kubeIntegrationManagedByLabelValue,
			},
		},
	}, metav1.CreateOptions{FieldManager: "kupilot-integration"})
	return err
}

func createIntegrationFixtures(ctx context.Context, bundle *ClientBundle, image string) error {
	labels := map[string]string{
		kubeIntegrationManagedByLabelKey: kubeIntegrationManagedByLabelValue,
		"app.kubernetes.io/name":         "kupilot-integration",
	}
	falseValue := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: kubeIntegrationAppPod, Namespace: kubeIntegrationNamespace, Labels: labels},
		Spec: corev1.PodSpec{
			AutomountServiceAccountToken: &falseValue,
			RestartPolicy:                corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name: "app", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
				Command: []string{"/bin/sh", "-c", "printf 'kupilot-integration-log\\n'; exec /bin/sleep 600"},
			}},
		},
	}
	if _, err := bundle.typed.CoreV1().Pods(kubeIntegrationNamespace).Create(ctx, pod, metav1.CreateOptions{FieldManager: "kupilot-integration"}); err != nil {
		return err
	}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: kubeIntegrationService, Namespace: kubeIntegrationNamespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports:    []corev1.ServicePort{{Name: "synthetic", Port: 8080, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if _, err := bundle.typed.CoreV1().Services(kubeIntegrationNamespace).Create(ctx, service, metav1.CreateOptions{FieldManager: "kupilot-integration"}); err != nil {
		return err
	}
	replicas := int32(1)
	deploymentLabels := map[string]string{
		kubeIntegrationManagedByLabelKey: kubeIntegrationManagedByLabelValue,
		"app.kubernetes.io/name":         "kupilot-integration-scale",
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: kubeIntegrationDeployment, Namespace: kubeIntegrationNamespace, Labels: deploymentLabels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: deploymentLabels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: deploymentLabels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &falseValue,
					Containers: []corev1.Container{{
						Name: "app", Image: image, ImagePullPolicy: corev1.PullIfNotPresent,
						Command: []string{"/bin/sleep", "600"},
					}},
				},
			},
		},
	}
	_, err := bundle.typed.AppsV1().Deployments(kubeIntegrationNamespace).Create(ctx, deployment, metav1.CreateOptions{FieldManager: "kupilot-integration"})
	return err
}

func waitIntegrationPod(ctx context.Context, bundle *ClientBundle, name string, timeout time.Duration) (*corev1.Pod, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		pod, err := bundle.typed.CoreV1().Pods(kubeIntegrationNamespace).Get(waitCtx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if pod.Status.Phase == corev1.PodRunning && len(pod.Status.ContainerStatuses) == 1 && pod.Status.ContainerStatuses[0].Ready {
			return pod, nil
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return nil, fmt.Errorf("fixture Pod entered terminal phase %s", pod.Status.Phase)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, waitCtx.Err()
		case <-timer.C:
		}
	}
}

func waitIntegrationDeployment(ctx context.Context, bundle *ClientBundle, replicas int32, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		deployment, err := bundle.typed.AppsV1().Deployments(kubeIntegrationNamespace).Get(waitCtx, kubeIntegrationDeployment, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if deployment.Status.ObservedGeneration >= deployment.Generation && deployment.Status.Replicas == replicas &&
			deployment.Status.UpdatedReplicas == replicas && deployment.Status.AvailableReplicas == replicas && deployment.Status.UnavailableReplicas == 0 {
			return nil
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return waitCtx.Err()
		case <-timer.C:
		}
	}
}

func deleteIntegrationNamespace(ctx context.Context, bundle *ClientBundle) error {
	err := bundle.typed.CoreV1().Namespaces().Delete(ctx, kubeIntegrationNamespace, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	for {
		_, err = bundle.typed.CoreV1().Namespaces().Get(ctx, kubeIntegrationNamespace, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func resourceListContains(list toolcontract.ResourceObservationList, name string) bool {
	for _, item := range list.Items {
		if item.Summary.Reference.Name == name && item.Summary.Reference.Namespace == kubeIntegrationNamespace {
			return true
		}
	}
	return false
}

func safeErrorClass(err error) domain.SafeErrorClass {
	var classified interface{ Class() domain.SafeErrorClass }
	if errors.As(err, &classified) {
		return classified.Class()
	}
	return ""
}

func imageDigestSuffix(image string) string {
	_, digest, found := strings.Cut(image, "@sha256:")
	if !found || len(digest) < 12 {
		return "invalid"
	}
	return digest[:12]
}

var _ http.RoundTripper = (*kubeIntegrationRoundTripper)(nil)
