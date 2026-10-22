/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Fake credentials are assembled by concatenation so the repository's own
// scanners never see a literal that looks like a key.
var (
	fakeAWSID    = "AKIA" + "ABCDEFGHIJKLMNOP"
	fakeGH       = "ghp_" + strings.Repeat("a1B2", 9)
	fakeConnPass = "s3cret" + "pass"
	fakePassword = "hunter2" + "hunter2"
	fakeEntropy  = "q9Zx2LmN7vKp4RtY" + "8wHs3JfD6gBc1AeU"
	fakePEM      = "-----BEGIN " + "RSA PRIVATE KEY-----"
	fakeJWT      = "eyJ" + strings.Repeat("a", 12) + "." + strings.Repeat("b", 12) + "." + strings.Repeat("c", 12)
)

func writeSecretsFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	files := map[string]string{
		"config.yaml": "aws_access_key_id: " + fakeAWSID + "\nurl: postgres://app:" + fakeConnPass + "@db.local/app\n" +
			"password = \"" + fakePassword + "\"\nTOKEN=" + fakeEntropy + "\n" + fakePEM + "\nexample: AKIA" + "EXAMPLEEXAMPLE12\n",
		"ci.env":                "GH=" + fakeGH + "\nIGNORED=" + fakeGH + " # secrets:ignore\nPASSWORD=\"${DB_PASSWORD}\"\n",
		"node_modules/x.js":     "const t = '" + fakeGH + "'\n",
		"app.log":               "jwt " + fakeJWT + "\n",
		"notes.txt":             "jwt " + fakeJWT + "\n",
		"blob.bin":              "x\x00y" + fakeGH,
		scanIgnoreFile:          "# comment\n*.log\njwt\n",
		filepath.Join("a", "b"): "clean\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSecrets_ScanFindsRedactsAndHonorsIgnores(t *testing.T) {
	writeSecretsFixture(t)
	out, err := NewBuiltinSecretsPlugin().Execute(context.Background(), []string{`{"cmd":"scan","args":{"path":"."}}`})
	if err != nil {
		t.Fatalf("scan: %v\n%s", err, out)
	}
	for _, want := range []string{
		"config.yaml:1 aws-access-key-id high AKIA…(20 chars)",
		"config.yaml:2 connection-string high s3cr…(10 chars)",
		"config.yaml:3 generic-assignment medium hunt…(14 chars)",
		"config.yaml:4 high-entropy-secret high q9Zx…(32 chars)",
		"config.yaml:5 private-key critical ----…",
		"ci.env:1 github-token critical ghp_…(40 chars)",
		"critical=2 high=3 medium=1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, leak := range []string{fakeAWSID, fakeGH, fakeConnPass, fakePassword, fakeEntropy, fakeJWT} {
		if strings.Contains(out, leak) {
			t.Errorf("full secret echoed: %q", leak)
		}
	}
	for _, no := range []string{"ci.env:2", "ci.env:3", "node_modules", "app.log", "notes.txt", "blob.bin", "EXAMPLE"} {
		if strings.Contains(out, no) {
			t.Errorf("unexpected %q in:\n%s", no, out)
		}
	}
	// A walk cut short by max_files says so.
	out, err = NewBuiltinSecretsPlugin().Execute(context.Background(), []string{"scan", "--max_files", "1"})
	if err != nil || !strings.Contains(out, "note: stopped at max_files=1") {
		t.Fatalf("max_files note: %v\n%s", err, out)
	}
	// include glob narrows the walk; a clean tree says so.
	out, err = NewBuiltinSecretsPlugin().Execute(context.Background(), []string{"scan", "--include", "a/*"})
	if err != nil || !strings.HasPrefix(out, "no secrets found (1 file(s) scanned") {
		t.Fatalf("include scan: %v\n%s", err, out)
	}
}

func TestSecrets_CheckRulesAndBoundary(t *testing.T) {
	t.Chdir(t.TempDir())
	p := NewBuiltinSecretsPlugin()
	out, err := p.Execute(context.Background(), []string{"check", "--text", "export GH=" + fakeGH})
	if err != nil || !strings.Contains(out, "<text>:1 github-token critical ghp_…(40 chars)") {
		t.Fatalf("check: %v\n%s", err, out)
	}
	out, err = p.Execute(context.Background(), []string{`{"text":"nothing here"}`})
	if err != nil || !strings.HasPrefix(out, "no secrets found") {
		t.Fatalf("clean check: %v\n%s", err, out)
	}
	if out, err = p.Execute(context.Background(), []string{`{"cmd":"rules"}`}); err != nil || !strings.Contains(out, "aws-access-key-id") || !strings.Contains(out, "high-entropy-secret") {
		t.Fatalf("rules: %v\n%s", err, out)
	}
	if _, err = p.Execute(context.Background(), []string{"scan", "--path", filepath.Join("..", "..", "..")}); err == nil {
		t.Fatal("scan outside the workspace must be refused")
	}
	if _, err = p.Execute(context.Background(), []string{`{"cmd":"bogus"}`}); err == nil {
		t.Fatal("unknown cmd must error")
	}
}

func TestSecrets_StagedScansGitIndexOnly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := writeSecretsFixture(t)
	for _, args := range [][]string{{"init", "-q"}, {"add", "ci.env"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := NewBuiltinSecretsPlugin().Execute(context.Background(), []string{"scan", "--staged"})
	if err != nil {
		t.Fatalf("staged: %v\n%s", err, out)
	}
	if !strings.Contains(out, "ci.env:1 github-token") || strings.Contains(out, "config.yaml") {
		t.Fatalf("staged scan must cover only the index:\n%s", out)
	}
}

func TestShannonEntropy(t *testing.T) {
	if e := shannonEntropy("aaaaaaaa"); e != 0 {
		t.Errorf("uniform string entropy = %v, want 0", e)
	}
	if e := shannonEntropy(fakeEntropy); e < secretsMinEntropy {
		t.Errorf("random string entropy = %v, want >= %v", e, secretsMinEntropy)
	}
}
