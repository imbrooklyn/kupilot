package config

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/spf13/viper"
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

// Load applies defaults, a strict YAML file, admitted environment variables,
// and typed CLI overrides in increasing precedence order.
func Load(ctx context.Context, options LoadOptions) (Loaded, error) {
	if ctx == nil || ctx.Err() != nil {
		return Loaded{}, newSafeError(ClassCancelled, "config_load_cancelled", "load_configuration", "Configuration loading was cancelled.")
	}
	lookup := options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}

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

	instance := viper.New()
	instance.SetConfigType("yaml")
	setDefaults(instance)
	content, found, permissionsWider, err := readConfigFile(ctx, configFile, explicitFile)
	if err != nil {
		return Loaded{}, err
	}
	var fileCredential SecretValue
	if found {
		var sanitized []byte
		sanitized, fileCredential, _, err = extractSensitiveConfig(content)
		zeroBytes(content)
		content = nil
		if err != nil {
			return Loaded{}, err
		}
		if err := validateStrictYAML(sanitized); err != nil {
			fileCredential.Destroy()
			return Loaded{}, err
		}
		if err := instance.ReadConfig(bytes.NewReader(sanitized)); err != nil {
			fileCredential.Destroy()
			return Loaded{}, newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration must be valid YAML and match the documented schema exactly.")
		}
	}

	if err := applyEnvironment(instance, lookup); err != nil {
		fileCredential.Destroy()
		return Loaded{}, err
	}
	applyOverrides(instance, options.Overrides)
	config := Defaults()
	if err := instance.UnmarshalExact(&config); err != nil {
		fileCredential.Destroy()
		return Loaded{}, newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration values must match the documented types and schema exactly.")
	}
	if err := Validate(&config); err != nil {
		fileCredential.Destroy()
		return Loaded{}, err
	}
	environmentSource := &EnvironmentSecretSource{LookupEnv: lookup, Unsetenv: options.Unsetenv}
	environmentCredential, environmentFound, err := environmentSource.ReadOptional()
	if err != nil {
		fileCredential.Destroy()
		return Loaded{}, err
	}
	credential := fileCredential
	credentialSource := CredentialSourceNone
	if credential.IsSet() {
		credentialSource = CredentialSourceFile
	}
	if environmentFound {
		fileCredential.Destroy()
		credential = environmentCredential
		credentialSource = CredentialSourceEnvironment
	}
	if err := ctx.Err(); err != nil {
		credential.Destroy()
		return Loaded{}, newSafeError(ClassCancelled, "config_load_cancelled", "load_configuration", "Configuration loading was cancelled.")
	}
	warnings := make([]string, 0, 2)
	if options.Paths.HomePermissionsWider {
		warnings = append(warnings, "KUPILOT_HOME is accessible beyond its owner; Kupilot will respect the existing user-managed permissions.")
	}
	if permissionsWider {
		warnings = append(warnings, "The selected configuration file is accessible beyond its owner; it may contain a plaintext model API key.")
	}
	if config.Logging.SensitiveDiagnostics {
		warnings = append(warnings, "Sensitive model diagnostics are enabled; local logs may contain endpoint details, provider error content, and source paths.")
	}
	return Loaded{
		Config: config, Paths: options.Paths, Credential: credential,
		CredentialSource: credentialSource, Warnings: warnings,
	}, nil
}

// extractSensitiveConfig removes the sole admitted credential field before
// strict decoding or Viper sees the document.
func extractSensitiveConfig(content []byte) ([]byte, SecretValue, bool, error) {
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decodeErr := decoder.Decode(&document)
	var extra any
	extraErr := decoder.Decode(&extra)
	if decodeErr != nil || !errors.Is(extraErr, io.EOF) || containsProhibitedYAMLNode(&document) ||
		!validConfigYAMLDocument(&document, true) {
		return nil, SecretValue{}, false, newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration values must use the documented YAML types and must not use null, alias, or merge values.")
	}
	root := document.Content[0]
	var credential SecretValue
	found := false
	for index := 0; index < len(root.Content); index += 2 {
		if root.Content[index].Value != "model" {
			continue
		}
		model := root.Content[index+1]
		filtered := make([]*yaml.Node, 0, len(model.Content))
		for field := 0; field < len(model.Content); field += 2 {
			key, value := model.Content[field], model.Content[field+1]
			if key.Value != "api_key" {
				filtered = append(filtered, key, value)
				continue
			}
			if found {
				return nil, SecretValue{}, false, newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration must not contain duplicate fields.")
			}
			var err error
			credential, err = NewSecretValue(value.Value)
			if err != nil {
				return nil, SecretValue{}, false, err
			}
			value.Value = ""
			found = true
		}
		model.Content = filtered
	}
	sanitized, err := yaml.Marshal(&document)
	if err != nil || len(sanitized) > MaxConfigFileBytes {
		credential.Destroy()
		return nil, SecretValue{}, false, newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration could not be decoded safely.")
	}
	return sanitized, credential, found, nil
}

func validateStrictYAML(content []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var decoded Config
	if err := decoder.Decode(&decoded); err != nil && !errors.Is(err, io.EOF) {
		return newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration must be valid YAML and match the documented types and schema exactly.")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration must contain exactly one YAML document.")
	}

	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil || containsProhibitedYAMLNode(&document) || !validConfigYAMLDocument(&document, false) {
		return newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration values must use the documented YAML types and must not use null, alias, or merge values.")
	}
	return nil
}

func validConfigYAMLDocument(document *yaml.Node, allowCredential bool) bool {
	if document == nil || document.Kind == 0 {
		return false
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
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
			if !validModelYAML(value, allowCredential) {
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

func validModelYAML(node *yaml.Node, allowCredential bool) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
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

func setDefaults(instance *viper.Viper) {
	defaults := Defaults()
	instance.SetDefault("version", defaults.Version)
	instance.SetDefault("context", defaults.Context)
	instance.SetDefault("namespace", defaults.Namespace)
	instance.SetDefault("no_color", defaults.NoColor)
	instance.SetDefault("runtime.budget_profile", defaults.Runtime.BudgetProfile)
	instance.SetDefault("model.provider_kind", defaults.Model.ProviderKind)
	instance.SetDefault("model.endpoint", defaults.Model.Endpoint)
	instance.SetDefault("model.model", defaults.Model.Model)
	instance.SetDefault("model.reasoning_effort", defaults.Model.ReasoningEffort)
	instance.SetDefault("model.temperature", defaults.Model.Temperature)
	instance.SetDefault("model.max_output_tokens", defaults.Model.MaxOutputTokens)
	instance.SetDefault("model.request_timeout_seconds", defaults.Model.RequestTimeoutSeconds)
	instance.SetDefault("model.streaming", defaults.Model.Streaming)
	instance.SetDefault("model.tool_calling_required", defaults.Model.ToolCallingRequired)
	instance.SetDefault("kubernetes.exec_credentials", defaults.Kubernetes.ExecCredentials)
	instance.SetDefault("kubernetes.namespace_access", defaults.Kubernetes.NamespaceAccess)
	instance.SetDefault("logging.enabled", defaults.Logging.Enabled)
	instance.SetDefault("logging.level", defaults.Logging.Level)
	instance.SetDefault("logging.sensitive_diagnostics", defaults.Logging.SensitiveDiagnostics)
}

type environmentValueKind uint8

const (
	environmentString environmentValueKind = iota + 1
	environmentBool
	environmentInt
	environmentFloat
)

func applyEnvironment(instance *viper.Viper, lookup func(string) (string, bool)) error {
	fields := []struct {
		environment string
		key         string
		kind        environmentValueKind
	}{
		{environment: "KUPILOT_CONTEXT", key: "context", kind: environmentString},
		{environment: "KUPILOT_NAMESPACE", key: "namespace", kind: environmentString},
		{environment: "KUPILOT_BUDGET_PROFILE", key: "runtime.budget_profile", kind: environmentString},
		{environment: "KUPILOT_NO_COLOR", key: "no_color", kind: environmentBool},
		{environment: "KUPILOT_MODEL_ENDPOINT", key: "model.endpoint", kind: environmentString},
		{environment: "KUPILOT_MODEL", key: "model.model", kind: environmentString},
		{environment: "KUPILOT_MODEL_REASONING_EFFORT", key: "model.reasoning_effort", kind: environmentString},
		{environment: "KUPILOT_MODEL_TEMPERATURE", key: "model.temperature", kind: environmentFloat},
		{environment: "KUPILOT_MODEL_MAX_OUTPUT_TOKENS", key: "model.max_output_tokens", kind: environmentInt},
		{environment: "KUPILOT_MODEL_REQUEST_TIMEOUT_SECONDS", key: "model.request_timeout_seconds", kind: environmentInt},
		{environment: "KUPILOT_EXEC_CREDENTIALS", key: "kubernetes.exec_credentials", kind: environmentString},
		{environment: "KUPILOT_NAMESPACE_ACCESS", key: "kubernetes.namespace_access", kind: environmentString},
		{environment: "KUPILOT_LOG_ENABLED", key: "logging.enabled", kind: environmentBool},
		{environment: "KUPILOT_LOG_LEVEL", key: "logging.level", kind: environmentString},
	}
	for _, field := range fields {
		if value, ok := lookup(field.environment); ok {
			parsed, err := parseEnvironmentValue(value, field.kind)
			if err != nil {
				return newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_environment_configuration", "An admitted configuration environment variable has an invalid type or value.")
			}
			instance.Set(field.key, parsed)
		}
	}
	if _, ok := lookup("NO_COLOR"); ok {
		instance.Set("no_color", true)
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

func applyOverrides(instance *viper.Viper, overrides Overrides) {
	if overrides.Context.Set {
		instance.Set("context", overrides.Context.Value)
	}
	if overrides.Namespace.Set {
		instance.Set("namespace", overrides.Namespace.Value)
	}
	if overrides.NoColor.Set {
		instance.Set("no_color", overrides.NoColor.Value)
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
		return nil, false, false, newSafeError(ClassConfigurationInvalid, "config_file_unavailable", "read_configuration", "Kupilot could not read the selected configuration file.")
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
