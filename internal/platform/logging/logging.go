package logging

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	LogFileName               = "kupilot.log"
	DefaultMaxFileBytes       = int64(1024 * 1024)
	DefaultMaxFiles           = 3
	DefaultMaxAge             = 7 * 24 * time.Hour
	minimumFileBytes          = int64(256)
	maxRecordAttrs            = 12
	maxInspectedAttrs         = 32
	maxCallStackBytes         = 512
	maxCallStackFrames        = 32
	maxSensitiveAttrs         = 9
	maxSensitiveErrorBytes    = 16 * 1024
	maxSensitiveBodyBytes     = 4 * 1024
	maxSensitiveStackBytes    = 64 * 1024
	maxSensitiveEndpointBytes = 4096
	maxSensitiveModelBytes    = 128
	EventStartup              = "startup"
	EventAgentRun             = "agent_run"
	EventModelRequest         = "model_request"
	projectModulePrefix       = "github.com/imbrooklyn/kupilot/"
)

// Options configures one independent local file sink. Non-zero rotation values
// may tighten but cannot expand the code-defined ceilings.
type Options struct {
	Directory            string
	Level                slog.Level
	SensitiveDiagnostics bool
	MaxFileBytes         int64
	MaxFiles             int
	MaxAge               time.Duration
	Now                  func() time.Time
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
		level:                normalized.Level,
		now:                  normalized.Now,
		sensitiveDiagnostics: normalized.SensitiveDiagnostics,
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
		return newSafeError(ClassInternal, "log_close_failed", "Kupilot could not close its local log safely.")
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
	level                slog.Level
	now                  func() time.Time
	sensitiveDiagnostics bool
	json                 slog.Handler
	attrs                []slog.Attr
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
	if record.Message == EventModelRequest && record.Level >= slog.LevelWarn && accepted+2 <= maxRecordAttrs {
		if callStack, truncated := safeProjectCallStack(); callStack != "" {
			safeRecord.AddAttrs(
				slog.String("call_stack", callStack),
				slog.Bool("stack_truncated", truncated),
			)
			accepted += 2
		}
	}
	sensitiveAccepted := 0
	if handler.sensitiveDiagnostics && record.Message == EventModelRequest && record.Level >= slog.LevelWarn {
		safeRecord.AddAttrs(slog.Bool("sensitive_diagnostics", true))
		sensitiveAccepted++
		stack, truncated := boundedSensitiveString(string(debug.Stack()), maxSensitiveStackBytes)
		if stack != "" && sensitiveAccepted+2 <= maxSensitiveAttrs {
			safeRecord.AddAttrs(
				slog.String("sensitive_call_stack", stack),
				slog.Bool("sensitive_stack_truncated", truncated),
			)
			sensitiveAccepted += 2
		}
	}
	inspected := 0
	record.Attrs(func(attr slog.Attr) bool {
		inspected++
		if inspected > maxInspectedAttrs {
			return false
		}
		if accepted < maxRecordAttrs {
			if safe, ok := sanitizeAttr(attr); ok {
				safeRecord.AddAttrs(safe)
				accepted++
				return true
			}
		}
		if handler.sensitiveDiagnostics && record.Message == EventModelRequest && record.Level >= slog.LevelWarn && sensitiveAccepted < maxSensitiveAttrs {
			if safe, ok := sanitizeSensitiveAttr(attr); ok {
				safeRecord.AddAttrs(safe)
				sensitiveAccepted++
			}
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
	return value == EventStartup || value == EventAgentRun || value == EventModelRequest
}

func sanitizeAttr(attr slog.Attr) (slog.Attr, bool) {
	switch attr.Key {
	case "component":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(attr.Value.String(), "composition", "config", "logging", "application", "model") {
			return slog.Attr{}, false
		}
	case "operation":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(
			attr.Value.String(),
			"configuration_load", "run_lifecycle", "model_request", "model_stream", "model_capability",
		) {
			return slog.Attr{}, false
		}
	case "outcome":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(attr.Value.String(), "success", "failure", "cancelled") {
			return slog.Attr{}, false
		}
	case "error_class":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(
			attr.Value.String(),
			"configuration_invalid", "authentication_failed", "permission_denied",
			"unsupported", "policy_denied", "budget_exhausted", "rate_limited", "unavailable",
			"timeout", "cancelled", "invalid_external_response", "internal",
		) {
			return slog.Attr{}, false
		}
	case "error_code":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(
			attr.Value.String(),
			"model_authentication_failed", "model_permission_denied", "model_rate_limited", "model_service_unavailable",
			"model_response_unsupported", "model_stream_invalid", "model_stream_limit_exceeded",
			"model_redirect_denied", "model_request_timeout", "model_request_cancelled", "model_internal",
		) {
			return slog.Attr{}, false
		}
	case "request_id":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !validUUIDv7(attr.Value.String()) {
			return slog.Attr{}, false
		}
	case "cause":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(
			attr.Value.String(),
			"adapter_invariant", "context_cancelled", "deadline_exceeded", "http_status",
			"redirect_policy", "response_media", "stream_limit",
			"stream_protocol", "transport_timeout", "transport_unavailable", "transport_validation",
		) {
			return slog.Attr{}, false
		}
	case "provider_kind":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || attr.Value.String() != "openai_compatible" {
			return slog.Attr{}, false
		}
	case "phase":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindString || !oneOf(attr.Value.String(), "started", "terminal", "persistence_degraded") {
			return slog.Attr{}, false
		}
	case "count", "duration_ms", "sequence", "scope_generation":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindInt64 || attr.Value.Int64() < 0 || attr.Value.Int64() > 1_000_000_000 {
			return slog.Attr{}, false
		}
	case "http_status":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindInt64 || attr.Value.Int64() < 100 || attr.Value.Int64() > 599 {
			return slog.Attr{}, false
		}
	case "truncated", "degraded", "retryable":
		attr.Value = attr.Value.Resolve()
		if attr.Value.Kind() != slog.KindBool {
			return slog.Attr{}, false
		}
	default:
		return slog.Attr{}, false
	}
	return attr, true
}

func sanitizeSensitiveAttr(attr slog.Attr) (slog.Attr, bool) {
	attr.Value = attr.Value.Resolve()
	switch attr.Key {
	case "sensitive_endpoint":
		return boundedSensitiveAttr(attr, maxSensitiveEndpointBytes)
	case "sensitive_model":
		return boundedSensitiveAttr(attr, maxSensitiveModelBytes)
	case "sensitive_error_chain":
		return boundedSensitiveAttr(attr, maxSensitiveErrorBytes)
	case "sensitive_provider_error_body":
		return boundedSensitiveAttr(attr, maxSensitiveBodyBytes)
	case "sensitive_error_truncated", "sensitive_provider_body_truncated":
		if attr.Value.Kind() != slog.KindBool {
			return slog.Attr{}, false
		}
		return attr, true
	default:
		return slog.Attr{}, false
	}
}

func boundedSensitiveAttr(attr slog.Attr, maximum int) (slog.Attr, bool) {
	if attr.Value.Kind() != slog.KindString || attr.Value.String() == "" {
		return slog.Attr{}, false
	}
	value, _ := boundedSensitiveString(attr.Value.String(), maximum)
	attr.Value = slog.StringValue(value)
	return attr, true
}

func boundedSensitiveString(value string, maximum int) (string, bool) {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= maximum {
		return value, false
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func safeProjectCallStack() (string, bool) {
	programCounters := make([]uintptr, maxCallStackFrames)
	count := runtime.Callers(2, programCounters)
	truncated := count == len(programCounters)
	frames := runtime.CallersFrames(programCounters[:count])
	var builder strings.Builder
	for {
		frame, more := frames.Next()
		name := strings.TrimPrefix(frame.Function, projectModulePrefix)
		if name != frame.Function && !loggingStackFrame(name) {
			separatorBytes := 0
			if builder.Len() > 0 {
				separatorBytes = 1
			}
			if builder.Len()+separatorBytes+len(name) > maxCallStackBytes {
				truncated = true
				break
			}
			if separatorBytes != 0 {
				builder.WriteByte('>')
			}
			builder.WriteString(name)
		}
		if !more {
			break
		}
	}
	return builder.String(), truncated
}

func loggingStackFrame(name string) bool {
	return name == "internal/platform/logging.safeProjectCallStack" ||
		name == "internal/platform/logging.(*allowlistHandler).Handle"
}

func validUUIDv7(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' || value[14] != '7' ||
		!strings.ContainsRune("89ab", rune(value[19])) {
		return false
	}
	for index := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		current := value[index]
		if current < '0' || current > '9' {
			if current < 'a' || current > 'f' {
				return false
			}
		}
	}
	return true
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
