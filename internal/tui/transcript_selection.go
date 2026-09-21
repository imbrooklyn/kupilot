package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Selection refers only to the displayed transcript revision. A changed view
// invalidates its cell coordinates instead of copying different or hidden text.
type transcriptTextSelection struct {
	content                    string
	startX, startY, endX, endY int
	dragging                   bool
}

func (model Model) selectableTranscript() string {
	if model.dialog.Open() || model.approvalDialog.Open() || model.evidenceDialog.Open() ||
		model.transcript.EvidenceSelecting() || !model.terminalFocused {
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
	switch event := message.(type) {
	case tea.MouseClickMsg:
		if event.Button == tea.MouseRight {
			return model.copyTranscriptSelection()
		}
		if event.Button != tea.MouseLeft {
			return model, nil
		}
		model.textSelection = transcriptTextSelection{}
		content := model.selectableTranscript()
		if content == "" || event.Y < 0 || event.Y >= lipgloss.Height(content) || event.X < 0 || event.X >= model.contentWidth() {
			return model, nil
		}
		model.textSelection = transcriptTextSelection{
			content: content, startX: event.X, endX: event.X,
			startY: event.Y, endY: event.Y, dragging: true,
		}
	case tea.MouseMotionMsg:
		if event.Button == tea.MouseLeft && model.textSelection.dragging {
			model.extendTranscriptSelection(event.X, event.Y)
		}
	case tea.MouseReleaseMsg:
		if event.Button == tea.MouseLeft && model.textSelection.dragging {
			model.extendTranscriptSelection(event.X, event.Y)
			model.textSelection.dragging = false
		}
	}
	return model, nil
}

func (model *Model) extendTranscriptSelection(x, y int) {
	model.textSelection.endX = max(0, min(x, model.contentWidth()))
	model.textSelection.endY = max(0, min(y, lipgloss.Height(model.textSelection.content)-1))
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
	text := model.selectedTranscriptText()
	if text == "" {
		return model, nil
	}
	model.textSelection = transcriptTextSelection{}
	return model.copyText(text)
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
