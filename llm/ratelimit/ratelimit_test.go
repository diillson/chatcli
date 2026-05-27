package ratelimit

import (
	"net/http"
	"testing"
)

func TestParse(t *testing.T) {
	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "100")
	h.Set("x-ratelimit-remaining-requests", "25")
	h.Set("x-ratelimit-reset-requests", "1m30s")
	h.Set("x-ratelimit-limit-tokens", "100000")
	h.Set("x-ratelimit-remaining-tokens", "90000")
	h.Set("x-ratelimit-reset-tokens", "12")

	req, tok := Parse(h)
	if req.Limit != 100 || req.Remaining != 25 {
		t.Errorf("req bucket wrong: %+v", req)
	}
	if req.ResetSecs != 90 {
		t.Errorf("expected 90s reset, got %v", req.ResetSecs)
	}
	if got := req.UsagePct(); got < 0.74 || got > 0.76 {
		t.Errorf("expected ~0.75 usage, got %v", got)
	}
	if tok.Limit != 100000 || tok.ResetSecs != 12 {
		t.Errorf("tok bucket wrong: %+v", tok)
	}
}

func TestParse_Empty(t *testing.T) {
	req, tok := Parse(http.Header{})
	if req.Valid() || tok.Valid() {
		t.Error("empty headers should yield invalid buckets")
	}
}

func TestRecordGetAll(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	h := http.Header{}
	h.Set("x-ratelimit-limit-requests", "10")
	h.Set("x-ratelimit-remaining-requests", "3")
	Record("OPENAI", h)

	// Empty headers must not overwrite the good snapshot.
	Record("OPENAI", http.Header{})

	s, ok := Get("OPENAI")
	if !ok || s.Requests.Remaining != 3 {
		t.Fatalf("expected stored snapshot, got %+v ok=%v", s, ok)
	}
	if !s.Has() {
		t.Error("snapshot should report Has()")
	}
	if len(All()) != 1 {
		t.Errorf("expected 1 snapshot, got %d", len(All()))
	}

	if _, ok := Get("MISSING"); ok {
		t.Error("missing provider should not be found")
	}
}

func TestRemainingSecondsNeverNegative(t *testing.T) {
	b := Bucket{Limit: 10, Remaining: 1, ResetSecs: 0}
	if b.RemainingSeconds() < 0 {
		t.Error("remaining seconds must not be negative")
	}
}
