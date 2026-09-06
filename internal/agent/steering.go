package agent

import (
	"context"
	"errors"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrInvalidSteeringContract = errors.New("Agent steering contract is invalid")

// SteerBoundary identifies one exact Eino model-invocation boundary. It does
// not grant authority to change the run, scope, or policy selected by
// Application.
type SteerBoundary struct {
	RunID            domain.AgentRunID
	SessionID        domain.SessionID
	ScopeGeneration  int64
	PolicyGeneration domain.PolicyGeneration
}

// Validate rejects stale or partially bound boundary data.
func (boundary SteerBoundary) Validate() error {
	if !boundary.RunID.Valid() || !boundary.SessionID.Valid() || boundary.ScopeGeneration < 1 ||
		!boundary.PolicyGeneration.Valid() {
		return ErrInvalidSteeringContract
	}
	return nil
}

// SteerClaim is one Application-owned pending input atomically claimed for a
// single model invocation. Content has already passed the local safety
// pipeline and remains bounded by the ordinary model-input ceiling.
type SteerClaim struct {
	ItemID           domain.MessageID
	RunID            domain.AgentRunID
	SessionID        domain.SessionID
	ScopeGeneration  int64
	PolicyGeneration domain.PolicyGeneration
	RunSequence      int
	Content          string
	ContentHash      string
}

// Validate checks the immutable claim without consulting live authority.
func (claim SteerClaim) Validate() error {
	if !claim.ItemID.Valid() || !claim.RunID.Valid() || !claim.SessionID.Valid() ||
		claim.ScopeGeneration < 1 || !claim.PolicyGeneration.Valid() ||
		claim.RunSequence < 1 || claim.RunSequence > domain.MaxCommittedSteerInputs ||
		!domain.ValidModelText(claim.Content, domain.MaxModelInputMessageBytes, false) ||
		claim.ContentHash != domain.MessageContentHash(claim.Content) {
		return ErrInvalidSteeringContract
	}
	return nil
}

// SteerResolution is reported only when a claim cannot remain in its normal
// committing or committed state. Application decides whether it is editable;
// the adapter cannot enqueue or retarget it.
type SteerResolution string

const (
	SteerResolutionRejected  SteerResolution = "rejected"
	SteerResolutionRecovered SteerResolution = "recovered"
	SteerResolutionUnknown   SteerResolution = "unknown"
)

// Valid reports whether the resolution has fixed product semantics.
func (resolution SteerResolution) Valid() bool {
	return resolution == SteerResolutionRejected || resolution == SteerResolutionRecovered ||
		resolution == SteerResolutionUnknown
}

// RunSteeringBridge is the narrow run-local input port consumed by the Eino
// adapter. Application remains the only owner of queue order, persistence,
// commit state, and generation checks.
type RunSteeringBridge interface {
	ClaimSteer(context.Context, SteerBoundary) (SteerClaim, bool, error)
	CommitSteer(context.Context, SteerClaim) error
	ResolveSteer(context.Context, SteerClaim, SteerResolution)
}
