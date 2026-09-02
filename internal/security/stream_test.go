package security

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestStreamingRedactorMatchesCompleteMultilineProcessing(t *testing.T) {
	t.Parallel()

	unicodeText := string([]rune{0x4f60, 0x597d, 0x4e16, 0x754c})
	input := "# Result\r\n\r\n" + unicodeText + " and ordinary text.  \n\n  - Ready  \n\n"
	fragments := []string{
		"# R", "esult\r", "\n\r\n", unicodeText[:6], unicodeText[6:],
		" and ordinary", " text.  \n", "\n  - Ready  \n", "\n",
	}
	got, err := collectStreamingProjection(fragments, 4096)
	if err != nil {
		t.Fatalf("streaming projection error = %v", err)
	}
	want, err := NewRedactor().ProcessLines(input, 4096)
	if err != nil {
		t.Fatalf("ProcessLines() error = %v", err)
	}
	if got != want.Value {
		t.Fatalf("streaming projection mismatch\nwant: %q\n got: %q", want.Value, got)
	}
}

func TestStreamingRedactorHoldsSplitSensitiveValuesUntilRedacted(t *testing.T) {
	t.Parallel()

	canaries := []string{
		strings.Repeat("b", 16),
		strings.Repeat("p", 16),
		strings.Repeat("u", 16),
		strings.Repeat("j", 16),
		strings.Repeat("A", 16),
	}
	inputs := []string{
		"Bearer " + canaries[0] + " accepted",
		"token = " + canaries[1] + " accepted",
		strings.Join([]string{"https", "://", "user", ":", canaries[2], "@generated.invalid/path accepted"}, ""),
		"eyJ" + canaries[3] + "." + strings.Repeat("m", 16) + "." + strings.Repeat("s", 16) + " accepted",
		"AKIA" + canaries[4] + " accepted",
	}
	for index, input := range inputs {
		input := input
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			fragments := splitEveryByte(input)
			got, err := collectStreamingProjection(fragments, 4096)
			if err != nil {
				t.Fatalf("streaming projection error = %v", err)
			}
			for _, canary := range canaries {
				if strings.Contains(got, canary) {
					t.Fatalf("streaming projection retained sensitive canary: %q", got)
				}
			}
			if !strings.Contains(got, redactedValue) {
				t.Fatalf("streaming projection did not include redaction marker: %q", got)
			}
		})
	}
}

func TestStreamingRedactorBlocksSplitPrivateKeyBeforeCandidateEmission(t *testing.T) {
	t.Parallel()

	input := "safe prefix " + strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ") +
		"\n" + strings.Repeat("private-key-canary", 4)
	redactor, err := NewStreamingRedactor(4096)
	if err != nil {
		t.Fatalf("NewStreamingRedactor() error = %v", err)
	}
	var emitted strings.Builder
	for _, fragment := range splitEveryByte(input) {
		value, pushErr := redactor.Push(fragment)
		if pushErr != nil {
			if !errors.Is(pushErr, ErrSensitiveOutputBlocked) {
				t.Fatalf("Push() error = %v", pushErr)
			}
			if strings.Contains(emitted.String(), "PRIVATE") || strings.Contains(emitted.String(), "KEY") {
				t.Fatalf("private-key candidate was emitted before blocking: %q", emitted.String())
			}
			return
		}
		emitted.WriteString(value)
	}
	t.Fatal("private-key stream was not blocked")
}

func TestStreamingRedactorRemovesTerminalControlsAcrossFragments(t *testing.T) {
	t.Parallel()

	canary := strings.Repeat("clipboard-canary", 3)
	fragments := []string{"before\x1b", "]52;c;", canary[:17], canary[17:], "\x07after\u202e"}
	got, err := collectStreamingProjection(fragments, 4096)
	if err != nil {
		t.Fatalf("streaming projection error = %v", err)
	}
	if strings.Contains(got, canary) || strings.ContainsRune(got, '\x1b') || strings.ContainsRune(got, '\u202e') ||
		got != "before after" {
		t.Fatalf("unsafe streaming projection = %q", got)
	}
}

func TestStreamingRedactorRejectsLimitAndPostFinishInput(t *testing.T) {
	t.Parallel()

	redactor, err := NewStreamingRedactor(4)
	if err != nil {
		t.Fatalf("NewStreamingRedactor() error = %v", err)
	}
	if _, err := redactor.Push("five!"); !errors.Is(err, errStreamingTextLimit) {
		t.Fatalf("oversized Push() error = %v", err)
	}
	redactor, _ = NewStreamingRedactor(4)
	if _, err := redactor.Push("safe"); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if _, err := redactor.Finish(); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if _, err := redactor.Push("x"); !errors.Is(err, errStreamingTextLimit) {
		t.Fatalf("post-finish Push() error = %v", err)
	}
}

func TestStreamingRedactorFailsClosedOnUnboundedSensitiveCandidate(t *testing.T) {
	t.Parallel()

	redactor, err := NewStreamingRedactor(2 * maxStreamingSensitiveCandidateBytes)
	if err != nil {
		t.Fatalf("NewStreamingRedactor() error = %v", err)
	}
	if _, err := redactor.Push("safe prefix "); err != nil {
		t.Fatalf("safe prefix Push() error = %v", err)
	}
	value, err := redactor.Push("token=" + strings.Repeat("x", maxStreamingSensitiveCandidateBytes+1))
	if !errors.Is(err, ErrSensitiveOutputBlocked) || value != "" {
		t.Fatalf("unbounded sensitive candidate value/error = %q / %v", value, err)
	}
}

func FuzzStreamingRedactorSafety(f *testing.F) {
	privateKey := strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ") + "\nsynthetic"
	unicodeText := string([]rune{0x4f60, 0x597d, 0x754c})
	for _, seed := range []string{
		"plain streaming text",
		"token=" + strings.Repeat("s", 32),
		"before\x1b]52;c;synthetic\x07after\u202e",
		"first\r\n" + unicodeText + "\nlast",
		privateKey,
		string([]byte{0xff, 'a', '\n', 0x00}),
	} {
		f.Add([]byte(seed), uint16(1))
	}

	f.Fuzz(func(t *testing.T, raw []byte, encodedSplit uint16) {
		if len(raw) > 8192 {
			raw = raw[:8192]
		}
		input := strings.ToValidUTF8(string(raw), string(utf8.RuneError))
		runes := []rune(input)
		split := int(encodedSplit) % (len(runes) + 1)
		fragments := []string{string(runes[:split]), string(runes[split:])}
		maximum := max(1, len(input)+64)
		redactor, err := NewStreamingRedactor(maximum)
		if err != nil {
			t.Fatalf("NewStreamingRedactor() error = %v", err)
		}
		var projected strings.Builder
		for _, fragment := range fragments {
			value, pushErr := redactor.Push(fragment)
			projected.WriteString(value)
			if pushErr != nil {
				if !errors.Is(pushErr, ErrSensitiveOutputBlocked) && !errors.Is(pushErr, errStreamingTextLimit) {
					t.Fatalf("Push() error = %v", pushErr)
				}
				assertSafeStreamingProjection(t, projected.String(), maximum)
				return
			}
		}
		value, finishErr := redactor.Finish()
		projected.WriteString(value)
		if finishErr != nil && !errors.Is(finishErr, ErrSensitiveOutputBlocked) &&
			!errors.Is(finishErr, errStreamingTextLimit) {
			t.Fatalf("Finish() error = %v", finishErr)
		}
		assertSafeStreamingProjection(t, projected.String(), maximum)
	})
}

func assertSafeStreamingProjection(t testing.TB, value string, maximum int) {
	t.Helper()
	if !utf8.ValidString(value) || len(value) > maximum {
		t.Fatalf("streaming projection is invalid or oversized: %q", value)
	}
	for _, current := range value {
		if current != '\n' && unsafeExternalRune(current) {
			t.Fatalf("streaming projection retained unsafe rune %U in %q", current, value)
		}
	}
	if containsBlockedSensitiveValue(value) {
		t.Fatalf("streaming projection retained blocked material: %q", value)
	}
	if _, count := redactSensitiveValues(value); count != 0 {
		t.Fatalf("streaming projection is not a sensitive-pattern fixed point: %q", value)
	}
}

func collectStreamingProjection(fragments []string, maximum int) (string, error) {
	redactor, err := NewStreamingRedactor(maximum)
	if err != nil {
		return "", err
	}
	var result strings.Builder
	for _, fragment := range fragments {
		value, pushErr := redactor.Push(fragment)
		if pushErr != nil {
			return "", pushErr
		}
		result.WriteString(value)
	}
	value, err := redactor.Finish()
	result.WriteString(value)
	return result.String(), err
}

func splitEveryByte(value string) []string {
	result := make([]string, 0, len(value))
	for index := 0; index < len(value); index++ {
		result = append(result, value[index:index+1])
	}
	return result
}
