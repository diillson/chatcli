package lessonq

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/agent/quality"
)

func TestDeriveJobID_Deterministic(t *testing.T) {
	req := quality.LessonRequest{
		Task:    "Edit main.go",
		Attempt: "used full rewrite",
		Outcome: "ERROR: file too large",
		Trigger: "error",
	}
	id1 := DeriveJobID(req)
	id2 := DeriveJobID(req)
	if id1 != id2 {
		t.Fatalf("same request must yield same ID; got %s vs %s", id1, id2)
	}
	if len(string(id1)) != 16 {
		t.Fatalf("JobID should be 16 hex chars; got %d (%s)", len(string(id1)), id1)
	}
}

func TestDeriveJobID_NormalizesWhitespace(t *testing.T) {
	// Trivial whitespace differences in the Task field must collapse
	// to the same ID — caller relies on this for dedupe.
	a := quality.LessonRequest{
		Task:    "Edit main.go",
		Attempt: "x",
		Outcome: "y",
		Trigger: "error",
	}
	b := quality.LessonRequest{
		Task:    "  edit   main.go  ",
		Attempt: "x",
		Outcome: "y",
		Trigger: "error",
	}
	if DeriveJobID(a) != DeriveJobID(b) {
		t.Fatalf("whitespace-only differences must not change JobID: %s vs %s",
			DeriveJobID(a), DeriveJobID(b))
	}
}

func TestDeriveJobID_DifferentTriggers(t *testing.T) {
	base := quality.LessonRequest{Task: "x", Attempt: "y", Outcome: "z"}
	triggers := []string{"error", "hallucination", "low_quality", "manual"}
	seen := make(map[JobID]string)
	for _, tr := range triggers {
		r := base
		r.Trigger = tr
		id := DeriveJobID(r)
		if prev, ok := seen[id]; ok {
			t.Fatalf("triggers %s and %s must yield distinct IDs; collided on %s",
				prev, tr, id)
		}
		seen[id] = tr
	}
}

func TestDeriveJobID_DifferentAttempts(t *testing.T) {
	base := quality.LessonRequest{Task: "x", Outcome: "y", Trigger: "error"}
	r1 := base
	r1.Attempt = "first attempt"
	r2 := base
	r2.Attempt = "second attempt"
	if DeriveJobID(r1) == DeriveJobID(r2) {
		t.Fatal("different attempts must yield distinct IDs")
	}
}

func TestDeriveJobID_IsHex(t *testing.T) {
	req := quality.LessonRequest{Task: "x", Attempt: "y", Outcome: "z", Trigger: "error"}
	id := string(DeriveJobID(req))
	const hexChars = "0123456789abcdef"
	for _, c := range id {
		if !strings.ContainsRune(hexChars, c) {
			t.Fatalf("JobID must be lowercase hex; found %q in %s", c, id)
		}
	}
}
