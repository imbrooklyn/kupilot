package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/kube"
	"github.com/imbrooklyn/kupilot/internal/persistence/sqlite"
)

func TestRuntimeCompositionClosesInOwnedOrder(t *testing.T) {
	t.Parallel()
	recorder := newCloseRecorder()
	composition := &runtimeComposition{
		coordinator: &recordingCoordinatorClose{name: "coordinator", recorder: recorder},
		scope:       &recordingErrorClose{name: "scope", recorder: recorder},
		database:    &recordingErrorClose{name: "database", recorder: recorder},
		logSink:     &recordingErrorClose{name: "logging", recorder: recorder},
	}
	if err := composition.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	want := []string{"coordinator", "scope", "database", "logging"}
	if got := recorder.values(); !reflect.DeepEqual(got, want) {
		t.Fatalf("close order = %#v, want %#v", got, want)
	}
	if err := composition.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if got := recorder.values(); !reflect.DeepEqual(got, want) {
		t.Fatalf("idempotent close order = %#v", got)
	}
}

func TestRuntimeCompositionDoesNotCloseDependenciesBeforeCoordinatorWait(t *testing.T) {
	t.Parallel()
	recorder := newCloseRecorder()
	waitFailure := errors.New("generated wait failure")
	composition := &runtimeComposition{
		coordinator: &recordingCoordinatorClose{name: "coordinator", recorder: recorder, err: waitFailure},
		scope:       &recordingErrorClose{name: "scope", recorder: recorder},
	}
	if err := composition.Close(context.Background()); !errors.Is(err, waitFailure) {
		t.Fatalf("Close() error = %v", err)
	}
	if got := recorder.values(); !reflect.DeepEqual(got, []string{"coordinator"}) {
		t.Fatalf("close order after wait failure = %#v", got)
	}
}

func TestSecurityAssuranceShutdownAggregationKeepsOnlySafeErrors(t *testing.T) {
	configCanary := strings.Repeat("c", 47) + "-generated"
	configSource := &config.EnvironmentSecretSource{
		LookupEnv: func(string) (string, bool) { return configCanary, true },
		Unsetenv: func(string) error {
			return errors.Join(fmt.Errorf("nested configuration failure: %w", errors.New(configCanary)), errors.New("secondary failure"))
		},
	}
	_, configErr := configSource.Read()
	if configErr == nil {
		t.Fatal("configuration safe error = nil")
	}

	kubeCanary := strings.Repeat("k", 47) + "-generated"
	kubePath := filepath.Join(t.TempDir(), kubeCanary+".yaml")
	if err := os.WriteFile(kubePath, []byte("invalid: ["+kubeCanary), 0o600); err != nil {
		t.Fatalf("os.WriteFile(kubeconfig) error = %v", err)
	}
	t.Setenv("KUBECONFIG", kubePath)
	_, kubeErr := kube.NewConfigLoader().Contexts(context.Background())
	if kubeErr == nil {
		t.Fatal("Kubernetes safe error = nil")
	}

	sqliteCanary := strings.Repeat("s", 47) + "-generated"
	stateDirectory := filepath.Join(t.TempDir(), "uncreated-state")
	_, sqliteErr := sqlite.Open(context.Background(), sqlite.OpenOptions{
		StateDir: stateDirectory, ApplicationVersion: "assurance", CorrelationID: sqliteCanary + "\n",
	})
	if sqliteErr == nil {
		t.Fatal("SQLite safe error = nil")
	}
	if _, err := os.Stat(stateDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid SQLite metadata changed the state path")
	}

	recorder := newCloseRecorder()
	composition := &runtimeComposition{
		scope:    &recordingErrorClose{name: "scope", recorder: recorder, err: kubeErr},
		database: &recordingErrorClose{name: "database", recorder: recorder, err: sqliteErr},
		logSink:  &recordingErrorClose{name: "logging", recorder: recorder, err: configErr},
	}
	joined := composition.Close(context.Background())
	if joined == nil {
		t.Fatal("runtimeComposition.Close() error = nil")
	}
	if got := recorder.values(); !reflect.DeepEqual(got, []string{"scope", "database", "logging"}) {
		t.Fatalf("close order = %#v", got)
	}
	if !errors.Is(joined, configErr) || !errors.Is(joined, kubeErr) || !errors.Is(joined, sqliteErr) {
		t.Fatal("shutdown aggregation lost a safe error identity")
	}
	var configSafe *config.SafeError
	var kubeSafe *kube.SafeError
	var sqliteSafe *sqlite.Error
	if !errors.As(joined, &configSafe) || !errors.As(joined, &kubeSafe) || !errors.As(joined, &sqliteSafe) {
		t.Fatal("shutdown aggregation lost a stable safe error type")
	}
	nested := fmt.Errorf("nested shutdown boundary: %w", joined)
	rejoined := errors.Join(errors.New("secondary safe shutdown failure"), nested)
	formatted := fmt.Sprintf("%s\n%q\n%v\n%+v\n%s", joined, joined, nested, nested, rejoined)
	for _, canary := range []string{configCanary, kubeCanary, sqliteCanary} {
		if strings.Contains(formatted, canary) {
			t.Fatal("formatted or joined shutdown error contains a source canary")
		}
	}
	if err := composition.Close(context.Background()); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if got := recorder.values(); !reflect.DeepEqual(got, []string{"scope", "database", "logging"}) {
		t.Fatalf("idempotent close order = %#v", got)
	}
}

func TestCompositionConstructsOneModelLifecycleAndOneSupervisedRestartPath(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller is unavailable")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	mainContent, err := os.ReadFile(filepath.Join(repositoryRoot, "cmd", "kupilot", "main.go"))
	if err != nil {
		t.Fatalf("os.ReadFile(main.go) error = %v", err)
	}
	mainSource := string(mainContent)
	if strings.Count(mainSource, "openaicompat.New(") != 1 || strings.Count(mainSource, "einoadapter.New(") != 1 ||
		strings.Count(mainSource, "tools.NewReadOnlyToolCatalog(") != 1 ||
		strings.Count(mainSource, "sqlite.NewScopePreferenceRepository(") != 1 ||
		strings.Count(mainSource, "ScopePreferences: scopePreferenceRepository") != 1 ||
		strings.Count(mainSource, "approval.NewService(") != 1 ||
		strings.Count(mainSource, "application.NewApprovalCoordinator(") != 1 ||
		strings.Count(mainSource, "kube.NewDeploymentRestarter(") != 1 ||
		strings.Count(mainSource, "kube.NewDeploymentRolloutObserver(") != 1 {
		t.Fatalf("composition constructor counts are model=%d agent=%d catalog=%d scope-preference=%d",
			strings.Count(mainSource, "openaicompat.New("), strings.Count(mainSource, "einoadapter.New("),
			strings.Count(mainSource, "tools.NewReadOnlyToolCatalog("),
			strings.Count(mainSource, "sqlite.NewScopePreferenceRepository("))
	}
	for _, forbidden := range []string{"einoopenai", "net/http", "kubectl", "os/exec", "dynamic.Interface", "WriteExecutor"} {
		if strings.Contains(mainSource, forbidden) {
			t.Fatalf("composition contains forbidden capability %q", forbidden)
		}
	}

	providerConstructors := 0
	providerPath := ""
	err = filepath.WalkDir(repositoryRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") ||
			strings.Contains(path, string(filepath.Separator)+"vendor"+string(filepath.Separator)) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		count := strings.Count(string(content), "einoopenai.NewChatModel(")
		if count > 0 {
			providerConstructors += count
			providerPath = filepath.ToSlash(path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("filepath.WalkDir() error = %v", err)
	}
	if providerConstructors != 1 || !strings.HasSuffix(providerPath, "internal/llm/openaicompat/adapter.go") {
		t.Fatalf("provider constructors/path = %d/%q", providerConstructors, providerPath)
	}
}

type closeRecorder struct {
	mu    sync.Mutex
	order []string
}

func newCloseRecorder() *closeRecorder { return &closeRecorder{} }

func (recorder *closeRecorder) add(value string) {
	recorder.mu.Lock()
	recorder.order = append(recorder.order, value)
	recorder.mu.Unlock()
}

func (recorder *closeRecorder) values() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.order...)
}

type recordingCoordinatorClose struct {
	name     string
	recorder *closeRecorder
	err      error
}

func (closer *recordingCoordinatorClose) Shutdown(context.Context) error {
	closer.recorder.add(closer.name)
	return closer.err
}

type recordingErrorClose struct {
	name     string
	recorder *closeRecorder
	err      error
}

func (closer *recordingErrorClose) Close() error {
	closer.recorder.add(closer.name)
	return closer.err
}
