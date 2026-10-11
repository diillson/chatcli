/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// stageTarget writes a fake current binary and returns its path.
func stageTarget(t *testing.T) string {
	t.Helper()
	target := filepath.Join(t.TempDir(), "chatcli")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil { // #nosec G306 -- test binary
		t.Fatal(err)
	}
	return target
}

func assertUntouched(t *testing.T, target string) {
	t.Helper()
	got, err := os.ReadFile(target) // #nosec G304 -- test temp path
	if err != nil || string(got) != "old binary" {
		t.Fatalf("current binary must stay untouched, got %q, %v", got, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".chatcli-update-*"))
	if len(leftovers) != 0 {
		t.Fatalf("staging files left behind: %v", leftovers)
	}
}

// releaseFixture returns a checksums.txt for a binary on this platform.
func releaseFixture(t *testing.T, binary string) (asset, checksums string) {
	t.Helper()
	asset, err := AssetName()
	if err != nil {
		t.Skipf("no release asset for this platform: %v", err)
	}
	return asset, fmt.Sprintf("%s  %s\n", sha256Hex(binary), asset)
}

func TestSelfReplaceRefusesWithoutEmbeddedKeys(t *testing.T) {
	const bin = "attacker binary"
	asset, checksums := releaseFixture(t, bin)
	orig := releaseSigningPublicKeys
	releaseSigningPublicKeys = nil
	t.Cleanup(func() { releaseSigningPublicKeys = orig })
	hits := serveRelease(t, "v2.0.0", checksums, nil, map[string]string{asset: bin})

	target := stageTarget(t)
	if err := SelfReplace(context.Background(), target, "v2.0.0"); !errors.Is(err, ErrNoReleaseKeys) {
		t.Fatalf("want ErrNoReleaseKeys, got %v", err)
	}
	if *hits != 0 {
		t.Fatal("nothing may be downloaded without an embedded key")
	}
	assertUntouched(t, target)
}

// The shipped placeholder list is empty, so a build without the real key
// fails closed rather than trusting the checksums alone.
func TestShippedReleaseKeysFailClosedWhenEmpty(t *testing.T) {
	if len(releaseSigningPublicKeys) == 0 {
		if _, err := trustedReleaseKeys(); !errors.Is(err, ErrNoReleaseKeys) {
			t.Fatalf("empty key list must yield ErrNoReleaseKeys, got %v", err)
		}
		return
	}
	if _, err := trustedReleaseKeys(); err != nil {
		t.Fatalf("embedded release keys must all decode: %v", err)
	}
}

func TestSelfReplaceRefusesMissingSignature(t *testing.T) {
	const bin = "attacker binary"
	asset, checksums := releaseFixture(t, bin)
	installTestReleaseKey(t)
	hits := serveRelease(t, "v2.0.0", checksums, nil, map[string]string{asset: bin})

	target := stageTarget(t)
	err := SelfReplace(context.Background(), target, "v2.0.0")
	var unavailable *SignatureUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("want SignatureUnavailableError, got %v", err)
	}
	if *hits != 0 {
		t.Fatal("the binary must not be downloaded before the checksums verify")
	}
	assertUntouched(t, target)
}

// TestSelfReplaceRefusesForgedChecksums is the core regression: a
// checksums.txt rewritten next to a swapped binary (the --clobber attack)
// carries a hash that matches the swapped binary, so only the signature
// stands between it and the user.
func TestSelfReplaceRefusesForgedChecksums(t *testing.T) {
	const genuine = "genuine binary"
	const forged = "attacker binary"
	asset, genuineSums := releaseFixture(t, genuine)
	_, forgedSums := releaseFixture(t, forged)
	_, priv := installTestReleaseKey(t)
	sigOfGenuine := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(genuineSums)))

	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sigByOtherKey := base64.StdEncoding.EncodeToString(ed25519.Sign(otherPriv, []byte(forgedSums)))

	cases := map[string]string{
		"signature of the genuine file": sigOfGenuine,
		"signature by an untrusted key": sigByOtherKey,
		"malformed signature":           "not base64 at all",
		"empty signature":               "",
		"truncated signature":           sigOfGenuine[:20],
	}
	for name, sig := range cases {
		t.Run(name, func(t *testing.T) {
			sig := sig
			hits := serveRelease(t, "v2.0.0", forgedSums, &sig, map[string]string{asset: forged})
			target := stageTarget(t)
			err := SelfReplace(context.Background(), target, "v2.0.0")
			var invalid *SignatureInvalidError
			if !errors.As(err, &invalid) {
				t.Fatalf("want SignatureInvalidError, got %v", err)
			}
			if *hits != 0 {
				t.Fatal("the binary must not be downloaded before the checksums verify")
			}
			assertUntouched(t, target)
		})
	}
}

// Rotation: any embedded key verifying is enough.
func TestSelfReplaceAcceptsAnyEmbeddedKey(t *testing.T) {
	const bin = "new binary"
	asset, checksums := releaseFixture(t, bin)
	oldPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	newPub, newPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	orig := releaseSigningPublicKeys
	releaseSigningPublicKeys = []string{
		base64.StdEncoding.EncodeToString(oldPub),
		base64.StdEncoding.EncodeToString(newPub),
	}
	t.Cleanup(func() { releaseSigningPublicKeys = orig })
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(newPriv, []byte(checksums))) + "\n"
	serveRelease(t, "v2.0.0", checksums, &sig, map[string]string{asset: bin})

	target := stageTarget(t)
	if err := SelfReplace(context.Background(), target, "v2.0.0"); err != nil {
		t.Fatalf("SelfReplace: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != bin { // #nosec G304 -- test temp path
		t.Fatalf("binary not replaced: %q", got)
	}
}

func TestTrustedReleaseKeysRejectsMalformedEntry(t *testing.T) {
	orig := releaseSigningPublicKeys
	releaseSigningPublicKeys = []string{"c2hvcnQ="}
	t.Cleanup(func() { releaseSigningPublicKeys = orig })
	if _, err := trustedReleaseKeys(); err == nil {
		t.Fatal("a key that is not 32 bytes must be a hard error")
	}
}

func TestSelfReplaceRejectsMalformedTag(t *testing.T) {
	installTestReleaseKey(t)
	hits := serveRelease(t, "v2.0.0", "x", nil, nil)
	target := stageTarget(t)
	for _, tag := range []string{"v999.0.0/../../..", "2.0.0/../../x", "2.0", "", "v2.0.0?x=1"} {
		err := SelfReplace(context.Background(), target, tag)
		var invalid *InvalidTagError
		if !errors.As(err, &invalid) {
			t.Fatalf("tag %q: want InvalidTagError, got %v", tag, err)
		}
		if _, err := ManualDownloadURL(tag); !errors.As(err, &invalid) {
			t.Fatalf("ManualDownloadURL(%q): want InvalidTagError, got %v", tag, err)
		}
	}
	if *hits != 0 {
		t.Fatal("no request may be made for a malformed tag")
	}
	assertUntouched(t, target)
}
