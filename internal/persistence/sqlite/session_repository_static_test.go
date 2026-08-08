package sqlite

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRepositorySourcesKeepExplicitSQLBoundary(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() did not return the test path")
	}
	directory := filepath.Dir(currentFile)
	files := []string{
		"session_repository.go",
		"message_repository.go",
		"run_repository.go",
	}
	for _, name := range files {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("ReadFile(%q) error = %v", name, err)
		}
		text := string(content)
		for _, forbidden := range []string{
			"SELECT " + "*",
			".Unsafe(",
			"MustExec",
			"MustBegin",
			"SelectContext(",
			"map[string]" + "any",
			".Exec(",
			".Query(",
			".Get(",
		} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s contains forbidden repository API or SQL %q", name, forbidden)
			}
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, "cwd") || strings.Contains(lower, "workspace") {
			t.Errorf("%s contains a prohibited directory-scoped resume term", name)
		}
	}
}
