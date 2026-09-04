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
