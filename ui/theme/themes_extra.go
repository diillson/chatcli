/*
 * ChatCLI - Extra built-in color themes
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Community-favorite palettes mapped onto ChatCLI's 12 semantic roles. Each
 * theme follows the same contract as Dark/Light (registry.go): the Hex drives
 * truecolor terminals, ANSI256 the 256-color profile, and ANSI16 the classic
 * 16-color SGR fallback — chosen so the roles stay distinguishable even on a
 * 16-color terminal (e.g. Accent and Info never collapse to the same code).
 *
 * Registering a theme is a single line in registry.go's `builtins` map plus a
 * cfg.ui.theme_desc_<name> string in every locale; everything else (validation,
 * /config ui listing, completer) derives from those.
 */
package theme

// DraculaTheme — the iconic high-contrast dark palette (purple/pink/cyan).
func DraculaTheme() Theme {
	return Theme{
		Name:    "dracula",
		Variant: VariantDark,
		Palette: Palette{
			Primary:    Color{Hex: "#BD93F9", ANSI256: 141, ANSI16: 5},
			Secondary:  Color{Hex: "#FF79C6", ANSI256: 212, ANSI16: 13},
			Accent:     Color{Hex: "#8BE9FD", ANSI256: 117, ANSI16: 14},
			Muted:      Color{Hex: "#6272A4", ANSI256: 60, ANSI16: 8},
			Success:    Color{Hex: "#50FA7B", ANSI256: 84, ANSI16: 10},
			Warning:    Color{Hex: "#F1FA8C", ANSI256: 228, ANSI16: 11},
			Danger:     Color{Hex: "#FF5555", ANSI256: 203, ANSI16: 9},
			Info:       Color{Hex: "#FFB86C", ANSI256: 215, ANSI16: 3},
			Border:     Color{Hex: "#44475A", ANSI256: 238, ANSI16: 8},
			Text:       Color{Hex: "#F8F8F2", ANSI256: 253, ANSI16: 7},
			TextStrong: Color{Hex: "#FFFFFF", ANSI256: 231, ANSI16: 15},
			Background: Color{Hex: "#282A36", ANSI256: 236, ANSI16: 0},
		},
	}
}

// NordTheme — the cool, low-saturation arctic palette (frost + aurora).
func NordTheme() Theme {
	return Theme{
		Name:    "nord",
		Variant: VariantDark,
		Palette: Palette{
			Primary:    Color{Hex: "#81A1C1", ANSI256: 110, ANSI16: 4},
			Secondary:  Color{Hex: "#B48EAD", ANSI256: 139, ANSI16: 5},
			Accent:     Color{Hex: "#88C0D0", ANSI256: 116, ANSI16: 6},
			Muted:      Color{Hex: "#4C566A", ANSI256: 240, ANSI16: 8},
			Success:    Color{Hex: "#A3BE8C", ANSI256: 108, ANSI16: 2},
			Warning:    Color{Hex: "#EBCB8B", ANSI256: 222, ANSI16: 3},
			Danger:     Color{Hex: "#BF616A", ANSI256: 131, ANSI16: 1},
			Info:       Color{Hex: "#8FBCBB", ANSI256: 109, ANSI16: 14},
			Border:     Color{Hex: "#434C5E", ANSI256: 238, ANSI16: 8},
			Text:       Color{Hex: "#D8DEE9", ANSI256: 188, ANSI16: 7},
			TextStrong: Color{Hex: "#ECEFF4", ANSI256: 255, ANSI16: 15},
			Background: Color{Hex: "#2E3440", ANSI256: 236, ANSI16: 0},
		},
	}
}

// TokyoNightTheme — modern night-blue palette with vivid accents.
func TokyoNightTheme() Theme {
	return Theme{
		Name:    "tokyo-night",
		Variant: VariantDark,
		Palette: Palette{
			Primary:    Color{Hex: "#7AA2F7", ANSI256: 111, ANSI16: 4},
			Secondary:  Color{Hex: "#BB9AF7", ANSI256: 141, ANSI16: 5},
			Accent:     Color{Hex: "#7DCFFF", ANSI256: 117, ANSI16: 6},
			Muted:      Color{Hex: "#565F89", ANSI256: 60, ANSI16: 8},
			Success:    Color{Hex: "#9ECE6A", ANSI256: 149, ANSI16: 2},
			Warning:    Color{Hex: "#E0AF68", ANSI256: 179, ANSI16: 3},
			Danger:     Color{Hex: "#F7768E", ANSI256: 204, ANSI16: 1},
			Info:       Color{Hex: "#FF9E64", ANSI256: 215, ANSI16: 11},
			Border:     Color{Hex: "#3B4261", ANSI256: 237, ANSI16: 8},
			Text:       Color{Hex: "#C0CAF5", ANSI256: 189, ANSI16: 7},
			TextStrong: Color{Hex: "#FFFFFF", ANSI256: 231, ANSI16: 15},
			Background: Color{Hex: "#1A1B26", ANSI256: 234, ANSI16: 0},
		},
	}
}

// SolarizedDarkTheme — Ethan Schoonover's Solarized on its dark base.
func SolarizedDarkTheme() Theme {
	return Theme{
		Name:    "solarized-dark",
		Variant: VariantDark,
		Palette: Palette{
			Primary:    Color{Hex: "#268BD2", ANSI256: 32, ANSI16: 4},
			Secondary:  Color{Hex: "#D33682", ANSI256: 168, ANSI16: 5},
			Accent:     Color{Hex: "#6C71C4", ANSI256: 61, ANSI16: 13},
			Muted:      Color{Hex: "#586E75", ANSI256: 240, ANSI16: 8},
			Success:    Color{Hex: "#859900", ANSI256: 100, ANSI16: 2},
			Warning:    Color{Hex: "#B58900", ANSI256: 136, ANSI16: 3},
			Danger:     Color{Hex: "#DC322F", ANSI256: 160, ANSI16: 1},
			Info:       Color{Hex: "#2AA198", ANSI256: 37, ANSI16: 6},
			Border:     Color{Hex: "#073642", ANSI256: 23, ANSI16: 8},
			Text:       Color{Hex: "#839496", ANSI256: 245, ANSI16: 7},
			TextStrong: Color{Hex: "#93A1A1", ANSI256: 247, ANSI16: 15},
			Background: Color{Hex: "#002B36", ANSI256: 23, ANSI16: 0},
		},
	}
}

// SolarizedLightTheme — Solarized on its light base3 background.
func SolarizedLightTheme() Theme {
	return Theme{
		Name:    "solarized-light",
		Variant: VariantLight,
		Palette: Palette{
			Primary:    Color{Hex: "#268BD2", ANSI256: 32, ANSI16: 4},
			Secondary:  Color{Hex: "#D33682", ANSI256: 168, ANSI16: 5},
			Accent:     Color{Hex: "#6C71C4", ANSI256: 61, ANSI16: 5},
			Muted:      Color{Hex: "#93A1A1", ANSI256: 247, ANSI16: 7},
			Success:    Color{Hex: "#859900", ANSI256: 100, ANSI16: 2},
			Warning:    Color{Hex: "#CB4B16", ANSI256: 166, ANSI16: 3},
			Danger:     Color{Hex: "#DC322F", ANSI256: 160, ANSI16: 1},
			Info:       Color{Hex: "#2AA198", ANSI256: 37, ANSI16: 6},
			Border:     Color{Hex: "#93A1A1", ANSI256: 247, ANSI16: 7},
			Text:       Color{Hex: "#657B83", ANSI256: 241, ANSI16: 0},
			TextStrong: Color{Hex: "#586E75", ANSI256: 240, ANSI16: 0},
			Background: Color{Hex: "#FDF6E3", ANSI256: 230, ANSI16: 15},
		},
	}
}

// GruvboxTheme — retro, warm, high-contrast dark palette.
func GruvboxTheme() Theme {
	return Theme{
		Name:    "gruvbox",
		Variant: VariantDark,
		Palette: Palette{
			Primary:    Color{Hex: "#FE8019", ANSI256: 208, ANSI16: 11},
			Secondary:  Color{Hex: "#D3869B", ANSI256: 175, ANSI16: 5},
			Accent:     Color{Hex: "#8EC07C", ANSI256: 108, ANSI16: 6},
			Muted:      Color{Hex: "#928374", ANSI256: 245, ANSI16: 8},
			Success:    Color{Hex: "#B8BB26", ANSI256: 142, ANSI16: 2},
			Warning:    Color{Hex: "#FABD2F", ANSI256: 214, ANSI16: 3},
			Danger:     Color{Hex: "#FB4934", ANSI256: 203, ANSI16: 1},
			Info:       Color{Hex: "#83A598", ANSI256: 109, ANSI16: 14},
			Border:     Color{Hex: "#504945", ANSI256: 240, ANSI16: 8},
			Text:       Color{Hex: "#EBDBB2", ANSI256: 223, ANSI16: 7},
			TextStrong: Color{Hex: "#FBF1C7", ANSI256: 230, ANSI16: 15},
			Background: Color{Hex: "#282828", ANSI256: 235, ANSI16: 0},
		},
	}
}

// CatppuccinMochaTheme — soft pastel dark palette (the Mocha flavor).
func CatppuccinMochaTheme() Theme {
	return Theme{
		Name:    "catppuccin-mocha",
		Variant: VariantDark,
		Palette: Palette{
			Primary:    Color{Hex: "#CBA6F7", ANSI256: 183, ANSI16: 5},
			Secondary:  Color{Hex: "#89B4FA", ANSI256: 111, ANSI16: 4},
			Accent:     Color{Hex: "#89DCEB", ANSI256: 117, ANSI16: 6},
			Muted:      Color{Hex: "#6C7086", ANSI256: 243, ANSI16: 8},
			Success:    Color{Hex: "#A6E3A1", ANSI256: 151, ANSI16: 2},
			Warning:    Color{Hex: "#F9E2AF", ANSI256: 223, ANSI16: 3},
			Danger:     Color{Hex: "#F38BA8", ANSI256: 211, ANSI16: 1},
			Info:       Color{Hex: "#94E2D5", ANSI256: 115, ANSI16: 14},
			Border:     Color{Hex: "#313244", ANSI256: 237, ANSI16: 8},
			Text:       Color{Hex: "#CDD6F4", ANSI256: 189, ANSI16: 7},
			TextStrong: Color{Hex: "#FFFFFF", ANSI256: 231, ANSI16: 15},
			Background: Color{Hex: "#1E1E2E", ANSI256: 235, ANSI16: 0},
		},
	}
}

// MonokaiTheme — the classic editor palette (pink/green/cyan on charcoal).
func MonokaiTheme() Theme {
	return Theme{
		Name:    "monokai",
		Variant: VariantDark,
		Palette: Palette{
			Primary:    Color{Hex: "#AE81FF", ANSI256: 141, ANSI16: 5},
			Secondary:  Color{Hex: "#66D9EF", ANSI256: 81, ANSI16: 4},
			Accent:     Color{Hex: "#F92672", ANSI256: 197, ANSI16: 13},
			Muted:      Color{Hex: "#75715E", ANSI256: 101, ANSI16: 8},
			Success:    Color{Hex: "#A6E22E", ANSI256: 148, ANSI16: 2},
			Warning:    Color{Hex: "#E6DB74", ANSI256: 186, ANSI16: 3},
			Danger:     Color{Hex: "#FF6188", ANSI256: 204, ANSI16: 1},
			Info:       Color{Hex: "#FD971F", ANSI256: 208, ANSI16: 11},
			Border:     Color{Hex: "#49483E", ANSI256: 238, ANSI16: 8},
			Text:       Color{Hex: "#F8F8F2", ANSI256: 253, ANSI16: 7},
			TextStrong: Color{Hex: "#FFFFFF", ANSI256: 231, ANSI16: 15},
			Background: Color{Hex: "#272822", ANSI256: 235, ANSI16: 0},
		},
	}
}

// OneDarkTheme — Atom's One Dark, balanced blue/gray with soft accents.
func OneDarkTheme() Theme {
	return Theme{
		Name:    "one-dark",
		Variant: VariantDark,
		Palette: Palette{
			Primary:    Color{Hex: "#61AFEF", ANSI256: 75, ANSI16: 4},
			Secondary:  Color{Hex: "#C678DD", ANSI256: 176, ANSI16: 5},
			Accent:     Color{Hex: "#56B6C2", ANSI256: 73, ANSI16: 6},
			Muted:      Color{Hex: "#5C6370", ANSI256: 241, ANSI16: 8},
			Success:    Color{Hex: "#98C379", ANSI256: 114, ANSI16: 2},
			Warning:    Color{Hex: "#E5C07B", ANSI256: 180, ANSI16: 3},
			Danger:     Color{Hex: "#E06C75", ANSI256: 168, ANSI16: 1},
			Info:       Color{Hex: "#D19A66", ANSI256: 173, ANSI16: 11},
			Border:     Color{Hex: "#3B4048", ANSI256: 238, ANSI16: 8},
			Text:       Color{Hex: "#ABB2BF", ANSI256: 249, ANSI16: 7},
			TextStrong: Color{Hex: "#FFFFFF", ANSI256: 231, ANSI16: 15},
			Background: Color{Hex: "#282C34", ANSI256: 236, ANSI16: 0},
		},
	}
}
