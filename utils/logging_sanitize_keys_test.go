/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package utils

import (
	"strings"
	"testing"
)

func TestSanitizeBody_OAuthAndCamelCaseKeys(t *testing.T) {
	lt := &LoggingTransport{MaxBodySize: 1 << 20}
	const fake = "FAKE-VALUE-0123456789"

	// Form bodies of the OAuth token, device and registration exchanges.
	for _, key := range []string{"code", "code_verifier", "device_code", "registration_access_token", "client_secret", "refresh_token", "access_token"} {
		out := string(lt.sanitizeBody("application/x-www-form-urlencoded", []byte("grant_type=x&"+key+"="+fake)))
		if strings.Contains(out, fake) {
			t.Errorf("form %s survived: %s", key, out)
		}
		if !strings.Contains(out, "grant_type=x") {
			t.Errorf("form %s: non-secret field lost: %s", key, out)
		}
	}

	// JSON keys in any case and separator style.
	for _, key := range []string{"secretAccessKey", "sessionToken", "SessionToken", "apiKey", "API-KEY", "codeVerifier", "deviceCode", "registrationAccessToken", "clientSecret", "refreshToken", "accessToken", "x-api-key"} {
		body := `{"region":"us-east-1","` + key + `":"` + fake + `"}`
		out := string(lt.sanitizeBody("application/json", []byte(body)))
		if strings.Contains(out, fake) {
			t.Errorf("json %s survived: %s", key, out)
		}
		if !strings.Contains(out, "us-east-1") {
			t.Errorf("json %s: non-secret field lost: %s", key, out)
		}
	}

	// Everything under a sensitive key is redacted, objects included.
	nested := `{"credentials":{"accessKeyId":"` + fake + `","nested":{"value":"` + fake + `"},"list":["` + fake + `"]},"model":"m-1"}`
	out := string(lt.sanitizeBody("application/json", []byte(nested)))
	if strings.Contains(out, fake) {
		t.Errorf("object under a sensitive key survived: %s", out)
	}
	if !strings.Contains(out, `"model":"m-1"`) {
		t.Errorf("non-secret field lost: %s", out)
	}
}

func TestSanitizeURL_UserinfoAndOAuthParams(t *testing.T) {
	lt := &LoggingTransport{}
	out := lt.sanitizeURL("https://user:FAKEPASS@example.test/cb?code=FAKECODE&state=s1&codeVerifier=FAKEVER")
	for _, secret := range []string{"FAKEPASS", "FAKECODE", "FAKEVER"} {
		if strings.Contains(out, secret) {
			t.Errorf("%s survived: %s", secret, out)
		}
	}
	if !strings.Contains(out, "state=s1") {
		t.Errorf("non-secret param lost: %s", out)
	}
}

func TestRedactURLForLog(t *testing.T) {
	out := RedactURLForLog("https://admin:FAKEPASS@mcp.example.test/sse?apiKey=FAKEKEY&workspace=FAKEWS#frag")
	for _, secret := range []string{"FAKEPASS", "FAKEKEY", "FAKEWS", "frag"} {
		if strings.Contains(out, secret) {
			t.Errorf("%s survived: %s", secret, out)
		}
	}
	for _, keep := range []string{"https://", "mcp.example.test/sse", "apiKey=", "workspace="} {
		if !strings.Contains(out, keep) {
			t.Errorf("%s lost: %s", keep, out)
		}
	}
	if got := RedactURLForLog("https://mcp.example.test/mcp"); got != "https://mcp.example.test/mcp" {
		t.Errorf("a URL with nothing secret changed: %s", got)
	}
	if got := RedactURLForLog("http://[::1"); got != "[REDACTED_URL]" {
		t.Errorf("unparseable URL = %q", got)
	}
}
