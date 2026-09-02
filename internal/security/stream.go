package security

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxStreamingSensitiveCandidateBytes = 4 * 1024

var errStreamingTextLimit = errors.New("streaming text exceeded its fixed limit")

// StreamingRedactor incrementally produces a terminal-safe, sensitive-value-
// filtered projection of one bounded multiline value. It retains only the
// suffix needed to recognize a sensitive pattern split across input chunks.
// Finish must be called only after the complete source value is available.
type StreamingRedactor struct {
	maximumBytes int
	inputBytes   int
	emittedBytes int

	terminal   terminalStreamNormalizer
	sensitive  sensitiveStreamGuard
	whitespace streamingWhitespace
	finished   bool
}

// NewStreamingRedactor constructs one bounded incremental text processor.
func NewStreamingRedactor(maximumBytes int) (*StreamingRedactor, error) {
	if maximumBytes < 1 {
		return nil, ErrInvalidTextPolicy
	}
	return &StreamingRedactor{maximumBytes: maximumBytes}, nil
}

// Push accepts the next ordered source fragment and returns only newly safe
// text. A sensitive match split across fragments is never partially emitted.
func (redactor *StreamingRedactor) Push(value string) (string, error) {
	if redactor == nil || redactor.finished || !utf8.ValidString(value) ||
		len(value) > redactor.maximumBytes-redactor.inputBytes {
		return "", errStreamingTextLimit
	}
	redactor.inputBytes += len(value)
	normalized := redactor.terminal.push(value, false)
	filtered, err := redactor.sensitive.push(normalized, false)
	if err != nil {
		return "", err
	}
	return redactor.emit(redactor.whitespace.push(filtered))
}

// Finish flushes a complete admitted value and drops trailing whitespace in
// the same way as the non-streaming multiline processor.
func (redactor *StreamingRedactor) Finish() (string, error) {
	if redactor == nil || redactor.finished {
		return "", errStreamingTextLimit
	}
	redactor.finished = true
	normalized := redactor.terminal.push("", true)
	filtered, err := redactor.sensitive.push(normalized, true)
	if err != nil {
		return "", err
	}
	result, err := redactor.emit(redactor.whitespace.push(filtered))
	redactor.whitespace.finish()
	return result, err
}

func (redactor *StreamingRedactor) emit(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > redactor.maximumBytes-redactor.emittedBytes {
		return "", errStreamingTextLimit
	}
	redactor.emittedBytes += len(value)
	return value, nil
}

type terminalStreamState uint8

const (
	terminalStreamText terminalStreamState = iota
	terminalStreamEscape
	terminalStreamCSI
	terminalStreamControlString
	terminalStreamControlStringEscape
)

type terminalStreamNormalizer struct {
	state     terminalStreamState
	pendingCR bool
}

func (normalizer *terminalStreamNormalizer) push(value string, final bool) string {
	value = strings.ToValidUTF8(value, string(utf8.RuneError))
	var result strings.Builder
	for _, current := range value {
		if normalizer.pendingCR {
			normalizer.pendingCR = false
			result.WriteByte('\n')
			if current == '\n' {
				continue
			}
		}
		normalizer.consume(current, &result)
	}
	if final {
		if normalizer.pendingCR {
			result.WriteByte('\n')
			normalizer.pendingCR = false
		}
		if normalizer.state != terminalStreamText {
			result.WriteByte(' ')
			normalizer.state = terminalStreamText
		}
	}
	return result.String()
}

func (normalizer *terminalStreamNormalizer) consume(current rune, result *strings.Builder) {
	for {
		switch normalizer.state {
		case terminalStreamEscape:
			switch current {
			case '[':
				normalizer.state = terminalStreamCSI
			case ']', 'P', 'X', '^', '_':
				normalizer.state = terminalStreamControlString
			default:
				result.WriteByte(' ')
				normalizer.state = terminalStreamText
				continue
			}
			return
		case terminalStreamCSI:
			if current >= 0x40 && current <= 0x7e {
				result.WriteByte(' ')
				normalizer.state = terminalStreamText
			}
			return
		case terminalStreamControlString:
			switch current {
			case 0x07, 0x9c:
				result.WriteByte(' ')
				normalizer.state = terminalStreamText
			case 0x1b:
				normalizer.state = terminalStreamControlStringEscape
			}
			return
		case terminalStreamControlStringEscape:
			if current == '\\' {
				result.WriteByte(' ')
				normalizer.state = terminalStreamText
			} else if current != 0x1b {
				normalizer.state = terminalStreamControlString
			}
			return
		}

		switch current {
		case '\r':
			normalizer.pendingCR = true
		case '\n':
			result.WriteByte('\n')
		case 0x1b:
			normalizer.state = terminalStreamEscape
		case 0x9b:
			normalizer.state = terminalStreamCSI
		case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
			normalizer.state = terminalStreamControlString
		default:
			if unsafeExternalRune(current) {
				result.WriteByte(' ')
			} else {
				result.WriteRune(current)
			}
		}
		return
	}
}

type sensitiveTriggerKind uint8

const (
	sensitiveAssignment sensitiveTriggerKind = iota + 1
	sensitiveBearer
	sensitiveJWT
	sensitiveAWS
	sensitiveBasicURL
	sensitivePrivateKey
)

type sensitiveTrigger struct {
	kind   sensitiveTriggerKind
	start  int
	length int
}

var streamingAssignmentKeys = []string{
	"password", "passwd", "api_key", "api-key", "apikey", "access_token",
	"access-token", "accesstoken", "refresh_token", "refresh-token",
	"refreshtoken", "token", "secret",
}

var streamingLiteralTriggers = []struct {
	value    string
	kind     sensitiveTriggerKind
	boundary bool
}{
	{value: "-----BEGIN", kind: sensitivePrivateKey},
	{value: "Bearer", kind: sensitiveBearer, boundary: true},
	{value: "eyJ", kind: sensitiveJWT, boundary: true},
	{value: "AKIA", kind: sensitiveAWS, boundary: true},
}

type sensitiveStreamGuard struct {
	buffer string
}

func (guard *sensitiveStreamGuard) push(value string, final bool) (string, error) {
	guard.buffer += value
	var result strings.Builder
	for guard.buffer != "" {
		trigger, found := earliestSensitiveTrigger(guard.buffer)
		if found && trigger.start > 0 {
			result.WriteString(guard.buffer[:trigger.start])
			guard.buffer = guard.buffer[trigger.start:]
			trigger.start = 0
		}
		if found {
			consumed, replacement, wait, err := resolveSensitiveTrigger(guard.buffer, trigger, final)
			if err != nil {
				return "", err
			}
			if wait {
				if len(guard.buffer) > maxStreamingSensitiveCandidateBytes {
					return "", ErrSensitiveOutputBlocked
				}
				break
			}
			if consumed < 1 || consumed > len(guard.buffer) {
				return "", ErrInvalidTextPolicy
			}
			result.WriteString(replacement)
			guard.buffer = guard.buffer[consumed:]
			continue
		}

		keep := longestPotentialSensitiveSuffix(guard.buffer)
		if final {
			keep = 0
		}
		cut := len(guard.buffer) - keep
		if cut == 0 {
			if len(guard.buffer) > maxStreamingSensitiveCandidateBytes {
				return "", ErrSensitiveOutputBlocked
			}
			break
		}
		result.WriteString(guard.buffer[:cut])
		guard.buffer = guard.buffer[cut:]
	}
	return result.String(), nil
}

func earliestSensitiveTrigger(value string) (sensitiveTrigger, bool) {
	best := sensitiveTrigger{start: len(value) + 1}
	record := func(candidate sensitiveTrigger) {
		if candidate.start < best.start {
			best = candidate
		}
	}
	for index := 0; index < len(value); index++ {
		for _, candidate := range streamingLiteralTriggers {
			if len(value)-index < len(candidate.value) ||
				!strings.EqualFold(value[index:index+len(candidate.value)], candidate.value) ||
				candidate.boundary && !asciiWordBoundaryBefore(value, index) {
				continue
			}
			record(sensitiveTrigger{kind: candidate.kind, start: index, length: len(candidate.value)})
		}
		for _, key := range streamingAssignmentKeys {
			if len(value)-index >= len(key) && strings.EqualFold(value[index:index+len(key)], key) &&
				asciiWordBoundaryBefore(value, index) {
				record(sensitiveTrigger{kind: sensitiveAssignment, start: index, length: len(key)})
			}
		}
		if length := basicURLTriggerLength(value[index:]); length > 0 && asciiWordBoundaryBefore(value, index) {
			record(sensitiveTrigger{kind: sensitiveBasicURL, start: index, length: length})
		}
	}
	return best, best.start <= len(value)
}

func resolveSensitiveTrigger(value string, trigger sensitiveTrigger, final bool) (int, string, bool, error) {
	switch trigger.kind {
	case sensitiveAssignment:
		return resolveAssignment(value, trigger.length, final)
	case sensitiveBearer:
		return resolveBearer(value, trigger.length, final)
	case sensitiveJWT:
		return resolveDelimitedCandidate(value, isJWTCandidateByte, final)
	case sensitiveAWS:
		return resolveDelimitedCandidate(value, isASCIIWordByte, final)
	case sensitiveBasicURL:
		return resolveURL(value, final)
	case sensitivePrivateKey:
		if containsBlockedSensitiveValue(value) {
			return 0, "", false, ErrSensitiveOutputBlocked
		}
		if end := strings.IndexByte(value, '\n'); end >= 0 {
			return end, value[:end], false, nil
		}
		if !final {
			return 0, "", true, nil
		}
		return len(value), value, false, nil
	default:
		return 0, "", false, ErrInvalidTextPolicy
	}
}

func resolveAssignment(value string, triggerLength int, final bool) (int, string, bool, error) {
	position := triggerLength
	if position == len(value) && !final {
		return 0, "", true, nil
	}
	if position < len(value) && isASCIIWordByte(value[position]) {
		return triggerLength, value[:triggerLength], false, nil
	}
	for position < len(value) && (value[position] == ' ' || value[position] == '\t') {
		position++
	}
	if position == len(value) {
		if !final {
			return 0, "", true, nil
		}
		return len(value), value, false, nil
	}
	if value[position] != ':' && value[position] != '=' {
		return position, value[:position], false, nil
	}
	position++
	for position < len(value) && (value[position] == ' ' || value[position] == '\t') {
		position++
	}
	if position == len(value) {
		if !final {
			return 0, "", true, nil
		}
		return len(value), value, false, nil
	}
	current, _ := utf8.DecodeRuneInString(value[position:])
	if unicode.IsSpace(current) || current == ',' || current == ';' {
		return position, value[:position], false, nil
	}
	end := sensitiveValueEnd(value, position)
	if end == len(value) && !final {
		return 0, "", true, nil
	}
	return end, redactedValue, false, nil
}

func resolveBearer(value string, triggerLength int, final bool) (int, string, bool, error) {
	position := triggerLength
	if position == len(value) && !final {
		return 0, "", true, nil
	}
	spaces := position
	for position < len(value) && (value[position] == ' ' || value[position] == '\t') {
		position++
	}
	if position == spaces {
		return triggerLength, value[:triggerLength], false, nil
	}
	if position == len(value) {
		if !final {
			return 0, "", true, nil
		}
		return len(value), value, false, nil
	}
	start := position
	for position < len(value) && isBearerByte(value[position]) {
		position++
	}
	if position == len(value) && !final {
		return 0, "", true, nil
	}
	if position-start < 8 {
		return position, value[:position], false, nil
	}
	return position, redactedValue, false, nil
}

func resolveDelimitedCandidate(value string, allowed func(byte) bool, final bool) (int, string, bool, error) {
	position := 0
	for position < len(value) && value[position] < utf8.RuneSelf && allowed(value[position]) {
		position++
	}
	if position == len(value) && !final {
		return 0, "", true, nil
	}
	if position == 0 {
		return 1, value[:1], false, nil
	}
	redacted, _ := redactSensitiveValues(value[:position])
	return position, redacted, false, nil
}

func resolveURL(value string, final bool) (int, string, bool, error) {
	position := 0
	for position < len(value) {
		current, width := utf8.DecodeRuneInString(value[position:])
		if unicode.IsSpace(current) {
			break
		}
		position += width
	}
	if position == len(value) && !final {
		return 0, "", true, nil
	}
	redacted, _ := redactSensitiveValues(value[:position])
	return position, redacted, false, nil
}

func sensitiveValueEnd(value string, start int) int {
	position := start
	for position < len(value) {
		current, width := utf8.DecodeRuneInString(value[position:])
		if unicode.IsSpace(current) || current == ',' || current == ';' {
			break
		}
		position += width
	}
	return position
}

func longestPotentialSensitiveSuffix(value string) int {
	longest := 0
	consider := func(start int, literal string, boundary bool) {
		if start < 0 || start >= len(value) || boundary && !asciiWordBoundaryBefore(value, start) {
			return
		}
		tail := value[start:]
		if len(tail) < len(literal) && strings.EqualFold(tail, literal[:len(tail)]) && len(tail) > longest {
			longest = len(tail)
		}
	}
	for start := 0; start < len(value); start++ {
		for _, candidate := range streamingLiteralTriggers {
			consider(start, candidate.value, candidate.boundary)
		}
		for _, key := range streamingAssignmentKeys {
			consider(start, key, true)
		}
		if asciiWordBoundaryBefore(value, start) && potentialBasicURLPrefix(value[start:]) && len(value)-start > longest {
			longest = len(value) - start
		}
	}
	return longest
}

func basicURLTriggerLength(value string) int {
	for colon := 2; colon <= 21 && colon+3 <= len(value); colon++ {
		if value[colon:colon+3] != "://" || !validSchemePrefix(value[:colon], true) {
			continue
		}
		return colon + 3
	}
	return 0
}

func potentialBasicURLPrefix(value string) bool {
	if value == "" {
		return false
	}
	colon := strings.IndexByte(value, ':')
	if colon < 0 {
		return len(value) <= 21 && validSchemePrefix(value, false)
	}
	if colon < 2 || colon > 21 || !validSchemePrefix(value[:colon], true) {
		return false
	}
	rest := value[colon+1:]
	return rest == "" || rest == "/"
}

func validSchemePrefix(value string, complete bool) bool {
	if value == "" || complete && len(value) < 2 || !isASCIIAlpha(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		current := value[index]
		if !isASCIIWordByte(current) && current != '+' && current != '.' && current != '-' {
			return false
		}
	}
	return true
}

func isASCIIAlpha(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func asciiWordBoundaryBefore(value string, index int) bool {
	return index == 0 || !isASCIIWordByte(value[index-1])
}

func isASCIIWordByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '_'
}

func isBearerByte(value byte) bool {
	return isASCIIWordByte(value) || value == '.' || value == '~' || value == '+' ||
		value == '/' || value == '=' || value == '-'
}

func isJWTCandidateByte(value byte) bool {
	return isASCIIWordByte(value) || value == '.' || value == '-'
}

type streamingWhitespace struct {
	started bool
	pending strings.Builder
}

func (whitespace *streamingWhitespace) push(value string) string {
	var result strings.Builder
	for _, current := range value {
		if unicode.IsSpace(current) {
			if current == '\n' {
				pending := whitespace.pending.String()
				if lastNewline := strings.LastIndexByte(pending, '\n'); lastNewline >= 0 {
					pending = pending[:lastNewline+1]
				} else {
					pending = ""
				}
				whitespace.pending.Reset()
				whitespace.pending.WriteString(pending)
			}
			whitespace.pending.WriteRune(current)
			continue
		}
		if whitespace.started {
			result.WriteString(whitespace.pending.String())
		}
		whitespace.pending.Reset()
		result.WriteRune(current)
		whitespace.started = true
	}
	return result.String()
}

func (whitespace *streamingWhitespace) finish() {
	whitespace.pending.Reset()
}
