/*
 * ChatCLI - Windows console input sanitizer.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * On Windows, go-prompt reads keys through go-tty, which translates any key
 * pressed with Alt held — INCLUDING AltGr (right Alt), which Windows reports
 * as Ctrl+Alt — into an ESC prefix followed by the character. That is correct
 * for Alt-as-Meta shortcuts, but AltGr is how several layouts type everyday
 * characters: "/" is AltGr+Q on Brazilian ABNT2, "@"/"{"/"[" need AltGr on
 * German and Nordic layouts, and so on. go-prompt doesn't recognize
 * ESC+<printable> sequences, so it inserts the raw ESC byte into the edit
 * buffer, which the terminal renders as a stray "?"-like glyph before the
 * character the user actually typed.
 *
 * altGrParser wraps the platform ConsoleParser and strips that spurious ESC
 * when — and only when — the batch is ESC followed by exactly one printable
 * rune. Real escape sequences (CSI "ESC [", SS3 "ESC O", double-ESC) and the
 * Alt shortcuts chatcli itself binds (ESC f / ESC b word navigation,
 * ESC DEL / ESC BS delete-word) pass through untouched. The logic is
 * platform-neutral (and unit-tested everywhere); the wrapper is only wired
 * into the prompt on Windows, where the go-tty translation exists.
 */
package cli

import (
	"unicode"
	"unicode/utf8"

	prompt "github.com/c-bata/go-prompt"
)

// altGrParser is a pass-through prompt.ConsoleParser that rewrites AltGr
// key batches (ESC + printable rune) into the bare rune. See the file
// header for the full rationale.
type altGrParser struct {
	inner prompt.ConsoleParser
}

// newAltGrParser wraps inner with the AltGr ESC-prefix sanitizer.
func newAltGrParser(inner prompt.ConsoleParser) *altGrParser {
	return &altGrParser{inner: inner}
}

// Setup delegates to the wrapped parser.
func (p *altGrParser) Setup() error { return p.inner.Setup() }

// TearDown delegates to the wrapped parser.
func (p *altGrParser) TearDown() error { return p.inner.TearDown() }

// GetWinSize delegates to the wrapped parser.
func (p *altGrParser) GetWinSize() *prompt.WinSize { return p.inner.GetWinSize() }

// Read sanitizes each batch produced by the wrapped parser.
func (p *altGrParser) Read() ([]byte, error) {
	data, err := p.inner.Read()
	if err != nil {
		return data, err
	}
	return stripAltGrEscape(data), nil
}

// stripAltGrEscape rewrites an "ESC + one printable rune" batch into the bare
// rune, leaving every other batch (real escape sequences, bound Alt
// shortcuts, plain text, lone ESC) unchanged.
func stripAltGrEscape(data []byte) []byte {
	if len(data) < 2 || data[0] != 0x1b {
		return data
	}
	rest := data[1:]
	switch rest[0] {
	case '[', 'O', 0x1b:
		// CSI / SS3 / double-ESC: genuine terminal escape sequences.
		return data
	}
	if len(rest) == 1 {
		switch rest[0] {
		case 'f', 'b', 0x7f, 0x08:
			// Alt shortcuts chatcli binds via ASCIICodeBind (word
			// navigation, delete word) — must keep the ESC prefix.
			return data
		}
	}
	r, size := utf8.DecodeRune(rest)
	if r == utf8.RuneError || size != len(rest) || !unicode.IsPrint(r) {
		return data
	}
	return rest
}
