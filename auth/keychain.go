/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auth

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// KeychainBackend specifies which keychain to use.
type KeychainBackend string

const (
	KeychainAuto   KeychainBackend = "auto"
	KeychainNative KeychainBackend = "keychain"
	KeychainFile   KeychainBackend = "file"
)

const keychainServiceName = "chatcli"

// KeychainStore provides cross-platform secure credential storage.
type KeychainStore struct {
	backend KeychainBackend
}

// NewKeychainStore creates a keychain store configured from CHATCLI_KEYCHAIN_BACKEND env.
// Supported: "auto" (default), "keychain" (force native), "file" (force file-based).
func NewKeychainStore() *KeychainStore {
	backend := KeychainAuto
	if v := os.Getenv("CHATCLI_KEYCHAIN_BACKEND"); v != "" {
		switch strings.ToLower(v) {
		case "keychain", "native":
			backend = KeychainNative
		case "file":
			backend = KeychainFile
		default:
			backend = KeychainAuto
		}
	}

	return &KeychainStore{backend: backend}
}

// Get retrieves a value from the keychain.
func (ks *KeychainStore) Get(account string) ([]byte, error) {
	if ks.useNative() {
		val, err := ks.nativeGet(account)
		if err == nil {
			return val, nil
		}
		// Fall back to file if native fails
		if ks.backend == KeychainAuto {
			return nil, err // let caller handle file-based fallback
		}
		return nil, err
	}
	return nil, fmt.Errorf("keychain: file backend — use auth/crypto.go directly")
}

// Set stores a value in the keychain.
func (ks *KeychainStore) Set(account string, data []byte) error {
	if ks.useNative() {
		return ks.nativeSet(account, data)
	}
	return fmt.Errorf("keychain: file backend — use auth/crypto.go directly")
}

// Delete removes a value from the keychain.
func (ks *KeychainStore) Delete(account string) error {
	if ks.useNative() {
		return ks.nativeDelete(account)
	}
	return fmt.Errorf("keychain: file backend — use auth/crypto.go directly")
}

// IsNativeAvailable returns true if native keychain is usable.
func (ks *KeychainStore) IsNativeAvailable() bool {
	return ks.useNative()
}

func (ks *KeychainStore) useNative() bool {
	if ks.backend == KeychainFile {
		return false
	}
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("security")
		return err == nil
	case "linux":
		_, err := exec.LookPath("secret-tool")
		return err == nil
	default:
		return false
	}
}

// --- macOS Keychain (security CLI) ---

func (ks *KeychainStore) nativeGet(account string) ([]byte, error) {
	switch runtime.GOOS {
	case "darwin":
		return ks.macGet(account)
	case "linux":
		return ks.linuxGet(account)
	default:
		return nil, fmt.Errorf("native keychain not supported on %s", runtime.GOOS)
	}
}

func (ks *KeychainStore) nativeSet(account string, data []byte) error {
	switch runtime.GOOS {
	case "darwin":
		return ks.macSet(account, data)
	case "linux":
		return ks.linuxSet(account, data)
	default:
		return fmt.Errorf("native keychain not supported on %s", runtime.GOOS)
	}
}

func (ks *KeychainStore) nativeDelete(account string) error {
	switch runtime.GOOS {
	case "darwin":
		return ks.macDelete(account)
	case "linux":
		return ks.linuxDelete(account)
	default:
		return fmt.Errorf("native keychain not supported on %s", runtime.GOOS)
	}
}

// macOS: uses `security` CLI for Keychain access
func (ks *KeychainStore) macGet(account string) ([]byte, error) {
	cmd := exec.Command("security", "find-generic-password", // #nosec G204 -- args are controlled constants
		"-s", keychainServiceName, "-a", account, "-w")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("keychain get failed: %w", err)
	}
	return []byte(strings.TrimSpace(string(out))), nil
}

func (ks *KeychainStore) macSet(account string, data []byte) error {
	// Delete first (update = delete + add in macOS keychain)
	_ = ks.macDelete(account) // ignore error if not exists

	cmd := exec.Command("security", "add-generic-password", // #nosec G204
		"-s", keychainServiceName, "-a", account,
		"-w", string(data), "-U")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain set failed: %w", err)
	}
	return nil
}

func (ks *KeychainStore) macDelete(account string) error {
	cmd := exec.Command("security", "delete-generic-password", // #nosec G204
		"-s", keychainServiceName, "-a", account)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain delete failed: %w", err)
	}
	return nil
}

// Linux: uses `secret-tool` from libsecret
func (ks *KeychainStore) linuxGet(account string) ([]byte, error) {
	cmd := exec.Command("secret-tool", "lookup", // #nosec G204
		"service", keychainServiceName, "account", account)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("secret-tool lookup failed: %w", err)
	}
	return out, nil
}

func (ks *KeychainStore) linuxSet(account string, data []byte) error {
	cmd := exec.Command("secret-tool", "store", // #nosec G204
		"--label", fmt.Sprintf("ChatCLI: %s", account),
		"service", keychainServiceName, "account", account)
	cmd.Stdin = strings.NewReader(string(data))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret-tool store failed: %w", err)
	}
	return nil
}

func (ks *KeychainStore) linuxDelete(account string) error {
	cmd := exec.Command("secret-tool", "clear", // #nosec G204
		"service", keychainServiceName, "account", account)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret-tool clear failed: %w", err)
	}
	return nil
}
