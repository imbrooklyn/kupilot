package einoadapter

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/imbrooklyn/kupilot/internal/agent"
	"github.com/imbrooklyn/kupilot/internal/config"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/security"
)

const maxProvisionalAnswerEvents = 2 * 1024

var errProvisionalAnswerStopped = errors.New("provisional answer projection stopped")

// provisionalAnswer projects only the first top-level answer_markdown string
// from a model response. It has no role in Eino response assembly, Tool-call
// interpretation, Diagnosis validation, persistence, or runtime authority.
type provisionalAnswer struct {
	ctx        context.Context
	stop       context.CancelFunc
	state      *runState
	extractor  answerMarkdownExtractor
	credential credentialStreamGuard
	redactor   *security.StreamingRedactor

	disabled bool
	finished bool
	failure  error
}

func newProvisionalAnswer(
	ctx context.Context,
	stop context.CancelFunc,
	state *runState,
	credential *config.SecretValue,
) (*provisionalAnswer, error) {
	redactor, err := security.NewStreamingRedactor(agent.MaxAnswerMarkdownBytes)
	if ctx == nil || stop == nil || state == nil || credential == nil || !credential.IsSet() || err != nil {
		return nil, failedRuntime(domain.SafeErrorClassInternal, safeInternalFailure, err)
	}
	return &provisionalAnswer{
		ctx: ctx, stop: stop, state: state, credential: credentialStreamGuard{credential: credential}, redactor: redactor,
	}, nil
}

func (preview *provisionalAnswer) accept(fragment string) error {
	if preview == nil || preview.finished || preview.failure != nil {
		return errProvisionalAnswerStopped
	}
	if preview.disabled {
		return nil
	}
	decoded := preview.extractor.push(fragment)
	if preview.extractor.disabled {
		preview.disabled = true
		return nil
	}
	return preview.project(decoded, false)
}

func (preview *provisionalAnswer) finish() error {
	if preview == nil || preview.finished || preview.failure != nil {
		return errProvisionalAnswerStopped
	}
	preview.finished = true
	if preview.disabled || !preview.extractor.complete() {
		return nil
	}
	if err := preview.project("", true); err != nil {
		return err
	}
	return nil
}

func (preview *provisionalAnswer) project(decoded string, final bool) error {
	admitted, credentialFound := preview.credential.push(decoded, final)
	if credentialFound {
		return preview.fail(failedRuntime(
			domain.SafeErrorClassSensitiveOutputBlocked,
			safeSensitiveModelTextBlocked,
			agent.ErrSensitiveModelTextBlocked,
		))
	}
	var (
		projected string
		err       error
	)
	if admitted != "" {
		projected, err = preview.redactor.Push(admitted)
	}
	if err == nil && final {
		var tail string
		tail, err = preview.redactor.Finish()
		projected += tail
	}
	if errors.Is(err, security.ErrSensitiveOutputBlocked) {
		return preview.fail(failedRuntime(
			domain.SafeErrorClassSensitiveOutputBlocked,
			safeSensitiveModelTextBlocked,
			agent.ErrSensitiveModelTextBlocked,
		))
	}
	if err != nil {
		return preview.fail(failedRuntime(
			domain.SafeErrorClassBudgetExhausted,
			"The model response stream exceeded a fixed limit.",
			err,
		))
	}
	if projected == "" {
		return nil
	}
	if err := preview.state.checkScope(preview.ctx); err != nil {
		return preview.fail(err)
	}
	if !preview.state.reserveProvisionalEvent() {
		return nil
	}
	if err := preview.state.publish(preview.ctx, agent.RunEvent{
		Kind: agent.RunEventTextDelta, TextDelta: projected,
	}); err != nil {
		return preview.fail(err)
	}
	return nil
}

func (preview *provisionalAnswer) fail(err error) error {
	if preview.failure == nil {
		preview.failure = err
		preview.stop()
	}
	return errProvisionalAnswerStopped
}

type credentialStreamGuard struct {
	credential *config.SecretValue
	pending    string
}

func (guard *credentialStreamGuard) push(value string, final bool) (string, bool) {
	if guard == nil || guard.credential == nil {
		return "", true
	}
	candidate := guard.pending + value
	guard.pending = ""
	admitted := ""
	found := false
	if err := guard.credential.Use(func(secret string) {
		if secret == "" || strings.Contains(candidate, secret) {
			found = true
			return
		}
		keep := 0
		if !final {
			maximum := min(len(candidate), len(secret)-1)
			for length := maximum; length > 0; length-- {
				if strings.HasSuffix(candidate, secret[:length]) {
					keep = length
					break
				}
			}
		}
		admitted = candidate[:len(candidate)-keep]
		guard.pending = candidate[len(candidate)-keep:]
	}); err != nil {
		return "", true
	}
	if found {
		guard.pending = ""
		return "", true
	}
	return admitted, false
}

type answerExtractorState uint8

const (
	answerExtractorStart answerExtractorState = iota
	answerExtractorObject
	answerExtractorKey
	answerExtractorColon
	answerExtractorValue
	answerExtractorString
	answerExtractorEscape
	answerExtractorUnicode
	answerExtractorSurrogateSlash
	answerExtractorSurrogateU
	answerExtractorDone
)

const answerMarkdownKey = `"` + agent.DiagnosticResponseAnswerField + `"`

type answerMarkdownExtractor struct {
	state        answerExtractorState
	keyOffset    int
	unicodeValue rune
	unicodeBytes int
	high         rune
	low          bool
	disabled     bool
}

func (extractor *answerMarkdownExtractor) push(fragment string) string {
	if extractor == nil || extractor.disabled || extractor.state == answerExtractorDone || !utf8.ValidString(fragment) {
		if extractor != nil && !utf8.ValidString(fragment) {
			extractor.disabled = true
		}
		return ""
	}
	var result strings.Builder
	for index := 0; index < len(fragment) && !extractor.disabled && extractor.state != answerExtractorDone; {
		current := fragment[index]
		switch extractor.state {
		case answerExtractorStart:
			if jsonWhitespace(current) {
				index++
				continue
			}
			if current != '{' {
				extractor.disabled = true
				continue
			}
			extractor.state = answerExtractorObject
			index++
		case answerExtractorObject:
			if jsonWhitespace(current) {
				index++
				continue
			}
			extractor.state = answerExtractorKey
		case answerExtractorKey:
			if extractor.keyOffset >= len(answerMarkdownKey) || current != answerMarkdownKey[extractor.keyOffset] {
				extractor.disabled = true
				continue
			}
			extractor.keyOffset++
			index++
			if extractor.keyOffset == len(answerMarkdownKey) {
				extractor.state = answerExtractorColon
			}
		case answerExtractorColon:
			if jsonWhitespace(current) {
				index++
				continue
			}
			if current != ':' {
				extractor.disabled = true
				continue
			}
			extractor.state = answerExtractorValue
			index++
		case answerExtractorValue:
			if jsonWhitespace(current) {
				index++
				continue
			}
			if current != '"' {
				extractor.disabled = true
				continue
			}
			extractor.state = answerExtractorString
			index++
		case answerExtractorString:
			switch {
			case current == '"':
				extractor.state = answerExtractorDone
				index++
			case current == '\\':
				extractor.state = answerExtractorEscape
				index++
			case current < 0x20:
				extractor.disabled = true
			case current < utf8.RuneSelf:
				result.WriteByte(current)
				index++
			default:
				_, width := utf8.DecodeRuneInString(fragment[index:])
				result.WriteString(fragment[index : index+width])
				index += width
			}
		case answerExtractorEscape:
			if current == 'u' {
				extractor.state = answerExtractorUnicode
				extractor.unicodeValue = 0
				extractor.unicodeBytes = 0
				extractor.low = false
				index++
				continue
			}
			decoded, ok := simpleJSONEscape(current)
			if !ok {
				extractor.disabled = true
				continue
			}
			result.WriteRune(decoded)
			extractor.state = answerExtractorString
			index++
		case answerExtractorUnicode:
			value, ok := jsonHex(current)
			if !ok {
				extractor.disabled = true
				continue
			}
			extractor.unicodeValue = extractor.unicodeValue*16 + rune(value)
			extractor.unicodeBytes++
			index++
			if extractor.unicodeBytes == 4 {
				extractor.completeUnicode(&result)
			}
		case answerExtractorSurrogateSlash:
			if current != '\\' {
				result.WriteRune(utf8.RuneError)
				extractor.high = 0
				extractor.state = answerExtractorString
				continue
			}
			extractor.state = answerExtractorSurrogateU
			index++
		case answerExtractorSurrogateU:
			if current != 'u' {
				extractor.disabled = true
				continue
			}
			extractor.state = answerExtractorUnicode
			extractor.unicodeValue = 0
			extractor.unicodeBytes = 0
			extractor.low = true
			index++
		}
	}
	return result.String()
}

func (extractor *answerMarkdownExtractor) completeUnicode(result *strings.Builder) {
	current := extractor.unicodeValue
	if extractor.low {
		if current >= 0xdc00 && current <= 0xdfff {
			result.WriteRune(0x10000 + (extractor.high-0xd800)*0x400 + current - 0xdc00)
		} else {
			result.WriteRune(utf8.RuneError)
			if current < 0xd800 || current > 0xdfff {
				result.WriteRune(current)
			}
		}
		extractor.high = 0
		extractor.low = false
		extractor.state = answerExtractorString
		return
	}
	if current >= 0xd800 && current <= 0xdbff {
		extractor.high = current
		extractor.state = answerExtractorSurrogateSlash
		return
	}
	if current >= 0xdc00 && current <= 0xdfff {
		result.WriteRune(utf8.RuneError)
	} else {
		result.WriteRune(current)
	}
	extractor.state = answerExtractorString
}

func (extractor *answerMarkdownExtractor) complete() bool {
	return extractor != nil && !extractor.disabled && extractor.state == answerExtractorDone
}

func jsonWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}

func simpleJSONEscape(value byte) (rune, bool) {
	switch value {
	case '"', '\\', '/':
		return rune(value), true
	case 'b':
		return '\b', true
	case 'f':
		return '\f', true
	case 'n':
		return '\n', true
	case 'r':
		return '\r', true
	case 't':
		return '\t', true
	default:
		return 0, false
	}
}

func jsonHex(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}
