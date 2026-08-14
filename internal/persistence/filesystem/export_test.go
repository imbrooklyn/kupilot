package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/imbrooklyn/kupilot/internal/application"
)

func TestExportWriterPublishesOwnerOnlyFileWithoutOverwrite(t *testing.T) {
	t.Parallel()
	directory := privateExportDirectory(t)
	target := filepath.Join(directory, "session-summary.md")
	writer := NewExportWriter()
	content := []byte("# KuPilot Session Summary\n\nSafe content.\n")

	if err := writer.WriteSummary(context.Background(), application.ExportFile{TargetPath: target, Content: content}); err != nil {
		t.Fatalf("WriteSummary() error = %v", err)
	}
	assertExportFile(t, target, content)
	assertNoExportTemporaryFiles(t, directory)

	if err := writer.WriteSummary(context.Background(), application.ExportFile{
		TargetPath: target,
		Content:    []byte("replacement must not be written"),
	}); !errors.Is(err, ErrExportTargetExists) {
		t.Fatalf("second WriteSummary() error = %v, want ErrExportTargetExists", err)
	}
	assertExportFile(t, target, content)
	assertNoExportTemporaryFiles(t, directory)
}

func TestExportWriterRejectsUnsafeTargetsWithoutPartialFiles(t *testing.T) {
	t.Parallel()
	privateDirectory := privateExportDirectory(t)
	outside := privateExportDirectory(t)
	regular := filepath.Join(outside, "regular.md")
	if err := os.WriteFile(regular, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	directoryTarget := filepath.Join(privateDirectory, "directory.md")
	if err := os.Mkdir(directoryTarget, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	tests := []struct {
		name   string
		target string
		setup  func(*testing.T, string)
		want   error
	}{
		{name: "relative", target: "summary.md", want: ErrInvalidExportTarget},
		{name: "unclean", target: privateDirectory + string(os.PathSeparator) + "nested" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "summary.md", want: ErrInvalidExportTarget},
		{name: "control character", target: filepath.Join(privateDirectory, "unsafe\nsummary.md"), want: ErrInvalidExportTarget},
		{name: "wrong extension", target: filepath.Join(privateDirectory, "summary.txt"), want: ErrInvalidExportTarget},
		{name: "directory target", target: directoryTarget, want: ErrExportPathUnsafe},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests,
			struct {
				name   string
				target string
				setup  func(*testing.T, string)
				want   error
			}{
				name: "symlink target", target: filepath.Join(privateDirectory, "linked.md"), want: ErrExportPathUnsafe,
				setup: func(t *testing.T, target string) {
					t.Helper()
					if err := os.Symlink(regular, target); err != nil {
						t.Fatalf("Symlink() error = %v", err)
					}
				},
			},
			struct {
				name   string
				target string
				setup  func(*testing.T, string)
				want   error
			}{
				name: "symlink parent", target: filepath.Join(privateDirectory, "linked-parent", "summary.md"), want: ErrExportPathUnsafe,
				setup: func(t *testing.T, target string) {
					t.Helper()
					if err := os.Symlink(outside, filepath.Dir(target)); err != nil {
						t.Fatalf("Symlink() error = %v", err)
					}
				},
			},
		)
	}

	writer := NewExportWriter()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.setup != nil {
				test.setup(t, test.target)
			}
			err := writer.WriteSummary(context.Background(), application.ExportFile{
				TargetPath: test.target,
				Content:    []byte("raw-export-canary-must-not-remain"),
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("WriteSummary() error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), test.target) || strings.Contains(err.Error(), "raw-export-canary") {
				t.Fatalf("safe error disclosed target or content: %q", err)
			}
			assertNoExportTemporaryFiles(t, privateDirectory)
		})
	}
}

func TestExportWriterRemovesTemporaryFileWhenDiskSyncFails(t *testing.T) {
	t.Parallel()
	directory := privateExportDirectory(t)
	target := filepath.Join(directory, "disk-failure.md")
	writer := &ExportWriter{syncFile: func(*os.File) error { return errors.New("synthetic disk failure") }}

	err := writer.WriteSummary(context.Background(), application.ExportFile{
		TargetPath: target,
		Content:    []byte("raw-disk-failure-canary"),
	})
	if !errors.Is(err, ErrExportWriteFailed) || strings.Contains(err.Error(), target) || strings.Contains(err.Error(), "raw-disk-failure-canary") {
		t.Fatalf("WriteSummary() error = %v", err)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target exists after disk failure: %v", statErr)
	}
	assertNoExportTemporaryFiles(t, directory)
}

func TestExportWriterConcurrentPublicationDoesNotReplace(t *testing.T) {
	t.Parallel()
	directory := privateExportDirectory(t)
	target := filepath.Join(directory, "concurrent.md")
	start := make(chan struct{})
	results := make(chan error, 2)
	contents := [][]byte{[]byte("first complete summary"), []byte("second complete summary")}
	for _, content := range contents {
		content := append([]byte(nil), content...)
		go func() {
			<-start
			results <- NewExportWriter().WriteSummary(context.Background(), application.ExportFile{TargetPath: target, Content: content})
		}()
	}
	close(start)
	firstErr, secondErr := <-results, <-results
	successes, exists := 0, 0
	for _, err := range []error{firstErr, secondErr} {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrExportTargetExists) {
			exists++
		} else {
			t.Fatalf("concurrent WriteSummary() error = %v", err)
		}
	}
	if successes != 1 || exists != 1 {
		t.Fatalf("concurrent outcomes: success=%d exists=%d", successes, exists)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != string(contents[0]) && string(content) != string(contents[1]) {
		t.Fatalf("published partial content = %q", content)
	}
	assertNoExportTemporaryFiles(t, directory)
}

func TestExportWriterRejectsUnsafeParentPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only Unix mode contract is not available on Windows")
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	target := filepath.Join(directory, "summary.md")
	err := NewExportWriter().WriteSummary(context.Background(), application.ExportFile{
		TargetPath: target,
		Content:    []byte("safe content"),
	})
	if !errors.Is(err, ErrExportPathUnsafe) {
		t.Fatalf("WriteSummary() error = %v, want ErrExportPathUnsafe", err)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target exists after denial: %v", statErr)
	}
	assertNoExportTemporaryFiles(t, directory)
}

func TestExportWriterRejectsWritableAncestorPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only Unix mode contract is not available on Windows")
	}
	root := privateExportDirectory(t)
	unsafeAncestor := filepath.Join(root, "shared")
	privateParent := filepath.Join(unsafeAncestor, "private")
	if err := os.Mkdir(unsafeAncestor, 0o700); err != nil {
		t.Fatalf("Mkdir(unsafe ancestor) error = %v", err)
	}
	if err := os.Chmod(unsafeAncestor, 0o777); err != nil {
		t.Fatalf("Chmod(unsafe ancestor) error = %v", err)
	}
	if err := os.Mkdir(privateParent, 0o700); err != nil {
		t.Fatalf("Mkdir(private parent) error = %v", err)
	}
	target := filepath.Join(privateParent, "summary.md")

	err := NewExportWriter().WriteSummary(context.Background(), application.ExportFile{
		TargetPath: target,
		Content:    []byte("safe content"),
	})
	if !errors.Is(err, ErrExportPathUnsafe) {
		t.Fatalf("WriteSummary() error = %v, want ErrExportPathUnsafe", err)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target exists after denial: %v", statErr)
	}
	assertNoExportTemporaryFiles(t, privateParent)
}

func TestExportWriterHonorsPreCancelledContext(t *testing.T) {
	t.Parallel()
	directory := privateExportDirectory(t)
	target := filepath.Join(directory, "cancelled.md")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewExportWriter().WriteSummary(ctx, application.ExportFile{TargetPath: target, Content: []byte("safe content")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteSummary() error = %v, want context.Canceled", err)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target exists after cancellation: %v", statErr)
	}
	assertNoExportTemporaryFiles(t, directory)
}

func TestExportWriterHonorsCancellationBeforeAtomicPublication(t *testing.T) {
	t.Parallel()
	directory := privateExportDirectory(t)
	target := filepath.Join(directory, "cancelled-before-publication.md")
	ctx, cancel := context.WithCancel(context.Background())
	writer := &ExportWriter{syncFile: func(*os.File) error {
		cancel()
		return nil
	}}

	err := writer.WriteSummary(ctx, application.ExportFile{TargetPath: target, Content: []byte("safe content")})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteSummary() error = %v, want context.Canceled", err)
	}
	if _, statErr := os.Lstat(target); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("target exists after cancellation: %v", statErr)
	}
	assertNoExportTemporaryFiles(t, directory)
}

func privateExportDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	directory = canonical
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			t.Fatalf("Chmod() error = %v", err)
		}
	}
	return directory
}

func assertExportFile(t *testing.T, target string, expected []byte) {
	t.Helper()
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != string(expected) {
		t.Fatalf("content = %q, want %q", content, expected)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("Lstat() error = %v", err)
	}
	if !info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("target mode = %v", info.Mode())
	}
}

func assertNoExportTemporaryFiles(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), exportTemporaryPrefix) {
			t.Fatalf("partial temporary file remained: %q", entry.Name())
		}
	}
}
