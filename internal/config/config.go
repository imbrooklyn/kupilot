package config

import (
	"sort"
	"time"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	LegacyVersion                     = 1
	CurrentVersion                    = 2
	ProviderOpenAICompatible          = "openai_compatible"
	ModelReasoningEffortNone          = "none"
	BudgetProfileCompact              = "compact"
	BudgetProfileBalanced             = "balanced"
	BudgetProfileExtended             = "extended"
	DefaultBudgetProfile              = BudgetProfileBalanced
	NamespaceAccessCurrent            = "current"
	NamespaceAccessAll                = "all"
	DefaultNamespaceAccess            = NamespaceAccessAll
	ExecCredentialsAllow              = "allow"
	ExecCredentialsDeny               = "deny"
	DefaultModelTemperature           = 0.1
	LegacyDefaultMaxModelOutputTokens = 8192
	DefaultModelRequestTimeoutSeconds = 900
	DefaultReviewerTimeoutSeconds     = 30
	MaxModelRequestTimeoutSeconds     = 900
	MaxContextBytes                   = 253
	MaxNamespaceBytes                 = 63
	DefaultNamespace                  = "default"
	MaxModelIdentifierBytes           = 128
	MaxModelProfileNameBytes          = 128
	MaxPathBytes                      = 4096
	DefaultDataSourceTimeoutSeconds   = 15
	MaxDataSourceTimeoutSeconds       = 60
)

// ModelRole is one fixed consumer binding admitted by the configuration
// schema. It is deliberately not an arbitrary routing key.
type ModelRole string

const (
	ModelRoleAgent            ModelRole = "agent"
	ModelRoleApprovalReviewer ModelRole = "approval_reviewer"
)

func (role ModelRole) valid() bool {
	return role == ModelRoleAgent || role == ModelRoleApprovalReviewer
}

// ModelCredentialReference selects one of the two fixed opaque credential
// slots. It never contains a credential or arbitrary lookup key.
type ModelCredentialReference string

const (
	ModelCredentialAgent            ModelCredentialReference = "agent"
	ModelCredentialApprovalReviewer ModelCredentialReference = "approval_reviewer"
)

func (reference ModelCredentialReference) valid() bool {
	return reference == ModelCredentialAgent || reference == ModelCredentialApprovalReviewer
}

// Config is the complete serializable, non-sensitive startup configuration.
// Paths and transport credentials are intentionally absent.
type Config struct {
	Version              int                  `yaml:"version" json:"version"`
	Context              string               `yaml:"context,omitempty" json:"context,omitempty"`
	Namespace            string               `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	NoColor              bool                 `yaml:"no_color" json:"no_color"`
	ReducedMotion        bool                 `yaml:"reduced_motion" json:"reduced_motion"`
	TerminalStatusTitles bool                 `yaml:"terminal_status_titles" json:"terminal_status_titles"`
	Runtime              RuntimeConfig        `yaml:"runtime" json:"runtime"`
	Models               ModelProfilesConfig  `yaml:"models" json:"models"`
	Kubernetes           KubernetesConfig     `yaml:"kubernetes" json:"kubernetes"`
	LocalExecution       LocalExecutionConfig `yaml:"local_execution,omitempty" json:"local_execution,omitempty"`
	Observability        ObservabilityConfig  `yaml:"observability" json:"observability"`
	Logging              LoggingConfig        `yaml:"logging" json:"logging"`
}

// LocalExecutionConfig contains exact default-off direct-argv and separate
// shell entries. Empty slices disable both local process capabilities.
type LocalExecutionConfig struct {
	Commands []LocalCommandPolicyConfig `yaml:"commands,omitempty" json:"commands,omitempty"`
	Shells   []LocalShellPolicyConfig   `yaml:"shells,omitempty" json:"shells,omitempty"`
}

type LocalCommandPolicyConfig struct {
	ID                  string   `yaml:"id" json:"id"`
	Kind                string   `yaml:"kind" json:"kind"`
	Executable          string   `yaml:"executable" json:"executable"`
	Arguments           []string `yaml:"arguments" json:"arguments"`
	WorkingDirectory    string   `yaml:"working_directory" json:"working_directory"`
	Environment         []string `yaml:"environment" json:"environment"`
	CredentialReference string   `yaml:"credential_ref" json:"credential_ref"`
	ServerOrigin        string   `yaml:"server_origin,omitempty" json:"server_origin,omitempty"`
	DiagnosticEffect    string   `yaml:"diagnostic_effect,omitempty" json:"diagnostic_effect,omitempty"`
	TimeoutSeconds      int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxLines            int      `yaml:"max_lines" json:"max_lines"`
	MaxBytes            int      `yaml:"max_bytes" json:"max_bytes"`
}

type LocalShellPolicyConfig struct {
	ID               string   `yaml:"id" json:"id"`
	Executable       string   `yaml:"executable" json:"executable"`
	Command          string   `yaml:"command" json:"command"`
	WorkingDirectory string   `yaml:"working_directory" json:"working_directory"`
	Environment      []string `yaml:"environment" json:"environment"`
	Network          string   `yaml:"network" json:"network"`
	NetworkOrigin    string   `yaml:"network_origin,omitempty" json:"network_origin,omitempty"`
	TimeoutSeconds   int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxLines         int      `yaml:"max_lines" json:"max_lines"`
	MaxBytes         int      `yaml:"max_bytes" json:"max_bytes"`
}

// DataSourceCredentialReference selects one fixed optional-source credential
// slot. The empty-source value "none" explicitly disables Authorization.
type DataSourceCredentialReference string

const (
	DataSourceCredentialNone       DataSourceCredentialReference = "none"
	DataSourceCredentialPrometheus DataSourceCredentialReference = "prometheus"
	DataSourceCredentialLoki       DataSourceCredentialReference = "loki"
)

// ObservabilityConfig has two fixed optional slots rather than an extensible
// source registry. A nil slot is disabled.
type ObservabilityConfig struct {
	Prometheus *DataSourceConfig `yaml:"prometheus,omitempty" json:"prometheus,omitempty"`
	Loki       *DataSourceConfig `yaml:"loki,omitempty" json:"loki,omitempty"`
}

// DataSourceConfig is the non-sensitive policy for one exact optional source.
// Endpoint is canonicalized to a bare origin during validation.
type DataSourceConfig struct {
	Endpoint              string                        `yaml:"endpoint" json:"endpoint"`
	Origin                string                        `yaml:"-" json:"origin,omitempty"`
	CredentialReference   DataSourceCredentialReference `yaml:"credential_ref" json:"credential_ref"`
	Queries               []string                      `yaml:"queries" json:"queries"`
	RequestTimeoutSeconds int                           `yaml:"request_timeout_seconds" json:"request_timeout_seconds"`
}

// RuntimeConfig selects one code-defined run envelope. Individual limits are
// intentionally not free-form configuration.
type RuntimeConfig struct {
	BudgetProfile string `yaml:"budget_profile" json:"budget_profile"`
}

// ModelProfilesConfig has fixed role fields rather than a generic map. The
// resolved runtime value always contains a complete Agent profile.
type ModelProfilesConfig struct {
	Agent            ModelProfileConfig  `yaml:"agent" json:"agent"`
	ApprovalReviewer *ModelProfileConfig `yaml:"approval_reviewer,omitempty" json:"approval_reviewer,omitempty"`
}

// ModelProfileConfig contains one resolved, explicitly named and role-bound
// profile. InheritAgent records only the user's v2 shorthand; all runtime
// settings below are complete after loading.
type ModelProfileConfig struct {
	Name                  string                   `yaml:"name" json:"name"`
	Role                  ModelRole                `yaml:"role" json:"role"`
	InheritAgent          bool                     `yaml:"inherit_agent,omitempty" json:"inherit_agent,omitempty"`
	CredentialReference   ModelCredentialReference `yaml:"credential_ref" json:"credential_ref"`
	ProviderKind          string                   `yaml:"provider_kind" json:"provider_kind"`
	Endpoint              string                   `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Origin                string                   `yaml:"-" json:"origin,omitempty"`
	Model                 string                   `yaml:"model,omitempty" json:"model,omitempty"`
	ReasoningEffort       string                   `yaml:"reasoning_effort,omitempty" json:"reasoning_effort,omitempty"`
	Temperature           float64                  `yaml:"temperature" json:"temperature"`
	MaxOutputTokens       int                      `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	RequestTimeoutSeconds int                      `yaml:"request_timeout_seconds" json:"request_timeout_seconds"`
	Streaming             bool                     `yaml:"streaming" json:"streaming"`
	ToolCallingRequired   bool                     `yaml:"tool_calling_required" json:"tool_calling_required"`
}

// CredentialSource identifies the selected sensitive source without exposing
// the value. It is runtime metadata and is never written to configuration.
type CredentialSource string

const (
	CredentialSourceNone        CredentialSource = ""
	CredentialSourceFile        CredentialSource = "file"
	CredentialSourceEnvironment CredentialSource = "environment"
	CredentialSourceInherited   CredentialSource = "inherited"
)

// ProfileCredential keeps one role's independently owned opaque wrapper and
// non-sensitive source metadata outside Config.
type ProfileCredential struct {
	Role      ModelRole
	Reference ModelCredentialReference
	Source    CredentialSource
	Value     SecretValue
}

// ModelCredentials contains only the two fixed role slots.
type ModelCredentials struct {
	Agent            ProfileCredential
	ApprovalReviewer *ProfileCredential
	Prometheus       *DataSourceCredential
	Loki             *DataSourceCredential
}

// DataSourceCredential keeps one optional source credential opaque and
// independently owned from model-role credentials.
type DataSourceCredential struct {
	Kind      domain.DataSourceKind
	Reference DataSourceCredentialReference
	Source    CredentialSource
	Value     SecretValue
}

// Destroy overwrites every independently owned role credential.
func (credentials *ModelCredentials) Destroy() {
	if credentials == nil {
		return
	}
	credentials.Agent.Value.Destroy()
	if credentials.ApprovalReviewer != nil {
		credentials.ApprovalReviewer.Value.Destroy()
	}
	if credentials.Prometheus != nil {
		credentials.Prometheus.Value.Destroy()
	}
	if credentials.Loki != nil {
		credentials.Loki.Value.Destroy()
	}
}

// Loaded is one complete startup load. Non-sensitive settings and role-bound
// opaque credentials remain distinct.
type Loaded struct {
	Config
	Paths         Paths
	SourceVersion int
	Credentials   ModelCredentials
	Warnings      []string
}

// KubernetesConfig contains the non-sensitive kubeconfig execution policy.
type KubernetesConfig struct {
	ExecCredentials   string                           `yaml:"exec_credentials" json:"exec_credentials"`
	NamespaceAccess   string                           `yaml:"namespace_access" json:"namespace_access"`
	ResourcePolicies  []KubernetesResourcePolicyConfig `yaml:"resource_policies,omitempty" json:"resource_policies,omitempty"`
	RemoteDiagnostics *RemoteDiagnosticsConfig         `yaml:"remote_diagnostics,omitempty" json:"remote_diagnostics,omitempty"`
}

// RemoteDiagnosticsConfig contains only exact, default-off policy entries.
// It is deliberately not a generic command or image registry.
type RemoteDiagnosticsConfig struct {
	PodExec        []PodExecPolicyConfig       `yaml:"pod_exec,omitempty" json:"pod_exec,omitempty"`
	ContainerFile  *ContainerFilePolicyConfig  `yaml:"container_file,omitempty" json:"container_file,omitempty"`
	DiagnosticPods []DiagnosticPodPolicyConfig `yaml:"diagnostic_pods,omitempty" json:"diagnostic_pods,omitempty"`
}

type PodExecPolicyConfig struct {
	ID             string   `yaml:"id" json:"id"`
	Class          string   `yaml:"class" json:"class"`
	Executable     string   `yaml:"executable" json:"executable"`
	Arguments      []string `yaml:"arguments" json:"arguments"`
	TimeoutSeconds int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxLines       int      `yaml:"max_lines" json:"max_lines"`
	MaxBytes       int      `yaml:"max_bytes" json:"max_bytes"`
}

type ContainerFilePolicyConfig struct {
	ReaderExecutable string   `yaml:"reader_executable" json:"reader_executable"`
	AllowedRoots     []string `yaml:"allowed_roots" json:"allowed_roots"`
	TimeoutSeconds   int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxLines         int      `yaml:"max_lines" json:"max_lines"`
	MaxBytes         int      `yaml:"max_bytes" json:"max_bytes"`
}

type DiagnosticPodPolicyConfig struct {
	ID                    string   `yaml:"id" json:"id"`
	Namespace             string   `yaml:"namespace" json:"namespace"`
	Image                 string   `yaml:"image" json:"image"`
	Executable            string   `yaml:"executable" json:"executable"`
	ArgumentPrefix        []string `yaml:"argument_prefix" json:"argument_prefix"`
	ServiceName           string   `yaml:"service_name" json:"service_name"`
	Port                  uint16   `yaml:"port" json:"port"`
	NetworkPolicyRequired bool     `yaml:"network_policy_required" json:"network_policy_required"`
	TimeoutSeconds        int      `yaml:"timeout_seconds" json:"timeout_seconds"`
	MaxLines              int      `yaml:"max_lines" json:"max_lines"`
	MaxBytes              int      `yaml:"max_bytes" json:"max_bytes"`
}

// KubernetesResourcePolicyConfig is one explicit CRD read policy. Built-in
// resources remain code-owned and cannot be replaced through configuration.
type KubernetesResourcePolicyConfig struct {
	ID       string                                `yaml:"id" json:"id"`
	Group    string                                `yaml:"group" json:"group"`
	Version  string                                `yaml:"version" json:"version"`
	Resource string                                `yaml:"resource" json:"resource"`
	Kind     string                                `yaml:"kind" json:"kind"`
	Scope    string                                `yaml:"scope" json:"scope"`
	Verbs    []string                              `yaml:"verbs" json:"verbs"`
	Fields   []KubernetesResourceFieldPolicyConfig `yaml:"fields" json:"fields"`
	Limits   KubernetesResourceQueryLimitsConfig   `yaml:"limits" json:"limits"`
}

// KubernetesResourceFieldPolicyConfig binds one local field name to one exact
// scalar CRD path and optional typed server selector.
type KubernetesResourceFieldPolicyConfig struct {
	ID             string   `yaml:"id" json:"id"`
	Path           string   `yaml:"path" json:"path"`
	Scalar         string   `yaml:"scalar" json:"scalar"`
	DataClass      string   `yaml:"data_class" json:"data_class"`
	SelectorSource string   `yaml:"selector_source" json:"selector_source"`
	SelectorKey    string   `yaml:"selector_key,omitempty" json:"selector_key,omitempty"`
	Operators      []string `yaml:"operators" json:"operators"`
	Evidence       bool     `yaml:"evidence" json:"evidence"`
}

// KubernetesResourceQueryLimitsConfig sets only per-entry ceilings. Runtime
// profiles and hard limits can always tighten these values.
type KubernetesResourceQueryLimitsConfig struct {
	MaxPages    int `yaml:"max_pages" json:"max_pages"`
	PageItems   int `yaml:"page_items" json:"page_items"`
	PageBytes   int `yaml:"page_bytes" json:"page_bytes"`
	MaxItems    int `yaml:"max_items" json:"max_items"`
	MaxBytes    int `yaml:"max_bytes" json:"max_bytes"`
	MaxReturned int `yaml:"max_returned" json:"max_returned"`
}

// ResourcePolicyCatalog constructs the complete immutable built-in plus CRD
// policy snapshot. It performs no discovery or Kubernetes I/O.
func (config Config) ResourcePolicyCatalog() (domain.ResourcePolicyCatalog, error) {
	copy := config
	if err := Validate(&copy); err != nil {
		return domain.ResourcePolicyCatalog{}, err
	}
	entries := domain.BuiltInResourcePolicies()
	for _, configured := range copy.Kubernetes.ResourcePolicies {
		entry, err := configured.domainPolicy()
		if err != nil {
			return domain.ResourcePolicyCatalog{}, newSafeError(
				ClassConfigurationInvalid,
				"config_resource_policy_invalid",
				"build_resource_policy_catalog",
				"Each Kubernetes resource policy must name one exact CRD, read verbs, scalar projection, query semantics, and finite limits.",
			)
		}
		entries = append(entries, entry)
	}
	catalog, err := domain.NewResourcePolicyCatalog(domain.ResourcePolicyVersion, entries)
	if err != nil {
		return domain.ResourcePolicyCatalog{}, newSafeError(
			ClassConfigurationInvalid,
			"config_resource_policy_invalid",
			"build_resource_policy_catalog",
			"Kubernetes resource policies must have unique local IDs and exact API identities.",
		)
	}
	return catalog, nil
}

// ObservabilityPolicyCatalog constructs the immutable, credential-free source
// policy snapshot supplied to one AgentRun.
func (config Config) ObservabilityPolicyCatalog() (domain.ObservabilityPolicyCatalog, error) {
	copy := config
	if err := Validate(&copy); err != nil {
		return domain.ObservabilityPolicyCatalog{}, err
	}
	policy := func(kind domain.DataSourceKind, source *DataSourceConfig) (domain.DataSourcePolicy, error) {
		if source == nil {
			return domain.DataSourcePolicy{}, nil
		}
		queries := make([]domain.ObservabilityQueryID, len(source.Queries))
		for index, query := range source.Queries {
			queries[index] = domain.ObservabilityQueryID(query)
		}
		result := domain.DataSourcePolicy{
			Kind: kind, OriginHash: domain.SHA256Hex(source.Origin), Queries: queries,
			RequestTimeout: time.Duration(source.RequestTimeoutSeconds) * time.Second,
		}
		if result.Validate() != nil {
			return domain.DataSourcePolicy{}, domain.ErrInvalidObservabilityPolicy
		}
		return result, nil
	}
	prometheus, err := policy(domain.DataSourcePrometheus, copy.Observability.Prometheus)
	if err != nil {
		return domain.ObservabilityPolicyCatalog{}, err
	}
	loki, err := policy(domain.DataSourceLoki, copy.Observability.Loki)
	if err != nil {
		return domain.ObservabilityPolicyCatalog{}, err
	}
	return domain.NewObservabilityPolicyCatalog(prometheus, loki)
}

// RemoteDiagnosticsPolicyCatalog constructs the credential-free exact policy
// snapshot supplied to each run. Absence disables all remote execution.
func (config Config) RemoteDiagnosticsPolicyCatalog() (domain.RemoteDiagnosticsPolicyCatalog, error) {
	copy := config
	if err := Validate(&copy); err != nil {
		return domain.RemoteDiagnosticsPolicyCatalog{}, err
	}
	return copy.remoteDiagnosticsPolicyCatalogUnchecked()
}

func (config Config) remoteDiagnosticsPolicyCatalogUnchecked() (domain.RemoteDiagnosticsPolicyCatalog, error) {
	configured := config.Kubernetes.RemoteDiagnostics
	if configured == nil {
		return domain.DisabledRemoteDiagnosticsPolicyCatalog(), nil
	}
	execPolicies := make([]domain.PodExecPolicy, len(configured.PodExec))
	for index, value := range configured.PodExec {
		arguments, err := domain.NewActionArguments(value.Arguments)
		if err != nil || !remoteDiagnosticArgumentsAreNonSensitive(value.Arguments) {
			return domain.RemoteDiagnosticsPolicyCatalog{}, domain.ErrInvalidRemoteDiagnosticsPolicy
		}
		execPolicies[index] = domain.PodExecPolicy{ID: value.ID, Class: domain.PodExecPolicyClass(value.Class), Executable: value.Executable, Arguments: arguments, Timeout: time.Duration(value.TimeoutSeconds) * time.Second, MaxLines: value.MaxLines, MaxBytes: value.MaxBytes}
	}
	var filePolicy *domain.ContainerFilePolicy
	if value := configured.ContainerFile; value != nil {
		roots, err := domain.NewContainerFileRoots(value.AllowedRoots)
		if err != nil {
			return domain.RemoteDiagnosticsPolicyCatalog{}, domain.ErrInvalidRemoteDiagnosticsPolicy
		}
		filePolicy = &domain.ContainerFilePolicy{Enabled: true, ReaderExecutable: value.ReaderExecutable, AllowedRoots: roots, Timeout: time.Duration(value.TimeoutSeconds) * time.Second, MaxLines: value.MaxLines, MaxBytes: value.MaxBytes}
	}
	diagnosticPolicies := make([]domain.DiagnosticPodPolicy, len(configured.DiagnosticPods))
	for index, value := range configured.DiagnosticPods {
		arguments, err := domain.NewActionArguments(value.ArgumentPrefix)
		if err != nil || !remoteDiagnosticArgumentsAreNonSensitive(value.ArgumentPrefix) {
			return domain.RemoteDiagnosticsPolicyCatalog{}, domain.ErrInvalidRemoteDiagnosticsPolicy
		}
		diagnosticPolicies[index] = domain.DiagnosticPodPolicy{ID: value.ID, Namespace: value.Namespace, Image: value.Image, Executable: value.Executable, ArgumentPrefix: arguments, ServiceName: value.ServiceName, Port: value.Port, NetworkPolicyRequired: value.NetworkPolicyRequired, Timeout: time.Duration(value.TimeoutSeconds) * time.Second, MaxLines: value.MaxLines, MaxBytes: value.MaxBytes}
	}
	return domain.NewRemoteDiagnosticsPolicyCatalog(execPolicies, filePolicy, diagnosticPolicies)
}

// LocalExecutionPolicyCatalogs constructs the exact credential-free policy
// snapshots used for proposal preparation. No entry means disabled.
func (config Config) LocalExecutionPolicyCatalogs() (domain.LocalCommandPolicyCatalog, domain.LocalShellPolicyCatalog, error) {
	copy := config
	if err := Validate(&copy); err != nil {
		return domain.LocalCommandPolicyCatalog{}, domain.LocalShellPolicyCatalog{}, err
	}
	return copy.localExecutionPolicyCatalogsUnchecked()
}

func (config Config) localExecutionPolicyCatalogsUnchecked() (domain.LocalCommandPolicyCatalog, domain.LocalShellPolicyCatalog, error) {
	commands := make([]domain.LocalCommandPolicy, len(config.LocalExecution.Commands))
	for index, value := range config.LocalExecution.Commands {
		arguments, err := domain.NewActionArguments(value.Arguments)
		if err != nil || !remoteDiagnosticArgumentsAreNonSensitive(value.Arguments) {
			return domain.LocalCommandPolicyCatalog{}, domain.LocalShellPolicyCatalog{}, domain.ErrInvalidLocalExecutionPolicy
		}
		environment, err := domain.NewActionEnvironment(value.Environment)
		if err != nil {
			return domain.LocalCommandPolicyCatalog{}, domain.LocalShellPolicyCatalog{}, domain.ErrInvalidLocalExecutionPolicy
		}
		operation, ok := domain.ClassifyLocalCommand(domain.LocalCommandKind(value.Kind), arguments)
		if !ok {
			return domain.LocalCommandPolicyCatalog{}, domain.LocalShellPolicyCatalog{}, domain.ErrInvalidLocalExecutionPolicy
		}
		origin := value.ServerOrigin
		originHash := domain.ActionDigest("")
		if origin != "" {
			var canonicalErr error
			origin, canonicalErr = domain.CanonicalLocalCommandOrigin(origin)
			if canonicalErr != nil {
				return domain.LocalCommandPolicyCatalog{}, domain.LocalShellPolicyCatalog{}, domain.ErrInvalidLocalExecutionPolicy
			}
			originHash = domain.LocalCommandOriginHash(origin)
		}
		commands[index] = domain.LocalCommandPolicy{
			ID: value.ID, Kind: domain.LocalCommandKind(value.Kind), Operation: operation,
			Executable: value.Executable, Arguments: arguments, WorkingDirectory: value.WorkingDirectory,
			Environment: environment, CredentialReference: domain.LocalCredentialReference(value.CredentialReference),
			ServerOrigin: origin, ServerOriginHash: originHash,
			DiagnosticEffect: domain.LocalDiagnosticEffect(value.DiagnosticEffect),
			Timeout:          time.Duration(value.TimeoutSeconds) * time.Second, MaxLines: value.MaxLines, MaxBytes: value.MaxBytes,
		}
	}
	shells := make([]domain.LocalShellPolicy, len(config.LocalExecution.Shells))
	for index, value := range config.LocalExecution.Shells {
		environment, err := domain.NewActionEnvironment(value.Environment)
		if err != nil {
			return domain.LocalCommandPolicyCatalog{}, domain.LocalShellPolicyCatalog{}, domain.ErrInvalidLocalExecutionPolicy
		}
		origin := value.NetworkOrigin
		originHash := domain.ActionDigest("")
		if origin != "" {
			var canonicalErr error
			origin, canonicalErr = domain.CanonicalLocalCommandOrigin(origin)
			if canonicalErr != nil {
				return domain.LocalCommandPolicyCatalog{}, domain.LocalShellPolicyCatalog{}, domain.ErrInvalidLocalExecutionPolicy
			}
			originHash = domain.LocalCommandOriginHash(origin)
		}
		shells[index] = domain.LocalShellPolicy{
			ID: value.ID, Executable: value.Executable, Command: value.Command,
			WorkingDirectory: value.WorkingDirectory, Environment: environment,
			Network: domain.LocalShellNetwork(value.Network), NetworkOrigin: origin, NetworkOriginHash: originHash,
			Timeout: time.Duration(value.TimeoutSeconds) * time.Second, MaxLines: value.MaxLines, MaxBytes: value.MaxBytes,
		}
	}
	commandCatalog, err := domain.NewLocalCommandPolicyCatalog(commands)
	if err != nil {
		return domain.LocalCommandPolicyCatalog{}, domain.LocalShellPolicyCatalog{}, err
	}
	shellCatalog, err := domain.NewLocalShellPolicyCatalog(shells)
	if err != nil {
		return domain.LocalCommandPolicyCatalog{}, domain.LocalShellPolicyCatalog{}, err
	}
	return commandCatalog, shellCatalog, nil
}

func (configured KubernetesResourcePolicyConfig) domainPolicy() (domain.ResourcePolicy, error) {
	verbs := make([]domain.ResourceVerb, len(configured.Verbs))
	for index, verb := range configured.Verbs {
		verbs[index] = domain.ResourceVerb(verb)
	}
	sort.Slice(verbs, func(left, right int) bool { return verbs[left] < verbs[right] })
	fields := make([]domain.ResourceFieldPolicy, len(configured.Fields))
	for index, configuredField := range configured.Fields {
		operators := make([]domain.ResourceFilterOperator, len(configuredField.Operators))
		for operatorIndex, operator := range configuredField.Operators {
			operators[operatorIndex] = domain.ResourceFilterOperator(operator)
		}
		sort.Slice(operators, func(left, right int) bool { return operators[left] < operators[right] })
		fields[index] = domain.ResourceFieldPolicy{
			ID: configuredField.ID, Path: configuredField.Path,
			Scalar: domain.ResourceScalarType(configuredField.Scalar), DataClass: domain.ResourceDataClass(configuredField.DataClass),
			SelectorSource: domain.ResourceSelectorSource(configuredField.SelectorSource), SelectorKey: configuredField.SelectorKey,
			Operators: operators, Evidence: configuredField.Evidence,
		}
	}
	policy := domain.ResourcePolicy{
		Type: domain.ResourceType{
			ID: configured.ID, Group: configured.Group, Version: configured.Version,
			Resource: configured.Resource, Kind: configured.Kind,
			Scope: domain.ResourceScope(configured.Scope), BuiltIn: false,
		},
		Verbs:  verbs,
		Fields: fields,
		Limits: domain.ResourceQueryLimits{
			MaxPages: configured.Limits.MaxPages, PageItems: configured.Limits.PageItems, PageBytes: configured.Limits.PageBytes,
			MaxItems: configured.Limits.MaxItems, MaxBytes: configured.Limits.MaxBytes,
			MaxReturned: configured.Limits.MaxReturned,
		},
	}
	if policy.Validate() != nil || policy.Type.Group == "" {
		return domain.ResourcePolicy{}, domain.ErrInvalidResourcePolicy
	}
	return policy, nil
}

// LoggingConfig controls the fixed local file logger. Rotation ceilings remain
// code-defined in the logging adapter and cannot be expanded by configuration.
type LoggingConfig struct {
	Enabled              bool   `yaml:"enabled" json:"enabled"`
	Level                string `yaml:"level" json:"level"`
	SensitiveDiagnostics bool   `yaml:"sensitive_diagnostics" json:"sensitive_diagnostics"`
}

// StringOverride distinguishes an absent CLI value from an explicit value.
type StringOverride struct {
	Set   bool
	Value string
}

// BoolOverride distinguishes an absent CLI value from an explicit value.
type BoolOverride struct {
	Set   bool
	Value bool
}

// Overrides contains only admitted non-sensitive CLI settings.
type Overrides struct {
	ConfigFile StringOverride
	Context    StringOverride
	Namespace  StringOverride
	NoColor    BoolOverride
}

func defaultAgentProfile() ModelProfileConfig {
	return ModelProfileConfig{
		Name: "agent", Role: ModelRoleAgent, CredentialReference: ModelCredentialAgent,
		ProviderKind: ProviderOpenAICompatible, Temperature: DefaultModelTemperature,
		RequestTimeoutSeconds: DefaultModelRequestTimeoutSeconds,
		Streaming:             true, ToolCallingRequired: true,
	}
}

func defaultReviewerProfile() ModelProfileConfig {
	return ModelProfileConfig{
		Name: "approval-reviewer", Role: ModelRoleApprovalReviewer,
		CredentialReference: ModelCredentialApprovalReviewer,
		ProviderKind:        ProviderOpenAICompatible, Temperature: 0,
		RequestTimeoutSeconds: DefaultReviewerTimeoutSeconds,
		Streaming:             false, ToolCallingRequired: false,
	}
}

// Defaults returns the code-defined configuration defaults.
func Defaults() Config {
	return Config{
		Version: CurrentVersion, Namespace: DefaultNamespace, TerminalStatusTitles: true,
		Runtime:       RuntimeConfig{BudgetProfile: DefaultBudgetProfile},
		Models:        ModelProfilesConfig{Agent: defaultAgentProfile()},
		Kubernetes:    KubernetesConfig{ExecCredentials: ExecCredentialsAllow, NamespaceAccess: DefaultNamespaceAccess},
		Observability: ObservabilityConfig{},
		Logging:       LoggingConfig{Enabled: true, Level: "info", SensitiveDiagnostics: false},
	}
}

// ValidatedProfile returns one complete role-bound profile suitable for
// adapter construction. It never contains an API key.
func (config Config) ValidatedProfile(role ModelRole) (ModelProfileConfig, error) {
	var profile *ModelProfileConfig
	switch role {
	case ModelRoleAgent:
		profile = &config.Models.Agent
	case ModelRoleApprovalReviewer:
		profile = config.Models.ApprovalReviewer
	default:
		return ModelProfileConfig{}, modelProfileRequiredError(role)
	}
	if profile == nil || profile.Endpoint == "" || profile.Model == "" {
		return ModelProfileConfig{}, modelProfileRequiredError(role)
	}
	copy := config
	if err := Validate(&copy); err != nil {
		return ModelProfileConfig{}, err
	}
	if role == ModelRoleAgent {
		return copy.Models.Agent, nil
	}
	if copy.Models.ApprovalReviewer == nil {
		return ModelProfileConfig{}, modelProfileRequiredError(role)
	}
	return *copy.Models.ApprovalReviewer, nil
}

func modelProfileRequiredError(role ModelRole) error {
	name := string(role)
	if name == "" {
		name = "requested"
	}
	return newSafeError(
		ClassConfigurationInvalid,
		"config_model_profile_required",
		"validate_model_configuration",
		"The "+name+" model profile requires an endpoint, model identifier, and role-bound credential.",
	)
}
