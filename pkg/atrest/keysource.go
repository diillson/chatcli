/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package atrest

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/utils"
)

// Where the secret that seals the stores comes from, in order:
//
//  1. CHATCLI_ENCRYPTION_KEY set to a secret: that secret. Servers, the
//     gateway and Kubernetes deployments inject it from a secret store.
//  2. CHATCLI_ENCRYPTION_KEY set to "off" (or none, disabled, false, 0):
//     encryption at rest is turned off and stores are written in plaintext;
//     files sealed earlier still open with the provider's key.
//  3. Unset: the key provider the process installed at startup. The CLI
//     installs one that derives a data key from the secret it keeps in the
//     OS keychain, so a user's stores are encrypted with no configuration.
//     The provider runs once per process and its answer is cached; a
//     failure is asked again after providerRetry.
//
// With no provider installed (library use, tests) an unset variable means
// plaintext, as before.

// KeySource names where the active secret came from.
type KeySource string

const (
	// KeySourceEnv is a secret from CHATCLI_ENCRYPTION_KEY.
	KeySourceEnv KeySource = "env"
	// KeySourceProvider is a secret from the installed key provider.
	KeySourceProvider KeySource = "provider"
	// KeySourceOff means encryption at rest was turned off explicitly.
	KeySourceOff KeySource = "off"
	// KeySourceNone means no secret: no variable and no provider.
	KeySourceNone KeySource = "none"
	// KeySourceUnavailable means the provider failed: stores are written in
	// plaintext for this process, and sealed ones cannot be opened.
	KeySourceUnavailable KeySource = "unavailable"
)

// KeyProvider returns the secret to seal stores with when
// CHATCLI_ENCRYPTION_KEY is unset, and a short label of where it is kept
// (for example "keychain" or "file").
type KeyProvider func() (secret, where string, err error)

var (
	providerMu    sync.Mutex
	provider      KeyProvider
	providerDone  bool
	providerValue string
	providerWhere string
	providerErr   error
	providerAt    time.Time
)

// providerRetry is how long a failed provider answer is kept before the
// provider is asked again: a daemon started while the keychain was locked
// starts sealing once it is unlocked, instead of staying in plaintext for
// the life of the process.
const providerRetry = 30 * time.Second

// KeyFallbacks returns read-only secrets for payloads sealed while another
// key was active (a key file that changed under the stores, the keychain
// entry and a file both present). They are tried after the active key and
// the retired ones; writes never use them.
type KeyFallbacks func() []string

var (
	fallbacksMu    sync.Mutex
	fallbacks      KeyFallbacks
	fallbacksDone  bool
	fallbacksValue []string
)

// SetKeyFallbacks installs the fallback source (nil removes it) and forgets
// any cached answer.
func SetKeyFallbacks(f KeyFallbacks) {
	fallbacksMu.Lock()
	defer fallbacksMu.Unlock()
	fallbacks, fallbacksDone, fallbacksValue = f, false, nil
}

// fallbackSecrets runs the fallback source once (only when a payload did
// not open with the active key) and caches its answer.
func fallbackSecrets() []string {
	fallbacksMu.Lock()
	defer fallbacksMu.Unlock()
	if fallbacks == nil {
		return nil
	}
	if !fallbacksDone {
		fallbacksValue, fallbacksDone = fallbacks(), true
	}
	return fallbacksValue
}

// SetKeyProvider installs the provider consulted when CHATCLI_ENCRYPTION_KEY
// is unset (nil removes it) and forgets any cached answer.
func SetKeyProvider(p KeyProvider) {
	providerMu.Lock()
	defer providerMu.Unlock()
	provider = p
	providerDone, providerValue, providerWhere, providerErr = false, "", "", nil
}

// isOffValue reports whether v turns encryption at rest off.
func isOffValue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "none", "disabled", "false", "0":
		return true
	}
	return false
}

// providedSecret runs the provider once and caches its answer.
func providedSecret() (string, string, error) {
	providerMu.Lock()
	defer providerMu.Unlock()
	if provider == nil {
		return "", "", nil
	}
	if !providerDone || (providerErr != nil && time.Since(providerAt) > providerRetry) {
		providerValue, providerWhere, providerErr = provider()
		providerDone, providerAt = true, time.Now()
	}
	return providerValue, providerWhere, providerErr
}

// currentSecret returns the secret stores are sealed with, "" when
// encryption at rest is off for this process.
func currentSecret() string {
	v := os.Getenv(EnvKey)
	switch {
	case isOffValue(v):
		return ""
	case v != "":
		return v
	}
	secret, _, err := providedSecret()
	if err != nil {
		return ""
	}
	return secret
}

// readSecret returns the secret payloads are opened with. Unlike
// currentSecret it ignores "off": turning encryption off stops sealing new
// writes, and what was sealed before keeps opening with the provider's key.
func readSecret() string {
	v := os.Getenv(EnvKey)
	if v != "" && !isOffValue(v) {
		return v
	}
	secret, _, err := providedSecret()
	if err != nil {
		return ""
	}
	return secret
}

// missingKey is ErrKeyMissing with the reason the provider gave, when it
// failed: "the keychain is locked" tells the user what to do, "the key is
// not set" does not.
func missingKey() error {
	if _, _, err := providedSecret(); err != nil {
		return fmt.Errorf("%w (key provider: %w)", ErrKeyMissing, err)
	}
	return ErrKeyMissing
}

// KeyStatus reports where the active secret comes from, where the provider
// keeps it ("" unless KeySourceProvider) and, for KeySourceUnavailable, why.
func KeyStatus() (KeySource, string, error) {
	v := os.Getenv(EnvKey)
	switch {
	case isOffValue(v):
		return KeySourceOff, "", nil
	case v != "":
		return KeySourceEnv, "", nil
	}
	secret, where, err := providedSecret()
	switch {
	case err != nil:
		return KeySourceUnavailable, "", err
	case secret == "":
		return KeySourceNone, "", nil
	default:
		return KeySourceProvider, where, nil
	}
}

// ReadFile reads path and opens it when it is sealed (bound to path or
// not); plaintext passes through. It is for readers of a file that may be
// a sealed store — an attachment handed to a tool, a daily note — and
// returns an error rather than ciphertext when the payload cannot open.
func ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- callers pass a path their own boundary checks admitted
	if err != nil {
		return nil, err
	}
	if !IsEncrypted(data) {
		return data, nil
	}
	return OpenAt(path, data)
}

// UnsealFile rewrites a sealed store file as plaintext: the way back to a
// version that cannot read sealed stores, after encryption was turned off.
// A plaintext file is left as it is. Returns whether the file was rewritten.
func UnsealFile(path string) (bool, error) {
	before, err := os.Stat(path) // #nosec G703 -- store path enumerated by the caller's walk of the state root
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path) // #nosec G304 G703 -- store path enumerated by the caller's walk of the state root
	if err != nil || !IsEncrypted(data) {
		return false, err
	}
	plain, err := OpenAt(path, data)
	if err != nil {
		return false, err
	}
	return true, replaceIfUnchanged(path, before, plain)
}

// UnsealLines rewrites every sealed line of a line-oriented store as
// plaintext; bound says whether its lines are bound to path.
func UnsealLines(path, linePrefix string, bound bool) (int, error) {
	at := ""
	if bound {
		at = path
	}
	before, err := os.Stat(path) // #nosec G703 -- store path enumerated by the caller's walk of the state root
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path) // #nosec G304 G703 -- store path enumerated by the caller's walk of the state root
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	changed := 0
	for i, line := range lines {
		if !strings.HasPrefix(line, linePrefix) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(line[len(linePrefix):])
		if err != nil {
			return 0, fmt.Errorf("line %d: %w", i+1, err)
		}
		plain, err := OpenAt(at, raw)
		if err != nil {
			return 0, fmt.Errorf("line %d: %w", i+1, err)
		}
		// A record holding a newline (a multi-line paste in the prompt
		// history) would come back as several records: it stays sealed,
		// and keeps opening with the key.
		if strings.ContainsAny(string(plain), "\r\n") {
			continue
		}
		lines[i] = string(plain)
		changed++
	}
	if changed == 0 {
		return 0, nil
	}
	return changed, replaceIfUnchanged(path, before, []byte(strings.Join(lines, "\n")))
}

// replaceIfUnchanged atomically replaces path with data unless another
// writer replaced it since before was taken.
func replaceIfUnchanged(path string, before os.FileInfo, data []byte) error {
	info, err := os.Stat(path) // #nosec G703 -- store path enumerated by the caller's walk of the state root
	if err != nil {
		return err
	}
	if !sameContent(info, before) {
		return ErrChangedDuringReseal
	}
	if err := utils.AtomicWriteFile(path, data, info.Mode().Perm()); err != nil {
		return err
	}
	keepModTime(path, before)
	return nil
}

// AttachmentsDirName is the store directory saved attachments live in.
const AttachmentsDirName = "attachments"

// ReadAttachment is ReadFile for paths a tool was handed: it opens a sealed
// file only when it sits directly in an attachments store — the images and
// PDFs ChatCLI saved for a turn and pointed the model at. Anything else is
// returned as it is on disk, so a tool call aimed at a session, memory or
// context store reads ciphertext, never the conversation.
func ReadAttachment(path string) ([]byte, error) {
	if filepath.Base(filepath.Dir(filepath.Clean(path))) == AttachmentsDirName {
		return ReadFile(path)
	}
	return os.ReadFile(path) // #nosec G304 -- callers pass a path their own boundary checks admitted
}

// RecoveryKey returns the secret the default key provider seals with, for
// the user to keep outside this machine: set as CHATCLI_ENCRYPTION_KEY on
// any machine, it opens every store sealed with the default key. It is
// available only while the provider is the active key source.
func RecoveryKey() (string, error) {
	src, _, err := KeyStatus()
	switch src {
	case KeySourceProvider:
		secret, _, perr := providedSecret()
		return secret, perr
	case KeySourceUnavailable:
		return "", err
	default:
		return "", ErrNoRecoveryKey
	}
}

// ErrNoRecoveryKey reports that no default key is active: encryption is
// off, or the key already comes from CHATCLI_ENCRYPTION_KEY.
var ErrNoRecoveryKey = errors.New("no default key is active")

// FingerprintFor is KeyFingerprint for a given secret: the fingerprint the
// stores would show if that secret were the active key.
func FingerprintFor(secret string) string {
	enc, err := legacyEncryptor(secret)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(enc.key)
	return fmt.Sprintf("%x", sum[:4])
}

// OpensWith reports whether payload (sealed for the store at path) opens
// with secret, without touching the active key: a way to check which key
// sealed a store before changing where the key lives.
func OpensWith(secret, path string, payload []byte) bool {
	if isV2(payload) {
		enc, err := v2Encryptor(secret)
		if err != nil {
			return false
		}
		for _, aad := range [][]byte{aadFor(path), nil} {
			if _, err := enc.decryptV2(payload, aad); err == nil {
				return true
			}
		}
		return false
	}
	if !IsEncrypted(payload) {
		return false
	}
	enc, err := legacyEncryptor(secret)
	if err != nil {
		return false
	}
	_, err = enc.Decrypt(payload)
	return err == nil
}
