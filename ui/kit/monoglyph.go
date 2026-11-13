/*
 * ChatCLI - UI kit: monochrome glyph rendering
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package kit

import (
	"strings"
	"unicode/utf8"

	"github.com/diillson/chatcli/ui/theme"
)

// A monochrome theme with full-color emoji scattered through its output is
// two designs at once. Under a theme that sets Monoglyph, text is rendered
// with the one-cell glyphs of this package only: an emoji that carries a
// meaning (failed, done, careful, working) becomes the glyph for that
// meaning, and one that only decorates a heading is dropped together with
// the space that followed it. Themes that do not ask for it are untouched.
//
// It works on finished strings because that is where the emoji are: about
// three hundred of them live in translated messages and in literals across
// the command handlers, and rewriting each call site would freeze the look
// for every other theme too.

// monoStatus maps the emoji used as status marks to their glyph.
var monoStatus = map[rune]Glyph{
	'❌': GlyphError, '⛔': GlyphError, '🛑': GlyphError, '🚫': GlyphError,
	'✅': GlyphSuccess, '✔': GlyphSuccess,
	'⚠': GlyphWarn, '❗': GlyphWarn, '🚨': GlyphWarn,
	'⏳': GlyphRunning, '🔄': GlyphRunning, '⌛': GlyphRunning,
	'🔔': GlyphInfo, '💡': GlyphInfo,
}

// monoArrows maps the emoji arrows to their plain forms.
var monoArrows = map[rune]string{'⬆': "↑", '⬇': "↓", '➡': "→", '⬅': "←", '➤': "→"}

// monoSymbols maps the pictographs that are the whole message where they
// appear: a lock in front of a sensitive field, the badges of the prompt.
// Dropping them as decoration would drop what they say. The stars and
// ballot boxes map to themselves: they sit in the block terminals paint in
// color, but they are text symbols and list markers depend on them.
var monoSymbols = map[rune]string{
	'🔒': "▣", '🔐': "▣", '🌐': "⊕", '🅿': "P",
	'★': "★", '☆': "☆", '☐': "☐", '☑': "☑", '☒': "☒",
}

// isDecorativeEmoji reports whether r is a pictograph with no plain-text
// form worth keeping: the emoji blocks, plus the few symbols in the older
// ranges that terminals paint in color.
func isDecorativeEmoji(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF: // pictographs, transport, supplemental
		return true
	case r >= 0x2600 && r <= 0x26FF: // miscellaneous symbols (cloud, gear, bolt)
		return true
	}
	switch r {
	case '✨', '✂', '✍', '⏱', '⏰', '⌚':
		return true
	}
	return false
}

// isEmojiJoiner reports whether r only modifies the emoji before it: the
// variation selector, the zero-width joiner, skin tones and gender signs.
func isEmojiJoiner(r rune) bool {
	return r == 0xFE0F || r == 0x200D || (r >= 0x1F3FB && r <= 0x1F3FF) || r == '♂' || r == '♀'
}

// Monoglyph returns s with colored emoji rendered as one-cell glyphs. It is
// allocation-free for text with nothing to change, which is nearly all of
// it: plain ASCII returns at once.
func Monoglyph(s string) string {
	plain := true
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	changed := false
	dropSpace := false // the previous rune was a dropped emoji
	for _, r := range s {
		if isEmojiJoiner(r) {
			changed = true
			continue
		}
		if dropSpace && r == ' ' {
			dropSpace = false
			continue
		}
		dropSpace = false
		if g, ok := monoStatus[r]; ok {
			b.WriteString(g.String())
			changed = true
			continue
		}
		if a, ok := monoArrows[r]; ok {
			b.WriteString(a)
			changed = true
			continue
		}
		if sym, ok := monoSymbols[r]; ok {
			b.WriteString(sym)
			changed = changed || sym != string(r)
			continue
		}
		if isDecorativeEmoji(r) {
			changed = true
			dropSpace = true
			continue
		}
		b.WriteRune(r)
	}
	if !changed {
		return s
	}
	return b.String()
}

// Plain applies Monoglyph when the active theme asks for it, and returns s
// as is otherwise. It is the call every output path makes.
func Plain(s string) string {
	if !theme.Active().Monoglyph {
		return s
	}
	return Monoglyph(s)
}
