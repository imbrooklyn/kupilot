//go:build !darwin && !linux

package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

func clearCacheContents(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "Kupilot could not inspect its local cache.")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return newSafeError(ClassConfigurationInvalid, "cache_path_unsafe", "clear_cache", "The Kupilot cache path must be a directory and must not be a symbolic link.")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "Kupilot could not read its local cache.")
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return newSafeError(ClassCancelled, "cache_clear_cancelled", "clear_cache", "Cache clearing was cancelled before every entry could be removed.")
		}
		target := filepath.Join(path, entry.Name())
		if filepath.Dir(target) != path || target == path {
			return newSafeError(ClassConfigurationInvalid, "cache_path_unsafe", "clear_cache", "A cache entry could not be resolved safely.")
		}
		if err := removeCacheEntry(ctx, target); err != nil {
			return err
		}
	}
	return nil
}

func removeCacheEntry(ctx context.Context, target string) error {
	if err := ctx.Err(); err != nil {
		return newSafeError(ClassCancelled, "cache_clear_cancelled", "clear_cache", "Cache clearing was cancelled before every entry could be removed.")
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "A local cache entry could not be inspected.")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		if err := os.Remove(target); err != nil {
			return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "A local cache entry could not be removed; the cache may be only partially cleared.")
		}
		return nil
	}
	children, err := os.ReadDir(target)
	if err != nil {
		return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "A local cache directory could not be read; the cache may be only partially cleared.")
	}
	for _, child := range children {
		if err := removeCacheEntry(ctx, filepath.Join(target, child.Name())); err != nil {
			return err
		}
	}
	if err := os.Remove(target); err != nil {
		return newSafeError(ClassInternal, "cache_clear_failed", "clear_cache", "A local cache directory could not be removed; the cache may be only partially cleared.")
	}
	return nil
}
