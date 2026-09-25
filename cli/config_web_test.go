/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2026 Edilson Freitas
 * License: MIT
 */

package cli

import (
	"strings"
	"testing"
)

func TestShowConfigWeb_IdleAndRunning(t *testing.T) {
	c := &ChatCLI{}
	out := captureStdout(t, func() { c.routeConfigWeb(nil) })
	if !strings.Contains(out, "web.log") || strings.Contains(out, "http://") {
		t.Fatalf("idle panorama = %q", out)
	}
	c.web.proc = &fakeWebProcess{}
	c.web.url = "http://127.0.0.1:1/?t=x"
	c.web.bound = "web-1"
	out = captureStdout(t, func() { c.showConfigWeb() })
	for _, want := range []string{"http://127.0.0.1:1/?t=x", "4242", "web-1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("running panorama lacks %q: %q", want, out)
		}
	}
}
