package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/approval"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// UIScopeResult is one request-bound scope activation projection.
type UIScopeResult struct {
	RequestID               uint64
	ExpectedGeneration      int64
	ScopeGeneration         int64
	Context                 string
	Namespace               string
	ReadOnly                bool
	ScopePreferenceDegraded bool
	Failure                 UIQueryFailureCode
	InterruptedRun          *UITerminalOutcome
}

// Validate checks success and fail-closed scope result shapes.
func (result UIScopeResult) Validate() error {
	if result.RequestID == 0 || result.ExpectedGeneration < 0 || result.ScopeGeneration < result.ExpectedGeneration {
		return ErrInvalidUIEvent
	}
	if result.InterruptedRun != nil && (!result.InterruptedRun.valid() ||
		result.InterruptedRun.Reason != domain.RunTerminalStaleGeneration || result.ScopeGeneration == result.ExpectedGeneration) {
		return ErrInvalidUIEvent
	}
	if result.Failure != "" {
		if !result.Failure.validOperational() || result.Context != "" || result.Namespace != "" || result.ReadOnly ||
			result.ScopePreferenceDegraded {
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
	Session                        *UISessionState
	Context                        string
	Namespace                      string
	NamespaceAccess                domain.NamespaceAccessPolicy
	ScopeGeneration                int64
	ReadOnly                       bool
	RunID                          domain.AgentRunID
	RunActive                      bool
	CapabilityCatalogVersion       string
	ResourcePolicyVersion          string
	ObservabilityPolicyVersion     string
	RemoteDiagnosticsPolicyVersion string
	LocalExecutionPolicyVersion    string
	ResourceTypeCount              int
	PodExecPolicyCount             int
	DiagnosticPodPolicyCount       int
	LocalCommandPolicyCount        int
	LocalShellPolicyCount          int
	ContainerFileReadEnabled       bool
	PrometheusEnabled              bool
	LokiEnabled                    bool
	PersistenceDegraded            bool
	ConversationInput              ConversationInputStatus
	Budget                         UIBudgetStatus
	ModelContext                   UIModelContextStatus
	AgentModel                     UIModelRoleStatus
	ReviewerModel                  UIModelRoleStatus
	Permission                     UIPermissionStatus
	Action                         *UIActionStatus
	PlanArmed                      bool
}

// UIPermissionStatus is a content-free local policy snapshot.
type UIPermissionStatus struct {
	Configured           bool
	Profile              domain.PermissionProfile
	PolicyGeneration     domain.PolicyGeneration
	Healthy              bool
	FullAccessAllowed    bool
	HighRiskAcknowledged bool
	SessionRuleCount     int
	CustomRoutes         []UICustomPermissionRoute
	SessionRules         []UISessionPermissionRuleStatus
}

// UICustomPermissionRoute is one content-free exact custom routing row.
type UICustomPermissionRoute struct {
	Operation   domain.ActionOperation
	Risk        domain.RiskClass
	Disposition domain.ReviewDisposition
}

// UISessionPermissionRuleStatus is a bounded, process-local rule projection.
// It contains no nonce, credential, raw response, or persisted authority.
type UISessionPermissionRuleStatus struct {
	ID                     domain.PermissionRuleID
	Operation              domain.ActionOperation
	Scope                  domain.ScopeSnapshot
	NamespaceAccess        domain.NamespaceAccessPolicy
	PolicyGeneration       domain.PolicyGeneration
	Target                 string
	TargetSubresource      string
	ParameterSummary       string
	Effect                 domain.CapabilityEffectClass
	Risk                   domain.RiskClass
	DataCategories         domain.ActionDataCategories
	AllowedSinks           domain.ActionSinks
	NetworkEffects         domain.ActionNetworkEffects
	NetworkDestinationHash domain.ActionDigest
	Limits                 domain.ActionLimits
	CreatedAtMillis        int64
	ExpiresAtMillis        int64
}

// UIReviewerState is an explicit delivery state. It never carries authority.
type UIReviewerState string

const (
	UIReviewerReviewing UIReviewerState = "reviewing"
	UIReviewerApproved  UIReviewerState = "approved"
	UIReviewerDenied    UIReviewerState = "denied"
	UIReviewerEscalated UIReviewerState = "escalated_to_user"
	UIReviewerTimedOut  UIReviewerState = "timed_out"
)

func (state UIReviewerState) valid() bool {
	return state == UIReviewerReviewing || state == UIReviewerApproved || state == UIReviewerDenied ||
		state == UIReviewerEscalated || state == UIReviewerTimedOut
}

// UIReviewerStatus identifies the optional Reviewer and its latest explicit
// state without representing its recommendation as a human decision.
type UIReviewerStatus struct {
	State            UIReviewerState
	Profile          string
	OriginHash       string
	RationaleSummary string
}

// UIReviewerEvent is one ordered, digest-bound Reviewer presentation event.
// EventIndex orders lifecycle states inside the ActionEnvelope's single run
// sequence and is not approval authority.
type UIReviewerEvent struct {
	RequestID        domain.ApprovalID
	RunID            domain.AgentRunID
	ScopeGeneration  int64
	PolicyGeneration domain.PolicyGeneration
	Sequence         int64
	EventIndex       int64
	Digest           domain.ApprovalDigest
	Status           UIReviewerStatus
}

func (event UIReviewerEvent) Validate() error {
	if !event.RequestID.Valid() || !event.RunID.Valid() || event.ScopeGeneration < 1 ||
		!event.PolicyGeneration.Valid() || event.Sequence < 1 || event.Sequence > maxUIEventSequence ||
		event.EventIndex < 1 || event.EventIndex > 16 || !event.Digest.Valid() || !event.Status.valid() {
		return ErrInvalidUIEvent
	}
	return nil
}

// UIActionStatus contains no target, parameter, credential, or nonce.
type UIActionStatus struct {
	RequestID        domain.ApprovalID
	Operation        domain.ActionOperation
	Risk             domain.RiskClass
	Route            domain.ReviewDisposition
	State            domain.ApprovalState
	ScopeGeneration  int64
	PolicyGeneration domain.PolicyGeneration
	Reviewing        bool
	Reviewer         *UIReviewerStatus
	ExpiresAtMillis  int64
}

// UIPermissionsResult is the local bounded result used by /permissions and
// permission changes. It performs no model, Kubernetes, repository, Tool, or
// executor I/O.
type UIPermissionsResult struct {
	RequestID      uint64
	Permission     UIPermissionStatus
	Reviewer       UIModelRoleStatus
	Action         *UIActionStatus
	Changed        bool
	RuleCreated    bool
	InterruptedRun *UITerminalOutcome
}

// UIModelContextStatus contains counts and coverage only, never content.
type UIModelContextStatus struct {
	Mode                    domain.PrivacyMode
	EligibleMessages        int
	EligibleBytes           int
	Compressed              bool
	CompressedAtUnixMillis  int64
	CoveredThroughMessageID domain.MessageID
	RecentTailMessages      int
	SummaryCallsUsed        int
	SummaryCallsMaximum     int
	StorageHealthy          bool
	Pressure                ContextPressureState
	WorkingMessages         int
	WorkingBytes            int
	MessageLimit            int
	ByteLimit               int
	SummaryMessageTrigger   int
	SummaryByteTrigger      int
}

// ContextPressureState is based only on exact retained message/byte state.
type ContextPressureState string

const (
	ContextPressureNormal   ContextPressureState = "normal"
	ContextPressureElevated ContextPressureState = "elevated"
	ContextPressureCritical ContextPressureState = "critical"
	ContextPressureDegraded ContextPressureState = "degraded"
)

func (state ContextPressureState) valid() bool {
	return state == ContextPressureNormal || state == ContextPressureElevated ||
		state == ContextPressureCritical || state == ContextPressureDegraded
}

// UIModelRoleStatus is one role-bound, content-free model status.
type UIModelRoleStatus struct {
	Role       domain.ModelRole
	Profile    string
	OriginHash string
	Configured bool
	Available  bool
	Consented  bool
}

// UIBudgetStatus is the safe local projection shown by /status. Durations are
// integer milliseconds so delivery does not receive a clock or live budget.
type UIBudgetStatus struct {
	ModelEvidenceBasis       string
	Profile                  agent.BudgetProfile
	RunMilliseconds          int64
	ElapsedMilliseconds      int64
	RemainingMilliseconds    int64
	StepsUsed                int
	StepsMaximum             int
	ToolCallsUsed            int
	ToolCallsMaximum         int
	ModelCallsUsed           int
	ModelCallsMaximum        int
	ModelCostUnitsUsed       int
	ModelCostUnitsMaximum    int
	SummaryCallsUsed         int
	SummaryCallsMaximum      int
	SummaryCostUnitsUsed     int
	SummaryCostUnitsMaximum  int
	ReviewerCallsUsed        int
	ReviewerCallsMaximum     int
	ReviewerCostUnitsUsed    int
	ReviewerCostUnitsMaximum int
	ToolResultBytesUsed      int
	ToolResultBytesMaximum   int
	LogCallsUsed             int
	LogCallsMaximum          int
	LogContainersMaximum     int
	LogBytesMaximum          int
	EventPagesMaximum        int
	EventPageItemsMaximum    int
	EventPageBytesMaximum    int
	EventBytesMaximum        int
	MetricCallsUsed          int
	MetricCallsMaximum       int
	MetricContainersMaximum  int
	MetricBytesMaximum       int
	DataSourceCallsUsed      int
	DataSourceCallsMaximum   int
	RemoteExecCallsUsed      int
	RemoteExecCallsMaximum   int
	LocalProcessCallsUsed    int
	LocalProcessCallsMaximum int
	DataSourcePagesMaximum   int
	DataSourceSeriesMaximum  int
	DataSourceSamplesMaximum int
	DataSourceLinesMaximum   int
	DataSourceBytesMaximum   int
	DataSourceWindowMillis   int64
	DataSourceStepMillis     int64
	ResourcePagesMaximum     int
	ResourcePageItemsMaximum int
	ResourcePageBytesMaximum int
	ResourceScannedMaximum   int
	ResourceReturnedMaximum  int
	ResourceBytesMaximum     int
	FineGrained              []UIBudgetMeasure
}

type UIBudgetValueBasis string

const (
	UIBudgetMeasured    UIBudgetValueBasis = "measured"
	UIBudgetConfigured  UIBudgetValueBasis = "configured"
	UIBudgetReserved    UIBudgetValueBasis = "reserved"
	UIBudgetUnavailable UIBudgetValueBasis = "unavailable"
	UIBudgetEstimated   UIBudgetValueBasis = "estimated"
)

type UIBudgetCategory string

const (
	UIBudgetModelInputBytes      UIBudgetCategory = "model_input_bytes"
	UIBudgetModelOutputBytes     UIBudgetCategory = "model_output_bytes"
	UIBudgetSummaryReserveBytes  UIBudgetCategory = "summary_reserve_bytes"
	UIBudgetModelAttempts        UIBudgetCategory = "model_attempts"
	UIBudgetToolCalls            UIBudgetCategory = "tool_calls"
	UIBudgetExternalReadCalls    UIBudgetCategory = "kubernetes_data_source_calls"
	UIBudgetEvidenceItems        UIBudgetCategory = "evidence_items"
	UIBudgetEvidenceBytes        UIBudgetCategory = "evidence_bytes"
	UIBudgetPages                UIBudgetCategory = "pages"
	UIBudgetLines                UIBudgetCategory = "lines"
	UIBudgetSamples              UIBudgetCategory = "samples"
	UIBudgetWallMilliseconds     UIBudgetCategory = "wall_time_ms"
	UIBudgetIdleMilliseconds     UIBudgetCategory = "idle_time_ms"
	UIBudgetQueueItems           UIBudgetCategory = "queue_items"
	UIBudgetQueueBytes           UIBudgetCategory = "queue_bytes"
	UIBudgetContinuationAttempts UIBudgetCategory = "continuation_attempts"
	UIBudgetContinuationTime     UIBudgetCategory = "continuation_time_ms"
)

type UIBudgetMeasure struct {
	Category UIBudgetCategory
	Used     int64
	Limit    int64
	Basis    UIBudgetValueBasis
}

// UICommandOutcome contains only the typed result shapes used by delivery.
// Exactly which fields are populated is determined by Command.
type UICommandOutcome struct {
	Command            UICommandKind
	RequestID          uint64
	Session            *UISessionState
	Resumed            *UIResumedSession
	Scope              *UIScopeResult
	Resource           *UIResourceSelectionResult
	Status             *UIStatusResult
	Doctor             *UIDoctorResult
	Privacy            *PrivacyReview
	Lifecycle          *SessionLifecycleReview
	DeletionReview     *SessionDeletionReview
	Deletion           *SessionDeletionResult
	HistoryDeletion    *HistoryDeletionResult
	LocalStateDeletion *LocalStateDeletionResult
	Export             *SessionExportResult
	Approval           *UIApprovalResult
	Permissions        *UIPermissionsResult
	ConversationInput  *ConversationInputProjection
	ConversationChange *ConversationInputMutationResult
	Compaction         *UIManualCompactionResult
	QuestionStart      *UIQuestionStartFailure
	PlanArmed          *bool
	RunMode            agent.RunMode
	RunID              domain.AgentRunID
	Failure            UIQueryFailureCode
}

// Validate checks command/result correlation and exclusive payload shapes.
func (result UICommandOutcome) Validate() error {
	approvalCommand := result.Command == UICommandApproveAction || result.Command == UICommandRejectAction ||
		result.Command == UICommandCancelAction || result.Command == UICommandExpireAction
	if approvalCommand != (result.Approval != nil) || result.Approval != nil && result.Approval.Validate() != nil {
		return ErrInvalidUIEvent
	}
	permissionsCommand := result.Command == UICommandShowPermissions || result.Command == UICommandChangePermission ||
		result.Command == UICommandCreateSessionRule
	if permissionsCommand != (result.Permissions != nil) || result.Permissions != nil && result.Permissions.valid() != nil {
		return ErrInvalidUIEvent
	}
	if result.Failure != "" && (!result.Failure.valid() ||
		result.Failure == UIQueryNotResumable && result.Command != UICommandResumeSession) {
		return ErrInvalidUIEvent
	}
	if result.QuestionStart != nil && (result.Command != UICommandSubmitQuestion || result.QuestionStart.Validate() != nil) {
		return ErrInvalidUIEvent
	}
	planCommand := result.Command == UICommandArmPlan || result.Command == UICommandCancelPlan
	if planCommand != (result.PlanArmed != nil) {
		return ErrInvalidUIEvent
	}
	if result.Privacy != nil {
		if result.Privacy.Validate() != nil ||
			result.Command != UICommandShowPrivacy && result.Command != UICommandToggleLogs &&
				result.Command != UICommandTightenRetention && result.Command != UICommandSetPersistenceMode &&
				!(result.Command == UICommandSubmitQuestion && result.QuestionStart != nil &&
					result.QuestionStart.Reason == QuestionStartConsentRequired) {
			return ErrInvalidUIEvent
		}
	}
	if result.Lifecycle != nil {
		if result.Lifecycle.Validate() != nil ||
			result.Command != UICommandShowPrivacy && result.Command != UICommandToggleLogs &&
				result.Command != UICommandTightenRetention && result.Command != UICommandSetPersistenceMode {
			return ErrInvalidUIEvent
		}
	}
	if result.Deletion != nil && (result.Deletion.Validate() != nil || result.Command != UICommandDeleteSession) {
		return ErrInvalidUIEvent
	}
	if result.DeletionReview != nil && (result.DeletionReview.Validate() != nil || result.Command != UICommandPreviewSessionDeletion) {
		return ErrInvalidUIEvent
	}
	if result.HistoryDeletion != nil && (result.HistoryDeletion.Validate() != nil || result.Command != UICommandClearHistory) {
		return ErrInvalidUIEvent
	}
	if result.LocalStateDeletion != nil && (result.LocalStateDeletion.Validate() != nil || result.Command != UICommandDeleteAllLocalState) {
		return ErrInvalidUIEvent
	}
	if result.Export != nil && (result.Export.Validate() != nil || result.Command != UICommandExportSession) {
		return ErrInvalidUIEvent
	}
	if result.Doctor != nil && (result.Doctor.Validate() != nil || result.Command != UICommandShowDoctor) {
		return ErrInvalidUIEvent
	}
	conversationCommand := result.Command == UICommandSubmitSteer || result.Command == UICommandEnqueueFollowUp ||
		result.Command == UICommandPopFollowUp
	if conversationCommand != (result.ConversationInput != nil) ||
		result.ConversationInput != nil && !result.ConversationInput.validate() {
		return ErrInvalidUIEvent
	}
	queueMutationCommand := result.Command == UICommandCancelFollowUp || result.Command == UICommandClearFollowUps
	if queueMutationCommand != (result.ConversationChange != nil) || result.ConversationChange != nil &&
		!result.ConversationChange.validate(result.Command == UICommandCancelFollowUp) {
		return ErrInvalidUIEvent
	}
	if (result.Command == UICommandCompactContext) != (result.Compaction != nil) ||
		result.Compaction != nil && result.Compaction.Validate() != nil {
		return ErrInvalidUIEvent
	}
	switch result.Command {
	case UICommandShowDoctor:
		if result.RequestID == 0 || result.Failure != "" || result.Doctor == nil || result.Session != nil ||
			result.Resumed != nil || result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
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
	case UICommandShowPermissions, UICommandChangePermission, UICommandCreateSessionRule:
		if result.RequestID == 0 || result.Permissions.RequestID != result.RequestID || result.Failure != "" ||
			result.Session != nil || result.Resumed != nil || result.Scope != nil || result.Resource != nil ||
			result.Status != nil || result.Privacy != nil || result.Lifecycle != nil || result.RunID != "" ||
			result.Command == UICommandShowPermissions && (result.Permissions.Changed || result.Permissions.RuleCreated) ||
			result.Command == UICommandChangePermission && (!result.Permissions.Changed || result.Permissions.RuleCreated) ||
			result.Command == UICommandCreateSessionRule && (!result.Permissions.RuleCreated || result.Permissions.Changed) {
			return ErrInvalidUIEvent
		}
	case UICommandSubmitQuestion:
		if result.RequestID == 0 || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.Failure != "" {
			return ErrInvalidUIEvent
		}
		if result.QuestionStart == nil {
			if !result.RunID.Valid() || result.Privacy != nil || !result.RunMode.Valid() {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.RunID != "" || result.RunMode != "" ||
			(result.QuestionStart.Reason == QuestionStartConsentRequired) != (result.Privacy != nil) {
			return ErrInvalidUIEvent
		}
	case UICommandSubmitSteer, UICommandEnqueueFollowUp, UICommandPopFollowUp:
		if result.RequestID == 0 || result.Failure != "" || !result.RunID.Valid() ||
			result.Session != nil || result.Resumed != nil ||
			result.Scope != nil || result.Resource != nil || result.Status != nil || result.Privacy != nil ||
			result.Lifecycle != nil || result.Deletion != nil || result.HistoryDeletion != nil ||
			result.LocalStateDeletion != nil || result.Export != nil || result.Approval != nil || result.Permissions != nil {
			return ErrInvalidUIEvent
		}
		if result.Command != UICommandPopFollowUp && result.ConversationInput.RunID != result.RunID {
			return ErrInvalidUIEvent
		}
		if result.Command == UICommandSubmitSteer && result.ConversationInput.State != ConversationInputPending ||
			result.Command == UICommandEnqueueFollowUp && result.ConversationInput.State != ConversationInputQueued ||
			result.Command == UICommandPopFollowUp && !result.ConversationInput.State.editable() {
			return ErrInvalidUIEvent
		}
	case UICommandCancelFollowUp, UICommandClearFollowUps:
		if result.RequestID == 0 || result.Failure != "" || result.RunID != "" || result.ConversationInput != nil ||
			result.Session != nil || result.Resumed != nil || result.Scope != nil || result.Resource != nil ||
			result.Status != nil || result.Privacy != nil || result.Lifecycle != nil || result.Deletion != nil ||
			result.HistoryDeletion != nil || result.LocalStateDeletion != nil || result.Export != nil ||
			result.Approval != nil || result.Permissions != nil {
			return ErrInvalidUIEvent
		}
	case UICommandCompactContext:
		if result.RequestID == 0 || result.Failure != "" || result.RunID != "" || result.RunMode != "" ||
			result.Session != nil || result.Resumed != nil || result.Scope != nil || result.Resource != nil ||
			result.Status != nil || result.Privacy != nil || result.Lifecycle != nil || result.Deletion != nil ||
			result.HistoryDeletion != nil || result.LocalStateDeletion != nil || result.Export != nil ||
			result.Approval != nil || result.Permissions != nil || result.ConversationInput != nil ||
			result.ConversationChange != nil || result.PlanArmed != nil {
			return ErrInvalidUIEvent
		}
	case UICommandCancelRun:
		if result.RequestID != 0 || !result.RunID.Valid() || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.Failure != "" {
			return ErrInvalidUIEvent
		}
	case UICommandArmPlan, UICommandCancelPlan:
		if result.RequestID == 0 || result.Failure != "" || result.RunID != "" || result.RunMode != "" ||
			result.Session != nil || result.Resumed != nil || result.Scope != nil || result.Resource != nil ||
			result.Status != nil || result.Privacy != nil || result.Lifecycle != nil || result.Deletion != nil ||
			result.HistoryDeletion != nil || result.LocalStateDeletion != nil || result.Export != nil ||
			result.Approval != nil || result.Permissions != nil || result.ConversationInput != nil ||
			result.ConversationChange != nil || *result.PlanArmed != (result.Command == UICommandArmPlan) {
			return ErrInvalidUIEvent
		}
	case UICommandShowPrivacy:
		if result.RequestID == 0 || result.Failure != "" || result.Privacy == nil || result.Lifecycle == nil || result.Session != nil ||
			result.Resumed != nil || result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	case UICommandToggleLogs:
		if result.RequestID == 0 || result.Failure != "" || result.Privacy == nil || result.Lifecycle == nil || result.Session != nil ||
			result.Resumed != nil || result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	case UICommandTightenRetention:
		if result.RequestID == 0 || result.Failure != "" || result.Privacy == nil || result.Lifecycle == nil || result.Session != nil ||
			result.Resumed != nil || result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	case UICommandSetPersistenceMode:
		if result.RequestID == 0 || result.Failure != "" || result.Privacy == nil || result.Lifecycle == nil ||
			result.Session == nil || !result.Session.validate() || result.Session.Resumed || result.Resumed != nil ||
			result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	case UICommandPreviewSessionDeletion:
		if result.RequestID == 0 || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.Privacy != nil || result.Lifecycle != nil ||
			result.RunID != "" || result.Deletion != nil {
			return ErrInvalidUIEvent
		}
		if result.Failure != "" {
			if !result.Failure.validOperational() || result.DeletionReview != nil {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.DeletionReview == nil {
			return ErrInvalidUIEvent
		}
	case UICommandDeleteSession:
		if result.RequestID == 0 || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.Privacy != nil || result.Lifecycle != nil || result.RunID != "" ||
			result.DeletionReview != nil {
			return ErrInvalidUIEvent
		}
		if result.Failure != "" {
			if !result.Failure.validOperational() || result.Deletion != nil {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.Deletion == nil {
			return ErrInvalidUIEvent
		}
	case UICommandClearHistory:
		if result.RequestID == 0 || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.Privacy != nil || result.Lifecycle != nil ||
			result.Deletion != nil || result.LocalStateDeletion != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
		if result.Failure != "" {
			if !result.Failure.validOperational() || result.HistoryDeletion != nil {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.HistoryDeletion == nil {
			return ErrInvalidUIEvent
		}
	case UICommandDeleteAllLocalState:
		if result.RequestID == 0 || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.Privacy != nil || result.Lifecycle != nil ||
			result.Deletion != nil || result.HistoryDeletion != nil || result.RunID != "" || result.LocalStateDeletion == nil {
			return ErrInvalidUIEvent
		}
		if result.Failure != "" {
			if !result.Failure.validOperational() || result.LocalStateDeletion.Complete {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if !result.LocalStateDeletion.Complete {
			return ErrInvalidUIEvent
		}
	case UICommandExportSession:
		if result.RequestID == 0 || result.Session != nil || result.Resumed != nil || result.Scope != nil ||
			result.Resource != nil || result.Status != nil || result.Privacy != nil || result.Lifecycle != nil ||
			result.Deletion != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
		if result.Failure != "" {
			if !result.Failure.validOperational() || result.Export != nil {
				return ErrInvalidUIEvent
			}
			return nil
		}
		if result.Export == nil {
			return ErrInvalidUIEvent
		}
	case UICommandAcceptPrivacy, UICommandRejectPrivacy, UICommandRevokePrivacy, UICommandCancelPrivacy:
		if result.RequestID == 0 || result.Failure != "" || result.Privacy != nil || result.Session != nil ||
			result.Resumed != nil || result.Scope != nil || result.Resource != nil || result.Status != nil || result.RunID != "" {
			return ErrInvalidUIEvent
		}
	case UICommandApproveAction, UICommandRejectAction, UICommandCancelAction, UICommandExpireAction:
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
		result.Context == "" && result.NamespaceAccess != "" || result.Context != "" && !result.NamespaceAccess.Valid() ||
		result.Context != "" && (!domain.ValidContextName(result.Context) || !domain.ValidNamespaceName(result.Namespace)) ||
		result.RunActive != result.RunID.Valid() || result.CapabilityCatalogVersion != agent.ToolCatalogVersion ||
		result.ResourcePolicyVersion != domain.ResourcePolicyVersion ||
		result.ObservabilityPolicyVersion != domain.ObservabilityPolicyVersion || result.RemoteDiagnosticsPolicyVersion != domain.RemoteDiagnosticsPolicyVersion ||
		result.LocalExecutionPolicyVersion != domain.LocalExecutionPolicyVersion ||
		result.PodExecPolicyCount < 0 || result.PodExecPolicyCount > domain.MaxRemoteDiagnosticPolicies ||
		result.DiagnosticPodPolicyCount < 0 || result.DiagnosticPodPolicyCount > domain.MaxRemoteDiagnosticPolicies ||
		result.PodExecPolicyCount+result.DiagnosticPodPolicyCount > domain.MaxRemoteDiagnosticPolicies ||
		result.LocalCommandPolicyCount < 0 || result.LocalCommandPolicyCount > domain.MaxLocalCommandPolicies ||
		result.LocalShellPolicyCount < 0 || result.LocalShellPolicyCount > domain.MaxLocalCommandPolicies ||
		result.ResourceTypeCount < len(domain.BuiltInResourcePolicies()) || result.ResourceTypeCount > domain.MaxResourcePolicyEntries ||
		!result.Budget.valid() || !result.ModelContext.valid(result.Session != nil) ||
		!result.AgentModel.valid(true) || !result.ReviewerModel.valid(false) ||
		!result.Permission.valid(result.Session != nil) || !result.ConversationInput.valid() ||
		result.Action != nil && !result.Action.valid() {
		return false
	}
	return true
}

func (status UIPermissionStatus) valid(_ bool) bool {
	if !status.Configured {
		return status.Profile == "" && status.PolicyGeneration == 0 && !status.Healthy &&
			!status.FullAccessAllowed && !status.HighRiskAcknowledged && status.SessionRuleCount == 0 &&
			len(status.CustomRoutes) == 0 && len(status.SessionRules) == 0
	}
	if !status.Profile.Valid() || !status.PolicyGeneration.Valid() ||
		status.SessionRuleCount < 0 || status.SessionRuleCount > MaxSessionPermissionRules ||
		status.SessionRuleCount != len(status.SessionRules) || len(status.CustomRoutes) > MaxCustomPermissionRoutes ||
		status.Profile == domain.PermissionProfileFullAccess && (!status.FullAccessAllowed || !status.HighRiskAcknowledged) ||
		status.Profile != domain.PermissionProfileFullAccess && (status.FullAccessAllowed || status.HighRiskAcknowledged) {
		return false
	}
	if status.Profile != domain.PermissionProfileCustom && len(status.CustomRoutes) != 0 {
		return false
	}
	for _, route := range status.CustomRoutes {
		if !route.Operation.Valid() || !route.Risk.Valid() || !route.Disposition.Valid() {
			return false
		}
	}
	for _, rule := range status.SessionRules {
		if rule.Validate() != nil || rule.PolicyGeneration != status.PolicyGeneration {
			return false
		}
	}
	return true
}

func (status UIActionStatus) valid() bool {
	if !status.RequestID.Valid() || !status.Operation.Valid() || !status.Risk.Valid() ||
		!status.Route.Valid() || status.Route == domain.ReviewDispositionDeny || !status.State.Valid() ||
		status.State.Terminated() || status.ScopeGeneration <= 0 || !status.PolicyGeneration.Valid() ||
		status.ExpiresAtMillis <= 0 {
		return false
	}
	if status.Reviewing != (status.Reviewer != nil && status.Reviewer.State == UIReviewerReviewing) {
		return false
	}
	return status.Reviewer == nil || status.Reviewer.valid()
}

func (status UIReviewerStatus) valid() bool {
	if !status.State.valid() || len(status.RationaleSummary) > agent.MaxReviewerRationaleBytes {
		return false
	}
	if status.Profile == "" {
		return status.OriginHash == "" && status.State == UIReviewerEscalated && status.RationaleSummary == ""
	}
	return domain.ValidModelToken(status.Profile, 128) && validPrivacyDigest(status.OriginHash)
}

func (rule UISessionPermissionRuleStatus) Validate() error {
	if !rule.ID.Valid() || !rule.Operation.Valid() || rule.Scope.Validate() != nil || !rule.NamespaceAccess.Valid() ||
		!rule.PolicyGeneration.Valid() ||
		rule.Target == "" || len(rule.Target) > 1024 || len(rule.TargetSubresource) > 64 ||
		rule.ParameterSummary == "" || len(rule.ParameterSummary) > MaxApprovalDisplaySummaryBytes ||
		!rule.Effect.Valid() || rule.Risk != domain.RiskReview || !rule.DataCategories.Valid() ||
		!rule.AllowedSinks.Valid() || !rule.NetworkEffects.Valid() || rule.Limits.Validate() != nil ||
		rule.CreatedAtMillis <= 0 || rule.ExpiresAtMillis <= rule.CreatedAtMillis {
		return ErrInvalidUIEvent
	}
	requiresDestination := rule.NetworkEffects&(domain.ActionNetworkModelOrigin|domain.ActionNetworkDataSource|domain.ActionNetworkRemotePod|domain.ActionNetworkExternalCommand) != 0
	if requiresDestination != rule.NetworkDestinationHash.Valid() {
		return ErrInvalidUIEvent
	}
	return nil
}

func (result UIPermissionsResult) valid() error {
	if result.RequestID == 0 || !result.Permission.valid(true) || !result.Reviewer.valid(false) ||
		result.Changed && result.RuleCreated || result.Action != nil && !result.Action.valid() ||
		result.InterruptedRun != nil && (!result.InterruptedRun.valid() || result.InterruptedRun.Reason != domain.RunTerminalStaleGeneration ||
			!result.Changed && !result.RuleCreated) {
		return ErrInvalidUIEvent
	}
	if result.Action != nil && result.Action.PolicyGeneration != result.Permission.PolicyGeneration {
		return ErrInvalidUIEvent
	}
	return nil
}

func projectUIPermissionStatus(status PermissionStatus) UIPermissionStatus {
	result := UIPermissionStatus{
		Configured: true, Profile: status.Profile, PolicyGeneration: status.PolicyGeneration,
		Healthy: status.Healthy, FullAccessAllowed: status.FullAccessAllowed,
		HighRiskAcknowledged: status.HighRiskAcknowledged,
		SessionRuleCount:     len(status.SessionRules),
		CustomRoutes:         make([]UICustomPermissionRoute, 0, len(status.CustomRoutes)),
		SessionRules:         make([]UISessionPermissionRuleStatus, 0, len(status.SessionRules)),
	}
	for _, route := range status.CustomRoutes {
		result.CustomRoutes = append(result.CustomRoutes, UICustomPermissionRoute{
			Operation: route.Operation, Risk: route.Risk, Disposition: route.Disposition,
		})
	}
	for _, rule := range status.SessionRules {
		result.SessionRules = append(result.SessionRules, projectUISessionPermissionRule(rule))
	}
	return result
}

func projectUISessionPermissionRule(rule SessionPermissionRuleStatus) UISessionPermissionRuleStatus {
	target := fmt.Sprintf("%s %s/%s* · API %s", rule.TargetKind, rule.TargetNamespace, rule.TargetPrefix, rule.TargetAPIVersion)
	if rule.TargetNamespace == "" {
		target = fmt.Sprintf("%s %s* · API %s", rule.TargetKind, rule.TargetPrefix, rule.TargetAPIVersion)
	}
	parameter := "kind=" + string(rule.ParameterKind)
	switch rule.ParameterKind {
	case domain.ActionParametersRemoteArgv, domain.ActionParametersLocalArgv:
		parameter = fmt.Sprintf("kind=%s executable=%q argv-prefix=%q", rule.ParameterKind, rule.Executable, rule.ArgumentPrefix.Values())
		if rule.Container != "" {
			parameter += fmt.Sprintf(" container=%q", rule.Container)
		}
		if rule.ParameterDigest.Valid() {
			parameter += " complete-parameter-digest=" + string(rule.ParameterDigest)
		}
	default:
		parameter += " digest=" + string(rule.ParameterDigest)
	}
	return UISessionPermissionRuleStatus{
		ID: rule.ID, Operation: rule.Operation, Scope: rule.Scope, NamespaceAccess: rule.NamespaceAccess,
		PolicyGeneration: rule.PolicyGeneration,
		Target:           target, TargetSubresource: rule.TargetSubresource, ParameterSummary: parameter,
		Effect: rule.Effect, Risk: rule.Risk, DataCategories: rule.DataCategories,
		AllowedSinks: rule.AllowedSinks, NetworkEffects: rule.NetworkEffects,
		NetworkDestinationHash: rule.NetworkDestinationHash, Limits: rule.Limits,
		CreatedAtMillis: rule.CreatedAt.UnixMilli(), ExpiresAtMillis: rule.ExpiresAt.UnixMilli(),
	}
}

func (status UIModelContextStatus) valid(hasSession bool) bool {
	if !hasSession {
		return status == (UIModelContextStatus{})
	}
	if status.Mode != domain.PrivacyModeStandard && status.Mode != domain.PrivacyModeMinimal ||
		status.EligibleMessages < 0 || status.EligibleMessages > domain.MaxSessionContextMessages ||
		status.EligibleBytes < 0 || status.EligibleBytes > domain.MaxSessionHistoryBytes ||
		status.RecentTailMessages < 0 || status.RecentTailMessages > status.EligibleMessages ||
		!validBudgetCounter(status.SummaryCallsUsed, status.SummaryCallsMaximum) || !status.Pressure.valid() ||
		status.WorkingMessages < 0 || status.WorkingMessages > status.EligibleMessages ||
		status.WorkingBytes < 0 || status.WorkingBytes > status.EligibleBytes ||
		status.MessageLimit != domain.MaxSessionContextMessages || status.ByteLimit != domain.MaxSessionHistoryBytes ||
		status.SummaryMessageTrigger != domain.SessionContextMessageTrigger ||
		status.SummaryByteTrigger != domain.MaxSessionContextBytes ||
		(status.Pressure == ContextPressureDegraded) == status.StorageHealthy {
		return false
	}
	return status.Compressed == status.CoveredThroughMessageID.Valid() &&
		status.Compressed == (status.CompressedAtUnixMillis > 0)
}

func (status UIModelRoleStatus) valid(required bool) bool {
	if !status.Configured {
		if !required {
			return status == (UIModelRoleStatus{})
		}
		return status.Role == domain.ModelRoleAgent && status.Profile == "" && status.OriginHash == "" &&
			!status.Available && !status.Consented
	}
	return status.Role.Valid() && domain.ValidModelToken(status.Profile, 128) && validPrivacyDigest(status.OriginHash) &&
		(!status.Consented || status.Available)
}

func (status UIBudgetStatus) valid() bool {
	if status.ModelEvidenceBasis != ModelBudgetEvidenceBasis || !status.Profile.Valid() ||
		status.RunMilliseconds <= 0 || status.ElapsedMilliseconds < 0 ||
		status.RemainingMilliseconds < 0 || status.ElapsedMilliseconds > status.RunMilliseconds ||
		status.RemainingMilliseconds > status.RunMilliseconds ||
		status.ElapsedMilliseconds+status.RemainingMilliseconds != status.RunMilliseconds {
		return false
	}
	return validFineGrainedBudget(status.FineGrained) &&
		validBudgetCounter(status.StepsUsed, status.StepsMaximum) &&
		validBudgetCounter(status.ToolCallsUsed, status.ToolCallsMaximum) &&
		validBudgetCounter(status.ModelCallsUsed, status.ModelCallsMaximum) &&
		validBudgetCounter(status.ModelCostUnitsUsed, status.ModelCostUnitsMaximum) &&
		validBudgetCounter(status.SummaryCallsUsed, status.SummaryCallsMaximum) &&
		validBudgetCounter(status.SummaryCostUnitsUsed, status.SummaryCostUnitsMaximum) &&
		validBudgetCounter(status.ReviewerCallsUsed, status.ReviewerCallsMaximum) &&
		validBudgetCounter(status.ReviewerCostUnitsUsed, status.ReviewerCostUnitsMaximum) &&
		validBudgetCounter(status.ToolResultBytesUsed, status.ToolResultBytesMaximum) &&
		validBudgetCounter(status.LogCallsUsed, status.LogCallsMaximum) &&
		status.LogContainersMaximum > 0 && status.LogContainersMaximum <= domain.MaxObservabilityLogContainers &&
		status.LogBytesMaximum > 0 && status.LogBytesMaximum <= domain.MaxObservabilityBytes &&
		status.EventPagesMaximum > 0 && status.EventPagesMaximum <= domain.MaxObservabilityPages &&
		status.EventPageItemsMaximum > 0 && status.EventPageItemsMaximum <= domain.MaxObservabilityLines &&
		status.EventPageBytesMaximum > 0 && status.EventPageBytesMaximum <= domain.MaxObservabilityBytes &&
		status.EventBytesMaximum >= status.EventPageBytesMaximum && status.EventBytesMaximum <= domain.MaxObservabilityBytes &&
		validBudgetCounter(status.MetricCallsUsed, status.MetricCallsMaximum) &&
		status.MetricContainersMaximum > 0 && status.MetricContainersMaximum <= domain.MaxMetricContainers &&
		status.MetricBytesMaximum > 0 && status.MetricBytesMaximum <= domain.MaxObservabilityBytes &&
		validBudgetCounter(status.DataSourceCallsUsed, status.DataSourceCallsMaximum) &&
		validBudgetCounter(status.RemoteExecCallsUsed, status.RemoteExecCallsMaximum) &&
		validBudgetCounter(status.LocalProcessCallsUsed, status.LocalProcessCallsMaximum) &&
		status.DataSourcePagesMaximum > 0 && status.DataSourcePagesMaximum <= domain.MaxObservabilityPages &&
		status.DataSourceSeriesMaximum > 0 && status.DataSourceSeriesMaximum <= domain.MaxObservabilitySeries &&
		status.DataSourceSamplesMaximum > 0 && status.DataSourceSamplesMaximum <= domain.MaxObservabilitySamples &&
		status.DataSourceLinesMaximum > 0 && status.DataSourceLinesMaximum <= domain.MaxObservabilityLines &&
		status.DataSourceBytesMaximum > 0 && status.DataSourceBytesMaximum <= domain.MaxObservabilityBytes &&
		status.DataSourceWindowMillis > 0 && status.DataSourceWindowMillis <= domain.MaxObservabilityWindow.Milliseconds() &&
		status.DataSourceStepMillis > 0 && status.DataSourceStepMillis <= domain.MaxObservabilityStep.Milliseconds() &&
		status.ResourcePagesMaximum > 0 && status.ResourcePagesMaximum <= domain.MaxResourceQueryPages &&
		status.ResourcePageItemsMaximum > 0 && status.ResourcePageItemsMaximum <= domain.MaxResourcePageItems &&
		status.ResourcePageBytesMaximum > 0 && status.ResourcePageBytesMaximum <= domain.MaxResourcePageBytes &&
		status.ResourceScannedMaximum > 0 && status.ResourceScannedMaximum <= domain.MaxResourceQueryItems &&
		status.ResourceReturnedMaximum > 0 && status.ResourceReturnedMaximum <= domain.MaxResourceSummaries &&
		status.ResourceBytesMaximum > 0 && status.ResourceBytesMaximum <= domain.MaxResourceQueryBytes &&
		status.ResourcePageItemsMaximum <= status.ResourceScannedMaximum &&
		status.ResourceReturnedMaximum <= status.ResourceScannedMaximum &&
		status.ResourcePageBytesMaximum <= status.ResourceBytesMaximum
}

func validFineGrainedBudget(values []UIBudgetMeasure) bool {
	want := [...]UIBudgetCategory{
		UIBudgetModelInputBytes, UIBudgetModelOutputBytes, UIBudgetSummaryReserveBytes,
		UIBudgetModelAttempts, UIBudgetToolCalls, UIBudgetExternalReadCalls,
		UIBudgetEvidenceItems, UIBudgetEvidenceBytes, UIBudgetPages, UIBudgetLines,
		UIBudgetSamples, UIBudgetWallMilliseconds, UIBudgetIdleMilliseconds,
		UIBudgetQueueItems, UIBudgetQueueBytes, UIBudgetContinuationAttempts, UIBudgetContinuationTime,
	}
	if len(values) != len(want) {
		return false
	}
	for index, measure := range values {
		if measure.Category != want[index] || measure.Used < 0 || measure.Limit < 0 {
			return false
		}
		switch measure.Basis {
		case UIBudgetMeasured, UIBudgetReserved, UIBudgetEstimated:
			if measure.Used > measure.Limit {
				return false
			}
		case UIBudgetConfigured:
			if measure.Used != 0 {
				return false
			}
		case UIBudgetUnavailable:
			if measure.Used != 0 || measure.Limit != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ModelBudgetEvidenceBasis is the exact content-free status value used while
// no selected-endpoint token or monetary-cost evidence is available.
const ModelBudgetEvidenceBasis = "bytes_calls_time_cost_units_no_endpoint_token_claim"

func validBudgetCounter(used, maximum int) bool {
	return used >= 0 && maximum > 0 && used <= maximum
}

// NewUIBudgetMeasures returns the fixed ordered content-free projection for a
// validated runtime profile. Delivery tests use the same catalog.
func NewUIBudgetMeasures(limits agent.RunBudgetLimits) []UIBudgetMeasure {
	if limits.Validate() != nil {
		return nil
	}
	return []UIBudgetMeasure{
		{Category: UIBudgetModelInputBytes, Limit: int64(limits.ModelCalls+limits.SummaryCalls) * int64(limits.ModelRequestBytes), Basis: UIBudgetMeasured},
		{Category: UIBudgetModelOutputBytes, Basis: UIBudgetUnavailable},
		{Category: UIBudgetSummaryReserveBytes, Limit: int64(limits.SummaryCalls) * int64(limits.SummaryRequestBytes+limits.SummaryOutputBytes), Basis: UIBudgetReserved},
		{Category: UIBudgetModelAttempts, Limit: int64(limits.ModelCalls), Basis: UIBudgetMeasured},
		{Category: UIBudgetToolCalls, Limit: int64(limits.ToolCalls), Basis: UIBudgetMeasured},
		{Category: UIBudgetExternalReadCalls, Limit: int64(limits.ToolCalls), Basis: UIBudgetMeasured},
		{Category: UIBudgetEvidenceItems, Limit: int64(limits.ToolCalls * domain.MaxEvidenceItemsPerResult), Basis: UIBudgetMeasured},
		{Category: UIBudgetEvidenceBytes, Basis: UIBudgetUnavailable},
		{Category: UIBudgetPages, Basis: UIBudgetUnavailable},
		{Category: UIBudgetLines, Basis: UIBudgetUnavailable},
		{Category: UIBudgetSamples, Basis: UIBudgetUnavailable},
		{Category: UIBudgetWallMilliseconds, Limit: limits.RunDuration.Milliseconds(), Basis: UIBudgetMeasured},
		{Category: UIBudgetIdleMilliseconds, Basis: UIBudgetUnavailable},
		{Category: UIBudgetQueueItems, Limit: int64(MaxConversationInputItems), Basis: UIBudgetMeasured},
		{Category: UIBudgetQueueBytes, Limit: int64(MaxConversationInputAggregateBytes), Basis: UIBudgetMeasured},
		{Category: UIBudgetContinuationAttempts, Basis: UIBudgetMeasured},
		{Category: UIBudgetContinuationTime, Basis: UIBudgetMeasured},
	}
}

func setBudgetMeasure(values []UIBudgetMeasure, category UIBudgetCategory, used int64, basis UIBudgetValueBasis) {
	for index := range values {
		if values[index].Category == category {
			values[index].Used = used
			values[index].Basis = basis
			return
		}
	}
}

func setBudgetUnavailable(values []UIBudgetMeasure, category UIBudgetCategory) {
	for index := range values {
		if values[index].Category == category {
			values[index].Used = 0
			values[index].Limit = 0
			values[index].Basis = UIBudgetUnavailable
			return
		}
	}
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
	UIEventRunStarted        UIEventKind = "run_started"
	UIEventModelEgress       UIEventKind = "model_egress_preflight"
	UIEventTextDelta         UIEventKind = "text_delta"
	UIEventToolStep          UIEventKind = "tool_step"
	UIEventRunCompleted      UIEventKind = "run_completed"
	UIEventRunFailed         UIEventKind = "run_failed"
	UIEventRunCancelled      UIEventKind = "run_cancelled"
	UIEventValidationWarning UIEventKind = "answer_validation_warning"
	// UIEventPersistenceDegraded is a visible nonterminal warning. It never
	// claims that incomplete data is resumable.
	UIEventPersistenceDegraded UIEventKind = "persistence_degraded"
	UIEventApprovalRequested   UIEventKind = "approval_requested"
	UIEventApprovalClosed      UIEventKind = "approval_closed"
	UIEventReviewerState       UIEventKind = "reviewer_state"
	UIEventRestartExecution    UIEventKind = "restart_execution"
	UIEventConversationInput   UIEventKind = "conversation_input"
)

const (
	// MaxAnswerMarkdownBytes is the delivery ceiling for a validated final answer.
	// It is intentionally distinct from the user-question ceiling.
	MaxAnswerMarkdownBytes = agent.MaxAnswerMarkdownBytes
	maxUIEventSequence     = 4096
)

const answerValidationWarningText = "Kupilot removed unsupported final-answer metadata. Review the remaining Evidence and proposed-action state before relying on affected claims."

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

// UIContextSummaryState distinguishes an absent retained summary from an
// already verified or newly generated summary without exposing its text.
type UIContextSummaryState string

const (
	UIContextSummaryAbsent    UIContextSummaryState = "absent"
	UIContextSummaryRetained  UIContextSummaryState = "retained"
	UIContextSummaryGenerated UIContextSummaryState = "generated"
)

// UIModelEgressPreflight is a content-free projection accepted before the
// adapter can invoke its transport. It is visibility, never authority.
type UIModelEgressPreflight struct {
	CallKind             agent.ModelCallKind
	Role                 domain.ModelRole
	OriginHash           string
	Consented            bool
	ContextMode          domain.PrivacyMode
	RunMode              agent.RunMode
	MessageCount         int
	MessageBytes         int
	SummaryState         UIContextSummaryState
	RecentTailMessages   int
	EligibleCategories   []ModelDataCategory
	ReservedRequestBytes int
	ReservedOutputBytes  int
	ReservedStreamBytes  int
	ReservedNanoseconds  int64
	ReservedCostUnits    int
	BudgetProfile        agent.BudgetProfile
}

// UINextAction is a fixed content-free action hint. It never mints retry,
// execution, approval, or recovery authority.
type UINextAction string

const (
	UINextAskNewQuestion     UINextAction = "ask_new_question"
	UINextProvideInput       UINextAction = "provide_explicit_input"
	UINextEditRecoveredInput UINextAction = "edit_recovered_input"
	UINextInspectEvidence    UINextAction = "inspect_evidence"
	UINextReviewScopePolicy  UINextAction = "review_scope_policy"
	UINextReviewBudget       UINextAction = "review_budget"
	UINextRunDoctor          UINextAction = "run_doctor"
	UINextSubmitExplicitly   UINextAction = "submit_explicitly"
	UINextDoNotRetryBlindly  UINextAction = "do_not_retry_blindly"
)

// UITerminalOutcome is Application-authoritative and deliberately contains no
// dynamic error, scope, resource, or answer text.
type UITerminalOutcome struct {
	Reason      domain.RunTerminalReason
	NextActions []UINextAction
	Budget      []UIBudgetMeasure
}

// UIAnswerCoverageState is the fixed top-level declared source condition.
type UIAnswerCoverageState string

const (
	UIAnswerCoverageComplete    UIAnswerCoverageState = "complete"
	UIAnswerCoveragePartial     UIAnswerCoverageState = "partial"
	UIAnswerCoverageTruncated   UIAnswerCoverageState = "truncated"
	UIAnswerCoverageUnavailable UIAnswerCoverageState = "unavailable"
)

// UIAnswerProvenance is a bounded metadata strip for one committed final. It
// intentionally excludes Evidence payloads and claim text.
type UIAnswerProvenance struct {
	EvidenceCount        int
	ObservedFrom         *time.Time
	ObservedThrough      *time.Time
	ScopeGeneration      int64
	PolicyGeneration     domain.PolicyGeneration
	CoverageState        UIAnswerCoverageState
	HasInference         bool
	HasUncertainty       bool
	HasConflict          bool
	HasSuperseded        bool
	CheckedSourceCount   int
	UncheckedSourceCount int
}

func (provenance UIAnswerProvenance) valid(runScope int64, policy domain.PolicyGeneration) bool {
	if provenance.EvidenceCount < 0 || provenance.EvidenceCount > 100 || provenance.ScopeGeneration != runScope ||
		provenance.PolicyGeneration != policy || provenance.CheckedSourceCount < 0 ||
		provenance.UncheckedSourceCount < 0 || provenance.CheckedSourceCount+provenance.UncheckedSourceCount > domain.MaxAnswerSources ||
		(provenance.ObservedFrom == nil) != (provenance.ObservedThrough == nil) {
		return false
	}
	if provenance.ObservedFrom != nil && (!validCoordinatorTime(*provenance.ObservedFrom) ||
		!validCoordinatorTime(*provenance.ObservedThrough) || provenance.ObservedThrough.Before(*provenance.ObservedFrom)) {
		return false
	}
	switch provenance.CoverageState {
	case UIAnswerCoverageComplete, UIAnswerCoveragePartial, UIAnswerCoverageTruncated, UIAnswerCoverageUnavailable:
		return true
	default:
		return false
	}
}

// ProjectTerminalOutcome returns the sole fixed next-action projection for a
// terminal reason.
func ProjectTerminalOutcome(reason domain.RunTerminalReason) (UITerminalOutcome, error) {
	actions := terminalNextActions(reason)
	if !reason.Valid() || len(actions) == 0 || len(actions) > 3 {
		return UITerminalOutcome{}, ErrInvalidUIEvent
	}
	return UITerminalOutcome{Reason: reason, NextActions: actions, Budget: NewUnavailableUIBudgetMeasures()}, nil
}

func (outcome UITerminalOutcome) valid() bool {
	want, err := ProjectTerminalOutcome(outcome.Reason)
	if err != nil || len(want.NextActions) != len(outcome.NextActions) || !validFineGrainedBudget(outcome.Budget) {
		return false
	}
	for index := range want.NextActions {
		if want.NextActions[index] != outcome.NextActions[index] {
			return false
		}
	}
	return true
}

// NewUnavailableUIBudgetMeasures returns every fixed category without
// inventing usage or ceilings when a terminal invalidation has no run-local
// accounting snapshot.
func NewUnavailableUIBudgetMeasures() []UIBudgetMeasure {
	categories := [...]UIBudgetCategory{
		UIBudgetModelInputBytes, UIBudgetModelOutputBytes, UIBudgetSummaryReserveBytes,
		UIBudgetModelAttempts, UIBudgetToolCalls, UIBudgetExternalReadCalls,
		UIBudgetEvidenceItems, UIBudgetEvidenceBytes, UIBudgetPages, UIBudgetLines,
		UIBudgetSamples, UIBudgetWallMilliseconds, UIBudgetIdleMilliseconds,
		UIBudgetQueueItems, UIBudgetQueueBytes, UIBudgetContinuationAttempts, UIBudgetContinuationTime,
	}
	result := make([]UIBudgetMeasure, len(categories))
	for index, category := range categories {
		result[index] = UIBudgetMeasure{Category: category, Basis: UIBudgetUnavailable}
	}
	return result
}

func terminalNextActions(reason domain.RunTerminalReason) []UINextAction {
	switch reason {
	case domain.RunTerminalCompleted:
		return []UINextAction{UINextAskNewQuestion}
	case domain.RunTerminalNeedsUserInput:
		return []UINextAction{UINextProvideInput}
	case domain.RunTerminalCancelled:
		return []UINextAction{UINextEditRecoveredInput, UINextSubmitExplicitly}
	case domain.RunTerminalRecovered:
		return []UINextAction{UINextEditRecoveredInput, UINextSubmitExplicitly}
	case domain.RunTerminalTimedOut, domain.RunTerminalBudgetExhausted:
		return []UINextAction{UINextReviewBudget, UINextSubmitExplicitly}
	case domain.RunTerminalUnknown:
		return []UINextAction{UINextDoNotRetryBlindly, UINextRunDoctor}
	case domain.RunTerminalPersistenceDegraded:
		return []UINextAction{UINextRunDoctor, UINextDoNotRetryBlindly}
	case domain.RunTerminalStaleGeneration, domain.RunTerminalPolicyDenied:
		return []UINextAction{UINextReviewScopePolicy, UINextSubmitExplicitly}
	case domain.RunTerminalInsufficientEvidence, domain.RunTerminalConflictingEvidence, domain.RunTerminalPartialResult:
		return []UINextAction{UINextInspectEvidence, UINextSubmitExplicitly}
	case domain.RunTerminalSourceUnavailable:
		return []UINextAction{UINextRunDoctor, UINextSubmitExplicitly}
	case domain.RunTerminalFailed:
		return []UINextAction{UINextRunDoctor, UINextSubmitExplicitly}
	default:
		return nil
	}
}

func (projection UIModelEgressPreflight) valid() bool {
	if projection.CallKind != agent.ModelCallAgent && projection.CallKind != agent.ModelCallSummary ||
		projection.Role != domain.ModelRoleAgent || !validPrivacyDigest(projection.OriginHash) || !projection.Consented ||
		(projection.ContextMode != domain.PrivacyModeStandard && projection.ContextMode != domain.PrivacyModeMinimal) ||
		!projection.RunMode.Valid() || projection.MessageCount < 1 || projection.MessageCount > agent.MaxModelPreflightMessages ||
		projection.MessageBytes < 1 || projection.MessageBytes > domain.MaxModelRequestBytes ||
		projection.ReservedRequestBytes < projection.MessageBytes || projection.ReservedRequestBytes > domain.MaxModelRequestBytes ||
		projection.ReservedOutputBytes < 1 || projection.ReservedOutputBytes > domain.MaxModelMessageBytes ||
		projection.ReservedStreamBytes < 0 || projection.ReservedStreamBytes > domain.MaxModelStreamBytes ||
		projection.ReservedNanoseconds < 1 || projection.ReservedCostUnits < 1 || !projection.BudgetProfile.Valid() ||
		projection.RecentTailMessages < 0 || projection.RecentTailMessages > domain.MaxSessionContextMessages ||
		!validEgressCategories(projection.EligibleCategories) {
		return false
	}
	switch projection.SummaryState {
	case UIContextSummaryAbsent:
	case UIContextSummaryRetained, UIContextSummaryGenerated:
		if projection.ContextMode != domain.PrivacyModeStandard {
			return false
		}
	default:
		return false
	}
	return true
}

func validEgressCategories(values []ModelDataCategory) bool {
	if len(values) < 1 || len(values) > len(privacyCategoryCatalog) {
		return false
	}
	position := make(map[ModelDataCategory]int, len(privacyCategoryCatalog))
	for index, definition := range privacyCategoryCatalog {
		position[definition.ID] = index
	}
	previous := -1
	for _, value := range values {
		current, exists := position[value]
		if !exists || current <= previous {
			return false
		}
		previous = current
	}
	return true
}

// UIEvent carries publisher-owned identity and exactly one projected payload.
type UIEvent struct {
	Kind               UIEventKind
	RunID              domain.AgentRunID
	ScopeGeneration    int64
	PolicyGeneration   domain.PolicyGeneration
	Sequence           int64
	Text               string
	EvidenceReferences []UIEvidenceReference
	ToolStep           *ToolStep
	Approval           *UIApprovalRequest
	ApprovalResult     *UIApprovalResult
	Reviewer           *UIReviewerEvent
	RestartExecution   *UIRestartExecution
	ConversationInput  *UIConversationInputEvent
	ModelEgress        *UIModelEgressPreflight
	TerminalOutcome    *UITerminalOutcome
	AnswerProvenance   *UIAnswerProvenance
}

// UIConversationInputEvent is a revisioned, bounded working-area snapshot.
// Its content is never a committed transcript or status payload.
type UIConversationInputEvent struct {
	Revision int64
	Status   ConversationInputStatus
	Changed  *ConversationInputProjection
	Preview  []ConversationInputProjection
}

func (event UIConversationInputEvent) valid() bool {
	if event.Revision < 1 || event.Status.Revision != event.Revision || !event.Status.valid() ||
		len(event.Preview) > MaxConversationInputPreviewItems {
		return false
	}
	if event.Changed != nil && (!event.Changed.validate() || event.Changed.Revision > event.Revision) {
		return false
	}
	seen := make(map[domain.MessageID]struct{}, len(event.Preview))
	for _, item := range event.Preview {
		if !item.validate() || item.Revision > event.Revision || item.State == ConversationInputCommitted ||
			item.State == conversationInputDraining {
			return false
		}
		if _, duplicate := seen[item.ItemID]; duplicate {
			return false
		}
		seen[item.ItemID] = struct{}{}
	}
	return true
}

// Terminal reports whether later events for the same run must be ignored.
func (event UIEvent) Terminal() bool {
	return event.Kind == UIEventRunCompleted || event.Kind == UIEventRunFailed || event.Kind == UIEventRunCancelled
}

// Validate checks identity, payload exclusivity, and fixed event states.
func (event UIEvent) Validate() error {
	if event.Kind != UIEventModelEgress && event.ModelEgress != nil {
		return ErrInvalidUIEvent
	}
	if !event.Terminal() && event.TerminalOutcome != nil {
		return ErrInvalidUIEvent
	}
	if event.Kind != UIEventRunCompleted && event.AnswerProvenance != nil {
		return ErrInvalidUIEvent
	}
	if event.Kind == UIEventConversationInput {
		if !event.RunID.Valid() || event.ScopeGeneration < 1 || !event.PolicyGeneration.Valid() ||
			event.Sequence != 0 || event.Text != "" || len(event.EvidenceReferences) != 0 || event.ToolStep != nil ||
			event.Approval != nil || event.ApprovalResult != nil || event.Reviewer != nil || event.RestartExecution != nil ||
			event.ConversationInput == nil || !event.ConversationInput.valid() {
			return ErrInvalidUIEvent
		}
		return nil
	}
	if !event.RunID.Valid() || event.ScopeGeneration < 1 || !event.PolicyGeneration.Valid() ||
		event.Sequence < 1 || event.Sequence > maxUIEventSequence || event.ConversationInput != nil {
		return ErrInvalidUIEvent
	}
	switch event.Kind {
	case UIEventRunStarted:
		if event.Text != "" && !validUICommandText(event.Text, MaxQuestionBytes) || len(event.EvidenceReferences) != 0 || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult != nil || event.Reviewer != nil || event.RestartExecution != nil {
			return ErrInvalidUIEvent
		}
	case UIEventModelEgress:
		if event.Text != "" || len(event.EvidenceReferences) != 0 || event.ToolStep != nil || event.Approval != nil ||
			event.ApprovalResult != nil || event.Reviewer != nil || event.RestartExecution != nil ||
			event.ModelEgress == nil || !event.ModelEgress.valid() {
			return ErrInvalidUIEvent
		}
	case UIEventRunCompleted:
		if event.Text == "" || len(event.Text) > MaxAnswerMarkdownBytes || len(event.EvidenceReferences) > 100 || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult != nil || event.Reviewer != nil || event.RestartExecution != nil ||
			event.TerminalOutcome == nil || !event.TerminalOutcome.valid() || event.AnswerProvenance == nil ||
			!event.AnswerProvenance.valid(event.ScopeGeneration, event.PolicyGeneration) {
			return ErrInvalidUIEvent
		}
		for _, reference := range event.EvidenceReferences {
			if reference.Validate() != nil || reference.RunID != event.RunID ||
				reference.Scope.Generation != event.ScopeGeneration || reference.Sequence != event.Sequence {
				return ErrInvalidUIEvent
			}
		}
	case UIEventTextDelta:
		if event.Text == "" || len(event.Text) > MaxAnswerMarkdownBytes || len(event.EvidenceReferences) != 0 || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult != nil || event.Reviewer != nil || event.RestartExecution != nil {
			return ErrInvalidUIEvent
		}
	case UIEventRunFailed, UIEventRunCancelled:
		if event.Text == "" || len(event.Text) > MaxQuestionBytes || len(event.EvidenceReferences) != 0 || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult != nil || event.Reviewer != nil || event.RestartExecution != nil ||
			event.TerminalOutcome == nil || !event.TerminalOutcome.valid() {
			return ErrInvalidUIEvent
		}
	case UIEventValidationWarning, UIEventPersistenceDegraded:
		if event.Text == "" || len(event.Text) > MaxQuestionBytes || len(event.EvidenceReferences) != 0 || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult != nil || event.Reviewer != nil || event.RestartExecution != nil {
			return ErrInvalidUIEvent
		}
	case UIEventToolStep:
		if event.Text != "" || len(event.EvidenceReferences) != 0 || event.ToolStep == nil || !event.ToolStep.valid() || event.Approval != nil || event.ApprovalResult != nil || event.Reviewer != nil || event.RestartExecution != nil {
			return ErrInvalidUIEvent
		}
	case UIEventApprovalRequested:
		if event.Text != "" || len(event.EvidenceReferences) != 0 || event.ToolStep != nil || event.Approval == nil || event.ApprovalResult != nil || event.Reviewer != nil || event.RestartExecution != nil ||
			event.Approval.Validate() != nil || event.Approval.RunID != event.RunID ||
			event.Approval.Scope.Generation != event.ScopeGeneration || event.Approval.PolicyGeneration != event.PolicyGeneration || event.Approval.Sequence != event.Sequence {
			return ErrInvalidUIEvent
		}
	case UIEventApprovalClosed:
		if event.Text != "" || len(event.EvidenceReferences) != 0 || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult == nil || event.Reviewer != nil || event.RestartExecution != nil ||
			event.ApprovalResult.Validate() != nil || event.ApprovalResult.RunID != event.RunID ||
			event.ApprovalResult.ScopeGeneration != event.ScopeGeneration || event.ApprovalResult.PolicyGeneration != event.PolicyGeneration || event.ApprovalResult.Sequence != event.Sequence {
			return ErrInvalidUIEvent
		}
	case UIEventReviewerState:
		if event.Text != "" || len(event.EvidenceReferences) != 0 || event.ToolStep != nil || event.Approval != nil ||
			event.ApprovalResult != nil || event.Reviewer == nil || event.RestartExecution != nil ||
			event.Reviewer.Validate() != nil || event.Reviewer.RunID != event.RunID ||
			event.Reviewer.ScopeGeneration != event.ScopeGeneration || event.Reviewer.PolicyGeneration != event.PolicyGeneration ||
			event.Reviewer.Sequence != event.Sequence {
			return ErrInvalidUIEvent
		}
	case UIEventRestartExecution:
		if event.Text != "" || len(event.EvidenceReferences) != 0 || event.ToolStep != nil || event.Approval != nil || event.ApprovalResult != nil || event.Reviewer != nil ||
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

const (
	approvalProposedSummary = "Update only the Kupilot-owned restart annotation to create a new Pod template revision."
	// MaxApprovalDisplaySummaryBytes covers the complete bounded executable,
	// argv, cwd, environment, and shell fields without UI truncation.
	MaxApprovalDisplaySummaryBytes = 32768
)

// UIApprovalRequest is the complete safe dialog projection. The opaque nonce
// is memory-only decision authority whose type cannot render or marshal bytes.
type UIApprovalRequest struct {
	RequestID              domain.ApprovalID
	RunID                  domain.AgentRunID
	SessionID              domain.SessionID
	Sequence               int64
	Operation              domain.ApprovalOperation
	OperationSchema        string
	PolicyVersion          string
	PermissionProfile      domain.PermissionProfile
	PolicyGeneration       domain.PolicyGeneration
	Risk                   domain.RiskClass
	Effect                 domain.CapabilityEffectClass
	Scope                  domain.ScopeSnapshot
	NamespaceAccess        domain.NamespaceAccessPolicy
	Target                 domain.ResourceRef
	TargetSubresource      string
	TemplateFingerprint    string
	DeploymentGeneration   int64
	TargetRevision         int64
	TargetSetDigest        domain.ActionDigest
	TargetCount            int
	Parameters             domain.ActionParameters
	Stdin                  bool
	TTY                    bool
	Shell                  bool
	DataCategories         domain.ActionDataCategories
	AllowedSinks           domain.ActionSinks
	NetworkEffects         domain.ActionNetworkEffects
	NetworkDestinationHash domain.ActionDigest
	Limits                 domain.ActionLimits
	VerificationPlanID     string
	ReasonSummary          string
	RiskSummary            string
	CurrentSummary         string
	ProposedSummary        string
	ParameterSummary       string
	EffectSummary          string
	Reviewer               *UIReviewerStatus
	Digest                 domain.ApprovalDigest
	Nonce                  domain.ApprovalNonce
	RequestedAt            time.Time
	ExpiresAt              time.Time
}

// Validate recomputes the operation digest from every displayed parameter.
func (request UIApprovalRequest) Validate() error {
	intent := domain.OperationIntent{
		Operation: request.Operation, OperationSchemaVersion: request.OperationSchema,
		PolicyVersion: request.PolicyVersion, PermissionProfile: request.PermissionProfile,
		Risk: request.Risk, Effect: request.Effect, PolicyGeneration: request.PolicyGeneration,
		Scope: request.Scope, NamespaceAccess: request.NamespaceAccess,
		Target: domain.ActionTarget{
			Resource: request.Target, Subresource: request.TargetSubresource,
			Fingerprint: request.TemplateFingerprint, Generation: request.DeploymentGeneration,
			Revision: request.TargetRevision, TargetSetDigest: request.TargetSetDigest, TargetCount: request.TargetCount,
		},
		Parameters: request.Parameters, Stdin: request.Stdin, TTY: request.TTY, Shell: request.Shell,
		DataCategories: request.DataCategories, AllowedSinks: request.AllowedSinks,
		NetworkEffects: request.NetworkEffects, NetworkDestinationHash: request.NetworkDestinationHash,
		Limits:             request.Limits,
		VerificationPlanID: request.VerificationPlanID, ReasonSummary: request.ReasonSummary,
		RiskSummary: request.RiskSummary,
	}
	domainRequest := domain.ApprovalRequest{
		ID: request.RequestID, RunID: request.RunID, SessionID: request.SessionID,
		Intent: intent, Digest: request.Digest, Nonce: request.Nonce,
		State: domain.ApprovalStatePending, RequestedAt: request.RequestedAt,
		ExpiresAt: request.ExpiresAt, StateChangedAt: request.RequestedAt,
	}
	wantCurrent, wantProposed := actionApprovalSummaries(intent)
	wantParameters := actionParameterSummary(intent.Parameters)
	wantEffects := actionEffectSummary(intent)
	digest, err := approval.OperationDigest(domainRequest)
	if request.Sequence < 1 || request.Sequence > 4096 || request.Target.Validate() != nil ||
		len(request.CurrentSummary) > MaxApprovalDisplaySummaryBytes || len(request.ProposedSummary) > MaxApprovalDisplaySummaryBytes ||
		!validApprovalActionIntent(intent) || request.CurrentSummary != wantCurrent ||
		request.ProposedSummary != wantProposed || request.ParameterSummary != wantParameters ||
		request.EffectSummary != wantEffects || request.Reviewer != nil && !request.Reviewer.valid() ||
		domainRequest.Validate() != nil ||
		err != nil || !request.Digest.Equal(digest) {
		return ErrInvalidUIEvent
	}
	return nil
}

func validApprovalActionIntent(intent domain.ActionIntent) bool {
	switch intent.Operation {
	case domain.ActionOperationRestartDeployment:
		return intent.ValidateRestartDeployment() == nil
	case domain.ActionOperationScaleWorkload, domain.ActionOperationRollbackDeployment,
		domain.ActionOperationDeleteOwnedPod, domain.ActionOperationCordonNode,
		domain.ActionOperationUncordonNode, domain.ActionOperationDrainNode:
		return domain.ValidateRemediationIntent(intent) == nil
	case domain.ActionOperationRestrictedLocalArgv:
		return intent.ValidateLocalCommand() == nil
	case domain.ActionOperationShell:
		return intent.ValidateShellCommand() == nil
	case domain.ActionOperationPodExec, domain.ActionOperationContainerFileRead,
		domain.ActionOperationDiagnosticPod, domain.ActionOperationPodDiagnostic:
		return intent.Validate() == nil
	case domain.ActionOperationLogsCurrent, domain.ActionOperationLogsPrevious,
		domain.ActionOperationLogsAllContainers, domain.ActionOperationLogSearch,
		domain.ActionOperationPrometheusQuery, domain.ActionOperationLokiQuery:
		return intent.ValidateObservationAction() == nil
	default:
		return false
	}
}

func actionApprovalSummaries(intent domain.ActionIntent) (string, string) {
	resource := intent.Target.Resource
	identity := fmt.Sprintf("%s %s/%s with UID and resource version bound by digest.", resource.Kind, resource.Namespace, resource.Name)
	if resource.Namespace == "" {
		identity = fmt.Sprintf("%s %s with UID and resource version bound by digest.", resource.Kind, resource.Name)
	}
	switch intent.Operation {
	case domain.ActionOperationRestartDeployment:
		return fmt.Sprintf("Deployment generation %d with Pod template fingerprint %s.", intent.Target.Generation, intent.Target.Fingerprint), approvalProposedSummary
	case domain.ActionOperationScaleWorkload:
		return identity, fmt.Sprintf("Change replicas from %d to %d through the exact scale subresource.", intent.Parameters.ReplicaCurrent, intent.Parameters.ReplicaTarget)
	case domain.ActionOperationRollbackDeployment:
		return identity, fmt.Sprintf("Replace only the Deployment Pod template with bound prior revision %d.", intent.Parameters.Revision)
	case domain.ActionOperationDeleteOwnedPod:
		return identity, fmt.Sprintf("Delete only this controller-owned Pod with a %d-second grace period.", intent.Parameters.GracePeriodSeconds)
	case domain.ActionOperationCordonNode:
		return identity, "Set only spec.unschedulable=true on this Node."
	case domain.ActionOperationUncordonNode:
		return identity, "Set only spec.unschedulable=false on this Node."
	case domain.ActionOperationDrainNode:
		return identity, fmt.Sprintf("Cordon this Node and evict only the %d digest-bound plan members.", intent.Target.TargetCount)
	case domain.ActionOperationRestrictedLocalArgv:
		return fmt.Sprintf("Executable identity %s and working-directory identity %s are bound by digest.", intent.Parameters.ExecutableID, intent.Parameters.WorkingDirectoryID),
			fmt.Sprintf("Direct executable=%q argv=%q cwd=%q environment=%q credential-reference=%q stdin=false tty=false shell=false network=%s. No OS filesystem or network sandbox is claimed.",
				intent.Parameters.Executable, intent.Parameters.Arguments.Values(), intent.Parameters.WorkingDirectory,
				intent.Parameters.Environment.Values(), intent.Parameters.CredentialReference, actionNetworkSummary(intent.NetworkEffects))
	case domain.ActionOperationShell:
		return fmt.Sprintf("Shell executable identity %s and working-directory identity %s are bound by digest.", intent.Parameters.ExecutableID, intent.Parameters.WorkingDirectoryID),
			fmt.Sprintf("Shell executable=%q command=%q cwd=%q environment=%q stdin=false tty=false shell=true network=%s. No OS filesystem or network sandbox is claimed.",
				intent.Parameters.Executable, intent.Parameters.ShellCommand, intent.Parameters.WorkingDirectory,
				intent.Parameters.Environment.Values(), actionNetworkSummary(intent.NetworkEffects))
	case domain.ActionOperationPodExec, domain.ActionOperationPodDiagnostic:
		return identity, "Execute only the displayed no-shell argv in the exact Pod container."
	case domain.ActionOperationContainerFileRead:
		return identity, "Read only the displayed normalized container path through the fixed no-shell reader."
	case domain.ActionOperationDiagnosticPod:
		return identity, "Create, observe, and clean up only the displayed policy-bound diagnostic Pod."
	case domain.ActionOperationLogsCurrent, domain.ActionOperationLogsPrevious,
		domain.ActionOperationLogsAllContainers, domain.ActionOperationLogSearch:
		return identity, "Read only the displayed bounded, sanitized log selection from this exact Pod."
	case domain.ActionOperationPrometheusQuery, domain.ActionOperationLokiQuery:
		return identity, "Send only the displayed code-owned query for this exact Pod to the configured origin bound by digest."
	default:
		return "", ""
	}
}

func actionNetworkSummary(effects domain.ActionNetworkEffects) string {
	values := make([]string, 0, 5)
	for _, value := range []struct {
		flag  domain.ActionNetworkEffects
		label string
	}{
		{domain.ActionNetworkKubernetesAPI, "Kubernetes API"},
		{domain.ActionNetworkModelOrigin, "model origin"},
		{domain.ActionNetworkDataSource, "configured data source"},
		{domain.ActionNetworkRemotePod, "exact remote Pod/Service"},
		{domain.ActionNetworkExternalCommand, "policy-bound command destination"},
	} {
		if effects&value.flag != 0 {
			values = append(values, value.label)
		}
	}
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func actionParameterSummary(parameters domain.ActionParameters) string {
	switch parameters.Kind {
	case domain.ActionParametersNone:
		return "none"
	case domain.ActionParametersReplicaTarget:
		return fmt.Sprintf("replicas %d -> %d", parameters.ReplicaCurrent, parameters.ReplicaTarget)
	case domain.ActionParametersRevision:
		return fmt.Sprintf("revision %d", parameters.Revision)
	case domain.ActionParametersNodeScheduling:
		return fmt.Sprintf("spec.unschedulable=%t", parameters.Unschedulable)
	case domain.ActionParametersPodDelete:
		return fmt.Sprintf("grace-period=%ds · force=false", parameters.GracePeriodSeconds)
	case domain.ActionParametersDrainPlan:
		return fmt.Sprintf("plan=%s · targets=%d · grace-period=%ds", parameters.PlanDigest, parameters.PlanTargetCount, parameters.GracePeriodSeconds)
	case domain.ActionParametersContainerFile:
		return fmt.Sprintf("container=%q path=%q executable=%q argv=%q", parameters.Container, parameters.NormalizedPath, parameters.Executable, parameters.Arguments.Values())
	case domain.ActionParametersRemoteArgv:
		return fmt.Sprintf("container=%q executable=%q argv=%q", parameters.Container, parameters.Executable, parameters.Arguments.Values())
	case domain.ActionParametersLocalArgv:
		return fmt.Sprintf("policy=%q executable=%q argv=%q cwd=%q environment=%q credential-reference=%q", parameters.PolicyID, parameters.Executable, parameters.Arguments.Values(), parameters.WorkingDirectory, parameters.Environment.Values(), parameters.CredentialReference)
	case domain.ActionParametersShellCommand:
		return fmt.Sprintf("policy=%q shell=%q command=%q cwd=%q environment=%q", parameters.PolicyID, parameters.Executable, parameters.ShellCommand, parameters.WorkingDirectory, parameters.Environment.Values())
	case domain.ActionParametersObservation:
		observation := parameters.Observation
		return fmt.Sprintf("kind=%s container=%q previous=%t all-containers=%t include-init=%t include-ephemeral=%t search=%q query=%q window=%ds step=%ds tail-lines=%d series-limit=%d line-limit=%d",
			observation.Kind, observation.Container, observation.Previous, observation.AllContainers,
			observation.IncludeInit, observation.IncludeEphemeral, observation.Search, observation.QueryID,
			observation.WindowSeconds, observation.StepSeconds, observation.TailLines,
			observation.SeriesLimit, observation.LineLimit)
	default:
		return "invalid"
	}
}

func actionEffectSummary(intent domain.ActionIntent) string {
	return fmt.Sprintf("effect=%s · data=%s · sinks=%s · network=%s · destination=%s · stdin=%t tty=%t shell=%t · timeout=%s · items=%d lines=%d bytes=%d output=%d",
		intent.Effect, actionDataSummary(intent.DataCategories), actionSinkSummary(intent.AllowedSinks),
		actionNetworkSummary(intent.NetworkEffects), optionalActionDigest(intent.NetworkDestinationHash),
		intent.Stdin, intent.TTY, intent.Shell, intent.Limits.Timeout,
		intent.Limits.MaximumItems, intent.Limits.MaximumLines, intent.Limits.MaximumBytes, intent.Limits.MaximumOutput)
}

func actionDataSummary(categories domain.ActionDataCategories) string {
	values := make([]string, 0, 6)
	for _, value := range []struct {
		flag  domain.ActionDataCategories
		label string
	}{
		{domain.ActionDataResourceMetadata, "resource metadata"},
		{domain.ActionDataProjectedStatus, "projected status"},
		{domain.ActionDataProjectedEvents, "projected events"},
		{domain.ActionDataContainerOutput, "container output"},
		{domain.ActionDataFileOutput, "file output"},
		{domain.ActionDataProcessOutput, "process output"},
	} {
		if categories&value.flag != 0 {
			values = append(values, value.label)
		}
	}
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func actionSinkSummary(sinks domain.ActionSinks) string {
	values := make([]string, 0, 4)
	for _, value := range []struct {
		flag  domain.ActionSinks
		label string
	}{
		{domain.ActionSinkTerminal, "terminal"},
		{domain.ActionSinkModel, "model"},
		{domain.ActionSinkKubernetesAPI, "Kubernetes API"},
		{domain.ActionSinkLocalProcess, "local process"},
	} {
		if sinks&value.flag != 0 {
			values = append(values, value.label)
		}
	}
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func optionalActionDigest(digest domain.ActionDigest) string {
	if digest == "" {
		return "none"
	}
	return string(digest)
}

// UIApprovalResult closes one exact dialog request without carrying its nonce.
type UIApprovalResult struct {
	RequestID        domain.ApprovalID
	RunID            domain.AgentRunID
	ScopeGeneration  int64
	PolicyGeneration domain.PolicyGeneration
	Sequence         int64
	Digest           domain.ApprovalDigest
	State            domain.ApprovalState
	StateReason      domain.ApprovalStateReason
	Execution        *UIRestartExecution
	ActionExecution  *UIActionExecution
}

// Validate checks one bounded non-pending dialog outcome.
func (result UIApprovalResult) Validate() error {
	if !result.RequestID.Valid() || !result.RunID.Valid() || result.ScopeGeneration < 1 || !result.PolicyGeneration.Valid() ||
		result.Sequence < 1 || result.Sequence > 4096 || !result.Digest.Valid() ||
		!validApprovalResultState(result.State, result.StateReason) {
		return ErrInvalidUIEvent
	}
	if result.State == domain.ApprovalStateConsumed {
		if (result.Execution == nil) == (result.ActionExecution == nil) {
			return ErrInvalidUIEvent
		}
		if result.Execution != nil && (result.Execution.Validate() != nil || !result.Execution.State.Terminal() ||
			result.Execution.RequestID != result.RequestID || result.Execution.RunID != result.RunID ||
			result.Execution.ScopeGeneration != result.ScopeGeneration || result.Execution.Sequence != result.Sequence ||
			!result.Execution.Digest.Equal(result.Digest)) {
			return ErrInvalidUIEvent
		}
		if result.ActionExecution != nil && (result.ActionExecution.Validate() != nil ||
			result.ActionExecution.RequestID != result.RequestID || result.ActionExecution.RunID != result.RunID ||
			result.ActionExecution.ScopeGeneration != result.ScopeGeneration || result.ActionExecution.Sequence != result.Sequence ||
			!result.ActionExecution.Digest.Equal(result.Digest)) {
			return ErrInvalidUIEvent
		}
	} else if result.Execution != nil || result.ActionExecution != nil {
		return ErrInvalidUIEvent
	}
	return nil
}

func validApprovalResultState(state domain.ApprovalState, reason domain.ApprovalStateReason) bool {
	switch state {
	case domain.ApprovalStateApproved:
		return reason == domain.ApprovalReasonUserApproved
	case domain.ApprovalStateRejected:
		return reason == domain.ApprovalReasonUserRejected || reason == domain.ApprovalReasonReviewerRejected
	case domain.ApprovalStateExpired:
		return reason == domain.ApprovalReasonTTLExpired
	case domain.ApprovalStateCancelled:
		return reason.ValidCancellation()
	case domain.ApprovalStateInvalidated:
		return reason == domain.ApprovalReasonScopeChanged || reason == domain.ApprovalReasonPolicyChanged ||
			reason == domain.ApprovalReasonTargetChanged ||
			reason == domain.ApprovalReasonDigestMismatch ||
			reason == domain.ApprovalReasonNonceMismatch || reason == domain.ApprovalReasonDecisionReplayed
	case domain.ApprovalStateConsumed:
		return reason == domain.ApprovalReasonConsumed
	default:
		return false
	}
}

func projectUIApprovalRequest(request domain.ApprovalRequest, sequence int64) UIApprovalRequest {
	projection := UIApprovalRequest{
		RequestID: request.ID, RunID: request.RunID, SessionID: request.SessionID, Sequence: sequence,
		Operation: request.Intent.Operation, OperationSchema: request.Intent.OperationSchemaVersion,
		PolicyVersion: request.Intent.PolicyVersion, PermissionProfile: request.Intent.PermissionProfile,
		PolicyGeneration: request.Intent.PolicyGeneration, Risk: request.Intent.Risk, Effect: request.Intent.Effect,
		Scope: request.Intent.Scope, NamespaceAccess: request.Intent.NamespaceAccess,
		Target:              request.Intent.Target.Resource,
		TargetSubresource:   request.Intent.Target.Subresource,
		TemplateFingerprint: request.Intent.Target.Fingerprint, DeploymentGeneration: request.Intent.Target.Generation,
		TargetRevision:  request.Intent.Target.Revision,
		TargetSetDigest: request.Intent.Target.TargetSetDigest,
		TargetCount:     request.Intent.Target.TargetCount,
		Parameters:      request.Intent.Parameters, Stdin: request.Intent.Stdin, TTY: request.Intent.TTY, Shell: request.Intent.Shell,
		DataCategories: request.Intent.DataCategories,
		AllowedSinks:   request.Intent.AllowedSinks, NetworkEffects: request.Intent.NetworkEffects,
		NetworkDestinationHash: request.Intent.NetworkDestinationHash,
		Limits:                 request.Intent.Limits, VerificationPlanID: request.Intent.VerificationPlanID,
		ReasonSummary: request.Intent.ReasonSummary, RiskSummary: request.Intent.RiskSummary,
		ParameterSummary: actionParameterSummary(request.Intent.Parameters),
		EffectSummary:    actionEffectSummary(request.Intent),
		Digest:           request.Digest, Nonce: request.Nonce,
		RequestedAt: request.RequestedAt, ExpiresAt: request.ExpiresAt,
	}
	projection.CurrentSummary, projection.ProposedSummary = actionApprovalSummaries(request.Intent)
	return projection
}

func projectUIApprovalResult(request domain.ApprovalRequest, sequence int64) UIApprovalResult {
	return UIApprovalResult{
		RequestID: request.ID, RunID: request.RunID, ScopeGeneration: request.Intent.Scope.Generation,
		PolicyGeneration: request.Intent.PolicyGeneration, Sequence: sequence, Digest: request.Digest,
		State: request.State, StateReason: request.StateReason,
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

const (
	uiDeltaFlushBytes    = 4 * 1024
	uiDeltaFlushInterval = 25 * time.Millisecond
	maxUIDeltaEvents     = 2 * 1024
)

// eventBridge is the synchronous Application-to-delivery coalescing boundary.
// It owns no goroutine or channel, never drops structural events, and assigns a
// UI-local sequence so coalesced Agent deltas cannot create ambiguous ordering.
type eventBridge struct {
	runID             domain.AgentRunID
	scopeGeneration   int64
	policyGeneration  domain.PolicyGeneration
	sink              UIEventSink
	sequence          int64
	publishedSequence atomic.Int64
	started           bool
	terminal          bool
	pendingDelta      string
	lastDeltaAt       time.Time
	lastDeltaFlush    time.Time
	deltaEvents       int
	deltaBytes        int
	diagnosis         *domain.Diagnosis
	initialInput      string
	egressBase        *modelEgressBase
	persistenceBad    bool
	terminalBudget    []UIBudgetMeasure
	startedAt         time.Time
	lastOccurredAt    time.Time
	modelAttempts     int64
	modelInputBytes   int64
	summaryReserved   int64
	toolCalls         int64
	externalCalls     int64
	evidenceItems     int64
}

func (bridge *eventBridge) currentSequence() int64 {
	if bridge == nil {
		return 0
	}
	return bridge.publishedSequence.Load()
}

func (bridge *eventBridge) commitSequence(sequence int64) {
	bridge.sequence = sequence
	bridge.publishedSequence.Store(sequence)
}

type modelEgressBase struct {
	role               domain.ModelRole
	originHash         string
	consented          bool
	contextMode        domain.PrivacyMode
	runMode            agent.RunMode
	summaryState       UIContextSummaryState
	recentTailMessages int
	coverageMessages   int
	eligibleCategories []ModelDataCategory
	budgetProfile      agent.BudgetProfile
}

func newEventBridge(runID domain.AgentRunID, scopeGeneration int64, policyGeneration domain.PolicyGeneration, sink UIEventSink, initialInput ...string) (*eventBridge, error) {
	if !runID.Valid() || scopeGeneration < 1 || !policyGeneration.Valid() || sink == nil || len(initialInput) > 1 ||
		len(initialInput) == 1 && !validUICommandText(initialInput[0], MaxQuestionBytes) {
		return nil, ErrInvalidUIEvent
	}
	bridge := &eventBridge{runID: runID, scopeGeneration: scopeGeneration, policyGeneration: policyGeneration, sink: sink}
	if len(initialInput) == 1 {
		bridge.initialInput = initialInput[0]
	}
	return bridge, nil
}

func (bridge *eventBridge) configureModelEgress(
	input agent.RunInput,
	contextMode domain.PrivacyMode,
	privacy PrivacyBindingSnapshot,
	categories []ModelDataCategory,
) error {
	if bridge == nil || input.RunID() != bridge.runID || input.Scope().Generation != bridge.scopeGeneration ||
		input.PolicyGeneration() != bridge.policyGeneration || privacy.Role != domain.ModelRoleAgent ||
		!privacy.Loaded || !privacy.Accepted || !validPrivacyDigest(privacy.OriginHash) ||
		(contextMode != domain.PrivacyModeStandard && contextMode != domain.PrivacyModeMinimal) ||
		!validEgressCategories(categories) {
		return ErrInvalidUIEvent
	}
	conversation := input.Conversation()
	summaryState := UIContextSummaryAbsent
	if conversation.Summary() != nil {
		summaryState = UIContextSummaryRetained
	}
	bridge.egressBase = &modelEgressBase{
		role: privacy.Role, originHash: privacy.OriginHash, consented: true,
		contextMode: contextMode, runMode: input.Mode(), summaryState: summaryState,
		recentTailMessages: len(conversation.Turns()),
		coverageMessages:   len(conversation.Coverage()),
		eligibleCategories: append([]ModelDataCategory(nil), categories...),
		budgetProfile:      input.BudgetLimits().Profile,
	}
	bridge.terminalBudget = NewUIBudgetMeasures(input.BudgetLimits())
	setBudgetUnavailable(bridge.terminalBudget, UIBudgetQueueItems)
	setBudgetUnavailable(bridge.terminalBudget, UIBudgetQueueBytes)
	return nil
}

func (bridge *eventBridge) accept(ctx context.Context, event agent.RunEvent) error {
	if bridge == nil || ctx == nil || ctx.Err() != nil || event.Validate() != nil ||
		event.RunID != bridge.runID || event.ScopeGeneration != bridge.scopeGeneration || bridge.terminal {
		return ErrInvalidUIEvent
	}
	bridge.lastOccurredAt = event.OccurredAt
	if event.Kind == agent.RunEventTextDelta {
		if !bridge.started {
			return ErrInvalidUIEvent
		}
		if bridge.deltaEvents >= maxUIDeltaEvents {
			bridge.pendingDelta = ""
			return nil
		}
		if len(event.TextDelta) > MaxAnswerMarkdownBytes-bridge.deltaBytes {
			return nil
		}
		bridge.pendingDelta += event.TextDelta
		bridge.deltaBytes += len(event.TextDelta)
		bridge.lastDeltaAt = event.OccurredAt
		if bridge.deltaEvents == 0 || len(bridge.pendingDelta) >= uiDeltaFlushBytes ||
			!bridge.lastDeltaFlush.IsZero() && event.OccurredAt.Sub(bridge.lastDeltaFlush) >= uiDeltaFlushInterval {
			return bridge.flushDelta(ctx)
		}
		return nil
	}
	if event.Kind == agent.RunEventRunFailed || event.Kind == agent.RunEventRunCancelled ||
		event.Kind == agent.RunEventRunTimedOut || event.Kind == agent.RunEventRunStaleScope ||
		event.Kind == agent.RunEventRunInterrupted {
		bridge.pendingDelta = ""
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
		bridge.startedAt = event.OccurredAt
		return bridge.emit(ctx, UIEvent{Kind: UIEventRunStarted, Text: bridge.initialInput})
	case agent.RunEventModelStreamStarted, agent.RunEventSummaryStarted:
		if bridge.egressBase == nil || event.ModelPreflight == nil {
			return ErrInvalidUIEvent
		}
		base := bridge.egressBase
		preflight := event.ModelPreflight
		projection := UIModelEgressPreflight{
			CallKind: preflight.Kind, Role: base.role, OriginHash: base.originHash, Consented: base.consented,
			ContextMode: base.contextMode, RunMode: base.runMode,
			MessageCount: preflight.MessageCount, MessageBytes: preflight.MessageBytes,
			SummaryState: base.summaryState, RecentTailMessages: base.recentTailMessages,
			EligibleCategories:   append([]ModelDataCategory(nil), base.eligibleCategories...),
			ReservedRequestBytes: preflight.ReservedRequestBytes, ReservedOutputBytes: preflight.ReservedOutputBytes,
			ReservedStreamBytes: preflight.ReservedStreamBytes, ReservedNanoseconds: preflight.ReservedNanoseconds,
			ReservedCostUnits: preflight.ReservedCostUnits, BudgetProfile: base.budgetProfile,
		}
		bridge.modelInputBytes += int64(preflight.MessageBytes)
		if preflight.Kind == agent.ModelCallAgent {
			bridge.modelAttempts++
		} else {
			bridge.summaryReserved += int64(preflight.ReservedRequestBytes + preflight.ReservedOutputBytes)
		}
		setBudgetMeasure(bridge.terminalBudget, UIBudgetModelInputBytes, bridge.modelInputBytes, UIBudgetMeasured)
		setBudgetMeasure(bridge.terminalBudget, UIBudgetModelAttempts, bridge.modelAttempts, UIBudgetMeasured)
		setBudgetMeasure(bridge.terminalBudget, UIBudgetSummaryReserveBytes, bridge.summaryReserved, UIBudgetReserved)
		return bridge.emit(ctx, UIEvent{Kind: UIEventModelEgress, ModelEgress: &projection})
	case agent.RunEventSummaryReady:
		if bridge.egressBase == nil || event.Summary == nil {
			return ErrInvalidUIEvent
		}
		bridge.egressBase.summaryState = UIContextSummaryGenerated
		bridge.egressBase.recentTailMessages = max(0, bridge.egressBase.coverageMessages-event.Summary.CoveredCount)
		return nil
	case agent.RunEventEvidenceCollected:
		bridge.evidenceItems++
		setBudgetMeasure(bridge.terminalBudget, UIBudgetEvidenceItems, bridge.evidenceItems, UIBudgetMeasured)
		return nil
	case agent.RunEventDiagnosisReady:
		diagnosis := cloneDiagnosis(*event.Diagnosis)
		bridge.diagnosis = &diagnosis
		if len(diagnosis.ValidationWarnings) != 0 {
			return bridge.emit(ctx, UIEvent{Kind: UIEventValidationWarning, Text: answerValidationWarningText})
		}
		return nil
	case agent.RunEventToolCallRequested, agent.RunEventToolCallStarted,
		agent.RunEventToolCallCompleted, agent.RunEventToolCallFailed, agent.RunEventToolCallDenied:
		step, err := projectToolStep(event)
		if err != nil {
			return err
		}
		if err := bridge.emit(ctx, UIEvent{Kind: UIEventToolStep, ToolStep: &step}); err != nil {
			return err
		}
		if event.Kind == agent.RunEventToolCallRequested {
			if event.ToolReuse == nil {
				bridge.toolCalls++
				bridge.externalCalls += int64(event.ExternalCallCost)
				setBudgetMeasure(bridge.terminalBudget, UIBudgetToolCalls, bridge.toolCalls, UIBudgetMeasured)
				setBudgetMeasure(bridge.terminalBudget, UIBudgetExternalReadCalls, bridge.externalCalls, UIBudgetMeasured)
			}
			bridge.deltaBytes = 0
		}
		return nil
	case agent.RunEventRunCompleted:
		if bridge.diagnosis == nil {
			return ErrInvalidUIEvent
		}
		reason := domain.RunTerminalCompleted
		if !bridge.diagnosis.Completeness.Empty() {
			reason = bridge.diagnosis.Completeness.StopReason
		}
		terminal, err := bridge.projectTerminal(reason)
		if err != nil {
			return err
		}
		provenance := projectAnswerProvenance(*bridge.diagnosis, bridge.policyGeneration)
		return bridge.emitTerminal(ctx, UIEvent{
			Kind: UIEventRunCompleted, Text: bridge.diagnosis.AnswerMarkdown,
			EvidenceReferences: projectUIEvidenceReferences(*bridge.diagnosis, bridge.sequence+1),
			TerminalOutcome:    &terminal,
			AnswerProvenance:   &provenance,
		})
	case agent.RunEventRunFailed:
		terminal, err := bridge.projectTerminal(terminalReasonForFailure(event.Failure.Class))
		if err != nil {
			return err
		}
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: event.Failure.SafeMessage, TerminalOutcome: &terminal})
	case agent.RunEventRunCancelled:
		terminal, err := bridge.projectTerminal(domain.RunTerminalCancelled)
		if err != nil {
			return err
		}
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunCancelled, Text: "The diagnostic run was cancelled.", TerminalOutcome: &terminal})
	case agent.RunEventRunTimedOut:
		terminal, err := bridge.projectTerminal(domain.RunTerminalTimedOut)
		if err != nil {
			return err
		}
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: "The diagnostic run reached its time limit.", TerminalOutcome: &terminal})
	case agent.RunEventRunStaleScope:
		terminal, err := bridge.projectTerminal(domain.RunTerminalStaleGeneration)
		if err != nil {
			return err
		}
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: "The diagnostic run stopped because the Kubernetes context or namespace changed.", TerminalOutcome: &terminal})
	case agent.RunEventRunInterrupted:
		terminal, err := bridge.projectTerminal(domain.RunTerminalUnknown)
		if err != nil {
			return err
		}
		return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: "The diagnostic run was interrupted.", TerminalOutcome: &terminal})
	default:
		return ErrInvalidUIEvent
	}
}

func projectAnswerProvenance(diagnosis domain.Diagnosis, policy domain.PolicyGeneration) UIAnswerProvenance {
	projection := UIAnswerProvenance{
		EvidenceCount: len(diagnosis.ReferencedEvidenceIDs()), ScopeGeneration: diagnosis.Scope.Generation,
		PolicyGeneration: policy, CoverageState: UIAnswerCoverageComplete,
	}
	if diagnosis.ObservedFrom != nil {
		value := diagnosis.ObservedFrom.UTC().Truncate(time.Millisecond)
		projection.ObservedFrom = &value
	}
	if diagnosis.ObservedTo != nil {
		value := diagnosis.ObservedTo.UTC().Truncate(time.Millisecond)
		projection.ObservedThrough = &value
	}
	if diagnosis.EvidenceDetailsState == domain.EvidenceDetailPartial || diagnosis.EvidenceDetailsState == domain.EvidenceDetailExpired {
		projection.CoverageState = UIAnswerCoveragePartial
	}
	for _, claim := range diagnosis.ClaimCoverage {
		projection.HasInference = projection.HasInference || claim.Kind == domain.ClaimInference || claim.Kind == domain.ClaimRecommendation
		projection.HasUncertainty = projection.HasUncertainty || claim.Kind == domain.ClaimUncertainty || claim.Kind == domain.ClaimUnsupportedObservation
	}
	for _, source := range diagnosis.Completeness.Sources {
		switch source.State {
		case domain.SourceCheckedPresent, domain.SourceCheckedAbsent, domain.SourcePartial,
			domain.SourceTruncated, domain.SourceStale, domain.SourceConflicting:
			projection.CheckedSourceCount++
		default:
			projection.UncheckedSourceCount++
		}
		projection.HasConflict = projection.HasConflict || source.Conflict == domain.EvidenceConflictDetected || source.State == domain.SourceConflicting
		projection.HasSuperseded = projection.HasSuperseded || source.Conflict == domain.EvidenceConflictSuperseded
		switch source.State {
		case domain.SourceUnavailable, domain.SourceDenied, domain.SourceTimedOut:
			projection.CoverageState = UIAnswerCoverageUnavailable
		case domain.SourceTruncated:
			if projection.CoverageState != UIAnswerCoverageUnavailable {
				projection.CoverageState = UIAnswerCoverageTruncated
			}
		case domain.SourcePartial, domain.SourceStale, domain.SourceConflicting:
			if projection.CoverageState == UIAnswerCoverageComplete {
				projection.CoverageState = UIAnswerCoveragePartial
			}
		}
	}
	return projection
}

func (bridge *eventBridge) persistenceDegraded(ctx context.Context) error {
	if bridge == nil || ctx == nil || ctx.Err() != nil || !bridge.started || bridge.terminal {
		return ErrInvalidUIEvent
	}
	if err := bridge.flushDelta(ctx); err != nil {
		return err
	}
	bridge.persistenceBad = true
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
		if err := bridge.emit(ctx, UIEvent{Kind: UIEventRunStarted, Text: bridge.initialInput}); err != nil {
			return err
		}
	}
	bridge.pendingDelta = ""
	terminal, err := bridge.projectTerminal(domain.RunTerminalFailed)
	if err != nil {
		return err
	}
	return bridge.emitTerminal(ctx, UIEvent{Kind: UIEventRunFailed, Text: safeMessage, TerminalOutcome: &terminal})
}

func (bridge *eventBridge) projectTerminal(reason domain.RunTerminalReason) (UITerminalOutcome, error) {
	if bridge.persistenceBad {
		reason = domain.RunTerminalPersistenceDegraded
	}
	outcome, err := ProjectTerminalOutcome(reason)
	if err != nil {
		return UITerminalOutcome{}, err
	}
	if validFineGrainedBudget(bridge.terminalBudget) {
		if !bridge.startedAt.IsZero() && !bridge.lastOccurredAt.Before(bridge.startedAt) {
			elapsed := bridge.lastOccurredAt.Sub(bridge.startedAt).Milliseconds()
			for _, measure := range bridge.terminalBudget {
				if measure.Category == UIBudgetWallMilliseconds && measure.Limit > 0 && elapsed > measure.Limit {
					elapsed = measure.Limit
					break
				}
			}
			setBudgetMeasure(bridge.terminalBudget, UIBudgetWallMilliseconds, elapsed, UIBudgetMeasured)
		}
		outcome.Budget = append([]UIBudgetMeasure(nil), bridge.terminalBudget...)
	}
	return outcome, nil
}

func terminalReasonForFailure(class domain.SafeErrorClass) domain.RunTerminalReason {
	switch class {
	case domain.SafeErrorClassBudgetExhausted:
		return domain.RunTerminalBudgetExhausted
	case domain.SafeErrorClassConsentRequired, domain.SafeErrorClassPermissionDenied, domain.SafeErrorClassPolicyDenied:
		return domain.RunTerminalPolicyDenied
	case domain.SafeErrorClassUnavailable, domain.SafeErrorClassAuthenticationFailed, domain.SafeErrorClassRateLimited,
		domain.SafeErrorClassUnsupported:
		return domain.RunTerminalSourceUnavailable
	case domain.SafeErrorClassPersistenceUnavailable:
		return domain.RunTerminalPersistenceDegraded
	case domain.SafeErrorClassStaleScope:
		return domain.RunTerminalStaleGeneration
	case domain.SafeErrorClassTimeout:
		return domain.RunTerminalTimedOut
	case domain.SafeErrorClassCancelled:
		return domain.RunTerminalCancelled
	case domain.SafeErrorClassConflict:
		return domain.RunTerminalConflictingEvidence
	default:
		return domain.RunTerminalFailed
	}
}

func (bridge *eventBridge) flushDelta(ctx context.Context) error {
	if bridge.pendingDelta == "" {
		return nil
	}
	if bridge.deltaEvents >= maxUIDeltaEvents {
		bridge.pendingDelta = ""
		return nil
	}
	value := bridge.pendingDelta
	if err := bridge.emit(ctx, UIEvent{Kind: UIEventTextDelta, Text: value}); err != nil {
		return err
	}
	bridge.pendingDelta = ""
	bridge.lastDeltaFlush = bridge.lastDeltaAt
	bridge.deltaEvents++
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
	event.PolicyGeneration = bridge.policyGeneration
	event.Sequence = nextSequence
	if event.Validate() != nil {
		return ErrInvalidUIEvent
	}
	if err := bridge.sink.PublishUIEvent(ctx, event); err != nil {
		return err
	}
	bridge.commitSequence(nextSequence)
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
	case agent.RunEventToolCallDenied:
		step.Status = ToolStepDenied
		if step.Summary == "" {
			step.Summary = "The cluster read was blocked safely."
		}
	case agent.RunEventToolCallFailed:
		step.Status = ToolStepFailed
		if invocation.Status == domain.ToolInvocationStatusCancelled {
			step.Status = ToolStepCancelled
		}
		if step.Summary == "" {
			step.Summary = "The cluster read failed safely."
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
