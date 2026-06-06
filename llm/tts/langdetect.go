/*
 * ChatCLI - Lightweight pt/en language routing for voice replies.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The embedded Kokoro model phonemizes through espeak-ng guided by the voice's
 * language, so Portuguese text spoken by an English voice comes out mangled.
 * detectLang scores the reply with a small stopword and diacritic heuristic —
 * deterministic, dependency-free, and cheap — so mixed conversations route to
 * the right voice per reply. Two-way pt/en only by design: those are the
 * languages the assistant answers in; everything else defaults to English.
 */
package tts

import (
	"strings"
	"unicode"
)

// Diacritics that are strong Portuguese signals in pt-vs-en discrimination.
// ã, õ and ç are near-exclusive to Portuguese; the acute/circumflex vowels
// never appear in English prose.
const ptDiacritics = "ãõçáéíóúâêôàü"

// Frequent words that identify each language. Kept short on purpose: replies
// are full sentences, so a handful of function words dominates quickly.
var (
	ptStopwords = map[string]struct{}{
		"que": {}, "não": {}, "nao": {}, "uma": {}, "com": {}, "para": {},
		"você": {}, "voce": {}, "isso": {}, "está": {}, "esta": {}, "são": {},
		"como": {}, "mais": {}, "foi": {}, "ele": {}, "ela": {}, "seu": {},
		"sua": {}, "dos": {}, "das": {}, "pelo": {}, "pela": {}, "também": {},
		"tudo": {}, "bem": {}, "sim": {}, "já": {}, "ser": {}, "fazer": {},
	}
	enStopwords = map[string]struct{}{
		"the": {}, "and": {}, "is": {}, "are": {}, "you": {}, "this": {},
		"that": {}, "with": {}, "for": {}, "have": {}, "was": {}, "will": {},
		"can": {}, "your": {}, "from": {}, "not": {}, "all": {}, "what": {},
		"here": {}, "now": {}, "done": {}, "they": {}, "been": {}, "would": {},
	}
)

// detectLang classifies text as "pt" or "en". Ties and empty input resolve to
// English, matching the assistant's default voice.
func detectLang(text string) string {
	ptScore, enScore := 0, 0
	for _, r := range text {
		if strings.ContainsRune(ptDiacritics, unicode.ToLower(r)) {
			ptScore += 2
		}
	}
	for _, w := range strings.Fields(strings.ToLower(text)) {
		w = strings.Trim(w, ".,;:!?\"'()[]{}")
		if _, ok := ptStopwords[w]; ok {
			ptScore++
		}
		if _, ok := enStopwords[w]; ok {
			enScore++
		}
	}
	if ptScore > enScore {
		return "pt"
	}
	return "en"
}

// pickVoice routes text to the Portuguese or English voice by detected
// language. An empty text falls back to the English voice.
func pickVoice(text, enVoice, ptVoice string) string {
	if detectLang(text) == "pt" {
		return ptVoice
	}
	return enVoice
}
