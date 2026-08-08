package logging

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileLoggerWritesAllowlistedJSONWithoutTerminalOutput(t *testing.T) {
	t.Parallel()

	root := privateTempDir(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	sink, err := Open(context.Background(), Options{
		Directory: root,
		Level:     slog.LevelInfo,
		Now:       fixedClock(time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	sink.Logger.InfoContext(context.Background(), EventStartup,
		"component", "composition",
		"operation", "configuration_load",
		"outcome", "success",
	)
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	content := readCurrentLog(t, root)
	for _, want := range []string{
		`"time":"2026-08-08T01:02:03Z"`,
		`"level":"INFO"`,
		`"msg":"startup"`,
		`"component":"composition"`,
		`"operation":"configuration_load"`,
		`"outcome":"success"`,
	} {
		if !strings.Contains(content, want) {
			t.Errorf("log does not contain %q: %s", want, content)
		}
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("logger wrote to terminal: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestFileLoggerDropsUnsafeMessagesFieldsAndValues(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("l", 43) + "-generated"
	root := privateTempDir(t)
	sink, err := Open(context.Background(), Options{Directory: root, Now: fixedClock(time.Now().UTC())})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	sink.Logger.InfoContext(context.Background(), EventStartup,
		"component", "composition",
		"operation", "configuration_load",
		"outcome", "success",
		"authorization", canary,
		"body", canary,
		"args", []string{canary},
		"error", errors.New(canary),
		"component", canary,
		"error_class", canary,
	)
	sink.Logger.InfoContext(context.Background(), canary, "operation", "configuration_load")
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	content := readCurrentLog(t, root)
	if strings.Contains(content, canary) {
		t.Fatal("log contains a sensitive canary")
	}
	for _, forbidden := range []string{"authorization", "body", "args", "error"} {
		if strings.Contains(content, `"`+forbidden+`"`) {
			t.Errorf("log contains forbidden field %q", forbidden)
		}
	}
	if strings.Count(content, "\n") != 1 {
		t.Fatalf("log record count = %d, want 1", strings.Count(content, "\n"))
	}
}

func TestFileLoggerBoundsRecordAndAttachedAttributes(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("b", 43) + "-generated"
	root := privateTempDir(t)
	sink, err := Open(context.Background(), Options{Directory: root})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	attributes := make([]any, 0, 200)
	for range 100 {
		attributes = append(attributes, "body", canary)
	}
	logger := sink.Logger.With(attributes...)
	logger.InfoContext(context.Background(), EventStartup,
		"component", "composition",
		"operation", "configuration_load",
		"outcome", "success",
	)
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	content := readCurrentLog(t, root)
	if strings.Contains(content, canary) {
		t.Fatal("bounded log contains an attached sensitive canary")
	}
	if len(content) > 1024 {
		t.Fatalf("bounded log record size = %d, want at most 1024", len(content))
	}
}

func TestFileLoggerHonorsLevelAndCancellation(t *testing.T) {
	t.Parallel()

	root := privateTempDir(t)
	sink, err := Open(context.Background(), Options{Directory: root, Level: slog.LevelWarn, Now: fixedClock(time.Now().UTC())})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	sink.Logger.InfoContext(context.Background(), EventStartup, "operation", "configuration_load")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink.Logger.ErrorContext(ctx, EventStartup, "operation", "configuration_load")
	sink.Logger.WarnContext(context.Background(), EventStartup,
		"component", "composition",
		"operation", "configuration_load",
		"outcome", "failure",
		"error_class", "configuration_invalid",
	)
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	content := readCurrentLog(t, root)
	if strings.Count(content, "\n") != 1 || !strings.Contains(content, `"level":"WARN"`) {
		t.Fatalf("unexpected filtered log: %q", content)
	}
}

func TestOpenHonorsCancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Open(ctx, Options{Directory: privateTempDir(t)})
	assertLoggingError(t, err, "cancelled", "log_open_cancelled")
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled logging error does not preserve cancellation semantics")
	}
}

func TestFileLoggerCreatesOwnerOnlyDirectoryAndFile(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	directory := filepath.Join(parent, "logs")
	sink, err := Open(context.Background(), Options{Directory: directory, Now: fixedClock(time.Now().UTC())})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	sink.Logger.InfoContext(context.Background(), EventStartup, "operation", "configuration_load")
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %04o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(directory, LogFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %04o, want 0600", got)
	}
}

func TestFileLoggerRejectsUnsafePathsAndPermissions(t *testing.T) {
	t.Parallel()

	t.Run("relative directory", func(t *testing.T) {
		_, err := Open(context.Background(), Options{Directory: "relative/logs"})
		assertLoggingError(t, err, "configuration_invalid", "log_path_invalid")
	})

	t.Run("directory symbolic link", func(t *testing.T) {
		root := privateTempDir(t)
		target := filepath.Join(root, "target")
		link := filepath.Join(root, "logs")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), Options{Directory: link})
		assertLoggingError(t, err, "configuration_invalid", "log_path_unsafe")
	})

	t.Run("broad directory permissions", func(t *testing.T) {
		root := privateTempDir(t)
		directory := filepath.Join(root, "logs")
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), Options{Directory: directory})
		assertLoggingError(t, err, "configuration_invalid", "log_permissions_unsafe")
	})

	t.Run("log file symbolic link", func(t *testing.T) {
		root := privateTempDir(t)
		target := filepath.Join(root, "target.log")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, LogFileName)); err != nil {
			t.Fatal(err)
		}
		_, err := Open(context.Background(), Options{Directory: root})
		assertLoggingError(t, err, "configuration_invalid", "log_path_unsafe")
	})
}

func TestFileLoggerRotatesAtByteAndCountCeilings(t *testing.T) {
	t.Parallel()

	root := privateTempDir(t)
	now := time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC)
	sink, err := Open(context.Background(), Options{
		Directory:    root,
		MaxFileBytes: 256,
		MaxFiles:     2,
		MaxAge:       time.Hour,
		Now:          fixedClock(now),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	for range 12 {
		sink.Logger.InfoContext(context.Background(), EventStartup,
			"component", "composition",
			"operation", "configuration_load",
			"outcome", "success",
		)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("log file count = %d, want 2", len(entries))
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > 256 {
			t.Errorf("%s size = %d, exceeds 256", entry.Name(), info.Size())
		}
	}
}

func TestFileLoggerPrunesFilesAtAgeCeiling(t *testing.T) {
	t.Parallel()

	root := privateTempDir(t)
	current := filepath.Join(root, LogFileName)
	archive := current + ".1"
	for _, path := range []string{current, archive} {
		if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC)
	old := now.Add(-2 * time.Hour)
	for _, path := range []string{current, archive} {
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}

	sink, err := Open(context.Background(), Options{
		Directory:    root,
		MaxFileBytes: 256,
		MaxFiles:     2,
		MaxAge:       time.Hour,
		Now:          fixedClock(now),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	sink.Logger.InfoContext(context.Background(), EventStartup, "operation", "configuration_load")
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("old archive still exists: %v", err)
	}
	if content := readCurrentLog(t, root); strings.Contains(content, "old") {
		t.Fatal("current log retained content past the age ceiling")
	}
}

func TestFileLoggerPrunesKnownFilesAboveByteCeiling(t *testing.T) {
	t.Parallel()

	root := privateTempDir(t)
	current := filepath.Join(root, LogFileName)
	archive := current + ".1"
	for _, path := range []string{current, archive} {
		if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, 257), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sink, err := Open(context.Background(), Options{
		Directory:    root,
		MaxFileBytes: 256,
		MaxFiles:     2,
		MaxAge:       time.Hour,
		Now:          fixedClock(time.Now().UTC()),
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if info, err := os.Stat(current); err != nil {
		t.Fatal(err)
	} else if info.Size() > 256 {
		t.Fatalf("current log size = %d, exceeds 256", info.Size())
	}
	if _, err := os.Stat(archive); !os.IsNotExist(err) {
		t.Fatalf("oversized archive still exists: %v", err)
	}
}

func TestOpenRejectsExpandedOrInvalidRotationLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options Options
	}{
		{name: "expanded bytes", options: Options{MaxFileBytes: DefaultMaxFileBytes + 1}},
		{name: "expanded file count", options: Options{MaxFiles: DefaultMaxFiles + 1}},
		{name: "expanded age", options: Options{MaxAge: DefaultMaxAge + time.Second}},
		{name: "too-small bytes", options: Options{MaxFileBytes: 1}},
		{name: "zero file count after explicit validation", options: Options{MaxFileBytes: 256, MaxFiles: -1}},
		{name: "negative age", options: Options{MaxAge: -time.Second}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tt.options.Directory = privateTempDir(t)
			_, err := Open(context.Background(), tt.options)
			assertLoggingError(t, err, "configuration_invalid", "log_limits_invalid")
		})
	}
}

func readCurrentLog(t *testing.T, directory string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(directory, LogFileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func fixedClock(value time.Time) func() time.Time {
	return func() time.Time { return value }
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func assertLoggingError(t *testing.T, err error, class string, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want class %q and code %q", class, code)
	}
	safe, ok := err.(*SafeError)
	if !ok {
		t.Fatalf("error type = %T, want *SafeError", err)
	}
	if string(safe.Class()) != class || safe.Code() != code {
		t.Fatalf("error class/code = %q/%q, want %q/%q", safe.Class(), safe.Code(), class, code)
	}
}
