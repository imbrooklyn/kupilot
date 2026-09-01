package agent

import (
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestToolSelectionValidationRejectsUnboundProtocolAuthority(t *testing.T) {
	valid := ToolSelection{
		ID:            "call-1",
		Name:          domain.ToolNameGetResource,
		ArgumentsJSON: `{"purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"}}`,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("ToolSelection.Validate(valid) error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*ToolSelection)
	}{
		{name: "invalid identifier", mutate: func(value *ToolSelection) { value.ID = "call 1" }},
		{name: "unknown Tool", mutate: func(value *ToolSelection) { value.Name = "run_shell" }},
		{name: "duplicate JSON key", mutate: func(value *ToolSelection) {
			value.ArgumentsJSON = `{"purpose":"Inspect.","purpose":"Override.","resource":{"kind":"Pod","name":"sample-pod"}}`
		}},
		{name: "runtime authority", mutate: func(value *ToolSelection) {
			value.ArgumentsJSON = `{"context":"other","purpose":"Inspect.","resource":{"kind":"Pod","name":"sample-pod"}}`
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			selection := valid
			current.mutate(&selection)
			if err := selection.Validate(); err == nil {
				t.Fatal("ToolSelection.Validate() error = nil")
			}
		})
	}
}

func TestToolSpecificationValidationRejectsInvalidCodeOwnedFields(t *testing.T) {
	valid := ToolSpecification{
		Name:            domain.ToolNameGetResource,
		Version:         ToolCatalogVersion,
		Description:     "Read one bounded resource.",
		InputSchemaJSON: `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("ToolSpecification.Validate(valid) error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*ToolSpecification)
	}{
		{name: "unknown Tool", mutate: func(value *ToolSpecification) { value.Name = "run_shell" }},
		{name: "invalid version", mutate: func(value *ToolSpecification) { value.Version = "bad version" }},
		{name: "oversized description", mutate: func(value *ToolSpecification) {
			value.Description = strings.Repeat("x", maxToolDescriptionBytes+1)
		}},
		{name: "open schema", mutate: func(value *ToolSpecification) {
			value.InputSchemaJSON = `{"additionalProperties":true,"properties":{},"required":[],"type":"object"}`
		}},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			specification := valid
			current.mutate(&specification)
			if err := specification.Validate(); err == nil {
				t.Fatal("ToolSpecification.Validate() error = nil")
			}
		})
	}
}
