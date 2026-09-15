package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type interactionConsentStore struct {
	load  func() (PrivacyRecord, bool, error)
	calls int
}

func (store *interactionConsentStore) LoadPrivacy(context.Context) (PrivacyRecord, bool, error) {
	store.calls++
	return store.load()
}
func (*interactionConsentStore) SavePrivacy(context.Context, PrivacyRecord) error {
	return errors.New("unexpected consent write")
}

func TestInteractionConsentReturnRechecksIdentityScopeAndAuthorization(t *testing.T) {
	for _, scenario := range []string{"revoked", "storage failure", "scope changed", "concurrent event"} {
		t.Run(scenario, func(t *testing.T) {
			clock := newCoordinatorClock()
			ready := make(chan agent.RunInput, 1)
			release := make(chan struct{})
			runner := runnerFunc(func(_ context.Context, input agent.RunInput, _ agent.EventSink) agent.RunOutcome {
				ready <- input
				<-release
				return agent.RunOutcome{}
			})
			coordinator, persistence, scope, ui := newCoordinatorHarness(t, clock, runner)
			session := createCoordinatorSession(t, coordinator)
			runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Inspect the selected Pod."})
			if err != nil {
				t.Fatal(err)
			}
			input := <-ready
			started := agent.RunEvent{RunID: runID, ScopeGeneration: 7, Sequence: 1, OccurredAt: clock.Now(), Kind: agent.RunEventRunStarted}
			if result := coordinator.Publish(context.Background(), started); result != agent.EventSinkAccepted {
				t.Fatalf("start = %s", result)
			}
			store := &interactionConsentStore{}
			store.load = func() (PrivacyRecord, bool, error) {
				switch scenario {
				case "storage failure":
					return PrivacyRecord{}, false, errors.New("synthetic consent storage failure")
				case "scope changed":
					scope.mu.Lock()
					scope.scope.Generation++
					scope.mu.Unlock()
				case "concurrent event":
					event := agent.RunEvent{RunID: runID, ScopeGeneration: 7, Sequence: 2, OccurredAt: clock.Now(), Kind: agent.RunEventTextDelta, TextDelta: "Safe provisional text."}
					if result := coordinator.Publish(context.Background(), event); result != agent.EventSinkAccepted {
						t.Errorf("concurrent event = %s", result)
					}
				}
				return PrivacyRecord{}, false, nil
			}
			privacy, err := NewPrivacyManager(PrivacyManagerConfig{Store: store, Origin: "https://model.example", Now: clock.Now})
			if err != nil {
				t.Fatal(err)
			}
			coordinator.privacy = privacy
			requestID := domain.ModelRequestID(coordinatorUUID(950))
			event := agent.RunEvent{RunID: runID, ScopeGeneration: 7, Sequence: 2, OccurredAt: clock.Now(), Kind: agent.RunEventModelStreamStarted, ModelRequestID: &requestID, ModelPreflight: testModelCallPreflightForInput(input, agent.ModelCallAgent)}
			want := agent.EventSinkPreflightRejected
			if scenario == "scope changed" {
				want = agent.EventSinkStaleRejected
			}
			if scenario == "concurrent event" {
				want = agent.EventSinkRejected
			}
			result := coordinator.Publish(context.Background(), event)
			close(release)
			if result != want || store.calls != 1 {
				t.Fatalf("recheck = %s, reads = %d; want %s/1", result, store.calls, want)
			}
			terminal, err := coordinator.WaitRun(context.Background(), runID)
			if err != nil || terminal.Status == domain.AgentRunStatusCompleted || len(persistence.diagnoses) != 0 {
				t.Fatalf("rejected preflight committed success: %#v, %v", terminal, err)
			}
			for _, accepted := range ui.events() {
				if accepted.Kind == UIEventModelEgress {
					t.Fatal("rejected boundary emitted model egress")
				}
			}
			assertOneUITerminal(t, ui.events(), UIEventRunFailed)
			// The runner has no model, Tool, Kubernetes, Reviewer or executor
			// dependency. A rejected preflight cannot produce an external call.
		})
	}
}

func TestInteractionForcedTerminalRejectsInvalidStateWithoutGhostRows(t *testing.T) {
	const runID domain.AgentRunID = "00000000-0000-7000-8000-000000000101"
	for _, scenario := range []string{"nil bridge", "cancelled", "terminal", "unknown reason"} {
		t.Run(scenario, func(t *testing.T) {
			var events []UIEvent
			bridge, err := newEventBridge(runID, 7, 1, UIEventSinkFunc(func(_ context.Context, event UIEvent) error { events = append(events, event); return nil }))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			diagnostic := domain.FailureInternal
			switch scenario {
			case "nil bridge":
				bridge = nil
			case "cancelled":
				cancel()
			case "terminal":
				bridge.terminal = true
			case "unknown reason":
				diagnostic = "unknown"
			}
			if err := bridge.forceFailed(ctx, domain.RunTerminalFailed, diagnostic); !errors.Is(err, ErrInvalidUIEvent) || len(events) != 0 {
				t.Fatalf("invalid force = %v, rows = %d", err, len(events))
			}
		})
	}
	var events []UIEvent
	bridge, err := newEventBridge(runID, 7, 1, UIEventSinkFunc(func(_ context.Context, event UIEvent) error { events = append(events, event); return nil }))
	if err != nil {
		t.Fatal(err)
	}
	for index, kind := range []agent.RunEventKind{agent.RunEventRunStarted, agent.RunEventRunInterrupted} {
		event := agent.RunEvent{RunID: runID, ScopeGeneration: 7, Sequence: int64(index + 1), OccurredAt: time.UnixMilli(1000).UTC(), Kind: kind}
		if kind == agent.RunEventRunInterrupted {
			event.Diagnostic = domain.FailureInternal
			event.TerminationReason = agent.RunTerminationInterrupted
		}
		if err := bridge.accept(context.Background(), event); err != nil {
			t.Fatalf("interrupted projection = %v", err)
		}
	}
	if len(events) != 2 || events[1].TerminalOutcome == nil || events[1].TerminalOutcome.Reason != domain.RunTerminalUnknown || events[1].TerminalOutcome.Diagnostic != domain.FailureInternal {
		t.Fatalf("interrupted terminal = %#v", events)
	}
}
