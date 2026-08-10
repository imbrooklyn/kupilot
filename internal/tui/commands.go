package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
)

func applicationCommand(command application.UICommand) tea.Cmd {
	return func() tea.Msg {
		return ApplicationCommandMsg{Command: command}
	}
}
