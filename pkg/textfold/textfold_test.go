/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package textfold

import (
	"testing"
	"unicode/utf8"
)

func TestFold(t *testing.T) {
	cases := map[string]string{
		"ação":        "acao",
		"café":        "cafe",
		"naïve":       "naive",
		"Próximo":     "Proximo",
		"plain ascii": "plain ascii",
		"":            "",
	}
	for in, want := range cases {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStem(t *testing.T) {
	cases := map[string]string{
		"jogos":         "jogo",
		"deploys":       "deploy",
		"configuracoes": "configuracao",
		"servidores":    "servidor",
		"queries":       "query",
		"deployed":      "deploy",
		"deploying":     "deploy",
		"bus":           "bus", // too short to stem
		"api":           "api",
		"flamengo":      "flamengo",
	}
	for in, want := range cases {
		if got := Stem(in); got != want {
			t.Errorf("Stem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCut(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 3, "hel"},
		{"aé", 2, "a"}, // é is 2 bytes; byte 2 would split it
		{"aé", 3, "aé"},
		{"日本語", 4, "日"}, // 3-byte runes
		{"日本語", 6, "日本"},
		{"🙂x", 3, ""}, // a 4-byte rune never fits in 3
		{"x", 0, ""},
		{"x", -1, ""},
		{"", 5, ""},
	}
	for _, c := range cases {
		got := Cut(c.in, c.max)
		if got != c.want {
			t.Errorf("Cut(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
		if !utf8.ValidString(got) || len(got) > max(c.max, 0) {
			t.Errorf("Cut(%q, %d) = %q is invalid or over the cap", c.in, c.max, got)
		}
	}
}

func TestTail(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 3, "llo"},
		{"éa", 2, "a"}, // byte len-2 would start inside é
		{"éa", 3, "éa"},
		{"日本語", 4, "語"},
		{"x🙂", 3, ""},
		{"x", 0, ""},
	}
	for _, c := range cases {
		got := Tail(c.in, c.max)
		if got != c.want {
			t.Errorf("Tail(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
		if !utf8.ValidString(got) || len(got) > max(c.max, 0) {
			t.Errorf("Tail(%q, %d) = %q is invalid or over the cap", c.in, c.max, got)
		}
	}
}

func TestIndexIgnoreCase(t *testing.T) {
	cases := []struct {
		s, sub     string
		start, end int
	}{
		{"say <TOOL_CALL x>", "<tool", 4, 9},
		{"İİİ <tool>", "<tool", 7, 12}, // İ lowercases to 3 bytes; a ToLower copy would drift
		{"KK done", "k", 0, 1},         // ASCII needle never matches the Kelvin sign
		{"K done", "k", -1, -1},
		{"Foi HÁ 3 meses", "há 3", 4, 9}, // non-ASCII needle, span in the original
		{"ẞtraße", "ss", -1, -1},         // simple folding only; no expansion
		{"abc", "", 0, 0},
		{"abc", "x", -1, -1},
		{"", "x", -1, -1},
	}
	for _, c := range cases {
		start, end := IndexIgnoreCase(c.s, c.sub)
		if start != c.start || end != c.end {
			t.Errorf("IndexIgnoreCase(%q, %q) = %d,%d want %d,%d", c.s, c.sub, start, end, c.start, c.end)
			continue
		}
		if start >= 0 && (!utf8.ValidString(c.s[:start]) || !utf8.ValidString(c.s[start:end])) {
			t.Errorf("IndexIgnoreCase(%q, %q) split a character", c.s, c.sub)
		}
	}
}
