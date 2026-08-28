package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/cli"
	"github.com/imbrooklyn/kupilot/internal/platform/buildinfo"
	platformlogging "github.com/imbrooklyn/kupilot/internal/platform/logging"
	"go.yaml.in/yaml/v3"
)

func TestModelAPIKeyCanaryIsAbsentFromEveryStartupSink(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("c", 53) + "-generated"
	environment := map[string]string{ModelAPIKeyEnvironmentVariable: canary}
	loaded, err := Load(context.Background(), LoadOptions{
		Paths:     testPaths(t.TempDir()),
		LookupEnv: lookupMap(environment),
		Unsetenv: func(key string) error {
			delete(environment, key)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	secret := &loaded.Credential
	defer secret.Destroy()
	if _, found := environment[ModelAPIKeyEnvironmentVariable]; found {
		t.Fatal("model API key remains in the source environment")
	}

	configJSON, err := json.Marshal(loaded.Config)
	if err != nil {
		t.Fatalf("json.Marshal(Config) error = %v", err)
	}
	_, marshalError := json.Marshal(secret)
	if marshalError == nil {
		t.Fatal("json.Marshal(SecretValue) succeeded")
	}

	logDirectory := t.TempDir()
	if err := os.Chmod(logDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	sink, err := platformlogging.Open(context.Background(), platformlogging.Options{Directory: logDirectory})
	if err != nil {
		t.Fatalf("logging.Open() error = %v", err)
	}
	sink.Logger.InfoContext(context.Background(), platformlogging.EventStartup,
		"component", "composition",
		"operation", "configuration_load",
		"outcome", "success",
		"body", canary,
		"args", []string{canary},
	)
	if err := sink.Close(); err != nil {
		t.Fatalf("logging.Close() error = %v", err)
	}
	logContent, err := os.ReadFile(filepath.Join(logDirectory, platformlogging.LogFileName))
	if err != nil {
		t.Fatal(err)
	}

	missingSource := &EnvironmentSecretSource{LookupEnv: lookupMap(environment)}
	_, missingError := missingSource.Read()
	if missingError == nil {
		t.Fatal("second secret read succeeded")
	}
	childEnvironment := FilterChildEnvironment([]string{
		"PATH=/usr/bin",
		ModelAPIKeyEnvironmentVariable + "=" + canary,
	})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := cli.Run(
		context.Background(),
		nil,
		&stdout,
		&stderr,
		buildinfo.Info{},
		func(context.Context, cli.StartIntent) error { return missingError },
	); code != cli.ExitFailure {
		t.Fatalf("cli.Run() exit code = %d, want %d", code, cli.ExitFailure)
	}

	sinks := map[string]string{
		"stdout":                    stdout.String(),
		"stderr":                    stderr.String(),
		"local log":                 string(logContent),
		"safe error":                missingError.Error(),
		"Config JSON":               string(configJSON),
		"Config formatting":         fmt.Sprintf("%v %#v", loaded.Config, loaded.Config),
		"SecretValue formatting":    fmt.Sprintf("%s %q %v %+v %#v", secret, secret, secret, secret, secret),
		"SecretValue marshal error": marshalError.Error(),
		"child environment":         strings.Join(childEnvironment, "\x00"),
	}
	for name, value := range sinks {
		if strings.Contains(value, canary) {
			t.Fatalf("%s contains the model API key canary", name)
		}
	}
}

func TestFileModelAPIKeyCanaryIsExtractedFromOrdinaryConfiguration(t *testing.T) {
	t.Parallel()
	canary := strings.Repeat("f", 47) + "-generated"
	root := t.TempDir()
	paths := pathsForHome(root)
	writePrivateFile(t, paths.ConfigFile, []byte("version: 1\nmodel:\n  endpoint: https://model.example.test/v1\n  model: diagnostic-model\n  api_key: "+canary+"\n"))
	loaded, err := Load(context.Background(), LoadOptions{Paths: paths, LookupEnv: lookupMap(nil)})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer loaded.Credential.Destroy()
	if loaded.CredentialSource != CredentialSourceFile {
		t.Fatalf("credential source = %q, want file", loaded.CredentialSource)
	}
	matched := false
	if err := loaded.Credential.Use(func(value string) { matched = value == canary }); err != nil || !matched {
		t.Fatalf("extracted credential unavailable: %v", err)
	}
	jsonValue, jsonErr := json.Marshal(loaded.Config)
	yamlValue, yamlErr := yaml.Marshal(loaded.Config)
	if jsonErr != nil || yamlErr != nil {
		t.Fatalf("ordinary configuration marshal errors = %v, %v", jsonErr, yamlErr)
	}
	for name, value := range map[string]string{
		"Config JSON": string(jsonValue), "Config YAML": string(yamlValue),
		"Loaded formatting": fmt.Sprintf("%v %#v", loaded, loaded),
	} {
		if strings.Contains(value, canary) {
			t.Fatalf("%s contains the file credential canary", name)
		}
	}
}
