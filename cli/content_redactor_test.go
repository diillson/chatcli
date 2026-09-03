/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/models"
)

// Synthetic secrets are assembled at runtime so the source never contains a
// literal that secret scanners (gitleaks) would flag, while the redactor
// still sees the real value shapes.
func fakeSecret(kind string) string { return "fake-" + kind + "-" + strings.Repeat("q", 24) }
func fakeOpenAIKey() string         { return "sk-" + strings.Repeat("a", 30) }
func fakeGitHubPAT() string         { return "ghp_" + strings.Repeat("b", 36) }
func fakeJWT() string {
	return "eyJ" + strings.Repeat("a", 20) + ".eyJ" + strings.Repeat("b", 20) + "." + strings.Repeat("c", 16)
}

func TestRedactSecrets_EnvLinesByName(t *testing.T) {
	in := strings.Join([]string{
		"HOME=/Users/dev",
		"AWS_SECRET_ACCESS_KEY=" + fakeSecret("aws"),
		"export DATABASE_URL=postgres://app:" + fakeSecret("pw") + "@db:5432/app",
		"MY_SERVICE_TOKEN=\"abc123\"",
		"GOFLAGS=-mod=vendor",
		"PATH=/usr/bin",
	}, "\n")
	out := redactSecretsWithMode(in, contentRedactPermissive)

	for _, keep := range []string{"HOME=/Users/dev", "GOFLAGS=-mod=vendor", "PATH=/usr/bin"} {
		if !strings.Contains(out, keep) {
			t.Fatalf("harmless line lost: %q\n%s", keep, out)
		}
	}
	for _, gone := range []string{fakeSecret("aws"), fakeSecret("pw"), "abc123"} {
		if strings.Contains(out, gone) {
			t.Fatalf("secret survived: %q\n%s", gone, out)
		}
	}
	for _, want := range []string{"AWS_SECRET_ACCESS_KEY=[REDACTED]", "export DATABASE_URL=[REDACTED]", "MY_SERVICE_TOKEN=[REDACTED]"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in:\n%s", want, out)
		}
	}
}

func TestRedactSecrets_ValueShapesInFreeText(t *testing.T) {
	in := "config: token " + fakeOpenAIKey() + " and header Authorization: Bearer " + fakeJWT()
	out := redactSecretsWithMode(in, contentRedactPermissive)
	if strings.Contains(out, fakeOpenAIKey()) {
		t.Fatalf("OpenAI-style key survived:\n%s", out)
	}
	if strings.Contains(out, fakeJWT()[:12]) {
		t.Fatalf("bearer/JWT survived:\n%s", out)
	}
}

func TestRedactSecrets_StrictRedactsUnknownNames(t *testing.T) {
	in := "HOME=/Users/dev\nMY_INTERNAL_SETTING=42\nPATH=/usr/bin"
	out := redactSecretsWithMode(in, contentRedactStrict)
	if !strings.Contains(out, "HOME=/Users/dev") || !strings.Contains(out, "PATH=/usr/bin") {
		t.Fatalf("allowlisted vars must survive strict mode:\n%s", out)
	}
	if strings.Contains(out, "MY_INTERNAL_SETTING=42") {
		t.Fatalf("strict mode must redact non-allowlisted names:\n%s", out)
	}
	// Permissive keeps the same harmless line.
	if perm := redactSecretsWithMode(in, contentRedactPermissive); !strings.Contains(perm, "MY_INTERNAL_SETTING=42") {
		t.Fatalf("permissive must keep unknown harmless names:\n%s", perm)
	}
}

func TestRedactSecrets_OffIsIdentity(t *testing.T) {
	in := "AWS_SECRET_ACCESS_KEY=" + fakeSecret("aws")
	if out := redactSecretsWithMode(in, contentRedactOff); out != in {
		t.Fatalf("off must not touch content, got %q", out)
	}
}

func TestRedactSecrets_LeavesCodeAndMarkersAlone(t *testing.T) {
	in := "if a == b {\n\tx := y\n}\n[full content recoverable via @recall <<ccr:9f2a7c1d0e3b4a5f6c7d8e9f0a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3>>]\nresult=ok"
	out := redactSecretsWithMode(in, contentRedactPermissive)
	if out != in {
		t.Fatalf("plain code, markers and harmless assignments must be untouched:\n%s", out)
	}
}

func TestContentRedactModeFromEnv(t *testing.T) {
	cases := map[string]contentRedactMode{
		"":           contentRedactPermissive,
		"permissive": contentRedactPermissive,
		"normal":     contentRedactPermissive, // historical spelling
		"full":       contentRedactPermissive, // historical spelling
		"STRICT":     contentRedactStrict,
		"off":        contentRedactOff,
		"none":       contentRedactOff,
	}
	for v, want := range cases {
		t.Setenv("CHATCLI_ENV_REDACT_MODE", v)
		if got := contentRedactModeFromEnv(); got != want {
			t.Fatalf("mode(%q) = %s, want %s", v, got, want)
		}
	}
}

func TestRedactSecretsForLLM_HonorsEnv(t *testing.T) {
	in := "OPENAI_API_KEY=" + fakeOpenAIKey()
	t.Setenv("CHATCLI_ENV_REDACT_MODE", "off")
	if got := redactSecretsForLLM(in); got != in {
		t.Fatalf("off: got %q", got)
	}
	t.Setenv("CHATCLI_ENV_REDACT_MODE", "")
	if got := redactSecretsForLLM(in); strings.Contains(got, fakeOpenAIKey()) {
		t.Fatalf("default must redact: %q", got)
	}
}

// The memory extractor must never see (and therefore never persist) a
// secret that passed through the conversation.
func TestBuildExtractionSnippet_RedactsBeforeExtraction(t *testing.T) {
	t.Setenv("CHATCLI_ENV_REDACT_MODE", "")
	msgs := []models.Message{
		{Role: "user", Content: "use this: GITHUB_TOKEN=" + fakeGitHubPAT()},
		{Role: "assistant", Content: "noted"},
	}
	sb := buildExtractionSnippet(msgs)
	out := sb.String()
	if strings.Contains(out, fakeGitHubPAT()) {
		t.Fatalf("token reached the extraction prompt:\n%s", out)
	}
	if !strings.Contains(out, "[user]:") || !strings.Contains(out, "[assistant]: noted") {
		t.Fatalf("snippet shape changed:\n%s", out)
	}
}
