package components

import (
	"fmt"
	"strings"
	"time"

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
	Kind               EntryKind
	Text               string
	Streaming          bool
	ToolSteps          []ToolStep
	EvidenceReferences []EvidenceReference
	WorkedFor          time.Duration
	ShowWorkedFor      bool
	markdownCache      string
	markdownCacheWidth int
	markdownCacheValid bool
}

// EvidenceReference is a display-only transcript citation. Index maps back to
// the TUI-owned typed reference without carrying observation content here.
type EvidenceReference struct {
	Index int
	ID    string
	State string
}

// TranscriptStyles defines only semantic surfaces and prose styles.
type TranscriptStyles struct {
	UserSurface lipgloss.Style
	UserPrompt  lipgloss.Style
	UserText    lipgloss.Style
	AgentText   lipgloss.Style
	Markdown    MarkdownStyles
	NoticeText  lipgloss.Style
	Placeholder lipgloss.Style
	Evidence    lipgloss.Style
	Selected    lipgloss.Style
	Separator   lipgloss.Style
	Timing      lipgloss.Style
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
	selecting   bool
	selected    int
}

// NewTranscript creates an empty viewport. The root reducer owns mouse routing.
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

// SetStyles updates presentation while retaining conversation, selection,
// viewport, and inline Tool state.
func (transcript *Transcript) SetStyles(styles TranscriptStyles, toolStyles ToolStepStyles) {
	transcript.styles = styles
	transcript.toolSteps.SetStyles(toolStyles)
	transcript.invalidateMarkdownCaches()
	transcript.refresh(false)
}

// SetSize changes only viewport layout state.
func (transcript *Transcript) SetSize(width, height int) {
	nextWidth := max(8, width)
	if transcript.width != nextWidth {
		transcript.invalidateMarkdownCaches()
	}
	transcript.width = nextWidth
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
	transcript.entries[transcript.activeAgent].markdownCacheValid = false
	transcript.refresh(true)
}

// FinishAgent replaces provisional text with one terminal safe result.
func (transcript *Transcript) FinishAgent(text string) {
	transcript.finishAgent(text, 0, false)
}

// FinishAgentWithDuration replaces provisional text and records display-only
// local elapsed time for a newly completed run.
func (transcript *Transcript) FinishAgentWithDuration(text string, workedFor time.Duration) {
	transcript.finishAgent(text, workedFor, true)
}

func (transcript *Transcript) finishAgent(text string, workedFor time.Duration, showWorkedFor bool) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) {
		return
	}
	if workedFor < 0 {
		workedFor = 0
	}
	transcript.entries[transcript.activeAgent].Text = text
	transcript.entries[transcript.activeAgent].markdownCacheValid = false
	transcript.entries[transcript.activeAgent].Streaming = false
	transcript.entries[transcript.activeAgent].WorkedFor = workedFor
	transcript.entries[transcript.activeAgent].ShowWorkedFor = showWorkedFor
	transcript.refresh(true)
}

// SetAgentEvidence adds bounded citations to the current terminal Agent item.
func (transcript *Transcript) SetAgentEvidence(references []EvidenceReference) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) || len(references) > 100 {
		return
	}
	transcript.entries[transcript.activeAgent].EvidenceReferences = append([]EvidenceReference(nil), references...)
	transcript.refresh(true)
}

// BeginEvidenceSelection selects the first citation in the newest Agent item.
func (transcript *Transcript) BeginEvidenceSelection() bool {
	for entryIndex := len(transcript.entries) - 1; entryIndex >= 0; entryIndex-- {
		if len(transcript.entries[entryIndex].EvidenceReferences) == 0 {
			continue
		}
		transcript.selecting = true
		transcript.selected = transcript.entries[entryIndex].EvidenceReferences[0].Index
		transcript.refresh(false)
		return true
	}
	return false
}

// EndEvidenceSelection clears keyboard selection without changing citations.
func (transcript *Transcript) EndEvidenceSelection() {
	transcript.selecting = false
	transcript.selected = 0
	transcript.refresh(false)
}

// EvidenceSelecting reports whether citation navigation owns arrow keys.
func (transcript Transcript) EvidenceSelecting() bool { return transcript.selecting }

// MoveEvidence moves through all visible citations with deterministic wrap.
func (transcript *Transcript) MoveEvidence(delta int) {
	indexes := transcript.evidenceIndexes()
	if !transcript.selecting || len(indexes) == 0 {
		return
	}
	position := 0
	for index, value := range indexes {
		if value == transcript.selected {
			position = index
			break
		}
	}
	position = (position + delta%len(indexes) + len(indexes)) % len(indexes)
	transcript.selected = indexes[position]
	transcript.refresh(false)
}

// SelectedEvidence returns the TUI-owned typed-reference index.
func (transcript Transcript) SelectedEvidence() (int, bool) {
	if !transcript.selecting {
		return 0, false
	}
	for _, index := range transcript.evidenceIndexes() {
		if index == transcript.selected {
			return index, true
		}
	}
	return 0, false
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
		entries[index].EvidenceReferences = append([]EvidenceReference(nil), entries[index].EvidenceReferences...)
		entries[index].markdownCache = ""
		entries[index].markdownCacheWidth = 0
		entries[index].markdownCacheValid = false
	}
	return entries
}

// ToolSteps returns a defensive copy of inline step state.
func (transcript Transcript) ToolSteps() []ToolStep { return transcript.toolSteps.Items() }

// PageUp scrolls transcript history without moving the composer.
func (transcript *Transcript) PageUp() { transcript.viewport.PageUp() }

// PageDown scrolls transcript history without moving the composer.
func (transcript *Transcript) PageDown() { transcript.viewport.PageDown() }

// ScrollUp moves the transcript by a bounded number of rows.
func (transcript *Transcript) ScrollUp(rows int) { transcript.viewport.ScrollUp(max(1, rows)) }

// ScrollDown moves the transcript by a bounded number of rows.
func (transcript *Transcript) ScrollDown(rows int) { transcript.viewport.ScrollDown(max(1, rows)) }

// ScrollOffset reports transcript position for deterministic reducer tests.
func (transcript Transcript) ScrollOffset() int { return transcript.viewport.YOffset() }

// View returns the current bounded transcript viewport.
func (transcript Transcript) View() string { return transcript.viewport.View() }

func (transcript *Transcript) refresh(follow bool) {
	wasAtBottom := transcript.viewport.AtBottom()
	transcript.viewport.SetContent(transcript.renderContent())
	if follow && wasAtBottom {
		transcript.viewport.GotoBottom()
	}
}

func (transcript *Transcript) renderContent() string {
	parts := make([]string, 0, len(transcript.entries))
	for entryIndex := range transcript.entries {
		entry := &transcript.entries[entryIndex]
		var rendered string
		switch entry.Kind {
		case EntryUser:
			// Both segments carry the surface background explicitly. Lipgloss
			// resets SGR state after the styled prompt, so leaving the body raw
			// would make only the historic message text fall back to the terminal
			// background inside the otherwise continuous user surface.
			content := transcript.styles.UserPrompt.Render("› ") + transcript.styles.UserText.Render(entry.Text)
			rendered = transcript.styles.UserSurface.Width(max(1, transcript.width-2)).Render(content)
		case EntryAgent:
			blocks := make([]string, 0, 5)
			stepRenderer := transcript.toolSteps
			stepRenderer.steps = append([]ToolStep(nil), entry.ToolSteps...)
			steps := stepRenderer.View()
			if steps != "" {
				blocks = append(blocks, steps)
			}
			text := entry.Text
			if text == "" && entry.Streaming {
				text = transcript.styles.Placeholder.Render("Working…")
			}
			if text != "" {
				if steps != "" && !entry.Streaming {
					blocks = append(blocks, transcript.styles.Separator.Render(strings.Repeat("─", max(1, transcript.width))))
				}
				blocks = append(blocks, transcript.renderMarkdown(entry))
			}
			if references := transcript.renderEvidenceReferences(entry.EvidenceReferences); references != "" {
				blocks = append(blocks, references)
			}
			if entry.ShowWorkedFor && !entry.Streaming {
				blocks = append(blocks, transcript.styles.Timing.Render("Worked for "+formatWorkedFor(entry.WorkedFor)))
			}
			rendered = strings.Join(blocks, "\n\n")
		case EntryNotice:
			rendered = transcript.styles.NoticeText.Render(entry.Text)
		}
		parts = append(parts, rendered)
	}
	return strings.Join(parts, "\n\n")
}

func (transcript *Transcript) renderMarkdown(entry *Entry) string {
	if entry.markdownCacheValid && entry.markdownCacheWidth == transcript.width {
		return entry.markdownCache
	}
	markdownStyles := transcript.styles.Markdown
	markdownStyles.Text = markdownStyles.Text.Inherit(transcript.styles.AgentText)
	entry.markdownCache = renderTerminalMarkdown(entry.Text, transcript.width, markdownStyles)
	entry.markdownCacheWidth = transcript.width
	entry.markdownCacheValid = true
	return entry.markdownCache
}

func (transcript *Transcript) invalidateMarkdownCaches() {
	for index := range transcript.entries {
		transcript.entries[index].markdownCache = ""
		transcript.entries[index].markdownCacheWidth = 0
		transcript.entries[index].markdownCacheValid = false
	}
}

func formatWorkedFor(duration time.Duration) string {
	if duration < time.Second {
		return "<1s"
	}
	return duration.Truncate(time.Second).String()
}

func (transcript Transcript) renderEvidenceReferences(references []EvidenceReference) string {
	if len(references) == 0 || !transcript.selecting {
		return ""
	}
	for position, reference := range references {
		if reference.Index == transcript.selected {
			return transcript.styles.Selected.Render(fmt.Sprintf(
				"› Observation %d/%d · %s",
				position+1,
				len(references),
				observationReferenceStatus(reference.State),
			))
		}
	}
	return ""
}

func observationReferenceStatus(state string) string {
	switch state {
	case "available":
		return "ready"
	case "partial":
		return "partial"
	case "expired":
		return "expired"
	default:
		return "unavailable"
	}
}

func (transcript Transcript) evidenceIndexes() []int {
	var result []int
	for _, entry := range transcript.entries {
		for _, reference := range entry.EvidenceReferences {
			result = append(result, reference.Index)
		}
	}
	return result
}
