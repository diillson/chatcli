/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package cli

import (
	"github.com/diillson/chatcli/auth"
	"github.com/diillson/chatcli/i18n"
)

// keychainBackendInEffect reports where the credential-encryption key
// actually lives on this machine.
//
// The setting alone does not answer that: "auto" resolves differently
// depending on whether a native store is present, and even "keychain" falls
// back to the file when none is. Asking the store itself is the only
// honest answer, and the reason to print it is that this setting used to
// have no effect at all.
func keychainBackendInEffect() string {
	if auth.NewKeychainStore().IsNativeAvailable() {
		return i18n.T("cfg.value.keychain_native")
	}
	return i18n.T("cfg.value.keychain_file")
}
