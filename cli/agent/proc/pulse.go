/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Live telemetry for supervised background processes. They outlive the turn
 * that started them — a dev server, a watcher, a REPL on a PTY — which makes
 * them exactly what a user loses track of. Each one is a span on the @proc
 * node, open for as long as the process lives.
 *
 * The command line is not emitted: it routinely carries tokens and
 * connection strings. Only the program name is, and only when the first word
 * is a plain program rather than a VAR=value assignment.
 */
package proc

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/diillson/chatcli/pkg/pulse"
)

// pulseProcNode is the dashboard node every supervised process reports on.
const pulseProcNode = "@proc"

// programName extracts a safe label from a command line.
func programName(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 || strings.Contains(fields[0], "=") {
		return ""
	}
	return filepath.Base(fields[0])
}

// pulseProcEvent builds the event of one process. Events are built by hand,
// keyed by the process id, rather than through a Span: a dashboard opened
// after the process started replays it from a snapshot, and its exit must
// still close that same node.
func pulseProcEvent(info Info, phase pulse.Phase) pulse.Event {
	ev := pulse.Event{
		Kind:   pulse.KindBackground,
		Phase:  phase,
		ID:     pulseProcID(info),
		Name:   pulseProcNode,
		Status: pulse.StatusRunning,
	}
	ev = ev.With("proc", info.ID).With("program", programName(info.Command))
	if info.Owner != "" {
		// Tagged so a reader scoped to one session can drop the others'.
		ev = ev.With(PulseOwnerAttr, info.Owner)
	}
	if info.PTY {
		ev = ev.With("pty", "true")
	}
	if phase != pulse.PhaseEnd {
		return ev
	}
	ev.Status = pulse.StatusOK
	if info.ExitCode != 0 {
		ev.Status = pulse.StatusError
	}
	if !info.Ended.IsZero() {
		ev = ev.Took(info.Ended.Sub(info.Started))
	}
	return ev.With("state", "exit "+strconv.Itoa(info.ExitCode))
}

// PulseOwnerAttr is the attribute a proc event of a named session carries
// with that session; the terminal's events have none.
const PulseOwnerAttr = "owner"

// PulseVisibleTo reports whether a reader scoped to owner may see ev. Only
// proc events are ever withheld: a session sees its own processes and no
// one else's, the terminal's included. The empty owner is the local
// operator, who sees the whole machine.
func PulseVisibleTo(owner string, ev pulse.Event) bool {
	if owner == "" || ev.Kind != pulse.KindBackground || !strings.HasPrefix(ev.ID, "proc:") {
		return true
	}
	return ev.Attrs[PulseOwnerAttr] == owner
}

// pulseProcID is the dashboard node id of a process. Ids repeat across
// owners, so a named owner's is qualified; the terminal's stay "proc:pN".
func pulseProcID(info Info) string {
	if info.Owner == "" {
		return "proc:" + info.ID
	}
	return "proc:" + info.Owner + "/" + info.ID
}

// pulseProc reports a process starting or exiting.
func pulseProc(info Info, phase pulse.Phase) {
	if pulse.Enabled() {
		pulse.Emit(pulseProcEvent(info, phase))
	}
}

// PulseSnapshot lists the processes running right now, so a dashboard opened
// mid-session shows the dev server started an hour ago.
func (s *Supervisor) PulseSnapshot() []pulse.Event {
	if s == nil {
		return nil
	}
	var out []pulse.Event
	for _, info := range s.List() {
		if info.State == StateRunning {
			out = append(out, pulseProcEvent(info, pulse.PhaseStart))
		}
	}
	return out
}
