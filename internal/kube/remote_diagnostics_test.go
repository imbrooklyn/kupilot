package kube

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/httpstream"
	serverstream "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	remotecommandconsts "k8s.io/apimachinery/pkg/util/remotecommand"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	clientscheme "k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
)

func TestRemoteTargetResolutionUsesExactGetsAndProjectsSensitiveMountRoots(t *testing.T) {
	canary := strings.Repeat("secret-source-canary", 3)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sample-pod", Namespace: "team-a", UID: "pod-uid", ResourceVersion: "20"}, Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "app", VolumeMounts: []corev1.VolumeMount{{Name: "secret", MountPath: "/var/app/credentials"}, {Name: "config", MountPath: "/var/app/config"}, {Name: "token", MountPath: "/var/run/secrets/kubernetes.io/serviceaccount"}}}},
		Volumes: []corev1.Volume{
			{Name: "secret", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: canary}}},
			{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "safe-config"}}}},
			{Name: "token", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Path: "token", ExpirationSeconds: remoteInt64Pointer(600)}}}}}},
		},
	}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a", UID: "service-uid", ResourceVersion: "21"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, Ports: []corev1.ServicePort{{Port: 8443}}}}
	gateway, client, fakeClient := newFakeGateway(t, "team-a", pod, service)
	defer client.Close()
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		t.Fatalf("NewToolResourceReader() error = %v", err)
	}
	resolved, err := reader.ResolvePod(context.Background(), toolcontract.PodResolveRequest{Scope: liveScope("team-a"), Namespace: "team-a", PodName: "sample-pod", Container: "app"})
	if err != nil || resolved.Reference.UID != "pod-uid" || resolved.Reference.ResourceVersion != "20" ||
		!reflect.DeepEqual(resolved.DeniedMountRoots, []string{"/var/app/config", "/var/app/credentials", "/var/run/secrets/kubernetes.io/serviceaccount"}) || strings.Contains(strings.Join(resolved.DeniedMountRoots, "\n"), canary) {
		t.Fatalf("ResolvePod() = %#v/%v", resolved, err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-a")
	fakeClient.ClearActions()
	resolvedService, err := reader.ResolveService(context.Background(), toolcontract.ServiceResolveRequest{Scope: liveScope("team-a"), Namespace: "team-a", ServiceName: "api", Port: 8443})
	if err != nil || resolvedService.Reference.UID != "service-uid" || resolvedService.Port != 8443 {
		t.Fatalf("ResolveService() = %#v/%v", resolvedService, err)
	}
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "services", "team-a")
}

func TestRemoteDiagnosticActionRevalidationUsesExactLiveTargets(t *testing.T) {
	t.Run("Pod identity", func(t *testing.T) {
		pod := remoteHTTPPod()
		gateway, client, fakeClient := newFakeGateway(t, "team-a", pod.DeepCopy())
		defer client.Close()
		reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
		if err != nil {
			t.Fatal(err)
		}
		plan := remoteKubePodExecPlan(t, pod)
		if err := reader.RevalidateRemoteDiagnosticAction(context.Background(), plan); err != nil {
			t.Fatalf("RevalidateRemoteDiagnosticAction() error = %v", err)
		}
		assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-a")
		stored, err := fakeClient.CoreV1().Pods("team-a").Get(context.Background(), pod.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		stored.ResourceVersion = "changed"
		if _, err := fakeClient.CoreV1().Pods("team-a").Update(context.Background(), stored, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := reader.RevalidateRemoteDiagnosticAction(context.Background(), plan); err == nil {
			t.Fatal("changed Pod identity was accepted")
		} else {
			assertRemoteKubeClass(t, err, domain.SafeErrorClassConflict)
		}
	})

	t.Run("Service identity and port", func(t *testing.T) {
		service := remoteHTTPService()
		gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy())
		defer client.Close()
		reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
		if err != nil {
			t.Fatal(err)
		}
		plan := remoteKubeDiagnosticPodPlan(t, service)
		if err := reader.RevalidateRemoteDiagnosticAction(context.Background(), plan); err != nil {
			t.Fatalf("RevalidateRemoteDiagnosticAction() error = %v", err)
		}
		assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "services", "team-a")
	})
}

func TestResolvedPodSensitiveMountProjectionFailsClosed(t *testing.T) {
	request := toolcontract.PodResolveRequest{Scope: liveScope("team-a"), Namespace: "team-a", PodName: "sample-pod", Container: "app"}
	base := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "sample-pod", Namespace: "team-a", UID: "pod-uid", ResourceVersion: "20"}, Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: "app"}},
		Volumes:    []corev1.Volume{{Name: "secret", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "sensitive"}}}},
	}}

	t.Run("root mount denies every file path", func(t *testing.T) {
		pod := base.DeepCopy()
		pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "secret", MountPath: "/"}}
		resolved, err := projectResolvedPod(pod, request)
		if err != nil || !reflect.DeepEqual(resolved.DeniedMountRoots, []string{"/"}) || !resolved.PathTouchesDeniedMount("/var/app/data/report.txt") {
			t.Fatalf("root sensitive mount projection = %#v/%v", resolved, err)
		}
	})

	t.Run("sensitive volume device is denied", func(t *testing.T) {
		pod := base.DeepCopy()
		pod.Spec.Containers[0].VolumeDevices = []corev1.VolumeDevice{{Name: "secret", DevicePath: "/var/app/data/device"}}
		resolved, err := projectResolvedPod(pod, request)
		if err != nil || !resolved.PathTouchesDeniedMount("/var/app/data/device") {
			t.Fatalf("sensitive device projection = %#v/%v", resolved, err)
		}
	})

	t.Run("unknown mount volume is invalid external data", func(t *testing.T) {
		pod := base.DeepCopy()
		pod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{{Name: "missing", MountPath: "/var/app/data"}}
		if resolved, err := projectResolvedPod(pod, request); err == nil || !reflect.DeepEqual(resolved, toolcontract.ResolvedPod{}) {
			t.Fatalf("unknown volume projection = %#v/%v", resolved, err)
		}
	})
}

func TestRemoteDiagnosticsRejectStalePolicyImmediatelyBeforeExternalAttempt(t *testing.T) {
	pod := remoteHTTPPod()
	service := remoteHTTPService()

	t.Run("Pod Exec", func(t *testing.T) {
		gateway, client, fakeClient := newFakeGateway(t, "team-a", pod.DeepCopy())
		defer client.Close()
		guard := &sequenceResourcePolicy{results: []bool{true, false}}
		reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), guard)
		if err != nil {
			t.Fatal(err)
		}
		_, err = reader.ExecuteRemoteCommand(context.Background(), remoteCommandRequest(t, pod))
		assertRemoteKubeClass(t, err, domain.SafeErrorClassStaleScope)
		assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "pods", "team-a")
	})

	t.Run("diagnostic Pod", func(t *testing.T) {
		gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy())
		defer client.Close()
		guard := &sequenceResourcePolicy{results: []bool{true, false}}
		reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), guard)
		if err != nil {
			t.Fatal(err)
		}
		_, err = reader.RunDiagnosticPod(context.Background(), diagnosticPodRequest(t, service))
		assertRemoteKubeClass(t, err, domain.SafeErrorClassStaleScope)
		assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "services", "team-a")
	})
}

func TestRemoteDiagnosticsDiscardPostAttemptStaleResultsAndCleanCreatedPod(t *testing.T) {
	t.Run("Pod Exec", func(t *testing.T) {
		pod := remoteHTTPPod()
		capture := &remoteExecCapture{}
		server := httptest.NewTLSServer(remoteExecHandler(t, pod, capture, func(streams *remoteTestStreams) {
			_, _ = streams.stdout.Write([]byte("must-be-discarded\n"))
			_ = streams.writeStatus(&apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusSuccess}})
		}))
		defer server.Close()
		reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
		defer client.Close()
		reader.policyGuard = &sequenceResourcePolicy{results: []bool{true, true, false}}
		observation, err := reader.ExecuteRemoteCommand(context.Background(), remoteCommandRequest(t, pod))
		assertRemoteKubeClass(t, err, domain.SafeErrorClassStaleScope)
		if !observation.Completed || capture.postCount() != 1 {
			t.Fatalf("post-attempt stale exec observation/posts = %#v/%d", observation, capture.postCount())
		}
	})

	t.Run("diagnostic Pod", func(t *testing.T) {
		service := remoteHTTPService()
		gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy())
		defer client.Close()
		request := diagnosticPodRequest(t, service)
		fakeClient.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
			created := action.(clienttesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
			created.UID = "stale-diag-uid"
			created.ResourceVersion = "2"
			if err := fakeClient.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, created, request.Service.Namespace); err != nil {
				t.Fatalf("track stale diagnostic Pod: %v", err)
			}
			return true, created.DeepCopy(), nil
		})
		guard := &sequenceResourcePolicy{results: []bool{true, true, false}}
		reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), guard)
		if err != nil {
			t.Fatal(err)
		}
		observation, err := reader.RunDiagnosticPod(context.Background(), request)
		assertRemoteKubeClass(t, err, domain.SafeErrorClassStaleScope)
		if !observation.Created || observation.Lifecycle.Create != domain.DiagnosticPodPhaseCompleted ||
			observation.Lifecycle.Wait != domain.DiagnosticPodPhaseNotAttempted || observation.Lifecycle.Log != domain.DiagnosticPodPhaseNotAttempted ||
			observation.Lifecycle.Delete != domain.DiagnosticPodPhaseCompleted || observation.CleanupState != domain.DiagnosticPodCleanupVerified {
			t.Fatalf("post-create stale diagnostic observation = %#v", observation)
		}
		verbs := make([]string, 0, len(fakeClient.Actions()))
		for _, action := range fakeClient.Actions() {
			verbs = append(verbs, action.GetVerb()+":"+action.GetResource().Resource)
		}
		if want := []string{"get:services", "create:pods", "delete:pods", "get:pods"}; !reflect.DeepEqual(verbs, want) {
			t.Fatalf("post-create stale actions = %#v, want %#v", verbs, want)
		}
	})
}

func TestDiagnosticPodRejectsChangedNetworkTargetBeforeKubernetesIO(t *testing.T) {
	service := remoteHTTPService()
	gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy())
	defer client.Close()
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	base := diagnosticPodRequest(t, service)
	changedArguments, err := domain.NewActionArguments([]string{"-z", "-v", "-w", "5", "other.team-a.svc", "8443"})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []toolcontract.DiagnosticPodRequest{
		func() toolcontract.DiagnosticPodRequest {
			changed := base
			changed.TargetHost = "metadata.team-a.svc"
			return changed
		}(),
		func() toolcontract.DiagnosticPodRequest {
			changed := base
			changed.Arguments = changedArguments
			return changed
		}(),
	} {
		if _, runErr := reader.RunDiagnosticPod(context.Background(), request); runErr == nil {
			t.Fatal("changed diagnostic network target was accepted")
		}
	}
	if len(fakeClient.Actions()) != 0 {
		t.Fatalf("changed diagnostic network target actions = %#v", fakeClient.Actions())
	}
}

func TestDiagnosticPodRejectsSelectorlessServiceBeforePodCreate(t *testing.T) {
	service := remoteHTTPService()
	service.Spec.Selector = nil
	gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy())
	defer client.Close()
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	requestService := remoteHTTPService()
	_, err = reader.RunDiagnosticPod(context.Background(), diagnosticPodRequest(t, requestService))
	assertRemoteKubeClass(t, err, domain.SafeErrorClassConflict)
	assertSingleClientAction(t, fakeClient.Actions(), "get", "", "v1", "services", "team-a")
}

func TestExecuteRemoteCommandUsesExactSPDYExecRequestAndOrderedBoundedChunks(t *testing.T) {
	pod := remoteHTTPPod()
	capture := &remoteExecCapture{}
	server := httptest.NewTLSServer(remoteExecHandler(t, pod, capture, func(streams *remoteTestStreams) {
		_, _ = streams.stdout.Write([]byte("stdout-line\n"))
		_, _ = streams.stderr.Write([]byte("stderr-line\n"))
		_ = streams.writeStatus(&apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusSuccess}})
	}))
	defer server.Close()
	reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
	defer client.Close()
	request := remoteCommandRequest(t, pod)
	observation, err := reader.ExecuteRemoteCommand(context.Background(), request)
	if err != nil || observation.Validate(request) != nil || !observation.Completed || observation.ExitCode != 0 || observation.ByteCount != len("stdout-line\nstderr-line\n") || len(observation.Chunks) != 2 {
		t.Fatalf("ExecuteRemoteCommand() = %#v/%v, validation=%v", observation, err, observation.Validate(request))
	}
	for index, chunk := range observation.Chunks {
		if chunk.Sequence() != index+1 || chunk.Stream() != toolcontract.RemoteOutputStdout && chunk.Stream() != toolcontract.RemoteOutputStderr {
			t.Fatalf("chunk[%d] = sequence %d stream %q", index, chunk.Sequence(), chunk.Stream())
		}
	}
	method, path, query, posts := capture.snapshot()
	wantCommands := []string{"/usr/bin/printf", "literal;not-a-shell", "$(ignored)"}
	if method != http.MethodPost || path != "/api/v1/namespaces/team-a/pods/sample-pod/exec" || posts != 1 ||
		!reflect.DeepEqual(query["command"], wantCommands) || query.Get("container") != "app" || query.Get("stdin") != "" ||
		query.Get("stdout") != "true" || query.Get("stderr") != "true" || query.Get("tty") != "" || query.Get("timeout") != "5s" || len(query) != 5 {
		t.Fatalf("SPDY request = %s %s query=%v posts=%d", method, path, query, posts)
	}
}

func TestRemoteOutputCollectorAllowsExactCeilingsAndRejectsOneOver(t *testing.T) {
	t.Run("bytes", func(t *testing.T) {
		cancelled := false
		collector := newRemoteOutputCollector(3, 5, func() { cancelled = true })
		writer := collector.writer(toolcontract.RemoteOutputStdout)
		if written, err := writer.Write([]byte("12345")); err != nil || written != 5 || collector.limitReached() || cancelled {
			t.Fatalf("exact byte write = %d/%v limited=%t cancelled=%t", written, err, collector.limitReached(), cancelled)
		}
		if written, err := writer.Write([]byte("6")); !errors.Is(err, errRemoteOutputLimit) || written != 0 || !collector.limitReached() || !cancelled {
			t.Fatalf("one-over byte write = %d/%v limited=%t cancelled=%t", written, err, collector.limitReached(), cancelled)
		}
	})

	t.Run("lines across chunks", func(t *testing.T) {
		cancelled := false
		collector := newRemoteOutputCollector(1, 32, func() { cancelled = true })
		writer := collector.writer(toolcontract.RemoteOutputStdout)
		for _, value := range []string{"one", "-line", "\n"} {
			if written, err := writer.Write([]byte(value)); err != nil || written != len(value) {
				t.Fatalf("exact line fragment %q = %d/%v", value, written, err)
			}
		}
		observation := collector.observation(domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod", UID: "pod-uid", ResourceVersion: "20"}, "app")
		if observation.LineCount != 1 || observation.Truncated || collector.limitReached() || cancelled {
			t.Fatalf("exact line observation = %#v limited=%t cancelled=%t", observation, collector.limitReached(), cancelled)
		}
		if written, err := writer.Write([]byte("second")); !errors.Is(err, errRemoteOutputLimit) || written != 0 || !collector.limitReached() || !cancelled {
			t.Fatalf("one-over line write = %d/%v limited=%t cancelled=%t", written, err, collector.limitReached(), cancelled)
		}
	})
}

func TestDiagnosticPodLogAppliesServerByteLimitAndExactLocalCeilings(t *testing.T) {
	for _, test := range []struct {
		name      string
		content   string
		maxBytes  int
		maxLines  int
		wantClass domain.SafeErrorClass
	}{
		{name: "exact bytes", content: "12345", maxBytes: 5, maxLines: 2},
		{name: "one over bytes", content: "123456", maxBytes: 5, maxLines: 2, wantClass: domain.SafeErrorClassBudgetExhausted},
		{name: "exact lines", content: "first\nsecond\n", maxBytes: 64, maxLines: 2},
		{name: "one over lines", content: "first\nsecond\nthird\n", maxBytes: 64, maxLines: 2, wantClass: domain.SafeErrorClassBudgetExhausted},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/api/v1/namespaces/team-a/pods/kupilot-diag-test/log" ||
					request.URL.Query().Get("container") != diagnosticContainerName || request.URL.Query().Get("limitBytes") != strconv.Itoa(test.maxBytes+1) {
					t.Errorf("diagnostic log request = %s %s %v", request.Method, request.URL.Path, request.URL.Query())
					writer.WriteHeader(http.StatusBadRequest)
					return
				}
				_, _ = writer.Write([]byte(test.content))
			}))
			defer server.Close()
			reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
			defer client.Close()
			bundle, err := reader.gateway.resourceBundle(reader.client, liveScope("team-a"), "test_diagnostic_pod_log")
			if err != nil {
				t.Fatal(err)
			}
			request := diagnosticPodRequest(t, remoteHTTPService())
			request.MaxBytes, request.MaxLines = test.maxBytes, test.maxLines
			content, err := readDiagnosticPodLog(context.Background(), bundle, request)
			if test.wantClass != "" {
				assertRemoteKubeClass(t, err, test.wantClass)
				if content != nil {
					t.Fatalf("limited diagnostic log content = %q", content)
				}
				return
			}
			if err != nil || string(content) != test.content {
				t.Fatalf("diagnostic log = %q/%v", content, err)
			}
		})
	}
}

func TestExecuteRemoteCommandCancelsAndBoundsOneSPDYAttempt(t *testing.T) {
	t.Run("byte limit", func(t *testing.T) {
		pod := remoteHTTPPod()
		capture := &remoteExecCapture{}
		server := httptest.NewTLSServer(remoteExecHandler(t, pod, capture, func(streams *remoteTestStreams) {
			_, _ = streams.stdout.Write([]byte("123456789"))
			_ = streams.writeStatus(&apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusSuccess}})
		}))
		defer server.Close()
		reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
		defer client.Close()
		request := remoteCommandRequest(t, pod)
		request.MaxBytes = 5
		observation, err := reader.ExecuteRemoteCommand(context.Background(), request)
		assertRemoteKubeClass(t, err, domain.SafeErrorClassBudgetExhausted)
		if !observation.Truncated || observation.ByteCount != 5 || observation.Completed || capture.postCount() != 1 {
			t.Fatalf("bounded observation = %#v, posts=%d", observation, capture.postCount())
		}
	})

	t.Run("context cancellation joins stream", func(t *testing.T) {
		pod := remoteHTTPPod()
		capture := &remoteExecCapture{}
		ready := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewTLSServer(remoteExecHandler(t, pod, capture, func(streams *remoteTestStreams) {
			close(ready)
			<-release
			_ = streams.writeStatus(&apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusSuccess}})
		}))
		defer server.Close()
		reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
		defer client.Close()
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := reader.ExecuteRemoteCommand(ctx, remoteCommandRequest(t, pod))
			result <- err
		}()
		<-ready
		cancel()
		var err error
		select {
		case err = <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("ExecuteRemoteCommand did not return after cancellation")
		}
		close(release)
		assertRemoteKubeClass(t, err, domain.SafeErrorClassCancelled)
		if capture.postCount() != 1 {
			t.Fatalf("cancelled SPDY posts = %d", capture.postCount())
		}
	})

	t.Run("deadline joins stream", func(t *testing.T) {
		pod := remoteHTTPPod()
		capture := &remoteExecCapture{}
		ready := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewTLSServer(remoteExecHandler(t, pod, capture, func(streams *remoteTestStreams) {
			close(ready)
			<-release
			_ = streams.writeStatus(&apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusSuccess}})
		}))
		defer server.Close()
		reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
		defer client.Close()
		request := remoteCommandRequest(t, pod)
		request.Timeout = 25 * time.Millisecond
		result := make(chan error, 1)
		go func() {
			_, err := reader.ExecuteRemoteCommand(context.Background(), request)
			result <- err
		}()
		<-ready
		var err error
		select {
		case err = <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("ExecuteRemoteCommand did not return after its deadline")
		}
		close(release)
		assertRemoteKubeClass(t, err, domain.SafeErrorClassTimeout)
		if capture.postCount() != 1 {
			t.Fatalf("timed out SPDY posts = %d", capture.postCount())
		}
	})

	t.Run("client close cancels and joins stream", func(t *testing.T) {
		pod := remoteHTTPPod()
		capture := &remoteExecCapture{}
		ready := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewTLSServer(remoteExecHandler(t, pod, capture, func(streams *remoteTestStreams) {
			close(ready)
			<-release
			_ = streams.writeStatus(&apierrors.StatusError{ErrStatus: metav1.Status{Status: metav1.StatusSuccess}})
		}))
		defer server.Close()
		reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
		result := make(chan error, 1)
		go func() {
			_, err := reader.ExecuteRemoteCommand(context.Background(), remoteCommandRequest(t, pod))
			result <- err
		}()
		<-ready
		closed := make(chan struct{})
		go func() {
			client.Close()
			close(closed)
		}()
		var err error
		select {
		case err = <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("ExecuteRemoteCommand did not return after client close")
		}
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("ClientBundle.Close did not join the remote stream")
		}
		close(release)
		assertRemoteKubeClass(t, err, domain.SafeErrorClassCancelled)
		if capture.postCount() != 1 {
			t.Fatalf("closed-client SPDY posts = %d", capture.postCount())
		}
	})
}

func TestExecuteRemoteCommandRejectsRedirectWithoutSecondAttempt(t *testing.T) {
	pod := remoteHTTPPod()
	var mu sync.Mutex
	posts, redirected := 0, 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/team-a/pods/sample-pod":
			writeRemoteJSON(t, writer, pod)
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/exec"):
			mu.Lock()
			posts++
			mu.Unlock()
			http.Redirect(writer, request, "/redirected", http.StatusTemporaryRedirect)
		case request.URL.Path == "/redirected":
			mu.Lock()
			redirected++
			mu.Unlock()
			writer.WriteHeader(http.StatusInternalServerError)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
	defer client.Close()
	_, err := reader.ExecuteRemoteCommand(context.Background(), remoteCommandRequest(t, pod))
	if err == nil {
		t.Fatal("redirected exec request returned no error")
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || redirected != 0 {
		t.Fatalf("redirect handling posts/redirected = %d/%d", posts, redirected)
	}
}

func TestRunDiagnosticPodUsesRestrictedSpecAndExactCreateLogDeleteLifecycle(t *testing.T) {
	service := remoteHTTPService()
	requestLog := make([]string, 0, 8)
	var mu sync.Mutex
	var created corev1.Pod
	var deleteOptions metav1.DeleteOptions
	deleted := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requestLog = append(requestLog, request.Method+" "+request.URL.EscapedPath())
		mu.Unlock()
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/team-a/services/api":
			writeRemoteJSON(t, writer, service)
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/namespaces/team-a/pods":
			body, readErr := io.ReadAll(request.Body)
			object, _, decodeErr := clientscheme.Codecs.UniversalDeserializer().Decode(body, nil, nil)
			decoded, ok := object.(*corev1.Pod)
			if readErr != nil || decodeErr != nil || !ok {
				t.Errorf("decode diagnostic Pod: read=%v decode=%v type=%T", readErr, decodeErr, object)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			created = *decoded
			response := created.DeepCopy()
			response.UID = "diag-uid"
			response.ResourceVersion = "2"
			writeRemoteJSON(t, writer, response)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/team-a/pods/kupilot-diag-test" && !deleted:
			terminal := created.DeepCopy()
			terminal.UID = "diag-uid"
			terminal.ResourceVersion = "3"
			terminal.Status.Phase = corev1.PodSucceeded
			terminal.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: diagnosticContainerName, State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
			writeRemoteJSON(t, writer, terminal)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/team-a/pods/kupilot-diag-test/log":
			if request.URL.Query().Get("container") != diagnosticContainerName || request.URL.Query().Get("limitBytes") != "4097" || request.URL.Query().Get("timeout") != "10s" || len(request.URL.Query()) != 3 {
				t.Errorf("diagnostic log query = %v", request.URL.Query())
			}
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = writer.Write([]byte("connected\n"))
		case request.Method == http.MethodDelete && request.URL.Path == "/api/v1/namespaces/team-a/pods/kupilot-diag-test":
			body, readErr := io.ReadAll(request.Body)
			object, _, decodeErr := clientscheme.Codecs.UniversalDeserializer().Decode(body, nil, nil)
			decoded, ok := object.(*metav1.DeleteOptions)
			if readErr != nil || decodeErr != nil || !ok {
				t.Errorf("decode delete options: read=%v decode=%v type=%T", readErr, decodeErr, object)
			} else {
				deleteOptions = *decoded
			}
			deleted = true
			writeRemoteJSON(t, writer, &metav1.Status{Status: metav1.StatusSuccess})
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/team-a/pods/kupilot-diag-test" && deleted:
			writeRemoteNotFound(t, writer, "pods", "kupilot-diag-test")
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
	defer client.Close()
	request := diagnosticPodRequest(t, service)
	observation, err := reader.RunDiagnosticPod(context.Background(), request)
	if err != nil || observation.Validate(request) != nil || !observation.Created || !observation.Completed || observation.ExitCode != 0 || observation.Lifecycle != completedKubeDiagnosticPodLifecycle() || observation.CleanupState != domain.DiagnosticPodCleanupVerified || observation.ByteCount != len("connected\n") {
		t.Fatalf("RunDiagnosticPod() = %#v/%v validation=%v", observation, err, observation.Validate(request))
	}
	if created.Name != request.Name || created.Namespace != "team-a" || created.Spec.ServiceAccountName != diagnosticServiceAccount || created.Spec.AutomountServiceAccountToken == nil || *created.Spec.AutomountServiceAccountToken ||
		created.Spec.HostNetwork || created.Spec.HostPID || created.Spec.HostIPC || len(created.Spec.Volumes) != 0 || created.Spec.RestartPolicy != corev1.RestartPolicyNever ||
		created.Spec.EnableServiceLinks == nil || *created.Spec.EnableServiceLinks || created.Spec.DNSPolicy != corev1.DNSClusterFirst || created.Spec.SchedulerName != corev1.DefaultSchedulerName ||
		created.Spec.TerminationGracePeriodSeconds == nil || *created.Spec.TerminationGracePeriodSeconds != 1 || created.Spec.ActiveDeadlineSeconds == nil || *created.Spec.ActiveDeadlineSeconds != 10 || len(created.Spec.Containers) != 1 {
		t.Fatalf("diagnostic Pod metadata/spec = %#v", created)
	}
	container := created.Spec.Containers[0]
	securityContext := container.SecurityContext
	if container.Image != request.Image || !reflect.DeepEqual(container.Command, append([]string{request.Executable}, request.Arguments.Values()...)) || securityContext == nil || securityContext.Privileged == nil || *securityContext.Privileged ||
		securityContext.AllowPrivilegeEscalation == nil || *securityContext.AllowPrivilegeEscalation || securityContext.ReadOnlyRootFilesystem == nil || !*securityContext.ReadOnlyRootFilesystem ||
		securityContext.RunAsNonRoot == nil || !*securityContext.RunAsNonRoot || securityContext.Capabilities == nil || !reflect.DeepEqual(securityContext.Capabilities.Drop, []corev1.Capability{"ALL"}) ||
		container.Resources.Limits.Cpu().IsZero() || container.Resources.Limits.Memory().IsZero() || container.Resources.Limits.StorageEphemeral().IsZero() {
		t.Fatalf("diagnostic container = %#v", container)
	}
	if deleteOptions.Preconditions == nil || deleteOptions.Preconditions.UID == nil || *deleteOptions.Preconditions.UID != types.UID("diag-uid") || deleteOptions.GracePeriodSeconds == nil || *deleteOptions.GracePeriodSeconds != 1 {
		t.Fatalf("diagnostic delete options = %#v", deleteOptions)
	}
	mu.Lock()
	gotLog := append([]string(nil), requestLog...)
	mu.Unlock()
	wantLog := []string{
		"GET /api/v1/namespaces/team-a/services/api",
		"POST /api/v1/namespaces/team-a/pods",
		"GET /api/v1/namespaces/team-a/pods/kupilot-diag-test",
		"GET /api/v1/namespaces/team-a/pods/kupilot-diag-test/log",
		"DELETE /api/v1/namespaces/team-a/pods/kupilot-diag-test",
		"GET /api/v1/namespaces/team-a/pods/kupilot-diag-test",
	}
	if !reflect.DeepEqual(gotLog, wantLog) {
		t.Fatalf("diagnostic lifecycle requests = %#v, want %#v", gotLog, wantLog)
	}
}

func TestDiagnosticPodRejectsAdmissionMutationAndCleansExactCreatedUID(t *testing.T) {
	service := remoteHTTPService()
	gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy())
	defer client.Close()
	request := diagnosticPodRequest(t, service)
	fakeClient.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		created := action.(clienttesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		created.UID = "mutated-diag-uid"
		created.ResourceVersion = "2"
		trueValue := true
		created.Spec.Containers[0].SecurityContext.Privileged = &trueValue
		if err := fakeClient.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, created, request.Service.Namespace); err != nil {
			t.Fatalf("track mutated diagnostic Pod: %v", err)
		}
		return true, created.DeepCopy(), nil
	})
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := reader.RunDiagnosticPod(context.Background(), request)
	assertRemoteKubeClass(t, err, domain.SafeErrorClassInvalidExternalResponse)
	if !observation.Created || observation.Lifecycle.Create != domain.DiagnosticPodPhaseCompleted ||
		observation.Lifecycle.Wait != domain.DiagnosticPodPhaseNotAttempted || observation.Lifecycle.Log != domain.DiagnosticPodPhaseNotAttempted ||
		observation.Lifecycle.Delete != domain.DiagnosticPodPhaseCompleted || observation.CleanupState != domain.DiagnosticPodCleanupVerified {
		t.Fatalf("mutated admission observation = %#v", observation)
	}
	verbs := make([]string, 0, len(fakeClient.Actions()))
	for _, action := range fakeClient.Actions() {
		verbs = append(verbs, action.GetVerb()+":"+action.GetResource().Resource)
	}
	if want := []string{"get:services", "create:pods", "delete:pods", "get:pods"}; !reflect.DeepEqual(verbs, want) {
		t.Fatalf("mutated admission actions = %#v, want %#v", verbs, want)
	}
}

func TestDiagnosticPodIdentityAllowsOnlySchedulerAndAPIDefaults(t *testing.T) {
	request := diagnosticPodRequest(t, remoteHTTPService())
	pod := diagnosticPodFor(request)
	pod.UID = "diag-uid"
	pod.ResourceVersion = "2"
	pod.Spec.NodeName = "worker-a"
	seconds := int64(300)
	pod.Spec.Tolerations = []corev1.Toleration{
		{Key: corev1.TaintNodeNotReady, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds},
		{Key: corev1.TaintNodeUnreachable, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: &seconds},
	}
	zeroPriority := int32(0)
	pod.Spec.Priority = &zeroPriority
	if !diagnosticPodMatchesRequest(pod, request) {
		t.Fatal("scheduler and fixed API defaults changed the diagnostic identity")
	}
	if pod.Spec.PreemptionPolicy == nil || *pod.Spec.PreemptionPolicy != corev1.PreemptNever {
		t.Fatalf("diagnostic Pod preemption policy = %#v", pod.Spec.PreemptionPolicy)
	}
	changed := pod.DeepCopy()
	changed.Spec.Tolerations = append(changed.Spec.Tolerations, corev1.Toleration{Key: "dedicated", Operator: corev1.TolerationOpExists})
	if diagnosticPodMatchesRequest(changed, request) {
		t.Fatal("an admission-added scheduling permission was accepted")
	}
	changed = pod.DeepCopy()
	changed.Spec.Containers = append(changed.Spec.Containers, corev1.Container{Name: "sidecar", Image: request.Image})
	if diagnosticPodMatchesRequest(changed, request) {
		t.Fatal("an admission-added sidecar was accepted")
	}
	changed = pod.DeepCopy()
	nonzeroPriority := int32(1)
	changed.Spec.Priority = &nonzeroPriority
	if diagnosticPodMatchesRequest(changed, request) {
		t.Fatal("a nonzero admission priority was accepted")
	}
	changed = pod.DeepCopy()
	changed.Spec.PriorityClassName = "global-default"
	if diagnosticPodMatchesRequest(changed, request) {
		t.Fatal("an admission-selected PriorityClass was accepted")
	}
}

func TestRemoteOutputCollectorCapsChunkAmplification(t *testing.T) {
	cancelled := false
	collector := newRemoteOutputCollector(domain.MaxRemoteDiagnosticLines, domain.MaxRemoteDiagnosticBytes, func() { cancelled = true })
	writer := collector.writer(toolcontract.RemoteOutputStdout)
	for index := 0; index < domain.MaxRemoteOutputChunks; index++ {
		if written, err := writer.Write([]byte("x")); err != nil || written != 1 {
			t.Fatalf("Write(%d) = %d/%v", index, written, err)
		}
	}
	if written, err := writer.Write([]byte("x")); !errors.Is(err, errRemoteOutputLimit) || written != 0 || !cancelled || !collector.limitReached() {
		t.Fatalf("one-over chunk Write = %d/%v cancelled=%t limited=%t", written, err, cancelled, collector.limitReached())
	}
}

func TestDiagnosticPodDefiniteCreateRejectionDoesNotDeletePreexistingPod(t *testing.T) {
	service := remoteHTTPService()
	request := diagnosticPodRequest(t, service)
	existing := diagnosticPodFor(request)
	existing.UID = "preexisting-uid"
	existing.ResourceVersion = "1"
	gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy(), existing)
	defer client.Close()
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := reader.RunDiagnosticPod(context.Background(), request)
	assertRemoteKubeClass(t, err, domain.SafeErrorClassConflict)
	if observation.Created || observation.Lifecycle.Create != domain.DiagnosticPodPhaseFailed ||
		observation.Lifecycle.Wait != domain.DiagnosticPodPhaseNotAttempted || observation.Lifecycle.Log != domain.DiagnosticPodPhaseNotAttempted ||
		observation.Lifecycle.Delete != domain.DiagnosticPodPhaseNotAttempted || observation.CleanupState != domain.DiagnosticPodCleanupNotNeeded {
		t.Fatalf("definite create rejection observation = %#v", observation)
	}
	verbs := make([]string, 0, len(fakeClient.Actions()))
	for _, action := range fakeClient.Actions() {
		verbs = append(verbs, action.GetVerb()+":"+action.GetResource().Resource)
	}
	if want := []string{"get:services", "create:pods"}; !reflect.DeepEqual(verbs, want) {
		t.Fatalf("definite create rejection actions = %#v, want %#v", verbs, want)
	}
}

func TestDiagnosticPodAmbiguousCleanupDeletesOnlyExactOwnedIdentity(t *testing.T) {
	request := diagnosticPodRequest(t, remoteHTTPService())
	pod := diagnosticPodFor(request)
	pod.UID = "diag-uid"
	pod.ResourceVersion = "2"

	t.Run("matching identity is cleaned", func(t *testing.T) {
		client := kubernetesfake.NewSimpleClientset(pod.DeepCopy())
		bundle := &ClientBundle{typed: client}
		phase, state := cleanupAmbiguousDiagnosticPod(bundle, request, "")
		if phase != domain.DiagnosticPodPhaseCompleted || state != domain.DiagnosticPodCleanupVerified {
			t.Fatalf("cleanup phase/state = %q/%q", phase, state)
		}
		actions := client.Actions()
		if len(actions) != 3 || actions[0].GetVerb() != "get" || actions[1].GetVerb() != "delete" || actions[2].GetVerb() != "get" {
			t.Fatalf("cleanup actions = %#v", actions)
		}
	})

	t.Run("changed target digest is not deleted", func(t *testing.T) {
		changed := pod.DeepCopy()
		changed.Annotations["kupilot.io/target-digest"] = strings.Repeat("0", 64)
		client := kubernetesfake.NewSimpleClientset(changed)
		bundle := &ClientBundle{typed: client}
		phase, state := cleanupAmbiguousDiagnosticPod(bundle, request, "")
		if phase != domain.DiagnosticPodPhaseNotAttempted || state != domain.DiagnosticPodCleanupUnknown || len(client.Actions()) != 1 || client.Actions()[0].GetVerb() != "get" {
			t.Fatalf("changed identity cleanup/actions = %q/%q/%#v", phase, state, client.Actions())
		}
	})

	t.Run("changed Pod spec is not deleted", func(t *testing.T) {
		changed := pod.DeepCopy()
		changed.Spec.Containers[0].SecurityContext.ReadOnlyRootFilesystem = nil
		client := kubernetesfake.NewSimpleClientset(changed)
		bundle := &ClientBundle{typed: client}
		phase, state := cleanupAmbiguousDiagnosticPod(bundle, request, "")
		if phase != domain.DiagnosticPodPhaseNotAttempted || state != domain.DiagnosticPodCleanupUnknown || len(client.Actions()) != 1 || client.Actions()[0].GetVerb() != "get" {
			t.Fatalf("changed Pod cleanup/actions = %q/%q/%#v", phase, state, client.Actions())
		}
	})

	for _, field := range []string{"uid", "resource version"} {
		t.Run("missing "+field+" is not deleted", func(t *testing.T) {
			changed := pod.DeepCopy()
			if field == "uid" {
				changed.UID = ""
			} else {
				changed.ResourceVersion = ""
			}
			client := kubernetesfake.NewSimpleClientset(changed)
			bundle := &ClientBundle{typed: client}
			phase, state := cleanupAmbiguousDiagnosticPod(bundle, request, "")
			if phase != domain.DiagnosticPodPhaseNotAttempted || state != domain.DiagnosticPodCleanupUnknown || len(client.Actions()) != 1 || client.Actions()[0].GetVerb() != "get" {
				t.Fatalf("incomplete identity cleanup/actions = %q/%q/%#v", phase, state, client.Actions())
			}
		})
	}

	t.Run("empty delete precondition performs no request", func(t *testing.T) {
		client := kubernetesfake.NewSimpleClientset()
		bundle := &ClientBundle{typed: client}
		phase, state := deleteDiagnosticPod(bundle, request, "")
		if phase != domain.DiagnosticPodPhaseNotAttempted || state != domain.DiagnosticPodCleanupUnknown || len(client.Actions()) != 0 {
			t.Fatalf("empty precondition cleanup/actions = %q/%q/%#v", phase, state, client.Actions())
		}
	})

	t.Run("delete transport failure is verified by get", func(t *testing.T) {
		client := kubernetesfake.NewSimpleClientset(pod.DeepCopy())
		client.PrependReactor("delete", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
			deleteAction := action.(clienttesting.DeleteAction)
			_ = client.Tracker().Delete(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, deleteAction.GetNamespace(), deleteAction.GetName())
			return true, nil, apierrors.NewInternalError(errors.New("synthetic lost delete response"))
		})
		bundle := &ClientBundle{typed: client}
		phase, state := deleteDiagnosticPod(bundle, request, "diag-uid")
		if phase != domain.DiagnosticPodPhaseCompleted || state != domain.DiagnosticPodCleanupVerified {
			t.Fatalf("ambiguous delete phase/state = %q/%q actions=%#v", phase, state, client.Actions())
		}
	})
}

func TestDiagnosticPodAmbiguousCreateCleansOnlyTheOwnedCandidate(t *testing.T) {
	service := remoteHTTPService()
	gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy())
	defer client.Close()
	request := diagnosticPodRequest(t, service)
	fakeClient.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		pod := diagnosticPodFor(request)
		pod.UID = "ambiguous-uid"
		pod.ResourceVersion = "2"
		if err := fakeClient.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, pod, request.Service.Namespace); err != nil {
			t.Fatalf("seed ambiguous Pod: %v", err)
		}
		return true, nil, apierrors.NewInternalError(errors.New("synthetic lost create response"))
	})
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := reader.RunDiagnosticPod(context.Background(), request)
	if err == nil || observation.Created || observation.Lifecycle.Create != domain.DiagnosticPodPhaseUnknown ||
		observation.Lifecycle.Wait != domain.DiagnosticPodPhaseNotAttempted || observation.Lifecycle.Log != domain.DiagnosticPodPhaseNotAttempted ||
		observation.Lifecycle.Delete != domain.DiagnosticPodPhaseCompleted || observation.CleanupState != domain.DiagnosticPodCleanupVerified {
		t.Fatalf("ambiguous create observation/error = %#v/%v", observation, err)
	}
	actions := fakeClient.Actions()
	verbs := make([]string, len(actions))
	for index, action := range actions {
		verbs[index] = action.GetVerb() + ":" + action.GetResource().Resource
	}
	want := []string{"get:services", "create:pods", "get:pods", "delete:pods", "get:pods"}
	if !reflect.DeepEqual(verbs, want) {
		t.Fatalf("ambiguous create actions = %#v, want %#v", verbs, want)
	}
}

func TestDiagnosticPodCancellationCleansAndJoinsTheExactCreatedPod(t *testing.T) {
	service := remoteHTTPService()
	gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy())
	defer client.Close()
	request := diagnosticPodRequest(t, service)
	fakeClient.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		created := action.(clienttesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		created.UID = "cancelled-diag-uid"
		created.ResourceVersion = "2"
		if err := fakeClient.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, created, request.Service.Namespace); err != nil {
			t.Fatalf("track created diagnostic Pod: %v", err)
		}
		return true, created.DeepCopy(), nil
	})
	firstWait := make(chan struct{})
	var signalOnce sync.Once
	fakeClient.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		signalOnce.Do(func() { close(firstWait) })
		return false, nil, nil
	})
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	type runResult struct {
		observation toolcontract.DiagnosticPodObservation
		err         error
	}
	result := make(chan runResult, 1)
	go func() {
		observation, runErr := reader.RunDiagnosticPod(ctx, request)
		result <- runResult{observation: observation, err: runErr}
	}()
	<-firstWait
	cancel()
	completed := <-result
	assertRemoteKubeClass(t, completed.err, domain.SafeErrorClassCancelled)
	if !completed.observation.Created || completed.observation.Lifecycle.Create != domain.DiagnosticPodPhaseCompleted ||
		completed.observation.Lifecycle.Wait != domain.DiagnosticPodPhaseFailed || completed.observation.Lifecycle.Log != domain.DiagnosticPodPhaseNotAttempted ||
		completed.observation.Lifecycle.Delete != domain.DiagnosticPodPhaseCompleted || completed.observation.CleanupState != domain.DiagnosticPodCleanupVerified {
		t.Fatalf("cancelled diagnostic lifecycle = %#v", completed.observation)
	}
	for _, action := range fakeClient.Actions() {
		if action.GetVerb() == "delete" && action.GetResource().Resource == "pods" {
			deleteAction := action.(clienttesting.DeleteAction)
			options := deleteAction.GetDeleteOptions()
			if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != types.UID("cancelled-diag-uid") {
				t.Fatalf("cancel cleanup delete = %#v", options)
			}
			return
		}
	}
	t.Fatal("cancelled diagnostic Pod was not deleted")
}

func TestClientCloseCancelsAndJoinsDiagnosticPodLifecycle(t *testing.T) {
	service := remoteHTTPService()
	gateway, client, fakeClient := newFakeGateway(t, "team-a", service.DeepCopy())
	request := diagnosticPodRequest(t, service)
	fakeClient.PrependReactor("create", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		created := action.(clienttesting.CreateAction).GetObject().(*corev1.Pod).DeepCopy()
		created.UID = "closing-diag-uid"
		created.ResourceVersion = "2"
		if err := fakeClient.Tracker().Create(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, created, request.Service.Namespace); err != nil {
			t.Fatalf("track closing diagnostic Pod: %v", err)
		}
		return true, created.DeepCopy(), nil
	})
	firstWait := make(chan struct{})
	var signalOnce sync.Once
	fakeClient.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		signalOnce.Do(func() { close(firstWait) })
		return false, nil, nil
	})
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	type runResult struct {
		observation toolcontract.DiagnosticPodObservation
		err         error
	}
	result := make(chan runResult, 1)
	go func() {
		observation, runErr := reader.RunDiagnosticPod(context.Background(), request)
		result <- runResult{observation: observation, err: runErr}
	}()
	<-firstWait
	client.Close()
	completed := <-result
	assertRemoteKubeClass(t, completed.err, domain.SafeErrorClassCancelled)
	if completed.observation.Lifecycle.Delete != domain.DiagnosticPodPhaseCompleted || completed.observation.CleanupState != domain.DiagnosticPodCleanupVerified {
		t.Fatalf("client-close diagnostic lifecycle = %#v", completed.observation)
	}
}

func TestClientCloseKeepsTransportOpenForBoundedDiagnosticCleanup(t *testing.T) {
	service := remoteHTTPService()
	waitStarted := make(chan struct{})
	deleteSeen := make(chan struct{})
	var waitOnce sync.Once
	var deleteOnce sync.Once
	var mu sync.Mutex
	deleted := false
	var created corev1.Pod
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/team-a/services/api":
			writeRemoteJSON(t, writer, service)
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/namespaces/team-a/pods":
			body, readErr := io.ReadAll(request.Body)
			object, _, decodeErr := clientscheme.Codecs.UniversalDeserializer().Decode(body, nil, nil)
			decoded, ok := object.(*corev1.Pod)
			if readErr != nil || decodeErr != nil || !ok {
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			created = *decoded
			response := created.DeepCopy()
			response.UID = "closing-http-diag-uid"
			response.ResourceVersion = "2"
			writeRemoteJSON(t, writer, response)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/team-a/pods/kupilot-diag-test":
			mu.Lock()
			isDeleted := deleted
			mu.Unlock()
			if isDeleted {
				writeRemoteNotFound(t, writer, "pods", "kupilot-diag-test")
				return
			}
			waitOnce.Do(func() { close(waitStarted) })
			<-request.Context().Done()
		case request.Method == http.MethodDelete && request.URL.Path == "/api/v1/namespaces/team-a/pods/kupilot-diag-test":
			mu.Lock()
			deleted = true
			mu.Unlock()
			deleteOnce.Do(func() { close(deleteSeen) })
			writeRemoteJSON(t, writer, &metav1.Status{Status: metav1.StatusSuccess})
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	reader, client := newRemoteHTTPReader(t, server.URL, testServerCAData(server))
	request := diagnosticPodRequest(t, service)
	type runResult struct {
		observation toolcontract.DiagnosticPodObservation
		err         error
	}
	result := make(chan runResult, 1)
	go func() {
		observation, runErr := reader.RunDiagnosticPod(context.Background(), request)
		result <- runResult{observation: observation, err: runErr}
	}()
	select {
	case <-waitStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("diagnostic Pod wait did not start")
	}
	closed := make(chan struct{})
	go func() {
		client.Close()
		close(closed)
	}()
	select {
	case <-deleteSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("client close prevented diagnostic Pod cleanup")
	}
	var completed runResult
	select {
	case completed = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("diagnostic Pod owner did not join after client close")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("client close did not join diagnostic Pod cleanup")
	}
	assertRemoteKubeClass(t, completed.err, domain.SafeErrorClassCancelled)
	if !completed.observation.Created || completed.observation.Lifecycle.Delete != domain.DiagnosticPodPhaseCompleted ||
		completed.observation.CleanupState != domain.DiagnosticPodCleanupVerified {
		t.Fatalf("client-close HTTP diagnostic lifecycle = %#v", completed.observation)
	}
}

func completedKubeDiagnosticPodLifecycle() domain.DiagnosticPodLifecycle {
	return domain.DiagnosticPodLifecycle{
		Create: domain.DiagnosticPodPhaseCompleted,
		Wait:   domain.DiagnosticPodPhaseCompleted,
		Log:    domain.DiagnosticPodPhaseCompleted,
		Delete: domain.DiagnosticPodPhaseCompleted,
	}
}

type remoteExecCapture struct {
	mu     sync.Mutex
	method string
	path   string
	query  url.Values
	posts  int
}

func (capture *remoteExecCapture) record(request *http.Request) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.method = request.Method
	capture.path = request.URL.EscapedPath()
	capture.query = request.URL.Query()
	capture.posts++
}

func (capture *remoteExecCapture) snapshot() (string, string, url.Values, int) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.method, capture.path, capture.query, capture.posts
}

func (capture *remoteExecCapture) postCount() int {
	_, _, _, count := capture.snapshot()
	return count
}

type remoteTestStreamAndReply struct {
	httpstream.Stream
	replySent <-chan struct{}
}

type remoteTestStreams struct {
	connection  io.Closer
	stdout      io.WriteCloser
	stderr      io.WriteCloser
	writeStatus func(*apierrors.StatusError) error
}

func remoteExecHandler(t *testing.T, pod *corev1.Pod, capture *remoteExecCapture, run func(*remoteTestStreams)) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/api/v1/namespaces/team-a/pods/sample-pod" {
			writeRemoteJSON(t, writer, pod)
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/namespaces/team-a/pods/sample-pod/exec" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		capture.record(request)
		streams, err := upgradeRemoteTestStreams(writer, request)
		if err != nil {
			t.Errorf("upgrade exec streams: %v", err)
			return
		}
		defer streams.connection.Close()
		run(streams)
	})
}

func upgradeRemoteTestStreams(writer http.ResponseWriter, request *http.Request) (*remoteTestStreams, error) {
	if _, err := httpstream.Handshake(request, writer, []string{remotecommandconsts.StreamProtocolV4Name}); err != nil {
		return nil, err
	}
	streamChannel := make(chan remoteTestStreamAndReply)
	upgrader := serverstream.NewResponseUpgrader()
	connection := upgrader.UpgradeResponse(writer, request, func(stream httpstream.Stream, replySent <-chan struct{}) error {
		streamChannel <- remoteTestStreamAndReply{Stream: stream, replySent: replySent}
		return nil
	})
	result := &remoteTestStreams{connection: connection}
	for received := 0; received < 3; received++ {
		stream := <-streamChannel
		<-stream.replySent
		switch stream.Headers().Get(corev1.StreamType) {
		case corev1.StreamTypeError:
			result.writeStatus = func(status *apierrors.StatusError) error {
				encoded, err := json.Marshal(status.Status())
				if err != nil {
					return err
				}
				_, err = stream.Write(encoded)
				return err
			}
		case corev1.StreamTypeStdout:
			result.stdout = stream
		case corev1.StreamTypeStderr:
			result.stderr = stream
		default:
			connection.Close()
			return nil, errors.New("unexpected remote stream")
		}
	}
	if result.stdout == nil || result.stderr == nil || result.writeStatus == nil {
		connection.Close()
		return nil, errors.New("incomplete remote streams")
	}
	return result, nil
}

func newRemoteHTTPReader(t *testing.T, serverURL string, certificateAuthority []byte) (*ToolResourceReader, application.ScopeClient) {
	t.Helper()
	path := writeNamespacedKubeconfig(t, serverURL, certificateAuthority, "team-a")
	factory, err := NewClientFactory(newConfigLoaderForPaths([]string{path}), ExecCredentialsAllow)
	if err != nil {
		t.Fatalf("NewClientFactory() error = %v", err)
	}
	gateway, err := NewGateway(factory)
	if err != nil {
		t.Fatalf("NewGateway() error = %v", err)
	}
	client, err := gateway.Create(context.Background(), "selected")
	if err != nil {
		t.Fatalf("Gateway.Create() error = %v", err)
	}
	reader, err := NewToolResourceReader(gateway, client, liveScope("team-a"), alwaysCurrentResourcePolicy{})
	if err != nil {
		client.Close()
		t.Fatalf("NewToolResourceReader() error = %v", err)
	}
	return reader, client
}

func remoteHTTPPod() *corev1.Pod {
	return &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "sample-pod", Namespace: "team-a", UID: "pod-uid", ResourceVersion: "20"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
}

func remoteHTTPService() *corev1.Service {
	return &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a", UID: "service-uid", ResourceVersion: "21"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, Ports: []corev1.ServicePort{{Port: 8443}}}}
}

func remoteCommandRequest(t *testing.T, pod *corev1.Pod) toolcontract.RemoteCommandRequest {
	t.Helper()
	arguments, err := domain.NewActionArguments([]string{"literal;not-a-shell", "$(ignored)"})
	if err != nil {
		t.Fatalf("NewActionArguments() error = %v", err)
	}
	return toolcontract.RemoteCommandRequest{
		Scope: liveScope("team-a"), PolicyGeneration: 3,
		Pod:       domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: pod.Name, UID: string(pod.UID), ResourceVersion: pod.ResourceVersion},
		Container: "app", Executable: "/usr/bin/printf", Arguments: arguments, Timeout: 5 * time.Second, MaxLines: 20, MaxBytes: 4096,
	}
}

func remoteKubePodExecPlan(t *testing.T, pod *corev1.Pod) domain.RemoteDiagnosticActionPlan {
	t.Helper()
	request := remoteCommandRequest(t, pod)
	parameters := domain.ActionParameters{
		Kind: domain.ActionParametersRemoteArgv, Container: request.Container,
		Executable: request.Executable, Arguments: request.Arguments,
	}
	plan := domain.RemoteDiagnosticActionPlan{
		RunID: "00000000-0000-7000-8000-000000084001", SessionID: "00000000-0000-7000-8000-000000084002",
		Scope: request.Scope, PolicyGeneration: request.PolicyGeneration, Operation: domain.ActionOperationPodExec,
		Target:     domain.ActionTarget{Resource: request.Pod, Subresource: "exec", Fingerprint: string(parameters.Digest())},
		Parameters: parameters, Risk: domain.RiskCritical, Effect: domain.CapabilityEffectRemoteExecute,
		DataCategories:   domain.ActionDataContainerOutput,
		AllowedSinks:     domain.ActionSinkTerminal | domain.ActionSinkModel | domain.ActionSinkKubernetesAPI,
		NetworkEffects:   domain.ActionNetworkKubernetesAPI | domain.ActionNetworkRemotePod,
		Limits:           domain.ActionLimits{Timeout: request.Timeout, MaximumItems: 1, MaximumLines: request.MaxLines, MaximumBytes: request.MaxBytes, MaximumOutput: request.MaxBytes},
		VerificationPlan: domain.PodExecVerificationPlanID, ReasonSummary: "Run one exact diagnostic argv.", RiskSummary: domain.PodExecRiskSummary,
	}
	plan.NetworkDestinationHash = domain.RemotePodNetworkDestinationHash(request.Pod, request.Container)
	if plan.Validate() != nil {
		t.Fatal("invalid Pod Exec revalidation fixture")
	}
	return plan
}

func remoteKubeDiagnosticPodPlan(t *testing.T, service *corev1.Service) domain.RemoteDiagnosticActionPlan {
	t.Helper()
	request := diagnosticPodRequest(t, service)
	parameters := domain.ActionParameters{
		Kind: domain.ActionParametersRemoteArgv, Container: "diagnostic",
		Executable: request.Executable, Arguments: request.Arguments,
	}
	plan := domain.RemoteDiagnosticActionPlan{
		RunID: "00000000-0000-7000-8000-000000084001", SessionID: "00000000-0000-7000-8000-000000084002",
		Scope: request.Scope, PolicyGeneration: request.PolicyGeneration, Operation: domain.ActionOperationDiagnosticPod,
		Target:     domain.ActionTarget{Resource: request.Service, Fingerprint: domain.SHA256Hex("diagnostic target")},
		Parameters: parameters, Risk: domain.RiskCritical, Effect: domain.CapabilityEffectClusterMutation,
		DataCategories:   domain.ActionDataContainerOutput | domain.ActionDataResourceMetadata,
		AllowedSinks:     domain.ActionSinkTerminal | domain.ActionSinkModel | domain.ActionSinkKubernetesAPI,
		NetworkEffects:   domain.ActionNetworkKubernetesAPI | domain.ActionNetworkRemotePod,
		Limits:           domain.ActionLimits{Timeout: request.Timeout, MaximumItems: 1, MaximumLines: request.MaxLines, MaximumBytes: request.MaxBytes, MaximumOutput: request.MaxBytes},
		VerificationPlan: domain.DiagnosticPodVerificationPlanID, ReasonSummary: "Run one exact Service diagnostic.", RiskSummary: domain.DiagnosticPodRiskSummary,
	}
	plan.NetworkDestinationHash = domain.DiagnosticPodNetworkDestinationHash(request.Service, request.TargetHost, request.TargetPort)
	if plan.Validate() != nil {
		t.Fatal("invalid diagnostic Pod revalidation fixture")
	}
	return plan
}

func diagnosticPodRequest(t *testing.T, service *corev1.Service) toolcontract.DiagnosticPodRequest {
	t.Helper()
	arguments, err := domain.NewActionArguments([]string{"-z", "-v", "-w", "5", "api.team-a.svc", "8443"})
	if err != nil {
		t.Fatalf("NewActionArguments() error = %v", err)
	}
	return toolcontract.DiagnosticPodRequest{
		Scope: liveScope("team-a"), PolicyGeneration: 3, Name: "kupilot-diag-test",
		Service: domain.ResourceRef{APIVersion: "v1", Kind: "Service", Namespace: "team-a", Name: service.Name, UID: string(service.UID), ResourceVersion: service.ResourceVersion},
		Image:   "registry.example/diag@sha256:" + strings.Repeat("a", 64), Executable: "/bin/nc", Arguments: arguments,
		TargetHost: "api.team-a.svc", TargetPort: 8443, Timeout: 10 * time.Second, MaxLines: 20, MaxBytes: 4096,
	}
}

func writeRemoteJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func writeRemoteNotFound(t *testing.T, writer http.ResponseWriter, resource, name string) {
	t.Helper()
	writer.WriteHeader(http.StatusNotFound)
	writeRemoteJSON(t, writer, &metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound, Message: resource + " " + name + " not found"})
}

func assertRemoteKubeClass(t *testing.T, err error, want domain.SafeErrorClass) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want class %q", want)
	}
	classified, ok := err.(interface{ Class() domain.SafeErrorClass })
	if !ok {
		t.Fatalf("error = %T %v, want classified error %q", err, err, want)
	}
	if classified.Class() != want {
		t.Fatalf("error = %T %v, class=%q want %q", err, err, classified.Class(), want)
	}
}

func remoteInt64Pointer(value int64) *int64 { return &value }
