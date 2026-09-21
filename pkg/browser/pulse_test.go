/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package browser

import (
	"context"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
)

// The browser stays up across turns. The dashboard shows that it is up and
// how it runs — never a URL or anything on a page.
func TestBrowserSessionIsReportedFromOpenToClose(t *testing.T) {
	(&Session{attached: true}).pulseBegin() // dashboard off: no span, no panic

	bus := pulse.Default()
	ch, cancel := bus.Subscribe(32)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	for _, tc := range []struct {
		s    *Session
		mode string
	}{
		{&Session{attached: true}, "attached"},
		{&Session{attached: true, headless: true}, "attached"},
	} {
		tc.s.pulseBegin()
		tc.s.Close(context.Background()) // attached with no connection: detach is a no-op
		tc.s.Close(context.Background()) // closing twice reports once
		var got []pulse.Event
		deadline := time.After(3 * time.Second)
		for len(got) < 2 {
			select {
			case ev := <-ch:
				if ev.Name == "@browser" {
					got = append(got, ev)
				}
			case <-deadline:
				t.Fatalf("got %d of 2 events", len(got))
			}
		}
		if got[0].Phase != pulse.PhaseStart || got[1].Phase != pulse.PhaseEnd || got[1].Attrs["mode"] != tc.mode || got[1].Attrs["state"] != "closed" {
			t.Fatalf("events = %+v", got)
		}
	}

	for s, mode := range map[*Session]string{{headless: true}: "headless", {}: "visible"} {
		s.pulseBegin()
		s.pulse.End(pulse.StatusOK)
		for {
			ev := <-ch
			if ev.Name == "@browser" && ev.Phase == pulse.PhaseEnd {
				if ev.Attrs["mode"] != mode {
					t.Fatalf("mode = %q, want %q", ev.Attrs["mode"], mode)
				}
				break
			}
		}
	}
}
