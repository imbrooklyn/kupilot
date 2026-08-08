package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestEnvironmentSecretSourceReadsOnceAndUnsets(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("k", 43) + "-generated"
	environment := map[string]string{ModelAPIKeyEnvironmentVariable: canary}
	unsetCalls := 0
	source := EnvironmentSecretSource{
		LookupEnv: lookupMap(environment),
		Unsetenv: func(key string) error {
			unsetCalls++
			delete(environment, key)
			return nil
		},
	}

	secret, err := source.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if unsetCalls != 1 {
		t.Fatalf("unset calls = %d, want 1", unsetCalls)
	}
	if !secret.IsSet() {
		t.Fatal("secret is not set")
	}

	used := 0
	if err := secret.Use(func(value string) {
		used++
		if value != canary {
			t.Fatal("Use() provided an unexpected value")
		}
	}); err != nil {
		t.Fatalf("Use() error = %v", err)
	}
	if used != 1 {
		t.Fatalf("Use() calls = %d, want 1", used)
	}

	_, err = source.Read()
	assertSafeError(t, err, ClassConfigurationInvalid, "model_api_key_already_read")
	if unsetCalls != 1 {
		t.Fatalf("unset calls after second read = %d, want 1", unsetCalls)
	}
}

func TestEnvironmentSecretSourceRejectsSafelyAndAlwaysUnsetsPresentValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		present bool
		code    string
		unset   int
	}{
		{name: "missing", code: "model_api_key_missing"},
		{name: "empty", present: true, code: "model_api_key_missing", unset: 1},
		{name: "oversized", present: true, value: strings.Repeat("x", MaxModelAPIKeyBytes+1), code: "model_api_key_invalid", unset: 1},
		{name: "line break", present: true, value: "generated\nvalue", code: "model_api_key_invalid", unset: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			unsetCalls := 0
			source := EnvironmentSecretSource{
				LookupEnv: func(string) (string, bool) { return tt.value, tt.present },
				Unsetenv: func(string) error {
					unsetCalls++
					return nil
				},
			}
			_, err := source.Read()
			assertSafeError(t, err, ClassConfigurationInvalid, tt.code)
			if unsetCalls != tt.unset {
				t.Fatalf("unset calls = %d, want %d", unsetCalls, tt.unset)
			}
			if tt.value != "" && strings.Contains(err.Error(), tt.value) {
				t.Fatal("safe error contains the rejected API key")
			}
		})
	}
}

func TestEnvironmentSecretSourceFailsClosedWhenUnsetFails(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("u", 37) + "-generated"
	source := EnvironmentSecretSource{
		LookupEnv: func(string) (string, bool) { return canary, true },
		Unsetenv:  func(string) error { return errors.New(canary) },
	}

	secret, err := source.Read()
	assertSafeError(t, err, ClassInternal, "model_api_key_unset_failed")
	if secret.IsSet() {
		t.Fatal("secret returned after environment removal failed")
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatal("safe error contains an environment error canary")
	}
}

func TestSecretValueCannotBeFormattedOrSerialized(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("f", 47) + "-generated"
	source := EnvironmentSecretSource{
		LookupEnv: func(string) (string, bool) { return canary, true },
		Unsetenv:  func(string) error { return nil },
	}
	secret, err := source.Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}

	formatted := fmt.Sprintf("%s %q %v %+v %#v", secret, secret, secret, secret, secret)
	if strings.Contains(formatted, canary) {
		t.Fatal("formatted SecretValue contains the API key")
	}
	if _, err := json.Marshal(secret); err == nil {
		t.Fatal("json.Marshal() succeeded, want explicit rejection")
	} else if strings.Contains(err.Error(), canary) {
		t.Fatal("JSON error contains the API key")
	}
	if _, err := secret.MarshalText(); err == nil {
		t.Fatal("MarshalText() succeeded, want explicit rejection")
	} else if strings.Contains(err.Error(), canary) {
		t.Fatal("text marshal error contains the API key")
	}
	if _, err := secret.MarshalYAML(); err == nil {
		t.Fatal("MarshalYAML() succeeded, want explicit rejection")
	} else if strings.Contains(err.Error(), canary) {
		t.Fatal("YAML marshal error contains the API key")
	}

	secret.Destroy()
	if secret.IsSet() {
		t.Fatal("secret remains set after Destroy()")
	}
}

func TestFilterChildEnvironmentRemovesEveryModelAPIKeyEntry(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("e", 43) + "-generated"
	environment := []string{
		"PATH=/usr/bin",
		ModelAPIKeyEnvironmentVariable + "=" + canary,
		"KUPILOT_CONTEXT=development",
		ModelAPIKeyEnvironmentVariable + "=" + canary + "-duplicate",
	}
	got := FilterChildEnvironment(environment)
	joined := strings.Join(got, "\x00")
	if strings.Contains(joined, ModelAPIKeyEnvironmentVariable) || strings.Contains(joined, canary) {
		t.Fatal("filtered child environment contains the model API key")
	}
	if len(got) != 2 || got[0] != "PATH=/usr/bin" || got[1] != "KUPILOT_CONTEXT=development" {
		t.Fatalf("FilterChildEnvironment() = %#v", got)
	}
}
