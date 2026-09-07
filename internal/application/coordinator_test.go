package application

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

func TestCoordinatorRunPreflightRejectsUnboundInputAndBudgetBeforeModelCall(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*agent.ModelCallPreflight)
	}{
		{name: "input digest mismatch", mutate: func(preflight *agent.ModelCallPreflight) {
			preflight.CurrentInputDigest = domain.SHA256Hex("foreign-input-manifest")
		}},
		{name: "input count mismatch", mutate: func(preflight *agent.ModelCallPreflight) {
			preflight.CurrentInputCount++
		}},
		{name: "reservation exceeds frozen budget", mutate: func(preflight *agent.ModelCallPreflight) {
			preflight.ReservedCostUnits = 100
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			var modelCalls atomic.Int64
			runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
				publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
				if err != nil {
					t.Fatalf("NewEventPublisher() error = %v", err)
				}
				if result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil || result != agent.EventSinkAccepted {
					t.Fatalf("Publish(started) = %q, %v", result, err)
				}
				requestID := domain.ModelRequestID(coordinatorUUID(950))
				preflight := testModelCallPreflightForInput(input, agent.ModelCallAgent)
				test.mutate(preflight)
				result, err := publisher.Publish(ctx, agent.RunEvent{
					Kind: agent.RunEventModelStreamStarted, ModelRequestID: &requestID, ModelPreflight: preflight,
				})
				if err != nil || result != agent.EventSinkRejected {
					t.Fatalf("Publish(preflight) = %q, %v", result, err)
				}
				if result == agent.EventSinkAccepted {
					modelCalls.Add(1)
				}
				class := domain.SafeErrorClassPersistenceUnavailable
				if result, err := publisher.Publish(ctx, agent.RunEvent{
					Kind:    agent.RunEventRunFailed,
					Failure: &agent.RunEventFailure{Class: class, SafeMessage: "The model invocation was rejected before transport."},
				}); err != nil || result == agent.EventSinkRejected {
					t.Fatalf("Publish(failed) = %q, %v", result, err)
				}
				return agent.RunOutcome{
					Status: domain.AgentRunStatusFailed, ErrorClass: &class,
					SafeMessage: "The model invocation was rejected before transport.",
				}
			})
			coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
			session := createCoordinatorSession(t, coordinator)
			runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
				SessionID: session.ID, Question: "Inspect the selected Pod.",
			})
			if err != nil {
				t.Fatalf("StartRun() error = %v", err)
			}
			result, err := coordinator.WaitRun(context.Background(), runID)
			if err != nil || result.Status != domain.AgentRunStatusFailed || modelCalls.Load() != 0 {
				t.Fatalf("preflight result/model calls = %#v/%d, %v", result, modelCalls.Load(), err)
			}
		})
	}
}

func TestCoordinatorStatusProjectsFrozenBudgetWithoutExternalWork(t *testing.T) {
	clock := newCoordinatorClock()
	coordinator, persistence, scope, ui := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		t.Fatal("status query called the Agent runner")
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	seedCoordinatorContext(t, coordinator, persistence, session, 4)
	limits := agent.DefaultRunBudgetLimits()
	conversation, err := coordinator.conversationForRun(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("conversationForRun() error = %v", err)
	}
	input, err := agent.NewRunInputWithContext(
		domain.AgentRunID(coordinatorUUID(901)), session.ID, domain.MessageID(coordinatorUUID(902)),
		"Inspect the selected Pod.", scope.scope, nil, limits, conversation,
	)
	if err != nil {
		t.Fatalf("NewRunInput() error = %v", err)
	}
	summary, err := summaryForRunInput(input, clock.Now(), 2)
	if err != nil {
		t.Fatalf("summaryForRunInput() error = %v", err)
	}
	startedAt := clock.Now().Add(-90 * time.Second)
	coordinator.mu.Lock()
	coordinator.modelContext.summary = &summary
	coordinator.modelContext.turns = append([]agent.ConversationTurn(nil), conversation.Turns()[2:]...)
	coordinator.modelContext.recentTailCount = 2
	compressedAt := summary.GeneratedAt
	coordinator.modelContext.compressedAt = &compressedAt
	coordinator.active = &activeRun{
		input: input,
		run: domain.AgentRun{
			ID: input.RunID(), SessionID: session.ID, RequestMessageID: domain.MessageID(coordinatorUUID(902)),
			Status: domain.AgentRunStatusRunning, Scope: scope.scope.Snapshot(), PromptVersion: input.PromptVersion(),
			ToolCatalogVersion: input.CatalogVersion(), StepCount: 3, ToolCallCount: 5, ModelRequestCount: 3,
			StartedAt: &startedAt,
		},
		toolResultBytes: 96 * 1024,
		logCalls:        2,
		summaryCalls:    1,
	}
	coordinator.mu.Unlock()

	status := coordinator.uiStatus()
	if !status.valid() || !status.RunActive || status.RunID != input.RunID() ||
		status.Budget.Profile != agent.BudgetProfileBalanced || status.Budget.StepsUsed != 3 ||
		status.Budget.ToolCallsUsed != 5 || status.Budget.ModelCallsUsed != 3 ||
		status.Budget.ModelCostUnitsUsed != 3 || status.Budget.SummaryCallsUsed != 1 ||
		status.Budget.SummaryCostUnitsUsed != 1 || status.Budget.ReviewerCallsMaximum != 8 ||
		status.Budget.ToolResultBytesUsed != 96*1024 || status.Budget.LogCallsUsed != 2 ||
		status.ModelContext.EligibleMessages != 4 || status.ModelContext.EligibleBytes == 0 ||
		!status.ModelContext.Compressed || status.ModelContext.CompressedAtUnixMillis != summary.GeneratedAt.UnixMilli() ||
		status.ModelContext.CoveredThroughMessageID != summary.CoveredThroughID ||
		status.ModelContext.RecentTailMessages != 2 || status.ModelContext.SummaryCallsUsed != 1 ||
		status.ModelContext.SummaryCallsMaximum != 2 || !status.ModelContext.StorageHealthy ||
		status.Budget.ElapsedMilliseconds < 90_000 || status.Budget.RemainingMilliseconds >= limits.RunDuration.Milliseconds() {
		t.Fatalf("UI status = %#v", status)
	}
	if persistence.beginCalls() != 0 || len(ui.events()) != 0 {
		t.Fatalf("status side effects: run starts=%d UI events=%d", persistence.beginCalls(), len(ui.events()))
	}
	coordinator.mu.Lock()
	coordinator.active = nil
	coordinator.mu.Unlock()
}

func TestRunInputAllowsOnlyExactFrozenResourcePolicyEvidence(t *testing.T) {
	scope := domain.ClusterScope{
		Context: "selected", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent,
		Generation: 7, ActivatedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
	}
	widgetType := domain.ResourceType{
		ID: "widgets", Group: "example.test", Version: "v1", Resource: "widgets", Kind: "Widget",
		Scope: domain.ResourceScopeNamespaced,
	}
	entries := domain.BuiltInResourcePolicies()
	entries = append(entries, domain.ResourcePolicy{
		Type:  widgetType,
		Verbs: []domain.ResourceVerb{domain.ResourceVerbGet, domain.ResourceVerbList},
		Fields: []domain.ResourceFieldPolicy{{
			ID: "state", Path: "status.state", Scalar: domain.ResourceScalarString,
			DataClass: domain.ResourceDataStatus, SelectorSource: domain.ResourceSelectorNone,
			Operators: []domain.ResourceFilterOperator{domain.ResourceFilterEquals}, Evidence: true,
		}},
		Limits: domain.ResourceQueryLimits{
			MaxPages: 2, PageItems: 20, PageBytes: 256 * 1024,
			MaxItems: 40, MaxBytes: 1 << 20, MaxReturned: 20,
		},
	})
	catalog, err := domain.NewResourcePolicyCatalog(domain.ResourcePolicyVersion, entries)
	if err != nil {
		t.Fatalf("NewResourcePolicyCatalog() error = %v", err)
	}
	sessionID := domain.SessionID(coordinatorUUID(941))
	conversation, err := agent.NewConversationContext(sessionID, nil, nil)
	if err != nil {
		t.Fatalf("NewConversationContext() error = %v", err)
	}
	input, err := agent.NewRunInputWithPolicyContext(
		domain.AgentRunID(coordinatorUUID(942)), sessionID, domain.MessageID(coordinatorUUID(943)),
		"Inspect the configured Widget.", scope, nil, agent.DefaultRunBudgetLimits(), conversation, catalog, 3,
	)
	if err != nil {
		t.Fatalf("NewRunInputWithPolicyContext() error = %v", err)
	}
	evidence := domain.Evidence{
		ResourceType: widgetType,
		Resource: domain.ResourceRef{
			APIVersion: "example.test/v1", Kind: "Widget", Namespace: "team-a", Name: "sample-widget",
		},
	}
	if !runInputAllowsEvidence(input, evidence) {
		t.Fatal("exact configured CRD Evidence was rejected")
	}

	unknown := evidence
	unknown.ResourceType.ID = "unknown-widgets"
	if runInputAllowsEvidence(input, unknown) {
		t.Fatal("unconfigured CRD Evidence was accepted")
	}
	spoofed := evidence
	spoofed.ResourceType.Group = "other.test"
	spoofed.Resource.APIVersion = "other.test/v1"
	if runInputAllowsEvidence(input, spoofed) {
		t.Fatal("mismatched CRD API Evidence was accepted")
	}
	crossNamespace := evidence
	crossNamespace.Resource.Namespace = "team-b"
	if runInputAllowsEvidence(input, crossNamespace) {
		t.Fatal("cross-Namespace CRD Evidence was accepted under current policy")
	}
}

func TestApplicationDiagnosisEvidenceBindingRequiresAcceptedOrderAndPolicy(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected Pod.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	input := waitConversationRunInput(t, runner)
	first := domain.EvidenceID(coordinatorUUID(950))
	second := domain.EvidenceID(coordinatorUUID(951))
	claim := "The projected Pod condition is not Ready."
	diagnosis := domain.Diagnosis{
		ID: "00000000-0000-7000-8000-000000000952", RunID: runID, Scope: input.Scope().Snapshot(),
		AnswerMarkdown: claim, CreatedAt: clock.Now(),
		ClaimCoverage: []domain.ClaimEvidenceCoverage{{
			Sequence: 1, Kind: domain.ClaimCurrentObservation, Text: claim, TextHash: domain.SHA256Hex(claim),
			EvidenceIDs: []domain.EvidenceID{first, second}, RunID: runID, Scope: input.Scope().Snapshot(),
			PolicyGeneration: input.PolicyGeneration(), State: domain.ClaimCoverageVerified,
		}},
	}
	if diagnosis.Validate() != nil {
		t.Fatal("Application Evidence binding fixture is not Domain-valid")
	}
	coordinator.mu.Lock()
	state := coordinator.active
	state.evidenceOrder = []domain.EvidenceID{first, second}
	coordinator.mu.Unlock()
	if !diagnosisEvidenceAccepted(state, diagnosis) {
		t.Fatal("accepted same-run Evidence order was rejected")
	}
	for _, current := range []struct {
		name   string
		mutate func(*domain.Diagnosis)
	}{
		{name: "unknown", mutate: func(value *domain.Diagnosis) {
			value.ClaimCoverage[0].EvidenceIDs[1] = domain.EvidenceID(coordinatorUUID(953))
		}},
		{name: "out of order", mutate: func(value *domain.Diagnosis) {
			value.ClaimCoverage[0].EvidenceIDs[0], value.ClaimCoverage[0].EvidenceIDs[1] =
				value.ClaimCoverage[0].EvidenceIDs[1], value.ClaimCoverage[0].EvidenceIDs[0]
		}},
		{name: "stale policy", mutate: func(value *domain.Diagnosis) {
			value.ClaimCoverage[0].PolicyGeneration++
		}},
	} {
		t.Run(current.name, func(t *testing.T) {
			value := cloneDiagnosis(diagnosis)
			current.mutate(&value)
			if value.Validate() != nil {
				t.Fatal("mutated Application Evidence fixture is not Domain-valid")
			}
			if diagnosisEvidenceAccepted(state, value) {
				t.Fatal("unbound Diagnosis Evidence was accepted")
			}
		})
	}
	runner.outcomes <- controlledConversationFail
	if _, err = coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestCoordinatorRejectsDiagnosisWithUnacceptedEvidenceBeforePersistence(t *testing.T) {
	clock := newCoordinatorClock()
	rejected := make(chan agent.EventSinkResult, 1)
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			return agent.RunOutcome{Status: domain.AgentRunStatusFailed}
		}
		_, _ = publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted})
		claim := "An unaccepted current-state claim must fail closed."
		diagnosis := domain.Diagnosis{
			ID: "00000000-0000-7000-8000-000000000954", RunID: input.RunID(), Scope: input.Scope().Snapshot(),
			AnswerMarkdown: claim, CreatedAt: clock.Now(),
			ClaimCoverage: []domain.ClaimEvidenceCoverage{{
				Sequence: 1, Kind: domain.ClaimCurrentObservation, Text: claim, TextHash: domain.SHA256Hex(claim),
				EvidenceIDs: []domain.EvidenceID{domain.EvidenceID(coordinatorUUID(955))}, RunID: input.RunID(),
				Scope: input.Scope().Snapshot(), PolicyGeneration: input.PolicyGeneration(), State: domain.ClaimCoverageVerified,
			}},
		}
		result, _ := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventDiagnosisReady, Diagnosis: &diagnosis})
		rejected <- result
		class := domain.SafeErrorClassInvalidExternalResponse
		_, _ = publisher.Publish(ctx, agent.RunEvent{
			Kind:    agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{Class: class, SafeMessage: "The final answer failed Evidence validation."},
		})
		return agent.RunOutcome{Status: domain.AgentRunStatusFailed, ErrorClass: &class, SafeMessage: "The final answer failed Evidence validation."}
	})
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Inspect the selected Pod."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil || result.Status != domain.AgentRunStatusFailed || <-rejected != agent.EventSinkRejected || len(persistence.diagnoses) != 0 {
		t.Fatalf("unaccepted Diagnosis result = %#v, %v; diagnoses=%d", result, err, len(persistence.diagnoses))
	}
}

func TestCoordinatorRejectsClarificationAfterToolLifecycle(t *testing.T) {
	clock := newCoordinatorClock()
	rejected := make(chan agent.EventSinkResult, 1)
	runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatalf("NewEventPublisher() error = %v", err)
		}
		if result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil || result != agent.EventSinkAccepted {
			t.Fatalf("Publish(started) = %q, %v", result, err)
		}
		purpose := "Inspect one bounded projection."
		arguments := `{"kind":"Pod","name":"sample-pod"}`
		startedAt := clock.Now()
		invocation := domain.ToolInvocation{
			ID: domain.ToolInvocationID(coordinatorUUID(960)), RunID: input.RunID(), Sequence: 1,
			Name: domain.ToolNameGetResource, Version: "tool-v1", Purpose: &purpose,
			Scope: input.Scope().Snapshot(), ArgumentsJSON: arguments, ArgumentsDigest: domain.SHA256Hex(arguments),
			Status: domain.ToolInvocationStatusRequested, StartedAt: &startedAt,
		}
		if result, err := publisher.Publish(ctx, agent.RunEvent{
			Kind: agent.RunEventToolCallRequested, ExternalCallCost: 1, ToolInvocation: &invocation,
		}); err != nil || result != agent.EventSinkAccepted {
			t.Fatalf("Publish(requested) = %q, %v", result, err)
		}
		finishedAt := clock.Now()
		summary := "The bounded read completed without accepted Evidence."
		invocation.Status = domain.ToolInvocationStatusSucceeded
		invocation.ResultSummary = &summary
		invocation.FinishedAt = &finishedAt
		if result, err := publisher.Publish(ctx, agent.RunEvent{
			Kind: agent.RunEventToolCallCompleted, ToolInvocation: &invocation,
		}); err != nil || result != agent.EventSinkAccepted {
			t.Fatalf("Publish(completed) = %q, %v", result, err)
		}
		clarification := domain.ClarificationRequest{
			SchemaVersion: domain.AnswerCompletenessSchemaVersion,
			Questions: []domain.ClarificationQuestion{{
				Sequence: 1, Kind: domain.ClarificationFreeForm, Prompt: "Which exact workload should be inspected?",
			}},
		}
		answer, err := agent.RenderClarificationMarkdown(clarification)
		if err != nil {
			t.Fatalf("RenderClarificationMarkdown() error = %v", err)
		}
		diagnosis := domain.Diagnosis{
			ID: domain.DiagnosisID(coordinatorUUID(961)), RunID: input.RunID(), Scope: input.Scope().Snapshot(),
			AnswerMarkdown: answer, CreatedAt: clock.Now(), Clarification: &clarification,
			Completeness: domain.AnswerCompletenessManifest{
				SchemaVersion: domain.AnswerCompletenessSchemaVersion, ResponseSchemaVersion: 2,
				StopReason: domain.RunTerminalNeedsUserInput, StopReasonBasis: domain.RunTerminalReasonFromClarification,
			},
		}
		if diagnosis.Validate() != nil {
			t.Fatal("clarification fixture is not Domain-valid")
		}
		result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventDiagnosisReady, Diagnosis: &diagnosis})
		if err != nil {
			t.Fatalf("Publish(clarification) error = %v", err)
		}
		rejected <- result
		class := domain.SafeErrorClassInvalidExternalResponse
		_, _ = publisher.Publish(ctx, agent.RunEvent{
			Kind:    agent.RunEventRunFailed,
			Failure: &agent.RunEventFailure{Class: class, SafeMessage: "Clarification was rejected after Tool activity."},
		})
		return agent.RunOutcome{
			Status: domain.AgentRunStatusFailed, ErrorClass: &class,
			SafeMessage: "Clarification was rejected after Tool activity.",
		}
	})
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{
		SessionID: session.ID, Question: "Inspect the selected Pod.",
	})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	result, err := coordinator.WaitRun(context.Background(), runID)
	if err != nil || result.Status != domain.AgentRunStatusFailed || <-rejected != agent.EventSinkRejected ||
		len(persistence.diagnoses) != 0 || persistence.toolCalls() != 1 {
		t.Fatalf("clarification-after-Tool result = %#v, %v; diagnoses=%d tools=%d", result, err, len(persistence.diagnoses), persistence.toolCalls())
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
			Kind: agent.RunEventToolCallRequested, ExternalCallCost: 1, ToolInvocation: &invocation,
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

func TestCoordinatorRejectsUnauthorizedEvidenceBeforePersistence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*domain.Evidence, agent.RunInput)
	}{
		{
			name: "cross-Namespace resource",
			mutate: func(evidence *domain.Evidence, _ agent.RunInput) {
				evidence.Resource.Namespace = "other-namespace"
			},
		},
		{
			name: "stale policy generation",
			mutate: func(evidence *domain.Evidence, input agent.RunInput) {
				evidence.PolicyGeneration = input.PolicyGeneration() + 1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			rejected := make(chan agent.EventSinkResult, 1)
			runner := runnerFunc(func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
				publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
				if err != nil {
					t.Fatalf("NewEventPublisher() error = %v", err)
				}
				if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
					t.Fatalf("Publish(started) error = %v", err)
				}
				purpose := "Inspect one bounded projection."
				arguments := `{"kind":"Pod","name":"sample-pod"}`
				startedAt := clock.Now()
				invocation := domain.ToolInvocation{
					ID: domain.ToolInvocationID(coordinatorUUID(881)), RunID: input.RunID(), Sequence: 1,
					Name: domain.ToolNameGetResource, Version: "tool-v1", Purpose: &purpose,
					Scope: input.Scope().Snapshot(), ArgumentsJSON: arguments, ArgumentsDigest: domain.SHA256Hex(arguments),
					Status: domain.ToolInvocationStatusRequested, StartedAt: &startedAt,
				}
				if result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallRequested, ExternalCallCost: 1, ToolInvocation: &invocation}); err != nil || result != agent.EventSinkAccepted {
					t.Fatalf("Publish(requested) = %q/%v", result, err)
				}
				finishedAt := clock.Now()
				summary := "One projected observation was collected."
				invocation.Status = domain.ToolInvocationStatusSucceeded
				invocation.ResultSummary = &summary
				invocation.EvidenceCount = 1
				invocation.FinishedAt = &finishedAt
				if result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventToolCallCompleted, ToolInvocation: &invocation}); err != nil || result != agent.EventSinkAccepted {
					t.Fatalf("Publish(completed) = %q/%v", result, err)
				}
				evidence := domain.Evidence{
					ID: domain.EvidenceID(coordinatorUUID(882)), RunID: input.RunID(), InvocationID: invocation.ID,
					Category: domain.EvidenceCategoryResourceStatus, Scope: input.Scope().Snapshot(),
					Resource: domain.ResourceRef{
						APIVersion: "v1", Kind: "Pod", Namespace: input.Scope().Namespace, Name: "sample-pod",
					},
					PolicyVersion: domain.ResourcePolicyVersion, PolicyGeneration: input.PolicyGeneration(),
					Fact: "The projected Pod phase is Pending.", Fingerprint: domain.SHA256Hex("unauthorized-evidence"),
					ObservedAt: finishedAt,
				}
				test.mutate(&evidence, input)
				result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventEvidenceCollected, Evidence: &evidence})
				if err != nil {
					t.Fatalf("Publish(Evidence) error = %v", err)
				}
				rejected <- result
				class := domain.SafeErrorClassPolicyDenied
				_, _ = publisher.Publish(ctx, agent.RunEvent{
					Kind: agent.RunEventRunFailed,
					Failure: &agent.RunEventFailure{
						Class: class, SafeMessage: "The AgentRun stopped at the Evidence policy boundary.",
					},
				})
				return agent.RunOutcome{
					Status: domain.AgentRunStatusFailed, ErrorClass: &class,
					SafeMessage: "The AgentRun stopped at the Evidence policy boundary.",
				}
			})
			coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
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
				t.Fatalf("unauthorized Evidence result/tool writes = %#v/%d", result, persistence.toolCalls())
			}
		})
	}
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
		result, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventModelStreamStarted, ModelRequestID: &requestID, ModelPreflight: testModelCallPreflightForInput(input, agent.ModelCallAgent)})
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
func (source *coordinatorIDs) NewCompactionID() (domain.CompactionID, error) {
	return domain.CompactionID(source.id()), nil
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
	contextMessages      map[domain.SessionID][]domain.Message
	contextPending       map[domain.AgentRunID]domain.Message
	contextSteers        map[domain.AgentRunID][]domain.Message
	runInputCount        int
	runInputFailure      bool
	contextSummaries     map[domain.SessionID]domain.SessionContextSummary
	contextReadFailure   bool
	contextWriteFailure  bool
	contextSummaryWrites int
	activityWrites       []time.Time
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

func (persistence *memoryCoordinatorPersistence) AdvanceLastActivity(_ context.Context, id domain.SessionID, activityAt time.Time) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	session, found := persistence.sessions[id]
	if !found || activityAt.IsZero() || activityAt.Location() != time.UTC || activityAt.Before(session.CreatedAt) {
		return errors.New("invalid Session activity")
	}
	if activityAt.After(session.LastActivityAt) {
		session.LastActivityAt = activityAt
		session.UpdatedAt = activityAt
	}
	session.Version++
	persistence.sessions[id] = session
	persistence.activityWrites = append(persistence.activityWrites, activityAt)
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
	delete(persistence.contextMessages, id)
	delete(persistence.contextSummaries, id)
	for runID, message := range persistence.contextPending {
		if message.SessionID == id {
			delete(persistence.contextPending, runID)
			delete(persistence.contextSteers, runID)
		}
	}
	return nil
}

func (persistence *memoryCoordinatorPersistence) ListSessionMetadata(_ context.Context, request SessionListStoreRequest) (SessionListStorePage, error) {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	rows := make([]SessionMetadataRecord, 0, len(persistence.sessions))
	for _, session := range persistence.sessions {
		rows = append(rows, memorySessionMetadata(session))
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].LastActiveUnixMillis > rows[j].LastActiveUnixMillis ||
			rows[i].LastActiveUnixMillis == rows[j].LastActiveUnixMillis && rows[i].ID > rows[j].ID
	})
	if request.BeforeLastActiveMillis != nil {
		filtered := rows[:0]
		for _, row := range rows {
			if row.LastActiveUnixMillis < *request.BeforeLastActiveMillis || row.LastActiveUnixMillis == *request.BeforeLastActiveMillis && row.ID < request.BeforeID {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	if len(rows) > request.Limit {
		rows = rows[:request.Limit]
	}
	return SessionListStorePage{Sessions: rows}, nil
}

func (persistence *memoryCoordinatorPersistence) PreviewSessionDeletion(_ context.Context, request SessionDeletionSelectionRequest) (SessionDeletionSnapshot, error) {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	snapshot := SessionDeletionSnapshot{Request: request, SchemaRevision: 15, Remaining: len(persistence.sessions)}
	for _, session := range persistence.sessions {
		row := memorySessionMetadata(session)
		matched := request.Kind == SessionDeletionExact && session.ID == request.SessionID ||
			request.Kind == SessionDeletionBefore && session.LastActivityAt.Before(request.Cutoff)
		if !matched {
			continue
		}
		snapshot.Matched++
		if session.LastActivityAt.After(request.FrozenNow) || request.Kind == SessionDeletionBefore && session.ID == request.CurrentSessionID {
			snapshot.Protected++
			continue
		}
		snapshot.Eligible++
		snapshot.Selected = append(snapshot.Selected, row)
	}
	sort.Slice(snapshot.Selected, func(i, j int) bool {
		return snapshot.Selected[i].LastActiveUnixMillis < snapshot.Selected[j].LastActiveUnixMillis ||
			snapshot.Selected[i].LastActiveUnixMillis == snapshot.Selected[j].LastActiveUnixMillis && snapshot.Selected[i].ID < snapshot.Selected[j].ID
	})
	snapshot.OverLimit = snapshot.Eligible > request.Limit
	if len(snapshot.Selected) > request.Limit {
		snapshot.Selected = snapshot.Selected[:request.Limit]
	}
	if !snapshot.OverLimit {
		snapshot.Remaining -= len(snapshot.Selected)
	}
	return snapshot, nil
}

func (persistence *memoryCoordinatorPersistence) CommitSessionDeletion(ctx context.Context, expected SessionDeletionSnapshot) (int, error) {
	for _, row := range expected.Selected {
		if err := persistence.DeleteSessionGraph(ctx, row.ID); err != nil {
			return 0, err
		}
	}
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return len(persistence.sessions), nil
}

func (persistence *memoryCoordinatorPersistence) SessionStorageHealth(_ context.Context, now time.Time) (SessionStorageHealth, error) {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	health := SessionStorageHealth{SchemaRevision: 15, SessionCount: len(persistence.sessions)}
	for _, session := range persistence.sessions {
		if session.LastActivityAt.After(now) {
			health.ProtectedActivity++
			health.FutureActivity++
		}
	}
	return health, nil
}

func memorySessionMetadata(session domain.Session) SessionMetadataRecord {
	return SessionMetadataRecord{
		ID: session.ID, Title: session.Title, Status: session.Status, PrivacyMode: session.PrivacyMode,
		Version: session.Version, CreatedAtUnixMillis: session.CreatedAt.UnixMilli(),
		LastActiveUnixMillis: session.LastActivityAt.UnixMilli(), UpdatedAtUnixMillis: session.UpdatedAt.UnixMilli(),
	}
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
	persistence.contextMessages = nil
	persistence.contextPending = nil
	persistence.contextSteers = nil
	persistence.contextSummaries = nil
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
	if session, ok := persistence.sessions[run.SessionID]; ok && session.PrivacyMode == domain.PrivacyModeStandard {
		if persistence.contextPending == nil {
			persistence.contextPending = make(map[domain.AgentRunID]domain.Message)
		}
		persistence.contextPending[run.ID] = message
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
	delete(persistence.contextPending, run.ID)
	delete(persistence.contextSteers, run.ID)
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
	if session, ok := persistence.sessions[run.SessionID]; ok && session.PrivacyMode == domain.PrivacyModeStandard {
		if persistence.contextMessages == nil {
			persistence.contextMessages = make(map[domain.SessionID][]domain.Message)
		}
		request, found := persistence.contextPending[run.ID]
		if !found {
			persistence.mu.Unlock()
			return errors.New("missing pending model-context request")
		}
		persistence.contextMessages[run.SessionID] = append(
			persistence.contextMessages[run.SessionID], request,
		)
		persistence.contextMessages[run.SessionID] = append(
			persistence.contextMessages[run.SessionID], persistence.contextSteers[run.ID]...,
		)
		persistence.contextMessages[run.SessionID] = append(persistence.contextMessages[run.SessionID], message)
	}
	persistence.mu.Unlock()
	return persistence.FinishWithAudit(ctx, run, audit)
}

func (persistence *memoryCoordinatorPersistence) AppendRunInput(_ context.Context, message domain.Message) error {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	persistence.runInputCount++
	if persistence.runInputFailure {
		return errors.New("generated run-input persistence failure")
	}
	if message.Validate() != nil || message.Role != domain.MessageRoleUser || message.RunID == nil ||
		message.RunSequence == nil || *message.RunSequence < 1 {
		return errors.New("invalid committed run input")
	}
	request, found := persistence.contextPending[*message.RunID]
	if !found || request.SessionID != message.SessionID {
		return errors.New("missing active run input")
	}
	wantSequence := 1 + len(persistence.contextSteers[*message.RunID])
	if *message.RunSequence != wantSequence {
		return errors.New("non-sequential run input")
	}
	if persistence.contextSteers == nil {
		persistence.contextSteers = make(map[domain.AgentRunID][]domain.Message)
	}
	persistence.contextSteers[*message.RunID] = append(persistence.contextSteers[*message.RunID], message)
	return nil
}

func (persistence *memoryCoordinatorPersistence) setRunInputFailure(value bool) {
	persistence.mu.Lock()
	persistence.runInputFailure = value
	persistence.mu.Unlock()
}

func (persistence *memoryCoordinatorPersistence) runInputCalls() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.runInputCount
}

func (persistence *memoryCoordinatorPersistence) ListEligibleModelContext(
	ctx context.Context,
	request ModelContextPageRequest,
) (ModelContextPage, error) {
	if err := ctx.Err(); err != nil {
		return ModelContextPage{}, err
	}
	if request.Validate() != nil {
		return ModelContextPage{}, ErrModelContextUnavailable
	}
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if persistence.contextReadFailure {
		return ModelContextPage{}, errors.New("generated model-context read failure")
	}
	values := persistence.contextMessages[request.SessionID]
	start := 0
	if request.After != nil {
		for start < len(values) && values[start].ID != request.After.ID {
			start++
		}
		if start == len(values) {
			return ModelContextPage{}, ErrModelContextUnavailable
		}
		start++
	}
	end := min(start+request.Limit, len(values))
	page := ModelContextPage{Messages: append([]domain.Message(nil), values[start:end]...)}
	if end < len(values) {
		last := page.Messages[len(page.Messages)-1]
		if last.RunID == nil || last.RunSequence == nil {
			return ModelContextPage{}, ErrModelContextUnavailable
		}
		page.Next = &ModelContextCursor{
			RunStartedAt: last.CreatedAt, RunID: *last.RunID, RunSequence: *last.RunSequence, ID: last.ID,
		}
	}
	return page, nil
}

func (persistence *memoryCoordinatorPersistence) LoadSessionContextSummary(
	ctx context.Context,
	sessionID domain.SessionID,
) (domain.SessionContextSummary, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.SessionContextSummary{}, false, err
	}
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	if persistence.contextReadFailure {
		return domain.SessionContextSummary{}, false, errors.New("generated model-context read failure")
	}
	value, ok := persistence.contextSummaries[sessionID]
	return value, ok, nil
}

func (persistence *memoryCoordinatorPersistence) SaveSessionContextSummary(
	ctx context.Context,
	summary domain.SessionContextSummary,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if summary.Validate() != nil {
		return ErrModelContextUnavailable
	}
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	persistence.contextSummaryWrites++
	if persistence.contextWriteFailure {
		return errors.New("generated model-context write failure")
	}
	if persistence.contextSummaries == nil {
		persistence.contextSummaries = make(map[domain.SessionID]domain.SessionContextSummary)
	}
	persistence.contextSummaries[summary.SessionID] = summary
	return nil
}

func (persistence *memoryCoordinatorPersistence) summaryWrites() int {
	persistence.mu.Lock()
	defer persistence.mu.Unlock()
	return persistence.contextSummaryWrites
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
	scope.mu.Lock()
	defer scope.mu.Unlock()
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
	mu                 sync.Mutex
	values             []UIEvent
	rejectConversation func(UIEvent) bool
}

type coordinatorResourcePolicies struct{}

func (coordinatorResourcePolicies) ResourcePolicySnapshot(ctx context.Context) (domain.ResourcePolicyCatalog, domain.PolicyGeneration, bool) {
	if ctx == nil || ctx.Err() != nil {
		return domain.ResourcePolicyCatalog{}, 0, false
	}
	return domain.DefaultResourcePolicyCatalog(), 1, true
}

func (coordinatorResourcePolicies) CurrentPolicyGeneration(ctx context.Context, generation domain.PolicyGeneration) bool {
	return ctx != nil && ctx.Err() == nil && generation == 1
}

func (sink *recordingUIEvents) PublishUIEvent(_ context.Context, event UIEvent) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if event.Validate() != nil {
		return ErrInvalidUIEvent
	}
	if sink.rejectConversation != nil && sink.rejectConversation(event) {
		return errors.New("synthetic UI conversation sink rejection")
	}
	sink.values = append(sink.values, event)
	return nil
}

func (sink *recordingUIEvents) setConversationRejection(reject func(UIEvent) bool) {
	sink.mu.Lock()
	sink.rejectConversation = reject
	sink.mu.Unlock()
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
		Context: "test-context", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7,
		ActivatedAt: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
	}}
	ui := new(recordingUIEvents)
	identifiers := new(coordinatorIDs)
	config := CoordinatorConfig{
		Sessions: persistence, Runs: persistence, Tools: persistence,
		Audits: persistence, ModelContext: persistence,
		Scope: scope, Runner: runner, Identifiers: identifiers, AuditIdentifiers: identifiers,
		Questions: security.NewRedactor(), Privacy: newAcceptedCoordinatorPrivacy(t), UIEvents: ui,
		RunResourcePolicies: coordinatorResourcePolicies{},
		Observer:            RunObserverFunc(func(context.Context, RunObservation) {}), Now: clock.Now,
	}
	if runtime, ok := runner.(ModelRuntime); ok {
		config.Runner = nil
		config.ModelRuntime = runtime
	}
	coordinator, err := NewCoordinator(config)
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	coordinator.sessionManager, err = NewSessionManager(persistence, clock.Now)
	if err != nil {
		t.Fatalf("NewSessionManager() error = %v", err)
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
