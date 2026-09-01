package agent_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/agent/einoadapter"
)

func TestDomainDoesNotReintroduceNeutralModelProtocolDTOs(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the test path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	domainRoot := filepath.Join(repositoryRoot, "internal", "domain")
	forbidden := map[string]bool{
		"ModelMessage":           true,
		"ModelMessageRole":       true,
		"ModelRequest":           true,
		"ModelToolCall":          true,
		"ModelToolSpecification": true,
	}
	err := filepath.WalkDir(domainRoot, func(path string, entry os.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			specification, ok := node.(*ast.TypeSpec)
			if ok && forbidden[specification.Name.Name] {
				t.Errorf("%s reintroduces neutral model protocol DTO %s", path, specification.Name.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
}

func TestEinoImportsRemainInTheSoleTranslationBoundary(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the test path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	internalRoot := filepath.Join(repositoryRoot, "internal")
	err := filepath.WalkDir(internalRoot, func(path string, entry os.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		relative, err := filepath.Rel(repositoryRoot, path)
		if err != nil {
			return err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			inAgentAdapter := strings.HasPrefix(relative, filepath.Join("internal", "agent", "einoadapter")+string(filepath.Separator))
			if strings.HasPrefix(importPath, "github.com/cloudwego/eino") && !inAgentAdapter {
				t.Errorf("%s imports Eino outside an admitted adapter: %s", relative, importPath)
			}
			if importPath == "github.com/imbrooklyn/kupilot/internal/llm/openaicompat" {
				t.Errorf("%s imports the removed parallel model adapter: %s", relative, importPath)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
}

func TestExportedAgentAdapterBoundaryContainsNoEinoTypes(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(einoadapter.Config{}),
		reflect.TypeOf(einoadapter.New),
		reflect.TypeOf((*einoadapter.Adapter)(nil)),
	}
	for _, current := range types {
		assertNoEinoType(t, current, map[reflect.Type]bool{})
		if current.Kind() != reflect.Interface && current.Kind() != reflect.Pointer {
			continue
		}
		for index := 0; index < current.NumMethod(); index++ {
			assertNoEinoType(t, current.Method(index).Type, map[reflect.Type]bool{})
		}
	}
}

func assertNoEinoType(t *testing.T, current reflect.Type, seen map[reflect.Type]bool) {
	t.Helper()
	if current == nil || seen[current] {
		return
	}
	seen[current] = true
	if strings.HasPrefix(current.PkgPath(), "github.com/cloudwego/eino") {
		t.Fatalf("exported Agent adapter boundary contains Eino type %s", current)
	}
	switch current.Kind() {
	case reflect.Array, reflect.Chan, reflect.Pointer, reflect.Slice:
		assertNoEinoType(t, current.Elem(), seen)
	case reflect.Func:
		for index := 0; index < current.NumIn(); index++ {
			assertNoEinoType(t, current.In(index), seen)
		}
		for index := 0; index < current.NumOut(); index++ {
			assertNoEinoType(t, current.Out(index), seen)
		}
	case reflect.Interface:
		for index := 0; index < current.NumMethod(); index++ {
			assertNoEinoType(t, current.Method(index).Type, seen)
		}
	case reflect.Map:
		assertNoEinoType(t, current.Key(), seen)
		assertNoEinoType(t, current.Elem(), seen)
	case reflect.Struct:
		for index := 0; index < current.NumField(); index++ {
			field := current.Field(index)
			if field.IsExported() {
				assertNoEinoType(t, field.Type, seen)
			}
		}
	}
}
