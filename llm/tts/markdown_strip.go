/*
 * ChatCLI - Markdown-to-speech text sanitizer.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Assistant replies are markdown. Feeding them verbatim to a TTS backend makes
 * every engine read formatting out loud — "asterisk asterisk", pipes from
 * tables, raw URLs. StripForSpeech flattens markdown into plain prose before
 * synthesis so any provider (local command, self-hosted, cloud, embedded)
 * speaks naturally. It is intentionally lossy: code blocks are dropped — code
 * is for reading, not listening.
 */
package tts

import (
	"regexp"
	"strings"
)

// maxSpeechRunes caps the synthesized clip. Gateway replies are already
// clipped near 3500 runes; this guards direct callers such as @speak from
// producing multi-minute audio out of a pasted document.
const maxSpeechRunes = 4000

// Compiled once; order of application matters (block-level first, then inline).
var (
	reFencedCode  = regexp.MustCompile("(?s)```.*?```|~~~.*?~~~")
	reHTMLTag     = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	reImage       = regexp.MustCompile(`!\[[^\]]*]\([^)]*\)`)
	reLink        = regexp.MustCompile(`\[([^\]]+)]\([^)]*\)`)
	reInlineCode  = regexp.MustCompile("`([^`]*)`")
	reHeading     = regexp.MustCompile(`(?m)^[ \t]{0,3}#{1,6}[ \t]+`)
	reBlockquote  = regexp.MustCompile(`(?m)^[ \t]*>[ \t]?`)
	reListMarker  = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d{1,3}[.)])[ \t]+`)
	reTableRule   = regexp.MustCompile(`(?m)^[ \t]*\|?[ \t:|-]+\|[ \t:|-]*$`)
	reHorizRule   = regexp.MustCompile(`(?m)^[ \t]*(?:-{3,}|\*{3,}|_{3,})[ \t]*$`)
	reBoldItalic  = regexp.MustCompile(`(\*{1,3}|_{1,3})(\S(?:.*?\S)?)(\*{1,3}|_{1,3})`)
	reStrike      = regexp.MustCompile(`~~([^~]+)~~`)
	reBlankLines  = regexp.MustCompile(`\n{3,}`)
	reEdgeWS      = regexp.MustCompile(`(?m)^[ \t]+|[ \t]+$`)
	reInlineSpace = regexp.MustCompile(`[ \t]{2,}`)
)

// StripForSpeech flattens markdown text into plain prose suitable for speech
// synthesis: code blocks are removed, links collapse to their label, emphasis
// markers, headings, list bullets, table scaffolding and emoji are stripped,
// and the result is clamped to maxSpeechRunes. Returns "" when nothing
// speakable remains (for example a reply that was a single code block).
func StripForSpeech(s string) string {
	s = stripEmoji(s)
	s = reFencedCode.ReplaceAllString(s, " ")
	s = reImage.ReplaceAllString(s, " ")
	s = reLink.ReplaceAllString(s, "$1")
	s = reInlineCode.ReplaceAllString(s, "$1")
	s = reHTMLTag.ReplaceAllString(s, " ")
	s = reHeading.ReplaceAllString(s, "")
	s = reBlockquote.ReplaceAllString(s, "")
	s = reTableRule.ReplaceAllString(s, "")
	s = reHorizRule.ReplaceAllString(s, "")
	s = reListMarker.ReplaceAllString(s, "")
	s = reBoldItalic.ReplaceAllString(s, "$2")
	s = reStrike.ReplaceAllString(s, "$1")
	s = strings.ReplaceAll(s, "|", " ")
	s = reEdgeWS.ReplaceAllString(s, "")
	s = reInlineSpace.ReplaceAllString(s, " ")
	s = reBlankLines.ReplaceAllString(s, "\n\n")
	return clampRunes(strings.TrimSpace(s), maxSpeechRunes)
}

// stripEmoji drops pictographic runes — emoji, dingbats, arrows, geometric
// shapes — plus their joiners and variation selectors. TTS engines either
// skip them with an awkward pause or, worse, read their Unicode names out
// loud, which buries what the reply actually says.
func stripEmoji(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0x1F000 && r <= 0x1FAFF: // emoji, emoticons, flags, symbols
			return -1
		case r >= 0x2600 && r <= 0x27BF: // misc symbols + dingbats (✅⚠️✨)
			return -1
		case r >= 0x2B00 && r <= 0x2BFF: // arrows + stars (⭐)
			return -1
		case r >= 0x2190 && r <= 0x21FF: // arrows (→)
			return -1
		case r >= 0x2300 && r <= 0x23FF: // technical symbols and clocks
			return -1
		case r >= 0x25A0 && r <= 0x25FF: // geometric shapes (▶■)
			return -1
		case r >= 0xFE00 && r <= 0xFE0F: // variation selectors
			return -1
		case r == 0x200D || r == 0x20E3: // ZWJ, combining keycap
			return -1
		case r == 0x2049 || r == 0x203C: // ⁉ ‼
			return -1
		}
		return r
	}, s)
}

// clampRunes truncates s to at most n runes, cutting back to the last space so
// the clip never ends mid-word.
func clampRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	cut := string(runes[:n])
	if i := strings.LastIndexByte(cut, ' '); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut)
}
