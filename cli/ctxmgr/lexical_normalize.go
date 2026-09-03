/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Opt-in term normalization for the lexical (BM25) index.
 *
 * "configuração" and "configuracao" are the same word to a pt-BR user and
 * two different terms to a byte-exact tokenizer; "deploys" and "deploy"
 * likewise. CHATCLI_KNOWLEDGE_NORMALIZE=fold strips diacritics; =stem also
 * applies a light, language-neutral suffix stemmer (pt/en plurals,
 * verb and adverb endings). Off by default: ranking stays byte-exact unless
 * the operator opts in. The mode is part of the corpus fingerprint, so a
 * change rebuilds the index instead of mixing tokenizations.
 */
package ctxmgr

import (
	"os"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// KnowledgeNormalizeEnv selects the lexical normalization: off | fold | stem.
const KnowledgeNormalizeEnv = "CHATCLI_KNOWLEDGE_NORMALIZE"

// NormalizeMode is the parsed CHATCLI_KNOWLEDGE_NORMALIZE value.
type NormalizeMode string

const (
	NormalizeOff  NormalizeMode = "off"
	NormalizeFold NormalizeMode = "fold"
	NormalizeStem NormalizeMode = "stem"
)

// KnowledgeNormalizeMode reads the env (unknown values = off).
func KnowledgeNormalizeMode() NormalizeMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(KnowledgeNormalizeEnv))) {
	case "fold", "accents", "diacritics":
		return NormalizeFold
	case "stem", "stemming", "full":
		return NormalizeStem
	}
	return NormalizeOff
}

// normalizeTerm applies the mode to one lowercased token.
func normalizeTerm(t string, mode NormalizeMode) string {
	switch mode {
	case NormalizeFold:
		return foldDiacritics(t)
	case NormalizeStem:
		return lightStem(foldDiacritics(t))
	}
	return t
}

// foldDiacritics removes combining marks (NFD → drop Mn → NFC), so
// "ação" → "acao", "café" → "cafe", "naïve" → "naive".
func foldDiacritics(s string) string {
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
// pt plurals/verb forms/adverbs, en plurals/participles/gerunds.
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
	{"s", "", 5},       // deploys → deploy, tokens → token
}

// lightStem strips one common suffix; too-short tokens are left alone so
// identifiers and acronyms keep their exact form.
func lightStem(t string) string {
	for _, r := range stemSuffixes {
		if len(t) >= r.minLen && strings.HasSuffix(t, r.suffix) {
			return t[:len(t)-len(r.suffix)] + r.replace
		}
	}
	return t
}
