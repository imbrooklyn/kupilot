package application

import (
	"context"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

// RestartDeploymentProposalPreparer performs the one trusted fresh read needed
// to turn a model suggestion into a digest-bound local approval request. It
// exposes no write method and accepts no patch, annotation, timestamp, UID,
// template fingerprint, generation, or resource version from model output.
type RestartDeploymentProposalPreparer interface {
	PrepareRestartDeploymentProposal(
		context.Context,
		domain.ScopeSnapshot,
		domain.ResourceRef,
		string,
	) (domain.ActionTarget, error)
}
