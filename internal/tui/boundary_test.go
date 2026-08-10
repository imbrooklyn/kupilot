package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestTUIImportBoundaryAndSingleTextareaAreStatic(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller is unavailable")
	}
	root := filepath.Dir(currentFile)
	files := token.NewFileSet()
	editorModels := 0
	goStatements := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, parseErr := parser.ParseFile(files, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		for _, imported := range parsed.Imports {
			value, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				return unquoteErr
			}
			if forbiddenTUIImport(value) {
				t.Errorf("forbidden TUI import %q in %s", value, filepath.Base(path))
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch current := node.(type) {
			case *ast.GoStmt:
				goStatements++
			case *ast.SelectorExpr:
				identifier, isIdentifier := current.X.(*ast.Ident)
				if isIdentifier && identifier.Name == "textarea" && current.Sel.Name == "Model" {
					editorModels++
				}
				if isIdentifier && identifier.Name == "textinput" && current.Sel.Name == "Model" {
					editorModels++
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk TUI source: %v", err)
	}
	if editorModels != 1 {
		t.Fatalf("editable component model count = %d, want 1", editorModels)
	}
	if goStatements != 0 {
		t.Fatalf("TUI owns %d goroutines, want 0", goStatements)
	}
}

func forbiddenTUIImport(path string) bool {
	for _, forbidden := range []string{
		"database/sql", "net/http", "os/exec",
		"github.com/cloudwego/eino", "github.com/jmoiron/sqlx",
		"k8s.io/", "modernc.org/sqlite",
		"github.com/imbrooklyn/kupilot/internal/agent",
		"github.com/imbrooklyn/kupilot/internal/kube",
		"github.com/imbrooklyn/kupilot/internal/llm",
		"github.com/imbrooklyn/kupilot/internal/persistence",
		"github.com/imbrooklyn/kupilot/internal/tools",
	} {
		if path == forbidden || strings.HasPrefix(path, forbidden) {
			return true
		}
	}
	return false
}
