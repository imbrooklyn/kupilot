package application

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

const MaxQuestionBytes = 64 * 1024

// ErrInvalidUICommand reports an invalid delivery intent without echoing its data.
var ErrInvalidUICommand = errors.New("UI command data is invalid")

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
	UICommandCancelRun       UICommandKind = "cancel_run"
)

// UICommand contains only the typed intent data needed by the current TUI.
// It carries no Bubble Tea value, callback, client, credential, or write path.
type UICommand struct {
	Kind                    UICommandKind
	Text                    string
	RunID                   domain.AgentRunID
	ExpectedScopeGeneration int64
}

// Validate checks payload exclusivity and bounded delivery data.
func (command UICommand) Validate() error {
	if command.ExpectedScopeGeneration < 0 {
		return ErrInvalidUICommand
	}
	switch command.Kind {
	case UICommandSubmitQuestion:
		if command.RunID != "" || command.ExpectedScopeGeneration < 1 || !validUICommandText(command.Text, MaxQuestionBytes) {
			return ErrInvalidUICommand
		}
	case UICommandSelectNamespace, UICommandSelectResource:
		if command.RunID != "" || command.ExpectedScopeGeneration < 1 || len(command.Text) > 1024 || !utf8.ValidString(command.Text) {
			return ErrInvalidUICommand
		}
	case UICommandSelectContext, UICommandResumeSession, UICommandRenameSession:
		if command.RunID != "" || len(command.Text) > 1024 || !utf8.ValidString(command.Text) {
			return ErrInvalidUICommand
		}
	case UICommandNewSession, UICommandShowPrivacy:
		if command.RunID != "" || command.Text != "" {
			return ErrInvalidUICommand
		}
	case UICommandCancelRun:
		if !command.RunID.Valid() || command.Text != "" || command.ExpectedScopeGeneration < 1 {
			return ErrInvalidUICommand
		}
	default:
		return ErrInvalidUICommand
	}
	return nil
}

func validUICommandText(value string, limit int) bool {
	return len(value) > 0 && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) != ""
}
