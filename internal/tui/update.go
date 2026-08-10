package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

const helpText = `/help                 Show commands and key bindings
/context [filter]     Select the Kubernetes Context
/namespace [filter]   Select the Kubernetes Namespace
/resource [filter]    Select or clear the target resource
/status               Show the current safe status
/new                  Start a new Session
/resume [filter]      Resume a local Session
/rename [title]       Rename the current Session
/privacy              Show model data-sharing information
/cancel               Cancel the active AgentRun
/quit                 Exit KuPilot

Enter sends. Ctrl+J inserts a newline. Tab completes a command.`

// Update reduces one message into pure UI state and deferred typed commands.
func (model Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch message := msg.(type) {
	case tea.WindowSizeMsg:
		model.width = max(1, message.Width)
		model.height = max(1, message.Height)
		model.syncSlashMenu()
		model.reflow()
		return model, nil
	case ApplicationEventMsg:
		model.acceptApplicationEvent(message.Event)
		model.reflow()
		return model, nil
	case tea.PasteMsg:
		if model.dialog.Open() {
			return model, nil
		}
		return model.updatePaste(message)
	case tea.KeyPressMsg:
		return model.updateKey(message)
	default:
		if model.dialog.Open() {
			return model, nil
		}
		updated, cmd, err := model.composer.Update(msg)
		if err == nil {
			model.composer = updated
		}
		return model, cmd
	}
}

func (model Model) updatePaste(message tea.PasteMsg) (tea.Model, tea.Cmd) {
	message.Content = sanitizeExternalText(message.Content, 0)
	updated, cmd, err := model.composer.Update(message)
	if err != nil {
		model.showDialog("Input limit", "The draft is too long. The maximum is 65536 bytes.")
		return model, nil
	}
	model.composer = updated
	model.syncSlashMenu()
	model.reflow()
	return model, cmd
}

func (model Model) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	message.Text = sanitizeExternalText(message.Text, 0)
	if model.dialog.Open() {
		if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) {
			model.closeDialog()
		}
		return model, nil
	}

	if key.Matches(message, model.keymap.Quit) {
		if model.run.Active {
			model.showDialog("Run active", "Cancel the active AgentRun before exiting.")
			return model, nil
		}
		return model, quitCommand()
	}
	if key.Matches(message, model.keymap.Close) {
		if model.slashMenu.Open() {
			model.slashMenu.Close()
			model.reflow()
		}
		return model, nil
	}
	if model.slashMenu.Open() {
		switch {
		case key.Matches(message, model.keymap.Reverse), key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.PreviousAlt):
			model.slashMenu.Move(-1)
			return model, nil
		case key.Matches(message, model.keymap.Next), key.Matches(message, model.keymap.NextAlt):
			model.slashMenu.Move(1)
			return model, nil
		case key.Matches(message, model.keymap.Complete):
			model.completeSlashSelection()
			model.reflow()
			return model, nil
		}
	}
	if key.Matches(message, model.keymap.Complete) {
		return model, nil
	}
	if key.Matches(message, model.keymap.Submit) {
		return model.submitDraft()
	}
	if key.Matches(message, model.keymap.TranscriptUp) {
		model.transcript.PageUp()
		return model, nil
	}
	if key.Matches(message, model.keymap.TranscriptDown) {
		model.transcript.PageDown()
		return model, nil
	}
	if key.Matches(message, model.keymap.Previous) && model.composer.HistoryEligible() {
		model.composer.PreviousHistory()
		model.syncSlashMenu()
		model.reflow()
		return model, nil
	}
	if key.Matches(message, model.keymap.Next) && model.composer.NextHistory() {
		model.syncSlashMenu()
		model.reflow()
		return model, nil
	}

	updated, cmd, err := model.composer.Update(message)
	if err != nil {
		model.showDialog("Input limit", "The draft is too long. The maximum is 65536 bytes.")
		return model, nil
	}
	model.composer = updated
	model.syncSlashMenu()
	model.reflow()
	return model, cmd
}

func (model Model) submitDraft() (tea.Model, tea.Cmd) {
	draft := model.composer.Value()
	historyDraft := draft
	if strings.TrimSpace(draft) == "" {
		model.showDialog("Nothing to send", "Enter a diagnostic question or a fixed Slash command.")
		return model, nil
	}
	if strings.HasPrefix(draft, "!") {
		model.showDialog("Command unavailable", "Shell and bang commands are not supported.")
		return model, nil
	}

	parsed := ParseSlashDraft(draft)
	switch parsed.Mode {
	case DraftUnknownSlash:
		if model.slashMenu.Open() {
			if _, ok := model.slashMenu.SelectedCandidate(); ok {
				model.completeSlashSelection()
				model.reflow()
				return model, nil
			}
		}
		model.showDialog("Unknown command", "Unknown Slash command. Type /help for available commands.")
		return model, nil
	case DraftSlash:
		if parsed.Command == nil {
			model.completeSlashSelection()
			model.reflow()
			return model, nil
		}
		return model.executeSlash(*parsed.Command, parsed.Argument)
	case DraftEscapedChat:
		draft = strings.TrimPrefix(draft, "/")
	}

	if model.run.Active {
		model.showDialog("Run active", "Cancel the active AgentRun before sending another question.")
		return model, nil
	}
	command := application.UICommand{
		Kind:                    application.UICommandSubmitQuestion,
		Text:                    draft,
		ExpectedScopeGeneration: model.scope.Generation,
	}
	if command.Validate() != nil {
		model.showDialog("Cannot send", "The question could not be submitted safely.")
		return model, nil
	}
	model.transcript.AppendUser(draft)
	model.composer.RecordSubmission(historyDraft)
	model.composer.Reset()
	model.slashMenu.Close()
	model.reflow()
	return model, applicationCommand(command)
}

func (model Model) executeSlash(command SlashCommand, argument string) (tea.Model, tea.Cmd) {
	if argument != "" && !command.AcceptsArgument {
		model.showDialog("Invalid command", "This Slash command does not accept an argument.")
		return model, nil
	}
	if enabled, reason := model.slashAvailability(command); !enabled {
		model.showDialog("Command unavailable", reason)
		return model, nil
	}

	switch command.action {
	case slashHelp:
		model.composer.Reset()
		model.slashMenu.Close()
		model.showDialog("Help", helpText)
		return model, nil
	case slashStatus:
		model.transcript.AppendNotice(model.safeStatus())
		model.composer.Reset()
		model.slashMenu.Close()
		model.reflow()
		return model, nil
	case slashQuit:
		model.composer.Reset()
		model.slashMenu.Close()
		return model, quitCommand()
	case slashApplication:
		intent := application.UICommand{
			Kind:                    command.commandKind,
			Text:                    argument,
			ExpectedScopeGeneration: model.scope.Generation,
		}
		if command.commandKind == application.UICommandCancelRun {
			intent.RunID = model.run.RunID
			intent.Text = ""
			intent.ExpectedScopeGeneration = model.run.ScopeGeneration
		}
		if intent.Validate() != nil {
			model.showDialog("Cannot continue", "The command could not be dispatched safely.")
			return model, nil
		}
		model.composer.Reset()
		model.slashMenu.Close()
		model.reflow()
		return model, applicationCommand(intent)
	default:
		model.showDialog("Command unavailable", "The command is not available.")
		return model, nil
	}
}

func (model Model) slashAvailability(command SlashCommand) (bool, string) {
	switch command.Name {
	case "cancel":
		if !model.run.Active {
			return false, "There is no active AgentRun to cancel."
		}
	case "new", "resume", "quit":
		if model.run.Active {
			return false, "Cancel the active AgentRun first."
		}
	}
	return true, ""
}

func (model *Model) syncSlashMenu() {
	draft := model.composer.Value()
	parsed := ParseSlashDraft(draft)
	if parsed.Mode != DraftSlash && parsed.Mode != DraftUnknownSlash || strings.ContainsRune(draft, '\n') || strings.Contains(draft, " ") {
		model.slashMenu.Close()
		return
	}
	query := strings.TrimPrefix(draft, "/")
	commands := FilterSlashCommands(query)
	candidates := make([]components.SlashCandidate, 0, len(commands))
	for _, command := range commands {
		enabled, reason := model.slashAvailability(command)
		candidates = append(candidates, components.SlashCandidate{
			Name: command.Name, Usage: command.Usage, Summary: command.Summary,
			Enabled: enabled, DisabledReason: reason,
		})
	}
	model.slashMenu.SetCandidates(candidates)
}

func (model *Model) completeSlashSelection() {
	candidate, ok := model.slashMenu.SelectedCandidate()
	if !ok {
		return
	}
	command, ok := findSlashCommand(candidate.Name)
	if !ok {
		return
	}
	value := "/" + command.Name
	if command.AcceptsArgument {
		value += " "
	}
	model.composer.SetValue(value)
	model.syncSlashMenu()
}

func (model *Model) showDialog(title, body string) {
	model.dialog.Show(title, body)
	model.composer.Blur()
	model.focus = FocusModal
}

func (model *Model) closeDialog() {
	model.dialog.Close()
	_ = model.composer.Focus()
	model.focus = FocusComposer
}

func (model Model) safeStatus() string {
	contextName := model.scope.Context
	if contextName == "" {
		contextName = "unavailable"
	}
	namespace := model.scope.Namespace
	if namespace == "" {
		namespace = "unavailable"
	}
	access := "scope unverified"
	if model.scope.ReadOnly {
		access = "read-only"
	}
	return fmt.Sprintf("Context: %s · Namespace: %s · %s", contextName, namespace, access)
}

func quitCommand() tea.Cmd {
	return func() tea.Msg { return tea.Quit() }
}

func (model *Model) acceptApplicationEvent(event application.UIEvent) {
	if event.Validate() != nil || event.ScopeGeneration != model.scope.Generation {
		return
	}
	if event.Kind == application.UIEventRunStarted {
		if event.Sequence != 1 || model.run.Active {
			return
		}
		model.run = RunView{
			RunID: event.RunID, ScopeGeneration: event.ScopeGeneration,
			LastSequence: event.Sequence, Active: true,
		}
		model.transcript.StartAgent()
		return
	}
	if !model.run.Active || model.run.Terminal || event.RunID != model.run.RunID ||
		event.ScopeGeneration != model.run.ScopeGeneration || event.Sequence <= model.run.LastSequence {
		return
	}

	switch event.Kind {
	case application.UIEventTextDelta:
		remaining := application.MaxQuestionBytes - len(model.run.StreamedText)
		if remaining < 0 {
			remaining = 0
		}
		text := ""
		if remaining > 0 {
			text = sanitizeExternalText(event.Text, remaining)
		}
		model.run.StreamedText += text
		model.transcript.AppendAgent(text)
	case application.UIEventToolStep:
		step := event.ToolStep
		model.transcript.UpsertToolStep(components.ToolStep{
			InvocationID:  string(step.InvocationID),
			Name:          string(step.Name),
			Purpose:       sanitizeExternalText(step.Purpose, 1024),
			Status:        string(step.Status),
			Summary:       sanitizeExternalText(step.Summary, 4096),
			EvidenceCount: step.EvidenceCount,
			Truncated:     step.Truncated,
		})
	case application.UIEventRunCompleted, application.UIEventRunFailed, application.UIEventRunCancelled:
		text := sanitizeExternalText(event.Text, application.MaxQuestionBytes)
		if text == "" {
			switch event.Kind {
			case application.UIEventRunCompleted:
				text = "The AgentRun completed without displayable text."
			case application.UIEventRunCancelled:
				text = "The AgentRun was cancelled."
			default:
				text = "The AgentRun failed safely."
			}
		}
		model.run.StreamedText = text
		model.run.Active = false
		model.run.Terminal = true
		model.transcript.FinishAgent(text)
	default:
		return
	}
	model.run.LastSequence = event.Sequence
}
