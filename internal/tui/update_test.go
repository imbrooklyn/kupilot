package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const (
	testRunID        domain.AgentRunID       = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a10"
	testInvocationID domain.ToolInvocationID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a11"
)

func TestStatusTextShowsDetailedBudgetWithoutFixedFooterCounters(t *testing.T) {
	status := application.UIStatusResult{
		Session: &application.UISessionState{
			ID: testSessionID, Title: "Operations", PrivacyMode: domain.PrivacyModeStandard,
		},
		Context: "test-context", Namespace: "test-namespace", NamespaceAccess: domain.NamespaceAccessAll, ScopeGeneration: 7, ReadOnly: true,
		RunID: testRunID, RunActive: true, CapabilityCatalogVersion: agent.ToolCatalogVersion,
		ConversationInput: application.ConversationInputStatus{
			Revision: 4, Pending: 1, Committing: 1, Committed: 1, Queued: 1,
			Items: 4, Bytes: 4096,
			MaximumItems:     application.MaxConversationInputItems,
			MaximumBytes:     application.MaxConversationInputAggregateBytes,
			MaximumItemBytes: application.MaxConversationInputItemBytes,
		},
		ResourcePolicyVersion: domain.ResourcePolicyVersion, ResourceTypeCount: len(domain.BuiltInResourcePolicies()),
		ObservabilityPolicyVersion: domain.ObservabilityPolicyVersion, PrometheusEnabled: true,
		RemoteDiagnosticsPolicyVersion: domain.RemoteDiagnosticsPolicyVersion,
		LocalExecutionPolicyVersion:    domain.LocalExecutionPolicyVersion,
		ModelContext: application.UIModelContextStatus{
			Mode: domain.PrivacyModeStandard, EligibleMessages: 4, EligibleBytes: 4096,
			Compressed: true, CompressedAtUnixMillis: 1_700_000_000_000,
			CoveredThroughMessageID: "0198a46e-7d2a-7d34-9b6f-2df5f45a2a55",
			RecentTailMessages:      2, SummaryCallsUsed: 1, SummaryCallsMaximum: 2, StorageHealthy: true,
		},
		AgentModel: application.UIModelRoleStatus{
			Role: domain.ModelRoleAgent, Profile: "agent", OriginHash: strings.Repeat("a", 64),
			Configured: true, Available: true, Consented: true,
		},
		ReviewerModel: application.UIModelRoleStatus{
			Role: domain.ModelRoleApprovalReviewer, Profile: "approval_reviewer", OriginHash: strings.Repeat("b", 64),
			Configured: true, Available: true, Consented: true,
		},
		Permission: application.UIPermissionStatus{
			Configured: true, Profile: domain.PermissionProfileAsk, PolicyGeneration: 7, Healthy: true,
			SessionRuleCount: 1,
			SessionRules: []application.UISessionPermissionRuleStatus{{
				ID: "00000000-0000-7000-8000-000000009002", Operation: domain.ActionOperationRestartDeployment,
				Scope:           domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
				NamespaceAccess: domain.NamespaceAccessCurrent, PolicyGeneration: 7,
				Target:           "Deployment test-namespace/sample-deployment* · API apps/v1",
				ParameterSummary: "kind=none digest=" + strings.Repeat("c", 64),
				Effect:           domain.CapabilityEffectClusterMutation, Risk: domain.RiskReview,
				DataCategories:  domain.ActionDataResourceMetadata,
				AllowedSinks:    domain.ActionSinkTerminal | domain.ActionSinkKubernetesAPI,
				NetworkEffects:  domain.ActionNetworkKubernetesAPI,
				Limits:          domain.ActionLimits{Timeout: 2 * time.Minute, MaximumItems: 1},
				CreatedAtMillis: 1_700_000_000_000, ExpiresAtMillis: 1_700_003_600_000,
			}},
		},
		Action: &application.UIActionStatus{
			RequestID: "00000000-0000-7000-8000-000000009001",
			Operation: domain.ActionOperationRestartDeployment, Risk: domain.RiskReview,
			Route: domain.ReviewDispositionHuman, State: domain.ApprovalStatePending,
			ScopeGeneration: 7, PolicyGeneration: 7, ExpiresAtMillis: 1_700_000_060_000,
		},
		Budget: application.UIBudgetStatus{
			ModelEvidenceBasis: application.ModelBudgetEvidenceBasis,
			Profile:            agent.BudgetProfileBalanced, RunMilliseconds: 600_000,
			ElapsedMilliseconds: 90_000, RemainingMilliseconds: 510_000,
			StepsUsed: 3, StepsMaximum: 32, ToolCallsUsed: 5, ToolCallsMaximum: 48,
			ModelCallsUsed: 3, ModelCallsMaximum: 16,
			ModelCostUnitsUsed: 3, ModelCostUnitsMaximum: 16,
			SummaryCallsUsed: 1, SummaryCallsMaximum: 2,
			SummaryCostUnitsUsed: 1, SummaryCostUnitsMaximum: 2,
			ReviewerCallsUsed: 0, ReviewerCallsMaximum: 8,
			ReviewerCostUnitsUsed: 0, ReviewerCostUnitsMaximum: 8,
			ToolResultBytesUsed: 96 * 1024, ToolResultBytesMaximum: 4 * 1024 * 1024,
			LogCallsUsed: 2, LogCallsMaximum: 12,
			LogContainersMaximum: 8, LogBytesMaximum: 256 * 1024,
			EventPagesMaximum: 4, EventPageItemsMaximum: 50, EventPageBytesMaximum: 128 * 1024, EventBytesMaximum: 512 * 1024,
			MetricCallsUsed: 1, MetricCallsMaximum: 12, MetricContainersMaximum: 35, MetricBytesMaximum: 256 * 1024,
			DataSourceCallsUsed: 4, DataSourceCallsMaximum: 16, DataSourcePagesMaximum: 4,
			RemoteExecCallsUsed: 1, RemoteExecCallsMaximum: 4,
			LocalProcessCallsUsed: 1, LocalProcessCallsMaximum: 1,
			DataSourceSeriesMaximum: 25, DataSourceSamplesMaximum: 400, DataSourceLinesMaximum: 400,
			DataSourceBytesMaximum: 512 * 1024, DataSourceWindowMillis: 21_600_000, DataSourceStepMillis: 300_000,
			ResourcePagesMaximum: 4, ResourcePageItemsMaximum: 50, ResourcePageBytesMaximum: 256 * 1024,
			ResourceScannedMaximum: 200, ResourceReturnedMaximum: 50, ResourceBytesMaximum: 1024 * 1024,
			FineGrained: application.NewUIBudgetMeasures(agent.DefaultRunBudgetLimits()),
		},
	}
	got := statusText(status, "diagnostic-model")
	for _, required := range []string{
		"Kupilot status", "Session", "Model       diagnostic-model", "Privacy     standard", "Storage     healthy",
		"Scope", "Context     test-context", "Namespace   test-namespace", "Generation  7",
		"Access      namespace policy all",
		"Actions     typed remediation and exact local policies · permission route and fresh RBAC required",
		"Model context", "covered through 0198a46e-7d2a-7d34-9b6f-2df5f45a2a55",
		"Compacted", "Recent tail 2 messages", "Summary budget 1/2 calls", "Storage     healthy",
		"Permission and action supervision", "Profile     ask", "Generation  7", "Health      healthy",
		"Boundary    safe automatic; review and critical require the local user",
		"Reviewer    not used", "Risk        default supervised profile", "1 current-process, current-Session rules",
		"restart_deployment · review · route human · state pending · scope 7 · policy 7",
		"scope test-context/test-namespace generation 7 access current · policy 7",
		"effect cluster_mutation · risk review · data resource metadata · sinks terminal, Kubernetes API",
		"network Kubernetes API · destination none · timeout 2m0s · items 1",
		"Run", "Catalog     " + agent.ToolCatalogVersion,
		"Resources   " + domain.ResourcePolicyVersion + " · 17 types", "Budget", "Profile     balanced",
		"Observability " + domain.ObservabilityPolicyVersion + " · Prometheus enabled · Loki disabled",
		"Remote diagnostics " + domain.RemoteDiagnosticsPolicyVersion + " · Pod exec policies 0 · container files disabled · diagnostic Pod policies 0",
		"Local execution " + domain.LocalExecutionPolicyVersion + " · direct argv policies 0 · shell policies 0 · OS sandbox not provided",
		"Basis       " + application.ModelBudgetEvidenceBasis,
		"1m 30s elapsed", "8m 30s remaining", "3/32 steps", "5/48 tools", "3/16 model",
		"96.0 KiB/4.0 MiB", "2/12 log calls", "1/1 reserved proposals",
		"4 pages · 50 items/page · 128.0 KiB/page · 512.0 KiB total", "8 containers · 256.0 KiB/read",
		"1/12 reads · 35 containers · 256.0 KiB/read", "4/16 reads · 4 pages · 25 series · 400 samples · 400 lines",
		"4 pages · 50 items/page · 256.0 KiB/page · 200 scanned · 50 returned · 1.0 MiB total",
	} {
		if !strings.Contains(got, required) {
			t.Fatalf("status text missing %q:\n%s", required, got)
		}
	}
	for _, width := range []int{24, 40} {
		model := NewModel(Config{
			Width: width, Height: 80, Theme: ThemeNoColor,
			Scope: ScopeView{Context: "test-context", Namespace: "test-namespace", Generation: 7, ReadOnly: true, Verified: true},
		})
		model.transcript.AppendNotice(got)
		model.reflow()
		for _, line := range strings.Split(model.transcript.View(), "\n") {
			if lineWidth := lipgloss.Width(line); lineWidth > model.contentWidth() {
				t.Fatalf("status line width = %d, limit %d at terminal width %d: %q", lineWidth, model.contentWidth(), width, line)
			}
		}
	}
}

func TestCtrlCClearsDraftBeforeCancellingOrExiting(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/sta"})
	if !model.slashMenu.Open() {
		t.Fatal("Slash suggestions did not open for the draft")
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil || model.composer.Value() != "" || model.slashMenu.Open() {
		t.Fatal("first Ctrl+C did not only clear the editable draft")
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !commandQuits(cmd) {
		t.Fatal("second Ctrl+C with an empty composer did not exit")
	}

	active := newTestModel()
	active, _ = updateModel(t, active, ApplicationEventMsg{Event: runStartedEvent(1)})
	active, _ = updateModel(t, active, tea.PasteMsg{Content: "draft for later"})
	active, cmd = updateModel(t, active, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil || active.composer.Value() != "" || !active.run.Active || active.quitAfterCancel {
		t.Fatal("first Ctrl+C during a run did more than clear the draft")
	}
	active, cmd = updateModel(t, active, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if command := applicationCommandFromCmd(t, cmd); command.Kind != application.UICommandCancelRun || !active.quitAfterCancel {
		t.Fatalf("second Ctrl+C command = %#v", command)
	}
}

func TestExitLeavesTrailingHistoryForTerminalRuntimeFallback(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.transcript.AppendUser("A submitted question awaiting startup.")
	model.reflow()
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !commandQuits(cmd) {
		t.Fatal("exit did not issue one direct quit command")
	}
	view := model.View()
	if view.AltScreen || view.MouseMode != tea.MouseModeNone ||
		!strings.Contains(view.Content, "A submitted question awaiting startup.") || view.Cursor != nil {
		t.Fatalf("exit state did not retain safe terminal history with native input ownership: %#v", view)
	}
	transcript := model.TerminalTranscript()
	if strings.Count(transcript, "A submitted question awaiting startup.") != 1 ||
		strings.Contains(transcript, "Ask a question") || strings.Contains(transcript, "Context test-context") {
		t.Fatalf("post-restore transcript contains missing or live UI state: %q", transcript)
	}
}

func TestUpdateMaintainsOneEditorAcrossStates(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	assertSingleEditor(t, model)

	model, _ = updateModel(t, model, keyText("/"))
	if !model.slashMenu.Open() {
		t.Fatal("slash menu did not open")
	}
	assertSingleEditor(t, model)

	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	model.composer.Reset()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	if !model.run.Active {
		t.Fatal("run did not become active")
	}
	assertSingleEditor(t, model)

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/not-a-command"})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !model.dialog.Open() {
		t.Fatal("unknown command did not open an error dialog")
	}
	if model.EditorCount() != 1 || model.FocusedEditorCount() != 0 {
		t.Fatal("modal added an editor or left the background editor focused")
	}
}

func TestUpdateSubmitsChatThroughDeferredTypedCommand(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "Why is the Pod restarting?"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	intent := commandFromCmd(t, cmd)
	if intent.Kind != application.UICommandSubmitQuestion || intent.Text != "Why is the Pod restarting?" ||
		intent.SessionID != testSessionID || intent.ExpectedScopeGeneration != 7 ||
		intent.ExpectedPolicyGeneration != 1 || intent.Resource != nil {
		t.Fatalf("command = %#v", intent)
	}
	if model.composer.Value() != "" {
		t.Fatalf("draft after submit = %q", model.composer.Value())
	}
	if got := model.transcript.Entries(); len(got) != 0 {
		t.Fatalf("uncommitted submit entered transcript: %#v", got)
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1, intent.Text)})
	if got := model.transcript.Entries(); len(got) != 2 || got[0].Kind != components.EntryUser || got[0].Text != intent.Text {
		t.Fatalf("committed transcript entries = %#v", got)
	}
}

func TestUpdateEnterAndNewlineKeysHaveDistinctBehavior(t *testing.T) {
	t.Parallel()

	newlineKeys := []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{name: "shift-enter", key: tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}},
		{name: "alt-enter", key: tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}},
		{name: "ctrl-j", key: tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}},
	}
	for _, tt := range newlineKeys {
		t.Run(tt.name, func(t *testing.T) {
			model := newTestModel()
			model, _ = updateModel(t, model, keyText("a"))
			model, _ = updateModel(t, model, tt.key)
			if got := model.composer.Value(); got != "a\n" {
				t.Fatalf("draft after %s = %q", tt.name, got)
			}
			model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
			if commandFromCmd(t, cmd).Text != "a\n" || model.composer.Value() != "" {
				t.Fatal("plain Enter did not submit and clear the multiline draft")
			}
		})
	}

	model := newTestModel()
	model, _ = updateModel(t, model, keyText("x"))
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab, Text: "\t"})
	if cmd != nil || model.composer.Value() != "x" {
		t.Fatal("Tab without a completion changed the draft or returned an action")
	}

	limited := newTestModel()
	limited.composer.SetValue(strings.Repeat("x", application.MaxQuestionBytes))
	limited, cmd = updateModel(t, limited, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	if cmd != nil || len(limited.composer.Value()) != application.MaxQuestionBytes || !limited.dialog.Open() {
		t.Fatal("newline alias did not fail closed at the draft byte limit")
	}
}

func TestUpdateComposerHeightPasteHistoryAndInternalScroll(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	if model.composer.Height() != MinComposerRows {
		t.Fatalf("initial height = %d", model.composer.Height())
	}

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "one\ntwo\nthree\nfour\nfive"})
	if model.composer.Height() != 5 {
		t.Fatalf("five-line height = %d", model.composer.Height())
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, cmd)
	model.clearPendingSubmit()
	model.composer.RecordSubmission("one\ntwo\nthree\nfour\nfive")

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "second"})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, cmd)
	model.clearPendingSubmit()
	model.composer.RecordSubmission("second")
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := model.composer.Value(); got != "second" {
		t.Fatalf("plain Up did not recall the newest input: %q", got)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := model.composer.Value(); got != "one\ntwo\nthree\nfour\nfive" {
		t.Fatalf("second plain-arrow history item = %q", got)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := model.composer.Value(); got != "one\ntwo\nthree\nfour\nfive" {
		t.Fatalf("plain Up moved or changed the oldest history item: %q", got)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := model.composer.Value(); got != "second" {
		t.Fatalf("plain Down did not recall the newer input: %q", got)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyDown})
	if got := model.composer.Value(); got != "" {
		t.Fatalf("plain-arrow history did not return to an empty draft: %q", got)
	}

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "draft line one\ndraft line two"})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := model.composer.Value(); got != "draft line one\ndraft line two" {
		t.Fatalf("plain Up replaced an ordinary multiline draft: %q", got)
	}
	model.composer.Reset()
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if got := model.composer.Value(); got != "" {
		t.Fatalf("non-arrow keys recalled input history: %q", got)
	}

	model, _ = updateModel(t, model, tea.PasteMsg{Content: strings.Repeat("line\n", 11) + "last"})
	if model.composer.Height() != MaxComposerRows {
		t.Fatalf("maximum height = %d", model.composer.Height())
	}
	if model.composer.ScrollOffset() == 0 {
		t.Fatal("composer did not scroll internally past eight rows")
	}
}

func TestMouseInputRemainsTerminalOwnedAndCannotRecallInputHistory(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.transcript.SetSize(40, 4)
	model.transcript.StartAgent()
	for index := range 24 {
		model.transcript.AppendNotice(strings.Repeat("x", index+1))
	}
	bottom := model.transcript.ScrollOffset()
	if bottom == 0 {
		t.Fatal("test transcript did not become scrollable")
	}

	view := model.View()
	if view.MouseMode != tea.MouseModeNone || view.OnMouse != nil {
		t.Fatalf("conversation view captured terminal mouse input: mode=%v callback=%v", view.MouseMode, view.OnMouse != nil)
	}
	for _, message := range []tea.MouseMsg{
		tea.MouseClickMsg{Button: tea.MouseLeft},
		tea.MouseReleaseMsg{Button: tea.MouseLeft},
		tea.MouseMotionMsg{Button: tea.MouseLeft},
		tea.MouseWheelMsg{Button: tea.MouseWheelUp},
		tea.MouseWheelMsg{Button: tea.MouseWheelDown},
		tea.MouseWheelMsg{Button: tea.MouseWheelLeft},
		tea.MouseWheelMsg{Button: tea.MouseWheelRight},
	} {
		updated, command := updateModel(t, model, message)
		if command != nil || updated.transcript.ScrollOffset() != bottom || updated.composer.Value() != "" {
			t.Fatalf("terminal-owned mouse event %T changed TUI state", message)
		}
	}

	model.composer.RecordSubmission("first question")
	model.composer.RecordSubmission("second question")
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := model.composer.Value(); got != "second question" {
		t.Fatalf("physical keyboard history was unavailable after terminal-owned mouse input: %q", got)
	}
}

func TestRemovedMouseSlashCannotChangeTerminalInputOwnership(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/mouse"})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if command != nil || !model.dialog.Open() || model.View().MouseMode != tea.MouseModeNone {
		t.Fatalf("removed mouse command changed terminal authority: command=%v dialog=%v mode=%v",
			command != nil, model.dialog.Open(), model.View().MouseMode)
	}
}

func TestUpdateSanitizesPasteBeforeRenderState(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	unsafe := "safe\x1b[31mred\x1b[0m\x1b]52;c;ignored\x07\x00\u202esuffix"
	model, _ = updateModel(t, model, tea.PasteMsg{Content: unsafe})
	got := model.composer.Value()
	if strings.Contains(got, "ignored") || containsUnsafeTerminalText(got) {
		t.Fatalf("unsafe paste reached render state: %q", got)
	}
	if !strings.Contains(got, "safered") || !strings.Contains(got, "suffix") {
		t.Fatalf("safe paste content was lost: %q", got)
	}

	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: '\u202e', Text: "\u202e"})
	if got := model.composer.Value(); got != "saferedsuffix" {
		t.Fatalf("unsafe key text reached render state: %q", got)
	}
}

func TestUpdateEscapedSlashHistoryPreservesChatMeaning(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "//help"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if intent := commandFromCmd(t, cmd); intent.Kind != application.UICommandSubmitQuestion || intent.Text != "/help" {
		t.Fatalf("first escaped intent = %#v", intent)
	}
	model.clearPendingSubmit()
	model.composer.RecordSubmission("//help")
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if model.composer.Value() != "//help" {
		t.Fatalf("escaped history draft = %q", model.composer.Value())
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if intent := commandFromCmd(t, cmd); intent.Kind != application.UICommandSubmitQuestion || intent.Text != "/help" {
		t.Fatalf("replayed escaped intent = %#v", intent)
	}
}

func TestUpdateSlashSelectionDispatchAndDenials(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		draft       string
		wantKind    application.UICommandKind
		wantText    string
		wantCommand bool
		wantDialog  bool
	}{
		{name: "literal slash is chat", draft: "//help", wantKind: application.UICommandSubmitQuestion, wantText: "/help", wantCommand: true},
		{name: "multiline slash is chat", draft: "/help\nexplain", wantKind: application.UICommandSubmitQuestion, wantText: "/help\nexplain", wantCommand: true},
		{name: "unknown slash", draft: "/does-not-exist", wantDialog: true},
		{name: "bang syntax", draft: "!kubectl get pods", wantDialog: true},
		{name: "multiline bang syntax", draft: "!command\nargument", wantDialog: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := newTestModel()
			model, _ = updateModel(t, model, tea.PasteMsg{Content: tt.draft})
			model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
			if tt.wantCommand {
				intent := commandFromCmd(t, cmd)
				if intent.Kind != tt.wantKind || intent.Text != tt.wantText {
					t.Fatalf("command = %#v", intent)
				}
			} else if cmd != nil {
				t.Fatal("denied input returned an action")
			}
			if model.dialog.Open() != tt.wantDialog {
				t.Fatalf("dialog open = %v", model.dialog.Open())
			}
			if tt.wantDialog && model.composer.Value() != tt.draft {
				t.Fatalf("denied draft changed to %q", model.composer.Value())
			}
		})
	}
}

func TestUpdateSlashMenuFiltersNavigatesAndCompletes(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/"})
	if got := model.slashMenu.Candidates(); len(got) != MaxSlashCandidates {
		t.Fatalf("initial candidates = %d", len(got))
	}
	model, _ = updateModel(t, model, keyText("r"))
	model, _ = updateModel(t, model, keyText("e"))
	model, _ = updateModel(t, model, keyText("s"))
	if got := model.slashMenu.Candidates(); len(got) == 0 || got[0].Name != "resource" {
		t.Fatalf("filtered candidates = %#v", got)
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyDown})
	selected := model.slashMenu.Selected()
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if model.slashMenu.Selected() == selected && len(model.slashMenu.Candidates()) > 1 {
		t.Fatal("direction keys did not change selection")
	}
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyTab})
	if cmd == nil {
		t.Fatal("Tab did not request typed Resource completion")
	}
	message, ok := cmd().(ApplicationQueryMsg)
	if !ok || message.Query.Kind != application.UICompletionResource {
		t.Fatalf("Tab command = %#v", cmd())
	}
	if got := model.composer.Value(); got != "/resource " {
		t.Fatalf("completed draft = %q", got)
	}
}

func TestUpdateDisabledSlashHasZeroAction(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/cancel"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || !model.dialog.Open() || model.composer.Value() != "/cancel" {
		t.Fatal("disabled /cancel did not fail closed")
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	intent := commandFromCmd(t, cmd)
	if intent.Kind != application.UICommandCancelRun || intent.RunID != testRunID || intent.ExpectedScopeGeneration != 7 {
		t.Fatalf("cancel command = %#v", intent)
	}
}

func TestSessionAndStatusSlashCommandsDispatchTypedApplicationIntents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		draft    string
		wantKind application.UICommandKind
		wantText string
	}{
		{draft: "/new", wantKind: application.UICommandNewSession},
		{draft: "/rename", wantKind: application.UICommandRenameSession},
		{draft: "/rename Incident review", wantKind: application.UICommandRenameSession, wantText: "Incident review"},
		{draft: "/status", wantKind: application.UICommandShowStatus},
	}
	for _, test := range tests {
		t.Run(test.draft, func(t *testing.T) {
			t.Parallel()
			model := newTestModel()
			model, _ = updateModel(t, model, tea.PasteMsg{Content: test.draft})
			_, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
			command := applicationCommandFromCmd(t, cmd)
			if command.Kind != test.wantKind || command.Text != test.wantText {
				t.Fatalf("command = %#v", command)
			}
		})
	}
}

func TestUpdateActiveRunSteersAndRejectsLateEvents(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1, "initial question")})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "next question"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	steer := applicationCommandFromCmd(t, cmd)
	if steer.Kind != application.UICommandSubmitSteer || steer.Text != "next question" ||
		steer.RunID != testRunID || steer.ExpectedScopeGeneration != 7 || steer.ExpectedPolicyGeneration != 1 ||
		model.composer.Value() != "" || model.dialog.Open() {
		t.Fatalf("active steer = %#v", steer)
	}
	model.pendingConversation = nil
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})

	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: "first",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: "duplicate",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 8, PolicyGeneration: 1, Sequence: 3, Text: "stale",
	}})
	if model.run.StreamedText != "first" || model.run.LastSequence != 2 {
		t.Fatalf("run after rejected events = %#v", model.run)
	}

	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 3,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID,
			Name:         domain.ToolNameGetResource,
			Purpose:      "Inspect the selected Pod.",
			Status:       application.ToolStepRunning,
		},
	}})
	if len(model.transcript.ToolSteps()) != 1 {
		t.Fatal("Tool step was not placed inline")
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 4,
		ToolStep: &application.ToolStep{
			InvocationID:  testInvocationID,
			Name:          domain.ToolNameGetResource,
			Status:        application.ToolStepSucceeded,
			EvidenceCount: 2,
		},
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 5,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID,
			Name:         domain.ToolNameGetResource,
			Status:       application.ToolStepRunning,
		},
	}})
	if got := model.transcript.ToolSteps(); len(got) != 1 || got[0].Status != string(application.ToolStepSucceeded) {
		t.Fatalf("terminal Tool step regressed: %#v", got)
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runTerminalEvent(
		application.UIEventRunCompleted, 6, "Final diagnosis.",
	)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 7, Text: "late",
	}})
	if model.run.Active || !model.run.Terminal || model.run.StreamedText != "Final diagnosis." || model.run.LastSequence != 6 {
		t.Fatalf("terminal run state = %#v", model.run)
	}
	model.composer.SetValue("follow-up after completion")
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	_ = commandFromCmd(t, cmd)
	entries := model.transcript.Entries()
	if len(entries) != 2 || len(entries[1].ToolSteps) != 1 {
		t.Fatalf("historic Tool steps were not retained: %#v", entries)
	}
}

func TestRequestedToolClearsOnlyThePreToolProvisionalAnswer(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2,
		Text: "Discard this pre-Tool draft.",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 3,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID,
			Name:         domain.ToolNameGetResource,
			Purpose:      "Inspect the selected Pod.",
			Status:       application.ToolStepRequested,
		},
	}})
	if model.run.StreamedText != "" || strings.Contains(model.transcript.View(), "Discard this pre-Tool draft.") {
		t.Fatalf("requested Tool retained pre-Tool text: run=%#v view=%q", model.run, model.transcript.View())
	}
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 4,
		Text: "Keep this final answer.",
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runTerminalEvent(
		application.UIEventRunCompleted, 5, "Keep this final answer.",
	)})
	if model.run.StreamedText != "Keep this final answer." ||
		strings.Contains(model.TerminalTranscript(), "Discard this pre-Tool draft.") ||
		!strings.Contains(model.TerminalTranscript(), "Keep this final answer.") {
		t.Fatalf("final stream after Tool = %#v / %q", model.run, model.TerminalTranscript())
	}
}

func TestUpdateBoundsCumulativeStreamText(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2,
		Text: strings.Repeat("x", application.MaxQuestionBytes),
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 3,
		Text: strings.Repeat("y", application.MaxQuestionBytes),
	}})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 4, Text: "overflow",
	}})
	if len(model.run.StreamedText) != application.MaxAnswerMarkdownBytes || model.run.LastSequence != 4 {
		t.Fatalf("bounded stream state = bytes %d, sequence %d", len(model.run.StreamedText), model.run.LastSequence)
	}
}

func TestUpdateComposerSoftWrapLimitAndResize(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 20, Height: 16, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "ctx", Namespace: "ns", Generation: 1, ReadOnly: true, Verified: true},
	})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: strings.Repeat("x", 80)})
	if model.composer.Height() <= MinComposerRows || model.composer.Height() > MaxComposerRows {
		t.Fatalf("soft-wrapped height = %d", model.composer.Height())
	}

	limited := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "ctx", Namespace: "ns", Generation: 1, ReadOnly: true, Verified: true},
	})
	limited, cmd := updateModel(t, limited, tea.PasteMsg{Content: strings.Repeat("x", application.MaxQuestionBytes+1)})
	if cmd != nil || limited.composer.Value() != "" || !limited.dialog.Open() {
		t.Fatal("oversized paste did not fail closed without partial insertion")
	}

	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 40, Height: 12})
	view := model.View()
	if model.width != 40 || model.height != 12 || model.EditorCount() != 1 ||
		view.MouseMode != tea.MouseModeNone {
		t.Fatalf("resize or mouse invariant failed: size=%dx%d editors=%d", model.width, model.height, model.EditorCount())
	}
}

func newTestModel() Model {
	model := NewModel(Config{
		Width:  80,
		Height: 24,
		Theme:  ThemeNoColor,
		Scope: ScopeView{
			Context:    "test-context",
			Namespace:  "test-namespace",
			Generation: 7,
			ReadOnly:   true,
			Verified:   true,
		},
	})
	model.session = SessionView{ID: testSessionID, Title: "Test Session"}
	return model
}

func runStartedEvent(sequence int64, text ...string) application.UIEvent {
	event := application.UIEvent{
		Kind:             application.UIEventRunStarted,
		RunID:            testRunID,
		ScopeGeneration:  7,
		PolicyGeneration: 1,
		Sequence:         sequence,
	}
	if len(text) > 0 {
		event.Text = text[0]
	}
	return event
}

func runTerminalEvent(kind application.UIEventKind, sequence int64, text string, references ...application.UIEvidenceReference) application.UIEvent {
	return terminalEventFor(testRunID, 7, 1, kind, sequence, text, references...)
}

func terminalEventFor(runID domain.AgentRunID, scopeGeneration int64, policyGeneration domain.PolicyGeneration, kind application.UIEventKind, sequence int64, text string, references ...application.UIEvidenceReference) application.UIEvent {
	reason := domain.RunTerminalFailed
	switch kind {
	case application.UIEventRunCompleted:
		reason = domain.RunTerminalCompleted
	case application.UIEventRunCancelled:
		reason = domain.RunTerminalCancelled
	}
	outcome, err := application.ProjectTerminalOutcome(reason)
	if err != nil {
		panic(err)
	}
	event := application.UIEvent{
		Kind: kind, RunID: runID, ScopeGeneration: scopeGeneration, PolicyGeneration: policyGeneration,
		Sequence: sequence, Text: text, TerminalOutcome: &outcome,
	}
	if kind == application.UIEventRunCompleted {
		coverage := application.UIAnswerCoverageUnavailable
		checked := 0
		if len(references) > 0 {
			coverage = application.UIAnswerCoverageComplete
			checked = 1
		}
		event.EvidenceReferences = append([]application.UIEvidenceReference(nil), references...)
		event.AnswerProvenance = &application.UIAnswerProvenance{
			EvidenceCount: len(references), ScopeGeneration: scopeGeneration, PolicyGeneration: policyGeneration,
			CoverageState: coverage, CheckedSourceCount: checked,
		}
	}
	return event
}

func updateModel(t *testing.T, model Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := model.Update(msg)
	result, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, want tui.Model", next)
	}
	return result, cmd
}

func keyText(value string) tea.KeyPressMsg {
	runes := []rune(value)
	return tea.KeyPressMsg{Code: runes[0], Text: value}
}

func commandFromCmd(t *testing.T, cmd tea.Cmd) application.UICommand {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected command, got nil")
	}
	msg := cmd()
	intent, ok := msg.(ApplicationCommandMsg)
	if !ok {
		t.Fatalf("command returned %T, want ApplicationCommandMsg", msg)
	}
	if err := intent.Command.Validate(); err != nil {
		t.Fatalf("command validation error = %v", err)
	}
	return intent.Command
}

func assertSingleEditor(t *testing.T, model Model) {
	t.Helper()
	if model.EditorCount() != 1 || model.FocusedEditorCount() != 1 || !model.composer.Focused() {
		t.Fatalf("editor invariant = total %d focused %d", model.EditorCount(), model.FocusedEditorCount())
	}
}
