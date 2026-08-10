package application

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

func TestApplicationImportBoundaryIsStatic(t *testing.T) {
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
	prohibited := []string{
		"database/sql", "net/http", "os/exec",
		"charm.land/bubbletea", "github.com/cloudwego/eino", "github.com/jmoiron/sqlx",
		"k8s.io/", "modernc.org/sqlite",
		"github.com/imbrooklyn/kupilot/internal/agent/einoadapter",
		"github.com/imbrooklyn/kupilot/internal/cli",
		"github.com/imbrooklyn/kupilot/internal/kube",
		"github.com/imbrooklyn/kupilot/internal/llm",
		"github.com/imbrooklyn/kupilot/internal/persistence",
		"github.com/imbrooklyn/kupilot/internal/tools",
		"github.com/imbrooklyn/kupilot/internal/tui",
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s) error = %v", entry.Name(), err)
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("strconv.Unquote(%s) error = %v", imported.Path.Value, err)
			}
			for _, denied := range prohibited {
				if value == denied || strings.HasPrefix(value, denied) {
					t.Fatalf("%s imports prohibited dependency %q", entry.Name(), value)
				}
			}
		}
	}
}
