package config

import (
	"math"
	"net"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Validate checks and canonicalizes a non-sensitive configuration.
func Validate(config *Config) error {
	if config == nil {
		return newSafeError(ClassInternal, "config_internal", "validate_configuration", "Kupilot could not validate its configuration.")
	}
	if config.Version != CurrentVersion {
		return newSafeError(ClassConfigurationInvalid, "config_version_unsupported", "validate_configuration", "Configuration version is unsupported; use version 1.")
	}
	if !validDisplayName(config.Context, MaxContextBytes) {
		return newSafeError(ClassConfigurationInvalid, "config_context_invalid", "validate_configuration", "Kubernetes Context must be valid, bounded text without control characters.")
	}
	if config.Namespace != "" && !validNamespace(config.Namespace) {
		return newSafeError(ClassConfigurationInvalid, "config_namespace_invalid", "validate_configuration", "Kubernetes working Namespace must be a single DNS label; use kubernetes.namespace_access to control cross-Namespace reads.")
	}
	switch config.Runtime.BudgetProfile {
	case BudgetProfileCompact, BudgetProfileBalanced, BudgetProfileExtended:
	default:
		return newSafeError(ClassConfigurationInvalid, "config_budget_profile_invalid", "validate_configuration", "runtime.budget_profile must be compact, balanced, or extended.")
	}
	if config.Model.ProviderKind != ProviderOpenAICompatible {
		return newSafeError(ClassConfigurationInvalid, "config_provider_invalid", "validate_configuration", "model.provider_kind must be openai_compatible.")
	}
	if config.Model.ReasoningEffort != "" && config.Model.ReasoningEffort != ModelReasoningEffortNone {
		return newSafeError(ClassConfigurationInvalid, "config_reasoning_effort_invalid", "validate_configuration", "model.reasoning_effort must be omitted or set to none.")
	}
	if math.IsNaN(config.Model.Temperature) || math.IsInf(config.Model.Temperature, 0) || config.Model.Temperature < 0 || config.Model.Temperature > 0.2 {
		return newSafeError(ClassConfigurationInvalid, "config_temperature_invalid", "validate_configuration", "model.temperature must be between 0 and 0.2.")
	}
	if config.Model.MaxOutputTokens < 1 || config.Model.MaxOutputTokens > MaxModelOutputTokens {
		return newSafeError(ClassConfigurationInvalid, "config_output_limit_invalid", "validate_configuration", "model.max_output_tokens must be between 1 and 8192.")
	}
	if config.Model.RequestTimeoutSeconds < 1 || config.Model.RequestTimeoutSeconds > MaxModelRequestTimeoutSeconds {
		return newSafeError(ClassConfigurationInvalid, "config_model_timeout_invalid", "validate_configuration", "model.request_timeout_seconds must be between 1 and 300.")
	}
	if !config.Model.Streaming || !config.Model.ToolCallingRequired {
		return newSafeError(ClassConfigurationInvalid, "config_model_capability_invalid", "validate_configuration", "Model streaming and structured Tool calling must remain enabled.")
	}
	if config.Model.Endpoint != "" {
		endpoint, origin, ok := canonicalEndpoint(config.Model.Endpoint)
		if !ok {
			return newSafeError(ClassConfigurationInvalid, "config_model_endpoint_invalid", "validate_configuration", "Model endpoint must use HTTPS, or HTTP only with an explicit loopback host; user information, query, fragments, and ambiguous paths are not allowed.")
		}
		config.Model.Endpoint = endpoint
		config.Model.Origin = origin
	} else {
		config.Model.Origin = ""
	}
	if config.Model.Model != "" && !validModelIdentifier(config.Model.Model) {
		return newSafeError(ClassConfigurationInvalid, "config_model_identifier_invalid", "validate_configuration", "Model identifier must be bounded ASCII text without spaces or control characters.")
	}
	if config.Kubernetes.ExecCredentials != ExecCredentialsAllow && config.Kubernetes.ExecCredentials != ExecCredentialsDeny {
		return newSafeError(ClassConfigurationInvalid, "config_exec_credentials_invalid", "validate_configuration", "kubernetes.exec_credentials must be allow or deny.")
	}
	if config.Kubernetes.NamespaceAccess != NamespaceAccessCurrent && config.Kubernetes.NamespaceAccess != NamespaceAccessAll {
		return newSafeError(ClassConfigurationInvalid, "config_namespace_access_invalid", "validate_configuration", "kubernetes.namespace_access must be current or all.")
	}
	switch config.Logging.Level {
	case "info", "warn", "error":
	default:
		return newSafeError(ClassConfigurationInvalid, "config_log_level_invalid", "validate_configuration", "logging.level must be info, warn, or error.")
	}
	return nil
}

// RedirectAllowed reports whether a redirect target remains on the exact
// canonical origin and contains no endpoint-confusion fields. Model transports
// must reject redirects when this returns false.
func RedirectAllowed(origin, target string) bool {
	_, canonicalOrigin, ok := canonicalEndpoint(target)
	if !ok {
		return false
	}
	return canonicalOrigin == origin
}

func canonicalEndpoint(value string) (string, string, bool) {
	if value == "" || len(value) > 2048 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return "", "", false
	}
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Opaque != "" || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", "", false
	}
	hostname := strings.ToLower(parsed.Hostname())
	if !validEndpointHost(hostname) || strings.Contains(parsed.Host, "%") {
		return "", "", false
	}
	port := parsed.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", "", false
		}
	}
	if scheme == "http" && !isExplicitLoopback(hostname) {
		return "", "", false
	}
	if parsed.RawPath != "" || parsed.EscapedPath() != parsed.Path || strings.Contains(parsed.Path, "//") || hasTraversalSegment(parsed.Path) {
		return "", "", false
	}
	for _, current := range parsed.Path {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return "", "", false
		}
	}

	if scheme == "https" && port == "443" || scheme == "http" && port == "80" {
		port = ""
	}
	host := hostname
	if net.ParseIP(hostname) != nil && strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	}
	origin := scheme + "://" + host
	cleanPath := parsed.Path
	if cleanPath == "/" {
		cleanPath = ""
	} else {
		cleanPath = strings.TrimRight(cleanPath, "/")
	}
	return origin + cleanPath, origin, true
}

func validEndpointHost(value string) bool {
	if value == "" || strings.HasSuffix(value, ".") {
		return false
	}
	if ip := net.ParseIP(value); ip != nil {
		return true
	}
	if len(value) > 253 {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, current := range label {
			if current < 'a' || current > 'z' {
				if current < '0' || current > '9' {
					if current != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}

func isExplicitLoopback(hostname string) bool {
	if hostname == "localhost" {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func hasTraversalSegment(value string) bool {
	if value == "" {
		return false
	}
	if path.Clean(value) != value && path.Clean(value)+"/" != value {
		return true
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func validDisplayName(value string, limit int) bool {
	if value == "" {
		return true
	}
	if len(value) > limit || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || isBidirectionalControl(current) {
			return false
		}
	}
	return true
}

func validNamespace(value string) bool {
	if len(value) == 0 || len(value) > MaxNamespaceBytes || !isLowerAlphanumeric(value[0]) {
		return false
	}
	last := value[len(value)-1]
	if !isLowerAlphanumeric(last) {
		return false
	}
	for _, current := range value {
		if current < 'a' || current > 'z' {
			if current < '0' || current > '9' {
				if current != '-' {
					return false
				}
			}
		}
	}
	return true
}

func isLowerAlphanumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func validModelIdentifier(value string) bool {
	if value == "" || len(value) > MaxModelIdentifierBytes {
		return false
	}
	for _, current := range value {
		if current >= 'a' && current <= 'z' || current >= 'A' && current <= 'Z' || current >= '0' && current <= '9' {
			continue
		}
		switch current {
		case '.', '_', '-', '/', ':':
		default:
			return false
		}
	}
	return true
}

func isBidirectionalControl(value rune) bool {
	return value == '\u061c' || value == '\u200e' || value == '\u200f' ||
		value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069'
}
