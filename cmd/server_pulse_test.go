/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cmd

import (
	"testing"

	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

// A server that hosts no ChatCLI records on its own, with a session node its
// gRPC calls hang from; one that hosts a ChatCLI leaves recording to it, so
// no event is spooled twice.
func TestStartServerPulseRecordsOnlyWithoutAHostedChatCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if stop := startServerPulse(true, zap.NewNop()); stop != nil {
		stop()
		t.Fatal("a hosted ChatCLI already records: no second recorder")
	}
	stop := startServerPulse(false, zap.NewNop())
	if stop == nil {
		t.Fatal("a bare server must record")
	}
	var session *pulse.Event
	for _, ev := range pulse.Default().Snapshot() {
		if ev.Kind == pulse.KindSession {
			found := ev
			session = &found
		}
	}
	stop()
	if session == nil || session.Name != "server" {
		t.Fatalf("session node = %+v, want one named server", session)
	}
	for _, ev := range pulse.Default().Snapshot() {
		if ev.Kind == pulse.KindSession {
			t.Fatal("stopping must drop the session node")
		}
	}
}
