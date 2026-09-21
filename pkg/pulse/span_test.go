/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSpanIsNilAndFreeWhileOff(t *testing.T) {
	b := New("t")
	s := b.Begin(KindTool, "@read", "run-1")
	if s != nil {
		t.Fatal("a disabled bus must hand out nil spans")
	}
	s.With("k", "v").End(StatusOK) // nil-safe chain
	s.EndErr(errors.New("x"))
	if st := b.Stats(); st.Published != 0 {
		t.Fatalf("published %d events while off", st.Published)
	}
}

func TestSpanEmitsStartAndASingleEnd(t *testing.T) {
	b := New("t")
	ch, cancel := b.Subscribe(8)
	defer cancel()
	b.SetEnabled(true)

	s := b.Begin(KindTool, "@read", "run-1")
	time.Sleep(3 * time.Millisecond)
	s.With("result_bytes", "120").With("", "skipped").End(StatusOK)
	s.End(StatusError) // a deferred safety net after the real End: ignored

	start, end := recv(t, ch), recv(t, ch)
	if start.Phase != PhaseStart || start.Status != StatusRunning || start.Name != "@read" || start.Parent != "run-1" {
		t.Fatalf("start = %+v", start)
	}
	if end.Phase != PhaseEnd || end.ID != start.ID || end.Status != StatusOK || end.DurMS < 1 {
		t.Fatalf("end = %+v", end)
	}
	if end.Attrs["result_bytes"] != "120" || len(end.Attrs) != 1 {
		t.Fatalf("end attrs = %+v", end.Attrs)
	}
	if len(start.Attrs) != 0 {
		t.Fatalf("attributes set after Begin must not leak onto the start event: %+v", start.Attrs)
	}
	select {
	case ev := <-ch:
		t.Fatalf("second End emitted %+v", ev)
	case <-time.After(40 * time.Millisecond):
	}

	other := b.Begin(KindTool, "@read", "")
	if other.ev.ID == start.ID {
		t.Fatal("span IDs must be unique")
	}
}

func TestStatusOf(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, StatusOK},
		{errors.New("boom"), StatusError},
		{context.Canceled, StatusCancelled},
		{fmt.Errorf("wrapped: %w", context.DeadlineExceeded), StatusCancelled},
	} {
		if got := StatusOf(tc.err); got != tc.want {
			t.Errorf("StatusOf(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestSpanEndErrNeverCarriesTheErrorText(t *testing.T) {
	b := New("t")
	ch, cancel := b.Subscribe(4)
	defer cancel()
	b.SetEnabled(true)
	b.Begin(KindMCP, "github", "").EndErr(errors.New("token ghp_SECRETVALUE rejected"))
	recv(t, ch)
	end := recv(t, ch)
	if end.Status != StatusError {
		t.Fatalf("status = %q", end.Status)
	}
	wire, err := json.Marshal(end)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "SECRETVALUE") {
		t.Fatalf("error text leaked: %s", wire)
	}
}

func TestPackageLevelBeginAndPointFollowTheDefaultBus(t *testing.T) {
	if s := Begin(KindTool, "@x", ""); s != nil {
		t.Fatal("process-wide bus is off in tests: Begin must return nil")
	}
	Point(KindSkill, "go-testing", "", StatusOK, nil) // off: no-op, no panic

	bus := Default()
	ch, cancel := bus.Subscribe(4)
	defer cancel()
	bus.SetEnabled(true)
	defer bus.SetEnabled(false)
	Point(KindSkill, "go-testing", "run-1", StatusOK, map[string]string{"source": "trigger"})
	for {
		ev := recv(t, ch)
		if ev.Kind != KindSkill {
			continue // snapshot of another registered source
		}
		if ev.Phase != PhasePoint || ev.Parent != "run-1" || ev.Attrs["source"] != "trigger" {
			t.Fatalf("point = %+v", ev)
		}
		return
	}
}
