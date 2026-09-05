package config

import (
	"math"
	"net"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

// Validate checks and canonicalizes a non-sensitive configuration.
func Validate(config *Config) error {
	if config == nil {
		return newSafeError(ClassInternal, "config_internal", "validate_configuration", "Kupilot could not validate its configuration.")
	}
	if config.Version != CurrentVersion {
		return newSafeError(ClassConfigurationInvalid, "config_version_unsupported", "validate_configuration", "Configuration version is unsupported; use version 2. Version 1 files are accepted only through the documented compatibility migration.")
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
	if err := validateModelProfile(&config.Models.Agent, ModelRoleAgent, true); err != nil {
		return err
	}
	if reviewer := config.Models.ApprovalReviewer; reviewer != nil {
		if err := validateModelProfile(reviewer, ModelRoleApprovalReviewer, false); err != nil {
			return err
		}
		if reviewer.Name == config.Models.Agent.Name {
			return newSafeError(ClassConfigurationInvalid, "config_model_profile_name_duplicate", "validate_configuration", "Model profile names must be unique.")
		}
	}
	if config.Kubernetes.ExecCredentials != ExecCredentialsAllow && config.Kubernetes.ExecCredentials != ExecCredentialsDeny {
		return newSafeError(ClassConfigurationInvalid, "config_exec_credentials_invalid", "validate_configuration", "kubernetes.exec_credentials must be allow or deny.")
	}
	if config.Kubernetes.NamespaceAccess != NamespaceAccessCurrent && config.Kubernetes.NamespaceAccess != NamespaceAccessAll {
		return newSafeError(ClassConfigurationInvalid, "config_namespace_access_invalid", "validate_configuration", "kubernetes.namespace_access must be current or all.")
	}
	if err := validateResourcePolicies(config.Kubernetes.ResourcePolicies); err != nil {
		return err
	}
	if _, err := config.remoteDiagnosticsPolicyCatalogUnchecked(); err != nil {
		return newSafeError(ClassConfigurationInvalid, "config_remote_diagnostics_invalid", "validate_configuration", "Remote diagnostics must use exact no-shell argv, normalized file roots, digest-pinned images, same-Namespace Services, finite limits, and an explicit NetworkPolicy prerequisite.")
	}
	if _, _, err := config.localExecutionPolicyCatalogsUnchecked(); err != nil {
		return newSafeError(ClassConfigurationInvalid, "config_local_execution_invalid", "validate_configuration", "Local execution must use exact structured argv or a separate exact shell entry, absolute non-symlink paths, a minimal environment, explicit network identity, and finite limits.")
	}
	if err := validateDataSource(&config.Observability.Prometheus, domain.DataSourcePrometheus); err != nil {
		return err
	}
	if err := validateDataSource(&config.Observability.Loki, domain.DataSourceLoki); err != nil {
		return err
	}
	switch config.Logging.Level {
	case "info", "warn", "error":
	default:
		return newSafeError(ClassConfigurationInvalid, "config_log_level_invalid", "validate_configuration", "logging.level must be info, warn, or error.")
	}
	return nil
}

func remoteDiagnosticArgumentsAreNonSensitive(values []string) bool {
	if len(values) == 0 || len(values) > domain.MaxActionArguments {
		return false
	}
	for _, value := range values {
		key := strings.ToLower(strings.TrimLeft(value, "-"))
		if separator := strings.IndexAny(key, "=:"); separator >= 0 {
			key = key[:separator]
		}
		key = strings.ReplaceAll(key, "_", "-")
		if sensitiveRemoteDiagnosticArgumentKey(key) {
			return false
		}
	}
	joined := strings.Join(values, "\n")
	processed, err := security.NewRedactor().ProcessLines(joined, domain.MaxActionArgumentBytes+domain.MaxActionArguments-1)
	return err == nil && !processed.Truncated && processed.RedactionCount == 0 && processed.Value == joined
}

func sensitiveRemoteDiagnosticArgumentKey(key string) bool {
	switch key {
	case "api-key", "apikey", "access-token", "authorization", "bearer", "client-key", "client-secret", "credential", "credentials", "kubeconfig", "password", "passwd", "private-key", "refresh-token", "secret", "service-account-token", "token":
		return true
	}
	for _, suffix := range []string{"-credential", "-credentials", "-key-file", "-password", "-password-file", "-secret", "-secret-file", "-token", "-token-file"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return false
}

func validateDataSource(slot **DataSourceConfig, kind domain.DataSourceKind) error {
	if slot == nil || *slot == nil {
		return nil
	}
	source := *slot
	endpoint, origin, ok := canonicalEndpoint(source.Endpoint)
	if !ok || endpoint != origin {
		return newSafeError(ClassConfigurationInvalid, "config_data_source_endpoint_invalid", "validate_configuration", "An observability endpoint must be one canonical HTTPS origin, or HTTP only with an explicit loopback host; paths, user information, query, fragments, and redirects are not allowed.")
	}
	if source.RequestTimeoutSeconds < 1 || source.RequestTimeoutSeconds > MaxDataSourceTimeoutSeconds {
		return newSafeError(ClassConfigurationInvalid, "config_data_source_timeout_invalid", "validate_configuration", "An observability request_timeout_seconds value must be between 1 and 60.")
	}
	wantReference := DataSourceCredentialPrometheus
	if kind == domain.DataSourceLoki {
		wantReference = DataSourceCredentialLoki
	}
	if source.CredentialReference != DataSourceCredentialNone && source.CredentialReference != wantReference {
		return newSafeError(ClassConfigurationInvalid, "config_data_source_credential_invalid", "validate_configuration", "An observability credential_ref must be none or the fixed slot matching that source.")
	}
	if len(source.Queries) < 1 || len(source.Queries) > domain.MaxObservabilityQueryTemplates {
		return newSafeError(ClassConfigurationInvalid, "config_data_source_query_invalid", "validate_configuration", "An observability source must allow one or more code-owned query template IDs.")
	}
	queries := append([]string(nil), source.Queries...)
	sort.Strings(queries)
	for index, query := range queries {
		if !domain.ObservabilityQueryID(query).ValidFor(kind) || index > 0 && queries[index-1] == query {
			return newSafeError(ClassConfigurationInvalid, "config_data_source_query_invalid", "validate_configuration", "An observability source contains an unknown or duplicate query template ID.")
		}
	}
	source.Endpoint = endpoint
	source.Origin = origin
	source.Queries = queries
	return nil
}

func validateResourcePolicies(configured []KubernetesResourcePolicyConfig) error {
	if len(configured)+len(domain.BuiltInResourcePolicies()) > domain.MaxResourcePolicyEntries {
		return newSafeError(ClassConfigurationInvalid, "config_resource_policy_invalid", "validate_configuration", "Kubernetes resource policies exceed the fixed catalog-entry limit.")
	}
	entries := domain.BuiltInResourcePolicies()
	for _, value := range configured {
		policy, err := value.domainPolicy()
		if err != nil {
			return newSafeError(ClassConfigurationInvalid, "config_resource_policy_invalid", "validate_configuration", "Each Kubernetes resource policy must name one exact CRD, read verbs, scalar projection, query semantics, and finite limits.")
		}
		entries = append(entries, policy)
	}
	if _, err := domain.NewResourcePolicyCatalog(domain.ResourcePolicyVersion, entries); err != nil {
		return newSafeError(ClassConfigurationInvalid, "config_resource_policy_invalid", "validate_configuration", "Kubernetes resource policies must have unique local IDs and exact API identities.")
	}
	return nil
}

func validateModelProfile(profile *ModelProfileConfig, expectedRole ModelRole, allowUnconfigured bool) error {
	if profile == nil || profile.Role != expectedRole || !profile.Role.valid() ||
		!validModelProfileName(profile.Name) || !profile.CredentialReference.valid() ||
		expectedRole == ModelRoleAgent && (profile.InheritAgent || profile.CredentialReference != ModelCredentialAgent) ||
		expectedRole == ModelRoleApprovalReviewer && profile.CredentialReference != ModelCredentialAgent &&
			profile.CredentialReference != ModelCredentialApprovalReviewer {
		return newSafeError(ClassConfigurationInvalid, "config_model_profile_invalid", "validate_configuration", "Each model profile must have one unique name, its fixed role, and an admitted role-bound credential reference.")
	}
	if profile.ProviderKind != ProviderOpenAICompatible {
		return newSafeError(ClassConfigurationInvalid, "config_provider_invalid", "validate_configuration", "Each models profile provider_kind must be openai_compatible.")
	}
	if profile.ReasoningEffort != "" && profile.ReasoningEffort != ModelReasoningEffortNone {
		return newSafeError(ClassConfigurationInvalid, "config_reasoning_effort_invalid", "validate_configuration", "Model profile reasoning_effort must be omitted or set to none.")
	}
	if math.IsNaN(profile.Temperature) || math.IsInf(profile.Temperature, 0) || profile.Temperature < 0 || profile.Temperature > 0.2 {
		return newSafeError(ClassConfigurationInvalid, "config_temperature_invalid", "validate_configuration", "Model profile temperature must be between 0 and 0.2.")
	}
	if profile.MaxOutputTokens < 0 {
		return newSafeError(ClassConfigurationInvalid, "config_output_limit_invalid", "validate_configuration", "Model profile max_output_tokens must be omitted without endpoint evidence or set to a positive endpoint-supported value.")
	}
	if profile.RequestTimeoutSeconds < 1 || profile.RequestTimeoutSeconds > MaxModelRequestTimeoutSeconds {
		return newSafeError(ClassConfigurationInvalid, "config_model_timeout_invalid", "validate_configuration", "Model profile request_timeout_seconds must be between 1 and 300.")
	}
	if expectedRole == ModelRoleAgent && (!profile.Streaming || !profile.ToolCallingRequired) ||
		expectedRole == ModelRoleApprovalReviewer && (profile.Streaming || profile.ToolCallingRequired) {
		return newSafeError(ClassConfigurationInvalid, "config_model_capability_invalid", "validate_configuration", "The agent profile must stream with Tools; the approval_reviewer profile must be non-streaming and Tool-free.")
	}
	if profile.Endpoint != "" {
		endpoint, origin, ok := canonicalEndpoint(profile.Endpoint)
		if !ok {
			return newSafeError(ClassConfigurationInvalid, "config_model_endpoint_invalid", "validate_configuration", "Model endpoint must use HTTPS, or HTTP only with an explicit loopback host; user information, query, fragments, and ambiguous paths are not allowed.")
		}
		profile.Endpoint = endpoint
		profile.Origin = origin
	} else {
		profile.Origin = ""
	}
	if profile.Model != "" && !validModelIdentifier(profile.Model) {
		return newSafeError(ClassConfigurationInvalid, "config_model_identifier_invalid", "validate_configuration", "Model identifier must be bounded ASCII text without spaces or control characters.")
	}
	if !allowUnconfigured && (profile.Endpoint == "" || profile.Model == "") {
		return modelProfileRequiredError(expectedRole)
	}
	return nil
}

func validModelProfileName(value string) bool {
	if value == "" || len(value) > MaxModelProfileNameBytes {
		return false
	}
	for index, current := range []byte(value) {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current == '-' && index > 0 {
			continue
		}
		return false
	}
	return value[len(value)-1] != '-'
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
