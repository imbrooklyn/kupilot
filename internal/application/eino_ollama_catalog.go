package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type nativeCatalogContextKey struct{}

// nativeCatalogModel carries only the already validated code-owned catalog to
// the current transport call. The native Eino component still owns the call.
type nativeCatalogModel struct {
	base    einomodel.ToolCallingChatModel
	catalog []agent.ToolSpecification
}

func bindNativeCatalog(model einomodel.ToolCallingChatModel, tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	if validateAnyBoundToolInfos(tools) != nil {
		return nil, agent.ErrToolPolicyDenied
	}
	bound, err := model.WithTools(tools)
	if err != nil {
		return nil, fmt.Errorf("bind native Tool catalog: %w", err)
	}
	catalog := agent.ToolSpecifications()
	if len(tools) != len(catalog) {
		catalog = toolSpecificationsForMode(agent.RunModePlanOnly)
	}
	return &nativeCatalogModel{base: bound, catalog: catalog}, nil
}

func (model *nativeCatalogModel) WithTools(tools []*schema.ToolInfo) (einomodel.ToolCallingChatModel, error) {
	return bindNativeCatalog(model.base, tools)
}

func (model *nativeCatalogModel) Generate(ctx context.Context, messages []*schema.Message, options ...einomodel.Option) (*schema.Message, error) {
	return model.base.Generate(context.WithValue(ctx, nativeCatalogContextKey{}, model.catalog), messages, options...)
}

func (model *nativeCatalogModel) Stream(ctx context.Context, messages []*schema.Message, options ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	return model.base.Stream(context.WithValue(ctx, nativeCatalogContextKey{}, model.catalog), messages, options...)
}

type nativeToolSchemaSpan struct {
	start, end int
	schema     string
}

// nativeToolSchemaBytes restores only parameters slots, never Tool identities,
// arguments, messages, or settings. A nil result means no replacement is needed.
func nativeToolSchemaBytes(payload []byte, catalog []agent.ToolSpecification, maximum int) ([]byte, error) {
	start, end, err := nativeJSONMember(payload, "tools")
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errTransportRequestInvalid, err)
	}
	if start < 0 {
		if len(catalog) != 0 {
			return nil, errTransportRequestInvalid
		}
		return nil, nil
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(payload[start:end], &tools); err != nil {
		return nil, fmt.Errorf("%w: %w", errTransportRequestInvalid, err)
	}
	if tools == nil || len(tools) != len(catalog) {
		return nil, errTransportRequestInvalid
	}
	if len(tools) == 0 {
		return nil, nil
	}
	spans := make([]nativeToolSchemaSpan, 0, len(tools))
	decoder := json.NewDecoder(bytes.NewReader(payload[start:end]))
	_, _ = decoder.Token() // The complete array was validated above.
	size := len(payload)
	for index := range tools {
		var raw json.RawMessage
		_ = decoder.Decode(&raw) // Exactly these elements were validated above.
		toolStart := start + int(decoder.InputOffset()) - len(raw)
		span, err := nativeToolParametersSpan(raw, catalog[index])
		if err != nil {
			return nil, fmt.Errorf("%w: %w", errTransportRequestInvalid, err)
		}
		span.start += toolStart
		span.end += toolStart
		spans = append(spans, span)
		size += len(span.schema) - (span.end - span.start)
	}
	if size > maximum || size > domain.MaxModelRequestBytes {
		return nil, errModelRequestLimitReached
	}
	corrected := make([]byte, 0, size)
	previous := 0
	for _, span := range spans {
		corrected = append(corrected, payload[previous:span.start]...)
		corrected = append(corrected, span.schema...)
		previous = span.end
	}
	return append(corrected, payload[previous:]...), nil
}

func nativeToolParametersSpan(payload []byte, specification agent.ToolSpecification) (nativeToolSchemaSpan, error) {
	var tool struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&tool); err != nil {
		return nativeToolSchemaSpan{}, err
	}
	if tool.Type != "function" || tool.Function.Name != string(specification.Name) || tool.Function.Description != specification.Description ||
		len(tool.Function.Parameters) == 0 || tool.Function.Parameters[0] != '{' {
		return nativeToolSchemaSpan{}, errTransportRequestInvalid
	}
	if _, _, err := nativeJSONMember(payload, "type"); err != nil {
		return nativeToolSchemaSpan{}, err
	}
	functionStart, functionEnd, err := nativeJSONMember(payload, "function")
	if err != nil {
		return nativeToolSchemaSpan{}, err
	}
	function := payload[functionStart:functionEnd]
	for _, key := range []string{"name", "description"} {
		if _, _, err := nativeJSONMember(function, key); err != nil {
			return nativeToolSchemaSpan{}, err
		}
	}
	start, end, err := nativeJSONMember(function, "parameters")
	if err != nil {
		return nativeToolSchemaSpan{}, err
	}
	return nativeToolSchemaSpan{start: functionStart + start, end: functionStart + end, schema: specification.InputSchemaJSON}, nil
}
