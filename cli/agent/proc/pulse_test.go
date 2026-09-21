/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package proc

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/pulse"
	"go.uber.org/zap"
)

// The command line routinely carries tokens and connection strings: only a
// plain program name may ever be shown.
func TestProgramNameNeverExposesArgumentsOrAssignments(t *testing.T) {
	for in, want := range map[string]string{
		"npm run dev -- --token=SECRET":       "npm",
		"/usr/local/bin/python3 server.py":    "python3",
		"API_KEY=SECRET node index.js":        "",
		"":                                    "",
		"   ":                                 "",
		"psql postgres://user:SECRET@host/db": "psql",
	} {
		if got := programName(in); got != want {
			t.Errorf("programName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProcEventShape(t *testing.T) {
	started := time.Now().Add(-2 * time.Second)
	info := Info{ID: "p3", Command: "npm run dev --token=SECRET", Dir: "/SECRET/dir", PTY: true, Started: started, State: StateRunning}

	start := pulseProcEvent(info, pulse.PhaseStart)
	if start.ID != "proc:p3" || start.Name != pulseProcNode || start.Kind != pulse.KindBackground || start.Status != pulse.StatusRunning {
		t.Fatalf("start = %+v", start)
	}
	if start.Attrs["program"] != "npm" || start.Attrs["pty"] != "true" || start.Attrs["proc"] != "p3" {
		t.Fatalf("start attrs = %+v", start.Attrs)
	}

	info.State, info.Ended, info.ExitCode = StateExited, started.Add(2*time.Second), 0
	if end := pulseProcEvent(info, pulse.PhaseEnd); end.Status != pulse.StatusOK || end.Attrs["state"] != "exit 0" || end.DurMS != 2000 {
		t.Fatalf("clean exit = %+v", end)
	}
	info.ExitCode = 137
	end := pulseProcEvent(info, pulse.PhaseEnd)
	if end.Status != pulse.StatusError || end.Attrs["state"] != "exit 137" {
		t.Fatalf("killed = %+v", end)
	}
	wire, _ := json.Marshal([]pulse.Event{start, end})
	if strings.Contains(string(wire), "SECRET") {
		t.Fatalf("command line or directory leaked: %s", wire)
	}
}

// A dashboard opened after a process started replays it from the snapshot;
// the exit must then close that same node, which is why events are keyed by
// the process id instead of going through a Span.
func TestSupervisorReportsStartSnapshotAndExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	var nilSup *Supervisor
	if nilSup.PulseSnapshot() != nil {
		t.Fatal("nil supervisor must snapshot as empty")
	}

	bus := pulse.Default()
	ch, cancel := bus.Subscribe(64)
	bus.SetEnabled(true)
	t.Cleanup(func() { bus.SetEnabled(false); cancel() })

	sup := NewSupervisor(func(string) error { return nil }, zap.NewNop())
	t.Cleanup(sup.CloseAll)
	info, err := sup.Start("sleep 0.4", "")
	if err != nil {
		t.Fatal(err)
	}
	if snap := sup.PulseSnapshot(); len(snap) != 1 || snap[0].ID != "proc:"+info.ID || snap[0].Phase != pulse.PhaseStart {
		t.Fatalf("snapshot while running = %+v", snap)
	}

	var evs []pulse.Event
	deadline := time.After(5 * time.Second)
	for len(evs) < 2 {
		select {
		case ev := <-ch:
			if ev.Name == pulseProcNode {
				evs = append(evs, ev)
			}
		case <-deadline:
			t.Fatalf("got %d of 2 events", len(evs))
		}
	}
	if evs[0].Phase != pulse.PhaseStart || evs[1].Phase != pulse.PhaseEnd || evs[0].ID != evs[1].ID {
		t.Fatalf("events = %+v", evs)
	}
	if evs[1].Attrs["state"] != "exit 0" || evs[1].Attrs["program"] != "sleep" {
		t.Fatalf("exit = %+v", evs[1])
	}
	if snap := sup.PulseSnapshot(); len(snap) != 0 {
		t.Fatalf("an exited process must leave the snapshot: %+v", snap)
	}
}
