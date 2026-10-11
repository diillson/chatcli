/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
)

// errMalformedSignature marks a checksums.txt.sig that is not the standard
// base64 of a 64-byte Ed25519 signature.
var errMalformedSignature = errors.New("malformed release signature")

// errUntrustedSignature: well-formed, but no embedded key verifies it.
var errUntrustedSignature = errors.New("no trusted release key verifies the signature")

// trustedReleaseKeys decodes the embedded release keys. A malformed entry is
// a build defect and is reported rather than skipped, so a broken key list
// can never degrade into "trust whatever key decoded".
func trustedReleaseKeys() ([]ed25519.PublicKey, error) {
	if len(releaseSigningPublicKeys) == 0 {
		return nil, ErrNoReleaseKeys
	}
	keys := make([]ed25519.PublicKey, 0, len(releaseSigningPublicKeys))
	for i, enc := range releaseSigningPublicKeys {
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("update: embedded release key %d is not a base64 Ed25519 public key", i)
		}
		keys = append(keys, ed25519.PublicKey(raw))
	}
	return keys, nil
}

// verifyReleaseSignature checks sigFile (standard base64 of the Ed25519
// signature, surrounding whitespace tolerated) over the exact bytes of
// content against every trusted key. Any one key verifying is enough.
func verifyReleaseSignature(content, sigFile []byte, keys []ed25519.PublicKey) error {
	sig, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(sigFile)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errMalformedSignature
	}
	for _, k := range keys {
		if ed25519.Verify(k, content, sig) {
			return nil
		}
	}
	return errUntrustedSignature
}
