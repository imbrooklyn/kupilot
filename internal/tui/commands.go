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

func applicationQuery(query application.UICompletionQuery) tea.Cmd {
	return func() tea.Msg {
		return ApplicationQueryMsg{Query: query}
	}
}

func applicationResume(request application.UIResumeRequest) tea.Cmd {
	return func() tea.Msg {
		return ApplicationResumeMsg{Request: request}
	}
}
