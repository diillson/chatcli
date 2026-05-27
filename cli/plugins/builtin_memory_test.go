package plugins

import (
	"context"
	"strings"
	"testing"
)

func TestParseMemoryInvocation_Envelope(t *testing.T) {
	cmd, inner, err := parseMemoryInvocation([]string{`{"cmd":"remember","args":{"content":"x","category":"personal"}}`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd != "remember" {
		t.Errorf("cmd = %q, want remember", cmd)
	}
	if !strings.Contains(inner, `"content":"x"`) {
		t.Errorf("inner missing content: %s", inner)
	}
}

func TestParseMemoryInvocation_Aliases(t *testing.T) {
	for _, alias := range []string{"add", "store", "note"} {
		cmd, _, err := parseMemoryInvocation([]string{`{"cmd":"` + alias + `","args":{}}`})
		if err != nil || cmd != "remember" {
			t.Errorf("alias %q -> (%q,%v), want remember", alias, cmd, err)
		}
	}
}

func TestParseProfileFields(t *testing.T) {
	// Wrapped form.
	f, err := parseProfileFields(`{"fields":{"role":"SRE","certifications":["AWS","CKA"]}}`)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if f["role"] != "SRE" {
		t.Errorf("role = %q", f["role"])
	}
	if f["certifications"] != "AWS, CKA" {
		t.Errorf("array should join with comma, got %q", f["certifications"])
	}

	// Bare form (no wrapper).
	f2, err := parseProfileFields(`{"company":"Acme"}`)
	if err != nil || f2["company"] != "Acme" {
		t.Errorf("bare form failed: %v / %v", f2, err)
	}
}

// fakeMemAdapter records calls for dispatch testing.
type fakeMemAdapter struct {
	lastRemember [2]string
	lastProfile  map[string]string
	lastForget   string
	lastRecall   string
}

func (f *fakeMemAdapter) Remember(content, category string) (string, error) {
	f.lastRemember = [2]string{content, category}
	return "ok", nil
}
func (f *fakeMemAdapter) UpdateProfile(u map[string]string) (string, error) {
	f.lastProfile = u
	return "ok", nil
}
func (f *fakeMemAdapter) Forget(m string) (string, error) { f.lastForget = m; return "ok", nil }
func (f *fakeMemAdapter) Recall(q string) (string, error) { f.lastRecall = q; return "ok", nil }

func TestBuiltinMemory_Dispatch(t *testing.T) {
	fake := &fakeMemAdapter{}
	SetMemoryAdapter(fake)
	t.Cleanup(func() { SetMemoryAdapter(nil) })

	p := NewBuiltinMemoryPlugin()
	ctx := context.Background()

	if _, err := p.Execute(ctx, []string{`{"cmd":"remember","args":{"content":"User got AWS cert","category":"personal"}}`}); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if fake.lastRemember[0] != "User got AWS cert" || fake.lastRemember[1] != "personal" {
		t.Errorf("remember dispatch wrong: %v", fake.lastRemember)
	}

	if _, err := p.Execute(ctx, []string{`{"cmd":"profile","args":{"fields":{"company":"Acme"}}}`}); err != nil {
		t.Fatalf("profile: %v", err)
	}
	if fake.lastProfile["company"] != "Acme" {
		t.Errorf("profile dispatch wrong: %v", fake.lastProfile)
	}

	if _, err := p.Execute(ctx, []string{`{"cmd":"forget","args":{"match":"tabs"}}`}); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if fake.lastForget != "tabs" {
		t.Errorf("forget dispatch wrong: %q", fake.lastForget)
	}

	// Missing required field is a clean error, not a panic.
	if _, err := p.Execute(ctx, []string{`{"cmd":"remember","args":{}}`}); err == nil {
		t.Error("expected error for remember without content")
	}
}

func TestBuiltinMemory_NoAdapter(t *testing.T) {
	SetMemoryAdapter(nil)
	p := NewBuiltinMemoryPlugin()
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"recall","args":{}}`}); err == nil {
		t.Error("expected error when no adapter is wired")
	}
}
