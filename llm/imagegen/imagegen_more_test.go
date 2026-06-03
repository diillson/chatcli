/*
 * ChatCLI - Image generation abstraction tests.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package imagegen

import (
	"context"
	"errors"
	"testing"
)

func TestNullProvider(t *testing.T) {
	n := NewNull()
	if n.Name() != "null" {
		t.Fatalf("name = %q", n.Name())
	}
	if !IsNull(n) || !IsNull(nil) {
		t.Fatal("IsNull should be true for Null and nil")
	}
	_, err := n.Generate(context.Background(), "x", Options{})
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("expected ErrDisabled, got %v", err)
	}
}

func TestEmptyPromptErrors(t *testing.T) {
	oc, _ := NewOpenAICompatible("http://localhost:1", "", "m", "x", nil)
	if _, err := oc.Generate(context.Background(), "  ", Options{}); err == nil {
		t.Fatal("openai: empty prompt should error")
	}
	sd, _ := NewAutomatic1111("http://localhost:1", 10, nil)
	if _, err := sd.Generate(context.Background(), "", Options{}); err == nil {
		t.Fatal("sdwebui: empty prompt should error")
	}
	g, _ := NewGoogle("k", "", nil)
	if _, err := g.Generate(context.Background(), "", Options{}); err == nil {
		t.Fatal("google: empty prompt should error")
	}
}

func TestConstructorValidation(t *testing.T) {
	if _, err := NewOpenAICompatible("", "", "", "", nil); err == nil {
		t.Fatal("empty baseURL should error")
	}
	if _, err := NewOpenAICompatible("ftp://x", "", "", "", nil); err == nil {
		t.Fatal("non-http baseURL should error")
	}
	if _, err := NewGoogle("", "", nil); err == nil {
		t.Fatal("empty key should error")
	}
	if _, err := NewAutomatic1111("ftp://x", 0, nil); err == nil {
		t.Fatal("non-http baseURL should error")
	}
}
