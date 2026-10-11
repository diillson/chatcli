/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

// Command releasesign signs and verifies release artifacts with Ed25519.
// It is a build-time tool for the release workflow, not part of the chatcli
// binary. The contract the self-updater relies on (update/release_keys.go):
//
//	releasesign keygen
//	    prints a new seed (standard base64 of 32 bytes) to stdout and the
//	    public key (standard base64 of 32 bytes) to stderr, so the seed can
//	    be piped into `gh secret set CHATCLI_RELEASE_SIGNING_KEY` unseen.
//	releasesign sign <file>
//	    reads the seed from CHATCLI_RELEASE_SIGNING_KEY and writes
//	    <file>.sig: the standard base64 of the 64-byte Ed25519 signature
//	    over the exact bytes of <file>.
//	releasesign verify <file> <sig> <pubkey-b64>
//	    exits 0 only when <sig> verifies over <file> under the key.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// seedEnv names the secret holding the signing seed.
const seedEnv = "CHATCLI_RELEASE_SIGNING_KEY"

// maxSignedFile bounds what sign/verify read; checksums.txt is a few hundred
// bytes, so anything near this is not a release manifest.
const maxSignedFile = 64 << 20

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "keygen":
		if len(args) != 1 {
			usage(stderr)
			return 2
		}
		err = keygen(stdout, stderr)
	case "sign":
		if len(args) != 2 {
			usage(stderr)
			return 2
		}
		err = sign(args[1], getenv(seedEnv))
	case "verify":
		if len(args) != 4 {
			usage(stderr)
			return 2
		}
		err = verify(args[1], args[2], args[3])
		if err == nil {
			_, _ = fmt.Fprintln(stdout, "signature OK")
		}
	default:
		usage(stderr)
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "releasesign:", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	_, _ = fmt.Fprintln(w, "usage:\n"+
		"  releasesign keygen                          seed to stdout, public key to stderr\n"+
		"  releasesign sign <file>                     writes <file>.sig using $"+seedEnv+"\n"+
		"  releasesign verify <file> <sig> <pubkey-b64>")
}

func keygen(stdout, stderr io.Writer) error {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return fmt.Errorf("generating seed: %w", err)
	}
	pub := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if _, err := fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(seed)); err != nil {
		return err
	}
	_, err := fmt.Fprintln(stderr, base64.StdEncoding.EncodeToString(pub))
	return err
}

// privateKeyFromSeed decodes the secret, refusing anything that is not the
// standard base64 of exactly 32 bytes.
func privateKeyFromSeed(encoded string) (ed25519.PrivateKey, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, fmt.Errorf("%s is not set: export the base64 Ed25519 seed (see `releasesign keygen`)", seedEnv)
	}
	seed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s must be the standard base64 of a %d-byte Ed25519 seed", seedEnv, ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(filepath.Clean(path)) // #nosec G304 -- operator-named release artifact
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxSignedFile+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSignedFile {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maxSignedFile)
	}
	return data, nil
}

func sign(path, encodedSeed string) error {
	priv, err := privateKeyFromSeed(encodedSeed)
	if err != nil {
		return err
	}
	data, err := readBounded(path)
	if err != nil {
		return err
	}
	sig := ed25519.Sign(priv, data)
	// The self-updater verifies the exact bytes it downloads; confirm the
	// signature round-trips before publishing it.
	if !ed25519.Verify(priv.Public().(ed25519.PublicKey), data, sig) {
		return errors.New("signature self-check failed")
	}
	return os.WriteFile(path+".sig", []byte(base64.StdEncoding.EncodeToString(sig)), 0o644) // #nosec G306 -- public release asset
}

func verify(path, sigPath, pubEncoded string) error {
	pub, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubEncoded))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("public key must be the standard base64 of %d bytes", ed25519.PublicKeySize)
	}
	data, err := readBounded(path)
	if err != nil {
		return err
	}
	sigFile, err := readBounded(sigPath)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%s is not the standard base64 of a %d-byte signature", sigPath, ed25519.SignatureSize)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), data, sig) {
		return errors.New("signature does not verify")
	}
	return nil
}
