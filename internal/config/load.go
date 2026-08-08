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
// load. LookupEnv is injectable for tests and never receives the API-key name.
type LoadOptions struct {
	Paths     Paths
	Overrides Overrides
	LookupEnv func(string) (string, bool)
}

// Load applies defaults, a strict YAML file, admitted environment variables,
// and typed CLI overrides in increasing precedence order.
func Load(ctx context.Context, options LoadOptions) (Config, error) {
	if ctx == nil || ctx.Err() != nil {
		return Config{}, newSafeError(ClassCancelled, "config_load_cancelled", "load_configuration", "Configuration loading was cancelled.")
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
		return Config{}, newSafeError(ClassConfigurationInvalid, "config_file_path_invalid", "load_configuration", "The configuration file path must be absolute and normalized.")
	}

	instance := viper.New()
	instance.SetConfigType("yaml")
	setDefaults(instance, options.Paths)
	content, found, err := readConfigFile(ctx, configFile, explicitFile)
	if err != nil {
		return Config{}, err
	}
	if found {
		if err := validateStrictYAML(content); err != nil {
			return Config{}, err
		}
		if err := instance.ReadConfig(bytes.NewReader(content)); err != nil {
			return Config{}, newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration must be valid YAML and match the documented schema exactly.")
		}
	}

	if err := applyEnvironment(instance, lookup); err != nil {
		return Config{}, err
	}
	applyOverrides(instance, options.Overrides)
	config := Defaults(options.Paths)
	if err := instance.UnmarshalExact(&config); err != nil {
		return Config{}, newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration values must match the documented types and schema exactly.")
	}
	config.Paths.ConfigDir = options.Paths.ConfigDir
	config.Paths.ConfigFile = configFile
	if err := Validate(&config); err != nil {
		return Config{}, err
	}
	if err := ctx.Err(); err != nil {
		return Config{}, newSafeError(ClassCancelled, "config_load_cancelled", "load_configuration", "Configuration loading was cancelled.")
	}
	return config, nil
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
	if err := yaml.Unmarshal(content, &document); err != nil || containsProhibitedYAMLNode(&document) || !validConfigYAMLDocument(&document) {
		return newSafeError(ClassConfigurationInvalid, "config_schema_invalid", "decode_configuration", "Configuration values must use the documented YAML types and must not use null, alias, or merge values.")
	}
	return nil
}

func validConfigYAMLDocument(document *yaml.Node) bool {
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
	for index := 0; index < len(root.Content); index += 2 {
		key, value := root.Content[index], root.Content[index+1]
		if !yamlString(key) {
			return false
		}
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
		case "paths":
			if !validPathsYAML(value) {
				return false
			}
		case "model":
			if !validModelYAML(value) {
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

func validPathsYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "state_dir", "cache_dir", "log_dir":
			return yamlString(value)
		default:
			return false
		}
	})
}

func validModelYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "provider_kind", "endpoint", "model", "api_key_source":
			return yamlString(value)
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
		return key == "exec_credentials" && yamlString(value)
	})
}

func validLoggingYAML(node *yaml.Node) bool {
	return validYAMLMapping(node, func(key string, value *yaml.Node) bool {
		switch key {
		case "enabled":
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
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if !yamlString(key) || !validate(key.Value, value) {
			return false
		}
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

func setDefaults(instance *viper.Viper, paths Paths) {
	defaults := Defaults(paths)
	instance.SetDefault("version", defaults.Version)
	instance.SetDefault("context", defaults.Context)
	instance.SetDefault("namespace", defaults.Namespace)
	instance.SetDefault("no_color", defaults.NoColor)
	instance.SetDefault("paths.state_dir", defaults.Paths.StateDir)
	instance.SetDefault("paths.cache_dir", defaults.Paths.CacheDir)
	instance.SetDefault("paths.log_dir", defaults.Paths.LogDir)
	instance.SetDefault("model.provider_kind", defaults.Model.ProviderKind)
	instance.SetDefault("model.endpoint", defaults.Model.Endpoint)
	instance.SetDefault("model.model", defaults.Model.Model)
	instance.SetDefault("model.api_key_source", defaults.Model.APIKeySource)
	instance.SetDefault("model.temperature", defaults.Model.Temperature)
	instance.SetDefault("model.max_output_tokens", defaults.Model.MaxOutputTokens)
	instance.SetDefault("model.request_timeout_seconds", defaults.Model.RequestTimeoutSeconds)
	instance.SetDefault("model.streaming", defaults.Model.Streaming)
	instance.SetDefault("model.tool_calling_required", defaults.Model.ToolCallingRequired)
	instance.SetDefault("kubernetes.exec_credentials", defaults.Kubernetes.ExecCredentials)
	instance.SetDefault("logging.enabled", defaults.Logging.Enabled)
	instance.SetDefault("logging.level", defaults.Logging.Level)
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
		{environment: "KUPILOT_NO_COLOR", key: "no_color", kind: environmentBool},
		{environment: "KUPILOT_STATE_DIR", key: "paths.state_dir", kind: environmentString},
		{environment: "KUPILOT_CACHE_DIR", key: "paths.cache_dir", kind: environmentString},
		{environment: "KUPILOT_LOG_DIR", key: "paths.log_dir", kind: environmentString},
		{environment: "KUPILOT_MODEL_ENDPOINT", key: "model.endpoint", kind: environmentString},
		{environment: "KUPILOT_MODEL", key: "model.model", kind: environmentString},
		{environment: "KUPILOT_MODEL_TEMPERATURE", key: "model.temperature", kind: environmentFloat},
		{environment: "KUPILOT_MODEL_MAX_OUTPUT_TOKENS", key: "model.max_output_tokens", kind: environmentInt},
		{environment: "KUPILOT_MODEL_REQUEST_TIMEOUT_SECONDS", key: "model.request_timeout_seconds", kind: environmentInt},
		{environment: "KUPILOT_EXEC_CREDENTIALS", key: "kubernetes.exec_credentials", kind: environmentString},
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

func readConfigFile(ctx context.Context, path string, required bool) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, newSafeError(ClassCancelled, "config_load_cancelled", "read_configuration", "Configuration loading was cancelled.")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, newSafeError(ClassConfigurationInvalid, "config_file_unavailable", "read_configuration", "KuPilot could not read the configuration file; verify that the selected owner-only file exists.")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, newSafeError(ClassConfigurationInvalid, "config_file_unsafe", "read_configuration", "The configuration file must be a regular file and must not be a symbolic link.")
	}
	if info.Mode().Perm() != 0o600 {
		return nil, false, newSafeError(ClassConfigurationInvalid, "config_file_permissions", "read_configuration", "The configuration file must be accessible only by its owner; use mode 0600.")
	}
	if info.Size() > MaxConfigFileBytes {
		return nil, false, newSafeError(ClassConfigurationInvalid, "config_file_too_large", "read_configuration", "The configuration file exceeds the 64 KiB limit.")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, newSafeError(ClassConfigurationInvalid, "config_file_unavailable", "read_configuration", "KuPilot could not read the configuration file; verify that the selected owner-only file exists.")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Mode().Perm() != 0o600 || !os.SameFile(info, openedInfo) {
		return nil, false, newSafeError(ClassConfigurationInvalid, "config_file_unsafe", "read_configuration", "The configuration file changed or became unsafe while KuPilot was opening it.")
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxConfigFileBytes+1))
	if err != nil {
		return nil, false, newSafeError(ClassConfigurationInvalid, "config_file_unavailable", "read_configuration", "KuPilot could not read the configuration file.")
	}
	if len(content) > MaxConfigFileBytes {
		return nil, false, newSafeError(ClassConfigurationInvalid, "config_file_too_large", "read_configuration", "The configuration file exceeds the 64 KiB limit.")
	}
	if err := ctx.Err(); err != nil {
		return nil, false, newSafeError(ClassCancelled, "config_load_cancelled", "read_configuration", "Configuration loading was cancelled.")
	}
	return content, true, nil
}
