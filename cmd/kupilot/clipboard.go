package main

import (
	"context"
	"encoding/base64"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/tui"
)

const clipboardTimeout = 2 * time.Second

// Native helpers are fixed delivery operations, never model-selected commands.
// Linux uses OSC 52 rather than spawning a detached clipboard-owner process.
func nativeClipboardAvailable(goos string, getenv func(string) string) bool {
	return goos == "darwin" && !remoteClipboardSession(getenv)
}

func remoteClipboardSession(getenv func(string) string) bool {
	return getenv != nil && (getenv("SSH_CONNECTION") != "" || getenv("SSH_TTY") != "" || getenv("SSH_CLIENT") != "")
}

func writeNativeClipboard(ctx context.Context, text string) error {
	ctx, cancel := context.WithTimeout(ctx, clipboardTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/pbcopy")
	command.Env = []string{"LANG=en_US.UTF-8", "LC_CTYPE=UTF-8"}
	command.Stdin = strings.NewReader(text)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.WaitDelay = 100 * time.Millisecond
	return command.Run()
}

func clipboardCommand(ctx context.Context, terminal bool, getenv func(string) string) func(uint64, string) tea.Cmd {
	return clipboardCommandForEnvironment(ctx, runtime.GOOS, terminal, getenv, writeNativeClipboard)
}

func clipboardCommandForEnvironment(ctx context.Context, goos string, terminal bool, getenv func(string) string,
	writeNative func(context.Context, string) error,
) func(uint64, string) tea.Cmd {
	native := nativeClipboardAvailable(goos, getenv)
	tmux := getenv != nil && (getenv("TMUX") != "" || getenv("TMUX_PANE") != "")
	screen := getenv != nil && getenv("STY") != ""
	return func(id uint64, text string) tea.Cmd {
		return func() tea.Msg {
			result := tui.ClipboardResultMsg{RequestID: id}
			if ctx.Err() != nil || text == "" || len(text) > tui.MaxClipboardAnswerBytes {
				return result
			}
			if native {
				result.Copied = writeNative(ctx, text) == nil
			}
			if ctx.Err() != nil || !terminal || (result.Copied && !tmux) {
				return result
			}
			// OSC 52 has no delivery acknowledgement. Encode all answer bytes;
			// only these fixed framing bytes may control the terminal.
			sequence := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
			if tmux {
				sequence = "\x1bPtmux;" + strings.ReplaceAll(sequence, "\x1b", "\x1b\x1b") + "\x1b\\"
			} else if screen {
				sequence = "\x1bP" + sequence + "\x1b\\"
			}
			result.Requested = true
			return tea.Sequence(tea.Raw(sequence), func() tea.Msg { return result })()
		}
	}
}
