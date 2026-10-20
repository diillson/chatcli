/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Shared term normalization: diacritic folding and a light, language-neutral
 * suffix stemmer.
 *
 * "configuração" and "configuracao" are the same word to a pt-BR user and
 * two different strings to a byte-exact comparison; "jogos" and "jogo",
 * "deploys" and "deploy" likewise. Every place that compares user-typed
 * words against stored words (the lexical index, profile de-duplication,
 * skill triggers) needs the same answer to "is this the same word", so the
 * rules live here once.
 */
package textfold

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Fold removes combining marks (NFD → drop Mn → NFC), so "ação" → "acao",
// "café" → "cafe", "naïve" → "naive". Case is left untouched; pure ASCII
// input is returned as is without allocating.
func Fold(s string) string {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(b.String())
}

// stemSuffixes are stripped in order (longest first per language family):
// pt plurals/verb forms/adverbs, en plurals/participles/gerunds. They assume
// a folded, lowercased token.
var stemSuffixes = []struct {
	suffix, replace string
	minLen          int
}{
	{"coes", "cao", 6}, // configuracoes → configuracao (after folding)
	{"mente", "", 8},   // rapidamente → rapida
	{"ing", "", 6},     // deploying → deploy
	{"ers", "er", 6},   // handlers → handler
	{"ies", "y", 6},    // queries → query
	{"eis", "el", 6},   // papeis → papel
	{"ais", "al", 6},   // locais → local
	{"ores", "or", 7},  // servidores → servidor
	{"ando", "ar", 7},  // configurando → configurar
	{"endo", "er", 7},  // escrevendo → escrever
	{"indo", "ir", 7},  // definindo → definir
	{"ed", "", 5},      // deployed → deploy
	{"es", "", 6},      // processes → process
	{"s", "", 5},       // deploys → deploy, jogos → jogo
}

// Stem strips one common suffix from a folded, lowercased token; too-short
// tokens are left alone so identifiers and acronyms keep their exact form.
func Stem(t string) string {
	for _, r := range stemSuffixes {
		if len(t) >= r.minLen && strings.HasSuffix(t, r.suffix) {
			return t[:len(t)-len(r.suffix)] + r.replace
		}
	}
	return t
}

// Cut returns the longest prefix of s that is at most maxBytes long and
// ends on a rune boundary, so a cap applied to text for display, a log
// line or a model prompt never leaves half of a multi-byte character
// behind (invalid UTF-8 breaks JSON encoders, terminals and clients that
// parse the output). maxBytes <= 0 yields "".
func Cut(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// Tail returns the longest suffix of s that is at most maxBytes long and
// starts on a rune boundary — the counterpart of Cut for keeping the end
// of a text. maxBytes <= 0 yields "".
func Tail(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

// IndexIgnoreCase finds the first case-insensitive occurrence of substr in
// s and returns its byte span in s itself: start and end are offsets into
// s, both on rune boundaries, and s[start:end] is the matched text. It
// returns -1, -1 when there is no match.
//
// Searching a strings.ToLower copy and applying the offset to the original
// is wrong whenever case mapping changes a character's byte length (İ, the
// Kelvin sign, ẞ): the offset drifts and can land inside a character. An
// empty substr matches at 0.
func IndexIgnoreCase(s, substr string) (start, end int) {
	if substr == "" {
		return 0, 0
	}
	if isASCII(substr) {
		n := len(substr)
		for i := 0; i+n <= len(s); i++ {
			if asciiEqualFold(s[i:i+n], substr) {
				return i, i + n
			}
		}
		return -1, -1
	}
	for i := 0; i < len(s); {
		if j, ok := matchFoldAt(s, i, substr); ok {
			return i, j
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return -1, -1
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// asciiEqualFold compares two equal-length byte strings, folding only ASCII
// letters; a non-ASCII byte matches only itself, so a match never starts or
// ends inside a multi-byte character when one side is ASCII.
func asciiEqualFold(a, b string) bool {
	for i := 0; i < len(a); i++ {
		x, y := a[i], b[i]
		if 'A' <= x && x <= 'Z' {
			x += 'a' - 'A'
		}
		if 'A' <= y && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// matchFoldAt reports whether substr matches s at offset i rune by rune
// under simple case folding, and where the match ends in s.
func matchFoldAt(s string, i int, substr string) (int, bool) {
	j := i
	for _, want := range substr {
		if j >= len(s) {
			return 0, false
		}
		got, size := utf8.DecodeRuneInString(s[j:])
		if !runeEqualFold(got, want) {
			return 0, false
		}
		j += size
	}
	return j, true
}

func runeEqualFold(a, b rune) bool {
	if a == b {
		return true
	}
	for f := unicode.SimpleFold(a); f != a; f = unicode.SimpleFold(f) {
		if f == b {
			return true
		}
	}
	return false
}
