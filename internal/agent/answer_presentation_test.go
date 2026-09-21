package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestAnswerPresentationAcrossFragments(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"Ready.\ue200cite\ue202evidence-1\ue201 Next.", "Ready. Next."},
		{"\ue200cite\ue202a\ue201\ue200cite\ue202b\ue201", ""},
		{"\u4e2d\u6587 **ready**\ue200cite\ue202id\ue201\n`code`", "\u4e2d\u6587 **ready**\n`code`"},
		{"literal \ue200cit", "literal \ue200cit"},
		{"literal \ue200cite\ue202unfinished", "literal \ue200cite\ue202unfinished"},
		{"\ue200other\ue202text\ue201 \ue123", "\ue200other\ue202text\ue201 \ue123"},
		{"\ue200\ue200cite\ue202id\ue201", "\ue200"},
	} {
		runes := []rune(test.input)
		for split := range len(runes) + 1 {
			var filter AnswerPresentation
			got := filter.Push(string(runes[:split])) + filter.Push(string(runes[split:])) + filter.Finish()
			if got != test.want {
				t.Fatalf("split %d input %q: got %q want %q", split, test.input, got, test.want)
			}
		}
		var filter AnswerPresentation
		var got strings.Builder
		for _, char := range runes {
			got.WriteString(filter.Push(string(char)))
		}
		got.WriteString(filter.Finish())
		if got.String() != test.want {
			t.Fatalf("one-rune chunks: got %q want %q", got.String(), test.want)
		}
	}
}

func TestAnswerPresentationDoesNotBypassSafetyOrLimits(t *testing.T) {
	const token = "\ue200cite\ue202id\ue201"
	got, err := processModelMarkdown("Answer"+token, len("Answer"+token))
	if err != nil || got != "Answer" {
		t.Fatalf("exact limit: %q %v", got, err)
	}
	if _, err = processModelMarkdown("Answer"+token, len("Answer"+token)-1); err == nil {
		t.Fatal("removing the token bypassed the original input limit")
	}
	blocked := inlineCitationStart + strings.Join([]string{"-----BEGIN", "PRIVATE", "KEY-----"}, " ") + "\ue201"
	if _, err = processModelMarkdown(blocked, MaxAnswerMarkdownBytes); !errors.Is(err, ErrSensitiveModelTextBlocked) {
		t.Fatalf("token hid sensitive content: %v", err)
	}
	joined := "-----BEGIN PRI" + token + "VATE KEY-----"
	if _, err = processModelMarkdown(joined, MaxAnswerMarkdownBytes); !errors.Is(err, ErrSensitiveModelTextBlocked) {
		t.Fatalf("token removal created unchecked sensitive content: %v", err)
	}
}
