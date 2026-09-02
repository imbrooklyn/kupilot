package components

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// unicodeLineBreakMarker exposes UAX #14 opportunities to the ANSI-aware word
// wrapper without consuming a terminal cell. The renderer removes any source
// occurrence before adding and then removing its own private markers.
const unicodeLineBreakMarker = "\u200b"

func wrapTerminalText(value string, width int) string {
	value = strings.ReplaceAll(value, unicodeLineBreakMarker, "")
	if value == "" {
		return ""
	}
	marked := insertUnicodeLineBreakOpportunities(value)
	wrapped := lipgloss.Wrap(marked, max(1, width), unicodeLineBreakMarker)
	return strings.ReplaceAll(wrapped, unicodeLineBreakMarker, "")
}

func insertUnicodeLineBreakOpportunities(value string) string {
	offsets := unicodeLineBreakOffsets(ansi.Strip(value))
	if len(offsets) == 0 {
		return value
	}

	var builder strings.Builder
	builder.Grow(len(value) + len(offsets)*len(unicodeLineBreakMarker))
	state := ansi.NormalState
	visibleOffset := 0
	remaining := value
	for len(remaining) > 0 {
		sequence, _, consumed, nextState := ansi.DecodeSequence(remaining, state, nil)
		if consumed <= 0 || consumed > len(remaining) {
			return value
		}
		builder.WriteString(sequence)
		visibleOffset += len(ansi.Strip(sequence))
		if _, ok := offsets[visibleOffset]; ok {
			builder.WriteString(unicodeLineBreakMarker)
		}
		remaining = remaining[consumed:]
		state = nextState
	}
	return builder.String()
}

func unicodeLineBreakOffsets(value string) map[int]struct{} {
	offsets := make(map[int]struct{})
	remaining := value
	state := -1
	offset := 0
	for len(remaining) > 0 {
		cluster, rest, boundaries, nextState := uniseg.StepString(remaining, state)
		if cluster == "" || len(rest) >= len(remaining) {
			break
		}
		offset += len(cluster)
		if offset < len(value) && boundaries&uniseg.MaskLine == uniseg.LineCanBreak &&
			!endsInWhitespace(cluster) &&
			(containsEastAsianText(cluster) || startsWithEastAsianText(rest)) {
			offsets[offset] = struct{}{}
		}
		remaining = rest
		state = nextState
	}
	return offsets
}

func containsEastAsianText(value string) bool {
	for _, current := range value {
		if unicode.In(current, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Bopomofo) {
			return true
		}
	}
	return false
}

func startsWithEastAsianText(value string) bool {
	first, _ := utf8.DecodeRuneInString(value)
	return unicode.In(first, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Bopomofo)
}

func endsInWhitespace(value string) bool {
	last, _ := utf8.DecodeLastRuneInString(value)
	return unicode.IsSpace(last)
}
