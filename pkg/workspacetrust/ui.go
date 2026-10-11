/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Workspace trust: the boot prompt, the non-interactive warning and the
 * `chatcli trust` subcommand.
 */
package workspacetrust

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/diillson/chatcli/i18n"
)

// Ask shows what was found in an untrusted workspace and asks once, default
// No. Yes records the trust; anything else (including EOF) keeps every
// covered file out for this run. It reports the answer.
func Ask(ins Inspection, in io.Reader, out io.Writer) bool {
	fmt.Fprintln(out, i18n.T("wstrust.prompt.title", ins.Dir))
	for _, f := range ins.Files {
		fmt.Fprintln(out, "  - "+f)
	}
	fmt.Fprintln(out, i18n.T("wstrust.prompt.explain"))
	fmt.Fprint(out, i18n.T("wstrust.prompt.question")+" ")
	switch strings.ToLower(strings.TrimSpace(readLine(in))) {
	case "y", "yes", "s", "sim":
		if err := Trust(ins); err != nil {
			fmt.Fprintln(out, i18n.T("wstrust.prompt.store_failed", err))
		}
		return true
	}
	Deny(ins)
	fmt.Fprintln(out, i18n.T("wstrust.prompt.declined", ins.Dir, ins.Dir))
	return false
}

// readLine reads up to a newline one byte at a time, so nothing typed after
// the answer is consumed before the interactive prompt takes over stdin.
func readLine(in io.Reader) string {
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < 64 {
		n, err := in.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			break
		}
	}
	return b.String()
}

// warned keeps Warn to one notice per directory and process.
var warned sync.Map

// Warn prints the single notice a non-interactive run gives when it skips
// the covered files of an untrusted workspace.
func Warn(w io.Writer, ins Inspection) {
	if _, dup := warned.LoadOrStore(ins.Dir, true); dup {
		return
	}
	fmt.Fprintln(w, i18n.T("wstrust.warn.untrusted", ins.Dir, strings.Join(ins.Files, ", "), ins.Dir))
}

// RunCLI implements `chatcli trust [dir] | --list | --revoke [dir]`.
func RunCLI(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("trust", flag.ContinueOnError)
	fs.SetOutput(stderr)
	list := fs.Bool("list", false, i18n.T("wstrust.cli.flag_list"))
	revoke := fs.Bool("revoke", false, i18n.T("wstrust.cli.flag_revoke"))
	fs.Usage = func() {
		fmt.Fprintln(stderr, i18n.T("wstrust.cli.usage"))
		fs.PrintDefaults()
	}
	if len(args) > 0 && args[0] == "help" {
		fs.Usage()
		return 0
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 1 || (*list && (*revoke || fs.NArg() > 0)) {
		fs.Usage()
		return 2
	}
	dir := "."
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	switch {
	case *list:
		return runList(stdout, stderr)
	case *revoke:
		removed, err := Revoke(dir)
		if err != nil {
			fmt.Fprintln(stderr, i18n.T("wstrust.cli.error", err))
			return 1
		}
		if !removed {
			fmt.Fprintln(stdout, i18n.T("wstrust.cli.not_trusted", dir))
			return 0
		}
		fmt.Fprintln(stdout, i18n.T("wstrust.cli.revoked", dir))
		return 0
	}
	ins, err := Inspect(dir)
	if err == nil {
		err = Trust(ins)
	}
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("wstrust.cli.error", err))
		return 1
	}
	fmt.Fprintln(stdout, i18n.T("wstrust.cli.trusted", ins.Dir))
	if len(ins.Files) == 0 {
		fmt.Fprintln(stdout, i18n.T("wstrust.cli.no_files"))
	}
	for _, f := range ins.Files {
		fmt.Fprintln(stdout, "  - "+f)
	}
	return 0
}

func runList(stdout, stderr io.Writer) int {
	recs, err := List()
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("wstrust.cli.error", err))
		return 1
	}
	if len(recs) == 0 {
		fmt.Fprintln(stdout, i18n.T("wstrust.cli.list_empty"))
		return 0
	}
	for _, r := range recs {
		state := i18n.T("wstrust.cli.state_lapsed")
		if ins, err := Inspect(r.Path); err == nil && ins.Fingerprint == r.Fingerprint {
			state = i18n.T("wstrust.cli.state_current")
		}
		fmt.Fprintf(stdout, "%s  [%s]  %s\n", r.Path, state, strings.Join(r.Files, ", "))
	}
	return 0
}
