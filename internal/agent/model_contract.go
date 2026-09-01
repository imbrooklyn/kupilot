package agent

import (
	"errors"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const (
	maxToolDescriptionBytes = 1024
	maxToolVersionBytes     = 128
)

var (
	// ErrInvalidToolSelection reports an invalid complete model Tool selection
	// without echoing provider-controlled content.
	ErrInvalidToolSelection = errors.New("model Tool selection is invalid")
	// ErrInvalidToolSpecification reports an invalid code-owned Tool definition.
	ErrInvalidToolSpecification = errors.New("model Tool specification is invalid")
)

// ToolSelection is one complete structured selection handed from the Eino
// boundary to the Agent-owned strict Tool binder. It carries no scope, limits,
// endpoint, credential, or execution authority.
type ToolSelection struct {
	ID            string
	Name          domain.ToolName
	ArgumentsJSON string
}

// Validate checks the bounded provider selection before strict catalog binding.
func (selection ToolSelection) Validate() error {
	if !domain.ValidModelToken(selection.ID, domain.MaxModelToolCallIDBytes) ||
		!selection.Name.Valid() ||
		!domain.ValidModelToolArguments(selection.ArgumentsJSON) {
		return ErrInvalidToolSelection
	}
	return nil
}

// ToolSpecification is one fixed, versioned, strict Agent capability definition.
type ToolSpecification struct {
	Name            domain.ToolName
	Version         string
	Description     string
	InputSchemaJSON string
}

// Validate checks one code-owned closed object schema.
func (specification ToolSpecification) Validate() error {
	if !specification.Name.Valid() ||
		!domain.ValidModelToken(specification.Version, maxToolVersionBytes) ||
		!domain.ValidModelText(specification.Description, maxToolDescriptionBytes, false) ||
		!domain.ValidStrictModelToolSchema(specification.InputSchemaJSON) {
		return ErrInvalidToolSpecification
	}
	return nil
}
