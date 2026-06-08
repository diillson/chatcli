package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserUA_DefaultAndOverride(t *testing.T) {
	// Default: the pinned Chrome UA, never the old bot string.
	t.Setenv("CHATCLI_WEBFETCH_USER_AGENT", "")
	if got := browserUA(); got != browserUserAgent {
		t.Fatalf("expected pinned default, got %q", got)
	}
	if strings.Contains(strings.ToLower(browserUA()), "bot") {
		t.Fatalf("default UA must not advertise a bot: %q", browserUA())
	}

	// Override wins, and is trimmed.
	t.Setenv("CHATCLI_WEBFETCH_USER_AGENT", "  CustomAgent/9.9  ")
	if got := browserUA(); got != "CustomAgent/9.9" {
		t.Fatalf("expected trimmed override, got %q", got)
	}

	// Whitespace-only falls back to the default.
	t.Setenv("CHATCLI_WEBFETCH_USER_AGENT", "   ")
	if got := browserUA(); got != browserUserAgent {
		t.Fatalf("whitespace override should fall back to default, got %q", got)
	}
}

func TestParseWebSearchArgs(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantQuery   string
		wantMaxRest int
	}{
		{"json cmd shape", []string{`{"cmd":"search","args":{"query":"go ctx","maxResults":3}}`}, "go ctx", 3},
		{"json flat snake_case", []string{`{"query":"go ctx","max_results":7}`}, "go ctx", 7},
		{"json flat camelCase", []string{`{"query":"go ctx","maxResults":5}`}, "go ctx", 5},
		{"json no max defaults to 10", []string{`{"query":"only"}`}, "only", 10},
		{"positional flags", []string{"search", "--query", "go ctx", "--maxResults", "4"}, "go ctx", 4},
		{"positional bare words joined", []string{"search", "golang", "context"}, "golang context", 10},
		{"simple join (no subcmd)", []string{"golang", "context"}, "golang context", 10},
		{"non-json single arg treated as query", []string{"just text"}, "just text", 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, m := parseWebSearchArgs(tc.args)
			if q != tc.wantQuery {
				t.Errorf("query = %q, want %q", q, tc.wantQuery)
			}
			if m != tc.wantMaxRest {
				t.Errorf("maxResults = %d, want %d", m, tc.wantMaxRest)
			}
		})
	}
}

func TestParseSearchJSON_NotJSON(t *testing.T) {
	if _, _, ok := parseSearchJSON("not json at all"); ok {
		t.Fatal("expected ok=false for non-JSON input")
	}
}

func TestFormatSearchResults(t *testing.T) {
	results := []searchResult{
		{Title: "First", URL: "https://a.example", Snippet: "snippet a"},
		{Title: "Second", URL: "https://b.example"}, // no snippet → snippet line omitted
	}
	var streamed []string
	out := formatSearchResults("q", ProviderDuckDuckGo, results, func(s string) { streamed = append(streamed, s) })

	if !strings.Contains(out, `Search results for: "q" (via duckduckgo)`) {
		t.Errorf("missing header: %q", out)
	}
	if !strings.Contains(out, "1. First") || !strings.Contains(out, "https://a.example") {
		t.Errorf("missing first result: %q", out)
	}
	if !strings.Contains(out, "snippet a") {
		t.Errorf("missing snippet: %q", out)
	}
	if strings.Contains(out, "   \n   \n") {
		t.Errorf("second result should not emit an empty snippet line: %q", out)
	}
	if len(streamed) != 2 {
		t.Errorf("expected 2 streamed preview lines, got %d", len(streamed))
	}
}

// End-to-end websearch through the provider chain, exercising
// ExecuteWithStream + runSearchChain + formatSearchResults without real
// network: force the SearxNG provider at a local httptest fixture.
func TestWebSearch_ExecuteThroughChain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(searxngResponse{
			Results: []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Content string `json:"content"`
			}{
				{Title: "Go context", URL: "https://pkg.go.dev/context", Content: "Package context"},
			},
		})
	}))
	defer srv.Close()

	t.Setenv("CHATCLI_WEBSEARCH_PROVIDER", "searxng")
	t.Setenv("SEARXNG_URL", srv.URL)

	plug := NewBuiltinWebSearchPlugin()
	out, err := plug.Execute(context.Background(), []string{"search", "--query", "go context"})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "Go context") || !strings.Contains(out, "via searxng") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestWebSearch_EmptyQueryErrors(t *testing.T) {
	plug := NewBuiltinWebSearchPlugin()
	// "search" subcommand with only flags and no query words → empty query,
	// which ExecuteWithStream rejects before touching any provider.
	if _, err := plug.Execute(context.Background(), []string{"search", "--maxResults", "5"}); err == nil {
		t.Fatal("expected error for missing query")
	}
}

func TestParseFetchArgs_PositionalFlags(t *testing.T) {
	got, err := parseFetchArgs([]string{
		"fetch", "--url", "https://x.example",
		"--max-length", "1234",
		"--filter", "^foo",
		"--exclude", "bar",
		"--from-line", "2", "--to-line", "9",
		"--save-to-file", "--save-path", "out.txt",
		"--raw",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.URL != "https://x.example" || got.MaxLength != 1234 || got.Filter != "^foo" ||
		got.Exclude != "bar" || got.FromLine != 2 || got.ToLine != 9 ||
		!got.SaveToFile || got.SavePath != "out.txt" || !got.Raw {
		t.Fatalf("unexpected parse result: %+v", got)
	}
}

func TestParseFetchArgs_ImplicitURL(t *testing.T) {
	got, err := parseFetchArgs([]string{"https://implicit.example"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.URL != "https://implicit.example" {
		t.Fatalf("expected implicit URL, got %+v", got)
	}
}

func TestBuildFetchOutput(t *testing.T) {
	t.Run("no save returns filtered untouched when small", func(t *testing.T) {
		p := fetchArgs{MaxLength: 100}
		out := buildFetchOutput(p, "hello", "hello", "", false)
		if out != "hello" {
			t.Fatalf("got %q", out)
		}
	})

	t.Run("max_length truncation", func(t *testing.T) {
		p := fetchArgs{MaxLength: 5}
		out := buildFetchOutput(p, "0123456789", "0123456789", "", false)
		if !strings.HasPrefix(out, "01234") || !strings.Contains(out, "(truncated)") {
			t.Fatalf("expected truncation, got %q", out)
		}
	})

	t.Run("saved path adds default prefix", func(t *testing.T) {
		p := fetchArgs{MaxLength: 100}
		out := buildFetchOutput(p, "body", "body", "/scratch/f.txt", false)
		if !strings.Contains(out, "full response saved to /scratch/f.txt") {
			t.Fatalf("missing saved-path prefix: %q", out)
		}
	})

	t.Run("auto-saved adds preview prefix and caps preview", func(t *testing.T) {
		full := strings.Repeat("x", 9000)
		p := fetchArgs{MaxLength: 8000}
		out := buildFetchOutput(p, full, full, "/scratch/f.txt", true)
		if !strings.Contains(out, "auto-saved") {
			t.Fatalf("missing auto-saved prefix: %q", out)
		}
		if !strings.Contains(out, "auto-truncated") {
			t.Fatalf("expected preview cap marker: %q", out)
		}
	})
}

func TestWebFetchAutoSaveThreshold_EnvOverride(t *testing.T) {
	t.Setenv("CHATCLI_WEBFETCH_AUTOSAVE_BYTES", "")
	if got := webFetchAutoSaveThreshold(); got != defaultWebFetchAutoSaveSize {
		t.Fatalf("expected default %d, got %d", defaultWebFetchAutoSaveSize, got)
	}
	t.Setenv("CHATCLI_WEBFETCH_AUTOSAVE_BYTES", "42")
	if got := webFetchAutoSaveThreshold(); got != 42 {
		t.Fatalf("expected override 42, got %d", got)
	}
	t.Setenv("CHATCLI_WEBFETCH_AUTOSAVE_BYTES", "garbage")
	if got := webFetchAutoSaveThreshold(); got != defaultWebFetchAutoSaveSize {
		t.Fatalf("garbage should fall back to default, got %d", got)
	}
}

func TestSaveFetchToScratch(t *testing.T) {
	scratch := t.TempDir()
	t.Setenv("CHATCLI_AGENT_TMPDIR", scratch)

	t.Run("no save requested returns empty", func(t *testing.T) {
		path, err := saveFetchToScratch(fetchArgs{SaveToFile: false}, "body")
		if err != nil || path != "" {
			t.Fatalf("expected empty path no error, got %q, %v", path, err)
		}
	})

	t.Run("writes confined to scratch", func(t *testing.T) {
		path, err := saveFetchToScratch(fetchArgs{SaveToFile: true, SavePath: "ok.txt"}, "data")
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if !strings.HasPrefix(path, scratch) {
			t.Fatalf("path %q not under scratch %q", path, scratch)
		}
	})

	t.Run("absolute escape path is reduced to basename", func(t *testing.T) {
		// filepath.Base strips the directory, so this stays inside scratch.
		path, err := saveFetchToScratch(fetchArgs{SaveToFile: true, SavePath: "/etc/passwd"}, "data")
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		if !strings.HasPrefix(path, scratch) {
			t.Fatalf("escape not contained: %q", path)
		}
	})
}
