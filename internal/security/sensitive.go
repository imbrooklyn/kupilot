package security

import (
	"regexp"
	"strings"
)

const redactedValue = "[REDACTED]"

var (
	privateKeyPattern = regexp.MustCompile(`(?i)-----BEGIN[ ]+(?:[A-Z0-9]+[ ]+)*PRIVATE[ ]+KEY-----`)
	sensitivePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]{8,256}`),
		regexp.MustCompile(`(?i)\b(?:password|passwd|api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret)[ \t]*[:=][ \t]*[^\s,;]{1,256}`),
		regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.-]{1,20}://[^/\s:@]{1,128}:[^/\s@]{1,256}@`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,256}\.[A-Za-z0-9_-]{8,512}\.[A-Za-z0-9_-]{8,512}\b`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	}
)

func containsBlockedSensitiveValue(value string) bool {
	return privateKeyPattern.MatchString(value)
}

func redactSensitiveValues(value string) (string, int) {
	redactions := 0
	for _, pattern := range sensitivePatterns {
		value = pattern.ReplaceAllStringFunc(value, func(string) string {
			redactions++
			return redactedValue
		})
	}
	return strings.TrimSpace(value), redactions
}
