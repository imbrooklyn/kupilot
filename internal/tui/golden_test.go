package tui

import (
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestFixedSizeThemeGoldens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode ThemeMode
	}{
		{name: "dark", mode: ThemeDark},
		{name: "light", mode: ThemeLight},
		{name: "ansi16", mode: ThemeANSI16},
		{name: "no_color", mode: ThemeNoColor},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := goldenSnapshot(goldenModel(t, tt.mode))
			path := filepath.Join("testdata", "golden", tt.name+".golden")
			wantBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden %s: %v\n--- got ---\n%s", path, err, got)
			}
			want := strings.TrimSuffix(string(wantBytes), "\n")
			if got != want {
				t.Fatalf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", tt.name, got, want)
			}
		})
	}
}

func TestGoldenStructureAfterANSIRemoval(t *testing.T) {
	t.Parallel()

	for _, mode := range []ThemeMode{ThemeDark, ThemeLight, ThemeANSI16, ThemeNoColor} {
		model := goldenModel(t, mode)
		raw := model.View().Content
		if mode == ThemeNoColor && hasColorSGR(raw) ||
			mode != ThemeNoColor && !hasColorSGR(raw) {
			t.Fatalf("theme %v ANSI behavior does not match its palette", mode)
		}
		content := sanitizeExternalText(raw, 0)
		questionAt := strings.Index(content, "Why is payment-api unavailable?")
		agentAt := strings.Index(content, "The Deployment has no available replicas.")
		toolAt := strings.Index(content, "Inspect resource · done")
		separatorAt := strings.Index(content, "────────")
		timingAt := strings.Index(content, "Worked for 20s")
		composerAt := strings.Index(content, "/resource pay")
		candidateAt := strings.Index(content, "Deployment/payment-api")
		footerAt := strings.Index(content, "Context development")
		if !(questionAt >= 0 && questionAt < toolAt && toolAt < separatorAt && separatorAt < agentAt &&
			agentAt < timingAt && timingAt < composerAt && composerAt < candidateAt && candidateAt < footerAt) {
			t.Fatalf("theme %v has invalid structural order", mode)
		}
		if strings.Contains(content, "You:") || strings.Contains(content, "Kupilot:") ||
			strings.Contains(content, "Context Picker") || strings.Contains(content, "Search resources") {
			t.Fatalf("theme %v added a role, Picker title, or second prompt", mode)
		}
	}
}

func TestNoColorConfigOverridesColoredTheme(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 40, Height: 12, Theme: ThemeDark, NoColor: true,
		Scope: ScopeView{Context: "ctx", Namespace: "ns", Generation: 1, ReadOnly: true, Verified: true},
	})
	if hasColorSGR(model.View().Content) || model.styles.palette.ColorEnabled {
		t.Fatal("NO_COLOR override left ANSI styling enabled")
	}
}

func TestAutoThemeUsesTerminalDefaultsUntilBackgroundResponse(t *testing.T) {
	model := NewModel(Config{
		Width: 72, Height: 22, Theme: ThemeAuto,
		Scope: ScopeView{Context: "ctx", Namespace: "ns", Generation: 1, ReadOnly: true, Verified: true},
	})
	model.composer.SetValue("preserved draft")
	model.transcript.AppendUser("preserved history")
	if !isDefaultColor(model.styles.palette.Surface) {
		t.Fatalf("unresolved automatic surface = %s, want terminal default", goldenColor(model.styles.palette.Surface, true))
	}
	requestType := fmt.Sprintf("%T", tea.RequestBackgroundColor())
	foundRequest := false
	for _, message := range messagesFromCmd(t, model.Init()) {
		if fmt.Sprintf("%T", message) == requestType {
			foundRequest = true
		}
	}
	if !foundRequest {
		t.Fatal("automatic theme did not request the terminal background color")
	}

	dark, _ := updateModel(t, model, tea.BackgroundColorMsg{Color: color.Black})
	if got := goldenColor(dark.styles.palette.Surface, true); got != "#1e1e1e" {
		t.Fatalf("dark terminal surface = %s", got)
	}
	if dark.composer.Value() != "preserved draft" || !strings.Contains(dark.View().Content, "preserved history") {
		t.Fatal("terminal palette update replaced conversation or composer state")
	}

	light, _ := updateModel(t, model, tea.BackgroundColorMsg{Color: color.White})
	if got := goldenColor(light.styles.palette.Surface, true); got != "#f4f4f4" {
		t.Fatalf("light terminal surface = %s", got)
	}

	terminalBackground := color.RGBA{R: 40, G: 44, B: 52, A: 255}
	customDark, _ := updateModel(t, model, tea.BackgroundColorMsg{Color: terminalBackground})
	if got := goldenColor(customDark.styles.palette.Surface, true); got != "#41454c" {
		t.Fatalf("custom dark terminal surface = %s", got)
	}
	surfaceRed, surfaceGreen, surfaceBlue, _ := customDark.styles.palette.Surface.RGBA()
	backgroundRed, backgroundGreen, backgroundBlue, _ := terminalBackground.RGBA()
	if surfaceRed <= backgroundRed || surfaceGreen <= backgroundGreen || surfaceBlue <= backgroundBlue {
		t.Fatal("dark user surface is not lighter than the terminal background")
	}
}

func TestFixedRGBThemesAlsoAdaptTheirSurfaceToTheTerminalBackground(t *testing.T) {
	t.Parallel()

	requestType := fmt.Sprintf("%T", tea.RequestBackgroundColor())
	for _, mode := range []ThemeMode{ThemeDark, ThemeLight} {
		model := NewModel(Config{Theme: mode})
		foundRequest := false
		for _, message := range messagesFromCmd(t, model.Init()) {
			if fmt.Sprintf("%T", message) == requestType {
				foundRequest = true
			}
		}
		if !foundRequest {
			t.Fatalf("fixed RGB theme %v did not request the terminal background", mode)
		}
	}

	model := NewModel(Config{Theme: ThemeDark})
	adapted, _ := updateModel(t, model, tea.BackgroundColorMsg{
		Color: color.RGBA{R: 40, G: 44, B: 52, A: 255},
	})
	if got := goldenColor(adapted.styles.palette.Surface, true); got != "#41454c" {
		t.Fatalf("fixed dark theme did not adapt to terminal background: %s", got)
	}
}

func goldenModel(t *testing.T, mode ThemeMode) Model {
	t.Helper()
	now := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	model := NewModel(Config{
		Width: 72, Height: 22, Theme: mode, DarkBackground: mode != ThemeLight,
		Scope: ScopeView{Context: "development", Namespace: "payments", Generation: 7, ReadOnly: true, Verified: true},
		Resource: ResourceView{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: "payment-api",
		},
		ModelName: "diagnostic-model", PrivacyMode: domain.PrivacyModeStandard,
		Now: func() time.Time {
			return now
		},
	})
	model.transcript.AppendUser("Why is payment-api unavailable?")
	model.acceptApplicationEvent(runStartedEvent(1))
	model.acceptApplicationEvent(application.UIEvent{
		Kind: application.UIEventTextDelta, RunID: testRunID,
		ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 2, Text: "Inspecting the current workload.",
	})
	model.acceptApplicationEvent(application.UIEvent{
		Kind: application.UIEventToolStep, RunID: testRunID, ScopeGeneration: 7, PolicyGeneration: 1, Sequence: 3,
		ToolStep: &application.ToolStep{
			InvocationID: testInvocationID, Name: domain.ToolNameGetResource,
			Purpose: "Inspect the selected Deployment.", Status: application.ToolStepSucceeded,
			Summary: "No available replicas.", EvidenceCount: 2,
		},
	})
	now = now.Add(20 * time.Second)
	model.acceptApplicationEvent(runTerminalEvent(
		application.UIEventRunCompleted, 4, "The Deployment has no available replicas.",
		application.UIEvidenceReference{
			EvidenceID: testEvidenceID, RunID: testRunID,
			Scope:    domain.ScopeSnapshot{Context: "development", Namespace: "payments", Generation: 7},
			Sequence: 4, State: application.UIEvidenceDetailAvailable,
		},
	))
	model.composer.SetValue("/resource pay")
	model.openCompletion(application.UICompletionResource, "pay", resumeOriginNone)
	query := model.pendingCompletion
	model.acceptCompletionResult(application.UICompletionResult{
		RequestID: query.RequestID, Kind: query.Kind, ScopeGeneration: query.ScopeGeneration,
		Resources: []application.UIResourceCandidate{
			{APIVersion: "apps/v1", Kind: domain.ResourceKindDeployment, Namespace: "payments", Name: "payment-api", Status: "Unavailable"},
			{APIVersion: "v1", Kind: domain.ResourceKindPod, Namespace: "payments", Name: "payment-api-7d9", Status: "Pending"},
		},
	})
	model.reflow()
	return model
}

func goldenSnapshot(model Model) string {
	content := sanitizeExternalText(model.View().Content, 0)
	lines := strings.Split(content, "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " ")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	palette := model.styles.palette
	header := fmt.Sprintf(
		"palette enabled=%t base=%s muted=%s surface=%s border=%s accent=%s success=%s warning=%s danger=%s",
		palette.ColorEnabled,
		goldenColor(palette.BaseText, palette.ColorEnabled), goldenColor(palette.MutedText, palette.ColorEnabled),
		goldenColor(palette.Surface, palette.ColorEnabled), goldenColor(palette.SurfaceBorder, palette.ColorEnabled),
		goldenColor(palette.Accent, palette.ColorEnabled), goldenColor(palette.Success, palette.ColorEnabled),
		goldenColor(palette.Warning, palette.ColorEnabled), goldenColor(palette.Danger, palette.ColorEnabled),
	)
	return header + "\n" + strings.Join(compressGoldenBlankRows(lines), "\n")
}

func compressGoldenBlankRows(lines []string) []string {
	result := make([]string, 0, len(lines))
	for index := 0; index < len(lines); {
		if lines[index] != "" {
			result = append(result, lines[index])
			index++
			continue
		}
		start := index
		for index < len(lines) && lines[index] == "" {
			index++
		}
		result = append(result, fmt.Sprintf("<blank rows=%d>", index-start))
	}
	return result
}

func goldenColor(value color.Color, enabled bool) string {
	if !enabled {
		return "none"
	}
	if isDefaultColor(value) {
		return "default"
	}
	red, green, blue, _ := value.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", red>>8, green>>8, blue>>8)
}

func hasColorSGR(value string) bool {
	for start := 0; start < len(value); {
		relative := strings.Index(value[start:], "\x1b[")
		if relative < 0 {
			return false
		}
		start += relative + 2
		end := strings.IndexByte(value[start:], 'm')
		if end < 0 {
			return false
		}
		for _, field := range strings.Split(value[start:start+end], ";") {
			code, err := strconv.Atoi(field)
			if err == nil && (code == 38 || code == 48 || code >= 30 && code <= 37 ||
				code >= 40 && code <= 47 || code >= 90 && code <= 97 || code >= 100 && code <= 107) {
				return true
			}
		}
		start += end + 1
	}
	return false
}
