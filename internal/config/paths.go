package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// Paths contains resolved local paths. Only state, cache, and log directories
// are configurable from YAML; configuration discovery stays platform-owned.
type Paths struct {
	ConfigDir  string `mapstructure:"-" yaml:"-" json:"-"`
	ConfigFile string `mapstructure:"-" yaml:"-" json:"-"`
	StateDir   string `mapstructure:"state_dir" yaml:"state_dir" json:"state_dir"`
	CacheDir   string `mapstructure:"cache_dir" yaml:"cache_dir" json:"cache_dir"`
	LogDir     string `mapstructure:"log_dir" yaml:"log_dir" json:"log_dir"`
}

// PathInput is the complete deterministic input to platform path resolution.
type PathInput struct {
	GOOS          string
	HomeDir       string
	XDGConfigHome string
	XDGStateHome  string
	XDGCacheHome  string
}

// SystemPaths resolves paths from the supported operating system and current
// user environment without consulting working-directory state.
func SystemPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, newSafeError(
			ClassConfigurationInvalid,
			"home_directory_invalid",
			"resolve_platform_paths",
			"KuPilot could not resolve the current user's home directory.",
		)
	}
	return ResolvePaths(PathInput{
		GOOS:          runtime.GOOS,
		HomeDir:       home,
		XDGConfigHome: os.Getenv("XDG_CONFIG_HOME"),
		XDGStateHome:  os.Getenv("XDG_STATE_HOME"),
		XDGCacheHome:  os.Getenv("XDG_CACHE_HOME"),
	})
}

// ResolvePaths applies Linux XDG and macOS per-user path rules.
func ResolvePaths(input PathInput) (Paths, error) {
	if input.GOOS != "linux" && input.GOOS != "darwin" {
		return Paths{}, newSafeError(
			ClassUnsupported,
			"platform_unsupported",
			"resolve_platform_paths",
			"KuPilot supports local path resolution on macOS and Linux.",
		)
	}
	if !validAbsoluteDirectory(input.HomeDir) {
		return Paths{}, newSafeError(
			ClassConfigurationInvalid,
			"home_directory_invalid",
			"resolve_platform_paths",
			"KuPilot requires an absolute current-user home directory.",
		)
	}
	if input.GOOS == "linux" {
		for _, value := range []string{input.XDGConfigHome, input.XDGStateHome, input.XDGCacheHome} {
			if value != "" && !validAbsoluteDirectory(value) {
				return Paths{}, newSafeError(
					ClassConfigurationInvalid,
					"xdg_path_invalid",
					"resolve_platform_paths",
					"Each configured XDG base directory must be an absolute, normalized path.",
				)
			}
		}
		configHome := input.XDGConfigHome
		if configHome == "" {
			configHome = filepath.Join(input.HomeDir, ".config")
		}
		stateHome := input.XDGStateHome
		if stateHome == "" {
			stateHome = filepath.Join(input.HomeDir, ".local", "state")
		}
		cacheHome := input.XDGCacheHome
		if cacheHome == "" {
			cacheHome = filepath.Join(input.HomeDir, ".cache")
		}
		configDir := filepath.Join(configHome, "kupilot")
		stateDir := filepath.Join(stateHome, "kupilot")
		return Paths{
			ConfigDir:  configDir,
			ConfigFile: filepath.Join(configDir, "config.yaml"),
			StateDir:   stateDir,
			CacheDir:   filepath.Join(cacheHome, "kupilot"),
			LogDir:     filepath.Join(stateDir, "logs"),
		}, nil
	}

	applicationSupport := filepath.Join(input.HomeDir, "Library", "Application Support", "KuPilot")
	return Paths{
		ConfigDir:  applicationSupport,
		ConfigFile: filepath.Join(applicationSupport, "config.yaml"),
		StateDir:   applicationSupport,
		CacheDir:   filepath.Join(input.HomeDir, "Library", "Caches", "KuPilot"),
		LogDir:     filepath.Join(input.HomeDir, "Library", "Logs", "KuPilot"),
	}, nil
}

func validAbsoluteDirectory(value string) bool {
	return value != "" && len(value) <= MaxPathBytes && filepath.IsAbs(value) && filepath.Clean(value) == value
}
