package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestClearCacheIsIdempotentAndDoesNotCreateHome(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "absent-home")
	if err := ClearCache(context.Background(), pathsForHome(root)); err != nil {
		t.Fatalf("ClearCache() error = %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ClearCache() created Home: %v", err)
	}
}

func TestClearCacheRemovesOnlyCacheContentsWithoutFollowingSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink contract")
	}
	root := t.TempDir()
	paths := pathsForHome(root)
	for _, path := range []string{paths.StateDir, paths.LogDir, paths.CacheDir, filepath.Join(paths.CacheDir, "nested")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	preserved := []string{paths.ConfigFile, filepath.Join(paths.StateDir, "kupilot.db"), filepath.Join(paths.LogDir, "kupilot.log")}
	for _, path := range preserved {
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(paths.CacheDir, "nested", "entry"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDirectory := t.TempDir()
	outside := filepath.Join(outsideDirectory, "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDirectory, filepath.Join(paths.CacheDir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := ClearCache(context.Background(), paths); err != nil {
		t.Fatalf("ClearCache() error = %v", err)
	}
	entries, err := os.ReadDir(paths.CacheDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cache entries = %v, %v", entries, err)
	}
	for _, path := range preserved {
		if content, err := os.ReadFile(path); err != nil || string(content) != "preserve" {
			t.Fatalf("preserved path changed: %v", err)
		}
	}
	if content, err := os.ReadFile(outside); err != nil || string(content) != "outside" {
		t.Fatal("cache symlink target was changed")
	}
}

func TestClearCacheRejectsCacheSymlinkAndCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink contract")
	}
	root := t.TempDir()
	paths := pathsForHome(root)
	target := t.TempDir()
	if err := os.Symlink(target, paths.CacheDir); err != nil {
		t.Fatal(err)
	}
	err := ClearCache(context.Background(), paths)
	assertSafeError(t, err, ClassConfigurationInvalid, "cache_path_unsafe")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = ClearCache(ctx, paths)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ClearCache() error = %v", err)
	}
}

func TestClearCacheRejectsNonDirectoryCache(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	paths := pathsForHome(root)
	if err := os.WriteFile(paths.CacheDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ClearCache(context.Background(), paths)
	assertSafeError(t, err, ClassConfigurationInvalid, "cache_path_unsafe")
}

func TestClearCacheReportsCancellationAfterPartialRemoval(t *testing.T) {
	root := t.TempDir()
	paths := pathsForHome(root)
	if err := os.Mkdir(paths.CacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(paths.CacheDir, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := &cancelAfterErrChecks{remaining: 2}
	err := ClearCache(ctx, paths)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ClearCache() error = %v", err)
	}
	entries, readErr := os.ReadDir(paths.CacheDir)
	if readErr != nil || len(entries) != 1 {
		t.Fatalf("remaining cache entries = %v, %v; want one after partial cancellation", entries, readErr)
	}
}

type cancelAfterErrChecks struct {
	context.Context
	remaining int
}

func (ctx *cancelAfterErrChecks) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancelAfterErrChecks) Done() <-chan struct{}       { return nil }
func (ctx *cancelAfterErrChecks) Value(any) any               { return nil }
func (ctx *cancelAfterErrChecks) Err() error {
	ctx.remaining--
	if ctx.remaining < 0 {
		return context.Canceled
	}
	return nil
}
