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

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/providerhost"
	"github.com/diillson/chatcli/pkg/providerproto"
	"github.com/diillson/chatcli/pkg/providerproto/conformance"
	"go.uber.org/zap"
	"golang.org/x/term"
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
	case "list":
		return runProviderList(ctx, stdout)
	case "login":
		return runProviderLogin(ctx, args[1:], stdout, stderr)
	case "logout":
		return runProviderLogout(ctx, args[1:], stdout, stderr)
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

// loadInstalledProviders starts the installed provider plugins under the
// same signature policy the REPL applies.
func loadInstalledProviders(ctx context.Context, interactive bool) *providerhost.Set {
	return providerhost.Load(ctx, cli.ProviderPluginLoadOptions(zap.NewNop(), interactive))
}

func runProviderList(ctx context.Context, stdout io.Writer) int {
	set := loadInstalledProviders(ctx, false)
	defer set.Close(ctx)
	plugins, errs := set.List(), set.Errors()
	if len(plugins) == 0 && len(errs) == 0 {
		fmt.Fprintln(stdout, i18n.T("provider.cli.list.empty", providerhost.DefaultDir()))
		return providerExitOK
	}
	for _, p := range plugins {
		info := p.Info()
		state := i18n.T("provider.plugin.status.no_login")
		if p.RequiresLogin() {
			if p.Available() {
				state = i18n.T("provider.plugin.status.logged_in")
			} else {
				state = i18n.T("provider.plugin.status.logged_out")
			}
		}
		fmt.Fprintf(stdout, "%-20s %s %s (%s) — %s\n", p.Name(), p.DisplayName(i18n.ActiveTag().String()), info.Provider.Version, info.Mode, state)
	}
	for _, e := range errs {
		fmt.Fprintln(stdout, i18n.T("provider.cli.list.failed", e.Path, e.Err))
	}
	if len(errs) > 0 {
		return providerExitFail
	}
	return providerExitOK
}

func runProviderLogin(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("provider login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	inputs := inputFlags{}
	fs.Var(inputs, "input", i18n.T("provider.cli.flag.input"))
	// The name usually comes first (`login acme --input k=v`), and the flag
	// package stops at the first positional: lift it out before parsing.
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return providerExitOK
		}
		return providerExitUsage
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		name = ""
	}
	if name == "" {
		fmt.Fprintln(stderr, i18n.T("provider.cli.login.usage"))
		return providerExitUsage
	}
	interactive := term.IsTerminal(int(os.Stdin.Fd())) // #nosec G115 -- a file descriptor always fits in int
	set := loadInstalledProviders(ctx, interactive)
	defer set.Close(ctx)
	p, ok := set.Get(name)
	if !ok {
		fmt.Fprintln(stderr, i18n.T("provider.cli.not_installed", name))
		return providerExitFail
	}
	var ui providerhost.LoginUI
	if interactive {
		ui = cli.TerminalLoginUI()
	}
	account, err := p.Login(ctx, ui, inputs)
	if err != nil {
		fmt.Fprintln(stderr, i18n.T("auth.login.failed", err))
		return providerExitFail
	}
	if account == "" {
		account = p.Name()
	}
	fmt.Fprintln(stdout, i18n.T("auth.login.success", p.DisplayName(i18n.ActiveTag().String()), account))
	return providerExitOK
}

func runProviderLogout(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, i18n.T("provider.cli.logout.usage"))
		return providerExitUsage
	}
	set := loadInstalledProviders(ctx, false)
	defer set.Close(ctx)
	p, ok := set.Get(args[0])
	if !ok {
		fmt.Fprintln(stderr, i18n.T("provider.cli.not_installed", args[0]))
		return providerExitFail
	}
	if err := p.Logout(ctx); err != nil {
		fmt.Fprintln(stderr, i18n.T("auth.logout.failed", err))
		return providerExitFail
	}
	fmt.Fprintln(stdout, i18n.T("auth.logout.success"))
	return providerExitOK
}
