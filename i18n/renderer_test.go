/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package i18n

import (
	"strings"
	"testing"
)

// The renderer has the last word on what T returns, and removing it puts
// T back exactly as it was.
func TestSetRenderer(t *testing.T) {
	t.Cleanup(func() { SetRenderer(nil) })
	const key = "no.such.key.for.the.renderer.test"
	plain := T(key)
	SetRenderer(strings.ToUpper)
	if got := T(key); got != strings.ToUpper(plain) {
		t.Fatalf("with a renderer T returned %q, want %q", got, strings.ToUpper(plain))
	}
	if got := T(key, 7); got != strings.ToUpper(T2(key, 7)) {
		t.Fatalf("with arguments T returned %q", got)
	}
	SetRenderer(nil)
	if got := T(key); got != plain {
		t.Fatalf("after removing the renderer T returned %q, want %q", got, plain)
	}
}

// T2 is T without the renderer, for comparison.
func T2(key string, args ...interface{}) string { return translate(key, args...) }
