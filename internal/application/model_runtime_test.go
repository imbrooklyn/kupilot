package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"go.yaml.in/yaml/v3"
)

func TestModelSetupSecretCannotBeFormattedOrSerialized(t *testing.T) {
	t.Parallel()
	canary := strings.Repeat("s", 41) + "-generated"
	secret, err := NewModelSetupSecret(canary)
	if err != nil {
		t.Fatal(err)
	}
	request := ModelSetupRequest{
		RequestID: 81, Endpoint: "https://model.example.test/v1", Model: "diagnostic-model", Secret: secret,
	}
	formatted := fmt.Sprintf("%s %q %v %+v %#v request=%v request-go=%#v", secret, secret, secret, secret, secret, request, request)
	if strings.Contains(formatted, canary) || !strings.Contains(formatted, redactedModelSecret) {
		t.Fatalf("secret formatting = %q", formatted)
	}
	if value, marshalErr := json.Marshal(request); marshalErr == nil || strings.Contains(string(value), canary) {
		t.Fatalf("json.Marshal(ModelSetupRequest) = %q, %v", value, marshalErr)
	}
	if value, marshalErr := yaml.Marshal(request); marshalErr == nil || strings.Contains(string(value), canary) {
		t.Fatalf("yaml.Marshal(ModelSetupRequest) = %q, %v", value, marshalErr)
	}
	secret.Destroy()
	if secret.IsSet() {
		t.Fatal("destroyed model setup secret remains set")
	}
}

func TestModelSetupSecretRejectsInvalidValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "contains space", "contains\ncontrol", strings.Repeat("x", MaxModelSetupSecretBytes+1)} {
		if secret, err := NewModelSetupSecret(value); !errors.Is(err, ErrModelSetupInvalid) || secret != nil {
			t.Fatalf("NewModelSetupSecret(%q) = %#v, %v", value, secret, err)
		}
	}
}

func TestCoordinatorUnconfiguredModelDeniesRunBeforePersistenceOrAgent(t *testing.T) {
	t.Parallel()
	var runnerCalls atomic.Int64
	coordinator, persistence, scope, _ := newCoordinatorHarness(t, newCoordinatorClock(), runnerFunc(func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome {
		runnerCalls.Add(1)
		return agent.RunOutcome{}
	}))
	coordinator.mu.Lock()
	coordinator.runner = nil
	coordinator.mu.Unlock()
	session := createCoordinatorSession(t, coordinator)
	_, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Inspect the selected Pod."})
	if !errors.Is(err, ErrModelUnconfigured) || persistence.beginCalls() != 0 || runnerCalls.Load() != 0 || scope.bound() {
		t.Fatalf("unconfigured start = error %v begin=%d runner=%d bound=%v", err, persistence.beginCalls(), runnerCalls.Load(), scope.bound())
	}
}

func TestCoordinatorModelSetupConstructsReplacementBeforeCancellingAndAtomicallySwaps(t *testing.T) {
	t.Parallel()
	clock := newCoordinatorClock()
	started := make(chan struct{})
	ended := make(chan struct{})
	oldRuntime := &recordingModelRuntime{name: "old-model", origin: "https://model.example"}
	oldRuntime.run = func(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
		publisher, err := agent.NewEventPublisher(input.RunID(), input.Scope().Generation, clock.Now, sink)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := publisher.Publish(ctx, agent.RunEvent{Kind: agent.RunEventRunStarted}); err != nil {
			t.Fatal(err)
		}
		close(started)
		<-ctx.Done()
		if _, err := publisher.Publish(context.WithoutCancel(ctx), agent.RunEvent{
			Kind: agent.RunEventRunCancelled, TerminationReason: agent.RunTerminationUserCancelled,
		}); err != nil {
			t.Fatal(err)
		}
		close(ended)
		class := domain.SafeErrorClassCancelled
		return agent.RunOutcome{Status: domain.AgentRunStatusCancelled, ErrorClass: &class, SafeMessage: "The Agent run was cancelled."}
	}
	coordinator, _, _, _ := newCoordinatorHarness(t, clock, oldRuntime)
	coordinator.mu.Lock()
	coordinator.modelRuntime = oldRuntime
	coordinator.mu.Unlock()
	replacement := &recordingModelRuntime{name: "new-model", origin: "https://new-model.example"}
	factory := &recordingModelFactory{build: func(ModelSetupRequest) (ModelRuntime, error) {
		select {
		case <-ended:
			t.Fatal("the active run was cancelled before the replacement passed local construction")
		default:
		}
		return replacement, nil
	}}
	profiles := new(recordingModelProfiles)
	coordinator.mu.Lock()
	coordinator.modelFactory = factory
	coordinator.modelProfiles = profiles
	coordinator.mu.Unlock()
	session := createCoordinatorSession(t, coordinator)
	if _, err := coordinator.StartRun(context.Background(), StartRunCommand{SessionID: session.ID, Question: "Inspect the selected Pod."}); err != nil {
		t.Fatal(err)
	}
	<-started
	secret, _ := NewModelSetupSecret("generated-replacement-key")
	result, err := coordinator.ConfigureModel(context.Background(), ModelSetupRequest{
		RequestID: 91, Endpoint: "https://new-model.example/v1", Model: "new-model", Secret: secret,
	})
	if err != nil || result.Validate() != nil || result.Persisted || factory.calls.Load() != 1 ||
		profiles.calls.Load() != 0 || oldRuntime.closed.Load() != 1 || replacement.closed.Load() != 0 || secret.IsSet() {
		t.Fatalf("ConfigureModel() = %#v, %v; factory=%d profiles=%d oldClose=%d newClose=%d secret=%v",
			result, err, factory.calls.Load(), profiles.calls.Load(), oldRuntime.closed.Load(), replacement.closed.Load(), secret.IsSet())
	}
	authorized, err := coordinator.privacy.AuthorizeModel(context.Background())
	if err != nil || authorized {
		t.Fatalf("changed-origin consent = authorized %v error %v", authorized, err)
	}
	if err := coordinator.Shutdown(context.Background()); err != nil || replacement.closed.Load() != 1 {
		t.Fatalf("Shutdown() error=%v replacement closes=%d", err, replacement.closed.Load())
	}
}

func TestCoordinatorModelSetupPersistenceFailureKeepsOldRuntime(t *testing.T) {
	t.Parallel()
	oldRuntime := &recordingModelRuntime{name: "old-model", origin: "https://model.example"}
	coordinator, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), oldRuntime)
	coordinator.modelRuntime = oldRuntime
	replacement := &recordingModelRuntime{name: "new-model", origin: "https://new-model.example"}
	coordinator.modelFactory = &recordingModelFactory{build: func(ModelSetupRequest) (ModelRuntime, error) { return replacement, nil }}
	coordinator.modelProfiles = &recordingModelProfiles{err: errors.New("generated persistence failure")}
	secret, _ := NewModelSetupSecret("generated-persistence-key")
	_, err := coordinator.ConfigureModel(context.Background(), ModelSetupRequest{
		RequestID: 92, Endpoint: "https://new-model.example/v1", Model: "new-model", Persist: true, Secret: secret,
	})
	if !errors.Is(err, ErrModelSetupFailed) || coordinator.runner != oldRuntime || oldRuntime.closed.Load() != 0 ||
		replacement.closed.Load() != 1 || secret.IsSet() {
		t.Fatalf("failed replacement = %v runner=%T oldClose=%d newClose=%d secret=%v", err, coordinator.runner, oldRuntime.closed.Load(), replacement.closed.Load(), secret.IsSet())
	}
}

func TestCoordinatorModelSetupConstructionFailureKeepsOldRuntime(t *testing.T) {
	t.Parallel()
	oldRuntime := &recordingModelRuntime{name: "old-model", origin: "https://model.example"}
	coordinator, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), oldRuntime)
	coordinator.modelRuntime = oldRuntime
	factory := &recordingModelFactory{build: func(ModelSetupRequest) (ModelRuntime, error) {
		return nil, errors.New("generated construction failure")
	}}
	profiles := new(recordingModelProfiles)
	coordinator.modelFactory = factory
	coordinator.modelProfiles = profiles
	secret, _ := NewModelSetupSecret("generated-construction-key")
	_, err := coordinator.ConfigureModel(context.Background(), ModelSetupRequest{
		RequestID: 93, Endpoint: "https://new-model.example/v1", Model: "new-model", Persist: true, Secret: secret,
	})
	if !errors.Is(err, ErrModelSetupFailed) || coordinator.runner != oldRuntime || factory.calls.Load() != 1 ||
		profiles.calls.Load() != 0 || oldRuntime.closed.Load() != 0 || secret.IsSet() {
		t.Fatalf("failed construction = %v runner=%T factory=%d profiles=%d oldClose=%d secret=%v",
			err, coordinator.runner, factory.calls.Load(), profiles.calls.Load(), oldRuntime.closed.Load(), secret.IsSet())
	}
}

func TestCoordinatorModelSetupCancellationDestroysCredentialWithoutDependencies(t *testing.T) {
	t.Parallel()
	oldRuntime := &recordingModelRuntime{name: "old-model", origin: "https://model.example"}
	coordinator, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), oldRuntime)
	coordinator.modelRuntime = oldRuntime
	factory := &recordingModelFactory{build: func(ModelSetupRequest) (ModelRuntime, error) {
		return nil, errors.New("factory must not be called")
	}}
	coordinator.modelFactory = factory
	coordinator.modelProfiles = new(recordingModelProfiles)
	secret, _ := NewModelSetupSecret("generated-cancelled-key")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := coordinator.ConfigureModel(ctx, ModelSetupRequest{
		RequestID: 94, Endpoint: "https://new-model.example/v1", Model: "new-model", Secret: secret,
	})
	if !errors.Is(err, context.Canceled) || factory.calls.Load() != 0 || oldRuntime.closed.Load() != 0 || secret.IsSet() {
		t.Fatalf("cancelled setup = %v factory=%d oldClose=%d secret=%v",
			err, factory.calls.Load(), oldRuntime.closed.Load(), secret.IsSet())
	}
}

func TestCoordinatorModelSetupCancellationAfterConstructionKeepsOldRuntime(t *testing.T) {
	t.Parallel()
	oldRuntime := &recordingModelRuntime{name: "old-model", origin: "https://model.example"}
	coordinator, _, _, _ := newCoordinatorHarness(t, newCoordinatorClock(), oldRuntime)
	coordinator.modelRuntime = oldRuntime
	replacement := &recordingModelRuntime{name: "new-model", origin: "https://new-model.example"}
	started := make(chan struct{})
	release := make(chan struct{})
	factory := &recordingModelFactory{build: func(ModelSetupRequest) (ModelRuntime, error) {
		close(started)
		<-release
		return replacement, nil
	}}
	profiles := new(recordingModelProfiles)
	coordinator.modelFactory = factory
	coordinator.modelProfiles = profiles
	secret, _ := NewModelSetupSecret("generated-in-flight-cancel-key")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := coordinator.ConfigureModel(ctx, ModelSetupRequest{
			RequestID: 95, Endpoint: "https://new-model.example/v1", Model: "new-model", Persist: true, Secret: secret,
		})
		result <- err
	}()
	<-started
	cancel()
	close(release)
	err := <-result
	if !errors.Is(err, context.Canceled) || coordinator.runner != oldRuntime || factory.calls.Load() != 1 ||
		profiles.calls.Load() != 0 || oldRuntime.closed.Load() != 0 || replacement.closed.Load() != 1 || secret.IsSet() {
		t.Fatalf("in-flight cancellation = %v runner=%T factory=%d profiles=%d oldClose=%d newClose=%d secret=%v",
			err, coordinator.runner, factory.calls.Load(), profiles.calls.Load(), oldRuntime.closed.Load(), replacement.closed.Load(), secret.IsSet())
	}
}

type recordingModelRuntime struct {
	name   string
	origin string
	run    func(context.Context, agent.RunInput, agent.EventSink) agent.RunOutcome
	closed atomic.Int64
}

func (runtime *recordingModelRuntime) Run(ctx context.Context, input agent.RunInput, sink agent.EventSink) agent.RunOutcome {
	if runtime.run != nil {
		return runtime.run(ctx, input, sink)
	}
	return agent.RunOutcome{}
}

func (runtime *recordingModelRuntime) Close()            { runtime.closed.Add(1) }
func (runtime *recordingModelRuntime) ModelName() string { return runtime.name }
func (runtime *recordingModelRuntime) Origin() string    { return runtime.origin }

type recordingModelFactory struct {
	build func(ModelSetupRequest) (ModelRuntime, error)
	calls atomic.Int64
}

func (factory *recordingModelFactory) BuildModelRuntime(_ context.Context, request ModelSetupRequest) (ModelRuntime, error) {
	factory.calls.Add(1)
	return factory.build(request)
}

type recordingModelProfiles struct {
	calls atomic.Int64
	err   error
}

func (profiles *recordingModelProfiles) SaveModelProfile(context.Context, ModelSetupRequest) error {
	profiles.calls.Add(1)
	return profiles.err
}
