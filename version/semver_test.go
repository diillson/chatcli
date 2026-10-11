/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package version

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIsValidTag(t *testing.T) {
	valid := []string{"1.2.3", "v1.2.3", "v0.0.1", "v1.215.1", "1.0.0-rc.1", "1.0.0-alpha.beta-2", "1.0.0+build.7"}
	for _, tag := range valid {
		if !IsValidTag(tag) {
			t.Errorf("IsValidTag(%q) = false, want true", tag)
		}
	}
	invalid := []string{
		"", "v", "1.2", "1.2.3.4", "01.2.3", "v999.0.0/../../..", "1.2.3/x",
		"1.2.3-", "1.2.3-a..b", "1.2.3 ", " 1.2.3", "1.2.3\n", "vv1.2.3",
		"1.2.3-a/b", `1.2.3\x`, "1.2.3?x", "1.2.3#x", "latest",
		"1.2.3-" + strings.Repeat("a", 200),
	}
	for _, tag := range invalid {
		if IsValidTag(tag) {
			t.Errorf("IsValidTag(%q) = true, want false", tag)
		}
	}
}

// TestNeedsUpdateRejectsMalformed is the regression for the Atoi-ignores-
// errors comparison: a traversal-shaped tag used to compare as 999.0.0.
func TestNeedsUpdateRejectsMalformed(t *testing.T) {
	for _, latest := range []string{"v999.0.0/../../..", "999.0.0/x", "999.x.0", "999", "1.300", "9.9.9-", ""} {
		if NeedsUpdate("1.0.0", latest) {
			t.Errorf("NeedsUpdate(1.0.0, %q) = true, want false", latest)
		}
	}
	if NeedsUpdate("garbage", "2.0.0") {
		t.Error("malformed current version must not report an update")
	}
}

func TestNeedsUpdatePrereleasePrecedence(t *testing.T) {
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"1.0.0-alpha", "1.0.0-alpha.1", true},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", true},
		{"1.0.0-beta.2", "1.0.0-beta.11", true},
		{"1.0.0-beta.11", "1.0.0-beta.2", false},
		{"1.0.0-rc.1", "1.0.0", true},
		{"1.0.0", "1.0.0-rc.1", false},
		{"1.9.0", "1.10.0", true},
	}
	for _, c := range cases {
		if got := NeedsUpdate(c.cur, c.latest); got != c.want {
			t.Errorf("NeedsUpdate(%q, %q) = %v, want %v", c.cur, c.latest, got, c.want)
		}
	}
}

func TestFetchLatestReleaseRejectsMalformedTag(t *testing.T) {
	isolateHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v999.0.0/../../.."})
	}))
	defer srv.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", srv.URL)
	t.Setenv("CHATCLI_DISABLE_VERSION_CHECK", "")

	if _, err := FetchLatestReleaseImpl(context.Background()); err == nil {
		t.Fatal("a non-semver tag must be rejected at ingestion")
	}
	rep := GetReport(context.Background())
	if rep.CheckErr == nil || rep.NeedsUpdate || rep.Latest != "" {
		t.Fatalf("report must surface the error and never offer the tag: %+v", rep)
	}
	if _, ok := loadReleaseCache(); ok {
		t.Fatal("a rejected release must not reach the cache")
	}
}

func TestFetchLatestReleaseBoundsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.3","body":"`))
		chunk := []byte(strings.Repeat("x", 64<<10))
		for i := 0; i < 32; i++ { // 2 MiB, past the 1 MiB bound
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()
	t.Setenv("CHATCLI_LATEST_VERSION_URL", srv.URL)

	_, err := FetchLatestReleaseImpl(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized release body must be refused, got %v", err)
	}
}

func TestReleaseCacheRejectsMalformedTag(t *testing.T) {
	home := isolateHome(t)
	path := filepath.Join(home, ".chatcli", "cache", "latest-release.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(releaseCacheEntry{
		FetchedAt: time.Now(),
		Release:   ReleaseInfo{TagName: "v9.9.9/../../evil"},
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadReleaseCache(); ok {
		t.Fatal("cached non-semver tag must count as no cache")
	}
}
