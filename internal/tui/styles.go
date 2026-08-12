package tui

import (
	"image/color"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/imbrooklyn/kupilot/internal/tui/components"
)

// ThemeMode selects one local semantic palette without enabling theme plugins.
type ThemeMode uint8

const (
	ThemeAuto ThemeMode = iota
	ThemeDark
	ThemeLight
	ThemeANSI16
	ThemeNoColor
)

// SemanticPalette gives every component the same meaning-based color tokens.
type SemanticPalette struct {
	BaseText      color.Color
	MutedText     color.Color
	Surface       color.Color
	SurfaceBorder color.Color
	Accent        color.Color
	Success       color.Color
	Warning       color.Color
	Danger        color.Color
	ColorEnabled  bool
}

// SemanticPaletteFor returns one fixed dark, light, ANSI16, or no-color palette.
func SemanticPaletteFor(mode ThemeMode, darkBackground bool) SemanticPalette {
	if mode == ThemeAuto {
		if darkBackground {
			mode = ThemeDark
		} else {
			mode = ThemeLight
		}
	}
	switch mode {
	case ThemeDark:
		return SemanticPalette{
			BaseText: lipgloss.Color("#d6d9e0"), MutedText: lipgloss.Color("#7f8799"),
			Surface: lipgloss.Color("#20242e"), SurfaceBorder: lipgloss.Color("#3a4152"),
			Accent: lipgloss.Color("#7aa2f7"), Success: lipgloss.Color("#9ece6a"),
			Warning: lipgloss.Color("#e0af68"), Danger: lipgloss.Color("#f7768e"), ColorEnabled: true,
		}
	case ThemeLight:
		return SemanticPalette{
			BaseText: lipgloss.Color("#20242e"), MutedText: lipgloss.Color("#667085"),
			Surface: lipgloss.Color("#f1f3f7"), SurfaceBorder: lipgloss.Color("#c7ceda"),
			Accent: lipgloss.Color("#315da8"), Success: lipgloss.Color("#2f7d32"),
			Warning: lipgloss.Color("#9a6700"), Danger: lipgloss.Color("#b42318"), ColorEnabled: true,
		}
	case ThemeANSI16:
		return SemanticPalette{
			BaseText: lipgloss.White, MutedText: lipgloss.BrightBlack,
			Surface: lipgloss.Black, SurfaceBorder: lipgloss.BrightBlack,
			Accent: lipgloss.BrightBlue, Success: lipgloss.Green,
			Warning: lipgloss.Yellow, Danger: lipgloss.Red, ColorEnabled: true,
		}
	default:
		noColor := lipgloss.NoColor{}
		return SemanticPalette{
			BaseText: noColor, MutedText: noColor, Surface: noColor, SurfaceBorder: noColor,
			Accent: noColor, Success: noColor, Warning: noColor, Danger: noColor,
		}
	}
}

type styleSet struct {
	palette       SemanticPalette
	composer      components.ComposerStyles
	transcript    components.TranscriptStyles
	toolSteps     components.ToolStepStyles
	slashMenu     components.SlashMenuStyles
	picker        components.PickerStyles
	dialog        components.DialogStyles
	approval      components.ApprovalDialogStyles
	scopeConflict components.ScopeConflictStyles
	footer        components.FooterStyles
}

func newStyleSet(mode ThemeMode, darkBackground bool) styleSet {
	palette := SemanticPaletteFor(mode, darkBackground)
	base := lipgloss.NewStyle().Foreground(palette.BaseText)
	muted := lipgloss.NewStyle().Foreground(palette.MutedText)
	surface := base.Background(palette.Surface)
	focusedSurface := surface.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(palette.Accent).
		Padding(0, 1)
	blurredSurface := surface.
		Border(lipgloss.RoundedBorder()).
		BorderForeground(palette.SurfaceBorder).
		Padding(0, 1)

	textareaFocused := textarea.StyleState{
		Base:             surface,
		CursorLine:       surface,
		CursorLineNumber: muted,
		EndOfBuffer:      muted,
		LineNumber:       muted,
		Placeholder:      muted.Background(palette.Surface),
		Prompt:           muted,
		Text:             surface,
	}
	textareaBlurred := textareaFocused
	textareaStyles := textarea.Styles{
		Focused: textareaFocused,
		Blurred: textareaBlurred,
		Cursor: textarea.CursorStyle{
			Color: palette.Accent,
			Shape: tea.CursorBar,
			Blink: true,
		},
	}

	return styleSet{
		palette: palette,
		composer: components.ComposerStyles{
			FocusedSurface: focusedSurface,
			BlurredSurface: blurredSurface,
			Textarea:       textareaStyles,
		},
		transcript: components.TranscriptStyles{
			UserSurface: blurredSurface,
			AgentText:   base,
			NoticeText:  muted,
			Placeholder: muted,
		},
		toolSteps: components.ToolStepStyles{
			Muted: muted, Success: lipgloss.NewStyle().Foreground(palette.Success),
			Warning: lipgloss.NewStyle().Foreground(palette.Warning),
			Danger:  lipgloss.NewStyle().Foreground(palette.Danger),
		},
		slashMenu: components.SlashMenuStyles{
			Normal: base, Selected: lipgloss.NewStyle().Foreground(palette.Accent).Bold(true),
			Muted: muted, Disabled: muted,
		},
		dialog: components.DialogStyles{
			Frame: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Danger).Padding(1, 2),
			Title: lipgloss.NewStyle().Foreground(palette.Danger).Bold(true),
			Body:  base,
			Hint:  muted,
		},
		approval: components.ApprovalDialogStyles{
			Frame: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Warning).Padding(1, 2),
			Title: lipgloss.NewStyle().Foreground(palette.Warning).Bold(true),
			Body:  base, Selected: lipgloss.NewStyle().Foreground(palette.Accent).Bold(true),
			Muted: muted, Danger: lipgloss.NewStyle().Foreground(palette.Danger),
		},
		picker: components.PickerStyles{
			Normal: base, Selected: lipgloss.NewStyle().Foreground(palette.Accent).Bold(true),
			Muted: muted, Danger: lipgloss.NewStyle().Foreground(palette.Danger),
		},
		scopeConflict: components.ScopeConflictStyles{
			Frame: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Warning).Padding(1, 2),
			Title: lipgloss.NewStyle().Foreground(palette.Warning).Bold(true),
			Body:  base, Selected: lipgloss.NewStyle().Foreground(palette.Accent).Bold(true), Muted: muted,
		},
		footer: components.FooterStyles{
			Primary: base, Secondary: muted, Warning: lipgloss.NewStyle().Foreground(palette.Warning),
		},
	}
}
