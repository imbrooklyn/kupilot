package application

import (
	"errors"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const RunPreflightSchemaVersion = "kupilot.run-preflight/v1"

var ErrRunPreflightRejected = errors.New("Application run preflight rejected the model invocation")

// RunInvocationPreflight is a content-free, Application-owned proof checked
// synchronously before each model transport call.
type RunInvocationPreflight struct {
	SchemaVersion       string
	SessionID           domain.SessionID
	RunID               domain.AgentRunID
	ScopeGeneration     int64
	PolicyGeneration    domain.PolicyGeneration
	PermissionProfile   domain.PermissionProfile
	ModelRole           domain.ModelRole
	ModelProfile        string
	OriginHash          string
	ConsentRevision     uint64
	ContextMode         domain.PrivacyMode
	CoverageMessages    int
	RecentTailMessages  int
	SummaryPresent      bool
	ToolCatalogVersion  string
	StorageHealthy      bool
	SinkAvailable       bool
	RunMode             agent.RunMode
	RecoveryClear       bool
	BudgetProfile       agent.BudgetProfile
	CurrentInputCount   int
	CurrentInputDigest  string
	InvocationKind      agent.ModelCallKind
	ReservedInputBytes  int
	ReservedOutputBytes int
	ReservedStreamBytes int
	ReservedCostUnits   int
}

func (projection RunInvocationPreflight) validate() error {
	if projection.SchemaVersion != RunPreflightSchemaVersion || !projection.SessionID.Valid() || !projection.RunID.Valid() ||
		projection.ScopeGeneration < 1 || !projection.PolicyGeneration.Valid() || !projection.PermissionProfile.Valid() ||
		projection.ModelRole != domain.ModelRoleAgent || !domain.ValidModelToken(projection.ModelProfile, 128) ||
		!validPrivacyDigest(projection.OriginHash) || projection.ConsentRevision == 0 ||
		(projection.ContextMode != domain.PrivacyModeStandard && projection.ContextMode != domain.PrivacyModeMinimal) ||
		projection.CoverageMessages < 0 || projection.CoverageMessages > domain.MaxSessionContextMessages ||
		projection.RecentTailMessages < 0 || projection.RecentTailMessages > projection.CoverageMessages ||
		projection.ToolCatalogVersion != agent.ToolCatalogVersion || !projection.StorageHealthy || !projection.SinkAvailable ||
		!projection.RunMode.Valid() || !projection.RecoveryClear || !projection.BudgetProfile.Valid() ||
		projection.CurrentInputCount < 1 || projection.CurrentInputCount > 1+domain.MaxCommittedSteerInputs ||
		!validPrivacyDigest(projection.CurrentInputDigest) ||
		(projection.InvocationKind != agent.ModelCallAgent && projection.InvocationKind != agent.ModelCallSummary) ||
		projection.ReservedInputBytes < 1 || projection.ReservedOutputBytes < 1 ||
		projection.ReservedStreamBytes < 0 || projection.ReservedCostUnits < 1 {
		return ErrRunPreflightRejected
	}
	if projection.ContextMode == domain.PrivacyModeMinimal && projection.SummaryPresent {
		return ErrRunPreflightRejected
	}
	return nil
}

func (coordinator *Coordinator) validateRunInvocationPreflightLocked(state *activeRun, event agent.RunEvent) error {
	if coordinator == nil || state == nil || event.ModelPreflight == nil || coordinator.active != state ||
		state.terminal || state.persistenceBad || coordinator.persistenceDegraded ||
		coordinator.currentSession != nil && coordinator.currentSession.ID != state.run.SessionID || state.run.Status != domain.AgentRunStatusRunning ||
		state.input.Validate() != nil || state.run.ID != state.input.RunID() || state.run.SessionID != state.input.SessionID() ||
		state.run.Scope != state.input.Scope().Snapshot() || state.run.PromptVersion != state.input.PromptVersion() ||
		state.run.ToolCatalogVersion != state.input.CatalogVersion() || coordinator.uiEvents == nil {
		return ErrRunPreflightRejected
	}
	privacy := coordinator.privacy.Snapshot()
	if !privacy.Loaded || !privacy.Accepted || privacy.Revision != state.consentRevision ||
		privacy.Role != domain.ModelRoleAgent || privacy.OriginHash != state.modelOriginHash {
		return ErrRunPreflightRejected
	}
	manifest, err := agent.BuildRunInputManifest(state.input, state.committedInputs)
	if err != nil || manifest.Count != event.ModelPreflight.CurrentInputCount || manifest.Digest != event.ModelPreflight.CurrentInputDigest {
		return ErrRunPreflightRejected
	}
	conversation := state.input.Conversation()
	limits := state.input.BudgetLimits()
	reservedInputMaximum := limits.ModelRequestBytes
	reservedOutputMaximum := domain.MaxModelMessageBytes
	reservedStreamMaximum := limits.ModelStreamBytes
	reservedCostMaximum := limits.ModelCostUnits
	if event.Kind == agent.RunEventSummaryStarted {
		reservedInputMaximum = limits.SummaryRequestBytes
		reservedOutputMaximum = limits.SummaryOutputBytes
		reservedStreamMaximum = 0
		reservedCostMaximum = limits.SummaryCostUnits
	}
	preflight := event.ModelPreflight
	projection := RunInvocationPreflight{
		SchemaVersion: RunPreflightSchemaVersion, SessionID: state.run.SessionID, RunID: state.run.ID,
		ScopeGeneration: state.run.Scope.Generation, PolicyGeneration: state.input.PolicyGeneration(),
		PermissionProfile: state.permissionProfile, ModelRole: domain.ModelRoleAgent,
		ModelProfile: state.modelProfile, OriginHash: state.modelOriginHash, ConsentRevision: state.consentRevision,
		ContextMode: state.contextMode, CoverageMessages: len(conversation.Coverage()),
		RecentTailMessages: len(conversation.Turns()), SummaryPresent: conversation.Summary() != nil,
		ToolCatalogVersion: state.input.CatalogVersion(), StorageHealthy: true, SinkAvailable: true,
		RunMode: state.input.Mode(), RecoveryClear: true, BudgetProfile: limits.Profile,
		CurrentInputCount: manifest.Count, CurrentInputDigest: manifest.Digest, InvocationKind: preflight.Kind,
		ReservedInputBytes: preflight.ReservedRequestBytes, ReservedOutputBytes: preflight.ReservedOutputBytes,
		ReservedStreamBytes: preflight.ReservedStreamBytes, ReservedCostUnits: preflight.ReservedCostUnits,
	}
	if projection.validate() != nil || preflight.ReservedRequestBytes > reservedInputMaximum ||
		preflight.ReservedOutputBytes > reservedOutputMaximum || preflight.ReservedStreamBytes > reservedStreamMaximum ||
		preflight.ReservedCostUnits > reservedCostMaximum {
		return ErrRunPreflightRejected
	}
	return nil
}
