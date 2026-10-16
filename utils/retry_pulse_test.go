/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package utils

import (
	"context"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

func TestRetryReportsEachRetryUnderItsCaller(t *testing.T) {
	rec := pulsetest.Record(t)
	ctx := pulse.WithParent(context.Background(), "turn-7")
	calls := 0
	_, err := Retry(ctx, zap.NewNop(), 3, time.Millisecond, func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", &APIError{StatusCode: 429, Message: "slow down"}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	ev := rec.WaitFor(t, "retry point", pulsetest.Is(pulse.KindPattern, "retry", pulse.PhasePoint))
	if ev.Parent != "turn-7" || ev.Attrs["attempt"] != "1/3" || ev.Attrs["cause"] != "429" || ev.Attrs["backoff_ms"] != "1" {
		t.Fatalf("retry = %+v", ev)
	}
	for _, e := range rec.Events() {
		for _, v := range e.Attrs {
			if v == "slow down" {
				t.Fatal("error text must never reach the bus")
			}
		}
	}
}

func TestPulseRetryNamesATimeout(t *testing.T) {
	rec := pulsetest.Record(t)
	pulseRetry(context.Background(), 2, 4, 10*time.Millisecond, context.DeadlineExceeded)
	ev := rec.WaitFor(t, "retry point", pulsetest.Is(pulse.KindPattern, "retry", pulse.PhasePoint))
	if ev.Attrs["cause"] != "timeout" || ev.Attrs["attempt"] != "2/4" {
		t.Fatalf("retry = %+v", ev)
	}
}
