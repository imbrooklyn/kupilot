package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestDispatchApplicationMapsTypedRequestsWithoutAdapterLeakage(t *testing.T) {
	t.Parallel()

	consumer := new(dispatchApplication)
	query := application.UICompletionQuery{
		RequestID: 1, Kind: application.UICompletionSession, Limit: application.MaxUIQueryCandidates,
	}
	if message := DispatchApplication(context.Background(), consumer, ApplicationQueryMsg{Query: query}); message.(CompletionResultMsg).Result.RequestID != query.RequestID {
		t.Fatalf("query dispatch message = %#v", message)
	}

	request := application.UIResumeRequest{RequestID: 2, Mode: application.UIResumeExact, SessionID: testSessionID}
	if message := DispatchApplication(context.Background(), consumer, ApplicationResumeMsg{Request: request}); message.(ResumeResultMsg).Result.RequestID != request.RequestID {
		t.Fatalf("resume dispatch message = %#v", message)
	}
	evidenceQuery := application.UIEvidenceDetailQuery{
		RequestID: 3,
		Reference: application.UIEvidenceReference{
			EvidenceID: testEvidenceID, RunID: testRunID,
			Scope:    domain.ScopeSnapshot{Context: "test-context", Namespace: "test-namespace", Generation: 7},
			Sequence: 2, State: application.UIEvidenceDetailAvailable,
		},
	}
	consumer.evidenceResult = evidenceDetailResult(evidenceQuery, "Safe projected detail.")
	message := DispatchApplication(context.Background(), consumer, ApplicationEvidenceDetailMsg{Query: evidenceQuery})
	if message.(EvidenceDetailResultMsg).Result.RequestID != evidenceQuery.RequestID {
		t.Fatalf("Evidence dispatch message = %#v", message)
	}

	command := application.UICommand{Kind: application.UICommandShowStatus}
	if message := DispatchApplication(context.Background(), consumer, ApplicationCommandMsg{Command: command}); message.(CommandResultMsg).Result.Command != command.Kind {
		t.Fatalf("command dispatch message = %#v", message)
	}
	if consumer.queryCalls != 1 || consumer.evidenceCalls != 1 || consumer.resumeCalls != 1 || consumer.commandCalls != 1 {
		t.Fatalf("dispatch calls = query %d Evidence %d resume %d command %d",
			consumer.queryCalls, consumer.evidenceCalls, consumer.resumeCalls, consumer.commandCalls)
	}
	failure, ok := DispatchApplication(context.Background(), nil, ApplicationQueryMsg{Query: query}).(ApplicationFailureMsg)
	if !ok || failure.RequestID != query.RequestID || failure.Query != query.Kind ||
		failure.ScopeGeneration != query.ScopeGeneration {
		t.Fatalf("query failure identity = %#v", failure)
	}
	evidenceFailure, ok := DispatchApplication(
		context.Background(), nil, ApplicationEvidenceDetailMsg{Query: evidenceQuery},
	).(ApplicationFailureMsg)
	if !ok || evidenceFailure.RequestID != evidenceQuery.RequestID ||
		!sameUIEvidenceIdentity(evidenceFailure.Evidence, evidenceQuery.Reference) {
		t.Fatalf("Evidence failure identity = %#v", evidenceFailure)
	}
}

func TestApplicationFailureOnlyReleasesMatchingPendingScope(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model.pendingScopeID = 17
	model.scope.Switching = true
	stale := ApplicationFailureMsg{
		Message: "The requested operation is busy.", RequestID: 18,
		ScopeGeneration: model.scope.Generation, Command: application.UICommandSelectNamespace,
	}
	model, _ = updateModel(t, model, stale)
	if !model.scope.Switching || model.pendingScopeID != 17 || model.dialog.Open() {
		t.Fatalf("stale failure changed UI state: scope %#v pending %d", model.scope, model.pendingScopeID)
	}

	current := stale
	current.RequestID = 17
	model, _ = updateModel(t, model, current)
	if model.scope.Switching || model.pendingScopeID != 0 || !model.dialog.Open() {
		t.Fatalf("current failure did not release UI state: scope %#v pending %d", model.scope, model.pendingScopeID)
	}
}

func TestResumedHistoryIsAppliedOnlyAfterApplicationAcceptance(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		StartIntent: application.UIStartIntent{Kind: application.UIStartResumeID, SessionID: testSessionID},
		Scope:       ScopeView{Context: "current", Namespace: "default", Generation: 7, ReadOnly: true},
	})
	request := resumeRequestFromCmd(t, model.Init())
	resumed := application.UIResumedSession{
		ResumeRequestID: request.RequestID,
		Session: application.UISessionCandidate{
			ID: testSessionID, Title: "Historic diagnosis", UpdatedAtUnixMillis: 1,
			Context: "current", Namespace: "default", PrivacyMode: domain.PrivacyModeStandard,
		},
		SavedScope: &domain.ScopeCandidate{Context: "current", Namespace: "default"},
		History: []application.UIHistoryMessage{
			{Role: domain.MessageRoleUser, Format: domain.MessageFormatPlain, Content: "Historic question."},
			{Role: domain.MessageRoleAssistant, Format: domain.MessageFormatMarkdown, Content: "Historic answer [E-OLD]."},
		},
	}
	model, cmd := updateModel(t, model, ResumeResultMsg{Result: application.UIResumeResult{
		RequestID: request.RequestID, Mode: request.Mode, Session: &resumed,
	}})
	if len(model.transcript.Entries()) != 0 || model.startup.Ready {
		t.Fatal("history became current before Application accepted resume")
	}
	accept := applicationCommandFromCmd(t, cmd)
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandAcceptResume, RequestID: accept.RequestID,
		Session: &application.UISessionState{
			ID: testSessionID, Title: "Historic diagnosis", PrivacyMode: domain.PrivacyModeStandard, Resumed: true,
		},
		Resumed: &resumed,
		Scope: &application.UIScopeResult{
			RequestID: accept.RequestID, ExpectedGeneration: 7, ScopeGeneration: 7,
			Context: "current", Namespace: "default", ReadOnly: true,
		},
	}})
	entries := model.transcript.Entries()
	if !model.startup.Ready || !model.session.Resumed || len(entries) != 3 ||
		!strings.Contains(entries[2].Text, "display-only") || len(entries[1].ToolSteps) != 0 {
		t.Fatalf("accepted resume entries = %#v", entries)
	}
}

type dispatchApplication struct {
	queryCalls     int
	evidenceCalls  int
	evidenceResult application.UIEvidenceDetailResult
	resumeCalls    int
	commandCalls   int
}

func (fake *dispatchApplication) QueryEvidenceDetail(
	context.Context,
	application.UIEvidenceDetailQuery,
) (application.UIEvidenceDetailResult, error) {
	fake.evidenceCalls++
	return fake.evidenceResult, nil
}

func (fake *dispatchApplication) QueryUI(
	context.Context,
	application.UICompletionQuery,
) (application.UICompletionResult, error) {
	fake.queryCalls++
	return application.UICompletionResult{
		RequestID: 1, Kind: application.UICompletionSession, ScopeGeneration: 0,
	}, nil
}

func (fake *dispatchApplication) ResumeUI(
	context.Context,
	application.UIResumeRequest,
) (application.UIResumeResult, error) {
	fake.resumeCalls++
	return application.UIResumeResult{
		RequestID: 2, Mode: application.UIResumeExact,
		Session: &application.UIResumedSession{
			ResumeRequestID: 2,
			Session: application.UISessionCandidate{
				ID: testSessionID, UpdatedAtUnixMillis: 1, PrivacyMode: domain.PrivacyModeStandard,
			},
		},
	}, nil
}

func (fake *dispatchApplication) ExecuteUICommand(
	context.Context,
	application.UICommand,
) (application.UICommandOutcome, error) {
	fake.commandCalls++
	return application.UICommandOutcome{
		Command: application.UICommandShowStatus,
		Status:  &application.UIStatusResult{},
	}, nil
}
