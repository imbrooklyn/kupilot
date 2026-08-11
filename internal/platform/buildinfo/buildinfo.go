package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

const unknown = "unknown"

// These values are set only by the release build's linker flags. Keeping the
// defaults empty preserves honest development-build output.
var (
	releaseVersion string
	releaseCommit  string
	releaseDate    string
)

// Info contains the non-sensitive metadata shown by the version command.
type Info struct {
	Version   string
	Commit    string
	BuildTime string
	GoVersion string
	GOOS      string
	GOARCH    string
}

// Read returns metadata embedded by the Go toolchain and safe runtime facts.
func Read() Info {
	info := Info{
		Version:   "dev",
		Commit:    unknown,
		BuildTime: unknown,
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}

	build, ok := debug.ReadBuildInfo()
	if !ok {
		return applyReleaseMetadata(info, releaseVersion, releaseCommit, releaseDate)
	}
	if build.Main.Version != "" && build.Main.Version != "(devel)" {
		info.Version = build.Main.Version
	}
	if build.GoVersion != "" {
		info.GoVersion = build.GoVersion
	}
	for _, setting := range build.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			info.Commit = shortenCommit(setting.Value)
		}
	}

	return applyReleaseMetadata(info, releaseVersion, releaseCommit, releaseDate)
}

// Line formats Info as one stable, non-sensitive output line.
func (info Info) Line() string {
	return fmt.Sprintf(
		"kupilot version=%s commit=%s built=%s go=%s platform=%s/%s",
		valueOr(info.Version, unknown),
		valueOr(info.Commit, unknown),
		valueOr(info.BuildTime, unknown),
		valueOr(info.GoVersion, unknown),
		valueOr(info.GOOS, unknown),
		valueOr(info.GOARCH, unknown),
	)
}

func shortenCommit(commit string) string {
	const displayedCommitLength = 12
	if len(commit) <= displayedCommitLength {
		return commit
	}
	return commit[:displayedCommitLength]
}

func applyReleaseMetadata(info Info, version, commit, date string) Info {
	if validVersion(version) {
		info.Version = version
	}
	if validCommit(commit) {
		info.Commit = shortenCommit(commit)
	}
	if parsed, err := time.Parse(time.RFC3339, date); err == nil {
		info.BuildTime = parsed.UTC().Format(time.RFC3339)
	}
	return info
}

func validVersion(version string) bool {
	if len(version) < 2 || len(version) > 64 || version[0] != 'v' {
		return false
	}
	for _, char := range version[1:] {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			strings.ContainsRune(".-+", char) {
			continue
		}
		return false
	}
	return true
}

func validCommit(commit string) bool {
	if len(commit) < 12 || len(commit) > 64 {
		return false
	}
	for _, char := range commit {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
