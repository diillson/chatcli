/*
 * ChatCLI - go-prompt colors derived from the active theme
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The REPL input line and its completion dropdown are painted by go-prompt,
 * which owns its own render loop and never sees an ANSI string we build — so
 * theme.Recolor (the bridge that re-skins every fmt.Print call site) cannot
 * reach it. Historically that meant the colors were LITERALS: the text you
 * type was prompt.White no matter which theme was active, which is fine on a
 * dark ground and unusable on a light one — white on white.
 *
 * This file is the missing bridge. Every go-prompt color is derived from a
 * semantic palette entry, so the input line, the suggestion list and the
 * description panel follow a theme switch exactly like the rest of the UI.
 *
 * Two constraints shape the mapping:
 *
 *   - go-prompt speaks only the 16-color enum, so we read each palette
 *     entry's ANSI16 index (the theme's own explicit downgrade) rather than
 *     letting a library guess from the hex.
 *   - A foreground painted ON a filled row must come from the OPPOSITE end of
 *     the palette. Background is the theme's own ground (dark under dark
 *     themes, light under light ones), so it is the correct ink on a
 *     saturated fill, and TextStrong is the correct ink on the neutral Border
 *     fill. Using TextStrong for both would print black on dark gray under
 *     every light theme.
 *
 * Under the default dark theme the resulting indices reproduce the previous
 * literals (Info→10 = prompt.Green, TextStrong→15 = prompt.White, Border→8 =
 * prompt.DarkGray, Background→0 = prompt.Black), so adopting the bridge is a
 * visually neutral change there and a legibility fix everywhere else.
 *
 * go-prompt freezes these on its renderer at construction, so a theme switch
 * mid-session only reaches the input line once the prompt is rebuilt — which
 * is why /config ui theme unwinds the REPL loop (see cli.Start).
 */
package cli

import (
	prompt "github.com/c-bata/go-prompt"
	"github.com/diillson/chatcli/ui/theme"
)

// promptColor converts a palette entry into go-prompt's color enum. The enum
// is the 16 classic indices shifted by one (DefaultColor occupies slot 0), so
// a palette index maps by a single increment. On a profile that cannot color
// (NO_COLOR, a pipe) every entry collapses to DefaultColor, which leaves the
// prompt in the terminal's own foreground instead of forcing a hue nothing
// else in the output is using.
func promptColor(c theme.Color) prompt.Color {
	if !theme.ActiveProfile().HasColor() {
		return prompt.DefaultColor
	}
	if c.ANSI16 > 15 {
		return prompt.DefaultColor
	}
	return prompt.Color(c.ANSI16) + 1
}

// composeOptions folds several go-prompt options into ONE, so a call site
// can pick up the whole theme wiring with a single entry in an option list
// that is otherwise a long literal (Go forbids mixing `opts...` with literal
// arguments in the same variadic call). Options are applied in order and the
// first failure short-circuits, exactly as prompt.New would apply them.
func composeOptions(opts ...prompt.Option) prompt.Option {
	return func(p *prompt.Prompt) error {
		for _, opt := range opts {
			if err := opt(p); err != nil {
				return err
			}
		}
		return nil
	}
}

// ThemePromptTextColors is the single option that paints the input line from
// the active theme: the coder-mode prompt takes just this one, since the
// input line is the whole surface it renders.
func themePromptTextColors() prompt.Option {
	return composeOptions(themePromptTextOptions()...)
}

// themePromptColors is the single option that paints the whole chat REPL
// prompt — input line plus completion dropdown — from the active theme.
func themePromptColors() prompt.Option {
	return composeOptions(themePromptColorOptions()...)
}

// themePromptTextOptions returns the color options for the input line itself:
// the prefix, the text the user types, and the inline completion preview.
// Shared by the chat REPL and the coder-mode prompt, where the input line is
// the whole surface go-prompt renders.
//
// The typed text takes TextStrong — the palette's highest-contrast
// foreground — because the line being composed is the most important text on
// screen. That is prompt.White on every dark theme (unchanged) and near-black
// ink on the light ones (the bug this replaces).
func themePromptTextOptions() []prompt.Option {
	p := theme.Active().Palette
	return []prompt.Option{
		prompt.OptionPrefixTextColor(promptColor(p.Info)),
		prompt.OptionInputTextColor(promptColor(p.TextStrong)),
		prompt.OptionPreviewSuggestionTextColor(promptColor(p.Info)),
	}
}

// themePromptColorOptions returns the full palette wiring for the chat REPL:
// the input line plus the completion dropdown (suggestions, descriptions and
// their selected rows) and the scrollbar.
//
// The dropdown reads as two stacked surfaces — suggestions on the neutral
// Border fill, descriptions on the theme's own ground — with the selected row
// lifted onto the Secondary accent. Every foreground is chosen against the
// fill it lands on, never against the terminal.
func themePromptColorOptions() []prompt.Option {
	p := theme.Active().Palette
	return append(themePromptTextOptions(),
		// Suggestion list: neutral raised surface, strongest ink.
		prompt.OptionSuggestionBGColor(promptColor(p.Border)),
		prompt.OptionSuggestionTextColor(promptColor(p.TextStrong)),
		// Selected suggestion: accent fill, so the ink is the theme's ground.
		prompt.OptionSelectedSuggestionBGColor(promptColor(p.Secondary)),
		prompt.OptionSelectedSuggestionTextColor(promptColor(p.Background)),
		// Description panel: the theme's ground, with the Warning hue that has
		// carried descriptions since before the theme system. The NON-bright
		// yellow matters here — bright yellow on a light terminal is invisible.
		prompt.OptionDescriptionBGColor(promptColor(p.Background)),
		prompt.OptionDescriptionTextColor(promptColor(p.Warning)),
		// Selected description: the same neutral surface as the suggestion
		// list, so the selected pair reads as one raised row.
		prompt.OptionSelectedDescriptionBGColor(promptColor(p.Border)),
		prompt.OptionSelectedDescriptionTextColor(promptColor(p.TextStrong)),
		// Scrollbar: muted thumb on the theme's ground — visible in both
		// variants, unlike go-prompt's stock cyan track.
		prompt.OptionScrollbarThumbColor(promptColor(p.Muted)),
		prompt.OptionScrollbarBGColor(promptColor(p.Background)),
	)
}
