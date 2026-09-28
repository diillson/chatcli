/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cmd

import (
	"strings"
	"testing"
)

// namesFlag reports whether err mentions flag (as rendered text, or as the
// i18n key when the catalog is not loaded in this test binary).
func namesFlag(err error, flag string) bool {
	if err == nil {
		return false
	}
	key := "cmd.server.tls_" + strings.TrimPrefix(flag, "--tls-") + "_missing"
	return strings.Contains(err.Error(), flag) || strings.Contains(err.Error(), key)
}

// A certificate without its key (or the reverse) is a misconfiguration, not
// a request for plaintext: the server used to fall through to a plaintext
// listener without a word.
func TestValidateTLSPair(t *testing.T) {
	if err := validateTLSPair("", ""); err != nil {
		t.Fatalf("neither set is plaintext on purpose: %v", err)
	}
	if err := validateTLSPair("/etc/chatcli/tls/tls.crt", "/etc/chatcli/tls/tls.key"); err != nil {
		t.Fatalf("both set: %v", err)
	}
	err := validateTLSPair("/etc/chatcli/tls/tls.crt", "")
	if !namesFlag(err, "--tls-key") {
		t.Fatalf("cert without key: got %v", err)
	}
	err = validateTLSPair("", "/etc/chatcli/tls/tls.key")
	if !namesFlag(err, "--tls-cert") {
		t.Fatalf("key without cert: got %v", err)
	}
}

// RunServer refuses the half pair before touching the LLM manager or the
// listener, so a nil manager is enough to prove the check runs first.
func TestRunServer_RefusesCertWithoutKey(t *testing.T) {
	t.Setenv("CHATCLI_SERVER_TLS_CERT", "")
	t.Setenv("CHATCLI_SERVER_TLS_KEY", "")
	err := RunServer([]string{"--tls-cert", "/etc/chatcli/tls/tls.crt"}, nil, nil)
	if !namesFlag(err, "--tls-key") {
		t.Fatalf("got %v, want the missing-key error", err)
	}
}
