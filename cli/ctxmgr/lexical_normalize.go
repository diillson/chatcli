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

	"github.com/diillson/chatcli/pkg/textfold"
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
		return textfold.Fold(t)
	case NormalizeStem:
		return textfold.Stem(textfold.Fold(t))
	}
	return t
}
