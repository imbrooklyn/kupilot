package application

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestCoordinatorZeroDayRetentionKeepsOperationalDetailInMemory(t *testing.T) {
	clock := newCoordinatorClock()
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatalf("NewEventPublisher() error = %v", err)
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
			t.Fatalf("Publish(started) error = %v", err)
		}
		purpose := "Inspect one bounded resource."
		arguments := `{"kind":"Pod","name":"api"}`
		startedAt := clock.Now()
		invocation := domain.ToolInvocation{
			ID: domain.ToolInvocationID(coordinatorUUID(951)), RunID: input.RunID(), Sequence: 1,
			Name: domain.ToolNameGetResource, Version: "tool-v1", Purpose: &purpose,
			Scope: input.Scope().Snapshot(), ArgumentsJSON: arguments, ArgumentsDigest: domain.SHA256Hex(arguments),
			Status: domain.ToolInvocationStatusRequested, StartedAt: &startedAt,
		}
		if result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallRequested, ToolInvocation: &invocation}); err != nil || result != agent.EventSinkAccepted {
			t.Fatalf("Publish(requested) = %q, %v", result, err)
		}
		invocation.Status = domain.ToolInvocationStatusRunning
		if result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallStarted, ToolInvocation: &invocation}); err != nil || result != agent.EventSinkAccepted {
			t.Fatalf("Publish(tool started) = %q, %v", result, err)
		}
		finishedAt := clock.Now()
		summary := "The projected resource was inspected."
		invocation.Status = domain.ToolInvocationStatusSucceeded
		invocation.ResultSummary = &summary
		invocation.FinishedAt = &finishedAt
		if result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallCompleted, ToolInvocation: &invocation}); err != nil || result != agent.EventSinkAccepted {
			t.Fatalf("Publish(tool completed) = %q, %v", result, err)
		}
		class := domain.SafeErrorClassUnavailable
		if _, err := publisher.Publish(ctx, agent.RunEvent{
			Kind:    agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{Class: class, SafeMessage: "The model service is unavailable."},
		}); err != nil {
			t.Fatalf("Publish(failed) error = %v", err)
		}
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed, ErrorClass: &class, SafeMessage: "The model service is unavailable."}
	})
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	persistence.retentionDays = 0
	persistence.retentionFound = true
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Inspect the selected Pod."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil || result.PersistenceDegraded || persistence.toolCalls() != 0 ||
		persistence.auditCalls(domain.AuditEventToolCompleted) != 1 {
		t.Fatalf("zero-day result/error/tool writes/tool audits = %#v/%v/%d/%d", result, err, persistence.toolCalls(), persistence.auditCalls(domain.AuditEventToolCompleted))
	}
}

func TestCoordinatorRetentionReadFailureStopsBeforeAgentCall(t *testing.T) {
	clock := newCoordinatorClock()
	var runnerCalls atomic.Int64
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		runnerCalls.Add(1)
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	persistence.retentionLoadFailure = true
	_, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Inspect the selected Pod."})
	if !errors.Is(err, ErrPersistenceUnavailable) || runnerCalls.Load() != 0 || persistence.beginCalls() != 0 {
		t.Fatalf("retention read failure error/agent/begin calls = %v/%d/%d", err, runnerCalls.Load(), persistence.beginCalls())
	}
}

func TestCoordinatorPrivacyLifecycleReadFailureClearsConfirmationAuthority(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	_ = createCoordinatorSession(t, coordinator)
	persistence.retentionLoadFailure = true
	_, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 45})
	coordinator.mu.Lock()
	challenge := coordinator.privacyChallenge
	coordinator.mu.Unlock()
	if !errors.Is(err, ErrPersistenceUnavailable) || challenge != nil {
		t.Fatalf("ShowPrivacy(lifecycle failure) error/challenge = %v/%#v", err, challenge)
	}
}

func TestCoordinatorZeroDayCompletionPersistsOnlyExpiredReferenceState(t *testing.T) {
	for _, test := range []struct {
		name      string
		reference bool
		wantState domain.EvidenceDetailState
	}{
		{name: "referenced Evidence", reference: true, wantState: domain.EvidenceDetailExpired},
		{name: "no Evidence references", wantState: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
				return agent.RunOutcome{}
			}))
			startedAt := clock.Now()
			run := domain.AgentRun{
				ID: domain.AgentRunID(coordinatorUUID(961)), SessionID: domain.SessionID(coordinatorUUID(962)),
				RequestMessageID: domain.MessageID(coordinatorUUID(963)), Status: domain.AgentRunStatusRunning,
				Scope:         domain.ScopeSnapshot{Context: "test-context", Namespace: "team-a", Generation: 7},
				PromptVersion: agent.SystemPromptVersion, ToolCatalogVersion: agent.ToolCatalogVersion, StartedAt: &startedAt,
			}
			diagnosis := domain.Diagnosis{
				ID: domain.DiagnosisID(coordinatorUUID(964)), RunID: run.ID, Scope: run.Scope,
				AnswerMarkdown: "The bounded result remains available.", CreatedAt: startedAt,
			}
			if test.reference {
				evidenceID := domain.EvidenceID(coordinatorUUID(965))
				diagnosis.ConfirmedFacts = []domain.ConfirmedFact{{
					Statement: "A bounded observation supports the finding.", EvidenceIDs: []domain.EvidenceID{evidenceID},
				}}
				diagnosis.ObservedFrom = &startedAt
				diagnosis.ObservedTo = &startedAt
			}
			state := &activeRun{
				run: run, diagnosis: &diagnosis, terminalStatus: domain.AgentRunStatusCompleted,
				persistOperationalDetail: false,
			}
			if err := coordinator.persistTerminal(
				context.Background(),
				state,
				agent.RunEvent{Kind: agent.RunEventRunCompleted},
				terminalAudit(agent.RunEvent{Kind: agent.RunEventRunCompleted}),
			); err != nil {
				t.Fatalf("persistTerminal() error = %v", err)
			}
			stored := persistence.lastDiagnosis()
			if stored.EvidenceDetailsState != test.wantState || state.diagnosis.EvidenceDetailsState != "" {
				t.Fatalf("stored/in-memory Evidence states = %q/%q, want %q/empty", stored.EvidenceDetailsState, state.diagnosis.EvidenceDetailsState, test.wantState)
			}
		})
	}
}

func TestCoordinatorPrivacyLifecycleReviewTightensButNeverWidensRetention(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)

	show, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 41})
	if err != nil || show.Privacy == nil || show.Lifecycle == nil || show.Lifecycle.Validate() != nil {
		t.Fatalf("ShowPrivacy outcome/error = %#v/%v", show, err)
	}
	if show.Lifecycle.CurrentSession == nil || show.Lifecycle.CurrentSession.ID != session.ID ||
		show.Lifecycle.OperationalDetailRetentionDays != DefaultOperationalDetailRetentionDays ||
		show.Lifecycle.ReadAuditRetentionDays != ReadAuditRetentionDays ||
		show.Lifecycle.WriteAuditRetentionDays != WriteAuditRetentionDays ||
		show.Lifecycle.MinimalSessionsResumable || show.Lifecycle.DeletionIsForensicErase {
		t.Fatalf("lifecycle review = %#v", show.Lifecycle)
	}

	days := 14
	tightened, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandTightenRetention, RequestID: 41, PrivacyRevision: show.Privacy.Revision,
		Lifecycle: &SessionLifecycleIntent{RetentionDays: &days},
	})
	if err != nil || tightened.Lifecycle == nil || tightened.Lifecycle.OperationalDetailRetentionDays != 14 || persistence.retentionWrites() != 1 {
		t.Fatalf("tighten outcome/error/writes = %#v/%v/%d", tightened, err, persistence.retentionWrites())
	}

	wider := 30
	_, err = coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandTightenRetention, RequestID: 41, PrivacyRevision: show.Privacy.Revision,
		Lifecycle: &SessionLifecycleIntent{RetentionDays: &wider},
	})
	if !errors.Is(err, ErrRetentionWouldWiden) || persistence.retentionWrites() != 1 {
		t.Fatalf("widen error/writes = %v/%d, want ErrRetentionWouldWiden/1", err, persistence.retentionWrites())
	}
}

func TestCoordinatorRetentionChangeDuringActiveRunWritesNothing(t *testing.T) {
	clock := newCoordinatorClock()
	started := make(chan struct{})
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatalf("NewEventPublisher() error = %v", err)
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
			t.Fatalf("Publish(started) error = %v", err)
		}
		close(started)
		<-ctx.Done()
		_, _ = publisher.Publish(context.WithoutCancel(ctx), agent.RunEvent{
			Kind: agent.RunEventRunCancelled, TerminationReason: agent.RunTerminationUserCancelled,
		})
		class := domain.SafeErrorClassCancelled
		return agent.RunOutcome{Status: domain.AgentRunStatusCancelled, ErrorClass: &class, SafeMessage: "The AgentRun was cancelled."}
	})
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	review, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 43})
	if err != nil {
		t.Fatalf("ShowPrivacy() error = %v", err)
	}
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Inspect the selected Pod."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	<-started
	days := 0
	_, err = coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandTightenRetention, RequestID: 43, PrivacyRevision: review.Privacy.Revision,
		Lifecycle: &SessionLifecycleIntent{RetentionDays: &days},
	})
	if !errors.Is(err, ErrRunAlreadyActive) || persistence.retentionWrites() != 0 {
		t.Fatalf("active retention error/writes = %v/%d, want ErrRunAlreadyActive/0", err, persistence.retentionWrites())
	}
	if err := coordinator.CancelRun(context.Background(), CancelRunCommand{RunID: runID, ScopeGeneration: 7}); err != nil {
		t.Fatalf("CancelRun() error = %v", err)
	}
	if _, err := coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestCoordinatorPrivacyModeStartsANewMinimalNonResumableSession(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	standard := createCoordinatorSession(t, coordinator)
	show, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 42})
	if err != nil {
		t.Fatalf("ShowPrivacy() error = %v", err)
	}

	changed, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandSetPersistenceMode, RequestID: 42, PrivacyRevision: show.Privacy.Revision,
		Lifecycle: &SessionLifecycleIntent{PrivacyMode: domain.PrivacyModeMinimal},
	})
	if err != nil || changed.Session == nil || changed.Session.ID == standard.ID ||
		changed.Session.PrivacyMode != domain.PrivacyModeMinimal || changed.Lifecycle == nil ||
		changed.Lifecycle.CurrentSession == nil || changed.Lifecycle.CurrentSession.ID != changed.Session.ID {
		t.Fatalf("SetPersistenceMode outcome/error = %#v/%v", changed, err)
	}
	if persistence.sessionMode(changed.Session.ID) != domain.PrivacyModeMinimal {
		t.Fatalf("stored minimal Session mode = %q", persistence.sessionMode(changed.Session.ID))
	}
}

func TestCoordinatorMinimalSessionCompletesWithLifecycleOnly(t *testing.T) {
	clock := newCoordinatorClock()
	answerCanary := "This answer must remain in memory for a minimal Session."
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatalf("NewEventPublisher() error = %v", err)
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
			t.Fatalf("Publish(started) error = %v", err)
		}
		modelRequestID := domain.ModelRequestID(coordinatorUUID(970))
		if _, err := publisher.Publish(ctx, agent.RunEvent{
			Kind: agent.RunEventModelStreamStarted, ModelRequestID: &modelRequestID,
		}); err != nil {
			t.Fatalf("Publish(model started) error = %v", err)
		}
		purpose := "Inspect one bounded projection."
		arguments := `{"kind":"Pod","name":"api"}`
		startedAt := clock.Now()
		invocation := domain.ToolInvocation{
			ID: domain.ToolInvocationID(coordinatorUUID(971)), RunID: input.RunID(), Sequence: 1,
			Name: domain.ToolNameGetResource, Version: "tool-v1", Purpose: &purpose,
			Scope: input.Scope().Snapshot(), ArgumentsJSON: arguments, ArgumentsDigest: domain.SHA256Hex(arguments),
			Status: domain.ToolInvocationStatusRequested, StartedAt: &startedAt,
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallRequested, ToolInvocation: &invocation}); err != nil {
			t.Fatalf("Publish(Tool requested) error = %v", err)
		}
		invocation.Status = domain.ToolInvocationStatusRunning
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallStarted, ToolInvocation: &invocation}); err != nil {
			t.Fatalf("Publish(Tool started) error = %v", err)
		}
		finishedAt := clock.Now()
		summary := "The bounded projection was inspected."
		invocation.Status = domain.ToolInvocationStatusSucceeded
		invocation.ResultSummary = &summary
		invocation.FinishedAt = &finishedAt
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallCompleted, ToolInvocation: &invocation}); err != nil {
			t.Fatalf("Publish(Tool completed) error = %v", err)
		}
		diagnosis := domain.Diagnosis{
			ID: domain.DiagnosisID(coordinatorUUID(969)), RunID: input.RunID(), Scope: input.Scope().Snapshot(),
			AnswerMarkdown: answerCanary, CreatedAt: clock.Now(),
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventDiagnosisReady, Diagnosis: &diagnosis}); err != nil {
			t.Fatalf("Publish(Diagnosis) error = %v", err)
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunCompleted}); err != nil {
			t.Fatalf("Publish(completed) error = %v", err)
		}
		return agent.RunOutcome{Status: domain.AgentRunStatusCompleted, Diagnosis: &diagnosis}
	})
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	_ = createCoordinatorSession(t, coordinator)
	show, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandShowPrivacy, RequestID: 44})
	if err != nil {
		t.Fatalf("ShowPrivacy() error = %v", err)
	}
	changed, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandSetPersistenceMode, RequestID: 44, PrivacyRevision: show.Privacy.Revision,
		Lifecycle: &SessionLifecycleIntent{PrivacyMode: domain.PrivacyModeMinimal},
	})
	if err != nil || changed.Session == nil {
		t.Fatalf("SetPersistenceMode outcome/error = %#v/%v", changed, err)
	}
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: changed.Session.ID, Question: "Inspect the selected Pod without retaining content.",
	})
	if err != nil {
		t.Fatalf("StartRun(minimal) error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil || result.Status != domain.AgentRunStatusCompleted || result.PersistenceDegraded {
		t.Fatalf("WaitRun(minimal) = %#v/%v", result, err)
	}
	if persistence.finishCalls() != 1 || persistence.diagnosisCalls() != 0 ||
		persistence.toolCalls() != 0 || persistence.auditCalls(domain.AuditEventRunStarted) != 1 ||
		persistence.auditCalls(domain.AuditEventRunCompleted) != 1 ||
		persistence.auditCalls(domain.AuditEventModelRequested) != 0 ||
		persistence.auditCalls(domain.AuditEventToolRequested) != 0 ||
		persistence.auditCalls(domain.AuditEventToolCompleted) != 0 {
		t.Fatalf(
			"minimal writes finish/Diagnosis/Tool/start/completion/model/Tool-requested/Tool-completed audits = %d/%d/%d/%d/%d/%d/%d/%d",
			persistence.finishCalls(), persistence.diagnosisCalls(), persistence.toolCalls(),
			persistence.auditCalls(domain.AuditEventRunStarted), persistence.auditCalls(domain.AuditEventRunCompleted),
			persistence.auditCalls(domain.AuditEventModelRequested), persistence.auditCalls(domain.AuditEventToolRequested),
			persistence.auditCalls(domain.AuditEventToolCompleted),
		)
	}
}

func TestCoordinatorDeleteSessionHandlesConfirmationCancellationAndDatabaseFailure(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)

	invalid := UICommand{
		Kind: UICommandDeleteSession, RequestID: 51,
		Lifecycle: &SessionLifecycleIntent{SessionID: session.ID, ExpectedCurrent: true},
	}
	if _, err := coordinator.ExecuteUICommand(context.Background(), invalid); !errors.Is(err, ErrInvalidUICommand) || persistence.deleteWrites() != 0 {
		t.Fatalf("unconfirmed delete error/writes = %v/%d", err, persistence.deleteWrites())
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	confirmed := invalid
	confirmed.Lifecycle.Confirmed = true
	if _, err := coordinator.ExecuteUICommand(cancelled, confirmed); !errors.Is(err, context.Canceled) || persistence.deleteWrites() != 0 {
		t.Fatalf("cancelled delete error/writes = %v/%d", err, persistence.deleteWrites())
	}
	stale, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandDeleteSession, RequestID: 52,
		Lifecycle: &SessionLifecycleIntent{SessionID: session.ID, ExpectedCurrent: false, Confirmed: true},
	})
	if err != nil || stale.Failure != UIQueryUnavailable || persistence.deleteWrites() != 0 {
		t.Fatalf("stale current binding outcome/error/writes = %#v/%v/%d", stale, err, persistence.deleteWrites())
	}

	persistence.setDeleteFailure(true)
	failed, err := coordinator.ExecuteUICommand(context.Background(), confirmed)
	if err != nil || failed.Failure != UIQueryUnavailable || persistence.deleteWrites() != 1 || coordinator.CurrentUISession() == nil {
		t.Fatalf("failed delete outcome/error/writes/current = %#v/%v/%d/%#v", failed, err, persistence.deleteWrites(), coordinator.CurrentUISession())
	}
	persistence.setDeleteFailure(false)
	confirmed.RequestID++
	deleted, err := coordinator.ExecuteUICommand(context.Background(), confirmed)
	if err != nil || deleted.Deletion == nil || deleted.Deletion.SessionID != session.ID || !deleted.Deletion.WasCurrent ||
		persistence.deleteWrites() != 2 || coordinator.CurrentUISession() != nil {
		t.Fatalf("successful delete outcome/error/writes/current = %#v/%v/%d/%#v", deleted, err, persistence.deleteWrites(), coordinator.CurrentUISession())
	}
}

func TestCoordinatorDeleteHistoricalSessionKeepsCurrentSession(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	historical := createCoordinatorSession(t, coordinator)
	current := createCoordinatorSession(t, coordinator)

	result, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandDeleteSession, RequestID: 53,
		Lifecycle: &SessionLifecycleIntent{SessionID: historical.ID, ExpectedCurrent: false, Confirmed: true},
	})
	persistence.mu.Lock()
	_, historicalStillStored := persistence.sessions[historical.ID]
	persistence.mu.Unlock()
	currentState := coordinator.CurrentUISession()
	if err != nil || result.Deletion == nil || result.Deletion.SessionID != historical.ID || result.Deletion.WasCurrent ||
		persistence.deleteWrites() != 1 || historicalStillStored || currentState == nil || currentState.ID != current.ID {
		t.Fatalf("historical delete outcome/error/writes/stored/current = %#v/%v/%d/%t/%#v",
			result, err, persistence.deleteWrites(), historicalStillStored, currentState)
	}
}

func TestCoordinatorDeleteCurrentSessionCancelsActiveRunBeforeRepositoryWrite(t *testing.T) {
	clock := newCoordinatorClock()
	started := make(chan struct{})
	terminated := make(chan struct{})
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatalf("NewEventPublisher() error = %v", err)
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
			t.Fatalf("Publish(started) error = %v", err)
		}
		close(started)
		<-ctx.Done()
		_, _ = publisher.Publish(context.WithoutCancel(ctx), agent.RunEvent{
			Kind: agent.RunEventRunCancelled, TerminationReason: agent.RunTerminationUserCancelled,
		})
		class := domain.SafeErrorClassCancelled
		close(terminated)
		return agent.RunOutcome{Status: domain.AgentRunStatusCancelled, ErrorClass: &class, SafeMessage: "The AgentRun was cancelled."}
	})
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	persistence.setDeleteReady(terminated)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Inspect the selected Pod."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	<-started
	result, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandDeleteSession, RequestID: 61,
		Lifecycle: &SessionLifecycleIntent{SessionID: session.ID, ExpectedCurrent: true, Confirmed: true},
	})
	if err != nil || result.Deletion == nil || persistence.deleteWrites() != 1 {
		t.Fatalf("active delete outcome/error/delete writes = %#v/%v/%d", result, err, persistence.deleteWrites())
	}
	run, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil || run.Status != domain.AgentRunStatusCancelled {
		t.Fatalf("WaitRun() = %#v, %v", run, err)
	}
}

func TestCoordinatorDeleteCurrentSessionWaitsForTerminalRunToQuiesce(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	startedAt := clock.Now()
	cancelCalled := make(chan struct{})
	var cancelOnce sync.Once
	state := &activeRun{
		run: domain.AgentRun{
			ID: domain.AgentRunID(coordinatorUUID(966)), SessionID: session.ID,
			RequestMessageID: domain.MessageID(coordinatorUUID(967)), Status: domain.AgentRunStatusRunning,
			Scope:         domain.ScopeSnapshot{Context: "test-context", Namespace: "team-a", Generation: 7},
			PromptVersion: agent.SystemPromptVersion, ToolCatalogVersion: agent.ToolCatalogVersion, StartedAt: &startedAt,
		},
		terminal: true,
		cancel:   func() { cancelOnce.Do(func() { close(cancelCalled) }) },
		done:     make(chan struct{}),
	}
	coordinator.mu.Lock()
	coordinator.active = state
	coordinator.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	type deletionAttempt struct {
		outcome UICommandOutcome
		err     error
	}
	completed := make(chan deletionAttempt, 1)
	go func() {
		outcome, err := coordinator.ExecuteUICommand(ctx, UICommand{
			Kind: UICommandDeleteSession, RequestID: 65,
			Lifecycle: &SessionLifecycleIntent{SessionID: session.ID, ExpectedCurrent: true, Confirmed: true},
		})
		completed <- deletionAttempt{outcome: outcome, err: err}
	}()

	select {
	case <-cancelCalled:
		cancel()
	case result := <-completed:
		cancel()
		t.Fatalf("deletion returned before terminal run quiescence: %#v/%v", result.outcome, result.err)
	}
	result := <-completed
	if !errors.Is(result.err, context.Canceled) || persistence.deleteWrites() != 0 {
		t.Fatalf("terminal quiescence deletion outcome/error/writes = %#v/%v/%d", result.outcome, result.err, persistence.deleteWrites())
	}
}

func TestCoordinatorDeleteSessionInvalidatesPendingAndApprovedApprovalBeforeDelete(t *testing.T) {
	for _, state := range []domain.ApprovalState{domain.ApprovalStatePending, domain.ApprovalStateApproved} {
		t.Run(string(state), func(t *testing.T) {
			clock := newCoordinatorClock()
			coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
				return agent.RunOutcome{}
			}))
			session := createCoordinatorSession(t, coordinator)
			approvalFixture := newApprovalCoordinatorFixture(t)
			approvalFixture.sessionID = session.ID
			request := approvalFixture.submit(t, 36)
			if state == domain.ApprovalStateApproved {
				approved, err := approvalFixture.coordinator.Decide(
					context.Background(),
					approvalDecisionCommand(UICommandApproveRestart, request, 36, 106),
				)
				if err != nil || approved.State != domain.ApprovalStateApproved {
					t.Fatalf("Decide(approve) = %#v, %v", approved, err)
				}
			}
			coordinator.approvals = approvalFixture.coordinator
			result, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
				Kind: UICommandDeleteSession, RequestID: 62,
				Lifecycle: &SessionLifecycleIntent{SessionID: session.ID, ExpectedCurrent: true, Confirmed: true},
			})
			if err != nil || result.Deletion == nil || persistence.deleteWrites() != 1 ||
				approvalFixture.persistence.lastCloseExpected != state || approvalFixture.executor.calls != 0 {
				t.Fatalf("delete outcome/error/delete/close/executor = %#v/%v/%d/%s/%d", result, err, persistence.deleteWrites(), approvalFixture.persistence.lastCloseExpected, approvalFixture.executor.calls)
			}
		})
	}
}

func TestCoordinatorDeleteSessionApprovalFailureAndConsumingStateFailClosed(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*approvalCoordinatorFixture, domain.ApprovalRequest)
	}{
		{
			name: "cancellation persistence failure",
			configure: func(fixture *approvalCoordinatorFixture, _ domain.ApprovalRequest) {
				fixture.persistence.closeErr = errors.New("synthetic approval close failure")
			},
		},
		{
			name: "consuming approval",
			configure: func(fixture *approvalCoordinatorFixture, request domain.ApprovalRequest) {
				fixture.coordinator.mu.Lock()
				tracked := fixture.coordinator.active[request.ID]
				tracked.consuming = true
				fixture.coordinator.active[request.ID] = tracked
				fixture.coordinator.mu.Unlock()
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
				return agent.RunOutcome{}
			}))
			session := createCoordinatorSession(t, coordinator)
			approvalFixture := newApprovalCoordinatorFixture(t)
			approvalFixture.sessionID = session.ID
			request := approvalFixture.submit(t, 37)
			test.configure(approvalFixture, request)
			coordinator.approvals = approvalFixture.coordinator
			result, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
				Kind: UICommandDeleteSession, RequestID: 63,
				Lifecycle: &SessionLifecycleIntent{SessionID: session.ID, ExpectedCurrent: true, Confirmed: true},
			})
			if err != nil || result.Failure != UIQueryUnavailable || persistence.deleteWrites() != 0 ||
				approvalFixture.executor.calls != 0 || coordinator.CurrentUISession() == nil {
				t.Fatalf("denied delete outcome/error/delete/executor/current = %#v/%v/%d/%d/%#v", result, err, persistence.deleteWrites(), approvalFixture.executor.calls, coordinator.CurrentUISession())
			}
		})
	}
}

func TestCoordinatorDeleteSessionDatabaseFailureKeepsApprovalNonExecutable(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	approvalFixture := newApprovalCoordinatorFixture(t)
	approvalFixture.sessionID = session.ID
	request := approvalFixture.submit(t, 38)
	coordinator.approvals = approvalFixture.coordinator
	persistence.setDeleteFailure(true)

	result, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandDeleteSession, RequestID: 64,
		Lifecycle: &SessionLifecycleIntent{SessionID: session.ID, ExpectedCurrent: true, Confirmed: true},
	})
	approvalFixture.coordinator.mu.Lock()
	_, approvalStillActive := approvalFixture.coordinator.active[request.ID]
	approvalFixture.coordinator.mu.Unlock()
	if err != nil || result.Failure != UIQueryUnavailable || persistence.deleteWrites() != 1 ||
		approvalFixture.persistence.lastCloseExpected != domain.ApprovalStatePending ||
		approvalStillActive || approvalFixture.executor.calls != 0 || coordinator.CurrentUISession() == nil {
		t.Fatalf("failed graph outcome/error/delete/close/active/executor/current = %#v/%v/%d/%s/%t/%d/%#v",
			result, err, persistence.deleteWrites(), approvalFixture.persistence.lastCloseExpected,
			approvalStillActive, approvalFixture.executor.calls, coordinator.CurrentUISession())
	}
}

func TestApprovalCoordinatorPreparesSessionDeletionWithoutExecuting(t *testing.T) {
	for _, state := range []domain.ApprovalState{domain.ApprovalStatePending, domain.ApprovalStateApproved} {
		t.Run(string(state), func(t *testing.T) {
			fixture := newApprovalCoordinatorFixture(t)
			request := fixture.submit(t, 33)
			if state == domain.ApprovalStateApproved {
				approved, err := fixture.coordinator.Decide(
					context.Background(),
					approvalDecisionCommand(UICommandApproveRestart, request, 33, 103),
				)
				if err != nil || approved.State != domain.ApprovalStateApproved {
					t.Fatalf("Decide(approve) = %#v, %v", approved, err)
				}
			}
			if err := fixture.coordinator.PrepareSessionDeletion(context.Background(), fixture.sessionID); err != nil {
				t.Fatalf("PrepareSessionDeletion() error = %v", err)
			}
			if fixture.persistence.lastCloseExpected != state ||
				fixture.persistence.lastClosed.State != domain.ApprovalStateCancelled ||
				fixture.persistence.lastClosed.StateReason != domain.ApprovalReasonUserCancelled ||
				fixture.executor.calls != 0 {
				t.Fatalf("deletion preparation expected/closed/executor = %s/%#v/%d", fixture.persistence.lastCloseExpected, fixture.persistence.lastClosed, fixture.executor.calls)
			}
		})
	}
}

func TestApprovalCoordinatorSessionDeletionDenialsHaveZeroWrites(t *testing.T) {
	t.Run("consuming", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		request := fixture.submit(t, 34)
		fixture.coordinator.mu.Lock()
		tracked := fixture.coordinator.active[request.ID]
		tracked.consuming = true
		fixture.coordinator.active[request.ID] = tracked
		fixture.coordinator.mu.Unlock()

		err := fixture.coordinator.PrepareSessionDeletion(context.Background(), fixture.sessionID)
		if !errors.Is(err, ErrSessionDeletionUnsafe) || fixture.persistence.closes != 0 || fixture.executor.calls != 0 {
			t.Fatalf("consuming deletion error/close/executor = %v/%d/%d", err, fixture.persistence.closes, fixture.executor.calls)
		}
	})

	t.Run("cancellation persistence failure", func(t *testing.T) {
		fixture := newApprovalCoordinatorFixture(t)
		request := fixture.submit(t, 35)
		fixture.persistence.closeErr = errors.New("synthetic approval close failure")
		err := fixture.coordinator.PrepareSessionDeletion(context.Background(), fixture.sessionID)
		if !errors.Is(err, ErrApprovalPersistenceUnavailable) || fixture.persistence.closes != 1 || fixture.executor.calls != 0 {
			t.Fatalf("failed cancellation error/close/executor = %v/%d/%d", err, fixture.persistence.closes, fixture.executor.calls)
		}
		if _, ok := fixture.coordinator.active[request.ID]; ok {
			t.Fatal("failed durable cancellation left executable in-memory approval authority")
		}
	})
}
