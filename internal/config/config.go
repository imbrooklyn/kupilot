package config

const (
	CurrentVersion                    = 1
	ProviderOpenAICompatible          = "openai_compatible"
	APIKeySourceEnvironment           = "environment"
	ExecCredentialsAllow              = "allow"
	ExecCredentialsDeny               = "deny"
	DefaultModelTemperature           = 0.1
	DefaultMaxModelOutputTokens       = 2048
	MaxModelOutputTokens              = 8192
	DefaultModelRequestTimeoutSeconds = 45
	MaxModelRequestTimeoutSeconds     = 45
	MaxContextBytes                   = 253
	MaxNamespaceBytes                 = 63
	MaxModelIdentifierBytes           = 128
	MaxPathBytes                      = 4096
)

// Config is the complete serializable, non-sensitive startup configuration.
// Transport credentials are intentionally absent.
type Config struct {
	Version    int              `mapstructure:"version" yaml:"version" json:"version"`
	Context    string           `mapstructure:"context" yaml:"context,omitempty" json:"context,omitempty"`
	Namespace  string           `mapstructure:"namespace" yaml:"namespace,omitempty" json:"namespace,omitempty"`
	NoColor    bool             `mapstructure:"no_color" yaml:"no_color" json:"no_color"`
	Paths      Paths            `mapstructure:"paths" yaml:"paths" json:"paths"`
	Model      ModelConfig      `mapstructure:"model" yaml:"model" json:"model"`
	Kubernetes KubernetesConfig `mapstructure:"kubernetes" yaml:"kubernetes" json:"kubernetes"`
	Logging    LoggingConfig    `mapstructure:"logging" yaml:"logging" json:"logging"`
}

// ModelConfig contains only validated, non-sensitive model settings.
type ModelConfig struct {
	ProviderKind          string  `mapstructure:"provider_kind" yaml:"provider_kind" json:"provider_kind"`
	Endpoint              string  `mapstructure:"endpoint" yaml:"endpoint,omitempty" json:"endpoint,omitempty"`
	Origin                string  `mapstructure:"-" yaml:"-" json:"origin,omitempty"`
	Model                 string  `mapstructure:"model" yaml:"model,omitempty" json:"model,omitempty"`
	APIKeySource          string  `mapstructure:"api_key_source" yaml:"api_key_source" json:"api_key_source"`
	Temperature           float64 `mapstructure:"temperature" yaml:"temperature" json:"temperature"`
	MaxOutputTokens       int     `mapstructure:"max_output_tokens" yaml:"max_output_tokens" json:"max_output_tokens"`
	RequestTimeoutSeconds int     `mapstructure:"request_timeout_seconds" yaml:"request_timeout_seconds" json:"request_timeout_seconds"`
	Streaming             bool    `mapstructure:"streaming" yaml:"streaming" json:"streaming"`
	ToolCallingRequired   bool    `mapstructure:"tool_calling_required" yaml:"tool_calling_required" json:"tool_calling_required"`
}

// KubernetesConfig contains the non-sensitive kubeconfig execution policy.
type KubernetesConfig struct {
	ExecCredentials string `mapstructure:"exec_credentials" yaml:"exec_credentials" json:"exec_credentials"`
}

// LoggingConfig controls the fixed local file logger. Rotation ceilings remain
// code-defined in the logging adapter and cannot be expanded by configuration.
type LoggingConfig struct {
	Enabled bool   `mapstructure:"enabled" yaml:"enabled" json:"enabled"`
	Level   string `mapstructure:"level" yaml:"level" json:"level"`
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

// Defaults returns the code-defined configuration defaults for resolved
// platform paths.
func Defaults(paths Paths) Config {
	return Config{
		Version: CurrentVersion,
		Paths:   paths,
		Model: ModelConfig{
			ProviderKind:          ProviderOpenAICompatible,
			APIKeySource:          APIKeySourceEnvironment,
			Temperature:           DefaultModelTemperature,
			MaxOutputTokens:       DefaultMaxModelOutputTokens,
			RequestTimeoutSeconds: DefaultModelRequestTimeoutSeconds,
			Streaming:             true,
			ToolCallingRequired:   true,
		},
		Kubernetes: KubernetesConfig{ExecCredentials: ExecCredentialsAllow},
		Logging: LoggingConfig{
			Enabled: true,
			Level:   "info",
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
