package tui

import (
	"fmt"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

const testSessionID domain.SessionID = "0198a46e-7d2a-7d34-9b6f-2df5f45a2a20"

func TestStartIntentsProduceOnlyTheirTypedStartupAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		intent     application.UIStartIntent
		wantQuery  application.UICompletionKind
		wantResume application.UIResumeMode
		wantReady  bool
	}{
		{name: "new", intent: application.UIStartIntent{Kind: application.UIStartNew}, wantReady: true},
		{name: "resume picker", intent: application.UIStartIntent{Kind: application.UIStartResumePicker}, wantQuery: application.UICompletionSession},
		{name: "resume ID", intent: application.UIStartIntent{Kind: application.UIStartResumeID, SessionID: testSessionID}, wantResume: application.UIResumeExact},
		{name: "resume last", intent: application.UIStartIntent{Kind: application.UIStartResumeLast}, wantResume: application.UIResumeLast},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := NewModel(Config{
				Width: 80, Height: 24, Theme: ThemeNoColor, StartIntent: tt.intent,
				Scope: ScopeView{Context: "current", Namespace: "default", Generation: 7, ReadOnly: true},
			})
			cmd := model.Init()
			if model.startup.Ready != tt.wantReady {
				t.Fatalf("startup ready = %v, want %v", model.startup.Ready, tt.wantReady)
			}
			switch {
			case tt.wantQuery != "":
				message, ok := cmd().(ApplicationQueryMsg)
				if !ok || message.Query.Kind != tt.wantQuery {
					t.Fatalf("startup command = %#v", cmd())
				}
			case tt.wantResume != "":
				message, ok := cmd().(ApplicationResumeMsg)
				if !ok || message.Request.Mode != tt.wantResume {
					t.Fatalf("startup command = %#v", cmd())
				}
			default:
				if cmd != nil {
					t.Fatal("new Session startup queried history")
				}
			}
			assertSingleEditor(t, model)
		})
	}
}

func TestDirectResumeStartIntentsReachReadyOnlyAfterMatchingFakeResult(t *testing.T) {
	t.Parallel()

	resumedID := domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2a22")
	tests := []struct {
		name     string
		intent   application.UIStartIntent
		resultID domain.SessionID
		wantMode application.UIResumeMode
	}{
		{
			name:     "exact ID",
			intent:   application.UIStartIntent{Kind: application.UIStartResumeID, SessionID: resumedID},
			resultID: resumedID, wantMode: application.UIResumeExact,
		},
		{
			name:     "last",
			intent:   application.UIStartIntent{Kind: application.UIStartResumeLast},
			resultID: resumedID, wantMode: application.UIResumeLast,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			model := NewModel(Config{
				Width: 80, Height: 24, Theme: ThemeNoColor, StartIntent: tt.intent,
				Scope: ScopeView{Context: "current", Namespace: "default", Generation: 7, ReadOnly: true},
			})
			request := resumeRequestFromCmd(t, model.Init())
			if request.Mode != tt.wantMode || model.startup.Ready {
				t.Fatalf("initial resume state = request %#v ready %v", request, model.startup.Ready)
			}
			model, _ = updateModel(t, model, ResumeResultMsg{Result: application.UIResumeResult{
				RequestID: request.RequestID, Mode: request.Mode,
				Session: &application.UIResumedSession{Session: application.UISessionCandidate{
					ID: tt.resultID, Title: "Recovered diagnosis", UpdatedAtUnixMillis: 1,
					Context: "current", Namespace: "default", PrivacyMode: domain.PrivacyModeStandard,
				}},
			}})
			if !model.startup.Ready || !model.session.Resumed || model.session.ID != tt.resultID || model.scopeConflict.Open() {
				t.Fatalf("resumed state = startup %#v session %#v", model.startup, model.session)
			}
			assertSingleEditor(t, model)
		})
	}
}

func TestCompletionRejectsStaleRequestAndGenerationAndKeepsOneEditor(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.openCompletion(application.UICompletionNamespace, "pay", resumeOriginNone)
	pending := model.pendingCompletion
	if pending.RequestID == 0 {
		t.Fatal("completion request has no identity")
	}

	staleRequest := application.UICompletionResult{
		RequestID: pending.RequestID + 1, Kind: pending.Kind,
		ScopeGeneration: pending.ScopeGeneration,
		Namespaces:      []application.UINamespaceCandidate{{Name: "stale-request"}},
	}
	model, _ = updateModel(t, model, CompletionResultMsg{Result: staleRequest})
	if model.namespacePicker.SourceCount() != 0 {
		t.Fatal("stale request changed Picker state")
	}

	staleGeneration := staleRequest
	staleGeneration.RequestID = pending.RequestID
	staleGeneration.ScopeGeneration++
	staleGeneration.Namespaces[0].Name = "stale-generation"
	model, _ = updateModel(t, model, CompletionResultMsg{Result: staleGeneration})
	if model.namespacePicker.SourceCount() != 0 {
		t.Fatal("stale generation changed Picker state")
	}

	current := staleGeneration
	current.ScopeGeneration = pending.ScopeGeneration
	current.Namespaces[0].Name = "payments"
	model, _ = updateModel(t, model, CompletionResultMsg{Result: current})
	if model.namespacePicker.SourceCount() != 1 {
		t.Fatal("current completion did not update Picker state")
	}
	assertSingleEditor(t, model)
}

func TestResourcePickerBoundsSourceAndVisibleRowsWithoutAnEditor(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.openCompletion(application.UICompletionResource, "", resumeOriginNone)
	pending := model.pendingCompletion
	resources := make([]application.UIResourceCandidate, application.MaxUIQueryCandidates)
	for index := range resources {
		resources[index] = application.UIResourceCandidate{
			APIVersion: "v1", Kind: domain.ResourceKindPod, Namespace: "payments",
			Name: fmt.Sprintf("pod-%02d", index), Status: "Ready",
		}
	}
	model, _ = updateModel(t, model, CompletionResultMsg{Result: application.UICompletionResult{
		RequestID: pending.RequestID, Kind: pending.Kind,
		ScopeGeneration: pending.ScopeGeneration, Resources: resources,
	}})
	if model.resourcePicker.SourceCount() != application.MaxUIQueryCandidates {
		t.Fatalf("source count = %d", model.resourcePicker.SourceCount())
	}
	if model.resourcePicker.VisibleCount() != MaxPickerCandidates {
		t.Fatalf("visible count = %d", model.resourcePicker.VisibleCount())
	}
	assertSingleEditor(t, model)
}
