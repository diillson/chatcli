/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
/*
 * ChatCLI - session_search_normalize.go
 *
 * Text normalization for saved-session search. Recall queries are natural
 * language ("o que discutimos sobre o spinner?"), while the session-level
 * AND filter in SearchSessions is a literal substring match — without
 * normalization, accents ("autenticação" vs "autenticacao") and
 * recall-framing words ("discutimos", "what did we decide") disqualify
 * exactly the sessions the user is asking about.
 *
 * Accent folding is a hand-rolled Latin table instead of x/text: the store
 * only sees PT/EN prose, and stdlib-only avoids a new direct dependency.
 */
package cli

import (
	"strings"
	"unicode"
)

// normalizeForSearch lowercases and accent-folds a string so query terms and
// session text compare accent-insensitively.
func normalizeForSearch(s string) string {
	return strings.Map(foldAccentRune, strings.ToLower(s))
}

// foldAccentRune maps one accented Latin rune to its ASCII base; anything
// else passes through unchanged. Input is expected to be lowercased already.
func foldAccentRune(r rune) rune {
	switch r {
	case 'á', 'à', 'â', 'ã', 'ä', 'å':
		return 'a'
	case 'é', 'è', 'ê', 'ë':
		return 'e'
	case 'í', 'ì', 'î', 'ï':
		return 'i'
	case 'ó', 'ò', 'ô', 'õ', 'ö':
		return 'o'
	case 'ú', 'ù', 'û', 'ü':
		return 'u'
	case 'ç':
		return 'c'
	case 'ñ':
		return 'n'
	case 'ý', 'ÿ':
		return 'y'
	}
	return r
}

// sessionSearchStopwords are terms carrying no recall signal: PT/EN
// grammatical stopwords plus the recall-framing verbs users write when
// asking about a past conversation ("o que DISCUTIMOS…", "what did we
// DECIDE…") — those verbs describe the act of recalling and almost never
// appear in the recorded conversation itself. Keys are normalized
// (lowercase, accent-folded).
var sessionSearchStopwords = map[string]struct{}{
	// PT grammatical
	"a": {}, "o": {}, "as": {}, "os": {}, "um": {}, "uma": {}, "uns": {}, "umas": {},
	"de": {}, "do": {}, "da": {}, "dos": {}, "das": {}, "em": {}, "no": {}, "na": {},
	"nos": {}, "nas": {}, "num": {}, "numa": {}, "ao": {}, "aos": {}, "pelo": {}, "pela": {},
	"que": {}, "e": {}, "ou": {}, "mas": {}, "porem": {}, "porque": {}, "pois": {},
	"para": {}, "pra": {}, "por": {}, "com": {}, "sem": {}, "se": {}, "sobre": {}, "entre": {},
	"eu": {}, "tu": {}, "ele": {}, "ela": {}, "eles": {}, "elas": {}, "voce": {}, "voces": {},
	"gente": {}, "me": {}, "te": {}, "lhe": {}, "meu": {}, "minha": {}, "seu": {}, "sua": {},
	"nosso": {}, "nossa": {}, "este": {}, "esta": {}, "esse": {}, "essa": {}, "isso": {},
	"isto": {}, "aquilo": {}, "aquele": {}, "aquela": {},
	"eh": {}, "foi": {}, "ser": {}, "era": {}, "sao": {}, "estava": {},
	"estamos": {}, "tem": {}, "tinha": {}, "ter": {}, "temos": {}, "ha": {}, "havia": {},
	"vai": {}, "vamos": {}, "como": {}, "mais": {}, "menos": {}, "muito": {}, "bem": {},
	"ja": {}, "tambem": {}, "ainda": {}, "so": {}, "la": {}, "aqui": {}, "ali": {},
	"qual": {}, "quais": {}, "quando": {}, "onde": {}, "quem": {}, "entao": {}, "depois": {},
	"antes": {}, "agora": {}, "hoje": {}, "ontem": {}, "amanha": {}, "coisa": {}, "coisas": {},
	"tipo": {}, "tals": {}, "tal": {}, "etc": {},
	// PT recall-framing verbs
	"discutimos": {}, "falamos": {}, "conversamos": {}, "decidimos": {}, "fizemos": {},
	"fez": {}, "faz": {}, "fazer": {}, "fizeram": {}, "falou": {}, "disse": {},
	"vimos": {}, "combinamos": {}, "resolvemos": {}, "trocamos": {}, "papo": {},
	"lembra": {}, "lembrar": {}, "sessao": {}, "sessoes": {}, "conversa": {}, "conversas": {},
	// EN grammatical
	"the": {}, "an": {}, "of": {}, "in": {}, "on": {}, "at": {}, "to": {}, "for": {},
	"with": {}, "without": {}, "and": {}, "or": {}, "but": {}, "that": {}, "this": {},
	"these": {}, "those": {}, "is": {}, "are": {}, "was": {}, "were": {}, "be": {},
	"been": {}, "being": {}, "it": {}, "its": {}, "we": {}, "you": {}, "i": {}, "they": {},
	"he": {}, "she": {}, "them": {}, "us": {}, "our": {}, "my": {}, "your": {}, "their": {},
	"what": {}, "which": {}, "when": {}, "where": {}, "who": {}, "how": {}, "why": {},
	"did": {}, "does": {}, "done": {}, "about": {}, "then": {}, "than": {},
	"too": {}, "very": {}, "just": {}, "also": {}, "there": {}, "here": {},
	"had": {}, "have": {}, "has": {}, "will": {}, "would": {}, "can": {}, "could": {},
	"should": {}, "may": {}, "might": {}, "not": {}, "yes": {}, "yesterday": {},
	"today": {}, "earlier": {}, "before": {}, "after": {}, "again": {}, "thing": {}, "things": {},
	// EN recall-framing verbs
	"discussed": {}, "talked": {}, "decided": {}, "mentioned": {}, "said": {},
	"remember": {}, "recall": {}, "session": {}, "sessions": {}, "conversation": {},
	"conversations": {}, "chat": {}, "left": {}, "off": {},
}

// significantSearchTerms filters normalized query terms down to the ones
// worth AND-filtering on: punctuation is trimmed off term edges ("coder?" →
// "coder") and stopwords drop out. Returns nil when every term is a
// stopword — the caller then lists recent sessions instead of filtering.
func significantSearchTerms(terms []string) []string {
	sig := make([]string, 0, len(terms))
	for _, t := range terms {
		t = strings.TrimFunc(t, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len(t) < 2 {
			continue
		}
		if _, stop := sessionSearchStopwords[t]; stop {
			continue
		}
		sig = append(sig, t)
	}
	if len(sig) == 0 {
		return nil
	}
	return sig
}
