//go:build darwin || linux

package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClearCacheEntryReplacementDoesNotEscapeCache(t *testing.T) {
	root := t.TempDir()
	paths := pathsForHome(root)
	victim := filepath.Join(paths.CacheDir, "victim")
	if err := os.MkdirAll(victim, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "entry"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	displaced := filepath.Join(root, "concurrently-displaced")
	outside := t.TempDir()
	outsideFile := filepath.Join(outside, "outside")
	if err := os.WriteFile(outsideFile, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}

	replaced := false
	err := clearCacheContentsWithHook(context.Background(), paths.CacheDir, func(name string) {
		if replaced || name != "victim" {
			return
		}
		replaced = true
		if renameErr := os.Rename(victim, displaced); renameErr != nil {
			t.Fatal(renameErr)
		}
		if linkErr := os.Symlink(outside, victim); linkErr != nil {
			t.Fatal(linkErr)
		}
	})
	if err != nil {
		t.Fatalf("clearCacheContentsWithHook() error = %v", err)
	}
	if !replaced {
		t.Fatal("replacement hook was not exercised")
	}
	if content, readErr := os.ReadFile(outsideFile); readErr != nil || string(content) != "preserve" {
		t.Fatalf("replacement link target changed: content=%q error=%v", content, readErr)
	}
	if content, readErr := os.ReadFile(filepath.Join(displaced, "entry")); readErr != nil || string(content) != "cache" {
		t.Fatalf("concurrently displaced directory changed: content=%q error=%v", content, readErr)
	}
	entries, readErr := os.ReadDir(paths.CacheDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("cache entries = %v, %v", entries, readErr)
	}
}
