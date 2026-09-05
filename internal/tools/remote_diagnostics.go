package tools

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

var (
	ErrInvalidRemoteDiagnosticDependencies = errors.New("remote diagnostic Tool dependencies are invalid")
	ErrInvalidRemoteDiagnosticRequest      = errors.New("remote diagnostic request is invalid")
	ErrRawRemoteOutputSerializationDenied  = errors.New("raw remote output cannot be serialized")
)

const remoteArchiveBlockBytes = 512

// RemoteOutputStream is a code-owned stream identity. Sequence is assigned by
// the Kubernetes adapter at the synchronized Write boundary.
type RemoteOutputStream string

const (
	RemoteOutputStdout RemoteOutputStream = "stdout"
	RemoteOutputStderr RemoteOutputStream = "stderr"
)

func (stream RemoteOutputStream) valid() bool {
	return stream == RemoteOutputStdout || stream == RemoteOutputStderr
}

// RemoteOutputChunk keeps raw container bytes opaque until the Tool safety
// pipeline applies terminal, Unicode, sensitive-value, line, and byte policy.
type RemoteOutputChunk struct {
	sequence int
	stream   RemoteOutputStream
	content  []byte
}

func NewRemoteOutputChunk(sequence int, stream RemoteOutputStream, content []byte) (RemoteOutputChunk, error) {
	if sequence < 1 || !stream.valid() || len(content) == 0 || len(content) > domain.MaxRemoteDiagnosticBytes {
		return RemoteOutputChunk{}, ErrInvalidRemoteDiagnosticRequest
	}
	return RemoteOutputChunk{sequence: sequence, stream: stream, content: append([]byte(nil), content...)}, nil
}

func (chunk RemoteOutputChunk) Sequence() int              { return chunk.sequence }
func (chunk RemoteOutputChunk) Stream() RemoteOutputStream { return chunk.stream }
func (chunk RemoteOutputChunk) bytes() []byte              { return append([]byte(nil), chunk.content...) }
func (RemoteOutputChunk) String() string                   { return "<bounded raw remote output>" }
func (RemoteOutputChunk) GoString() string                 { return "tools.RemoteOutputChunk{<bounded>}" }
func (RemoteOutputChunk) MarshalJSON() ([]byte, error) {
	return nil, ErrRawRemoteOutputSerializationDenied
}

// PodResolveRequest is the exact safe read needed to bind action authority to
// a live UID, resourceVersion, and container before any exec subresource call.
type PodResolveRequest struct {
	Scope     domain.ClusterScope
	Namespace string
	PodName   string
	Container string
}

func (request PodResolveRequest) Validate() error {
	reference := domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: request.Namespace, Name: request.PodName}
	if request.Scope.Validate() != nil || domain.ValidateLiveResourceRef(reference) != nil ||
		!request.Scope.AllowsReference(reference) || !domain.ValidResourceName(request.Container) {
		return ErrInvalidRemoteDiagnosticRequest
	}
	return nil
}

// ResolvedPod contains only action identity and normalized denied mount roots;
// it never carries a raw Pod or volume source details.
type ResolvedPod struct {
	Reference        domain.ResourceRef
	Container        string
	DeniedMountRoots []string
}

func (pod ResolvedPod) Validate(request PodResolveRequest) error {
	if request.Validate() != nil || pod.Reference.Validate() != nil || pod.Reference.UID == "" || pod.Reference.ResourceVersion == "" ||
		pod.Reference.APIVersion != "v1" || pod.Reference.Kind != "Pod" || pod.Reference.Namespace != request.Namespace ||
		pod.Reference.Name != request.PodName || pod.Container != request.Container || len(pod.DeniedMountRoots) > 64 {
		return ErrInvalidRemoteDiagnosticRequest
	}
	for _, root := range pod.DeniedMountRoots {
		if root == "/" {
			continue
		}
		if normalized, err := domain.NormalizeContainerFilePath(root); err != nil || normalized != root {
			return ErrInvalidRemoteDiagnosticRequest
		}
	}
	return nil
}

func (pod ResolvedPod) PathTouchesDeniedMount(value string) bool {
	for _, root := range pod.DeniedMountRoots {
		if root == "/" || value == root || strings.HasPrefix(value, root+"/") || strings.HasPrefix(root, value+"/") {
			return true
		}
	}
	return false
}

type ServiceResolveRequest struct {
	Scope       domain.ClusterScope
	Namespace   string
	ServiceName string
	Port        uint16
}

func (request ServiceResolveRequest) Validate() error {
	reference := domain.ResourceRef{APIVersion: "v1", Kind: "Service", Namespace: request.Namespace, Name: request.ServiceName}
	returnIfInvalid := request.Scope.Validate() != nil || request.Port == 0 || domain.ValidateLiveResourceRef(reference) != nil || !request.Scope.AllowsReference(reference)
	if returnIfInvalid {
		return ErrInvalidRemoteDiagnosticRequest
	}
	return nil
}

type ResolvedService struct {
	Reference domain.ResourceRef
	Port      uint16
}

func (service ResolvedService) Validate(request ServiceResolveRequest) error {
	if request.Validate() != nil || service.Reference.Validate() != nil || service.Reference.UID == "" || service.Reference.ResourceVersion == "" ||
		service.Reference.APIVersion != "v1" || service.Reference.Kind != "Service" || service.Reference.Namespace != request.Namespace ||
		service.Reference.Name != request.ServiceName || service.Port != request.Port {
		return ErrInvalidRemoteDiagnosticRequest
	}
	return nil
}

type RemoteTargetResolver interface {
	ResolvePod(context.Context, PodResolveRequest) (ResolvedPod, error)
	ResolveService(context.Context, ServiceResolveRequest) (ResolvedService, error)
}

// RemoteCommandRequest is the sole Pod exec adapter input. Command is an argv
// vector; stdin, TTY, and shell are structurally absent and therefore false.
type RemoteCommandRequest struct {
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Pod              domain.ResourceRef
	Container        string
	Executable       string
	Arguments        domain.ActionArguments
	Timeout          time.Duration
	MaxLines         int
	MaxBytes         int
}

func (request RemoteCommandRequest) Validate() error {
	if request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() || request.Pod.Validate() != nil || request.Pod.UID == "" || request.Pod.ResourceVersion == "" ||
		request.Pod.APIVersion != "v1" || request.Pod.Kind != "Pod" || !request.Scope.AllowsReference(request.Pod) ||
		!domain.ValidResourceName(request.Container) || !domain.ValidNoShellRemoteCommand(request.Executable, request.Arguments) ||
		request.Timeout <= 0 || request.Timeout > domain.MaxRequestTimeoutForRemoteDiagnostic() ||
		request.MaxLines < 1 || request.MaxLines > domain.MaxRemoteDiagnosticLines || request.MaxBytes < 1 || request.MaxBytes > domain.MaxRemoteDiagnosticBytes {
		return ErrInvalidRemoteDiagnosticRequest
	}
	return nil
}

type RemoteCommandObservation struct {
	Pod       domain.ResourceRef
	Container string
	Chunks    []RemoteOutputChunk
	ExitCode  int
	Completed bool
	Truncated bool
	LineCount int
	ByteCount int
}

func (observation RemoteCommandObservation) Validate(request RemoteCommandRequest) error {
	if request.Validate() != nil || observation.Pod != request.Pod || observation.Container != request.Container || observation.ExitCode < 0 ||
		observation.LineCount < 0 || observation.LineCount > request.MaxLines || observation.ByteCount < 0 || observation.ByteCount > request.MaxBytes ||
		len(observation.Chunks) > request.MaxBytes || len(observation.Chunks) > domain.MaxRemoteOutputChunks {
		return ErrInvalidRemoteDiagnosticRequest
	}
	bytesSeen := 0
	for index, chunk := range observation.Chunks {
		if chunk.sequence != index+1 || !chunk.stream.valid() || len(chunk.content) == 0 {
			return ErrInvalidRemoteDiagnosticRequest
		}
		bytesSeen += len(chunk.content)
	}
	if bytesSeen != observation.ByteCount || observation.Completed && observation.Truncated {
		return ErrInvalidRemoteDiagnosticRequest
	}
	return nil
}

type RemoteCommandExecutor interface {
	ExecuteRemoteCommand(context.Context, RemoteCommandRequest) (RemoteCommandObservation, error)
}

type DiagnosticPodRequest struct {
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Name             string
	Service          domain.ResourceRef
	Image            string
	Executable       string
	Arguments        domain.ActionArguments
	TargetHost       string
	TargetPort       uint16
	Timeout          time.Duration
	MaxLines         int
	MaxBytes         int
}

func (request DiagnosticPodRequest) Validate() error {
	values := request.Arguments.Values()
	expectedHost := request.Service.Name + "." + request.Service.Namespace + ".svc"
	if request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() || !domain.ValidResourceName(request.Name) ||
		request.Service.Validate() != nil || request.Service.UID == "" || request.Service.ResourceVersion == "" ||
		request.Service.Kind != "Service" || request.Service.APIVersion != "v1" || !request.Scope.AllowsReference(request.Service) ||
		!domain.ValidPinnedContainerImage(request.Image) || request.Executable != "/bin/nc" || !request.Arguments.Valid() ||
		request.TargetHost != expectedHost || !domain.ValidRemoteDiagnosticTarget(request.TargetHost) || request.TargetPort == 0 ||
		len(values) != 6 || values[0] != "-z" || values[1] != "-v" || values[2] != "-w" || values[3] != "5" ||
		values[4] != request.TargetHost || values[5] != strconv.Itoa(int(request.TargetPort)) ||
		request.Timeout <= 0 || request.Timeout > domain.MaxRequestTimeoutForRemoteDiagnostic() ||
		request.MaxLines < 1 || request.MaxLines > domain.MaxRemoteDiagnosticLines || request.MaxBytes < 1 || request.MaxBytes > domain.MaxRemoteDiagnosticBytes {
		return ErrInvalidRemoteDiagnosticRequest
	}
	return nil
}

type DiagnosticPodObservation struct {
	Pod          domain.ResourceRef
	Chunks       []RemoteOutputChunk
	ExitCode     int
	Completed    bool
	Created      bool
	Truncated    bool
	LineCount    int
	ByteCount    int
	Lifecycle    domain.DiagnosticPodLifecycle
	CleanupState domain.DiagnosticPodCleanupState
}

func (observation DiagnosticPodObservation) Validate(request DiagnosticPodRequest) error {
	commandRequest := RemoteCommandRequest{
		Scope: request.Scope, PolicyGeneration: request.PolicyGeneration,
		Pod: observation.Pod, Container: "diagnostic", Executable: request.Executable, Arguments: request.Arguments,
		Timeout: request.Timeout, MaxLines: request.MaxLines, MaxBytes: request.MaxBytes,
	}
	command := RemoteCommandObservation{Pod: observation.Pod, Container: "diagnostic", Chunks: observation.Chunks, ExitCode: observation.ExitCode, Completed: observation.Completed, Truncated: observation.Truncated, LineCount: observation.LineCount, ByteCount: observation.ByteCount}
	if request.Validate() != nil || !observation.Created || observation.Pod.Namespace != request.Service.Namespace || observation.Pod.Name != request.Name ||
		command.Validate(commandRequest) != nil ||
		observation.Lifecycle.Validate() != nil || observation.Lifecycle.Create != domain.DiagnosticPodPhaseCompleted ||
		observation.Lifecycle.Wait != domain.DiagnosticPodPhaseCompleted || observation.Lifecycle.Log != domain.DiagnosticPodPhaseCompleted ||
		observation.Lifecycle.Delete != domain.DiagnosticPodPhaseCompleted || observation.CleanupState != domain.DiagnosticPodCleanupVerified {
		return ErrInvalidRemoteDiagnosticRequest
	}
	return nil
}

type DiagnosticPodRunner interface {
	RunDiagnosticPod(context.Context, DiagnosticPodRequest) (DiagnosticPodObservation, error)
}

// RemoteDiagnosticActionGate owns permission/reviewer/human routing, durable
// approve-once consumption, and the pre-operation audit. Returning an envelope
// is the only result that permits one external attempt.
type RemoteDiagnosticActionGate interface {
	AuthorizeAndConsume(context.Context, domain.RemoteDiagnosticActionPlan) (domain.ActionEnvelope, error)
	RecordOutcome(context.Context, domain.ActionEnvelope, domain.RemoteDiagnosticOutcome) error
}

// RemoteOutputPolicyRequest contains no output or command bytes. It lets the
// Application recheck the exact model-transfer category before Kubernetes I/O
// and again before bounded output can become Evidence or model input.
type RemoteOutputPolicyRequest struct {
	RunID            domain.AgentRunID
	SessionID        domain.SessionID
	Scope            domain.ClusterScope
	PolicyGeneration domain.PolicyGeneration
	Operation        domain.ActionOperation
}

func (request RemoteOutputPolicyRequest) valid() bool {
	if !request.RunID.Valid() || !request.SessionID.Valid() || request.Scope.Validate() != nil || !request.PolicyGeneration.Valid() {
		return false
	}
	switch request.Operation {
	case domain.ActionOperationPodDiagnostic, domain.ActionOperationPodExec,
		domain.ActionOperationContainerFileRead, domain.ActionOperationDiagnosticPod:
		return true
	default:
		return false
	}
}

type RemoteOutputDataPolicy interface {
	AuthorizeRemoteOutput(context.Context, RemoteOutputPolicyRequest) LogPolicyDecision
}

type RemoteDiagnosticToolDependencies struct {
	Resolver       RemoteTargetResolver
	Commands       RemoteCommandExecutor
	DiagnosticPods DiagnosticPodRunner
	ScopeGuard     ScopeGuard
	PolicyGuard    PolicyGenerationGuard
	Actions        RemoteDiagnosticActionGate
	OutputPolicy   RemoteOutputDataPolicy
	EvidenceIDs    EvidenceIDSource
	Text           LogTextProcessor
	Now            func() time.Time
}

func (dependencies RemoteDiagnosticToolDependencies) validate() error {
	if dependencies.Resolver == nil || dependencies.Commands == nil || dependencies.DiagnosticPods == nil ||
		dependencies.ScopeGuard == nil || dependencies.PolicyGuard == nil || dependencies.Actions == nil || dependencies.OutputPolicy == nil || dependencies.EvidenceIDs == nil ||
		dependencies.Text == nil || dependencies.Now == nil || !validRequiredUTCTime(dependencies.Now()) {
		return ErrInvalidRemoteDiagnosticDependencies
	}
	return nil
}

type PodExecTool struct {
	dependencies RemoteDiagnosticToolDependencies
}
type ReadContainerFileTool struct {
	dependencies RemoteDiagnosticToolDependencies
}
type RunDiagnosticPodTool struct {
	dependencies RemoteDiagnosticToolDependencies
}

var _ agent.Tool = (*PodExecTool)(nil)
var _ agent.Tool = (*ReadContainerFileTool)(nil)
var _ agent.Tool = (*RunDiagnosticPodTool)(nil)

func NewPodExecTool(dependencies RemoteDiagnosticToolDependencies) (*PodExecTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidRemoteDiagnosticDependencies
	}
	return &PodExecTool{dependencies: dependencies}, nil
}

func NewReadContainerFileTool(dependencies RemoteDiagnosticToolDependencies) (*ReadContainerFileTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidRemoteDiagnosticDependencies
	}
	return &ReadContainerFileTool{dependencies: dependencies}, nil
}

func NewRunDiagnosticPodTool(dependencies RemoteDiagnosticToolDependencies) (*RunDiagnosticPodTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidRemoteDiagnosticDependencies
	}
	return &RunDiagnosticPodTool{dependencies: dependencies}, nil
}

type remotePodArguments struct {
	Arguments  []string `json:"arguments,omitempty"`
	CommandID  string   `json:"command_id,omitempty"`
	Container  string   `json:"container"`
	Executable string   `json:"executable,omitempty"`
	Namespace  string   `json:"namespace"`
	Path       string   `json:"path,omitempty"`
	PodName    string   `json:"pod_name"`
	Purpose    string   `json:"purpose"`
}

type remoteDiagnosticPodArguments struct {
	DiagnosticID string `json:"diagnostic_id"`
	Purpose      string `json:"purpose"`
}

func (tool *PodExecTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil {
		return ToolResult{}
	}
	return executePodExec(ctx, call, tool.dependencies)
}

func (tool *ReadContainerFileTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil {
		return ToolResult{}
	}
	return executeContainerFileRead(ctx, call, tool.dependencies)
}

func (tool *RunDiagnosticPodTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil {
		return ToolResult{}
	}
	return executeDiagnosticPod(ctx, call, tool.dependencies)
}

func executePodExec(ctx context.Context, call BoundToolCall, dependencies RemoteDiagnosticToolDependencies) ToolResult {
	observed := remoteObservedAt(call, dependencies.Now)
	var arguments remotePodArguments
	if !validRemoteCall(ctx, call, domain.ToolNamePodExec, dependencies) || json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	policy, found := call.RemoteDiagnosticsPolicies().ResolvePodExec(arguments.CommandID)
	actionArguments, argumentErr := domain.NewActionArguments(arguments.Arguments)
	if !found || argumentErr != nil || policy.Executable != arguments.Executable || !equalRemoteArguments(policy.Arguments, actionArguments) {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	timeout, maxLines, maxBytes := boundedRemoteLimits(call, policy.Timeout, policy.MaxLines, policy.MaxBytes)
	operation, risk := domain.ActionOperationPodExec, domain.RiskCritical
	if policy.Class == domain.PodExecPolicyPredefined {
		operation, risk = domain.ActionOperationPodDiagnostic, domain.RiskReview
	}
	if class := authorizeRemoteOutput(ctx, call, operation, dependencies); class != "" {
		return failedResult(call, observed, class)
	}
	request := PodResolveRequest{Scope: call.Scope(), Namespace: arguments.Namespace, PodName: arguments.PodName, Container: arguments.Container}
	resolved, err := dependencies.Resolver.ResolvePod(ctx, request)
	if !remoteCallCurrent(ctx, call, dependencies) {
		return failedResult(call, observed, remoteCallCurrentFailure(ctx))
	}
	if err != nil || resolved.Validate(request) != nil {
		class := classifyFailure(ctx, err)
		if err == nil {
			class = domain.SafeErrorClassInvalidExternalResponse
		}
		return failedResult(call, observed, class)
	}
	parameters := domain.ActionParameters{Kind: domain.ActionParametersRemoteArgv, Container: resolved.Container, Executable: policy.Executable, Arguments: policy.Arguments}
	plan := remotePodPlan(call, resolved.Reference, parameters, operation, risk, timeout, maxLines, maxBytes)
	envelope, class := authorizeRemoteAction(ctx, call, plan, dependencies)
	if class != "" {
		if envelope.Validate() == nil {
			return failAfterRemoteAuthority(ctx, call, observed, envelope, class, operation, dependencies)
		}
		return failedResult(call, observed, class)
	}
	if class = authorizeRemoteOutput(ctx, call, operation, dependencies); class != "" {
		outcome := failedRemoteOutcome(class, false, RemoteCommandObservation{}, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	if class = remoteAuthorityFailure(ctx, call, envelope, dependencies); class != "" {
		return failAfterRemoteAuthority(ctx, call, observed, envelope, class, operation, dependencies)
	}
	requestExec := RemoteCommandRequest{Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Pod: resolved.Reference, Container: resolved.Container, Executable: policy.Executable, Arguments: policy.Arguments, Timeout: timeout, MaxLines: maxLines, MaxBytes: maxBytes}
	result, execErr := dependencies.Commands.ExecuteRemoteCommand(ctx, requestExec)
	return finishRemoteCommand(ctx, call, observed, envelope, result, requestExec, execErr, dependencies, operation, domain.EvidenceCategoryRemoteCommand, podExecEvidenceSourcePath(resolved.Reference), "remote command")
}

func executeContainerFileRead(ctx context.Context, call BoundToolCall, dependencies RemoteDiagnosticToolDependencies) ToolResult {
	observed := remoteObservedAt(call, dependencies.Now)
	var arguments remotePodArguments
	if !validRemoteCall(ctx, call, domain.ToolNameReadContainerFile, dependencies) || json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	policy, found := call.RemoteDiagnosticsPolicies().ContainerFile()
	components, pathErr := domain.ContainerFilePathComponents(arguments.Path)
	if !found || pathErr != nil || !policy.AllowedRoots.Allows(arguments.Path) {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	timeout, maxLines, maxBytes := boundedRemoteLimits(call, policy.Timeout, policy.MaxLines, policy.MaxBytes)
	contentMaxBytes, contentLimitErr := domain.ContainerFileContentLimit(arguments.Path, maxBytes)
	if contentLimitErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted)
	}
	if class := authorizeRemoteOutput(ctx, call, domain.ActionOperationContainerFileRead, dependencies); class != "" {
		return failedResult(call, observed, class)
	}
	resolveRequest := PodResolveRequest{Scope: call.Scope(), Namespace: arguments.Namespace, PodName: arguments.PodName, Container: arguments.Container}
	resolved, err := dependencies.Resolver.ResolvePod(ctx, resolveRequest)
	if !remoteCallCurrent(ctx, call, dependencies) {
		return failedResult(call, observed, remoteCallCurrentFailure(ctx))
	}
	if err != nil || resolved.Validate(resolveRequest) != nil {
		class := classifyFailure(ctx, err)
		if err == nil {
			class = domain.SafeErrorClassInvalidExternalResponse
		}
		return failedResult(call, observed, class)
	}
	if resolved.PathTouchesDeniedMount(arguments.Path) {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	tarArguments, err := domain.ContainerFileReaderArguments(arguments.Path)
	if err != nil {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	parameters := domain.ActionParameters{Kind: domain.ActionParametersContainerFile, Container: resolved.Container, NormalizedPath: arguments.Path, Executable: policy.ReaderExecutable, Arguments: tarArguments}
	plan := remotePodPlan(call, resolved.Reference, parameters, domain.ActionOperationContainerFileRead, domain.RiskReview, timeout, maxLines, maxBytes, contentMaxBytes)
	envelope, class := authorizeRemoteAction(ctx, call, plan, dependencies)
	if class != "" {
		if envelope.Validate() == nil {
			return failAfterRemoteAuthority(ctx, call, observed, envelope, class, domain.ActionOperationContainerFileRead, dependencies)
		}
		return failedResult(call, observed, class)
	}
	if class = authorizeRemoteOutput(ctx, call, domain.ActionOperationContainerFileRead, dependencies); class != "" {
		outcome := failedRemoteOutcome(class, false, RemoteCommandObservation{}, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	if class = remoteAuthorityFailure(ctx, call, envelope, dependencies); class != "" {
		return failAfterRemoteAuthority(ctx, call, observed, envelope, class, domain.ActionOperationContainerFileRead, dependencies)
	}
	requestExec := RemoteCommandRequest{Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Pod: resolved.Reference, Container: envelope.Intent.Parameters.Container, Executable: envelope.Intent.Parameters.Executable, Arguments: envelope.Intent.Parameters.Arguments, Timeout: timeout, MaxLines: maxLines, MaxBytes: maxBytes}
	result, execErr := dependencies.Commands.ExecuteRemoteCommand(ctx, requestExec)
	if !remoteCallCurrent(ctx, call, dependencies) {
		outcome := failedRemoteOutcome(remoteCallCurrentFailure(ctx), true, RemoteCommandObservation{}, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, outcome.ErrorClass)
	}
	if execErr != nil || result.Validate(requestExec) != nil || !result.Completed || result.ExitCode != 0 {
		return finishRemoteCommand(ctx, call, observed, envelope, result, requestExec, execErr, dependencies, domain.ActionOperationContainerFileRead, domain.EvidenceCategoryContainerFile, arguments.Path, "container file")
	}
	if class = authorizeRemoteOutput(ctx, call, domain.ActionOperationContainerFileRead, dependencies); class != "" {
		outcome := failedRemoteOutcome(class, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	if remoteChunksContainStream(result.Chunks, RemoteOutputStderr) {
		outcome := failedRemoteOutcome(domain.SafeErrorClassInvalidExternalResponse, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, domain.SafeErrorClassInvalidExternalResponse)
	}
	archiveBytes := joinRemoteChunks(result.Chunks, RemoteOutputStdout)
	fileBytes, extractErr := extractRegularContainerFile(archiveBytes, components, contentMaxBytes)
	if extractErr != nil {
		outcome := failedRemoteOutcome(domain.SafeErrorClassSensitiveOutputBlocked, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, domain.SafeErrorClassSensitiveOutputBlocked)
	}
	safe, processErr := dependencies.Text.ProcessLines(string(fileBytes), contentMaxBytes)
	if processErr != nil {
		outcome := failedRemoteOutcome(domain.SafeErrorClassSensitiveOutputBlocked, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, domain.SafeErrorClassSensitiveOutputBlocked)
	}
	boundedValue, lineTruncated := boundRemoteLines(safe.Value, maxLines)
	safe.Value = boundedValue
	safe.Truncated = safe.Truncated || lineTruncated
	prepared, outputTruncated, outputClass := fitSuccessfulRemoteResult(call, observed, resolved.Reference, domain.EvidenceCategoryContainerFile, arguments.Path, "container file", safe.Value, safe.RedactionCount, safe.Truncated, dependencies)
	if outputClass != "" {
		outcome := failedRemoteOutcome(outputClass, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return prepared
	}
	if class = authorizeRemoteOutput(ctx, call, domain.ActionOperationContainerFileRead, dependencies); class != "" {
		outcome := failedRemoteOutcome(class, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	outcome := domain.RemoteDiagnosticOutcome{State: domain.RemoteDiagnosticOutcomeSucceeded, OutputBytes: len(fileBytes), OutputLines: countRemoteLines(safe.Value), Truncated: outputTruncated, CleanupState: domain.DiagnosticPodCleanupNotNeeded}
	if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
		return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
	}
	if class = authorizeRemoteOutput(ctx, call, domain.ActionOperationContainerFileRead, dependencies); class != "" {
		return failedResult(call, observed, class)
	}
	return prepared
}

func executeDiagnosticPod(ctx context.Context, call BoundToolCall, dependencies RemoteDiagnosticToolDependencies) ToolResult {
	observed := remoteObservedAt(call, dependencies.Now)
	var arguments remoteDiagnosticPodArguments
	if !validRemoteCall(ctx, call, domain.ToolNameRunDiagnosticPod, dependencies) || json.Unmarshal([]byte(call.ArgumentsJSON()), &arguments) != nil || arguments.Purpose != call.Purpose() {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	policy, found := call.RemoteDiagnosticsPolicies().ResolveDiagnosticPod(arguments.DiagnosticID)
	host, hostErr := policy.TargetHost()
	executable, commandArguments, commandErr := policy.Command()
	if !found || hostErr != nil || commandErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassPolicyDenied)
	}
	timeout, maxLines, maxBytes := boundedRemoteLimits(call, policy.Timeout, policy.MaxLines, policy.MaxBytes)
	if class := authorizeRemoteOutput(ctx, call, domain.ActionOperationDiagnosticPod, dependencies); class != "" {
		return failedResult(call, observed, class)
	}
	resolveRequest := ServiceResolveRequest{Scope: call.Scope(), Namespace: policy.Namespace, ServiceName: policy.ServiceName, Port: policy.Port}
	service, err := dependencies.Resolver.ResolveService(ctx, resolveRequest)
	if !remoteCallCurrent(ctx, call, dependencies) {
		return failedResult(call, observed, remoteCallCurrentFailure(ctx))
	}
	if err != nil || service.Validate(resolveRequest) != nil {
		class := classifyFailure(ctx, err)
		if err == nil {
			class = domain.SafeErrorClassInvalidExternalResponse
		}
		return failedResult(call, observed, class)
	}
	parameters := domain.ActionParameters{Kind: domain.ActionParametersRemoteArgv, Container: "diagnostic", Executable: executable, Arguments: commandArguments}
	plan := diagnosticPodPlan(call, service.Reference, parameters, policy, timeout, maxLines, maxBytes)
	envelope, class := authorizeRemoteAction(ctx, call, plan, dependencies)
	if class != "" {
		if envelope.Validate() == nil {
			return failAfterRemoteAuthority(ctx, call, observed, envelope, class, domain.ActionOperationDiagnosticPod, dependencies)
		}
		return failedResult(call, observed, class)
	}
	if class = authorizeRemoteOutput(ctx, call, domain.ActionOperationDiagnosticPod, dependencies); class != "" {
		outcome := failedDiagnosticOutcome(class, false, RemoteCommandObservation{}, domain.NotAttemptedDiagnosticPodLifecycle(), domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	if class = remoteAuthorityFailure(ctx, call, envelope, dependencies); class != "" {
		return failAfterRemoteAuthority(ctx, call, observed, envelope, class, domain.ActionOperationDiagnosticPod, dependencies)
	}
	name := diagnosticPodName(call.InvocationID())
	request := DiagnosticPodRequest{Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Name: name, Service: service.Reference, Image: policy.Image, Executable: executable, Arguments: commandArguments, TargetHost: host, TargetPort: policy.Port, Timeout: timeout, MaxLines: maxLines, MaxBytes: maxBytes}
	result, runErr := dependencies.DiagnosticPods.RunDiagnosticPod(ctx, request)
	if !remoteCallCurrent(ctx, call, dependencies) {
		lifecycle, cleanup := normalizedDiagnosticFailureState(result)
		outcome := failedDiagnosticOutcome(remoteCallCurrentFailure(ctx), true, RemoteCommandObservation{}, lifecycle, cleanup)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, outcome.ErrorClass)
	}
	if runErr != nil || result.Validate(request) != nil {
		class := classifyFailure(ctx, runErr)
		if runErr == nil {
			class = domain.SafeErrorClassInvalidExternalResponse
		}
		lifecycle, cleanup := normalizedDiagnosticFailureState(result)
		unknown := cleanup == domain.DiagnosticPodCleanupUnknown || diagnosticPodLifecycleUnknown(lifecycle)
		outcome := failedDiagnosticOutcome(class, unknown, RemoteCommandObservation{ByteCount: result.ByteCount, LineCount: result.LineCount, Truncated: result.Truncated}, lifecycle, cleanup)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, outcome.ErrorClass)
	}
	state := domain.RemoteDiagnosticOutcomeSucceeded
	class = ""
	if !result.Completed || result.ExitCode != 0 {
		state, class = domain.RemoteDiagnosticOutcomeFailed, domain.SafeErrorClassInvalidExternalResponse
	}
	if state != domain.RemoteDiagnosticOutcomeSucceeded {
		outcome := domain.RemoteDiagnosticOutcome{State: state, ErrorClass: class, OutputBytes: result.ByteCount, OutputLines: result.LineCount, Truncated: result.Truncated, Lifecycle: result.Lifecycle, CleanupState: result.CleanupState}
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	if class = authorizeRemoteOutput(ctx, call, domain.ActionOperationDiagnosticPod, dependencies); class != "" {
		outcome := failedDiagnosticOutcome(class, false, RemoteCommandObservation{ByteCount: result.ByteCount, LineCount: result.LineCount, Truncated: result.Truncated}, result.Lifecycle, result.CleanupState)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	safe, err := sanitizeRemoteChunks(result.Chunks, dependencies.Text, maxLines, maxBytes)
	if err != nil {
		outcome := failedDiagnosticOutcome(domain.SafeErrorClassSensitiveOutputBlocked, false, RemoteCommandObservation{ByteCount: result.ByteCount, LineCount: result.LineCount, Truncated: result.Truncated}, result.Lifecycle, result.CleanupState)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, domain.SafeErrorClassSensitiveOutputBlocked)
	}
	prepared, outputTruncated, outputClass := fitSuccessfulRemoteResult(call, observed, service.Reference, domain.EvidenceCategoryDiagnosticPod, diagnosticPodEvidenceSourcePath(service.Reference), "diagnostic Pod", safe.value, safe.redactions, safe.truncated || result.Truncated, dependencies)
	if outputClass != "" {
		outcome := failedDiagnosticOutcome(outputClass, false, RemoteCommandObservation{ByteCount: result.ByteCount, LineCount: result.LineCount, Truncated: result.Truncated}, result.Lifecycle, result.CleanupState)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return prepared
	}
	if class = authorizeRemoteOutput(ctx, call, domain.ActionOperationDiagnosticPod, dependencies); class != "" {
		outcome := failedDiagnosticOutcome(class, false, RemoteCommandObservation{ByteCount: result.ByteCount, LineCount: result.LineCount, Truncated: result.Truncated}, result.Lifecycle, result.CleanupState)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	outcome := domain.RemoteDiagnosticOutcome{State: state, ErrorClass: class, OutputBytes: result.ByteCount, OutputLines: result.LineCount, Truncated: outputTruncated, Lifecycle: result.Lifecycle, CleanupState: result.CleanupState}
	if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
		return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
	}
	if class = authorizeRemoteOutput(ctx, call, domain.ActionOperationDiagnosticPod, dependencies); class != "" {
		return failedResult(call, observed, class)
	}
	return prepared
}

func validRemoteCall(ctx context.Context, call BoundToolCall, name domain.ToolName, dependencies RemoteDiagnosticToolDependencies) bool {
	return dependencies.validate() == nil && call.Validate() == nil && call.Name() == name && call.Version() == agent.ToolCatalogVersion &&
		remoteCallCurrent(ctx, call, dependencies)
}

func authorizeRemoteOutput(ctx context.Context, call BoundToolCall, operation domain.ActionOperation, dependencies RemoteDiagnosticToolDependencies) domain.SafeErrorClass {
	request := RemoteOutputPolicyRequest{RunID: call.RunID(), SessionID: call.SessionID(), Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Operation: operation}
	if !request.valid() {
		return domain.SafeErrorClassPolicyDenied
	}
	if !remoteCallCurrent(ctx, call, dependencies) {
		return remoteCallCurrentFailure(ctx)
	}
	class := classifyLogPolicyDecision(dependencies.OutputPolicy.AuthorizeRemoteOutput(ctx, request))
	if !remoteCallCurrent(ctx, call, dependencies) {
		return remoteCallCurrentFailure(ctx)
	}
	return class
}

func remoteCallCurrent(ctx context.Context, call BoundToolCall, dependencies RemoteDiagnosticToolDependencies) bool {
	return ctx != nil && ctx.Err() == nil && dependencies.ScopeGuard.Current(ctx, call.Scope()) &&
		dependencies.PolicyGuard.CurrentPolicyGeneration(ctx, call.PolicyGeneration())
}

func remoteCallCurrentFailure(ctx context.Context) domain.SafeErrorClass {
	if ctx != nil && ctx.Err() != nil {
		return classifyFailure(ctx, ctx.Err())
	}
	return domain.SafeErrorClassStaleScope
}

func authorizeRemoteAction(ctx context.Context, call BoundToolCall, plan domain.RemoteDiagnosticActionPlan, dependencies RemoteDiagnosticToolDependencies) (domain.ActionEnvelope, domain.SafeErrorClass) {
	if plan.Validate() != nil {
		return domain.ActionEnvelope{}, domain.SafeErrorClassStaleScope
	}
	if !remoteCallCurrent(ctx, call, dependencies) {
		return domain.ActionEnvelope{}, remoteCallCurrentFailure(ctx)
	}
	envelope, err := dependencies.Actions.AuthorizeAndConsume(ctx, plan)
	if err != nil {
		if envelope.Validate() == nil && plan.MatchesIntent(envelope.Intent) {
			return envelope, classifyFailure(ctx, err)
		}
		return domain.ActionEnvelope{}, classifyFailure(ctx, err)
	}
	if envelope.Validate() != nil || !plan.MatchesIntent(envelope.Intent) {
		return domain.ActionEnvelope{}, domain.SafeErrorClassStaleScope
	}
	if !remoteCallCurrent(ctx, call, dependencies) {
		return envelope, remoteCallCurrentFailure(ctx)
	}
	return envelope, ""
}

func remoteAuthorityFailure(ctx context.Context, call BoundToolCall, envelope domain.ActionEnvelope, dependencies RemoteDiagnosticToolDependencies) domain.SafeErrorClass {
	if envelope.Validate() != nil {
		return domain.SafeErrorClassPolicyDenied
	}
	if !remoteCallCurrent(ctx, call, dependencies) {
		return remoteCallCurrentFailure(ctx)
	}
	current := dependencies.Now()
	if !validRequiredUTCTime(current) {
		return domain.SafeErrorClassInternal
	}
	if !current.Before(envelope.ExpiresAt) {
		return domain.SafeErrorClassTimeout
	}
	return ""
}

func failAfterRemoteAuthority(ctx context.Context, call BoundToolCall, observed time.Time, envelope domain.ActionEnvelope, class domain.SafeErrorClass, operation domain.ActionOperation, dependencies RemoteDiagnosticToolDependencies) ToolResult {
	outcome := failedRemoteOutcome(class, false, RemoteCommandObservation{}, domain.DiagnosticPodCleanupNotNeeded)
	if operation == domain.ActionOperationDiagnosticPod {
		outcome = failedDiagnosticOutcome(class, false, RemoteCommandObservation{}, domain.NotAttemptedDiagnosticPodLifecycle(), domain.DiagnosticPodCleanupNotNeeded)
	}
	if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
		return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
	}
	return failedResult(call, observed, class)
}

func remotePodPlan(call BoundToolCall, target domain.ResourceRef, parameters domain.ActionParameters, operation domain.ActionOperation, risk domain.RiskClass, timeout time.Duration, maxLines, maxBytes int, maximumOutput ...int) domain.RemoteDiagnosticActionPlan {
	riskSummary, data, verify := domain.PodExecRiskSummary, domain.ActionDataContainerOutput, domain.PodExecVerificationPlanID
	if operation == domain.ActionOperationPodDiagnostic {
		riskSummary = domain.PodDiagnosticRiskSummary
	}
	if operation == domain.ActionOperationContainerFileRead {
		riskSummary, data, verify = domain.ContainerFileRiskSummary, domain.ActionDataFileOutput, domain.ContainerFileVerificationPlanID
	}
	output := maxBytes
	if len(maximumOutput) == 1 {
		output = maximumOutput[0]
	}
	return domain.RemoteDiagnosticActionPlan{RunID: call.RunID(), SessionID: call.SessionID(), Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Operation: operation,
		Target: domain.ActionTarget{Resource: target, Subresource: "exec", Fingerprint: string(parameters.Digest())}, Parameters: parameters, Risk: risk,
		Effect: domain.CapabilityEffectRemoteExecute, DataCategories: data, AllowedSinks: domain.ActionSinkTerminal | domain.ActionSinkModel | domain.ActionSinkKubernetesAPI,
		NetworkEffects: domain.ActionNetworkKubernetesAPI | domain.ActionNetworkRemotePod, NetworkDestinationHash: domain.RemotePodNetworkDestinationHash(target, parameters.Container), Limits: domain.ActionLimits{Timeout: timeout, MaximumItems: 1, MaximumLines: maxLines, MaximumBytes: maxBytes, MaximumOutput: output},
		VerificationPlan: verify, ReasonSummary: call.Purpose(), RiskSummary: riskSummary}
}

func diagnosticPodPlan(call BoundToolCall, target domain.ResourceRef, parameters domain.ActionParameters, policy domain.DiagnosticPodPolicy, timeout time.Duration, maxLines, maxBytes int) domain.RemoteDiagnosticActionPlan {
	fingerprint := policy.ActionFingerprint(parameters)
	return domain.RemoteDiagnosticActionPlan{RunID: call.RunID(), SessionID: call.SessionID(), Scope: call.Scope(), PolicyGeneration: call.PolicyGeneration(), Operation: domain.ActionOperationDiagnosticPod,
		Target: domain.ActionTarget{Resource: target, Fingerprint: string(fingerprint)}, Parameters: parameters, Risk: domain.RiskCritical, Effect: domain.CapabilityEffectClusterMutation,
		DataCategories: domain.ActionDataContainerOutput | domain.ActionDataResourceMetadata, AllowedSinks: domain.ActionSinkTerminal | domain.ActionSinkModel | domain.ActionSinkKubernetesAPI,
		NetworkEffects: domain.ActionNetworkKubernetesAPI | domain.ActionNetworkRemotePod, NetworkDestinationHash: domain.DiagnosticPodNetworkDestinationHash(target, target.Name+"."+target.Namespace+".svc", policy.Port), Limits: domain.ActionLimits{Timeout: timeout, MaximumItems: 1, MaximumLines: maxLines, MaximumBytes: maxBytes, MaximumOutput: maxBytes},
		VerificationPlan: domain.DiagnosticPodVerificationPlanID, ReasonSummary: call.Purpose(), RiskSummary: domain.DiagnosticPodRiskSummary}
}

func boundedRemoteLimits(call BoundToolCall, timeout time.Duration, maxLines, maxBytes int) (time.Duration, int, int) {
	ceilings := call.Ceilings()
	if ceilings.RequestTimeout < timeout {
		timeout = ceilings.RequestTimeout
	}
	if ceilings.MaxLogLines < maxLines {
		maxLines = ceilings.MaxLogLines
	}
	if ceilings.MaxLogBytes < maxBytes {
		maxBytes = ceilings.MaxLogBytes
	}
	if ceilings.MaxResultBytes < maxBytes {
		maxBytes = ceilings.MaxResultBytes
	}
	return timeout, maxLines, maxBytes
}

func equalRemoteArguments(left, right domain.ActionArguments) bool {
	leftValues, rightValues := left.Values(), right.Values()
	if len(leftValues) != len(rightValues) {
		return false
	}
	for index := range leftValues {
		if leftValues[index] != rightValues[index] {
			return false
		}
	}
	return true
}

func diagnosticPodName(id domain.ToolInvocationID) string {
	value := strings.ReplaceAll(string(id), "-", "")
	if len(value) > 20 {
		value = value[len(value)-20:]
	}
	return "kupilot-diag-" + value
}

type sanitizedRemote struct {
	value      string
	redactions int
	truncated  bool
}

func sanitizeRemoteChunks(chunks []RemoteOutputChunk, processor LogTextProcessor, maxLines, maxBytes int) (sanitizedRemote, error) {
	var builder strings.Builder
	for _, chunk := range chunks {
		if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "\n") {
			builder.WriteByte('\n')
		}
		builder.WriteString("[")
		builder.WriteString(string(chunk.Stream()))
		builder.WriteString("] ")
		builder.Write(chunk.bytes())
	}
	processed, err := processor.ProcessLines(builder.String(), maxBytes)
	if err != nil {
		return sanitizedRemote{}, ErrInvalidRemoteDiagnosticRequest
	}
	value, lineTruncated := boundRemoteLines(processed.Value, maxLines)
	return sanitizedRemote{value: value, redactions: processed.RedactionCount, truncated: processed.Truncated || lineTruncated}, nil
}

func boundRemoteLines(value string, maximum int) (string, bool) {
	if value == "" || maximum < 1 {
		return "", value != ""
	}
	newlines := 0
	for index, current := range []byte(value) {
		if current != '\n' {
			continue
		}
		newlines++
		if newlines == maximum && index+1 < len(value) {
			return value[:index+1], true
		}
	}
	return value, false
}

func finishRemoteCommand(ctx context.Context, call BoundToolCall, observed time.Time, envelope domain.ActionEnvelope, result RemoteCommandObservation, request RemoteCommandRequest, execErr error, dependencies RemoteDiagnosticToolDependencies, operation domain.ActionOperation, category domain.EvidenceCategory, sourcePath, label string) ToolResult {
	if !remoteCallCurrent(ctx, call, dependencies) {
		outcome := failedRemoteOutcome(remoteCallCurrentFailure(ctx), true, RemoteCommandObservation{}, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, outcome.ErrorClass)
	}
	if execErr != nil || result.Validate(request) != nil {
		class := classifyFailure(ctx, execErr)
		if execErr == nil {
			class = domain.SafeErrorClassInvalidExternalResponse
		}
		outcome := failedRemoteOutcome(class, true, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	if !result.Completed || result.ExitCode != 0 {
		class := domain.SafeErrorClassInvalidExternalResponse
		outcome := failedRemoteOutcome(class, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	if class := authorizeRemoteOutput(ctx, call, operation, dependencies); class != "" {
		outcome := failedRemoteOutcome(class, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	safe, err := sanitizeRemoteChunks(result.Chunks, dependencies.Text, request.MaxLines, request.MaxBytes)
	if err != nil {
		outcome := failedRemoteOutcome(domain.SafeErrorClassSensitiveOutputBlocked, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, domain.SafeErrorClassSensitiveOutputBlocked)
	}
	prepared, outputTruncated, outputClass := fitSuccessfulRemoteResult(call, observed, result.Pod, category, sourcePath, label, safe.value, safe.redactions, result.Truncated || safe.truncated, dependencies)
	if outputClass != "" {
		outcome := failedRemoteOutcome(outputClass, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return prepared
	}
	if class := authorizeRemoteOutput(ctx, call, operation, dependencies); class != "" {
		outcome := failedRemoteOutcome(class, false, result, domain.DiagnosticPodCleanupNotNeeded)
		if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
			return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
		}
		return failedResult(call, observed, class)
	}
	outcome := domain.RemoteDiagnosticOutcome{State: domain.RemoteDiagnosticOutcomeSucceeded, OutputBytes: result.ByteCount, OutputLines: result.LineCount, Truncated: outputTruncated, CleanupState: domain.DiagnosticPodCleanupNotNeeded}
	if dependencies.Actions.RecordOutcome(ctx, envelope, outcome) != nil {
		return failedResult(call, observed, domain.SafeErrorClassPersistenceUnavailable)
	}
	if class := authorizeRemoteOutput(ctx, call, operation, dependencies); class != "" {
		return failedResult(call, observed, class)
	}
	return prepared
}

func failedRemoteOutcome(class domain.SafeErrorClass, unknown bool, result RemoteCommandObservation, cleanup domain.DiagnosticPodCleanupState) domain.RemoteDiagnosticOutcome {
	if !class.Valid() {
		class = domain.SafeErrorClassInternal
	}
	state := domain.RemoteDiagnosticOutcomeFailed
	if unknown {
		state = domain.RemoteDiagnosticOutcomeUnknown
	}
	return domain.RemoteDiagnosticOutcome{State: state, ErrorClass: class, OutputBytes: result.ByteCount, OutputLines: result.LineCount, Truncated: result.Truncated, CleanupState: cleanup}
}

func failedDiagnosticOutcome(class domain.SafeErrorClass, unknown bool, result RemoteCommandObservation, lifecycle domain.DiagnosticPodLifecycle, cleanup domain.DiagnosticPodCleanupState) domain.RemoteDiagnosticOutcome {
	outcome := failedRemoteOutcome(class, unknown, result, cleanup)
	outcome.Lifecycle = lifecycle
	return outcome
}

func unknownDiagnosticPodLifecycle(created bool) domain.DiagnosticPodLifecycle {
	if !created {
		return domain.NotAttemptedDiagnosticPodLifecycle()
	}
	return domain.DiagnosticPodLifecycle{
		Create: domain.DiagnosticPodPhaseCompleted,
		Wait:   domain.DiagnosticPodPhaseUnknown,
		Log:    domain.DiagnosticPodPhaseNotAttempted,
		Delete: domain.DiagnosticPodPhaseUnknown,
	}
}

func normalizedDiagnosticFailureState(result DiagnosticPodObservation) (domain.DiagnosticPodLifecycle, domain.DiagnosticPodCleanupState) {
	cleanup := result.CleanupState
	if cleanup != domain.DiagnosticPodCleanupNotNeeded && cleanup != domain.DiagnosticPodCleanupVerified && cleanup != domain.DiagnosticPodCleanupUnknown {
		cleanup = domain.DiagnosticPodCleanupUnknown
	}
	lifecycle := result.Lifecycle
	if lifecycle.Validate() != nil {
		lifecycle = unknownDiagnosticPodLifecycle(result.Created)
		cleanup = domain.DiagnosticPodCleanupUnknown
	}
	return lifecycle, cleanup
}

func diagnosticPodLifecycleUnknown(lifecycle domain.DiagnosticPodLifecycle) bool {
	return lifecycle.Create == domain.DiagnosticPodPhaseUnknown || lifecycle.Wait == domain.DiagnosticPodPhaseUnknown ||
		lifecycle.Log == domain.DiagnosticPodPhaseUnknown || lifecycle.Delete == domain.DiagnosticPodPhaseUnknown
}

type safeRemoteData struct {
	Content            string `json:"content"`
	ContentFingerprint string `json:"content_fingerprint"`
	ByteCount          int    `json:"byte_count"`
	LineCount          int    `json:"line_count"`
	RedactionCount     int    `json:"redaction_count"`
	Truncated          bool   `json:"truncated"`
}

func fitSuccessfulRemoteResult(call BoundToolCall, observed time.Time, resource domain.ResourceRef, category domain.EvidenceCategory, sourcePath, label, content string, redactions int, truncated bool, dependencies RemoteDiagnosticToolDependencies) (ToolResult, bool, domain.SafeErrorClass) {
	dataGuard, dataGuardErr := security.NewOutputGuard(domain.MaxToolResultBytes)
	completeGuard, completeGuardErr := security.NewOutputGuard(call.Ceilings().MaxResultBytes)
	if dataGuardErr != nil || completeGuardErr != nil {
		return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted), true, domain.SafeErrorClassBudgetExhausted
	}
	candidate := content
	outputTrimmed := false
	for attempts := 0; attempts <= 128; attempts++ {
		partial := truncated || outputTrimmed
		data := safeRemoteData{Content: candidate, ContentFingerprint: domain.SHA256Hex(candidate), ByteCount: len(candidate), LineCount: countRemoteLines(candidate), RedactionCount: redactions, Truncated: partial}
		fact, factTruncated := boundedEvidenceFact(fmt.Sprintf(
			"The bounded %s projection for %s %s contains %d sanitized line(s) and %d byte(s); fingerprint %s.",
			label, resource.Kind, resource.Name, data.LineCount, data.ByteCount, data.ContentFingerprint,
		))
		templates := []evidenceTemplate{{category: category, resource: resource, policyVersion: domain.RemoteDiagnosticsPolicyVersion, fact: fact, sourcePath: sourcePath, severity: stableSeverity(domain.EvidenceSeverityInfo), redactionCount: redactions, truncated: partial || factTruncated, partial: partial}}
		raw, encodeErr := json.Marshal(data)
		encoded := ""
		if encodeErr == nil {
			encoded, encodeErr = dataGuard.CanonicalizeJSONObject(raw)
		}
		status, reason := domain.ToolResultStatusSuccess, ""
		if partial {
			status, reason = domain.ToolResultStatusPartial, outputLimitReason
		}
		result := ToolResult{InvocationID: call.InvocationID(), Name: call.Name(), Version: call.Version(), Scope: call.Scope().Snapshot(), ObservedAt: observed, Status: status, DataJSON: encoded, Evidence: previewEvidence(call, observed, templates),
			Truncation: domain.ToolResultTruncation{Truncated: partial, Reason: reason, ReturnedCount: data.LineCount}}
		measured, measureErr := measureResult(call, result)
		if encodeErr == nil && measureErr == nil && completeGuard.Allows(measured.Truncation.ReturnedBytes) {
			evidence, evidenceErr := materializeEvidence(ResourceToolDependencies{EvidenceIDs: dependencies.EvidenceIDs}, call, observed, templates)
			if evidenceErr != nil {
				return failedResult(call, observed, domain.SafeErrorClassInternal), true, domain.SafeErrorClassInternal
			}
			result.Evidence = evidence
			measured, measureErr = measureResult(call, result)
			if measureErr != nil || !completeGuard.Allows(measured.Truncation.ReturnedBytes) {
				return failedResult(call, observed, domain.SafeErrorClassInternal), true, domain.SafeErrorClassInternal
			}
			return measured, partial, ""
		}
		if candidate == "" {
			break
		}
		outputTrimmed = true
		candidate = shorterRemoteContent(candidate)
	}
	return failedResult(call, observed, domain.SafeErrorClassBudgetExhausted), true, domain.SafeErrorClassBudgetExhausted
}

func shorterRemoteContent(value string) string {
	if value == "" {
		return ""
	}
	maximum := len(value) * 7 / 8
	if maximum >= len(value) {
		maximum = len(value) - 1
	}
	for maximum > 0 && !utf8.ValidString(value[:maximum]) {
		maximum--
	}
	return value[:maximum]
}

func podExecEvidenceSourcePath(resource domain.ResourceRef) string {
	return "api/v1/namespaces/" + resource.Namespace + "/pods/" + resource.Name + "/exec"
}

func diagnosticPodEvidenceSourcePath(resource domain.ResourceRef) string {
	return "api/v1/namespaces/" + resource.Namespace + "/services/" + resource.Name + "#diagnostic_pod"
}

func extractRegularContainerFile(value []byte, components []string, maximum int) ([]byte, error) {
	if len(components) == 0 || maximum < 1 {
		return nil, ErrInvalidRemoteDiagnosticRequest
	}
	reader := tar.NewReader(bytes.NewReader(value))
	expected := 0
	var content []byte
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || header == nil || header.Name == "" || strings.HasPrefix(header.Name, "/") {
			return nil, ErrInvalidRemoteDiagnosticRequest
		}
		name := strings.TrimSuffix(header.Name, "/")
		if expected >= len(components) || name != components[expected] || header.Linkname != "" {
			return nil, ErrInvalidRemoteDiagnosticRequest
		}
		if expected < len(components)-1 {
			if header.Typeflag != tar.TypeDir || header.Size != 0 || header.Devmajor != 0 || header.Devminor != 0 {
				return nil, ErrInvalidRemoteDiagnosticRequest
			}
			expected++
			continue
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA || header.Size < 0 || header.Size > int64(maximum) || header.Devmajor != 0 || header.Devminor != 0 {
			return nil, ErrInvalidRemoteDiagnosticRequest
		}
		content, err = io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
		if err != nil || len(content) > maximum {
			return nil, ErrInvalidRemoteDiagnosticRequest
		}
		expected++
	}
	if expected != len(components) || content == nil {
		return nil, ErrInvalidRemoteDiagnosticRequest
	}
	contentBlocks := (len(content) + remoteArchiveBlockBytes - 1) / remoteArchiveBlockBytes
	expectedArchiveBytes := (len(components) + 2 + contentBlocks) * remoteArchiveBlockBytes
	if len(value) != expectedArchiveBytes {
		return nil, ErrInvalidRemoteDiagnosticRequest
	}
	return content, nil
}

func remoteChunksContainStream(chunks []RemoteOutputChunk, stream RemoteOutputStream) bool {
	for _, chunk := range chunks {
		if chunk.Stream() == stream {
			return true
		}
	}
	return false
}

func joinRemoteChunks(chunks []RemoteOutputChunk, stream RemoteOutputStream) []byte {
	var result []byte
	for _, chunk := range chunks {
		if chunk.Stream() == stream {
			result = append(result, chunk.bytes()...)
		}
	}
	return result
}

func countRemoteLines(value string) int {
	if value == "" {
		return 0
	}
	lines := strings.Count(value, "\n")
	if !strings.HasSuffix(value, "\n") {
		lines++
	}
	return lines
}

func remoteObservedAt(call BoundToolCall, now func() time.Time) time.Time {
	return observedAt(ResourceToolDependencies{Now: now}, call)
}
