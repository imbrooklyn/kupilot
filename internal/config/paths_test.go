package config

import (
	"context"
	"path/filepath"
	"testing"
)

func TestResolvePlatformPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input PathInput
		want  Paths
	}{
		{
			name:  "Linux defaults",
			input: PathInput{GOOS: "linux", HomeDir: "/home/alex"},
			want: Paths{
				ConfigDir:  "/home/alex/.config/kupilot",
				ConfigFile: "/home/alex/.config/kupilot/config.yaml",
				StateDir:   "/home/alex/.local/state/kupilot",
				CacheDir:   "/home/alex/.cache/kupilot",
				LogDir:     "/home/alex/.local/state/kupilot/logs",
			},
		},
		{
			name: "Linux XDG",
			input: PathInput{
				GOOS:          "linux",
				HomeDir:       "/home/alex",
				XDGConfigHome: "/mnt/config",
				XDGStateHome:  "/mnt/state",
				XDGCacheHome:  "/mnt/cache",
			},
			want: Paths{
				ConfigDir:  "/mnt/config/kupilot",
				ConfigFile: "/mnt/config/kupilot/config.yaml",
				StateDir:   "/mnt/state/kupilot",
				CacheDir:   "/mnt/cache/kupilot",
				LogDir:     "/mnt/state/kupilot/logs",
			},
		},
		{
			name:  "macOS defaults",
			input: PathInput{GOOS: "darwin", HomeDir: "/Users/alex"},
			want: Paths{
				ConfigDir:  "/Users/alex/Library/Application Support/KuPilot",
				ConfigFile: "/Users/alex/Library/Application Support/KuPilot/config.yaml",
				StateDir:   "/Users/alex/Library/Application Support/KuPilot",
				CacheDir:   "/Users/alex/Library/Caches/KuPilot",
				LogDir:     "/Users/alex/Library/Logs/KuPilot",
			},
		},
		{
			name: "macOS ignores XDG environment",
			input: PathInput{
				GOOS:          "darwin",
				HomeDir:       "/Users/alex",
				XDGConfigHome: "/mnt/config",
				XDGStateHome:  "/mnt/state",
				XDGCacheHome:  "/mnt/cache",
			},
			want: Paths{
				ConfigDir:  "/Users/alex/Library/Application Support/KuPilot",
				ConfigFile: "/Users/alex/Library/Application Support/KuPilot/config.yaml",
				StateDir:   "/Users/alex/Library/Application Support/KuPilot",
				CacheDir:   "/Users/alex/Library/Caches/KuPilot",
				LogDir:     "/Users/alex/Library/Logs/KuPilot",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolvePaths(tt.input)
			if err != nil {
				t.Fatalf("ResolvePaths() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("ResolvePaths() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolvePathsRejectsUnsafeInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input PathInput
		class ErrorClass
		code  string
	}{
		{name: "unsupported platform", input: PathInput{GOOS: "windows", HomeDir: `C:\\Users\\alex`}, class: ClassUnsupported, code: "platform_unsupported"},
		{name: "missing home", input: PathInput{GOOS: "linux"}, class: ClassConfigurationInvalid, code: "home_directory_invalid"},
		{name: "relative home", input: PathInput{GOOS: "linux", HomeDir: "relative"}, class: ClassConfigurationInvalid, code: "home_directory_invalid"},
		{name: "relative config XDG", input: PathInput{GOOS: "linux", HomeDir: "/home/alex", XDGConfigHome: "relative"}, class: ClassConfigurationInvalid, code: "xdg_path_invalid"},
		{name: "relative state XDG", input: PathInput{GOOS: "linux", HomeDir: "/home/alex", XDGStateHome: "relative"}, class: ClassConfigurationInvalid, code: "xdg_path_invalid"},
		{name: "relative cache XDG", input: PathInput{GOOS: "linux", HomeDir: "/home/alex", XDGCacheHome: "relative"}, class: ClassConfigurationInvalid, code: "xdg_path_invalid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ResolvePaths(tt.input)
			assertSafeError(t, err, tt.class, tt.code)
		})
	}
}

func TestConfigPathOverridesAreAbsoluteAndPrecedePlatformPaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configFile := filepath.Join(root, "config.yaml")
	stateDir := filepath.Join(root, "custom-state")
	cacheDir := filepath.Join(root, "custom-cache")
	logDir := filepath.Join(root, "custom-logs")
	writePrivateFile(t, configFile, []byte("version: 1\npaths:\n  state_dir: "+stateDir+"\n  cache_dir: "+cacheDir+"\n  log_dir: "+logDir+"\n"))

	paths := testPaths(root)
	paths.ConfigFile = configFile
	got, err := Load(context.Background(), LoadOptions{
		Paths: paths,
		LookupEnv: lookupMap(map[string]string{
			"KUPILOT_STATE_DIR": filepath.Join(root, "environment-state"),
		}),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Paths.StateDir != filepath.Join(root, "environment-state") {
		t.Errorf("StateDir = %q, want environment override", got.Paths.StateDir)
	}
	if got.Paths.CacheDir != cacheDir || got.Paths.LogDir != logDir {
		t.Errorf("file path overrides were not retained: %#v", got.Paths)
	}
}
