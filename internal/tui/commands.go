package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
)

// ApplicationConsumer is the minimal delivery-owned command/query surface.
// It contains no persistence, Kubernetes, model, Tool, or framework type.
type ApplicationConsumer interface {
	QueryUI(context.Context, application.UICompletionQuery) (application.UICompletionResult, error)
	ResumeUI(context.Context, application.UIResumeRequest) (application.UIResumeResult, error)
	ExecuteUICommand(context.Context, application.UICommand) (application.UICommandOutcome, error)
}

// DispatchApplication translates one deferred TUI request into one neutral
// result message. The caller supplies the operation Context and owns any
// asynchronous execution.
func DispatchApplication(ctx context.Context, consumer ApplicationConsumer, message tea.Msg) tea.Msg {
	if ctx == nil || consumer == nil {
		return applicationFailure(message, "The requested operation is unavailable.")
	}
	switch request := message.(type) {
	case ApplicationQueryMsg:
		result, err := consumer.QueryUI(ctx, request.Query)
		if err != nil || result.Validate() != nil {
			return applicationFailure(message, "Candidates are unavailable.")
		}
		return CompletionResultMsg{Result: result}
	case ApplicationResumeMsg:
		result, err := consumer.ResumeUI(ctx, request.Request)
		if err != nil || result.Validate() != nil {
			return applicationFailure(message, "The Session could not be resumed safely.")
		}
		return ResumeResultMsg{Result: result}
	case ApplicationCommandMsg:
		result, err := consumer.ExecuteUICommand(ctx, request.Command)
		if err != nil || result.Validate() != nil {
			return applicationFailure(message, "The requested operation could not be completed safely.")
		}
		return CommandResultMsg{Result: result}
	default:
		return applicationFailure(message, "The requested operation is unavailable.")
	}
}

func applicationFailure(message tea.Msg, safeMessage string) ApplicationFailureMsg {
	result := ApplicationFailureMsg{Message: safeMessage}
	switch request := message.(type) {
	case ApplicationQueryMsg:
		result.RequestID = request.Query.RequestID
		result.ScopeGeneration = request.Query.ScopeGeneration
		result.Query = request.Query.Kind
	case ApplicationResumeMsg:
		result.RequestID = request.Request.RequestID
		result.Resume = request.Request.Mode
	case ApplicationCommandMsg:
		result.RequestID = request.Command.RequestID
		result.ScopeGeneration = request.Command.ExpectedScopeGeneration
		result.RunID = request.Command.RunID
		result.Command = request.Command.Kind
	}
	return result
}

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
