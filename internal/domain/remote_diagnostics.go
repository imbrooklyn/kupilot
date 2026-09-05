package domain

import (
	"errors"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	// RemoteDiagnosticsPolicyVersion identifies the exact configured
	// Pod-execution, container-file, and diagnostic-Pod policy snapshot.
	RemoteDiagnosticsPolicyVersion = "kupilot.remote-diagnostics-policy/v1"

	MaxRemoteDiagnosticPolicies = 32
	MaxContainerFileRoots       = 8
	MaxContainerFilePathBytes   = 256
	MaxRemoteDiagnosticLines    = 1000
	MaxRemoteDiagnosticBytes    = MaxToolResultBytes
	MaxRemoteOutputChunks       = 1024

	PodExecVerificationPlanID       = "pod-exec-result/v1"
	ContainerFileVerificationPlanID = "container-file-regular/v1"
	DiagnosticPodVerificationPlanID = "diagnostic-pod-cleanup/v1"

	PodDiagnosticRiskSummary = "The predefined command executes fixed arguments in one exact Pod container and returns bounded untrusted output."
	PodExecRiskSummary       = "The command executes exact policy-admitted arguments in one exact Pod container and may affect that container."
	ContainerFileRiskSummary = "The operation executes a fixed archive reader in one exact Pod container and may disclose bounded file content."
	DiagnosticPodRiskSummary = "The operation creates one restricted temporary Pod, contacts one exact in-cluster target, and must clean up the Pod."
)

var (
	ErrInvalidRemoteDiagnosticsPolicy = errors.New("remote diagnostics policy is invalid")
	ErrInvalidRemoteDiagnosticPlan    = errors.New("remote diagnostic action plan is invalid")
	ErrInvalidRemoteDiagnosticOutcome = errors.New("remote diagnostic outcome is invalid")
)

// PodExecPolicyClass distinguishes a predefined read-only diagnostic from a
// general exact argv rule. It never represents shell execution.
type PodExecPolicyClass string

const (
	PodExecPolicyPredefined PodExecPolicyClass = "predefined"
	PodExecPolicyGeneral    PodExecPolicyClass = "general"
)

func (class PodExecPolicyClass) Valid() bool {
	return class == PodExecPolicyPredefined || class == PodExecPolicyGeneral
}

// PodExecPolicy binds one exact no-shell command. Arguments are compared as
// an argv vector, so metacharacters have no special interpretation.
type PodExecPolicy struct {
	ID         string
	Class      PodExecPolicyClass
	Executable string
	Arguments  ActionArguments
	Timeout    time.Duration
	MaxLines   int
	MaxBytes   int
}

func (policy PodExecPolicy) Validate() error {
	if !validRemotePolicyID(policy.ID) || !policy.Class.Valid() ||
		!ValidNoShellRemoteCommand(policy.Executable, policy.Arguments) ||
		policy.Timeout <= 0 || policy.Timeout > MaxRequestTimeoutForRemoteDiagnostic() ||
		policy.MaxLines < 1 || policy.MaxLines > MaxRemoteDiagnosticLines ||
		policy.MaxBytes < 1 || policy.MaxBytes > MaxRemoteDiagnosticBytes {
		return ErrInvalidRemoteDiagnosticsPolicy
	}
	if policy.Class == PodExecPolicyPredefined && !predefinedPodDiagnostic(policy) {
		return ErrInvalidRemoteDiagnosticsPolicy
	}
	return nil
}

// ContainerFileRoots is an immutable bounded set of normalized absolute
// application-data roots. Root itself and code-denied system roots cannot be
// represented.
type ContainerFileRoots struct {
	values [MaxContainerFileRoots]string
	count  uint8
}

func NewContainerFileRoots(values []string) (ContainerFileRoots, error) {
	var roots ContainerFileRoots
	if len(values) == 0 || len(values) > MaxContainerFileRoots {
		return roots, ErrInvalidRemoteDiagnosticsPolicy
	}
	for index, value := range values {
		normalized, err := NormalizeContainerFilePath(value)
		if err != nil || normalized == "/" || deniedContainerPath(normalized) {
			return ContainerFileRoots{}, ErrInvalidRemoteDiagnosticsPolicy
		}
		for previous := 0; previous < index; previous++ {
			if roots.values[previous] == normalized {
				return ContainerFileRoots{}, ErrInvalidRemoteDiagnosticsPolicy
			}
		}
		roots.values[index] = normalized
	}
	roots.count = uint8(len(values))
	return roots, nil
}

func (roots ContainerFileRoots) Valid() bool {
	if roots.count == 0 || int(roots.count) > len(roots.values) {
		return false
	}
	for index, value := range roots.values {
		if index >= int(roots.count) {
			if value != "" {
				return false
			}
			continue
		}
		normalized, err := NormalizeContainerFilePath(value)
		if err != nil || normalized != value || value == "/" || deniedContainerPath(value) {
			return false
		}
		for previous := 0; previous < index; previous++ {
			if roots.values[previous] == value {
				return false
			}
		}
	}
	return true
}

func (roots ContainerFileRoots) Values() []string {
	if !roots.Valid() {
		return nil
	}
	return append([]string(nil), roots.values[:roots.count]...)
}

func (roots ContainerFileRoots) Allows(value string) bool {
	normalized, err := NormalizeContainerFilePath(value)
	if err != nil || normalized != value || deniedContainerPath(normalized) || !roots.Valid() {
		return false
	}
	for _, root := range roots.Values() {
		if normalized == root || strings.HasPrefix(normalized, root+"/") {
			return true
		}
	}
	return false
}

// ContainerFilePolicy selects the one code-defined tar reader contract.
// Tar receives every parent path plus the file in one argv-only exec so the
// local parser can reject any component reported as a link without a second
// command. This does not provide an atomic filesystem snapshot.
type ContainerFilePolicy struct {
	Enabled          bool
	ReaderExecutable string
	AllowedRoots     ContainerFileRoots
	Timeout          time.Duration
	MaxLines         int
	MaxBytes         int
}

func (policy ContainerFilePolicy) Validate() error {
	if !policy.Enabled || !ValidContainerFileReaderExecutable(policy.ReaderExecutable) ||
		!policy.AllowedRoots.Valid() || policy.Timeout <= 0 || policy.Timeout > MaxRequestTimeoutForRemoteDiagnostic() ||
		policy.MaxLines < 1 || policy.MaxLines > MaxRemoteDiagnosticLines ||
		policy.MaxBytes < 1 || policy.MaxBytes > MaxRemoteDiagnosticBytes {
		return ErrInvalidRemoteDiagnosticsPolicy
	}
	return nil
}

// DiagnosticPodPolicy binds one digest-pinned image and one code-owned TCP
// probe prefix to an exact same-Namespace Service and port. The runtime appends
// only the derived Service DNS name and decimal port.
type DiagnosticPodPolicy struct {
	ID                    string
	Namespace             string
	Image                 string
	Executable            string
	ArgumentPrefix        ActionArguments
	ServiceName           string
	Port                  uint16
	NetworkPolicyRequired bool
	Timeout               time.Duration
	MaxLines              int
	MaxBytes              int
}

func (policy DiagnosticPodPolicy) Validate() error {
	if !validRemotePolicyID(policy.ID) || !ValidPinnedContainerImage(policy.Image) ||
		!ValidNoShellRemoteCommand(policy.Executable, policy.ArgumentPrefix) ||
		!ValidNamespaceName(policy.Namespace) || !ValidResourceName(policy.ServiceName) || policy.Port == 0 || !policy.NetworkPolicyRequired ||
		policy.Timeout <= 0 || policy.Timeout > MaxRequestTimeoutForRemoteDiagnostic() ||
		policy.MaxLines < 1 || policy.MaxLines > MaxRemoteDiagnosticLines ||
		policy.MaxBytes < 1 || policy.MaxBytes > MaxRemoteDiagnosticBytes ||
		len(policy.ArgumentPrefix.Values()) > MaxActionArguments-2 || !predefinedDiagnosticPodCommand(policy) {
		return ErrInvalidRemoteDiagnosticsPolicy
	}
	return nil
}

func (policy DiagnosticPodPolicy) TargetHost() (string, error) {
	if policy.Validate() != nil {
		return "", ErrInvalidRemoteDiagnosticsPolicy
	}
	host := policy.ServiceName + "." + policy.Namespace + ".svc"
	if !ValidRemoteDiagnosticTarget(host) {
		return "", ErrInvalidRemoteDiagnosticsPolicy
	}
	return host, nil
}

func (policy DiagnosticPodPolicy) Command() (string, ActionArguments, error) {
	host, err := policy.TargetHost()
	if err != nil {
		return "", ActionArguments{}, err
	}
	values := policy.ArgumentPrefix.Values()
	values = append(values, host, strconv.Itoa(int(policy.Port)))
	arguments, err := NewActionArguments(values)
	if err != nil {
		return "", ActionArguments{}, ErrInvalidRemoteDiagnosticsPolicy
	}
	return policy.Executable, arguments, nil
}

// RemoteDiagnosticsPolicyCatalog is the immutable per-run policy snapshot.
// An empty catalog is valid and disables all three model-visible capabilities.
type RemoteDiagnosticsPolicyCatalog struct {
	version     string
	podExec     []PodExecPolicy
	file        *ContainerFilePolicy
	diagnostics []DiagnosticPodPolicy
}

func NewRemoteDiagnosticsPolicyCatalog(
	podExec []PodExecPolicy,
	file *ContainerFilePolicy,
	diagnostics []DiagnosticPodPolicy,
) (RemoteDiagnosticsPolicyCatalog, error) {
	if len(podExec)+len(diagnostics) > MaxRemoteDiagnosticPolicies {
		return RemoteDiagnosticsPolicyCatalog{}, ErrInvalidRemoteDiagnosticsPolicy
	}
	result := RemoteDiagnosticsPolicyCatalog{
		version: RemoteDiagnosticsPolicyVersion,
		podExec: append([]PodExecPolicy(nil), podExec...), diagnostics: append([]DiagnosticPodPolicy(nil), diagnostics...),
	}
	if file != nil {
		copy := *file
		result.file = &copy
	}
	if result.Validate() != nil {
		return RemoteDiagnosticsPolicyCatalog{}, ErrInvalidRemoteDiagnosticsPolicy
	}
	return result, nil
}

func DisabledRemoteDiagnosticsPolicyCatalog() RemoteDiagnosticsPolicyCatalog {
	catalog, _ := NewRemoteDiagnosticsPolicyCatalog(nil, nil, nil)
	return catalog
}

func (catalog RemoteDiagnosticsPolicyCatalog) Validate() error {
	if catalog.version != RemoteDiagnosticsPolicyVersion || len(catalog.podExec)+len(catalog.diagnostics) > MaxRemoteDiagnosticPolicies {
		return ErrInvalidRemoteDiagnosticsPolicy
	}
	seen := make(map[string]struct{}, len(catalog.podExec)+len(catalog.diagnostics))
	for _, policy := range catalog.podExec {
		if policy.Validate() != nil {
			return ErrInvalidRemoteDiagnosticsPolicy
		}
		key := "exec\x00" + policy.ID
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidRemoteDiagnosticsPolicy
		}
		seen[key] = struct{}{}
	}
	if catalog.file != nil && catalog.file.Validate() != nil {
		return ErrInvalidRemoteDiagnosticsPolicy
	}
	for _, policy := range catalog.diagnostics {
		if policy.Validate() != nil {
			return ErrInvalidRemoteDiagnosticsPolicy
		}
		key := "pod\x00" + policy.ID
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidRemoteDiagnosticsPolicy
		}
		seen[key] = struct{}{}
	}
	return nil
}

func (catalog RemoteDiagnosticsPolicyCatalog) Version() string {
	if catalog.Validate() != nil {
		return ""
	}
	return catalog.version
}

func (catalog RemoteDiagnosticsPolicyCatalog) PodExecPolicies() []PodExecPolicy {
	if catalog.Validate() != nil {
		return nil
	}
	return append([]PodExecPolicy(nil), catalog.podExec...)
}

func (catalog RemoteDiagnosticsPolicyCatalog) ResolvePodExec(id string) (PodExecPolicy, bool) {
	if catalog.Validate() != nil {
		return PodExecPolicy{}, false
	}
	for _, policy := range catalog.podExec {
		if policy.ID == id {
			return policy, true
		}
	}
	return PodExecPolicy{}, false
}

func (catalog RemoteDiagnosticsPolicyCatalog) ContainerFile() (ContainerFilePolicy, bool) {
	if catalog.Validate() != nil || catalog.file == nil {
		return ContainerFilePolicy{}, false
	}
	return *catalog.file, true
}

func (catalog RemoteDiagnosticsPolicyCatalog) DiagnosticPodPolicies() []DiagnosticPodPolicy {
	if catalog.Validate() != nil {
		return nil
	}
	return append([]DiagnosticPodPolicy(nil), catalog.diagnostics...)
}

func (catalog RemoteDiagnosticsPolicyCatalog) ResolveDiagnosticPod(id string) (DiagnosticPodPolicy, bool) {
	if catalog.Validate() != nil {
		return DiagnosticPodPolicy{}, false
	}
	for _, policy := range catalog.diagnostics {
		if policy.ID == id {
			return policy, true
		}
	}
	return DiagnosticPodPolicy{}, false
}

// AdmitsAction proves that one normalized plan is an exact member of this
// frozen catalog. Dynamic target identity and bounded user purpose may vary;
// executable, argv, path roots, diagnostic image/target, and ceilings may not.
func (catalog RemoteDiagnosticsPolicyCatalog) AdmitsAction(plan RemoteDiagnosticActionPlan) bool {
	if catalog.Validate() != nil || plan.Validate() != nil {
		return false
	}
	switch plan.Operation {
	case ActionOperationPodDiagnostic, ActionOperationPodExec:
		for _, policy := range catalog.podExec {
			operation := ActionOperationPodExec
			if policy.Class == PodExecPolicyPredefined {
				operation = ActionOperationPodDiagnostic
			}
			if plan.Operation == operation && plan.Parameters.Executable == policy.Executable && plan.Parameters.Arguments == policy.Arguments &&
				remotePolicyLimitsAdmit(plan.Limits, policy.Timeout, policy.MaxLines, policy.MaxBytes) && plan.Limits.MaximumOutput == plan.Limits.MaximumBytes {
				return true
			}
		}
	case ActionOperationContainerFileRead:
		if catalog.file != nil {
			policy := *catalog.file
			return policy.ReaderExecutable == plan.Parameters.Executable && policy.AllowedRoots.Allows(plan.Parameters.NormalizedPath) &&
				remotePolicyLimitsAdmit(plan.Limits, policy.Timeout, policy.MaxLines, policy.MaxBytes)
		}
	case ActionOperationDiagnosticPod:
		for _, policy := range catalog.diagnostics {
			executable, arguments, err := policy.Command()
			if err == nil && plan.Target.Resource.Namespace == policy.Namespace && plan.Target.Resource.Name == policy.ServiceName &&
				plan.Parameters.Executable == executable && plan.Parameters.Arguments == arguments &&
				plan.Target.Fingerprint == string(policy.ActionFingerprint(plan.Parameters)) &&
				remotePolicyLimitsAdmit(plan.Limits, policy.Timeout, policy.MaxLines, policy.MaxBytes) && plan.Limits.MaximumOutput == plan.Limits.MaximumBytes {
				return true
			}
		}
	}
	return false
}

func remotePolicyLimitsAdmit(limits ActionLimits, timeout time.Duration, maximumLines, maximumBytes int) bool {
	return limits.Validate() == nil && limits.Timeout <= timeout && limits.MaximumLines <= maximumLines && limits.MaximumBytes <= maximumBytes
}

// ActionFingerprint binds a diagnostic Pod plan to the exact catalog ID,
// digest-pinned image, and normalized command without exposing those values in
// durable audit rows.
func (policy DiagnosticPodPolicy) ActionFingerprint(parameters ActionParameters) ActionDigest {
	executable, arguments, err := policy.Command()
	if err != nil || parameters.Validate() != nil || parameters.Kind != ActionParametersRemoteArgv || parameters.Container != "diagnostic" ||
		parameters.Executable != executable || parameters.Arguments != arguments {
		return ""
	}
	return ActionDigest(SHA256Hex("kupilot.diagnostic-pod-policy/v1\n" + policy.ID + "\n" + policy.Image + "\n" + string(parameters.Digest())))
}

func validRemotePolicyID(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, current := range []byte(value) {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '-' || current == '_' {
			continue
		}
		return false
	}
	return true
}

// ValidRemoteExecutable rejects direct shells while admitting one normalized
// absolute executable token. ValidNoShellRemoteCommand also rejects explicit
// shell dispatch through common executable multiplexers.
func ValidRemoteExecutable(value string) bool {
	if !ValidModelToken(value, 1024) || !strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return false
	}
	switch path.Base(value) {
	case "sh", "ash", "bash", "csh", "dash", "zsh", "ksh", "fish", "tcsh", "pwsh", "powershell":
		return false
	default:
		return true
	}
}

// ValidNoShellRemoteCommand validates the complete remote argv contract. A
// policy may still select an exact critical executable with side effects, but
// it cannot disguise a shell behind a common command-dispatch wrapper.
func ValidNoShellRemoteCommand(executable string, arguments ActionArguments) bool {
	if !ValidRemoteExecutable(executable) || !arguments.Valid() {
		return false
	}
	for _, argument := range arguments.Values() {
		if strings.ContainsAny(argument, "\r\n\t") {
			return false
		}
	}
	switch path.Base(executable) {
	case "busybox", "chroot", "doas", "env", "nohup", "nsenter", "runuser", "setpriv", "setsid", "sudo", "timeout", "toybox", "xargs":
		for _, argument := range arguments.Values() {
			if remoteShellToken(argument) {
				return false
			}
		}
	}
	return true
}

func remoteShellToken(value string) bool {
	value = strings.TrimSpace(value)
	if separator := strings.IndexAny(value, " \t=,:;"); separator >= 0 {
		value = value[:separator]
	}
	switch path.Base(value) {
	case "sh", "ash", "bash", "csh", "dash", "zsh", "ksh", "fish", "tcsh", "pwsh", "powershell":
		return true
	default:
		return false
	}
}

// ValidContainerFileReaderExecutable admits only the two absolute tar paths
// supported by the code-owned archive contract.
func ValidContainerFileReaderExecutable(value string) bool {
	return value == "/bin/tar" || value == "/usr/bin/tar"
}

// ValidPinnedContainerImage requires one exact digest-pinned image reference.
func ValidPinnedContainerImage(value string) bool {
	if value == "" || len(value) > 2048 || strings.TrimSpace(value) != value || !ValidModelToken(value, 2048) {
		return false
	}
	separator := strings.Index(value, "@sha256:")
	return strings.Count(value, "@") == 1 && separator > 0 && separator+len("@sha256:")+64 == len(value) &&
		validPinnedImageName(value[:separator]) && validSHA256Hex(value[separator+len("@sha256:"):])
}

func validPinnedImageName(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") ||
		strings.Contains(value, "://") || strings.ContainsAny(value, "\\?#%") {
		return false
	}
	components := strings.Split(value, "/")
	for index, component := range components {
		if component == "" {
			return false
		}
		if index == 0 && len(components) > 1 && strings.Contains(component, ":") {
			host, portValue, found := strings.Cut(component, ":")
			port, err := strconv.ParseUint(portValue, 10, 16)
			if !found || strings.Contains(portValue, ":") || !validImageNameComponent(host, false) || err != nil || port == 0 {
				return false
			}
			continue
		}
		if index != len(components)-1 && strings.Contains(component, ":") {
			return false
		}
		name := component
		if index == len(components)-1 {
			if nameValue, tag, found := strings.Cut(component, ":"); found {
				if strings.Contains(tag, ":") || !validImageTag(tag) {
					return false
				}
				name = nameValue
			}
		}
		if !validImageNameComponent(name, true) {
			return false
		}
	}
	return true
}

func validImageNameComponent(value string, underscore bool) bool {
	if value == "" || !asciiLowerAlphaNumeric(value[0]) || !asciiLowerAlphaNumeric(value[len(value)-1]) {
		return false
	}
	for index := 1; index < len(value)-1; index++ {
		character := value[index]
		if !asciiLowerAlphaNumeric(character) && character != '.' && character != '-' && (!underscore || character != '_') {
			return false
		}
	}
	return true
}

func validImageTag(value string) bool {
	if value == "" || len(value) > 128 || !asciiAlphaNumeric(value[0]) && value[0] != '_' {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !asciiAlphaNumeric(character) && character != '_' && character != '.' && character != '-' {
			return false
		}
	}
	return true
}

func predefinedPodDiagnostic(policy PodExecPolicy) bool {
	values := policy.Arguments.Values()
	switch policy.ID {
	case "dns-config":
		return policy.Executable == "/bin/cat" && len(values) == 1 && values[0] == "/etc/resolv.conf"
	case "process-status":
		return policy.Executable == "/bin/ps" && len(values) == 2 && values[0] == "-eo" && values[1] == "pid,ppid,state,comm"
	default:
		return false
	}
}

func predefinedDiagnosticPodCommand(policy DiagnosticPodPolicy) bool {
	values := policy.ArgumentPrefix.Values()
	return policy.Executable == "/bin/nc" && len(values) == 4 &&
		values[0] == "-z" && values[1] == "-v" && values[2] == "-w" && values[3] == "5" &&
		policy.Timeout >= 6*time.Second
}

// NormalizeContainerFilePath accepts only an already-normalized absolute
// path. It does not resolve symlinks; the in-container archive contract reports
// each component type for strict local validation.
func NormalizeContainerFilePath(value string) (string, error) {
	if value == "" || len(value) > MaxContainerFilePathBytes || !strings.HasPrefix(value, "/") ||
		strings.TrimSpace(value) != value || path.Clean(value) != value || strings.HasSuffix(value, "/") ||
		strings.ContainsAny(value, "\r\n\t") || !ValidModelText(value, MaxContainerFilePathBytes, false) {
		return "", ErrInvalidRemoteDiagnosticsPolicy
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidRemoteDiagnosticsPolicy
		}
	}
	return value, nil
}

const (
	containerFileReaderFixedArgumentCount = 7
	containerFileArchiveBlockBytes        = 512
)

var containerFileReaderArgumentPrefix = []string{
	"--format=ustar", "--blocking-factor=1", "--no-recursion", "--create", "--file=-", "--directory=/", "--",
}

// ContainerFilePathComponents returns each relative parent and the final file
// name in deterministic order for the one-exec archive plan.
func ContainerFilePathComponents(value string) ([]string, error) {
	normalized, err := NormalizeContainerFilePath(value)
	if err != nil || deniedContainerPath(normalized) {
		return nil, ErrInvalidRemoteDiagnosticsPolicy
	}
	segments := strings.Split(strings.TrimPrefix(normalized, "/"), "/")
	if len(segments) > MaxActionArguments-containerFileReaderFixedArgumentCount {
		return nil, ErrInvalidRemoteDiagnosticsPolicy
	}
	result := make([]string, 0, len(segments))
	for index := range segments {
		result = append(result, strings.Join(segments[:index+1], "/"))
	}
	return result, nil
}

// ContainerFileReaderArguments returns the one code-owned no-shell tar argv
// bound into both the ActionEnvelope and the Kubernetes exec request.
func ContainerFileReaderArguments(value string) (ActionArguments, error) {
	components, err := ContainerFilePathComponents(value)
	if err != nil {
		return ActionArguments{}, ErrInvalidRemoteDiagnosticsPolicy
	}
	values := append(append([]string(nil), containerFileReaderArgumentPrefix...), components...)
	arguments, err := NewActionArguments(values)
	if err != nil {
		return ActionArguments{}, ErrInvalidRemoteDiagnosticsPolicy
	}
	return arguments, nil
}

// ContainerFileContentLimit derives the largest file body that fits inside
// the immutable raw archive byte ceiling. USTAR contributes one header per
// requested component, block padding for the body, and two terminal blocks.
func ContainerFileContentLimit(value string, archiveMaximum int) (int, error) {
	components, err := ContainerFilePathComponents(value)
	if err != nil || archiveMaximum < 1 || archiveMaximum > MaxRemoteDiagnosticBytes {
		return 0, ErrInvalidRemoteDiagnosticsPolicy
	}
	framingBytes := (len(components) + 2) * containerFileArchiveBlockBytes
	availableBlocks := (archiveMaximum - framingBytes) / containerFileArchiveBlockBytes
	if availableBlocks < 1 {
		return 0, ErrInvalidRemoteDiagnosticsPolicy
	}
	return availableBlocks * containerFileArchiveBlockBytes, nil
}

func deniedContainerPath(value string) bool {
	for _, denied := range []string{
		"/proc", "/sys", "/dev", "/run/secrets", "/var/run/secrets",
		"/var/lib/kubelet", "/etc/kubernetes", "/etc/ssl/private", "/etc/pki/private",
		"/root/.kube", "/root/.ssh", "/root/.gnupg",
	} {
		if value == denied || strings.HasPrefix(value, denied+"/") {
			return true
		}
	}
	for _, segment := range strings.Split(strings.TrimPrefix(strings.ToLower(value), "/"), "/") {
		switch segment {
		case ".aws", ".azure", ".docker", ".gnupg", ".kube", ".netrc", ".ssh", "credential", "credentials", "kubeconfig", "secret", "secrets", "token":
			return true
		}
	}
	return false
}

// ValidRemoteDiagnosticTarget admits only a Kubernetes Service DNS name. IP
// literals, localhost, metadata, link-local, and external names fail closed.
func ValidRemoteDiagnosticTarget(value string) bool {
	if value == "" || len(value) > 253 || strings.ToLower(value) != value || strings.HasSuffix(value, ".") ||
		strings.Contains(value, "localhost") {
		return false
	}
	if _, err := netip.ParseAddr(value); err == nil {
		return false
	}
	labels := strings.Split(value, ".")
	if len(labels) != 3 || labels[2] != "svc" || !ValidResourceName(labels[0]) || !ValidNamespaceName(labels[1]) {
		return false
	}
	service := labels[0]
	return !strings.Contains(service, "metadata") && !strings.Contains(service, "link-local") &&
		!strings.HasPrefix(service, "127-") && !strings.HasPrefix(service, "169-254-") && service != "0-0-0-0"
}

// MaxRequestTimeoutForRemoteDiagnostic is kept as a function so callers do
// not confuse the action ceiling with an endpoint compatibility claim.
func MaxRequestTimeoutForRemoteDiagnostic() time.Duration { return time.Minute }

// RemoteDiagnosticActionPlan is the complete deterministic operation shape
// before the Application stamps the current permission profile. It carries no
// raw output, credential, vendor value, or execution authority.
type RemoteDiagnosticActionPlan struct {
	RunID                  AgentRunID
	SessionID              SessionID
	Scope                  ClusterScope
	PolicyGeneration       PolicyGeneration
	Operation              ActionOperation
	Target                 ActionTarget
	Parameters             ActionParameters
	Risk                   RiskClass
	Effect                 CapabilityEffectClass
	DataCategories         ActionDataCategories
	AllowedSinks           ActionSinks
	NetworkEffects         ActionNetworkEffects
	NetworkDestinationHash ActionDigest
	Limits                 ActionLimits
	VerificationPlan       string
	ReasonSummary          string
	RiskSummary            string
}

func (plan RemoteDiagnosticActionPlan) Validate() error {
	if !plan.RunID.Valid() || !plan.SessionID.Valid() || plan.Scope.Validate() != nil ||
		!plan.PolicyGeneration.Valid() || plan.Target.Validate() != nil || plan.Parameters.Validate() != nil ||
		plan.Limits.Validate() != nil || !ValidActionReasonSummary(plan.ReasonSummary) {
		return ErrInvalidRemoteDiagnosticPlan
	}
	resource := plan.Target.Resource
	if !plan.Scope.AllowsReference(resource) {
		return ErrInvalidRemoteDiagnosticPlan
	}
	remotePodDestination := RemotePodNetworkDestinationHash(resource, plan.Parameters.Container)
	commonRemote := plan.Target.Subresource == "exec" && resource.APIVersion == "v1" && resource.Kind == "Pod" &&
		plan.Target.Fingerprint == string(plan.Parameters.Digest()) &&
		plan.Parameters.Kind == ActionParametersRemoteArgv && ValidResourceName(plan.Parameters.Container) &&
		ValidNoShellRemoteCommand(plan.Parameters.Executable, plan.Parameters.Arguments) && !plan.Parameters.Arguments.ValuesIsEmpty() &&
		plan.Effect == CapabilityEffectRemoteExecute && plan.AllowedSinks == ActionSinkTerminal|ActionSinkModel|ActionSinkKubernetesAPI &&
		plan.NetworkEffects == ActionNetworkKubernetesAPI|ActionNetworkRemotePod && plan.NetworkDestinationHash == remotePodDestination &&
		plan.Limits.MaximumItems == 1 && plan.Limits.MaximumLines > 0 && plan.Limits.MaximumBytes > 0 &&
		plan.Limits.MaximumOutput > 0
	if commonRemote {
		switch plan.Operation {
		case ActionOperationPodDiagnostic:
			if predefinedPodDiagnosticParameters(plan.Parameters) && plan.Risk == RiskReview && plan.DataCategories == ActionDataContainerOutput &&
				plan.VerificationPlan == PodExecVerificationPlanID && plan.RiskSummary == PodDiagnosticRiskSummary {
				return nil
			}
		case ActionOperationPodExec:
			if plan.Risk == RiskCritical && plan.DataCategories == ActionDataContainerOutput &&
				plan.VerificationPlan == PodExecVerificationPlanID && plan.RiskSummary == PodExecRiskSummary {
				return nil
			}
		}
	}
	if plan.Operation == ActionOperationContainerFileRead && plan.Target.Subresource == "exec" &&
		resource.APIVersion == "v1" && resource.Kind == "Pod" && plan.Target.Fingerprint == string(plan.Parameters.Digest()) && plan.Parameters.Kind == ActionParametersContainerFile &&
		containerFileParametersMatch(plan.Parameters) &&
		plan.Risk == RiskReview && plan.Effect == CapabilityEffectRemoteExecute && plan.DataCategories == ActionDataFileOutput &&
		plan.AllowedSinks == ActionSinkTerminal|ActionSinkModel|ActionSinkKubernetesAPI &&
		plan.NetworkEffects == ActionNetworkKubernetesAPI|ActionNetworkRemotePod && plan.NetworkDestinationHash == remotePodDestination && plan.Limits.MaximumItems == 1 &&
		plan.Limits.MaximumLines > 0 && plan.Limits.MaximumBytes > 0 && plan.Limits.MaximumOutput > 0 &&
		containerFileLimitsMatch(plan.Parameters.NormalizedPath, plan.Limits) &&
		plan.VerificationPlan == ContainerFileVerificationPlanID && plan.RiskSummary == ContainerFileRiskSummary {
		return nil
	}
	if plan.Operation == ActionOperationDiagnosticPod && resource.APIVersion == "v1" && resource.Kind == "Service" &&
		plan.Target.Subresource == "" && plan.Target.Fingerprint != "" &&
		plan.Parameters.Kind == ActionParametersRemoteArgv && plan.Parameters.Container == "diagnostic" && diagnosticPodParametersMatchTarget(plan.Parameters, resource) &&
		plan.Risk == RiskCritical && plan.Effect == CapabilityEffectClusterMutation &&
		plan.DataCategories == ActionDataContainerOutput|ActionDataResourceMetadata &&
		plan.AllowedSinks == ActionSinkTerminal|ActionSinkModel|ActionSinkKubernetesAPI &&
		plan.NetworkEffects == ActionNetworkKubernetesAPI|ActionNetworkRemotePod && plan.NetworkDestinationHash == diagnosticPodNetworkDestinationHash(resource, plan.Parameters) &&
		plan.Limits.MaximumItems == 1 && plan.Limits.MaximumLines > 0 && plan.Limits.MaximumBytes > 0 && plan.Limits.MaximumOutput > 0 &&
		plan.VerificationPlan == DiagnosticPodVerificationPlanID && plan.RiskSummary == DiagnosticPodRiskSummary {
		return nil
	}
	return ErrInvalidRemoteDiagnosticPlan
}

func containerFileLimitsMatch(path string, limits ActionLimits) bool {
	want, err := ContainerFileContentLimit(path, limits.MaximumBytes)
	return err == nil && limits.MaximumOutput == want
}

func containerFileParametersMatch(parameters ActionParameters) bool {
	if !ValidContainerFileReaderExecutable(parameters.Executable) {
		return false
	}
	normalized, err := NormalizeContainerFilePath(parameters.NormalizedPath)
	if err != nil || normalized != parameters.NormalizedPath {
		return false
	}
	want, err := ContainerFileReaderArguments(normalized)
	return err == nil && parameters.Arguments == want
}

func predefinedPodDiagnosticParameters(parameters ActionParameters) bool {
	values := parameters.Arguments.Values()
	switch parameters.Executable {
	case "/bin/cat":
		return len(values) == 1 && values[0] == "/etc/resolv.conf"
	case "/bin/ps":
		return len(values) == 2 && values[0] == "-eo" && values[1] == "pid,ppid,state,comm"
	default:
		return false
	}
}

func diagnosticPodParametersMatchTarget(parameters ActionParameters, target ResourceRef) bool {
	values := parameters.Arguments.Values()
	if parameters.Executable != "/bin/nc" || len(values) != 6 || values[0] != "-z" || values[1] != "-v" || values[2] != "-w" || values[3] != "5" ||
		values[4] != target.Name+"."+target.Namespace+".svc" || !ValidRemoteDiagnosticTarget(values[4]) {
		return false
	}
	port, err := strconv.ParseUint(values[5], 10, 16)
	return err == nil && port > 0
}

// RemotePodNetworkDestinationHash binds the in-container network destination
// to one live Pod identity and one exact container without persisting raw
// endpoint material in decision records.
func RemotePodNetworkDestinationHash(target ResourceRef, container string) ActionDigest {
	if ValidateLiveResourceRef(target) != nil || target.APIVersion != "v1" || target.Kind != "Pod" ||
		target.UID == "" || target.ResourceVersion == "" || !ValidResourceName(container) {
		return ""
	}
	return remoteNetworkDestinationHash("pod", target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID, target.ResourceVersion, container)
}

// DiagnosticPodNetworkDestinationHash binds the only admitted in-cluster
// Service DNS destination, port, and live Service identity.
func DiagnosticPodNetworkDestinationHash(target ResourceRef, host string, port uint16) ActionDigest {
	if ValidateLiveResourceRef(target) != nil || target.APIVersion != "v1" || target.Kind != "Service" ||
		target.UID == "" || target.ResourceVersion == "" || port == 0 || host != target.Name+"."+target.Namespace+".svc" ||
		!ValidRemoteDiagnosticTarget(host) {
		return ""
	}
	return remoteNetworkDestinationHash("service", target.APIVersion, target.Kind, target.Namespace, target.Name, target.UID, target.ResourceVersion, host, strconv.Itoa(int(port)))
}

func diagnosticPodNetworkDestinationHash(target ResourceRef, parameters ActionParameters) ActionDigest {
	values := parameters.Arguments.Values()
	if len(values) != 6 {
		return ""
	}
	port, err := strconv.ParseUint(values[5], 10, 16)
	if err != nil {
		return ""
	}
	return DiagnosticPodNetworkDestinationHash(target, values[4], uint16(port))
}

func remoteNetworkDestinationHash(kind string, values ...string) ActionDigest {
	var builder strings.Builder
	builder.WriteString("kupilot.remote-network-destination/v1\n")
	builder.WriteString(strconv.Itoa(len(kind)))
	builder.WriteByte(':')
	builder.WriteString(kind)
	for _, value := range values {
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	return ActionDigest(SHA256Hex(builder.String()))
}

// MatchesIntent proves that an Application-created envelope contains exactly
// this plan plus the current permission profile.
func (plan RemoteDiagnosticActionPlan) MatchesIntent(intent ActionIntent) bool {
	return plan.Validate() == nil && intent.Validate() == nil &&
		intent.Operation == plan.Operation && intent.OperationSchemaVersion == plan.Operation.SchemaVersion() &&
		intent.PolicyVersion == ActionPolicyVersion && intent.PolicyGeneration == plan.PolicyGeneration &&
		intent.Scope == plan.Scope.Snapshot() && intent.NamespaceAccess == plan.Scope.NamespaceAccess &&
		intent.Target == plan.Target && intent.Parameters == plan.Parameters && !intent.Stdin && !intent.TTY && !intent.Shell &&
		intent.Risk == plan.Risk && intent.Effect == plan.Effect && intent.DataCategories == plan.DataCategories &&
		intent.AllowedSinks == plan.AllowedSinks && intent.NetworkEffects == plan.NetworkEffects && intent.NetworkDestinationHash == plan.NetworkDestinationHash &&
		intent.Limits == plan.Limits && intent.VerificationPlanID == plan.VerificationPlan &&
		intent.ReasonSummary == plan.ReasonSummary && intent.RiskSummary == plan.RiskSummary
}

// ArgumentsValuesIsEmpty is an internal validation helper kept on the value
// type to avoid exposing its fixed backing array.
func (arguments ActionArguments) ValuesIsEmpty() bool { return len(arguments.Values()) == 0 }

type RemoteDiagnosticOutcomeState string

const (
	RemoteDiagnosticOutcomeSucceeded RemoteDiagnosticOutcomeState = "succeeded"
	RemoteDiagnosticOutcomeFailed    RemoteDiagnosticOutcomeState = "failed"
	RemoteDiagnosticOutcomeUnknown   RemoteDiagnosticOutcomeState = "unknown"
)

type DiagnosticPodCleanupState string

const (
	DiagnosticPodCleanupNotNeeded DiagnosticPodCleanupState = "not_needed"
	DiagnosticPodCleanupVerified  DiagnosticPodCleanupState = "verified"
	DiagnosticPodCleanupUnknown   DiagnosticPodCleanupState = "unknown"
)

type DiagnosticPodPhaseState string

const (
	DiagnosticPodPhaseNotAttempted DiagnosticPodPhaseState = "not_attempted"
	DiagnosticPodPhaseCompleted    DiagnosticPodPhaseState = "completed"
	DiagnosticPodPhaseFailed       DiagnosticPodPhaseState = "failed"
	DiagnosticPodPhaseUnknown      DiagnosticPodPhaseState = "unknown"
)

func (state DiagnosticPodPhaseState) Valid() bool {
	return state == DiagnosticPodPhaseNotAttempted || state == DiagnosticPodPhaseCompleted ||
		state == DiagnosticPodPhaseFailed || state == DiagnosticPodPhaseUnknown
}

// DiagnosticPodLifecycle keeps each externally meaningful phase distinct.
// It is content-free and therefore eligible for durable outcome audit.
type DiagnosticPodLifecycle struct {
	Create DiagnosticPodPhaseState
	Wait   DiagnosticPodPhaseState
	Log    DiagnosticPodPhaseState
	Delete DiagnosticPodPhaseState
}

func NotAttemptedDiagnosticPodLifecycle() DiagnosticPodLifecycle {
	return DiagnosticPodLifecycle{
		Create: DiagnosticPodPhaseNotAttempted,
		Wait:   DiagnosticPodPhaseNotAttempted,
		Log:    DiagnosticPodPhaseNotAttempted,
		Delete: DiagnosticPodPhaseNotAttempted,
	}
}

func (lifecycle DiagnosticPodLifecycle) Validate() error {
	if !lifecycle.Create.Valid() || !lifecycle.Wait.Valid() || !lifecycle.Log.Valid() || !lifecycle.Delete.Valid() {
		return ErrInvalidRemoteDiagnosticOutcome
	}
	if lifecycle.Create == DiagnosticPodPhaseNotAttempted &&
		(lifecycle.Wait != DiagnosticPodPhaseNotAttempted || lifecycle.Log != DiagnosticPodPhaseNotAttempted || lifecycle.Delete != DiagnosticPodPhaseNotAttempted) {
		return ErrInvalidRemoteDiagnosticOutcome
	}
	if lifecycle.Create != DiagnosticPodPhaseCompleted &&
		(lifecycle.Wait != DiagnosticPodPhaseNotAttempted || lifecycle.Log != DiagnosticPodPhaseNotAttempted) {
		return ErrInvalidRemoteDiagnosticOutcome
	}
	if lifecycle.Wait != DiagnosticPodPhaseCompleted && lifecycle.Log != DiagnosticPodPhaseNotAttempted {
		return ErrInvalidRemoteDiagnosticOutcome
	}
	return nil
}

// RemoteDiagnosticOutcome is the only projection eligible for action audit.
// Raw stdout, stderr, files, logs, Pod bodies, and error text are absent.
type RemoteDiagnosticOutcome struct {
	State        RemoteDiagnosticOutcomeState
	ErrorClass   SafeErrorClass
	OutputBytes  int
	OutputLines  int
	Truncated    bool
	Lifecycle    DiagnosticPodLifecycle
	CleanupState DiagnosticPodCleanupState
}

func (outcome RemoteDiagnosticOutcome) Validate(operation ActionOperation) error {
	if outcome.OutputBytes < 0 || outcome.OutputBytes > MaxActionBytes ||
		outcome.OutputLines < 0 || outcome.OutputLines > MaxActionLines {
		return ErrInvalidRemoteDiagnosticOutcome
	}
	switch outcome.State {
	case RemoteDiagnosticOutcomeSucceeded:
		if outcome.ErrorClass != "" {
			return ErrInvalidRemoteDiagnosticOutcome
		}
	case RemoteDiagnosticOutcomeFailed, RemoteDiagnosticOutcomeUnknown:
		if !outcome.ErrorClass.Valid() {
			return ErrInvalidRemoteDiagnosticOutcome
		}
	default:
		return ErrInvalidRemoteDiagnosticOutcome
	}
	if operation == ActionOperationDiagnosticPod {
		if outcome.Lifecycle.Validate() != nil {
			return ErrInvalidRemoteDiagnosticOutcome
		}
		if outcome.CleanupState != DiagnosticPodCleanupNotNeeded && outcome.CleanupState != DiagnosticPodCleanupVerified && outcome.CleanupState != DiagnosticPodCleanupUnknown {
			return ErrInvalidRemoteDiagnosticOutcome
		}
		if outcome.State == RemoteDiagnosticOutcomeSucceeded &&
			(outcome.Lifecycle.Create != DiagnosticPodPhaseCompleted || outcome.Lifecycle.Wait != DiagnosticPodPhaseCompleted ||
				outcome.Lifecycle.Log != DiagnosticPodPhaseCompleted || outcome.Lifecycle.Delete != DiagnosticPodPhaseCompleted || outcome.CleanupState != DiagnosticPodCleanupVerified) {
			return ErrInvalidRemoteDiagnosticOutcome
		}
	} else if outcome.Lifecycle != (DiagnosticPodLifecycle{}) || outcome.CleanupState != DiagnosticPodCleanupNotNeeded {
		return ErrInvalidRemoteDiagnosticOutcome
	}
	return nil
}
