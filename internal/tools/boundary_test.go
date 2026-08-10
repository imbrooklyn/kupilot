package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestToolsKeepConsumerOwnedBoundaries(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the package path")
	}
	directory := filepath.Dir(currentFile)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("os.ReadDir() error = %v", err)
	}
	prohibited := []string{
		"github.com/imbrooklyn/kupilot/internal/application",
		"github.com/imbrooklyn/kupilot/internal/kube",
		"github.com/imbrooklyn/kupilot/internal/persistence",
		"github.com/imbrooklyn/kupilot/internal/tui",
		"github.com/cloudwego/eino",
		"k8s.io/",
		"os/exec",
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s) error = %v", entry.Name(), err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.IMPORT {
				continue
			}
			for _, specification := range general.Specs {
				importSpec := specification.(*ast.ImportSpec)
				path, err := strconv.Unquote(importSpec.Path.Value)
				if err != nil {
					t.Fatalf("strconv.Unquote(%s) error = %v", importSpec.Path.Value, err)
				}
				for _, denied := range prohibited {
					if strings.HasPrefix(path, denied) {
						t.Fatalf("%s imports prohibited dependency %q", entry.Name(), path)
					}
				}
			}
		}
	}
}
