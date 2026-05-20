/*
 * ChatCLI - tests for the unified response envelope
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * These tests defend three invariants that the user-visible bug
 * report hinged on:
 *
 *  1. The right border never drifts when the body contains emojis
 *     with variation selectors / ZWJ sequences (🏟️, ⚫, 🔴, ⚪).
 *  2. Long lines wrap to the inner width — they never escape the box.
 *  3. The card width follows the requested Width, with both borders
 *     measuring the same number of visible columns.
 */

package agent

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"
)

// captureEnvStdout runs fn and returns whatever it wrote to stdout.
// Stand-alone helper so we don't depend on the cli package's
// captureStdout (different package, different file scope).
func captureEnvStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	_ = w.Close()
	os.Stdout = old
	<-done
	return buf.String()
}

// stripANSIEnv strips CSI color sequences so visible-width checks can
// run on plain text. Mirrors stripANSIForCard but inline so this test
// file stays self-contained.
func stripANSIEnv(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// TestRenderResponseEnvelope_BoxIsClosed asserts the envelope draws
// matching top + side + bottom borders for a simple body. Without
// this guarantee long bodies could escape on the right.
func TestRenderResponseEnvelope_BoxIsClosed(t *testing.T) {
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	out := captureEnvStdout(t, func() {
		r.RenderResponseEnvelope(ResponseEnvelopeOptions{
			HeaderLeft:  " 💬 RESPOSTA ",
			HeaderRight: " 1.4s · 312↑ 1.8k↓ ",
			Body:        "Hello, world.",
			Color:       ColorGray,
			Width:       80,
		})
	})
	plain := stripANSIEnv(out)

	assert.Contains(t, plain, "╭", "top-left corner present")
	assert.Contains(t, plain, "╮", "top-right corner present")
	assert.Contains(t, plain, "╰", "bottom-left corner present")
	assert.Contains(t, plain, "╯", "bottom-right corner present")
	assert.Contains(t, plain, "│", "vertical side border present")
	assert.Contains(t, plain, "RESPOSTA", "header left label surfaces")
	assert.Contains(t, plain, "1.4s", "header right label surfaces")
	assert.Contains(t, plain, "Hello, world.", "body content surfaces")
}

// TestRenderResponseEnvelope_BordersAlignAcrossWidths verifies that
// at multiple terminal widths the top, every body row, and the bottom
// all measure the same visible width. This is the contract that
// prevents the "text outside the box" regression.
func TestRenderResponseEnvelope_BordersAlignAcrossWidths(t *testing.T) {
	cases := []int{40, 60, 80, 120, 180}
	for _, w := range cases {
		r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
		out := captureEnvStdout(t, func() {
			r.RenderResponseEnvelope(ResponseEnvelopeOptions{
				HeaderLeft:  " 💬 RESPOSTA ",
				HeaderRight: " 1.4s ",
				Body:        "Curta resposta com algumas palavras.",
				Color:       ColorGray,
				Width:       w,
			})
		})
		plain := stripANSIEnv(out)
		rows := splitNonEmpty(plain)

		// Each row that is part of the box (i.e. starts with one of the
		// border glyphs) must report the same visible width.
		var widths []int
		for _, row := range rows {
			if startsWithBorder(row) {
				widths = append(widths, lipgloss.Width(row))
			}
		}
		if len(widths) < 3 {
			t.Fatalf("width=%d: expected at least top+body+bottom rows, got %d (rows=%v)", w, len(widths), rows)
		}
		first := widths[0]
		for _, got := range widths {
			assert.Equalf(t, first, got, "width=%d: every border row must agree on visible width", w)
		}
	}
}

// TestRenderResponseEnvelope_EmojiHeavyContent feeds the box the same
// emoji-heavy body pattern the user reported overflowing in the bug
// report. After the runewidth.StrictEmojiNeutral normalization, the
// right border must stay aligned with the rest of the box.
func TestRenderResponseEnvelope_EmojiHeavyContent(t *testing.T) {
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	body := strings.Join([]string{
		"Tem sim! 🔥",
		"Flamengo x Estudiantes (ARG)",
		"• 📅 Hoje, quarta-feira (20/05)",
		"• ⏰ 21h30 (horário de Brasília)",
		"• 🏟️ Maracanã, Rio de Janeiro",
		"• 🏆 Copa Libertadores 2026",
		"No primeiro confronto: 🏟️⚫🔴",
	}, "\n")
	out := captureEnvStdout(t, func() {
		r.RenderResponseEnvelope(ResponseEnvelopeOptions{
			HeaderLeft:  " 💬 RESPOSTA ",
			HeaderRight: " 1.4s · 312↑ 1.8k↓ ",
			Body:        body,
			Color:       ColorGray,
			Width:       90,
		})
	})
	plain := stripANSIEnv(out)

	rows := splitNonEmpty(plain)
	var widths []int
	for _, row := range rows {
		if startsWithBorder(row) {
			widths = append(widths, lipgloss.Width(row))
		}
	}
	if len(widths) < 3 {
		t.Fatalf("expected at least top+body+bottom rows, got rows=%v", rows)
	}
	first := widths[0]
	for _, got := range widths {
		assert.Equal(t, first, got,
			"emoji-heavy content must not drift the right border")
	}
}

// TestRenderResponseEnvelope_LongLineWraps proves the renderer wraps
// long single-line bodies inside the inner width so no row ever
// exceeds the card width. Reproduces the chat-mode failure mode the
// user described ("não respeita o mesmo tab de linha que o tamanho
// da box").
func TestRenderResponseEnvelope_LongLineWraps(t *testing.T) {
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	long := strings.Repeat("palavra ", 60)
	out := captureEnvStdout(t, func() {
		r.RenderResponseEnvelope(ResponseEnvelopeOptions{
			HeaderLeft: " 💬 ",
			Body:       long,
			Color:      ColorGray,
			Width:      60,
		})
	})
	plain := stripANSIEnv(out)

	for _, row := range strings.Split(plain, "\n") {
		if !startsWithBorder(row) {
			continue
		}
		// Borders + padding consume at most 6 cols of the requested
		// width — every visible row must therefore be ≤ requested width.
		assert.LessOrEqual(t, lipgloss.Width(row), 60,
			"long body row must wrap inside the box: %q", row)
	}
}

// TestRenderResponseEnvelope_ShortBodyLongHeader is the regression
// test for the bug the user reported on 2026-05-20: when the header
// bilateral labels (model + latency · tokens) measured wider than the
// body content, the top border painted wider than the body+bottom and
// the box read as broken. The envelope must now grow the card to fit
// the header, padding the body out to match.
func TestRenderResponseEnvelope_ShortBodyLongHeader(t *testing.T) {
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	out := captureEnvStdout(t, func() {
		r.RenderResponseEnvelope(ResponseEnvelopeOptions{
			HeaderLeft:  " Claude sonnet 4.6 (1M context) ",
			HeaderRight: " 3.3s · 2↑ 12↓ ",
			Body:        "Boa tarde, Edilson! Tudo certo?\nComo posso ajudar?",
			Color:       ColorGray,
			Width:       192,
		})
	})
	plain := stripANSIEnv(out)

	var widths []int
	for _, row := range strings.Split(plain, "\n") {
		if startsWithBorder(row) {
			widths = append(widths, lipgloss.Width(row))
		}
	}
	if len(widths) < 4 {
		t.Fatalf("expected top + 2 body rows + bottom, got %d rows", len(widths))
	}
	first := widths[0]
	for _, got := range widths {
		assert.Equal(t, first, got,
			"short body + long header must produce a closed box — every row same width")
	}
}

// TestRenderResponseEnvelope_NoLabels covers the minimal-call shape:
// no labels, just a body and a color. The envelope must still draw a
// valid closed box (corners + sides + bottom).
func TestRenderResponseEnvelope_NoLabels(t *testing.T) {
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	out := captureEnvStdout(t, func() {
		r.RenderResponseEnvelope(ResponseEnvelopeOptions{
			Body:  "Plain body, no labels.",
			Color: ColorGray,
			Width: 50,
		})
	})
	plain := stripANSIEnv(out)

	assert.Contains(t, plain, "╭")
	assert.Contains(t, plain, "╮")
	assert.Contains(t, plain, "╰")
	assert.Contains(t, plain, "╯")
	assert.Contains(t, plain, "│")
	assert.Contains(t, plain, "Plain body, no labels.")
}

// TestRunewidthNormalization_EmojiIsTwoCells locks the init() side
// effect: with our normalization, runewidth reports emojis as 2 cols,
// matching what modern terminals actually paint. If a future refactor
// removes the init() call, this test fires.
func TestRunewidthNormalization_EmojiIsTwoCells(t *testing.T) {
	cases := []struct {
		glyph string
		want  int
	}{
		{"🔥", 2},
		{"⚫", 2},
		{"🔴", 2},
		{"📅", 2},
		{"🏆", 2},
	}
	for _, tc := range cases {
		got := runewidth.StringWidth(tc.glyph)
		assert.Equalf(t, tc.want, got,
			"runewidth normalization must report %q as %d cols, got %d",
			tc.glyph, tc.want, got)
	}
}

// TestEnvelopeWidth_FallbackOutsideTTY asserts the helper returns a
// safe positive number when no TTY is attached (the test runner case).
// The exact value isn't part of the contract — only that it's ≥ 40
// (so the box never collapses).
func TestEnvelopeWidth_FallbackOutsideTTY(t *testing.T) {
	w := EnvelopeWidth()
	assert.GreaterOrEqual(t, w, 40, "envelope width must always be ≥ 40")
}

// --- small local helpers (avoid leaking into the package surface) ---

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func startsWithBorder(row string) bool {
	trim := strings.TrimLeft(row, " ")
	if trim == "" {
		return false
	}
	first := []rune(trim)[0]
	return first == '╭' || first == '╮' || first == '╰' || first == '╯' || first == '│'
}
