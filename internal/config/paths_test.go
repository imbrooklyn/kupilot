package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolvePathsUsesOneFixedHomeLayout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input PathInput
		root  string
	}{
		{name: "default", input: PathInput{HomeDir: "/home/alex"}, root: "/home/alex/.kupilot"},
		{name: "override", input: PathInput{HomeDir: "/home/alex", KuPilotHome: "/srv/alex-kupilot"}, root: "/srv/alex-kupilot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolvePaths(tt.input)
			if err != nil {
				t.Fatalf("ResolvePaths() error = %v", err)
			}
			want := Paths{
				HomeDir: tt.root, ConfigFile: filepath.Join(tt.root, "config.yaml"),
				StateDir: filepath.Join(tt.root, "state"), CacheDir: filepath.Join(tt.root, "cache"),
				LogDir: filepath.Join(tt.root, "logs"),
			}
			if got != want {
				t.Fatalf("ResolvePaths() = %#v, want %#v", got, want)
			}
		})
	}
}

func TestResolvePathsRejectsInvalidRoots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input PathInput
		code  string
	}{
		{name: "missing user home", input: PathInput{}, code: "home_directory_invalid"},
		{name: "relative user home", input: PathInput{HomeDir: "relative"}, code: "home_directory_invalid"},
		{name: "relative override", input: PathInput{HomeDir: "/home/alex", KuPilotHome: "relative"}, code: "kupilot_home_invalid"},
		{name: "root override", input: PathInput{HomeDir: "/home/alex", KuPilotHome: filepath.VolumeName(filepath.Clean(string(filepath.Separator))) + string(filepath.Separator)}, code: "kupilot_home_invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolvePaths(tt.input)
			assertSafeError(t, err, ClassConfigurationInvalid, tt.code)
		})
	}
}

func TestSystemPathsUsesOverrideWithoutRequiringUserHome(t *testing.T) {
	selected := t.TempDir()
	t.Setenv("HOME", "")
	t.Setenv(HomeEnvironmentVariable, selected)

	got, err := SystemPaths()
	if err != nil {
		t.Fatalf("SystemPaths() error = %v", err)
	}
	canonical, err := filepath.EvalSymlinks(selected)
	if err != nil {
		t.Fatal(err)
	}
	if got.HomeDir != canonical || got.ConfigFile != filepath.Join(canonical, "config.yaml") {
		t.Fatalf("SystemPaths() = %#v", got)
	}
}

func TestCanonicalizeExistingHomeResolvesRootSymlinkAndWarnsOnWideMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink and mode contract")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "home-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, err := canonicalizeExistingHome(pathsForHome(link))
	if err != nil {
		t.Fatalf("canonicalizeExistingHome() error = %v", err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got.HomeDir != canonicalTarget || !got.HomePermissionsWider || got.StateDir != filepath.Join(canonicalTarget, "state") {
		t.Fatalf("canonicalized paths = %#v", got)
	}
}

func TestCanonicalizeExistingHomeResolvesAncestorForMissingHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink contract")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "ancestor-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	wantedRoot := filepath.Join(target, "missing", "home")
	got, err := canonicalizeExistingHome(pathsForHome(filepath.Join(link, "missing", "home")))
	if err != nil {
		t.Fatalf("canonicalizeExistingHome() error = %v", err)
	}
	canonicalTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	wantedRoot = filepath.Join(canonicalTarget, "missing", "home")
	if got != pathsForHome(wantedRoot) {
		t.Fatalf("canonicalized missing Home = %#v, want %#v", got, pathsForHome(wantedRoot))
	}
}

func TestCanonicalizeExistingHomeRejectsNonDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := canonicalizeExistingHome(pathsForHome(file))
	assertSafeError(t, err, ClassConfigurationInvalid, "kupilot_home_unsafe")
}
