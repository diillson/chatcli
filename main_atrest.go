/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package main

import (
	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/pkg/atrest"
)

// installAtRestKeyProvider turns encryption at rest on by default: with
// CHATCLI_ENCRYPTION_KEY unset, the stores are sealed with a data key
// derived from the secret ChatCLI keeps in the OS keychain (the file key
// where no keychain exists). CHATCLI_ENCRYPTION_KEY still wins, and "off"
// turns it off.
//
// `chatcli server` is left out: a server's stores are shared by its
// clients and usually live on a volume, and a key generated inside a
// container would be lost with it. Servers take the key from a secret
// through CHATCLI_ENCRYPTION_KEY (the operator wires one).
func installAtRestKeyProvider(args []string) {
	if len(args) > 0 {
		switch args[0] {
		case "server", "serve":
			return
		}
	}
	atrest.SetKeyProvider(auth.DataKeySecret)
	atrest.SetKeyFallbacks(auth.DataKeyFallbacks)
}
