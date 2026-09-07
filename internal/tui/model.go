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
	Verified   bool
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
	PolicyGeneration    domain.PolicyGeneration
	LastSequence        int64
	LastActionSequence  int64
	StartedAt           time.Time
	Active              bool
	Terminal            bool
	PersistenceDegraded bool
	StreamedText        string
	Status              string
	TerminalReason      domain.RunTerminalReason
	TerminalActions     []application.UINextAction
	ModelEgress         *application.UIModelEgressPreflight
}

// actionPresentation binds ordered automatic/Reviewer-only lifecycle events
// that have no human approval dialog. It is display state, never authority.
type actionPresentation struct {
	RequestID      domain.ApprovalID
	Digest         domain.ApprovalDigest
	Sequence       int64
	ExecutionIndex int64
}

// pendingConversationInput correlates one short Application acceptance or
// edit request. It is delivery state only and never owns queue authority.
type pendingConversationInput struct {
	RequestID        uint64
	Kind             application.UICommandKind
	RunID            domain.AgentRunID
	ScopeGeneration  int64
	PolicyGeneration domain.PolicyGeneration
	Draft            string
}

type queueClearConfirmation struct {
	Revision         int64
	ScopeGeneration  int64
	PolicyGeneration domain.PolicyGeneration
	Editable         int
}

// Config supplies pure initial UI state; it contains no infrastructure client.
type Config struct {
	Width                   int
	Height                  int
	Theme                   ThemeMode
	DarkBackground          bool
	NoColor                 bool
	ReducedMotion           bool
	StartIntent             application.UIStartIntent
	Scope                   ScopeView
	Resource                ResourceView
	ModelEndpoint           string
	ModelName               string
	ModelConfigured         bool
	ModelConfiguredSet      bool
	ScopePreferenceDegraded bool
	PrivacyMode             domain.PrivacyMode
	Permission              application.UIPermissionStatus
	TerminalStatusTitles    bool
	TerminalClipboard       bool
	TerminalCapabilities    *TerminalCapabilityProfile
	Now                     func() time.Time
}

type resumeOrigin uint8

const (
	resumeOriginNone resumeOrigin = iota
	resumeOriginTopLevel
	resumeOriginInTUI
)

// Model is the root Bubble Tea state and owns exactly one editable composer.
type Model struct {
	width           int
	height          int
	theme           ThemeMode
	focus           Focus
	scope           ScopeView
	resource        ResourceView
	session         SessionView
	startup         StartupView
	run             RunView
	workingAt       time.Time
	workingFrame    uint64
	reducedMotion   bool
	modelName       string
	modelEndpoint   string
	modelConfigured bool
	privacyMode     domain.PrivacyMode
	now             func() time.Time

	composer         components.Composer
	transcript       components.Transcript
	slashMenu        components.SlashMenu
	contextPicker    components.ContextPicker
	namespacePicker  components.NamespacePicker
	resourcePicker   components.ResourcePicker
	sessionPicker    components.SessionPicker
	permissionPicker components.PermissionPicker
	dialog           components.ErrorDialog
	evidenceDialog   components.EvidenceDetailDialog
	approvalDialog   components.ApprovalDialog
	footer           components.Footer

	activePicker             application.UICompletionKind
	pendingCompletion        application.UICompletionQuery
	pendingResume            application.UIResumeRequest
	pendingResumed           *application.UIResumedSession
	resumeOrigin             resumeOrigin
	resumeScopeSelection     bool
	scopeSelectionRequired   bool
	nextRequestID            uint64
	initialQuery             application.UICompletionQuery
	initialResume            application.UIResumeRequest
	pendingScopeID           uint64
	pendingResourceID        uint64
	pendingResource          ResourceView
	pendingSubmitID          uint64
	pendingSubmitDraft       string
	pendingSubmitSessionID   domain.SessionID
	pendingSubmitScope       int64
	pendingSubmitPolicy      domain.PolicyGeneration
	questionRecoveryDraft    string
	questionRecoveryKind     application.UICompletionKind
	pendingConversation      *pendingConversationInput
	conversationRevision     int64
	conversationStatus       application.ConversationInputStatus
	conversationPreview      []application.ConversationInputProjection
	committedConversation    map[domain.MessageID]struct{}
	queueClearConfirmation   *queueClearConfirmation
	pendingCompactionID      uint64
	pendingPlanID            uint64
	pendingDoctorID          uint64
	planArmed                bool
	contextPressure          application.ContextPressureState
	searchMode               bool
	searchReturnDraft        string
	historySearchMode        bool
	historySearchReturnDraft string
	historySearchQuery       string
	historySearchEntries     []string
	historySearchMatches     []int
	historySearchIndex       int
	terminalStatusTitles     bool
	terminalClipboard        bool
	terminalCapabilities     TerminalCapabilityProfile
	pendingPrivacyID         uint64
	pendingDeleteID          uint64
	pendingExportID          uint64
	pendingApprovalID        uint64
	pendingPermissionID      uint64
	pendingModelSetupID      uint64
	modelSetup               *modelSetupState
	privacyReview            *application.PrivacyReview
	lifecycleReview          *application.SessionLifecycleReview
	sessionDelete            *sessionDeleteState
	localDeletion            *localDeletionState
	sessionExport            *sessionExportState
	pendingApproval          *application.UIApprovalRequest
	permission               application.UIPermissionStatus
	permissionReviewer       application.UIModelRoleStatus
	permissionConfirmation   *domain.PermissionProfile
	reviewerEvent            *application.UIReviewerEvent
	actionPresentation       *actionPresentation
	evidenceReferences       []application.UIEvidenceReference
	pendingEvidence          application.UIEvidenceDetailQuery
	evidenceGeneration       int64
	approvalState            domain.ApprovalState
	privacyPending           bool
	quitAfterCancel          bool
	quitAfterLocalDeletion   bool
	exitAfterSessionDeletion bool
	terminalFocused          bool
	terminalHistoryRows      int

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
	modelConfigured := true
	if config.ModelConfiguredSet {
		modelConfigured = config.ModelConfigured
	}
	permission := config.Permission
	if !permission.Configured || !permission.Profile.Valid() || !permission.PolicyGeneration.Valid() {
		permission = application.UIPermissionStatus{
			Configured: true, Profile: domain.PermissionProfileAsk, PolicyGeneration: 1, Healthy: true,
		}
	}
	terminalCapabilities := defaultTerminalCapabilityProfile(theme, config.TerminalClipboard, config.TerminalStatusTitles)
	if config.TerminalCapabilities != nil && config.TerminalCapabilities.valid() {
		terminalCapabilities = *config.TerminalCapabilities
	}
	model := Model{
		width: width, height: height, theme: theme, focus: FocusComposer,
		scope: sanitizedScope(config.Scope), resource: sanitizedResource(config.Resource),
		modelName:       sanitizeExternalText(config.ModelName, 256),
		modelEndpoint:   sanitizeExternalText(config.ModelEndpoint, application.MaxModelSetupEndpointBytes),
		modelConfigured: modelConfigured,
		privacyMode:     privacy, permission: permission, now: now,
		reducedMotion:        config.ReducedMotion,
		terminalStatusTitles: terminalCapabilities.Title == TerminalCapabilityAvailable,
		terminalClipboard:    terminalCapabilities.clipboardAvailable(),
		terminalCapabilities: terminalCapabilities,
		composer:             components.NewComposer(styles.composer, application.MaxQuestionBytes),
		transcript:           components.NewTranscript(styles.transcript, styles.toolSteps),
		slashMenu:            components.NewSlashMenu(styles.slashMenu),
		contextPicker:        components.NewContextPicker(styles.picker),
		namespacePicker:      components.NewNamespacePicker(styles.picker),
		resourcePicker:       components.NewResourcePicker(styles.picker),
		sessionPicker:        components.NewSessionPicker(styles.picker),
		permissionPicker:     components.NewPermissionPicker(styles.picker),
		dialog:               components.NewErrorDialog(styles.dialog),
		evidenceDialog:       components.NewEvidenceDetailDialog(styles.evidence),
		approvalDialog:       components.NewApprovalDialog(styles.approval),
		footer:               components.NewFooter(styles.footer),
		styles:               styles, keymap: DefaultKeyMap(), terminalFocused: true,
	}
	model.configureStartup(config.StartIntent)
	model.scopeSelectionRequired = model.startup.Intent.Kind == application.UIStartNew &&
		model.startup.Ready && !model.scope.Verified
	if config.ScopePreferenceDegraded {
		model.transcript.AppendNotice("The previous Kubernetes Context preference could not be read. Kupilot used current kubeconfig state; a successful Context activation is stored when possible.")
	}
	if !model.modelConfigured && model.startup.Ready {
		model.beginMissingModelSetup()
	} else if model.scopeSelectionRequired {
		query := model.beginRequiredScopeSelection()
		model.initialQuery = model.pendingCompletion
		_ = query
	}
	model.reflow()
	return model
}

func sanitizedScope(scope ScopeView) ScopeView {
	scope.Context = sanitizeExternalText(scope.Context, 253)
	scope.Namespace = sanitizeExternalText(scope.Namespace, 63)
	if scope.Generation < 0 {
		scope.Generation = 0
	}
	if scope.Verified && (scope.Generation < 1 || !scope.ReadOnly || scope.Context == "" || scope.Namespace == "") {
		scope.Verified = false
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

func resourceReference(view ResourceView) *domain.ResourceRef {
	if view == (ResourceView{}) {
		return nil
	}
	reference := domain.ResourceRef{
		APIVersion: view.APIVersion, Kind: view.Kind,
		Namespace: view.Namespace, Name: view.Name,
	}
	if domain.ValidateLiveResourceRef(reference) != nil {
		return nil
	}
	return &reference
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

// Init emits the typed Application request selected during construction and,
// for an RGB color mode, one terminal background query. Neither performs
// business I/O.
func (model Model) Init() tea.Cmd {
	commands := make([]tea.Cmd, 0, 2)
	if model.initialQuery.RequestID != 0 {
		commands = append(commands, applicationQuery(model.initialQuery))
	}
	if model.initialResume.RequestID != 0 {
		commands = append(commands, applicationResume(model.initialResume))
	}
	if model.theme == ThemeAuto || model.theme == ThemeDark || model.theme == ThemeLight {
		commands = append(commands, tea.RequestBackgroundColor)
	}
	return tea.Batch(commands...)
}

func (model *Model) applyStyleSet(styles styleSet) {
	model.styles = styles
	model.composer.SetStyles(styles.composer)
	model.transcript.SetStyles(styles.transcript, styles.toolSteps)
	model.slashMenu.SetStyles(styles.slashMenu)
	model.contextPicker.SetStyles(styles.picker)
	model.namespacePicker.SetStyles(styles.picker)
	model.resourcePicker.SetStyles(styles.picker)
	model.sessionPicker.SetStyles(styles.picker)
	model.permissionPicker.SetStyles(styles.picker)
	model.dialog.SetStyles(styles.dialog)
	model.evidenceDialog.SetStyles(styles.evidence)
	model.approvalDialog.SetStyles(styles.approval)
	model.footer.SetStyles(styles.footer)
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
	contentWidth := model.contentWidth()
	composerRows := MaxComposerRows
	if model.height < 20 {
		composerRows = max(MinComposerRows, model.height/3)
	}
	model.composer.SetMaxRows(composerRows)
	model.composer.SetWidth(contentWidth)
	model.slashMenu.SetWidth(contentWidth)
	model.contextPicker.SetWidth(contentWidth)
	model.namespacePicker.SetWidth(contentWidth)
	model.resourcePicker.SetWidth(contentWidth)
	model.sessionPicker.SetWidth(contentWidth)
	model.permissionPicker.SetWidth(contentWidth)
	footerHeight := 1 + strings.Count(model.footerView(), "\n")
	workingHeight := 0
	if model.run.Active && !model.run.Terminal {
		workingHeight = 1
	}
	inputLabelHeight := 0
	if label := model.inputLabelView(); label != "" {
		inputLabelHeight = 1 + strings.Count(label, "\n")
	}
	gap := model.layoutGap()
	reservedWithoutSuggestions := model.composer.FrameHeight() + footerHeight + inputLabelHeight + workingHeight + gap
	availableSuggestions := max(1, model.height-reservedWithoutSuggestions-1)
	visible := min(MaxPickerCandidates, availableSuggestions)
	model.slashMenu.SetMaxVisible(visible)
	model.contextPicker.SetMaxVisible(visible)
	model.namespacePicker.SetMaxVisible(visible)
	model.resourcePicker.SetMaxVisible(visible)
	model.sessionPicker.SetMaxVisible(visible)
	topSections := 0
	if model.transcript.Visible() {
		topSections++
	}
	if workingHeight > 0 {
		topSections++
	}
	topGaps := max(0, topSections-1) * gap
	beforeComposer := 0
	if topSections > 0 {
		beforeComposer = gap
	}
	transcriptHeight := model.height - model.composer.FrameHeight() - model.suggestionsHeight() - footerHeight -
		inputLabelHeight - workingHeight - gap - topGaps - beforeComposer
	model.transcript.SetSize(contentWidth, max(1, transcriptHeight))
}

func (model Model) contentWidth() int {
	// Keep the last terminal column unused. This matches the Codex composer
	// layout and prevents terminal autowrap both in the managed frame and in the
	// completed history inserted into terminal scrollback.
	return max(1, model.width-1)
}

// TerminalTranscript returns only completed, terminal-safe conversation. It
// deliberately excludes the composer, footer, live Working row, dialogs, and
// any provisional Agent stream.
func (model *Model) TerminalTranscript() string {
	return model.transcript.TerminalTranscript()
}

// PendingTerminalTranscript returns only completed history that the active
// renderer did not already insert into terminal-owned scrollback.
func (model *Model) PendingTerminalTranscript() string {
	return model.transcript.PendingTerminalTranscript()
}

// TerminalFrameHeight reports the renderer-owned live rows that must be
// cleared after Bubble Tea restores terminal modes. Completed history lives
// above this frame and is deliberately excluded.
func (model Model) TerminalFrameHeight() int {
	content := model.View().Content
	if content == "" {
		return 0
	}
	return min(max(1, model.height), 1+strings.Count(content, "\n"))
}

func (model Model) layoutGap() int {
	if model.height < 10 {
		return 0
	}
	return 1
}
