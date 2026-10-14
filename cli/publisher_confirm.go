/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/utils"
)

// PublisherConfirm returns the interactive trust-on-first-use prompt for
// plugin installs, or nil when no person is at the terminal — unattended
// installs then need an explicit --trust fingerprint.
func PublisherConfirm() func(name string, p plugins.Publisher) bool {
	if !utils.OwnsTerminal() {
		return nil
	}
	return confirmPublisher
}

func confirmPublisher(name string, p plugins.Publisher) bool {
	fmt.Println(colorize("  "+i18n.T("plugin.trust.unknown_publisher", name), ColorYellow))
	fmt.Println("  " + i18n.T("plugin.trust.fingerprint", p.Fingerprint))
	fmt.Println(colorize("  "+i18n.T("plugin.trust.verify_hint"), ColorGray))
	fmt.Print("  " + i18n.T("plugin.trust.confirm"))
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "s", "sim":
		return true
	}
	return false
}

// PrintInstallResult reports a finished bundle install.
func PrintInstallResult(w io.Writer, res plugins.InstallResult) {
	switch {
	case res.Publisher.Unsigned:
		fmt.Fprintln(w, colorize("  "+i18n.T("plugin.install.unsigned_installed", res.Path), ColorYellow))
	case res.NewlyTrusted:
		fmt.Fprintln(w, colorize("  "+i18n.T("plugin.install.publisher_trusted", res.Publisher.Fingerprint), ColorGreen))
		fmt.Fprintln(w, "  "+i18n.T("plugin.install.done", res.Path))
	default:
		fmt.Fprintln(w, "  "+i18n.T("plugin.install.done_trusted", res.Path, res.Publisher.Fingerprint))
	}
}

// InstallErrorMessage words a bundle install failure in the active
// language, keeping the facts (fingerprints) the person acts on.
func InstallErrorMessage(err error) string {
	var pe *plugins.PublisherError
	if !errors.As(err, &pe) {
		return i18n.T("plugin.install.bundle_failed", err)
	}
	switch pe.Reason {
	case plugins.ReasonUnsigned:
		return i18n.T("plugin.install.refused_unsigned", pe.Plugin)
	case plugins.ReasonBadSignature:
		return i18n.T("plugin.install.refused_bad_signature", pe.Plugin)
	case plugins.ReasonKeyChanged:
		return i18n.T("plugin.install.refused_key_changed", pe.Plugin, pe.Pinned, pe.Fingerprint)
	case plugins.ReasonTrustMismatch:
		return i18n.T("plugin.install.refused_trust_mismatch", pe.Given, pe.Fingerprint)
	case plugins.ReasonNoBundledKey:
		return i18n.T("plugin.install.refused_no_bundled_key", pe.Plugin)
	default:
		return i18n.T("plugin.install.refused_not_trusted", pe.Plugin, pe.Fingerprint, pe.Fingerprint)
	}
}
