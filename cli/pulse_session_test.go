/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
	"github.com/diillson/chatcli/pkg/pulse"
	"github.com/diillson/chatcli/pkg/pulse/pulsetest"
	"go.uber.org/zap"
)

// Every save and load of the session store is a span with counts and the
// at-rest state; the session name and its contents never reach the bus.
func TestSessionSaveAndLoadShowOnTheDashboard(t *testing.T) {
	for _, key := range []string{"", "pulse-test-key"} {
		t.Run("key="+key, func(t *testing.T) {
			t.Setenv("CHATCLI_ENCRYPTION_KEY", key)
			rec := pulsetest.Record(t)
			sm, err := NewSessionManagerAt(t.TempDir(), zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			sd := &SessionData{
				ChatHistory:  []models.Message{{Role: "user", Content: "SECRET-Q"}, {Role: "assistant", Content: "SECRET-A"}},
				CoderHistory: []models.Message{{Role: "user", Content: "SECRET-C"}},
			}
			if err := sm.SaveSessionV2("secretname", sd); err != nil {
				t.Fatal(err)
			}
			if _, err := sm.LoadSessionV2("secretname"); err != nil {
				t.Fatal(err)
			}
			if _, err := sm.LoadSessionV2("missing"); err == nil {
				t.Fatal("a missing session must fail")
			}

			wantEnc := "false"
			if key != "" {
				wantEnc = "true"
			}
			save := rec.WaitFor(t, "save end", pulsetest.Is(pulse.KindBackground, "session:save", pulse.PhaseEnd))
			if save.Status != pulse.StatusOK || save.Attrs["messages"] != "3" || save.Attrs["bytes"] == "" || save.Attrs["encrypted"] != wantEnc {
				t.Fatalf("save = %+v", save)
			}
			loads := rec.Match(pulsetest.Is(pulse.KindBackground, "session:load", pulse.PhaseEnd))
			for len(loads) < 2 {
				rec.WaitFor(t, "two loads", func(pulse.Event) bool {
					return len(rec.Match(pulsetest.Is(pulse.KindBackground, "session:load", pulse.PhaseEnd))) >= 2
				})
				loads = rec.Match(pulsetest.Is(pulse.KindBackground, "session:load", pulse.PhaseEnd))
			}
			if l := loads[0]; l.Status != pulse.StatusOK || l.Attrs["messages"] != "3" || l.Attrs["bytes"] != save.Attrs["bytes"] || l.Attrs["encrypted"] != wantEnc {
				t.Fatalf("load = %+v", l)
			}
			if l := loads[1]; l.Status != pulse.StatusError || l.Attrs["bytes"] != "" {
				t.Fatalf("missing load = %+v", l)
			}
			wire, _ := json.Marshal(rec.Events())
			if strings.Contains(string(wire), "SECRET") || strings.Contains(string(wire), "secretname") {
				t.Fatalf("session name or content leaked: %s", wire)
			}
		})
	}
}
