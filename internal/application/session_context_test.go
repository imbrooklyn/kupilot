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
)

func TestCoordinatorCarriesOnlyCompletedTurnsIntoLaterAgentRuns(t *testing.T) {
	t.Parallel()

	clock := newCoordinatorClock()
	runner := &conversationRecordingRunner{clock: clock, failCall: 1}
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)

	questions := []string{"What happened first?", "This run will fail.", "What remains in context?"}
	for index, question := range questions {
		runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: question})
		if err != nil {
			t.Fatalf("StartRun(%d) error = %v", index, err)
		}
		result, err := coordinator.WaitRun(context.Background(), runID)
		if err != nil {
			t.Fatalf("WaitRun(%d) error = %v", index, err)
		}
		want := domain.AgentRunStatusCompleted
		if index == 1 {
			want = domain.AgentRunStatusFailed
		}
		if result.Status != want {
			t.Fatalf("run %d status = %q, want %q", index, result.Status, want)
		}
	}

	inputs := runner.Inputs()
	if len(inputs) != 3 {
		t.Fatalf("captured inputs = %d, want 3", len(inputs))
	}
	if turns := inputs[0].Conversation().Turns(); len(turns) != 0 {
		t.Fatalf("first-run history = %#v", turns)
	}
	firstTurns := inputs[1].Conversation().Turns()
	assertConversationContents(t, firstTurns, []string{questions[0], "Final answer 1."})
	thirdTurns := inputs[2].Conversation().Turns()
	assertConversationContents(t, thirdTurns, []string{questions[0], "Final answer 1."})
	for _, turn := range thirdTurns {
		if turn.Content == questions[1] {
			t.Fatal("failed-run question entered later model context")
		}
	}
}

func TestCoordinatorLoadsEveryEligiblePageForFreshStandardContext(t *testing.T) {
	t.Parallel()

	clock := newCoordinatorClock()
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		t.Fatal("context selection called the Agent runner")
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	const messageCount = 202
	messages := make([]domain.Message, messageCount)
	base := time.Date(2026, 8, 10, 5, 0, 0, 0, time.UTC)
	for index := range messages {
		runID := domain.AgentRunID(coordinatorUUID(20_000 + index/2))
		role := domain.MessageRoleUser
		format := domain.MessageFormatPlain
		content := fmt.Sprintf("Prior question %03d", index/2)
		if index%2 == 1 {
			role = domain.MessageRoleAssistant
			format = domain.MessageFormatMarkdown
			content = fmt.Sprintf("Prior final answer %03d", index/2)
		}
		messages[index] = domain.Message{
			ID: domain.MessageID(coordinatorUUID(30_000 + index)), SessionID: session.ID, RunID: &runID,
			RunSequence: intPointer(index % 2),
			Role:        role, Content: content, Format: format, Status: domain.MessageStatusCommitted,
			Hash: domain.MessageContentHash(content), CreatedAt: base.Add(time.Duration(index) * time.Millisecond),
		}
		if messages[index].Validate() != nil {
			t.Fatalf("fixture Message %d is invalid", index)
		}
	}
	persistence.mu.Lock()
	persistence.contextMessages = map[domain.SessionID][]domain.Message{session.ID: messages}
	persistence.mu.Unlock()
	coordinator.mu.Lock()
	coordinator.modelContext = newSessionModelContext(session, false)
	coordinator.mu.Unlock()

	conversation, err := coordinator.conversationForRun(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("conversationForRun() error = %v", err)
	}
	turns := conversation.Turns()
	if len(turns) != messageCount || turns[0].Content != "Prior question 000" ||
		turns[99].Content != "Prior final answer 049" || turns[len(turns)-1].Content != "Prior final answer 100" {
		t.Fatalf("selected paged context = %d turns, first=%q middle=%q last=%q",
			len(turns), turns[0].Content, turns[99].Content, turns[len(turns)-1].Content)
	}
}

func TestCoordinatorLoadsStoredSummaryAndExactTailAndRejectsCorruptCoverage(t *testing.T) {
	t.Parallel()

	clock := newCoordinatorClock()
	coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		t.Fatal("context replay called the Agent runner")
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	seedCoordinatorContext(t, coordinator, persistence, session, 6)
	full, err := coordinator.conversationForRun(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("conversationForRun(full) error = %v", err)
	}
	input, err := agent.NewRunInputWithContext(
		domain.AgentRunID(coordinatorUUID(50_001)), session.ID, domain.MessageID(coordinatorUUID(50_002)),
		"Replay the safe summary.", scope.scope, nil, agent.DefaultRunBudgetLimits(), full,
	)
	if err != nil {
		t.Fatalf("NewRunInputWithContext() error = %v", err)
	}
	summary, err := summaryForRunInput(input, clock.Now(), 4)
	if err != nil {
		t.Fatalf("summaryForRunInput() error = %v", err)
	}
	persistence.mu.Lock()
	if persistence.contextSummaries == nil {
		persistence.contextSummaries = make(map[domain.SessionID]domain.SessionContextSummary)
	}
	persistence.contextSummaries[session.ID] = summary
	persistence.mu.Unlock()

	for restart := 0; restart < 2; restart++ {
		coordinator.mu.Lock()
		coordinator.modelContext = newSessionModelContext(session, false)
		coordinator.mu.Unlock()
		replayed, replayErr := coordinator.conversationForRun(context.Background(), session.ID)
		if replayErr != nil {
			t.Fatalf("conversationForRun(restart %d) error = %v", restart, replayErr)
		}
		if got := replayed.Summary(); got == nil || *got != summary {
			t.Fatalf("replayed summary on restart %d = %#v", restart, got)
		}
		assertConversationContents(t, replayed.Turns(), []string{"Stored user question 002", "Stored final answer 002"})
	}
	if persistence.summaryWrites() != 0 {
		t.Fatalf("summary replay wrote %d replacement summaries", persistence.summaryWrites())
	}

	corrupt := summary
	corrupt.CoverageDigest = domain.SHA256Hex("different ordered prefix")
	persistence.mu.Lock()
	persistence.contextSummaries[session.ID] = corrupt
	persistence.mu.Unlock()
	coordinator.mu.Lock()
	coordinator.modelContext = newSessionModelContext(session, false)
	coordinator.mu.Unlock()
	if _, err = coordinator.conversationForRun(context.Background(), session.ID); !errors.Is(err, ErrModelContextUnavailable) {
		t.Fatalf("conversationForRun(corrupt coverage) error = %v, want %v", err, ErrModelContextUnavailable)
	}
}

func TestCoordinatorModelContextReadFailurePreventsAgentCall(t *testing.T) {
	t.Parallel()

	clock := newCoordinatorClock()
	var calls atomic.Int64
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		calls.Add(1)
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	coordinator.mu.Lock()
	coordinator.modelContext = newSessionModelContext(session, false)
	coordinator.mu.Unlock()
	persistence.mu.Lock()
	persistence.contextReadFailure = true
	persistence.mu.Unlock()

	if _, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Do not call the model."}); err != ErrPersistenceUnavailable {
		t.Fatalf("StartRun() error = %v, want %v", err, ErrPersistenceUnavailable)
	}
	if calls.Load() != 0 || persistence.beginCalls() != 0 {
		t.Fatalf("context failure calls = Agent %d, durable begin %d", calls.Load(), persistence.beginCalls())
	}
}

func TestMinimalSessionContextStaysInProcessAndOutOfPersistence(t *testing.T) {
	t.Parallel()

	clock := newCoordinatorClock()
	runner := &conversationRecordingRunner{clock: clock, failCall: -1}
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session, err := coordinator.CreateSession(context.Background(), CreateSessionCommand{PrivacyMode: domain.PrivacyModeMinimal})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	for _, question := range []string{"Minimal first question.", "Minimal second question."} {
		runID, startErr := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: question})
		if startErr != nil {
			t.Fatalf("StartRun() error = %v", startErr)
		}
		if _, waitErr := coordinator.WaitRun(context.Background(), runID); waitErr != nil {
			t.Fatalf("WaitRun() error = %v", waitErr)
		}
	}
	inputs := runner.Inputs()
	assertConversationContents(t, inputs[1].Conversation().Turns(), []string{"Minimal first question.", "Final answer 1."})
	persistence.mu.Lock()
	durableMessages := len(persistence.contextMessages[session.ID])
	persistence.mu.Unlock()
	if durableMessages != 0 {
		t.Fatalf("minimal context persisted %d Messages", durableMessages)
	}

	fresh := newSessionModelContext(session, true)
	conversation, err := fresh.conversation()
	if err != nil || len(conversation.Turns()) != 0 {
		t.Fatalf("fresh-process minimal context = %#v/%v", conversation.Turns(), err)
	}
}

func TestCoordinatorSummaryStorageFailureRejectsBeforeMainModelWork(t *testing.T) {
	t.Parallel()

	clock := newCoordinatorClock()
	summaryResults := make(chan agent.EventSinkResult, 1)
	var mainModelCalls atomic.Int64
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
		}
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted})
		requestID := domain.ModelRequestID(coordinatorUUID(51_001))
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventSummaryStarted, ModelRequestID: &requestID})
		summary, summaryErr := summaryForRunInput(input, clock.Now(), 2)
		if summaryErr != nil {
			return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
		}
		result, _ := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventSummaryReady, Summary: &summary})
		summaryResults <- result
		if result == agent.EventSinkAccepted || result == agent.EventSinkDegraded {
			mainModelCalls.Add(1)
		}
		class := domain.SafeErrorClassPersistenceUnavailable
		_, _ = publisher.Publish(context.WithoutCancel(ctx), agent.RunEvent{Kind: agent.RunEventRunFailed, Failure: &agent.RunEventFailure{
			Class: class, SafeMessage: "The conversation summary could not be committed safely.",
		}})
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed, ErrorClass: &class, SafeMessage: "The conversation summary could not be committed safely."}
	})
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	seedCoordinatorContext(t, coordinator, persistence, session, 2)
	persistence.mu.Lock()
	persistence.contextWriteFailure = true
	persistence.mu.Unlock()

	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Use safe context."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	if summaryResult := <-summaryResults; summaryResult != agent.EventSinkRejected {
		t.Fatalf("summary sink result = %q", summaryResult)
	}
	if result.Status != domain.AgentRunStatusFailed || !result.PersistenceDegraded ||
		mainModelCalls.Load() != 0 || persistence.summaryWrites() != 1 {
		t.Fatalf("run/storage/main calls = %#v/%d/%d", result, persistence.summaryWrites(), mainModelCalls.Load())
	}
	persistence.mu.Lock()
	_, stored := persistence.contextSummaries[session.ID]
	persistence.mu.Unlock()
	if stored {
		t.Fatal("failed summary storage changed the last committed summary")
	}
}

func TestCoordinatorRejectsSummaryReturnedAfterScopeGenerationChanged(t *testing.T) {
	t.Parallel()

	clock := newCoordinatorClock()
	summaryStarted := make(chan struct{})
	continueRun := make(chan struct{})
	summaryResults := make(chan agent.EventSinkResult, 1)
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
		}
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted})
		requestID := domain.ModelRequestID(coordinatorUUID(52_001))
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventSummaryStarted, ModelRequestID: &requestID})
		close(summaryStarted)
		<-continueRun
		summary, summaryErr := summaryForRunInput(input, clock.Now(), 2)
		if summaryErr != nil {
			return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
		}
		result, _ := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventSummaryReady, Summary: &summary})
		summaryResults <- result
		class := domain.SafeErrorClassStaleScope
		_, _ = publisher.Publish(context.WithoutCancel(ctx), agent.RunEvent{
			Kind: agent.RunEventRunStaleScope, TerminationReason: agent.RunTerminationScopeChanged,
		})
		return agent.RunOutcome{
			Status: domain.AgentRunStatusStaleScope, ErrorClass: &class,
			SafeMessage: "The diagnostic run stopped because the Kubernetes scope changed.",
		}
	})
	coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	seedCoordinatorContext(t, coordinator, persistence, session, 2)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Do not accept stale context."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	<-summaryStarted
	scope.mu.Lock()
	scope.scope.Generation++
	scope.mu.Unlock()
	close(continueRun)
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	if summaryResult := <-summaryResults; summaryResult != agent.EventSinkRejected {
		t.Fatalf("stale summary sink result = %q", summaryResult)
	}
	if result.Status != domain.AgentRunStatusFailed || result.PersistenceDegraded || persistence.summaryWrites() != 0 {
		t.Fatalf("stale run/writes = %#v/%d", result, persistence.summaryWrites())
	}
}

func seedCoordinatorContext(
	t *testing.T,
	coordinator *Coordinator,
	persistence *memoryCoordinatorPersistence,
	session domain.Session,
	messageCount int,
) {
	t.Helper()
	if messageCount < 2 || messageCount%2 != 0 {
		t.Fatal("context fixture requires complete pairs")
	}
	messages := make([]domain.Message, messageCount)
	base := time.Date(2026, 8, 10, 5, 30, 0, 0, time.UTC)
	for index := range messages {
		runID := domain.AgentRunID(coordinatorUUID(53_000 + index/2))
		role, format := domain.MessageRoleUser, domain.MessageFormatPlain
		content := fmt.Sprintf("Stored user question %03d", index/2)
		if index%2 == 1 {
			role, format = domain.MessageRoleAssistant, domain.MessageFormatMarkdown
			content = fmt.Sprintf("Stored final answer %03d", index/2)
		}
		messages[index] = domain.Message{
			ID: domain.MessageID(coordinatorUUID(54_000 + index)), SessionID: session.ID, RunID: &runID,
			RunSequence: intPointer(index % 2),
			Role:        role, Content: content, Format: format, Status: domain.MessageStatusCommitted,
			Hash: domain.MessageContentHash(content), CreatedAt: base.Add(time.Duration(index) * time.Millisecond),
		}
		if messages[index].Validate() != nil {
			t.Fatalf("context fixture Message %d is invalid", index)
		}
	}
	persistence.mu.Lock()
	if persistence.contextMessages == nil {
		persistence.contextMessages = make(map[domain.SessionID][]domain.Message)
	}
	persistence.contextMessages[session.ID] = messages
	persistence.mu.Unlock()
	coordinator.mu.Lock()
	coordinator.modelContext = newSessionModelContext(session, false)
	coordinator.mu.Unlock()
}

func summaryForRunInput(input agent.RunInput, generatedAt time.Time, coveredCount int) (domain.SessionContextSummary, error) {
	coverage := input.Conversation().Coverage()
	if coveredCount < 2 || coveredCount > len(coverage) {
		return domain.SessionContextSummary{}, errors.New("invalid summary coverage")
	}
	digest, coveredBytes, err := domain.SessionContextCoverageDigestItems(coverage[:coveredCount])
	if err != nil {
		return domain.SessionContextSummary{}, err
	}
	text := "Earlier completed Session turns were summarized without restoring authority."
	return domain.SessionContextSummary{
		SessionID: input.SessionID(), Text: text, SummaryHash: domain.SHA256Hex(text),
		SchemaVersion: domain.SessionContextSummarySchemaVersion, PolicyVersion: domain.SafeConversationContextPolicyVersion,
		CoveredFirstID: coverage[0].MessageID, CoveredThroughID: coverage[coveredCount-1].MessageID,
		CoveredCount: coveredCount, CoveredBytes: coveredBytes, CoverageDigest: digest, GeneratedAt: generatedAt,
		AgentProfile: string(domain.ModelRoleAgent), AgentOriginHash: domain.SHA256Hex("https://model.example"),
	}, nil
}

type conversationRecordingRunner struct {
	mu       sync.Mutex
	clock    *coordinatorClock
	inputs   []agent.RunInput
	failCall int
}

func (runner *conversationRecordingRunner) Run(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
	runner.mu.Lock()
	call := len(runner.inputs)
	runner.inputs = append(runner.inputs, input)
	runner.mu.Unlock()
	publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, runner.clock.Now, sink)
	if err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	if _, err = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	if call == runner.failCall {
		class := domain.SafeErrorClassUnavailable
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunFailed, Failure: &agent.RunEventFailure{
			Class: class, SafeMessage: "The scripted run failed safely.",
		}})
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed, ErrorClass: &class, SafeMessage: "The scripted run failed safely."}
	}
	answer := fmt.Sprintf("Final answer %d.", call+1)
	diagnosis := domain.Diagnosis{
		ID: domain.DiagnosisID(coordinatorUUID(40_000 + call)), RunID: input.RunID(), Scope: input.Scope().Snapshot(),
		AnswerMarkdown: answer, CreatedAt: runner.clock.Now(),
	}
	if diagnosis.Validate() != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	if _, err = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventDiagnosisReady, Diagnosis: &diagnosis}); err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	if _, err = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunCompleted}); err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	return agent.RunOutcome{Status: domain.AgentRunStatusCompleted, Diagnosis: &diagnosis}
}

func (runner *conversationRecordingRunner) Inputs() []agent.RunInput {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return append([]agent.RunInput(nil), runner.inputs...)
}

func assertConversationContents(t *testing.T, turns []agent.ConversationTurn, want []string) {
	t.Helper()
	if len(turns) != len(want) {
		t.Fatalf("conversation turn count = %d, want %d: %#v", len(turns), len(want), turns)
	}
	for index := range want {
		if turns[index].Content != want[index] {
			t.Fatalf("conversation turn %d = %q, want %q", index, turns[index].Content, want[index])
		}
		wantRole := domain.MessageRoleUser
		if index%2 == 1 {
			wantRole = domain.MessageRoleAssistant
		}
		if turns[index].Role != wantRole {
			t.Fatalf("conversation role %d = %q, want %q", index, turns[index].Role, wantRole)
		}
	}
}
