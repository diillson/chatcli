/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// The positional command line of start. The first word is the command, or
// a fragment of a command line, and is never quoted. A later word is one
// argument: it is quoted when it carries what the shell would otherwise
// act on — whitespace, a quote, a backtick, "$(", a ";" inside it, a
// newline — and left alone when it IS shell syntax the user typed between
// commands (an operator), a plain $VAR or a glob.
func TestProcStartPositionalQuotingRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX quoting")
	}
	p, r := newProcRecorder(t)
	for _, c := range []struct {
		argv []string
		want string
	}{
		// The first word is never quoted, whatever it holds.
		{[]string{"start", "npm run dev", "--", "--port", "3000"}, "npm run dev -- --port 3000"},
		{[]string{"start", "FOO=1 ./run.sh", "arg"}, "FOO=1 ./run.sh arg"},
		// Later words that would be re-read by the shell are quoted.
		{[]string{"start", "echo", "it's"}, `echo 'it'\''s'`},
		{[]string{"start", "echo", `say "hi"`}, `echo 'say "hi"'`},
		{[]string{"start", "echo", "`id`"}, "echo '`id`'"},
		{[]string{"start", "echo", "$(whoami)"}, `echo '$(whoami)'`},
		{[]string{"start", "sh", "-c", "a;b"}, `sh -c 'a;b'`},
		{[]string{"start", "printf", "one\ntwo"}, "printf 'one\ntwo'"},
		{[]string{"start", "grep", "", "file"}, `grep '' file`},
		// Words that ARE shell syntax, a plain variable or a glob stay.
		{[]string{"start", "a", "&&", "b", "||", "c", "|", "d", ";", "e", "&"}, "a && b || c | d ; e &"},
		{[]string{"start", "run", ">", "out.log", "2>&1", "<", "in", ">>", "all"}, "run > out.log 2>&1 < in >> all"},
		{[]string{"start", "echo", "$HOME", "${USER}", "*.go", "src/**/x?.[ch]"}, "echo $HOME ${USER} *.go src/**/x?.[ch]"},
	} {
		r.calls = nil
		want := "start|" + c.want + "|"
		if out, err := p.Execute(context.Background(), c.argv); err != nil || len(r.calls) != 1 || r.calls[0] != want {
			t.Errorf("%q: out=%q err=%v\n got  %q\n want %q", c.argv, out, err, r.calls, want)
		}
	}
	if !strings.Contains(p.Usage(), "shell syntax") {
		t.Error("Usage must send anything with shell syntax to the command value")
	}
}
