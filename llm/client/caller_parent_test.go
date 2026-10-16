/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package client

import (
	"context"
	"testing"

	"go.uber.org/zap"
)

type ctxKeyTest string

func TestCallerFieldKeepsTheCallerAndCarriesTheParent(t *testing.T) {
	t.Cleanup(func() { SetCallerResolver(nil); SetParentResolver(nil) })
	SetCallerResolver(func(ctx context.Context) string { s, _ := ctx.Value(ctxKeyTest("run")).(string); return s })
	SetParentResolver(func(ctx context.Context) string { s, _ := ctx.Value(ctxKeyTest("parent")).(string); return s })

	fields := func(ctx context.Context) map[string]string { return fieldsToStrings([]zap.Field{CallerField(ctx)}) }
	run := context.WithValue(context.Background(), ctxKeyTest("run"), "run-1")

	if got := fields(run); got[CallerFieldKey] != "run-1" || got[ParentFieldKey] != "" {
		t.Fatalf("run only: %v", got)
	}
	same := context.WithValue(run, ctxKeyTest("parent"), "run-1")
	if got := fields(same); got[CallerFieldKey] != "run-1" || got[ParentFieldKey] != "" {
		t.Fatalf("a parent equal to the caller adds nothing: %v", got)
	}
	tool := context.WithValue(run, ctxKeyTest("parent"), "tool-4")
	if got := fields(tool); got[CallerFieldKey] != "run-1" || got[ParentFieldKey] != "tool-4" {
		t.Fatalf("the caller stays the run, the node travels apart: %v", got)
	}
	turn := context.WithValue(context.Background(), ctxKeyTest("parent"), "turn-2")
	if got := fields(turn); got[CallerFieldKey] != "" || got[ParentFieldKey] != "turn-2" {
		t.Fatalf("no run: no caller, only the node: %v", got)
	}
	if got := fields(context.Background()); len(got) != 0 {
		t.Fatalf("nothing to say: %v", got)
	}
	var none context.Context
	if CallerField(none).Type != zap.Skip().Type {
		t.Fatal("a nil ctx is skipped")
	}
}
