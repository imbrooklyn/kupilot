package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// UIScopeResult is one request-bound scope activation projection.
type UIScopeResult struct {
	RequestID          uint64
	ExpectedGeneration int64
	ScopeGeneration    int64
	Context            string
	Namespace          string
	ReadOnly           bool
	Failure            UIQueryFailureCode
}

// Validate checks success and fail-closed scope result shapes.
func (result UIScopeResult) Validate() error {
	if result.RequestID == 0 || result.ExpectedGeneration < 0 || result.ScopeGeneration < result.ExpectedGeneration {
		return ErrInvalidUIEvent
	}
	if result.Failure != "" {
		if !result.Failure.validOperational() || result.Context != "" || result.Namespace != "" || result.ReadOnly {
			return ErrInvalidUIEvent
		}
		return nil
	}
	candidate := domain.ScopeCandidate{Context: result.Context, Namespace: result.Namespace}
	if candidate.Validate() != nil || !result.ReadOnly {
		return ErrInvalidUIEvent
	}
	return nil
}

// UIStatusResult is the current safe in-memory status projection. Historic
// scope metadata and pending resume candidates are intentionally excluded.
type UIStatusResult struct {
	Session         *UISessionState
	Context         string
	Namespace       string
	ScopeGeneration int64
	ReadOnly        bool
	RunID           domain.AgentRunID
	RunActive       bool
}

// UICommandOutcome contains only the typed result shapes used by delivery.
// Exactly which fields are populated is determined by Command.
type UICommandOutcome struct {
	Command   UICommandKind
	RequestID uint64
	Session   *UISessionState
	Resumed   *UIResumedSession
	Scope     *UIScopeResult
	Resource  *UIResourceSelectionResult
	Status    *UIStatusResult
	Privacy   *PrivacyReview
	Approval  *UIApprovalResult
	RunID     domain.AgentRunID
	Failure   UIQueryFailureCode
}

// Validate checks command/result correlation and exclusive payload shapes.
func (result UICommandOutcome) Validate() error {
	approvalCommand := result.Command == UICommandApproveRestart || result.Command == UICommandRejectRestart ||
		result.Command == UICommandExpireRestart
	if approvalCommand != (result.Approval != nil) || result.Approval != nil && result.Approval.Validate() != nil {
		return ErrInvalidUIEvent
	}
	if result.Failure != "" && (!result.Failure.valid() ||
		result.Failure == UIQueryNotResumable && result.Command != UICommandResumeSession) {
		return ErrInvalidUIEvent
	}
	if result.Privacy != nil {
		if result.Privacy.Validate() != nil ||
			result.Command != UICommandShowPrivacy && result.Command != UICommandToggleLogs &&
				!(result.Command == UICommandSubmitQuestion && result.Failure == UIQueryConsentRequired) {
			return ErrInvalidUIEvent
		}
	}
	switch result.Command {
	case UICommandAcceptResume:
		if result.RequestID == 0 {
			return ErrInvalidUIEvent
		}
		if result.Failure != "" {
			if result.Session != nil || result.Resumed != nil || result.Scope != nil || result.Resource != nil ||
				result.Status != nil || result.RunID != "" {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.Session == nil || !result.Session.validate() || !result.Session.Resumed ||
			result.Resumed == nil || !validUIResumedSession(*result.Resumed, result.RequestID) ||
			result.Resumed.Session.ID != result.Session.ID || result.Scope == nil || result.Scope.RequestID != result.RequestID ||
			result.Scope.Validate() != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
		if result.Resource != nil {
			if result.Resource.RequestID != result.RequestID || result.Resource.ScopeGeneration != result.Scope.ScopeGeneration ||
				result.Resource.Validate() != nil {
				return ErrInvalidUIEvent
			}
		}
	case UICommandCancelResume:
		if result.RequestID == 0 || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandSelectContext, UICommandSelectNamespace, UICommandActivateScope:
		if result.RequestID == 0 || result.Scope == nil || result.Scope.RequestID != result.RequestID || result.Scope.Validate() != nil ||
			result.Session != nil || result.Resumed != nil || result.Resource != nil || result.Status != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandSelectResource:
		if result.RequestID == 0 || result.Resource == nil || result.Resource.RequestID != result.RequestID || result.Resource.Validate() != nil ||
			result.Session != nil || result.Resumed != nil || result.Scope != nil || result.Status != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandNewSession:
		if result.RequestID != 0 || result.Session == nil || !result.Session.validate() || result.Resumed != nil ||
			result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandRenameSession:
		if result.RequestID != 0 || result.Resumed != nil || result.Scope != nil || result.Resource != nil ||
			result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
		if result.Failure != "" {
			if result.Session != nil {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.Session == nil || !result.Session.validate() {
			return ErrInvalidUIEvent
		}
	case UICommandShowStatus:
		if result.RequestID != 0 || result.Status == nil || !result.Status.valid() || result.Session != nil || result.Resumed != nil ||
			result.Scope != nil || result.Resource != nil || result.RunID != "" || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandSubmitQuestion:
		if result.RequestID == 0 || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil {
			return ErrInvalidUIEvent
		}
		if result.Failure == "" {
			if !result.RunID.Valid() || result.Privacy != nil {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.RunID != "" || result.Failure != UIQueryConsentRequired && result.Failure != UIQueryUnavailable ||
			result.Failure == UIQueryConsentRequired && result.Privacy == nil ||
			result.Failure != UIQueryConsentRequired && result.Privacy != nil {
			return ErrInvalidUIEvent
		}
	case UICommandCancelRun:
		if result.RequestID != 0 || !result.RunID.Valid() || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandShowPrivacy:
		if result.RequestID == 0 || result.Failure != "" || result.Privacy == nil || result.Session != nil ||
			result.Resumed != nil || result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	case UICommandToggleLogs:
		if result.RequestID == 0 || result.Failure != "" || result.Privacy == nil || result.Session != nil ||
			result.Resumed != nil || result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	case UICommandAcceptPrivacy, UICommandRejectPrivacy, UICommandRevokePrivacy, UICommandCancelPrivacy:
		if result.RequestID == 0 || result.Failure != "" || result.Privacy != nil || result.Session != nil ||
			result.Resumed != nil || result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	case UICommandApproveRestart, UICommandRejectRestart, UICommandExpireRestart:
		if result.RequestID == 0 || result.Failure != "" || result.Session != nil || result.Resumed != nil ||
			result.Scope != nil || result.Resource != nil || result.Status != nil || result.Privacy != nil ||
			result.RunID != result.Approval.RunID {
			return ErrInvalidUIEvent
		}
	case UICommandResumeSession:
		if result.RequestID == 0 || result.Failure == "" || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	default:
		return ErrInvalidUIEvent
	}
	return nil
}

func (result UIStatusResult) valid() bool {
	if result.Session != nil && !result.Session.validate() || result.ScopeGeneration < 0 ||
		(result.Context == "") != (result.Namespace == "") || result.ReadOnly != (result.Context != "") ||
		result.Context != "" && (!domain.ValidContextName(result.Context) || !domain.ValidNamespaceName(result.Namespace)) ||
		result.RunActive != result.RunID.Valid() {
		return false
	}
	return true
}

// UIResourceSelectionResult accepts or rejects one request-bound ResourceRef.
type UIResourceSelectionResult struct {
	RequestID       uint64
	ScopeGeneration int64
	Resource        *domain.ResourceRef
	Cleared         bool
	Failure         UIQueryFailureCode
}

// Validate checks scope binding and exclusive selected, cleared, or failed state.
func (result UIResourceSelectionResult) Validate() error {
	if result.RequestID == 0 || result.ScopeGeneration < 1 {
		return ErrInvalidUIEvent
	}
	if result.Failure != "" {
		if !result.Failure.validOperational() || result.Resource != nil || result.Cleared {
			return ErrInvalidUIEvent
		}
		return nil
	}
	if result.Cleared == (result.Resource != nil) {
		return ErrInvalidUIEvent
	}
	if result.Resource != nil && result.Resource.Validate() != nil {
		return ErrInvalidUIEvent
	}
	return nil
}

// ErrInvalidUIEvent reports an invalid UI projection without echoing its data.
var ErrInvalidUIEvent = errors.New("UI event data is invalid")

// UIEventKind identifies one ordered Application-to-TUI run projection.
type UIEventKind string

const (
	UIEventRunStarted   UIEventKind = "run_started"
	UIEventTextDelta    UIEventKind = "text_delta"
	UIEventToolStep     UIEventKind = "tool_step"
	UIEventRunCompleted UIEventKind = "run_completed"
	UIEventRunFailed    UIEventKind = "run_failed"
	UIEventRunCancelled UIEventKind = "run_cancelled"
	// UIEventPersistenceDegraded is a visible nonterminal warning. It never
	// claims that incomplete data is resumable.
	UIEventPersistenceDegraded UIEventKind = "persistence_degraded"
	UIEventApprovalRequested   UIEventKind = "approval_requested"
	UIEventApprovalClosed      UIEventKind = "approval_closed"
	UIEventRestartExecution    UIEventKind = "restart_execution"
)

// ToolStepStatus is the delivery-safe state of one inline Tool step.
type ToolStepStatus string

const (
	ToolStepRequested ToolStepStatus = "requested"
	ToolStepRunning   ToolStepStatus = "running"
	ToolStepSucceeded ToolStepStatus = "succeeded"
	ToolStepPartial   ToolStepStatus = "partial"
	ToolStepDenied    ToolStepStatus = "denied"
	ToolStepFailed    ToolStepStatus = "failed"
	ToolStepCancelled ToolStepStatus = "cancelled"
)

// ToolStep is a bounded safe UI projection, never a raw ToolResult.
type ToolStep struct {
	InvocationID  domain.ToolInvocationID
	Name          domain.ToolName
	Purpose       string
	Status        ToolStepStatus
	Summary       string
	EvidenceCount int
	Truncated     bool
}

// UIEvent carries publisher-owned identity and exactly one projected payload.
type UIEvent struct {
	Kind             UIEventKind
	RunID            domain.AgentRunID
	ScopeGeneration  int64
	Sequence         int64
	Text             string
	ToolStep         *ToolStep
	Approval         *UIApprovalRequest
	ApprovalResult   *UIApprovalResult
	RestartExecution *UIRestartExecution
}

// Terminal reports whether later events for the same run must be ignored.
func (event UIEvent) Terminal() bool {
	return event.Kind == UIEventRunCompleted || event.Kind == UIEventRunFailed || event.Kind == UIEventRunCancelled
}

// Validate checks identity, payload exclusivity, and fixed event states.
func (event UIEvent) Validate() error {
	if !event.RunID.Valid() || event.ScopeGeneration < 1 || event.Sequence < 1 || event.Sequence > 4096 {
		return ErrInvalidUIEvent
	}
	switch event.Kind {
	case UIEventRunStarted:
		if event.Text != "" || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult != nil || event.RestartExecution != nil {
			return ErrInvalidUIEvent
		}
	case UIEventTextDelta, UIEventRunCompleted, UIEventRunFailed, UIEventRunCancelled, UIEventPersistenceDegraded:
		if event.Text == "" || len(event.Text) > MaxQuestionBytes || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult != nil || event.RestartExecution != nil {
			return ErrInvalidUIEvent
		}
	case UIEventToolStep:
		if event.Text != "" || event.ToolStep == nil || !event.ToolStep.valid() || event.Approval != nil || event.ApprovalResult != nil || event.RestartExecution != nil {
			return ErrInvalidUIEvent
		}
	case UIEventApprovalRequested:
		if event.Text != "" || event.ToolStep != nil || event.Approval == nil || event.ApprovalResult != nil || event.RestartExecution != nil ||
			event.Approval.Validate() != nil || event.Approval.RunID != event.RunID ||
			event.Approval.Scope.Generation != event.ScopeGeneration || event.Approval.Sequence != event.Sequence {
			return ErrInvalidUIEvent
		}
	case UIEventApprovalClosed:
		if event.Text != "" || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult == nil || event.RestartExecution != nil ||
			event.ApprovalResult.Validate() != nil || event.ApprovalResult.RunID != event.RunID ||
			event.ApprovalResult.ScopeGeneration != event.ScopeGeneration || event.ApprovalResult.Sequence != event.Sequence {
			return ErrInvalidUIEvent
		}
	case UIEventRestartExecution:
		if event.Text != "" || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult != nil ||
			event.RestartExecution == nil || event.RestartExecution.Validate() != nil ||
			event.RestartExecution.RunID != event.RunID || event.RestartExecution.ScopeGeneration != event.ScopeGeneration ||
			event.RestartExecution.Sequence != event.Sequence {
			return ErrInvalidUIEvent
		}
	default:
		return ErrInvalidUIEvent
	}
	return nil
}

const approvalProposedSummary = "Update only the KuPilot-owned restart annotation to create a new Pod template revision."

// UIApprovalRequest is the complete safe dialog projection. The opaque nonce
// is memory-only decision authority whose type cannot render or marshal bytes.
type UIApprovalRequest struct {
	RequestID            domain.ApprovalID
	RunID                domain.AgentRunID
	SessionID            domain.SessionID
	Sequence             int64
	Operation            domain.ApprovalOperation
	Scope                domain.ScopeSnapshot
	Target               domain.ResourceRef
	TemplateFingerprint  string
	DeploymentGeneration int64
	ReasonSummary        string
	RiskSummary          string
	CurrentSummary       string
	ProposedSummary      string
	Digest               domain.ApprovalDigest
	Nonce                domain.ApprovalNonce
	RequestedAt          time.Time
	ExpiresAt            time.Time
}

// Validate recomputes the operation digest from every displayed parameter.
func (request UIApprovalRequest) Validate() error {
	intent := domain.OperationIntent{
		Operation: request.Operation, Scope: request.Scope,
		DeploymentName: request.Target.Name, DeploymentUID: request.Target.UID,
		TemplateFingerprint: request.TemplateFingerprint, DeploymentGeneration: request.DeploymentGeneration,
		PolicyVersion: domain.RestartDeploymentApprovalPolicyVersion, ReasonSummary: request.ReasonSummary,
	}
	domainRequest := domain.ApprovalRequest{
		ID: request.RequestID, RunID: request.RunID, SessionID: request.SessionID,
		Intent: intent, Digest: request.Digest, Nonce: request.Nonce,
		State: domain.ApprovalStatePending, RequestedAt: request.RequestedAt,
		ExpiresAt: request.ExpiresAt, StateChangedAt: request.RequestedAt,
	}
	wantCurrent := fmt.Sprintf(
		"Deployment generation %d with Pod template fingerprint %s.",
		request.DeploymentGeneration,
		request.TemplateFingerprint,
	)
	digest, err := approval.OperationDigest(domainRequest)
	if request.Sequence < 1 || request.Sequence > 4096 || request.Target.Validate() != nil ||
		request.Target.APIVersion != domain.RestartDeploymentTargetAPIVersion ||
		request.Target.Kind != domain.RestartDeploymentTargetKind || request.Target.Namespace != request.Scope.Namespace ||
		request.RiskSummary != domain.RestartDeploymentRiskSummary || request.CurrentSummary != wantCurrent ||
		request.ProposedSummary != approvalProposedSummary || domainRequest.Validate() != nil ||
		err != nil || !request.Digest.Equal(digest) {
		return ErrInvalidUIEvent
	}
	return nil
}

// UIApprovalResult closes one exact dialog request without carrying its nonce.
type UIApprovalResult struct {
	RequestID       domain.ApprovalID
	RunID           domain.AgentRunID
	ScopeGeneration int64
	Sequence        int64
	Digest          domain.ApprovalDigest
	State           domain.ApprovalState
	StateReason     domain.ApprovalStateReason
	Execution       *UIRestartExecution
}

// Validate checks one bounded non-pending dialog outcome.
func (result UIApprovalResult) Validate() error {
	if !result.RequestID.Valid() || !result.RunID.Valid() || result.ScopeGeneration < 1 ||
		result.Sequence < 1 || result.Sequence > 4096 || !result.Digest.Valid() ||
		!validApprovalResultState(result.State, result.StateReason) {
		return ErrInvalidUIEvent
	}
	if result.State == domain.ApprovalStateConsumed {
		if result.Execution == nil || result.Execution.Validate() != nil || !result.Execution.State.Terminal() ||
			result.Execution.RequestID != result.RequestID || result.Execution.RunID != result.RunID ||
			result.Execution.ScopeGeneration != result.ScopeGeneration || result.Execution.Sequence != result.Sequence ||
			!result.Execution.Digest.Equal(result.Digest) {
			return ErrInvalidUIEvent
		}
	} else if result.Execution != nil {
		return ErrInvalidUIEvent
	}
	return nil
}

func validApprovalResultState(state domain.ApprovalState, reason domain.ApprovalStateReason) bool {
	switch state {
	case domain.ApprovalStateApproved:
		return reason == domain.ApprovalReasonUserApproved
	case domain.ApprovalStateRejected:
		return reason == domain.ApprovalReasonUserRejected
	case domain.ApprovalStateExpired:
		return reason == domain.ApprovalReasonTTLExpired
	case domain.ApprovalStateCancelled:
		return reason.ValidCancellation()
	case domain.ApprovalStateInvalidated:
		return reason == domain.ApprovalReasonScopeChanged || reason == domain.ApprovalReasonDigestMismatch ||
			reason == domain.ApprovalReasonNonceMismatch || reason == domain.ApprovalReasonDecisionReplayed
	case domain.ApprovalStateConsumed:
		return reason == domain.ApprovalReasonConsumed
	default:
		return false
	}
}

func projectUIApprovalRequest(request domain.ApprovalRequest, sequence int64) UIApprovalRequest {
	return UIApprovalRequest{
		RequestID: request.ID, RunID: request.RunID, SessionID: request.SessionID, Sequence: sequence,
		Operation: request.Intent.Operation, Scope: request.Intent.Scope,
		Target: domain.ResourceRef{
			APIVersion: domain.RestartDeploymentTargetAPIVersion, Kind: domain.RestartDeploymentTargetKind,
			Namespace: request.Intent.Scope.Namespace, Name: request.Intent.DeploymentName, UID: request.Intent.DeploymentUID,
		},
		TemplateFingerprint: request.Intent.TemplateFingerprint, DeploymentGeneration: request.Intent.DeploymentGeneration,
		ReasonSummary: request.Intent.ReasonSummary, RiskSummary: domain.RestartDeploymentRiskSummary,
		CurrentSummary:  fmt.Sprintf("Deployment generation %d with Pod template fingerprint %s.", request.Intent.DeploymentGeneration, request.Intent.TemplateFingerprint),
		ProposedSummary: approvalProposedSummary, Digest: request.Digest, Nonce: request.Nonce,
		RequestedAt: request.RequestedAt, ExpiresAt: request.ExpiresAt,
	}
}

func projectUIApprovalResult(request domain.ApprovalRequest, sequence int64) UIApprovalResult {
	return UIApprovalResult{
		RequestID: request.ID, RunID: request.RunID, ScopeGeneration: request.Intent.Scope.Generation,
		Sequence: sequence, Digest: request.Digest, State: request.State, StateReason: request.StateReason,
	}
}

// RunObservationKind identifies fixed text-free lifecycle metadata admitted to
// the local logging observer.
type RunObservationKind string

const (
	RunObservationStarted             RunObservationKind = "started"
	RunObservationTerminal            RunObservationKind = "terminal"
	RunObservationPersistenceDegraded RunObservationKind = "persistence_degraded"
)

// RunObservation contains no user, model, Tool, Evidence, or Diagnosis text.
type RunObservation struct {
	Kind                RunObservationKind
	RunID               domain.AgentRunID
	ScopeGeneration     int64
	Status              domain.AgentRunStatus
	PersistenceDegraded bool
}

func (observation RunObservation) valid() bool {
	if !observation.RunID.Valid() || observation.ScopeGeneration < 1 {
		return false
	}
	switch observation.Kind {
	case RunObservationStarted:
		return observation.Status == domain.AgentRunStatusRunning && !observation.PersistenceDegraded
	case RunObservationTerminal:
		return observation.Status.Terminal()
	case RunObservationPersistenceDegraded:
		return observation.Status == domain.AgentRunStatusRunning && observation.PersistenceDegraded
	default:
		return false
	}
}

const uiDeltaFlushBytes = 4 * 1024

// eventBridge is the synchronous Application-to-delivery coalescing boundary.
// It owns no goroutine or channel, never drops structural events, and assigns a
// UI-local sequence so coalesced Agent deltas cannot create ambiguous ordering.
type eventBridge struct {
	runID           domain.AgentRunID
	scopeGeneration int64
	sink            UIEventSink
	sequence        int64
	started         bool
	terminal        bool
	pendingDelta    string
	diagnosis       string
}

func newEventBridge(runID domain.AgentRunID, scopeGeneration int64, sink UIEventSink) (*eventBridge, error) {
	if !runID.Valid() || scopeGeneration < 1 || sink == nil {
		return nil, ErrInvalidUIEvent
	}
	return &eventBridge{runID: runID, scopeGeneration: scopeGeneration, sink: sink}, nil
}

func (bridge *eventBridge) accept(ctx context.Context, event agent.RunEvent) error {
	if bridge == nil || ctx == nil || ctx.Err() != nil || event.Validate() != nil ||
		event.RunID != bridge.runID || event.ScopeGeneration != bridge.scopeGeneration || bridge.terminal {
		return ErrInvalidUIEvent
	}
	if event.Kind == agent.RunEventTextDelta {
		if !bridge.started {
			return ErrInvalidUIEvent
		}
		if len(bridge.pendingDelta)+len(event.TextDelta) > MaxQuestionBytes {
			if err := bridge.flushDelta(ctx); err != nil {
				return err
			}
		}
		bridge.pendingDelta += event.TextDelta
		if len(bridge.pendingDelta) >= uiDeltaFlushBytes {
			return bridge.flushDelta(ctx)
		}
		return nil
	}
	if err := bridge.flushDelta(ctx); err != nil {
		return err
	}
	switch event.Kind {
	case agent.RunEventRunStarted:
		if bridge.started {
			return ErrInvalidUIEvent
		}
		bridge.started = true
		return bridge.emit(ctx, UIEvent{Kind: UIEventRunStarted})
	case agent.RunEventModelStreamStarted, agent.RunEventEvidenceCollected:
		return nil
	case agent.RunEventDiagnosisReady:
		bridge.diagnosis = event.Diagnosis.AnswerMarkdown
		return nil
	case agent.RunEventToolCallRequested, agent.RunEventToolCallStarted,
		agent.RunEventToolCallCompleted, agent.RunEventToolCallFailed, agent.RunEventToolCallDenied:
		step, err := projectToolStep(event)
		if err != nil {
			return err
		}
		return bridge.emit(ctx, UIEvent{Kind: UIEventToolStep, ToolStep: &step})
	case agent.RunEventRunCompleted:
		if bridge.diagnosis == "" {
			return ErrInvalidUIEvent
		}
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunCompleted, Text: bridge.diagnosis})
	case agent.RunEventRunFailed:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: event.Failure.SafeMessage})
	case agent.RunEventRunCancelled:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunCancelled, Text: "The AgentRun was cancelled."})
	case agent.RunEventRunTimedOut:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: "The AgentRun reached its deadline."})
	case agent.RunEventRunStaleScope:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: "The AgentRun stopped because its Kubernetes scope changed."})
	case agent.RunEventRunInterrupted:
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: "The AgentRun was interrupted."})
	default:
		return ErrInvalidUIEvent
	}
}

func (bridge *eventBridge) persistenceDegraded(ctx context.Context) error {
	if bridge == nil || ctx == nil || ctx.Err() != nil || !bridge.started || bridge.terminal {
		return ErrInvalidUIEvent
	}
	if err := bridge.flushDelta(ctx); err != nil {
		return err
	}
	return bridge.emit(ctx, UIEvent{
		Kind: UIEventPersistenceDegraded,
		Text: "Local persistence is degraded; this run may not be resumable.",
	})
}

func (bridge *eventBridge) forceFailed(ctx context.Context, safeMessage string) error {
	if bridge == nil || ctx == nil || ctx.Err() != nil || bridge.terminal || safeMessage == "" {
		return ErrInvalidUIEvent
	}
	if !bridge.started {
		bridge.started = true
		if err := bridge.emit(ctx, UIEvent{Kind: UIEventRunStarted}); err != nil {
			return err
		}
	}
	if err := bridge.flushDelta(ctx); err != nil {
		return err
	}
	return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: safeMessage})
}

func (bridge *eventBridge) flushDelta(ctx context.Context) error {
	if bridge.pendingDelta == "" {
		return nil
	}
	value := bridge.pendingDelta
	if err := bridge.emit(ctx, UIEvent{Kind: UIEventTextDelta, Text: value}); err != nil {
		return err
	}
	bridge.pendingDelta = ""
	return nil
}

func (bridge *eventBridge) emitTerminal(ctx context.Context, event UIEvent) error {
	if err := bridge.emit(ctx, event); err != nil {
		return err
	}
	bridge.terminal = true
	return nil
}

func (bridge *eventBridge) emit(ctx context.Context, event UIEvent) error {
	nextSequence := bridge.sequence + 1
	event.RunID = bridge.runID
	event.ScopeGeneration = bridge.scopeGeneration
	event.Sequence = nextSequence
	if event.Validate() != nil {
		return ErrInvalidUIEvent
	}
	if err := bridge.sink.PublishUIEvent(ctx, event); err != nil {
		return err
	}
	bridge.sequence = nextSequence
	return nil
}

func projectToolStep(event agent.RunEvent) (ToolStep, error) {
	invocation := event.ToolInvocation
	if invocation == nil {
		return ToolStep{}, ErrInvalidUIEvent
	}
	step := ToolStep{
		InvocationID: invocation.ID,
		Name:         invocation.Name,
		Status:       ToolStepRequested,
		Truncated:    invocation.Truncated,
	}
	if invocation.Purpose != nil {
		step.Purpose = *invocation.Purpose
	}
	if invocation.ResultSummary != nil {
		step.Summary = *invocation.ResultSummary
	} else if invocation.SafeError != nil {
		step.Summary = *invocation.SafeError
	}
	switch event.Kind {
	case agent.RunEventToolCallRequested:
		step.Status = ToolStepRequested
	case agent.RunEventToolCallStarted:
		step.Status = ToolStepRunning
	case agent.RunEventToolCallCompleted:
		step.Status = ToolStepSucceeded
		if invocation.Truncated {
			step.Status = ToolStepPartial
		}
		if step.Summary == "" {
			step.Summary = "Tool collection completed."
		}
	case agent.RunEventToolCallDenied:
		step.Status = ToolStepDenied
		if step.Summary == "" {
			step.Summary = "The Tool call was denied safely."
		}
	case agent.RunEventToolCallFailed:
		step.Status = ToolStepFailed
		if invocation.Status == domain.ToolInvocationStatusCancelled {
			step.Status = ToolStepCancelled
		}
		if step.Summary == "" {
			step.Summary = "The Tool call failed safely."
		}
	default:
		return ToolStep{}, ErrInvalidUIEvent
	}
	step.EvidenceCount = invocation.EvidenceCount
	if !step.valid() {
		return ToolStep{}, ErrInvalidUIEvent
	}
	return step, nil
}

func (step ToolStep) valid() bool {
	if !step.InvocationID.Valid() || !step.Name.Valid() || len(step.Purpose) > 1024 || len(step.Summary) > 4096 ||
		step.EvidenceCount < 0 || step.EvidenceCount > 100 {
		return false
	}
	switch step.Status {
	case ToolStepRequested, ToolStepRunning, ToolStepSucceeded, ToolStepPartial,
		ToolStepDenied, ToolStepFailed, ToolStepCancelled:
		return true
	default:
		return false
	}
}
