package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

const transcriptDoubleClickInterval = 500 * time.Millisecond

// Selection refers only to the displayed transcript revision. A changed view
// invalidates its cell coordinates instead of copying different or hidden text.
type transcriptTextSelection struct {
	content                    string
	startX, startY, endX, endY int
	dragging                   bool
	clickedAt                  time.Time
	wordStart, wordEnd         int
	copyQueued                 bool
	copyResult                 ClipboardResultMsg
}

func (model Model) selectableTranscript() string {
	if model.dialog.Open() || model.approvalDialog.Open() || model.evidenceDialog.Open() ||
		model.transcript.EvidenceSelecting() {
		return ""
	}
	content := constrainLayoutWidth(model.transcript.View(), model.contentWidth())
	layout, _, _ := model.renderLayout()
	if !strings.HasPrefix(layout, content) {
		return ""
	}
	return content
}

func (model *Model) clearStaleTextSelection() {
	if model.textSelection.content != "" && model.textSelection.content != model.selectableTranscript() {
		model.textSelection = transcriptTextSelection{}
	}
}

func (model Model) updateTranscriptMouse(message tea.Msg) (Model, tea.Cmd) {
	model.clearStaleTextSelection()
	if !model.terminalFocused {
		return model, nil
	}
	switch event := message.(type) {
	case tea.MouseClickMsg:
		if event.Button == tea.MouseRight {
			return model.copyTranscriptSelection()
		}
		if event.Button != tea.MouseLeft {
			return model, nil
		}
		content := model.selectableTranscript()
		if content == "" || event.Y < 0 || event.Y >= lipgloss.Height(content) || event.X < 0 || event.X >= model.contentWidth() {
			model.textSelection = transcriptTextSelection{}
			return model, nil
		}
		previous, now := model.textSelection, model.now()
		doubleClick := previous.content == content && !previous.dragging &&
			!previous.clickedAt.IsZero() && now.Sub(previous.clickedAt) >= 0 && now.Sub(previous.clickedAt) <= transcriptDoubleClickInterval &&
			previous.startX == event.X && previous.endX == event.X && previous.startY == event.Y && previous.endY == event.Y
		model.textSelection = transcriptTextSelection{
			content: content, startX: event.X, endX: event.X,
			startY: event.Y, endY: event.Y, dragging: true, clickedAt: now,
		}
		if doubleClick {
			line := strings.Split(content, "\n")[event.Y]
			left, right := selectionWordColumns(line, event.X)
			model.textSelection.wordStart, model.textSelection.wordEnd = left, right
			model.textSelection.startX, model.textSelection.endX = left, right
		}
	case tea.MouseMotionMsg:
		if event.Button == tea.MouseLeft && model.textSelection.dragging {
			model.extendTranscriptSelection(event.X, event.Y)
		}
	case tea.MouseReleaseMsg:
		if (event.Button == tea.MouseLeft || event.Button == tea.MouseNone) && model.textSelection.dragging {
			model.extendTranscriptSelection(event.X, event.Y)
			model.textSelection.dragging = false
		}
	}
	return model, nil
}

func (model *Model) extendTranscriptSelection(x, y int) {
	selection := &model.textSelection
	previousStartX, previousEndX, previousEndY := selection.startX, selection.endX, selection.endY
	x = max(0, min(x, model.contentWidth()))
	y = max(0, min(y, lipgloss.Height(selection.content)-1))
	if x != selection.startX || y != selection.startY {
		selection.clickedAt = time.Time{}
	}
	selection.endX, selection.endY = x, y
	if selection.wordEnd > selection.wordStart {
		left, right := selectionWordColumns(strings.Split(selection.content, "\n")[y], x)
		if y < selection.startY || y == selection.startY && left < selection.wordStart {
			selection.startX, selection.endX = selection.wordEnd, left
		} else {
			selection.startX, selection.endX = selection.wordStart, right
		}
	}
	if selection.startX != previousStartX || selection.endX != previousEndX || selection.endY != previousEndY {
		selection.copyQueued = false
		selection.copyResult = ClipboardResultMsg{}
	}
}

// Reuse the pinned Unicode word segmenter; coordinates remain terminal cells.
func selectionWordColumns(line string, x int) (int, int) {
	remaining, state, left := ansi.Strip(line), -1, 0
	for remaining != "" {
		var word string
		word, remaining, state = uniseg.FirstWordInString(remaining, state)
		right := left + ansi.StringWidth(word)
		if x >= left && x < right {
			return left, right
		}
		left = right
	}
	return left, left
}

// selectionColumns includes whole graphemes when a terminal cell falls inside
// a wide glyph; byte offsets, combining marks and emoji never split a selection.
func selectionColumns(line string, left, right int) (int, int) {
	position := 0
	clusters := uniseg.NewGraphemes(ansi.Strip(line))
	for clusters.Next() {
		next := position + ansi.StringWidth(clusters.Str())
		if left > position && left < next {
			left = position
		}
		if right > position && right < next {
			right = next
		}
		position = next
	}
	return left, right
}

func (selection transcriptTextSelection) columns(row int, line string) (int, int) {
	sx, sy, ex, ey := selection.startX, selection.startY, selection.endX, selection.endY
	if sy > ey || sy == ey && sx > ex {
		sx, sy, ex, ey = ex, ey, sx, sy
	}
	if row < sy || row > ey || sx == ex && sy == ey {
		return 0, 0
	}
	left, right := 0, ansi.StringWidth(line)
	if row == sy {
		left = sx
	}
	if row == ey {
		right = ex
	}
	return selectionColumns(line, left, right)
}

func (model Model) selectedTranscriptText() string {
	selection := model.textSelection
	if selection.content == "" {
		return ""
	}
	var rows []string
	for row, line := range strings.Split(selection.content, "\n") {
		left, right := selection.columns(row, line)
		if row >= min(selection.startY, selection.endY) && row <= max(selection.startY, selection.endY) {
			rows = append(rows, strings.TrimRight(ansi.Cut(ansi.Strip(line), left, right), " "))
		}
	}
	text := strings.Join(rows, "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return strings.Trim(text, "\n")
}

func (model Model) copyTranscriptSelection() (Model, tea.Cmd) {
	model.clearStaleTextSelection()
	text := model.selectedTranscriptText()
	if text == "" {
		return model, nil
	}
	if model.pendingClipboardID != 0 {
		// One current selection is the entire pending intent; never enqueue text
		// snapshots or allow an older write to finish after a newer write.
		model.textSelection.copyQueued = model.textSelection.copyResult.RequestID != model.pendingClipboardID
		return model, nil
	}
	model.textSelection.copyQueued = false
	model, command := model.copyText(text)
	if model.pendingClipboardID != 0 {
		model.copyingSelection = true
		model.textSelection.copyResult = ClipboardResultMsg{RequestID: model.pendingClipboardID}
	}
	return model, command
}

func (model Model) selectionCopyHint() string {
	if model.textSelection.copyQueued {
		return "Selection copy queued"
	}
	result := model.textSelection.copyResult
	if result.RequestID == 0 {
		if model.selectedTranscriptText() != "" {
			return model.keymap.Copy.Help().Key + " copy"
		}
		return ""
	}
	if result.RequestID == model.pendingClipboardID {
		return "Copying selection"
	}
	if result.Copied {
		return "Selection copied"
	}
	if result.Requested {
		return "Selection copy unconfirmed"
	}
	return "Selection copy failed"
}

func (model Model) highlightTranscriptSelection(content string) string {
	selection := model.textSelection
	if selection.content == "" || selection.content != model.selectableTranscript() {
		return content
	}
	rows := strings.Split(selection.content, "\n")
	for row, line := range rows {
		left, right := selection.columns(row, line)
		if right > left {
			rows[row] = ansi.Cut(line, 0, left) +
				lipgloss.NewStyle().Reverse(true).Render(ansi.Strip(ansi.Cut(line, left, right))) +
				ansi.Cut(line, right, ansi.StringWidth(line))
		}
	}
	return strings.Join(rows, "\n") + strings.TrimPrefix(content, selection.content)
}
