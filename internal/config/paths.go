package config

import (
	"errors"
	"os"
	"path/filepath"
)

const HomeEnvironmentVariable = "KUPILOT_HOME"

// Paths contains the fixed descendants of one process-frozen KuPilot Home.
// None of these paths is part of the serializable configuration schema.
type Paths struct {
	HomeDir              string `mapstructure:"-" yaml:"-" json:"-"`
	ConfigFile           string `mapstructure:"-" yaml:"-" json:"-"`
	StateDir             string `mapstructure:"-" yaml:"-" json:"-"`
	CacheDir             string `mapstructure:"-" yaml:"-" json:"-"`
	LogDir               string `mapstructure:"-" yaml:"-" json:"-"`
	HomePermissionsWider bool   `mapstructure:"-" yaml:"-" json:"-"`
}

// PathInput is the complete deterministic input to lexical Home resolution.
type PathInput struct {
	HomeDir     string
	KuPilotHome string
}

// SystemPaths resolves and freezes the current process Home without creating it.
func SystemPaths() (Paths, error) {
	selected := os.Getenv(HomeEnvironmentVariable)
	home := ""
	if selected == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return Paths{}, newSafeError(
				ClassConfigurationInvalid,
				"home_directory_invalid",
				"resolve_home",
				"KuPilot could not resolve the current user's home directory.",
			)
		}
	}
	paths, err := ResolvePaths(PathInput{HomeDir: home, KuPilotHome: selected})
	if err != nil {
		return Paths{}, err
	}
	return canonicalizeExistingHome(paths)
}

// ResolvePaths derives the one fixed layout without consulting working-directory
// state or any platform-specific XDG or Library locations.
func ResolvePaths(input PathInput) (Paths, error) {
	root := input.KuPilotHome
	if root == "" {
		if !validAbsoluteDirectory(input.HomeDir) {
			return Paths{}, newSafeError(
				ClassConfigurationInvalid,
				"home_directory_invalid",
				"resolve_home",
				"KuPilot requires an absolute current-user home directory.",
			)
		}
		root = filepath.Join(input.HomeDir, ".kupilot")
	}
	if !validHomePath(root) {
		return Paths{}, newSafeError(
			ClassConfigurationInvalid,
			"kupilot_home_invalid",
			"resolve_home",
			"KUPILOT_HOME must be an absolute, normalized, non-root path.",
		)
	}
	return pathsForHome(root), nil
}

func canonicalizeExistingHome(paths Paths) (Paths, error) {
	root, info, exists, err := canonicalHomeRoot(paths.HomeDir)
	if err != nil {
		return Paths{}, err
	}
	resolved := pathsForHome(root)
	if !exists {
		return resolved, nil
	}
	if info == nil || !info.IsDir() {
		return Paths{}, newSafeError(ClassConfigurationInvalid, "kupilot_home_unsafe", "resolve_home", "KUPILOT_HOME must resolve to a directory.")
	}
	resolved.HomePermissionsWider = info.Mode().Perm()&0o077 != 0
	return resolved, nil
}

// canonicalHomeRoot resolves every existing ancestor once, including the
// parent of a Home that KuPilot has not created yet. Missing suffixes remain
// lexical descendants of that canonical directory.
func canonicalHomeRoot(root string) (string, os.FileInfo, bool, error) {
	current := root
	missing := make([]string, 0, 2)
	for {
		_, err := os.Lstat(current)
		if err == nil {
			canonical, resolveErr := filepath.EvalSymlinks(current)
			if resolveErr != nil || !validAbsoluteDirectory(canonical) {
				return "", nil, false, newSafeError(ClassConfigurationInvalid, "kupilot_home_unsafe", "resolve_home", "KUPILOT_HOME could not be resolved to a safe directory.")
			}
			info, inspectErr := os.Lstat(canonical)
			if inspectErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", nil, false, newSafeError(ClassConfigurationInvalid, "kupilot_home_unsafe", "resolve_home", "KUPILOT_HOME must resolve beneath an available directory.")
			}
			for index := len(missing) - 1; index >= 0; index-- {
				canonical = filepath.Join(canonical, missing[index])
			}
			if !validHomePath(canonical) {
				return "", nil, false, newSafeError(ClassConfigurationInvalid, "kupilot_home_unsafe", "resolve_home", "KUPILOT_HOME could not be resolved to a safe directory.")
			}
			if len(missing) != 0 {
				return canonical, nil, false, nil
			}
			return canonical, info, true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, false, newSafeError(ClassConfigurationInvalid, "kupilot_home_unavailable", "resolve_home", "KuPilot could not inspect KUPILOT_HOME.")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", nil, false, newSafeError(ClassConfigurationInvalid, "kupilot_home_unavailable", "resolve_home", "KuPilot could not inspect KUPILOT_HOME.")
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathsForHome(root string) Paths {
	return Paths{
		HomeDir:    root,
		ConfigFile: filepath.Join(root, "config.yaml"),
		StateDir:   filepath.Join(root, "state"),
		CacheDir:   filepath.Join(root, "cache"),
		LogDir:     filepath.Join(root, "logs"),
	}
}

func validHomePath(value string) bool {
	return validAbsoluteDirectory(value) && !filesystemRoot(value)
}

func filesystemRoot(value string) bool {
	volume := filepath.VolumeName(value)
	return filepath.Clean(value) == filepath.Clean(volume+string(filepath.Separator))
}

func validAbsoluteDirectory(value string) bool {
	return value != "" && len(value) <= MaxPathBytes && filepath.IsAbs(value) && filepath.Clean(value) == value
}
