/*
 * ChatCLI - Glamour style derived from the active theme
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Markdown is rendered by glamour. Historically ChatCLI hard-coded
 * glamour.WithStandardStyle("dark"), so markdown colors were disconnected
 * from the rest of the UI and code blocks used chroma's stock palette. This
 * file derives a glamour ansi.StyleConfig from the active theme so headings,
 * links, inline code and — crucially — fenced code blocks share the exact
 * hues of every card and border. glamour receives the truecolor hex values
 * and downgrades them itself via WithColorProfile, kept in lock-step with the
 * rest of the UI through the theme Profile.
 */
package theme

import (
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
)

func hexPtr(c Color) *string { s := c.Hex; return &s }
func boolPtr(b bool) *bool   { return &b }
func uintPtr(u uint) *uint   { return &u }

// chromaBase picks a stock chroma style as the base for syntax tokens we do
// not override explicitly. The explicit Chroma overrides below win for the
// common tokens; the base covers the long tail (decorators, namespaces, …).
func (t Theme) chromaBase() string {
	if t.Variant == VariantLight {
		return "github"
	}
	return "monokai"
}

// GlamourStyleConfig builds a glamour ansi.StyleConfig from the palette. Every
// color is the palette's truecolor hex; profile downgrade is glamour's job.
func (t Theme) GlamourStyleConfig() ansi.StyleConfig {
	p := t.Palette

	codeToken := func(c Color) ansi.StylePrimitive {
		return ansi.StylePrimitive{Color: hexPtr(c)}
	}

	return ansi.StyleConfig{
		Document: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: hexPtr(p.Text)},
			Margin:         uintPtr(0),
		},
		BlockQuote: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: hexPtr(p.Muted), Italic: boolPtr(true)},
			Indent:         uintPtr(1),
			IndentToken:    strPtr("│ "),
		},
		Paragraph: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: hexPtr(p.Text)},
		},
		List: ansi.StyleList{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{Color: hexPtr(p.Text)},
			},
			LevelIndent: 2,
		},
		Heading: ansi.StyleBlock{
			StylePrimitive: ansi.StylePrimitive{Color: hexPtr(p.TextStrong), Bold: boolPtr(true)},
		},
		H1: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			Color: hexPtr(p.Primary), Bold: boolPtr(true), Prefix: "# "}},
		H2: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			Color: hexPtr(p.Accent), Bold: boolPtr(true), Prefix: "## "}},
		H3: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			Color: hexPtr(p.Info), Bold: boolPtr(true), Prefix: "### "}},
		H4: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			Color: hexPtr(p.Info), Bold: boolPtr(true), Prefix: "#### "}},
		H5: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			Color: hexPtr(p.Muted), Bold: boolPtr(true), Prefix: "##### "}},
		H6: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			Color: hexPtr(p.Muted), Bold: boolPtr(true), Prefix: "###### "}},
		Text:           ansi.StylePrimitive{Color: hexPtr(p.Text)},
		Strong:         ansi.StylePrimitive{Color: hexPtr(p.TextStrong), Bold: boolPtr(true)},
		Emph:           ansi.StylePrimitive{Color: hexPtr(p.Text), Italic: boolPtr(true)},
		Strikethrough:  ansi.StylePrimitive{CrossedOut: boolPtr(true)},
		HorizontalRule: ansi.StylePrimitive{Color: hexPtr(p.Muted), Format: "\n──────\n"},
		Item:           ansi.StylePrimitive{Color: hexPtr(p.Text)},
		Enumeration:    ansi.StylePrimitive{Color: hexPtr(p.Muted)},
		Link:           ansi.StylePrimitive{Color: hexPtr(p.Secondary), Underline: boolPtr(true)},
		LinkText:       ansi.StylePrimitive{Color: hexPtr(p.Accent)},
		// Inline `code`: tinted and padded so it reads as a chip.
		Code: ansi.StyleBlock{StylePrimitive: ansi.StylePrimitive{
			Color: hexPtr(p.Warning), Prefix: " ", Suffix: " "}},
		CodeBlock: ansi.StyleCodeBlock{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{Color: hexPtr(p.Text)},
				Margin:         uintPtr(2),
			},
			Theme: t.chromaBase(),
			Chroma: &ansi.Chroma{
				Text:            codeToken(p.Text),
				Error:           ansi.StylePrimitive{Color: hexPtr(p.Danger)},
				Comment:         ansi.StylePrimitive{Color: hexPtr(p.Muted), Italic: boolPtr(true)},
				CommentPreproc:  ansi.StylePrimitive{Color: hexPtr(p.Muted)},
				Keyword:         ansi.StylePrimitive{Color: hexPtr(p.Primary), Bold: boolPtr(true)},
				KeywordReserved: ansi.StylePrimitive{Color: hexPtr(p.Primary)},
				KeywordType:     ansi.StylePrimitive{Color: hexPtr(p.Secondary)},
				Operator:        ansi.StylePrimitive{Color: hexPtr(p.Secondary)},
				Punctuation:     codeToken(p.Text),
				Name:            codeToken(p.Text),
				NameBuiltin:     ansi.StylePrimitive{Color: hexPtr(p.Accent)},
				NameTag:         ansi.StylePrimitive{Color: hexPtr(p.Primary)},
				NameAttribute:   ansi.StylePrimitive{Color: hexPtr(p.Accent)},
				NameClass:       ansi.StylePrimitive{Color: hexPtr(p.Warning)},
				NameConstant:    ansi.StylePrimitive{Color: hexPtr(p.Warning)},
				NameFunction:    ansi.StylePrimitive{Color: hexPtr(p.Accent)},
				LiteralString:   ansi.StylePrimitive{Color: hexPtr(p.Success)},
				LiteralNumber:   ansi.StylePrimitive{Color: hexPtr(p.Warning)},
				GenericDeleted:  ansi.StylePrimitive{Color: hexPtr(p.Danger)},
				GenericInserted: ansi.StylePrimitive{Color: hexPtr(p.Success)},
				GenericEmph:     ansi.StylePrimitive{Italic: boolPtr(true)},
				GenericStrong:   ansi.StylePrimitive{Bold: boolPtr(true)},
			},
		},
		Table: ansi.StyleTable{
			StyleBlock: ansi.StyleBlock{
				StylePrimitive: ansi.StylePrimitive{Color: hexPtr(p.Text)},
			},
		},
		Task: ansi.StyleTask{
			StylePrimitive: ansi.StylePrimitive{Color: hexPtr(p.Text)},
			Ticked:         "[✓] ",
			Unticked:       "[ ] ",
		},
		DefinitionTerm:        ansi.StylePrimitive{Color: hexPtr(p.TextStrong), Bold: boolPtr(true)},
		DefinitionDescription: ansi.StylePrimitive{Color: hexPtr(p.Text)},
	}
}

// GlamourOptions returns the renderer options for the active theme under the
// given profile. It replaces the legacy glamour.WithStandardStyle("dark");
// WithWordWrap(0) is preserved because ChatCLI owns wrapping (wrapStructured)
// downstream so indentation in code blocks survives.
func (t Theme) GlamourOptions(p Profile) []glamour.TermRendererOption {
	return []glamour.TermRendererOption{
		glamour.WithStyles(t.GlamourStyleConfig()),
		glamour.WithWordWrap(0),
		glamour.WithColorProfile(p.Termenv()),
	}
}

func strPtr(s string) *string { return &s }
