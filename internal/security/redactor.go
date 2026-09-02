// Package security provides bounded, deterministic safety primitives for
// untrusted external text and model-bound JSON.
package security

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrSensitiveOutputBlocked reports high-risk content that cannot be
	// represented safely by a replacement token.
	ErrSensitiveOutputBlocked = errors.New("sensitive output was blocked")
	// ErrInvalidTextPolicy reports an invalid local processing ceiling.
	ErrInvalidTextPolicy = errors.New("text safety policy is invalid")
)

var terminalEscapePattern = regexp.MustCompile(`(?:\x1b\][^\x07\x1b]*(?:\x07|\x1b\\))|(?:\x1b\[[0-?]*[ -/]*[@-~])`)

// TextResult is a bounded derivative of one untrusted external string.
type TextResult struct {
	Value           string
	RedactionCount  int
	Truncated       bool
	InstructionLike bool
}

// Redactor applies the fixed normalization and sensitive-value policy. It is
// stateless and safe for concurrent use.
type Redactor struct{}

// NewRedactor returns the fixed text processor.
func NewRedactor() *Redactor {
	return &Redactor{}
}

// Process normalizes external text, removes terminal controls, blocks or
// redacts sensitive values, and finally enforces a UTF-8 byte ceiling.
func (*Redactor) Process(value string, maximumBytes int) (TextResult, error) {
	if maximumBytes < 1 {
		return TextResult{}, ErrInvalidTextPolicy
	}
	normalized := normalizeExternalText(value)
	if containsBlockedSensitiveValue(normalized) {
		return TextResult{}, ErrSensitiveOutputBlocked
	}
	redacted, count := redactSensitiveValues(normalized)
	bounded, truncated := truncateUTF8(redacted, maximumBytes)
	return TextResult{
		Value:           bounded,
		RedactionCount:  count,
		Truncated:       truncated,
		InstructionLike: InstructionLike(bounded),
	}, nil
}

// ProcessLines applies the same fixed policy while preserving normalized line
// boundaries for multiline external text.
func (*Redactor) ProcessLines(value string, maximumBytes int) (TextResult, error) {
	if maximumBytes < 1 {
		return TextResult{}, ErrInvalidTextPolicy
	}
	normalized := normalizeExternalLogText(value)
	if containsBlockedSensitiveValue(normalized) {
		return TextResult{}, ErrSensitiveOutputBlocked
	}
	redacted, count := redactSensitiveValues(normalized)
	bounded, truncated := truncateUTF8(redacted, maximumBytes)
	return TextResult{
		Value:           bounded,
		RedactionCount:  count,
		Truncated:       truncated,
		InstructionLike: InstructionLike(bounded),
	}, nil
}

func normalizeExternalText(value string) string {
	value = strings.ToValidUTF8(value, string(utf8.RuneError))
	value = terminalEscapePattern.ReplaceAllString(value, " ")
	var builder strings.Builder
	builder.Grow(len(value))
	for _, current := range value {
		if unsafeExternalRune(current) {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(current)
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func normalizeExternalLogText(value string) string {
	value = strings.ToValidUTF8(value, string(utf8.RuneError))
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = terminalEscapePattern.ReplaceAllString(value, " ")
	var builder strings.Builder
	builder.Grow(len(value))
	for _, current := range value {
		if current == '\n' {
			builder.WriteRune(current)
			continue
		}
		if unsafeExternalRune(current) {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(current)
	}
	lines := strings.Split(builder.String(), "\n")
	for index := range lines {
		lines[index] = strings.TrimRightFunc(lines[index], unicode.IsSpace)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func truncateUTF8(value string, maximumBytes int) (string, bool) {
	if len(value) <= maximumBytes {
		return value, false
	}
	var builder strings.Builder
	builder.Grow(maximumBytes)
	for _, current := range value {
		width := utf8.RuneLen(current)
		if width < 0 || builder.Len()+width > maximumBytes {
			break
		}
		builder.WriteRune(current)
	}
	return strings.TrimSpace(builder.String()), true
}

func unsafeExternalRune(value rune) bool {
	return unicode.IsControl(value) || unicode.In(value, unicode.Cf) ||
		value == '\u061c' || value == '\u200e' || value == '\u200f' ||
		value >= '\u202a' && value <= '\u202e' || value >= '\u2066' && value <= '\u2069'
}
