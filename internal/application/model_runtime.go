package application

import (
	"context"
	"encoding"
	"errors"
	"fmt"
	"io"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/agent"
)

const (
	MaxModelSetupEndpointBytes = 2048
	MaxModelSetupNameBytes     = 128
	MaxModelSetupSecretBytes   = 4096
	redactedModelSecret        = "[REDACTED]"
)

var (
	ErrModelUnconfigured = errors.New("model configuration is required")
	ErrModelSetupInvalid = errors.New("model setup is invalid")
	ErrModelSetupFailed  = errors.New("model setup failed safely")
)

var _ fmt.Formatter = (*ModelSetupSecret)(nil)
var _ fmt.Stringer = (*ModelSetupSecret)(nil)
var _ fmt.GoStringer = (*ModelSetupSecret)(nil)
var _ encoding.TextMarshaler = (*ModelSetupSecret)(nil)

// ModelSetupSecret is the one transient TUI-to-Application credential path.
// It is non-renderable, non-serializable, and explicitly destroyed after use.
type ModelSetupSecret struct {
	mu    sync.Mutex
	value []byte
}

// NewModelSetupSecret copies one bounded credential from the masked composer.
func NewModelSetupSecret(value string) (*ModelSetupSecret, error) {
	if !validModelSetupSecret(value) {
		return nil, ErrModelSetupInvalid
	}
	return &ModelSetupSecret{value: []byte(value)}, nil
}

// IsSet reports whether the transient wrapper still owns a value.
func (secret *ModelSetupSecret) IsSet() bool {
	if secret == nil {
		return false
	}
	secret.mu.Lock()
	defer secret.mu.Unlock()
	return len(secret.value) != 0
}

// Use exposes the credential only to a narrow construction or persistence
// callback. The callback must not retain, render, serialize, or log the value.
func (secret *ModelSetupSecret) Use(callback func(string)) error {
	if secret == nil || callback == nil {
		return ErrModelSetupInvalid
	}
	secret.mu.Lock()
	defer secret.mu.Unlock()
	if len(secret.value) == 0 {
		return ErrModelSetupInvalid
	}
	callback(string(secret.value))
	return nil
}

// Destroy overwrites the currently owned byte buffer. Go does not guarantee
// erasure of earlier runtime copies.
func (secret *ModelSetupSecret) Destroy() {
	if secret == nil {
		return
	}
	secret.mu.Lock()
	for index := range secret.value {
		secret.value[index] = 0
	}
	secret.value = nil
	secret.mu.Unlock()
}

func (*ModelSetupSecret) String() string { return redactedModelSecret }
func (*ModelSetupSecret) GoString() string {
	return "application.ModelSetupSecret(" + redactedModelSecret + ")"
}
func (*ModelSetupSecret) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, redactedModelSecret)
}
func (*ModelSetupSecret) MarshalJSON() ([]byte, error) {
	return nil, errors.New("model credentials cannot be serialized")
}
func (*ModelSetupSecret) MarshalText() ([]byte, error) {
	return nil, errors.New("model credentials cannot be serialized")
}
func (*ModelSetupSecret) MarshalYAML() (any, error) {
	return nil, errors.New("model credentials cannot be serialized")
}

func validModelSetupSecret(value string) bool {
	if value == "" || len(value) > MaxModelSetupSecretBytes || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return false
		}
	}
	return true
}

// ModelSetupRequest is the complete typed interactive configuration intent.
type ModelSetupRequest struct {
	RequestID uint64
	Endpoint  string
	Model     string
	Persist   bool
	Secret    *ModelSetupSecret
}

// Validate checks only transport-neutral shape and limits. The concrete model
// adapter remains responsible for endpoint canonicalization and capabilities.
func (request ModelSetupRequest) Validate() error {
	if request.RequestID == 0 || request.Secret == nil || !request.Secret.IsSet() ||
		!validModelSetupText(request.Endpoint, MaxModelSetupEndpointBytes) ||
		!validModelSetupText(request.Model, MaxModelSetupNameBytes) {
		return ErrModelSetupInvalid
	}
	return nil
}

func validModelSetupText(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	for _, current := range value {
		if unicode.IsControl(current) || unicode.IsSpace(current) {
			return false
		}
	}
	return true
}

// ModelSetupResult is the bounded non-sensitive replacement projection.
type ModelSetupResult struct {
	RequestID uint64
	Model     string
	Origin    string
	Persisted bool
}

func (result ModelSetupResult) Validate() error {
	if result.RequestID == 0 || !validModelSetupText(result.Model, MaxModelSetupNameBytes) || !validPrivacyOrigin(result.Origin) {
		return ErrModelSetupInvalid
	}
	return nil
}

// ModelRuntime owns one constructed Agent runner and all of its model resources.
type ModelRuntime interface {
	agent.AgentRunner
	Close()
	ModelName() string
	Origin() string
}

// ModelRuntimeFactory constructs exactly one replacement runtime.
type ModelRuntimeFactory interface {
	BuildModelRuntime(context.Context, ModelSetupRequest) (ModelRuntime, error)
}

// ModelProfileWriter publishes the explicit local persistence choice.
type ModelProfileWriter interface {
	SaveModelProfile(context.Context, ModelSetupRequest) error
}
