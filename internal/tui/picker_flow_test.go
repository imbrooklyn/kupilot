package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestTypedPickerQueriesAndSelectionsReuseTheComposer(t *testing.T) {
	t.Parallel()

	t.Run("Context", func(t *testing.T) {
		model, query := openPickerFromDraft(t, "/context ", application.UICompletionContext)
		model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
			RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
			Contexts: []application.UIContextCandidate{{Name: "development", Current: true}},
		}})
		assertPersistentInputLabel(t, model, "Context · type to filter")
		model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		command := applicationCommandFromCmd(t, cmd)
		if command.Kind != application.UICommandSelectContext || command.Text != "development" || command.RequestID == 0 {
			t.Fatalf("Context command = %#v", command)
		}
		assertSingleEditor(t, model)
	})

	t.Run("Namespace", func(t *testing.T) {
		model, query := openPickerFromDraft(t, "/namespace pay", application.UICompletionNamespace)
		model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
			RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
			Namespaces: []application.UINamespaceCandidate{{Name: "payments"}},
		}})
		assertPersistentInputLabel(t, model, "Namespace · type to filter")
		model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		command := applicationCommandFromCmd(t, cmd)
		if command.Kind != application.UICommandSelectNamespace || command.Text != "payments" || command.ExpectedScopeGeneration != 7 {
			t.Fatalf("Namespace command = %#v", command)
		}
		assertSingleEditor(t, model)
	})

	t.Run("Resource", func(t *testing.T) {
		model, query := openPickerFromDraft(t, "/resource payment", application.UICompletionResource)
		model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
			RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
			Resources: []application.UIResourceCandidate{{
				APIVersion: "apps/v1", Kind: domain.ResourceKindDeployment,
				Namespace: "test-namespace", Name: "payment-api", Status: "Available",
			}},
		}})
		assertPersistentInputLabel(t, model, "Resource · type to filter")
		model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		command := applicationCommandFromCmd(t, cmd)
		if command.Kind != application.UICommandSelectResource || command.Resource == nil ||
			command.Resource.Kind != "Deployment" || command.Resource.Name != "payment-api" {
			t.Fatalf("Resource command = %#v", command)
		}
		if model.resource.Name != "" || model.pendingResource.Name != "payment-api" {
			t.Fatal("Resource selection became current before the fake Application result")
		}
		assertSingleEditor(t, model)
	})

	t.Run("Session", func(t *testing.T) {
		model, query := openPickerFromDraft(t, "/resume payment", application.UICompletionSession)
		model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
			RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
			Sessions: []application.UISessionCandidate{{
				ID: testSessionID, Title: "Payment diagnosis", UpdatedAtUnixMillis: 1,
				Context: "test-context", Namespace: "test-namespace", PrivacyMode: domain.PrivacyModeStandard,
			}},
		}})
		assertPersistentInputLabel(t, model, "Session · type to filter")
		model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		request := resumeRequestFromCmd(t, cmd)
		if request.Mode != application.UIResumeExact || request.SessionID != testSessionID {
			t.Fatalf("resume request = %#v", request)
		}
		assertSingleEditor(t, model)
	})
}

func assertPersistentInputLabel(t *testing.T, model Model, want string) {
	t.Helper()
	if !strings.Contains(model.inputLabelView(), want) || !strings.Contains(model.render(), want) {
		t.Fatalf("input label %q is not persistent: label=%q", want, model.inputLabelView())
	}
}

func TestResumePickerCancellationDiffersByOrigin(t *testing.T) {
	t.Parallel()

	topLevel := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		StartIntent: application.UIStartIntent{Kind: application.UIStartResumePicker},
		Scope:       ScopeView{Context: "current", Namespace: "default", Generation: 7, ReadOnly: true, Verified: true},
	})
	topLevel, cmd := updateModel(t, topLevel, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !commandQuits(cmd) || topLevel.startup.Ready {
		t.Fatal("top-level resume Picker cancellation did not exit")
	}

	inTUI := newTestModel()
	inTUI.session = SessionView{ID: testSessionID, Title: "Current Session"}
	inTUI, _ = updateModel(t, inTUI, tea.PasteMsg{Content: "/resume "})
	inTUI, cmd = updateModel(t, inTUI, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil || inTUI.session.ID != testSessionID || !inTUI.startup.Ready || inTUI.composer.Value() != "" {
		t.Fatalf("in-TUI resume cancellation changed current Session: %#v", inTUI.session)
	}
	assertSingleEditor(t, inTUI)
}

func TestResumePickerDeletesOnlyAfterExplicitConfirmation(t *testing.T) {
	model, query := openPickerFromDraft(t, "/resume payment", application.UICompletionSession)
	model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
		RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
		Sessions: []application.UISessionCandidate{{
			ID: testSessionID, Title: "Payment diagnosis", UpdatedAtUnixMillis: 1,
			Context: "test-context", Namespace: "test-namespace", PrivacyMode: domain.PrivacyModeStandard,
		}},
	}})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'D', Text: "D"})
	if cmd != nil || model.sessionDelete == nil || model.sessionDelete.ExpectedCurrent ||
		!strings.Contains(model.render(), "Delete selected Session?") {
		t.Fatal("resume Picker delete did not open a bound confirmation")
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if cmd != nil || model.sessionDelete != nil || !model.sessionPicker.Open() {
		t.Fatal("resume Picker delete cancellation changed history or closed the Picker")
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'D', Text: "D"})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: 'y'})
	command := applicationCommandFromCmd(t, cmd)
	if command.Kind != application.UICommandDeleteSession || command.Lifecycle == nil ||
		command.Lifecycle.SessionID != testSessionID || command.Lifecycle.ExpectedCurrent ||
		!command.Lifecycle.Confirmed {
		t.Fatalf("resume Picker delete command = %#v", command)
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandDeleteSession, RequestID: command.RequestID,
		Deletion: &application.SessionDeletionResult{SessionID: testSessionID},
	}})
	if !model.sessionPicker.Open() || model.sessionPicker.SourceCount() != 0 ||
		!strings.Contains(model.render(), "Session deleted") {
		t.Fatal("committed historical deletion did not remove only the bound Picker row")
	}
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil || !model.sessionPicker.Open() {
		t.Fatal("closing historical deletion result did not return to the resume Picker")
	}
}

func TestResumePickerKeepsLowercaseDeleteLetterInTheComposerFilter(t *testing.T) {
	model, query := openPickerFromDraft(t, "/resume payment", application.UICompletionSession)
	model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
		RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
		Sessions: []application.UISessionCandidate{{
			ID: testSessionID, Title: "Payment diagnosis", UpdatedAtUnixMillis: 1,
			Context: "test-context", Namespace: "test-namespace", PrivacyMode: domain.PrivacyModeStandard,
		}},
	}})

	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'd', Text: "d"})
	updated := completionQueryFromCmd(t, cmd)
	if model.sessionDelete != nil || model.composer.Value() != "/resume paymentd" || updated.Filter != "paymentd" {
		t.Fatalf("lowercase filter state = delete %#v draft %q query %#v", model.sessionDelete, model.composer.Value(), updated)
	}
}

func TestTopLevelResumePickerDeletionNeverFallsBackToANewSession(t *testing.T) {
	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		StartIntent: application.UIStartIntent{Kind: application.UIStartResumePicker},
		Scope:       ScopeView{Context: "current", Namespace: "default", Generation: 7, ReadOnly: true, Verified: true},
	})
	query := completionQueryFromCmd(t, model.Init())
	model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
		RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
		Sessions: []application.UISessionCandidate{{
			ID: testSessionID, Title: "Payment diagnosis", UpdatedAtUnixMillis: 1,
			Context: "test-context", Namespace: "test-namespace", PrivacyMode: domain.PrivacyModeStandard,
		}},
	}})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'D', Text: "D"})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: 'y'})
	command := applicationCommandFromCmd(t, cmd)
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandDeleteSession, RequestID: command.RequestID,
		Deletion: &application.SessionDeletionResult{SessionID: testSessionID},
	}})
	if model.startup.Ready || model.resumeOrigin != resumeOriginTopLevel || !model.sessionPicker.Open() || model.session.ID != "" {
		t.Fatal("top-level deletion created or implied a fallback Session")
	}
}

func TestScopeAndResourceResultsRejectStaleIdentity(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.pendingScopeID = 9
	model.scope.Switching = true
	model, _ = updateModel(t, model, ScopeResultMsg{Result: application.UIScopeResult{
		RequestID: 10, ExpectedGeneration: 7, ScopeGeneration: 8,
		Context: "stale", Namespace: "stale", ReadOnly: true,
	}})
	if model.scope.Context != "test-context" || !model.scope.Switching {
		t.Fatal("stale scope result changed UI state")
	}
	model, _ = updateModel(t, model, ScopeResultMsg{Result: application.UIScopeResult{
		RequestID: 9, ExpectedGeneration: 7, ScopeGeneration: 8,
		Context: "development", Namespace: "payments", ReadOnly: true,
	}})
	if model.scope.Context != "development" || model.scope.Namespace != "payments" || model.scope.Generation != 8 || model.scope.Switching {
		t.Fatalf("current scope result = %#v", model.scope)
	}

	model.pendingResourceID = 12
	model.pendingResource = ResourceView{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "payment-api"}
	reference := domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "payments", Name: "payment-api"}
	model, _ = updateModel(t, model, ResourceSelectionResultMsg{Result: application.UIResourceSelectionResult{
		RequestID: 12, ScopeGeneration: 7, Resource: &reference,
	}})
	if model.resource.Name != "" {
		t.Fatal("old-generation Resource result changed current state")
	}
	model, _ = updateModel(t, model, ResourceSelectionResultMsg{Result: application.UIResourceSelectionResult{
		RequestID: 12, ScopeGeneration: 8, Resource: &reference,
	}})
	if model.resource.Name != "payment-api" {
		t.Fatal("current Resource result was not accepted")
	}
}

func TestScopePreferenceFailuresRemainVisibleWithoutRevokingScope(t *testing.T) {
	t.Parallel()

	startup := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		Scope:                   ScopeView{Context: "current", Namespace: "default", Generation: 1, ReadOnly: true, Verified: true},
		ScopePreferenceDegraded: true,
	})
	entries := startup.transcript.Entries()
	if len(entries) != 1 || !strings.Contains(entries[0].Text, "could not be read") {
		t.Fatalf("startup preference warning entries = %#v", entries)
	}

	model := newTestModel()
	model.pendingScopeID = 19
	model.scope.Switching = true
	model, _ = updateModel(t, model, ScopeResultMsg{Result: application.UIScopeResult{
		RequestID: 19, ExpectedGeneration: 7, ScopeGeneration: 8,
		Context: "development", Namespace: "default", ReadOnly: true,
		ScopePreferenceDegraded: true,
	}})
	entries = model.transcript.Entries()
	if model.scope.Context != "development" || model.scope.Namespace != "default" || !model.scope.ReadOnly ||
		len(entries) != 2 || !strings.Contains(entries[1].Text, "could not save this Context") {
		t.Fatalf("active scope or preference warning = %#v / %#v", model.scope, entries)
	}
}

func TestScopeSwitchDiscardsLateRunEventsAndTerminatesOldRunProjection(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model.pendingScopeID = 15
	model.scope.Switching = true
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: "late old-scope text",
	}})
	if model.run.StreamedText != "" || model.run.LastSequence != 1 {
		t.Fatal("scope-switching UI accepted a late old-generation event")
	}
	model, _ = updateModel(t, model, ScopeResultMsg{Result: application.UIScopeResult{
		RequestID: 15, ExpectedGeneration: 7, ScopeGeneration: 8,
		Context: "development", Namespace: "payments", ReadOnly: true,
	}})
	if model.run.Active || !model.run.Terminal || model.run.Status != "cancelled" ||
		strings.Contains(model.run.StreamedText, "late old-scope") {
		t.Fatalf("old run projection after scope switch = %#v", model.run)
	}
}

func openPickerFromDraft(t *testing.T, draft string, want application.UICompletionKind) (Model, application.UICompletionQuery) {
	t.Helper()
	model := newTestModel()
	model, cmd := updateModel(t, model, tea.PasteMsg{Content: draft})
	query := completionQueryFromCmd(t, cmd)
	if query.Kind != want || query.RequestID == 0 || query.ScopeGeneration != 7 {
		t.Fatalf("completion query = %#v", query)
	}
	if model.composer.Value() != draft || model.EditorCount() != 1 || !model.pickerOpen() {
		t.Fatal("Picker did not reuse the sole composer")
	}
	return model, query
}

func completionQueryFromCmd(t *testing.T, cmd tea.Cmd) application.UICompletionQuery {
	t.Helper()
	for _, message := range messagesFromCmd(t, cmd) {
		if query, ok := message.(ApplicationQueryMsg); ok {
			if err := query.Query.Validate(); err != nil {
				t.Fatalf("completion query validation error = %v", err)
			}
			return query.Query
		}
	}
	t.Fatal("command did not return ApplicationQueryMsg")
	return application.UICompletionQuery{}
}

func resumeRequestFromCmd(t *testing.T, cmd tea.Cmd) application.UIResumeRequest {
	t.Helper()
	for _, message := range messagesFromCmd(t, cmd) {
		if resume, ok := message.(ApplicationResumeMsg); ok {
			if err := resume.Request.Validate(); err != nil {
				t.Fatalf("resume request validation error = %v", err)
			}
			return resume.Request
		}
	}
	t.Fatal("command did not return ApplicationResumeMsg")
	return application.UIResumeRequest{}
}

func applicationCommandFromCmd(t *testing.T, cmd tea.Cmd) application.UICommand {
	t.Helper()
	for _, message := range messagesFromCmd(t, cmd) {
		if value, ok := message.(ApplicationCommandMsg); ok {
			if err := value.Command.Validate(); err != nil {
				t.Fatalf("Application command validation error = %v", err)
			}
			return value.Command
		}
	}
	t.Fatal("command did not return ApplicationCommandMsg")
	return application.UICommand{}
}

func messagesFromCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected command, got nil")
	}
	message := cmd()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{message}
	}
	result := make([]tea.Msg, 0, len(batch))
	for _, child := range batch {
		result = append(result, messagesFromCmd(t, child)...)
	}
	return result
}

func commandQuits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

type fakeApplication struct {
	scopeActions int
}

func newFakeApplication() *fakeApplication { return new(fakeApplication) }

func (fake *fakeApplication) consumeApplicationCommand(t *testing.T, cmd tea.Cmd) application.UICommand {
	t.Helper()
	command := applicationCommandFromCmd(t, cmd)
	switch command.Kind {
	case application.UICommandSelectContext, application.UICommandSelectNamespace, application.UICommandActivateScope:
		fake.scopeActions++
	case application.UICommandAcceptResume:
		if command.Scope != nil {
			fake.scopeActions++
		}
	}
	return command
}
