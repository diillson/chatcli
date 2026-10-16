/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import "testing"

func TestRecallCaps(t *testing.T) {
	p := NewBuiltinRecallPlugin()
	args := []string{`{"key":"aaaaaaaaaaaaaaaa"}`}
	if !p.IsReadOnly(args) || !p.IsConcurrencySafe(args) {
		t.Fatal("@recall only reads back compressed content and must not need confirmation")
	}
	var _ ReadOnlyAware = p
	var _ ConcurrencySafeAware = p
}

func TestMemoryCaps(t *testing.T) {
	p := &BuiltinMemoryPlugin{}
	cases := map[string]bool{
		`{"cmd":"recall","args":{"query":"deploy"}}`:            true,
		`{"cmd":"search","args":{"query":"deploy"}}`:            true,
		`{"cmd":"timeline","args":{}}`:                          true,
		`{"cmd":"map"}`:                                         true,
		`{"cmd":"neighbors","args":{"query":"karpenter"}}`:      true,
		`{"cmd":"remember","args":{"content":"prefers Go"}}`:    false,
		`{"cmd":"profile","args":{"key":"role","value":"sre"}}`: false,
		`{"cmd":"forget","args":{"match":"x"}}`:                 false,
		`{"cmd":"explode"}`:                                     false,
		`not json`:                                              false,
	}
	for in, want := range cases {
		args := []string{in}
		if got := p.IsReadOnly(args); got != want {
			t.Errorf("IsReadOnly(%s) = %v, want %v", in, got, want)
		}
		if p.IsConcurrencySafe(args) != p.IsReadOnly(args) {
			t.Errorf("IsConcurrencySafe(%s) should mirror IsReadOnly", in)
		}
	}
	if !p.IsReadOnly([]string{"recall", "--query", "deploy"}) || p.IsReadOnly([]string{"forget", "--match", "x"}) {
		t.Fatal("the argv form should be classified the same way")
	}
}
