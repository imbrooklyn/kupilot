package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"go.yaml.in/yaml/v3"
)

const MaxConfigFileBytes = 64 * 1024

// LoadOptions contains deterministic inputs to one independent configuration
// load. LookupEnv and Unsetenv make environment and one-shot key handling
// deterministic in tests.
type LoadOptions struct {
	Paths     Paths
	Overrides Overrides
	LookupEnv func(string) (string, bool)
	Unsetenv  func(string) error
}

type extractedCredentials struct {
	agent         SecretValue
	agentFound    bool
	reviewer      SecretValue
	reviewerFound bool
}

func (credentials *extractedCredentials) destroy() {
	if credentials == nil {
		return
	}
	credentials.agent.Destroy()
	credentials.reviewer.Destroy()
}

// Load applies defaults, a strict versioned YAML file, admitted environment
// variables, and typed CLI overrides in increasing precedence order. A v1
// document is migrated in memory and is never rewritten by loading.
func Load(ctx context.Context, options LoadOptions) (Loaded, error) {
	if ctx == nil || ctx.Err() != nil {
		return Loaded{}, newSafeError(ClassCancelled, "config_load_cancelled", "load_configuration", "Configuration loading was cancelled.")
	}
	lookup := options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	agentEnvironment := &EnvironmentSecretSource{
		LookupEnv: lookup, Unsetenv: options.Unsetenv,
		Variables: []string{AgentAPIKeyEnvironmentVariable, ModelAPIKeyEnvironmentVariable},
	}
	reviewerEnvironment := &EnvironmentSecretSource{
		LookupEnv: lookup, Unsetenv: options.Unsetenv,
		Variables: []string{ApprovalReviewerAPIKeyEnvironmentVariable},
	}
	agentEnvironmentCredential, agentEnvironmentFound, agentEnvironmentErr := agentEnvironment.ReadOptional()
	reviewerEnvironmentCredential, reviewerEnvironmentFound, reviewerEnvironmentErr := reviewerEnvironment.ReadOptional()
	if agentEnvironmentErr != nil || reviewerEnvironmentErr != nil {
		agentEnvironmentCredential.Destroy()
		reviewerEnvironmentCredential.Destroy()
		if agentEnvironmentErr != nil {
			return Loaded{}, agentEnvironmentErr
		}
		return Loaded{}, reviewerEnvironmentErr
	}
	environmentCredentialsTransferred := false
	defer func() {
		if !environmentCredentialsTransferred {
			agentEnvironmentCredential.Destroy()
			reviewerEnvironmentCredential.Destroy()
		}
	}()

	configFile := options.Paths.ConfigFile
	explicitFile := false
	if value, ok := lookup("KUPILOT_CONFIG_FILE"); ok {
		configFile = value
		explicitFile = true
	}
	if options.Overrides.ConfigFile.Set {
		configFile = options.Overrides.ConfigFile.Value
		explicitFile = true
	}
	if !validAbsoluteDirectory(filepath.Dir(configFile)) || !filepath.IsAbs(configFile) || filepath.Clean(configFile) != configFile {
		return Loaded{}, newSafeError(ClassConfigurationInvalid, "config_file_path_invalid", "load_configuration", "The configuration file path must be absolute and normalized.")
	}

	content, found, permissionsWider, err := readConfigFile(ctx, configFile, explicitFile)
	if err != nil {
		return Loaded{}, err
	}
	config := Defaults()
	sourceVersion := CurrentVersion
	var fileCredentials extractedCredentials
	if found {
		var sanitized []byte
		sanitized, fileCredentials, sourceVersion, err = extractSensitiveConfig(content)
		zeroBytes(content)
		content = nil
		if err != nil {
			return Loaded{}, err
		}
		config, err = decodeConfigDocument(sanitized, sourceVersion)
		zeroBytes(sanitized)
		if err != nil {
			fileCredentials.destroy()
			return Loaded{}, err
		}
	}

	if err := applyEnvironment(&config, lookup); err != nil {
		fileCredentials.destroy()
		return Loaded{}, err
	}
	applyOverrides(&config, options.Overrides)
	if err := Validate(&config); err != nil {
		fileCredentials.destroy()
		return Loaded{}, err
	}

	agentCredential := fileCredentials.agent
	agentSource := CredentialSourceNone
	if fileCredentials.agentFound {
		agentSource = CredentialSourceFile
	}
	if agentEnvironmentFound {
		fileCredentials.agent.Destroy()
		agentCredential = agentEnvironmentCredential
		agentSource = CredentialSourceEnvironment
	}
	credentials := ModelCredentials{Agent: ProfileCredential{
		Role: ModelRoleAgent, Reference: ModelCredentialAgent, Source: agentSource, Value: agentCredential,
	}}

	if config.Models.ApprovalReviewer == nil {
		fileCredentials.reviewer.Destroy()
		reviewerEnvironmentCredential.Destroy()
		if fileCredentials.reviewerFound || reviewerEnvironmentFound {
			credentials.Destroy()
			return Loaded{}, newSafeError(ClassConfigurationInvalid, "config_reviewer_credential_unbound", "load_configuration", "An approval_reviewer API key requires an approval_reviewer model profile.")
		}
	} else {
		reviewerProfile := config.Models.ApprovalReviewer
		reviewerCredential := ProfileCredential{
			Role: ModelRoleApprovalReviewer, Reference: reviewerProfile.CredentialReference,
		}
		switch reviewerProfile.CredentialReference {
		case ModelCredentialAgent:
			fileCredentials.reviewer.Destroy()
			reviewerEnvironmentCredential.Destroy()
			if fileCredentials.reviewerFound || reviewerEnvironmentFound {
				credentials.Destroy()
				return Loaded{}, newSafeError(ClassConfigurationInvalid, "config_reviewer_credential_conflict", "load_configuration", "A reviewer that references the agent credential must not define another reviewer API key.")
			}
			if credentials.Agent.Value.IsSet() {
				clone, cloneErr := credentials.Agent.Value.Clone()
				if cloneErr != nil {
					credentials.Destroy()
					return Loaded{}, cloneErr
				}
				reviewerCredential.Value = clone
				reviewerCredential.Source = CredentialSourceInherited
			}
		case ModelCredentialApprovalReviewer:
			reviewerCredential.Value = fileCredentials.reviewer
			if fileCredentials.reviewerFound {
				reviewerCredential.Source = CredentialSourceFile
			}
			if reviewerEnvironmentFound {
				fileCredentials.reviewer.Destroy()
				reviewerCredential.Value = reviewerEnvironmentCredential
				reviewerCredential.Source = CredentialSourceEnvironment
			}
		default:
			credentials.Destroy()
			fileCredentials.reviewer.Destroy()
			reviewerEnvironmentCredential.Destroy()
			return Loaded{}, newSafeError(ClassConfigurationInvalid, "config_model_profile_invalid", "load_configuration", "The reviewer credential reference is invalid.")
		}
		credentials.ApprovalReviewer = &reviewerCredential
	}

	if err := ctx.Err(); err != nil {
		credentials.Destroy()
		return Loaded{}, newSafeError(ClassCancelled, "config_load_cancelled", "load_configuration", "Configuration loading was cancelled.")
	}
	warnings := make([]string, 0, 4)
	if sourceVersion == LegacyVersion {
		warnings = append(warnings, "Configuration schema version 1 was loaded in compatibility mode and was not rewritten; a future explicit save writes version 2.")
	}
	if options.Paths.HomePermissionsWider {
		warnings = append(warnings, "KUPILOT_HOME is accessible beyond its owner; Kupilot will respect the existing user-managed permissions.")
	}
	if permissionsWider {
		warnings = append(warnings, "The selected configuration file is accessible beyond its owner; it may contain plaintext model API keys.")
	}
	if config.Logging.SensitiveDiagnostics {
		warnings = append(warnings, "Sensitive model diagnostics are enabled; local logs may contain endpoint details, provider error content, and source paths.")
	}
	environmentCredentialsTransferred = true
	return Loaded{
		Config: config, Paths: options.Paths, SourceVersion: sourceVersion,
		Credentials: credentials, Warnings: warnings,
	}, nil
}

type configV1Document struct {
	Version    int               `yaml:"version"`
	Context    string            `yaml:"context,omitempty"`
	Namespace  string            `yaml:"namespace,omitempty"`
	NoColor    bool              `yaml:"no_color"`
	Runtime    RuntimeConfig     `yaml:"runtime"`
	Model      legacyModelConfig `yaml:"model"`
	Kubernetes KubernetesConfig  `yaml:"kubernetes"`
	Logging    LoggingConfig     `yaml:"logging"`
}

type legacyModelConfig struct {
	ProviderKind          string  `yaml:"provider_kind"`
	Endpoint              string  `yaml:"endpoint,omitempty"`
	Model                 string  `yaml:"model,omitempty"`
	ReasoningEffort       string  `yaml:"reasoning_effort,omitempty"`
	Temperature           float64 `yaml:"temperature"`
	MaxOutputTokens       int     `yaml:"max_output_tokens"`
	RequestTimeoutSeconds int     `yaml:"request_timeout_seconds"`
	Streaming             bool    `yaml:"streaming"`
	ToolCallingRequired   bool    `yaml:"tool_calling_required"`
}

type configV2Document struct {
	Version    int                 `yaml:"version"`
	Context    *string             `yaml:"context,omitempty"`
	Namespace  *string             `yaml:"namespace,omitempty"`
	NoColor    *bool               `yaml:"no_color,omitempty"`
	Runtime    *runtimeDocument    `yaml:"runtime,omitempty"`
	Models     *modelsDocument     `yaml:"models"`
	Kubernetes *kubernetesDocument `yaml:"kubernetes,omitempty"`
	Logging    *loggingDocument    `yaml:"logging,omitempty"`
}

type runtimeDocument struct {
	BudgetProfile *string `yaml:"budget_profile"`
}

type modelsDocument struct {
	Agent            *modelProfileDocument `yaml:"agent"`
	ApprovalReviewer *modelProfileDocument `yaml:"approval_reviewer,omitempty"`
}

type modelProfileDocument struct {
	Name                  *string                   `yaml:"name"`
	Role                  *ModelRole                `yaml:"role"`
	InheritAgent          *bool                     `yaml:"inherit_agent,omitempty"`
	CredentialReference   *ModelCredentialReference `yaml:"credential_ref"`
	ProviderKind          *string                   `yaml:"provider_kind,omitempty"`
	Endpoint              *string                   `yaml:"endpoint,omitempty"`
	Model                 *string                   `yaml:"model,omitempty"`
	ReasoningEffort       *string                   `yaml:"reasoning_effort,omitempty"`
	Temperature           *float64                  `yaml:"temperature,omitempty"`
	MaxOutputTokens       *int                      `yaml:"max_output_tokens,omitempty"`
	RequestTimeoutSeconds *int                      `yaml:"request_timeout_seconds,omitempty"`
	Streaming             *bool                     `yaml:"streaming,omitempty"`
	ToolCallingRequired   *bool                     `yaml:"tool_calling_required,omitempty"`
}

type kubernetesDocument struct {
	ExecCredentials *string `yaml:"exec_credentials,omitempty"`
	NamespaceAccess *string `yaml:"namespace_access,omitempty"`
}

type loggingDocument struct {
	Enabled              *bool   `yaml:"enabled,omitempty"`
	Level                *string `yaml:"level,omitempty"`
	SensitiveDiagnostics *bool   `yaml:"sensitive_diagnostics,omitempty"`
}

func decodeConfigDocument(content []byte, version int) (Config, error) {
	switch version {
	case LegacyVersion:
		defaults := Defaults()
		document := configV1Document{
			Version: LegacyVersion, Namespace: defaults.Namespace, Runtime: defaults.Runtime,
			Model: legacyModelConfig{
				ProviderKind: defaults.Models.Agent.ProviderKind,
				Temperature:  defaults.Models.Agent.Temperature, MaxOutputTokens: LegacyDefaultMaxModelOutputTokens,
				RequestTimeoutSeconds: defaults.Models.Agent.RequestTimeoutSeconds,
				Streaming:             defaults.Models.Agent.Streaming, ToolCallingRequired: defaults.Models.Agent.ToolCallingRequired,
			},
			Kubernetes: defaults.Kubernetes, Logging: defaults.Logging,
		}
		if err := decodeStrictDocument(content, &document); err != nil {
			return Config{}, err
		}
		config := Config{
			Version: CurrentVersion, Context: document.Context, Namespace: document.Namespace, NoColor: document.NoColor,
			Runtime: document.Runtime,
			Models: ModelProfilesConfig{Agent: ModelProfileConfig{
				Name: "agent", Role: ModelRoleAgent, CredentialReference: ModelCredentialAgent,
				ProviderKind: document.Model.ProviderKind, Endpoint: document.Model.Endpoint, Model: document.Model.Model,
				ReasoningEffort: document.Model.ReasoningEffort, Temperature: document.Model.Temperature,
				MaxOutputTokens: document.Model.MaxOutputTokens, RequestTimeoutSeconds: document.Model.RequestTimeoutSeconds,
				Streaming: document.Model.Streaming, ToolCallingRequired: document.Model.ToolCallingRequired,
			}},
			Kubernetes: document.Kubernetes, Logging: document.Logging,
		}
		return config, nil
	case CurrentVersion:
		var document configV2Document
		if err := decodeStrictDocument(content, &document); err != nil {
			return Config{}, err
		}
		if document.Version != CurrentVersion || document.Models == nil || document.Models.Agent == nil ||
			!document.Models.Agent.complete(false) ||
			(*document.Models.Agent.Endpoint == "") != (*document.Models.Agent.Model == "") {
			return Config{}, schemaError("Configuration version 2 requires one complete models.agent profile.")
		}
		config := Defaults()
		applyRootDocument(&config, document)
		config.Models.Agent = applyProfileDocument(defaultAgentProfile(), document.Models.Agent)
		if reviewer := document.Models.ApprovalReviewer; reviewer != nil {
			if reviewer.Name == nil || reviewer.Role == nil || reviewer.InheritAgent == nil || reviewer.CredentialReference == nil {
				return Config{}, schemaError("The approval_reviewer profile must explicitly name its role, inheritance choice, and credential reference.")
			}
			if !*reviewer.InheritAgent && !reviewer.complete(true) {
				return Config{}, schemaError("A non-inheriting approval_reviewer profile must provide every model setting.")
			}
			base := defaultReviewerProfile()
			if *reviewer.InheritAgent {
				base = config.Models.Agent
				base.Role = ModelRoleApprovalReviewer
				base.Streaming = false
				base.ToolCallingRequired = false
			}
			resolved := applyProfileDocument(base, reviewer)
			config.Models.ApprovalReviewer = &resolved
		}
		return config, nil
	default:
		return Config{}, newSafeError(ClassConfigurationInvalid, "config_version_unsupported", "decode_configuration", "Configuration version is unsupported; use version 2 or migrate a version 1 file.")
	}
}

func (profile *modelProfileDocument) complete(requireInheritance bool) bool {
	return profile != nil && profile.Name != nil && profile.Role != nil && profile.CredentialReference != nil &&
		(!requireInheritance || profile.InheritAgent != nil) && profile.ProviderKind != nil && profile.Endpoint != nil &&
		profile.Model != nil && profile.Temperature != nil &&
		profile.RequestTimeoutSeconds != nil && profile.Streaming != nil && profile.ToolCallingRequired != nil
}

func applyRootDocument(config *Config, document configV2Document) {
	if document.Context != nil {
		config.Context = *document.Context
	}
	if document.Namespace != nil {
		config.Namespace = *document.Namespace
	}
	if document.NoColor != nil {
		config.NoColor = *document.NoColor
	}
	if document.Runtime != nil && document.Runtime.BudgetProfile != nil {
		config.Runtime.BudgetProfile = *document.Runtime.BudgetProfile
	}
	if document.Kubernetes != nil {
		if document.Kubernetes.ExecCredentials != nil {
			config.Kubernetes.ExecCredentials = *document.Kubernetes.ExecCredentials
		}
		if document.Kubernetes.NamespaceAccess != nil {
			config.Kubernetes.NamespaceAccess = *document.Kubernetes.NamespaceAccess
		}
	}
	if document.Logging != nil {
		if document.Logging.Enabled != nil {
			config.Logging.Enabled = *document.Logging.Enabled
		}
		if document.Logging.Level != nil {
			config.Logging.Level = *document.Logging.Level
		}
		if document.Logging.SensitiveDiagnostics != nil {
			config.Logging.SensitiveDiagnostics = *document.Logging.SensitiveDiagnostics
		}
	}
}

func applyProfileDocument(profile ModelProfileConfig, document *modelProfileDocument) ModelProfileConfig {
	if document.Name != nil {
		profile.Name = *document.Name
	}
	if document.Role != nil {
		profile.Role = *document.Role
	}
	if document.InheritAgent != nil {
		profile.InheritAgent = *document.InheritAgent
	}
	if document.CredentialReference != nil {
		profile.CredentialReference = *document.CredentialReference
	}
	if document.ProviderKind != nil {
		profile.ProviderKind = *document.ProviderKind
	}
	if document.Endpoint != nil {
		profile.Endpoint = *document.Endpoint
	}
	if document.Model != nil {
		profile.Model = *document.Model
	}
	if document.ReasoningEffort != nil {
		profile.ReasoningEffort = *document.ReasoningEffort
	}
	if document.Temperature != nil {
		profile.Temperature = *document.Temperature
	}
	if document.MaxOutputTokens != nil {
		profile.MaxOutputTokens = *document.MaxOutputTokens
	}
	if document.RequestTimeoutSeconds != nil {
		profile.RequestTimeoutSeconds = *document.RequestTimeoutSeconds
	}
	if document.Streaming != nil {
		profile.Streaming = *document.Streaming
	}
	if document.ToolCallingRequired != nil {
		profile.ToolCallingRequired = *document.ToolCallingRequired
	}
	return profile
}

func decodeStrictDocument(content []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil && !errors.Is(err, io.EOF) {
		return schemaError("Configuration must be valid YAML and match the documented types and schema exactly.")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return schemaError("Configuration must contain exactly one YAML document.")
	}
	return nil
}

func schemaError(message string) error {
	return newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", message)
}

// extractSensitiveConfig removes both fixed role credential fields before
// ordinary configuration decoding.
func extractSensitiveConfig(content []byte) ([]byte, extractedCredentials, int, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decodeErr := decoder.Decode(&document)
	var extra any
	extraErr := decoder.Decode(&extra)
	version, versionOK := configDocumentVersion(&document)
	if decodeErr != nil || !errors.Is(extraErr, io.EOF) || containsProhibitedYAMLNode(&document) ||
		!versionOK || (version != LegacyVersion && version != CurrentVersion) {
		return nil, extractedCredentials{}, version, schemaError("Configuration values must use a supported version and the documented YAML types without null, alias, or merge values.")
	}
	if !validConfigYAMLDocument(&document, true, version) {
		return nil, extractedCredentials{}, version, schemaError("Configuration values must use the documented YAML types and must not contain unknown or duplicate fields.")
	}
	root := document.Content[0]
	var credentials extractedCredentials
	if version == LegacyVersion {
		model := mappingValue(root, "model")
		if model != nil {
			var err error
			credentials.agent, credentials.agentFound, err = extractProfileCredential(model)
			if err != nil {
				return nil, extractedCredentials{}, version, err
			}
		}
	} else {
		models := mappingValue(root, "models")
		if models != nil {
			if agent := mappingValue(models, "agent"); agent != nil {
				var err error
				credentials.agent, credentials.agentFound, err = extractProfileCredential(agent)
				if err != nil {
					return nil, extractedCredentials{}, version, err
				}
			}
			if reviewer := mappingValue(models, "approval_reviewer"); reviewer != nil {
				var err error
				credentials.reviewer, credentials.reviewerFound, err = extractProfileCredential(reviewer)
				if err != nil {
					credentials.destroy()
					return nil, extractedCredentials{}, version, err
				}
			}
		}
	}
	sanitized, err := yaml.Marshal(&document)
	if err != nil || len(sanitized) > MaxConfigFileBytes {
		credentials.destroy()
		return nil, extractedCredentials{}, version, schemaError("Configuration could not be decoded safely.")
	}
	return sanitized, credentials, version, nil
}

func extractProfileCredential(profile *yaml.Node) (SecretValue, bool, error) {
	filtered := make([]*yaml.Node, 0, len(profile.Content))
	var credential SecretValue
	found := false
	for index := 0; index < len(profile.Content); index += 2 {
		key, value := profile.Content[index], profile.Content[index+1]
		if key.Value != "api_key" {
			filtered = append(filtered, key, value)
			continue
		}
		if found {
			return SecretValue{}, false, schemaError("Configuration must not contain duplicate credential fields.")
		}
		var err error
		credential, err = NewSecretValue(value.Value)
		value.Value = ""
		if err != nil {
			return SecretValue{}, false, err
		}
		found = true
	}
	profile.Content = filtered
	return credential, found, nil
}

func mappingValue(mapping *yaml.Node, name string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == name {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func configDocumentVersion(document *yaml.Node) (int, bool) {
	if document == nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 ||
		document.Content[0].Kind != yaml.MappingNode {
		return 0, false
	}
	root := document.Content[0]
	seen := false
	version := 0
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value != "version" {
			continue
		}
		if seen || !yamlScalar(root.Content[index+1], "!!int") {
			return 0, false
		}
		parsed, err := strconv.Atoi(root.Content[index+1].Value)
		if err != nil {
			return 0, false
		}
		seen = true
		version = parsed
	}
	return version, seen
}

func validConfigYAMLDocument(document *yaml.Node, allowCredential bool, version int) bool {
	if document == nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return false
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode || len(root.Content)%2 != 0 {
		return false
	}
	versionSeen := false
	seen := make(map[string]struct{}, len(root.Content)/2)
	for index := 0; index < len(root.Content); index += 2 {
		key, value := root.Content[index], root.Content[index+1]
		if !yamlString(key) {
			return false
		}
		if _, duplicate := seen[key.Value]; duplicate {
			return false
		}
		seen[key.Value] = struct{}{}
		switch key.Value {
		case "version":
			if !yamlScalar(value, "!!int") {
				return false
			}
			versionSeen = true
		case "context", "namespace":
			if !yamlString(value) {
				return false
			}
		case "no_color":
			if !yamlScalar(value, "!!bool") {
				return false
			}
		case "runtime":
			if !validRuntimeYAML(value) {
				return false
			}
		case "model":
			if version != LegacyVersion || !validModelYAML(value, allowCredential, false) {
				return false
			}
		case "models":
			if version != CurrentVersion || !validModelsYAML(value, allowCredential) {
				return false
			}
		case "kubernetes":
			if !validKubernetesYAML(value) {
				return false
			}
		case "logging":
			if !validLoggingYAML(value) {
				return false
			}
		default:
			return false
		}
	}
	return versionSeen
}

func validRuntimeYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		return key == "budget_profile" && yamlString(value)
	})
}

func validModelsYAML(node *yaml.Node, allowCredential bool) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		return (key == "agent" || key == "approval_reviewer") && validModelYAML(value, allowCredential, true)
	})
}

func validModelYAML(node *yaml.Node, allowCredential, named bool) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "name", "role", "credential_ref":
			return named && yamlString(value)
		case "inherit_agent":
			return named && yamlScalar(value, "!!bool")
		case "provider_kind", "endpoint", "model", "reasoning_effort":
			return yamlString(value)
		case "api_key":
			return allowCredential && yamlString(value)
		case "temperature":
			return yamlScalar(value, "!!int", "!!float")
		case "max_output_tokens", "request_timeout_seconds":
			return yamlScalar(value, "!!int")
		case "streaming", "tool_calling_required":
			return yamlScalar(value, "!!bool")
		default:
			return false
		}
	})
}

func validKubernetesYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		return (key == "exec_credentials" || key == "namespace_access") && yamlString(value)
	})
}

func validLoggingYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "enabled", "sensitive_diagnostics":
			return yamlScalar(value, "!!bool")
		case "level":
			return yamlString(value)
		default:
			return false
		}
	})
}

func validYAMLMapping(node *yaml.Node, validate func(string, *yaml.Node) bool) bool {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return false
	}
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if !yamlString(key) || !validate(key.Value, value) {
			return false
		}
		if _, duplicate := seen[key.Value]; duplicate {
			return false
		}
		seen[key.Value] = struct{}{}
	}
	return true
}

func yamlString(node *yaml.Node) bool {
	return yamlScalar(node, "!!str")
}

func yamlScalar(node *yaml.Node, tags ...string) bool {
	if node == nil || node.Kind != yaml.ScalarNode {
		return false
	}
	for _, tag := range tags {
		if node.Tag == tag {
			return true
		}
	}
	return false
}

func containsProhibitedYAMLNode(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode || node.Tag == "!!null" || node.Tag == "!!merge" {
		return true
	}
	for _, child := range node.Content {
		if containsProhibitedYAMLNode(child) {
			return true
		}
	}
	return false
}

type environmentValueKind uint8

const (
	environmentString environmentValueKind = iota + 1
	environmentBool
	environmentInt
	environmentFloat
)

func applyEnvironment(config *Config, lookup func(string) (string, bool)) error {
	if config == nil || lookup == nil {
		return schemaError("Configuration environment processing is unavailable.")
	}
	type field struct {
		names []string
		kind  environmentValueKind
		apply func(any)
	}
	fields := []field{
		{[]string{"KUPILOT_CONTEXT"}, environmentString, func(value any) { config.Context = value.(string) }},
		{[]string{"KUPILOT_NAMESPACE"}, environmentString, func(value any) { config.Namespace = value.(string) }},
		{[]string{"KUPILOT_BUDGET_PROFILE"}, environmentString, func(value any) { config.Runtime.BudgetProfile = value.(string) }},
		{[]string{"KUPILOT_NO_COLOR"}, environmentBool, func(value any) { config.NoColor = value.(bool) }},
		{[]string{"KUPILOT_AGENT_ENDPOINT", "KUPILOT_MODEL_ENDPOINT"}, environmentString, func(value any) { config.Models.Agent.Endpoint = value.(string) }},
		{[]string{"KUPILOT_AGENT_MODEL", "KUPILOT_MODEL"}, environmentString, func(value any) { config.Models.Agent.Model = value.(string) }},
		{[]string{"KUPILOT_AGENT_REASONING_EFFORT", "KUPILOT_MODEL_REASONING_EFFORT"}, environmentString, func(value any) { config.Models.Agent.ReasoningEffort = value.(string) }},
		{[]string{"KUPILOT_AGENT_TEMPERATURE", "KUPILOT_MODEL_TEMPERATURE"}, environmentFloat, func(value any) { config.Models.Agent.Temperature = value.(float64) }},
		{[]string{"KUPILOT_AGENT_MAX_OUTPUT_TOKENS", "KUPILOT_MODEL_MAX_OUTPUT_TOKENS"}, environmentInt, func(value any) { config.Models.Agent.MaxOutputTokens = value.(int) }},
		{[]string{"KUPILOT_AGENT_REQUEST_TIMEOUT_SECONDS", "KUPILOT_MODEL_REQUEST_TIMEOUT_SECONDS"}, environmentInt, func(value any) { config.Models.Agent.RequestTimeoutSeconds = value.(int) }},
		{[]string{"KUPILOT_EXEC_CREDENTIALS"}, environmentString, func(value any) { config.Kubernetes.ExecCredentials = value.(string) }},
		{[]string{"KUPILOT_NAMESPACE_ACCESS"}, environmentString, func(value any) { config.Kubernetes.NamespaceAccess = value.(string) }},
		{[]string{"KUPILOT_LOG_ENABLED"}, environmentBool, func(value any) { config.Logging.Enabled = value.(bool) }},
		{[]string{"KUPILOT_LOG_LEVEL"}, environmentString, func(value any) { config.Logging.Level = value.(string) }},
	}
	for _, candidate := range fields {
		value, found, err := lookupUniqueEnvironment(lookup, candidate.names)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		parsed, err := parseEnvironmentValue(value, candidate.kind)
		if err != nil {
			return schemaError("An admitted configuration environment variable has an invalid type or value.")
		}
		candidate.apply(parsed)
	}
	if _, ok := lookup("NO_COLOR"); ok {
		config.NoColor = true
	}
	if config.Models.ApprovalReviewer != nil {
		reviewer := config.Models.ApprovalReviewer
		reviewerFields := []field{
			{[]string{"KUPILOT_APPROVAL_REVIEWER_ENDPOINT"}, environmentString, func(value any) { reviewer.Endpoint = value.(string) }},
			{[]string{"KUPILOT_APPROVAL_REVIEWER_MODEL"}, environmentString, func(value any) { reviewer.Model = value.(string) }},
			{[]string{"KUPILOT_APPROVAL_REVIEWER_REASONING_EFFORT"}, environmentString, func(value any) { reviewer.ReasoningEffort = value.(string) }},
			{[]string{"KUPILOT_APPROVAL_REVIEWER_TEMPERATURE"}, environmentFloat, func(value any) { reviewer.Temperature = value.(float64) }},
			{[]string{"KUPILOT_APPROVAL_REVIEWER_MAX_OUTPUT_TOKENS"}, environmentInt, func(value any) { reviewer.MaxOutputTokens = value.(int) }},
			{[]string{"KUPILOT_APPROVAL_REVIEWER_REQUEST_TIMEOUT_SECONDS"}, environmentInt, func(value any) { reviewer.RequestTimeoutSeconds = value.(int) }},
		}
		for _, candidate := range reviewerFields {
			value, found, err := lookupUniqueEnvironment(lookup, candidate.names)
			if err != nil {
				return err
			}
			if !found {
				continue
			}
			parsed, err := parseEnvironmentValue(value, candidate.kind)
			if err != nil {
				return schemaError("An approval_reviewer environment variable has an invalid type or value.")
			}
			candidate.apply(parsed)
		}
	} else {
		for _, name := range []string{
			"KUPILOT_APPROVAL_REVIEWER_ENDPOINT", "KUPILOT_APPROVAL_REVIEWER_MODEL",
			"KUPILOT_APPROVAL_REVIEWER_REASONING_EFFORT", "KUPILOT_APPROVAL_REVIEWER_TEMPERATURE",
			"KUPILOT_APPROVAL_REVIEWER_MAX_OUTPUT_TOKENS", "KUPILOT_APPROVAL_REVIEWER_REQUEST_TIMEOUT_SECONDS",
		} {
			if _, found := lookup(name); found {
				return schemaError("Approval reviewer environment settings require an explicit models.approval_reviewer profile.")
			}
		}
	}
	return nil
}

func lookupUniqueEnvironment(lookup func(string) (string, bool), names []string) (string, bool, error) {
	value := ""
	found := false
	for _, name := range names {
		candidate, present := lookup(name)
		if !present {
			continue
		}
		if found {
			return "", false, schemaError("Legacy and role-specific aliases for the same setting must not both be set.")
		}
		value = candidate
		found = true
	}
	return value, found, nil
}

func parseEnvironmentValue(value string, kind environmentValueKind) (any, error) {
	switch kind {
	case environmentString:
		return value, nil
	case environmentBool:
		return strconv.ParseBool(value)
	case environmentInt:
		return strconv.Atoi(value)
	case environmentFloat:
		return strconv.ParseFloat(value, 64)
	default:
		return nil, errors.New("unsupported environment configuration type")
	}
}

func applyOverrides(config *Config, overrides Overrides) {
	if overrides.Context.Set {
		config.Context = overrides.Context.Value
	}
	if overrides.Namespace.Set {
		config.Namespace = overrides.Namespace.Value
	}
	if overrides.NoColor.Set {
		config.NoColor = overrides.NoColor.Value
	}
}

func readConfigFile(ctx context.Context, path string, required bool) ([]byte, bool, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, false, newSafeError(ClassCancelled, "config_load_cancelled", "read_configuration", "Configuration loading was cancelled.")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, newSafeError(ClassConfigurationInvalid, "config_file_unavailable", "read_configuration", "Kupilot could not read the selected configuration file.")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, false, newSafeError(ClassConfigurationInvalid, "config_file_unsafe", "read_configuration", "The configuration file must be a regular file and must not be a symbolic link.")
	}
	if info.Size() > MaxConfigFileBytes {
		return nil, false, false, newSafeError(ClassConfigurationInvalid, "config_file_too_large", "read_configuration", "The configuration file exceeds the 64 KiB limit.")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, false, newSafeError(ClassConfigurationInvalid, "config_file_unavailable", "read_configuration", "Kupilot could not read the configuration file.")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, false, false, newSafeError(ClassConfigurationInvalid, "config_file_unsafe", "read_configuration", "The configuration file changed or became unsafe while Kupilot was opening it.")
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxConfigFileBytes+1))
	if err != nil {
		return nil, false, false, newSafeError(ClassConfigurationInvalid, "config_file_unavailable", "read_configuration", "Kupilot could not read the configuration file.")
	}
	if len(content) > MaxConfigFileBytes {
		return nil, false, false, newSafeError(ClassConfigurationInvalid, "config_file_too_large", "read_configuration", "The configuration file exceeds the 64 KiB limit.")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, false, newSafeError(ClassCancelled, "config_load_cancelled", "read_configuration", "Configuration loading was cancelled.")
	}
	return content, true, openedInfo.Mode().Perm()&0o077 != 0, nil
}
