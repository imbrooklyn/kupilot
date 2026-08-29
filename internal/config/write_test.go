package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSaveModelProfileCreatesPrivateHomeConfigAndLoadExtractsCredential(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new-home")
	paths := pathsForHome(root)
	secret, err := NewSecretValue(strings.Repeat("k", 37) + "-generated")
	if err != nil {
		t.Fatal(err)
	}
	defer secret.Destroy()
	base := Defaults()
	base.Model.ReasoningEffort = ModelReasoningEffortNone
	base.Logging.SensitiveDiagnostics = true
	if err := SaveModelProfile(context.Background(), paths, base, ModelProfile{
		Endpoint: "https://model.example.test/v1", Model: "diagnostic-model",
	}, &secret); err != nil {
		t.Fatalf("SaveModelProfile() error = %v", err)
	}
	if runtime.GOOS != "windows" {
		assertPathMode(t, root, 0o700)
		assertPathMode(t, paths.ConfigFile, 0o600)
	}
	loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer loaded.Credential.Destroy()
	if loaded.Model.Endpoint != "https://model.example.test/v1" || loaded.Model.Model != "diagnostic-model" ||
		loaded.Model.ReasoningEffort != ModelReasoningEffortNone || loaded.CredentialSource != CredentialSourceFile ||
		!loaded.Credential.IsSet() || !loaded.Logging.SensitiveDiagnostics {
		t.Fatalf("loaded model profile = %#v source=%q credential=%v", loaded.Model, loaded.CredentialSource, loaded.Credential.IsSet())
	}
	encoded, err := json.Marshal(loaded.Config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "generated") {
		t.Fatal("non-sensitive Config serialization contains credential data")
	}
}

func TestLoadEnvironmentCredentialOverridesFileAndIsUnsetOnce(t *testing.T) {
	root := t.TempDir()
	paths := pathsForHome(root)
	writePrivateFile(t, paths.ConfigFile, []byte("version: 1\nmodel:\n  endpoint: https://file.example.test/v1\n  model: file-model\n  api_key: file-generated-key\n"))
	environment := map[string]string{
		"KUPILOT_MODEL_ENDPOINT":       "https://environment.example.test/v1",
		"KUPILOT_MODEL":                "environment-model",
		ModelAPIKeyEnvironmentVariable: "environment-generated-key",
	}
	unsetCalls := 0
	loaded, err := Load(context.Background(), LoadOptions{
		Paths: paths, LookupEnv: lookupMap(environment),
		Unsetenv: func(name string) error {
			unsetCalls++
			delete(environment, name)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer loaded.Credential.Destroy()
	if loaded.Model.Endpoint != "https://environment.example.test/v1" || loaded.Model.Model != "environment-model" ||
		loaded.CredentialSource != CredentialSourceEnvironment || unsetCalls != 1 {
		t.Fatalf("environment precedence = %#v source=%q unset=%d", loaded.Model, loaded.CredentialSource, unsetCalls)
	}
}

func TestSaveModelProfileRespectsExistingWideHomeAndRejectsSymlinkTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode and symlink contract")
	}
	root := filepath.Join(t.TempDir(), "user-home")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths := pathsForHome(root)
	if err := os.Symlink(outside, paths.ConfigFile); err != nil {
		t.Fatal(err)
	}
	secret, _ := NewSecretValue("generated-local-key")
	defer secret.Destroy()
	err := SaveModelProfile(context.Background(), paths, Defaults(), ModelProfile{
		Endpoint: "https://model.example.test/v1", Model: "diagnostic-model",
	}, &secret)
	assertSafeError(t, err, ClassConfigurationInvalid, "config_file_unsafe")
	content, readErr := os.ReadFile(outside)
	if readErr != nil || string(content) != "outside" {
		t.Fatal("symlink target was modified")
	}
	assertPathMode(t, root, 0o755)
}

func TestSaveModelProfilePreservesExistingUserManagedConfigMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode contract")
	}
	root := t.TempDir()
	paths := pathsForHome(root)
	if err := os.WriteFile(paths.ConfigFile, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret, _ := NewSecretValue("generated-local-key")
	defer secret.Destroy()
	if err := SaveModelProfile(context.Background(), paths, Defaults(), ModelProfile{
		Endpoint: "https://model.example.test/v1", Model: "diagnostic-model",
	}, &secret); err != nil {
		t.Fatalf("SaveModelProfile() error = %v", err)
	}
	assertPathMode(t, paths.ConfigFile, 0o644)
}

func TestEnsureHomeCreatesPrivateHomeAndRespectsExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode contract")
	}
	parent := t.TempDir()
	created := filepath.Join(parent, "created")
	if err := EnsureHome(context.Background(), pathsForHome(created)); err != nil {
		t.Fatalf("EnsureHome(created) error = %v", err)
	}
	assertPathMode(t, created, 0o700)

	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	paths := pathsForHome(existing)
	paths.HomePermissionsWider = true
	if err := EnsureHome(context.Background(), paths); err != nil {
		t.Fatalf("EnsureHome(existing) error = %v", err)
	}
	assertPathMode(t, existing, 0o755)
}

func TestSaveModelProfileCancellationCreatesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := filepath.Join(t.TempDir(), "cancelled-home")
	secret, _ := NewSecretValue("generated-local-key")
	defer secret.Destroy()
	err := SaveModelProfile(ctx, pathsForHome(root), Defaults(), ModelProfile{
		Endpoint: "https://model.example.test/v1", Model: "diagnostic-model",
	}, &secret)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SaveModelProfile() error = %v", err)
	}
	if _, statErr := os.Lstat(root); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled write created Home: %v", statErr)
	}
}

func TestPublishConfigurationCancellationBeforePublicationLeavesNoPartialFile(t *testing.T) {
	root := t.TempDir()
	paths := pathsForHome(root)
	ctx, cancel := context.WithCancel(context.Background())
	err := publishConfigurationWithHook(ctx, paths, []byte("version: 1\n"), cancel)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("publishConfigurationWithHook() error = %v", err)
	}
	if _, statErr := os.Lstat(paths.ConfigFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled publication created configuration: %v", statErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(root, ".config.yaml.tmp-*"))
	if globErr != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, %v", matches, globErr)
	}
}

func TestPublishConfigurationRejectsTargetReplacement(t *testing.T) {
	root := t.TempDir()
	paths := pathsForHome(root)
	if err := os.WriteFile(paths.ConfigFile, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	replacement := []byte("version: 1\ncontext: replacement\n")
	err := publishConfigurationWithHook(context.Background(), paths, []byte("version: 1\ncontext: candidate\n"), func() {
		if removeErr := os.Remove(paths.ConfigFile); removeErr != nil {
			t.Fatal(removeErr)
		}
		if writeErr := os.WriteFile(paths.ConfigFile, replacement, 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	})
	assertSafeError(t, err, ClassConfigurationInvalid, "config_file_changed")
	content, readErr := os.ReadFile(paths.ConfigFile)
	if readErr != nil || string(content) != string(replacement) {
		t.Fatalf("replacement target changed: %q, %v", content, readErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(root, ".config.yaml.tmp-*"))
	if globErr != nil || len(matches) != 0 {
		t.Fatalf("temporary files = %v, %v", matches, globErr)
	}
}

func TestPublishConfigurationRejectsTemporaryReplacementWithoutFollowingLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink contract")
	}
	root := t.TempDir()
	paths := pathsForHome(root)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("preserve"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := publishConfigurationWithHook(context.Background(), paths, []byte("version: 1\n"), func() {
		matches, globErr := filepath.Glob(filepath.Join(root, ".config.yaml.tmp-*"))
		if globErr != nil || len(matches) != 1 {
			t.Fatalf("temporary files = %v, %v", matches, globErr)
		}
		if renameErr := os.Rename(matches[0], filepath.Join(root, "displaced-temporary")); renameErr != nil {
			t.Fatal(renameErr)
		}
		if linkErr := os.Symlink(outside, matches[0]); linkErr != nil {
			t.Fatal(linkErr)
		}
	})
	assertSafeError(t, err, ClassConfigurationInvalid, "config_file_changed")
	content, readErr := os.ReadFile(outside)
	if readErr != nil || string(content) != "preserve" {
		t.Fatalf("replacement link target changed: %q, %v", content, readErr)
	}
	assertPathMode(t, outside, 0o644)
	if _, statErr := os.Lstat(paths.ConfigFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("configuration was published after temporary replacement: %v", statErr)
	}
}

func assertPathMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode for %q = %04o, want %04o", filepath.Base(path), info.Mode().Perm(), want)
	}
}
