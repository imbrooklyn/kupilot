package logging

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"time"
)

const (
	LogFileName         = "kupilot.log"
	DefaultMaxFileBytes = int64(1024 * 1024)
	DefaultMaxFiles     = 3
	DefaultMaxAge       = 7 * 24 * time.Hour
	minimumFileBytes    = int64(256)
	maxRecordAttrs      = 12
	maxInspectedAttrs   = 32
	EventStartup        = "startup"
)

// Options configures one independent local file sink. Non-zero rotation values
// may tighten but cannot expand the code-defined ceilings.
type Options struct {
	Directory    string
	Level        slog.Level
	MaxFileBytes int64
	MaxFiles     int
	MaxAge       time.Duration
	Now          func() time.Time
}

// Sink owns one logger and its local file lifecycle.
type Sink struct {
	Logger *slog.Logger
	writer *rotatingWriter
}

// Open creates an owner-only, bounded JSON log sink. It never writes to stdout
// or stderr and does not install a process-global logger.
func Open(ctx context.Context, options Options) (*Sink, error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, newSafeError(ClassCancelled, "log_open_cancelled", "Local logging initialization was cancelled.")
	}
	normalized, err := normalizeOptions(options)
	if err != nil {
		return nil, err
	}
	writer, err := openRotatingWriter(ctx, normalized)
	if err != nil {
		return nil, err
	}
	handler := &allowlistHandler{
		level: normalized.Level,
		now:   normalized.Now,
		json: slog.NewJSONHandler(writer, &slog.HandlerOptions{
			Level: normalized.Level,
		}),
	}
	return &Sink{Logger: slog.New(handler), writer: writer}, nil
}

// Close flushes and releases the file owned by the sink.
func (sink *Sink) Close() error {
	if sink == nil || sink.writer == nil {
		return nil
	}
	if err := sink.writer.Close(); err != nil {
		return newSafeError(ClassInternal, "log_close_failed", "KuPilot could not close its local log safely.")
	}
	return nil
}

func normalizeOptions(options Options) (Options, error) {
	if options.Directory == "" || !filepath.IsAbs(options.Directory) || filepath.Clean(options.Directory) != options.Directory || filepath.Clean(options.Directory) == filepath.Clean(string(filepath.Separator)) {
		return Options{}, newSafeError(ClassConfigurationInvalid, "log_path_invalid", "The log directory must be an absolute, normalized, non-root path.")
	}
	if options.Level != slog.LevelInfo && options.Level != slog.LevelWarn && options.Level != slog.LevelError {
		return Options{}, newSafeError(ClassConfigurationInvalid, "log_level_invalid", "The local log level must be info, warn, or error.")
	}
	if options.MaxFileBytes == 0 {
		options.MaxFileBytes = DefaultMaxFileBytes
	}
	if options.MaxFiles == 0 {
		options.MaxFiles = DefaultMaxFiles
	}
	if options.MaxAge == 0 {
		options.MaxAge = DefaultMaxAge
	}
	if options.MaxFileBytes < minimumFileBytes || options.MaxFileBytes > DefaultMaxFileBytes ||
		options.MaxFiles < 1 || options.MaxFiles > DefaultMaxFiles ||
		options.MaxAge < time.Second || options.MaxAge > DefaultMaxAge {
		return Options{}, newSafeError(ClassConfigurationInvalid, "log_limits_invalid", "Local log rotation limits may only tighten the documented byte, file-count, and age ceilings.")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	return options, nil
}

type allowlistHandler struct {
	level slog.Level
	now   func() time.Time
	json  slog.Handler
	attrs []slog.Attr
}

func (handler *allowlistHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return ctx != nil && ctx.Err() == nil && level >= handler.level
}

func (handler *allowlistHandler) Handle(ctx context.Context, record slog.Record) error {
	if ctx == nil || ctx.Err() != nil || !allowedEvent(record.Message) {
		if ctx != nil {
			return ctx.Err()
		}
		return context.Canceled
	}
	safeRecord := slog.NewRecord(handler.now().UTC(), record.Level, record.Message, 0)
	for _, attr := range handler.attrs {
		safeRecord.AddAttrs(attr)
	}
	accepted := len(handler.attrs)
	inspected := 0
	record.Attrs(func(attr slog.Attr) bool {
		inspected++
		if inspected > maxInspectedAttrs || accepted >= maxRecordAttrs {
			return false
		}
		if safe, ok := sanitizeAttr(attr); ok {
			safeRecord.AddAttrs(safe)
			accepted++
		}
		return true
	})
	return handler.json.Handle(ctx, safeRecord)
}

func (handler *allowlistHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *handler
	clone.attrs = append([]slog.Attr(nil), handler.attrs...)
	for index, attr := range attrs {
		if index >= maxInspectedAttrs || len(clone.attrs) >= maxRecordAttrs {
			break
		}
		if safe, ok := sanitizeAttr(attr); ok {
			clone.attrs = append(clone.attrs, safe)
		}
	}
	return &clone
}

func (handler *allowlistHandler) WithGroup(string) slog.Handler {
	clone := *handler
	return &clone
}

func allowedEvent(value string) bool {
	return value == EventStartup
}

func sanitizeAttr(attr slog.Attr) (slog.Attr, bool) {
	switch attr.Key {
	case "component":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(attr.Value.String(), "composition", "config", "logging") {
			return slog.Attr{}, false
		}
	case "operation":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || attr.Value.String() != "configuration_load" {
			return slog.Attr{}, false
		}
	case "outcome":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(attr.Value.String(), "success", "failure", "cancelled") {
			return slog.Attr{}, false
		}
	case "error_class":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(attr.Value.String(), "configuration_invalid", "unsupported", "cancelled", "internal") {
			return slog.Attr{}, false
		}
	case "provider_kind":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || attr.Value.String() != "openai_compatible" {
			return slog.Attr{}, false
		}
	case "count", "duration_ms", "sequence":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindInt64 || attr.Value.Int64() < 0 || attr.Value.Int64() > 1_000_000_000 {
			return slog.Attr{}, false
		}
	case "truncated", "degraded":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindBool {
			return slog.Attr{}, false
		}
	default:
		return slog.Attr{}, false
	}
	return attr, true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

var _ slog.Handler = (*allowlistHandler)(nil)
var _ io.Closer = (*rotatingWriter)(nil)
