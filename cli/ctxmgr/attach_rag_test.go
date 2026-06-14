/*
 * ChatCLI - Regression test: AttachContextWithOptions must propagate every
 * AttachOptions field, including RetrievalTopK (the --rag knob). It was
 * silently dropped from the attachment struct literal, so --rag registered an
 * attachment but never enabled per-turn retrieval.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package ctxmgr

import "testing"

func TestAttachContextWithOptions_PropagatesAllOptions(t *testing.T) {
	m := newTestManager(t)
	fc := addTestContext(t, m, "rag-ctx", sampleFiles(), ModeFull, nil)

	opts := AttachOptions{Priority: 3, RetrievalTopK: 7, SelectedChunks: []int{1, 2}}
	if err := m.AttachContextWithOptions("sess", fc.ID, opts); err != nil {
		t.Fatalf("AttachContextWithOptions: %v", err)
	}

	atts := m.attachedContexts["sess"]
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	a := atts[0]
	if a.RetrievalTopK != 7 {
		t.Errorf("RetrievalTopK = %d, want 7 (regression: dropped from the struct literal, breaking --rag)", a.RetrievalTopK)
	}
	if a.Priority != 3 {
		t.Errorf("Priority = %d, want 3", a.Priority)
	}
	if len(a.SelectedChunks) != 2 || a.SelectedChunks[0] != 1 || a.SelectedChunks[1] != 2 {
		t.Errorf("SelectedChunks = %v, want [1 2]", a.SelectedChunks)
	}
}
