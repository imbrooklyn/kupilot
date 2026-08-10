package tools

import (
	"context"

	"github.com/imbrooklyn/kupilot/internal/agent"
)

// GetPreviousPodLogsTool returns one bounded, sanitized previous container log
// tail and never falls back to the current instance.
type GetPreviousPodLogsTool struct {
	dependencies LogToolDependencies
}

var _ agent.Tool = (*GetPreviousPodLogsTool)(nil)

// NewGetPreviousPodLogsTool validates the immutable dependencies for
// get_previous_pod_logs.
func NewGetPreviousPodLogsTool(dependencies LogToolDependencies) (*GetPreviousPodLogsTool, error) {
	if dependencies.validate() != nil {
		return nil, ErrInvalidLogToolDependencies
	}
	return &GetPreviousPodLogsTool{dependencies: dependencies}, nil
}

// Execute reads only a verified previous container instance.
func (tool *GetPreviousPodLogsTool) Execute(ctx context.Context, call BoundToolCall) ToolResult {
	if tool == nil {
		return ToolResult{}
	}
	return executePodLogTool(ctx, call, true, tool.dependencies)
}
