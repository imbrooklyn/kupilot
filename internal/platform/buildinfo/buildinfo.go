package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

const unknown = "unknown"

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
		return info
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

	return info
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

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
