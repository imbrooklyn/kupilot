package application

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const MaxQuestionBytes = 64 * 1024

// ErrInvalidUICommand reports an invalid delivery intent without echoing its data.
var ErrInvalidUICommand = errors.New("UI command data is invalid")

// CreateSessionCommand contains the only policy choice needed to create a new
// Session shell. Privacy onboarding remains a separate delivery flow.
type CreateSessionCommand struct {
	PrivacyMode domain.PrivacyMode
}

// Validate checks the fixed durable privacy modes.
func (command CreateSessionCommand) Validate() error {
	if command.PrivacyMode != domain.PrivacyModeStandard && command.PrivacyMode != domain.PrivacyModeMinimal {
		return ErrInvalidUICommand
	}
	return nil
}

// StartRunCommand contains user intent but no caller-supplied live scope,
// provider, Tool authority, deadline, or hard limit.
type StartRunCommand struct {
	SessionID domain.SessionID
	Question  string
	Resource  *domain.ResourceRef
}

// Validate checks bounded intent before Application applies its egress policy.
func (command StartRunCommand) Validate() error {
	if !command.SessionID.Valid() || !validUICommandText(command.Question, MaxQuestionBytes) {
		return ErrInvalidUICommand
	}
	if command.Resource != nil && domain.ValidateLiveResourceRef(*command.Resource) != nil {
		return ErrInvalidUICommand
	}
	return nil
}

// CancelRunCommand binds cancellation to the exact active run generation.
type CancelRunCommand struct {
	RunID           domain.AgentRunID
	ScopeGeneration int64
}

// Validate rejects stale or unbound cancellation intent.
func (command CancelRunCommand) Validate() error {
	if !command.RunID.Valid() || command.ScopeGeneration < 1 {
		return ErrInvalidUICommand
	}
	return nil
}

// UICommandKind identifies one fixed TUI-to-Application intent.
type UICommandKind string

const (
	UICommandSubmitQuestion    UICommandKind = "submit_question"
	UICommandSubmitSteer       UICommandKind = "submit_steer"
	UICommandEnqueueFollowUp   UICommandKind = "enqueue_follow_up"
	UICommandPopFollowUp       UICommandKind = "pop_follow_up"
	UICommandCancelFollowUp    UICommandKind = "cancel_follow_up"
	UICommandClearFollowUps    UICommandKind = "clear_follow_ups"
	UICommandArmPlan           UICommandKind = "arm_plan"
	UICommandCancelPlan        UICommandKind = "cancel_plan"
	UICommandCompactContext    UICommandKind = "compact_context"
	UICommandSelectContext     UICommandKind = "select_context"
	UICommandSelectNamespace   UICommandKind = "select_namespace"
	UICommandSelectResource    UICommandKind = "select_resource"
	UICommandNewSession        UICommandKind = "new_session"
	UICommandResumeSession     UICommandKind = "resume_session"
	UICommandRenameSession     UICommandKind = "rename_session"
	UICommandShowPrivacy       UICommandKind = "show_privacy"
	UICommandAcceptPrivacy     UICommandKind = "accept_privacy"
	UICommandRejectPrivacy     UICommandKind = "reject_privacy"
	UICommandRevokePrivacy     UICommandKind = "revoke_privacy"
	UICommandToggleLogs        UICommandKind = "toggle_logs"
	UICommandCancelPrivacy     UICommandKind = "cancel_privacy"
	UICommandCancelRun         UICommandKind = "cancel_run"
	UICommandActivateScope     UICommandKind = "activate_scope"
	UICommandAcceptResume      UICommandKind = "accept_resume"
	UICommandCancelResume      UICommandKind = "cancel_resume"
	UICommandShowStatus        UICommandKind = "show_status"
	UICommandShowDoctor        UICommandKind = "show_doctor"
	UICommandShowPermissions   UICommandKind = "show_permissions"
	UICommandChangePermission  UICommandKind = "change_permission"
	UICommandExportSession     UICommandKind = "export_session"
	UICommandApproveAction     UICommandKind = "approve_action"
	UICommandRejectAction      UICommandKind = "reject_action"
	UICommandCancelAction      UICommandKind = "cancel_action"
	UICommandExpireAction      UICommandKind = "expire_action"
	UICommandCreateSessionRule UICommandKind = "create_session_rule"
)

// UICommand contains only the typed intent data needed by the current TUI.
// It carries no Bubble Tea value, callback, client, credential, or write path.
type UICommand struct {
	Kind                     UICommandKind
	RequestID                uint64
	SessionID                domain.SessionID
	Text                     string
	RunID                    domain.AgentRunID
	ItemID                   domain.MessageID
	ExpectedScopeGeneration  int64
	ExpectedQueueRevision    int64
	Scope                    *domain.ScopeCandidate
	Resource                 *domain.ResourceRef
	PrivacyRevision          string
	LogsEnabled              *bool
	ApprovalID               domain.ApprovalID
	ApprovalDigest           domain.ApprovalDigest
	ApprovalNonce            domain.ApprovalNonce
	ApprovalSequence         int64
	ExpectedPolicyGeneration domain.PolicyGeneration
	PermissionProfile        domain.PermissionProfile
	HighRiskAcknowledged     bool
	Lifecycle                *SessionLifecycleIntent
	Export                   *ExportSummaryIntent
}

// Validate checks payload exclusivity and bounded delivery data.
func (command UICommand) Validate() error {
	if command.ExpectedScopeGeneration < 0 || command.ExpectedQueueRevision < 0 {
		return ErrInvalidUICommand
	}
	approvalCommand := command.Kind == UICommandApproveAction || command.Kind == UICommandRejectAction ||
		command.Kind == UICommandCancelAction || command.Kind == UICommandExpireAction ||
		command.Kind == UICommandCreateSessionRule
	if !approvalCommand && command.hasApprovalPayload() {
		return ErrInvalidUICommand
	}
	permissionCommand := command.Kind == UICommandChangePermission || command.Kind == UICommandCreateSessionRule ||
		command.Kind == UICommandApproveAction || command.Kind == UICommandRejectAction ||
		command.Kind == UICommandCancelAction || command.Kind == UICommandExpireAction ||
		command.Kind == UICommandSubmitSteer || command.Kind == UICommandEnqueueFollowUp ||
		command.Kind == UICommandPopFollowUp || command.Kind == UICommandCancelFollowUp ||
		command.Kind == UICommandClearFollowUps || command.Kind == UICommandCompactContext ||
		command.Kind == UICommandSubmitQuestion
	if !permissionCommand && command.hasPermissionPayload() {
		return ErrInvalidUICommand
	}
	queueMutationCommand := command.Kind == UICommandCancelFollowUp || command.Kind == UICommandClearFollowUps
	if !queueMutationCommand && command.hasQueueMutationPayload() {
		return ErrInvalidUICommand
	}
	if command.Kind != UICommandSubmitQuestion && command.SessionID != "" {
		return ErrInvalidUICommand
	}
	lifecycleCommand := command.Kind == UICommandTightenRetention || command.Kind == UICommandSetPersistenceMode ||
		command.Kind == UICommandPreviewSessionDeletion ||
		command.Kind == UICommandDeleteSession || command.Kind == UICommandClearHistory ||
		command.Kind == UICommandDeleteAllLocalState
	if lifecycleCommand != (command.Lifecycle != nil) || command.Lifecycle != nil && command.Lifecycle.validateFor(command.Kind) != nil {
		return ErrInvalidUICommand
	}
	exportCommand := command.Kind == UICommandExportSession
	if exportCommand != (command.Export != nil) || command.Export != nil && command.Export.Validate() != nil {
		return ErrInvalidUICommand
	}
	switch command.Kind {
	case UICommandSubmitQuestion:
		if command.RequestID == 0 || !command.SessionID.Valid() || command.RunID != "" ||
			command.ExpectedScopeGeneration < 1 || !command.ExpectedPolicyGeneration.Valid() ||
			command.Scope != nil || command.Resource != nil && domain.ValidateLiveResourceRef(*command.Resource) != nil ||
			command.hasPrivacyPayload() || command.PermissionProfile != "" || command.HighRiskAcknowledged ||
			!validUICommandText(command.Text, MaxQuestionBytes) {
			return ErrInvalidUICommand
		}
	case UICommandSubmitSteer, UICommandEnqueueFollowUp:
		if command.RequestID == 0 || !command.RunID.Valid() || command.ExpectedScopeGeneration < 1 ||
			!command.ExpectedPolicyGeneration.Valid() || command.Scope != nil || command.Resource != nil ||
			command.hasPrivacyPayload() || command.hasApprovalPayload() || command.PermissionProfile != "" ||
			command.HighRiskAcknowledged || !validUICommandText(command.Text, MaxConversationInputItemBytes) {
			return ErrInvalidUICommand
		}
	case UICommandPopFollowUp:
		if command.RequestID == 0 || !command.RunID.Valid() || command.ExpectedScopeGeneration < 1 ||
			!command.ExpectedPolicyGeneration.Valid() || command.Text != "" || command.Scope != nil ||
			command.Resource != nil || command.hasPrivacyPayload() || command.hasApprovalPayload() ||
			command.PermissionProfile != "" || command.HighRiskAcknowledged {
			return ErrInvalidUICommand
		}
	case UICommandCancelFollowUp:
		if command.RequestID == 0 || command.RunID != "" || !command.ItemID.Valid() || command.ExpectedQueueRevision < 1 ||
			command.ExpectedScopeGeneration < 1 || !command.ExpectedPolicyGeneration.Valid() || command.Text != "" ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() || command.hasApprovalPayload() ||
			command.PermissionProfile != "" || command.HighRiskAcknowledged {
			return ErrInvalidUICommand
		}
	case UICommandClearFollowUps:
		if command.RequestID == 0 || command.RunID != "" || command.ItemID != "" || command.ExpectedQueueRevision < 1 ||
			command.ExpectedScopeGeneration < 1 || !command.ExpectedPolicyGeneration.Valid() || command.Text != "" ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() || command.hasApprovalPayload() ||
			command.PermissionProfile != "" || command.HighRiskAcknowledged {
			return ErrInvalidUICommand
		}
	case UICommandCompactContext:
		if command.RequestID == 0 || command.RunID != "" || command.ItemID != "" || command.ExpectedQueueRevision != 0 ||
			command.ExpectedScopeGeneration < 1 || !command.ExpectedPolicyGeneration.Valid() || command.Text != "" ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() || command.hasApprovalPayload() ||
			command.PermissionProfile != "" || command.HighRiskAcknowledged {
			return ErrInvalidUICommand
		}
	case UICommandSelectContext:
		if command.RequestID == 0 || command.RunID != "" || command.Scope != nil || command.Resource != nil ||
			command.hasPrivacyPayload() || !validUICommandText(command.Text, 253) {
			return ErrInvalidUICommand
		}
	case UICommandSelectNamespace:
		if command.RequestID == 0 || command.RunID != "" || command.ExpectedScopeGeneration < 1 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() || !validUICommandText(command.Text, 63) {
			return ErrInvalidUICommand
		}
	case UICommandSelectResource:
		clear := command.Text == "clear" && command.Resource == nil
		selectCandidate := command.Text == "" && command.Resource != nil && command.Resource.Validate() == nil
		if command.RequestID == 0 || command.RunID != "" || command.ExpectedScopeGeneration < 1 ||
			command.Scope != nil || command.hasPrivacyPayload() || (!clear && !selectCandidate) {
			return ErrInvalidUICommand
		}
	case UICommandActivateScope:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.Resource != nil ||
			command.Scope == nil || command.Scope.Validate() != nil || command.hasPrivacyPayload() {
			return ErrInvalidUICommand
		}
	case UICommandAcceptResume:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.Resource != nil ||
			command.Scope != nil || command.hasPrivacyPayload() {
			return ErrInvalidUICommand
		}
	case UICommandCancelResume:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() {
			return ErrInvalidUICommand
		}
	case UICommandResumeSession:
		if command.RequestID == 0 || command.RunID != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil ||
			command.hasPrivacyPayload() || !domain.SessionID(command.Text).Valid() {
			return ErrInvalidUICommand
		}
	case UICommandRenameSession:
		if command.RequestID != 0 || command.RunID != "" || command.Scope != nil || command.Resource != nil ||
			command.ExpectedScopeGeneration != 0 || command.hasPrivacyPayload() ||
			(command.Text != "" && !validUICommandText(command.Text, 512)) {
			return ErrInvalidUICommand
		}
	case UICommandShowPrivacy:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() {
			return ErrInvalidUICommand
		}
	case UICommandTightenRetention, UICommandSetPersistenceMode:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || !validPrivacyDigest(command.PrivacyRevision) || command.LogsEnabled != nil {
			return ErrInvalidUICommand
		}
	case UICommandPreviewSessionDeletion, UICommandDeleteSession, UICommandClearHistory, UICommandDeleteAllLocalState:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() {
			return ErrInvalidUICommand
		}
	case UICommandExportSession:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || !validPrivacyDigest(command.PrivacyRevision) ||
			command.LogsEnabled != nil {
			return ErrInvalidUICommand
		}
	case UICommandAcceptPrivacy, UICommandRejectPrivacy, UICommandRevokePrivacy, UICommandCancelPrivacy:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || !validPrivacyDigest(command.PrivacyRevision) ||
			command.LogsEnabled != nil {
			return ErrInvalidUICommand
		}
	case UICommandToggleLogs:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || !validPrivacyDigest(command.PrivacyRevision) ||
			command.LogsEnabled == nil {
			return ErrInvalidUICommand
		}
	case UICommandNewSession, UICommandShowStatus:
		if command.RequestID != 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() {
			return ErrInvalidUICommand
		}
	case UICommandShowDoctor:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() || command.hasApprovalPayload() ||
			command.hasPermissionPayload() || command.hasQueueMutationPayload() {
			return ErrInvalidUICommand
		}
	case UICommandArmPlan, UICommandCancelPlan:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() || command.hasApprovalPayload() ||
			command.hasPermissionPayload() || command.hasQueueMutationPayload() {
			return ErrInvalidUICommand
		}
	case UICommandShowPermissions:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() || command.hasApprovalPayload() ||
			command.hasPermissionPayload() {
			return ErrInvalidUICommand
		}
	case UICommandChangePermission:
		if command.RequestID == 0 || command.RunID != "" || command.Text != "" || command.ExpectedScopeGeneration != 0 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() || command.hasApprovalPayload() ||
			!command.ExpectedPolicyGeneration.Valid() || !command.PermissionProfile.Valid() ||
			(command.PermissionProfile == domain.PermissionProfileFullAccess) != command.HighRiskAcknowledged {
			return ErrInvalidUICommand
		}
	case UICommandCancelRun:
		if command.RequestID != 0 || !command.RunID.Valid() || command.Text != "" || command.ExpectedScopeGeneration < 1 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() {
			return ErrInvalidUICommand
		}
	case UICommandApproveAction, UICommandRejectAction, UICommandCancelAction, UICommandExpireAction,
		UICommandCreateSessionRule:
		if command.RequestID == 0 || !command.RunID.Valid() || command.Text != "" || command.ExpectedScopeGeneration < 1 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() ||
			!command.ApprovalID.Valid() || !command.ApprovalDigest.Valid() || !command.ApprovalNonce.Valid() ||
			command.ApprovalSequence < 1 || command.ApprovalSequence > 4096 ||
			!command.ExpectedPolicyGeneration.Valid() || command.PermissionProfile != "" || command.HighRiskAcknowledged {
			return ErrInvalidUICommand
		}
	default:
		return ErrInvalidUICommand
	}
	return nil
}

func (command UICommand) hasPrivacyPayload() bool {
	return command.PrivacyRevision != "" || command.LogsEnabled != nil
}

func (command UICommand) hasApprovalPayload() bool {
	return command.ApprovalID != "" || command.ApprovalDigest != "" || command.ApprovalNonce.Valid() || command.ApprovalSequence != 0
}

func (command UICommand) hasPermissionPayload() bool {
	return command.ExpectedPolicyGeneration != 0 || command.PermissionProfile != "" || command.HighRiskAcknowledged
}

func (command UICommand) hasQueueMutationPayload() bool {
	return command.ItemID != "" || command.ExpectedQueueRevision != 0
}

func validUICommandText(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) != ""
}

// RenameSessionRecord is the optimistic title write owned by Application.
type RenameSessionRecord struct {
	SessionID       domain.SessionID
	Title           string
	ExpectedVersion int64
	UpdatedAt       time.Time
}

// SessionTitleState is the exact durable optimistic-concurrency state read
// immediately before one title write. It contains no Message or title text.
type SessionTitleState struct {
	SessionID domain.SessionID
	Version   int64
}

// SessionTitleStore persists one bounded title change without exposing a
// repository or database type.
type SessionTitleStore interface {
	ReadTitleState(context.Context, domain.SessionID) (SessionTitleState, error)
	Rename(context.Context, RenameSessionRecord) error
}
