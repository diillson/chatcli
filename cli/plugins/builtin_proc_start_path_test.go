/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// The first positional word of start is a command — or a fragment of a
// command line, which must stay unquoted however much it looks like a
// path. It is quoted in exactly one case: it holds whitespace AND names a
// file or directory that exists, looked up where the process will start
// (--dir when given). Later words are one argument each: one that holds
// &, |, <, >, #, ( or ) without being an operator or a redirection is
// quoted, or the shell would cut or misread the command there.
func TestProcStartQuotesPathsAndShellCharacters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX quoting")
	}
	base := t.TempDir()
	dir := filepath.Join(base, "my tools")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(dir, "run")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir()) // chatcli's own directory is somewhere else

	p, r := newProcRecorder(t)
	for _, c := range []struct {
		argv []string
		want string
	}{
		// A path with spaces that exists: quoted, alone or with arguments.
		{[]string{"start", tool, "x"}, "start|'" + tool + "' x|"},
		{[]string{"start", tool}, "start|'" + tool + "'|"},
		// Relative paths are looked up in the start directory, not here.
		{[]string{"start", "--dir", base, "my tools/run", "x"}, "start|'my tools/run' x|" + base},
		{[]string{"start", "--dir", base, "./my tools/run", "x"}, "start|'./my tools/run' x|" + base},
		{[]string{"start", "my tools/run", "x"}, "start|my tools/run x|"},
		// A command line fragment is never quoted, path-like or not.
		{[]string{"start", "/usr/bin/env FOO=1 node", "server.js"}, "start|/usr/bin/env FOO=1 node server.js|"},
		{[]string{"start", "./manage.py runserver", "--noreload"}, "start|./manage.py runserver --noreload|"},
		{[]string{"start", "/opt/my tools/missing", "x"}, "start|/opt/my tools/missing x|"},
		{[]string{"start", "npm run dev", "--", "--port", "3000"}, "start|npm run dev -- --port 3000|"},
		{[]string{"start", "FOO=1 ./run.sh", "arg"}, "start|FOO=1 ./run.sh arg|"},
		{[]string{"start", "/usr/bin/env node server.js"}, "start|/usr/bin/env node server.js|"},
		// Later words holding shell characters stay one argument.
		{[]string{"start", "curl", "http://h/?a=1&b=2"}, "start|curl 'http://h/?a=1&b=2'|"},
		{[]string{"start", "sh", "-c", "a|b"}, "start|sh -c 'a|b'|"},
		{[]string{"start", "echo", "a>b", "x<y", "issue#12", "#tag"}, "start|echo 'a>b' 'x<y' 'issue#12' '#tag'|"},
		{[]string{"start", "python", "-c", "print(1)"}, "start|python -c 'print(1)'|"},
		{[]string{"start", "echo", "(a)", "f(", ")x"}, "start|echo '(a)' 'f(' ')x'|"},
		// Operators and redirections the user typed keep working.
		{[]string{"start", "a", "&&", "b", "|", "c", "&"}, "start|a && b | c &|"},
		{[]string{"start", "(", "a", ";", "b", ")"}, "start|( a ; b )|"},
		{[]string{"start", "run", ">out.log", "2>/dev/null", "<in.txt", ">>all.log", "2>&1", "&>both"}, "start|run >out.log 2>/dev/null <in.txt >>all.log 2>&1 &>both|"},
		{[]string{"start", "echo", "$HOME", "*.go"}, "start|echo $HOME *.go|"},
	} {
		r.calls = nil
		if out, err := p.Execute(context.Background(), c.argv); err != nil || len(r.calls) != 1 || r.calls[0] != c.want {
			t.Errorf("%q: out=%q err=%v\n got  %q\n want %q", c.argv, out, err, r.calls, c.want)
		}
	}
}
