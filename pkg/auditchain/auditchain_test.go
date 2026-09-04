/*
 * ChatCLI - Command Line Interface for LLM interaction
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package auditchain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sample struct {
	Kind string `json:"kind"`
	N    int    `json:"n"`
}

func never() bool { return false }

func TestWriter_ChainsAndVerifies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	w, err := Open(path, Options{Seal: never})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if err := w.Append(sample{Kind: "llm", N: i}); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	rep, err := Verify(path, nil)
	if err != nil || !rep.Intact() || rep.Chained != 3 || rep.Entries != 3 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	if seq, last := Tail(path); seq != 3 || last == "" {
		t.Fatalf("tail = %d %q", seq, last)
	}
	// A restarted writer continues the chain.
	w2, _ := Open(path, Options{Seal: never})
	_ = w2.Append(sample{Kind: "llm", N: 4})
	_ = w2.Close()
	if rep, _ := Verify(path, nil); !rep.Intact() || rep.Chained != 4 {
		t.Fatalf("continued chain: %+v", rep)
	}
	// Tampering with a middle line breaks it there.
	raw, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	lines[1] = strings.Replace(lines[1], `"n":2`, `"n":20`, 1)
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
	if rep, _ := Verify(path, nil); rep.Intact() || rep.BrokenAt != 2 {
		t.Fatalf("tamper must break at line 2: %+v", rep)
	}
}

func TestWriter_TwoWritersShareOneTrail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a, _ := Open(path, Options{Seal: never})
	b, _ := Open(path, Options{Seal: never})
	defer func() { _ = a.Close(); _ = b.Close() }()
	for i := 0; i < 5; i++ {
		if err := a.Append(sample{Kind: "llm", N: i}); err != nil {
			t.Fatal(err)
		}
		if err := b.Append(sample{Kind: "grpc", N: i}); err != nil {
			t.Fatal(err)
		}
	}
	rep, _ := Verify(path, nil)
	if !rep.Intact() || rep.Chained != 10 {
		t.Fatalf("interleaved writers must form one chain: %+v", rep)
	}
	if seq, _ := Tail(path); seq != 10 {
		t.Fatalf("seq = %d", seq)
	}
}

func TestWriter_RotatesAndVerifyFollowsTheBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	w, _ := Open(path, Options{Seal: never, MaxBytes: 200})
	for i := 0; i < 12; i++ {
		if err := w.Append(sample{Kind: "llm", N: i}); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	rotated := Rotated(path)
	if len(rotated) == 0 {
		t.Fatal("expected rotated files")
	}
	live, _ := Verify(path, nil)
	if !live.Intact() || live.RotatedFrom == "" {
		t.Fatalf("live file must continue a rotated one: %+v", live)
	}
	total := live.Chained
	for _, r := range rotated {
		rep, _ := Verify(r, nil)
		if !rep.Intact() {
			t.Fatalf("rotated %s: %+v", r, rep)
		}
		total += rep.Chained
	}
	if total != 12 {
		t.Fatalf("entries across files = %d", total)
	}
	if seq, _ := Tail(path); seq != 12 {
		t.Fatalf("seq carries over rotation: %d", seq)
	}
	// Retention removes rotated files past the cutoff and never the live one.
	old := time.Now().Add(-48 * time.Hour)
	for _, r := range rotated {
		_ = os.Chtimes(r, old, old)
	}
	if n := PruneRotated(path, time.Now().Add(-24*time.Hour)); n != len(rotated) {
		t.Fatalf("pruned %d, want %d", n, len(rotated))
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("live trail must survive retention")
	}
	// A writer that still holds the rotated file notices and reopens.
	w2, _ := Open(path, Options{Seal: never, MaxBytes: 200})
	w3, _ := Open(path, Options{Seal: never, MaxBytes: 200})
	for i := 0; i < 6; i++ {
		_ = w2.Append(sample{N: i})
	}
	if err := w3.Append(sample{Kind: "late"}); err != nil {
		t.Fatal(err)
	}
	_ = w2.Close()
	_ = w3.Close()
	if rep, _ := Verify(path, nil); !rep.Intact() {
		t.Fatalf("writer holding a rotated file must reopen the live path: %+v", rep)
	}
}

func TestVerify_TornTailIsNotTampering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	w, _ := Open(path, Options{Seal: never})
	_ = w.Append(sample{N: 1})
	_ = w.Append(sample{N: 2})
	_ = w.Close()
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"kind":"llm","seq":3,"prev_h`)
	_ = f.Close()
	rep, err := Verify(path, nil)
	if err != nil || !rep.Intact() || !rep.Torn || rep.Chained != 2 {
		t.Fatalf("torn tail: %+v err=%v", rep, err)
	}
	if seq, _ := Tail(path); seq != 2 {
		t.Fatalf("tail skips the torn line: %d", seq)
	}
	// The next append starts on a fresh line and re-links to entry 2.
	w2, _ := Open(path, Options{Seal: never})
	if err := w2.Append(sample{N: 3}); err != nil {
		t.Fatal(err)
	}
	_ = w2.Close()
	rep, _ = Verify(path, nil)
	if !rep.Intact() || rep.Chained != 3 {
		t.Fatalf("after torn tail: %+v", rep)
	}
	raw, _ := os.ReadFile(path)
	if strings.Count(string(raw), "\n") != 4 {
		t.Fatalf("torn line must be terminated before the new entry:\n%s", raw)
	}
}

func TestVerify_LegacyLinesUseTheOwnerHasher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	// Two version-1 lines (no chain_v) with fake hashes, then a v2 line.
	legacy := []string{`{"kind":"llm","seq":1,"hash":"h1"}`, `{"kind":"llm","seq":2,"prev_hash":"h1","hash":"h2"}`}
	_ = os.WriteFile(path, []byte(strings.Join(legacy, "\n")+"\n"), 0o600)
	w, _ := Open(path, Options{Seal: never})
	_ = w.Append(sample{N: 3})
	_ = w.Close()
	calls := 0
	hasher := func(line []byte, prev string) (string, bool) {
		calls++
		var e struct {
			Prev string `json:"prev_hash"`
			Hash string `json:"hash"`
		}
		_ = json.Unmarshal(line, &e)
		return e.Hash, e.Prev == prev
	}
	rep, _ := Verify(path, hasher)
	if !rep.Intact() || rep.Chained != 3 || calls != 2 {
		t.Fatalf("legacy verification: %+v calls=%d", rep, calls)
	}
	if rep, _ := Verify(path, nil); !rep.Intact() || rep.Legacy != 2 || rep.Chained != 1 {
		t.Fatalf("without a hasher legacy lines are counted, not verified: %+v", rep)
	}
}

func TestWriter_SealsLinesWhenAsked(t *testing.T) {
	t.Setenv("CHATCLI_ENCRYPTION_KEY", "audit-chain-test-key")
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Append(sample{Kind: "secret", N: 1})
	_ = w.Append(sample{Kind: "secret", N: 2})
	_ = w.Close()
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "secret") || !strings.HasPrefix(string(raw), sealedPrefix) {
		t.Fatalf("lines must be sealed on disk:\n%s", raw)
	}
	rep, _ := Verify(path, nil)
	if !rep.Intact() || rep.Sealed != 2 || rep.Chained != 2 {
		t.Fatalf("sealed chain: %+v", rep)
	}
	if seq, _ := Tail(path); seq != 2 {
		t.Fatalf("tail through sealed lines: %d", seq)
	}
}
