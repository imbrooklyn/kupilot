package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestPlanOnlyArmCancelAndOneShotLifecycle(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	beginBefore := persistence.beginCalls()
	armed, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandArmPlan, RequestID: 1})
	if err != nil || armed.RequestID != 1 || armed.PlanArmed == nil || !*armed.PlanArmed || runner.calls.Load() != 0 || persistence.beginCalls() != beginBefore {
		t.Fatalf("arm plan = %#v, %v; runner=%d persistence=%d", armed, err, runner.calls.Load(), persistence.beginCalls())
	}
	cancelled, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandCancelPlan, RequestID: 2})
	if err != nil || cancelled.RequestID != 2 || cancelled.PlanArmed == nil || *cancelled.PlanArmed || runner.calls.Load() != 0 || persistence.beginCalls() != beginBefore {
		t.Fatalf("cancel plan = %#v, %v; runner=%d persistence=%d", cancelled, err, runner.calls.Load(), persistence.beginCalls())
	}
	if _, err = coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandArmPlan, RequestID: 3}); err != nil {
		t.Fatalf("second arm error = %v", err)
	}
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Plan a bounded readiness diagnosis."})
	if err != nil {
		t.Fatalf("StartRun(plan-only) error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	if input.Mode() != agent.RunModePlanOnly {
		t.Fatalf("frozen run mode = %q", input.Mode())
	}
	status := coordinator.uiStatus()
	if status.PlanArmed {
		t.Fatal("plan arm was not consumed after durable run start")
	}
	runner.outcomes <- controlledConversationComplete
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil || result.Status != domain.AgentRunStatusCompleted {
		t.Fatalf("plan WaitRun() = %#v, %v", result, err)
	}
	if len(persistence.diagnoses) != 1 || persistence.diagnoses[0].Plan == nil || len(persistence.diagnoses[0].RecommendedActions) != 0 {
		t.Fatalf("persisted plan diagnosis = %#v", persistence.diagnoses)
	}

	ordinaryID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Now answer normally."})
	if err != nil {
		t.Fatalf("StartRun(ordinary) error = %v", err)
	}
	ordinary := waitConversationRunInput(t, runner)
	if ordinary.Mode() != agent.RunModeOrdinary {
		t.Fatalf("one-shot successor mode = %q", ordinary.Mode())
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), ordinaryID); err != nil {
		t.Fatalf("ordinary WaitRun() error = %v", err)
	}
}

func TestSubmitQuestionReportsModeFrozenAtRunStart(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	createCoordinatorSession(t, coordinator)
	if _, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandArmPlan, RequestID: 1}); err != nil {
		t.Fatalf("arm plan error = %v", err)
	}
	outcome, err := coordinator.ExecuteUICommand(context.Background(), UICommand{
		Kind: UICommandSubmitQuestion, RequestID: 2, ExpectedScopeGeneration: 7,
		Text: "Plan a bounded readiness diagnosis.",
	})
	if err != nil || outcome.RunMode != agent.RunModePlanOnly || !outcome.RunID.Valid() {
		t.Fatalf("submit outcome = %#v, %v", outcome, err)
	}
	input := waitConversationRunInput(t, runner)
	if input.RunID() != outcome.RunID || input.Mode() != outcome.RunMode {
		t.Fatalf("frozen input = run %q mode %q, outcome = run %q mode %q", input.RunID(), input.Mode(), outcome.RunID, outcome.RunMode)
	}
	runner.outcomes <- controlledConversationComplete
	if result, waitErr := coordinator.WaitRun(context.Background(), outcome.RunID); waitErr != nil || result.Status != domain.AgentRunStatusCompleted {
		t.Fatalf("WaitRun() = %#v, %v", result, waitErr)
	}
}

func TestSuccessfulPlanMayDrainOneQueuedOrdinarySuccessor(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	if _, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandArmPlan, RequestID: 1}); err != nil {
		t.Fatalf("arm plan error = %v", err)
	}
	planRun, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Plan the next checks."})
	if err != nil {
		t.Fatalf("StartRun(plan) error = %v", err)
	}
	planInput := waitConversationRunInput(t, runner)
	if _, err = coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(planInput, "Execute no plan; answer this new question normally.")); err != nil {
		t.Fatalf("EnqueueFollowUp() error = %v", err)
	}
	runner.outcomes <- controlledConversationComplete
	if result, waitErr := coordinator.WaitRun(context.Background(), planRun); waitErr != nil || result.Status != domain.AgentRunStatusCompleted {
		t.Fatalf("plan WaitRun() = %#v, %v", result, waitErr)
	}
	successor := waitConversationRunInput(t, runner)
	if successor.Mode() != agent.RunModeOrdinary || successor.Question() != "Execute no plan; answer this new question normally." || runner.calls.Load() != 2 {
		t.Fatalf("plan successor = mode %q question %q calls %d", successor.Mode(), successor.Question(), runner.calls.Load())
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), successor.RunID()); err != nil {
		t.Fatalf("successor WaitRun() error = %v", err)
	}
}

func TestFailedPlanRecoversQueueWithoutAutomaticDrain(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	if _, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandArmPlan, RequestID: 1}); err != nil {
		t.Fatalf("arm plan error = %v", err)
	}
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Plan a bounded check."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	queued, err := coordinator.EnqueueFollowUp(context.Background(), conversationInputCommand(input, "Do not drain after plan failure."))
	if err != nil {
		t.Fatalf("EnqueueFollowUp() error = %v", err)
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
	coordinator.mu.Lock()
	item := coordinator.conversationInputs.items[queued.ItemID]
	coordinator.mu.Unlock()
	if runner.calls.Load() != 1 || item == nil || item.state != ConversationInputRecovered {
		t.Fatalf("failed plan queue = calls %d item %#v", runner.calls.Load(), item)
	}
}

func TestPlanArmRejectsActiveRunWithoutChangingItsMode(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Active ordinary run."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	if _, err = coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandArmPlan, RequestID: 1}); !errors.Is(err, ErrCoordinatorBusy) {
		t.Fatalf("active arm error = %v", err)
	}
	if input.Mode() != agent.RunModeOrdinary || coordinator.uiStatus().PlanArmed {
		t.Fatalf("active run mode/arm = %q/%t", input.Mode(), coordinator.uiStatus().PlanArmed)
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestApplicationRejectsActionBearingPlanBeforeAuthorityPreparation(t *testing.T) {
	clock := newCoordinatorClock()
	var diagnosisResult atomic.Int64
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
		}
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted})
		diagnosis := domain.Diagnosis{
			ID: "00000000-0000-7000-8000-000000009201", RunID: input.RunID(), Scope: input.Scope().Snapshot(),
			AnswerMarkdown: "This ordinary answer improperly proposes an action during a plan-only run.", CreatedAt: clock.Now(),
			RecommendedActions: []domain.RecommendedAction{{Action: "This must never become authority.", Risk: "No authority."}},
		}
		if diagnosis.Validate() != nil {
			t.Error("action-bearing plan fixture is not domain-valid")
			return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
		}
		if result, _ := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventDiagnosisReady, Diagnosis: &diagnosis}); result == agent.EventSinkRejected {
			diagnosisResult.Store(1)
		}
		class := domain.SafeErrorClassInvalidExternalResponse
		failure := agent.RunEventFailure{Class: class, SafeMessage: "The plan response was rejected safely."}
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunFailed, Failure: &failure})
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed, ErrorClass: &class, SafeMessage: failure.SafeMessage}
	})
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	if _, err := coordinator.ExecuteUICommand(context.Background(), UICommand{Kind: UICommandArmPlan, RequestID: 1}); err != nil {
		t.Fatalf("arm plan error = %v", err)
	}
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Return a plan only."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil || result.Status != domain.AgentRunStatusFailed || diagnosisResult.Load() != 1 || len(persistence.diagnoses) != 0 {
		t.Fatalf("action-bearing plan result = %#v, %v rejected=%d diagnoses=%d", result, err, diagnosisResult.Load(), len(persistence.diagnoses))
	}
}
