/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
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
		return runPluginTrust(rest)
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
		dir = filepath.Join(home, ".chatcli", "plugin-keys")
	}

	keyPath, pubPath, err := plugins.GenerateSigningKeyPair(dir)
	if err != nil {
		return err
	}
	fmt.Println(i18n.T("plugincli.keygen.done", keyPath, pubPath))
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
	q := plugins.NewQuarantine(filepath.Join(home, ".chatcli", "plugins"))

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
		entries := q.List()
		if len(entries) == 0 {
			fmt.Println(i18n.T("plugincli.quarantine.empty", q.Window().String()))
			return nil
		}
		fmt.Println(i18n.T("plugincli.quarantine.header", q.Window().String()))
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
		return nil

	case "release":
		if len(args) < 2 {
			return fmt.Errorf("%s", i18n.T("plugincli.quarantine.release_name_required"))
		}
		if err := q.Release(args[1]); err != nil {
			return err
		}
		fmt.Println(i18n.T("plugincli.quarantine.released", args[1]))
		return nil

	default:
		return fmt.Errorf("%s", i18n.T("plugincli.quarantine.unknown_verb", sub))
	}
}
