/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import (
	"sort"
	"strings"
	"testing"
)

func tokenSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, t := range tokenizeLexical(s) {
		m[t] = true
	}
	return m
}

func TestTokenizeLexical_SnakeAndKebab(t *testing.T) {
	got := tokenSet("aws_eks_cluster argo-rollout")
	for _, want := range []string{"aws", "eks", "cluster", "argo", "rollout"} {
		if !got[want] {
			t.Errorf("snake/kebab split missing %q in %v", want, sortedKeys(got))
		}
	}
}

func TestTokenizeLexical_CamelAndPascal(t *testing.T) {
	got := tokenSet("getUserName HTTPServer")
	// sub-words
	for _, want := range []string{"get", "user", "name", "http", "server"} {
		if !got[want] {
			t.Errorf("camel split missing sub-word %q in %v", want, sortedKeys(got))
		}
	}
	// the whole identifier is still indexed for exact-match recall
	for _, want := range []string{"getusername", "httpserver"} {
		if !got[want] {
			t.Errorf("camel split dropped the full token %q in %v", want, sortedKeys(got))
		}
	}
}

func TestTokenizeLexical_PreservesAlphanumericTokens(t *testing.T) {
	// case-only splitting must not break identifiers like s3, oauth2, ipv4.
	got := tokenSet("s3 oauth2 ipv4")
	for _, want := range []string{"s3", "oauth2", "ipv4"} {
		if !got[want] {
			t.Errorf("alphanumeric token %q was split apart; got %v", want, sortedKeys(got))
		}
	}
}

func TestTokenizeLexical_DropsSingleRunes(t *testing.T) {
	for _, tok := range tokenizeLexical("a b cc d") {
		if len(tok) < 2 {
			t.Errorf("one-rune token %q leaked through", tok)
		}
	}
}

// TestTokenizeLexical_CamelRecallEndToEnd proves the practical win: a corpus
// that mentions getUserName is findable by the sub-word "username"-style query
// "user name", and a Terraform identifier by "eks".
func TestTokenizeLexical_CamelRecallEndToEnd(t *testing.T) {
	segs := []Segment{
		{Content: "func getUserName(id int) string { return store.Lookup(id) }"},
		{Content: "resource aws_eks_cluster main { name = prod }"},
		{Content: "the quick brown fox jumps over the lazy dog"},
	}
	lex := newLexicalIndex(segs)

	if hits := lex.search("user", 3); len(hits) == 0 || hits[0].idx != 0 {
		t.Errorf("camelCase sub-word query 'user' should rank the getUserName segment first, got %v", hits)
	}
	if hits := lex.search("eks", 3); len(hits) == 0 || hits[0].idx != 1 {
		t.Errorf("snake_case sub-word query 'eks' should rank the aws_eks_cluster segment first, got %v", hits)
	}
}

func TestSplitCamelCase(t *testing.T) {
	cases := map[string][]string{
		"getUserName": {"get", "User", "Name"},
		"HTTPServer":  {"HTTP", "Server"},
		"lowercase":   {"lowercase"},
		"ALLCAPS":     {"ALLCAPS"},
		"A":           {"A"},
	}
	for in, want := range cases {
		got := splitCamelCase(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("splitCamelCase(%q) = %v, want %v", in, got, want)
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
