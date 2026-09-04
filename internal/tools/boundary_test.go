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

	"github.com/imbrooklyn/kupilot/internal/domain"
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

func TestReadOnlyToolPathContainsNoWriteShellOrGenericKubernetesEscape(t *testing.T) {
	t.Parallel()

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the package path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	paths := []string{
		filepath.Join(repositoryRoot, "internal", "kube", "resources.go"),
		filepath.Join(repositoryRoot, "internal", "tools", "contracts.go"),
		filepath.Join(repositoryRoot, "internal", "tools", "catalog.go"),
		filepath.Join(repositoryRoot, "internal", "tools", "get_resource.go"),
		filepath.Join(repositoryRoot, "internal", "tools", "list_resources.go"),
		filepath.Join(repositoryRoot, "internal", "tools", "get_events.go"),
		filepath.Join(repositoryRoot, "internal", "tools", "get_pod_logs.go"),
		filepath.Join(repositoryRoot, "internal", "tools", "get_previous_pod_logs.go"),
		filepath.Join(repositoryRoot, "internal", "tools", "get_related_resources.go"),
		filepath.Join(repositoryRoot, "internal", "agent", "einoadapter", "tool_bridge.go"),
	}
	deniedImports := []string{
		"os/exec",
		"k8s.io/client-go/dynamic",
		"k8s.io/client-go/discovery",
		"k8s.io/client-go/rest",
	}
	deniedCalls := map[string]bool{
		"Create": true, "Update": true, "UpdateStatus": true, "Patch": true,
		"Delete": true, "DeleteCollection": true, "Watch": true,
		"RESTClient": true, "AbsPath": true, "Do": true, "DoRaw": true,
	}
	for _, path := range paths {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s) error = %v", path, err)
		}
		for _, importSpec := range file.Imports {
			importPath, err := strconv.Unquote(importSpec.Path.Value)
			if err != nil {
				t.Fatalf("strconv.Unquote(%s) error = %v", importSpec.Path.Value, err)
			}
			for _, denied := range deniedImports {
				if strings.HasPrefix(importPath, denied) {
					t.Fatalf("%s imports prohibited capability %q", filepath.Base(path), importPath)
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && deniedCalls[selector.Sel.Name] {
				t.Fatalf("%s calls prohibited method %s", filepath.Base(path), selector.Sel.Name)
			}
			return true
		})
	}
}

func TestEvidenceTypeDoesNotReplaceInvalidExplicitAPIIdentity(t *testing.T) {
	t.Parallel()

	invalid := domain.ResourceType{
		ID: "pods", Version: "v1", Resource: "secrets", Kind: "Pod",
		Scope: domain.ResourceScopeNamespaced, BuiltIn: true,
	}
	if got := effectiveEvidenceResourceType(evidenceTemplate{resourceType: invalid}); got != invalid {
		t.Fatalf("effective Evidence resource type = %#v, want invalid explicit identity %#v", got, invalid)
	}
	legacy := domain.ResourceRef{APIVersion: "v1", Kind: "Pod", Namespace: "team-a", Name: "sample-pod"}
	if got := effectiveEvidenceResourceType(evidenceTemplate{resource: legacy}); got != domain.BuiltInResourceType(domain.ResourceKindPod) {
		t.Fatalf("legacy inferred Evidence resource type = %#v", got)
	}
}
