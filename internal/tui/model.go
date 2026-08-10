package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const (
	MinComposerRows = components.MinComposerRows
	MaxComposerRows = components.MaxComposerRows
)

// Focus identifies the root keyboard-priority owner. Menus keep composer focus.
type Focus uint8

const (
	FocusComposer Focus = iota + 1
	FocusTranscript
	FocusModal
)

// ScopeView is bounded display state and carries no live client or authority.
type ScopeView struct {
	Context    string
	Namespace  string
	Generation int64
	ReadOnly   bool
}

// RunView is the accepted ordered projection for the active or last AgentRun.
type RunView struct {
	RunID           domain.AgentRunID
	ScopeGeneration int64
	LastSequence    int64
	Active          bool
	Terminal        bool
	StreamedText    string
}

// Config supplies pure initial UI state; it contains no I/O dependency.
type Config struct {
	Width          int
	Height         int
	Theme          ThemeMode
	DarkBackground bool
	Scope          ScopeView
}

// Model is the root Bubble Tea state and owns exactly one editable composer.
type Model struct {
	width      int
	height     int
	focus      Focus
	scope      ScopeView
	run        RunView
	composer   components.Composer
	transcript components.Transcript
	slashMenu  components.SlashMenu
	dialog     components.ErrorDialog
	styles     styleSet
	keymap     KeyMap
}

// NewModel constructs a pure TUI core with no Application or infrastructure I/O.
func NewModel(config Config) Model {
	width := config.Width
	if width <= 0 {
		width = 80
	}
	height := config.Height
	if height <= 0 {
		height = 24
	}
	styles := newStyleSet(config.Theme, config.DarkBackground)
	model := Model{
		width:      width,
		height:     height,
		focus:      FocusComposer,
		scope:      sanitizedScope(config.Scope),
		composer:   components.NewComposer(styles.composer, application.MaxQuestionBytes),
		transcript: components.NewTranscript(styles.transcript, styles.toolSteps),
		slashMenu:  components.NewSlashMenu(styles.slashMenu),
		dialog:     components.NewErrorDialog(styles.dialog),
		styles:     styles,
		keymap:     DefaultKeyMap(),
	}
	model.reflow()
	return model
}

func sanitizedScope(scope ScopeView) ScopeView {
	scope.Context = sanitizeExternalText(scope.Context, 256)
	scope.Namespace = sanitizeExternalText(scope.Namespace, 256)
	if scope.Generation < 0 {
		scope.Generation = 0
	}
	return scope
}

// Init performs no I/O; the composer is focused during construction.
func (model Model) Init() tea.Cmd { return nil }

// EditorCount is the structural one-editor invariant.
func (model Model) EditorCount() int { return 1 }

// FocusedEditorCount reports zero under a modal and one otherwise.
func (model Model) FocusedEditorCount() int {
	if model.composer.Focused() {
		return 1
	}
	return 0
}

func (model *Model) reflow() {
	model.composer.SetWidth(model.width)
	availableSuggestions := max(1, model.height-model.composer.FrameHeight()-2)
	model.slashMenu.SetMaxVisible(min(MaxSlashCandidates, availableSuggestions))
	transcriptHeight := model.height - model.composer.FrameHeight() - model.slashMenu.Height() - 1
	model.transcript.SetSize(model.width, max(1, transcriptHeight))
}
