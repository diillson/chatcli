/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzTokenizeLexical hammers the BM25 tokenizer with arbitrary input — it
// sits on the per-turn retrieval path over user-provided corpora. Invariants:
// never panics; every token is lowercase and at least 2 bytes.
func FuzzTokenizeLexical(f *testing.F) {
	f.Add("getUserName HTTPServer snake_case kebab-case s3 oauth2")
	f.Add("configuração de autenticação com criptografia homomórfica")
	f.Add("日本語テキスト mixed with ASCII and 数字123")
	f.Add(strings.Repeat("→", 500))
	f.Add("")
	f.Fuzz(func(t *testing.T, text string) {
		for _, tok := range tokenizeLexical(text) {
			if tok != strings.ToLower(tok) {
				t.Fatalf("token %q not lowercased", tok)
			}
			if len(tok) < 2 {
				t.Fatalf("one-byte token %q emitted (noise filter broken)", tok)
			}
		}
	})
}

// FuzzSnippetValidUTF8 pins the rune-boundary contract on the search snippet
// under arbitrary content: valid UTF-8 in ⇒ valid UTF-8 out, and the result
// never exceeds the byte budget plus the elision marker.
func FuzzSnippetValidUTF8(f *testing.F) {
	f.Add("plain ascii content that is fairly short")
	f.Add("a" + strings.Repeat("→", 400))
	f.Add(strings.Repeat("ção é açúcar ", 100))
	f.Fuzz(func(t *testing.T, s string) {
		out := snippet(s, searchSnippetChars)
		if utf8.ValidString(s) && !utf8.ValidString(out) {
			t.Fatal("valid UTF-8 input produced invalid UTF-8 snippet")
		}
		if len(out) > searchSnippetChars+len(" […]") {
			t.Fatalf("snippet exceeds budget: %d bytes", len(out))
		}
	})
}

// FuzzDigestLineValidUTF8 pins the same contract on the TOC line — this text
// enters the cached system-prompt prefix.
func FuzzDigestLineValidUTF8(f *testing.F) {
	f.Add("docs/config.md", "Configuração da integração de autenticação série avançada para produção")
	f.Add("a#b", strings.Repeat("ç", 200))
	f.Fuzz(func(t *testing.T, path, title string) {
		line := digestLine(digestSource{path: path, chunks: 1, title: title})
		if utf8.ValidString(path) && utf8.ValidString(title) && !utf8.ValidString(line) {
			t.Fatal("valid UTF-8 inputs produced an invalid UTF-8 digest line")
		}
	})
}
