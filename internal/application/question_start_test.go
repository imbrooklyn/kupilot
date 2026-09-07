package application

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestQuestionStartTypedPrecommitDenialsCallNoModelOrRunPersistence(t *testing.T) {
	tests := []struct {
		name       string
		want       QuestionStartFailureReason
		recovery   QuestionStartRecoveryAction
		configure  func(*Coordinator, *memoryCoordinatorPersistence, *controlledConversationRunner, domain.Session)
		command    func(domain.Session) UICommand
		wantBegins int
		wantReview bool
	}{
		{
			name: "current Session unavailable", want: QuestionStartSessionUnavailable,
			recovery: QuestionStartRecoverStartOrResumeSession,
			command: func(domain.Session) UICommand {
				return questionStartCommand(domain.SessionID(coordinatorUUID(909)), 7, 1)
			},
		},
		{
			name: "scope not verified", want: QuestionStartScopeNotVerified,
			recovery: QuestionStartRecoverSelectScope,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.scope = &questionStartUnavailableScope{}
			},
		},
		{
			name: "scope generation one behind", want: QuestionStartScopeGenerationStale,
			recovery: QuestionStartRecoverSubmitAgain,
			command: func(session domain.Session) UICommand {
				return questionStartCommand(session.ID, 6, 1)
			},
		},
		{
			name: "scope generation one ahead", want: QuestionStartScopeGenerationStale,
			recovery: QuestionStartRecoverSubmitAgain,
			command: func(session domain.Session) UICommand {
				return questionStartCommand(session.ID, 8, 1)
			},
		},
		{
			name: "selected resource stale", want: QuestionStartSelectedResourceStale,
			recovery: QuestionStartRecoverSelectResource,
			command: func(session domain.Session) UICommand {
				command := questionStartCommand(session.ID, 7, 1)
				command.Resource = &domain.ResourceRef{
					APIVersion: "v1", Kind: string(domain.ResourceKindPod), Namespace: "other-team", Name: "sample",
				}
				return command
			},
		},
		{
			name: "policy generation one behind", want: QuestionStartPolicyGenerationStale,
			recovery: QuestionStartRecoverReviewPolicy,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.runResourcePolicies = fixedQuestionStartPolicies{generation: 7, current: true}
			},
			command: func(session domain.Session) UICommand {
				return questionStartCommand(session.ID, 7, 6)
			},
		},
		{
			name: "policy generation one ahead", want: QuestionStartPolicyGenerationStale,
			recovery: QuestionStartRecoverReviewPolicy,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.runResourcePolicies = fixedQuestionStartPolicies{generation: 7, current: true}
			},
			command: func(session domain.Session) UICommand {
				return questionStartCommand(session.ID, 7, 8)
			},
		},
		{
			name: "policy snapshot invalid", want: QuestionStartPolicySnapshotInvalid,
			recovery: QuestionStartRecoverRunDoctor,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.runResourcePolicies = fixedQuestionStartPolicies{generation: 1, current: false}
			},
		},
		{
			name: "run starting", want: QuestionStartRunStarting,
			recovery: QuestionStartRecoverWait,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.mu.Lock()
				coordinator.starting = true
				coordinator.mu.Unlock()
			},
		},
		{
			name: "bounded Application operation active", want: QuestionStartApplicationOperationActive,
			recovery: QuestionStartRecoverWait,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.mu.Lock()
				coordinator.operations = 1
				coordinator.mu.Unlock()
			},
		},
		{
			name: "persistence degraded", want: QuestionStartPersistenceDegraded,
			recovery: QuestionStartRecoverRunDoctor,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.mu.Lock()
				coordinator.persistenceDegraded = true
				coordinator.mu.Unlock()
			},
		},
		{
			name: "precommit persistence failure", want: QuestionStartPrecommitPersistenceFailed,
			recovery: QuestionStartRecoverRunDoctor, wantBegins: 1,
			configure: func(_ *Coordinator, persistence *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				persistence.mu.Lock()
				persistence.beginFailure = true
				persistence.mu.Unlock()
			},
		},
		{
			name: "model configuration missing", want: QuestionStartModelConfigurationMissing,
			recovery: QuestionStartRecoverConfigureModel,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.mu.Lock()
				coordinator.runner = nil
				coordinator.mu.Unlock()
			},
		},
		{
			name: "consent required", want: QuestionStartConsentRequired,
			recovery: QuestionStartRecoverReviewConsent, wantReview: true,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.privacy.FailClosed()
			},
		},
		{
			name: "model origin changed", want: QuestionStartConsentRequired,
			recovery: QuestionStartRecoverReviewConsent, wantReview: true,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				_ = coordinator.privacy.ReconfigureOrigin("https://changed-model.example")
			},
		},
		{
			name: "locally rejected input", want: QuestionStartInputRejected,
			recovery: QuestionStartRecoverEditInput,
			command: func(session domain.Session) UICommand {
				command := questionStartCommand(session.ID, 7, 1)
				command.Text = strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ") + "\nsynthetic\n" +
					strings.Join([]string{"-----END", "PRIVATE", "KEY-----"}, " ")
				return command
			},
		},
		{
			name: "unknown internal safe failure", want: QuestionStartUnknownSafeFailure,
			recovery: QuestionStartRecoverRunDoctor,
			configure: func(coordinator *Coordinator, _ *memoryCoordinatorPersistence, _ *controlledConversationRunner, _ domain.Session) {
				coordinator.identifiers = questionStartFailingIdentifiers{}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := newCoordinatorClock()
			runner := newControlledConversationRunner(clock)
			coordinator, persistence, _, ui := newCoordinatorHarness(t, clock, runner)
			session := createCoordinatorSession(t, coordinator)
			if test.configure != nil {
				test.configure(coordinator, persistence, runner, session)
			}
			command := questionStartCommand(session.ID, 7, 1)
			if test.command != nil {
				command = test.command(session)
			}
			coordinator.mu.Lock()
			beforeQueue := coordinator.conversationInputStatusLocked()
			coordinator.mu.Unlock()

			outcome, err := coordinator.ExecuteUICommand(context.Background(), command)
			if err != nil {
				t.Fatalf("ExecuteUICommand() error = %v", err)
			}
			if outcome.Validate() != nil || outcome.QuestionStart == nil ||
				outcome.QuestionStart.Reason != test.want || outcome.QuestionStart.Recovery != test.recovery {
				t.Fatalf("typed outcome = %#v", outcome)
			}
			if (outcome.Privacy != nil) != test.wantReview {
				t.Fatalf("privacy review present = %t, want %t", outcome.Privacy != nil, test.wantReview)
			}
			if got := runner.calls.Load(); got != 0 {
				t.Fatalf("model calls = %d, want 0", got)
			}
			persistence.mu.Lock()
			begins := persistence.beginCount
			persistence.mu.Unlock()
			if begins != test.wantBegins {
				t.Fatalf("run precommit writes = %d, want %d", begins, test.wantBegins)
			}
			if events := ui.events(); len(events) != 0 {
				t.Fatalf("question denial emitted run events: %#v", events)
			}
			coordinator.mu.Lock()
			queue := coordinator.conversationInputStatusLocked()
			coordinator.mu.Unlock()
			if queue.Items != beforeQueue.Items || queue.Bytes != beforeQueue.Bytes || queue.Revision != beforeQueue.Revision {
				t.Fatalf("question denial changed the follow-up queue: before %#v after %#v", beforeQueue, queue)
			}
		})
	}
}

func TestQuestionStartAcceptsExactCurrentGenerationsAndStartsOnce(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)

	outcome, err := coordinator.ExecuteUICommand(context.Background(), questionStartCommand(session.ID, 7, 1))
	if err != nil || outcome.Validate() != nil || outcome.QuestionStart != nil || !outcome.RunID.Valid() {
		t.Fatalf("exact current start = %#v, %v", outcome, err)
	}
	input := waitConversationRunInput(t, runner)
	if input.SessionID() != session.ID || input.Scope().Generation != 7 || input.PolicyGeneration() != 1 ||
		runner.calls.Load() != 1 {
		t.Fatalf("accepted input = session %q scope %d policy %d calls %d", input.SessionID(), input.Scope().Generation,
			input.PolicyGeneration(), runner.calls.Load())
	}
	persistence.mu.Lock()
	begins := persistence.beginCount
	persistence.mu.Unlock()
	if begins != 1 {
		t.Fatalf("run precommit writes = %d, want 1", begins)
	}
	runner.outcomes <- controlledConversationFail
	if _, err := coordinator.WaitRun(context.Background(), outcome.RunID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func TestQuestionStartDetectsScopeAndPolicyActivationRacesWithoutRetry(t *testing.T) {
	t.Run("scope activation race", func(t *testing.T) {
		clock := newCoordinatorClock()
		runner := newControlledConversationRunner(clock)
		coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
		session := createCoordinatorSession(t, coordinator)
		coordinator.scope = newQuestionStartSequenceScope(
			domain.ClusterScope{Context: "test-context", Namespace: "team-a", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 7},
			domain.ClusterScope{Context: "next-context", Namespace: "team-b", NamespaceAccess: domain.NamespaceAccessCurrent, Generation: 8},
		)

		outcome, err := coordinator.ExecuteUICommand(context.Background(), questionStartCommand(session.ID, 7, 1))
		assertQuestionStartRaceDenial(t, outcome, err, QuestionStartScopeGenerationStale, runner, persistence)
		if outcome.QuestionStart.State.ScopeGeneration != 8 || outcome.QuestionStart.State.Context != "next-context" ||
			outcome.QuestionStart.State.Namespace != "team-b" {
			t.Fatalf("current scope projection = %#v", outcome.QuestionStart.State)
		}
	})

	t.Run("policy activation race", func(t *testing.T) {
		clock := newCoordinatorClock()
		runner := newControlledConversationRunner(clock)
		coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
		session := createCoordinatorSession(t, coordinator)
		coordinator.runResourcePolicies = &sequenceQuestionStartPolicies{generations: []domain.PolicyGeneration{7, 8}}

		outcome, err := coordinator.ExecuteUICommand(context.Background(), questionStartCommand(session.ID, 7, 7))
		assertQuestionStartRaceDenial(t, outcome, err, QuestionStartPolicyGenerationStale, runner, persistence)
		if outcome.QuestionStart.State.PolicyGeneration != 8 {
			t.Fatalf("current policy projection = %#v", outcome.QuestionStart.State)
		}
	})
}

func TestQuestionStartReportsApplicationActiveRunWithoutSecondModelCall(t *testing.T) {
	clock := newCoordinatorClock()
	runner := newControlledConversationRunner(clock)
	coordinator, persistence, _, _ := newCoordinatorHarness(t, clock, runner)
	session := createCoordinatorSession(t, coordinator)
	runID, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "First question."})
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	_ = waitConversationRunInput(t, runner)

	outcome, err := coordinator.ExecuteUICommand(context.Background(), questionStartCommand(session.ID, 7, 1))
	if err != nil || outcome.Validate() != nil || outcome.QuestionStart == nil ||
		outcome.QuestionStart.Reason != QuestionStartRunActive ||
		outcome.QuestionStart.Recovery != QuestionStartRecoverSteerOrQueue {
		t.Fatalf("active-run outcome = %#v, %v", outcome, err)
	}
	state := outcome.QuestionStart.State
	if state.RunState != QuestionStartRunStateRunning || state.RunID != runID ||
		state.RunScopeGeneration != 7 || state.RunPolicyGeneration != 1 || state.RunSequence < 1 {
		t.Fatalf("active-run current state = %#v", state)
	}
	if runner.calls.Load() != 1 {
		t.Fatalf("model calls = %d, want the existing call only", runner.calls.Load())
	}
	persistence.mu.Lock()
	begins := persistence.beginCount
	persistence.mu.Unlock()
	if begins != 1 {
		t.Fatalf("run precommit writes = %d, want the existing write only", begins)
	}
	runner.outcomes <- controlledConversationFail
	if _, err := coordinator.WaitRun(context.Background(), runID); err != nil {
		t.Fatalf("WaitRun() error = %v", err)
	}
}

func questionStartCommand(sessionID domain.SessionID, scopeGeneration int64, policyGeneration domain.PolicyGeneration) UICommand {
	return UICommand{
		Kind: UICommandSubmitQuestion, RequestID: 73, SessionID: sessionID,
		Text: "Why is the workload unavailable?", ExpectedScopeGeneration: scopeGeneration,
		ExpectedPolicyGeneration: policyGeneration,
	}
}

func assertQuestionStartRaceDenial(
	t *testing.T,
	outcome UICommandOutcome,
	err error,
	want QuestionStartFailureReason,
	runner *controlledConversationRunner,
	persistence *memoryCoordinatorPersistence,
) {
	t.Helper()
	if err != nil || outcome.Validate() != nil || outcome.QuestionStart == nil || outcome.QuestionStart.Reason != want {
		t.Fatalf("race outcome = %#v, %v", outcome, err)
	}
	if runner.calls.Load() != 0 {
		t.Fatalf("model calls = %d, want 0", runner.calls.Load())
	}
	persistence.mu.Lock()
	begins := persistence.beginCount
	persistence.mu.Unlock()
	if begins != 0 {
		t.Fatalf("run precommit writes = %d, want 0", begins)
	}
}

type questionStartUnavailableScope struct{}

func (*questionStartUnavailableScope) CurrentScope() (domain.ClusterScope, bool) {
	return domain.ClusterScope{}, false
}
func (*questionStartUnavailableScope) BindRun(domain.ClusterScope, domain.AgentRunID, context.CancelFunc) error {
	return errors.New("scope unavailable")
}
func (*questionStartUnavailableScope) UnbindRun(domain.AgentRunID) {}

type questionStartSequenceScope struct {
	mu     sync.Mutex
	values []domain.ClusterScope
	index  int
}

func newQuestionStartSequenceScope(values ...domain.ClusterScope) *questionStartSequenceScope {
	return &questionStartSequenceScope{values: values}
}

func (scope *questionStartSequenceScope) CurrentScope() (domain.ClusterScope, bool) {
	scope.mu.Lock()
	defer scope.mu.Unlock()
	if len(scope.values) == 0 {
		return domain.ClusterScope{}, false
	}
	index := min(scope.index, len(scope.values)-1)
	value := scope.values[index]
	if scope.index < len(scope.values)-1 {
		scope.index++
	}
	return value, true
}

func (*questionStartSequenceScope) BindRun(domain.ClusterScope, domain.AgentRunID, context.CancelFunc) error {
	return nil
}
func (*questionStartSequenceScope) UnbindRun(domain.AgentRunID) {}

type fixedQuestionStartPolicies struct {
	generation domain.PolicyGeneration
	current    bool
}

func (source fixedQuestionStartPolicies) ResourcePolicySnapshot(context.Context) (domain.ResourcePolicyCatalog, domain.PolicyGeneration, bool) {
	return domain.DefaultResourcePolicyCatalog(), source.generation, source.current
}

func (source fixedQuestionStartPolicies) CurrentPolicyGeneration(_ context.Context, generation domain.PolicyGeneration) bool {
	return source.current && generation == source.generation
}

type sequenceQuestionStartPolicies struct {
	calls       atomic.Int64
	generations []domain.PolicyGeneration
}

func (source *sequenceQuestionStartPolicies) ResourcePolicySnapshot(context.Context) (domain.ResourcePolicyCatalog, domain.PolicyGeneration, bool) {
	call := int(source.calls.Add(1)) - 1
	call = min(call, len(source.generations)-1)
	return domain.DefaultResourcePolicyCatalog(), source.generations[call], true
}

func (source *sequenceQuestionStartPolicies) CurrentPolicyGeneration(_ context.Context, generation domain.PolicyGeneration) bool {
	return generation == source.generations[len(source.generations)-1]
}

type questionStartFailingIdentifiers struct{}

func (questionStartFailingIdentifiers) NewSessionID() (domain.SessionID, error) {
	return "", ErrIdentifierUnavailable
}
func (questionStartFailingIdentifiers) NewMessageID() (domain.MessageID, error) {
	return "", ErrIdentifierUnavailable
}
func (questionStartFailingIdentifiers) NewAgentRunID() (domain.AgentRunID, error) {
	return "", ErrIdentifierUnavailable
}
func (questionStartFailingIdentifiers) NewCompactionID() (domain.CompactionID, error) {
	return "", ErrIdentifierUnavailable
}
