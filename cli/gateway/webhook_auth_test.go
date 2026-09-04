package gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An adapter with no secret configured cannot tell a real caller from
// anyone who found the address, and what it reaches approves its own tool
// calls. It used to accept everything in that case.
func TestGenericWebhookRefusesWithoutASecret(t *testing.T) {
	w := NewWebhookAdapter(":0", "/hook", "", "", nil)
	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(`{"text":"hi"}`))
	if w.authorized(req) {
		t.Error("an unconfigured webhook must accept nothing")
	}
}

func TestGenericWebhookChecksTheSecret(t *testing.T) {
	w := NewWebhookAdapter(":0", "/hook", "s3cr3t", "", nil)

	req := httptest.NewRequest(http.MethodPost, "/hook", strings.NewReader(`{"text":"hi"}`))
	if w.authorized(req) {
		t.Error("a request with no secret header must be refused")
	}
	req.Header.Set("X-ChatCLI-Secret", "wrong")
	if w.authorized(req) {
		t.Error("a wrong secret must be refused")
	}
	req.Header.Set("X-ChatCLI-Secret", "s3cr3t")
	if !w.authorized(req) {
		t.Error("the configured secret must be accepted")
	}
}

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
