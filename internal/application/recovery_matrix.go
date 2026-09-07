package application

import (
	"errors"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

var ErrInvalidRecoveryPolicy = errors.New("recovery policy is invalid")

type RecoveryFailureSource string

const (
	RecoveryModelTransport       RecoveryFailureSource = "model_transport"
	RecoverySummary              RecoveryFailureSource = "summary"
	RecoverySQLitePreCommit      RecoveryFailureSource = "sqlite_pre_commit"
	RecoverySQLitePostCommit     RecoveryFailureSource = "sqlite_post_commit"
	RecoveryToolSource           RecoveryFailureSource = "tool_kubernetes_data_source"
	RecoveryApprovalReviewer     RecoveryFailureSource = "approval_reviewer"
	RecoveryNotificationTitle    RecoveryFailureSource = "notification_title"
	RecoveryClipboard            RecoveryFailureSource = "clipboard"
	RecoveryTranscriptSearch     RecoveryFailureSource = "transcript_history_search"
	RecoveryEndpointContinuation RecoveryFailureSource = "endpoint_continuation"
	RecoveryTerminalShutdown     RecoveryFailureSource = "terminal_shutdown"
)

type RecoveryPersistenceEffect string

const (
	RecoveryPersistencePreserved RecoveryPersistenceEffect = "preserved"
	RecoveryPersistenceDegraded  RecoveryPersistenceEffect = "degraded"
	RecoveryPersistenceUnknown   RecoveryPersistenceEffect = "unknown"
)

type RecoveryInputDisposition string

const (
	RecoveryInputUnchanged RecoveryInputDisposition = "unchanged"
	RecoveryInputEditable  RecoveryInputDisposition = "editable"
	RecoveryInputRecovered RecoveryInputDisposition = "recovered"
	RecoveryInputUnknown   RecoveryInputDisposition = "unknown"
)

type RecoveryRetryPolicy string

const (
	RecoveryRetryDenied        RecoveryRetryPolicy = "denied"
	RecoveryRetryExplicitOnly  RecoveryRetryPolicy = "new_explicit_input_only"
	RecoveryRetryNotApplicable RecoveryRetryPolicy = "not_applicable"
)

type RecoveryNextAction string

const (
	RecoveryNextEditInput         RecoveryNextAction = "edit_recovered_input"
	RecoveryNextRunDoctor         RecoveryNextAction = "run_doctor"
	RecoveryNextInspectEvidence   RecoveryNextAction = "inspect_evidence"
	RecoveryNextReviewApproval    RecoveryNextAction = "review_approval"
	RecoveryNextUseTerminalSelect RecoveryNextAction = "use_terminal_selection"
	RecoveryNextAdjustSearch      RecoveryNextAction = "adjust_local_search"
	RecoveryNextDisableDelivery   RecoveryNextAction = "disable_optional_delivery"
	RecoveryNextRestartExplicitly RecoveryNextAction = "restart_and_submit_explicitly"
	RecoveryNextDoNotRetryBlindly RecoveryNextAction = "do_not_retry_blindly"
)

// RecoveryPolicy is a fixed, content-free decision. A nil TerminalReason means
// an optional delivery failure must preserve the AgentRun's existing reason.
type RecoveryPolicy struct {
	Source                        RecoveryFailureSource
	AgentRunAffected              bool
	TerminalReason                *domain.RunTerminalReason
	Persistence                   RecoveryPersistenceEffect
	QueueAutoDrain                bool
	Input                         RecoveryInputDisposition
	ModelRetry                    RecoveryRetryPolicy
	ToolOrActionRetry             RecoveryRetryPolicy
	NextAction                    RecoveryNextAction
	MaximumAutomaticExternalCalls int
}

func RecoveryPolicyFor(source RecoveryFailureSource) (RecoveryPolicy, error) {
	reason := func(value domain.RunTerminalReason) *domain.RunTerminalReason { return &value }
	base := RecoveryPolicy{
		Source: source, AgentRunAffected: true, Persistence: RecoveryPersistencePreserved,
		Input: RecoveryInputUnchanged, ModelRetry: RecoveryRetryDenied, ToolOrActionRetry: RecoveryRetryDenied,
		MaximumAutomaticExternalCalls: 0,
	}
	switch source {
	case RecoveryModelTransport:
		base.TerminalReason, base.Input, base.NextAction = reason(domain.RunTerminalUnknown), RecoveryInputUnknown, RecoveryNextDoNotRetryBlindly
	case RecoverySummary:
		base.TerminalReason, base.Input, base.NextAction = reason(domain.RunTerminalFailed), RecoveryInputRecovered, RecoveryNextEditInput
	case RecoverySQLitePreCommit:
		base.TerminalReason, base.Persistence, base.Input, base.NextAction = reason(domain.RunTerminalPersistenceDegraded), RecoveryPersistenceDegraded, RecoveryInputEditable, RecoveryNextRunDoctor
	case RecoverySQLitePostCommit:
		base.TerminalReason, base.Persistence, base.Input, base.NextAction = reason(domain.RunTerminalPersistenceDegraded), RecoveryPersistenceUnknown, RecoveryInputUnknown, RecoveryNextDoNotRetryBlindly
	case RecoveryToolSource:
		base.TerminalReason, base.NextAction = reason(domain.RunTerminalSourceUnavailable), RecoveryNextInspectEvidence
	case RecoveryApprovalReviewer:
		base.AgentRunAffected, base.TerminalReason, base.NextAction = false, nil, RecoveryNextReviewApproval
	case RecoveryNotificationTitle:
		base.AgentRunAffected, base.TerminalReason, base.ModelRetry, base.ToolOrActionRetry, base.NextAction = false, nil, RecoveryRetryNotApplicable, RecoveryRetryNotApplicable, RecoveryNextDisableDelivery
	case RecoveryClipboard:
		base.AgentRunAffected, base.TerminalReason, base.ModelRetry, base.ToolOrActionRetry, base.NextAction = false, nil, RecoveryRetryNotApplicable, RecoveryRetryNotApplicable, RecoveryNextUseTerminalSelect
	case RecoveryTranscriptSearch:
		base.AgentRunAffected, base.TerminalReason, base.ModelRetry, base.ToolOrActionRetry, base.NextAction = false, nil, RecoveryRetryNotApplicable, RecoveryRetryNotApplicable, RecoveryNextAdjustSearch
	case RecoveryEndpointContinuation:
		base.TerminalReason, base.Input, base.NextAction = reason(domain.RunTerminalUnknown), RecoveryInputUnknown, RecoveryNextDoNotRetryBlindly
	case RecoveryTerminalShutdown:
		base.TerminalReason, base.Persistence, base.Input, base.NextAction = reason(domain.RunTerminalRecovered), RecoveryPersistenceUnknown, RecoveryInputRecovered, RecoveryNextRestartExplicitly
	default:
		return RecoveryPolicy{}, ErrInvalidRecoveryPolicy
	}
	if base.validate() != nil {
		return RecoveryPolicy{}, ErrInvalidRecoveryPolicy
	}
	return base, nil
}

func (policy RecoveryPolicy) validate() error {
	if !validRecoveryFailureSource(policy.Source) || !validRecoveryPersistenceEffect(policy.Persistence) ||
		!validRecoveryInputDisposition(policy.Input) || !validRecoveryRetryPolicy(policy.ModelRetry) ||
		!validRecoveryRetryPolicy(policy.ToolOrActionRetry) || !validRecoveryNextAction(policy.NextAction) ||
		policy.MaximumAutomaticExternalCalls != 0 ||
		policy.AgentRunAffected != (policy.TerminalReason != nil) || policy.TerminalReason != nil && !policy.TerminalReason.Valid() ||
		policy.QueueAutoDrain {
		return ErrInvalidRecoveryPolicy
	}
	return nil
}

func validRecoveryFailureSource(source RecoveryFailureSource) bool {
	switch source {
	case RecoveryModelTransport, RecoverySummary, RecoverySQLitePreCommit, RecoverySQLitePostCommit,
		RecoveryToolSource, RecoveryApprovalReviewer, RecoveryNotificationTitle, RecoveryClipboard,
		RecoveryTranscriptSearch, RecoveryEndpointContinuation, RecoveryTerminalShutdown:
		return true
	default:
		return false
	}
}

func validRecoveryPersistenceEffect(effect RecoveryPersistenceEffect) bool {
	return effect == RecoveryPersistencePreserved || effect == RecoveryPersistenceDegraded || effect == RecoveryPersistenceUnknown
}

func validRecoveryInputDisposition(disposition RecoveryInputDisposition) bool {
	return disposition == RecoveryInputUnchanged || disposition == RecoveryInputEditable ||
		disposition == RecoveryInputRecovered || disposition == RecoveryInputUnknown
}

func validRecoveryRetryPolicy(policy RecoveryRetryPolicy) bool {
	return policy == RecoveryRetryDenied || policy == RecoveryRetryExplicitOnly || policy == RecoveryRetryNotApplicable
}

func validRecoveryNextAction(action RecoveryNextAction) bool {
	switch action {
	case RecoveryNextEditInput, RecoveryNextRunDoctor, RecoveryNextInspectEvidence, RecoveryNextReviewApproval,
		RecoveryNextUseTerminalSelect, RecoveryNextAdjustSearch, RecoveryNextDisableDelivery,
		RecoveryNextRestartExplicitly, RecoveryNextDoNotRetryBlindly:
		return true
	default:
		return false
	}
}
