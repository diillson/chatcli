/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"github.com/diillson/chatcli/i18n"
)

// Capability advertisements for BuiltinBrowserPlugin, in the same per-plugin
// caps file shape as @lsp/@webfetch.

// browserReadOnlyCmds are the invocations without page side effects beyond
// navigation itself. `open` is classified like @webfetch (a fetch); `click`,
// `type`, `press`, `hover`, `select`, `upload`, `eval` and `cookies --clear`
// can trigger real actions on remote sites (or log the user out) and
// therefore go through the security gate.
var browserReadOnlyCmds = map[string]bool{
	"open":       true,
	"show":       true,
	"hide":       true,
	"wait":       true,
	"html":       true,
	"pdf":        true,
	"resize":     true,
	"tabs":       true,
	"tab":        true,
	"cookies":    true, // listing only; --clear is checked below
	"snapshot":   true,
	"screenshot": true,
	"console":    true,
	"network":    true,
	"scroll":     true,
	"back":       true,
	"status":     true,
	"close":      true,
}

// IsReadOnly reports whether this specific invocation observes the page or
// acts on it. Unparseable args fail closed (not read-only).
func (p *BuiltinBrowserPlugin) IsReadOnly(args []string) bool {
	inv, err := parseBrowserInvocation(args)
	if err != nil {
		return false
	}
	if inv.cmd == "cookies" && inv.clear {
		return false
	}
	return browserReadOnlyCmds[inv.cmd]
}

// IsConcurrencySafe reports false for every invocation: all commands drive
// the single stateful browser session, and interleaving click/snapshot from
// parallel calls would corrupt the element refs.
func (p *BuiltinBrowserPlugin) IsConcurrencySafe(_ []string) bool { return false }

// DescribeCall surfaces what the browser is doing so the spinner reads
// "Browser: opening http://localhost:3000" instead of the raw JSON envelope.
func (p *BuiltinBrowserPlugin) DescribeCall(args []string) string {
	inv, err := parseBrowserInvocation(args)
	if err != nil {
		return i18n.T("plugins.browser.describe_generic")
	}
	switch inv.cmd {
	case "open":
		if inv.visibleSet && inv.visible {
			return i18n.T("plugins.browser.describe_show", describeTrim(inv.url))
		}
		return i18n.T("plugins.browser.describe_open", describeTrim(inv.url))
	case "show":
		if inv.url == "" {
			return i18n.T("plugins.browser.describe_show_bare")
		}
		return i18n.T("plugins.browser.describe_show", describeTrim(inv.url))
	case "hide":
		return i18n.T("plugins.browser.describe_hide")
	case "wait":
		return i18n.T("plugins.browser.describe_wait", describeTrim(browserWaitCondition(inv.url, inv.text, inv.selector)))
	case "press":
		return i18n.T("plugins.browser.describe_press", describeTrim(inv.key))
	case "hover":
		return i18n.T("plugins.browser.describe_hover", describeTrim(inv.target))
	case "select":
		return i18n.T("plugins.browser.describe_select", describeTrim(inv.target))
	case "upload":
		return i18n.T("plugins.browser.describe_upload", describeTrim(inv.target))
	case "html":
		return i18n.T("plugins.browser.describe_html")
	case "pdf":
		return i18n.T("plugins.browser.describe_pdf")
	case "resize":
		return i18n.T("plugins.browser.describe_resize")
	case "tabs":
		return i18n.T("plugins.browser.describe_tabs")
	case "tab":
		return i18n.T("plugins.browser.describe_tab", describeTrim(inv.target))
	case "cookies":
		if inv.clear {
			return i18n.T("plugins.browser.describe_cookies_clear")
		}
		return i18n.T("plugins.browser.describe_cookies")
	case "snapshot":
		return i18n.T("plugins.browser.describe_snapshot")
	case "click":
		return i18n.T("plugins.browser.describe_click", describeTrim(inv.target))
	case "type":
		return i18n.T("plugins.browser.describe_type", describeTrim(inv.target))
	case "eval":
		return i18n.T("plugins.browser.describe_eval")
	case "screenshot":
		return i18n.T("plugins.browser.describe_screenshot")
	case "console":
		return i18n.T("plugins.browser.describe_console")
	case "network":
		return i18n.T("plugins.browser.describe_network")
	default:
		return i18n.T("plugins.browser.describe_generic")
	}
}
