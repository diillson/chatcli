/*
 * ChatCLI - Theme registry and active-theme state
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * This file owns the built-in themes and the process-wide active theme +
 * detected color profile. Both are read on every render, so access is
 * lock-free via atomic pointers: a theme swap (rare, user-driven) publishes a
 * new pointer; renders (frequent) load it without contention.
 *
 * The dark theme is calibrated so that, at the 16-color profile, every role
 * resolves to the EXACT SGR code ChatCLI emitted before this package existed
 * (e.g. reasoning → "\033[36m"). That is what makes adopting the theme a
 * visually neutral change on legacy terminals while truecolor terminals get
 * the richer hex palette for free.
 */
package theme

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/lipgloss"
	"github.com/diillson/chatcli/config"
)

// activeTheme and activeProfile hold the live UI state. They are never nil
// after init(); helpers below load them atomically.
var (
	activeTheme   atomic.Pointer[Theme]
	activeProfile atomic.Int32
)

// builtins maps theme names to their constructors. Adding a theme is a single
// entry here plus its constructor — no other file needs to change.
var builtins = map[string]func() Theme{
	"dark":  DarkTheme,
	"light": LightTheme,
}

// init publishes a safe default (the configured default theme + the detected
// profile) so the package is fully usable from the first import — even before
// the CLI has loaded its .env. The env-driven choice is applied later by
// InitFromEnv, which the CLI calls once the dotenv file is loaded; resolving
// CHATCLI_THEME here would race that load and miss values set only in .env.
func init() {
	activeProfile.Store(int32(DetectProfile()))
	t := defaultTheme()
	activeTheme.Store(&t)
}

// defaultTheme returns the configured default theme, falling back to dark if
// the configured name is somehow unknown (defensive — DefaultTheme is a
// compile-time constant that should always resolve).
func defaultTheme() Theme {
	if ctor, ok := builtins[config.DefaultTheme]; ok {
		return ctor()
	}
	return DarkTheme()
}

// InitFromEnv re-detects the terminal color profile and applies the theme
// named by CHATCLI_THEME (falling back to the configured default for an empty
// or unknown value). The CLI calls this right after loading the .env file —
// and again on config reload — so a theme set only in .env takes effect. It
// never errors: an unknown value degrades to the default rather than failing
// startup.
func InitFromEnv() {
	activeProfile.Store(int32(DetectProfile()))

	name := strings.ToLower(strings.TrimSpace(os.Getenv(config.ThemeEnv)))
	if name == "" {
		name = config.DefaultTheme
	}
	if ctor, ok := builtins[name]; ok {
		t := ctor()
		activeTheme.Store(&t)
		return
	}
	t := defaultTheme()
	activeTheme.Store(&t)
}

// Active returns the live theme. Safe for concurrent use.
func Active() Theme { return *activeTheme.Load() }

// ActiveProfile returns the detected (or overridden) color profile.
func ActiveProfile() Profile { return Profile(activeProfile.Load()) }

// SetActive switches the active theme by name. Unknown names are rejected so
// a typo at the /config prompt surfaces an error instead of silently doing
// nothing. The switch is atomic and takes effect on the next render.
func SetActive(name string) error {
	key := strings.ToLower(strings.TrimSpace(name))
	ctor, ok := builtins[key]
	if !ok {
		return fmt.Errorf("unknown theme %q (available: %s)", name, strings.Join(Names(), ", "))
	}
	t := ctor()
	activeTheme.Store(&t)
	return nil
}

// SetProfile overrides the detected color profile. Primarily for tests and
// for honoring an explicit operator override; production normally relies on
// the boot-time detection.
func SetProfile(p Profile) { activeProfile.Store(int32(p)) }

// Names lists the available theme names in stable alphabetical order, for
// help text and validation messages.
func Names() []string {
	names := make([]string, 0, len(builtins))
	for k := range builtins {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ── Ergonomic package-level accessors ────────────────────────────────────
// Call sites use these instead of threading the theme + profile by hand:
//
//	r.Colorize(text, theme.ANSI(theme.RoleReasoning))
//	border := theme.Lip(theme.RoleBorder)

// ANSI returns the foreground escape for a role under the active theme and
// profile. Returns "" on non-color profiles so piped output stays clean.
func ANSI(r Role) string { return Active().ColorFor(r).SGR(ActiveProfile()) }

// Lip returns a lipgloss.Color for a role under the active theme and profile.
func Lip(r Role) lipgloss.Color { return Active().ColorFor(r).Lip(ActiveProfile()) }

// Reset returns the SGR reset sequence, or "" when the profile cannot color,
// so callers can build "color … reset" spans that vanish cleanly in pipes.
func Reset() string {
	if ActiveProfile().HasColor() {
		return "\033[0m"
	}
	return ""
}

// LipFromANSI converts an ANSI foreground escape into a lipgloss.Color. It
// serves two callers at once:
//
//   - Theme-aware call sites pass a sequence produced by ANSI(role) — which,
//     on a truecolor or 256-color terminal, is an extended "\033[38;2;…m" or
//     "\033[38;5;…m" sequence. Those are parsed directly so the card border
//     matches the text exactly.
//   - Legacy call sites (and the basic 16-color form of the above on ANSI
//     terminals) pass a classic constant like "\033[36m". Those map by HUE to
//     the ACTIVE palette, so even un-migrated borders pick up a theme switch.
//
// The basic-code → palette mapping is NOT duplicated here: the parameter is
// extracted and resolved through basicFgColor (the single source of truth that
// Recolor also uses), so the two paths can never drift. Unknown sequences fall
// back to the terminal default so a typo never makes a border vanish.
func LipFromANSI(code string) lipgloss.Color {
	if c, ok := parseExtendedFg(code); ok {
		return c
	}
	if strings.HasPrefix(code, "\033[") && strings.HasSuffix(code, "m") {
		param := code[len("\033[") : len(code)-len("m")]
		if c, ok := basicFgColor(param); ok {
			return c.Lip(ActiveProfile())
		}
	}
	return lipgloss.Color("")
}

// parseExtendedFg parses the extended SGR foreground forms
// "\033[38;2;r;g;bm" (truecolor) and "\033[38;5;nm" (256-color) into a
// lipgloss.Color. Returns ok=false for anything that is not one of those two
// shapes so the caller can fall through to the legacy hue table.
func parseExtendedFg(code string) (lipgloss.Color, bool) {
	if !strings.HasPrefix(code, "\033[38;") || !strings.HasSuffix(code, "m") {
		return "", false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(code, "\033["), "m")
	parts := strings.Split(body, ";")
	switch {
	case len(parts) == 5 && parts[1] == "2":
		r, err1 := strconv.Atoi(parts[2])
		g, err2 := strconv.Atoi(parts[3])
		b, err3 := strconv.Atoi(parts[4])
		if err1 != nil || err2 != nil || err3 != nil {
			return "", false
		}
		return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", r, g, b)), true
	case len(parts) == 3 && parts[1] == "5":
		if _, err := strconv.Atoi(parts[2]); err != nil {
			return "", false
		}
		return lipgloss.Color(parts[2]), true
	default:
		return "", false
	}
}

// DarkTheme is the default theme. Its ANSI16 indices reproduce ChatCLI's
// historical 16-color output exactly; its hex values give truecolor
// terminals a refined, higher-contrast palette of the same hues.
func DarkTheme() Theme {
	return Theme{
		Name:    "dark",
		Variant: VariantDark,
		Palette: Palette{
			Primary:    Color{Hex: "#B681F4", ANSI256: 141, ANSI16: 5},
			Secondary:  Color{Hex: "#5B9DFF", ANSI256: 75, ANSI16: 4},
			Accent:     Color{Hex: "#38BDF8", ANSI256: 45, ANSI16: 6},
			Muted:      Color{Hex: "#8A93A2", ANSI256: 245, ANSI16: 8},
			Success:    Color{Hex: "#4ADE80", ANSI256: 78, ANSI16: 2},
			Warning:    Color{Hex: "#FACC15", ANSI256: 220, ANSI16: 3},
			Danger:     Color{Hex: "#F87171", ANSI256: 203, ANSI16: 1},
			Info:       Color{Hex: "#A3E635", ANSI256: 149, ANSI16: 10},
			Border:     Color{Hex: "#4B5563", ANSI256: 240, ANSI16: 8},
			Text:       Color{Hex: "#D4D4D4", ANSI256: 252, ANSI16: 7},
			TextStrong: Color{Hex: "#F5F5F5", ANSI256: 231, ANSI16: 15},
			Background: Color{Hex: "#1E2030", ANSI256: 235, ANSI16: 0},
		},
	}
}

// LightTheme is a fully-realized light variant for terminals with light
// backgrounds. Hues mirror the dark theme but are darkened for contrast on
// white; ANSI16 indices use the non-bright range so 16-color light terminals
// stay legible.
func LightTheme() Theme {
	return Theme{
		Name:    "light",
		Variant: VariantLight,
		Palette: Palette{
			Primary:   Color{Hex: "#7C3AED", ANSI256: 92, ANSI16: 5},
			Secondary: Color{Hex: "#2563EB", ANSI256: 26, ANSI16: 4},
			Accent:    Color{Hex: "#0891B2", ANSI256: 31, ANSI16: 6},
			Muted:     Color{Hex: "#6B7280", ANSI256: 244, ANSI16: 8},
			Success:   Color{Hex: "#16A34A", ANSI256: 34, ANSI16: 2},
			Warning:   Color{Hex: "#B45309", ANSI256: 130, ANSI16: 3},
			Danger:    Color{Hex: "#DC2626", ANSI256: 160, ANSI16: 1},
			// ANSI16 10 (bright green), distinct from Success' 2 so the two
			// roles stay distinguishable on 16-color terminals — matching the
			// dark theme's Info/Success separation.
			Info:       Color{Hex: "#4D7C0F", ANSI256: 64, ANSI16: 10},
			Border:     Color{Hex: "#9CA3AF", ANSI256: 247, ANSI16: 7},
			Text:       Color{Hex: "#1F2937", ANSI256: 236, ANSI16: 0},
			TextStrong: Color{Hex: "#111827", ANSI256: 232, ANSI16: 0},
			Background: Color{Hex: "#F3F4F6", ANSI256: 254, ANSI16: 15},
		},
	}
}
