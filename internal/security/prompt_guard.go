package security

import "strings"

// UntrustedDataClass is the fixed marker for projected external text. It is a
// trust label, not an instruction parser or authorization decision.
const UntrustedDataClass = "untrusted_external_data"

var instructionLikeMarkers = []string{
	"ignore previous instruction",
	"ignore system instruction",
	"system prompt",
	"developer message",
	"call run_shell",
	"call the tool",
	"use the tool",
	"execute kubectl",
	"run kubectl",
	"<|system|>",
}

// InstructionLike reports metadata only. Every external value remains
// untrusted whether or not it happens to match one of these markers.
func InstructionLike(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range instructionLikeMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
