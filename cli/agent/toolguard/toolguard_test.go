package toolguard

import (
	"strings"
	"testing"
)

func TestObserve_SameSigWarns(t *testing.T) {
	g := New(Config{}) // defaults: WarnAfterSameSig=2
	if d := g.Observe("@read", `{"path":"x"}`, "no such file", true); d.Guidance != "" {
		t.Fatalf("first failure should not warn, got %q", d.Guidance)
	}
	d := g.Observe("@read", `{"path":"x"}`, "no such file", true)
	if d.Guidance == "" {
		t.Fatal("second identical failure should warn")
	}
	if !strings.Contains(d.Guidance, "same arguments") {
		t.Errorf("expected same-args guidance, got %q", d.Guidance)
	}
	if !strings.Contains(d.Guidance, "no such file") {
		t.Errorf("expected last error echoed, got %q", d.Guidance)
	}
}

func TestObserve_ToolDriftWarns(t *testing.T) {
	g := New(Config{}) // WarnAfterToolFailures=3
	// Different args each time -> not same-sig, but same tool.
	g.Observe("@search", `{"q":"a"}`, "err", true)
	g.Observe("@search", `{"q":"b"}`, "err", true)
	d := g.Observe("@search", `{"q":"c"}`, "err", true)
	if d.Guidance == "" || !strings.Contains(d.Guidance, "in a row") {
		t.Errorf("expected drift guidance on 3rd failure, got %q", d.Guidance)
	}
}

func TestObserve_SuccessResetsStreak(t *testing.T) {
	g := New(Config{})
	g.Observe("@read", `{"path":"x"}`, "err", true)
	g.Observe("@read", `{"path":"x"}`, "", false) // success resets
	if d := g.Observe("@read", `{"path":"x"}`, "err", true); d.Guidance != "" {
		t.Errorf("streak should reset after success, got %q", d.Guidance)
	}
	if len(g.sortedSigs()) != 1 {
		// only the post-reset failure remains
		t.Errorf("expected 1 tracked sig after reset, got %v", g.sortedSigs())
	}
}

func TestObserve_Halt(t *testing.T) {
	g := New(Config{HaltAfterSameSig: 3})
	g.Observe("@x", "a", "boom", true)
	g.Observe("@x", "a", "boom", true)
	d := g.Observe("@x", "a", "boom", true)
	if !d.Halt {
		t.Fatal("expected halt on 3rd identical failure")
	}
	if !strings.Contains(d.Guidance, "halted") {
		t.Errorf("expected halt message, got %q", d.Guidance)
	}
}

func TestObserve_NoWarnSpam(t *testing.T) {
	g := New(Config{})
	g.Observe("@read", "a", "e", true)
	first := g.Observe("@read", "a", "e", true)  // warns
	second := g.Observe("@read", "a", "e", true) // already warned -> no repeat
	if first.Guidance == "" {
		t.Fatal("expected first warning")
	}
	if second.Guidance != "" {
		t.Errorf("should not re-warn for same sig, got %q", second.Guidance)
	}
}

func TestSignatureNormalizesWhitespace(t *testing.T) {
	if Signature("@t", "a   b\n c") != Signature("@t", "a b c") {
		t.Error("signature should collapse whitespace")
	}
}
