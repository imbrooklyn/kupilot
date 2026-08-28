package domain

import "testing"

func TestModelToolSpecificationRequiresStrictObjectSchema(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		valid  bool
	}{
		{
			name:   "nullable field is required",
			schema: `{"additionalProperties":false,"properties":{"optional":{"type":["string","null"]}},"required":["optional"],"type":"object"}`,
			valid:  true,
		},
		{
			name:   "empty strict object",
			schema: `{"additionalProperties":false,"properties":{},"required":[],"type":"object"}`,
			valid:  true,
		},
		{
			name:   "missing root required",
			schema: `{"additionalProperties":false,"properties":{"value":{"type":"string"}},"type":"object"}`,
		},
		{
			name:   "nullable root object",
			schema: `{"additionalProperties":false,"properties":{},"required":[],"type":["object","null"]}`,
		},
		{
			name:   "incomplete root required",
			schema: `{"additionalProperties":false,"properties":{"optional":{"type":["string","null"]},"value":{"type":"string"}},"required":["value"],"type":"object"}`,
		},
		{
			name:   "missing nested required",
			schema: `{"additionalProperties":false,"properties":{"resource":{"additionalProperties":false,"properties":{"name":{"type":"string"}},"type":"object"}},"required":["resource"],"type":"object"}`,
		},
		{
			name:   "duplicate required field",
			schema: `{"additionalProperties":false,"properties":{"value":{"type":"string"}},"required":["value","value"],"type":"object"}`,
		},
		{
			name:   "unsupported unique items",
			schema: `{"additionalProperties":false,"properties":{"values":{"items":{"type":"string"},"type":"array","uniqueItems":true}},"required":["values"],"type":"object"}`,
		},
	}

	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			specification := ModelToolSpecification{
				Name:            ToolNameGetResource,
				Version:         "test-v1",
				Description:     "Exercise strict model Tool schema validation.",
				InputSchemaJSON: current.schema,
			}
			if got := specification.Validate() == nil; got != current.valid {
				t.Fatalf("ModelToolSpecification.Validate() success = %t, want %t", got, current.valid)
			}
		})
	}
}
