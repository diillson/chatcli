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
