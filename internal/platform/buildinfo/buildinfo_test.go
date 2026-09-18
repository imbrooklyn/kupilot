package buildinfo

import (
	"strings"
	"testing"
)

func TestInfoLine(t *testing.T) {
	t.Parallel()

	info := Info{
		Version:   "v0.1.0",
		Commit:    "0123456789ab",
		BuildTime: "2026-08-08T00:00:00Z",
		GoVersion: "go1.27.0",
		GOOS:      "linux",
		GOARCH:    "arm64",
	}
	const want = "kupilot version=v0.1.0 commit=0123456789ab built=2026-08-08T00:00:00Z go=go1.27.0 platform=linux/arm64"

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
	if Read().Version != "v0.1.0" {
		t.Fatal("The unreleased product version changed")
	}
	if strings.Contains(got, canary) {
		t.Fatal("build information contains an environment value")
	}
	for _, required := range []string{"version=", "commit=", "built=", "go=", "platform="} {
		if !strings.Contains(got, required) {
			t.Errorf("build information does not contain %q", required)
		}
	}
}

func TestUnreleasedVersionRejectsDevelopmentBumps(t *testing.T) {
	for _, version := range []string{"", "dev", "v0.2.0", "v0.5.0", "v0.1.1", "v0.1.0-alpha.1"} {
		if validVersion(version) {
			t.Fatalf("Unexpected development version admitted: %q", version)
		}
	}
	if !validVersion("v0.1.0") {
		t.Fatal("Initial version rejected")
	}
}

func TestApplyReleaseMetadata(t *testing.T) {
	t.Parallel()

	base := Info{
		Version:   "v0.1.0",
		Commit:    unknown,
		BuildTime: unknown,
	}
	got := applyReleaseMetadata(
		base,
		"v0.1.0",
		"0123456789abcdef0123456789abcdef01234567",
		"2026-08-11T00:22:27+08:00",
	)

	if got.Version != "v0.1.0" {
		t.Fatalf("Version = %q, want v0.1.0", got.Version)
	}
	if got.Commit != "0123456789ab" {
		t.Fatalf("Commit = %q, want 0123456789ab", got.Commit)
	}
	if got.BuildTime != "2026-08-10T16:22:27Z" {
		t.Fatalf("BuildTime = %q, want normalized UTC time", got.BuildTime)
	}
}

func TestApplyReleaseMetadataRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	base := Info{
		Version:   "v0.1.0",
		Commit:    unknown,
		BuildTime: unknown,
	}
	got := applyReleaseMetadata(
		base,
		"v0.1.0\nsecret",
		"not-a-commit",
		"not-a-time",
	)

	if got != base {
		t.Fatalf("unsafe release metadata changed Info: %#v", got)
	}
}
