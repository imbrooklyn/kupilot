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
)

func TestModelAPIKeyCanaryIsAbsentFromEveryStartupSink(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("c", 53) + "-generated"
	environment := map[string]string{ModelAPIKeyEnvironmentVariable: canary}
	loaded, err := Load(context.Background(), LoadOptions{
		Paths:     testPaths(t.TempDir()),
		LookupEnv: lookupMap(environment),
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	source := &EnvironmentSecretSource{
		LookupEnv: lookupMap(environment),
		Unsetenv: func(key string) error {
			delete(environment, key)
			return nil
		},
	}
	secret, err := source.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer secret.Destroy()
	if _, found := environment[ModelAPIKeyEnvironmentVariable]; found {
		t.Fatal("model API key remains in the source environment")
	}

	configJSON, err := json.Marshal(loaded)
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
		"Config formatting":         fmt.Sprintf("%v %#v", loaded, loaded),
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
