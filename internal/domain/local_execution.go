package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

const (
	LocalExecutionPolicyVersion = "kupilot.local-execution-policy/v1"

	MaxLocalCommandPolicies = 32
	MaxLocalPolicyIDBytes   = 128
	MaxLocalOutputLines     = 10000
	MaxLocalOutputBytes     = 4 * 1024 * 1024
	MaxLocalCommandTimeout  = 10 * time.Minute

	LocalCommandVerificationPlanID = "local-command-exit/v1"
	LocalRequestVerificationPlanID = "local-request-accepted/v1"
	ShellCommandVerificationPlanID = "shell-command-exit/v1"
)

var (
	ErrInvalidLocalExecutionPolicy = errors.New("local execution policy is invalid")
	ErrInvalidLocalExecutionPlan   = errors.New("local execution plan is invalid")
	ErrInvalidLocalExecutionResult = errors.New("local execution result is invalid")
)

// LocalCommandKind is a closed executable protocol. A configured path does not
// select parsing semantics or relax the protocol associated with the entry.
type LocalCommandKind string

const (
	LocalCommandKubectl    LocalCommandKind = "kubectl"
	LocalCommandHelm       LocalCommandKind = "helm"
	LocalCommandArgoCD     LocalCommandKind = "argocd"
	LocalCommandDiagnostic LocalCommandKind = "diagnostic"
)

func (kind LocalCommandKind) Valid() bool {
	return kind == LocalCommandKubectl || kind == LocalCommandHelm ||
		kind == LocalCommandArgoCD || kind == LocalCommandDiagnostic
}

// LocalCommandOperation is the result of structural argv classification. It is
// never supplied by model text and is re-derived during every validation.
type LocalCommandOperation string

const (
	LocalOperationKubectlVersion       LocalCommandOperation = "kubectl_version"
	LocalOperationKubectlRolloutStatus LocalCommandOperation = "kubectl_rollout_status"
	LocalOperationKubectlAuthCanI      LocalCommandOperation = "kubectl_auth_can_i"
	LocalOperationHelmList             LocalCommandOperation = "helm_list"
	LocalOperationHelmStatus           LocalCommandOperation = "helm_status"
	LocalOperationHelmHistory          LocalCommandOperation = "helm_history"
	LocalOperationHelmRollback         LocalCommandOperation = "helm_rollback"
	LocalOperationArgoCDAppGet         LocalCommandOperation = "argocd_app_get"
	LocalOperationArgoCDAppDiff        LocalCommandOperation = "argocd_app_diff"
	LocalOperationArgoCDAppSync        LocalCommandOperation = "argocd_app_sync"
	LocalOperationArgoCDAppRollback    LocalCommandOperation = "argocd_app_rollback"
	LocalOperationDiagnostic           LocalCommandOperation = "diagnostic"
)

func (operation LocalCommandOperation) Valid() bool {
	switch operation {
	case LocalOperationKubectlVersion,
		LocalOperationKubectlRolloutStatus,
		LocalOperationKubectlAuthCanI,
		LocalOperationHelmList,
		LocalOperationHelmStatus,
		LocalOperationHelmHistory,
		LocalOperationHelmRollback,
		LocalOperationArgoCDAppGet,
		LocalOperationArgoCDAppDiff,
		LocalOperationArgoCDAppSync,
		LocalOperationArgoCDAppRollback,
		LocalOperationDiagnostic:
		return true
	default:
		return false
	}
}

func (operation LocalCommandOperation) mutates() bool {
	return operation == LocalOperationHelmRollback || operation == LocalOperationArgoCDAppSync ||
		operation == LocalOperationArgoCDAppRollback
}

// LocalCredentialReference is opaque policy identity, never credential data.
type LocalCredentialReference string

const (
	LocalCredentialNone             LocalCredentialReference = "none"
	LocalCredentialArgoCDCLIProfile LocalCredentialReference = "argocd_cli_profile"
)

func (reference LocalCredentialReference) validFor(kind LocalCommandKind) bool {
	if kind == LocalCommandArgoCD {
		return reference == LocalCredentialArgoCDCLIProfile
	}
	return reference == LocalCredentialNone
}

// LocalDiagnosticEffect lets an exact configured diagnostic disclose its
// protocol-level effect. Risk is derived from this value and cannot be lowered.
type LocalDiagnosticEffect string

const (
	LocalDiagnosticNoNetworkRead LocalDiagnosticEffect = "no_network_read"
	LocalDiagnosticExternalRead  LocalDiagnosticEffect = "external_read"
	LocalDiagnosticSideEffect    LocalDiagnosticEffect = "external_side_effect"
)

func (effect LocalDiagnosticEffect) validFor(kind LocalCommandKind) bool {
	if kind != LocalCommandDiagnostic {
		return effect == ""
	}
	return effect == LocalDiagnosticNoNetworkRead || effect == LocalDiagnosticExternalRead ||
		effect == LocalDiagnosticSideEffect
}

// LocalCommandPolicy binds one exact direct-argv entry. The model selects only
// ID; it cannot replace any field in this value.
type LocalCommandPolicy struct {
	ID                  string
	Kind                LocalCommandKind
	Operation           LocalCommandOperation
	Executable          string
	Arguments           ActionArguments
	WorkingDirectory    string
	Environment         ActionEnvironment
	CredentialReference LocalCredentialReference
	ServerOrigin        string
	ServerOriginHash    ActionDigest
	DiagnosticEffect    LocalDiagnosticEffect
	Timeout             time.Duration
	MaxLines            int
	MaxBytes            int
}

func (policy LocalCommandPolicy) Validate() error {
	operation, ok := ClassifyLocalCommand(policy.Kind, policy.Arguments)
	canonicalOrigin, originErr := CanonicalLocalCommandOrigin(policy.ServerOrigin)
	requiresOrigin := policy.Kind == LocalCommandArgoCD ||
		policy.Kind == LocalCommandDiagnostic && policy.DiagnosticEffect != LocalDiagnosticNoNetworkRead
	originValid := !requiresOrigin && policy.ServerOrigin == "" && policy.ServerOriginHash == "" ||
		requiresOrigin && originErr == nil && canonicalOrigin == policy.ServerOrigin &&
			policy.ServerOriginHash == LocalCommandOriginHash(canonicalOrigin)
	if !validLocalPolicyID(policy.ID) || !policy.Kind.Valid() || !ok ||
		policy.Operation != operation || !validAbsoluteActionPath(policy.Executable) ||
		!validLocalExecutable(policy.Executable, policy.Kind, false) || !policy.Arguments.Valid() ||
		!validAbsoluteActionPath(policy.WorkingDirectory) || !policy.Environment.Valid() ||
		!policy.CredentialReference.validFor(policy.Kind) || !policy.DiagnosticEffect.validFor(policy.Kind) ||
		!originValid || policy.Timeout <= 0 || policy.Timeout > MaxLocalCommandTimeout ||
		policy.MaxLines < 1 || policy.MaxLines > MaxLocalOutputLines ||
		policy.MaxBytes < 1 || policy.MaxBytes > MaxLocalOutputBytes {
		return ErrInvalidLocalExecutionPolicy
	}
	return nil
}

func (policy LocalCommandPolicy) Risk() RiskClass {
	if policy.Validate() != nil {
		return RiskDeny
	}
	if policy.Operation.mutates() || policy.DiagnosticEffect == LocalDiagnosticSideEffect {
		return RiskCritical
	}
	return RiskReview
}

func (policy LocalCommandPolicy) NetworkEffects() ActionNetworkEffects {
	if policy.Validate() != nil {
		return 0
	}
	switch policy.Kind {
	case LocalCommandKubectl:
		if policy.Operation == LocalOperationKubectlVersion {
			return ActionNetworkNone
		}
		return ActionNetworkKubernetesAPI
	case LocalCommandHelm:
		return ActionNetworkKubernetesAPI
	case LocalCommandArgoCD:
		return ActionNetworkExternalCommand
	case LocalCommandDiagnostic:
		if policy.DiagnosticEffect == LocalDiagnosticNoNetworkRead {
			return ActionNetworkNone
		}
		return ActionNetworkExternalCommand
	default:
		return 0
	}
}

func (policy LocalCommandPolicy) VerificationPlanID() string {
	if policy.Validate() != nil {
		return ""
	}
	if policy.Operation.mutates() || policy.DiagnosticEffect == LocalDiagnosticSideEffect {
		return LocalRequestVerificationPlanID
	}
	return LocalCommandVerificationPlanID
}

// RuntimeArguments appends only runtime-owned scope/origin flags. Policy argv
// is validated to reject the same reserved flags, so it cannot override them.
func (policy LocalCommandPolicy) RuntimeArguments(scope ClusterScope) (ActionArguments, error) {
	if policy.Validate() != nil || scope.Validate() != nil {
		return ActionArguments{}, ErrInvalidLocalExecutionPolicy
	}
	values := policy.Arguments.Values()
	switch policy.Kind {
	case LocalCommandKubectl:
		if policy.Operation != LocalOperationKubectlVersion {
			values = append(values, "--context", scope.Context, "--namespace", scope.Namespace)
		}
	case LocalCommandHelm:
		values = append(values, "--kube-context", scope.Context, "--namespace", scope.Namespace)
	case LocalCommandArgoCD:
		values = append(values, "--server", policy.ServerOrigin)
	}
	return NewActionArguments(values)
}

// LocalCommandPolicyCatalog is one immutable default-off policy snapshot.
type LocalCommandPolicyCatalog struct {
	entries []LocalCommandPolicy
	digest  ActionDigest
}

// LocalShellNetwork is an explicit destination class for the separately
// admitted shell operation. It does not attempt to infer effects from text.
type LocalShellNetwork string

const (
	LocalShellNetworkNone       LocalShellNetwork = "none"
	LocalShellNetworkKubernetes LocalShellNetwork = "kubernetes_api"
	LocalShellNetworkExternal   LocalShellNetwork = "external"
)

func (network LocalShellNetwork) valid() bool {
	return network == LocalShellNetworkNone || network == LocalShellNetworkKubernetes ||
		network == LocalShellNetworkExternal
}

// LocalShellPolicy is an exact default-off critical command-string entry. It
// is deliberately distinct from LocalCommandPolicy and has no argv fallback.
type LocalShellPolicy struct {
	ID                string
	Executable        string
	Command           string
	WorkingDirectory  string
	Environment       ActionEnvironment
	Network           LocalShellNetwork
	NetworkOrigin     string
	NetworkOriginHash ActionDigest
	Timeout           time.Duration
	MaxLines          int
	MaxBytes          int
}

func (policy LocalShellPolicy) Validate() error {
	canonicalOrigin, originErr := CanonicalLocalCommandOrigin(policy.NetworkOrigin)
	originValid := policy.Network != LocalShellNetworkExternal && policy.NetworkOrigin == "" && policy.NetworkOriginHash == "" ||
		policy.Network == LocalShellNetworkExternal && originErr == nil && canonicalOrigin == policy.NetworkOrigin &&
			policy.NetworkOriginHash == LocalCommandOriginHash(canonicalOrigin)
	if !validLocalPolicyID(policy.ID) || !validAbsoluteActionPath(policy.Executable) ||
		!validLocalExecutable(policy.Executable, "", true) ||
		policy.Command == "" || !validSafeOptionalText(policy.Command, MaxActionShellCommandBytes) || strings.TrimSpace(policy.Command) != policy.Command ||
		!validAbsoluteActionPath(policy.WorkingDirectory) || !policy.Environment.Valid() || !policy.Network.valid() ||
		!originValid || policy.Timeout <= 0 || policy.Timeout > MaxLocalCommandTimeout ||
		policy.MaxLines < 1 || policy.MaxLines > MaxLocalOutputLines ||
		policy.MaxBytes < 1 || policy.MaxBytes > MaxLocalOutputBytes {
		return ErrInvalidLocalExecutionPolicy
	}
	return nil
}

func validLocalPolicyID(value string) bool {
	if value == "" || len(value) > MaxLocalPolicyIDBytes {
		return false
	}
	for index := range len(value) {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			index > 0 && index < len(value)-1 && (character == '-' || character == '_' || character == '.') {
			continue
		}
		return false
	}
	return true
}

func (policy LocalShellPolicy) NetworkEffects() ActionNetworkEffects {
	if policy.Validate() != nil {
		return 0
	}
	switch policy.Network {
	case LocalShellNetworkNone:
		return ActionNetworkNone
	case LocalShellNetworkKubernetes:
		return ActionNetworkKubernetesAPI
	case LocalShellNetworkExternal:
		return ActionNetworkExternalCommand
	default:
		return 0
	}
}

// LocalShellPolicyCatalog is separate so enabling direct argv never enables a
// command-string entry as a side effect.
type LocalShellPolicyCatalog struct {
	entries []LocalShellPolicy
	digest  ActionDigest
}

func NewLocalShellPolicyCatalog(entries []LocalShellPolicy) (LocalShellPolicyCatalog, error) {
	if len(entries) > MaxLocalCommandPolicies {
		return LocalShellPolicyCatalog{}, ErrInvalidLocalExecutionPolicy
	}
	copyEntries := append([]LocalShellPolicy(nil), entries...)
	for index := range copyEntries {
		if copyEntries[index].Validate() != nil {
			return LocalShellPolicyCatalog{}, ErrInvalidLocalExecutionPolicy
		}
		for previous := 0; previous < index; previous++ {
			if copyEntries[previous].ID == copyEntries[index].ID {
				return LocalShellPolicyCatalog{}, ErrInvalidLocalExecutionPolicy
			}
		}
	}
	digest := localShellCatalogDigest(copyEntries)
	return LocalShellPolicyCatalog{entries: copyEntries, digest: digest}, nil
}

func DisabledLocalShellPolicyCatalog() LocalShellPolicyCatalog {
	catalog, _ := NewLocalShellPolicyCatalog(nil)
	return catalog
}

func (catalog LocalShellPolicyCatalog) Validate() error {
	rebuilt, err := NewLocalShellPolicyCatalog(catalog.entries)
	if err != nil || catalog.digest == "" || !catalog.digest.Equal(rebuilt.digest) {
		return ErrInvalidLocalExecutionPolicy
	}
	return nil
}

func (catalog LocalShellPolicyCatalog) Version() string {
	if catalog.Validate() != nil {
		return ""
	}
	return LocalExecutionPolicyVersion
}

func (catalog LocalShellPolicyCatalog) Digest() ActionDigest {
	if catalog.Validate() != nil {
		return ""
	}
	return catalog.digest
}

func (catalog LocalShellPolicyCatalog) Policies() []LocalShellPolicy {
	if catalog.Validate() != nil {
		return nil
	}
	return append([]LocalShellPolicy(nil), catalog.entries...)
}

func (catalog LocalShellPolicyCatalog) Resolve(id string) (LocalShellPolicy, bool) {
	if catalog.Validate() != nil {
		return LocalShellPolicy{}, false
	}
	for _, entry := range catalog.entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return LocalShellPolicy{}, false
}

func localShellCatalogDigest(entries []LocalShellPolicy) ActionDigest {
	var builder strings.Builder
	builder.WriteString(LocalExecutionPolicyVersion)
	builder.WriteString("\nshell\n")
	for _, entry := range entries {
		fields := []string{
			entry.ID, entry.Executable, entry.Command, entry.WorkingDirectory, entry.Environment.canonical(),
			string(entry.Network), entry.NetworkOrigin, string(entry.NetworkOriginHash),
			strconv.FormatInt(entry.Timeout.Milliseconds(), 10), strconv.Itoa(entry.MaxLines), strconv.Itoa(entry.MaxBytes),
		}
		for _, field := range fields {
			builder.WriteString(strconv.Itoa(len(field)))
			builder.WriteByte(':')
			builder.WriteString(field)
		}
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return ActionDigest(hex.EncodeToString(digest[:]))
}

func NewLocalCommandPolicyCatalog(entries []LocalCommandPolicy) (LocalCommandPolicyCatalog, error) {
	if len(entries) > MaxLocalCommandPolicies {
		return LocalCommandPolicyCatalog{}, ErrInvalidLocalExecutionPolicy
	}
	copyEntries := append([]LocalCommandPolicy(nil), entries...)
	for index := range copyEntries {
		if copyEntries[index].Validate() != nil {
			return LocalCommandPolicyCatalog{}, ErrInvalidLocalExecutionPolicy
		}
		for previous := 0; previous < index; previous++ {
			if copyEntries[previous].ID == copyEntries[index].ID {
				return LocalCommandPolicyCatalog{}, ErrInvalidLocalExecutionPolicy
			}
		}
	}
	digest := localCommandCatalogDigest(copyEntries)
	return LocalCommandPolicyCatalog{entries: copyEntries, digest: digest}, nil
}

func DisabledLocalCommandPolicyCatalog() LocalCommandPolicyCatalog {
	catalog, _ := NewLocalCommandPolicyCatalog(nil)
	return catalog
}

func (catalog LocalCommandPolicyCatalog) Validate() error {
	rebuilt, err := NewLocalCommandPolicyCatalog(catalog.entries)
	if err != nil || catalog.digest == "" || !catalog.digest.Equal(rebuilt.digest) {
		return ErrInvalidLocalExecutionPolicy
	}
	return nil
}

func (catalog LocalCommandPolicyCatalog) Version() string {
	if catalog.Validate() != nil {
		return ""
	}
	return LocalExecutionPolicyVersion
}

func (catalog LocalCommandPolicyCatalog) Digest() ActionDigest {
	if catalog.Validate() != nil {
		return ""
	}
	return catalog.digest
}

func (catalog LocalCommandPolicyCatalog) Policies() []LocalCommandPolicy {
	if catalog.Validate() != nil {
		return nil
	}
	return append([]LocalCommandPolicy(nil), catalog.entries...)
}

func (catalog LocalCommandPolicyCatalog) Resolve(id string) (LocalCommandPolicy, bool) {
	if catalog.Validate() != nil {
		return LocalCommandPolicy{}, false
	}
	for _, entry := range catalog.entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return LocalCommandPolicy{}, false
}

func localCommandCatalogDigest(entries []LocalCommandPolicy) ActionDigest {
	var builder strings.Builder
	builder.WriteString(LocalExecutionPolicyVersion)
	builder.WriteByte('\n')
	for _, entry := range entries {
		fields := []string{
			entry.ID, string(entry.Kind), string(entry.Operation), entry.Executable,
			entry.Arguments.canonical(), entry.WorkingDirectory, entry.Environment.canonical(),
			string(entry.CredentialReference), entry.ServerOrigin, string(entry.ServerOriginHash),
			string(entry.DiagnosticEffect), strconv.FormatInt(entry.Timeout.Milliseconds(), 10),
			strconv.Itoa(entry.MaxLines), strconv.Itoa(entry.MaxBytes),
		}
		for _, field := range fields {
			builder.WriteString(strconv.Itoa(len(field)))
			builder.WriteByte(':')
			builder.WriteString(field)
		}
	}
	digest := sha256.Sum256([]byte(builder.String()))
	return ActionDigest(hex.EncodeToString(digest[:]))
}

// ClassifyLocalCommand parses a bounded argv vector by exact tokens and
// positions. It does not rely on a regular expression or shell interpretation.
func ClassifyLocalCommand(kind LocalCommandKind, arguments ActionArguments) (LocalCommandOperation, bool) {
	values := arguments.Values()
	if !kind.Valid() || values == nil || localArgumentsContainReservedFlags(kind, values) {
		return "", false
	}
	switch kind {
	case LocalCommandKubectl:
		switch {
		case equalStrings(values, []string{"version", "--client=true"}):
			return LocalOperationKubectlVersion, true
		case len(values) == 4 && values[0] == "rollout" && values[1] == "status" &&
			validKubectlRolloutTarget(values[2]) && values[3] == "--watch=false":
			return LocalOperationKubectlRolloutStatus, true
		case len(values) == 4 && values[0] == "auth" && values[1] == "can-i" &&
			(values[2] == "get" || values[2] == "list") && validKubectlReadResource(values[3]):
			return LocalOperationKubectlAuthCanI, true
		}
	case LocalCommandHelm:
		switch {
		case equalStrings(values, []string{"list", "--max", "50"}):
			return LocalOperationHelmList, true
		case len(values) == 2 && values[0] == "status" && ValidResourceName(values[1]):
			return LocalOperationHelmStatus, true
		case len(values) == 4 && values[0] == "history" && ValidResourceName(values[1]) &&
			values[2] == "--max" && values[3] == "20":
			return LocalOperationHelmHistory, true
		case len(values) == 3 && values[0] == "rollback" && ValidResourceName(values[1]) && validPositiveDecimal(values[2]):
			return LocalOperationHelmRollback, true
		}
	case LocalCommandArgoCD:
		switch {
		case len(values) == 3 && values[0] == "app" && values[1] == "get" && ValidResourceName(values[2]):
			return LocalOperationArgoCDAppGet, true
		case len(values) == 5 && values[0] == "app" && values[1] == "diff" && ValidResourceName(values[2]) &&
			values[3] == "--revision" && validRevisionToken(values[4]):
			return LocalOperationArgoCDAppDiff, true
		case len(values) == 5 && values[0] == "app" && values[1] == "sync" && ValidResourceName(values[2]) &&
			values[3] == "--revision" && validRevisionToken(values[4]):
			return LocalOperationArgoCDAppSync, true
		case len(values) == 4 && values[0] == "app" && values[1] == "rollback" && ValidResourceName(values[2]) && validPositiveDecimal(values[3]):
			return LocalOperationArgoCDAppRollback, true
		}
	case LocalCommandDiagnostic:
		if validDirectDiagnosticArguments(values) {
			return LocalOperationDiagnostic, true
		}
	}
	return "", false
}

func localArgumentsContainReservedFlags(kind LocalCommandKind, values []string) bool {
	reserved := []string{
		"--kubeconfig", "--context", "--token", "--as", "--as-group", "--as-uid",
		"--client-certificate", "--client-key", "--certificate-authority", "--server",
		"--insecure-skip-tls-verify", "--username", "--password", "--request-timeout",
		"--kube-context", "--registry-config", "--repository-config", "--repository-cache",
		"--ca-file", "--cert-file", "--key-file", "--post-renderer", "--post-renderer-args",
		"--values", "--set", "--set-string", "--set-file", "--set-json", "--config",
		"--auth-token", "--sso", "--core", "--plaintext", "--insecure",
	}
	if kind == LocalCommandDiagnostic {
		return false
	}
	for _, value := range values {
		for _, flag := range reserved {
			if value == flag || strings.HasPrefix(value, flag+"=") {
				return true
			}
		}
		if value == "-f" || strings.HasPrefix(value, "-f=") || value == "-n" || strings.HasPrefix(value, "-n=") {
			return true
		}
	}
	return false
}

func validKubectlRolloutTarget(value string) bool {
	kind, name, found := strings.Cut(value, "/")
	return found && (kind == "deployment" || kind == "statefulset" || kind == "daemonset") && ValidResourceName(name)
}

func validKubectlReadResource(value string) bool {
	switch value {
	case "pods", "deployments.apps", "statefulsets.apps", "daemonsets.apps", "nodes", "events":
		return true
	default:
		return false
	}
}

func validPositiveDecimal(value string) bool {
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatInt(parsed, 10) == value
}

func validRevisionToken(value string) bool {
	if value == "" || len(value) > 253 || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range []byte(value) {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' ||
			current >= '0' && current <= '9' || current == '.' || current == '-' || current == '_' || current == '/' {
			continue
		}
		return false
	}
	return true
}

func validDirectDiagnosticArguments(values []string) bool {
	if len(values) == 0 || len(values) > MaxActionArguments {
		return false
	}
	for _, value := range values {
		if directDiagnosticArgumentCarriesCode(value) {
			return false
		}
	}
	return true
}

// directDiagnosticArgumentCarriesCode rejects argv shapes commonly used to
// load commands, scripts, configuration, or plugins. This is a structural
// protocol guard, not a claim that an administrator-selected native binary is
// sandboxed or that every executable has identical flag semantics.
func directDiagnosticArgumentCarriesCode(value string) bool {
	if value == "" || strings.HasPrefix(value, "@") {
		return true
	}
	flag := value
	if before, _, found := strings.Cut(value, "="); found {
		flag = before
	}
	switch flag {
	case "-c", "-e", "-f",
		"--command", "--eval", "--execute", "--exec", "--shell",
		"--script", "--file", "--config", "--config-file", "--rcfile",
		"--init-file", "--require", "--load", "--plugin", "--extension":
		return true
	}
	if strings.HasPrefix(value, "-") {
		return false
	}
	lower := strings.ToLower(value)
	for _, suffix := range []string{
		".sh", ".bash", ".zsh", ".ksh", ".fish", ".py", ".pyw", ".pl",
		".rb", ".js", ".mjs", ".cjs", ".lua", ".php", ".ps1", ".cmd", ".bat",
	} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func validLocalExecutable(executable string, kind LocalCommandKind, shell bool) bool {
	base := path.Base(executable)
	if shell {
		return base == "sh" || base == "bash" || base == "zsh" || base == "dash"
	}
	if kind == LocalCommandKubectl && base != "kubectl" || kind == LocalCommandHelm && base != "helm" ||
		kind == LocalCommandArgoCD && base != "argocd" {
		return false
	}
	if kind == LocalCommandDiagnostic && (base == "kubectl" || base == "helm" || base == "argocd") {
		return false
	}
	for _, denied := range []string{
		"sh", "bash", "zsh", "dash", "ksh", "fish", "env", "xargs", "sudo", "nohup", "script",
		"python", "python3", "perl", "ruby", "node", "osascript", "pwsh", "powershell", "cmd",
		"make", "just", "task", "busybox", "toybox", "docker", "podman", "awk", "gawk", "mawk",
		"nawk", "sed", "find", "git", "ssh", "timeout", "nice", "chroot", "open", "xdg-open",
	} {
		if base == denied {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// CanonicalLocalCommandOrigin validates a bare HTTPS origin, or loopback-only
// HTTP, with no userinfo, path, query, or fragment.
func CanonicalLocalCommandOrigin(raw string) (string, error) {
	if raw == "" || len(raw) > 2048 || strings.TrimSpace(raw) != raw {
		return "", ErrInvalidLocalExecutionPolicy
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path != "" && parsed.Path != "/" || parsed.Hostname() == "" ||
		parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", ErrInvalidLocalExecutionPolicy
	}
	host := strings.ToLower(parsed.Hostname())
	if !validOriginHost(host) || parsed.Scheme == "http" && !localOriginLoopback(host) {
		return "", ErrInvalidLocalExecutionPolicy
	}
	port := parsed.Port()
	if port != "" {
		value, portErr := strconv.ParseUint(port, 10, 16)
		if portErr != nil || value == 0 {
			return "", ErrInvalidLocalExecutionPolicy
		}
		port = strconv.FormatUint(value, 10)
	}
	hostPort := host
	if port != "" {
		hostPort = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		hostPort = "[" + host + "]"
	}
	return parsed.Scheme + "://" + hostPort, nil
}

func validOriginHost(host string) bool {
	if net.ParseIP(host) != nil || host == "localhost" {
		return true
	}
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !ValidNamespaceName(label) {
			return false
		}
	}
	return true
}

func localOriginLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func LocalCommandOriginHash(origin string) ActionDigest {
	canonical, err := CanonicalLocalCommandOrigin(origin)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256([]byte("kupilot.local-command-origin/v1\n" + canonical))
	return ActionDigest(hex.EncodeToString(digest[:]))
}

// LocalExecutionObservation binds the exact executable and cwd filesystem
// identities observed without retaining OS-specific stat data.
type LocalExecutionObservation struct {
	ExecutableID       ActionDigest
	WorkingDirectoryID ActionDigest
}

func (observation LocalExecutionObservation) Validate() error {
	if !observation.ExecutableID.Valid() || !observation.WorkingDirectoryID.Valid() {
		return ErrInvalidLocalExecutionPlan
	}
	return nil
}

// LocalCommandActionPlan is the project-owned input to approval and execution.
type LocalCommandActionPlan struct {
	RunID            AgentRunID
	SessionID        SessionID
	Scope            ClusterScope
	PolicyGeneration PolicyGeneration
	NamespaceTarget  ResourceRef
	Policy           LocalCommandPolicy
	Arguments        ActionArguments
	Observation      LocalExecutionObservation
	Limits           ActionLimits
	Purpose          string
}

func (plan LocalCommandActionPlan) Validate() error {
	expectedArguments, argumentsErr := plan.Policy.RuntimeArguments(plan.Scope)
	if !plan.RunID.Valid() || !plan.SessionID.Valid() || plan.Scope.Validate() != nil ||
		!plan.PolicyGeneration.Valid() || plan.NamespaceTarget.Validate() != nil ||
		plan.NamespaceTarget.APIVersion != "v1" || plan.NamespaceTarget.Kind != "Namespace" ||
		plan.NamespaceTarget.Namespace != "" || plan.NamespaceTarget.Name != plan.Scope.Namespace ||
		plan.NamespaceTarget.UID == "" || plan.NamespaceTarget.ResourceVersion == "" ||
		plan.Policy.Validate() != nil || argumentsErr != nil || plan.Arguments != expectedArguments ||
		plan.Observation.Validate() != nil || plan.Limits != (ActionLimits{
		Timeout: plan.Policy.Timeout, MaximumItems: 1, MaximumLines: plan.Policy.MaxLines,
		MaximumBytes: plan.Policy.MaxBytes, MaximumOutput: plan.Policy.MaxBytes,
	}) || !ValidActionReasonSummary(plan.Purpose) {
		return ErrInvalidLocalExecutionPlan
	}
	return nil
}

func (plan LocalCommandActionPlan) Intent(profile PermissionProfile) (ActionIntent, error) {
	if plan.Validate() != nil || !profile.Valid() {
		return ActionIntent{}, ErrInvalidLocalExecutionPlan
	}
	parameters := ActionParameters{
		Kind: ActionParametersLocalArgv, PolicyID: plan.Policy.ID,
		Executable: plan.Policy.Executable, ExecutableID: plan.Observation.ExecutableID,
		Arguments: plan.Arguments, WorkingDirectory: plan.Policy.WorkingDirectory,
		WorkingDirectoryID: plan.Observation.WorkingDirectoryID, Environment: plan.Policy.Environment,
		CredentialReference: plan.Policy.CredentialReference,
	}
	destination := ActionDigest("")
	if plan.Policy.NetworkEffects() == ActionNetworkExternalCommand {
		destination = plan.Policy.ServerOriginHash
	}
	riskSummary := "The approved process runs one exact direct argv vector without a shell or inherited environment."
	if plan.Policy.Risk() == RiskCritical {
		riskSummary = "The approved process may change external state and runs without an OS sandbox; its request outcome may be ambiguous."
	}
	intent := ActionIntent{
		Operation: ActionOperationRestrictedLocalArgv, OperationSchemaVersion: ActionOperationRestrictedLocalArgv.SchemaVersion(),
		PolicyVersion: ActionPolicyVersion, PermissionProfile: profile, Risk: plan.Policy.Risk(),
		Effect: CapabilityEffectLocalExecute, PolicyGeneration: plan.PolicyGeneration,
		Scope: plan.Scope.Snapshot(), NamespaceAccess: plan.Scope.NamespaceAccess,
		Target:     ActionTarget{Resource: plan.NamespaceTarget, Fingerprint: string(plan.PolicyCatalogBinding())},
		Parameters: parameters, Stdin: false, TTY: false, Shell: false,
		DataCategories: ActionDataProcessOutput,
		AllowedSinks:   ActionSinkTerminal | ActionSinkLocalProcess,
		NetworkEffects: plan.Policy.NetworkEffects(), NetworkDestinationHash: destination,
		Limits: plan.Limits, VerificationPlanID: plan.Policy.VerificationPlanID(),
		ReasonSummary: plan.Purpose, RiskSummary: riskSummary,
	}
	if intent.ValidateLocalCommand() != nil {
		return ActionIntent{}, ErrInvalidLocalExecutionPlan
	}
	return intent, nil
}

func (plan LocalCommandActionPlan) PolicyCatalogBinding() ActionDigest {
	if plan.Policy.Validate() != nil {
		return ""
	}
	return localCommandCatalogDigest([]LocalCommandPolicy{plan.Policy})
}

func (plan LocalCommandActionPlan) MatchesIntent(intent ActionIntent) bool {
	if plan.Validate() != nil || intent.ValidateLocalCommand() != nil {
		return false
	}
	want, err := plan.Intent(intent.PermissionProfile)
	return err == nil && want == intent
}

// ValidateLocalCommand checks the exact direct-process ActionEnvelope branch.
func (intent ActionIntent) ValidateLocalCommand() error {
	if intent.Validate() != nil || intent.Operation != ActionOperationRestrictedLocalArgv ||
		intent.Target.Resource.APIVersion != "v1" || intent.Target.Resource.Kind != "Namespace" ||
		intent.Target.Resource.Namespace != "" || intent.Target.Resource.Name != intent.Scope.Namespace ||
		intent.Target.Subresource != "" || intent.Target.Fingerprint == "" || intent.Target.Generation != 0 ||
		intent.Target.Revision != 0 || intent.Target.TargetSetDigest != "" || intent.Target.TargetCount != 0 ||
		intent.Parameters.Kind != ActionParametersLocalArgv || intent.Stdin || intent.TTY || intent.Shell ||
		intent.Risk != RiskReview && intent.Risk != RiskCritical || intent.Effect != CapabilityEffectLocalExecute ||
		intent.DataCategories != ActionDataProcessOutput ||
		intent.AllowedSinks != ActionSinkTerminal|ActionSinkLocalProcess ||
		intent.NetworkEffects != ActionNetworkNone && intent.NetworkEffects != ActionNetworkKubernetesAPI &&
			intent.NetworkEffects != ActionNetworkExternalCommand ||
		(intent.NetworkEffects == ActionNetworkExternalCommand) != intent.NetworkDestinationHash.Valid() ||
		intent.NetworkEffects != ActionNetworkExternalCommand && intent.NetworkDestinationHash != "" ||
		intent.Limits.MaximumItems != 1 || intent.Limits.MaximumLines < 1 || intent.Limits.MaximumBytes < 1 ||
		intent.Limits.MaximumOutput != intent.Limits.MaximumBytes ||
		intent.VerificationPlanID != LocalCommandVerificationPlanID && intent.VerificationPlanID != LocalRequestVerificationPlanID {
		return ErrInvalidActionIntent
	}
	return nil
}

// LocalShellActionPlan is the project-owned input to the separate critical
// shell approval. The command string is policy-owned and never reconstructed
// from an argv vector.
type LocalShellActionPlan struct {
	RunID            AgentRunID
	SessionID        SessionID
	Scope            ClusterScope
	PolicyGeneration PolicyGeneration
	NamespaceTarget  ResourceRef
	Policy           LocalShellPolicy
	Observation      LocalExecutionObservation
	Limits           ActionLimits
	Purpose          string
}

func (plan LocalShellActionPlan) Validate() error {
	if !plan.RunID.Valid() || !plan.SessionID.Valid() || plan.Scope.Validate() != nil ||
		!plan.PolicyGeneration.Valid() || plan.NamespaceTarget.Validate() != nil ||
		plan.NamespaceTarget.APIVersion != "v1" || plan.NamespaceTarget.Kind != "Namespace" ||
		plan.NamespaceTarget.Namespace != "" || plan.NamespaceTarget.Name != plan.Scope.Namespace ||
		plan.NamespaceTarget.UID == "" || plan.NamespaceTarget.ResourceVersion == "" ||
		plan.Policy.Validate() != nil || plan.Observation.Validate() != nil ||
		plan.Limits != (ActionLimits{
			Timeout: plan.Policy.Timeout, MaximumItems: 1, MaximumLines: plan.Policy.MaxLines,
			MaximumBytes: plan.Policy.MaxBytes, MaximumOutput: plan.Policy.MaxBytes,
		}) || !ValidActionReasonSummary(plan.Purpose) {
		return ErrInvalidLocalExecutionPlan
	}
	return nil
}

func (plan LocalShellActionPlan) PolicyCatalogBinding() ActionDigest {
	if plan.Policy.Validate() != nil {
		return ""
	}
	return localShellCatalogDigest([]LocalShellPolicy{plan.Policy})
}

func (plan LocalShellActionPlan) Intent(profile PermissionProfile) (ActionIntent, error) {
	if plan.Validate() != nil || !profile.Valid() {
		return ActionIntent{}, ErrInvalidLocalExecutionPlan
	}
	parameters := ActionParameters{
		Kind: ActionParametersShellCommand, PolicyID: plan.Policy.ID,
		Executable: plan.Policy.Executable, ExecutableID: plan.Observation.ExecutableID,
		WorkingDirectory: plan.Policy.WorkingDirectory, WorkingDirectoryID: plan.Observation.WorkingDirectoryID,
		Environment: plan.Policy.Environment, ShellCommand: plan.Policy.Command,
	}
	destination := ActionDigest("")
	if plan.Policy.NetworkEffects() == ActionNetworkExternalCommand {
		destination = plan.Policy.NetworkOriginHash
	}
	intent := ActionIntent{
		Operation: ActionOperationShell, OperationSchemaVersion: ActionOperationShell.SchemaVersion(),
		PolicyVersion: ActionPolicyVersion, PermissionProfile: profile, Risk: RiskCritical,
		Effect: CapabilityEffectLocalExecute, PolicyGeneration: plan.PolicyGeneration,
		Scope: plan.Scope.Snapshot(), NamespaceAccess: plan.Scope.NamespaceAccess,
		Target:     ActionTarget{Resource: plan.NamespaceTarget, Fingerprint: string(plan.PolicyCatalogBinding())},
		Parameters: parameters, Stdin: false, TTY: false, Shell: true,
		DataCategories: ActionDataProcessOutput,
		AllowedSinks:   ActionSinkTerminal | ActionSinkLocalProcess,
		NetworkEffects: plan.Policy.NetworkEffects(), NetworkDestinationHash: destination,
		Limits: plan.Limits, VerificationPlanID: ShellCommandVerificationPlanID,
		ReasonSummary: plan.Purpose,
		RiskSummary:   "The approved exact command string runs through a shell without an OS sandbox and may have unbounded semantic side effects within process permissions.",
	}
	if intent.ValidateShellCommand() != nil {
		return ActionIntent{}, ErrInvalidLocalExecutionPlan
	}
	return intent, nil
}

func (plan LocalShellActionPlan) MatchesIntent(intent ActionIntent) bool {
	if plan.Validate() != nil || intent.ValidateShellCommand() != nil {
		return false
	}
	want, err := plan.Intent(intent.PermissionProfile)
	return err == nil && want == intent
}

// ValidateShellCommand checks the separately tagged command-string branch.
func (intent ActionIntent) ValidateShellCommand() error {
	if intent.Validate() != nil || intent.Operation != ActionOperationShell ||
		intent.Target.Resource.APIVersion != "v1" || intent.Target.Resource.Kind != "Namespace" ||
		intent.Target.Resource.Namespace != "" || intent.Target.Resource.Name != intent.Scope.Namespace ||
		intent.Target.Subresource != "" || intent.Target.Fingerprint == "" || intent.Target.Generation != 0 ||
		intent.Target.Revision != 0 || intent.Target.TargetSetDigest != "" || intent.Target.TargetCount != 0 ||
		intent.Parameters.Kind != ActionParametersShellCommand || intent.Stdin || intent.TTY || !intent.Shell ||
		intent.Risk != RiskCritical || intent.Effect != CapabilityEffectLocalExecute ||
		intent.DataCategories != ActionDataProcessOutput ||
		intent.AllowedSinks != ActionSinkTerminal|ActionSinkLocalProcess ||
		intent.NetworkEffects != ActionNetworkNone && intent.NetworkEffects != ActionNetworkKubernetesAPI &&
			intent.NetworkEffects != ActionNetworkExternalCommand ||
		(intent.NetworkEffects == ActionNetworkExternalCommand) != intent.NetworkDestinationHash.Valid() ||
		intent.NetworkEffects != ActionNetworkExternalCommand && intent.NetworkDestinationHash != "" ||
		intent.Limits.MaximumItems != 1 || intent.Limits.MaximumLines < 1 || intent.Limits.MaximumBytes < 1 ||
		intent.Limits.MaximumOutput != intent.Limits.MaximumBytes || intent.VerificationPlanID != ShellCommandVerificationPlanID {
		return ErrInvalidActionIntent
	}
	return nil
}

// LocalProcessState separates start, exit, definitive failure, and ambiguous
// post-start cancellation/timeout. It never implies typed write verification.
type LocalProcessState string

const (
	LocalProcessNotAttempted  LocalProcessState = "not_attempted"
	LocalProcessExited        LocalProcessState = "process_exited"
	LocalProcessOutputBlocked LocalProcessState = "output_blocked"
	LocalProcessFailed        LocalProcessState = "process_failed"
	LocalProcessUnknown       LocalProcessState = "process_outcome_unknown"
)

// LocalCommandResult contains only output after adapter-owned terminal,
// Unicode, sensitive-value, line, and byte processing.
type LocalCommandResult struct {
	State          LocalProcessState
	SafeOutput     string
	OutputDigest   ActionDigest
	ExitCode       int
	Started        bool
	Truncated      bool
	LineCount      int
	ByteCount      int
	RedactionCount int
	ErrorClass     SafeErrorClass
}

func (result LocalCommandResult) Validate(limits ActionLimits) error {
	if limits.Validate() != nil || result.ExitCode < 0 || result.LineCount < 0 || result.LineCount > limits.MaximumLines ||
		result.ByteCount < 0 || result.ByteCount > limits.MaximumOutput || result.RedactionCount < 0 ||
		len(result.SafeOutput) > limits.MaximumOutput || !result.OutputDigest.Valid() ||
		result.SafeOutput != "" && !ValidModelText(result.SafeOutput, limits.MaximumOutput, false) {
		return ErrInvalidLocalExecutionResult
	}
	switch result.State {
	case LocalProcessNotAttempted:
		if result.Started || result.ExitCode != 0 || result.SafeOutput != "" || result.LineCount != 0 || result.ByteCount != 0 ||
			result.Truncated || result.RedactionCount != 0 || !result.ErrorClass.Valid() {
			return ErrInvalidLocalExecutionResult
		}
	case LocalProcessExited:
		if !result.Started || result.ErrorClass != "" {
			return ErrInvalidLocalExecutionResult
		}
	case LocalProcessOutputBlocked:
		if !result.Started || result.ExitCode != 0 || result.SafeOutput != "" || result.LineCount != 0 ||
			result.ByteCount != 0 || result.RedactionCount != 0 || result.ErrorClass != SafeErrorClassSensitiveOutputBlocked {
			return ErrInvalidLocalExecutionResult
		}
	case LocalProcessFailed:
		if !result.Started || !result.ErrorClass.Valid() {
			return ErrInvalidLocalExecutionResult
		}
	case LocalProcessUnknown:
		if !result.Started || !result.ErrorClass.Valid() {
			return ErrInvalidLocalExecutionResult
		}
	default:
		return ErrInvalidLocalExecutionResult
	}
	return nil
}

func LocalSafeOutputDigest(value string) ActionDigest {
	digest := sha256.Sum256([]byte("kupilot.local-safe-output/v1\n" + value))
	return ActionDigest(hex.EncodeToString(digest[:]))
}
