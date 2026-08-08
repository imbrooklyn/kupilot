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
	ModelAPIKeyEnvironmentVariable = "KUPILOT_MODEL_API_KEY"
	MaxModelAPIKeyBytes            = 4096
	redactedSecret                 = "[REDACTED]"
)

var _ fmt.Formatter = SecretValue{}
var _ fmt.Stringer = SecretValue{}
var _ fmt.GoStringer = SecretValue{}
var _ encoding.TextMarshaler = SecretValue{}

// EnvironmentSecretSource reads the one admitted v0.1 model credential source.
type EnvironmentSecretSource struct {
	LookupEnv func(string) (string, bool)
	Unsetenv  func(string) error

	mu       sync.Mutex
	consumed bool
}

// Read copies one API key into an opaque wrapper and immediately removes the
// source environment entry before validation returns.
func (source *EnvironmentSecretSource) Read() (SecretValue, error) {
	if source == nil {
		return SecretValue{}, missingAPIKeyError()
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.consumed {
		return SecretValue{}, newSafeError(ClassConfigurationInvalid, "model_api_key_already_read", "read_model_api_key", "The model API key source is one-shot and has already been consumed.")
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
	value, found := lookup(ModelAPIKeyEnvironmentVariable)
	if !found {
		return SecretValue{}, missingAPIKeyError()
	}
	if err := unset(ModelAPIKeyEnvironmentVariable); err != nil {
		return SecretValue{}, newSafeError(ClassInternal, "model_api_key_unset_failed", "read_model_api_key", "KuPilot could not remove the model API key from its process environment; startup was stopped.")
	}
	if value == "" {
		return SecretValue{}, missingAPIKeyError()
	}
	if !validSecret(value) {
		return SecretValue{}, newSafeError(ClassConfigurationInvalid, "model_api_key_invalid", "read_model_api_key", "The model API key is invalid or exceeds the 4096-byte limit.")
	}
	return SecretValue{value: []byte(value)}, nil
}

func missingAPIKeyError() *SafeError {
	return newSafeError(ClassConfigurationInvalid, "model_api_key_missing", "read_model_api_key", "Model API key is required; set KUPILOT_MODEL_API_KEY before starting KuPilot.")
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
	prefix := ModelAPIKeyEnvironmentVariable + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
