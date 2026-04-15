package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper: spin up a test server returning the given body for any GET.
func fixtureServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWebFetch_FilterKeepsMatchingLines(t *testing.T) {
	body := strings.Join([]string{
		"# HELP go_goroutines Number of goroutines",
		"go_goroutines 42",
		"chatcli_tool_calls_total 10",
		"chatcli_errors_total 0",
		"unrelated_metric 1",
	}, "\n")
	srv := fixtureServer(t, body)

	plug := NewBuiltinWebFetchPlugin()
	args := mustMarshal(t, map[string]interface{}{
		"cmd":  "fetch",
		"args": map[string]interface{}{"url": srv.URL, "raw": true, "filter": "^chatcli_"},
	})
	out, err := plug.Execute(context.Background(), []string{args})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "chatcli_tool_calls_total 10") {
		t.Fatalf("expected filtered metric in output, got %q", out)
	}
	if strings.Contains(out, "unrelated_metric") {
		t.Fatalf("filter should have dropped unrelated_metric, output: %q", out)
	}
}

func TestWebFetch_ExcludeAndLineRange(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, fmt.Sprintf("metric_%d %d", i, i*10))
	}
	body := strings.Join(lines, "\n")
	srv := fixtureServer(t, body)

	plug := NewBuiltinWebFetchPlugin()
	args := mustMarshal(t, map[string]interface{}{
		"cmd": "fetch",
		"args": map[string]interface{}{
			"url": srv.URL, "raw": true,
			"exclude":   "_5 ",
			"from_line": 2,
			"to_line":   5,
		},
	})
	out, err := plug.Execute(context.Background(), []string{args})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	// After exclude, lines 0..19 minus the one matching "_5 " (metric_5 50).
	// from_line=2 to_line=5 slices the filtered view (1-based inclusive).
	got := strings.Split(out, "\n")
	for _, line := range got {
		if strings.Contains(line, "metric_5 ") {
			t.Fatalf("exclude failed: %q still present", line)
		}
	}
	// Range clipped to 4 lines.
	if len(got) != 4 {
		t.Fatalf("expected 4 lines after range, got %d: %v", len(got), got)
	}
}

func TestWebFetch_SaveToFileWritesSessionDir(t *testing.T) {
	body := "line1\nline2\nline3"
	srv := fixtureServer(t, body)

	scratch := t.TempDir()
	t.Setenv("CHATCLI_AGENT_TMPDIR", scratch)

	plug := NewBuiltinWebFetchPlugin()
	args := mustMarshal(t, map[string]interface{}{
		"cmd": "fetch",
		"args": map[string]interface{}{
			"url":          srv.URL,
			"raw":          true,
			"save_to_file": true,
			"save_path":    "fixture.txt",
		},
	})
	out, err := plug.Execute(context.Background(), []string{args})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	savedPath := filepath.Join(scratch, "fixture.txt")
	data, err := os.ReadFile(savedPath)
	if err != nil {
		t.Fatalf("expected file at %s: %v", savedPath, err)
	}
	if string(data) != body {
		t.Fatalf("saved content mismatch: got %q want %q", data, body)
	}
	if !strings.Contains(out, savedPath) {
		t.Fatalf("output should include saved path marker, got %q", out)
	}
}

func TestApplyLineFilters_InvalidRegex(t *testing.T) {
	_, err := applyLineFilters("x", "[", "", 0, 0)
	if err == nil {
		t.Fatal("expected error for invalid filter regex")
	}
}

func mustMarshal(t *testing.T, v interface{}) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
