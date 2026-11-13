/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"runtime"
	"testing"
)

// --pty is a bare flag: the word after it always begins the child command,
// even when that word is "true" or "false" — both are programs. A value is
// read only as --pty=true|false|1|0.
func TestProcStartPTYIsABareFlag(t *testing.T) {
	p, r := newProcRecorder(t)
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"start", "--pty", "true"}, "pty|true|"},
		{[]string{"start", "--pty", "false"}, "pty|false|"},
		{[]string{"start", "--pty", "true", "&&", "echo", "x"}, "pty|true && echo x|"},
		{[]string{"start", "--command", "htop", "--pty"}, "pty|htop|"},
		{[]string{"start", "--command", "ls", "--pty=0"}, "start|ls|"},
		{[]string{"start", "--command", "ls", "--pty=false"}, "start|ls|"},
		{[]string{"start", "--command", "ls", "--pty=1"}, "pty|ls|"},
		{[]string{"start", "--command", "ls", "--pty=true"}, "pty|ls|"},
		{[]string{"stdin", "--id", "p1", "--text", "q", "--no_enter=1"}, "stdin|p1|q|false"},
		{[]string{"stdin", "--id", "p1", "--text", "q", "--no_enter=0"}, "stdin|p1|q|true"},
	} {
		r.calls = nil
		if out, err := p.Execute(context.Background(), c.argv); err != nil || len(r.calls) != 1 || r.calls[0] != c.want {
			t.Errorf("%q: out=%q err=%v calls=%q, want %q", c.argv, out, err, r.calls, c.want)
		}
	}
	r.calls = nil
	for _, argv := range [][]string{
		{"start", "--command", "ls", "--pty=maybe"},
		{"start", "--command", "ls", "--pty="},
		{"stdin", "--id", "p1", "--text", "q", "--no_enter", "true"}, // a stray word, not a value
	} {
		if out, err := p.Execute(context.Background(), argv); err == nil {
			t.Errorf("%q must be an error, got %q", argv, out)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("a rejected call reached the adapter: %q", r.calls)
	}
}

// A positional command line arrives as separate arguments, and re-joining
// them with spaces lost the quoting: `start sh -c "echo hi; sleep 5"` ran
// `sh -c echo hi; sleep 5`. An argument that carries whitespace was ONE
// argument and is quoted for the shell again. The envelope form — the
// whole line as one value — is passed through untouched.
func TestProcStartPositionalKeepsArgumentBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX quoting")
	}
	p, r := newProcRecorder(t)
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"start", "sh", "-c", "echo hi; sleep 5"}, `start|sh -c 'echo hi; sleep 5'|`},
		{[]string{"start", "python3", "-c", "print('a b')"}, `start|python3 -c 'print('\''a b'\'')'|`},
		{[]string{"start", "grep", "", "file"}, `start|grep '' file|`},
		// Words without whitespace stay as typed: operators keep working.
		{[]string{"start", "npm", "run", "dev", "&&", "echo", "done"}, "start|npm run dev && echo done|"},
		// One positional word holding a whole line is that line.
		{[]string{"start", "npm run dev -- --port 3000"}, "start|npm run dev -- --port 3000|"},
		{[]string{"start", "--command", `sh -c "echo hi; sleep 5"`}, `start|sh -c "echo hi; sleep 5"|`},
		{[]string{`{"cmd":"start","args":{"command":"sh -c \"echo hi; sleep 5\""}}`}, `start|sh -c "echo hi; sleep 5"|`},
	} {
		r.calls = nil
		if out, err := p.Execute(context.Background(), c.argv); err != nil || len(r.calls) != 1 || r.calls[0] != c.want {
			t.Errorf("%q: out=%q err=%v calls=%q, want %q", c.argv, out, err, r.calls, c.want)
		}
	}
}
