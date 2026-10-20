/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli"
	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/providerhost"
	"github.com/diillson/chatcli/pkg/stateroot"
)

// RunPluginCLI implements `chatcli plugin ...` — the plugin supply chain:
// generating a signing key, signing a binary, registering a public key so
// signatures verify, and inspecting what quarantine is holding.
//
// It runs without an LLM manager or a full ChatCLI boot, the same shape as
// `chatcli mcp`: these are key-management operations an operator or a CI
// job runs, not a conversation.
func RunPluginCLI(args []string) error {
	if len(args) == 0 {
		printPluginUsage()
		return nil
	}

	verb := args[0]
	rest := args[1:]

	switch verb {
	case "keygen":
		return runPluginKeygen(rest)
	case "sign":
		return runPluginSign(rest)
	case "verify":
		return runPluginVerify(rest)
	case "trust":
		if len(rest) > 0 && rest[0] == "list" {
			return runPluginTrustList()
		}
		return runPluginTrust(rest)
	case "install":
		return runBundleInstall(context.Background(), plugins.KindTool, rest, os.Stdout)
	case "quarantine":
		return runPluginQuarantine(rest)
	case "help", "-h", "--help":
		printPluginUsage()
		return nil
	default:
		printPluginUsage()
		return fmt.Errorf("%s", i18n.T("plugincli.unknown_verb", verb))
	}
}

func printPluginUsage() {
	fmt.Println(`Usage:
  chatcli plugin keygen --output <dir>
      Generate an Ed25519 signing key pair. Writes plugin-signing.key (0600)
      and plugin-signing.pub (0644). Refuses to overwrite an existing key.

  chatcli plugin sign --binary <path> --key <path> [--output <path>]
      Sign a plugin binary. Writes a detached signature next to the binary
      as <binary>.sig unless --output says otherwise.

  chatcli plugin verify --binary <path>
      Verify a plugin against the public keys in ~/.chatcli/trusted-keys/.

  chatcli plugin trust --key <path> [--name <name>]
      Register a public key so plugins signed with the matching private key
      verify on this machine.

  chatcli plugin trust list
      Trusted keys (yours and the administrator's managed keys) with their
      fingerprints, and the publisher pinned for each installed plugin.

  chatcli plugin install <path|https-url> [--trust <fingerprint>]
      Install a prebuilt plugin. <src>.sig and <src>.pub are read beside it;
      an unknown publisher is confirmed once by fingerprint (or with --trust
      when no person is at the terminal) and pinned: a later bundle for the
      same plugin signed by another key is refused.

  chatcli plugin quarantine [list]
      Show unsigned plugins still inside their waiting period.

  chatcli plugin quarantine release <name>
      Admit a reviewed plugin immediately. Replacing the binary later puts
      it back in quarantine.

Inside the REPL, /plugin list, /plugin install and /plugin inspect manage
installed plugins.`)
}

func runPluginKeygen(args []string) error {
	fs := flag.NewFlagSet("plugin keygen", flag.ContinueOnError)
	out := fs.String("output", "", "directory to write the key pair into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := strings.TrimSpace(*out)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("%s", i18n.T("plugincli.keygen.output_required"))
		}
		dir = filepath.Join(stateroot.Under(home), "plugin-keys")
	}

	keyPath, pubPath, err := plugins.GenerateSigningKeyPair(dir)
	if err != nil {
		return err
	}
	fmt.Println(i18n.T("plugincli.keygen.done", keyPath, pubPath))
	if fp, err := plugins.PublicKeyFingerprint(pubPath); err == nil {
		fmt.Println(i18n.T("plugincli.keygen.fingerprint", fp))
	}
	fmt.Println(i18n.T("plugincli.keygen.next", pubPath))
	return nil
}

func runPluginSign(args []string) error {
	fs := flag.NewFlagSet("plugin sign", flag.ContinueOnError)
	binary := fs.String("binary", "", "plugin binary to sign")
	key := fs.String("key", "", "Ed25519 private key produced by 'plugin keygen'")
	out := fs.String("output", "", "signature file path (default: <binary>.sig)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*binary) == "" {
		return fmt.Errorf("%s", i18n.T("plugincli.sign.binary_required"))
	}
	if strings.TrimSpace(*key) == "" {
		return fmt.Errorf("%s", i18n.T("plugincli.sign.key_required"))
	}

	sigPath, err := plugins.SignPlugin(*binary, *key, *out)
	if err != nil {
		return err
	}
	fmt.Println(i18n.T("plugincli.sign.done", sigPath))
	return nil
}

func runPluginVerify(args []string) error {
	fs := flag.NewFlagSet("plugin verify", flag.ContinueOnError)
	binary := fs.String("binary", "", "plugin binary to verify")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*binary) == "" {
		return fmt.Errorf("%s", i18n.T("plugincli.verify.binary_required"))
	}

	// Inspect rather than VerifyPlugin: this command answers the factual
	// question, and must keep answering it even where policy has been set
	// to tolerate unsigned plugins.
	status, err := plugins.NewPluginVerifier().Inspect(*binary)
	switch status {
	case plugins.StatusVerified:
		fmt.Println(i18n.T("plugincli.verify.ok", *binary))
		return nil
	case plugins.StatusUnsigned:
		return fmt.Errorf("%s", i18n.T("plugincli.verify.no_signature", *binary))
	case plugins.StatusUnverifiable:
		return fmt.Errorf("%s", i18n.T("plugincli.verify.no_trusted_keys"))
	default:
		return fmt.Errorf("%s", i18n.T("plugincli.verify.failed", err))
	}
}

func runPluginTrust(args []string) error {
	fs := flag.NewFlagSet("plugin trust", flag.ContinueOnError)
	key := fs.String("key", "", "public key to trust")
	name := fs.String("name", "", "name to file it under (default: the file's base name)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" {
		return fmt.Errorf("%s", i18n.T("plugincli.trust.key_required"))
	}

	dest, err := plugins.TrustPublicKey(*key, *name)
	if err != nil {
		return err
	}
	fmt.Println(i18n.T("plugincli.trust.done", dest))
	return nil
}

func runPluginQuarantine(args []string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	q := plugins.NewQuarantine(filepath.Join(stateroot.Under(home), "plugins"))
	pq := plugins.NewQuarantine(providerhost.DefaultDir())

	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "list":
		if !q.Enabled() {
			fmt.Println(i18n.T("plugincli.quarantine.disabled"))
			return nil
		}
		printQuarantine(q, pq)
		return nil

	case "release":
		if len(args) < 2 {
			return fmt.Errorf("%s", i18n.T("plugincli.quarantine.release_name_required"))
		}
		if err := plugins.ReleaseIn(args[1], q, pq); err != nil {
			return err
		}
		fmt.Println(i18n.T("plugincli.quarantine.released", args[1]))
		return nil

	default:
		return fmt.Errorf("%s", i18n.T("plugincli.quarantine.unknown_verb", sub))
	}
}

// printQuarantine lists what the tool-plugin quarantine (q) and the
// provider-plugin quarantine (pq) hold.
func printQuarantine(q, pq *plugins.Quarantine) {
	entries, providerEntries := q.List(), pq.List()
	if len(entries) == 0 && len(providerEntries) == 0 {
		fmt.Println(i18n.T("plugincli.quarantine.empty", q.Window().String()))
		return
	}
	fmt.Println(i18n.T("plugincli.quarantine.header", q.Window().String()))
	printQuarantineRows(entries)
	if len(providerEntries) > 0 {
		fmt.Println(i18n.T("plugincli.quarantine.providers_header"))
		printQuarantineRows(providerEntries)
	}
}

func printQuarantineRows(entries []plugins.QuarantineEntry) {
	for _, e := range entries {
		switch {
		case e.Released:
			fmt.Println(i18n.T("plugincli.quarantine.row_released", e.Name))
		case e.Remaining > 0:
			fmt.Println(i18n.T("plugincli.quarantine.row_waiting", e.Name, e.Remaining.Round(time.Second).String()))
		default:
			fmt.Println(i18n.T("plugincli.quarantine.row_admitted", e.Name))
		}
	}
}

// runBundleInstall installs a prebuilt tool or provider plugin bundle.
func runBundleInstall(ctx context.Context, kind string, args []string, stdout io.Writer) error {
	var src string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		src, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(kind+" install", flag.ContinueOnError)
	trust := fs.String("trust", "", "publisher fingerprint you vouch for (SHA256:...)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if src == "" && fs.NArg() == 1 {
		src = fs.Arg(0)
	}
	if src == "" {
		return fmt.Errorf("%s", i18n.T("plugincli.install.usage"))
	}
	dest, err := bundleDest(kind)
	if err != nil {
		return err
	}
	res, err := plugins.InstallBundle(ctx, src, plugins.InstallOptions{
		Kind:          kind,
		DestDir:       dest,
		Trust:         strings.TrimSpace(*trust),
		Confirm:       cli.PublisherConfirm(),
		AllowUnsigned: plugins.NewPluginVerifier().AllowsUnsigned(),
	})
	if err != nil {
		return errors.New(cli.InstallErrorMessage(err))
	}
	cli.PrintInstallResult(stdout, res)
	return nil
}

func bundleDest(kind string) (string, error) {
	if kind == plugins.KindProvider {
		return providerhost.DefaultDir(), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateroot.Under(home), "plugins"), nil
}

func runPluginTrustList() error {
	keys := plugins.ListTrustedKeys()
	if len(keys) == 0 {
		fmt.Println(i18n.T("plugincli.trust.list.empty"))
	} else {
		fmt.Println(i18n.T("plugincli.trust.list.keys_header"))
		for _, k := range keys {
			fmt.Println(i18n.T("plugincli.trust.list.key_row", k.Name, k.Fingerprint, k.Source))
		}
	}
	pins := plugins.ListPins()
	if len(pins) > 0 {
		fmt.Println(i18n.T("plugincli.trust.list.pins_header"))
		names := make([]string, 0, len(pins))
		for n := range pins {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Println(i18n.T("plugincli.trust.list.pin_row", n, pins[n].Fingerprint))
		}
	}
	return nil
}
