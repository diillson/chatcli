package plugins

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/diillson/chatcli/cli/agent/park"
)

// TestParkPlugin_DelaySentinel asserts the canonical happy path:
// the plugin returns NewParkError carrying the validated Request.
func TestParkPlugin_DelaySentinel(t *testing.T) {
	p := NewBuiltinParkPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"delay","args":{"duration":"5m","note":"ci"}}`})
	if out != "" {
		t.Fatalf("expected empty output, got %q", out)
	}
	req, ok := park.AsParkError(err)
	if !ok {
		t.Fatalf("expected park sentinel, got %v", err)
	}
	if req.Mode != park.ModeDelay || req.Delay != 5*time.Minute || req.Note != "ci" {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestParkPlugin_UntilRelative(t *testing.T) {
	p := NewBuiltinParkPlugin()
	_, err := p.Execute(context.Background(), []string{`{"cmd":"until","args":{"when":"in 2m"}}`})
	req, ok := park.AsParkError(err)
	if !ok {
		t.Fatalf("expected park sentinel, got %v", err)
	}
	if req.Mode != park.ModeUntil {
		t.Fatalf("expected ModeUntil, got %s", req.Mode)
	}
	d := time.Until(req.Until)
	if d < 90*time.Second || d > 130*time.Second {
		t.Fatalf("unexpected until time: %s (want ~2m from now)", req.Until)
	}
}

func TestParkPlugin_ForURL(t *testing.T) {
	p := NewBuiltinParkPlugin()
	_, err := p.Execute(context.Background(), []string{
		`{"cmd":"for_url","args":{"url":"https://example.com/health","interval":"30s","deadline":"5m","success_when":"status=200..299"}}`,
	})
	req, ok := park.AsParkError(err)
	if !ok {
		t.Fatalf("expected park sentinel, got %v", err)
	}
	if req.Mode != park.ModeForURL {
		t.Fatalf("expected for_url, got %s", req.Mode)
	}
	if req.URL != "https://example.com/health" {
		t.Fatalf("URL mismatch: %s", req.URL)
	}
	if req.Interval != 30*time.Second {
		t.Fatalf("interval mismatch: %s", req.Interval)
	}
	if req.SuccessWhen != "status=200..299" {
		t.Fatalf("success_when mismatch: %s", req.SuccessWhen)
	}
}

func TestParkPlugin_ForCmd_TimeoutAlias(t *testing.T) {
	p := NewBuiltinParkPlugin()
	// "timeout" is accepted as an alias for "deadline".
	_, err := p.Execute(context.Background(), []string{
		`{"cmd":"for_cmd","args":{"cmd":"echo done","interval":"10s","timeout":"3m"}}`,
	})
	req, ok := park.AsParkError(err)
	if !ok {
		t.Fatalf("expected park sentinel, got %v", err)
	}
	if req.Mode != park.ModeForCmd || req.Command != "echo done" {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestParkPlugin_RejectsBareDuration(t *testing.T) {
	// A model emitting a delay without the cmd envelope should hit a
	// clear error rather than silently park.
	p := NewBuiltinParkPlugin()
	_, err := p.Execute(context.Background(), []string{`5m`})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "subcommand") && !strings.Contains(err.Error(), "JSON envelope") {
		t.Fatalf("error message should hint envelope expectation: %v", err)
	}
}

func TestParkPlugin_ArgvForm(t *testing.T) {
	// Argv form is what the agent's tool sanitizer can produce for some
	// providers — must accept it equivalently.
	p := NewBuiltinParkPlugin()
	_, err := p.Execute(context.Background(), []string{"delay", "--duration", "1m", "--note", "x"})
	req, ok := park.AsParkError(err)
	if !ok {
		t.Fatalf("expected park sentinel, got %v", err)
	}
	if req.Delay != time.Minute || req.Note != "x" {
		t.Fatalf("argv form parsed wrong: %+v", req)
	}
}

func TestParkPlugin_ValidationBubblesUp(t *testing.T) {
	p := NewBuiltinParkPlugin()
	// Interval below MinPollInterval triggers Validate failure inside
	// the plugin (not a sentinel).
	_, err := p.Execute(context.Background(), []string{
		`{"cmd":"for_url","args":{"url":"https://x","interval":"1s","deadline":"5m"}}`,
	})
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if _, ok := park.AsParkError(err); ok {
		t.Fatalf("validation failure must NOT return as a park sentinel")
	}
}

func TestParkPlugin_ListEmpty(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	p := NewBuiltinParkPlugin()
	out, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if out != "No parked agents." {
		t.Fatalf("unexpected list output: %q", out)
	}
}

func TestParkPlugin_ListAndNote(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	snap := &park.Snapshot{
		Token: park.NewToken(),
		Park:  park.Request{Mode: park.ModeDelay, Delay: 2 * time.Minute, Note: "reporte #3"},
	}
	if err := snap.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	p := NewBuiltinParkPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"list"}`})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, snap.Token[:8]) || !strings.Contains(out, `note="reporte #3"`) {
		t.Fatalf("list output missing park info: %q", out)
	}

	// Note by token prefix.
	out, err = p.Execute(context.Background(), []string{
		`{"cmd":"note","args":{"token":"` + snap.Token[:8] + `","text":"mude o ciclo para 5m"}}`,
	})
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	if !strings.Contains(out, snap.Token[:8]) {
		t.Fatalf("note confirmation missing token: %q", out)
	}

	// Note without token targets the newest park; "message" alias accepted.
	if _, err = p.Execute(context.Background(), []string{
		`{"cmd":"note","args":{"message":"o deploy terminou"}}`,
	}); err != nil {
		t.Fatalf("note latest: %v", err)
	}

	got, err := park.Load(snap.Token)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"mude o ciclo para 5m", "o deploy terminou"}
	if len(got.PendingUserDirectives) != 2 || got.PendingUserDirectives[0] != want[0] || got.PendingUserDirectives[1] != want[1] {
		t.Fatalf("directives = %v, want %v", got.PendingUserDirectives, want)
	}
}

func TestParkPlugin_NoteErrors(t *testing.T) {
	t.Setenv("CHATCLI_PARK_DIR", t.TempDir())
	p := NewBuiltinParkPlugin()

	// No parks at all.
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"note","args":{"text":"x"}}`}); err == nil {
		t.Fatal("note without any parked agent must error")
	}

	snap := &park.Snapshot{Token: park.NewToken(), Park: park.Request{Mode: park.ModeDelay, Delay: time.Minute}}
	if err := snap.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Missing text.
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"note","args":{"token":"` + snap.Token[:8] + `"}}`}); err == nil {
		t.Fatal("note without text must error")
	}
	// Unknown token prefix.
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"note","args":{"token":"zzzz9999","text":"x"}}`}); err == nil {
		t.Fatal("note with unknown token prefix must error")
	}
}
