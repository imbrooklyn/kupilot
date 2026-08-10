package components

import (
	"strings"

	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// EntryKind distinguishes surfaces without rendering repeated role labels.
type EntryKind uint8

const (
	EntryUser EntryKind = iota + 1
	EntryAgent
	EntryNotice
)

// Entry is one bounded, non-editable transcript item.
type Entry struct {
	Kind      EntryKind
	Text      string
	Streaming bool
	ToolSteps []ToolStep
}

// TranscriptStyles defines only semantic surfaces and prose styles.
type TranscriptStyles struct {
	UserSurface lipgloss.Style
	AgentText   lipgloss.Style
	NoticeText  lipgloss.Style
	Placeholder lipgloss.Style
}

// Transcript is a continuous scrollable conversation projection.
type Transcript struct {
	entries     []Entry
	activeAgent int
	viewport    viewport.Model
	width       int
	height      int
	styles      TranscriptStyles
	toolSteps   ToolSteps
}

// NewTranscript creates an empty, mouse-free viewport.
func NewTranscript(styles TranscriptStyles, toolStyles ToolStepStyles) Transcript {
	view := viewport.New(viewport.WithWidth(80), viewport.WithHeight(12))
	view.SoftWrap = true
	view.FillHeight = true
	view.MouseWheelEnabled = false
	return Transcript{
		activeAgent: -1,
		viewport:    view,
		width:       80,
		height:      12,
		styles:      styles,
		toolSteps:   NewToolSteps(toolStyles),
	}
}

// SetSize changes only viewport layout state.
func (transcript *Transcript) SetSize(width, height int) {
	transcript.width = max(8, width)
	transcript.height = max(1, height)
	transcript.viewport.SetWidth(transcript.width)
	transcript.viewport.SetHeight(transcript.height)
	transcript.refresh(false)
}

// AppendUser adds a historic user surface; it never adds an editor.
func (transcript *Transcript) AppendUser(text string) {
	transcript.entries = append(transcript.entries, Entry{Kind: EntryUser, Text: text})
	transcript.activeAgent = -1
	transcript.refresh(true)
}

// AppendNotice adds muted typed status or failure text.
func (transcript *Transcript) AppendNotice(text string) {
	transcript.entries = append(transcript.entries, Entry{Kind: EntryNotice, Text: text})
	transcript.refresh(true)
}

// StartAgent creates one unframed provisional Agent item and clears old steps.
func (transcript *Transcript) StartAgent() {
	transcript.entries = append(transcript.entries, Entry{Kind: EntryAgent, Streaming: true})
	transcript.activeAgent = len(transcript.entries) - 1
	transcript.toolSteps.Reset()
	transcript.refresh(true)
}

// AppendAgent appends one accepted ordered delta to the active Agent item.
func (transcript *Transcript) AppendAgent(delta string) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) {
		return
	}
	transcript.entries[transcript.activeAgent].Text += delta
	transcript.refresh(true)
}

// FinishAgent replaces provisional text with one terminal safe result.
func (transcript *Transcript) FinishAgent(text string) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) {
		return
	}
	transcript.entries[transcript.activeAgent].Text = text
	transcript.entries[transcript.activeAgent].Streaming = false
	transcript.refresh(true)
}

// UpsertToolStep adds one step beneath the active Agent prose.
func (transcript *Transcript) UpsertToolStep(step ToolStep) {
	if transcript.activeAgent < 0 {
		return
	}
	transcript.toolSteps.Upsert(step)
	transcript.entries[transcript.activeAgent].ToolSteps = transcript.toolSteps.Items()
	transcript.refresh(true)
}

// Entries returns a defensive copy of non-editable transcript state.
func (transcript Transcript) Entries() []Entry {
	entries := append([]Entry(nil), transcript.entries...)
	for index := range entries {
		entries[index].ToolSteps = append([]ToolStep(nil), entries[index].ToolSteps...)
	}
	return entries
}

// ToolSteps returns a defensive copy of inline step state.
func (transcript Transcript) ToolSteps() []ToolStep { return transcript.toolSteps.Items() }

// PageUp scrolls transcript history without moving the composer.
func (transcript *Transcript) PageUp() { transcript.viewport.PageUp() }

// PageDown scrolls transcript history without moving the composer.
func (transcript *Transcript) PageDown() { transcript.viewport.PageDown() }

// View returns the current bounded transcript viewport.
func (transcript Transcript) View() string { return transcript.viewport.View() }

func (transcript *Transcript) refresh(follow bool) {
	wasAtBottom := transcript.viewport.AtBottom()
	transcript.viewport.SetContent(transcript.renderContent())
	if follow && wasAtBottom {
		transcript.viewport.GotoBottom()
	}
}

func (transcript Transcript) renderContent() string {
	parts := make([]string, 0, len(transcript.entries))
	for _, entry := range transcript.entries {
		var rendered string
		switch entry.Kind {
		case EntryUser:
			rendered = transcript.styles.UserSurface.Width(max(1, transcript.width-4)).Render(entry.Text)
		case EntryAgent:
			text := entry.Text
			if text == "" && entry.Streaming {
				text = transcript.styles.Placeholder.Render("Working…")
			}
			rendered = transcript.styles.AgentText.Width(max(1, transcript.width)).Render(text)
			stepRenderer := transcript.toolSteps
			stepRenderer.steps = append([]ToolStep(nil), entry.ToolSteps...)
			if steps := stepRenderer.View(); steps != "" {
				rendered += "\n" + steps
			}
		case EntryNotice:
			rendered = transcript.styles.NoticeText.Render(entry.Text)
		}
		parts = append(parts, rendered)
	}
	return strings.Join(parts, "\n\n")
}
