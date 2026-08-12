package approval

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestApprovalPackageHasNoInfrastructureOrDeliveryImports(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller is unavailable")
	}
	directory := filepath.Dir(currentFile)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v", err)
	}
	forbidden := []string{
		"charm.land/",
		"github.com/cloudwego/eino",
		"github.com/jmoiron/sqlx",
		"k8s.io/",
		"internal/application",
		"internal/kube",
		"internal/persistence",
		"internal/tui",
		"internal/tools",
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s) error = %v", entry.Name(), err)
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("strconv.Unquote(%s) error = %v", imported.Path.Value, err)
			}
			for _, denied := range forbidden {
				if strings.Contains(value, denied) {
					t.Errorf("%s imports prohibited boundary %q", entry.Name(), value)
				}
			}
		}
	}
}

func TestServiceImplementsNarrowApprovalPort(t *testing.T) {
	t.Parallel()
	var _ ApprovalService = (*Service)(nil)
	var _ RestartDeploymentExecutor = (*fakeRestartExecutor)(nil)
}
