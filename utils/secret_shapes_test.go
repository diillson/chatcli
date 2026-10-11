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

func TestMaskSensitiveInText_CoversTheMissingShapes(t *testing.T) {
	// Fixtures are assembled at runtime so no secret-shaped literal sits in
	// the source (push protection scans the tree; the point of the test is
	// that these shapes never reach disk in the first place).
	join := func(parts ...string) string { return strings.Join(parts, "") }
	cases := map[string]string{
		"slack bot":  join("xox", "b-", "1234567890", "-", "abcdefghijklmnop"),
		"slack hook": join("https://hooks.slack.com/", "services/T000/B000/", "XXXXXXXXXXXXXXXX"),
		"pem":        join("-----BEGIN RSA ", "PRIVATE KEY-----\nMIIEow\nABCD\n-----END RSA ", "PRIVATE KEY-----"),
		"postgres":   join("postgres://app:", "S3cr3tPassw0rd", "@db.internal:5432/app"),
		"mongodb":    join("mongodb+srv://user:", "hunter2", "@cluster0.example.net/db"),
		"aws secret": join("aws_secret_", "access_key = ", "wJalrXUtnFEMI/K7MDENG/bPxRfiCY", "EXAMPLEKEY"),
		"gcp sa":     join(`{"type":"service_account","private_`, `key_id":"0123456789abcdef"}`),
		"azure":      join("DefaultEndpointsProtocol=https;AccountName=x;Account", "Key=abcdefghijklmnopqrstuvwxyz0123456789ABCD=="),
		"sas":        join("https://acct.blob.core.windows.net/c?sv=2020&", "sig=abcdefghijklmnopqrstuvwxyz0123"),
	}
	needles := map[string]string{
		"slack bot": "abcdefghijklmnop", "slack hook": "XXXXXXXXXXXXXXXX", "pem": "MIIEow", "postgres": "S3cr3tPassw0rd",
		"mongodb": "hunter2", "aws secret": "wJalrXUtnFEMI", "gcp sa": "0123456789abcdef", "azure": "abcdefghijklmnopqrstuvwxyz0123456789ABCD", "sas": "abcdefghijklmnopqrstuvwxyz0123",
	}
	for name, in := range cases {
		out := maskSensitiveInText(in)
		if strings.Contains(out, needles[name]) {
			t.Errorf("%s: secret survived: %q", name, out)
		}
	}
	if out := maskSensitiveInText("https://example.com/path and postgres://db.internal/app"); out != "https://example.com/path and postgres://db.internal/app" {
		t.Fatalf("urls without credentials must stay: %q", out)
	}
}

func TestMaskSensitiveInText_CurrentKeyFormats(t *testing.T) {
	// Synthetic, obviously fake values shaped like each provider's format,
	// assembled at runtime so no secret-shaped literal sits in the source.
	join := func(parts ...string) string { return strings.Join(parts, "") }
	body := "FAKEfake0123456789FAKEfake0123456789"
	cases := []struct{ name, in, needle string }{
		{"openai project", join("key sk-", "proj-", body, "_-x"), body},
		{"openai service account", join("sk-", "svcacct-", body), body},
		{"openai admin", join("sk-", "admin-", body), body},
		{"openrouter", join("sk-", "or-v1-", "0123456789abcdef0123456789abcdef"), "0123456789abcdef0123456789abcdef"},
		{"anthropic", join("sk-", "ant-api03-", body, "-AA"), body},
		{"github oauth", join("gh", "o_", body), body},
		{"github user", join("gh", "u_", body), body},
		{"github server", join("gh", "s_", body), body},
		{"github refresh", join("gh", "r_", body), body},
		{"aws temporary", join("AS", "IA", "FAKEFAKEFAKE0000"), "FAKEFAKEFAKE0000"},
		{"gitlab", join("glp", "at-", "FAKEfake01234567890x"), "FAKEfake01234567890x"},
		{"groq", join("gs", "k_", body), body},
		{"google oauth", join("ya", "29.", body), body},
		{"json apiKey", join(`{"apiKey": "`, body, `"}`), body},
		{"json api-key", join(`{"api-key":"`, body, `"}`), body},
		{"json API_KEY upper", join(`{"OPENAI_API_KEY":"`, body, `"}`), body},
		{"json secretAccessKey", join(`{"secretAccessKey":"`, body, `"}`), body},
		{"json sessionToken", join(`{"SessionToken": "`, body, `"}`), body},
		{"json x-api-key", join(`{"headers":{"x-api-key":"`, body, `"}}`), body},
		{"yaml apiKey", join("provider:\n  apiKey: ", body, "\n"), body},
		{"yaml quoted password", join("db:\n  password: \"", body, "\"\n"), body},
		{"yaml sessionToken", join("  sessionToken: '", body, "'"), body},
		{"header x-api-key", join(`curl -H "x-api-key: `, body, `" https://api.example.test`), body},
		{"header api-key", join("api-key: ", body), body},
		{"env suffix api key", join("OPENAI_", "API_KEY=", body), body},
		{"env suffix token", join("export GITLAB_", "TOKEN=", body), body},
		{"env suffix secret", join("MY_APP_", "SECRET = ", body), body},
	}
	for _, tc := range cases {
		out := maskSensitiveInText(tc.in)
		if strings.Contains(out, tc.needle) {
			t.Errorf("%s: secret survived: %q", tc.name, out)
		}
	}

	// Look-alikes that are not credentials stay untouched.
	keep := []string{
		`{"next_page_token":"abc123","max_tokens":4096}`,
		"MAX_TOKENS=4096",
		"tokens: 12",
		"scikit-learn sk-learn is a library",
		"the task-list ask-user flow",
	}
	for _, in := range keep {
		if out := maskSensitiveInText(in); out != in {
			t.Errorf("non-secret changed: %q -> %q", in, out)
		}
	}
}
