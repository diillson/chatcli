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

// TestRenderResponseEnvelope_SobrioShape asserts the borderless reply
// shape: a bilateral titled rule opens the reply, the body sits on the
// two-space indent, and no box glyph is drawn anywhere.
func TestRenderResponseEnvelope_SobrioShape(t *testing.T) {
	r := NewUIRendererWithStyle(zap.NewNop(), UIStyleFull)
	out := captureEnvStdout(t, func() {
		r.RenderResponseEnvelope(ResponseEnvelopeOptions{
			HeaderLeft:  " 💬 RESPOSTA ",
			HeaderRight: " 1.4s · 312↑ 1.8k↓ ",
			FooterRight: "$0.004 · ctx 12%",
			Body:        "Hello, world.",
			Color:       ColorGray,
			Width:       80,
		})
	})
	plain := stripANSIEnv(out)

	assert.Contains(t, plain, "RESPOSTA", "header left label surfaces")
	assert.Contains(t, plain, "1.4s", "header right label surfaces")
	assert.Contains(t, plain, "──", "titled rule opens the reply")
	assert.Contains(t, plain, "\n  Hello, world.", "body sits on the two-space indent")
	assert.Contains(t, plain, "  $0.004 · ctx 12%", "footer telemetry closes the reply")
	for _, glyph := range []string{"╭", "╮", "╰", "╯", "│"} {
		assert.NotContains(t, plain, glyph, "sóbrio treatment draws no box glyphs")
	}
}

// TestRenderResponseEnvelope_RuleSpansRequestedWidth verifies the header
// rule measures exactly the pinned width at several sizes — the successor
// of the closed-box alignment contract.
func TestRenderResponseEnvelope_RuleSpansRequestedWidth(t *testing.T) {
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
		rule := firstRuleRow(plain)
		if rule == "" {
			t.Fatalf("width=%d: no rule row found", w)
		}
		assert.Equalf(t, w, lipgloss.Width(rule), "width=%d: rule must span the pinned width", w)
	}
}

// TestRenderResponseEnvelope_EmojiHeavyContent feeds the reply the same
// emoji-heavy body from the historical overflow bug: no row (rule or body)
// may exceed the pinned width.
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
	for _, row := range strings.Split(stripANSIEnv(out), "\n") {
		assert.LessOrEqualf(t, lipgloss.Width(row), 90,
			"emoji-heavy row must stay inside the pinned width: %q", row)
	}
}

// TestRenderResponseEnvelope_LongLineWraps proves long single-line bodies
// wrap inside the pinned width on the indent.
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
	for _, row := range strings.Split(stripANSIEnv(out), "\n") {
		assert.LessOrEqualf(t, lipgloss.Width(row), 60,
			"long body row must wrap inside the width: %q", row)
	}
}

// TestRenderResponseEnvelope_ShortBodyLongHeader: labels wider than the
// body must be absorbed by the rule without overflowing the pinned width.
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
	rule := firstRuleRow(plain)
	assert.Equal(t, 192, lipgloss.Width(rule), "rule spans the pinned width with long labels")
	assert.Contains(t, plain, "Claude sonnet 4.6")
	assert.Contains(t, plain, "Como posso ajudar?")
}

// TestRenderResponseEnvelope_NoLabels covers the minimal-call shape: a
// bare rule and the indented body.
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
	rule := firstRuleRow(plain)
	assert.Equal(t, 50, lipgloss.Width(rule), "label-less rule still spans the width")
	assert.Contains(t, plain, "  Plain body, no labels.")
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

// firstRuleRow returns the first row that is a horizontal rule (starts
// with the dash glyph) — the sóbrio reply header.
func firstRuleRow(s string) string {
	for _, row := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimLeft(row, " "), "──") {
			return row
		}
	}
	return ""
}
