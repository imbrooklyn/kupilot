package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestRuntimeCompositionClosesInOwnedOrder(t *testing.T) {
	t.Parallel()
	recorder := newCloseRecorder()
	composition := &runtimeComposition{
		coordinator: &recordingCoordinatorClose{name: "coordinator", recorder: recorder},
		agent:       &recordingWaitClose{name: "agent", recorder: recorder},
		model:       &recordingWaitClose{name: "model", recorder: recorder},
		scope:       &recordingErrorClose{name: "scope", recorder: recorder},
		database:    &recordingErrorClose{name: "database", recorder: recorder},
		logSink:     &recordingErrorClose{name: "logging", recorder: recorder},
	}
	if err := composition.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	want := []string{"coordinator", "agent", "model", "scope", "database", "logging"}
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

func TestRuntimeCompositionDoesNotCloseAgentOrModelBeforeRunWait(t *testing.T) {
	t.Parallel()
	recorder := newCloseRecorder()
	waitFailure := errors.New("generated wait failure")
	composition := &runtimeComposition{
		coordinator: &recordingCoordinatorClose{name: "coordinator", recorder: recorder, err: waitFailure},
		agent:       &recordingWaitClose{name: "agent", recorder: recorder},
		model:       &recordingWaitClose{name: "model", recorder: recorder},
	}
	if err := composition.Close(context.Background()); !errors.Is(err, waitFailure) {
		t.Fatalf("Close() error = %v", err)
	}
	if got := recorder.values(); !reflect.DeepEqual(got, []string{"coordinator"}) {
		t.Fatalf("close order after wait failure = %#v", got)
	}
}

func TestCompositionConstructsOneModelLifecycleAndNoWritePath(t *testing.T) {
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
		strings.Count(mainSource, "tools.NewReadOnlyToolCatalog(") != 1 {
		t.Fatalf("composition constructor counts are model=%d agent=%d catalog=%d",
			strings.Count(mainSource, "openaicompat.New("), strings.Count(mainSource, "einoadapter.New("),
			strings.Count(mainSource, "tools.NewReadOnlyToolCatalog("))
	}
	for _, forbidden := range []string{"einoopenai", "net/http", "restart_deployment", "WriteExecutor", "ApprovalCoordinator"} {
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

type recordingWaitClose struct {
	name     string
	recorder *closeRecorder
}

func (closer *recordingWaitClose) Close() { closer.recorder.add(closer.name) }

type recordingErrorClose struct {
	name     string
	recorder *closeRecorder
}

func (closer *recordingErrorClose) Close() error {
	closer.recorder.add(closer.name)
	return nil
}
