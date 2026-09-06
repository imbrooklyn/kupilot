package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type controlledConversationOutcome uint8

const (
	controlledConversationComplete controlledConversationOutcome = iota + 1
	controlledConversationFail
	controlledConversationTimeout
)

type controlledConversationRunner struct {
	clock    *coordinatorClock
	inputs   chan agent.RunInput
	outcomes chan controlledConversationOutcome
	calls    atomic.Int64
	answers  atomic.Int64
	profile  atomic.Value
}

func (*controlledConversationRunner) Close()            {}
func (*controlledConversationRunner) ModelName() string { return "scripted-model" }
func (*controlledConversationRunner) Origin() string    { return "https://model.example" }
func (runner *controlledConversationRunner) ProfileName() string {
	value, _ := runner.profile.Load().(string)
	return value
}

func newControlledConversationRunner(clock *coordinatorClock) *controlledConversationRunner {
	runner := &controlledConversationRunner{
		clock: clock, inputs: make(chan agent.RunInput, 8), outcomes: make(chan controlledConversationOutcome, 8),
	}
	runner.profile.Store("agent")
	return runner
}

func (runner *controlledConversationRunner) Run(
	ctx context.Context,
	input agent.RunInput,
	sink agent.EventSink,
) agent.RunOutcome {
	runner.calls.Add(1)
	publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, runner.clock.Now, sink)
	if err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	if _, err = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
	}
	select {
	case runner.inputs <- input:
	case <-ctx.Done():
		return controlledCancelledOutcome(ctx, publisher)
	}
	var outcome controlledConversationOutcome
	select {
	case outcome = <-runner.outcomes:
	case <-ctx.Done():
		return controlledCancelledOutcome(ctx, publisher)
	}
	if outcome == controlledConversationFail {
		class := domain.SafeErrorClassUnavailable
		failure := agent.RunEventFailure{Class: class, SafeMessage: "The scripted run failed safely."}
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunFailed, Failure: &failure})
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed, ErrorClass: &class, SafeMessage: failure.SafeMessage}
	}
	if outcome == controlledConversationTimeout {
		class := domain.SafeErrorClassTimeout
		_, _ = publisher.Publish(ctx, agent.RunEvent{
			Kind: agent.RunEventRunTimedOut, TerminationReason: agent.RunTerminationDeadline,
		})
		return agent.RunOutcome{
			Status: domain.AgentRunStatusTimedOut, ErrorClass: &class,
			SafeMessage: "The scripted run reached its time limit.",
		}
	}
	answerNumber := runner.answers.Add(1)
	answer := "Scripted final answer."
	diagnosis := domain.Diagnosis{
		ID:    domain.DiagnosisID(coordinatorUUID(70_000 + int(answerNumber))),
		RunID: input.RunID(), Scope: input.Scope().Snapshot(), AnswerMarkdown: answer, CreatedAt: runner.clock.Now(),
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

func controlledCancelledOutcome(ctx context.Context, publisher *agent.EventPublisher) agent.RunOutcome {
	class := domain.SafeErrorClassCancelled
	_, _ = publisher.Publish(context.WithoutCancel(ctx), agent.RunEvent{
		Kind: agent.RunEventRunCancelled, TerminationReason: agent.RunTerminationUserCancelled,
	})
	return agent.RunOutcome{
		Status: domain.AgentRunStatusCancelled, ErrorClass: &class,
		SafeMessage: "The scripted run was cancelled.",
	}
}

func waitConversationRunInput(t *testing.T, runner *controlledConversationRunner) agent.RunInput {
	t.Helper()
	select {
	case input := <-runner.inputs:
		return input
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the scripted run")
		return agent.RunInput{}
	}
}

func conversationInputCommand(input agent.RunInput, text string) SubmitSteerCommand {
	return SubmitSteerCommand{
		RunID: input.RunID(), SessionID: input.SessionID(),
		ExpectedScopeGeneration:  input.Scope().Generation,
		ExpectedPolicyGeneration: input.PolicyGeneration(), Text: text,
	}
}

func conversationBoundary(input agent.RunInput) agent.SteerBoundary {
	return agent.SteerBoundary{
		RunID: input.RunID(), SessionID: input.SessionID(),
		ScopeGeneration: input.Scope().Generation, PolicyGeneration: input.PolicyGeneration(),
	}
}

type runInputPersistenceFunc func(context.Context, domain.Message) error

func (function runInputPersistenceFunc) AppendRunInput(ctx context.Context, message domain.Message) error {
	return function(ctx, message)
}

func TestConversationSteerCommitsOnceBeforeCompletedContext(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, persistence, _, ui := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected workload.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	accepted, err := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Also inspect recent Events."))
	if err != nil || accepted.State != ConversationInputPending {
		t.Fatalf("SubmitSteer() = %#v, %v", accepted, err)
	}
	bridge := input.Steering()
	if bridge == nil {
		t.Fatal("active RunInput did not carry the narrow steering bridge")
	}
	claim, found, err := bridge.ClaimSteer(context.Background(), conversationBoundary(input))
	if err != nil || !found || claim.Content != "Also inspect recent Events." || claim.RunSequence != 1 {
		t.Fatalf("ClaimSteer() = %#v, %v, %v", claim, found, err)
	}
	if err = bridge.CommitSteer(context.Background(), claim); err != nil {
		t.Fatalf("CommitSteer() error = %v", err)
	}
	if _, found, err = bridge.ClaimSteer(context.Background(), conversationBoundary(input)); err != nil || found {
		t.Fatalf("duplicate ClaimSteer() found/error = %v/%v", found, err)
	}
	if persistence.runInputCalls() != 1 {
		t.Fatalf("committed run-input writes = %d, want 1", persistence.runInputCalls())
	}

	runner.outcomes <- controlledConversationComplete
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil || result.Status != domain.AgentRunStatusCompleted || result.PersistenceDegraded {
		t.Fatalf("WaitRun() = %#v, %v", result, err)
	}
	persistence.mu.Lock()
	messages := append([]domain.Message(nil), persistence.contextMessages[session.ID]...)
	persistence.mu.Unlock()
	if len(messages) != 3 {
		t.Fatalf("completed context messages = %#v", messages)
	}
	wantRoles := []domain.MessageRole{domain.MessageRoleUser, domain.MessageRoleUser, domain.MessageRoleAssistant}
	wantSequences := []int{0, 1, 2}
	for index := range messages {
		if messages[index].Role != wantRoles[index] || messages[index].RunID == nil || *messages[index].RunID != runID ||
			messages[index].RunSequence == nil || *messages[index].RunSequence != wantSequences[index] {
			t.Fatalf("message %d = %#v", index, messages[index])
		}
	}
	if messages[1].Content != claim.Content {
		t.Fatalf("committed steer content = %q", messages[1].Content)
	}

	states := make([]ConversationInputState, 0, 3)
	for _, event := range ui.events() {
		if event.Kind == UIEventConversationInput && event.ConversationInput.Changed != nil &&
			event.ConversationInput.Changed.ItemID == accepted.ItemID {
			states = append(states, event.ConversationInput.Changed.State)
		}
	}
	wantStates := []ConversationInputState{ConversationInputPending, ConversationInputCommitting, ConversationInputCommitted}
	if len(states) < len(wantStates) {
		t.Fatalf("steer lifecycle states = %#v", states)
	}
	for index := range wantStates {
		if states[index] != wantStates[index] {
			t.Fatalf("steer lifecycle states = %#v", states)
		}
	}
}

func TestConversationSteerPersistenceFailureRecoversAndDegradesRun(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected workload.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	accepted, err := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Inspect one more bounded signal."))
	if err != nil {
		t.Fatalf("SubmitSteer() error = %v", err)
	}
	claim, found, err := input.Steering().ClaimSteer(context.Background(), conversationBoundary(input))
	if err != nil || !found {
		t.Fatalf("ClaimSteer() = %#v, %v, %v", claim, found, err)
	}
	persistence.setRunInputFailure(true)
	if err = input.Steering().CommitSteer(context.Background(), claim); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("CommitSteer() error = %v", err)
	}
	coordinator.mu.Lock()
	item := coordinator.conversationInputs.items[accepted.ItemID]
	state := ConversationInputState("")
	if item != nil {
		state = item.state
	}
	coordinator.mu.Unlock()
	if state != ConversationInputRecovered {
		t.Fatalf("failed commit state = %q", state)
	}
	runner.outcomes <- controlledConversationComplete
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil || result.Status != domain.AgentRunStatusCompleted || !result.PersistenceDegraded {
		t.Fatalf("degraded WaitRun() = %#v, %v", result, err)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("persistence failure started %d runs, want 1", runner.calls.Load())
	}
}

func TestConversationSteerSinkBarriersKeepKnownStatesAndMakeZeroModelCalls(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, persistence, _, ui := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected workload.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)

	ui.setConversationRejection(func(event UIEvent) bool {
		return event.Kind == UIEventConversationInput && event.ConversationInput.Changed != nil &&
			event.ConversationInput.Changed.State == ConversationInputPending
	})
	accepted, err := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Preserve this accepted input."))
	if err != nil || accepted.State != ConversationInputPending {
		t.Fatalf("SubmitSteer(sink rejection) = %#v, %v", accepted, err)
	}
	ui.setConversationRejection(nil)
	claim, found, err := input.Steering().ClaimSteer(context.Background(), conversationBoundary(input))
	if err != nil || !found {
		t.Fatalf("ClaimSteer() = %#v, %t, %v", claim, found, err)
	}
	ui.setConversationRejection(func(event UIEvent) bool {
		return event.Kind == UIEventConversationInput && event.ConversationInput.Changed != nil &&
			event.ConversationInput.Changed.State == ConversationInputCommitted
	})
	if err := input.Steering().CommitSteer(context.Background(), claim); !errors.Is(err, ErrConversationInputUnavailable) {
		t.Fatalf("CommitSteer(committed-event rejection) error = %v", err)
	}
	coordinator.mu.Lock()
	item := coordinator.conversationInputs.items[accepted.ItemID]
	state := ConversationInputState("")
	if item != nil {
		state = item.state
	}
	coordinator.mu.Unlock()
	if state != ConversationInputCommitted || persistence.runInputCalls() != 1 || runner.calls.Load() != 1 {
		t.Fatalf("known committed sink failure = state %q writes %d runs %d", state, persistence.runInputCalls(), runner.calls.Load())
	}
	if _, found, err := input.Steering().ClaimSteer(context.Background(), conversationBoundary(input)); err != nil || found {
		t.Fatalf("committed sink failure became claimable = found %t/error %v", found, err)
	}
	ui.setConversationRejection(func(event UIEvent) bool {
		return event.Kind == UIEventConversationInput && event.ConversationInput.Changed != nil &&
			event.ConversationInput.Changed.State == ConversationInputCommitting
	})
	rejected, err := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Reject this before the model boundary."))
	if err != nil {
		t.Fatalf("SubmitSteer(second) error = %v", err)
	}
	if _, found, err := input.Steering().ClaimSteer(context.Background(), conversationBoundary(input)); !errors.Is(err, ErrConversationInputUnavailable) || found {
		t.Fatalf("ClaimSteer(committing-event rejection) = found %t/error %v", found, err)
	}
	coordinator.mu.Lock()
	rejectedItem := coordinator.conversationInputs.items[rejected.ItemID]
	coordinator.mu.Unlock()
	if rejectedItem == nil || rejectedItem.state != ConversationInputRejected || persistence.runInputCalls() != 1 {
		t.Fatalf("rejected sink barrier = item %#v writes %d", rejectedItem, persistence.runInputCalls())
	}
	ui.setConversationRejection(nil)
	runner.outcomes <- controlledConversationFail
	if _, err := coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestConversationSteerInvalidatedAfterPersistenceRemainsKnownCommitted(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected workload.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	accepted, err := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Preserve the committed fact across invalidation."))
	if err != nil {
		t.Fatalf("SubmitSteer() error = %v", err)
	}
	claim, found, err := input.Steering().ClaimSteer(context.Background(), conversationBoundary(input))
	if err != nil || !found {
		t.Fatalf("ClaimSteer() = %#v, %t, %v", claim, found, err)
	}
	coordinator.runInputs = runInputPersistenceFunc(func(ctx context.Context, message domain.Message) error {
		if persistErr := persistence.AppendRunInput(ctx, message); persistErr != nil {
			return persistErr
		}
		coordinator.mu.Lock()
		coordinator.invalidateConversationInputsLocked()
		coordinator.mu.Unlock()
		return nil
	})
	if err = input.Steering().CommitSteer(context.Background(), claim); !errors.Is(err, ErrConversationInputUnavailable) {
		t.Fatalf("CommitSteer() error = %v, want unavailable after committed invalidation", err)
	}
	coordinator.mu.Lock()
	item := coordinator.conversationInputs.items[accepted.ItemID]
	status := coordinator.conversationInputStatusLocked()
	committedInputs := append([]agent.ConversationTurn(nil), coordinator.active.committedInputs...)
	coordinator.mu.Unlock()
	if item == nil || item.state != ConversationInputCommitted || status.Committed != 1 ||
		status.Unknown != 0 || status.Recovered != 0 || len(committedInputs) != 1 ||
		committedInputs[0].MessageID != accepted.ItemID || persistence.runInputCalls() != 1 {
		t.Fatalf("post-persistence invalidation = item %#v status %#v inputs %#v writes %d",
			item, status, committedInputs, persistence.runInputCalls())
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestQueuedSuccessorSinkRejectionRecoversWithoutAutomaticSend(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, ui := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	queued, err := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "Do not auto-send across a rejected delivery sink."))
	if err != nil {
		t.Fatalf("EnqueueFollowUp() error = %v", err)
	}
	recovered := make(chan struct{})
	var recoveredOnce sync.Once
	ui.setConversationRejection(func(event UIEvent) bool {
		if event.Kind != UIEventConversationInput || event.ConversationInput.Changed == nil ||
			event.ConversationInput.Changed.ItemID != queued.ItemID {
			return false
		}
		if event.ConversationInput.Changed.State == ConversationInputQueued && event.ConversationInput.Status.Items == 0 {
			return true
		}
		if event.ConversationInput.Changed.State == ConversationInputRecovered {
			recoveredOnce.Do(func() { close(recovered) })
		}
		return false
	})
	runner.outcomes <- controlledConversationComplete
	if result, waitErr := coordinator.WaitRun(context.Background(), runID); waitErr != nil || result.Status != domain.AgentRunStatusCompleted {
		t.Fatalf("WaitRun() = %#v, %v", result, waitErr)
	}
	select {
	case <-recovered:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued successor recovery")
	}
	coordinator.mu.Lock()
	item := coordinator.conversationInputs.items[queued.ItemID]
	status := coordinator.conversationInputStatusLocked()
	coordinator.mu.Unlock()
	if runner.calls.Load() != 1 || item == nil || item.state != ConversationInputRecovered || status.Recovered != 1 {
		t.Fatalf("sink-rejected successor = calls %d item %#v status %#v", runner.calls.Load(), item, status)
	}
}

func TestQueuedFollowUpsDrainFIFOOnePerCleanTurnAndStopAfterFailure(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	firstRun, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	first := waitConversationRunInput(t, runner)
	for _, text := range []string{"First queued follow-up.", "Second queued follow-up."} {
		if _, err = coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(first, text)); err != nil {
			t.Fatalf("EnqueueFollowUp(%q) error = %v", text, err)
		}
	}
	runner.outcomes <- controlledConversationComplete
	if result, waitErr := coordinator.WaitRun(context.Background(), firstRun); waitErr != nil || result.Status != domain.AgentRunStatusCompleted {
		t.Fatalf("first WaitRun() = %#v, %v", result, waitErr)
	}
	second := waitConversationRunInput(t, runner)
	if second.Question() != "First queued follow-up." || runner.calls.Load() != 2 {
		t.Fatalf("first successor question/calls = %q/%d", second.Question(), runner.calls.Load())
	}
	coordinator.mu.Lock()
	status := coordinator.conversationInputStatusLocked()
	coordinator.mu.Unlock()
	if status.Queued != 1 || status.Recovered != 0 {
		t.Fatalf("queue while one successor is active = %#v", status)
	}

	runner.outcomes <- controlledConversationFail
	if result, waitErr := coordinator.WaitRun(context.Background(), second.RunID()); waitErr != nil || result.Status != domain.AgentRunStatusFailed {
		t.Fatalf("second WaitRun() = %#v, %v", result, waitErr)
	}
	coordinator.mu.Lock()
	status = coordinator.conversationInputStatusLocked()
	active := coordinator.active
	coordinator.mu.Unlock()
	if runner.calls.Load() != 2 || active != nil || status.Queued != 0 || status.Recovered != 1 || status.Editable != 1 {
		t.Fatalf("failed successor auto-drain state: calls=%d active=%v status=%#v", runner.calls.Load(), active != nil, status)
	}
}

func TestPendingSteerWithoutAnotherBoundaryBecomesOneSuccessorAfterCleanTurn(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	firstRun, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	first := waitConversationRunInput(t, runner)
	pending, err := coordinator.SubmitSteer(context.Background(), conversationInputCommand(
		first, "Continue this as a successor because the final boundary already passed.",
	))
	if err != nil || pending.State != ConversationInputPending {
		t.Fatalf("SubmitSteer() = %#v, %v", pending, err)
	}

	runner.outcomes <- controlledConversationComplete
	if result, waitErr := coordinator.WaitRun(context.Background(), firstRun); waitErr != nil ||
		result.Status != domain.AgentRunStatusCompleted {
		t.Fatalf("first WaitRun() = %#v, %v", result, waitErr)
	}
	second := waitConversationRunInput(t, runner)
	if second.Question() != pending.Text || runner.calls.Load() != 2 {
		t.Fatalf("successor question/calls = %q/%d", second.Question(), runner.calls.Load())
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), second.RunID()); err != nil {
		t.Fatalf("successor WaitRun() error = %v", err)
	}
}

func TestPendingAndUnknownSteersNeverAutoSendAfterUnsafeTerminal(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{name: "failure", mode: "failure"},
		{name: "cancellation", mode: "cancel"},
		{name: "timeout", mode: "timeout"},
		{name: "unknown even after otherwise clean completion", mode: "unknown"},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			runner := newControlledConversationRunner(clock)
			coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
			session := createCoordinatorSession(t, coordinator)
			runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
				SessionID: session.ID, Question: "Initial question.",
			})
			if err != nil {
				t.Fatalf("StartRun() error = %v", err)
			}
			input := waitConversationRunInput(t, runner)
			pending, err := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Do not retry this automatically."))
			if err != nil {
				t.Fatalf("SubmitSteer() error = %v", err)
			}
			queued, err := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "Do not drain after an unsafe outcome."))
			if err != nil {
				t.Fatalf("EnqueueFollowUp() error = %v", err)
			}
			switch current.mode {
			case "unknown":
				claim, found, claimErr := input.Steering().ClaimSteer(context.Background(), conversationBoundary(input))
				if claimErr != nil || !found {
					t.Fatalf("ClaimSteer() = %#v, %t, %v", claim, found, claimErr)
				}
				if commitErr := input.Steering().CommitSteer(context.Background(), claim); commitErr != nil {
					t.Fatalf("CommitSteer() error = %v", commitErr)
				}
				input.Steering().ResolveSteer(context.Background(), claim, agent.SteerResolutionUnknown)
				runner.outcomes <- controlledConversationComplete
			case "failure":
				runner.outcomes <- controlledConversationFail
			case "timeout":
				runner.outcomes <- controlledConversationTimeout
			case "cancel":
				if cancelErr := coordinator.CancelRun(context.Background(), CancelRunCommand{
					RunID: runID, ScopeGeneration: input.Scope().Generation,
				}); cancelErr != nil {
					t.Fatalf("CancelRun() error = %v", cancelErr)
				}
			}
			if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
				t.Fatalf("WaitRun() error = %v", err)
			}
			coordinator.mu.Lock()
			status := coordinator.conversationInputStatusLocked()
			item := coordinator.conversationInputs.items[pending.ItemID]
			queuedItem := coordinator.conversationInputs.items[queued.ItemID]
			active := coordinator.active
			coordinator.mu.Unlock()
			wantState := ConversationInputRecovered
			if current.mode == "unknown" {
				wantState = ConversationInputUnknown
			}
			if runner.calls.Load() != 1 || active != nil || item == nil || item.state != wantState || status.Queued != 0 {
				t.Fatalf("unsafe terminal auto-send state: calls=%d active=%t item=%#v status=%#v",
					runner.calls.Load(), active != nil, item, status)
			}
			wantRecovered := 2
			if current.mode == "unknown" {
				wantRecovered = 1
			}
			if queuedItem == nil || queuedItem.state != ConversationInputRecovered || status.Recovered != wantRecovered {
				t.Fatalf("queue after unsafe outcome = item %#v status %#v", queuedItem, status)
			}
		})
	}
}

func TestQueuedFollowUpCountLimitIsExactAndOneOverFailsClosed(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	for index := 0; index < MaxConversationInputItems; index++ {
		text := fmt.Sprintf("Queued item %d.", index+1)
		if _, err = coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, text)); err != nil {
			t.Fatalf("exact item %d error = %v", index+1, err)
		}
	}
	if _, err = coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "One item over.")); !errors.Is(err, ErrConversationInputLimit) {
		t.Fatalf("item-count one-over error = %v", err)
	}
	coordinator.mu.Lock()
	status := coordinator.conversationInputStatusLocked()
	coordinator.mu.Unlock()
	if status.Items != MaxConversationInputItems || status.Queued != MaxConversationInputItems || !status.valid() {
		t.Fatalf("exact item-count status = %#v", status)
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestQueuedFollowUpLimitsAreExactAndOneOverFailsClosed(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	exact := strings.Repeat("x", MaxConversationInputItemBytes)
	for index := 0; index < MaxConversationInputAggregateBytes/MaxConversationInputItemBytes; index++ {
		if _, err = coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, exact)); err != nil {
			t.Fatalf("exact aggregate item %d error = %v", index, err)
		}
	}
	if _, err = coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "one over aggregate")); !errors.Is(err, ErrConversationInputLimit) {
		t.Fatalf("aggregate one-over error = %v", err)
	}
	tooLarge := conversationInputCommand(input, strings.Repeat("x", MaxConversationInputItemBytes+1))
	if _, err = coordinator.EnqueueFollowUp(context.Background(), tooLarge); !errors.Is(err, ErrConversationInputInvalid) {
		t.Fatalf("per-item one-over error = %v", err)
	}
	coordinator.mu.Lock()
	status := coordinator.conversationInputStatusLocked()
	coordinator.mu.Unlock()
	if status.Items != 4 || status.Bytes != MaxConversationInputAggregateBytes || !status.valid() {
		t.Fatalf("exact queue status = %#v", status)
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestPopLastConversationInputIsLIFOAndNeverPopsPendingOrUnknown(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	first, _ := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "First queued."))
	second, _ := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "Second queued."))
	pending, _ := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Pending steer."))
	command := PopConversationInputCommand{
		SessionID: session.ID, RunID: runID, ExpectedScopeGeneration: input.Scope().Generation,
		ExpectedPolicyGeneration: input.PolicyGeneration(),
	}
	popped, err := coordinator.PopLastConversationInput(context.Background(), command)
	if err != nil || popped.ItemID != second.ItemID || popped.Text != "Second queued." {
		t.Fatalf("first PopLastConversationInput() = %#v, %v", popped, err)
	}
	popped, err = coordinator.PopLastConversationInput(context.Background(), command)
	if err != nil || popped.ItemID != first.ItemID || popped.Text != "First queued." {
		t.Fatalf("second PopLastConversationInput() = %#v, %v", popped, err)
	}
	if _, err = coordinator.PopLastConversationInput(context.Background(), command); !errors.Is(err, ErrConversationInputConflict) {
		t.Fatalf("pending-only pop error = %v", err)
	}
	coordinator.mu.Lock()
	item := coordinator.conversationInputs.items[pending.ItemID]
	item.state = ConversationInputUnknown
	item.revision = coordinator.conversationInputs.nextRevision()
	coordinator.mu.Unlock()
	if _, err = coordinator.PopLastConversationInput(context.Background(), command); !errors.Is(err, ErrConversationInputConflict) {
		t.Fatalf("unknown-only pop error = %v", err)
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestPopLastConversationInputEditsRecoveredRejectedAndQueuedInLIFOOrder(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	queued, _ := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "Queued input."))
	rejected, _ := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Rejected input."))
	claim, found, claimErr := input.Steering().ClaimSteer(context.Background(), conversationBoundary(input))
	if claimErr != nil || !found {
		t.Fatalf("rejected ClaimSteer() = %#v, %t, %v", claim, found, claimErr)
	}
	input.Steering().ResolveSteer(context.Background(), claim, agent.SteerResolutionRejected)
	recovered, _ := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Recovered input."))
	claim, found, claimErr = input.Steering().ClaimSteer(context.Background(), conversationBoundary(input))
	if claimErr != nil || !found {
		t.Fatalf("recovered ClaimSteer() = %#v, %t, %v", claim, found, claimErr)
	}
	input.Steering().ResolveSteer(context.Background(), claim, agent.SteerResolutionRecovered)
	command := PopConversationInputCommand{
		SessionID: session.ID, RunID: runID, ExpectedScopeGeneration: input.Scope().Generation,
		ExpectedPolicyGeneration: input.PolicyGeneration(),
	}
	for index, want := range []ConversationInputProjection{recovered, rejected, queued} {
		popped, popErr := coordinator.PopLastConversationInput(context.Background(), command)
		if popErr != nil || popped.ItemID != want.ItemID || popped.Text != want.Text {
			t.Fatalf("LIFO pop %d = %#v, %v, want %#v", index, popped, popErr, want)
		}
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestPopLastConversationInputNeverEditsCommittingOrCommitted(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	_, err = coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Committed input."))
	if err != nil {
		t.Fatalf("SubmitSteer() error = %v", err)
	}
	claim, found, claimErr := input.Steering().ClaimSteer(context.Background(), conversationBoundary(input))
	if claimErr != nil || !found {
		t.Fatalf("ClaimSteer() = %#v, %t, %v", claim, found, claimErr)
	}
	command := PopConversationInputCommand{
		SessionID: session.ID, RunID: runID, ExpectedScopeGeneration: input.Scope().Generation,
		ExpectedPolicyGeneration: input.PolicyGeneration(),
	}
	if _, err = coordinator.PopLastConversationInput(context.Background(), command); !errors.Is(err, ErrConversationInputConflict) {
		t.Fatalf("committing pop error = %v", err)
	}
	if err = input.Steering().CommitSteer(context.Background(), claim); err != nil {
		t.Fatalf("CommitSteer() error = %v", err)
	}
	if _, err = coordinator.PopLastConversationInput(context.Background(), command); !errors.Is(err, ErrConversationInputConflict) {
		t.Fatalf("committed pop error = %v", err)
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestPopAndAutoDrainRaceHasExactlyOneWinner(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	queued, err := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "Race follow-up."))
	if err != nil {
		t.Fatalf("EnqueueFollowUp() error = %v", err)
	}
	command := PopConversationInputCommand{
		SessionID: session.ID, RunID: runID, ExpectedScopeGeneration: input.Scope().Generation,
		ExpectedPolicyGeneration: input.PolicyGeneration(),
	}
	start := make(chan struct{})
	var (
		popped    ConversationInputProjection
		popErr    error
		waitGroup sync.WaitGroup
	)
	waitGroup.Add(2)
	go func() {
		defer waitGroup.Done()
		<-start
		popped, popErr = coordinator.PopLastConversationInput(context.Background(), command)
	}()
	go func() {
		defer waitGroup.Done()
		<-start
		runner.outcomes <- controlledConversationComplete
	}()
	close(start)
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	waitGroup.Wait()

	if popErr == nil {
		if popped.ItemID != queued.ItemID || runner.calls.Load() != 1 {
			t.Fatalf("pop winner = %#v, calls=%d", popped, runner.calls.Load())
		}
		return
	}
	if !errors.Is(popErr, ErrConversationInputUnavailable) && !errors.Is(popErr, ErrConversationInputConflict) {
		t.Fatalf("pop race error = %v", popErr)
	}
	second := waitConversationRunInput(t, runner)
	if second.Question() != queued.Text || runner.calls.Load() != 2 {
		t.Fatalf("drain winner question/calls = %q/%d", second.Question(), runner.calls.Load())
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), second.RunID()); err != nil {
		t.Fatalf("successor WaitRun() error = %v", err)
	}
}

func TestConversationBindingChangesInvalidateBeforeAnyAutomaticRetarget(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *Coordinator, *controlledConversationRunner) bool
	}{
		{
			name: "scope generation",
			mutate: func(t *testing.T, coordinator *Coordinator, _ *controlledConversationRunner) bool {
				t.Helper()
				if err := coordinator.InvalidateScope(8); err != nil {
					t.Fatalf("InvalidateScope() error = %v", err)
				}
				return false
			},
		},
		{
			name: "permission policy generation",
			mutate: func(_ *testing.T, coordinator *Coordinator, _ *controlledConversationRunner) bool {
				coordinator.cancelRunAfterPermissionChange()
				return true
			},
		},
		{
			name: "model profile",
			mutate: func(_ *testing.T, coordinator *Coordinator, runner *controlledConversationRunner) bool {
				runner.profile.Store("agent-next")
				coordinator.publishConversationInvalidation(context.Background())
				return false
			},
		},
		{
			name: "model origin",
			mutate: func(t *testing.T, coordinator *Coordinator, _ *controlledConversationRunner) bool {
				t.Helper()
				if err := coordinator.privacy.ReconfigureOrigin("https://replacement.model.example"); err != nil {
					t.Fatalf("ReconfigureOrigin() error = %v", err)
				}
				coordinator.publishConversationInvalidation(context.Background())
				return false
			},
		},
		{
			name: "consent",
			mutate: func(t *testing.T, coordinator *Coordinator, _ *controlledConversationRunner) bool {
				t.Helper()
				review, err := coordinator.privacy.Review(context.Background())
				if err != nil {
					t.Fatalf("Privacy Review() error = %v", err)
				}
				if _, err = coordinator.privacy.Decide(context.Background(), PrivacyActionRevoke, review.Revision, nil); err != nil {
					t.Fatalf("Privacy Decide(revoke) error = %v", err)
				}
				coordinator.publishConversationInvalidation(context.Background())
				return false
			},
		},
		{
			name: "budget profile",
			mutate: func(t *testing.T, coordinator *Coordinator, _ *controlledConversationRunner) bool {
				t.Helper()
				limits, err := agent.RunBudgetLimitsForProfile(agent.BudgetProfileCompact)
				if err != nil {
					t.Fatalf("RunBudgetLimitsForProfile() error = %v", err)
				}
				coordinator.mu.Lock()
				coordinator.budgetLimits = limits
				coordinator.mu.Unlock()
				coordinator.publishConversationInvalidation(context.Background())
				return false
			},
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			runner := newControlledConversationRunner(clock)
			coordinator, _, _, ui := newCoordinatorHarness(t, clock, runner)
			session := createCoordinatorSession(t, coordinator)
			runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
				SessionID: session.ID, Question: "Initial question.",
			})
			if err != nil {
				t.Fatalf("StartRun() error = %v", err)
			}
			input := waitConversationRunInput(t, runner)
			pending, err := coordinator.SubmitSteer(context.Background(), conversationInputCommand(input, "Pending input for invalidation."))
			if err != nil {
				t.Fatalf("SubmitSteer() error = %v", err)
			}
			queued, err := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "Queued input for invalidation."))
			if err != nil {
				t.Fatalf("EnqueueFollowUp() error = %v", err)
			}
			cancelled := current.mutate(t, coordinator, runner)
			coordinator.mu.Lock()
			pendingItem := coordinator.conversationInputs.items[pending.ItemID]
			queuedItem := coordinator.conversationInputs.items[queued.ItemID]
			coordinator.mu.Unlock()
			if pendingItem == nil || pendingItem.state != ConversationInputRecovered ||
				queuedItem == nil || queuedItem.state != ConversationInputRecovered {
				t.Fatalf("invalidated items = pending %#v queued %#v", pendingItem, queuedItem)
			}
			var recoveredProjection bool
			for _, event := range ui.events() {
				if event.Kind == UIEventConversationInput && event.ConversationInput.Status.Recovered == 2 &&
					len(event.ConversationInput.Preview) == 2 {
					recoveredProjection = true
				}
			}
			if !recoveredProjection {
				t.Fatal("invalidation did not publish the recovered working-area projection")
			}
			if !cancelled {
				runner.outcomes <- controlledConversationComplete
			}
			if _, err := coordinator.WaitRun(context.Background(), runID); err != nil {
				t.Fatalf("WaitRun() error = %v", err)
			}
			if runner.calls.Load() != 1 {
				t.Fatalf("binding change started %d runs, want 1", runner.calls.Load())
			}
		})
	}
}

func TestConversationInputSafetyPipelineBlocksAndRedactsBeforeQueueOwnership(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, persistence, _, ui := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Initial question.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	canary := strings.Repeat("queue-sensitive-canary", 2)
	projection, err := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(
		input,
		"token="+canary+"\x1b[31m unsafe terminal suffix\x1b[0m",
	))
	if err != nil || strings.Contains(projection.Text, canary) || strings.ContainsRune(projection.Text, '\x1b') ||
		!strings.Contains(projection.Text, "[REDACTED]") {
		t.Fatalf("sanitized queue projection = %#v, %v", projection, err)
	}
	for _, event := range ui.events() {
		if event.Kind == UIEventConversationInput && strings.Contains(event.ConversationInput.Preview[0].Text, canary) {
			t.Fatal("sensitive canary entered the working preview")
		}
	}
	privateKey := strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ") + "\nsynthetic\n" +
		strings.Join([]string{"-----END", "PRIVATE", "KEY-----"}, " ")
	if _, err := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, privateKey)); !errors.Is(err, ErrQuestionRejected) {
		t.Fatalf("blocked sensitive input error = %v, want ErrQuestionRejected", err)
	}
	coordinator.mu.Lock()
	status := coordinator.conversationInputStatusLocked()
	coordinator.mu.Unlock()
	if status.Items != 1 || status.Bytes != len(projection.Text) || persistence.runInputCalls() != 0 || runner.calls.Load() != 1 {
		t.Fatalf("safe queue ownership = status %#v writes %d runs %d", status, persistence.runInputCalls(), runner.calls.Load())
	}
	runner.outcomes <- controlledConversationFail
	if _, err := coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}
