/*
 * ChatCLI - Tests for the keyed fact-removal hook registry (facts.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"testing"

	"go.uber.org/zap"
)

func newHookIndex(t *testing.T) *FactIndex {
	t.Helper()
	return NewFactIndex(t.TempDir(), DefaultConfig(), zap.NewNop())
}

func TestRemovalHooks_MultipleConsumersAllFire(t *testing.T) {
	fi := newHookIndex(t)
	var vector, graph [][]string
	fi.SetRemovalHook("vector", func(ids []string) { vector = append(vector, ids) })
	fi.SetRemovalHook("graph", func(ids []string) { graph = append(graph, ids) })

	fi.AddFact("the deploy uses helm charts", "gotcha", nil)
	if removed := fi.ForgetMatching("helm"); len(removed) == 0 {
		t.Fatal("ForgetMatching found nothing")
	}
	if len(vector) != 1 || len(graph) != 1 {
		t.Fatalf("hooks fired vector=%d graph=%d, want 1/1", len(vector), len(graph))
	}
}

func TestRemovalHooks_DetachOneKeepsOther(t *testing.T) {
	fi := newHookIndex(t)
	var vector, graph int
	fi.SetRemovalHook("vector", func([]string) { vector++ })
	fi.SetRemovalHook("graph", func([]string) { graph++ })
	fi.SetRemovalHook("vector", nil) // detach vector only

	fi.AddFact("temporary fact to delete", "general", nil)
	fi.ForgetMatching("temporary")
	if vector != 0 {
		t.Fatalf("detached vector hook fired %d times", vector)
	}
	if graph != 1 {
		t.Fatalf("graph hook fired %d times, want 1 (must survive vector detach)", graph)
	}
}

func TestSetOnRemovedLegacyDelegateStillWorks(t *testing.T) {
	fi := newHookIndex(t)
	var calls int
	fi.SetOnRemoved(func([]string) { calls++ })
	fi.AddFact("legacy hook fact", "general", nil)
	fi.ForgetMatching("legacy")
	if calls != 1 {
		t.Fatalf("legacy SetOnRemoved hook fired %d times, want 1", calls)
	}
	fi.SetOnRemoved(nil)
	fi.AddFact("second fact for round two", "general", nil)
	fi.ForgetMatching("second")
	if calls != 1 {
		t.Fatalf("nil SetOnRemoved did not detach (calls=%d)", calls)
	}
}

func TestAttachVectorIndexNilPreservesGraphHook(t *testing.T) {
	mgr := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	var graph int
	mgr.Facts.SetRemovalHook("graph", func([]string) { graph++ })

	mgr.AttachVectorIndex(nil) // /config reload with embeddings disabled

	mgr.Facts.AddFact("fact that will be forgotten", "general", nil)
	mgr.Facts.ForgetMatching("forgotten")
	if graph != 1 {
		t.Fatalf("graph hook fired %d times after AttachVectorIndex(nil), want 1", graph)
	}
}
