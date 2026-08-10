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
	UICommandSubmitQuestion  UICommandKind = "submit_question"
	UICommandSelectContext   UICommandKind = "select_context"
	UICommandSelectNamespace UICommandKind = "select_namespace"
	UICommandSelectResource  UICommandKind = "select_resource"
	UICommandNewSession      UICommandKind = "new_session"
	UICommandResumeSession   UICommandKind = "resume_session"
	UICommandRenameSession   UICommandKind = "rename_session"
	UICommandShowPrivacy     UICommandKind = "show_privacy"
	UICommandAcceptPrivacy   UICommandKind = "accept_privacy"
	UICommandRejectPrivacy   UICommandKind = "reject_privacy"
	UICommandRevokePrivacy   UICommandKind = "revoke_privacy"
	UICommandToggleLogs      UICommandKind = "toggle_logs"
	UICommandCancelPrivacy   UICommandKind = "cancel_privacy"
	UICommandCancelRun       UICommandKind = "cancel_run"
	UICommandActivateScope   UICommandKind = "activate_scope"
	UICommandAcceptResume    UICommandKind = "accept_resume"
	UICommandCancelResume    UICommandKind = "cancel_resume"
	UICommandShowStatus      UICommandKind = "show_status"
)

// UICommand contains only the typed intent data needed by the current TUI.
// It carries no Bubble Tea value, callback, client, credential, or write path.
type UICommand struct {
	Kind                    UICommandKind
	RequestID               uint64
	Text                    string
	RunID                   domain.AgentRunID
	ExpectedScopeGeneration int64
	Scope                   *domain.ScopeCandidate
	Resource                *domain.ResourceRef
	PrivacyRevision         string
	LogsEnabled             *bool
}

// Validate checks payload exclusivity and bounded delivery data.
func (command UICommand) Validate() error {
	if command.ExpectedScopeGeneration < 0 {
		return ErrInvalidUICommand
	}
	switch command.Kind {
	case UICommandSubmitQuestion:
		if command.RequestID == 0 || command.RunID != "" || command.ExpectedScopeGeneration < 1 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() ||
			!validUICommandText(command.Text, MaxQuestionBytes) {
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
			(command.Scope != nil && command.Scope.Validate() != nil) || command.hasPrivacyPayload() {
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
	case UICommandCancelRun:
		if command.RequestID != 0 || !command.RunID.Valid() || command.Text != "" || command.ExpectedScopeGeneration < 1 ||
			command.Scope != nil || command.Resource != nil || command.hasPrivacyPayload() {
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

// SessionTitleStore persists one bounded title change without exposing a
// repository or database type.
type SessionTitleStore interface {
	Rename(context.Context, RenameSessionRecord) error
}
