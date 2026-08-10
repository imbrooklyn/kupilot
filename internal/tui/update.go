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
		model.reflow()
		return model, nil
	case ApplicationEventMsg:
		model.acceptApplicationEvent(message.Event)
		model.reflow()
		if model.quitAfterCancel && !model.run.Active && model.run.Terminal {
			model.quitAfterCancel = false
			return model, quitCommand()
		}
		return model, nil
	case CompletionResultMsg:
		model.acceptCompletionResult(message.Result)
		model.reflow()
		return model, nil
	case ResumeResultMsg:
		model.acceptResumeResult(message.Result)
		model.reflow()
		return model, nil
	case ScopeResultMsg:
		model.acceptScopeResult(message.Result)
		model.reflow()
		return model, nil
	case ResourceSelectionResultMsg:
		model.acceptResourceSelectionResult(message.Result)
		model.reflow()
		return model, nil
	case tea.BlurMsg:
		model.terminalFocused = false
		model.composer.Blur()
		return model, nil
	case tea.FocusMsg:
		model.terminalFocused = true
		if !model.dialog.Open() && !model.scopeConflict.Open() {
			_ = model.composer.Focus()
			model.focus = FocusComposer
		}
		return model, nil
	case tea.PasteMsg:
		if model.dialog.Open() || model.scopeConflict.Open() || !model.terminalFocused {
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
	queryCmd := model.syncSuggestionsAfterEdit()
	model.reflow()
	return model, combineCommands(cmd, queryCmd)
}

func (model Model) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	message.Text = sanitizeExternalText(message.Text, 0)
	if !model.terminalFocused {
		return model, nil
	}
	if key.Matches(message, model.keymap.Quit) {
		if model.run.Active {
			model.quitAfterCancel = true
			return model, model.cancelRunCommand()
		}
		return model, quitCommand()
	}
	if model.scopeConflict.Open() {
		return model.updateScopeConflictKey(message)
	}
	if model.dialog.Open() {
		if key.Matches(message, model.keymap.Close) || key.Matches(message, model.keymap.Submit) {
			model.closeDialog()
		}
		return model, nil
	}

	if key.Matches(message, model.keymap.Close) {
		if model.pickerOpen() {
			return model.cancelPicker()
		}
		if model.slashMenu.Open() {
			model.slashMenu.Close()
			model.reflow()
		}
		return model, nil
	}
	if key.Matches(message, model.keymap.Cancel) {
		if model.run.Active {
			return model, model.cancelRunCommand()
		}
		return model, nil
	}
	if model.pickerOpen() {
		switch {
		case key.Matches(message, model.keymap.Reverse), key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.PreviousAlt):
			model.movePicker(-1)
			return model, nil
		case key.Matches(message, model.keymap.Next), key.Matches(message, model.keymap.NextAlt):
			model.movePicker(1)
			return model, nil
		case key.Matches(message, model.keymap.Complete):
			model.completePickerSelection()
			model.reflow()
			return model, nil
		case key.Matches(message, model.keymap.Submit):
			cmd := model.selectPickerCandidate()
			model.reflow()
			return model, cmd
		}
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
			queryCmd := model.syncSuggestionsAfterEdit()
			model.reflow()
			return model, queryCmd
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
		queryCmd := model.syncSuggestionsAfterEdit()
		model.reflow()
		return model, queryCmd
	}
	if key.Matches(message, model.keymap.Next) && model.composer.NextHistory() {
		queryCmd := model.syncSuggestionsAfterEdit()
		model.reflow()
		return model, queryCmd
	}

	updated, cmd, err := model.composer.Update(message)
	if err != nil {
		model.showDialog("Input limit", "The draft is too long. The maximum is 65536 bytes.")
		return model, nil
	}
	model.composer = updated
	queryCmd := model.syncSuggestionsAfterEdit()
	model.reflow()
	return model, combineCommands(cmd, queryCmd)
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
				queryCmd := model.syncSuggestionsAfterEdit()
				model.reflow()
				return model, queryCmd
			}
		}
		model.showDialog("Unknown command", "Unknown Slash command. Type /help for available commands.")
		return model, nil
	case DraftSlash:
		if parsed.Command == nil {
			model.completeSlashSelection()
			queryCmd := model.syncSuggestionsAfterEdit()
			model.reflow()
			return model, queryCmd
		}
		return model.executeSlash(*parsed.Command, parsed.Argument)
	case DraftEscapedChat:
		draft = strings.TrimPrefix(draft, "/")
	}

	if model.run.Active {
		model.showDialog("Run active", "Cancel the active AgentRun before sending another question.")
		return model, nil
	}
	if model.scope.Switching {
		model.showDialog("Scope switching", "Wait for scope activation to finish before sending a question.")
		return model, nil
	}
	if model.scope.Generation < 1 || !model.scope.ReadOnly {
		model.showDialog("Scope required", "Select and verify one Context and Namespace before sending a question.")
		return model, nil
	}
	if !model.startup.Ready {
		model.showDialog("Session loading", "Finish or cancel the explicit Session resume before sending a question.")
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
	if command.Name == "resource" && argument == "clear" {
		requestID := model.nextUIRequestID()
		intent := application.UICommand{
			Kind: application.UICommandSelectResource, RequestID: requestID,
			Text: "clear", ExpectedScopeGeneration: model.scope.Generation,
		}
		if intent.Validate() != nil {
			model.showDialog("Cannot continue", "The Resource selection could not be cleared safely.")
			return model, nil
		}
		model.pendingResourceID = requestID
		model.pendingResource = ResourceView{}
		model.composer.Reset()
		model.slashMenu.Close()
		return model, applicationCommand(intent)
	}
	if kind := completionKindForCommand(command.Name); kind != "" {
		prefix := "/" + command.Name + " "
		model.composer.SetValue(prefix + argument)
		origin := resumeOriginNone
		if kind == application.UICompletionSession {
			origin = resumeOriginInTUI
		}
		queryCmd := model.openCompletion(kind, argument, origin)
		return model, queryCmd
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
	if model.scope.Switching {
		switch command.Name {
		case "context", "namespace", "resource":
			return false, "Wait for the current scope activation to finish."
		}
	}
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
}

func (model *Model) showDialog(title, body string) {
	model.dialog.Show(title, body)
	model.composer.Blur()
	model.focus = FocusModal
}

func (model *Model) closeDialog() {
	model.dialog.Close()
	if model.terminalFocused {
		_ = model.composer.Focus()
	}
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

func combineCommands(commands ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(commands))
	for _, command := range commands {
		if command != nil {
			filtered = append(filtered, command)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tea.Batch(filtered...)
	}
}

func (model Model) cancelRunCommand() tea.Cmd {
	command := application.UICommand{
		Kind: application.UICommandCancelRun, RunID: model.run.RunID,
		ExpectedScopeGeneration: model.run.ScopeGeneration,
	}
	if command.Validate() != nil {
		return nil
	}
	return applicationCommand(command)
}

func (model Model) cancelPicker() (tea.Model, tea.Cmd) {
	isSession := model.activePicker == application.UICompletionSession
	topLevel := isSession && model.resumeOrigin == resumeOriginTopLevel && !model.startup.Ready
	model.closePickers()
	if isSession {
		model.composer.Reset()
		model.resumeOrigin = resumeOriginNone
		if !topLevel {
			model.startup.Ready = true
		}
	}
	model.reflow()
	if topLevel {
		return model, quitCommand()
	}
	return model, nil
}

func (model Model) updateScopeConflictKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(message, model.keymap.Close):
		topLevel := model.resumeOrigin == resumeOriginTopLevel
		model.scopeConflict.Close()
		model.pendingResumed = nil
		model.composer.Reset()
		model.resumeOrigin = resumeOriginNone
		if model.terminalFocused {
			_ = model.composer.Focus()
		}
		model.focus = FocusComposer
		if topLevel {
			return model, quitCommand()
		}
		model.startup.Ready = true
		return model, nil
	case key.Matches(message, model.keymap.Previous), key.Matches(message, model.keymap.Next),
		key.Matches(message, model.keymap.PreviousAlt), key.Matches(message, model.keymap.NextAlt),
		key.Matches(message, model.keymap.Complete), key.Matches(message, model.keymap.Reverse):
		model.scopeConflict.Move(1)
		return model, nil
	case key.Matches(message, model.keymap.Submit):
		if model.pendingResumed == nil {
			return model, nil
		}
		resumed := *model.pendingResumed
		useSaved := model.scopeConflict.UseSavedScope()
		model.applyResumedSession(resumed)
		if !useSaved || resumed.SavedScope == nil {
			return model, nil
		}
		scope := *resumed.SavedScope
		requestID := model.nextUIRequestID()
		command := application.UICommand{
			Kind: application.UICommandActivateScope, RequestID: requestID,
			ExpectedScopeGeneration: model.scope.Generation, Scope: &scope,
		}
		if command.Validate() != nil {
			model.showDialog("Scope unavailable", "The saved scope could not be activated safely.")
			return model, nil
		}
		model.pendingScopeID = requestID
		model.scope.Switching = true
		model.resource = ResourceView{}
		return model, applicationCommand(command)
	default:
		return model, nil
	}
}

func (model *Model) acceptApplicationEvent(event application.UIEvent) {
	if event.Validate() != nil || model.scope.Switching || event.ScopeGeneration != model.scope.Generation {
		return
	}
	if event.Kind == application.UIEventRunStarted {
		if event.Sequence != 1 || model.run.Active || !model.startup.Ready || !model.scope.ReadOnly {
			return
		}
		model.run = RunView{
			RunID: event.RunID, ScopeGeneration: event.ScopeGeneration,
			LastSequence: event.Sequence, Active: true, Status: "active",
		}
		model.transcript.StartAgent()
		return
	}
	if !model.run.Active || model.run.Terminal || event.RunID != model.run.RunID ||
		event.ScopeGeneration != model.run.ScopeGeneration || event.Sequence != model.run.LastSequence+1 {
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
	case application.UIEventPersistenceDegraded:
		text := sanitizeExternalText(event.Text, application.MaxQuestionBytes)
		if text == "" {
			text = "Local persistence is degraded; this run may not be resumable."
		}
		model.run.PersistenceDegraded = true
		model.transcript.AppendNotice(text)
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
		switch event.Kind {
		case application.UIEventRunCompleted:
			model.run.Status = "completed"
		case application.UIEventRunCancelled:
			model.run.Status = "cancelled"
		default:
			model.run.Status = "failed"
		}
		model.transcript.FinishAgent(text)
	default:
		return
	}
	model.run.LastSequence = event.Sequence
}
