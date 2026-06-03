/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mockOSV returns a vuln for one specific package@version, nothing otherwise.
func mockOSV(t *testing.T, vulnName, vulnVersion string) func() {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var q struct {
			Version string `json:"version"`
			Package struct {
				Name string `json:"name"`
			} `json:"package"`
		}
		_ = json.Unmarshal(body, &q)
		if q.Package.Name == vulnName && q.Version == vulnVersion {
			_, _ = w.Write([]byte(`{"vulns":[{"id":"GHSA-xxxx","summary":"boom","aliases":["CVE-2020-0001"],"severity":[{"type":"CVSS_V3","score":"9.8"}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	oldURL, oldClient := osvBaseURL, osvHTTPClient
	osvBaseURL = srv.URL
	osvHTTPClient = srv.Client()
	return func() {
		srv.Close()
		osvBaseURL, osvHTTPClient = oldURL, oldClient
	}
}

func TestOsvCheck_Hit(t *testing.T) {
	defer mockOSV(t, "requests", "2.19.0")()
	p := NewBuiltinOsvPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"check","args":{"ecosystem":"PyPI","package":"requests","version":"2.19.0"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "GHSA-xxxx") || !strings.Contains(out, "CVE-2020-0001") {
		t.Fatalf("expected vuln details, got %q", out)
	}
}

func TestOsvCheck_Clean(t *testing.T) {
	defer mockOSV(t, "requests", "2.19.0")()
	p := NewBuiltinOsvPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"check","args":{"ecosystem":"PyPI","package":"requests","version":"2.32.0"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no known vulnerabilities") {
		t.Fatalf("expected clean result, got %q", out)
	}
}

func TestOsvScan_GoMod(t *testing.T) {
	defer mockOSV(t, "github.com/evil/pkg", "v1.0.0")()
	dir := t.TempDir()
	gomod := "module example.com/x\n\ngo 1.22\n\nrequire (\n\tgithub.com/evil/pkg v1.0.0\n\tgithub.com/safe/pkg v2.3.4\n)\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewBuiltinOsvPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"scan","args":{"path":"` + filepath.Join(dir, "go.mod") + `"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "github.com/evil/pkg") || !strings.Contains(out, "GHSA-xxxx") {
		t.Fatalf("scan should flag the vulnerable dep, got %q", out)
	}
	if strings.Contains(out, "github.com/safe/pkg") {
		t.Fatalf("safe dep should not be flagged, got %q", out)
	}
}

func TestOsvScan_DirResolves(t *testing.T) {
	defer mockOSV(t, "nope", "0")()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("flask==2.0.0\n# comment\nrequests==2.19.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewBuiltinOsvPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"scan","args":{"path":"` + dir + `"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 dependencies checked") {
		t.Fatalf("expected 2 deps checked, got %q", out)
	}
	if !strings.Contains(out, "No known vulnerabilities") {
		t.Fatalf("expected clean, got %q", out)
	}
}

func TestParseManifests(t *testing.T) {
	if got := parseGoMod([]byte("require (\n\tfoo/bar v1.2.3\n)\nrequire baz/qux v0.1.0\n")); len(got) != 2 {
		t.Fatalf("parseGoMod = %v", got)
	}
	if got := parseRequirements([]byte("a==1.0\nb>=2.0\nc==3.1 ; python_version<'3'\n")); len(got) != 2 {
		t.Fatalf("parseRequirements = %v", got)
	}
	if got := parseCargoLock([]byte("[[package]]\nname = \"serde\"\nversion = \"1.0.1\"\n")); len(got) != 1 || got[0].Name != "serde" {
		t.Fatalf("parseCargoLock = %v", got)
	}
	lock := `{"packages":{"":{"version":"1.0.0"},"node_modules/lodash":{"version":"4.17.0"}}}`
	got, err := parsePackageLock([]byte(lock))
	if err != nil || len(got) != 1 || got[0].Name != "lodash" || got[0].Version != "4.17.0" {
		t.Fatalf("parsePackageLock = %v err=%v", got, err)
	}
}

func TestCanonicalOsvCmd(t *testing.T) {
	if canonicalOsvCmd("audit") != "scan" || canonicalOsvCmd("query") != "check" || canonicalOsvCmd("x") != "" {
		t.Fatal("canonicalOsvCmd mismatch")
	}
}

func TestOsv_FlattenedArgvCheck(t *testing.T) {
	defer mockOSV(t, "requests", "2.19.0")()
	p := NewBuiltinOsvPlugin()
	out, err := p.Execute(context.Background(), []string{"check", "--ecosystem", "PyPI", "--package", "requests", "--version", "2.19.0"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "GHSA-xxxx") {
		t.Fatalf("flattened check failed: %q", out)
	}
}
