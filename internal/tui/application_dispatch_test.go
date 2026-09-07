package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/agent"
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
		Scope:       ScopeView{Context: "current", Namespace: "default", Generation: 7, ReadOnly: true, Verified: true},
	})
	request := resumeRequestFromCmd(t, model.Init())
	historicAnswer := strings.Repeat("a", application.MaxQuestionBytes+1)
	resumed := application.UIResumedSession{
		ResumeRequestID: request.RequestID,
		Session: application.UISessionCandidate{
			ID: testSessionID, Title: "Historic diagnosis", LastActivityAtUnixMillis: 1,
			Context: "current", Namespace: "default", PrivacyMode: domain.PrivacyModeStandard,
		},
		SavedScope: &domain.ScopeCandidate{Context: "current", Namespace: "default"},
		History: []application.UIHistoryMessage{
			{Role: domain.MessageRoleUser, Format: domain.MessageFormatPlain, Content: "Historic question."},
			{Role: domain.MessageRoleAssistant, Format: domain.MessageFormatMarkdown, Content: historicAnswer},
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
	if entries[1].Text != historicAnswer {
		t.Fatalf("historic answer bytes = %d, want %d", len(entries[1].Text), len(historicAnswer))
	}
	model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
	if got := model.composer.Value(); got != "Historic question." {
		t.Fatalf("resumed user input history = %q", got)
	}
}

func TestAcceptedResumeReplacesInputHistoryWithResumedUserMessages(t *testing.T) {
	t.Parallel()

	resumedSessionID := domain.SessionID("0198a46e-7d2a-7d34-9b6f-2df5f45a2a24")
	model := newTestModel()
	model.session = SessionView{ID: testSessionID, Title: "Current Session"}
	model.composer.RecordSubmission("Question from the current Session.")
	request := resumeRequestFromCmd(t, model.beginResume(
		application.UIResumeExact,
		resumedSessionID,
		resumeOriginInTUI,
	))
	resumed := application.UIResumedSession{
		ResumeRequestID: request.RequestID,
		Session: application.UISessionCandidate{
			ID: resumedSessionID, Title: "Resumed Session", LastActivityAtUnixMillis: 1,
			Context: "test-context", Namespace: "test-namespace", PrivacyMode: domain.PrivacyModeStandard,
		},
		SavedScope: &domain.ScopeCandidate{Context: "test-context", Namespace: "test-namespace"},
		History: []application.UIHistoryMessage{
			{Role: domain.MessageRoleUser, Format: domain.MessageFormatPlain, Content: "Older resumed question."},
			{Role: domain.MessageRoleAssistant, Format: domain.MessageFormatMarkdown, Content: "Historic answer."},
			{Role: domain.MessageRoleSystemNotice, Format: domain.MessageFormatPlain, Content: "Historic notice."},
			{Role: domain.MessageRoleUser, Format: domain.MessageFormatPlain, Content: "Newest resumed\x1b]52;c;clipboard-canary\x07 question."},
		},
	}
	model, command := updateModel(t, model, ResumeResultMsg{Result: application.UIResumeResult{
		RequestID: request.RequestID, Mode: request.Mode, Session: &resumed,
	}})
	model.composer.Reset()
	if !model.composer.PreviousHistory() || model.composer.Value() != "Question from the current Session." {
		t.Fatalf("staged resume changed current input history: %q", model.composer.Value())
	}
	model.composer.Reset()
	accept := applicationCommandFromCmd(t, command)
	model, _ = updateModel(t, model, CommandResultMsg{Result: application.UICommandOutcome{
		Command: application.UICommandAcceptResume, RequestID: accept.RequestID,
		Session: &application.UISessionState{
			ID: resumedSessionID, Title: "Resumed Session", PrivacyMode: domain.PrivacyModeStandard, Resumed: true,
		},
		Resumed: &resumed,
		Scope: &application.UIScopeResult{
			RequestID: accept.RequestID, ExpectedGeneration: 7, ScopeGeneration: 7,
			Context: "test-context", Namespace: "test-namespace", ReadOnly: true,
		},
	}})

	for index, want := range []string{
		"Newest resumed question.",
		"Older resumed question.",
		"Older resumed question.",
	} {
		model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyUp})
		if got := model.composer.Value(); got != want {
			t.Fatalf("resume history Up %d = %q, want %q", index+1, got, want)
		}
	}
	for index, want := range []string{"Newest resumed question.", ""} {
		model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyDown})
		if got := model.composer.Value(); got != want {
			t.Fatalf("resume history Down %d = %q, want %q", index+1, got, want)
		}
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
				ID: testSessionID, LastActivityAtUnixMillis: 1, PrivacyMode: domain.PrivacyModeStandard,
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
		Status: &application.UIStatusResult{
			CapabilityCatalogVersion:       agent.ToolCatalogVersion,
			ResourcePolicyVersion:          domain.ResourcePolicyVersion,
			ObservabilityPolicyVersion:     domain.ObservabilityPolicyVersion,
			RemoteDiagnosticsPolicyVersion: domain.RemoteDiagnosticsPolicyVersion,
			LocalExecutionPolicyVersion:    domain.LocalExecutionPolicyVersion,
			ResourceTypeCount:              len(domain.BuiltInResourcePolicies()),
			ConversationInput: application.ConversationInputStatus{
				MaximumItems:     application.MaxConversationInputItems,
				MaximumBytes:     application.MaxConversationInputAggregateBytes,
				MaximumItemBytes: application.MaxConversationInputItemBytes,
			},
			AgentModel: application.UIModelRoleStatus{
				Role: domain.ModelRoleAgent, Profile: "agent", OriginHash: strings.Repeat("a", 64),
				Configured: true, Available: true, Consented: true,
			},
			Budget: application.UIBudgetStatus{
				ModelEvidenceBasis: application.ModelBudgetEvidenceBasis,
				Profile:            agent.BudgetProfileBalanced, RunMilliseconds: 1_800_000, RemainingMilliseconds: 1_800_000,
				StepsMaximum: 32, ToolCallsMaximum: 48, ModelCallsMaximum: 16,
				ModelCostUnitsMaximum: 16, SummaryCallsMaximum: 2, SummaryCostUnitsMaximum: 2,
				ReviewerCallsMaximum: 8, ReviewerCostUnitsMaximum: 8,
				ToolResultBytesMaximum: 4 * 1024 * 1024, LogCallsMaximum: 12,
				LogContainersMaximum: 8, LogBytesMaximum: 256 * 1024,
				EventPagesMaximum: 4, EventPageItemsMaximum: 50, EventPageBytesMaximum: 128 * 1024, EventBytesMaximum: 512 * 1024,
				MetricCallsMaximum: 12, MetricContainersMaximum: 35, MetricBytesMaximum: 256 * 1024,
				DataSourceCallsMaximum: 16, DataSourcePagesMaximum: 4, DataSourceSeriesMaximum: 25,
				RemoteExecCallsMaximum:   4,
				LocalProcessCallsMaximum: 1,
				DataSourceSamplesMaximum: 400, DataSourceLinesMaximum: 400, DataSourceBytesMaximum: 512 * 1024,
				DataSourceWindowMillis: 21_600_000, DataSourceStepMillis: 300_000,
				ResourcePagesMaximum: 4, ResourcePageItemsMaximum: 50, ResourcePageBytesMaximum: 256 * 1024,
				ResourceScannedMaximum: 200, ResourceReturnedMaximum: 50, ResourceBytesMaximum: 1024 * 1024,
				FineGrained: application.NewUIBudgetMeasures(agent.DefaultRunBudgetLimits()),
			},
		},
	}, nil
}
