package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/application"
	"github.com/imbrooklyn/kupilot/internal/domain"
)

func TestFooterUsesTwoRowPriorityForResourceRunModelAndPrivacy(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 120, Height: 24, Theme: ThemeNoColor,
		Scope:     ScopeView{Context: "development", Namespace: "payments", Generation: 7, ReadOnly: true},
		Resource:  ResourceView{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: "payment-api"},
		ModelName: "diagnostic-model", PrivacyMode: domain.PrivacyModeMinimal,
	})
	model.run.Active = true
	footer := model.footerView()
	for _, want := range []string{
		"Context development", "Namespace payments", "read-only", "Deployment/payment-api",
		"diagnosing", "model diagnostic-model", "memory-only history",
	} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer missing %q: %q", want, footer)
		}
	}
	if lines := strings.Split(footer, "\n"); len(lines) != 2 {
		t.Fatalf("footer rows = %d, want 2: %q", len(lines), footer)
	}
}

func TestFooterUsesReadableLabelsAndDropsWholeLowPrioritySegments(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 72, Height: 22, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "development", Namespace: "payments", Generation: 7, ReadOnly: true},
		Resource: ResourceView{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: "payments", Name: "payment-api",
		},
		ModelName: "a-model-name-that-does-not-fit", PrivacyMode: domain.PrivacyModeStandard,
	})
	model.run.Status = "completed"
	footer := model.footerView()
	for _, want := range []string{"history saved", "Deployment/payment-api", "diagnosis complete"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("footer missing %q: %q", want, footer)
		}
	}
	for _, forbidden := range []string{"ctx/", "ns/", "res/", "run/", "model/", "privacy/", "…"} {
		if strings.Contains(footer, forbidden) {
			t.Fatalf("footer contains implementation label or clipped fragment %q: %q", forbidden, footer)
		}
	}
}

func TestFooterNarrowWidthsRetainScopeAndReadOnlyBeforeOptionalState(t *testing.T) {
	t.Parallel()

	for _, width := range []int{40, 24, 16} {
		width := width
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			t.Parallel()
			model := NewModel(Config{
				Width: width, Height: 12, Theme: ThemeNoColor,
				Scope: ScopeView{
					Context: "a-very-long-development-context", Namespace: "a-very-long-namespace",
					Generation: 7, ReadOnly: true,
				},
				Resource:  ResourceView{APIVersion: "v1", Kind: "Pod", Namespace: "a-very-long-namespace", Name: "a-very-long-resource-name"},
				ModelName: "a-very-long-model-name", PrivacyMode: domain.PrivacyModeStandard,
			})
			footer := model.footerView()
			if !strings.Contains(footer, "read-only") ||
				width >= 24 && (!strings.Contains(footer, "Context") || !strings.Contains(footer, "Namespace")) ||
				width < 24 && !strings.Contains(footer, " / ") {
				t.Fatalf("required footer state was cropped at width %d: %q", width, footer)
			}
			lines := strings.Split(footer, "\n")
			if len(lines) > 2 {
				t.Fatalf("footer rows = %d at width %d", len(lines), width)
			}
			for _, line := range lines {
				if lipgloss.Width(line) > width {
					t.Fatalf("footer width = %d, limit %d: %q", lipgloss.Width(line), width, line)
				}
			}
		})
	}
}

func TestFooterAndPickerExposeNoSensitiveFieldSurface(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{
		Width: 80, Height: 24, Theme: ThemeNoColor,
		Scope: ScopeView{Context: "development", Namespace: "payments", Generation: 7, ReadOnly: true},
	})
	model.openCompletion("context", "", resumeOriginNone)
	view := model.render()
	for _, forbidden := range []string{"server URL", "username", "token", "API key", "raw Tool output"} {
		if strings.Contains(view, forbidden) {
			t.Fatalf("view exposed prohibited field label %q", forbidden)
		}
	}
}

func TestPickerContractsHaveNoCredentialOrRawOutputFields(t *testing.T) {
	t.Parallel()

	contracts := []any{
		application.UIContextCandidate{},
		application.UINamespaceCandidate{},
		application.UIResourceCandidate{},
		application.UISessionCandidate{},
		ScopeView{}, ResourceView{},
	}
	for _, contract := range contracts {
		typeOf := reflect.TypeOf(contract)
		for index := 0; index < typeOf.NumField(); index++ {
			name := strings.ToLower(typeOf.Field(index).Name)
			for _, forbidden := range []string{"server", "user", "token", "key", "credential", "raw", "output", "body"} {
				if strings.Contains(name, forbidden) {
					t.Fatalf("%s exposes prohibited field %q", typeOf.Name(), typeOf.Field(index).Name)
				}
			}
		}
	}
}
