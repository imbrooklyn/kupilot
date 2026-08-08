package buildinfo

import (
	"strings"
	"testing"
)

func TestInfoLine(t *testing.T) {
	t.Parallel()

	info := Info{
		Version:   "v0.0.0-test",
		Commit:    "0123456789ab",
		BuildTime: "2026-08-08T00:00:00Z",
		GoVersion: "go1.25.0",
		GOOS:      "linux",
		GOARCH:    "arm64",
	}
	const want = "kupilot version=v0.0.0-test commit=0123456789ab built=2026-08-08T00:00:00Z go=go1.25.0 platform=linux/arm64"

	if got := info.Line(); got != want {
		t.Fatalf("Info.Line() = %q, want %q", got, want)
	}
}

func TestInfoLineUsesHonestUnknownValues(t *testing.T) {
	t.Parallel()

	const want = "kupilot version=unknown commit=unknown built=unknown go=unknown platform=unknown/unknown"
	if got := (Info{}).Line(); got != want {
		t.Fatalf("Info.Line() = %q, want %q", got, want)
	}
}

func TestReadDoesNotIncludeEnvironmentValues(t *testing.T) {
	canary := strings.Repeat("q", 32)
	t.Setenv("KUPILOT_BUILDINFO_TEST_VALUE", canary)

	got := Read().Line()
	if strings.Contains(got, canary) {
		t.Fatal("build information contains an environment value")
	}
	for _, required := range []string{"version=", "commit=", "built=", "go=", "platform="} {
		if !strings.Contains(got, required) {
			t.Errorf("build information does not contain %q", required)
		}
	}
}
