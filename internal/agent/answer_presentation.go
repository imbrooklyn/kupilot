package agent

import "strings"

const inlineCitationStart = "\ue200cite\ue202"

// AnswerPresentation removes complete inline citation artifacts, not structured
// Evidence references. Callers bound input and screen sensitive content on both
// sides of removal; this type owns only presentation.
// The zero value is ready for fragments; Finish preserves incomplete tokens.
type AnswerPresentation struct {
	prefix string
	token  strings.Builder
	inside bool
}

func (presentation *AnswerPresentation) Push(text string) string {
	var out strings.Builder
	for _, char := range text {
		if presentation.inside {
			presentation.token.WriteRune(char)
			if char == '\ue201' {
				presentation.token.Reset()
				presentation.inside = false
			}
			continue
		}
		candidate := presentation.prefix + string(char)
		if strings.HasPrefix(inlineCitationStart, candidate) {
			presentation.prefix = candidate
			if candidate == inlineCitationStart {
				presentation.token.WriteString(candidate)
				presentation.prefix = ""
				presentation.inside = true
			}
			continue
		}
		out.WriteString(presentation.prefix)
		presentation.prefix = ""
		if char == '\ue200' {
			presentation.prefix = string(char)
		} else {
			out.WriteRune(char)
		}
	}
	return out.String()
}

func (presentation *AnswerPresentation) Finish() string {
	tail := presentation.token.String() + presentation.prefix
	presentation.token.Reset()
	presentation.prefix = ""
	presentation.inside = false
	return tail
}
