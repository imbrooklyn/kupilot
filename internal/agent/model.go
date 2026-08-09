// Package agent owns neutral single-Agent policy and its consumer ports.
package agent

import (
	"context"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// ModelStreamConsumer receives ordered non-provider stream events synchronously.
// It must return promptly; cancellation is expressed through the owning Context.
type ModelStreamConsumer func(domain.ModelStreamEvent)

// Model is the Agent-owned model consumer port. Stream blocks for one request,
// honors ctx, and has exactly one terminal outcome: either one final Completed
// event followed by nil, or one classified ModelError return with no Completed
// event. Implementations must not return raw transport or framework errors.
type Model interface {
	Stream(ctx context.Context, request domain.ModelRequest, consume ModelStreamConsumer) *domain.ModelError
}
