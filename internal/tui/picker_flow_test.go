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
		model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		request := resumeRequestFromCmd(t, cmd)
		if request.Mode != application.UIResumeExact || request.SessionID != testSessionID {
			t.Fatalf("resume request = %#v", request)
		}
		assertSingleEditor(t, model)
	})
}

func TestResumePickerCancellationDiffersByOrigin(t *testing.T) {
	t.Parallel()

	topLevel := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		StartIntent: application.UIStartIntent{Kind: application.UIStartResumePicker},
		Scope:       ScopeView{Context: "current", Namespace: "default", Generation: 7, ReadOnly: true},
	})
	topLevel, cmd := updateModel(t, topLevel, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !commandQuits(cmd) || topLevel.startup.Ready {
		t.Fatal("top-level resume Picker cancellation did not exit")
	}

	inTUI := newTestModel()
	inTUI.session = SessionView{ID: testSessionID, Title: "Current Session"}
	inTUI, _ = updateModel(t, inTUI, tea.PasteMsg{Content: "/resume "})
	inTUI, cmd = updateModel(t, inTUI, tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd != nil || inTUI.session.ID != testSessionID || !inTUI.startup.Ready || inTUI.composer.Value() != "" {
		t.Fatalf("in-TUI resume cancellation changed current Session: %#v", inTUI.session)
	}
	assertSingleEditor(t, inTUI)
}

func TestResumeScopeConflictHasZeroScopeActionUntilExplicitChoice(t *testing.T) {
	t.Parallel()

	newConflictModel := func(t *testing.T) Model {
		t.Helper()
		model := newTestModel()
		model.session = SessionView{ID: testSessionID, Title: "Current Session"}
		resumedID := domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2a21")
		cmd := model.beginResume(application.UIResumeExact, resumedID, resumeOriginInTUI)
		request := resumeRequestFromCmd(t, cmd)
		model, _ = updateModel(t, model, ResumeResultMsg{Result: application.UIResumeResult{
			RequestID: request.RequestID, Mode: request.Mode,
			Session: &application.UIResumedSession{
				Session: application.UISessionCandidate{
					ID: resumedID, Title: "Resumed Session", UpdatedAtUnixMillis: 1,
					Context: "saved", Namespace: "payments", PrivacyMode: domain.PrivacyModeStandard,
				},
				SavedScope: &domain.ScopeCandidate{Context: "saved", Namespace: "payments"},
			},
		}})
		if !model.scopeConflict.Open() || model.FocusedEditorCount() != 0 {
			t.Fatal("scope conflict did not capture focus")
		}
		return model
	}

	keepCurrent := newConflictModel(t)
	fake := newFakeApplication()
	if fake.scopeActions != 0 {
		t.Fatal("fake Application started with a scope action")
	}
	keepCurrent, cmd := updateModel(t, keepCurrent, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || fake.scopeActions != 0 || !keepCurrent.session.Resumed || keepCurrent.scope.Switching {
		t.Fatal("default keep-current choice dispatched a scope action")
	}

	useSaved := newConflictModel(t)
	useSaved, _ = updateModel(t, useSaved, tea.KeyPressMsg{Code: tea.KeyDown})
	if fake.scopeActions != 0 {
		t.Fatal("moving confirmation focus dispatched a scope action")
	}
	useSaved, cmd = updateModel(t, useSaved, tea.KeyPressMsg{Code: tea.KeyEnter})
	command := fake.consumeApplicationCommand(t, cmd)
	if command.Kind != application.UICommandActivateScope || command.Scope == nil ||
		command.Scope.Context != "saved" || fake.scopeActions != 1 || !useSaved.scope.Switching {
		t.Fatalf("saved-scope command = %#v, actions = %d", command, fake.scopeActions)
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

func TestScopeSwitchDiscardsLateRunEventsAndTerminatesOldRunProjection(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model.pendingScopeID = 15
	model.scope.Switching = true
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID,
		ScopeGeneration: 7, Sequence: 2, Text: "late old-scope text",
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
	}
	return command
}
