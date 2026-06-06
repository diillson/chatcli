/*
 * ChatCLI - Markdown-to-speech sanitizer tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package tts

import (
	"strings"
	"testing"
)

func TestStripForSpeech(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain text untouched", "Tudo certo, senhor.", "Tudo certo, senhor."},
		{"bold and italic", "isso é **muito** _importante_ mesmo", "isso é muito importante mesmo"},
		{"heading", "## Status\nTudo operacional.", "Status\nTudo operacional."},
		{"link keeps label", "veja [a doc](https://example.com) agora", "veja a doc agora"},
		{"image dropped", "antes ![diagrama](img.png) depois", "antes depois"},
		{"inline code unwrapped", "rode `go build` no root", "rode go build no root"},
		{"fenced code dropped", "resultado:\n```go\nfmt.Println(1)\n```\nfim", "resultado:\n\nfim"},
		{"list markers", "- um\n- dois\n1. três\n2) quatro", "um\ndois\ntrês\nquatro"},
		{"blockquote", "> citação aqui", "citação aqui"},
		{"table flattened", "| a | b |\n|---|---|\n| 1 | 2 |", "a b\n\n1 2"},
		{"horizontal rule dropped", "antes\n---\ndepois", "antes\n\ndepois"},
		{"strikethrough", "isso ~~não~~ vale", "isso não vale"},
		{"html tags", "um <b>dois</b> três<br/>", "um dois três"},
		{"only code returns empty", "```\nx := 1\n```", ""},
		{"empty input", "   \n  ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripForSpeech(tt.in); got != tt.want {
				t.Errorf("StripForSpeech(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStripForSpeech_ClampsLongText(t *testing.T) {
	long := strings.Repeat("palavra ", 2000) // ~16k runes
	got := StripForSpeech(long)
	if n := len([]rune(got)); n > maxSpeechRunes {
		t.Fatalf("clamped length = %d runes, want <= %d", n, maxSpeechRunes)
	}
	if strings.HasSuffix(got, "palavr") {
		t.Fatal("clamp cut mid-word")
	}
}

func TestClampRunes_ShortStringUntouched(t *testing.T) {
	if got := clampRunes("curto", 10); got != "curto" {
		t.Fatalf("clampRunes = %q, want %q", got, "curto")
	}
}
