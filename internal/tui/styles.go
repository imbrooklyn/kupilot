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
	defaultColor := lipgloss.NoColor{}
	switch mode {
	case ThemeAuto:
		// Until the terminal answers the background-color query, keep both
		// foreground and surface on terminal defaults. ANSI semantic colors are
		// theme-adjustable and cannot create a hard-coded light-on-light surface.
		return SemanticPalette{
			BaseText: defaultColor, MutedText: defaultColor,
			Surface: defaultColor, SurfaceBorder: defaultColor,
			Accent: lipgloss.Cyan, Success: lipgloss.Green,
			Warning: defaultColor, Danger: lipgloss.Red, ColorEnabled: true,
		}
	case ThemeDark:
		return SemanticPalette{
			BaseText: defaultColor, MutedText: defaultColor,
			Surface: lipgloss.Color("#1e1e1e"), SurfaceBorder: defaultColor,
			Accent: lipgloss.Cyan, Success: lipgloss.Green,
			Warning: lipgloss.Yellow, Danger: lipgloss.Red, ColorEnabled: true,
		}
	case ThemeLight:
		return SemanticPalette{
			BaseText: defaultColor, MutedText: defaultColor,
			Surface: lipgloss.Color("#f4f4f4"), SurfaceBorder: defaultColor,
			Accent: lipgloss.Color("#005f87"), Success: lipgloss.Green,
			Warning: defaultColor, Danger: lipgloss.Red, ColorEnabled: true,
		}
	case ThemeANSI16:
		warning := color.Color(defaultColor)
		if darkBackground {
			warning = lipgloss.Yellow
		}
		return SemanticPalette{
			BaseText: defaultColor, MutedText: defaultColor,
			Surface: defaultColor, SurfaceBorder: defaultColor,
			Accent: lipgloss.Cyan, Success: lipgloss.Green,
			Warning: warning, Danger: lipgloss.Red, ColorEnabled: true,
		}
	default:
		return SemanticPalette{
			BaseText: defaultColor, MutedText: defaultColor, Surface: defaultColor, SurfaceBorder: defaultColor,
			Accent: defaultColor, Success: defaultColor, Warning: defaultColor, Danger: defaultColor,
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
	evidence      components.EvidenceDetailStyles
	approval      components.ApprovalDialogStyles
	scopeConflict components.ScopeConflictStyles
	footer        components.FooterStyles
}

func newStyleSet(mode ThemeMode, darkBackground bool) styleSet {
	palette := SemanticPaletteFor(mode, darkBackground)
	return styleSetForPalette(palette)
}

func newStyleSetForBackground(mode ThemeMode, darkBackground bool, background color.Color) styleSet {
	palette := SemanticPaletteFor(mode, darkBackground)
	if background != nil && (mode == ThemeDark || mode == ThemeLight) {
		palette.Surface = blendedUserSurface(background, darkBackground)
	}
	return styleSetForPalette(palette)
}

// blendedUserSurface follows Codex CLI's terminal-relative user-message
// surface: 12% white over a dark background or 4% black over a light one.
func blendedUserSurface(background color.Color, darkBackground bool) color.Color {
	red, green, blue, _ := background.RGBA()
	alpha := uint32(4)
	top := uint32(0)
	if darkBackground {
		alpha = 12
		top = 255
	}
	blend := func(channel uint32) uint8 {
		return uint8((top*alpha + (channel>>8)*(100-alpha)) / 100)
	}
	return color.RGBA{R: blend(red), G: blend(green), B: blend(blue), A: 255}
}

func styleSetForPalette(palette SemanticPalette) styleSet {
	base := foregroundStyle(palette.BaseText)
	muted := foregroundStyle(palette.MutedText)
	accent := foregroundStyle(palette.Accent)
	success := foregroundStyle(palette.Success)
	warning := foregroundStyle(palette.Warning)
	danger := foregroundStyle(palette.Danger)
	if palette.ColorEnabled {
		muted = muted.Faint(true)
		accent = accent.Bold(true)
		success = success.Bold(true)
		warning = warning.Bold(true)
		danger = danger.Bold(true)
	}
	surface := backgroundStyle(base, palette.Surface)
	mutedSurface := backgroundStyle(muted, palette.Surface)
	accentSurface := backgroundStyle(accent, palette.Surface)
	focusedSurface := surface.Padding(1, 1)
	blurredSurface := surface.Padding(1, 1)
	userSurface := surface.Padding(1, 1)

	textareaFocused := textarea.StyleState{
		Base:             surface,
		CursorLine:       surface,
		CursorLineNumber: muted,
		EndOfBuffer:      muted,
		LineNumber:       muted,
		Placeholder:      mutedSurface,
		Prompt:           accentSurface,
		Text:             surface,
	}
	textareaBlurred := textareaFocused
	textareaBlurred.Prompt = mutedSurface
	textareaStyles := textarea.Styles{
		Focused: textareaFocused,
		Blurred: textareaBlurred,
		Cursor: textarea.CursorStyle{
			Color: palette.Accent,
			Shape: tea.CursorBar,
			Blink: true,
		},
	}
	evidenceSelected := lipgloss.NewStyle()
	evidenceTitle := lipgloss.NewStyle()
	if palette.ColorEnabled {
		evidenceSelected = accent
		evidenceTitle = accent
	}

	return styleSet{
		palette: palette,
		composer: components.ComposerStyles{
			FocusedSurface: focusedSurface,
			BlurredSurface: blurredSurface,
			Textarea:       textareaStyles,
		},
		transcript: components.TranscriptStyles{
			UserSurface: userSurface,
			UserPrompt:  accentSurface,
			UserText:    surface,
			AgentText:   base,
			Markdown: components.MarkdownStyles{
				Text:          base,
				Heading:       base.Bold(true),
				Strong:        base.Bold(true),
				Emphasis:      base.Italic(true),
				Strikethrough: base.Strikethrough(true),
				Code:          accent,
				Quote:         muted,
				ListMarker:    accent,
				Link:          accent.Underline(true),
				TableHeader:   base.Bold(true),
				TableBorder:   muted,
			},
			NoticeText:  muted,
			Placeholder: muted,
			Evidence:    muted,
			Selected:    evidenceSelected,
			Separator:   muted,
			Timing:      muted,
		},
		toolSteps: components.ToolStepStyles{
			Normal: base, Muted: muted, Success: success,
			Warning: warning, Danger: danger,
		},
		slashMenu: components.SlashMenuStyles{
			Normal: base, Selected: accent,
			Muted: muted, Disabled: muted,
		},
		dialog: components.DialogStyles{
			Frame: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Danger).Padding(1, 2),
			Title: danger,
			Body:  base,
			Hint:  muted,
		},
		evidence: components.EvidenceDetailStyles{
			Frame: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Accent).Padding(1, 2),
			Title: evidenceTitle,
			Body:  base, Muted: muted, Warning: warning,
		},
		approval: components.ApprovalDialogStyles{
			Frame: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Warning).Padding(1, 2),
			Title: warning,
			Body:  base, Selected: accent,
			Muted: muted, Danger: danger,
		},
		picker: components.PickerStyles{
			Normal: base, Selected: accent,
			Muted: muted, Danger: danger,
		},
		scopeConflict: components.ScopeConflictStyles{
			Frame: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(palette.Warning).Padding(1, 2),
			Title: warning,
			Body:  base, Selected: accent, Muted: muted,
		},
		footer: components.FooterStyles{
			Primary: base, Secondary: muted, Warning: warning,
		},
	}
}

func foregroundStyle(value color.Color) lipgloss.Style {
	style := lipgloss.NewStyle()
	if !isDefaultColor(value) {
		style = style.Foreground(value)
	}
	return style
}

func backgroundStyle(style lipgloss.Style, value color.Color) lipgloss.Style {
	if !isDefaultColor(value) {
		style = style.Background(value)
	}
	return style
}

func isDefaultColor(value color.Color) bool {
	_, ok := value.(lipgloss.NoColor)
	return ok
}
