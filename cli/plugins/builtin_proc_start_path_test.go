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
// command line, which must stay unquoted. It is a PATH with spaces in it,
// and is quoted, when it names something that exists, or when more words
// follow and it is spelled like a path. Later words are one argument each:
// one that holds &, |, <, > or # without being an operator or a
// redirection is quoted, or the shell would cut the command there.
func TestProcStartQuotesPathsAndShellCharacters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX quoting")
	}
	dir := filepath.Join(t.TempDir(), "my tools")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(dir, "run")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(filepath.Dir(dir))

	p, r := newProcRecorder(t)
	for _, c := range []struct {
		argv []string
		want string
	}{
		// A path with spaces: quoted, alone or followed by arguments.
		{[]string{"start", tool, "x"}, "'" + tool + "' x"},
		{[]string{"start", tool}, "'" + tool + "'"},
		{[]string{"start", "my tools/run", "x"}, "'my tools/run' x"},
		{[]string{"start", "/opt/my tools/run", "x"}, "'/opt/my tools/run' x"},
		{[]string{"start", "./my tools/missing", "--flag"}, "'./my tools/missing' --flag"},
		{[]string{"start", "../some dir/run", "x"}, "'../some dir/run' x"},
		{[]string{"start", "~/my tools/run", "x"}, "~/'my tools/run' x"},
		{[]string{"start", `C:\Program Files\App\app.exe`, "/s"}, `'C:\Program Files\App\app.exe' /s`},
		// A command line fragment: never quoted.
		{[]string{"start", "npm run dev", "--", "--port", "3000"}, "npm run dev -- --port 3000"},
		{[]string{"start", "FOO=1 ./run.sh", "arg"}, "FOO=1 ./run.sh arg"},
		{[]string{"start", "/usr/bin/env node server.js"}, "/usr/bin/env node server.js"},
		{[]string{"start", "./run.sh --watch"}, "./run.sh --watch"},
		// Later words holding shell characters stay one argument.
		{[]string{"start", "curl", "http://h/?a=1&b=2"}, "curl 'http://h/?a=1&b=2'"},
		{[]string{"start", "sh", "-c", "a|b"}, "sh -c 'a|b'"},
		{[]string{"start", "echo", "a>b", "x<y", "issue#12", "#tag"}, "echo 'a>b' 'x<y' 'issue#12' '#tag'"},
		// Operators and redirections the user typed keep working.
		{[]string{"start", "a", "&&", "b", "|", "c", "&"}, "a && b | c &"},
		{[]string{"start", "run", ">out.log", "2>/dev/null", "<in.txt", ">>all.log", "2>&1", "&>both"}, "run >out.log 2>/dev/null <in.txt >>all.log 2>&1 &>both"},
		{[]string{"start", "echo", "$HOME", "*.go"}, "echo $HOME *.go"},
	} {
		r.calls = nil
		want := "start|" + c.want + "|"
		if out, err := p.Execute(context.Background(), c.argv); err != nil || len(r.calls) != 1 || r.calls[0] != want {
			t.Errorf("%q: out=%q err=%v\n got  %q\n want %q", c.argv, out, err, r.calls, want)
		}
	}
}
