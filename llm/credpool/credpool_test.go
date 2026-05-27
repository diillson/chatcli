package credpool

import (
	"testing"
	"time"
)

func TestParseKeys(t *testing.T) {
	got := ParseKeys("k1, k2 ;k3\nk4\tk1")
	want := []string{"k1", "k2", "k3", "k4"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("at %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestSingleKeyPassThrough(t *testing.T) {
	p := New([]string{"only"}, FillFirst, time.Minute)
	for i := 0; i < 5; i++ {
		k, ok := p.Next()
		if !ok || k != "only" {
			t.Fatalf("single key should always return 'only', got %q ok=%v", k, ok)
		}
	}
}

func TestFillFirstRotatesOnExhaustion(t *testing.T) {
	p := New([]string{"a", "b"}, FillFirst, time.Minute)
	if k, _ := p.Next(); k != "a" {
		t.Fatalf("expected a, got %q", k)
	}
	p.MarkExhausted("a")
	if k, _ := p.Next(); k != "b" {
		t.Fatalf("after exhausting a, expected b, got %q", k)
	}
	if p.Available() != 1 {
		t.Errorf("expected 1 available, got %d", p.Available())
	}
	// Recovery clears the cooldown.
	p.MarkOK("a")
	if k, _ := p.Next(); k != "a" {
		t.Errorf("after recovery expected a, got %q", k)
	}
}

func TestRoundRobinSpreads(t *testing.T) {
	p := New([]string{"a", "b", "c"}, RoundRobin, time.Minute)
	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		k, _ := p.Next()
		seen[k]++
	}
	for _, k := range []string{"a", "b", "c"} {
		if seen[k] != 2 {
			t.Errorf("round-robin should hit %q twice, got %d", k, seen[k])
		}
	}
}

func TestAllParkedReturnsSoonest(t *testing.T) {
	base := time.Now()
	clock := base
	p := New([]string{"a", "b"}, FillFirst, time.Minute)
	p.now = func() time.Time { return clock }

	p.MarkExhausted("a") // a parked until base+60s
	clock = base.Add(10 * time.Second)
	p.MarkExhausted("b") // b parked until base+70s

	// Both parked; a recovers first.
	if k, ok := p.Next(); !ok || k != "a" {
		t.Errorf("expected soonest-recovering key a, got %q ok=%v", k, ok)
	}
}

func TestEmptyPool(t *testing.T) {
	p := New(nil, FillFirst, time.Minute)
	if _, ok := p.Next(); ok {
		t.Error("empty pool should return ok=false")
	}
}

func TestFingerprintNoLeak(t *testing.T) {
	fp := Fingerprint("super-secret-key")
	if fp == "super-secret-key" || len(fp) > 12 {
		t.Errorf("fingerprint must not reveal the key, got %q", fp)
	}
	if Fingerprint("") != "none" {
		t.Error("empty key should fingerprint as none")
	}
}

func TestRegistry(t *testing.T) {
	ResetRegistry()
	t.Cleanup(ResetRegistry)
	Register("OPENAI", New([]string{"a"}, FillFirst, time.Minute))
	if _, ok := For("OPENAI"); !ok {
		t.Error("expected registered pool")
	}
	if _, ok := For("MISSING"); ok {
		t.Error("unregistered provider should not be found")
	}
}
