/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/config"
	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

// Publisher trust answers "who may sign plugins this machine runs" without
// a manual key copy per plugin, and without the one shortcut that would
// defeat signatures: trusting whatever public key ships next to the binary.
// A key that travels with the plugin proves only that the bundle is
// self-consistent, so it becomes trusted solely through a decision made
// outside the bundle:
//
//   - a person confirms the publisher's fingerprint once (trust on first
//     use); later bundles from the same publisher install silently, and a
//     bundle for the same plugin signed by a different key is refused —
//     the known_hosts model;
//   - an administrator ships keys in the managed trusted-keys directory
//     next to managed.env, trusted by every user of the machine;
//   - binaries built from source on this machine, after the person
//     confirmed the install, are signed with a local machine key.

// Plugin kinds that share the trust store. The kind namespaces pins, so a
// tool plugin and a provider plugin with the same file name never share one.
const (
	KindTool     = "tool"
	KindProvider = "provider"
)

const (
	pinsFileName      = "pins.json"
	localKeyDirName   = "local-signing"
	localKeyTrustName = "local-machine"
	maxBundleBytes    = 256 << 20
	downloadTimeout   = 5 * time.Minute
)

// ErrPublisherChanged reports a bundle signed by a different key than the
// one pinned for that plugin.
var ErrPublisherChanged = errors.New("the plugin's publisher key changed")

// ErrPublisherNotTrusted reports a valid bundle whose publisher nobody on
// this machine has trusted yet.
var ErrPublisherNotTrusted = errors.New("publisher not trusted")

// Reasons a bundle was refused (PublisherError.Reason).
const (
	ReasonUnsigned      = "unsigned"
	ReasonBadSignature  = "bad_signature"
	ReasonKeyChanged    = "key_changed"
	ReasonNotTrusted    = "not_trusted"
	ReasonTrustMismatch = "trust_mismatch"
	ReasonNoBundledKey  = "no_bundled_key"
)

// PublisherError explains why a bundle was refused, with the facts a
// caller needs to word it (and to tell the person what to do next).
type PublisherError struct {
	Err         error // ErrPluginNotTrusted, ErrPublisherChanged or ErrPublisherNotTrusted
	Reason      string
	Plugin      string
	Fingerprint string // signer of the bundle
	Pinned      string // publisher pinned for the plugin
	Given       string // fingerprint passed with --trust
}

func (e *PublisherError) Error() string {
	switch e.Reason {
	case ReasonUnsigned:
		return fmt.Sprintf("%v: %s is unsigned and unsigned plugins are not allowed", e.Err, e.Plugin)
	case ReasonBadSignature:
		return fmt.Sprintf("%v: the signature of %s matches neither the bundled key nor any trusted key", e.Err, e.Plugin)
	case ReasonKeyChanged:
		return fmt.Sprintf("%v for %s: pinned %s, bundle signed by %s", e.Err, e.Plugin, e.Pinned, e.Fingerprint)
	case ReasonTrustMismatch:
		return fmt.Sprintf("%v: --trust %s does not match the signer %s", e.Err, e.Given, e.Fingerprint)
	case ReasonNoBundledKey:
		return fmt.Sprintf("%v: %s carries no publisher key to trust", e.Err, e.Plugin)
	default:
		return fmt.Sprintf("%v: %s is signed by %s; confirm it interactively or pass --trust %s", e.Err, e.Plugin, e.Fingerprint, e.Fingerprint)
	}
}

func (e *PublisherError) Unwrap() error { return e.Err }

// KeyFingerprint renders a public key the way people compare them:
// SHA256:<unpadded base64 of the digest of the raw key>, as SSH does.
func KeyFingerprint(key ed25519.PublicKey) string {
	sum := sha256.Sum256(key)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// PublicKeyFingerprint reads a public key file and returns its fingerprint
// — what a publisher announces so users can compare it before trusting.
func PublicKeyFingerprint(pubPath string) (string, error) {
	key, err := loadEd25519PublicKey(pubPath)
	if err != nil {
		return "", err
	}
	return KeyFingerprint(key), nil
}

// ManagedTrustedKeysDir is the administrator-controlled key directory next
// to the managed configuration file: /etc/chatcli/trusted-keys, or
// %ProgramData%\chatcli\trusted-keys on Windows.
func ManagedTrustedKeysDir() string { return managedKeysDir() }

// managedKeysDir is a variable so tests can point it at a temp directory.
var managedKeysDir = func() string {
	return filepath.Join(filepath.Dir(config.ManagedConfigPath()), "trusted-keys")
}

// TrustedKey is one key the verifier accepts.
type TrustedKey struct {
	Name        string
	Fingerprint string
	// Source is "user" (~/.chatcli/trusted-keys) or "managed".
	Source string
	Path   string
	key    ed25519.PublicKey
}

// ListTrustedKeys returns the user and managed trusted keys.
func ListTrustedKeys() []TrustedKey {
	var out []TrustedKey
	if dir, err := TrustedKeysDir(); err == nil {
		out = append(out, readKeyDir(dir, "user")...)
	}
	out = append(out, readKeyDir(ManagedTrustedKeysDir(), "managed")...)
	return out
}

func readKeyDir(dir, source string) []TrustedKey {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []TrustedKey
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		key, err := loadEd25519PublicKey(path)
		if err != nil {
			continue
		}
		out = append(out, TrustedKey{
			Name: strings.TrimSuffix(e.Name(), ".pub"), Fingerprint: KeyFingerprint(key),
			Source: source, Path: path, key: key,
		})
	}
	return out
}

func isTrustedFingerprint(fp string) bool {
	for _, k := range ListTrustedKeys() {
		if k.Fingerprint == fp {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------ local key

var localKeyMu sync.Mutex

// localKeyDir is ~/.chatcli/local-signing.
func localKeyDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".chatcli", localKeyDirName), nil
}

// EnsureLocalSigningKey returns this machine's signing key, creating it and
// trusting its public half on first use. It signs only binaries built here
// after a person confirmed the install.
func EnsureLocalSigningKey() (string, error) {
	localKeyMu.Lock()
	defer localKeyMu.Unlock()
	dir, err := localKeyDir()
	if err != nil {
		return "", err
	}
	keyPath := filepath.Join(dir, SigningKeyFileName)
	pubPath := filepath.Join(dir, SigningPubFileName)
	if _, err := os.Stat(keyPath); errors.Is(err, os.ErrNotExist) {
		if _, _, err := GenerateSigningKeyPair(dir); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	if _, err := TrustPublicKey(pubPath, localKeyTrustName); err != nil {
		return "", err
	}
	return keyPath, nil
}

// SignWithLocalKey signs a binary built on this machine (writes <bin>.sig).
func SignWithLocalKey(binaryPath string) error {
	keyPath, err := EnsureLocalSigningKey()
	if err != nil {
		return err
	}
	_, err = SignPlugin(binaryPath, keyPath, binaryPath+".sig")
	return err
}

// ---------------------------------------------------------------- pins

// Pin records the publisher a plugin was installed from.
type Pin struct {
	Fingerprint string    `json:"fingerprint"`
	PinnedAt    time.Time `json:"pinned_at"`
}

type pinFile struct {
	Plugins map[string]Pin `json:"plugins"`
}

var pinsMu sync.Mutex

func pinsPath() (string, error) {
	dir, err := TrustedKeysDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, pinsFileName), nil
}

func pinKey(kind, name string) string { return kind + ":" + name }

func loadPins() (pinFile, error) {
	pf := pinFile{Plugins: map[string]Pin{}}
	path, err := pinsPath()
	if err != nil {
		return pf, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- fixed file inside the trusted-keys directory
	if errors.Is(err, os.ErrNotExist) {
		return pf, nil
	}
	if err != nil {
		return pf, err
	}
	if err := json.Unmarshal(data, &pf); err != nil {
		return pf, fmt.Errorf("reading %s: %w", path, err)
	}
	if pf.Plugins == nil {
		pf.Plugins = map[string]Pin{}
	}
	return pf, nil
}

func savePins(pf pinFile) error {
	path, err := pinsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return err
	}
	return utils.AtomicWriteFile(path, data, 0o600)
}

// PinnedPublisher returns the fingerprint pinned for a plugin, or "".
func PinnedPublisher(kind, name string) string {
	pinsMu.Lock()
	defer pinsMu.Unlock()
	pf, err := loadPins()
	if err != nil {
		return ""
	}
	return pf.Plugins[pinKey(kind, name)].Fingerprint
}

// ListPins returns every pin, keyed "kind:name".
func ListPins() map[string]Pin {
	pinsMu.Lock()
	defer pinsMu.Unlock()
	pf, err := loadPins()
	if err != nil {
		return nil
	}
	return pf.Plugins
}

func setPin(kind, name, fp string) error {
	pinsMu.Lock()
	defer pinsMu.Unlock()
	pf, err := loadPins()
	if err != nil {
		return err
	}
	pf.Plugins[pinKey(kind, name)] = Pin{Fingerprint: fp, PinnedAt: time.Now().UTC()}
	return savePins(pf)
}

// -------------------------------------------------------------- bundle

// Bundle is a prebuilt plugin as distributed: the binary, its detached
// signature and, optionally, the publisher's public key.
type Bundle struct {
	Binary string
	Sig    string
	Pub    string
}

// Publisher is what evaluating a bundle concluded.
type Publisher struct {
	// Fingerprint of the key that signed the binary ("" when unsigned).
	Fingerprint string
	// Trusted is true when a user or managed key already matches.
	Trusted bool
	// Pinned is the fingerprint previously pinned for this plugin.
	Pinned string
	// Unsigned is true when the bundle carries no signature.
	Unsigned bool
}

// EvaluateBundle checks a bundle without trusting anything: the signature
// must verify against the bundled key (or an already trusted one), and the
// signer must match any pin recorded for this plugin.
func EvaluateBundle(kind, name string, b Bundle) (Publisher, error) {
	pub := Publisher{Pinned: PinnedPublisher(kind, name)}
	sigData, err := os.ReadFile(b.Sig) // #nosec G304 -- staged bundle file chosen by the installer
	if errors.Is(err, os.ErrNotExist) || b.Sig == "" {
		pub.Unsigned = true
		return pub, nil
	}
	if err != nil {
		return pub, err
	}
	digest, err := PluginDigest(b.Binary)
	if err != nil {
		return pub, err
	}
	sig, err := decodeSignature(sigData)
	if err != nil {
		return pub, err
	}

	var candidates []ed25519.PublicKey
	if b.Pub != "" {
		if k, err := loadEd25519PublicKey(b.Pub); err == nil {
			candidates = append(candidates, k)
		}
	}
	for _, k := range ListTrustedKeys() {
		candidates = append(candidates, k.key)
	}
	for _, k := range candidates {
		if ed25519.Verify(k, digest, sig) {
			pub.Fingerprint = KeyFingerprint(k)
			break
		}
	}
	if pub.Fingerprint == "" {
		return pub, &PublisherError{Err: ErrPluginNotTrusted, Reason: ReasonBadSignature, Plugin: name}
	}
	if pub.Pinned != "" && pub.Pinned != pub.Fingerprint {
		return pub, &PublisherError{Err: ErrPublisherChanged, Reason: ReasonKeyChanged, Plugin: name, Fingerprint: pub.Fingerprint, Pinned: pub.Pinned}
	}
	pub.Trusted = isTrustedFingerprint(pub.Fingerprint)
	return pub, nil
}

// decodeSignature reads the .sig format SignPlugin writes: base64 of the
// signature on the first line.
func decodeSignature(data []byte) ([]byte, error) {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0])
	sig, err := base64.StdEncoding.DecodeString(line)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("malformed signature file")
	}
	return sig, nil
}

// InstallOptions configures InstallBundle.
type InstallOptions struct {
	Kind string
	// DestDir receives the binary and its signature.
	DestDir string
	// Trust is a fingerprint the caller vouches for (unattended installs).
	Trust string
	// Confirm asks a person to trust an unknown publisher. Nil means no
	// person is present: only Trust can admit a new publisher.
	Confirm func(name string, p Publisher) bool
	// AllowUnsigned mirrors CHATCLI_ALLOW_UNSIGNED_PLUGINS.
	AllowUnsigned bool
}

// InstallResult reports what was installed.
type InstallResult struct {
	Path      string
	Publisher Publisher
	// NewlyTrusted is true when this install trusted the publisher.
	NewlyTrusted bool
}

// InstallBundle installs a prebuilt plugin from src — a local file or an
// https URL; the signature and publisher key are looked up beside it as
// <src>.sig and <src>.pub. Nothing is written to DestDir unless the
// publisher is trusted (already, by fingerprint, or by confirmation).
func InstallBundle(ctx context.Context, src string, opts InstallOptions) (InstallResult, error) {
	var res InstallResult
	stage, err := os.MkdirTemp("", "chatcli-bundle-")
	if err != nil {
		return res, err
	}
	defer func() { _ = os.RemoveAll(stage) }()

	name := bundleName(src)
	if name == "" {
		return res, fmt.Errorf("cannot derive a plugin name from %q", src)
	}
	b := Bundle{
		Binary: filepath.Join(stage, name),
		Sig:    filepath.Join(stage, name+".sig"),
		Pub:    filepath.Join(stage, name+".pub"),
	}
	if err := fetch(ctx, src, b.Binary, true); err != nil {
		return res, err
	}
	if err := fetch(ctx, src+".sig", b.Sig, false); err != nil {
		return res, err
	}
	if err := fetch(ctx, src+".pub", b.Pub, false); err != nil {
		return res, err
	}

	pub, err := EvaluateBundle(opts.Kind, name, b)
	if err != nil {
		return res, err
	}
	res.Publisher = pub
	switch {
	case pub.Unsigned:
		if !opts.AllowUnsigned {
			return res, &PublisherError{Err: ErrPluginNotTrusted, Reason: ReasonUnsigned, Plugin: name}
		}
	case pub.Trusted:
	case opts.Trust != "":
		if opts.Trust != pub.Fingerprint {
			return res, &PublisherError{Err: ErrPublisherNotTrusted, Reason: ReasonTrustMismatch, Plugin: name, Fingerprint: pub.Fingerprint, Given: opts.Trust}
		}
		res.NewlyTrusted = true
	case opts.Confirm != nil && opts.Confirm(name, pub):
		res.NewlyTrusted = true
	default:
		return res, &PublisherError{Err: ErrPublisherNotTrusted, Reason: ReasonNotTrusted, Plugin: name, Fingerprint: pub.Fingerprint}
	}

	if res.NewlyTrusted {
		if _, err := os.Stat(b.Pub); err != nil {
			return res, &PublisherError{Err: ErrPublisherNotTrusted, Reason: ReasonNoBundledKey, Plugin: name, Fingerprint: pub.Fingerprint}
		}
		keyName := "publisher-" + strings.NewReplacer("/", "_", "+", "-", ":", "-").Replace(pub.Fingerprint)
		if _, err := TrustPublicKey(b.Pub, keyName); err != nil {
			return res, err
		}
		res.Publisher.Trusted = true
	}
	if !pub.Unsigned {
		if err := setPin(opts.Kind, name, pub.Fingerprint); err != nil {
			return res, err
		}
	}

	if err := os.MkdirAll(opts.DestDir, 0o700); err != nil {
		return res, err
	}
	dest := filepath.Join(opts.DestDir, name)
	if err := copyFileMode(b.Binary, dest, 0o755); err != nil {
		return res, err
	}
	if !pub.Unsigned {
		if err := copyFileMode(b.Sig, dest+".sig", 0o644); err != nil {
			return res, err
		}
	}
	res.Path = dest
	return res, nil
}

// bundleName derives the installed file name from a path or URL.
func bundleName(src string) string {
	p := src
	if u, err := url.Parse(src); err == nil && u.Scheme != "" && u.Host != "" {
		p = u.Path
	}
	name := filepath.Base(filepath.FromSlash(p))
	if name == "." || name == "/" || name == "" || strings.HasPrefix(name, ".") || !safeKeyName(name) {
		return ""
	}
	if runtime.GOOS == "windows" && !strings.EqualFold(filepath.Ext(name), ".exe") {
		name += ".exe"
	}
	return name
}

func isRemote(src string) bool {
	u, err := url.Parse(src)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

// fetch copies src (local file or URL) to dest. Optional files that do not
// exist leave dest absent.
func fetch(ctx context.Context, src, dest string, required bool) error {
	if !isRemote(src) {
		in, err := os.Open(src) // #nosec G304 -- the operator names the bundle to install
		if errors.Is(err, os.ErrNotExist) && !required {
			return nil
		}
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		return writeLimited(in, dest)
	}
	u, _ := url.Parse(src)
	if u.Scheme != "https" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("refusing to download %s over plain http", src)
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	resp, err := utils.NewHTTPClient(zap.NewNop(), downloadTimeout).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound && !required {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", src, resp.StatusCode)
	}
	return writeLimited(resp.Body, dest)
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func writeLimited(r io.Reader, dest string) error {
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- dest is inside the installer's private staging dir
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, maxBundleBytes+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if n > maxBundleBytes {
		return fmt.Errorf("bundle file exceeds %d MiB", maxBundleBytes>>20)
	}
	return nil
}

func copyFileMode(src, dest string, mode os.FileMode) error {
	data, err := os.ReadFile(src) // #nosec G304 -- staged bundle file
	if err != nil {
		return err
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil { // #nosec G306 -- plugin binaries must be executable, signatures readable
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil { // #nosec G302 -- see above
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}
