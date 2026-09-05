package domain

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// ActionEnvelopeSchemaVersion identifies the current immutable envelope shape.
	ActionEnvelopeSchemaVersion = "kupilot.action-envelope/v1"
	// ActionDigestVersion identifies the fixed-order length-prefixed encoding.
	ActionDigestVersion = "kupilot.action-digest/v1"
	// ActionPolicyVersion identifies the currently implemented action policy.
	ActionPolicyVersion = "kupilot.action-policy/2026-09-04"
	// ActionApprovalTTL is the exact half-open approve-once lifetime.
	ActionApprovalTTL = 60 * time.Second

	MaxActionReasonSummaryBytes = 512
	MaxActionRiskSummaryBytes   = 1024
	MaxActionArguments          = 16
	MaxActionArgumentBytes      = 4096
	MaxActionEnvironment        = 8
	MaxActionEnvironmentBytes   = 2048
	MaxActionShellCommandBytes  = 4096
	MaxActionTimeout            = 30 * time.Minute
	MaxActionItems              = 4096
	MaxActionLines              = 100000
	MaxActionBytes              = 16 * 1024 * 1024

	// RestartDeploymentActionTimeout bounds the currently composed typed
	// mutation including its fixed verification plan.
	RestartDeploymentActionTimeout      = 2 * time.Minute
	RestartDeploymentVerificationPlanID = "restart-rollout/v1"
)

var (
	ErrInvalidActionEnvelope   = errors.New("ActionEnvelope data is invalid")
	ErrInvalidActionIntent     = errors.New("action intent data is invalid")
	ErrInvalidActionTarget     = errors.New("action target data is invalid")
	ErrInvalidActionParameters = errors.New("action parameters are invalid")
	ErrInvalidActionLimits     = errors.New("action limits are invalid")
)

// ActionOperation is a closed catalog identifier, never a command string.
// Listing an Accepted target here does not enable its runtime implementation.
type ActionOperation string

const (
	ActionOperationResourceGet         ActionOperation = "resource_get"
	ActionOperationResourceList        ActionOperation = "resource_list"
	ActionOperationResourceDescribe    ActionOperation = "resource_describe"
	ActionOperationResourceQuery       ActionOperation = "resource_query"
	ActionOperationEvents              ActionOperation = "events"
	ActionOperationLogsCurrent         ActionOperation = "logs_current"
	ActionOperationLogsPrevious        ActionOperation = "logs_previous"
	ActionOperationLogsAllContainers   ActionOperation = "logs_all_containers"
	ActionOperationLogSearch           ActionOperation = "log_search"
	ActionOperationPodMetrics          ActionOperation = "pod_metrics"
	ActionOperationNodeMetrics         ActionOperation = "node_metrics"
	ActionOperationPrometheusQuery     ActionOperation = "prometheus_query"
	ActionOperationLokiQuery           ActionOperation = "loki_query"
	ActionOperationContainerFileRead   ActionOperation = "container_file_read"
	ActionOperationPodDiagnostic       ActionOperation = "pod_diagnostic"
	ActionOperationPodExec             ActionOperation = "pod_exec"
	ActionOperationDiagnosticPod       ActionOperation = "diagnostic_pod"
	ActionOperationRestartDeployment   ActionOperation = "restart_deployment"
	ActionOperationScaleWorkload       ActionOperation = "scale_workload"
	ActionOperationRollbackDeployment  ActionOperation = "rollback_deployment"
	ActionOperationDeleteOwnedPod      ActionOperation = "delete_owned_pod"
	ActionOperationCordonNode          ActionOperation = "cordon_node"
	ActionOperationUncordonNode        ActionOperation = "uncordon_node"
	ActionOperationDrainNode           ActionOperation = "drain_node"
	ActionOperationRestrictedLocalArgv ActionOperation = "restricted_local_argv"
	ActionOperationShell               ActionOperation = "shell"
)

// Valid reports whether the operation is part of the Accepted code-owned P0 catalog.
func (operation ActionOperation) Valid() bool {
	switch operation {
	case ActionOperationResourceGet,
		ActionOperationResourceList,
		ActionOperationResourceDescribe,
		ActionOperationResourceQuery,
		ActionOperationEvents,
		ActionOperationLogsCurrent,
		ActionOperationLogsPrevious,
		ActionOperationLogsAllContainers,
		ActionOperationLogSearch,
		ActionOperationPodMetrics,
		ActionOperationNodeMetrics,
		ActionOperationPrometheusQuery,
		ActionOperationLokiQuery,
		ActionOperationContainerFileRead,
		ActionOperationPodDiagnostic,
		ActionOperationPodExec,
		ActionOperationDiagnosticPod,
		ActionOperationRestartDeployment,
		ActionOperationScaleWorkload,
		ActionOperationRollbackDeployment,
		ActionOperationDeleteOwnedPod,
		ActionOperationCordonNode,
		ActionOperationUncordonNode,
		ActionOperationDrainNode,
		ActionOperationRestrictedLocalArgv,
		ActionOperationShell:
		return true
	default:
		return false
	}
}

// SchemaVersion returns the code-owned schema identifier for one operation.
func (operation ActionOperation) SchemaVersion() string {
	if !operation.Valid() {
		return ""
	}
	return string(operation) + "/v1"
}

// ActionDigest is a lowercase SHA-256 digest of one canonical envelope.
type ActionDigest string

func (digest ActionDigest) Valid() bool { return validSHA256Hex(string(digest)) }

func (digest ActionDigest) Equal(other ActionDigest) bool {
	if !digest.Valid() || !other.Valid() {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(digest), []byte(other)) == 1
}

// ActionDataCategories is a fixed bit set of eligible data carried by an action.
type ActionDataCategories uint32

const (
	ActionDataResourceMetadata ActionDataCategories = 1 << iota
	ActionDataProjectedStatus
	ActionDataProjectedEvents
	ActionDataContainerOutput
	ActionDataFileOutput
	ActionDataProcessOutput
)

const allActionDataCategories = ActionDataResourceMetadata | ActionDataProjectedStatus |
	ActionDataProjectedEvents | ActionDataContainerOutput | ActionDataFileOutput | ActionDataProcessOutput

func (categories ActionDataCategories) Valid() bool {
	return categories != 0 && categories&^allActionDataCategories == 0
}

func (categories ActionDataCategories) canonical() string {
	values := make([]string, 0, 6)
	for _, item := range []struct {
		flag ActionDataCategories
		name string
	}{
		{ActionDataResourceMetadata, "resource_metadata"},
		{ActionDataProjectedStatus, "projected_status"},
		{ActionDataProjectedEvents, "projected_events"},
		{ActionDataContainerOutput, "container_output"},
		{ActionDataFileOutput, "file_output"},
		{ActionDataProcessOutput, "process_output"},
	} {
		if categories&item.flag != 0 {
			values = append(values, item.name)
		}
	}
	return strings.Join(values, ",")
}

// ActionSinks is a fixed bit set of destinations eligible to receive action data.
type ActionSinks uint32

const (
	ActionSinkTerminal ActionSinks = 1 << iota
	ActionSinkModel
	ActionSinkKubernetesAPI
	ActionSinkLocalProcess
)

const allActionSinks = ActionSinkTerminal | ActionSinkModel | ActionSinkKubernetesAPI | ActionSinkLocalProcess

func (sinks ActionSinks) Valid() bool { return sinks != 0 && sinks&^allActionSinks == 0 }

func (sinks ActionSinks) canonical() string {
	values := make([]string, 0, 4)
	for _, item := range []struct {
		flag ActionSinks
		name string
	}{
		{ActionSinkTerminal, "terminal"},
		{ActionSinkModel, "model"},
		{ActionSinkKubernetesAPI, "kubernetes_api"},
		{ActionSinkLocalProcess, "local_process"},
	} {
		if sinks&item.flag != 0 {
			values = append(values, item.name)
		}
	}
	return strings.Join(values, ",")
}

// ActionNetworkEffects identifies exact code-owned destination classes. Exact
// origins remain bound by scope or role consent rather than raw endpoint text.
type ActionNetworkEffects uint32

const (
	ActionNetworkNone ActionNetworkEffects = 1 << iota
	ActionNetworkKubernetesAPI
	ActionNetworkModelOrigin
	ActionNetworkDataSource
	ActionNetworkRemotePod
	ActionNetworkExternalCommand
)

const allActionNetworkEffects = ActionNetworkNone | ActionNetworkKubernetesAPI |
	ActionNetworkModelOrigin | ActionNetworkDataSource | ActionNetworkRemotePod | ActionNetworkExternalCommand

func (effects ActionNetworkEffects) Valid() bool {
	return effects != 0 && effects&^allActionNetworkEffects == 0 &&
		(effects == ActionNetworkNone || effects&ActionNetworkNone == 0)
}

func (effects ActionNetworkEffects) canonical() string {
	values := make([]string, 0, 4)
	for _, item := range []struct {
		flag ActionNetworkEffects
		name string
	}{
		{ActionNetworkNone, "none"},
		{ActionNetworkKubernetesAPI, "kubernetes_api"},
		{ActionNetworkModelOrigin, "model_origin"},
		{ActionNetworkDataSource, "data_source"},
		{ActionNetworkRemotePod, "remote_pod"},
		{ActionNetworkExternalCommand, "external_command"},
	} {
		if effects&item.flag != 0 {
			values = append(values, item.name)
		}
	}
	return strings.Join(values, ",")
}

// ActionTarget binds one exact resource identity and optional semantic
// fingerprints. It contains no vendor object or generic payload.
type ActionTarget struct {
	Resource        ResourceRef
	Subresource     string
	Fingerprint     string
	Generation      int64
	Revision        int64
	TargetSetDigest ActionDigest
	TargetCount     int
}

func (target ActionTarget) Validate() error {
	if target.Resource.Validate() != nil || target.Resource.UID == "" || target.Resource.ResourceVersion == "" ||
		!ValidActionSubresource(target.Subresource) || !validOptionalSHA256(target.Fingerprint) ||
		target.Generation < 0 || target.Revision < 0 ||
		target.TargetCount < 0 || target.TargetCount > MaxActionItems ||
		(target.TargetCount == 0) != (target.TargetSetDigest == "") ||
		target.TargetSetDigest != "" && !target.TargetSetDigest.Valid() {
		return ErrInvalidActionTarget
	}
	return nil
}

func validOptionalSHA256(value string) bool { return value == "" || validSHA256Hex(value) }

func validOptionalActionToken(value string, maximum int) bool {
	return value == "" || validActionToken(value, maximum)
}

// ValidActionSubresource validates an optional code-owned API subresource token.
func ValidActionSubresource(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 64 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, current := range []byte(value) {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '-' {
			continue
		}
		return false
	}
	return true
}

// ActionParameterKind is a closed tagged union selector. No map, JSON, raw
// command string, or vendor value can enter the envelope through it.
type ActionParameterKind string

const (
	ActionParametersNone           ActionParameterKind = "none"
	ActionParametersReplicaTarget  ActionParameterKind = "replica_target"
	ActionParametersRevision       ActionParameterKind = "revision"
	ActionParametersNodeScheduling ActionParameterKind = "node_scheduling"
	ActionParametersPodDelete      ActionParameterKind = "pod_delete"
	ActionParametersDrainPlan      ActionParameterKind = "drain_plan"
	ActionParametersContainerFile  ActionParameterKind = "container_file"
	ActionParametersRemoteArgv     ActionParameterKind = "remote_argv"
	ActionParametersLocalArgv      ActionParameterKind = "local_argv"
	ActionParametersShellCommand   ActionParameterKind = "shell_command"
)

// Valid reports whether the parameter discriminator is code-owned.
func (kind ActionParameterKind) Valid() bool {
	switch kind {
	case ActionParametersNone,
		ActionParametersReplicaTarget,
		ActionParametersRevision,
		ActionParametersNodeScheduling,
		ActionParametersPodDelete,
		ActionParametersDrainPlan,
		ActionParametersContainerFile,
		ActionParametersRemoteArgv,
		ActionParametersLocalArgv,
		ActionParametersShellCommand:
		return true
	default:
		return false
	}
}

// ActionArguments is a bounded immutable-by-construction argv value.
type ActionArguments struct {
	values [MaxActionArguments]string
	count  uint8
}

// ActionEnvironment is one immutable, ordered, minimal child environment.
// Construction accepts only the code-owned non-sensitive keys and values used
// by the restricted process adapter. It never inherits the parent process.
type ActionEnvironment struct {
	values [MaxActionEnvironment]string
	count  uint8
}

// NewActionEnvironment copies an exact KEY=VALUE vector after enforcing the
// fixed allowlist. Ordering is canonicalized by key so configuration order
// cannot change action identity.
func NewActionEnvironment(values []string) (ActionEnvironment, error) {
	var result ActionEnvironment
	if len(values) > MaxActionEnvironment {
		return result, ErrInvalidActionParameters
	}
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	total := 0
	previous := ""
	for index, value := range ordered {
		key, item, found := strings.Cut(value, "=")
		if !found || key == previous || !validLocalEnvironmentValue(key, item) ||
			!validSafeOptionalText(value, MaxActionEnvironmentBytes) {
			return ActionEnvironment{}, ErrInvalidActionParameters
		}
		total += len(value)
		if total > MaxActionEnvironmentBytes {
			return ActionEnvironment{}, ErrInvalidActionParameters
		}
		result.values[index] = value
		result.count++
		previous = key
	}
	return result, nil
}

func validLocalEnvironmentValue(key, value string) bool {
	switch key {
	case "LANG", "LC_ALL":
		return value == "C" || value == "POSIX" || value == "C.UTF-8"
	case "NO_COLOR":
		return value == "1"
	default:
		return false
	}
}

func (environment ActionEnvironment) Valid() bool {
	if int(environment.count) > len(environment.values) {
		return false
	}
	values := environment.Values()
	if values == nil && environment.count != 0 {
		return false
	}
	rebuilt, err := NewActionEnvironment(values)
	return err == nil && rebuilt == environment
}

// Values returns an independent ordered environment copy.
func (environment ActionEnvironment) Values() []string {
	if int(environment.count) > len(environment.values) {
		return nil
	}
	return append([]string(nil), environment.values[:environment.count]...)
}

func (environment ActionEnvironment) canonical() string {
	if !environment.Valid() {
		return ""
	}
	var builder strings.Builder
	for _, value := range environment.Values() {
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	return builder.String()
}

// NewActionArguments copies one bounded argv vector. It never accepts a shell command.
func NewActionArguments(values []string) (ActionArguments, error) {
	var result ActionArguments
	if len(values) == 0 || len(values) > MaxActionArguments {
		return result, ErrInvalidActionParameters
	}
	total := 0
	for index, value := range values {
		if !validSafeOptionalText(value, MaxActionArgumentBytes) || value == "" {
			return ActionArguments{}, ErrInvalidActionParameters
		}
		total += len(value)
		if total > MaxActionArgumentBytes {
			return ActionArguments{}, ErrInvalidActionParameters
		}
		result.values[index] = value
	}
	result.count = uint8(len(values))
	return result, nil
}

func (arguments ActionArguments) Valid() bool {
	if arguments.count == 0 || int(arguments.count) > len(arguments.values) {
		return false
	}
	total := 0
	for index, value := range arguments.values {
		if index < int(arguments.count) {
			if value == "" || !validSafeOptionalText(value, MaxActionArgumentBytes) {
				return false
			}
			total += len(value)
		} else if value != "" {
			return false
		}
	}
	return total <= MaxActionArgumentBytes
}

// Values returns an independent argv copy.
func (arguments ActionArguments) Values() []string {
	if !arguments.Valid() {
		return nil
	}
	return append([]string(nil), arguments.values[:arguments.count]...)
}

func (arguments ActionArguments) canonical() string {
	if !arguments.Valid() {
		return ""
	}
	var builder strings.Builder
	for index := 0; index < int(arguments.count); index++ {
		value := arguments.values[index]
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	return builder.String()
}

// ActionParameters is a closed tagged union. Fields not selected by Kind must
// remain zero so one operation cannot smuggle parameters for another.
type ActionParameters struct {
	Kind                ActionParameterKind
	ReplicaCurrent      int64
	ReplicaTarget       int64
	Revision            int64
	Unschedulable       bool
	GracePeriodSeconds  int64
	Container           string
	NormalizedPath      string
	Executable          string
	Arguments           ActionArguments
	PolicyID            string
	ExecutableID        ActionDigest
	WorkingDirectory    string
	WorkingDirectoryID  ActionDigest
	Environment         ActionEnvironment
	CredentialReference LocalCredentialReference
	ShellCommand        string
	PlanDigest          ActionDigest
	PlanTargetCount     int
}

func (parameters ActionParameters) Validate() error {
	if !parameters.Kind.Valid() {
		return ErrInvalidActionParameters
	}
	zeroArguments := ActionArguments{}
	switch parameters.Kind {
	case ActionParametersNone:
		if parameters.ReplicaCurrent != 0 || parameters.ReplicaTarget != 0 || parameters.Revision != 0 || parameters.Unschedulable || parameters.GracePeriodSeconds != 0 ||
			parameters.Container != "" || parameters.NormalizedPath != "" || parameters.Executable != "" ||
			parameters.Arguments != zeroArguments || parameters.hasLocalExecutionFields() ||
			parameters.PlanDigest != "" || parameters.PlanTargetCount != 0 {
			return ErrInvalidActionParameters
		}
	case ActionParametersReplicaTarget:
		if parameters.ReplicaCurrent < 0 || parameters.ReplicaCurrent > int64(^uint32(0)>>1) ||
			parameters.ReplicaTarget < 0 || parameters.ReplicaTarget > int64(^uint32(0)>>1) ||
			parameters.hasFieldsExceptReplicaTarget() {
			return ErrInvalidActionParameters
		}
	case ActionParametersRevision:
		if parameters.Revision < 1 || parameters.hasFieldsExceptRevision() {
			return ErrInvalidActionParameters
		}
	case ActionParametersNodeScheduling:
		if parameters.ReplicaCurrent != 0 || parameters.ReplicaTarget != 0 || parameters.Revision != 0 || parameters.GracePeriodSeconds != 0 || parameters.Container != "" ||
			parameters.NormalizedPath != "" || parameters.Executable != "" || parameters.Arguments != zeroArguments ||
			parameters.hasLocalExecutionFields() ||
			parameters.PlanDigest != "" || parameters.PlanTargetCount != 0 {
			return ErrInvalidActionParameters
		}
	case ActionParametersPodDelete:
		if parameters.GracePeriodSeconds < 1 || parameters.GracePeriodSeconds > 3600 ||
			parameters.ReplicaCurrent != 0 || parameters.ReplicaTarget != 0 || parameters.Revision != 0 || parameters.Unschedulable ||
			parameters.Container != "" || parameters.NormalizedPath != "" || parameters.Executable != "" ||
			parameters.Arguments != zeroArguments || parameters.hasLocalExecutionFields() || parameters.PlanDigest != "" ||
			parameters.PlanTargetCount != 0 {
			return ErrInvalidActionParameters
		}
	case ActionParametersDrainPlan:
		if !parameters.PlanDigest.Valid() || parameters.PlanTargetCount < 1 || parameters.PlanTargetCount > MaxActionItems ||
			parameters.GracePeriodSeconds < 1 || parameters.GracePeriodSeconds > 3600 ||
			parameters.ReplicaCurrent != 0 || parameters.ReplicaTarget != 0 || parameters.Revision != 0 || parameters.Unschedulable ||
			parameters.Container != "" || parameters.NormalizedPath != "" || parameters.Executable != "" ||
			parameters.Arguments != zeroArguments || parameters.hasLocalExecutionFields() {
			return ErrInvalidActionParameters
		}
	case ActionParametersContainerFile:
		normalized, pathErr := NormalizeContainerFilePath(parameters.NormalizedPath)
		readerArguments, argumentsErr := ContainerFileReaderArguments(normalized)
		if !ValidResourceName(parameters.Container) || pathErr != nil || normalized != parameters.NormalizedPath ||
			!ValidContainerFileReaderExecutable(parameters.Executable) || argumentsErr != nil || parameters.Arguments != readerArguments ||
			parameters.ReplicaCurrent != 0 || parameters.ReplicaTarget != 0 || parameters.Revision != 0 || parameters.Unschedulable || parameters.GracePeriodSeconds != 0 ||
			parameters.PlanDigest != "" || parameters.PlanTargetCount != 0 || parameters.hasLocalExecutionFields() {
			return ErrInvalidActionParameters
		}
	case ActionParametersRemoteArgv:
		if !validActionToken(parameters.Executable, 1024) || !parameters.Arguments.Valid() ||
			parameters.ReplicaCurrent != 0 || parameters.ReplicaTarget != 0 || parameters.Revision != 0 || parameters.Unschedulable || parameters.GracePeriodSeconds != 0 ||
			parameters.NormalizedPath != "" || parameters.PlanDigest != "" || parameters.PlanTargetCount != 0 ||
			!validActionToken(parameters.Container, 253) || parameters.hasLocalExecutionFields() {
			return ErrInvalidActionParameters
		}
	case ActionParametersLocalArgv:
		if !validAbsoluteActionPath(parameters.Executable) || !parameters.Arguments.Valid() ||
			!validLocalPolicyID(parameters.PolicyID) || !parameters.ExecutableID.Valid() ||
			!validAbsoluteActionPath(parameters.WorkingDirectory) || !parameters.WorkingDirectoryID.Valid() ||
			!parameters.Environment.Valid() ||
			(parameters.CredentialReference != LocalCredentialNone && parameters.CredentialReference != LocalCredentialArgoCDCLIProfile) ||
			parameters.ShellCommand != "" || parameters.Container != "" ||
			parameters.ReplicaCurrent != 0 || parameters.ReplicaTarget != 0 || parameters.Revision != 0 || parameters.Unschedulable || parameters.GracePeriodSeconds != 0 ||
			parameters.NormalizedPath != "" || parameters.PlanDigest != "" || parameters.PlanTargetCount != 0 {
			return ErrInvalidActionParameters
		}
	case ActionParametersShellCommand:
		if !validAbsoluteActionPath(parameters.Executable) || parameters.Arguments != zeroArguments ||
			!validLocalPolicyID(parameters.PolicyID) || !parameters.ExecutableID.Valid() ||
			!validAbsoluteActionPath(parameters.WorkingDirectory) || !parameters.WorkingDirectoryID.Valid() ||
			!parameters.Environment.Valid() || parameters.CredentialReference != "" ||
			!validSafeOptionalText(parameters.ShellCommand, MaxActionShellCommandBytes) ||
			strings.TrimSpace(parameters.ShellCommand) != parameters.ShellCommand || parameters.Container != "" ||
			parameters.ReplicaCurrent != 0 || parameters.ReplicaTarget != 0 || parameters.Revision != 0 || parameters.Unschedulable || parameters.GracePeriodSeconds != 0 ||
			parameters.NormalizedPath != "" || parameters.PlanDigest != "" || parameters.PlanTargetCount != 0 {
			return ErrInvalidActionParameters
		}
	}
	return nil
}

func (parameters ActionParameters) hasLocalExecutionFields() bool {
	return parameters.PolicyID != "" || parameters.ExecutableID != "" || parameters.WorkingDirectory != "" ||
		parameters.WorkingDirectoryID != "" || parameters.Environment != (ActionEnvironment{}) ||
		parameters.CredentialReference != "" || parameters.ShellCommand != ""
}

func validAbsoluteActionPath(value string) bool {
	return len(value) > 1 && len(value) <= 4096 && value[0] == '/' && strings.TrimSpace(value) == value &&
		!strings.Contains(value, "//") && !strings.Contains(value, "/./") && !strings.Contains(value, "/../") &&
		!strings.HasSuffix(value, "/.") && !strings.HasSuffix(value, "/..") && validSafeOptionalText(value, 4096)
}

// ValidLocalExecutionPath exposes only the pure lexical portion of the local
// process path contract. The executor separately proves filesystem identity,
// regular-file/directory type, permissions, and absence of symlinks.
func ValidLocalExecutionPath(value string) bool {
	return validAbsoluteActionPath(value)
}

func (parameters ActionParameters) hasFieldsExceptReplicaTarget() bool {
	return parameters.Revision != 0 || parameters.Unschedulable || parameters.GracePeriodSeconds != 0 || parameters.Container != "" ||
		parameters.NormalizedPath != "" || parameters.Executable != "" || parameters.Arguments != (ActionArguments{}) ||
		parameters.hasLocalExecutionFields() || parameters.PlanDigest != "" || parameters.PlanTargetCount != 0
}

func (parameters ActionParameters) hasFieldsExceptRevision() bool {
	return parameters.ReplicaCurrent != 0 || parameters.ReplicaTarget != 0 || parameters.Unschedulable || parameters.GracePeriodSeconds != 0 || parameters.Container != "" ||
		parameters.NormalizedPath != "" || parameters.Executable != "" || parameters.Arguments != (ActionArguments{}) ||
		parameters.hasLocalExecutionFields() || parameters.PlanDigest != "" || parameters.PlanTargetCount != 0
}

func (parameters ActionParameters) canonical() string {
	fields := []string{
		string(parameters.Kind),
		strconv.FormatInt(parameters.ReplicaTarget, 10),
		strconv.FormatInt(parameters.Revision, 10),
		strconv.FormatBool(parameters.Unschedulable),
		parameters.Container,
		parameters.NormalizedPath,
		parameters.Executable,
		parameters.Arguments.canonical(),
		string(parameters.PlanDigest),
		strconv.Itoa(parameters.PlanTargetCount),
	}
	if parameters.Kind == ActionParametersReplicaTarget {
		fields = append(fields, strconv.FormatInt(parameters.ReplicaCurrent, 10))
	}
	if parameters.Kind == ActionParametersPodDelete || parameters.Kind == ActionParametersDrainPlan {
		fields = append(fields, strconv.FormatInt(parameters.GracePeriodSeconds, 10))
	}
	// The v1 branches that were already enabled retain byte-for-byte canonical
	// encoding. These fields extend only the previously uncomposed local branch
	// and the newly tagged shell branch.
	if parameters.Kind == ActionParametersLocalArgv || parameters.Kind == ActionParametersShellCommand {
		fields = append(fields,
			parameters.PolicyID,
			string(parameters.ExecutableID),
			parameters.WorkingDirectory,
			string(parameters.WorkingDirectoryID),
			parameters.Environment.canonical(),
			string(parameters.CredentialReference),
			parameters.ShellCommand,
		)
	}
	return strings.Join(fields, "\n")
}

// Digest returns the safe derivative eligible for durable parameter binding.
func (parameters ActionParameters) Digest() ActionDigest {
	if parameters.Validate() != nil {
		return ""
	}
	digest := sha256.Sum256([]byte("kupilot.action-parameters/v1\n" + parameters.canonical()))
	return ActionDigest(hex.EncodeToString(digest[:]))
}

func validActionToken(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value && validSafeOptionalText(value, maximum)
}

// ActionLimits are immutable independent hard ceilings, not endpoint claims.
type ActionLimits struct {
	Timeout       time.Duration
	MaximumItems  int
	MaximumLines  int
	MaximumBytes  int
	MaximumOutput int
}

func (limits ActionLimits) Validate() error {
	if limits.Timeout <= 0 || limits.Timeout > MaxActionTimeout ||
		limits.MaximumItems < 0 || limits.MaximumItems > MaxActionItems ||
		limits.MaximumLines < 0 || limits.MaximumLines > MaxActionLines ||
		limits.MaximumBytes < 0 || limits.MaximumBytes > MaxActionBytes ||
		limits.MaximumOutput < 0 || limits.MaximumOutput > MaxActionBytes {
		return ErrInvalidActionLimits
	}
	return nil
}

// ActionIntent is the complete immutable policy and execution input before
// request identity and time are added. Operation-specific Application policy
// validates the relationship among Operation, Target, and Parameters.
type ActionIntent struct {
	Operation              ActionOperation
	OperationSchemaVersion string
	PolicyVersion          string
	PermissionProfile      PermissionProfile
	Risk                   RiskClass
	Effect                 CapabilityEffectClass
	PolicyGeneration       PolicyGeneration
	Scope                  ScopeSnapshot
	NamespaceAccess        NamespaceAccessPolicy
	Target                 ActionTarget
	Parameters             ActionParameters
	Stdin                  bool
	TTY                    bool
	Shell                  bool
	DataCategories         ActionDataCategories
	AllowedSinks           ActionSinks
	NetworkEffects         ActionNetworkEffects
	NetworkDestinationHash ActionDigest
	Limits                 ActionLimits
	VerificationPlanID     string
	ReasonSummary          string
	RiskSummary            string
}

func (intent ActionIntent) Validate() error {
	if !intent.Operation.Valid() || intent.OperationSchemaVersion != intent.Operation.SchemaVersion() ||
		intent.PolicyVersion != ActionPolicyVersion || !intent.PermissionProfile.Valid() || intent.Risk == RiskDeny ||
		!intent.Risk.CompatibleWithEffect(intent.Effect) || !intent.PolicyGeneration.Valid() || intent.Scope.Validate() != nil ||
		!ValidContextName(intent.Scope.Context) || !ValidNamespaceName(intent.Scope.Namespace) ||
		intent.Scope.Generation < 1 || !intent.NamespaceAccess.Valid() || intent.Target.Validate() != nil ||
		intent.Parameters.Validate() != nil || intent.TTY && !intent.Stdin ||
		intent.Shell != (intent.Operation == ActionOperationShell) || intent.Shell && (intent.Stdin || intent.TTY) ||
		!intent.DataCategories.Valid() || !intent.AllowedSinks.Valid() || !intent.NetworkEffects.Valid() ||
		!intent.validNetworkDestination() ||
		intent.Limits.Validate() != nil || !validActionToken(intent.VerificationPlanID, 128) ||
		!ValidActionReasonSummary(intent.ReasonSummary) ||
		!validBoundedText(intent.RiskSummary, 1, MaxActionRiskSummaryBytes) || strings.TrimSpace(intent.RiskSummary) != intent.RiskSummary {
		return ErrInvalidActionIntent
	}
	return nil
}

func (intent ActionIntent) validNetworkDestination() bool {
	requiresHash := intent.NetworkEffects&(ActionNetworkModelOrigin|ActionNetworkDataSource|ActionNetworkRemotePod|ActionNetworkExternalCommand) != 0
	if requiresHash {
		return intent.NetworkDestinationHash.Valid()
	}
	return intent.NetworkDestinationHash == ""
}

// ValidateRestartDeployment checks the exact currently composed operation
// shape. Other catalog identifiers remain definitions, not enabled actions.
func (intent ActionIntent) ValidateRestartDeployment() error {
	if intent.Validate() != nil || intent.Operation != ActionOperationRestartDeployment ||
		intent.Target.Resource.APIVersion != RestartDeploymentTargetAPIVersion ||
		intent.Target.Resource.Kind != RestartDeploymentTargetKind ||
		intent.Target.Resource.Namespace != intent.Scope.Namespace ||
		intent.Target.Subresource != "" ||
		intent.Target.Fingerprint == "" || intent.Target.Generation < 1 ||
		intent.Target.Revision != 0 || intent.Target.TargetSetDigest != "" || intent.Target.TargetCount != 0 ||
		intent.Parameters != (ActionParameters{Kind: ActionParametersNone}) ||
		intent.Stdin || intent.TTY || intent.Shell || intent.Risk != RiskReview ||
		intent.Effect != CapabilityEffectClusterMutation || intent.DataCategories != ActionDataResourceMetadata ||
		intent.AllowedSinks != ActionSinkTerminal|ActionSinkKubernetesAPI ||
		intent.NetworkEffects != ActionNetworkKubernetesAPI ||
		intent.NetworkDestinationHash != "" ||
		intent.Limits != (ActionLimits{Timeout: RestartDeploymentActionTimeout, MaximumItems: 1}) ||
		intent.VerificationPlanID != RestartDeploymentVerificationPlanID ||
		intent.RiskSummary != RestartDeploymentRiskSummary {
		return ErrInvalidActionIntent
	}
	return nil
}

func ValidActionReasonSummary(value string) bool {
	return value != "" && len(value) <= MaxActionReasonSummaryBytes && strings.TrimSpace(value) == value &&
		validSafeOptionalText(value, MaxActionReasonSummaryBytes)
}

// ActionEnvelope is immutable execution identity. Digest excludes no field
// except itself and is recomputed by Validate.
type ActionEnvelope struct {
	SchemaVersion string
	RequestID     ApprovalID
	SessionID     SessionID
	RunID         AgentRunID
	Intent        ActionIntent
	RequestedAt   time.Time
	ExpiresAt     time.Time
	Digest        ActionDigest
}

// NewActionEnvelope seals one valid intent for the exact 60-second lifetime.
func NewActionEnvelope(requestID ApprovalID, sessionID SessionID, runID AgentRunID, intent ActionIntent, requestedAt time.Time) (ActionEnvelope, error) {
	envelope := ActionEnvelope{
		SchemaVersion: ActionEnvelopeSchemaVersion,
		RequestID:     requestID,
		SessionID:     sessionID,
		RunID:         runID,
		Intent:        intent,
		RequestedAt:   requestedAt,
		ExpiresAt:     requestedAt.Add(ActionApprovalTTL),
	}
	if envelope.validateUnsigned() != nil {
		return ActionEnvelope{}, ErrInvalidActionEnvelope
	}
	canonical, _ := CanonicalAction(envelope)
	digest := sha256.Sum256(canonical)
	envelope.Digest = ActionDigest(hex.EncodeToString(digest[:]))
	if envelope.Validate() != nil {
		return ActionEnvelope{}, ErrInvalidActionEnvelope
	}
	return envelope, nil
}

func (envelope ActionEnvelope) validateUnsigned() error {
	if envelope.SchemaVersion != ActionEnvelopeSchemaVersion || !envelope.RequestID.Valid() ||
		!envelope.SessionID.Valid() || !envelope.RunID.Valid() || envelope.Intent.Validate() != nil ||
		!validPersistenceTime(envelope.RequestedAt) || !validPersistenceTime(envelope.ExpiresAt) ||
		!envelope.ExpiresAt.Equal(envelope.RequestedAt.Add(ActionApprovalTTL)) {
		return ErrInvalidActionEnvelope
	}
	return nil
}

func (envelope ActionEnvelope) Validate() error {
	if envelope.validateUnsigned() != nil || !envelope.Digest.Valid() {
		return ErrInvalidActionEnvelope
	}
	canonical, err := CanonicalAction(envelope)
	if err != nil {
		return ErrInvalidActionEnvelope
	}
	digest := sha256.Sum256(canonical)
	want := ActionDigest(hex.EncodeToString(digest[:]))
	if !envelope.Digest.Equal(want) {
		return ErrInvalidActionEnvelope
	}
	return nil
}

// CanonicalAction returns independent fixed-order length-prefixed bytes.
func CanonicalAction(envelope ActionEnvelope) ([]byte, error) {
	if envelope.validateUnsigned() != nil {
		return nil, ErrInvalidActionEnvelope
	}
	intent := envelope.Intent
	target := intent.Target
	fields := []actionCanonicalField{
		{"envelope_schema_version", envelope.SchemaVersion},
		{"request_id", string(envelope.RequestID)},
		{"session_id", string(envelope.SessionID)},
		{"run_id", string(envelope.RunID)},
		{"operation", string(intent.Operation)},
		{"operation_schema_version", intent.OperationSchemaVersion},
		{"policy_version", intent.PolicyVersion},
		{"permission_profile", string(intent.PermissionProfile)},
		{"risk", string(intent.Risk)},
		{"effect", string(intent.Effect)},
		{"policy_generation", strconv.FormatInt(int64(intent.PolicyGeneration), 10)},
		{"scope_context", intent.Scope.Context},
		{"scope_namespace", intent.Scope.Namespace},
		{"namespace_access", string(intent.NamespaceAccess)},
		{"scope_generation", strconv.FormatInt(intent.Scope.Generation, 10)},
		{"target_api_version", target.Resource.APIVersion},
		{"target_kind", target.Resource.Kind},
		{"target_namespace", target.Resource.Namespace},
		{"target_name", target.Resource.Name},
		{"target_uid", target.Resource.UID},
		{"target_resource_version", target.Resource.ResourceVersion},
		{"target_subresource", target.Subresource},
		{"target_fingerprint", target.Fingerprint},
		{"target_generation", strconv.FormatInt(target.Generation, 10)},
		{"target_revision", strconv.FormatInt(target.Revision, 10)},
		{"target_set_digest", string(target.TargetSetDigest)},
		{"target_count", strconv.Itoa(target.TargetCount)},
		{"parameters", intent.Parameters.canonical()},
		{"parameters_digest", string(intent.Parameters.Digest())},
		{"stdin", strconv.FormatBool(intent.Stdin)},
		{"tty", strconv.FormatBool(intent.TTY)},
		{"shell", strconv.FormatBool(intent.Shell)},
		{"data_categories", intent.DataCategories.canonical()},
		{"allowed_sinks", intent.AllowedSinks.canonical()},
		{"network_effects", intent.NetworkEffects.canonical()},
		{"network_destination_hash", string(intent.NetworkDestinationHash)},
		{"timeout_ms", strconv.FormatInt(intent.Limits.Timeout.Milliseconds(), 10)},
		{"maximum_items", strconv.Itoa(intent.Limits.MaximumItems)},
		{"maximum_lines", strconv.Itoa(intent.Limits.MaximumLines)},
		{"maximum_bytes", strconv.Itoa(intent.Limits.MaximumBytes)},
		{"maximum_output", strconv.Itoa(intent.Limits.MaximumOutput)},
		{"verification_plan_id", intent.VerificationPlanID},
		{"reason_summary", intent.ReasonSummary},
		{"risk_summary", intent.RiskSummary},
		{"requested_at_ms", strconv.FormatInt(envelope.RequestedAt.UnixMilli(), 10)},
		{"expires_at_ms", strconv.FormatInt(envelope.ExpiresAt.UnixMilli(), 10)},
	}
	var builder strings.Builder
	builder.WriteString(ActionDigestVersion)
	builder.WriteByte('\n')
	for _, field := range fields {
		builder.WriteString(field.name)
		builder.WriteByte('=')
		builder.WriteString(strconv.Itoa(len(field.value)))
		builder.WriteByte(':')
		builder.WriteString(field.value)
		builder.WriteByte('\n')
	}
	return []byte(builder.String()), nil
}

type actionCanonicalField struct {
	name  string
	value string
}
