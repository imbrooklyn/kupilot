package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// ModelProfile is the non-sensitive portion accepted by interactive setup.
type ModelProfile struct {
	Endpoint string
	Model    string
}

type writableConfig struct {
	Version    int                 `yaml:"version"`
	Context    string              `yaml:"context,omitempty"`
	Namespace  string              `yaml:"namespace,omitempty"`
	NoColor    bool                `yaml:"no_color"`
	Model      writableModelConfig `yaml:"model"`
	Kubernetes KubernetesConfig    `yaml:"kubernetes"`
	Logging    LoggingConfig       `yaml:"logging"`
}

type writableModelConfig struct {
	ProviderKind          string  `yaml:"provider_kind"`
	Endpoint              string  `yaml:"endpoint"`
	Model                 string  `yaml:"model"`
	APIKey                string  `yaml:"api_key"`
	Temperature           float64 `yaml:"temperature"`
	MaxOutputTokens       int     `yaml:"max_output_tokens"`
	RequestTimeoutSeconds int     `yaml:"request_timeout_seconds"`
	Streaming             bool    `yaml:"streaming"`
	ToolCallingRequired   bool    `yaml:"tool_calling_required"`
}

// SaveModelProfile atomically publishes the current typed settings and one
// plaintext credential to the fixed Home configuration file.
func SaveModelProfile(ctx context.Context, paths Paths, base Config, profile ModelProfile, secret *SecretValue) error {
	if ctx == nil || ctx.Err() != nil {
		return newSafeError(ClassCancelled, "config_write_cancelled", "write_configuration", "Configuration writing was cancelled.")
	}
	if pathsForHome(paths.HomeDir).ConfigFile != paths.ConfigFile || !validHomePath(paths.HomeDir) || secret == nil || !secret.IsSet() {
		return newSafeError(ClassInternal, "config_write_invalid", "write_configuration", "KuPilot could not prepare the local configuration update.")
	}
	base.Model.Endpoint = profile.Endpoint
	base.Model.Model = profile.Model
	if err := Validate(&base); err != nil {
		return err
	}
	document := writableConfig{
		Version: base.Version, Context: base.Context, Namespace: base.Namespace, NoColor: base.NoColor,
		Model: writableModelConfig{
			ProviderKind: base.Model.ProviderKind, Endpoint: base.Model.Endpoint, Model: base.Model.Model,
			Temperature: base.Model.Temperature, MaxOutputTokens: base.Model.MaxOutputTokens,
			RequestTimeoutSeconds: base.Model.RequestTimeoutSeconds,
			Streaming:             base.Model.Streaming, ToolCallingRequired: base.Model.ToolCallingRequired,
		},
		Kubernetes: base.Kubernetes, Logging: base.Logging,
	}
	if err := secret.Use(func(value string) { document.Model.APIKey = value }); err != nil {
		return err
	}
	content, err := yaml.Marshal(document)
	document.Model.APIKey = ""
	if err != nil || len(content) > MaxConfigFileBytes {
		zeroBytes(content)
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not encode the local configuration safely.")
	}
	defer zeroBytes(content)
	return publishConfiguration(ctx, paths, content)
}

func publishConfiguration(ctx context.Context, paths Paths, content []byte) error {
	return publishConfigurationWithHook(ctx, paths, content, nil)
}

func publishConfigurationWithHook(ctx context.Context, paths Paths, content []byte, beforeRevalidate func()) error {
	if err := ensureHomeDirectory(paths.HomeDir, "write_configuration"); err != nil {
		return err
	}
	initialTarget, err := inspectConfigTarget(paths.ConfigFile)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(paths.HomeDir, ".config.yaml.tmp-")
	if err != nil {
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not create the local configuration safely.")
	}
	temporaryName := temporary.Name()
	published := false
	defer func() {
		_ = temporary.Close()
		if !published {
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not protect the new local configuration file.")
	}
	if _, err := temporary.Write(content); err != nil {
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not write the local configuration safely.")
	}
	if err := temporary.Sync(); err != nil {
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not synchronize the local configuration safely.")
	}
	if err := ctx.Err(); err != nil {
		return newSafeError(ClassCancelled, "config_write_cancelled", "write_configuration", "Configuration writing was cancelled.")
	}
	if beforeRevalidate != nil {
		beforeRevalidate()
	}
	currentTarget, err := inspectConfigTarget(paths.ConfigFile)
	if err != nil {
		return err
	}
	if configTargetChanged(initialTarget, currentTarget) {
		return newSafeError(ClassConfigurationInvalid, "config_file_changed", "write_configuration", "The Home configuration changed while KuPilot was preparing its update.")
	}
	if err := ctx.Err(); err != nil {
		return newSafeError(ClassCancelled, "config_write_cancelled", "write_configuration", "Configuration writing was cancelled.")
	}
	mode := os.FileMode(0o600)
	if currentTarget != nil {
		mode = currentTarget.Mode().Perm()
	}
	if err := temporary.Chmod(mode); err != nil {
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not preserve the local configuration permissions safely.")
	}
	openedTemporary, err := temporary.Stat()
	if err != nil || !openedTemporary.Mode().IsRegular() {
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not verify the local configuration temporary file.")
	}
	if err := temporary.Sync(); err != nil {
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not synchronize the local configuration safely.")
	}
	if err := temporary.Close(); err != nil {
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not close the local configuration safely.")
	}
	pathTemporary, err := os.Lstat(temporaryName)
	if err != nil || pathTemporary.Mode()&os.ModeSymlink != 0 || !pathTemporary.Mode().IsRegular() ||
		!os.SameFile(openedTemporary, pathTemporary) {
		return newSafeError(ClassConfigurationInvalid, "config_file_changed", "write_configuration", "The Home configuration temporary file changed before publication.")
	}
	if err := os.Rename(temporaryName, paths.ConfigFile); err != nil {
		return newSafeError(ClassInternal, "config_write_failed", "write_configuration", "KuPilot could not publish the local configuration safely.")
	}
	published = true
	if directory, err := os.Open(paths.HomeDir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

// EnsureHome creates only a missing process-frozen KuPilot Home. Existing
// user-managed permissions are left unchanged.
func EnsureHome(ctx context.Context, paths Paths) error {
	if ctx == nil || ctx.Err() != nil {
		return newSafeError(ClassCancelled, "home_initialization_cancelled", "initialize_home", "KuPilot Home initialization was cancelled.")
	}
	if !validHomePath(paths.HomeDir) || pathsForHome(paths.HomeDir) != pathsWithoutRuntimeMetadata(paths) {
		return newSafeError(ClassConfigurationInvalid, "kupilot_home_unsafe", "initialize_home", "KUPILOT_HOME could not be initialized safely.")
	}
	return ensureHomeDirectory(paths.HomeDir, "initialize_home")
}

func ensureHomeDirectory(home, operation string) error {
	info, err := os.Lstat(home)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(home, 0o700); err != nil {
			return newSafeError(ClassInternal, "kupilot_home_unavailable", operation, "KuPilot could not create KUPILOT_HOME.")
		}
		created = true
		info, err = os.Lstat(home)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return newSafeError(ClassConfigurationInvalid, "kupilot_home_unsafe", operation, "KUPILOT_HOME is not an available directory.")
	}
	if created {
		if err := os.Chmod(home, 0o700); err != nil {
			return newSafeError(ClassInternal, "kupilot_home_unavailable", operation, "KuPilot could not protect the new KUPILOT_HOME directory.")
		}
	}
	return nil
}

func inspectConfigTarget(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || filepath.Base(path) != "config.yaml" {
		return nil, newSafeError(ClassConfigurationInvalid, "config_file_unsafe", "write_configuration", "The Home configuration target must be a regular file and must not be a symbolic link.")
	}
	return info, nil
}

func configTargetChanged(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return before != nil || after != nil
	}
	return !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime())
}

func pathsWithoutRuntimeMetadata(paths Paths) Paths {
	paths.HomePermissionsWider = false
	return paths
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
