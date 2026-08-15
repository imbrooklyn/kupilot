package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const (
	MinComposerRows     = components.MinComposerRows
	MaxComposerRows     = components.MaxComposerRows
	MaxPickerCandidates = components.MaxPickerCandidates
)

// Focus identifies the root keyboard-priority owner. Pickers keep composer focus.
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
	Switching  bool
}

// ResourceView is one safe current ResourceRef projection.
type ResourceView struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
}

// SessionView distinguishes a new Session from safely resumed history.
type SessionView struct {
	ID      domain.SessionID
	Title   string
	Resumed bool
}

// StartupView exposes deterministic startup state without owning startup I/O.
type StartupView struct {
	Intent application.UIStartIntent
	Ready  bool
	Failed bool
}

// RunView is the accepted ordered projection for the active or last AgentRun.
type RunView struct {
	RunID               domain.AgentRunID
	ScopeGeneration     int64
	LastSequence        int64
	Active              bool
	Terminal            bool
	PersistenceDegraded bool
	StreamedText        string
	Status              string
}

// Config supplies pure initial UI state; it contains no infrastructure client.
type Config struct {
	Width          int
	Height         int
	Theme          ThemeMode
	DarkBackground bool
	NoColor        bool
	StartIntent    application.UIStartIntent
	Scope          ScopeView
	Resource       ResourceView
	ModelName      string
	PrivacyMode    domain.PrivacyMode
	Now            func() time.Time
}

type resumeOrigin uint8

const (
	resumeOriginNone resumeOrigin = iota
	resumeOriginTopLevel
	resumeOriginInTUI
)

// Model is the root Bubble Tea state and owns exactly one editable composer.
type Model struct {
	width       int
	height      int
	focus       Focus
	scope       ScopeView
	resource    ResourceView
	session     SessionView
	startup     StartupView
	run         RunView
	modelName   string
	privacyMode domain.PrivacyMode
	now         func() time.Time

	composer        components.Composer
	transcript      components.Transcript
	slashMenu       components.SlashMenu
	contextPicker   components.ContextPicker
	namespacePicker components.NamespacePicker
	resourcePicker  components.ResourcePicker
	sessionPicker   components.SessionPicker
	dialog          components.ErrorDialog
	evidenceDialog  components.EvidenceDetailDialog
	approvalDialog  components.ApprovalDialog
	scopeConflict   components.ScopeConflictDialog
	footer          components.Footer

	activePicker           application.UICompletionKind
	pendingCompletion      application.UICompletionQuery
	pendingResume          application.UIResumeRequest
	pendingResumed         *application.UIResumedSession
	resumeOrigin           resumeOrigin
	nextRequestID          uint64
	initialQuery           application.UICompletionQuery
	initialResume          application.UIResumeRequest
	pendingScopeID         uint64
	pendingResourceID      uint64
	pendingResource        ResourceView
	pendingSubmitID        uint64
	pendingPrivacyID       uint64
	pendingDeleteID        uint64
	pendingExportID        uint64
	pendingApprovalID      uint64
	privacyReview          *application.PrivacyReview
	lifecycleReview        *application.SessionLifecycleReview
	sessionDelete          *sessionDeleteState
	localDeletion          *localDeletionState
	sessionExport          *sessionExportState
	pendingApproval        *application.UIApprovalRequest
	evidenceReferences     []application.UIEvidenceReference
	pendingEvidence        application.UIEvidenceDetailQuery
	evidenceGeneration     int64
	approvalState          domain.ApprovalState
	privacyPending         bool
	quitAfterCancel        bool
	quitAfterLocalDeletion bool
	terminalFocused        bool

	styles styleSet
	keymap KeyMap
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
	theme := config.Theme
	if config.NoColor {
		theme = ThemeNoColor
	}
	styles := newStyleSet(theme, config.DarkBackground)
	privacy := config.PrivacyMode
	if privacy != domain.PrivacyModeStandard && privacy != domain.PrivacyModeMinimal {
		privacy = domain.PrivacyModeStandard
	}
	now := config.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }
	}
	if value := now(); value.IsZero() || value.UnixMilli() < 0 {
		now = func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }
	}
	model := Model{
		width: width, height: height, focus: FocusComposer,
		scope: sanitizedScope(config.Scope), resource: sanitizedResource(config.Resource),
		modelName: sanitizeExternalText(config.ModelName, 256), privacyMode: privacy, now: now,
		composer:        components.NewComposer(styles.composer, application.MaxQuestionBytes),
		transcript:      components.NewTranscript(styles.transcript, styles.toolSteps),
		slashMenu:       components.NewSlashMenu(styles.slashMenu),
		contextPicker:   components.NewContextPicker(styles.picker),
		namespacePicker: components.NewNamespacePicker(styles.picker),
		resourcePicker:  components.NewResourcePicker(styles.picker),
		sessionPicker:   components.NewSessionPicker(styles.picker),
		dialog:          components.NewErrorDialog(styles.dialog),
		evidenceDialog:  components.NewEvidenceDetailDialog(styles.evidence),
		approvalDialog:  components.NewApprovalDialog(styles.approval),
		scopeConflict:   components.NewScopeConflictDialog(styles.scopeConflict),
		footer:          components.NewFooter(styles.footer),
		styles:          styles, keymap: DefaultKeyMap(), terminalFocused: true,
	}
	model.configureStartup(config.StartIntent)
	model.reflow()
	return model
}

func sanitizedScope(scope ScopeView) ScopeView {
	scope.Context = sanitizeExternalText(scope.Context, 253)
	scope.Namespace = sanitizeExternalText(scope.Namespace, 63)
	if scope.Generation < 0 {
		scope.Generation = 0
	}
	return scope
}

func sanitizedResource(resource ResourceView) ResourceView {
	resource.APIVersion = sanitizeExternalText(resource.APIVersion, 253)
	resource.Kind = sanitizeExternalText(resource.Kind, 63)
	resource.Namespace = sanitizeExternalText(resource.Namespace, 63)
	resource.Name = sanitizeExternalText(resource.Name, 253)
	if resource == (ResourceView{}) {
		return resource
	}
	reference := domain.ResourceRef{
		APIVersion: resource.APIVersion, Kind: resource.Kind,
		Namespace: resource.Namespace, Name: resource.Name,
	}
	if reference.Validate() != nil {
		return ResourceView{}
	}
	return resource
}

func (model *Model) configureStartup(intent application.UIStartIntent) {
	if intent.Kind == "" {
		intent = application.UIStartIntent{Kind: application.UIStartNew}
	}
	model.startup.Intent = intent
	if intent.Validate() != nil {
		model.startup.Failed = true
		model.showDialog("Startup unavailable", "The Session start intent could not be accepted safely.")
		return
	}
	switch intent.Kind {
	case application.UIStartNew:
		model.startup.Ready = true
	case application.UIStartResumePicker:
		model.resumeOrigin = resumeOriginTopLevel
		model.composer.SetValue("/resume ")
		query := model.openCompletion(application.UICompletionSession, "", resumeOriginTopLevel)
		model.initialQuery = model.pendingCompletion
		_ = query
	case application.UIStartResumeID:
		request := model.beginResume(application.UIResumeExact, intent.SessionID, resumeOriginTopLevel)
		model.initialResume = model.pendingResume
		_ = request
	case application.UIStartResumeLast:
		request := model.beginResume(application.UIResumeLast, "", resumeOriginTopLevel)
		model.initialResume = model.pendingResume
		_ = request
	}
}

// Init emits only the typed Application request selected during construction.
func (model Model) Init() tea.Cmd {
	if model.initialQuery.RequestID != 0 {
		return applicationQuery(model.initialQuery)
	}
	if model.initialResume.RequestID != 0 {
		return applicationResume(model.initialResume)
	}
	return nil
}

// EditorCount is the structural one-editor invariant.
func (model Model) EditorCount() int { return 1 }

// FocusedEditorCount reports zero under a modal or terminal blur and one otherwise.
func (model Model) FocusedEditorCount() int {
	if model.composer.Focused() {
		return 1
	}
	return 0
}

func (model *Model) nextUIRequestID() uint64 {
	model.nextRequestID++
	if model.nextRequestID == 0 {
		model.nextRequestID++
	}
	return model.nextRequestID
}

func (model *Model) reflow() {
	composerRows := MaxComposerRows
	if model.height < 20 {
		composerRows = max(MinComposerRows, model.height/3)
	}
	model.composer.SetMaxRows(composerRows)
	model.composer.SetWidth(model.width)
	model.slashMenu.SetWidth(model.width)
	model.contextPicker.SetWidth(model.width)
	model.namespacePicker.SetWidth(model.width)
	model.resourcePicker.SetWidth(model.width)
	model.sessionPicker.SetWidth(model.width)
	footerHeight := 1 + strings.Count(model.footerView(), "\n")
	availableSuggestions := max(1, model.height-model.composer.FrameHeight()-footerHeight-1)
	visible := min(MaxPickerCandidates, availableSuggestions)
	model.slashMenu.SetMaxVisible(visible)
	model.contextPicker.SetMaxVisible(visible)
	model.namespacePicker.SetMaxVisible(visible)
	model.resourcePicker.SetMaxVisible(visible)
	model.sessionPicker.SetMaxVisible(visible)
	transcriptHeight := model.height - model.composer.FrameHeight() - model.suggestionsHeight() - footerHeight
	model.transcript.SetSize(model.width, max(1, transcriptHeight))
}
