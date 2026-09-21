/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package client

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type callerKey struct{}

func TestCallerFieldNamesTheCallerOnlyWhenThereIsOne(t *testing.T) {
	t.Cleanup(func() { SetCallerResolver(nil) })

	if f := CallerField(context.Background()); f.Type != zapcore.SkipType {
		t.Fatalf("no resolver installed: field = %+v, want a no-op", f)
	}

	SetCallerResolver(func(ctx context.Context) string {
		id, _ := ctx.Value(callerKey{}).(string)
		return id
	})
	if f := CallerField(context.Background()); f.Type != zapcore.SkipType {
		t.Fatalf("no caller in the context: field = %+v, want a no-op", f)
	}
	var nilCtx context.Context
	if f := CallerField(nilCtx); f.Type != zapcore.SkipType {
		t.Fatalf("nil context: field = %+v, want a no-op", f)
	}
	f := CallerField(context.WithValue(context.Background(), callerKey{}, "run-7"))
	if f.Key != CallerFieldKey || f.String != "run-7" {
		t.Fatalf("field = %+v", f)
	}

	SetCallerResolver(nil)
	if f := CallerField(context.WithValue(context.Background(), callerKey{}, "run-7")); f.Type != zapcore.SkipType {
		t.Fatalf("cleared resolver: field = %+v, want a no-op", f)
	}
}

// The caller travels to every sink of the request chokepoint, and a request
// with no caller adds nothing: the audit entry only grows when there is
// something to say.
func TestCallerReachesTheAuditSinkOnBothPhases(t *testing.T) {
	resetAuditors(t)
	t.Cleanup(func() { SetCallerResolver(nil) })
	SetCallerResolver(func(ctx context.Context) string {
		id, _ := ctx.Value(callerKey{}).(string)
		return id
	})
	var got []RequestAuditEvent
	RegisterRequestAuditorKeyed("t", func(ev RequestAuditEvent) { got = append(got, ev) })

	worker := context.WithValue(context.Background(), callerKey{}, "run-42")
	LogRequestStart(zap.NewNop(), "openai", "gpt-x", CallerField(worker), zap.Int("history_len", 2))
	LogRequestFinish(nil, "openai", "gpt-x", "success", time.Second, CallerField(worker))
	LogRequestStart(nil, "openai", "gpt-x", CallerField(context.Background()), zap.Int("history_len", 2))

	if len(got) != 3 {
		t.Fatalf("events = %d", len(got))
	}
	if got[0].Fields[CallerFieldKey] != "run-42" || got[1].Fields[CallerFieldKey] != "run-42" {
		t.Fatalf("caller missing: send=%v recv=%v", got[0].Fields, got[1].Fields)
	}
	if _, has := got[2].Fields[CallerFieldKey]; has || got[2].Fields["history_len"] != "2" {
		t.Fatalf("a request with no caller must not carry the field: %v", got[2].Fields)
	}
}
