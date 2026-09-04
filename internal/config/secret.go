package config

import (
	"encoding"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	// ModelAPIKeyEnvironmentVariable is the version 1 Agent alias retained for
	// deterministic compatibility. New files and setup copy use the role name.
	ModelAPIKeyEnvironmentVariable            = "KUPILOT_MODEL_API_KEY"
	AgentAPIKeyEnvironmentVariable            = "KUPILOT_AGENT_API_KEY"
	ApprovalReviewerAPIKeyEnvironmentVariable = "KUPILOT_APPROVAL_REVIEWER_API_KEY"
	MaxModelAPIKeyBytes                       = 4096
	redactedSecret                            = "[REDACTED]"
)

var _ fmt.Formatter = SecretValue{}
var _ fmt.Stringer = SecretValue{}
var _ fmt.GoStringer = SecretValue{}
var _ encoding.TextMarshaler = SecretValue{}

// EnvironmentSecretSource reads one fixed role's admitted environment
// aliases. Every present alias is removed before validation completes.
type EnvironmentSecretSource struct {
	LookupEnv func(string) (string, bool)
	Unsetenv  func(string) error
	Variables []string

	mu       sync.Mutex
	consumed bool
}

// Read copies one API key into an opaque wrapper and immediately removes the
// source environment entry before validation returns.
func (source *EnvironmentSecretSource) Read() (SecretValue, error) {
	secret, found, err := source.ReadOptional()
	if err != nil {
		return SecretValue{}, err
	}
	if !found {
		return SecretValue{}, missingAPIKeyError()
	}
	return secret, nil
}

// ReadOptional consumes the environment source once and distinguishes absence
// from an invalid present value.
func (source *EnvironmentSecretSource) ReadOptional() (SecretValue, bool, error) {
	if source == nil {
		return SecretValue{}, false, nil
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.consumed {
		return SecretValue{}, false, newSafeError(ClassConfigurationInvalid, "model_api_key_already_read", "read_model_api_key", "The model API key source is one-shot and has already been consumed.")
	}
	source.consumed = true
	lookup := source.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	unset := source.Unsetenv
	if unset == nil {
		unset = os.Unsetenv
	}
	variables := append([]string(nil), source.Variables...)
	if len(variables) == 0 {
		variables = []string{ModelAPIKeyEnvironmentVariable}
	}
	seen := make(map[string]struct{}, len(variables))
	values := make([]string, 0, 1)
	presentCount := 0
	unsetFailed := false
	for _, variable := range variables {
		if variable == "" {
			return SecretValue{}, false, newSafeError(ClassInternal, "model_api_key_source_invalid", "read_model_api_key", "The model API key source is invalid.")
		}
		if _, duplicate := seen[variable]; duplicate {
			return SecretValue{}, false, newSafeError(ClassInternal, "model_api_key_source_invalid", "read_model_api_key", "The model API key source is invalid.")
		}
		seen[variable] = struct{}{}
		value, found := lookup(variable)
		if !found {
			continue
		}
		presentCount++
		if err := unset(variable); err != nil {
			unsetFailed = true
		}
		values = append(values, value)
	}
	if unsetFailed {
		return SecretValue{}, false, newSafeError(ClassInternal, "model_api_key_unset_failed", "read_model_api_key", "Kupilot could not remove a model API key from its process environment; startup was stopped.")
	}
	if presentCount == 0 {
		return SecretValue{}, false, nil
	}
	if presentCount != 1 || len(values) != 1 {
		for index := range values {
			values[index] = ""
		}
		return SecretValue{}, false, newSafeError(ClassConfigurationInvalid, "model_api_key_ambiguous", "read_model_api_key", "Only one API key environment alias may be set for a model role.")
	}
	secret, err := NewSecretValue(values[0])
	values[0] = ""
	if err != nil {
		return SecretValue{}, false, err
	}
	return secret, true, nil
}

func missingAPIKeyError() *SafeError {
	return newSafeError(ClassConfigurationInvalid, "model_api_key_missing", "read_model_api_key", "Model API key is required; set KUPILOT_MODEL_API_KEY before starting Kupilot.")
}

func validSecret(value string) bool {
	if len(value) == 0 || len(value) > MaxModelAPIKeyBytes || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return false
		}
	}
	return true
}

// NewSecretValue copies a validated credential into the non-renderable runtime
// wrapper. Callers must discard their source buffer as soon as practical.
func NewSecretValue(value string) (SecretValue, error) {
	if !validSecret(value) {
		return SecretValue{}, newSafeError(ClassConfigurationInvalid, "model_api_key_invalid", "read_model_api_key", "The model API key is invalid or exceeds the 4096-byte limit.")
	}
	return SecretValue{value: []byte(value)}, nil
}

// SecretValue is a runtime-only model transport credential. Formatting is
// always redacted and serialization is explicitly rejected.
type SecretValue struct {
	value []byte
}

// IsSet reports whether the wrapper currently contains a value.
func (secret SecretValue) IsSet() bool {
	return len(secret.value) != 0
}

// Use provides the value only to an explicit transport callback. Callers must
// not retain, format, serialize, or log the supplied value. Callback failures
// must be mapped separately so arbitrary errors cannot escape this boundary.
func (secret *SecretValue) Use(callback func(string)) error {
	if secret == nil || len(secret.value) == 0 || callback == nil {
		return newSafeError(ClassInternal, "model_api_key_unavailable", "use_model_api_key", "The model credential is unavailable.")
	}
	callback(string(secret.value))
	return nil
}

// Destroy overwrites the wrapper's current byte buffer and releases it. Go does
// not guarantee erasure of prior runtime copies.
func (secret *SecretValue) Destroy() {
	if secret == nil {
		return
	}
	for index := range secret.value {
		secret.value[index] = 0
	}
	secret.value = nil
}

// Clone creates a separately owned opaque buffer for an explicitly shared
// credential reference. It never exposes the credential as a return value.
func (secret *SecretValue) Clone() (SecretValue, error) {
	if secret == nil || len(secret.value) == 0 {
		return SecretValue{}, newSafeError(ClassInternal, "model_api_key_unavailable", "clone_model_api_key", "The model credential is unavailable.")
	}
	return SecretValue{value: append([]byte(nil), secret.value...)}, nil
}

func (SecretValue) String() string {
	return redactedSecret
}

func (SecretValue) GoString() string {
	return "config.SecretValue(" + redactedSecret + ")"
}

func (SecretValue) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, redactedSecret)
}

func (SecretValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("model credentials cannot be serialized")
}

func (SecretValue) MarshalText() ([]byte, error) {
	return nil, errors.New("model credentials cannot be serialized")
}

func (SecretValue) MarshalYAML() (any, error) {
	return nil, errors.New("model credentials cannot be serialized")
}

// FilterChildEnvironment returns a fresh child environment with every model
// API-key entry removed. It is a second defense after source unsetting.
func FilterChildEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	prefixes := [...]string{
		ModelAPIKeyEnvironmentVariable + "=",
		AgentAPIKeyEnvironmentVariable + "=",
		ApprovalReviewerAPIKeyEnvironmentVariable + "=",
	}
	for _, entry := range environment {
		blocked := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry, prefix) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
