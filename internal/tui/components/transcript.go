package components

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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

// TranscriptLandmark is a fixed semantic navigation class. It carries no
// content, authority, or persistence semantics.
type TranscriptLandmark uint8

const (
	TranscriptLandmarkUser TranscriptLandmark = 1 << iota
	TranscriptLandmarkAssistantFinal
	TranscriptLandmarkFailureUnknown
	TranscriptLandmarkApproval
)

// Entry is one bounded, non-editable transcript item.
type Entry struct {
	Kind               EntryKind
	Text               string
	Streaming          bool
	CommittedFinal     bool
	ToolSteps          []ToolStep
	EvidenceReferences []EvidenceReference
	Provenance         AnswerProvenance
	TerminalSummary    string
	Landmark           TranscriptLandmark
	WorkedFor          time.Duration
	ShowWorkedFor      bool
	markdownCache      string
	markdownCacheWidth int
	markdownCacheValid bool
}

// EvidenceReference is a display-only transcript citation. Index maps back to
// the TUI-owned typed reference without carrying observation content here.
type EvidenceReference struct {
	Index      int
	ID         string
	State      string
	Claims     []int
	ClaimKinds []string
}

// AnswerProvenance is already content-free display metadata from Application.
type AnswerProvenance struct {
	Visible        bool
	EvidenceCount  int
	ObservedRange  string
	Generation     string
	Coverage       string
	Reasoning      string
	SourceCoverage string
}

// TranscriptStyles defines only semantic surfaces and prose styles.
type TranscriptStyles struct {
	UserSurface lipgloss.Style
	UserPrompt  lipgloss.Style
	UserText    lipgloss.Style
	AgentText   lipgloss.Style
	Markdown    MarkdownStyles
	NoticeText  lipgloss.Style
	Evidence    lipgloss.Style
	Selected    lipgloss.Style
	Separator   lipgloss.Style
	Timing      lipgloss.Style
}

// Transcript is a continuous scrollable conversation projection.
type Transcript struct {
	entries       []Entry
	activeAgent   int
	committed     int
	prepared      int
	viewport      viewport.Model
	width         int
	height        int
	maxHeight     int
	styles        TranscriptStyles
	toolSteps     ToolSteps
	selecting     bool
	selected      int
	selectedEntry int
	selectedClaim int
	searching     bool
	search        []SearchMatch
	searchIndex   int
	searchNow     func() time.Time
	reviewing     bool
	landmarkEntry int
	landmarkKind  TranscriptLandmark
	visible       bool
}

const (
	terminalHistorySeparatorRows  = 1
	MaxTranscriptSearchQueryBytes = 512
	MaxTranscriptSearchMatches    = 100
	MaxTranscriptSearchEntries    = 4096
	MaxTranscriptSearchBytes      = 4 * 1024 * 1024
	MaxTranscriptSearchDuration   = 50 * time.Millisecond
)

var (
	ErrTranscriptSearchInvalid = errors.New("transcript search query is invalid")
	ErrTranscriptSearchLimit   = errors.New("transcript search limit was reached")
)

// SearchMatch identifies one exact byte range in a committed transcript entry.
// It is current-process display state and never carries authority or persistence.
type SearchMatch struct {
	EntryIndex int
	StartByte  int
	EndByte    int
}

// NewTranscript creates an empty viewport. The root reducer owns mouse routing.
func NewTranscript(styles TranscriptStyles, toolStyles ToolStepStyles) Transcript {
	view := viewport.New(viewport.WithWidth(80), viewport.WithHeight(12))
	view.SoftWrap = true
	view.FillHeight = true
	view.MouseWheelEnabled = false
	return Transcript{
		activeAgent:   -1,
		landmarkEntry: -1,
		selectedEntry: -1,
		searchNow:     time.Now,
		viewport:      view,
		width:         80,
		height:        12,
		maxHeight:     12,
		styles:        styles,
		toolSteps:     NewToolSteps(toolStyles),
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
	transcript.maxHeight = max(1, height)
	transcript.viewport.SetWidth(transcript.width)
	transcript.refresh(false)
}

// AppendUser adds a historic user surface; it never adds an editor.
func (transcript *Transcript) AppendUser(text string) {
	transcript.entries = append(transcript.entries, Entry{Kind: EntryUser, Text: text, Landmark: TranscriptLandmarkUser})
	transcript.activeAgent = -1
	transcript.reviewing = false
	transcript.refresh(false)
	transcript.viewport.GotoBottom()
}

// InsertUserBeforeActiveAgent commits a steer in the current run without
// detaching the one streaming Agent entry. The final answer and Tool steps
// remain after every user input committed for that run.
func (transcript *Transcript) InsertUserBeforeActiveAgent(text string) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) ||
		!transcript.entries[transcript.activeAgent].Streaming {
		transcript.AppendUser(text)
		return
	}
	index := transcript.activeAgent
	transcript.entries = append(transcript.entries, Entry{})
	copy(transcript.entries[index+1:], transcript.entries[index:len(transcript.entries)-1])
	transcript.entries[index] = Entry{Kind: EntryUser, Text: text, Landmark: TranscriptLandmarkUser}
	transcript.activeAgent++
	transcript.reviewing = false
	transcript.refresh(false)
	transcript.viewport.GotoBottom()
}

// AppendNotice adds muted typed status or failure text.
func (transcript *Transcript) AppendNotice(text string) {
	transcript.entries = append(transcript.entries, Entry{Kind: EntryNotice, Text: text})
	transcript.refresh(true)
}

// AppendLandmarkNotice records a typed navigable status without parsing its
// prose. Only fixed delivery call sites select the landmark class.
func (transcript *Transcript) AppendLandmarkNotice(text string, landmark TranscriptLandmark) {
	if landmark != TranscriptLandmarkFailureUnknown && landmark != TranscriptLandmarkApproval {
		transcript.AppendNotice(text)
		return
	}
	transcript.entries = append(transcript.entries, Entry{Kind: EntryNotice, Text: text, Landmark: landmark})
	transcript.refresh(true)
}

// StartAgent creates one unframed provisional Agent item and clears old steps.
func (transcript *Transcript) StartAgent() {
	transcript.entries = append(transcript.entries, Entry{Kind: EntryAgent, Streaming: true})
	transcript.activeAgent = len(transcript.entries) - 1
	transcript.toolSteps.Reset()
	transcript.reviewing = false
	transcript.refresh(false)
	transcript.viewport.GotoBottom()
}

// AppendAgent appends one accepted ordered delta to the active Agent item.
func (transcript *Transcript) AppendAgent(delta string) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) ||
		!transcript.entries[transcript.activeAgent].Streaming {
		return
	}
	transcript.entries[transcript.activeAgent].Text += delta
	transcript.entries[transcript.activeAgent].markdownCacheValid = false
	transcript.refresh(true)
}

// ClearAgent discards provisional prose from a pre-Tool model turn while
// retaining the active streaming entry for the eventual final answer.
func (transcript *Transcript) ClearAgent() {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) ||
		!transcript.entries[transcript.activeAgent].Streaming || transcript.entries[transcript.activeAgent].Text == "" {
		return
	}
	transcript.entries[transcript.activeAgent].Text = ""
	transcript.entries[transcript.activeAgent].markdownCache = ""
	transcript.entries[transcript.activeAgent].markdownCacheWidth = 0
	transcript.entries[transcript.activeAgent].markdownCacheValid = false
	transcript.refresh(true)
}

// FinishAgent replaces provisional text with one terminal safe result.
func (transcript *Transcript) FinishAgent(text string) {
	transcript.finishAgent(text, 0, false, false)
}

// FinishAgentWithDuration replaces provisional text and records display-only
// local elapsed time for a newly completed run.
func (transcript *Transcript) FinishAgentWithDuration(text string, workedFor time.Duration) {
	transcript.finishAgent(text, workedFor, true, false)
}

// FinishCommittedAgent records one persisted or persistable successful final
// answer. Only entries finished through this path are eligible for /copy and
// assistant-side transcript search.
func (transcript *Transcript) FinishCommittedAgent(text string) {
	transcript.finishAgent(text, 0, false, true)
}

// FinishCommittedAgentWithDuration records a successful final answer and its
// local display-only elapsed duration.
func (transcript *Transcript) FinishCommittedAgentWithDuration(text string, workedFor time.Duration) {
	transcript.finishAgent(text, workedFor, true, true)
}

func (transcript *Transcript) finishAgent(text string, workedFor time.Duration, showWorkedFor, committedFinal bool) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) ||
		!transcript.entries[transcript.activeAgent].Streaming {
		return
	}
	if workedFor < 0 {
		workedFor = 0
	}
	transcript.entries[transcript.activeAgent].Text = text
	transcript.entries[transcript.activeAgent].markdownCacheValid = false
	transcript.entries[transcript.activeAgent].Streaming = false
	transcript.entries[transcript.activeAgent].CommittedFinal = committedFinal
	transcript.entries[transcript.activeAgent].WorkedFor = workedFor
	transcript.entries[transcript.activeAgent].ShowWorkedFor = showWorkedFor
	transcript.refresh(true)
}

// SetAgentEvidence adds bounded citations to the current terminal Agent item.
func (transcript *Transcript) SetAgentEvidence(references []EvidenceReference) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) ||
		transcript.entries[transcript.activeAgent].Streaming || len(references) > 100 {
		return
	}
	transcript.entries[transcript.activeAgent].EvidenceReferences = append([]EvidenceReference(nil), references...)
	transcript.refresh(true)
}

// SetAgentProvenance adds the low-chrome content-free provenance strip to the
// current terminal Agent entry.
func (transcript *Transcript) SetAgentProvenance(provenance AnswerProvenance) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) ||
		transcript.entries[transcript.activeAgent].Streaming || !provenance.Visible {
		return
	}
	transcript.entries[transcript.activeAgent].Provenance = provenance
	transcript.refresh(true)
}

// SetAgentTerminalSummary records Application-selected reason and next actions.
func (transcript *Transcript) SetAgentTerminalSummary(summary string) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) ||
		transcript.entries[transcript.activeAgent].Streaming || summary == "" {
		return
	}
	transcript.entries[transcript.activeAgent].TerminalSummary = summary
	transcript.refresh(true)
}

// SetAgentLandmark assigns an Application-projected semantic class to the
// current terminal Agent entry. It never derives a class from answer prose.
func (transcript *Transcript) SetAgentLandmark(landmark TranscriptLandmark) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) ||
		transcript.entries[transcript.activeAgent].Streaming ||
		(landmark != TranscriptLandmarkAssistantFinal && landmark != TranscriptLandmarkFailureUnknown) {
		return
	}
	transcript.entries[transcript.activeAgent].Landmark |= landmark
	transcript.refresh(true)
}

// JumpLandmark moves to the previous or next entry in one fixed semantic
// class. Selection is rendered with a textual marker, never color alone.
func (transcript *Transcript) JumpLandmark(landmark TranscriptLandmark, delta int) bool {
	if len(transcript.entries) == 0 || delta == 0 ||
		(landmark != TranscriptLandmarkUser && landmark != TranscriptLandmarkAssistantFinal &&
			landmark != TranscriptLandmarkFailureUnknown && landmark != TranscriptLandmarkApproval) {
		return false
	}
	indexes := make([]int, 0, len(transcript.entries))
	for index, entry := range transcript.entries {
		if entry.Landmark&landmark != 0 {
			indexes = append(indexes, index)
		}
	}
	if len(indexes) == 0 {
		return false
	}
	position := -1
	for index, value := range indexes {
		if value == transcript.landmarkEntry && transcript.landmarkKind == landmark {
			position = index
			break
		}
	}
	if position < 0 {
		if delta < 0 {
			position = len(indexes)
		}
	}
	position = (position + delta%len(indexes) + len(indexes)) % len(indexes)
	transcript.landmarkEntry = indexes[position]
	transcript.landmarkKind = landmark
	transcript.reviewing = true
	transcript.refresh(false)
	transcript.revealEntry(transcript.landmarkEntry)
	return true
}

// BeginEvidenceSelection selects the first citation in the newest Agent item.
func (transcript *Transcript) BeginEvidenceSelection() bool {
	for entryIndex := len(transcript.entries) - 1; entryIndex >= 0; entryIndex-- {
		if len(transcript.entries[entryIndex].EvidenceReferences) == 0 {
			continue
		}
		transcript.selecting = true
		transcript.selectedEntry = entryIndex
		claims := claimSequences(transcript.entries[entryIndex].EvidenceReferences)
		if len(claims) > 0 {
			transcript.selectedClaim = claims[0]
			transcript.selected = evidenceIndexesForClaim(transcript.entries[entryIndex].EvidenceReferences, claims[0])[0]
		} else {
			transcript.selectedClaim = 0
			transcript.selected = transcript.entries[entryIndex].EvidenceReferences[0].Index
		}
		transcript.reviewing = true
		transcript.refresh(false)
		transcript.viewport.GotoBottom()
		return true
	}
	return false
}

// EndEvidenceSelection clears keyboard selection without changing citations.
func (transcript *Transcript) EndEvidenceSelection() {
	transcript.selecting = false
	transcript.selected = 0
	transcript.selectedEntry = -1
	transcript.selectedClaim = 0
	transcript.reviewing = false
	transcript.refresh(true)
}

// EvidenceSelecting reports whether citation navigation owns arrow keys.
func (transcript Transcript) EvidenceSelecting() bool { return transcript.selecting }

// MoveEvidence moves through the exact citations for the selected claim, or
// through the selected final's citations when legacy metadata has no claims.
func (transcript *Transcript) MoveEvidence(delta int) {
	indexes := transcript.selectedEvidenceIndexes()
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

// MoveEvidenceClaim moves through the bounded claim index for one committed
// final and selects the first exact Evidence reference for the new claim.
func (transcript *Transcript) MoveEvidenceClaim(delta int) {
	if !transcript.selecting || transcript.selectedEntry < 0 || transcript.selectedEntry >= len(transcript.entries) || delta == 0 {
		return
	}
	claims := claimSequences(transcript.entries[transcript.selectedEntry].EvidenceReferences)
	if len(claims) == 0 {
		return
	}
	position := 0
	for index, sequence := range claims {
		if sequence == transcript.selectedClaim {
			position = index
			break
		}
	}
	position = (position + delta%len(claims) + len(claims)) % len(claims)
	transcript.selectedClaim = claims[position]
	indexes := evidenceIndexesForClaim(transcript.entries[transcript.selectedEntry].EvidenceReferences, transcript.selectedClaim)
	if len(indexes) > 0 {
		transcript.selected = indexes[0]
	}
	transcript.refresh(false)
}

// SelectedEvidence returns the TUI-owned typed-reference index.
func (transcript Transcript) SelectedEvidence() (int, bool) {
	if !transcript.selecting {
		return 0, false
	}
	for _, index := range transcript.selectedEvidenceIndexes() {
		if index == transcript.selected {
			return index, true
		}
	}
	return 0, false
}

// UpsertToolStep adds one step beneath the active Agent prose.
func (transcript *Transcript) UpsertToolStep(step ToolStep) {
	if transcript.activeAgent < 0 || transcript.activeAgent >= len(transcript.entries) ||
		!transcript.entries[transcript.activeAgent].Streaming {
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
		for referenceIndex := range entries[index].EvidenceReferences {
			entries[index].EvidenceReferences[referenceIndex].Claims = append([]int(nil), entries[index].EvidenceReferences[referenceIndex].Claims...)
			entries[index].EvidenceReferences[referenceIndex].ClaimKinds = append([]string(nil), entries[index].EvidenceReferences[referenceIndex].ClaimKinds...)
		}
		entries[index].markdownCache = ""
		entries[index].markdownCacheWidth = 0
		entries[index].markdownCacheValid = false
	}
	return entries
}

// ToolSteps returns a defensive copy of inline step state.
func (transcript Transcript) ToolSteps() []ToolStep { return transcript.toolSteps.Items() }

// LatestCommittedAssistantFinal returns only the newest successful committed
// assistant final. Provisional, failed, cancelled, and notice entries are
// deliberately ineligible.
func (transcript Transcript) LatestCommittedAssistantFinal() (string, bool) {
	for index := len(transcript.entries) - 1; index >= 0; index-- {
		entry := transcript.entries[index]
		if entry.Kind == EntryAgent && entry.CommittedFinal && !entry.Streaming && entry.Text != "" {
			return entry.Text, true
		}
	}
	return "", false
}

// HasSearchableContent reports whether the bounded local transcript contains
// a committed ordinary user message or committed assistant final.
func (transcript Transcript) HasSearchableContent() bool {
	for _, entry := range transcript.entries {
		if entry.Kind == EntryUser || entry.Kind == EntryAgent && entry.CommittedFinal && !entry.Streaming {
			return true
		}
	}
	return false
}

// BeginSearch scans only committed user messages and successful assistant
// finals. It rejects rather than truncates at every bound.
func (transcript *Transcript) BeginSearch(query string) (int, error) {
	if query == "" || !utf8.ValidString(query) || len(query) > MaxTranscriptSearchQueryBytes {
		return 0, ErrTranscriptSearchInvalid
	}
	now := transcript.searchNow
	if now == nil {
		now = time.Now
	}
	deadline := now().Add(MaxTranscriptSearchDuration)
	matches := make([]SearchMatch, 0, min(8, MaxTranscriptSearchMatches))
	eligibleEntries := 0
	eligibleBytes := 0
	for entryIndex, entry := range transcript.entries {
		if now().After(deadline) {
			return 0, ErrTranscriptSearchLimit
		}
		eligible := entry.Kind == EntryUser || entry.Kind == EntryAgent && entry.CommittedFinal && !entry.Streaming
		if !eligible {
			continue
		}
		eligibleEntries++
		eligibleBytes += len(entry.Text)
		if eligibleEntries > MaxTranscriptSearchEntries || eligibleBytes > MaxTranscriptSearchBytes {
			return 0, ErrTranscriptSearchLimit
		}
		for offset := 0; offset <= len(entry.Text)-len(query); {
			if now().After(deadline) {
				return 0, ErrTranscriptSearchLimit
			}
			relative := strings.Index(entry.Text[offset:], query)
			if now().After(deadline) {
				return 0, ErrTranscriptSearchLimit
			}
			if relative < 0 {
				break
			}
			start := offset + relative
			matches = append(matches, SearchMatch{EntryIndex: entryIndex, StartByte: start, EndByte: start + len(query)})
			if len(matches) > MaxTranscriptSearchMatches {
				return 0, ErrTranscriptSearchLimit
			}
			offset = start + len(query)
		}
	}
	transcript.searching = true
	transcript.search = matches
	transcript.searchIndex = 0
	transcript.reviewing = true
	transcript.refresh(false)
	transcript.revealSearchMatch()
	return len(matches), nil
}

// MoveSearch selects the next or previous exact match with deterministic wrap.
func (transcript *Transcript) MoveSearch(delta int) {
	if !transcript.searching || len(transcript.search) == 0 {
		return
	}
	transcript.searchIndex = (transcript.searchIndex + delta%len(transcript.search) + len(transcript.search)) % len(transcript.search)
	transcript.refresh(false)
	transcript.revealSearchMatch()
}

// EndSearch removes every ephemeral query result and marker.
func (transcript *Transcript) EndSearch() {
	transcript.searching = false
	transcript.search = nil
	transcript.searchIndex = 0
	transcript.reviewing = false
	transcript.landmarkEntry = -1
	transcript.landmarkKind = 0
	transcript.refresh(true)
}

// SearchState reports bounded display-only navigation state.
func (transcript Transcript) SearchState() (current, total int, active bool) {
	if !transcript.searching {
		return 0, 0, false
	}
	if len(transcript.search) == 0 {
		return 0, 0, true
	}
	return transcript.searchIndex + 1, len(transcript.search), true
}

// TerminalTranscript returns every completed terminal-safe entry. A streaming
// Agent entry stops the projection so a provisional model draft cannot enter
// terminal-owned scrollback.
func (transcript *Transcript) TerminalTranscript() string {
	end := transcript.committableEnd()
	if end == 0 {
		return ""
	}
	return transcript.renderRange(0, end, false)
}

// PrepareCommit removes the next immutable block from the live projection
// without claiming that the terminal renderer has inserted it. A streaming
// Agent entry is a barrier: neither its provisional prose nor later entries can
// reach terminal-owned scrollback until the Agent becomes terminal.
func (transcript *Transcript) PrepareCommit() (string, int, int) {
	if transcript.prepared > transcript.committed {
		return "", 0, 0
	}
	end := transcript.committableEnd()
	if end <= transcript.committed {
		return "", 0, 0
	}
	block := transcript.renderCommitBlock(transcript.committed, end)
	if block == "" {
		transcript.committed = end
		transcript.prepared = end
		transcript.refresh(true)
		return "", 0, 0
	}
	transcript.prepared = end
	transcript.refresh(true)
	return block, lipgloss.Height(block), end
}

// CompleteCommit records that one prepared block reached terminal scrollback.
func (transcript *Transcript) CompleteCommit(end int) bool {
	if end <= transcript.committed || end != transcript.prepared {
		return false
	}
	transcript.committed = end
	transcript.refresh(true)
	return true
}

// AbortCommit restores a prepared block to the live projection when the
// terminal cannot provide one safe insertion row.
func (transcript *Transcript) AbortCommit(end int) bool {
	if end <= transcript.committed || end != transcript.prepared {
		return false
	}
	transcript.prepared = transcript.committed
	transcript.refresh(true)
	return true
}

// CommitReady is the synchronous component-level form used outside the
// terminal renderer. Runtime insertion uses PrepareCommit and CompleteCommit
// so pending output remains recoverable until it has actually been handed off.
func (transcript *Transcript) CommitReady() (string, int) {
	block, rows, end := transcript.PrepareCommit()
	if block == "" {
		return "", 0
	}
	if !transcript.CompleteCommit(end) {
		return "", 0
	}
	return block, rows
}

// PendingTerminalTranscript returns immutable history that has not yet been
// handed to the running terminal renderer. It is used only as a bounded
// shutdown fallback.
func (transcript *Transcript) PendingTerminalTranscript() string {
	end := transcript.committableEnd()
	if end <= transcript.committed {
		return ""
	}
	return transcript.renderCommitBlock(transcript.committed, end)
}

func (transcript *Transcript) renderCommitBlock(start, end int) string {
	block := transcript.renderRange(start, end, false)
	if block == "" {
		return ""
	}
	// Every renderer-owned immutable block leaves one inert row after itself.
	// That single rule separates submitted history from the live Working row
	// and a final Worked separator from the composer without transient chrome.
	return block + strings.Repeat("\n", terminalHistorySeparatorRows)
}

func (transcript *Transcript) committableEnd() int {
	end := 0
	for end < len(transcript.entries) {
		entry := transcript.entries[end]
		if entry.Kind == EntryAgent && entry.Streaming {
			break
		}
		end++
	}
	return end
}

// PageUp scrolls the retained in-memory transcript without moving the composer.
func (transcript *Transcript) PageUp() {
	transcript.beginReview()
	transcript.viewport.PageUp()
}

// PageDown scrolls retained history and returns to the live projection at the
// bottom boundary.
func (transcript *Transcript) PageDown() {
	transcript.viewport.PageDown()
	if transcript.reviewing && transcript.viewport.AtBottom() && !transcript.selecting {
		transcript.reviewing = false
		transcript.landmarkEntry = -1
		transcript.landmarkKind = 0
		transcript.refresh(true)
	}
}

// ScrollUp moves the transcript by a bounded number of rows.
func (transcript *Transcript) ScrollUp(rows int) {
	transcript.beginReview()
	transcript.viewport.ScrollUp(max(1, rows))
}

// ScrollDown moves the transcript by a bounded number of rows.
func (transcript *Transcript) ScrollDown(rows int) {
	transcript.viewport.ScrollDown(max(1, rows))
	if transcript.reviewing && transcript.viewport.AtBottom() && !transcript.selecting {
		transcript.reviewing = false
		transcript.landmarkEntry = -1
		transcript.landmarkKind = 0
		transcript.refresh(true)
	}
}

// ScrollOffset reports transcript position for deterministic reducer tests.
func (transcript Transcript) ScrollOffset() int { return transcript.viewport.YOffset() }

// View returns the current bounded live or explicitly reviewed transcript.
func (transcript Transcript) View() string {
	if !transcript.visible {
		return ""
	}
	return transcript.viewport.View()
}

// Visible reports whether the managed frame currently contains transcript rows.
func (transcript Transcript) Visible() bool { return transcript.visible }

func (transcript *Transcript) refresh(follow bool) {
	wasAtBottom := transcript.viewport.AtBottom()
	content := transcript.renderVisibleContent()
	transcript.visible = content != ""
	contentHeight := lipgloss.Height(content)
	if contentHeight < 1 {
		contentHeight = 1
	}
	transcript.height = min(transcript.maxHeight, contentHeight)
	transcript.viewport.SetHeight(transcript.height)
	transcript.viewport.SetContent(content)
	// Outside explicit transcript review, every reflow remains attached to the
	// live bottom. Working-state layout changes can shrink the viewport before
	// new content arrives; relying only on the old AtBottom value would strand
	// the viewport above a newly submitted user surface and all later output.
	if !transcript.reviewing || (follow && wasAtBottom) {
		transcript.viewport.GotoBottom()
	}
}

func (transcript *Transcript) renderContent() string {
	return transcript.renderRange(0, len(transcript.entries), transcript.selecting || transcript.landmarkEntry >= 0)
}

func (transcript *Transcript) renderVisibleContent() string {
	if transcript.reviewing || transcript.selecting || transcript.searching {
		return transcript.renderRange(0, len(transcript.entries), transcript.selecting || transcript.searching || transcript.landmarkEntry >= 0)
	}
	return transcript.renderRange(max(transcript.committed, transcript.prepared), len(transcript.entries), false)
}

func (transcript *Transcript) renderRange(start, end int, includeSelection bool) string {
	start = max(0, min(start, len(transcript.entries)))
	end = max(start, min(end, len(transcript.entries)))
	parts := make([]string, 0, end-start)
	for entryIndex := start; entryIndex < end; entryIndex++ {
		entry := &transcript.entries[entryIndex]
		entryText := entry.Text
		searchLabel := ""
		if includeSelection && transcript.searching && len(transcript.search) > 0 {
			match := transcript.search[transcript.searchIndex]
			if match.EntryIndex == entryIndex && match.StartByte >= 0 && match.EndByte <= len(entryText) && match.StartByte < match.EndByte {
				entryText = entryText[:match.StartByte] + "⟦" + entryText[match.StartByte:match.EndByte] + "⟧" + entryText[match.EndByte:]
				searchLabel = transcript.styles.Selected.Render(fmt.Sprintf("Search match %d/%d", transcript.searchIndex+1, len(transcript.search)))
			}
		}
		var rendered string
		switch entry.Kind {
		case EntryUser:
			// Both segments carry the surface background explicitly. Lipgloss
			// resets SGR state after the styled prompt, so leaving the body raw
			// would make only the historic message text fall back to the terminal
			// background inside the otherwise continuous user surface.
			content := transcript.renderUserEntry(entryText)
			rendered = transcript.styles.UserSurface.Width(transcript.width).Render(content)
		case EntryAgent:
			blocks := make([]string, 0, 7)
			stepRenderer := transcript.toolSteps
			stepRenderer.steps = append([]ToolStep(nil), entry.ToolSteps...)
			steps := stepRenderer.View()
			if steps != "" {
				blocks = append(blocks, steps)
			}
			if entryText != "" {
				if steps != "" && !entry.Streaming {
					blocks = append(blocks, transcript.styles.Separator.Render(strings.Repeat("─", max(1, transcript.width))))
				}
				if entryText == entry.Text {
					blocks = append(blocks, transcript.renderMarkdown(entry))
				} else {
					selected := *entry
					selected.Text = entryText
					selected.markdownCacheValid = false
					blocks = append(blocks, transcript.renderMarkdown(&selected))
				}
			}
			if references := transcript.renderEvidenceReferences(entryIndex, entry.EvidenceReferences, includeSelection); references != "" {
				blocks = append(blocks, references)
			}
			if metadata := transcript.renderAnswerMetadata(entry.Provenance, entry.TerminalSummary); metadata != "" {
				blocks = append(blocks, metadata)
			}
			if entry.ShowWorkedFor && !entry.Streaming {
				blocks = append(blocks, renderWorkedFor(entry.WorkedFor, transcript.width, transcript.styles.Timing))
			}
			rendered = strings.Join(blocks, "\n\n")
		case EntryNotice:
			rendered = transcript.styles.NoticeText.Render(entry.Text)
		}
		if searchLabel != "" && rendered != "" {
			rendered = searchLabel + "\n" + rendered
		}
		if includeSelection && transcript.landmarkEntry == entryIndex && rendered != "" {
			rendered = transcript.styles.Selected.Render("Jump target · "+landmarkLabel(transcript.landmarkKind)) + "\n" + rendered
		}
		if rendered != "" {
			parts = append(parts, rendered)
		}
	}
	return strings.Join(parts, "\n\n")
}

func (transcript *Transcript) revealSearchMatch() {
	if !transcript.searching || len(transcript.search) == 0 {
		transcript.viewport.GotoTop()
		return
	}
	entryIndex := transcript.search[transcript.searchIndex].EntryIndex
	prefix := transcript.renderRange(0, entryIndex, true)
	line := lipgloss.Height(prefix)
	if entryIndex > 0 && prefix != "" {
		line++
	}
	transcript.viewport.SetYOffset(max(0, line-1))
}

func (transcript *Transcript) revealEntry(entryIndex int) {
	if entryIndex < 0 || entryIndex >= len(transcript.entries) {
		return
	}
	prefix := transcript.renderRange(0, entryIndex, true)
	line := lipgloss.Height(prefix)
	if entryIndex > 0 && prefix != "" {
		line++
	}
	transcript.viewport.SetYOffset(max(0, line-1))
}

func landmarkLabel(landmark TranscriptLandmark) string {
	switch landmark {
	case TranscriptLandmarkUser:
		return "user message"
	case TranscriptLandmarkAssistantFinal:
		return "assistant final"
	case TranscriptLandmarkFailureUnknown:
		return "failure or unknown outcome"
	case TranscriptLandmarkApproval:
		return "approval"
	default:
		return "transcript item"
	}
}

func (transcript Transcript) renderUserEntry(text string) string {
	// The two-column prompt is the only left inset. The model-level content
	// width already reserves the terminal's final column for safe wrapping.
	lines := strings.Split(wrapTerminalText(text, max(1, transcript.width-2)), "\n")
	for index, line := range lines {
		prompt := "  "
		if index == 0 {
			prompt = "› "
		}
		lines[index] = transcript.styles.UserPrompt.Render(prompt) + transcript.styles.UserText.Render(line)
	}
	return strings.Join(lines, "\n")
}

func (transcript *Transcript) beginReview() {
	if transcript.reviewing || len(transcript.entries) == 0 {
		return
	}
	transcript.reviewing = true
	transcript.refresh(false)
	transcript.viewport.GotoBottom()
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
	return formatElapsedCompact(duration)
}

// FormatElapsedCompact formats a local display duration like Codex's runtime
// status: seconds, minutes plus two-digit seconds, or hours plus both fields.
func FormatElapsedCompact(duration time.Duration) string {
	return formatElapsedCompact(duration)
}

func formatElapsedCompact(duration time.Duration) string {
	seconds := int64(duration / time.Second)
	if seconds < 0 {
		seconds = 0
	}
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %02dm %02ds", seconds/3600, seconds%3600/60, seconds%60)
}

func renderWorkedFor(duration time.Duration, width int, style lipgloss.Style) string {
	width = max(1, width)
	label := "─ Worked for " + formatWorkedFor(duration) + " ─"
	if labelWidth := lipgloss.Width(label); labelWidth < width {
		label += strings.Repeat("─", width-labelWidth)
	} else if labelWidth > width {
		label = string([]rune(label)[:width])
	}
	return style.Render(label)
}

func (transcript Transcript) renderEvidenceReferences(entryIndex int, references []EvidenceReference, includeSelection bool) string {
	if len(references) == 0 || !includeSelection || !transcript.selecting || entryIndex != transcript.selectedEntry {
		return ""
	}
	indexes := transcript.selectedEvidenceIndexes()
	for position, reference := range references {
		if reference.Index == transcript.selected {
			evidencePosition := 0
			for index, value := range indexes {
				if value == reference.Index {
					evidencePosition = index
					break
				}
			}
			claims := claimSequences(references)
			if transcript.selectedClaim > 0 && len(claims) > 0 {
				claimPosition := 0
				for index, sequence := range claims {
					if sequence == transcript.selectedClaim {
						claimPosition = index
						break
					}
				}
				return transcript.styles.Selected.Render(fmt.Sprintf(
					"Claim index · claim %d/%d (#%d, %s) · Evidence %d/%d · %s\n←/→ claim · ↑/↓ Evidence · Enter detail",
					claimPosition+1, len(claims), transcript.selectedClaim,
					claimKindForSequence(references, transcript.selectedClaim),
					evidencePosition+1, len(indexes), observationReferenceStatus(reference.State),
				))
			}
			return transcript.styles.Selected.Render(fmt.Sprintf(
				"› Observation %d/%d · %s · ↑/↓ select · Enter detail",
				position+1, len(references), observationReferenceStatus(reference.State),
			))
		}
	}
	return ""
}

func (transcript Transcript) renderAnswerMetadata(provenance AnswerProvenance, terminalSummary string) string {
	metadata := make([]string, 0, 2)
	if provenance.Visible {
		metadata = append(metadata, transcript.provenanceText(provenance))
	}
	if terminalSummary != "" {
		metadata = append(metadata, terminalSummary)
	}
	if len(metadata) == 0 {
		return ""
	}
	return transcript.styles.NoticeText.Render(middleElideColumns(strings.Join(metadata, " · "), transcript.width))
}

func (transcript Transcript) provenanceText(provenance AnswerProvenance) string {
	parts := []string{
		fmt.Sprintf("Evidence %d", provenance.EvidenceCount),
		provenance.Coverage,
		provenance.SourceCoverage,
		provenance.Generation,
	}
	if provenance.ObservedRange != "" {
		parts = append(parts, provenance.ObservedRange)
	}
	if provenance.Reasoning != "" {
		parts = append(parts, provenance.Reasoning)
	}
	return "Provenance · " + strings.Join(parts, " · ")
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

func (transcript Transcript) selectedEvidenceIndexes() []int {
	if transcript.selectedEntry < 0 || transcript.selectedEntry >= len(transcript.entries) {
		return nil
	}
	references := transcript.entries[transcript.selectedEntry].EvidenceReferences
	if transcript.selectedClaim > 0 {
		return evidenceIndexesForClaim(references, transcript.selectedClaim)
	}
	result := make([]int, 0, len(references))
	for _, reference := range references {
		result = append(result, reference.Index)
	}
	return result
}

func claimSequences(references []EvidenceReference) []int {
	seen := make(map[int]struct{}, len(references))
	result := make([]int, 0, len(references))
	for _, reference := range references {
		for _, sequence := range reference.Claims {
			if _, exists := seen[sequence]; exists {
				continue
			}
			seen[sequence] = struct{}{}
			result = append(result, sequence)
		}
	}
	slices.Sort(result)
	return result
}

func evidenceIndexesForClaim(references []EvidenceReference, sequence int) []int {
	result := make([]int, 0, len(references))
	for _, reference := range references {
		for _, claim := range reference.Claims {
			if claim == sequence {
				result = append(result, reference.Index)
				break
			}
		}
	}
	return result
}

func claimKindForSequence(references []EvidenceReference, sequence int) string {
	for _, reference := range references {
		for index, claim := range reference.Claims {
			if claim == sequence && index < len(reference.ClaimKinds) && reference.ClaimKinds[index] != "" {
				return strings.ReplaceAll(reference.ClaimKinds[index], "_", " ")
			}
		}
	}
	return "declared claim"
}
