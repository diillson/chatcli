/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package providerhost

import (
	"fmt"
	"net/url"
	"regexp"

	"github.com/diillson/chatcli/llm/catalog"
	"github.com/diillson/chatcli/pkg/providerproto"
)

var providerName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,31}$`)

// validateInfo enforces the handshake contract the host relies on. A plugin
// that fails it never registers, so a malformed or impersonating plugin
// cannot reach /switch.
func validateInfo(info providerproto.InitializeResult) error {
	if info.ProtocolVersion != providerproto.ProtocolVersion {
		return fmt.Errorf("protocol version %q, want %q", info.ProtocolVersion, providerproto.ProtocolVersion)
	}
	name := info.Provider.Name
	if !providerName.MatchString(name) {
		return fmt.Errorf("provider name %q must match %s", name, providerName)
	}
	if d, ok := catalog.LookupProvider(name); ok && d.Builtin() {
		return fmt.Errorf("provider name %q belongs to a built-in provider", name)
	}
	if info.Provider.DisplayName["en"] == "" {
		return fmt.Errorf(`provider %s has no displayName["en"]`, name)
	}
	switch info.Mode {
	case providerproto.ModeFull:
	case providerproto.ModeDialect:
		if info.Dialect == nil {
			return fmt.Errorf("provider %s: dialect mode without a dialect", name)
		}
		if !supportedDialect(info.Dialect.API) {
			return fmt.Errorf("provider %s: dialect %q is not supported by this chatcli (supported: %s)",
				name, info.Dialect.API, providerproto.DialectOpenAIChat)
		}
		u, err := url.Parse(info.Dialect.BaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("provider %s: dialect baseURL %q is not an http(s) URL", name, info.Dialect.BaseURL)
		}
	default:
		return fmt.Errorf("provider %s: unknown mode %q", name, info.Mode)
	}
	return nil
}

// supportedDialect lists the wire formats this host can drive with a
// built-in client.
func supportedDialect(api providerproto.DialectAPI) bool {
	return api == providerproto.DialectOpenAIChat
}
