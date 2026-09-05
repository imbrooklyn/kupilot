package kube

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
	toolcontract "github.com/imbrooklyn/kupilot/internal/tools"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	httpstreamspdy "k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	remotecommandclient "k8s.io/client-go/tools/remotecommand"
	clienttransport "k8s.io/client-go/transport"
	clientspdy "k8s.io/client-go/transport/spdy"
	clientexec "k8s.io/client-go/util/exec"
)

const (
	diagnosticContainerName  = "diagnostic"
	diagnosticServiceAccount = "default"
	diagnosticCleanupTimeout = 5 * time.Second
)

var errRemoteOutputLimit = errors.New("remote output limit reached")

func (reader *ToolResourceReader) ResolvePod(ctx context.Context, request toolcontract.PodResolveRequest) (toolcontract.ResolvedPod, error) {
	if err := reader.validateContext(ctx, request.Scope, "resolve_remote_pod"); err != nil {
		return toolcontract.ResolvedPod{}, err
	}
	if request.Validate() != nil {
		return toolcontract.ResolvedPod{}, newKubeSafeError(ClassInvalidInput, "kubernetes_remote_pod_request_invalid", "resolve_remote_pod", "The remote Pod target is invalid.")
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, "resolve_remote_pod")
	if err != nil {
		return toolcontract.ResolvedPod{}, err
	}
	pod, rawErr := bundle.typed.CoreV1().Pods(request.Namespace).Get(ctx, request.PodName, metav1.GetOptions{})
	if err := resourceCallError(ctx, rawErr, "resolve_remote_pod"); err != nil {
		return toolcontract.ResolvedPod{}, err
	}
	resolved, projectErr := projectResolvedPod(pod, request)
	if projectErr != nil {
		return toolcontract.ResolvedPod{}, invalidKubernetesProjectionError("resolve_remote_pod")
	}
	return resolved, nil
}

func projectResolvedPod(pod *corev1.Pod, request toolcontract.PodResolveRequest) (toolcontract.ResolvedPod, error) {
	if pod == nil || pod.Name != request.PodName || pod.Namespace != request.Namespace || pod.UID == "" || pod.ResourceVersion == "" {
		return toolcontract.ResolvedPod{}, errors.New("invalid Pod identity")
	}
	var container *corev1.Container
	for index := range pod.Spec.Containers {
		if pod.Spec.Containers[index].Name == request.Container {
			container = &pod.Spec.Containers[index]
			break
		}
	}
	if container == nil {
		return toolcontract.ResolvedPod{}, errors.New("container is absent")
	}
	volumeKinds := make(map[string]bool, len(pod.Spec.Volumes))
	for _, volume := range pod.Spec.Volumes {
		if _, duplicate := volumeKinds[volume.Name]; duplicate || volume.Name == "" {
			return toolcontract.ResolvedPod{}, errors.New("invalid Pod volume identity")
		}
		sensitive := volume.Secret != nil || volume.ConfigMap != nil || volume.Projected != nil || volume.DownwardAPI != nil || volume.HostPath != nil || volume.CSI != nil
		if volume.Projected != nil {
			for _, source := range volume.Projected.Sources {
				if source.ServiceAccountToken != nil || source.Secret != nil {
					sensitive = true
				}
			}
		}
		volumeKinds[volume.Name] = sensitive
	}
	deniedSet := make(map[string]struct{}, len(container.VolumeMounts)+len(container.VolumeDevices))
	for _, mount := range container.VolumeMounts {
		sensitive, found := volumeKinds[mount.Name]
		if !found {
			return toolcontract.ResolvedPod{}, errors.New("Pod mount references an unknown volume")
		}
		if !sensitive {
			continue
		}
		normalized, normalizeErr := normalizedDeniedMountRoot(mount.MountPath)
		if normalizeErr != nil {
			return toolcontract.ResolvedPod{}, errors.New("sensitive Pod mount path is invalid")
		}
		deniedSet[normalized] = struct{}{}
	}
	for _, device := range container.VolumeDevices {
		sensitive, found := volumeKinds[device.Name]
		if !found {
			return toolcontract.ResolvedPod{}, errors.New("Pod device references an unknown volume")
		}
		if !sensitive {
			continue
		}
		normalized, normalizeErr := normalizedDeniedMountRoot(device.DevicePath)
		if normalizeErr != nil {
			return toolcontract.ResolvedPod{}, errors.New("sensitive Pod device path is invalid")
		}
		deniedSet[normalized] = struct{}{}
	}
	denied := make([]string, 0, len(deniedSet))
	for root := range deniedSet {
		denied = append(denied, root)
	}
	sort.Strings(denied)
	resolved := toolcontract.ResolvedPod{Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: string(pod.UID), ResourceVersion: pod.ResourceVersion}, Container: request.Container, DeniedMountRoots: denied}
	if resolved.Validate(request) != nil {
		return toolcontract.ResolvedPod{}, errors.New("invalid projected Pod")
	}
	return resolved, nil
}

func normalizedDeniedMountRoot(value string) (string, error) {
	trimmed := strings.TrimRight(value, "/")
	if trimmed == "" && strings.HasPrefix(value, "/") {
		return "/", nil
	}
	normalized, err := domain.NormalizeContainerFilePath(trimmed)
	if err != nil || normalized != trimmed {
		return "", errors.New("invalid denied mount root")
	}
	return normalized, nil
}

func (reader *ToolResourceReader) ResolveService(ctx context.Context, request toolcontract.ServiceResolveRequest) (toolcontract.ResolvedService, error) {
	if err := reader.validateContext(ctx, request.Scope, "resolve_diagnostic_service"); err != nil {
		return toolcontract.ResolvedService{}, err
	}
	if request.Validate() != nil {
		return toolcontract.ResolvedService{}, newKubeSafeError(ClassInvalidInput, "kubernetes_diagnostic_service_invalid", "resolve_diagnostic_service", "The diagnostic Service target is invalid.")
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, "resolve_diagnostic_service")
	if err != nil {
		return toolcontract.ResolvedService{}, err
	}
	service, rawErr := bundle.typed.CoreV1().Services(request.Namespace).Get(ctx, request.ServiceName, metav1.GetOptions{})
	if err := resourceCallError(ctx, rawErr, "resolve_diagnostic_service"); err != nil {
		return toolcontract.ResolvedService{}, err
	}
	if service == nil || service.UID == "" || service.ResourceVersion == "" || service.Spec.Type == corev1.ServiceTypeExternalName || service.Spec.ExternalName != "" || len(service.Spec.Selector) == 0 {
		return toolcontract.ResolvedService{}, invalidKubernetesProjectionError("resolve_diagnostic_service")
	}
	found := false
	for _, port := range service.Spec.Ports {
		if port.Port == int32(request.Port) {
			found = true
			break
		}
	}
	if !found {
		return toolcontract.ResolvedService{}, newKubeSafeError(ClassPolicyDenied, "kubernetes_diagnostic_port_denied", "resolve_diagnostic_service", "The configured diagnostic Service port is not present.")
	}
	result := toolcontract.ResolvedService{Reference: domain.ResourceRef{APIVersion: "v1", Kind: "Service", Namespace: service.Namespace, Name: service.Name, UID: string(service.UID), ResourceVersion: service.ResourceVersion}, Port: request.Port}
	if result.Validate(request) != nil {
		return toolcontract.ResolvedService{}, invalidKubernetesProjectionError("resolve_diagnostic_service")
	}
	return result, nil
}

// RevalidateRemoteDiagnosticAction performs the Application-owned fresh target
// check after a decision and before durable approval consumption. It returns
// only success or a safe error; raw Kubernetes objects remain adapter-local.
func (reader *ToolResourceReader) RevalidateRemoteDiagnosticAction(ctx context.Context, plan domain.RemoteDiagnosticActionPlan) error {
	if plan.Validate() != nil {
		return newKubeSafeError(ClassInvalidInput, "kubernetes_remote_action_invalid", "revalidate_remote_diagnostic_action", "The remote diagnostic action is invalid.")
	}
	switch plan.Operation {
	case domain.ActionOperationPodDiagnostic, domain.ActionOperationPodExec, domain.ActionOperationContainerFileRead:
		request := toolcontract.PodResolveRequest{
			Scope: plan.Scope, Namespace: plan.Target.Resource.Namespace,
			PodName: plan.Target.Resource.Name, Container: plan.Parameters.Container,
		}
		resolved, err := reader.ResolvePod(ctx, request)
		if err != nil {
			return err
		}
		if resolved.Reference != plan.Target.Resource ||
			plan.Operation == domain.ActionOperationContainerFileRead && resolved.PathTouchesDeniedMount(plan.Parameters.NormalizedPath) {
			return newKubeSafeError(ClassConflict, "kubernetes_remote_target_changed", "revalidate_remote_diagnostic_action", "The remote diagnostic Pod target changed before approval consumption.")
		}
	case domain.ActionOperationDiagnosticPod:
		values := plan.Parameters.Arguments.Values()
		if len(values) != 6 {
			return newKubeSafeError(ClassInvalidInput, "kubernetes_diagnostic_target_invalid", "revalidate_remote_diagnostic_action", "The diagnostic Service target is invalid.")
		}
		port, err := strconv.ParseUint(values[5], 10, 16)
		if err != nil || port == 0 {
			return newKubeSafeError(ClassInvalidInput, "kubernetes_diagnostic_target_invalid", "revalidate_remote_diagnostic_action", "The diagnostic Service target is invalid.")
		}
		request := toolcontract.ServiceResolveRequest{
			Scope: plan.Scope, Namespace: plan.Target.Resource.Namespace,
			ServiceName: plan.Target.Resource.Name, Port: uint16(port),
		}
		resolved, err := reader.ResolveService(ctx, request)
		if err != nil {
			return err
		}
		if resolved.Reference != plan.Target.Resource || values[4] != resolved.Reference.Name+"."+resolved.Reference.Namespace+".svc" {
			return newKubeSafeError(ClassConflict, "kubernetes_diagnostic_target_changed", "revalidate_remote_diagnostic_action", "The diagnostic Service target changed before approval consumption.")
		}
	default:
		return newKubeSafeError(ClassPolicyDenied, "kubernetes_remote_action_denied", "revalidate_remote_diagnostic_action", "The remote diagnostic action is not admitted.")
	}
	return nil
}

func (reader *ToolResourceReader) ExecuteRemoteCommand(ctx context.Context, request toolcontract.RemoteCommandRequest) (toolcontract.RemoteCommandObservation, error) {
	if err := reader.validateContext(ctx, request.Scope, "execute_remote_command"); err != nil {
		return toolcontract.RemoteCommandObservation{}, err
	}
	if request.Validate() != nil {
		return toolcontract.RemoteCommandObservation{}, newKubeSafeError(ClassInvalidInput, "kubernetes_remote_command_invalid", "execute_remote_command", "The remote command request is invalid.")
	}
	if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
		return toolcontract.RemoteCommandObservation{}, newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "execute_remote_command", "The remote command policy changed before execution.")
	}
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, "execute_remote_command")
	if err != nil {
		return toolcontract.RemoteCommandObservation{}, err
	}
	pod, rawErr := bundle.typed.CoreV1().Pods(request.Pod.Namespace).Get(ctx, request.Pod.Name, metav1.GetOptions{})
	if err := resourceCallError(ctx, rawErr, "revalidate_remote_command"); err != nil {
		return toolcontract.RemoteCommandObservation{}, err
	}
	resolveRequest := toolcontract.PodResolveRequest{Scope: request.Scope, Namespace: request.Pod.Namespace, PodName: request.Pod.Name, Container: request.Container}
	resolved, projectErr := projectResolvedPod(pod, resolveRequest)
	if projectErr != nil || resolved.Reference.UID != request.Pod.UID || resolved.Reference.ResourceVersion != request.Pod.ResourceVersion {
		return toolcontract.RemoteCommandObservation{}, newKubeSafeError(ClassConflict, "kubernetes_remote_target_changed", "revalidate_remote_command", "The remote Pod changed before execution.")
	}
	if !reader.policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
		return toolcontract.RemoteCommandObservation{}, newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "execute_remote_command", "The remote command policy changed before execution.")
	}
	observation, execErr := executeSPDYRemoteCommand(ctx, bundle, request)
	if !reader.policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
		return observation, newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "execute_remote_command", "The remote command policy changed before its result was accepted.")
	}
	return observation, execErr
}

func executeSPDYRemoteCommand(ctx context.Context, bundle *ClientBundle, request toolcontract.RemoteCommandRequest) (toolcontract.RemoteCommandObservation, error) {
	if bundle == nil || bundle.typed == nil || bundle.streamConfig == nil || !bundle.beginRemoteAttempt() {
		return toolcontract.RemoteCommandObservation{}, gatewayUnavailableError("execute_remote_command")
	}
	defer bundle.endRemoteAttempt()
	operationContext, cancel := context.WithTimeout(ctx, request.Timeout)
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		select {
		case <-bundle.remoteClosed:
			cancel()
		case <-operationContext.Done():
		}
	}()
	defer func() {
		cancel()
		<-ownerDone
	}()
	command := append([]string{request.Executable}, request.Arguments.Values()...)
	url := bundle.typed.CoreV1().RESTClient().Post().Resource("pods").Namespace(request.Pod.Namespace).Name(request.Pod.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: request.Container, Command: command, Stdin: false, Stdout: true, Stderr: true, TTY: false}, scheme.ParameterCodec).URL()
	query := url.Query()
	query.Set("timeout", request.Timeout.String())
	url.RawQuery = query.Encode()
	transport, upgrader, transportErr := remoteCommandTransports(bundle.streamConfig, bundle.exec)
	if transportErr != nil {
		return toolcontract.RemoteCommandObservation{}, newKubeSafeError(ClassConfigurationInvalid, "kubernetes_remote_transport_invalid", "execute_remote_command", "The Kubernetes remote command transport is unavailable.")
	}
	defer closeIdleConnections(transport)
	executor, rawErr := remotecommandclient.NewSPDYExecutorRejectRedirects(transport, upgrader, http.MethodPost, url)
	if rawErr != nil {
		return toolcontract.RemoteCommandObservation{}, newKubeSafeError(ClassConfigurationInvalid, "kubernetes_remote_transport_invalid", "execute_remote_command", "The Kubernetes remote command transport is unavailable.")
	}
	collector := newRemoteOutputCollector(request.MaxLines, request.MaxBytes, cancel)
	rawErr = executor.StreamWithContext(operationContext, remotecommandclient.StreamOptions{Stdin: nil, Stdout: collector.writer(toolcontract.RemoteOutputStdout), Stderr: collector.writer(toolcontract.RemoteOutputStderr), Tty: false})
	observation := collector.observation(request.Pod, request.Container)
	if collector.limitReached() {
		return observation, newKubeSafeError(ClassBudgetExhausted, "kubernetes_remote_output_limit", "execute_remote_command", "The remote command exceeded its output limit.")
	}
	if operationContext.Err() != nil {
		return observation, classifyContextError(operationContext.Err(), "execute_remote_command")
	}
	if rawErr == nil {
		observation.Completed = true
		return observation, nil
	}
	var exitError clientexec.ExitError
	if errors.As(rawErr, &exitError) {
		observation.Completed = true
		observation.ExitCode = exitError.ExitStatus()
		return observation, nil
	}
	return observation, classifyKubernetesError("execute_remote_command", rawErr)
}

func remoteCommandTransports(config *rest.Config, manager *execCredentialManager) (http.RoundTripper, clientspdy.Upgrader, error) {
	transportConfig, err := config.TransportConfig()
	if err != nil {
		return nil, nil, err
	}
	transportConfig.WrapTransport, transportConfig.Transport = nil, nil
	if manager != nil {
		transportConfig.TLS.GetCertHolder = &clienttransport.GetCertHolder{GetCert: manager.cachedCertificate}
	}
	tlsConfig, err := clienttransport.TLSConfigFor(transportConfig)
	if err != nil {
		return nil, nil, err
	}
	proxy := http.ProxyFromEnvironment
	if transportConfig.Proxy != nil {
		proxy = transportConfig.Proxy
	}
	upgrade, err := httpstreamspdy.NewRoundTripperWithConfig(httpstreamspdy.RoundTripperConfig{TLS: tlsConfig, Proxier: proxy, PingPeriod: 5 * time.Second})
	if err != nil {
		return nil, nil, err
	}
	var wrapper http.RoundTripper = upgrade
	if manager != nil {
		wrapper = &execCredentialRoundTripper{manager: manager, base: wrapper}
	}
	wrapper, err = clienttransport.HTTPWrappersForConfig(transportConfig, wrapper)
	if err != nil {
		return nil, nil, err
	}
	return wrapper, upgrade, nil
}

type remoteOutputCollector struct {
	mu             sync.Mutex
	chunks         []toolcontract.RemoteOutputChunk
	lines          int
	bytes          int
	maxLines       int
	maxBytes       int
	limited        bool
	cancel         context.CancelFunc
	stdoutLineOpen bool
	stderrLineOpen bool
}

type remoteStreamWriter struct {
	collector *remoteOutputCollector
	stream    toolcontract.RemoteOutputStream
}

func newRemoteOutputCollector(maxLines, maxBytes int, cancel context.CancelFunc) *remoteOutputCollector {
	return &remoteOutputCollector{maxLines: maxLines, maxBytes: maxBytes, cancel: cancel}
}
func (collector *remoteOutputCollector) writer(stream toolcontract.RemoteOutputStream) io.Writer {
	return &remoteStreamWriter{collector: collector, stream: stream}
}
func (writer *remoteStreamWriter) Write(value []byte) (int, error) {
	writer.collector.mu.Lock()
	defer writer.collector.mu.Unlock()
	if writer.collector.limited {
		return 0, errRemoteOutputLimit
	}
	if len(writer.collector.chunks) >= domain.MaxRemoteOutputChunks {
		writer.collector.limited = true
		writer.collector.cancel()
		return 0, errRemoteOutputLimit
	}
	lineOpen := &writer.collector.stdoutLineOpen
	if writer.stream == toolcontract.RemoteOutputStderr {
		lineOpen = &writer.collector.stderrLineOpen
	} else if writer.stream != toolcontract.RemoteOutputStdout {
		return 0, toolcontract.ErrInvalidRemoteDiagnosticRequest
	}
	accepted := 0
	for accepted < len(value) && writer.collector.bytes < writer.collector.maxBytes {
		if !*lineOpen {
			if writer.collector.lines >= writer.collector.maxLines {
				break
			}
			writer.collector.lines++
			*lineOpen = true
		}
		current := value[accepted]
		accepted++
		writer.collector.bytes++
		if current == '\n' {
			*lineOpen = false
		}
	}
	if accepted > 0 {
		candidate := value[:accepted]
		chunk, err := toolcontract.NewRemoteOutputChunk(len(writer.collector.chunks)+1, writer.stream, candidate)
		if err != nil {
			return 0, err
		}
		writer.collector.chunks = append(writer.collector.chunks, chunk)
	}
	if accepted < len(value) {
		writer.collector.limited = true
		writer.collector.cancel()
		return accepted, errRemoteOutputLimit
	}
	return accepted, nil
}
func (collector *remoteOutputCollector) limitReached() bool {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return collector.limited
}
func (collector *remoteOutputCollector) observation(pod domain.ResourceRef, container string) toolcontract.RemoteCommandObservation {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return toolcontract.RemoteCommandObservation{Pod: pod, Container: container, Chunks: append([]toolcontract.RemoteOutputChunk(nil), collector.chunks...), Truncated: collector.limited, LineCount: collector.lines, ByteCount: collector.bytes}
}

func bytesCount(value []byte, wanted byte) int {
	count := 0
	for _, current := range value {
		if current == wanted {
			count++
		}
	}
	return count
}
func (reader *ToolResourceReader) RunDiagnosticPod(ctx context.Context, request toolcontract.DiagnosticPodRequest) (toolcontract.DiagnosticPodObservation, error) {
	if err := reader.validateContext(ctx, request.Scope, "run_diagnostic_pod"); err != nil {
		return toolcontract.DiagnosticPodObservation{}, err
	}
	if request.Validate() != nil {
		return toolcontract.DiagnosticPodObservation{}, newKubeSafeError(ClassInvalidInput, "kubernetes_diagnostic_pod_invalid", "run_diagnostic_pod", "The diagnostic Pod request is invalid.")
	}
	if reader.policyGuard == nil || !reader.policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
		return notAttemptedDiagnosticPodObservation(), newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "run_diagnostic_pod", "The diagnostic Pod policy changed before creation.")
	}
	operationContext, cancelOperation := context.WithTimeout(ctx, request.Timeout)
	defer cancelOperation()
	bundle, err := reader.gateway.resourceBundle(reader.client, request.Scope, "run_diagnostic_pod")
	if err != nil {
		return notAttemptedDiagnosticPodObservation(), err
	}
	if bundle == nil || bundle.typed == nil || !bundle.beginRemoteAttempt() {
		return notAttemptedDiagnosticPodObservation(), gatewayUnavailableError("run_diagnostic_pod")
	}
	defer bundle.endRemoteAttempt()
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		select {
		case <-bundle.remoteClosed:
			cancelOperation()
		case <-operationContext.Done():
		}
	}()
	defer func() {
		cancelOperation()
		<-ownerDone
	}()
	service, rawErr := bundle.typed.CoreV1().Services(request.Service.Namespace).Get(operationContext, request.Service.Name, metav1.GetOptions{})
	if err := resourceCallError(operationContext, rawErr, "revalidate_diagnostic_service"); err != nil {
		return notAttemptedDiagnosticPodObservation(), err
	}
	if service == nil || service.Name != request.Service.Name || service.Namespace != request.Service.Namespace ||
		string(service.UID) != request.Service.UID || service.ResourceVersion != request.Service.ResourceVersion ||
		len(service.Spec.Selector) == 0 || !serviceHasPort(service, request.TargetPort) {
		return notAttemptedDiagnosticPodObservation(), newKubeSafeError(ClassConflict, "kubernetes_diagnostic_target_changed", "revalidate_diagnostic_service", "The diagnostic Service changed before Pod creation.")
	}
	if err := operationContext.Err(); err != nil {
		return notAttemptedDiagnosticPodObservation(), classifyContextError(err, "run_diagnostic_pod")
	}
	if !reader.policyGuard.CurrentPolicyGeneration(operationContext, request.PolicyGeneration) {
		return notAttemptedDiagnosticPodObservation(), newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "run_diagnostic_pod", "The diagnostic Pod policy changed before creation.")
	}
	pod := diagnosticPodFor(request)
	lifecycle := domain.NotAttemptedDiagnosticPodLifecycle()
	created, createErr := bundle.typed.CoreV1().Pods(request.Service.Namespace).Create(operationContext, pod, metav1.CreateOptions{FieldManager: "kupilot"})
	if createErr != nil {
		if diagnosticCreateDefinitelyRejected(createErr) {
			lifecycle.Create = domain.DiagnosticPodPhaseFailed
			return toolcontract.DiagnosticPodObservation{Lifecycle: lifecycle, CleanupState: domain.DiagnosticPodCleanupNotNeeded}, classifyKubernetesError("create_diagnostic_pod", createErr)
		}
		lifecycle.Create = domain.DiagnosticPodPhaseUnknown
		deletePhase, cleanup := cleanupAmbiguousDiagnosticPod(bundle, request, "")
		lifecycle.Delete = deletePhase
		return toolcontract.DiagnosticPodObservation{Lifecycle: lifecycle, CleanupState: cleanup}, classifyKubernetesError("create_diagnostic_pod", createErr)
	}
	lifecycle.Create = domain.DiagnosticPodPhaseCompleted
	if created == nil || created.UID == "" || created.ResourceVersion == "" || created.Name != request.Name || created.Namespace != request.Service.Namespace || !diagnosticPodMatchesRequest(created, request) {
		deletePhase, cleanup := domain.DiagnosticPodPhaseNotAttempted, domain.DiagnosticPodCleanupUnknown
		if created != nil && created.Name == request.Name && created.Namespace == request.Service.Namespace && created.UID != "" {
			deletePhase, cleanup = deleteDiagnosticPod(bundle, request, string(created.UID))
		} else {
			lifecycle.Create = domain.DiagnosticPodPhaseUnknown
			deletePhase, cleanup = cleanupAmbiguousDiagnosticPod(bundle, request, "")
		}
		lifecycle.Delete = deletePhase
		return toolcontract.DiagnosticPodObservation{Created: lifecycle.Create == domain.DiagnosticPodPhaseCompleted, Lifecycle: lifecycle, CleanupState: cleanup}, invalidKubernetesProjectionError("create_diagnostic_pod")
	}
	uid := string(created.UID)
	observation := toolcontract.DiagnosticPodObservation{Created: true, Pod: domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: created.Namespace, Name: created.Name, UID: uid, ResourceVersion: created.ResourceVersion}, Lifecycle: lifecycle}
	if operationContext.Err() != nil || !reader.policyGuard.CurrentPolicyGeneration(operationContext, request.PolicyGeneration) {
		observation.Lifecycle.Delete, observation.CleanupState = deleteDiagnosticPod(bundle, request, uid)
		if operationContext.Err() != nil {
			return observation, classifyContextError(operationContext.Err(), "run_diagnostic_pod")
		}
		return observation, newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "run_diagnostic_pod", "The diagnostic Pod policy changed after creation.")
	}
	terminal, waitErr := waitDiagnosticPod(operationContext, bundle, request, uid, reader.policyGuard)
	if terminal != nil {
		observation.Lifecycle.Wait = domain.DiagnosticPodPhaseCompleted
		observation.ExitCode, observation.Completed = diagnosticExit(terminal)
		if !reader.policyGuard.CurrentPolicyGeneration(operationContext, request.PolicyGeneration) {
			observation.Lifecycle.Log = domain.DiagnosticPodPhaseFailed
			waitErr = newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "read_diagnostic_pod_log", "The diagnostic Pod policy changed before logs were read.")
		} else if content, logErr := readDiagnosticPodLog(operationContext, bundle, request); logErr == nil && reader.policyGuard.CurrentPolicyGeneration(operationContext, request.PolicyGeneration) {
			observation.Lifecycle.Log = domain.DiagnosticPodPhaseCompleted
			if len(content) > 0 {
				chunk, chunkErr := toolcontract.NewRemoteOutputChunk(1, toolcontract.RemoteOutputStdout, content)
				if chunkErr != nil {
					observation.Lifecycle.Log = domain.DiagnosticPodPhaseUnknown
					waitErr = invalidKubernetesProjectionError("read_diagnostic_pod_log")
				} else {
					observation.Chunks = []toolcontract.RemoteOutputChunk{chunk}
					observation.ByteCount = len(content)
					observation.LineCount = remoteOutputLineCount(content)
				}
			}
		} else if waitErr == nil {
			if logErr == nil {
				logErr = newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "read_diagnostic_pod_log", "The diagnostic Pod policy changed before logs were accepted.")
			}
			observation.Lifecycle.Log = diagnosticPodFailurePhase(logErr)
			waitErr = logErr
		}
	} else if waitErr != nil {
		observation.Lifecycle.Wait = diagnosticPodFailurePhase(waitErr)
	}
	observation.Lifecycle.Delete, observation.CleanupState = deleteDiagnosticPod(bundle, request, uid)
	if waitErr != nil {
		return observation, waitErr
	}
	if observation.CleanupState != domain.DiagnosticPodCleanupVerified {
		return observation, newKubeSafeError(ClassUnavailable, "kubernetes_diagnostic_cleanup_unknown", "delete_diagnostic_pod", "Diagnostic Pod cleanup could not be verified.")
	}
	return observation, nil
}

func notAttemptedDiagnosticPodObservation() toolcontract.DiagnosticPodObservation {
	return toolcontract.DiagnosticPodObservation{
		Lifecycle:    domain.NotAttemptedDiagnosticPodLifecycle(),
		CleanupState: domain.DiagnosticPodCleanupNotNeeded,
	}
}

func diagnosticPodFor(request toolcontract.DiagnosticPodRequest) *corev1.Pod {
	falseValue, trueValue := false, true
	one := int64(1)
	preemptionPolicy := corev1.PreemptNever
	deadline := int64((request.Timeout + time.Second - 1) / time.Second)
	command := append([]string{request.Executable}, request.Arguments.Values()...)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: request.Name, Namespace: request.Service.Namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "kupilot", "kupilot.io/purpose": "diagnostic"}, Annotations: map[string]string{"kupilot.io/target-digest": diagnosticPodIdentityDigest(request)}}, Spec: corev1.PodSpec{
		AutomountServiceAccountToken: &falseValue, ServiceAccountName: diagnosticServiceAccount, RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline, HostNetwork: false, HostPID: false, HostIPC: false,
		DNSPolicy: corev1.DNSClusterFirst, SchedulerName: corev1.DefaultSchedulerName, EnableServiceLinks: &falseValue, PreemptionPolicy: &preemptionPolicy,
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &trueValue, RunAsUser: &one, RunAsGroup: &one, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers: []corev1.Container{{Name: diagnosticContainerName, Image: request.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: command,
			SecurityContext:        &corev1.SecurityContext{AllowPrivilegeEscalation: &falseValue, ReadOnlyRootFilesystem: &trueValue, RunAsNonRoot: &trueValue, RunAsUser: &one, RunAsGroup: &one, Privileged: &falseValue, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			TerminationMessagePath: corev1.TerminationMessagePathDefault, TerminationMessagePolicy: corev1.TerminationMessageReadFile,
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("10m"), corev1.ResourceMemory: apiresource.MustParse("16Mi"), corev1.ResourceEphemeralStorage: apiresource.MustParse("16Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: apiresource.MustParse("100m"), corev1.ResourceMemory: apiresource.MustParse("64Mi"), corev1.ResourceEphemeralStorage: apiresource.MustParse("64Mi")}}}},
		TerminationGracePeriodSeconds: &one,
	}}
}

func diagnosticPodMatchesRequest(pod *corev1.Pod, request toolcontract.DiagnosticPodRequest) bool {
	if pod == nil || request.Validate() != nil {
		return false
	}
	want := diagnosticPodFor(request)
	if pod.Name != want.Name || pod.Namespace != want.Namespace || pod.GenerateName != "" ||
		len(pod.OwnerReferences) != 0 || len(pod.Finalizers) != 0 ||
		!apiequality.Semantic.DeepEqual(pod.Labels, want.Labels) || !apiequality.Semantic.DeepEqual(pod.Annotations, want.Annotations) {
		return false
	}
	gotSpec := pod.Spec.DeepCopy()
	wantSpec := want.Spec.DeepCopy()
	if gotSpec == nil || wantSpec == nil || !validDiagnosticDefaultTolerations(gotSpec.Tolerations) ||
		gotSpec.PriorityClassName != "" || gotSpec.Priority != nil && *gotSpec.Priority != 0 {
		return false
	}
	// NodeName is assigned by the scheduler. The API server commonly injects
	// only the two fixed NoExecute tolerations below. No other admitted field
	// may differ from the runtime-owned PodSpec.
	gotSpec.NodeName = ""
	gotSpec.Tolerations = nil
	gotSpec.Priority = nil
	return apiequality.Semantic.DeepEqual(*gotSpec, *wantSpec)
}

func validDiagnosticDefaultTolerations(values []corev1.Toleration) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.Operator != corev1.TolerationOpExists || value.Effect != corev1.TaintEffectNoExecute ||
			value.Value != "" || value.TolerationSeconds == nil || *value.TolerationSeconds != 300 ||
			(value.Key != corev1.TaintNodeNotReady && value.Key != corev1.TaintNodeUnreachable) {
			return false
		}
		if _, duplicate := seen[value.Key]; duplicate {
			return false
		}
		seen[value.Key] = struct{}{}
	}
	return true
}

func diagnosticPodIdentityDigest(request toolcontract.DiagnosticPodRequest) string {
	values := []string{
		request.Name, request.Scope.Context, request.Scope.Namespace, string(request.Scope.NamespaceAccess), strconv.FormatInt(request.Scope.Generation, 10),
		strconv.FormatInt(int64(request.PolicyGeneration), 10), request.Service.APIVersion, request.Service.Kind, request.Service.Namespace,
		request.Service.Name, request.Service.UID, request.Service.ResourceVersion, request.Image,
		request.Executable, request.TargetHost, strconv.Itoa(int(request.TargetPort)), request.Timeout.String(), strconv.Itoa(request.MaxLines), strconv.Itoa(request.MaxBytes),
	}
	values = append(values, request.Arguments.Values()...)
	var builder strings.Builder
	builder.WriteString("kupilot.diagnostic-pod-identity/v1\n")
	for _, value := range values {
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	return domain.SHA256Hex(builder.String())
}

func diagnosticCreateDefinitelyRejected(err error) bool {
	return apierrors.IsAlreadyExists(err) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) ||
		apierrors.IsInvalid(err) || apierrors.IsBadRequest(err) || apierrors.IsMethodNotSupported(err) ||
		apierrors.IsNotAcceptable(err) || apierrors.IsUnsupportedMediaType(err)
}

func serviceHasPort(service *corev1.Service, port uint16) bool {
	if service == nil || service.Spec.Type == corev1.ServiceTypeExternalName || service.Spec.ExternalName != "" {
		return false
	}
	for _, current := range service.Spec.Ports {
		if current.Port == int32(port) {
			return true
		}
	}
	return false
}

func waitDiagnosticPod(ctx context.Context, bundle *ClientBundle, request toolcontract.DiagnosticPodRequest, uid string, policyGuard toolcontract.PolicyGenerationGuard) (*corev1.Pod, error) {
	for {
		if policyGuard == nil || !policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
			return nil, newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "wait_diagnostic_pod", "The diagnostic Pod policy changed while waiting.")
		}
		pod, err := bundle.typed.CoreV1().Pods(request.Service.Namespace).Get(ctx, request.Name, metav1.GetOptions{})
		if err != nil {
			return nil, classifyKubernetesError("wait_diagnostic_pod", err)
		}
		if policyGuard == nil || !policyGuard.CurrentPolicyGeneration(ctx, request.PolicyGeneration) {
			return nil, newKubeSafeError(ClassStaleScope, "kubernetes_policy_generation_stale", "wait_diagnostic_pod", "The diagnostic Pod policy changed while waiting.")
		}
		if pod == nil || string(pod.UID) != uid || !diagnosticPodMatchesRequest(pod, request) {
			return nil, newKubeSafeError(ClassConflict, "kubernetes_diagnostic_pod_changed", "wait_diagnostic_pod", "The diagnostic Pod identity changed.")
		}
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			return pod, nil
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, classifyContextError(ctx.Err(), "wait_diagnostic_pod")
		case <-timer.C:
		}
	}
}

func diagnosticExit(pod *corev1.Pod) (int, bool) {
	if pod == nil {
		return 0, false
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name == diagnosticContainerName && status.State.Terminated != nil {
			code := int(status.State.Terminated.ExitCode)
			if code < 0 {
				return 0, false
			}
			return code, true
		}
	}
	return 0, false
}

func readDiagnosticPodLog(ctx context.Context, bundle *ClientBundle, request toolcontract.DiagnosticPodRequest) ([]byte, error) {
	limitBytes := int64(request.MaxBytes) + 1
	stream, err := bundle.typed.CoreV1().Pods(request.Service.Namespace).GetLogs(request.Name, &corev1.PodLogOptions{Container: diagnosticContainerName, Follow: false, Timestamps: false, LimitBytes: &limitBytes}).Stream(ctx)
	if err != nil {
		return nil, classifyKubernetesError("read_diagnostic_pod_log", err)
	}
	defer stream.Close()
	content, err := io.ReadAll(io.LimitReader(stream, int64(request.MaxBytes)+1))
	if err != nil {
		return nil, classifyKubernetesError("read_diagnostic_pod_log", err)
	}
	if len(content) > request.MaxBytes || remoteOutputLineCount(content) > request.MaxLines {
		return nil, newKubeSafeError(ClassBudgetExhausted, "kubernetes_diagnostic_output_limit", "read_diagnostic_pod_log", "The diagnostic Pod exceeded its output limit.")
	}
	return content, nil
}

func remoteOutputLineCount(value []byte) int {
	if len(value) == 0 {
		return 0
	}
	lines := bytesCount(value, '\n')
	if value[len(value)-1] != '\n' {
		lines++
	}
	return lines
}

func diagnosticPodFailurePhase(err error) domain.DiagnosticPodPhaseState {
	if err == nil {
		return domain.DiagnosticPodPhaseUnknown
	}
	return domain.DiagnosticPodPhaseFailed
}

func deleteDiagnosticPod(bundle *ClientBundle, request toolcontract.DiagnosticPodRequest, uid string) (domain.DiagnosticPodPhaseState, domain.DiagnosticPodCleanupState) {
	if uid == "" {
		return domain.DiagnosticPodPhaseNotAttempted, domain.DiagnosticPodCleanupUnknown
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), diagnosticCleanupTimeout)
	defer cancel()
	uidValue := types.UID(uid)
	grace := int64(1)
	err := bundle.typed.CoreV1().Pods(request.Service.Namespace).Delete(cleanupContext, request.Name, metav1.DeleteOptions{GracePeriodSeconds: &grace, Preconditions: &metav1.Preconditions{UID: &uidValue}})
	if apierrors.IsNotFound(err) {
		return domain.DiagnosticPodPhaseCompleted, domain.DiagnosticPodCleanupVerified
	}
	definitelyRejected := diagnosticDeleteDefinitelyRejected(err)
	for cleanupContext.Err() == nil {
		pod, verificationErr := bundle.typed.CoreV1().Pods(request.Service.Namespace).Get(cleanupContext, request.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(verificationErr) || verificationErr == nil && pod != nil && string(pod.UID) != uid {
			return domain.DiagnosticPodPhaseCompleted, domain.DiagnosticPodCleanupVerified
		}
		if verificationErr != nil {
			return domain.DiagnosticPodPhaseUnknown, domain.DiagnosticPodCleanupUnknown
		}
		if definitelyRejected {
			return domain.DiagnosticPodPhaseFailed, domain.DiagnosticPodCleanupUnknown
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-cleanupContext.Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
	return domain.DiagnosticPodPhaseUnknown, domain.DiagnosticPodCleanupUnknown
}

func diagnosticDeleteDefinitelyRejected(err error) bool {
	return apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || apierrors.IsInvalid(err) ||
		apierrors.IsBadRequest(err) || apierrors.IsMethodNotSupported(err) || apierrors.IsNotAcceptable(err) ||
		apierrors.IsUnsupportedMediaType(err)
}

func cleanupAmbiguousDiagnosticPod(bundle *ClientBundle, request toolcontract.DiagnosticPodRequest, knownUID string) (domain.DiagnosticPodPhaseState, domain.DiagnosticPodCleanupState) {
	cleanupContext, cancel := context.WithTimeout(context.Background(), diagnosticCleanupTimeout)
	defer cancel()
	pod, err := bundle.typed.CoreV1().Pods(request.Service.Namespace).Get(cleanupContext, request.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return domain.DiagnosticPodPhaseNotAttempted, domain.DiagnosticPodCleanupVerified
	}
	targetDigest := diagnosticPodIdentityDigest(request)
	if err != nil || pod == nil || pod.UID == "" || pod.ResourceVersion == "" ||
		pod.Labels["app.kubernetes.io/managed-by"] != "kupilot" || pod.Labels["kupilot.io/purpose"] != "diagnostic" ||
		pod.Annotations["kupilot.io/target-digest"] != targetDigest || !diagnosticPodMatchesRequest(pod, request) ||
		(knownUID != "" && string(pod.UID) != knownUID) {
		return domain.DiagnosticPodPhaseNotAttempted, domain.DiagnosticPodCleanupUnknown
	}
	return deleteDiagnosticPod(bundle, request, string(pod.UID))
}

var _ toolcontract.RemoteTargetResolver = (*ToolResourceReader)(nil)
var _ toolcontract.RemoteCommandExecutor = (*ToolResourceReader)(nil)
var _ toolcontract.DiagnosticPodRunner = (*ToolResourceReader)(nil)
