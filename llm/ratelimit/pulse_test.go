/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package ratelimit

import (
	"net/http"
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
)

func TestRecordReportsHeadroom(t *testing.T) {
	rec := pulsetest.Record(t)
	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "100")
	h.Set("x-ratelimit-remaining-requests", "40")
	h.Set("x-ratelimit-limit-tokens", "1000")
	h.Set("x-ratelimit-remaining-tokens", "50")
	Record("OPENAI", h)
	ev := rec.WaitFor(t, "headroom point", pulsetest.Is(pulse.KindBackground, "ratelimit:openai", pulse.PhasePoint))
	if ev.Attrs["req_remaining"] != "40" || ev.Attrs["req_limit"] != "100" || ev.Attrs["tok_remaining"] != "50" ||
		ev.Attrs["usage_pct"] != "95" || ev.Status != pulse.StatusError {
		t.Fatalf("a bucket 95%% spent should read as an alarm: %+v", ev)
	}

	h2 := http.Header{}
	h2.Set("x-ratelimit-limit-requests", "100")
	h2.Set("x-ratelimit-remaining-requests", "90")
	Record("XAI", h2)
	ok := rec.WaitFor(t, "healthy headroom", pulsetest.Is(pulse.KindBackground, "ratelimit:xai", pulse.PhasePoint))
	if ok.Status != pulse.StatusOK || ok.Attrs["usage_pct"] != "10" {
		t.Fatalf("headroom = %+v", ok)
	}
}
