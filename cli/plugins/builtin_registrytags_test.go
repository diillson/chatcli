/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */

package plugins

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDetectRegistryTarget(t *testing.T) {
	cases := []struct {
		name         string
		image        string
		override     string
		wantRegistry string
		wantRepo     string
	}{
		{"bare name → hub", "redis", "", "https://hub.docker.com", "redis"},
		{"namespaced → hub", "bitnami/redis", "", "https://hub.docker.com", "bitnami/redis"},
		{"docker.io prefix → hub", "docker.io/library/nginx", "", "https://hub.docker.com", "library/nginx"},
		{"ghcr host", "ghcr.io/cli/cli", "", "https://ghcr.io", "cli/cli"},
		{"custom host with port", "localhost:5000/team/app", "", "https://localhost:5000", "team/app"},
		{"explicit override wins", "team/app", "https://harbor.example.com", "https://harbor.example.com", "team/app"},
		{"override without scheme", "team/app", "harbor.example.com", "https://harbor.example.com", "team/app"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotReg, gotRepo := detectRegistryTarget(tc.image, tc.override)
			if gotReg != tc.wantRegistry || gotRepo != tc.wantRepo {
				t.Fatalf("detectRegistryTarget(%q,%q) = (%q,%q), want (%q,%q)",
					tc.image, tc.override, gotReg, gotRepo, tc.wantRegistry, tc.wantRepo)
			}
		})
	}
}

func TestParseRegistryTagsArgs(t *testing.T) {
	t.Run("flat json", func(t *testing.T) {
		cfg, err := parseRegistryTagsArgs([]string{`{"image":"redis","limit":50}`})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Image != "redis" || cfg.Limit != 50 {
			t.Fatalf("got %+v", cfg)
		}
	})
	t.Run("envelope", func(t *testing.T) {
		cfg, err := parseRegistryTagsArgs([]string{`{"cmd":"tags","args":{"image":"ghcr.io/cli/cli"}}`})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Image != "ghcr.io/cli/cli" {
			t.Fatalf("got %+v", cfg)
		}
	})
	t.Run("argv with bare image and flag", func(t *testing.T) {
		cfg, err := parseRegistryTagsArgs([]string{"nginx", "--limit", "5"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Image != "nginx" || cfg.Limit != 5 {
			t.Fatalf("got %+v", cfg)
		}
	})
	t.Run("missing image errors", func(t *testing.T) {
		if _, err := parseRegistryTagsArgs([]string{`{"limit":5}`}); err == nil {
			t.Fatal("expected error for missing image")
		}
	})
	t.Run("limit capped at hard ceiling", func(t *testing.T) {
		cfg, err := parseRegistryTagsArgs([]string{`{"image":"x","limit":999999}`})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Limit != registryTagsHardCap {
			t.Fatalf("limit = %d, want %d", cfg.Limit, registryTagsHardCap)
		}
	})
}

func TestParseRegistryTagsArgs_Sort(t *testing.T) {
	t.Run("sort newest", func(t *testing.T) {
		cfg, err := parseRegistryTagsArgs([]string{`{"image":"x","sort":"newest"}`})
		if err != nil || cfg.Sort != "newest" {
			t.Fatalf("got %+v err %v", cfg, err)
		}
	})
	t.Run("last is sugar for newest+limit", func(t *testing.T) {
		cfg, err := parseRegistryTagsArgs([]string{`{"image":"x","last":10}`})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Sort != "newest" || cfg.Limit != 10 {
			t.Fatalf("got sort=%q limit=%d", cfg.Sort, cfg.Limit)
		}
	})
	t.Run("invalid sort errors", func(t *testing.T) {
		if _, err := parseRegistryTagsArgs([]string{`{"image":"x","sort":"bogus"}`}); err == nil {
			t.Fatal("expected error for invalid sort")
		}
	})
	t.Run("fetchBudget", func(t *testing.T) {
		if (registryTagsArgs{Limit: 5}).fetchBudget() != 5 {
			t.Error("no sort → budget is limit")
		}
		if (registryTagsArgs{Limit: 5, Sort: "newest"}).fetchBudget() != registryTagsHardCap {
			t.Error("sort → budget is hard cap")
		}
	})
}

func TestSortTags(t *testing.T) {
	indexOf := func(s []string, v string) int {
		for i, x := range s {
			if x == v {
				return i
			}
		}
		return -1
	}

	newest := []string{"v1.0.0", "v2.3.0", "v2.3.1", "latest", "v1.2.0", "v2.3.1-rc1"}
	sortTags(newest, "newest")
	if newest[0] != "v2.3.1" {
		t.Errorf("newest[0] = %q, want v2.3.1 (highest version)", newest[0])
	}
	if newest[len(newest)-1] != "latest" {
		t.Errorf("non-semver tag must sort last for newest, got %v", newest)
	}
	if indexOf(newest, "v2.3.1") > indexOf(newest, "v2.3.1-rc1") {
		t.Errorf("a release must outrank its pre-release: %v", newest)
	}

	oldest := []string{"v2.0.0", "v1.0.0", "v1.5.0"}
	sortTags(oldest, "oldest")
	if strings.Join(oldest, ",") != "v1.0.0,v1.5.0,v2.0.0" {
		t.Errorf("oldest = %v", oldest)
	}

	name := []string{"b", "a", "c"}
	sortTags(name, "name")
	if strings.Join(name, ",") != "a,b,c" {
		t.Errorf("name = %v", name)
	}

	pushed := []string{"old", "mid", "new"}
	sortTags(pushed, "pushed")
	if strings.Join(pushed, ",") != "new,mid,old" {
		t.Errorf("pushed = %v", pushed)
	}
}

// TestFetchRegistryTags_SortPaginatesToEnd proves the gap fix: the newest tag
// sits on a LATER page (push order), and would be cut by the old first-N
// behavior; with sort=newest the fetch reads to the end, sorts, then truncates.
func TestFetchRegistryTags_SortPaginatesToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("last") == "" {
			w.Header().Set("Link", `</v2/team/app/tags/list?n=100&last=p1>; rel="next"`)
			_, _ = w.Write([]byte(`{"tags":["v1.0.0","v1.1.0"]}`)) // oldest first
			return
		}
		_, _ = w.Write([]byte(`{"tags":["v2.0.0","v1.2.0"]}`)) // newest on the last page
	}))
	defer server.Close()

	cfg := registryTagsArgs{Limit: 2, Sort: "newest"}
	tags, truncated, err := fetchRegistryTags(context.Background(), server.URL, "team/app", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "v2.0.0,v1.2.0" {
		t.Fatalf("tags = %v, want [v2.0.0 v1.2.0] (newest after reading all pages)", tags)
	}
	if !truncated {
		t.Error("truncated should be true when sort cut to the limit")
	}
}

func TestParseBearerChallenge(t *testing.T) {
	realm, params := parseBearerChallenge(`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:cli/cli:pull"`)
	if realm != "https://ghcr.io/token" {
		t.Fatalf("realm = %q", realm)
	}
	if params["service"] != "ghcr.io" || params["scope"] != "repository:cli/cli:pull" {
		t.Fatalf("params = %+v", params)
	}
}

func TestSplitBearerParamsKeepsQuotedCommas(t *testing.T) {
	parts := splitBearerParams(`service="reg",scope="repository:a/b:pull,push"`)
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d: %v", len(parts), parts)
	}
	if !strings.Contains(parts[1], "pull,push") {
		t.Fatalf("quoted comma was split: %v", parts)
	}
}

func TestNextLinkURL(t *testing.T) {
	got := nextLinkURL("https://reg.example.com", `</v2/team/app/tags/list?n=100&last=v9>; rel="next"`)
	want := "https://reg.example.com/v2/team/app/tags/list?n=100&last=v9"
	if got != want {
		t.Fatalf("nextLinkURL = %q, want %q", got, want)
	}
	if nextLinkURL("https://reg.example.com", "") != "" {
		t.Fatal("empty Link should yield empty next")
	}
}

func TestRegistryKeyMatches(t *testing.T) {
	if !registryKeyMatches("https://index.docker.io/v1/", "hub.docker.com") {
		t.Fatal("docker hub legacy key should match")
	}
	if !registryKeyMatches("ghcr.io", "ghcr.io") {
		t.Fatal("exact host should match")
	}
	if registryKeyMatches("ghcr.io", "quay.io") {
		t.Fatal("different hosts must not match")
	}
}

// TestFetchOCITagsTokenNegotiation exercises the full anonymous OCI flow: a 401
// challenge, the token request, the retry, and Link pagination.
func TestFetchOCITagsTokenNegotiation(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			if r.URL.Query().Get("scope") == "" {
				t.Errorf("token request missing scope")
			}
			_, _ = w.Write([]byte(`{"token":"abc123"}`))
		case strings.HasSuffix(r.URL.Path, "/tags/list"):
			if r.Header.Get("Authorization") != "Bearer abc123" {
				// First (unauthenticated) hit → challenge.
				w.Header().Set("WWW-Authenticate",
					`Bearer realm="`+server.URL+`/token",service="reg",scope="repository:team/app:pull"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("last") == "" {
				// Page 1 → one tag + a next link.
				w.Header().Set("Link", `</v2/team/app/tags/list?n=100&last=v1>; rel="next"`)
				_, _ = w.Write([]byte(`{"name":"team/app","tags":["v1"]}`))
				return
			}
			// Page 2 → final tag, no link.
			_, _ = w.Write([]byte(`{"name":"team/app","tags":["v2"]}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	cfg := registryTagsArgs{Limit: registryTagsDefaultLimit}
	tags, truncated, err := fetchOCITags(context.Background(), server.URL, "team/app", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("did not expect truncation")
	}
	if strings.Join(tags, ",") != "v1,v2" {
		t.Fatalf("tags = %v, want [v1 v2]", tags)
	}
}

func TestFetchOCITagsTruncatesAtLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tags":["a","b","c","d"]}`))
	}))
	defer server.Close()

	cfg := registryTagsArgs{Limit: 2}
	tags, truncated, err := fetchOCITags(context.Background(), server.URL, "team/app", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(tags) != 2 {
		t.Fatalf("tags = %v truncated=%v, want 2 tags truncated", tags, truncated)
	}
}

func TestFetchDockerHubTagsPagination(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page2") == "" {
			_, _ = w.Write([]byte(`{"next":"` + server.URL + `/v2/repositories/library/redis/tags/?page2=1","results":[{"name":"7"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"next":"","results":[{"name":"6"}]}`))
	}))
	defer server.Close()

	cfg := registryTagsArgs{Limit: registryTagsDefaultLimit}
	tags, _, err := fetchDockerHubTags(context.Background(), server.URL, "redis", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(tags, ",") != "7,6" {
		t.Fatalf("tags = %v, want [7 6]", tags)
	}
}

func TestRegistryTagsExecuteEndToEnd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tags":["1.0","1.1"]}`))
	}))
	defer server.Close()

	p := NewBuiltinRegistryTagsPlugin()
	out, err := p.Execute(context.Background(),
		[]string{`{"image":"team/app","registry":"` + server.URL + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1.0") || !strings.Contains(out, "1.1") {
		t.Fatalf("output missing tags: %q", out)
	}
	if !strings.Contains(out, "2 tag(s)") {
		t.Fatalf("output missing count: %q", out)
	}
}
