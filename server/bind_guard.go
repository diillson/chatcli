/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package server

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// Refusing to serve an unauthenticated API on a reachable address.
//
// Authentication is optional by design: the local CLI talks to its own
// server over loopback, where the trust boundary is the machine and a
// token would only be ceremony. That default becomes dangerous the moment
// the listener is reachable from elsewhere — inside Kubernetes the bind
// address is every interface, and an unauthenticated caller was admitted
// as an administrator.
//
// So the rule is about the address, not the environment: loopback may run
// open, anything else needs a credential, and the server says which one is
// missing instead of starting and hoping.

// requireAuthOnReachableBind returns an error when the server would listen
// on an address other than loopback with neither a shared token nor a JWT
// secret configured.
func requireAuthOnReachableBind(bindAddr, token string) error {
	if isLoopbackBind(bindAddr) {
		return nil
	}
	if strings.TrimSpace(token) != "" || strings.TrimSpace(os.Getenv("CHATCLI_JWT_SECRET")) != "" {
		return nil
	}
	return fmt.Errorf(
		"refusing to serve an unauthenticated API on %s: every caller that can reach it would be admitted as an administrator. "+
			"Set CHATCLI_SERVER_TOKEN (or --token) for a shared token, or CHATCLI_JWT_SECRET for per-user JWTs. "+
			"To run without authentication, bind loopback instead (CHATCLI_BIND_ADDRESS=127.0.0.1)",
		bindAddr)
}

// isLoopbackBind reports whether an address reaches only this machine.
// An empty address means "every interface" in Go's listener, so it is not
// loopback; a hostname that does not parse as an IP is treated as
// reachable, because the safe answer to "unknown" is to require a
// credential.
func isLoopbackBind(bindAddr string) bool {
	host := strings.TrimSpace(bindAddr)
	if host == "" {
		return false
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
