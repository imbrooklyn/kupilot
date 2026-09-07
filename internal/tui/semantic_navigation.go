package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

func (model Model) semanticNavigation(message tea.KeyPressMsg) (components.TranscriptLandmark, int, bool) {
	switch {
	case key.Matches(message, model.keymap.PreviousUser):
		return components.TranscriptLandmarkUser, -1, true
	case key.Matches(message, model.keymap.NextUser):
		return components.TranscriptLandmarkUser, 1, true
	case key.Matches(message, model.keymap.PreviousAgent):
		return components.TranscriptLandmarkAssistantFinal, -1, true
	case key.Matches(message, model.keymap.NextAgent):
		return components.TranscriptLandmarkAssistantFinal, 1, true
	case key.Matches(message, model.keymap.PreviousIssue):
		return components.TranscriptLandmarkFailureUnknown, -1, true
	case key.Matches(message, model.keymap.NextIssue):
		return components.TranscriptLandmarkFailureUnknown, 1, true
	case key.Matches(message, model.keymap.PreviousApproval):
		return components.TranscriptLandmarkApproval, -1, true
	case key.Matches(message, model.keymap.NextApproval):
		return components.TranscriptLandmarkApproval, 1, true
	default:
		return 0, 0, false
	}
}
