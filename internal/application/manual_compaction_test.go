package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

type manualCompactorFunc func(context.Context, agent.ManualCompactionInput, agent.ManualCompactionEventSink) (agent.ManualCompactionResult, error)

func (function manualCompactorFunc) Compact(
	ctx context.Context,
	input agent.ManualCompactionInput,
	sink agent.ManualCompactionEventSink,
) (agent.ManualCompactionResult, error) {
	return function(ctx, input, sink)
}

func manualSummary(t testing.TB, input agent.ManualCompactionInput, generatedAt time.Time, covered int) domain.SessionContextSummary {
	t.Helper()
	coverage := input.Conversation().Coverage()
	if covered < 2 || covered > len(coverage) {
		t.Fatalf("invalid manual summary coverage %d/%d", covered, len(coverage))
	}
	digest, coveredBytes, err := domain.SessionContextCoverageDigestItems(coverage[:covered])
	if err != nil {
		t.Fatalf("SessionContextCoverageDigestItems() error = %v", err)
	}
	const summaryText = "Earlier committed turns were summarized for the current Session without restoring authority."
	return domain.SessionContextSummary{
		SessionID: input.SessionID(), Text: summaryText, SummaryHash: domain.SHA256Hex(summaryText),
		SchemaVersion: domain.SessionContextSummarySchemaVersion, PolicyVersion: domain.SafeConversationContextPolicyVersion,
		CoveredFirstID: coverage[0].MessageID, CoveredThroughID: coverage[covered-1].MessageID,
		CoveredCount: covered, CoveredBytes: coveredBytes, CoverageDigest: digest,
		GeneratedAt: generatedAt.UTC().Truncate(time.Millisecond), AgentProfile: input.Profile(), AgentOriginHash: input.OriginHash(),
	}
}

func successfulManualCompactor(t testing.TB, clock *coordinatorClock, calls *atomic.Int64, covered int) agent.ContextCompactor {
	t.Helper()
	return manualCompactorFunc(func(ctx context.Context, input agent.ManualCompactionInput, sink agent.ManualCompactionEventSink) (agent.ManualCompactionResult, error) {
		calls.Add(1)
		requestID := domain.ModelRequestID(coordinatorUUID(98_001))
		if got := sink.AcceptManualCompaction(ctx, agent.ManualCompactionEvent{
			ID: input.ID(), SessionID: input.SessionID(), ScopeGeneration: input.Scope().Generation,
			PolicyGeneration: input.PolicyGeneration(), Sequence: 1, Kind: agent.ManualCompactionStarted,
			ModelRequestID: &requestID,
		}); got != agent.EventSinkAccepted {
			return agent.ManualCompactionResult{}, errors.New("manual compaction start rejected")
		}
		summary := manualSummary(t, input, clock.Now(), covered)
		if got := sink.AcceptManualCompaction(ctx, agent.ManualCompactionEvent{
			ID: input.ID(), SessionID: input.SessionID(), ScopeGeneration: input.Scope().Generation,
			PolicyGeneration: input.PolicyGeneration(), Sequence: 2, Kind: agent.ManualCompactionReady,
			Summary: &summary,
		}); got != agent.EventSinkAccepted {
			return agent.ManualCompactionResult{}, errors.New("manual compaction commit rejected")
		}
		return agent.ManualCompactionResult{Summary: &summary}, nil
	})
}

func loadSeededContextInMemory(t testing.TB, coordinator *Coordinator, persistence *memoryCoordinatorPersistence, session domain.Session) {
	t.Helper()
	persistence.mu.Lock()
	messages := append([]domain.Message(nil), persistence.contextMessages[session.ID]...)
	persistence.mu.Unlock()
	turns := make([]agent.ConversationTurn, len(messages))
	coverage := make([]domain.SessionContextCoverageItem, len(messages))
	totalBytes := 0
	for index, message := range messages {
		turn, err := eligibleConversationTurn(message)
		if err != nil {
			t.Fatalf("eligibleConversationTurn(%d) error = %v", index, err)
		}
		turns[index] = turn
		coverage[index] = domain.SessionContextCoverageItem{
			MessageID: message.ID, RunID: *message.RunID, RunSequence: *message.RunSequence,
			Role: message.Role, ContentHash: message.Hash, ContentBytes: len(message.Content),
		}
		totalBytes += len(message.Content)
	}
	coordinator.mu.Lock()
	coordinator.modelContext.turns = turns
	coordinator.modelContext.coverage = coverage
	coordinator.modelContext.eligibleBytes = totalBytes
	coordinator.modelContext.recentTailCount = len(turns)
	coordinator.modelContext.loaded = true
	coordinator.modelContext.storageHealthy = true
	coordinator.mu.Unlock()
}

func TestManualCompactionCommitsExactCoverageWithoutStartingRunOrQueueDrain(t *testing.T) {
	clock := newCoordinatorClock()
	var runnerCalls, compactCalls atomic.Int64
	runner := runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		runnerCalls.Add(1)
		return agent.RunOutcome{}
	})
	coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	seedCoordinatorContext(t, coordinator, persistence, session, 20)
	coordinator.compactor = successfulManualCompactor(t, clock, &compactCalls, 4)

	result, err := coordinator.RequestManualCompaction(context.Background(), RequestManualCompactionCommand{
		SessionID: session.ID, ExpectedScopeGeneration: scope.scope.Generation, ExpectedPolicyGeneration: 1,
	})
	if err != nil || result.Noop || result.CoveredMessages != 4 || result.RecentTailMessages != 16 {
		t.Fatalf("RequestManualCompaction() = %#v, %v", result, err)
	}
	coordinator.mu.Lock()
	contextState := coordinator.modelContext
	queueStatus := coordinator.conversationInputStatusLocked()
	coordinator.mu.Unlock()
	if compactCalls.Load() != 1 || runnerCalls.Load() != 0 || persistence.beginCalls() != 0 ||
		persistence.summaryWrites() != 1 || contextState.summary == nil || contextState.summary.CoveredCount != 4 ||
		len(contextState.turns) != 16 || queueStatus.Items != 0 {
		t.Fatalf("manual compaction side effects: compactor=%d runner=%d begins=%d writes=%d context=%#v queue=%#v",
			compactCalls.Load(), runnerCalls.Load(), persistence.beginCalls(), persistence.summaryWrites(), contextState, queueStatus)
	}
}

func TestManualCompactionFailurePreservesCommittedState(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*memoryCoordinatorPersistence)
		compactor  func(*testing.T, *coordinatorClock, *atomic.Int64) agent.ContextCompactor
		want       error
		wantWrites int
	}{
		{
			name: "model failure",
			compactor: func(_ *testing.T, _ *coordinatorClock, calls *atomic.Int64) agent.ContextCompactor {
				return manualCompactorFunc(func(context.Context, agent.ManualCompactionInput, agent.ManualCompactionEventSink) (agent.ManualCompactionResult, error) {
					calls.Add(1)
					return agent.ManualCompactionResult{}, errors.New("generated summary failure")
				})
			},
			want: ErrManualCompactionFailed,
		},
		{
			name: "adapter failure after ready",
			compactor: func(t *testing.T, clock *coordinatorClock, calls *atomic.Int64) agent.ContextCompactor {
				successful := successfulManualCompactor(t, clock, calls, 4)
				return manualCompactorFunc(func(ctx context.Context, input agent.ManualCompactionInput, sink agent.ManualCompactionEventSink) (agent.ManualCompactionResult, error) {
					result, err := successful.Compact(ctx, input, sink)
					if err != nil {
						return agent.ManualCompactionResult{}, err
					}
					return result, errors.New("adapter failed after publishing ready")
				})
			},
			want: ErrManualCompactionFailed,
		},
		{
			name:      "persistence failure",
			configure: func(persistence *memoryCoordinatorPersistence) { persistence.contextWriteFailure = true },
			compactor: func(t *testing.T, clock *coordinatorClock, calls *atomic.Int64) agent.ContextCompactor {
				return successfulManualCompactor(t, clock, calls, 4)
			},
			want: ErrPersistenceUnavailable, wantWrites: 1,
		},
	}
	for _, current := range tests {
		t.Run(current.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			var compactCalls atomic.Int64
			coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
				t.Fatal("manual compaction started an AgentRun")
				return agent.RunOutcome{}
			}))
			session := createCoordinatorSession(t, coordinator)
			seedCoordinatorContext(t, coordinator, persistence, session, 20)
			if _, err := coordinator.conversationForRun(context.Background(), session.ID); err != nil {
				t.Fatalf("conversationForRun() error = %v", err)
			}
			coordinator.mu.Lock()
			before, err := coordinator.modelContext.conversation()
			coordinator.mu.Unlock()
			if err != nil {
				t.Fatalf("model context before compaction = %v", err)
			}
			if current.configure != nil {
				current.configure(persistence)
			}
			coordinator.compactor = current.compactor(t, clock, &compactCalls)
			_, err = coordinator.RequestManualCompaction(context.Background(), RequestManualCompactionCommand{
				SessionID: session.ID, ExpectedScopeGeneration: scope.scope.Generation, ExpectedPolicyGeneration: 1,
			})
			if !errors.Is(err, current.want) {
				t.Fatalf("RequestManualCompaction() error = %v, want %v", err, current.want)
			}
			coordinator.mu.Lock()
			after, afterErr := coordinator.modelContext.conversation()
			coordinator.mu.Unlock()
			if afterErr != nil || !conversationContextsEqual(before, after) || persistence.summaryWrites() != current.wantWrites || compactCalls.Load() != 1 {
				t.Fatalf("failed compaction changed state: equal=%t error=%v writes=%d calls=%d",
					conversationContextsEqual(before, after), afterErr, persistence.summaryWrites(), compactCalls.Load())
			}
		})
	}
}

func TestManualCompactionCancellationAfterReadyPreservesCommittedState(t *testing.T) {
	clock := newCoordinatorClock()
	var compactCalls atomic.Int64
	coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		t.Fatal("manual compaction started an AgentRun")
		return agent.RunOutcome{}
	}))
	session := createCoordinatorSession(t, coordinator)
	seedCoordinatorContext(t, coordinator, persistence, session, 20)
	if _, err := coordinator.conversationForRun(context.Background(), session.ID); err != nil {
		t.Fatalf("conversationForRun() error = %v", err)
	}
	coordinator.mu.Lock()
	before, err := coordinator.modelContext.conversation()
	coordinator.mu.Unlock()
	if err != nil {
		t.Fatalf("model context before compaction = %v", err)
	}
	operationContext, cancel := context.WithCancel(context.Background())
	successful := successfulManualCompactor(t, clock, &compactCalls, 4)
	coordinator.compactor = manualCompactorFunc(func(ctx context.Context, input agent.ManualCompactionInput, sink agent.ManualCompactionEventSink) (agent.ManualCompactionResult, error) {
		result, compactErr := successful.Compact(ctx, input, sink)
		if compactErr != nil {
			return agent.ManualCompactionResult{}, compactErr
		}
		cancel()
		return result, ctx.Err()
	})
	_, err = coordinator.RequestManualCompaction(operationContext, RequestManualCompactionCommand{
		SessionID: session.ID, ExpectedScopeGeneration: scope.scope.Generation, ExpectedPolicyGeneration: 1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RequestManualCompaction() error = %v, want context.Canceled", err)
	}
	coordinator.mu.Lock()
	after, afterErr := coordinator.modelContext.conversation()
	coordinator.mu.Unlock()
	if afterErr != nil || !conversationContextsEqual(before, after) || persistence.summaryWrites() != 0 || compactCalls.Load() != 1 {
		t.Fatalf("cancelled compaction changed state: equal=%t error=%v writes=%d calls=%d",
			conversationContextsEqual(before, after), afterErr, persistence.summaryWrites(), compactCalls.Load())
	}
}

func TestManualCompactionRejectsBusyStaleAndCancelledBeforeCompactor(t *testing.T) {
	clock := newCoordinatorClock()
	var compactCalls atomic.Int64
	coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, newControlledConversationRunner(clock))
	session := createCoordinatorSession(t, coordinator)
	seedCoordinatorContext(t, coordinator, persistence, session, 20)
	coordinator.compactor = manualCompactorFunc(func(context.Context, agent.ManualCompactionInput, agent.ManualCompactionEventSink) (agent.ManualCompactionResult, error) {
		compactCalls.Add(1)
		return agent.ManualCompactionResult{Noop: true}, nil
	})
	for _, current := range []struct {
		name    string
		ctx     func() context.Context
		command RequestManualCompactionCommand
		want    error
	}{
		{
			name: "stale scope", ctx: context.Background,
			command: RequestManualCompactionCommand{SessionID: session.ID, ExpectedScopeGeneration: scope.scope.Generation + 1, ExpectedPolicyGeneration: 1},
			want:    ErrScopeUnavailable,
		},
		{
			name: "stale policy", ctx: context.Background,
			command: RequestManualCompactionCommand{SessionID: session.ID, ExpectedScopeGeneration: scope.scope.Generation, ExpectedPolicyGeneration: 2},
			want:    ErrManualCompactionUnavailable,
		},
		{
			name: "cancelled", ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx },
			command: RequestManualCompactionCommand{SessionID: session.ID, ExpectedScopeGeneration: scope.scope.Generation, ExpectedPolicyGeneration: 1},
			want:    context.Canceled,
		},
	} {
		t.Run(current.name, func(t *testing.T) {
			if _, err := coordinator.RequestManualCompaction(current.ctx(), current.command); !errors.Is(err, current.want) {
				t.Fatalf("RequestManualCompaction() error = %v, want %v", err, current.want)
			}
		})
	}
	if compactCalls.Load() != 0 || persistence.summaryWrites() != 0 {
		t.Fatalf("rejected compaction calls/writes = %d/%d", compactCalls.Load(), persistence.summaryWrites())
	}
}

func TestManualCompactionRejectsBusyConsentAndOriginBeforeCompactor(t *testing.T) {
	for _, current := range []struct {
		name      string
		configure func(*testing.T, *Coordinator, *controlledConversationRunner, domain.Session) domain.AgentRunID
		want      error
	}{
		{
			name: "active run",
			configure: func(t *testing.T, coordinator *Coordinator, runner *controlledConversationRunner, session domain.Session) domain.AgentRunID {
				runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Keep this run active."})
				if err != nil {
					t.Fatalf("StartRun() error = %v", err)
				}
				_ = waitConversationRunInput(t, runner)
				return runID
			},
			want: ErrRunAlreadyActive,
		},
		{
			name: "missing consent",
			configure: func(_ *testing.T, coordinator *Coordinator, _ *controlledConversationRunner, _ domain.Session) domain.AgentRunID {
				coordinator.privacy.FailClosed()
				return ""
			},
			want: ErrConsentRequired,
		},
		{
			name: "changed origin",
			configure: func(t *testing.T, coordinator *Coordinator, _ *controlledConversationRunner, _ domain.Session) domain.AgentRunID {
				if err := coordinator.privacy.ReconfigureOrigin("https://replacement.example"); err != nil {
					t.Fatalf("ReconfigureOrigin() error = %v", err)
				}
				return ""
			},
			want: ErrConsentRequired,
		},
	} {
		t.Run(current.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			runner := newControlledConversationRunner(clock)
			var compactCalls atomic.Int64
			coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, runner)
			session := createCoordinatorSession(t, coordinator)
			seedCoordinatorContext(t, coordinator, persistence, session, 20)
			coordinator.compactor = manualCompactorFunc(func(context.Context, agent.ManualCompactionInput, agent.ManualCompactionEventSink) (agent.ManualCompactionResult, error) {
				compactCalls.Add(1)
				return agent.ManualCompactionResult{Noop: true}, nil
			})
			activeRun := current.configure(t, coordinator, runner, session)
			_, err := coordinator.RequestManualCompaction(context.Background(), RequestManualCompactionCommand{
				SessionID: session.ID, ExpectedScopeGeneration: scope.scope.Generation, ExpectedPolicyGeneration: 1,
			})
			if !errors.Is(err, current.want) || compactCalls.Load() != 0 || persistence.summaryWrites() != 0 {
				t.Fatalf("rejected compaction = %v; compactor=%d writes=%d", err, compactCalls.Load(), persistence.summaryWrites())
			}
			if activeRun.Valid() {
				runner.outcomes <- controlledConversationFail
				if _, err = coordinator.WaitRun(context.Background(), activeRun); err != nil {
					t.Fatalf("WaitRun() error = %v", err)
				}
			}
		})
	}
}

func TestManualCompactionRejectsBindingChangeBeforePersistence(t *testing.T) {
	for _, change := range []string{"profile", "origin"} {
		t.Run(change, func(t *testing.T) {
			clock := newCoordinatorClock()
			runner := newControlledConversationRunner(clock)
			coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, runner)
			session := createCoordinatorSession(t, coordinator)
			seedCoordinatorContext(t, coordinator, persistence, session, 20)
			if _, err := coordinator.conversationForRun(context.Background(), session.ID); err != nil {
				t.Fatalf("conversationForRun() error = %v", err)
			}
			coordinator.mu.Lock()
			before, err := coordinator.modelContext.conversation()
			coordinator.mu.Unlock()
			if err != nil {
				t.Fatalf("model context before compaction = %v", err)
			}
			coordinator.compactor = manualCompactorFunc(func(ctx context.Context, input agent.ManualCompactionInput, sink agent.ManualCompactionEventSink) (agent.ManualCompactionResult, error) {
				requestID := domain.ModelRequestID(coordinatorUUID(98_101))
				if sink.AcceptManualCompaction(ctx, agent.ManualCompactionEvent{
					ID: input.ID(), SessionID: input.SessionID(), ScopeGeneration: input.Scope().Generation,
					PolicyGeneration: input.PolicyGeneration(), Sequence: 1, Kind: agent.ManualCompactionStarted,
					ModelRequestID: &requestID,
				}) != agent.EventSinkAccepted {
					return agent.ManualCompactionResult{}, errors.New("manual compaction start rejected")
				}
				if change == "profile" {
					runner.profile.Store("replacement-agent")
				} else if err := coordinator.privacy.ReconfigureOrigin("https://replacement.example"); err != nil {
					return agent.ManualCompactionResult{}, err
				}
				summary := manualSummary(t, input, clock.Now(), 4)
				if sink.AcceptManualCompaction(ctx, agent.ManualCompactionEvent{
					ID: input.ID(), SessionID: input.SessionID(), ScopeGeneration: input.Scope().Generation,
					PolicyGeneration: input.PolicyGeneration(), Sequence: 2, Kind: agent.ManualCompactionReady,
					Summary: &summary,
				}) != agent.EventSinkRejected {
					return agent.ManualCompactionResult{}, errors.New("stale compaction was accepted")
				}
				return agent.ManualCompactionResult{}, errors.New("binding changed")
			})
			if _, err = coordinator.RequestManualCompaction(context.Background(), RequestManualCompactionCommand{
				SessionID: session.ID, ExpectedScopeGeneration: scope.scope.Generation, ExpectedPolicyGeneration: 1,
			}); !errors.Is(err, ErrManualCompactionFailed) {
				t.Fatalf("RequestManualCompaction() error = %v", err)
			}
			coordinator.mu.Lock()
			after, afterErr := coordinator.modelContext.conversation()
			coordinator.mu.Unlock()
			if afterErr != nil || !conversationContextsEqual(before, after) || persistence.summaryWrites() != 0 {
				t.Fatalf("binding change altered committed context: equal=%t error=%v writes=%d",
					conversationContextsEqual(before, after), afterErr, persistence.summaryWrites())
			}
		})
	}
}

func TestManualCompactionInMinimalModeUpdatesOnlyCurrentProcessContext(t *testing.T) {
	clock := newCoordinatorClock()
	var compactCalls atomic.Int64
	coordinator, persistence, scope, _ := newCoordinatorHarness(t, clock, runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		t.Fatal("manual compaction started an AgentRun")
		return agent.RunOutcome{}
	}))
	session, err := coordinator.CreateSession(context.Background(), CreateSessionCommand{PrivacyMode: domain.PrivacyModeMinimal})
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	seedCoordinatorContext(t, coordinator, persistence, session, 20)
	loadSeededContextInMemory(t, coordinator, persistence, session)
	coordinator.compactor = successfulManualCompactor(t, clock, &compactCalls, 4)
	result, err := coordinator.RequestManualCompaction(context.Background(), RequestManualCompactionCommand{
		SessionID: session.ID, ExpectedScopeGeneration: scope.scope.Generation, ExpectedPolicyGeneration: 1,
	})
	if err != nil || result.CoveredMessages != 4 || result.RecentTailMessages != 16 || compactCalls.Load() != 1 ||
		persistence.summaryWrites() != 0 {
		t.Fatalf("minimal compaction = %#v, %v; calls=%d writes=%d", result, err, compactCalls.Load(), persistence.summaryWrites())
	}
	coordinator.mu.Lock()
	summary := coordinator.modelContext.summary
	coordinator.mu.Unlock()
	if summary == nil || summary.CoveredCount != 4 {
		t.Fatalf("minimal current-process summary = %#v", summary)
	}
}
