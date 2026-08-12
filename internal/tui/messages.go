package tui

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

// Msg and Cmd keep Bubble Tea types confined to the TUI delivery package.
type (
	Msg = tea.Msg
	Cmd = tea.Cmd
)

// ApplicationEventMsg carries one neutral ordered Application projection.
type ApplicationEventMsg struct {
	Event application.UIEvent
}

// ApprovalExpiryMsg is a local deadline signal bound to one tracked request.
type ApprovalExpiryMsg struct {
	RequestID       domain.ApprovalID
	RunID           domain.AgentRunID
	ScopeGeneration int64
	Sequence        int64
	Digest          domain.ApprovalDigest
}

// ApplicationCommandMsg is the deferred typed intent emitted by a TUI Cmd.
type ApplicationCommandMsg struct {
	Command application.UICommand
}

// ApplicationQueryMsg is a deferred typed completion query for an adapter.
type ApplicationQueryMsg struct {
	Query application.UICompletionQuery
}

// ApplicationResumeMsg is a deferred explicit Session resume request.
type ApplicationResumeMsg struct {
	Request application.UIResumeRequest
}

// CompletionResultMsg carries one bounded typed Picker result.
type CompletionResultMsg struct {
	Result application.UICompletionResult
}

// ResumeResultMsg carries one safe resumed Session container result.
type ResumeResultMsg struct {
	Result application.UIResumeResult
}

// ScopeResultMsg carries one request-bound fake scope activation result.
type ScopeResultMsg struct {
	Result application.UIScopeResult
}

// ResourceSelectionResultMsg carries one request-bound ResourceRef result.
type ResourceSelectionResultMsg struct {
	Result application.UIResourceSelectionResult
}

// CommandResultMsg carries one validated Application command outcome.
type CommandResultMsg struct {
	Result application.UICommandOutcome
}

// ApplicationFailureMsg carries code-authored delivery-safe text and the
// applicable request identity needed to reject stale asynchronous failures.
type ApplicationFailureMsg struct {
	Message          string
	RequestID        uint64
	ScopeGeneration  int64
	RunID            domain.AgentRunID
	ApprovalID       domain.ApprovalID
	ApprovalDigest   domain.ApprovalDigest
	ApprovalSequence int64
	Command          application.UICommandKind
	Query            application.UICompletionKind
	Resume           application.UIResumeMode
}

func sanitizeExternalText(value string, limit int) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	runes := []rune(value)
	var result strings.Builder
	for index := 0; index < len(runes); {
		current := runes[index]
		switch current {
		case 0x1b:
			index = skipEscapeSequence(runes, index)
			continue
		case 0x9b:
			index = skipControlSequence(runes, index+1)
			continue
		case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
			index = skipControlString(runes, index+1)
			continue
		}
		if current < 0x20 && current != '\n' || current >= 0x7f && current <= 0x9f || unsafeBidiRune(current) {
			index++
			continue
		}
		width := utf8.RuneLen(current)
		if limit > 0 && result.Len()+width > limit {
			appendTruncationMark(&result, limit)
			break
		}
		result.WriteRune(current)
		index++
	}
	return result.String()
}

func skipEscapeSequence(runes []rune, index int) int {
	if index+1 >= len(runes) {
		return len(runes)
	}
	switch runes[index+1] {
	case '[':
		return skipControlSequence(runes, index+2)
	case ']', 'P', 'X', '^', '_':
		return skipControlString(runes, index+2)
	default:
		return min(index+2, len(runes))
	}
}

func skipControlSequence(runes []rune, index int) int {
	for index < len(runes) {
		current := runes[index]
		index++
		if current >= 0x40 && current <= 0x7e {
			return index
		}
	}
	return len(runes)
}

func skipControlString(runes []rune, index int) int {
	for index < len(runes) {
		switch runes[index] {
		case 0x07, 0x9c:
			return index + 1
		case 0x1b:
			if index+1 < len(runes) && runes[index+1] == '\\' {
				return index + 2
			}
		}
		index++
	}
	return len(runes)
}

func unsafeBidiRune(value rune) bool {
	switch value {
	case 0x061c, 0x200e, 0x200f,
		0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2066, 0x2067, 0x2068, 0x2069:
		return true
	default:
		return false
	}
}

func appendTruncationMark(builder *strings.Builder, limit int) {
	const mark = "…"
	if builder.Len()+len(mark) <= limit {
		builder.WriteString(mark)
	}
}

func containsUnsafeTerminalText(value string) bool {
	for _, current := range value {
		if current == 0x1b || current < 0x20 && current != '\n' ||
			current >= 0x7f && current <= 0x9f || unsafeBidiRune(current) {
			return true
		}
	}
	return false
}
