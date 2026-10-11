package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestMetaSignatureVerification(t *testing.T) {
	const secret = "app-secret"
	body := []byte(`{"entry":[]}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !verifyMetaSignature(secret, body, good) {
		t.Error("a correctly signed delivery must be accepted")
	}
	if verifyMetaSignature(secret, body, "sha256=deadbeef") {
		t.Error("a wrong signature must be refused")
	}
	if verifyMetaSignature(secret, body, hex.EncodeToString(mac.Sum(nil))) {
		t.Error("a signature without the sha256= prefix must be refused")
	}
	if verifyMetaSignature(secret, body, "") {
		t.Error("a missing signature must be refused")
	}
	// No secret configured: nothing can be verified, so nothing is accepted.
	if verifyMetaSignature("", body, good) {
		t.Error("an unconfigured adapter must accept nothing")
	}
	// The signature covers the body: changing it invalidates the delivery.
	if verifyMetaSignature(secret, []byte(`{"entry":[{"forged":true}]}`), good) {
		t.Error("a tampered body must be refused")
	}
}

// Slack's own verifier gains the same rule.
func TestSlackSignatureRefusesWithoutASecret(t *testing.T) {
	if verifySlackSignature("", "1757000000", []byte(`{}`), "v0=abc") {
		t.Error("an unconfigured Slack adapter must accept nothing")
	}
}
