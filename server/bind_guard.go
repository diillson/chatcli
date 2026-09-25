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

	"github.com/diillson/chatcli/pkg/jwtkey"
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

// bindCredentials is every credential the guard recognizes. It mirrors what
// the auth interceptor and the TLS listener actually enforce: a shared
// token, JWT material of either algorithm, or a client CA (mTLS), so a
// deployment secured by RS256 or by client certificates alone is not refused
// at startup.
type bindCredentials struct {
	token        string
	jwtSecret    string
	jwtPublicKey string
	clientCAFile string
}

// configured reports whether any credential is set.
func (c bindCredentials) configured() bool {
	if strings.TrimSpace(c.token) != "" || strings.TrimSpace(c.clientCAFile) != "" {
		return true
	}
	return jwtkey.Algorithm(c.jwtPublicKey, c.jwtSecret) != jwtkey.AlgNone
}

// requireAuthOnReachableBind returns an error when the server would listen
// on an address other than loopback with no credential configured: no
// shared token, no JWT secret or public key, and no client CA.
func requireAuthOnReachableBind(bindAddr string, creds bindCredentials) error {
	if isLoopbackBind(bindAddr) {
		return nil
	}
	if creds.configured() {
		return nil
	}
	return fmt.Errorf(
		"refusing to serve an unauthenticated API on %s: every caller that can reach it would be admitted as an administrator. "+
			"Set CHATCLI_SERVER_TOKEN (or --token) for a shared token, CHATCLI_JWT_SECRET for HS256 JWTs, "+
			"CHATCLI_JWT_PUBLIC_KEY for RS256 JWTs, or CHATCLI_SERVER_TLS_CLIENT_CA (--tls-client-ca) for mTLS. "+
			"To run without authentication, bind loopback instead (CHATCLI_BIND_ADDRESS=127.0.0.1)",
		bindAddr)
}

// bindCredentialsFromEnv reads the JWT material from the environment next
// to the token and client CA the server was configured with.
func bindCredentialsFromEnv(token, clientCAFile string) bindCredentials {
	return bindCredentials{
		token:        token,
		jwtSecret:    os.Getenv("CHATCLI_JWT_SECRET"),
		jwtPublicKey: os.Getenv("CHATCLI_JWT_PUBLIC_KEY"),
		clientCAFile: clientCAFile,
	}
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
