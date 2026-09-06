package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestPermissionsSlashOpensOneComposerPickerWithEveryFixedProfile(t *testing.T) {
	t.Parallel()

	model := newTestModel()
	model, _ = updateModel(t, model, tea.PasteMsg{Content: "/permissions"})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	request := applicationCommandFromCmd(t, command)
	if request.Kind != application.UICommandShowPermissions || request.RequestID == 0 {
		t.Fatalf("/permissions command = %#v", request)
	}
	model, _ = updateModel(t, model, CommandResultMsg{Result: permissionOutcome(
		application.UICommandShowPermissions, request.RequestID, domain.PermissionProfileAsk, 1, false,
	)})
	if !model.permissionPicker.Open() || model.FocusedEditorCount() != 1 {
		t.Fatalf("permission picker open/editors = %v/%d", model.permissionPicker.Open(), model.FocusedEditorCount())
	}
	view := model.render()
	for _, profile := range []domain.PermissionProfile{
		domain.PermissionProfileReadOnly,
		domain.PermissionProfileAsk,
		domain.PermissionProfileAutoReview,
		domain.PermissionProfileFullAccess,
		domain.PermissionProfileCustom,
	} {
		if !strings.Contains(view, string(profile)) {
			t.Fatalf("permission picker missing %q:\n%s", profile, view)
		}
	}
	for _, label := range []string{"B = boundary", "V = Reviewer", "R = risk", "B:", " V:", " R:"} {
		if !strings.Contains(view, label) {
			t.Fatalf("permission picker missing textual boundary %q:\n%s", label, view)
		}
	}
}

func TestPermissionPickerEmitsTypedProfileChangesAndFullAccessIsExplicit(t *testing.T) {
	profiles := []domain.PermissionProfile{
		domain.PermissionProfileReadOnly,
		domain.PermissionProfileAsk,
		domain.PermissionProfileAutoReview,
		domain.PermissionProfileFullAccess,
		domain.PermissionProfileCustom,
	}
	for _, profile := range profiles {
		profile := profile
		t.Run(string(profile), func(t *testing.T) {
			current := domain.PermissionProfileAsk
			if profile == current {
				current = domain.PermissionProfileReadOnly
			}
			model := permissionPickerTestModel(current)
			model, _ = updateModel(t, model, tea.PasteMsg{Content: string(profile)})
			model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
			if profile == domain.PermissionProfileFullAccess {
				if command != nil || model.permissionConfirmation == nil || !model.dialog.Open() {
					t.Fatal("full-access selection did not stop at explicit high-risk confirmation")
				}
				confirmation := compactRenderedText(model.render())
				for _, impact := range []string{"cluster mutations", "Pod diagnostics or Exec", "local processes or shell", "without another prompt"} {
					if !strings.Contains(confirmation, impact) {
						t.Fatalf("full-access confirmation omitted %q:\n%s", impact, confirmation)
					}
				}
				model, command = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
				if command != nil || model.permissionConfirmation != nil || model.dialog.Open() {
					t.Fatal("safe default did not keep the prior profile")
				}
				model = permissionPickerTestModel(current)
				model, _ = updateModel(t, model, tea.PasteMsg{Content: string(profile)})
				model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
				model, command = updateModel(t, model, keyText("y"))
			}
			change := applicationCommandFromCmd(t, command)
			if change.Kind != application.UICommandChangePermission || change.PermissionProfile != profile ||
				change.ExpectedPolicyGeneration != 1 ||
				change.HighRiskAcknowledged != (profile == domain.PermissionProfileFullAccess) {
				t.Fatalf("permission change = %#v", change)
			}
		})
	}
}

func TestPermissionPickerCancellationPrecedesRunAndGlobalExit(t *testing.T) {
	t.Parallel()

	for _, message := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		model := permissionPickerTestModel(domain.PermissionProfileAsk)
		model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
		model, command := updateModel(t, model, message)
		if command != nil || model.permissionPicker.Open() || !model.run.Active || model.quitAfterCancel {
			t.Fatalf("picker cancellation leaked to run/exit: open=%v active=%v quit=%v command=%v",
				model.permissionPicker.Open(), model.run.Active, model.quitAfterCancel, command != nil)
		}
	}

	for _, message := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Code: 'c', Mod: tea.ModCtrl},
	} {
		model := permissionPickerTestModel(domain.PermissionProfileAsk)
		model, _ = updateModel(t, model, tea.PasteMsg{Content: string(domain.PermissionProfileFullAccess)})
		model, _ = updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
		model, command := updateModel(t, model, message)
		if command != nil || model.permissionConfirmation != nil || model.dialog.Open() ||
			model.permission.Profile != domain.PermissionProfileAsk {
			t.Fatal("full-access confirmation cancellation changed permission state")
		}
	}
}

func TestPermissionPickerKeepsTextualBoundariesAtNarrowWidths(t *testing.T) {
	t.Parallel()

	for _, width := range []int{24, 40} {
		model := permissionPickerTestModel(domain.PermissionProfileAsk)
		model.width = width
		model.height = 40
		model.reflow()
		view := model.permissionPicker.View()
		if lipgloss.Height(view) != 11 || !strings.Contains(view, "B:") ||
			!strings.Contains(view, " V:") || !strings.Contains(view, " R:") {
			t.Fatalf("narrow permission picker lost textual semantics at width %d:\n%s", width, view)
		}
		for _, line := range strings.Split(view, "\n") {
			if lineWidth := lipgloss.Width(line); lineWidth > model.contentWidth() {
				t.Fatalf("permission row width = %d, limit %d at terminal width %d: %q", lineWidth, model.contentWidth(), width, line)
			}
		}
	}
}

func TestPermissionGenerationChangeTerminatesOldRunAndRejectsLateEvents(t *testing.T) {
	t.Parallel()

	model := permissionPickerTestModel(domain.PermissionProfileAsk)
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: runStartedEvent(1)})
	model, _ = updateModel(t, model, tea.PasteMsg{Content: string(domain.PermissionProfileReadOnly)})
	model, command := updateModel(t, model, tea.KeyPressMsg{Code: tea.KeyEnter})
	change := applicationCommandFromCmd(t, command)
	model, _ = updateModel(t, model, CommandResultMsg{Result: permissionOutcome(
		application.UICommandChangePermission, change.RequestID, domain.PermissionProfileReadOnly, 2, false,
	)})
	if model.run.Active || !model.run.Terminal || model.run.Status != "cancelled" ||
		model.permission.PolicyGeneration != 2 {
		t.Fatalf("permission change state = run %#v permission %#v", model.run, model.permission)
	}
	before := len(model.transcript.Entries())
	model, _ = updateModel(t, model, ApplicationEventMsg{Event: application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID, ScopeGeneration: 7,
		PolicyGeneration: 1, Sequence: 2, Text: "late old-policy text",
	}})
	if len(model.transcript.Entries()) != before || strings.Contains(model.render(), "late old-policy text") {
		t.Fatal("late old-policy event entered the transcript")
	}
}

func permissionPickerTestModel(current domain.PermissionProfile) Model {
	model := newTestModel()
	model.showPermissionPicker(application.UIPermissionsResult{
		RequestID: 1,
		Permission: application.UIPermissionStatus{
			Configured: true, Profile: current, PolicyGeneration: 1, Healthy: true,
			FullAccessAllowed:    current == domain.PermissionProfileFullAccess,
			HighRiskAcknowledged: current == domain.PermissionProfileFullAccess,
		},
	})
	return model
}

func permissionOutcome(
	kind application.UICommandKind,
	requestID uint64,
	profile domain.PermissionProfile,
	generation domain.PolicyGeneration,
	ruleCreated bool,
) application.UICommandOutcome {
	permission := application.UIPermissionStatus{
		Configured: true, Profile: profile, PolicyGeneration: generation, Healthy: true,
		FullAccessAllowed:    profile == domain.PermissionProfileFullAccess,
		HighRiskAcknowledged: profile == domain.PermissionProfileFullAccess,
	}
	return application.UICommandOutcome{
		Command: kind, RequestID: requestID,
		Permissions: &application.UIPermissionsResult{
			RequestID: requestID, Permission: permission,
			Changed: kind == application.UICommandChangePermission, RuleCreated: ruleCreated,
		},
	}
}
