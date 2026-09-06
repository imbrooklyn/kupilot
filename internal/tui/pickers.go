package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

func (model *Model) syncSuggestionsAfterEdit() tea.Cmd {
	if model.permissionPicker.Open() {
		model.refreshPermissionPicker()
		return nil
	}
	if model.sessionExport != nil && model.sessionExport.Stage == sessionExportTargetEntry {
		model.closePickers()
		model.slashMenu.Close()
		return nil
	}
	draft := model.composer.Value()
	if kind, filter, origin, ok := pickerCompletionDraft(draft); ok {
		if kind == application.UICompletionSession && model.resumeOrigin == resumeOriginTopLevel && !model.startup.Ready {
			origin = resumeOriginTopLevel
		}
		if kind == application.UICompletionSession && model.run.Active {
			model.closePickers()
			model.slashMenu.Close()
			return nil
		}
		if model.scope.Switching && (kind == application.UICompletionContext || kind == application.UICompletionNamespace || kind == application.UICompletionResource) {
			model.closePickers()
			model.slashMenu.Close()
			return nil
		}
		return model.openCompletion(kind, filter, origin)
	}
	model.closePickers()
	model.syncSlashMenu()
	return nil
}

func pickerCompletionDraft(draft string) (application.UICompletionKind, string, resumeOrigin, bool) {
	if strings.ContainsRune(draft, '\n') || !strings.HasPrefix(draft, "/") || !strings.Contains(draft, " ") {
		return "", "", resumeOriginNone, false
	}
	parsed := ParseSlashDraft(draft)
	if parsed.Mode != DraftSlash || parsed.Command == nil {
		return "", "", resumeOriginNone, false
	}
	switch parsed.Command.Name {
	case "context":
		return application.UICompletionContext, parsed.Argument, resumeOriginNone, true
	case "namespace":
		return application.UICompletionNamespace, parsed.Argument, resumeOriginNone, true
	case "resource":
		if parsed.Argument == "clear" {
			return "", "", resumeOriginNone, false
		}
		return application.UICompletionResource, parsed.Argument, resumeOriginNone, true
	case "resume":
		return application.UICompletionSession, parsed.Argument, resumeOriginInTUI, true
	default:
		return "", "", resumeOriginNone, false
	}
}

func completionKindForCommand(name string) application.UICompletionKind {
	switch name {
	case "context":
		return application.UICompletionContext
	case "namespace":
		return application.UICompletionNamespace
	case "resource":
		return application.UICompletionResource
	case "resume":
		return application.UICompletionSession
	default:
		return ""
	}
}

func (model *Model) completePickerSelection() {
	switch model.activePicker {
	case application.UICompletionContext:
		if candidate, ok := model.contextPicker.Selected(); ok {
			model.composer.SetValue("/context " + candidate.Name)
		}
	case application.UICompletionNamespace:
		if candidate, ok := model.namespacePicker.Selected(); ok {
			model.composer.SetValue("/namespace " + candidate.Name)
		}
	case application.UICompletionResource:
		if candidate, ok := model.resourcePicker.Selected(); ok {
			model.composer.SetValue("/resource " + candidate.Kind + "/" + candidate.Name)
		}
	case application.UICompletionSession:
		if candidate, ok := model.sessionPicker.Selected(); ok {
			model.composer.SetValue("/resume " + candidate.ID)
		}
	}
}

func (model *Model) selectPickerCandidate() tea.Cmd {
	switch model.activePicker {
	case application.UICompletionContext:
		candidate, ok := model.contextPicker.Selected()
		if !ok {
			return nil
		}
		resumeSelection := model.resumeScopeSelection && model.pendingResumed != nil
		requestID := model.nextUIRequestID()
		if resumeSelection {
			requestID = model.pendingResumed.ResumeRequestID
		}
		command := application.UICommand{
			Kind: application.UICommandSelectContext, RequestID: requestID,
			Text: candidate.Name, ExpectedScopeGeneration: model.scope.Generation,
		}
		if command.Validate() != nil {
			return nil
		}
		model.closeEvidenceInteraction()
		model.pendingScopeID = requestID
		model.scope.Switching = true
		model.composer.Reset()
		model.closePickers()
		model.resumeScopeSelection = resumeSelection
		return applicationCommand(command)
	case application.UICompletionNamespace:
		candidate, ok := model.namespacePicker.Selected()
		if !ok {
			return nil
		}
		resumeSelection := model.resumeScopeSelection && model.pendingResumed != nil
		requestID := model.nextUIRequestID()
		if resumeSelection {
			requestID = model.pendingResumed.ResumeRequestID
		}
		command := application.UICommand{
			Kind: application.UICommandSelectNamespace, RequestID: requestID,
			Text: candidate.Name, ExpectedScopeGeneration: model.scope.Generation,
		}
		if command.Validate() != nil {
			return nil
		}
		model.closeEvidenceInteraction()
		model.pendingScopeID = requestID
		model.scope.Switching = true
		model.composer.Reset()
		model.closePickers()
		model.resumeScopeSelection = resumeSelection
		return applicationCommand(command)
	case application.UICompletionResource:
		candidate, ok := model.resourcePicker.Selected()
		if !ok {
			return nil
		}
		reference := domain.ResourceRef{
			APIVersion: candidate.APIVersion, Kind: candidate.Kind,
			Namespace: candidate.Namespace, Name: candidate.Name,
		}
		requestID := model.nextUIRequestID()
		command := application.UICommand{
			Kind: application.UICommandSelectResource, RequestID: requestID,
			Resource: &reference, ExpectedScopeGeneration: model.scope.Generation,
		}
		if command.Validate() != nil {
			return nil
		}
		model.pendingResourceID = requestID
		model.pendingResource = ResourceView{
			APIVersion: candidate.APIVersion, Kind: candidate.Kind,
			Namespace: candidate.Namespace, Name: candidate.Name,
		}
		model.composer.Reset()
		model.closePickers()
		return applicationCommand(command)
	case application.UICompletionSession:
		candidate, ok := model.sessionPicker.Selected()
		if !ok {
			return nil
		}
		origin := model.resumeOrigin
		if origin == resumeOriginNone {
			origin = resumeOriginInTUI
		}
		model.composer.Reset()
		return model.beginResume(application.UIResumeExact, domain.SessionID(candidate.ID), origin)
	default:
		return nil
	}
}

func (model *Model) beginRequiredScopeSelection() tea.Cmd {
	model.closeDialog()
	model.composer.Reset()
	model.composer.SetValue("/context ")
	model.resumeScopeSelection = false
	return model.openCompletion(application.UICompletionContext, "", resumeOriginNone)
}

func (model *Model) beginResumeScopeSelection(resumed application.UIResumedSession) tea.Cmd {
	model.closeEvidenceInteraction()
	model.closeDialog()
	model.focus = FocusComposer
	if model.terminalFocused {
		_ = model.composer.Focus()
	}
	kind := application.UICompletionContext
	prefix := "/context "
	if resumed.SavedScope != nil && model.scope.Context != "" &&
		resumed.SavedScope.Context == model.scope.Context && resumed.SavedScope.Namespace != model.scope.Namespace {
		kind = application.UICompletionNamespace
		prefix = "/namespace "
	}
	model.composer.Reset()
	model.composer.SetValue(prefix)
	command := model.openCompletion(kind, "", model.resumeOrigin)
	model.resumeScopeSelection = true
	return command
}

func (model *Model) openCompletion(kind application.UICompletionKind, filter string, origin resumeOrigin) tea.Cmd {
	model.closePickers()
	model.slashMenu.Close()
	model.activePicker = kind
	if kind == application.UICompletionSession {
		model.resumeOrigin = origin
	}
	model.setPickerLoading(kind)
	query := application.UICompletionQuery{
		RequestID: model.nextUIRequestID(), Kind: kind,
		Filter: sanitizeExternalText(filter, 512), ScopeGeneration: model.scope.Generation,
		Limit: application.MaxUIQueryCandidates,
	}
	if query.Validate() != nil {
		model.setPickerFailed(kind)
		model.pendingCompletion = application.UICompletionQuery{}
		return nil
	}
	model.pendingCompletion = query
	model.reflow()
	return applicationQuery(query)
}

func (model *Model) beginResume(mode application.UIResumeMode, sessionID domain.SessionID, origin resumeOrigin) tea.Cmd {
	model.closePickers()
	model.slashMenu.Close()
	model.resumeOrigin = origin
	model.startup.Ready = false
	request := application.UIResumeRequest{
		RequestID: model.nextUIRequestID(), Mode: mode, SessionID: sessionID,
	}
	if request.Validate() != nil {
		model.startup.Failed = true
		model.showDialog("Resume unavailable", "The Session could not be requested safely.")
		return nil
	}
	model.pendingResume = request
	return applicationResume(request)
}

func (model *Model) acceptCompletionResult(result application.UICompletionResult) {
	if result.Validate() != nil || model.pendingCompletion.RequestID == 0 ||
		result.RequestID != model.pendingCompletion.RequestID || result.Kind != model.pendingCompletion.Kind ||
		result.ScopeGeneration != model.pendingCompletion.ScopeGeneration || result.ScopeGeneration != model.scope.Generation ||
		result.Kind != model.activePicker {
		return
	}
	model.pendingCompletion = application.UICompletionQuery{}
	if result.Failure != "" {
		model.setPickerFailed(result.Kind)
		return
	}

	switch result.Kind {
	case application.UICompletionContext:
		values := make([]components.ContextCandidate, 0, len(result.Contexts))
		for _, candidate := range result.Contexts {
			values = append(values, components.ContextCandidate{
				Name: sanitizeExternalText(candidate.Name, 253), Current: candidate.Current,
			})
		}
		model.contextPicker.SetCandidates(values)
	case application.UICompletionNamespace:
		values := make([]components.NamespaceCandidate, 0, len(result.Namespaces))
		for _, candidate := range result.Namespaces {
			values = append(values, components.NamespaceCandidate{Name: sanitizeExternalText(candidate.Name, 63)})
		}
		model.namespacePicker.SetCandidates(values)
	case application.UICompletionResource:
		values := make([]components.ResourceCandidate, 0, len(result.Resources))
		for _, candidate := range result.Resources {
			values = append(values, components.ResourceCandidate{
				APIVersion: sanitizeExternalText(candidate.APIVersion, 253),
				Kind:       string(candidate.Kind), Namespace: sanitizeExternalText(candidate.Namespace, 63),
				Name: sanitizeExternalText(candidate.Name, 253), Status: sanitizeExternalText(candidate.Status, 256),
			})
		}
		model.resourcePicker.SetCandidates(values)
	case application.UICompletionSession:
		values := make([]components.SessionCandidate, 0, len(result.Sessions))
		for _, candidate := range result.Sessions {
			values = append(values, components.SessionCandidate{
				ID: string(candidate.ID), Title: sanitizeExternalText(candidate.Title, 512),
				UpdatedAt: time.UnixMilli(candidate.UpdatedAtUnixMillis).UTC().Format("2006-01-02 15:04Z"),
				Context:   sanitizeExternalText(candidate.Context, 253), Namespace: sanitizeExternalText(candidate.Namespace, 63),
				Privacy: string(candidate.PrivacyMode),
			})
		}
		model.sessionPicker.SetCandidates(values)
	}
}

func (model *Model) acceptResourceSelectionResult(result application.UIResourceSelectionResult) {
	if result.Validate() != nil || model.pendingResourceID == 0 || result.RequestID != model.pendingResourceID ||
		result.ScopeGeneration != model.scope.Generation {
		return
	}
	model.pendingResourceID = 0
	if result.Failure != "" {
		model.pendingResource = ResourceView{}
		model.showDialog("Resource unavailable", "The Resource selection could not be accepted safely.")
		return
	}
	if result.Cleared {
		model.resource = ResourceView{}
		model.pendingResource = ResourceView{}
		model.transcript.AppendNotice("The current Resource was cleared.")
		return
	}
	model.resource = sanitizedResource(ResourceView{
		APIVersion: result.Resource.APIVersion, Kind: result.Resource.Kind,
		Namespace: result.Resource.Namespace, Name: result.Resource.Name,
	})
	model.pendingResource = ResourceView{}
	model.transcript.AppendNotice("Resource selected for the next question. Selection does not verify that it currently exists.")
}

func sanitizedResumedSession(value application.UIResumedSession) application.UIResumedSession {
	value.Session.Title = sanitizeExternalText(value.Session.Title, 512)
	value.Session.Context = sanitizeExternalText(value.Session.Context, 253)
	value.Session.Namespace = sanitizeExternalText(value.Session.Namespace, 63)
	if value.SavedScope != nil {
		scope := *value.SavedScope
		scope.Context = sanitizeExternalText(scope.Context, 253)
		scope.Namespace = sanitizeExternalText(scope.Namespace, 63)
		value.SavedScope = &scope
	}
	if value.SavedResource != nil {
		resource := *value.SavedResource
		resource.APIVersion = sanitizeExternalText(resource.APIVersion, 253)
		resource.Namespace = sanitizeExternalText(resource.Namespace, 63)
		resource.Name = sanitizeExternalText(resource.Name, 253)
		resource.Status = sanitizeExternalText(resource.Status, 256)
		value.SavedResource = &resource
	}
	return value
}

func (model *Model) applyResumedSession(resumed application.UIResumedSession) {
	model.session = SessionView{
		ID: resumed.Session.ID, Title: resumed.Session.Title, Resumed: true,
	}
	model.privacyMode = resumed.Session.PrivacyMode
	model.startup.Ready = true
	model.startup.Failed = false
	model.resource = ResourceView{}
	model.pendingResourceID = 0
	model.pendingResource = ResourceView{}
	model.pendingResumed = nil
	model.resumeOrigin = resumeOriginNone
	model.resumeScopeSelection = false
	model.closePickers()
	model.composer.Reset()
	if model.terminalFocused {
		_ = model.composer.Focus()
	}
	model.focus = FocusComposer
	model.transcript.AppendNotice("Session resumed. History does not restore an active diagnostic run or verify the saved scope and resource.")
}

func resumeFailureText(code application.UIQueryFailureCode) string {
	switch code {
	case application.UIQueryTimeout:
		return "The Session query timed out safely."
	case application.UIQueryForbidden:
		return "The Session is not eligible for resume."
	default:
		return "The Session could not be resumed safely."
	}
}

func scopeFailureText(code application.UIQueryFailureCode) string {
	switch code {
	case application.UIQueryTimeout:
		return "The scope activation timed out and the previous scope was not restored implicitly."
	case application.UIQueryForbidden:
		return "The scope could not be verified with current access."
	default:
		return "The scope could not be activated safely."
	}
}

func (model *Model) setPickerLoading(kind application.UICompletionKind) {
	switch kind {
	case application.UICompletionContext:
		model.contextPicker.SetLoading()
	case application.UICompletionNamespace:
		model.namespacePicker.SetLoading()
	case application.UICompletionResource:
		model.resourcePicker.SetLoading()
	case application.UICompletionSession:
		model.sessionPicker.SetLoading()
	}
}

func (model *Model) setPickerFailed(kind application.UICompletionKind) {
	switch kind {
	case application.UICompletionContext:
		model.contextPicker.SetFailed()
	case application.UICompletionNamespace:
		model.namespacePicker.SetFailed()
	case application.UICompletionResource:
		model.resourcePicker.SetFailed()
	case application.UICompletionSession:
		model.sessionPicker.SetFailed()
	}
}

func (model *Model) closePickers() {
	model.contextPicker.Close()
	model.namespacePicker.Close()
	model.resourcePicker.Close()
	model.sessionPicker.Close()
	if model.permissionPicker.Open() {
		model.permissionPicker.Close()
		model.composer.ResetPlaceholder()
		model.composer.Reset()
	}
	model.activePicker = ""
	model.pendingCompletion = application.UICompletionQuery{}
}

func (model Model) pickerOpen() bool {
	if model.permissionPicker.Open() {
		return true
	}
	switch model.activePicker {
	case application.UICompletionContext:
		return model.contextPicker.Open()
	case application.UICompletionNamespace:
		return model.namespacePicker.Open()
	case application.UICompletionResource:
		return model.resourcePicker.Open()
	case application.UICompletionSession:
		return model.sessionPicker.Open()
	default:
		return false
	}
}

func (model Model) pickerView() string {
	if model.permissionPicker.Open() {
		return model.permissionPicker.View()
	}
	switch model.activePicker {
	case application.UICompletionContext:
		return model.contextPicker.View()
	case application.UICompletionNamespace:
		return model.namespacePicker.View()
	case application.UICompletionResource:
		return model.resourcePicker.View()
	case application.UICompletionSession:
		return model.sessionPicker.View()
	default:
		return ""
	}
}

func (model Model) pickerHeight() int {
	if model.permissionPicker.Open() {
		return model.permissionPicker.Height()
	}
	switch model.activePicker {
	case application.UICompletionContext:
		return model.contextPicker.Height()
	case application.UICompletionNamespace:
		return model.namespacePicker.Height()
	case application.UICompletionResource:
		return model.resourcePicker.Height()
	case application.UICompletionSession:
		return model.sessionPicker.Height()
	default:
		return 0
	}
}

func (model *Model) movePicker(delta int) {
	if model.permissionPicker.Open() {
		model.permissionPicker.Move(delta)
		return
	}
	switch model.activePicker {
	case application.UICompletionContext:
		model.contextPicker.Move(delta)
	case application.UICompletionNamespace:
		model.namespacePicker.Move(delta)
	case application.UICompletionResource:
		model.resourcePicker.Move(delta)
	case application.UICompletionSession:
		model.sessionPicker.Move(delta)
	}
}

func (model Model) suggestionsHeight() int {
	if model.pickerOpen() {
		return model.pickerHeight()
	}
	return model.slashMenu.Height()
}
