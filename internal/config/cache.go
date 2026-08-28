package config

import (
	"context"
)

// ClearCache removes only entries below the fixed cache child. A missing cache
// is an idempotent success and neither Home nor cache is created.
func ClearCache(ctx context.Context, paths Paths) error {
	if ctx == nil || ctx.Err() != nil {
		return newSafeError(ClassCancelled, "cache_clear_cancelled", "clear_cache", "Cache clearing was cancelled.")
	}
	expected := pathsForHome(paths.HomeDir)
	if !validHomePath(paths.HomeDir) || expected.CacheDir != paths.CacheDir {
		return newSafeError(ClassConfigurationInvalid, "cache_path_invalid", "clear_cache", "KuPilot could not resolve its fixed cache directory safely.")
	}
	return clearCacheContents(ctx, paths.CacheDir)
}
