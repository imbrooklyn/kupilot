package config

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestTerminalStatusTitlesDefaultAndExplicitDisable(t *testing.T) {
	if !Defaults().TerminalStatusTitles {
		t.Fatal("terminal status titles must default to enabled")
	}
	root := t.TempDir()
	paths := testPaths(root)
	document := strings.Replace(version2Config("", ""), "version: 2\n", "version: 2\nterminal_status_titles: false\n", 1)
	writePrivateFile(t, paths.ConfigFile, []byte(document))
	loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer loaded.Credentials.Destroy()
	if loaded.TerminalStatusTitles {
		t.Fatal("explicit terminal status title disable was ignored")
	}
}

func TestModelProfileWritePreservesTerminalStatusTitleDisable(t *testing.T) {
	root := t.TempDir()
	paths := pathsForHome(root)
	secret, err := NewSecretValue("terminal-title-test-key")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	base := Defaults()
	base.TerminalStatusTitles = false
	if err = SaveModelProfile(context.Background(), paths, base, ModelProfile{
		Endpoint: "https://model.example.test/v1", Model: "diagnostic-model",
	}, &secret); err != nil {
		t.Fatalf("SaveModelProfile() error = %v", err)
	}
	content, err := os.ReadFile(paths.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "terminal_status_titles: false") {
		t.Fatalf("saved configuration omitted disabled title policy: %s", content)
	}
}
