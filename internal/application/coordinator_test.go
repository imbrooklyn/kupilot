package application

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

func TestCoordinatorCancelsOneRunAndRejectsLateEvents(t *testing.T) {
	t.Parallel()
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
		if _, err := publisher.Publish(context.WithoutCancel(ctx), agent.RunEvent{
			Kind: agent.RunEventRunCancelled, TerminationReason: agent.RunTerminationUserCancelled,
		}); err != nil {
			t.Fatalf("Publish(cancelled) error = %v", err)
		}
		class := domain.SafeErrorClassCancelled
		return agent.RunOutcome{
			Status: domain.AgentRunStatusCancelled, ErrorClass: &class,
			SafeMessage: "The Agent run was cancelled.",
		}
	})
	coordinator, persistence, scope, ui := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected Pod.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	<-started
	if err := coordinator.CancelRun(context.Background(), CancelRunCommand{RunID: runID, ScopeGeneration: 7}); err != nil {
		t.Fatalf("CancelRun() error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	if result.Status != domain.AgentRunStatusCancelled || result.PersistenceDegraded {
		t.Fatalf("RunResult = %#v", result)
	}
	if persistence.finishCalls() != 1 || persistence.lastFinished().Status != domain.AgentRunStatusCancelled {
		t.Fatalf("terminal persistence = %#v", persistence.lastFinished())
	}
	if scope.bound() {
		t.Fatal("scope remained bound after cancellation")
	}
	assertOneUITerminal(t, ui.events(), UIEventRunCancelled)
	late := agent.RunEvent{
		RunID: runID, ScopeGeneration: 7, Sequence: 3, OccurredAt: clock.Now(),
		Kind:    agent.RunEventRunFailed,
		Failure: &agent.RunEventFailure{Class: domain.SafeErrorClassInternal, SafeMessage: "Late failure."},
	}
	if result := coordinator.Publish(context.Background(), late); result != agent.EventSinkRejected {
		t.Fatalf("late Publish() = %q", result)
	}
	if err := coordinator.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestCoordinatorDurableBeginFailurePerformsZeroAgentCalls(t *testing.T) {
	t.Parallel()
	clock := newCoordinatorClock()
	var runnerCalls atomic.Int64
	coordinator, persistence, scope, ui := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		runnerCalls.Add(1)
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	persistence.setBeginFailure(true)
	_, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected Pod.",
	})
	if !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("StartRun() error = %v", err)
	}
	if runnerCalls.Load() != 0 || persistence.beginCalls() != 1 || scope.bound() || len(ui.events()) != 0 {
		t.Fatalf("failure side effects: runner=%d begin=%d bound=%v UI=%d", runnerCalls.Load(), persistence.beginCalls(), scope.bound(), len(ui.events()))
	}
	_, err = coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Try again.",
	})
	if !errors.Is(err, ErrPersistenceUnavailable) || persistence.beginCalls() != 1 || runnerCalls.Load() != 0 {
		t.Fatalf("degraded retry error/calls = %v/%d/%d", err, persistence.beginCalls(), runnerCalls.Load())
	}
}

func TestCoordinatorRejectsCancelledAndStaleStartsBeforePersistence(t *testing.T) {
	t.Parallel()
	t.Run("cancelled Context", func(t *testing.T) {
		clock := newCoordinatorClock()
		var runnerCalls atomic.Int64
		coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
			runnerCalls.Add(1)
			return agent.RunOutcome{}
		}))
		session := createCoordinatorSession(t, coordinator)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := coordinator.StartRun(ctx, StartRunCommand{SessionID: session.ID, Question: "Cancelled start."})
		if !errors.Is(err, context.Canceled) || persistence.beginCalls() != 0 || runnerCalls.Load() != 0 {
			t.Fatalf("cancelled start error/calls = %v/%d/%d", err, persistence.beginCalls(), runnerCalls.Load())
		}
	})
	t.Run("stale scope binding", func(t *testing.T) {
		clock := newCoordinatorClock()
		var runnerCalls atomic.Int64
		coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
			runnerCalls.Add(1)
			return agent.RunOutcome{}
		}))
		session := createCoordinatorSession(t, coordinator)
		scope.mu.Lock()
		scope.runID = domain.AgentRunID(coordinatorUUID(777))
		scope.cancel = func() {}
		scope.mu.Unlock()
		_, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Stale start."})
		if !errors.Is(err, ErrScopeUnavailable) || persistence.beginCalls() != 0 || runnerCalls.Load() != 0 {
			t.Fatalf("stale start error/calls = %v/%d/%d", err, persistence.beginCalls(), runnerCalls.Load())
		}
	})
}

func TestCoordinatorRejectsCompleteScopeMismatchBeforeToolPersistence(t *testing.T) {
	t.Parallel()
	clock := newCoordinatorClock()
	rejected := make(chan agent.EventSinkResult, 1)
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
			return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
		}
		purpose := "Inspect a mismatched scope."
		arguments := `{}`
		startedAt := clock.Now()
		invocation := domain.ToolInvocation{
			ID: domain.ToolInvocationID(coordinatorUUID(880)), RunID: input.RunID(), Sequence: 1,
			Name: domain.ToolNameGetResource, Version: "tool-v1", Purpose: &purpose,
			Scope: domain.ScopeSnapshot{
				Context: "other-context", Namespace: input.Scope().Namespace, Generation: input.Scope().Generation,
			},
			ArgumentsJSON: arguments, ArgumentsDigest: domain.SHA256Hex(arguments),
			Status: domain.ToolInvocationStatusRequested, StartedAt: &startedAt,
		}
		result, _ := publisher.Publish(ctx, agent.RunEvent{
			Kind: agent.RunEventToolCallRequested, ToolInvocation: &invocation,
		})
		rejected <- result
		class := domain.SafeErrorClassInternal
		_, _ = publisher.Publish(ctx, agent.RunEvent{
			Kind: agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{
				Class: class, SafeMessage: "The AgentRun failed safely.",
			},
		})
		return agent.RunOutcome{
			Status: domain.AgentRunStatusFailed, ErrorClass: &class,
			SafeMessage: "The AgentRun failed safely.",
		}
	})
	coordinator, persistence, _, ui := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected Pod.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	if <-rejected != agent.EventSinkRejected || persistence.toolCalls() != 0 || result.Status != domain.AgentRunStatusFailed {
		t.Fatalf("scope mismatch result/tool calls = %#v/%d", result, persistence.toolCalls())
	}
	assertOneUITerminal(t, ui.events(), UIEventRunFailed)
}

func TestCoordinatorSurfacesLaterAuditDegradationAndFinishesInMemory(t *testing.T) {
	t.Parallel()
	clock := newCoordinatorClock()
	var externalCalls atomic.Int64
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatalf("NewEventPublisher() error = %v", err)
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
			t.Fatalf("Publish(started) error = %v", err)
		}
		requestID := domain.ModelRequestID(coordinatorUUID(900))
		result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventModelStreamStarted, ModelRequestID: &requestID})
		if err != nil || result != agent.EventSinkDegraded {
			t.Fatalf("Publish(model) = %q/%v", result, err)
		}
		externalCalls.Add(1)
		class := domain.SafeErrorClassUnavailable
		if _, err := publisher.Publish(ctx, agent.RunEvent{
			Kind:    agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{Class: class, SafeMessage: "The model service is unavailable."},
		}); err != nil {
			t.Fatalf("Publish(failed) error = %v", err)
		}
		return agent.RunOutcome{
			Status: domain.AgentRunStatusFailed, ErrorClass: &class,
			SafeMessage: "The model service is unavailable.",
		}
	})
	coordinator, persistence, _, ui := newCoordinatorHarness(t, clock, runner)
	persistence.setAuditFailure(domain.AuditEventModelRequested)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected Pod.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	if externalCalls.Load() != 1 || result.Status != domain.AgentRunStatusFailed || !result.PersistenceDegraded {
		t.Fatalf("degraded result/calls = %#v/%d", result, externalCalls.Load())
	}
	finished := persistence.lastFinished()
	if !finished.PersistenceDegraded || finished.Status != domain.AgentRunStatusFailed {
		t.Fatalf("finished run = %#v", finished)
	}
	events := ui.events()
	degradedIndex, terminalIndex := -1, -1
	for index, event := range events {
		if event.Kind == UIEventPersistenceDegraded {
			degradedIndex = index
		}
		if event.Terminal() {
			terminalIndex = index
		}
	}
	if degradedIndex < 0 || terminalIndex <= degradedIndex {
		t.Fatalf("UI event order = %#v", events)
	}
	assertOneUITerminal(t, events, UIEventRunFailed)
}

func TestCoordinatorDoesNotClaimDurableTerminalStateWhenTerminalAuditFails(t *testing.T) {
	t.Parallel()
	clock := newCoordinatorClock()
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatalf("NewEventPublisher() error = %v", err)
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
			t.Fatalf("Publish(started) error = %v", err)
		}
		class := domain.SafeErrorClassUnavailable
		result, err := publisher.Publish(ctx, agent.RunEvent{
			Kind: agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{
				Class: class, SafeMessage: "The model service is unavailable.",
			},
		})
		if err != nil || result != agent.EventSinkDegraded {
			t.Fatalf("Publish(failed) = %q/%v", result, err)
		}
		return agent.RunOutcome{
			Status: domain.AgentRunStatusFailed, ErrorClass: &class,
			SafeMessage: "The model service is unavailable.",
		}
	})
	coordinator, persistence, _, ui := newCoordinatorHarness(t, clock, runner)
	persistence.setAuditFailure(domain.AuditEventRunFailed)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected Pod.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	if result.Status != domain.AgentRunStatusFailed || !result.PersistenceDegraded || persistence.finishCalls() != 0 {
		t.Fatalf("terminal persistence result/calls = %#v/%d", result, persistence.finishCalls())
	}
	assertOneUITerminal(t, ui.events(), UIEventRunFailed)
}

func TestCoordinatorShutdownCancelsAndWaitsActiveRun(t *testing.T) {
	t.Parallel()
	clock := newCoordinatorClock()
	started := make(chan struct{})
	exited := make(chan struct{})
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		defer close(exited)
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatalf("NewEventPublisher() error = %v", err)
		}
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted})
		close(started)
		<-ctx.Done()
		_, _ = publisher.Publish(context.WithoutCancel(ctx), agent.RunEvent{
			Kind: agent.RunEventRunCancelled, TerminationReason: agent.RunTerminationOwnerCancelled,
		})
		class := domain.SafeErrorClassCancelled
		return agent.RunOutcome{
			Status: domain.AgentRunStatusCancelled, ErrorClass: &class,
			SafeMessage: "The Agent run was cancelled.",
		}
	})
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	_, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Inspect the selected Pod."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	<-started
	if err := coordinator.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Shutdown returned before the active run exited")
	}
	_, err = coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Start after shutdown."})
	if !errors.Is(err, ErrCoordinatorClosed) {
		t.Fatalf("StartRun(after shutdown) error = %v", err)
	}
}

func TestCoordinatorShutdownOwnsConsentReadDuringRunAdmission(t *testing.T) {
	t.Parallel()
	clock := newCoordinatorClock()
	var runnerCalls atomic.Int64
	runner := runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		runnerCalls.Add(1)
		return agent.RunOutcome{}
	})
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	store := newBlockingPrivacyStore()
	privacy, err := NewPrivacyManager(PrivacyManagerConfig{
		Store: store, Origin: "https://model.example", Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	coordinator.privacy = privacy

	startDone := make(chan error, 1)
	go func() {
		_, startErr := coordinator.StartRun(context.Background(), StartRunCommand{
			SessionID: session.ID, Question: "Inspect the selected Pod.",
		})
		startDone <- startErr
	}()
	<-store.entered
	shutdownErr := coordinator.Shutdown(context.Background())
	select {
	case <-store.cancelled:
	default:
		close(store.release)
		t.Fatalf("Shutdown() returned before cancelling the consent read: %v", shutdownErr)
	}
	close(store.release)
	if shutdownErr != nil {
		t.Fatalf("Shutdown() error = %v", shutdownErr)
	}
	if startErr := <-startDone; !errors.Is(startErr, context.Canceled) {
		t.Fatalf("StartRun() error = %v", startErr)
	}
	if runnerCalls.Load() != 0 {
		t.Fatalf("Agent calls before consent = %d", runnerCalls.Load())
	}
}

type runnerFunc func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome

func (function runnerFunc) Run(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
	return function(ctx, input, sink)
}

type coordinatorClock struct {
	mu   sync.Mutex
	next time.Time
}

func newCoordinatorClock() *coordinatorClock {
	return &coordinatorClock{next: time.Date(2026, 8, 10, 1, 0, 0, 0, time.UTC)}
}

func (clock *coordinatorClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	value := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return value
}

type coordinatorIDs struct {
	next atomic.Int64
}

func (source *coordinatorIDs) id() string {
	return coordinatorUUID(int(source.next.Add(1)))
}

func (source *coordinatorIDs) NewSessionID() (domain.SessionID, error) {
	return domain.SessionID(source.id()), nil
}
func (source *coordinatorIDs) NewMessageID() (domain.MessageID, error) {
	return domain.MessageID(source.id()), nil
}
func (source *coordinatorIDs) NewAgentRunID() (domain.AgentRunID, error) {
	return domain.AgentRunID(source.id()), nil
}
func (source *coordinatorIDs) NewAuditEventID() (domain.AuditEventID, error) {
	return domain.AuditEventID(source.id()), nil
}

func coordinatorUUID(value int) string {
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", value)
}

type memoryCoordinatorPersistence struct {
	mu                   sync.Mutex
	beginCount           int
	finishCount          int
	toolCount            int
	beginFailure         bool
	auditFailure         domain.AuditEventType
	finished             []domain.AgentRun
	diagnoses            []domain.Diagnosis
	audits               []domain.AuditEvent
	sessions             map[domain.SessionID]domain.Session
	retentionDays        int
	retentionFound       bool
	retentionLoadFailure bool
	retentionWriteCount  int
	deleteWriteCount     int
	deleteFailure        bool
	deleteReady          <-chan struct{}
	clearHistoryCount    int
	clearHistoryFailure  bool
	clearHistoryReady    <-chan struct{}
}

func (persistence *memoryCoordinatorPersistence) CreateWithAudit(
	_ context.Context,
	session domain.Session,
	audit domain.AuditEvent,
) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if session.Validate() != nil || audit.Validate() != nil {
		return errors.New("invalid session transaction values")
	}
	if audit.Type == persistence.auditFailure {
		return errors.New("generated audit failure")
	}
	if persistence.sessions == nil {
		persistence.sessions = make(map[domain.SessionID]domain.Session)
	}
	persistence.sessions[session.ID] = session
	persistence.audits = append(persistence.audits, audit)
	return nil
}

func (persistence *memoryCoordinatorPersistence) LoadOperationalDetailRetention(context.Context) (int, bool, error) {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if persistence.retentionLoadFailure {
		return 0, false, errors.New("generated retention read failure")
	}
	return persistence.retentionDays, persistence.retentionFound, nil
}

func (persistence *memoryCoordinatorPersistence) TightenOperationalDetailRetention(
	_ context.Context,
	update RetentionSettingUpdate,
) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	current := DefaultOperationalDetailRetentionDays
	if persistence.retentionFound {
		current = persistence.retentionDays
	}
	if update.Validate() != nil || update.Days > current {
		return ErrRetentionWouldWiden
	}
	if update.ExpectedDays != current {
		return ErrRetentionSettingConflict
	}
	persistence.retentionDays = update.Days
	persistence.retentionFound = true
	persistence.retentionWriteCount++
	return nil
}

func (persistence *memoryCoordinatorPersistence) DeleteSessionGraph(_ context.Context, id domain.SessionID) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	persistence.deleteWriteCount++
	if persistence.deleteReady != nil {
		select {
		case <-persistence.deleteReady:
		default:
			return errors.New("generated delete before prerequisite termination")
		}
	}
	if persistence.deleteFailure {
		return errors.New("generated delete failure")
	}
	if _, ok := persistence.sessions[id]; !ok {
		return errors.New("generated missing Session")
	}
	delete(persistence.sessions, id)
	return nil
}

func (persistence *memoryCoordinatorPersistence) ClearHistory(_ context.Context) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	persistence.clearHistoryCount++
	if persistence.clearHistoryReady != nil {
		select {
		case <-persistence.clearHistoryReady:
		default:
			return errors.New("generated clear-history before prerequisite termination")
		}
	}
	if persistence.clearHistoryFailure {
		return errors.New("generated clear-history failure")
	}
	persistence.sessions = make(map[domain.SessionID]domain.Session)
	persistence.audits = nil
	return nil
}

func (persistence *memoryCoordinatorPersistence) retentionWrites() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.retentionWriteCount
}

func (persistence *memoryCoordinatorPersistence) deleteWrites() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.deleteWriteCount
}

func (persistence *memoryCoordinatorPersistence) setDeleteFailure(value bool) {
	persistence.mu.Lock()
	persistence.deleteFailure = value
	persistence.mu.Unlock()
}

func (persistence *memoryCoordinatorPersistence) setDeleteReady(ready <-chan struct{}) {
	persistence.mu.Lock()
	persistence.deleteReady = ready
	persistence.mu.Unlock()
}

func (persistence *memoryCoordinatorPersistence) sessionMode(id domain.SessionID) domain.PrivacyMode {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.sessions[id].PrivacyMode
}

func (persistence *memoryCoordinatorPersistence) BeginWithAudit(
	_ context.Context,
	message domain.Message,
	run domain.AgentRun,
	audit domain.AuditEvent,
) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	persistence.beginCount++
	if persistence.beginFailure || audit.Type == persistence.auditFailure {
		return errors.New("generated persistence failure")
	}
	if message.Validate() != nil || run.Validate() != nil || audit.Validate() != nil {
		return errors.New("invalid begin values")
	}
	persistence.audits = append(persistence.audits, audit)
	return nil
}

func (persistence *memoryCoordinatorPersistence) FinishWithAudit(
	_ context.Context,
	run domain.AgentRun,
	audit domain.AuditEvent,
) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if run.Validate() != nil || !run.Status.Terminal() || audit.Validate() != nil {
		return errors.New("invalid terminal run")
	}
	if audit.Type == persistence.auditFailure {
		return errors.New("generated audit failure")
	}
	persistence.finishCount++
	persistence.finished = append(persistence.finished, run)
	persistence.audits = append(persistence.audits, audit)
	return nil
}

func (persistence *memoryCoordinatorPersistence) CompleteWithAudit(
	ctx context.Context,
	diagnosis domain.Diagnosis,
	message domain.Message,
	run domain.AgentRun,
	audit domain.AuditEvent,
) error {
	if diagnosis.Validate() != nil || message.Validate() != nil {
		return errors.New("invalid completion transaction values")
	}
	persistence.mu.Lock()
	persistence.diagnoses = append(persistence.diagnoses, cloneDiagnosis(diagnosis))
	persistence.mu.Unlock()
	return persistence.FinishWithAudit(ctx, run, audit)
}

func (persistence *memoryCoordinatorPersistence) Append(_ context.Context, event domain.AuditEvent) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if event.Validate() != nil {
		return errors.New("invalid audit event")
	}
	if event.Type == persistence.auditFailure {
		return errors.New("generated audit failure")
	}
	persistence.audits = append(persistence.audits, event)
	return nil
}

func (persistence *memoryCoordinatorPersistence) setBeginFailure(value bool) {
	persistence.mu.Lock()
	persistence.beginFailure = value
	persistence.mu.Unlock()
}

func (persistence *memoryCoordinatorPersistence) setAuditFailure(eventType domain.AuditEventType) {
	persistence.mu.Lock()
	persistence.auditFailure = eventType
	persistence.mu.Unlock()
}

func (persistence *memoryCoordinatorPersistence) beginCalls() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.beginCount
}

func (persistence *memoryCoordinatorPersistence) finishCalls() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.finishCount
}

func (persistence *memoryCoordinatorPersistence) lastFinished() domain.AgentRun {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if len(persistence.finished) == 0 {
		return domain.AgentRun{}
	}
	return persistence.finished[len(persistence.finished)-1]
}

func (persistence *memoryCoordinatorPersistence) lastDiagnosis() domain.Diagnosis {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if len(persistence.diagnoses) == 0 {
		return domain.Diagnosis{}
	}
	return cloneDiagnosis(persistence.diagnoses[len(persistence.diagnoses)-1])
}

func (persistence *memoryCoordinatorPersistence) diagnosisCalls() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return len(persistence.diagnoses)
}

func (persistence *memoryCoordinatorPersistence) SaveWithAudit(
	_ context.Context,
	invocation domain.ToolInvocation,
	evidence []domain.Evidence,
	audit domain.AuditEvent,
) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if invocation.Validate() != nil || audit.Validate() != nil || len(evidence) != invocation.EvidenceCount {
		return errors.New("invalid Tool transaction values")
	}
	if audit.Type == persistence.auditFailure {
		return errors.New("generated audit failure")
	}
	persistence.toolCount++
	persistence.audits = append(persistence.audits, audit)
	return nil
}

func (persistence *memoryCoordinatorPersistence) toolCalls() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.toolCount
}

func (persistence *memoryCoordinatorPersistence) auditCalls(eventType domain.AuditEventType) int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	count := 0
	for _, event := range persistence.audits {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

type coordinatorScope struct {
	mu     sync.Mutex
	scope  domain.ClusterScope
	runID  domain.AgentRunID
	cancel context.CancelFunc
}

func (scope *coordinatorScope) CurrentScope() (domain.ClusterScope, bool) {
	return scope.scope, true
}

func (scope *coordinatorScope) BindRun(value domain.ClusterScope, runID domain.AgentRunID, cancel context.CancelFunc) error {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if value != scope.scope || scope.cancel != nil {
		return errors.New("scope unavailable")
	}
	scope.runID, scope.cancel = runID, cancel
	return nil
}

func (scope *coordinatorScope) UnbindRun(runID domain.AgentRunID) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if scope.runID == runID {
		scope.runID, scope.cancel = "", nil
	}
}

func (scope *coordinatorScope) bound() bool {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	return scope.cancel != nil
}

type recordingUIEvents struct {
	mu     sync.Mutex
	values []UIEvent
}

func (sink *recordingUIEvents) PublishUIEvent(_ context.Context, event UIEvent) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if event.Validate() != nil {
		return ErrInvalidUIEvent
	}
	sink.values = append(sink.values, event)
	return nil
}

func (sink *recordingUIEvents) events() []UIEvent {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return append([]UIEvent(nil), sink.values...)
}

func newCoordinatorHarness(
	t *testing.T,
	clock *coordinatorClock,
	runner agent.AgentRunner,
) (*Coordinator, *memoryCoordinatorPersistence, *coordinatorScope, *recordingUIEvents) {
	t.Helper()
	persistence := new(memoryCoordinatorPersistence)
	scope := &coordinatorScope{scope: domain.ClusterScope{
		Context: "test-context", Namespace: "team-a", Generation: 7,
		ActivatedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
	}}
	ui := new(recordingUIEvents)
	identifiers := new(coordinatorIDs)
	coordinator, err := NewCoordinator(CoordinatorConfig{
		Sessions: persistence, Runs: persistence, Tools: persistence,
		Audits: persistence,
		Scope:  scope, Runner: runner, Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: security.NewRedactor(), Privacy: newAcceptedCoordinatorPrivacy(t), UIEvents: ui,
		Observer: RunObserverFunc(func(context.Context, RunObservation) {}), Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	return coordinator, persistence, scope, ui
}

type coordinatorPrivacyStore struct {
	mu     sync.Mutex
	record PrivacyRecord
	found  bool
}

type blockingPrivacyStore struct {
	entered   chan struct{}
	release   chan struct{}
	cancelled chan struct{}
}

func newBlockingPrivacyStore() *blockingPrivacyStore {
	return &blockingPrivacyStore{
		entered: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{}),
	}
}

func (store *blockingPrivacyStore) LoadPrivacy(ctx context.Context) (PrivacyRecord, bool, error) {
	close(store.entered)
	select {
	case <-ctx.Done():
		close(store.cancelled)
		return PrivacyRecord{}, false, ctx.Err()
	case <-store.release:
		return PrivacyRecord{}, false, nil
	}
}

func (*blockingPrivacyStore) SavePrivacy(context.Context, PrivacyRecord) error { return nil }

func (store *coordinatorPrivacyStore) LoadPrivacy(context.Context) (PrivacyRecord, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return clonePrivacyRecord(store.record), store.found, nil
}

func (store *coordinatorPrivacyStore) SavePrivacy(_ context.Context, record PrivacyRecord) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.record = clonePrivacyRecord(record)
	store.found = true
	return nil
}

func newAcceptedCoordinatorPrivacy(t *testing.T) *PrivacyManager {
	t.Helper()
	next := int64(1_000)
	now := func() time.Time {
		next++
		return time.UnixMilli(next).UTC()
	}
	manager, err := NewPrivacyManager(PrivacyManagerConfig{
		Store: new(coordinatorPrivacyStore), Origin: "https://model.example", Now: now,
	})
	if err != nil {
		t.Fatalf("NewPrivacyManager() error = %v", err)
	}
	review, err := manager.Review(context.Background())
	if err != nil {
		t.Fatalf("Privacy Review() error = %v", err)
	}
	if _, err := manager.Decide(context.Background(), PrivacyActionAccept, review.Revision, nil); err != nil {
		t.Fatalf("Privacy Decide() error = %v", err)
	}
	return manager
}

func createCoordinatorSession(t *testing.T, coordinator *Coordinator) domain.Session {
	t.Helper()
	session, err := coordinator.CreateSession(context.Background(), CreateSessionCommand{PrivacyMode: domain.PrivacyModeStandard})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	return session
}

func assertOneUITerminal(t *testing.T, events []UIEvent, kind UIEventKind) {
	t.Helper()
	terminals := 0
	for index, event := range events {
		if event.Sequence != int64(index+1) {
			t.Fatalf("UI event[%d] sequence = %d", index, event.Sequence)
		}
		if event.Terminal() {
			terminals++
			if event.Kind != kind {
				t.Fatalf("terminal kind = %q, want %q", event.Kind, kind)
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal count = %d, want 1", terminals)
	}
}
