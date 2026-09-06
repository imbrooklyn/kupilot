package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const testEvidenceID domain.EvidenceID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a12"

const testEvidenceIDTwo domain.EvidenceID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a14"

func TestEvidenceDetailUpdateRejectsLateRequestRunScopeAndSequence(t *testing.T) {
	t.Parallel()

	model, reference := modelWithEvidenceReference(t)
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	query := evidenceDetailQueryFromCmd(t, cmd)

	accepted := evidenceDetailResult(query, "The projected readiness condition is false.")
	staleResults := []application.UIEvidenceDetailResult{
		accepted,
		accepted,
		accepted,
		accepted,
	}
	staleResults[0].RequestID++
	staleResults[1].Reference.RunID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a13"
	staleResults[2].Reference.Scope.Generation++
	staleResults[3].Reference.Sequence++
	for _, result := range staleResults {
		model, _ = updateModel(t, model, EvidenceDetailResultMsg{Result: result})
		if strings.Contains(model.View().Content, accepted.Detail.Projection) {
			t.Fatalf("stale Evidence detail became visible: %#v", result.Reference)
		}
	}

	model, _ = updateModel(t, model, EvidenceDetailResultMsg{Result: accepted})
	if content := model.View().Content; !strings.Contains(content, accepted.Detail.Projection) ||
		strings.Contains(content, string(reference.EvidenceID)) || strings.Contains(content, string(reference.RunID)) ||
		!strings.Contains(content, "Observation detail") || !strings.Contains(content, "Type: Condition") ||
		strings.Contains(content, "projected.status") || strings.Contains(content, "generation 7") ||
		strings.Contains(content, "partial:") || strings.Contains(content, "truncated:") {
		t.Fatalf("matching Evidence detail is not visible: %q", content)
	}

	late := accepted
	late.Detail = cloneUIEvidenceDetail(accepted.Detail)
	late.Detail.Projection = "Late replacement must be ignored."
	model, _ = updateModel(t, model, EvidenceDetailResultMsg{Result: late})
	if content := model.View().Content; strings.Contains(content, late.Detail.Projection) ||
		!strings.Contains(content, accepted.Detail.Projection) {
		t.Fatalf("terminal Evidence detail accepted a late replacement: %q", content)
	}
}

func TestEvidenceDetailSinkCanaryNeverRendersRawInputs(t *testing.T) {
	t.Parallel()

	model, _ := modelWithEvidenceReference(t)
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	query := evidenceDetailQueryFromCmd(t, cmd)
	fake := &evidenceCanaryApplication{
		dispatchApplication: dispatchApplication{},
		rawLog:              "raw-log-canary-3f95",
		rawToolPayload:      "raw-tool-payload-canary-8a24",
		rawModelPayload:     "raw-model-payload-canary-6c17",
		credential:          "credential-canary-5b42",
		result:              evidenceDetailResult(query, "The safe projected restart count is 3."),
	}
	message := DispatchApplication(context.Background(), fake, ApplicationEvidenceDetailMsg{Query: query})
	model, _ = updateModel(t, model, message)

	content := model.View().Content
	if !strings.Contains(content, fake.result.Detail.Projection) {
		t.Fatalf("safe Evidence projection is missing: %q", content)
	}
	for _, canary := range []string{fake.rawLog, fake.rawToolPayload, fake.rawModelPayload, fake.credential} {
		if strings.Contains(content, canary) {
			t.Fatalf("raw sink canary reached the TUI: %q", canary)
		}
	}
	if model.EditorCount() != 1 || model.FocusedEditorCount() != 0 {
		t.Fatalf("Evidence detail changed the single-composer invariant: editors=%d focused=%d",
			model.EditorCount(), model.FocusedEditorCount())
	}
}

func TestEvidenceDetailKeyboardNavigationAndCancellation(t *testing.T) {
	t.Parallel()

	model, first := modelWithEvidenceReference(t)
	second := first
	second.EvidenceID = testEvidenceIDTwo
	model = modelWithEvidenceReferences(t, []application.UIEvidenceReference{first, second})

	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/r"})
	if !model.slashMenu.Open() {
		t.Fatal("test setup did not open composer suggestions")
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	if model.slashMenu.Open() || model.pickerOpen() {
		t.Fatal("Evidence selection left a second navigation surface open")
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	query := evidenceDetailQueryFromCmd(t, cmd)
	if query.Reference.EvidenceID != second.EvidenceID || model.EditorCount() != 1 || model.FocusedEditorCount() != 0 {
		t.Fatalf("selected Evidence/query/editor state = %#v/%d/%d", query.Reference, model.EditorCount(), model.FocusedEditorCount())
	}

	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if model.evidenceDialog.Open() || model.transcript.EvidenceSelecting() || model.pendingEvidence.RequestID != 0 ||
		model.FocusedEditorCount() != 1 {
		t.Fatalf("cancelled Evidence detail state = dialog %v selecting %v pending %#v focused %d",
			model.evidenceDialog.Open(), model.transcript.EvidenceSelecting(), model.pendingEvidence, model.FocusedEditorCount())
	}
	late := evidenceDetailResult(query, "Cancelled detail must remain invisible.")
	model, _ = updateModel(t, model, EvidenceDetailResultMsg{Result: late})
	if strings.Contains(model.View().Content, late.Detail.Projection) {
		t.Fatal("cancelled Evidence detail accepted a late result")
	}
}

func TestEvidenceDetailNarrowNoColorPartialAndExpiredStates(t *testing.T) {
	t.Parallel()

	model, _ := modelWithEvidenceReference(t)
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	query := evidenceDetailQueryFromCmd(t, cmd)
	partial := evidenceDetailResult(query, strings.Repeat("bounded projected condition ", 16))
	partial.Reference.State = application.UIEvidenceDetailPartial
	partial.Detail.Truncated = true
	model, _ = updateModel(t, model, EvidenceDetailResultMsg{Result: partial})
	model, _ = updateModel(t, model, tea.WindowSizeMsg{Width: 40, Height: 28})
	content := model.View().Content
	if lipgloss.Width(content) > 40 || lipgloss.Height(content) > 28 || strings.Contains(content, "\x1b[") ||
		!strings.Contains(content, "Ctrl+C") || !strings.Contains(content, "Enter to") || !strings.Contains(content, "Status: partial") ||
		strings.Contains(content, "partial:") || strings.Contains(content, "truncated:") {
		t.Fatalf("narrow no-color partial detail is unusable: %dx%d\n%s", lipgloss.Width(content), lipgloss.Height(content), content)
	}

	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEscape})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	model, cmd = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	query = evidenceDetailQueryFromCmd(t, cmd)
	expired := application.UIEvidenceDetailResult{RequestID: query.RequestID, Reference: query.Reference}
	expired.Reference.State = application.UIEvidenceDetailExpired
	model, _ = updateModel(t, model, EvidenceDetailResultMsg{Result: expired})
	content = model.View().Content
	if !strings.Contains(content, "State: expired") || !strings.Contains(content, "deleted, expired, or is no") ||
		strings.Contains(content, "raw") || model.EditorCount() != 1 {
		t.Fatalf("expired Evidence state is unsafe or unclear: %q", content)
	}
}

func TestEvidenceDetailScopeChangeClearsPendingRequest(t *testing.T) {
	t.Parallel()

	model, _ := modelWithEvidenceReference(t)
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	query := evidenceDetailQueryFromCmd(t, cmd)
	model.pendingScopeID = 51
	model.scope.Switching = true
	switchingResult := evidenceDetailResult(query, "Switching-scope detail must be discarded.")
	model, _ = updateModel(t, model, EvidenceDetailResultMsg{Result: switchingResult})
	if model.evidenceDialog.Open() || model.pendingEvidence.RequestID != 0 ||
		strings.Contains(model.View().Content, switchingResult.Detail.Projection) {
		t.Fatal("Evidence result was accepted while scope switching was pending")
	}
	model, _ = updateModel(t, model, ScopeResultMsg{Result: application.UIScopeResult{
		RequestID: 51, ExpectedGeneration: 7, ScopeGeneration: 8,
		Context: "next-context", Namespace: "next-namespace", ReadOnly: true,
	}})
	if model.evidenceDialog.Open() || model.pendingEvidence.RequestID != 0 || model.scope.Generation != 8 {
		t.Fatalf("scope change retained Evidence request: dialog %v pending %#v scope %#v",
			model.evidenceDialog.Open(), model.pendingEvidence, model.scope)
	}
	late := evidenceDetailResult(query, "Old-generation detail must be discarded.")
	model, _ = updateModel(t, model, EvidenceDetailResultMsg{Result: late})
	if strings.Contains(model.View().Content, late.Detail.Projection) {
		t.Fatal("old-generation Evidence result polluted the current Context")
	}
}

func TestEvidenceDetailReferencesSurviveSafeHistoryRestore(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	reference := application.UIEvidenceReference{
		EvidenceID: testEvidenceID, RunID: testRunID,
		Scope:    domain.ScopeSnapshot{Context: "historic-context", Namespace: "historic-namespace", Generation: 3},
		Sequence: 2, State: application.UIEvidenceDetailExpired,
	}
	model.applyAcceptedResume(application.UIResumedSession{
		ResumeRequestID: 7,
		Session: application.UISessionCandidate{
			ID: testSessionID, Title: "Historic diagnosis", UpdatedAtUnixMillis: 1,
			PrivacyMode: domain.PrivacyModeStandard,
		},
		History: []application.UIHistoryMessage{{
			Role: domain.MessageRoleAssistant, Format: domain.MessageFormatMarkdown,
			Content: "Historic conclusion.", RunID: testRunID,
			EvidenceReferences: []application.UIEvidenceReference{reference},
		}},
	})
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: 'e', Mod: tea.ModCtrl})
	model, cmd := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	query := evidenceDetailQueryFromCmd(t, cmd)
	content := model.View().Content
	if !sameUIEvidenceIdentity(query.Reference, reference) ||
		strings.Contains(content, string(reference.EvidenceID)) ||
		!strings.Contains(content, "Loading the saved observation") {
		t.Fatalf("historic Evidence reference/query = %#v", query.Reference)
	}
}

func modelWithEvidenceReference(t *testing.T) (Model, application.UIEvidenceReference) {
	t.Helper()
	reference := application.UIEvidenceReference{
		EvidenceID: testEvidenceID,
		RunID:      testRunID,
		Scope: domain.ScopeSnapshot{
			Context: "test-context", Namespace: "test-namespace", Generation: 7,
		},
		Sequence: 2,
		State:    application.UIEvidenceDetailAvailable,
	}
	return modelWithEvidenceReferences(t, []application.UIEvidenceReference{reference}), reference
}

func modelWithEvidenceReferences(t *testing.T, references []application.UIEvidenceReference) Model {
	t.Helper()
	model := newTestModel()
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind:               application.UIEventRunCompleted,
		RunID:              testRunID,
		ScopeGeneration:    7,
		PolicyGeneration:   1,
		Sequence:           2,
		Text:               "Final diagnosis.",
		EvidenceReferences: references,
	}})
	return model
}

func evidenceDetailQueryFromCmd(t *testing.T, cmd tea.Cmd) application.UIEvidenceDetailQuery {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected Evidence detail query, got nil")
	}
	message, ok := cmd().(ApplicationEvidenceDetailMsg)
	if !ok {
		t.Fatalf("Evidence detail command message = %T", cmd())
	}
	return message.Query
}

func evidenceDetailResult(
	query application.UIEvidenceDetailQuery,
	projection string,
) application.UIEvidenceDetailResult {
	detail := &application.UIEvidenceDetail{
		Category:   domain.EvidenceCategoryCondition,
		SourcePath: "projected.status.conditions",
		Resource: application.UIEvidenceResource{
			Type:       domain.BuiltInResourceType(domain.ResourceKindPod),
			APIVersion: "v1", Kind: "Pod", Namespace: "test-namespace", Name: "sample-pod",
		},
		ObservedAt:      time.Date(2026, time.August, 13, 7, 8, 9, 0, time.UTC),
		Truncated:       false,
		SensitiveFilter: application.UIEvidenceSensitiveFilterNotApplied,
		Projection:      projection,
	}
	return application.UIEvidenceDetailResult{
		RequestID: query.RequestID,
		Reference: query.Reference,
		Detail:    detail,
	}
}

func cloneUIEvidenceDetail(detail *application.UIEvidenceDetail) *application.UIEvidenceDetail {
	if detail == nil {
		return nil
	}
	clone := *detail
	return &clone
}

type evidenceCanaryApplication struct {
	dispatchApplication
	rawLog          string
	rawToolPayload  string
	rawModelPayload string
	credential      string
	result          application.UIEvidenceDetailResult
}

func (fake *evidenceCanaryApplication) QueryEvidenceDetail(
	context.Context,
	application.UIEvidenceDetailQuery,
) (application.UIEvidenceDetailResult, error) {
	return fake.result, nil
}
