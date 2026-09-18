package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/imbrooklyn/kupilot/internal/domain"
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
	agent           SecretValue
	agentFound      bool
	reviewer        SecretValue
	reviewerFound   bool
	prometheus      SecretValue
	prometheusFound bool
	loki            SecretValue
	lokiFound       bool
}

func (credentials *extractedCredentials) destroy() {
	if credentials == nil {
		return
	}
	credentials.agent.Destroy()
	credentials.reviewer.Destroy()
	credentials.prometheus.Destroy()
	credentials.loki.Destroy()
}

// Load applies defaults, a strict versioned YAML file, admitted environment
// variables, and typed CLI overrides in increasing precedence order.
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
		Variable: AgentAPIKeyEnvironmentVariable,
	}
	reviewerEnvironment := &EnvironmentSecretSource{
		LookupEnv: lookup, Unsetenv: options.Unsetenv,
		Variable: ApprovalReviewerAPIKeyEnvironmentVariable,
	}
	prometheusEnvironment := &EnvironmentSecretSource{
		LookupEnv: lookup, Unsetenv: options.Unsetenv, Variable: PrometheusAPIKeyEnvironmentVariable,
	}
	lokiEnvironment := &EnvironmentSecretSource{
		LookupEnv: lookup, Unsetenv: options.Unsetenv, Variable: LokiAPIKeyEnvironmentVariable,
	}
	agentEnvironmentCredential, agentEnvironmentFound, agentEnvironmentErr := agentEnvironment.ReadOptional()
	reviewerEnvironmentCredential, reviewerEnvironmentFound, reviewerEnvironmentErr := reviewerEnvironment.ReadOptional()
	prometheusEnvironmentCredential, prometheusEnvironmentFound, prometheusEnvironmentErr := prometheusEnvironment.ReadOptional()
	lokiEnvironmentCredential, lokiEnvironmentFound, lokiEnvironmentErr := lokiEnvironment.ReadOptional()
	if agentEnvironmentErr != nil || reviewerEnvironmentErr != nil || prometheusEnvironmentErr != nil || lokiEnvironmentErr != nil {
		agentEnvironmentCredential.Destroy()
		reviewerEnvironmentCredential.Destroy()
		prometheusEnvironmentCredential.Destroy()
		lokiEnvironmentCredential.Destroy()
		if agentEnvironmentErr != nil {
			return Loaded{}, agentEnvironmentErr
		}
		if reviewerEnvironmentErr != nil {
			return Loaded{}, reviewerEnvironmentErr
		}
		if prometheusEnvironmentErr != nil {
			return Loaded{}, prometheusEnvironmentErr
		}
		return Loaded{}, lokiEnvironmentErr
	}
	environmentCredentialsTransferred := false
	defer func() {
		if !environmentCredentialsTransferred {
			agentEnvironmentCredential.Destroy()
			reviewerEnvironmentCredential.Destroy()
			prometheusEnvironmentCredential.Destroy()
			lokiEnvironmentCredential.Destroy()
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
		Role: ModelRoleAgent, Reference: config.Models.Agent.CredentialReference, Source: agentSource, Value: agentCredential,
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
		reviewerCredential.Value = fileCredentials.reviewer
		if fileCredentials.reviewerFound {
			reviewerCredential.Source = CredentialSourceFile
		}
		if reviewerEnvironmentFound {
			fileCredentials.reviewer.Destroy()
			reviewerCredential.Value = reviewerEnvironmentCredential
			reviewerCredential.Source = CredentialSourceEnvironment
		}
		credentials.ApprovalReviewer = &reviewerCredential
	}

	bindDataSourceCredential := func(
		kind domain.DataSourceKind,
		configured *DataSourceConfig,
		reference DataSourceCredentialReference,
		fileValue SecretValue,
		fileFound bool,
		environmentValue SecretValue,
		environmentFound bool,
	) (*DataSourceCredential, error) {
		if configured == nil {
			fileValue.Destroy()
			environmentValue.Destroy()
			if fileFound || environmentFound {
				return nil, newSafeError(ClassConfigurationInvalid, "config_data_source_credential_unbound", "load_configuration", "An observability API key requires its explicit observability source configuration.")
			}
			return nil, nil
		}
		credential := &DataSourceCredential{Kind: kind, Reference: configured.CredentialReference}
		if configured.CredentialReference == DataSourceCredentialNone {
			fileValue.Destroy()
			environmentValue.Destroy()
			if fileFound || environmentFound {
				return nil, newSafeError(ClassConfigurationInvalid, "config_data_source_credential_conflict", "load_configuration", "An observability source with credential_ref none must not define an API key.")
			}
			return credential, nil
		}
		if configured.CredentialReference != reference {
			fileValue.Destroy()
			environmentValue.Destroy()
			return nil, newSafeError(ClassConfigurationInvalid, "config_data_source_credential_invalid", "load_configuration", "An observability credential reference does not match its source.")
		}
		credential.Value = fileValue
		if fileFound {
			credential.Source = CredentialSourceFile
		}
		if environmentFound {
			fileValue.Destroy()
			credential.Value = environmentValue
			credential.Source = CredentialSourceEnvironment
		}
		if !credential.Value.IsSet() {
			return nil, newSafeError(ClassConfigurationInvalid, "config_data_source_credential_missing", "load_configuration", "An observability source credential reference requires its fixed API key source.")
		}
		return credential, nil
	}
	credentials.Prometheus, err = bindDataSourceCredential(
		domain.DataSourcePrometheus, config.Observability.Prometheus, DataSourceCredentialPrometheus,
		fileCredentials.prometheus, fileCredentials.prometheusFound,
		prometheusEnvironmentCredential, prometheusEnvironmentFound,
	)
	if err != nil {
		credentials.Destroy()
		fileCredentials.loki.Destroy()
		lokiEnvironmentCredential.Destroy()
		return Loaded{}, err
	}
	credentials.Loki, err = bindDataSourceCredential(
		domain.DataSourceLoki, config.Observability.Loki, DataSourceCredentialLoki,
		fileCredentials.loki, fileCredentials.lokiFound, lokiEnvironmentCredential, lokiEnvironmentFound,
	)
	if err != nil {
		credentials.Destroy()
		return Loaded{}, err
	}

	if err := ctx.Err(); err != nil {
		credentials.Destroy()
		return Loaded{}, newSafeError(ClassCancelled, "config_load_cancelled", "load_configuration", "Configuration loading was cancelled.")
	}
	warnings := make([]string, 0, 3)
	if options.Paths.HomePermissionsWider {
		warnings = append(warnings, "KUPILOT_HOME is accessible beyond its owner; Kupilot will respect the existing user-managed permissions.")
	}
	if permissionsWider {
		warnings = append(warnings, "The selected configuration file is accessible beyond its owner; it may contain plaintext model or observability API keys.")
	}
	if config.Logging.SensitiveDiagnostics {
		warnings = append(warnings, "Sensitive model diagnostics are enabled; local logs may contain endpoint details, provider error content, and source paths.")
	}
	environmentCredentialsTransferred = true
	return Loaded{
		Config: config, Paths: options.Paths,
		Credentials: credentials, Warnings: warnings,
	}, nil
}

type configDocument struct {
	Version              int                    `yaml:"version"`
	Context              *string                `yaml:"context,omitempty"`
	Namespace            *string                `yaml:"namespace,omitempty"`
	NoColor              *bool                  `yaml:"no_color,omitempty"`
	ReducedMotion        *bool                  `yaml:"reduced_motion,omitempty"`
	TerminalStatusTitles *bool                  `yaml:"terminal_status_titles,omitempty"`
	Runtime              *runtimeDocument       `yaml:"runtime,omitempty"`
	Models               *modelsDocument        `yaml:"models"`
	Kubernetes           *kubernetesDocument    `yaml:"kubernetes,omitempty"`
	LocalExecution       *LocalExecutionConfig  `yaml:"local_execution,omitempty"`
	Observability        *observabilityDocument `yaml:"observability,omitempty"`
	Logging              *loggingDocument       `yaml:"logging,omitempty"`
}

type observabilityDocument struct {
	Prometheus *dataSourceDocument `yaml:"prometheus,omitempty"`
	Loki       *dataSourceDocument `yaml:"loki,omitempty"`
}

type dataSourceDocument struct {
	Endpoint              *string                        `yaml:"endpoint"`
	CredentialReference   *DataSourceCredentialReference `yaml:"credential_ref"`
	Queries               *[]string                      `yaml:"queries"`
	RequestTimeoutSeconds *int                           `yaml:"request_timeout_seconds"`
}

type runtimeDocument struct {
	BudgetProfile *string `yaml:"budget_profile"`
}

type modelsDocument struct {
	Agent            *modelProfileDocument `yaml:"agent"`
	ApprovalReviewer *modelProfileDocument `yaml:"approval_reviewer,omitempty"`
}

type modelProfileDocument struct {
	Endpoint              *string  `yaml:"endpoint,omitempty"`
	Model                 *string  `yaml:"model,omitempty"`
	APIProtocol           *string  `yaml:"api_protocol,omitempty" json:"api_protocol,omitempty"`
	ReasoningEffort       *string  `yaml:"reasoning_effort,omitempty"`
	ResponseFormat        *string  `yaml:"response_format,omitempty"`
	Temperature           *float64 `yaml:"temperature,omitempty"`
	MaxOutputTokens       *int     `yaml:"max_output_tokens,omitempty"`
	RequestTimeoutSeconds *int     `yaml:"request_timeout_seconds,omitempty"`
}

type kubernetesDocument struct {
	ExecCredentials   *string                           `yaml:"exec_credentials,omitempty"`
	NamespaceAccess   *string                           `yaml:"namespace_access,omitempty"`
	ResourcePolicies  *[]KubernetesResourcePolicyConfig `yaml:"resource_policies,omitempty"`
	RemoteDiagnostics *RemoteDiagnosticsConfig          `yaml:"remote_diagnostics,omitempty"`
}

type loggingDocument struct {
	Enabled              *bool   `yaml:"enabled,omitempty"`
	Level                *string `yaml:"level,omitempty"`
	SensitiveDiagnostics *bool   `yaml:"sensitive_diagnostics,omitempty"`
}

func decodeConfigDocument(content []byte, version int) (Config, error) {
	if version != CurrentVersion {
		return Config{}, newSafeError(ClassConfigurationInvalid, "config_version_unsupported", "decode_configuration", "Configuration version is unsupported; use version 1.")
	}
	var document configDocument
	if err := decodeStrictDocument(content, &document); err != nil {
		return Config{}, err
	}
	if document.Version != CurrentVersion || document.Models == nil || document.Models.Agent == nil ||
		!document.Models.Agent.complete() ||
		(*document.Models.Agent.Endpoint == "") != (*document.Models.Agent.Model == "") {
		return Config{}, schemaError("Configuration version 1 requires one complete models.agent profile.")
	}
	config := Defaults()
	applyRootDocument(&config, document)
	agentBase := defaultAgentProfile()
	agentBase.Temperature = nil
	config.Models.Agent = applyProfileDocument(agentBase, document.Models.Agent)
	if reviewer := document.Models.ApprovalReviewer; reviewer != nil {
		if !reviewer.complete() {
			return Config{}, schemaError("The approval_reviewer profile requires its own endpoint and model.")
		}
		base := defaultReviewerProfile()
		base.Temperature = nil
		resolved := applyProfileDocument(base, reviewer)
		config.Models.ApprovalReviewer = &resolved
	}
	return config, nil
}

func (profile *modelProfileDocument) complete() bool {
	return profile != nil && profile.Endpoint != nil && profile.Model != nil
}

func applyRootDocument(config *Config, document configDocument) {
	if document.Context != nil {
		config.Context = *document.Context
	}
	if document.Namespace != nil {
		config.Namespace = *document.Namespace
	}
	if document.NoColor != nil {
		config.NoColor = *document.NoColor
	}
	if document.ReducedMotion != nil {
		config.ReducedMotion = *document.ReducedMotion
	}
	if document.TerminalStatusTitles != nil {
		config.TerminalStatusTitles = *document.TerminalStatusTitles
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
		if document.Kubernetes.ResourcePolicies != nil {
			config.Kubernetes.ResourcePolicies = append(
				[]KubernetesResourcePolicyConfig(nil),
				(*document.Kubernetes.ResourcePolicies)...,
			)
		}
		if document.Kubernetes.RemoteDiagnostics != nil {
			config.Kubernetes.RemoteDiagnostics = cloneRemoteDiagnosticsConfig(document.Kubernetes.RemoteDiagnostics)
		}
	}
	if document.LocalExecution != nil {
		config.LocalExecution = cloneLocalExecutionConfig(*document.LocalExecution)
	}
	if document.Observability != nil {
		if document.Observability.Prometheus != nil {
			config.Observability.Prometheus = applyDataSourceDocument(document.Observability.Prometheus)
		}
		if document.Observability.Loki != nil {
			config.Observability.Loki = applyDataSourceDocument(document.Observability.Loki)
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

func applyDataSourceDocument(document *dataSourceDocument) *DataSourceConfig {
	if document == nil {
		return nil
	}
	result := &DataSourceConfig{RequestTimeoutSeconds: DefaultDataSourceTimeoutSeconds}
	if document.Endpoint != nil {
		result.Endpoint = *document.Endpoint
	}
	if document.CredentialReference != nil {
		result.CredentialReference = *document.CredentialReference
	}
	if document.Queries != nil {
		result.Queries = append([]string(nil), (*document.Queries)...)
	}
	if document.RequestTimeoutSeconds != nil {
		result.RequestTimeoutSeconds = *document.RequestTimeoutSeconds
	}
	return result
}

func applyProfileDocument(profile ModelProfileConfig, document *modelProfileDocument) ModelProfileConfig {
	if document.Endpoint != nil {
		profile.Endpoint = *document.Endpoint
	}
	if document.Model != nil {
		profile.Model = *document.Model
	}
	if document.APIProtocol != nil {
		profile.APIProtocol = *document.APIProtocol
	}
	if document.ReasoningEffort != nil {
		profile.ReasoningEffort = *document.ReasoningEffort
	}
	if document.ResponseFormat != nil {
		profile.ResponseFormat = *document.ResponseFormat
	}
	if document.Temperature != nil {
		profile.Temperature = document.Temperature
	}
	if document.MaxOutputTokens != nil {
		profile.MaxOutputTokens = *document.MaxOutputTokens
	}
	if document.RequestTimeoutSeconds != nil {
		profile.RequestTimeoutSeconds = *document.RequestTimeoutSeconds
	}
	profile.Streaming = profile.Role == ModelRoleAgent && profile.APIProtocol != "responses"
	profile.ToolCallingRequired = profile.Role == ModelRoleAgent
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
		!versionOK || version != CurrentVersion {
		return nil, extractedCredentials{}, version, schemaError("Configuration values must use a supported version and the documented YAML types without null, alias, or merge values.")
	}
	if !validConfigYAMLDocument(&document, true) {
		return nil, extractedCredentials{}, version, schemaError("Configuration values must use the documented YAML types and must not contain unknown or duplicate fields.")
	}
	root := document.Content[0]
	var credentials extractedCredentials
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
	observability := mappingValue(root, "observability")
	if observability != nil {
		if prometheus := mappingValue(observability, "prometheus"); prometheus != nil {
			var err error
			credentials.prometheus, credentials.prometheusFound, err = extractProfileCredential(prometheus)
			if err != nil {
				credentials.destroy()
				return nil, extractedCredentials{}, version, err
			}
		}
		if loki := mappingValue(observability, "loki"); loki != nil {
			var err error
			credentials.loki, credentials.lokiFound, err = extractProfileCredential(loki)
			if err != nil {
				credentials.destroy()
				return nil, extractedCredentials{}, version, err
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

func validConfigYAMLDocument(document *yaml.Node, allowCredential bool) bool {
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
		case "no_color", "reduced_motion":
			if !yamlScalar(value, "!!bool") {
				return false
			}
		case "terminal_status_titles":
			if !yamlScalar(value, "!!bool") {
				return false
			}
		case "runtime":
			if !validRuntimeYAML(value) {
				return false
			}
		case "models":
			if !validModelsYAML(value, allowCredential) {
				return false
			}
		case "kubernetes":
			if !validKubernetesYAML(value, true) {
				return false
			}
		case "local_execution":
			if !validLocalExecutionYAML(value) {
				return false
			}
		case "observability":
			if !validObservabilityYAML(value, allowCredential) {
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

func validObservabilityYAML(node *yaml.Node, allowCredential bool) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		return (key == "prometheus" || key == "loki") && validDataSourceYAML(value, allowCredential)
	})
}

func validDataSourceYAML(node *yaml.Node, allowCredential bool) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "endpoint", "credential_ref":
			return yamlString(value)
		case "api_key":
			return allowCredential && yamlString(value)
		case "queries":
			return validStringSequenceYAML(value, domain.MaxObservabilityQueryTemplates)
		case "request_timeout_seconds":
			return yamlScalar(value, "!!int")
		default:
			return false
		}
	}) && yamlMappingContainsEvery(node, "endpoint", "credential_ref", "queries", "request_timeout_seconds")
}

func validRuntimeYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		return key == "budget_profile" && yamlString(value)
	})
}

func validModelsYAML(node *yaml.Node, allowCredential bool) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		return (key == "agent" || key == "approval_reviewer") && validModelYAML(value, allowCredential)
	})
}

func validModelYAML(node *yaml.Node, allowCredential bool) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "endpoint", "model", "reasoning_effort", "api_protocol", "response_format":
			return yamlString(value)
		case "api_key":
			return allowCredential && yamlString(value)
		case "temperature":
			return yamlScalar(value, "!!int", "!!float")
		case "max_output_tokens", "request_timeout_seconds":
			return yamlScalar(value, "!!int")
		default:
			return false
		}
	})
}

func validKubernetesYAML(node *yaml.Node, allowOperationalPolicies bool) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "exec_credentials", "namespace_access":
			return yamlString(value)
		case "resource_policies":
			return allowOperationalPolicies && validResourcePoliciesYAML(value)
		case "remote_diagnostics":
			return allowOperationalPolicies && validRemoteDiagnosticsYAML(value)
		default:
			return false
		}
	})
}

func validRemoteDiagnosticsYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "pod_exec":
			return validRemotePolicySequence(value, validPodExecPolicyYAML)
		case "container_file":
			return validContainerFilePolicyYAML(value)
		case "diagnostic_pods":
			return validRemotePolicySequence(value, validDiagnosticPodPolicyYAML)
		default:
			return false
		}
	})
}

func validLocalExecutionYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "commands":
			return validLocalPolicySequence(value, validLocalCommandPolicyYAML)
		case "shells":
			return validLocalPolicySequence(value, validLocalShellPolicyYAML)
		default:
			return false
		}
	})
}

func validLocalPolicySequence(node *yaml.Node, validate func(*yaml.Node) bool) bool {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) > domain.MaxLocalCommandPolicies {
		return false
	}
	for _, item := range node.Content {
		if !validate(item) {
			return false
		}
	}
	return true
}

func validLocalCommandPolicyYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "id", "kind", "executable", "working_directory", "credential_ref", "server_origin", "diagnostic_effect":
			return yamlString(value)
		case "arguments":
			return validStringSequenceYAML(value, domain.MaxActionArguments)
		case "environment":
			return validStringSequenceYAML(value, domain.MaxActionEnvironment)
		case "timeout_seconds", "max_lines", "max_bytes":
			return yamlScalar(value, "!!int")
		default:
			return false
		}
	}) && yamlMappingContainsEvery(node, "id", "kind", "executable", "arguments", "working_directory", "environment", "credential_ref", "timeout_seconds", "max_lines", "max_bytes")
}

func validLocalShellPolicyYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "id", "executable", "command", "working_directory", "network", "network_origin":
			return yamlString(value)
		case "environment":
			return validStringSequenceYAML(value, domain.MaxActionEnvironment)
		case "timeout_seconds", "max_lines", "max_bytes":
			return yamlScalar(value, "!!int")
		default:
			return false
		}
	}) && yamlMappingContainsEvery(node, "id", "executable", "command", "working_directory", "environment", "network", "timeout_seconds", "max_lines", "max_bytes")
}

func validRemotePolicySequence(node *yaml.Node, validate func(*yaml.Node) bool) bool {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) > domain.MaxRemoteDiagnosticPolicies {
		return false
	}
	for _, item := range node.Content {
		if !validate(item) {
			return false
		}
	}
	return true
}

func validPodExecPolicyYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "id", "class", "executable":
			return yamlString(value)
		case "arguments":
			return validStringSequenceYAML(value, domain.MaxActionArguments)
		case "timeout_seconds", "max_lines", "max_bytes":
			return yamlScalar(value, "!!int")
		default:
			return false
		}
	}) && yamlMappingContainsEvery(node, "id", "class", "executable", "arguments", "timeout_seconds", "max_lines", "max_bytes")
}

func validContainerFilePolicyYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "reader_executable":
			return yamlString(value)
		case "allowed_roots":
			return validStringSequenceYAML(value, domain.MaxContainerFileRoots)
		case "timeout_seconds", "max_lines", "max_bytes":
			return yamlScalar(value, "!!int")
		default:
			return false
		}
	}) && yamlMappingContainsEvery(node, "reader_executable", "allowed_roots", "timeout_seconds", "max_lines", "max_bytes")
}

func validDiagnosticPodPolicyYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "id", "namespace", "image", "executable", "service_name":
			return yamlString(value)
		case "argument_prefix":
			return validStringSequenceYAML(value, domain.MaxActionArguments-2)
		case "port", "timeout_seconds", "max_lines", "max_bytes":
			return yamlScalar(value, "!!int")
		case "network_policy_required":
			return yamlScalar(value, "!!bool")
		default:
			return false
		}
	}) && yamlMappingContainsEvery(node, "id", "namespace", "image", "executable", "argument_prefix", "service_name", "port", "network_policy_required", "timeout_seconds", "max_lines", "max_bytes")
}

func cloneRemoteDiagnosticsConfig(source *RemoteDiagnosticsConfig) *RemoteDiagnosticsConfig {
	if source == nil {
		return nil
	}
	result := &RemoteDiagnosticsConfig{PodExec: append([]PodExecPolicyConfig(nil), source.PodExec...), DiagnosticPods: append([]DiagnosticPodPolicyConfig(nil), source.DiagnosticPods...)}
	for index := range result.PodExec {
		result.PodExec[index].Arguments = append([]string(nil), source.PodExec[index].Arguments...)
	}
	for index := range result.DiagnosticPods {
		result.DiagnosticPods[index].ArgumentPrefix = append([]string(nil), source.DiagnosticPods[index].ArgumentPrefix...)
	}
	if source.ContainerFile != nil {
		copied := *source.ContainerFile
		copied.AllowedRoots = append([]string(nil), source.ContainerFile.AllowedRoots...)
		result.ContainerFile = &copied
	}
	return result
}

func cloneLocalExecutionConfig(source LocalExecutionConfig) LocalExecutionConfig {
	result := LocalExecutionConfig{
		Commands: append([]LocalCommandPolicyConfig(nil), source.Commands...),
		Shells:   append([]LocalShellPolicyConfig(nil), source.Shells...),
	}
	for index := range result.Commands {
		result.Commands[index].Arguments = append([]string(nil), source.Commands[index].Arguments...)
		result.Commands[index].Environment = append([]string(nil), source.Commands[index].Environment...)
	}
	for index := range result.Shells {
		result.Shells[index].Environment = append([]string(nil), source.Shells[index].Environment...)
	}
	return result
}

func validResourcePoliciesYAML(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content)+len(domain.BuiltInResourcePolicies()) > domain.MaxResourcePolicyEntries {
		return false
	}
	for _, entry := range node.Content {
		if !validResourcePolicyYAML(entry) {
			return false
		}
	}
	return true
}

func validResourcePolicyYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "id", "group", "version", "resource", "kind", "scope":
			return yamlString(value)
		case "verbs":
			return validStringSequenceYAML(value, 2)
		case "fields":
			return validResourceFieldsYAML(value)
		case "limits":
			return validResourceLimitsYAML(value)
		default:
			return false
		}
	}) && yamlMappingContainsEvery(node, "id", "group", "version", "resource", "kind", "scope", "verbs", "fields", "limits")
}

func validResourceFieldsYAML(node *yaml.Node) bool {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) > domain.MaxResourceFields {
		return false
	}
	for _, field := range node.Content {
		if !validYAMLMapping(field, func(key string, value *yaml.Node) bool {
			switch key {
			case "id", "path", "scalar", "data_class", "selector_source", "selector_key":
				return yamlString(value)
			case "operators":
				return validStringSequenceYAML(value, 4)
			case "evidence":
				return yamlScalar(value, "!!bool")
			default:
				return false
			}
		}) || !yamlMappingContainsEvery(field, "id", "path", "scalar", "data_class", "selector_source", "operators", "evidence") {
			return false
		}
	}
	return true
}

func validResourceLimitsYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "max_pages", "page_items", "page_bytes", "max_items", "max_bytes", "max_returned":
			return yamlScalar(value, "!!int")
		default:
			return false
		}
	}) && yamlMappingContainsEvery(node, "max_pages", "page_items", "page_bytes", "max_items", "max_bytes", "max_returned")
}

func yamlMappingContainsEvery(node *yaml.Node, names ...string) bool {
	if node == nil || node.Kind != yaml.MappingNode {
		return false
	}
	seen := make(map[string]struct{}, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		seen[node.Content[index].Value] = struct{}{}
	}
	for _, name := range names {
		if _, found := seen[name]; !found {
			return false
		}
	}
	return true
}

func validStringSequenceYAML(node *yaml.Node, maximum int) bool {
	if node == nil || node.Kind != yaml.SequenceNode || len(node.Content) > maximum {
		return false
	}
	for _, value := range node.Content {
		if !yamlString(value) {
			return false
		}
	}
	return true
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
		name  string
		kind  environmentValueKind
		apply func(any)
	}
	fields := []field{
		{"KUPILOT_CONTEXT", environmentString, func(value any) { config.Context = value.(string) }},
		{"KUPILOT_NAMESPACE", environmentString, func(value any) { config.Namespace = value.(string) }},
		{"KUPILOT_BUDGET_PROFILE", environmentString, func(value any) { config.Runtime.BudgetProfile = value.(string) }},
		{"KUPILOT_NO_COLOR", environmentBool, func(value any) { config.NoColor = value.(bool) }},
		{"KUPILOT_AGENT_ENDPOINT", environmentString, func(value any) { config.Models.Agent.Endpoint = value.(string) }},
		{"KUPILOT_AGENT_MODEL", environmentString, func(value any) { config.Models.Agent.Model = value.(string) }},
		{"KUPILOT_AGENT_REASONING_EFFORT", environmentString, func(value any) { config.Models.Agent.ReasoningEffort = value.(string) }},
		{"KUPILOT_AGENT_TEMPERATURE", environmentFloat, func(value any) { temperature := value.(float64); config.Models.Agent.Temperature = &temperature }},
		{"KUPILOT_AGENT_MAX_OUTPUT_TOKENS", environmentInt, func(value any) { config.Models.Agent.MaxOutputTokens = value.(int) }},
		{"KUPILOT_AGENT_REQUEST_TIMEOUT_SECONDS", environmentInt, func(value any) { config.Models.Agent.RequestTimeoutSeconds = value.(int) }},
		{"KUPILOT_EXEC_CREDENTIALS", environmentString, func(value any) { config.Kubernetes.ExecCredentials = value.(string) }},
		{"KUPILOT_NAMESPACE_ACCESS", environmentString, func(value any) { config.Kubernetes.NamespaceAccess = value.(string) }},
		{"KUPILOT_LOG_ENABLED", environmentBool, func(value any) { config.Logging.Enabled = value.(bool) }},
		{"KUPILOT_LOG_LEVEL", environmentString, func(value any) { config.Logging.Level = value.(string) }},
	}
	for _, candidate := range fields {
		value, found := lookup(candidate.name)
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
			{"KUPILOT_APPROVAL_REVIEWER_ENDPOINT", environmentString, func(value any) { reviewer.Endpoint = value.(string) }},
			{"KUPILOT_APPROVAL_REVIEWER_MODEL", environmentString, func(value any) { reviewer.Model = value.(string) }},
			{"KUPILOT_APPROVAL_REVIEWER_REASONING_EFFORT", environmentString, func(value any) { reviewer.ReasoningEffort = value.(string) }},
			{"KUPILOT_APPROVAL_REVIEWER_TEMPERATURE", environmentFloat, func(value any) { temperature := value.(float64); reviewer.Temperature = &temperature }},
			{"KUPILOT_APPROVAL_REVIEWER_MAX_OUTPUT_TOKENS", environmentInt, func(value any) { reviewer.MaxOutputTokens = value.(int) }},
			{"KUPILOT_APPROVAL_REVIEWER_REQUEST_TIMEOUT_SECONDS", environmentInt, func(value any) { reviewer.RequestTimeoutSeconds = value.(int) }},
		}
		for _, candidate := range reviewerFields {
			value, found := lookup(candidate.name)
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
