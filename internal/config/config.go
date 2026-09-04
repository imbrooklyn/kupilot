package config

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
	DefaultModelRequestTimeoutSeconds = 300
	DefaultReviewerTimeoutSeconds     = 30
	MaxModelRequestTimeoutSeconds     = 300
	MaxContextBytes                   = 253
	MaxNamespaceBytes                 = 63
	DefaultNamespace                  = "default"
	MaxModelIdentifierBytes           = 128
	MaxModelProfileNameBytes          = 128
	MaxPathBytes                      = 4096
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
	Version    int                 `yaml:"version" json:"version"`
	Context    string              `yaml:"context,omitempty" json:"context,omitempty"`
	Namespace  string              `yaml:"namespace,omitempty" json:"namespace,omitempty"`
	NoColor    bool                `yaml:"no_color" json:"no_color"`
	Runtime    RuntimeConfig       `yaml:"runtime" json:"runtime"`
	Models     ModelProfilesConfig `yaml:"models" json:"models"`
	Kubernetes KubernetesConfig    `yaml:"kubernetes" json:"kubernetes"`
	Logging    LoggingConfig       `yaml:"logging" json:"logging"`
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
	ExecCredentials string `yaml:"exec_credentials" json:"exec_credentials"`
	NamespaceAccess string `yaml:"namespace_access" json:"namespace_access"`
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
		Version: CurrentVersion, Namespace: DefaultNamespace,
		Runtime:    RuntimeConfig{BudgetProfile: DefaultBudgetProfile},
		Models:     ModelProfilesConfig{Agent: defaultAgentProfile()},
		Kubernetes: KubernetesConfig{ExecCredentials: ExecCredentialsAllow, NamespaceAccess: DefaultNamespaceAccess},
		Logging:    LoggingConfig{Enabled: true, Level: "info", SensitiveDiagnostics: false},
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
