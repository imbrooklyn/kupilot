package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/tui"
)

func clipboardMessages(command tea.Cmd) []tea.Msg {
	message := command()
	// tea.Sequence intentionally keeps its message type private. Enumerate its
	// command slice without depending on the private type's name or fields.
	value := reflect.ValueOf(message)
	if value.Kind() != reflect.Slice {
		return []tea.Msg{message}
	}
	var messages []tea.Msg
	for index := 0; index < value.Len(); index++ {
		messages = append(messages, clipboardMessages(value.Index(index).Interface().(tea.Cmd))...)
	}
	return messages
}

func TestClipboardRoutesNativeAndTerminalWithoutBrandAllowlist(t *testing.T) {
	for _, test := range []struct {
		name, goos            string
		env                   map[string]string
		nativeFails, terminal bool
		calls                 int
		copied, requested     bool
		prefix                string
	}{
		{"apple terminal", "darwin", map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, false, true, 1, true, false, ""},
		{"native failed", "darwin", nil, true, true, 1, false, true, "\x1b]52;c;"},
		{"unknown linux terminal", "linux", nil, false, true, 0, false, true, "\x1b]52;c;"},
		{"ssh", "darwin", map[string]string{"SSH_TTY": "present"}, false, true, 0, false, true, "\x1b]52;c;"},
		{"tmux local and attached", "darwin", map[string]string{"TMUX": "present"}, false, true, 1, true, true, "\x1bPtmux;\x1b\x1b]52;c;"},
		{"screen", "linux", map[string]string{"STY": "present"}, false, true, 0, false, true, "\x1bP\x1b]52;c;"},
		{"no terminal", "linux", nil, false, false, 0, false, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			getenv := func(key string) string { return test.env[key] }
			copy := clipboardCommandForEnvironment(t.Context(), test.goos, test.terminal, getenv, func(ctx context.Context, text string) error {
				calls++
				if text != "safe\nanswer" {
					t.Fatal("clipboard text changed")
				}
				if test.nativeFails {
					return errors.New("private helper diagnostic")
				}
				return nil
			})
			messages := clipboardMessages(copy(42, "safe\nanswer"))
			result := messages[len(messages)-1].(tui.ClipboardResultMsg)
			if calls != test.calls || result.RequestID != 42 || result.Copied != test.copied || result.Requested != test.requested {
				t.Fatalf("calls=%d result=%#v", calls, result)
			}
			if test.requested {
				raw := messages[0].(tea.RawMsg).Msg.(string)
				if !strings.HasPrefix(raw, test.prefix) || !strings.Contains(raw, base64.StdEncoding.EncodeToString([]byte("safe\nanswer"))) {
					t.Fatalf("bad terminal framing: %q", raw)
				}
			}
			if strings.Contains(fmt.Sprint(messages), "private helper diagnostic") {
				t.Fatal("helper output disclosed")
			}
		})
	}
}

func TestClipboardCancellationAndLimitsPerformZeroWrites(t *testing.T) {
	for _, test := range []struct {
		text      string
		cancelled bool
	}{{"", false}, {strings.Repeat("x", tui.MaxClipboardAnswerBytes+1), false}, {"safe", true}} {
		ctx, cancel := context.WithCancel(t.Context())
		if test.cancelled {
			cancel()
		}
		calls := 0
		copy := clipboardCommandForEnvironment(ctx, "darwin", true, func(string) string { return "" }, func(context.Context, string) error { calls++; return nil })
		messages := clipboardMessages(copy(1, test.text))
		cancel()
		if calls != 0 || len(messages) != 1 || messages[0].(tui.ClipboardResultMsg).Requested {
			t.Fatal("denied copy performed I/O")
		}
	}
	copy := clipboardCommandForEnvironment(t.Context(), "linux", true, nil, nil)
	if len(clipboardMessages(copy(1, strings.Repeat("x", tui.MaxClipboardAnswerBytes)))) != 2 {
		t.Fatal("exact byte limit denied")
	}
}

func TestClipboardTimeoutFallsBackButCancellationDoesNot(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	copy := clipboardCommandForEnvironment(ctx, "darwin", true, nil, func(context.Context, string) error { return context.DeadlineExceeded })
	messages := clipboardMessages(copy(1, "safe"))
	result := messages[len(messages)-1].(tui.ClipboardResultMsg)
	if result.Copied || !result.Requested {
		t.Fatal("native timeout did not use unconfirmed terminal delivery")
	}
	copy = clipboardCommandForEnvironment(ctx, "darwin", true, nil, func(context.Context, string) error { cancel(); return context.Canceled })
	messages = clipboardMessages(copy(2, "safe"))
	if len(messages) != 1 || messages[0].(tui.ClipboardResultMsg).Requested {
		t.Fatal("cancellation emitted terminal output")
	}
}

func TestNativeClipboardHonorsAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := writeNativeClipboard(ctx, "unused synthetic text"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled native write: %v", err)
	}
}
