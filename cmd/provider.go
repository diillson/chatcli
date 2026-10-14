/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/pkg/providerproto/conformance"
)

// Exit codes of `chatcli provider`.
const (
	providerExitOK    = 0
	providerExitFail  = 1
	providerExitUsage = 2
)

// RunProviderCLI executes `chatcli provider …`:
//
//	chatcli provider test [flags] <plugin> [plugin args…]
//
// test runs the conformance kit against a provider plugin binary and exits
// non-zero when any check fails, so plugin repositories can gate CI on it.
func RunProviderCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || isHelp(args[0]) {
		fmt.Fprintln(stdout, i18n.T("provider.cli.usage"))
		if len(args) == 0 {
			return providerExitUsage
		}
		return providerExitOK
	}
	switch args[0] {
	case "test":
		return runProviderTest(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, i18n.T("provider.cli.unknown", args[0]))
		fmt.Fprintln(stderr, i18n.T("provider.cli.usage"))
		return providerExitUsage
	}
}

func isHelp(a string) bool { return a == "-h" || a == "--help" || a == "help" }

// inputFlags collects repeated --input id=value pairs.
type inputFlags map[string]string

func (f inputFlags) String() string { return "" }

func (f inputFlags) Set(v string) error {
	id, val, ok := strings.Cut(v, "=")
	if !ok || strings.TrimSpace(id) == "" {
		return fmt.Errorf("%s", i18n.T("provider.cli.bad_input", v))
	}
	f[strings.TrimSpace(id)] = val
	return nil
}

func runProviderTest(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("provider test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inputs := inputFlags{}
	fs.Var(inputs, "input", i18n.T("provider.cli.flag.input"))
	credFile := fs.String("credential-file", "", i18n.T("provider.cli.flag.credential_file"))
	model := fs.String("model", "", i18n.T("provider.cli.flag.model"))
	timeout := fs.Duration("timeout", 30*time.Second, i18n.T("provider.cli.flag.timeout"))
	asJSON := fs.Bool("json", false, i18n.T("provider.cli.flag.json"))
	fs.Usage = func() {
		fmt.Fprintln(stderr, i18n.T("provider.cli.usage"))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return providerExitOK
		}
		return providerExitUsage
	}
	command := fs.Args()
	if len(command) == 0 {
		fmt.Fprintln(stderr, i18n.T("provider.cli.missing_plugin"))
		return providerExitUsage
	}

	opts := conformance.Options{Command: command, Inputs: inputs, Model: *model, Timeout: *timeout}
	if *credFile != "" {
		cred, err := readCredentialFile(*credFile)
		if err != nil {
			fmt.Fprintln(stderr, i18n.T("provider.cli.credential_read", err))
			return providerExitUsage
		}
		opts.Credential = cred
	}

	rep, err := conformance.Run(ctx, opts)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("provider.cli.run_failed", err))
		return providerExitFail
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		printProviderReport(stdout, rep)
	}
	if !rep.Passed() {
		return providerExitFail
	}
	return providerExitOK
}

func readCredentialFile(path string) (providerproto.Credential, error) {
	// #nosec G304 -- the operator names their own credential file.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cred providerproto.Credential
	if err := json.Unmarshal(data, &cred); err != nil {
		return nil, err
	}
	return cred, nil
}

func printProviderReport(w io.Writer, rep *conformance.Report) {
	name := "?"
	if rep.Plugin != nil {
		name = fmt.Sprintf("%s %s (%s)", rep.Plugin.Provider.Name, rep.Plugin.Provider.Version, rep.Plugin.Mode)
	}
	fmt.Fprintln(w, i18n.T("provider.cli.header", name))
	var pass, fail, skip int
	for _, c := range rep.Checks {
		mark := "✓"
		switch c.Status {
		case conformance.Pass:
			pass++
		case conformance.Fail:
			mark = "✗"
			fail++
		case conformance.Skip:
			mark = "-"
			skip++
		}
		line := fmt.Sprintf("  %s %-32s", mark, c.ID)
		if c.Detail != "" {
			line += " " + c.Detail
		}
		fmt.Fprintln(w, strings.TrimRight(line, " "))
	}
	fmt.Fprintln(w, i18n.T("provider.cli.summary", pass, fail, skip))
	if fail > 0 && strings.TrimSpace(rep.Stderr) != "" {
		fmt.Fprintln(w, i18n.T("provider.cli.stderr_tail"))
		fmt.Fprintln(w, strings.TrimSpace(rep.Stderr))
	}
}
