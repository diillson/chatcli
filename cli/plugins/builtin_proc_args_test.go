/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// procRecorder is a ProcAdapter that records the one call it received, as
// the verb followed by its arguments.
type procRecorder struct{ calls []string }

func (r *procRecorder) note(format string, a ...any) (string, error) {
	r.calls = append(r.calls, fmt.Sprintf(format, a...))
	return "ok", nil
}
func (r *procRecorder) Start(command, dir string) (string, error) {
	return r.note("start|%s|%s", command, dir)
}
func (r *procRecorder) StartPTY(command, dir string) (string, error) {
	return r.note("pty|%s|%s", command, dir)
}
func (r *procRecorder) Stdin(id, text string, enter bool) (string, error) {
	return r.note("stdin|%s|%s|%v", id, text, enter)
}
func (r *procRecorder) Status(id string) (string, error) { return r.note("status|%s", id) }
func (r *procRecorder) Logs(id string, tail int) (string, error) {
	return r.note("logs|%s|%d", id, tail)
}
func (r *procRecorder) LogsGrep(id, pattern string, tail int) (string, error) {
	return r.note("grep|%s|%s|%d", id, pattern, tail)
}
func (r *procRecorder) Wait(_ context.Context, id, pattern string, timeout int) (string, error) {
	return r.note("wait|%s|%s|%d", id, pattern, timeout)
}
func (r *procRecorder) Stop(id string) (string, error)   { return r.note("stop|%s", id) }
func (r *procRecorder) Remove(id string) (string, error) { return r.note("remove|%s", id) }
func (r *procRecorder) List() (string, error)            { return r.note("list") }

func newProcRecorder(t *testing.T) (*BuiltinProcPlugin, *procRecorder) {
	t.Helper()
	r := &procRecorder{}
	SetProcAdapter(r)
	t.Cleanup(func() { SetProcAdapter(nil) })
	return NewBuiltinProcPlugin(), r
}

// Every argument of every verb, in the argv the agent loop delivers: the
// {cmd,args} envelope flattened to [cmd, --key, value, ...] with the keys
// sorted, true as a bare flag and numbers as text.
func TestProcFlattenedArgvReachesTheAdapter(t *testing.T) {
	p, r := newProcRecorder(t)
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"start", "--command", "npm run dev", "--dir", "./web"}, "start|npm run dev|./web"},
		{[]string{"start", "--command", "python3", "--pty"}, "pty|python3|"},
		// The flattener writes the command under "cmd".
		{[]string{"start", "--cmd", "go run ./cmd/server"}, "start|go run ./cmd/server|"},
		// The child's own dash arguments are part of the ONE command value...
		{[]string{"start", "--command", "npm run dev -- --port 3000 --host", "--dir", "web"}, "start|npm run dev -- --port 3000 --host|web"},
		{[]string{"start", "--command", "--weird-binary --dir /x"}, "start|--weird-binary --dir /x|"},
		// ...and of the positional command line, from its first word on.
		{[]string{"start", "npm", "run", "dev", "--", "--port", "3000"}, "start|npm run dev -- --port 3000|"},
		{[]string{"start", "go", "test", "-v", "--dir", "./x", "--bogus"}, "start|go test -v --dir ./x --bogus|"},
		{[]string{"start", "--dir", "web", "npm", "start", "--pty"}, "start|npm start --pty|web"},
		{[]string{"start", "--pty", "--", "--version"}, "pty|--version|"},
		{[]string{"start", "--command=sleep 5", "--pty=false"}, "start|sleep 5|"},
		{[]string{"stdin", "--id", "p1", "--no_enter", "--text", "--help"}, "stdin|p1|--help|false"},
		{[]string{"stdin", "--id", "p1", "--text", "1+1"}, "stdin|p1|1+1|true"},
		{[]string{"stdin", "p1", "--no-enter", "--text", "y"}, "stdin|p1|y|false"},
		{[]string{"status", "--id", "p1"}, "status|p1"},
		{[]string{"status", "p2"}, "status|p2"},
		{[]string{"wait", "--id", "p1", "--pattern", "listening on", "--timeout", "90"}, "wait|p1|listening on|90"},
		{[]string{"wait", "--id", "p1"}, "wait|p1||60"},
		{[]string{"logs", "--id", "p1", "--tail", "50"}, "logs|p1|50"},
		{[]string{"logs", "--grep", "--- FAIL", "--id", "p1"}, "grep|p1|--- FAIL|0"},
		{[]string{"logs", "p1", "--tail=7"}, "logs|p1|7"},
		{[]string{"stop", "--id", "p1"}, "stop|p1"},
		{[]string{"remove", "--id", "p1"}, "remove|p1"},
		{[]string{"list"}, "list"},
		{[]string{`{"cmd":"start","args":{"cmd":"sleep 5","dir":"x"}}`}, "start|sleep 5|x"},
		{[]string{`{"cmd":"logs","id":"p1","tail":3}`}, "logs|p1|3"},
	} {
		r.calls = nil
		if out, err := p.Execute(context.Background(), c.argv); err != nil || out != "ok" || len(r.calls) != 1 || r.calls[0] != c.want {
			t.Errorf("%q: out=%q err=%v calls=%q, want %q", c.argv, out, err, r.calls, c.want)
		}
	}
}

// What the tool does not understand is reported, with the arguments the
// verb accepts, and nothing starts, stops or is read.
func TestProcRejectsWhatItDoesNotUnderstand(t *testing.T) {
	p, r := newProcRecorder(t)
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"start", "--command", "npm run dev", "--dri", "web"}, `unknown argument "--dri" (accepted: --command, --dir, --pty)`},
		{[]string{"start", "--bogus", "npm", "start"}, `unknown argument "--bogus"`},
		{[]string{"start", "--command", "a", "b"}, `unexpected argument "b" — command was already given`},
		{[]string{"start", "--command", "a", "--command", "b"}, "--command given more than once"},
		{[]string{"start", "--command"}, "--command needs a value"},
		{[]string{"start", "--command", "a", "--dir"}, "--dir needs a value"},
		{[]string{"start", "--dir", "--pty", "--command", "a"}, "--dir needs a value"},
		{[]string{"start", "--dir", "web"}, `"command" is required`},
		{[]string{"stop", "--id", "p1", "--force"}, `unknown argument "--force" (accepted: --id)`},
		{[]string{"stop", "--id"}, "--id needs a value"},
		{[]string{"stop", "p1", "p2"}, `unexpected argument "p2" — id was already given`},
		{[]string{"remove", "--all"}, `unknown argument "--all" (accepted: --id)`},
		{[]string{"logs", "--id", "p1", "--tial", "5"}, `unknown argument "--tial" (accepted: --grep, --id, --tail)`},
		{[]string{"logs", "--id", "p1", "--tail", "many"}, `--tail must be a whole number, got "many"`},
		{[]string{"logs", "--id", "p1", "--tail"}, "--tail needs a value"},
		{[]string{"wait", "--id", "p1", "--timeout", "--pattern", "x"}, "--timeout needs a value"},
		{[]string{"wait", "--id", "p1", "--timeout", "1m"}, `--timeout must be a whole number, got "1m"`},
		{[]string{"stdin", "--id", "p1", "--text"}, "--text needs a value"},
		{[]string{"stdin", "--id", "p1", "--text", "x", "--enter"}, `unknown argument "--enter" (accepted: --id, --no_enter, --text)`},
		{[]string{"list", "--all"}, `unknown argument "--all" (it takes no arguments)`},
		{[]string{"list", "p1"}, `unexpected argument "p1" (it takes no arguments)`},
		{[]string{`{"cmd":"stop","args":{"id":"p1","signal":"KILL"}}`}, `unknown argument "signal" (accepted: --id)`},
		{[]string{`{"cmd":"start","args":{"command":"a","cmd":"b"}}`}, "command given more than once"},
		{[]string{`{"cmd":"start","command":"a","cwd":"x"}`}, `unknown argument "cwd"`},
		{[]string{"restart", "--id", "p1"}, "unknown cmd \"restart\" (valid: start|stdin|wait|status|logs|stop|remove|list)"},
	} {
		out, err := p.Execute(context.Background(), c.argv)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "@proc") {
			t.Errorf("%q: got %q %v, want an @proc error with %q", c.argv, out, err, c.want)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("a rejected call reached the adapter: %q", r.calls)
	}
	if !strings.Contains(p.Usage(), "an unknown one, or one missing") {
		t.Error("Usage must say unknown arguments are errors")
	}
}
