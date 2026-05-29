/*
 * ChatCLI - Terminal color capability detection
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The theme defines colors as truecolor hex; the terminal may only speak
 * 256-color, 16-color, or no color at all (a pipe, a CI log). Profile is the
 * theme package's own abstraction over that capability so the rest of the
 * package never imports colorprofile/termenv directly and the degradation
 * rules live in exactly one place.
 *
 * Detection is delegated to charmbracelet/colorprofile, which already honors
 * NO_COLOR, CLICOLOR_FORCE, TERM, COLORTERM and tmux — re-implementing that
 * heuristic would be a maintenance liability, so we wrap it instead.
 */
package theme

import (
	"os"

	"github.com/charmbracelet/colorprofile"
	"github.com/muesli/termenv"
)

// Profile is a terminal's color capability, ordered from least to most
// capable. The theme package owns this type so callers depend on a stable
// local abstraction rather than a third-party enum. The underlying type is
// int32 so it round-trips through the atomic.Int32 that holds the active
// profile without a widening/narrowing conversion (avoids gosec G115).
type Profile int32

const (
	// ProfileNoTTY: output is not a terminal (pipe, file, CI). No styling.
	ProfileNoTTY Profile = iota
	// ProfileASCII: a terminal that should not be colored (NO_COLOR, dumb).
	ProfileASCII
	// ProfileANSI: classic 16-color terminal (SGR 30–37 / 90–97).
	ProfileANSI
	// ProfileANSI256: 8-bit, 256-color terminal.
	ProfileANSI256
	// ProfileTrueColor: 24-bit color terminal.
	ProfileTrueColor
)

// HasColor reports whether the profile can render any color at all. Used to
// decide whether to emit color escapes versus plain text.
func (p Profile) HasColor() bool { return p >= ProfileANSI }

// IsTerminal reports whether output is an interactive terminal (any profile
// other than NoTTY). Animations like spinners are only worthwhile on a
// terminal — into a pipe the carriage-return repaints are just noise — but
// they remain useful on a color-less terminal (NO_COLOR), so the gate is
// "is a terminal", not "has color".
func (p Profile) IsTerminal() bool { return p != ProfileNoTTY }

// String renders the profile name for /config display.
func (p Profile) String() string {
	switch p {
	case ProfileTrueColor:
		return "truecolor"
	case ProfileANSI256:
		return "256-color"
	case ProfileANSI:
		return "16-color"
	case ProfileASCII:
		return "ascii"
	default:
		return "no-tty"
	}
}

// Termenv maps the profile to a termenv.Profile so glamour's
// WithColorProfile can downgrade markdown rendering in lock-step with the
// rest of the UI. NoTTY collapses to Ascii because glamour has no "no color
// at all distinct from ascii" notion — both mean "emit no escapes".
func (p Profile) Termenv() termenv.Profile {
	switch p {
	case ProfileTrueColor:
		return termenv.TrueColor
	case ProfileANSI256:
		return termenv.ANSI256
	case ProfileANSI:
		return termenv.ANSI
	default:
		return termenv.Ascii
	}
}

// fromColorProfile converts a detected colorprofile.Profile into our own.
func fromColorProfile(p colorprofile.Profile) Profile {
	switch p {
	case colorprofile.TrueColor:
		return ProfileTrueColor
	case colorprofile.ANSI256:
		return ProfileANSI256
	case colorprofile.ANSI:
		return ProfileANSI
	case colorprofile.Ascii:
		return ProfileASCII
	default:
		return ProfileNoTTY
	}
}

// DetectProfile inspects stdout and the process environment to determine the
// terminal's color capability. It is cheap enough to call at boot and on a
// theme switch; callers that need it on a hot path should cache the result.
func DetectProfile() Profile {
	return fromColorProfile(colorprofile.Detect(os.Stdout, os.Environ()))
}
