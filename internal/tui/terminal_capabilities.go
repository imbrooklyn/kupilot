package tui

// TerminalCapabilityState is delivery-owned and never grants Application
// authority. Values are fixed, content-free, run-local, and non-persistent.
type TerminalCapabilityState string

const (
	TerminalCapabilityAvailable   TerminalCapabilityState = "available"
	TerminalCapabilityDisabled    TerminalCapabilityState = "disabled"
	TerminalCapabilityRestricted  TerminalCapabilityState = "restricted"
	TerminalCapabilityUnsupported TerminalCapabilityState = "unsupported"
)

// TerminalColorProfile describes the selected local rendering contract.
type TerminalColorProfile string

const (
	TerminalColorANSI   TerminalColorProfile = "ansi"
	TerminalColorANSI16 TerminalColorProfile = "ansi_16"
	TerminalColorNone   TerminalColorProfile = "no_color"
)

// TerminalCapabilityProfile is a bounded projection derived by the delivery
// composition root from the output TTY and a small allowlist of terminal
// environment markers. It performs no active capability query.
type TerminalCapabilityProfile struct {
	NativeClipboard TerminalCapabilityState
	OSC52           TerminalCapabilityState
	Multiplexer     TerminalCapabilityState
	RemoteSession   TerminalCapabilityState
	Title           TerminalCapabilityState
	Notification    TerminalCapabilityState
	Color           TerminalColorProfile
	AlternateScreen TerminalCapabilityState
	ReducedMotion   bool
	Scrollback      string
}

const TerminalScrollbackRestoredCommitted = "primary_screen_restored_committed"

func (profile TerminalCapabilityProfile) valid() bool {
	return validTerminalCapability(profile.NativeClipboard) && validTerminalCapability(profile.OSC52) &&
		validTerminalCapability(profile.Multiplexer) && validTerminalCapability(profile.RemoteSession) &&
		validTerminalCapability(profile.Title) && validTerminalCapability(profile.Notification) &&
		validTerminalCapability(profile.AlternateScreen) &&
		(profile.Color == TerminalColorANSI || profile.Color == TerminalColorANSI16 || profile.Color == TerminalColorNone) &&
		profile.Scrollback == TerminalScrollbackRestoredCommitted
}

func validTerminalCapability(value TerminalCapabilityState) bool {
	return value == TerminalCapabilityAvailable || value == TerminalCapabilityDisabled ||
		value == TerminalCapabilityRestricted || value == TerminalCapabilityUnsupported
}

func defaultTerminalCapabilityProfile(theme ThemeMode, clipboard, title bool) TerminalCapabilityProfile {
	color := TerminalColorANSI
	if theme == ThemeNoColor {
		color = TerminalColorNone
	} else if theme == ThemeANSI16 {
		color = TerminalColorANSI16
	}
	osc52 := TerminalCapabilityUnsupported
	if clipboard {
		osc52 = TerminalCapabilityAvailable
	}
	titleState := TerminalCapabilityUnsupported
	if title {
		titleState = TerminalCapabilityAvailable
	}
	return TerminalCapabilityProfile{
		NativeClipboard: TerminalCapabilityUnsupported,
		OSC52:           osc52,
		Multiplexer:     TerminalCapabilityAvailable,
		RemoteSession:   TerminalCapabilityAvailable,
		Title:           titleState,
		Notification:    TerminalCapabilityDisabled,
		Color:           color,
		AlternateScreen: TerminalCapabilityDisabled,
		ReducedMotion:   false,
		Scrollback:      TerminalScrollbackRestoredCommitted,
	}
}

func (profile TerminalCapabilityProfile) clipboardAvailable() bool {
	return profile.NativeClipboard == TerminalCapabilityAvailable || profile.OSC52 == TerminalCapabilityAvailable
}
