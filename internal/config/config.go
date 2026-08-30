package config

const (
	CurrentVersion                    = 1
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
	DefaultMaxModelOutputTokens       = 2048
	MaxModelOutputTokens              = 8192
	DefaultModelRequestTimeoutSeconds = 300
	MaxModelRequestTimeoutSeconds     = 300
	MaxContextBytes                   = 253
	MaxNamespaceBytes                 = 63
	DefaultNamespace                  = "default"
	MaxModelIdentifierBytes           = 128
	MaxPathBytes                      = 4096
)

// Config is the complete serializable, non-sensitive startup configuration.
// Paths and transport credentials are intentionally absent.
type Config struct {
	Version    int              `mapstructure:"version" yaml:"version" json:"version"`
	Context    string           `mapstructure:"context" yaml:"context,omitempty" json:"context,omitempty"`
	Namespace  string           `mapstructure:"namespace" yaml:"namespace,omitempty" json:"namespace,omitempty"`
	NoColor    bool             `mapstructure:"no_color" yaml:"no_color" json:"no_color"`
	Runtime    RuntimeConfig    `mapstructure:"runtime" yaml:"runtime" json:"runtime"`
	Model      ModelConfig      `mapstructure:"model" yaml:"model" json:"model"`
	Kubernetes KubernetesConfig `mapstructure:"kubernetes" yaml:"kubernetes" json:"kubernetes"`
	Logging    LoggingConfig    `mapstructure:"logging" yaml:"logging" json:"logging"`
}

// RuntimeConfig selects one code-defined run envelope. Individual limits are
// intentionally not free-form configuration.
type RuntimeConfig struct {
	BudgetProfile string `mapstructure:"budget_profile" yaml:"budget_profile" json:"budget_profile"`
}

// ModelConfig contains only validated, non-sensitive model settings.
type ModelConfig struct {
	ProviderKind          string  `mapstructure:"provider_kind" yaml:"provider_kind" json:"provider_kind"`
	Endpoint              string  `mapstructure:"endpoint" yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Origin                string  `mapstructure:"-" yaml:"-" json:"origin,omitempty"`
	Model                 string  `mapstructure:"model" yaml:"model,omitempty" json:"model,omitempty"`
	ReasoningEffort       string  `mapstructure:"reasoning_effort" yaml:"reasoning_effort,omitempty" json:"reasoning_effort,omitempty"`
	Temperature           float64 `mapstructure:"temperature" yaml:"temperature" json:"temperature"`
	MaxOutputTokens       int     `mapstructure:"max_output_tokens" yaml:"max_output_tokens" json:"max_output_tokens"`
	RequestTimeoutSeconds int     `mapstructure:"request_timeout_seconds" yaml:"request_timeout_seconds" json:"request_timeout_seconds"`
	Streaming             bool    `mapstructure:"streaming" yaml:"streaming" json:"streaming"`
	ToolCallingRequired   bool    `mapstructure:"tool_calling_required" yaml:"tool_calling_required" json:"tool_calling_required"`
}

// CredentialSource identifies the selected sensitive source without exposing
// the value. It is runtime metadata and is never written to configuration.
type CredentialSource string

const (
	CredentialSourceNone        CredentialSource = ""
	CredentialSourceFile        CredentialSource = "file"
	CredentialSourceEnvironment CredentialSource = "environment"
)

// Loaded is one complete startup load. Embedding keeps non-sensitive settings
// convenient while the opaque credential and fixed paths remain separate.
type Loaded struct {
	Config
	Paths            Paths
	Credential       SecretValue
	CredentialSource CredentialSource
	Warnings         []string
}

// KubernetesConfig contains the non-sensitive kubeconfig execution policy.
type KubernetesConfig struct {
	ExecCredentials string `mapstructure:"exec_credentials" yaml:"exec_credentials" json:"exec_credentials"`
	NamespaceAccess string `mapstructure:"namespace_access" yaml:"namespace_access" json:"namespace_access"`
}

// LoggingConfig controls the fixed local file logger. Rotation ceilings remain
// code-defined in the logging adapter and cannot be expanded by configuration.
type LoggingConfig struct {
	Enabled              bool   `mapstructure:"enabled" yaml:"enabled" json:"enabled"`
	Level                string `mapstructure:"level" yaml:"level" json:"level"`
	SensitiveDiagnostics bool   `mapstructure:"sensitive_diagnostics" yaml:"sensitive_diagnostics" json:"sensitive_diagnostics"`
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

// Defaults returns the code-defined configuration defaults.
func Defaults() Config {
	return Config{
		Version: CurrentVersion, Namespace: DefaultNamespace,
		Runtime: RuntimeConfig{BudgetProfile: DefaultBudgetProfile},
		Model: ModelConfig{
			ProviderKind:          ProviderOpenAICompatible,
			Temperature:           DefaultModelTemperature,
			MaxOutputTokens:       DefaultMaxModelOutputTokens,
			RequestTimeoutSeconds: DefaultModelRequestTimeoutSeconds,
			Streaming:             true,
			ToolCallingRequired:   true,
		},
		Kubernetes: KubernetesConfig{ExecCredentials: ExecCredentialsAllow, NamespaceAccess: DefaultNamespaceAccess},
		Logging: LoggingConfig{
			Enabled:              true,
			Level:                "info",
			SensitiveDiagnostics: false,
		},
	}
}

// ValidatedModel returns a complete model configuration suitable for adapter
// construction. It never contains the API key value.
func (config Config) ValidatedModel() (ModelConfig, error) {
	if config.Model.Endpoint == "" || config.Model.Model == "" {
		return ModelConfig{}, newSafeError(
			ClassConfigurationInvalid,
			"config_model_required",
			"validate_model_configuration",
			"Model endpoint and model identifier are required; set model.endpoint and model.model or their documented environment variables.",
		)
	}
	copy := config
	if err := Validate(&copy); err != nil {
		return ModelConfig{}, err
	}
	return copy.Model, nil
}
