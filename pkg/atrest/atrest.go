/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Package atrest is the single encryption-at-rest primitive for ChatCLI's
// durable stores (saved sessions, autosaves, MCP session mirrors, park
// snapshots). It is a leaf package on purpose: stores that cannot import cli
// (cli/agent/park) and cli itself share one format and one key derivation.
//
// Format: Magic + 12-byte GCM nonce + AES-256-GCM ciphertext (tag included).
// Key: HKDF-SHA256(master = SHA-256(CHATCLI_ENCRYPTION_KEY), info) → 32 bytes,
// the exact derivation the original cli.SessionEncryptor documented, so a file
// written by either path opens with the other.
//
// Policy: Seal encrypts whenever a secret is available — CHATCLI_ENCRYPTION_KEY,
// or the key provider the CLI installs at startup (a data key derived from
// the secret in the OS keychain; see keysource.go). CHATCLI_ENCRYPTION_KEY=off
// turns encryption off. Open transparently passes plaintext through, so an
// existing plaintext store keeps loading and is re-written encrypted on its
// next save (transparent migration). An encrypted file with no key present
// is an error, never silently empty.
package atrest

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/diillson/chatcli/pkg/stateroot"
	"github.com/diillson/chatcli/utils"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
)

const (
	// Magic is the header every version-1 encrypted payload starts with
	// (no additional authenticated data).
	Magic = "CHATCLI_ENC_v1\n"
	// MagicV2 heads payloads bound to their store location: the AEAD's
	// additional data is the store's relative path and tenant, so a file
	// copied from one tenant's directory into another's (or renamed) no
	// longer opens as if it belonged there. Version-1 payloads stay
	// readable and are rewritten as v2 on their next save or reseal.
	MagicV2 = "CHATCLI_ENC_v2\n"
	// ShortSecretBytes is the length under which a secret is treated as a
	// passphrase and stretched with Argon2id before key derivation; a
	// random secret of at least this many bytes goes straight to HKDF.
	ShortSecretBytes = 32
	// EnvKey is the environment variable that opts a process into
	// encryption at rest and supplies the master secret.
	EnvKey = "CHATCLI_ENCRYPTION_KEY"
	// EnvPreviousKeys lists retired secrets (comma-separated) that Open may
	// still use to read payloads sealed before a rotation. Seal never uses
	// them; re-sealing every store with the current key (Reseal) retires
	// them for good.
	EnvPreviousKeys = "CHATCLI_ENCRYPTION_KEY_PREVIOUS"
	// SessionInfo is the HKDF info string for session-class stores.
	SessionInfo = "chatcli-session-encryption"
)

// ErrKeyMissing is returned by Open when the payload is encrypted but no key
// is configured in the environment.
var ErrKeyMissing = errors.New("encrypted at rest but " + EnvKey + " is not set")

// Encryptor seals and opens payloads with one derived AES-256 key.
type Encryptor struct {
	key []byte
}

// NewFromMaster derives an Encryptor from master key material via
// HKDF-SHA256 with the given info string.
func NewFromMaster(master []byte, info string) (*Encryptor, error) {
	if len(master) == 0 {
		return nil, errors.New("atrest: empty master key")
	}
	derived := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, master, nil, []byte(info)), derived); err != nil {
		return nil, fmt.Errorf("atrest: key derivation failed: %w", err)
	}
	return &Encryptor{key: derived}, nil
}

// Encrypt returns Magic + nonce + ciphertext for plaintext.
func (e *Encryptor) Encrypt(plaintext []byte) ([]byte, error) {
	gcm, err := e.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("atrest: nonce generation failed: %w", err)
	}
	out := make([]byte, 0, len(Magic)+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, Magic...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

// Decrypt validates the header and returns the plaintext. A wrong key or a
// tampered payload fails authentication.
func (e *Encryptor) Decrypt(data []byte) ([]byte, error) {
	if !IsEncrypted(data) {
		return nil, errors.New("atrest: not an encrypted payload (missing magic header)")
	}
	gcm, err := e.gcm()
	if err != nil {
		return nil, err
	}
	body := data[len(Magic):]
	if len(body) < gcm.NonceSize() {
		return nil, errors.New("atrest: payload too short for nonce")
	}
	nonce, ciphertext := body[:gcm.NonceSize()], body[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("atrest: decryption failed (tampered or wrong key): %w", err)
	}
	return plaintext, nil
}

func (e *Encryptor) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return nil, fmt.Errorf("atrest: cipher init failed: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("atrest: GCM init failed: %w", err)
	}
	return gcm, nil
}

// IsEncrypted reports whether data carries the encrypted-payload header.
func IsEncrypted(data []byte) bool {
	return (len(data) >= len(Magic) && string(data[:len(Magic)]) == Magic) || isV2(data)
}

// Enabled reports whether the process opted into encryption at rest.
func Enabled() bool {
	return currentSecret() != ""
}

// fromEnv builds the version-1 Encryptor from CHATCLI_ENCRYPTION_KEY
// (SHA-256 of the secret into HKDF). Re-derived per call: the derivation
// costs microseconds and reading the environment each time keeps
// behavior consistent with a runtime reload.
func fromEnv() (*Encryptor, error) {
	secret := readSecret()
	if secret == "" {
		return nil, missingKey()
	}
	return legacyEncryptor(secret)
}

// legacyEncryptor is the v1 derivation for a secret.
func legacyEncryptor(secret string) (*Encryptor, error) {
	master := sha256.Sum256([]byte(secret))
	return NewFromMaster(master[:], SessionInfo)
}

// v2Encryptor derives the version-2 key: a random secret of at least
// ShortSecretBytes goes to HKDF as before; a shorter one is a passphrase
// and is stretched with Argon2id (64 MiB, 3 passes) first, cached per
// secret because the stretch costs real time.
func v2Encryptor(secret string) (*Encryptor, error) {
	if len(secret) >= ShortSecretBytes {
		master := sha256.Sum256([]byte(secret))
		return NewFromMaster(master[:], SessionInfo)
	}
	stretchMu.Lock()
	master, ok := stretched[secret]
	stretchMu.Unlock()
	if !ok {
		master = argon2.IDKey([]byte(secret), []byte(argonSalt), 3, 64*1024, 4, 32)
		stretchMu.Lock()
		stretched[secret] = master
		stretchMu.Unlock()
	}
	return NewFromMaster(master, SessionInfo)
}

// argonSalt is the fixed application salt for passphrase stretching (the
// secret is per installation; the salt separates this use from any other
// Argon2 use of the same passphrase).
const argonSalt = "chatcli-atrest-v2-passphrase"

var (
	stretchMu sync.Mutex
	stretched = map[string][]byte{}
)

// aadFor derives the additional authenticated data that binds a payload
// to its store: the tenant slug and the last path components under the
// state root. "" (no path) binds nothing — the v2 format without a
// location, still stretched and authenticated.
func aadFor(path string) []byte {
	if path == "" {
		return nil
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	tenant := ""
	rel := clean
	if i := strings.Index(clean, "/tenants/"); i >= 0 {
		rest := clean[i+len("/tenants/"):]
		if j := strings.IndexByte(rest, '/'); j > 0 {
			tenant, rel = rest[:j], rest[j+1:]
		}
	} else if rest, ok := underCustomRoot(clean); ok {
		// CHATCLI_STATE_DIR: the same path relative to the root as under
		// ~/.chatcli, so a state directory copied between roots still opens.
		rel = rest
	} else if i := strings.Index(clean, "/.chatcli/"); i >= 0 {
		rel = clean[i+len("/.chatcli/"):]
	} else {
		// Custom roots: bind to the store's own directory and name.
		rel = filepath.ToSlash(filepath.Join(filepath.Base(filepath.Dir(clean)), filepath.Base(clean)))
	}
	return []byte("v2\x00" + rel + "\x00" + tenant)
}

// underCustomRoot returns clean relative to CHATCLI_STATE_DIR when that is
// set and contains it.
func underCustomRoot(clean string) (string, bool) {
	if !stateroot.Custom() {
		return "", false
	}
	root := filepath.ToSlash(filepath.Clean(stateroot.Under(""))) + "/"
	if !strings.HasPrefix(clean, root) {
		return "", false
	}
	return clean[len(root):], true
}

// SealAt encrypts plaintext bound to the store at path (version 2). A
// no-op when encryption at rest is off.
func SealAt(path string, plaintext []byte) ([]byte, error) {
	if !Enabled() {
		return plaintext, nil
	}
	enc, err := v2Encryptor(currentSecret())
	if err != nil {
		return nil, err
	}
	return enc.encryptV2(plaintext, aadFor(path))
}

// OpenAt decrypts a payload sealed for the store at path: version 2 with
// the current or a retired key, version 1 through Open. Plaintext passes
// through.
func OpenAt(path string, data []byte) ([]byte, error) {
	if !isV2(data) {
		return Open(data)
	}
	secret := readSecret()
	if secret == "" {
		return nil, missingKey()
	}
	// Bound to this path first; a payload sealed without a location (Seal,
	// or a store that has not been resealed at its path yet) carries no
	// binding and opens with the empty AAD.
	aads := [][]byte{aadFor(path)}
	if path != "" {
		aads = append(aads, nil)
	}
	enc, err := v2Encryptor(secret)
	if err != nil {
		return nil, err
	}
	var firstErr error
	for _, aad := range aads {
		plain, derr := enc.decryptV2(data, aad)
		if derr == nil {
			return plain, nil
		}
		if firstErr == nil {
			firstErr = derr
		}
	}
	for _, old := range previousSecrets() {
		oe, derr := v2Encryptor(old)
		if derr != nil {
			continue
		}
		for _, aad := range aads {
			if plain, perr := oe.decryptV2(data, aad); perr == nil {
				return plain, nil
			}
		}
	}
	return nil, firstErr
}

func isV2(data []byte) bool {
	return len(data) >= len(MagicV2) && string(data[:len(MagicV2)]) == MagicV2
}

// encryptV2 returns MagicV2 + nonce + ciphertext authenticated with aad.
func (e *Encryptor) encryptV2(plaintext, aad []byte) ([]byte, error) {
	gcm, err := e.gcm()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("atrest: nonce generation failed: %w", err)
	}
	out := make([]byte, 0, len(MagicV2)+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, MagicV2...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, aad), nil
}

// decryptV2 validates the v2 header and opens the payload with aad.
func (e *Encryptor) decryptV2(data, aad []byte) ([]byte, error) {
	if !isV2(data) {
		return nil, errors.New("atrest: not a version-2 payload")
	}
	gcm, err := e.gcm()
	if err != nil {
		return nil, err
	}
	body := data[len(MagicV2):]
	if len(body) < gcm.NonceSize() {
		return nil, errors.New("atrest: payload too short for nonce")
	}
	nonce, ciphertext := body[:gcm.NonceSize()], body[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("atrest: decryption failed (tampered, wrong key, or moved between stores): %w", err)
	}
	return plain, nil
}

// Seal encrypts plaintext when encryption at rest is enabled and returns it
// unchanged otherwise. Stores call it right before writing.
func Seal(plaintext []byte) ([]byte, error) {
	if !Enabled() {
		return plaintext, nil
	}
	enc, err := v2Encryptor(currentSecret())
	if err != nil {
		return nil, err
	}
	return enc.encryptV2(plaintext, nil)
}

// Open decrypts an encrypted payload and passes plaintext through untouched.
// Stores call it right after reading, so a plaintext file written before the
// key was configured keeps loading (and is sealed on its next save). After a
// rotation, payloads sealed with a retired key listed in EnvPreviousKeys
// still open; they are sealed with the current key on their next save.
func Open(data []byte) ([]byte, error) {
	if !IsEncrypted(data) {
		return data, nil
	}
	if isV2(data) {
		return OpenAt("", data)
	}
	enc, err := fromEnv()
	if err != nil {
		return nil, err
	}
	plain, err := enc.Decrypt(data)
	if err == nil {
		return plain, nil
	}
	for _, secret := range previousSecrets() {
		master := sha256.Sum256([]byte(secret))
		old, derr := NewFromMaster(master[:], SessionInfo)
		if derr != nil {
			continue
		}
		if plain, perr := old.Decrypt(data); perr == nil {
			return plain, nil
		}
	}
	return nil, err
}

// previousSecrets parses EnvPreviousKeys (comma-separated, blanks
// skipped). While CHATCLI_ENCRYPTION_KEY names a secret, the provider's
// secret (the keychain key) is a retired key too: moving from the default
// key to one of your own is set the variable, then reseal.
func previousSecrets() []string {
	var out []string
	if v := os.Getenv(EnvKey); v != "" && !isOffValue(v) {
		if secret, _, err := providedSecret(); err == nil && secret != "" && secret != v {
			out = append(out, secret)
		}
	}
	for _, s := range strings.Split(os.Getenv(EnvPreviousKeys), ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return append(out, fallbackSecrets()...)
}

// SealedWithCurrentKey reports whether data opens with the current key
// alone (false for plaintext and for payloads only a retired key opens).
func SealedWithCurrentKey(data []byte) bool {
	return sealedWithCurrentKeyAt("", data)
}

// sealedWithCurrentKeyAt is SealedWithCurrentKey for a payload bound to
// path; a version-1 payload is never "current" (it lacks the binding).
func sealedWithCurrentKeyAt(path string, data []byte) bool {
	if !isV2(data) {
		return false
	}
	enc, err := v2Encryptor(currentSecret())
	if err != nil {
		return false
	}
	_, err = enc.decryptV2(data, aadFor(path))
	return err == nil
}

// KeyFingerprint identifies the current key for display (first 8 hex of
// SHA-256 of the derived key), "" when encryption is off.
func KeyFingerprint() string {
	if !Enabled() {
		return ""
	}
	enc, err := fromEnv()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(enc.key)
	return fmt.Sprintf("%x", sum[:4])
}

// ResealFile rewrites path sealed with the current key when it is plaintext
// or sealed with a retired key. Returns whether the file was rewritten. A
// file that cannot be opened with any known key is left untouched and
// reported as an error.
func ResealFile(path string) (bool, error) {
	if !Enabled() {
		return false, ErrKeyMissing
	}
	before, err := os.Stat(path) // #nosec G703 -- operator-owned store path
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path) // #nosec G304 G703 -- store path enumerated by the caller's walk of the operator-owned state root, never model or network input
	if err != nil {
		return false, err
	}
	if len(data) == 0 || sealedWithCurrentKeyAt(path, data) {
		return false, nil
	}
	plain, err := OpenAt(path, data)
	if err != nil {
		return false, err
	}
	sealed, err := SealAt(path, plain)
	if err != nil {
		return false, err
	}
	info, err := os.Stat(path) // #nosec G703 -- operator-owned store path
	if err != nil {
		return false, err
	}
	if !sameContent(info, before) {
		return false, ErrChangedDuringReseal
	}
	// fsync'd temp + rename: a power loss between the write and the rename
	// must never leave an empty store behind the old name.
	if err := utils.AtomicWriteFile(path, sealed, info.Mode().Perm()); err != nil {
		return false, err
	}
	keepModTime(path, before)
	return true, nil
}

// keepModTime gives a resealed file back the modification time it had:
// the content did not change, and session recency, retention TTLs and
// "continue the last session" all read it.
func keepModTime(path string, before os.FileInfo) {
	_ = os.Chtimes(path, before.ModTime(), before.ModTime()) // #nosec G703 -- store path enumerated by the caller's walk of the state root
}

// ErrChangedDuringReseal reports a file another writer replaced while it
// was being resealed. It is left as that writer saved it — sealed by the
// writer itself when encryption is on — and the next pass picks it up.
var ErrChangedDuringReseal = errors.New("store changed while it was being resealed; left for the next pass")

// sameContent reports whether a file still has the size and modification
// time it had when it was read.
func sameContent(now, before os.FileInfo) bool {
	return before != nil && now.Size() == before.Size() && now.ModTime().Equal(before.ModTime())
}

// ResealLines rewrites a line-oriented store (one sealed record per line,
// prefixed by linePrefix and base64-encoded) with the current key. Lines
// that are plaintext or already current are copied as they are.
func ResealLines(path, linePrefix string) (int, error) {
	return rewriteLines(path, linePrefix, func(i int, line string) (string, bool, error) {
		if !strings.HasPrefix(line, linePrefix) {
			return line, false, nil
		}
		return resealLine(path, linePrefix, i, line, true)
	})
}

// SealLines seals every record of a line-oriented store with the current
// key: plaintext lines are sealed (the migration of a store written before
// encryption was on) and lines sealed with a retired key move to the
// current one. bound selects the path binding: true seals with SealAt(path),
// false with Seal, matching how the store's own writer seals. Blank lines
// are kept. Returns how many lines were rewritten.
func SealLines(path, linePrefix string, bound bool) (int, error) {
	at := ""
	if bound {
		at = path
	}
	return rewriteLines(path, linePrefix, func(i int, line string) (string, bool, error) {
		if strings.HasPrefix(line, linePrefix) {
			return resealLine(at, linePrefix, i, line, bound)
		}
		if strings.TrimSpace(line) == "" {
			return line, false, nil
		}
		sealed, err := SealAt(at, []byte(line))
		if err != nil {
			return line, false, err
		}
		return linePrefix + base64.StdEncoding.EncodeToString(sealed), true, nil
	})
}

// resealLine moves one sealed line to the current key; at is the path the
// line is bound to ("" for unbound). A line already current, or one whose
// payload is not base64, is kept. skipBad keeps ResealLines' tolerance of
// undecodable lines.
func resealLine(at, linePrefix string, i int, line string, skipBad bool) (string, bool, error) {
	raw, err := base64.StdEncoding.DecodeString(line[len(linePrefix):])
	if err != nil {
		if skipBad {
			return line, false, nil
		}
		return line, false, fmt.Errorf("line %d: %w", i+1, err)
	}
	if sealedWithCurrentKeyAt(at, raw) {
		return line, false, nil
	}
	plain, err := OpenAt(at, raw)
	if err != nil {
		return line, false, fmt.Errorf("line %d: %w", i+1, err)
	}
	sealed, err := SealAt(at, plain)
	if err != nil {
		return line, false, err
	}
	return linePrefix + base64.StdEncoding.EncodeToString(sealed), true, nil
}

// rewriteLines applies fn to every line of path and atomically replaces the
// file when any line changed. An error from fn aborts with the file intact.
func rewriteLines(path, linePrefix string, fn func(i int, line string) (string, bool, error)) (int, error) {
	if !Enabled() {
		return 0, ErrKeyMissing
	}
	before, err := os.Stat(path) // #nosec G703 -- operator-owned store path
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(path) // #nosec G304 G703 -- store path enumerated by the caller's walk of the operator-owned state root, never model or network input
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(data), "\n")
	changed := 0
	for i, line := range lines {
		out, did, err := fn(i, line)
		if err != nil {
			return 0, err
		}
		if did {
			lines[i] = out
			changed++
		}
	}
	if changed == 0 {
		return 0, nil
	}
	info, err := os.Stat(path) // #nosec G703 -- operator-owned store path
	if err != nil {
		return 0, err
	}
	if !sameContent(info, before) {
		return 0, ErrChangedDuringReseal
	}
	if err := utils.AtomicWriteFile(path, []byte(strings.Join(lines, "\n")), info.Mode().Perm()); err != nil {
		return 0, err
	}
	keepModTime(path, before)
	return changed, nil
}
