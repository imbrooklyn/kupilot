package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/rivo/uniseg"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const MaxSubmittedHistorySearchQueryBytes = 512

func (model Model) beginSubmittedHistorySearch() (Model, tea.Cmd) {
	if model.modelSetup != nil || model.sessionExport != nil || model.pickerOpen() || model.dialog.Open() ||
		model.approvalDialog.Open() || model.evidenceDialog.Open() || model.transcript.EvidenceSelecting() ||
		model.pendingConversation != nil {
		model.showDialog("History search unavailable", "Finish the current local interaction before searching committed submitted input.")
		return model, nil
	}
	history := model.composer.SubmittedHistory()
	if len(history) == 0 {
		model.showDialog("History empty", "There is no committed ordinary submitted input in this Session.")
		return model, nil
	}
	model.closePickers()
	model.slashMenu.Close()
	model.historySearchMode = true
	model.historySearchReturnDraft = model.composer.Value()
	model.historySearchQuery = ""
	model.historySearchEntries = history
	model.historySearchMatches = nil
	model.historySearchIndex = 0
	model.composer.Reset()
	model.composer.SetMaxBytes(application.MaxQuestionBytes)
	model.composer.SetPlaceholder("Reverse search committed submitted input")
	model.syncSubmittedHistorySearch()
	model.reflow()
	return model, nil
}

func (model *Model) syncSubmittedHistorySearch() {
	if !model.historySearchMode {
		return
	}
	query := strings.ToLower(model.historySearchQuery)
	matches := make([]int, 0, min(len(model.historySearchEntries), components.MaxTranscriptSearchMatches))
	for index := len(model.historySearchEntries) - 1; index >= 0; index-- {
		if query != "" && !strings.Contains(strings.ToLower(model.historySearchEntries[index]), query) {
			continue
		}
		matches = append(matches, index)
		if len(matches) == components.MaxTranscriptSearchMatches {
			break
		}
	}
	model.historySearchMatches = matches
	if len(matches) == 0 {
		model.historySearchIndex = 0
		model.composer.SetValue(model.historySearchQuery)
		return
	}
	model.historySearchIndex = max(0, min(model.historySearchIndex, len(matches)-1))
	model.composer.SetValue(model.historySearchEntries[matches[model.historySearchIndex]])
}

func (model *Model) moveSubmittedHistorySearch(delta int) {
	if !model.historySearchMode || len(model.historySearchMatches) == 0 {
		return
	}
	model.historySearchIndex = (model.historySearchIndex + delta%len(model.historySearchMatches) + len(model.historySearchMatches)) % len(model.historySearchMatches)
	index := model.historySearchMatches[model.historySearchIndex]
	model.composer.SetValue(model.historySearchEntries[index])
}

func (model *Model) endSubmittedHistorySearch(accept bool) {
	value := model.historySearchReturnDraft
	if accept && len(model.historySearchMatches) > 0 {
		value = model.historySearchEntries[model.historySearchMatches[model.historySearchIndex]]
	}
	model.historySearchMode = false
	model.historySearchReturnDraft = ""
	model.historySearchQuery = ""
	clear(model.historySearchEntries)
	clear(model.historySearchMatches)
	model.historySearchEntries = nil
	model.historySearchMatches = nil
	model.historySearchIndex = 0
	model.composer.Reset()
	model.composer.SetMaxBytes(application.MaxQuestionBytes)
	model.composer.ResetPlaceholder()
	if value != "" {
		model.composer.SetValue(value)
	}
	model.reflow()
}

func (model Model) updateSubmittedHistorySearchKey(message tea.KeyPressMsg) (Model, tea.Cmd) {
	switch {
	case key.Matches(message, model.keymap.Close), key.Matches(message, model.keymap.Quit):
		model.endSubmittedHistorySearch(false)
		return model, nil
	case key.Matches(message, model.keymap.Submit):
		model.endSubmittedHistorySearch(true)
		return model, nil
	case key.Matches(message, model.keymap.HistorySearch), key.Matches(message, model.keymap.Next), key.Matches(message, model.keymap.NextAlt):
		model.moveSubmittedHistorySearch(1)
		model.reflow()
		return model, nil
	case key.Matches(message, model.keymap.Reverse), key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.PreviousAlt):
		model.moveSubmittedHistorySearch(-1)
		model.reflow()
		return model, nil
	case message.Code == tea.KeyBackspace:
		model.historySearchQuery = removeLastGrapheme(model.historySearchQuery)
		model.historySearchIndex = 0
		model.syncSubmittedHistorySearch()
		model.reflow()
		return model, nil
	case message.Text != "" && !strings.ContainsRune(message.Text, '\n'):
		value := sanitizeExternalText(message.Text, 0)
		if value == "" || len(model.historySearchQuery)+len(value) > MaxSubmittedHistorySearchQueryBytes {
			return model, nil
		}
		model.historySearchQuery += value
		model.historySearchIndex = 0
		model.syncSubmittedHistorySearch()
		model.reflow()
		return model, nil
	default:
		return model, nil
	}
}

func (model Model) updateSubmittedHistorySearchPaste(message tea.PasteMsg) (Model, tea.Cmd) {
	value := sanitizeExternalText(message.Content, 0)
	if value == "" || strings.ContainsRune(value, '\n') || len(model.historySearchQuery)+len(value) > MaxSubmittedHistorySearchQueryBytes {
		return model, nil
	}
	model.historySearchQuery += value
	model.historySearchIndex = 0
	model.syncSubmittedHistorySearch()
	model.reflow()
	return model, nil
}

func removeLastGrapheme(value string) string {
	clusters := uniseg.NewGraphemes(value)
	lastStart := 0
	lastEnd := 0
	for clusters.Next() {
		lastStart, lastEnd = clusters.Positions()
	}
	if lastEnd == 0 {
		return ""
	}
	return value[:lastStart]
}
