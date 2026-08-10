/*
 * ChatCLI - Tests for the persisted knowledge-graph cache (graph_cache.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diillson/chatcli/pkg/knowledge"
	"go.uber.org/zap"
)

// drainPersist blocks until any in-flight async persist finished, so
// TempDir cleanup never races the background write.
func drainPersist(t *testing.T, gc *GraphCache) {
	t.Helper()
	t.Cleanup(func() {
		deadline := time.Now().Add(3 * time.Second)
		for gc.persistInFlight.Load() {
			if time.Now().After(deadline) {
				t.Error("async persist never finished")
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
}

func testGraph(nodes int) *knowledge.Graph {
	g := knowledge.New()
	prev := ""
	for i := 0; i < nodes; i++ {
		id := "fact:" + string(rune('a'+i))
		g.AddNode(knowledge.Node{ID: id, Kind: knowledge.KindFact, Title: id})
		if prev != "" {
			g.AddEdge(prev, id, 1)
		}
		prev = id
	}
	return g
}

func writeEnvelope(t *testing.T, dir string, version int, fp string, g *knowledge.Graph) string {
	t.Helper()
	payload, err := g.MarshalGraph()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(graphFile{
		SchemaVersion: version, Fingerprint: fp, SavedAt: time.Now(), Graph: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGraphCache_InertWithoutSource(t *testing.T) {
	gc := NewGraphCache(t.TempDir(), zap.NewNop())
	if got := gc.Snapshot(); got != nil {
		t.Fatalf("inert cache returned a graph: %v", got)
	}
	if _, _, ok := gc.Stats(); ok {
		t.Fatal("inert cache reported stats")
	}
	var nilCache *GraphCache
	nilCache.MarkDirty() // must not panic
	if nilCache.Snapshot() != nil {
		t.Fatal("nil cache returned a graph")
	}
}

func TestGraphCache_AdoptsValidPersistedFileWithoutBuilding(t *testing.T) {
	dir := t.TempDir()
	writeEnvelope(t, dir, graphSchemaVersion, "fp-1", testGraph(3))

	var builds atomic.Int32
	gc := NewGraphCache(dir, zap.NewNop())
	drainPersist(t, gc)
	gc.SetSource(func() *knowledge.Graph {
		builds.Add(1)
		return testGraph(3)
	}, func() string { return "fp-1" })

	g := gc.Snapshot()
	if g == nil || g.Len() != 3 {
		t.Fatalf("expected adopted 3-node graph, got %v", g)
	}
	if builds.Load() != 0 {
		t.Fatalf("builder ran %d times, want 0 (file was valid)", builds.Load())
	}
}

func TestGraphCache_StaleFingerprintDiscardsAndRebuilds(t *testing.T) {
	dir := t.TempDir()
	path := writeEnvelope(t, dir, graphSchemaVersion, "old-fp", testGraph(2))

	var builds atomic.Int32
	gc := NewGraphCache(dir, zap.NewNop())
	drainPersist(t, gc)
	gc.SetSource(func() *knowledge.Graph {
		builds.Add(1)
		return testGraph(5)
	}, func() string { return "new-fp" })

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("stale file was not discarded")
	}
	g := gc.Snapshot()
	if g == nil || g.Len() != 5 {
		t.Fatalf("expected rebuilt 5-node graph, got %v", g)
	}
	if builds.Load() != 1 {
		t.Fatalf("builder ran %d times, want 1", builds.Load())
	}
}

func TestGraphCache_SchemaMismatchDiscards(t *testing.T) {
	dir := t.TempDir()
	path := writeEnvelope(t, dir, graphSchemaVersion+1, "fp", testGraph(2))
	gc := NewGraphCache(dir, zap.NewNop())
	drainPersist(t, gc)
	gc.SetSource(func() *knowledge.Graph { return testGraph(1) }, func() string { return "fp" })
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("version-mismatched file was not discarded")
	}
}

func TestGraphCache_CorruptFileDiscardedNoQuarantine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "graph.json")
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	gc := NewGraphCache(dir, zap.NewNop())
	drainPersist(t, gc)
	gc.SetSource(func() *knowledge.Graph { return testGraph(1) }, func() string { return "fp" })

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("corrupt file was not discarded")
	}
	// vindex policy: derived content is removed, never quarantined.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".corrupt" {
			t.Fatalf("quarantine file created for derived cache: %s", e.Name())
		}
	}
}

func TestGraphCache_MarkDirtyTriggersExactlyOneRebuild(t *testing.T) {
	var builds atomic.Int32
	gc := NewGraphCache(t.TempDir(), zap.NewNop())
	drainPersist(t, gc)
	gc.SetSource(func() *knowledge.Graph {
		builds.Add(1)
		return testGraph(2)
	}, nil)

	gc.Snapshot() // first build
	gc.Snapshot() // cached
	gc.Snapshot() // cached
	if builds.Load() != 1 {
		t.Fatalf("clean snapshots rebuilt: %d builds", builds.Load())
	}
	gc.MarkDirty()
	gc.Snapshot()
	gc.Snapshot()
	if builds.Load() != 2 {
		t.Fatalf("dirty snapshot rebuilt %d times total, want 2", builds.Load())
	}
}

func TestGraphCache_ConcurrentSnapshotSingleFlight(t *testing.T) {
	var builds atomic.Int32
	release := make(chan struct{})
	gc := NewGraphCache(t.TempDir(), zap.NewNop())
	drainPersist(t, gc)
	gc.SetSource(func() *knowledge.Graph {
		builds.Add(1)
		<-release
		return testGraph(2)
	}, nil)

	const readers = 8
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if g := gc.Snapshot(); g == nil {
				t.Error("nil snapshot from concurrent reader")
			}
		}()
	}
	time.Sleep(50 * time.Millisecond) // let readers pile up on the build
	close(release)
	wg.Wait()
	if builds.Load() != 1 {
		t.Fatalf("concurrent snapshots ran %d builds, want 1", builds.Load())
	}
}

func TestGraphCache_PersistAsyncWritesEnvelope(t *testing.T) {
	dir := t.TempDir()
	gc := NewGraphCache(dir, zap.NewNop())
	drainPersist(t, gc)
	gc.SetSource(func() *knowledge.Graph { return testGraph(4) }, func() string { return "fp-x" })
	gc.Snapshot()

	path := filepath.Join(dir, "graph.json")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(path); err == nil {
			var env graphFile
			if err := json.Unmarshal(data, &env); err != nil {
				t.Fatalf("persisted envelope corrupt: %v", err)
			}
			if env.SchemaVersion != graphSchemaVersion || env.Fingerprint != "fp-x" {
				t.Fatalf("envelope fields wrong: %+v", env)
			}
			g, err := knowledge.UnmarshalGraph(env.Graph)
			if err != nil || g.Len() != 4 {
				t.Fatalf("persisted graph bad: %v / %v", g, err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("graph.json never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestGraphCache_StatsNeverBuilds(t *testing.T) {
	var builds atomic.Int32
	gc := NewGraphCache(t.TempDir(), zap.NewNop())
	drainPersist(t, gc)
	gc.SetSource(func() *knowledge.Graph {
		builds.Add(1)
		return testGraph(2)
	}, nil)

	if _, _, ok := gc.Stats(); ok {
		t.Fatal("Stats reported a snapshot before any build")
	}
	if builds.Load() != 0 {
		t.Fatalf("Stats triggered %d builds, want 0", builds.Load())
	}
	gc.Snapshot()
	nodes, edges, ok := gc.Stats()
	if !ok || nodes != 2 || edges != 1 {
		t.Fatalf("Stats = (%d, %d, %v), want (2, 1, true)", nodes, edges, ok)
	}
}

func TestDirtyTaps_ContentMutationsMark(t *testing.T) {
	mgr := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	// Wire a counting builder so we can observe dirtiness through Snapshot.
	var builds atomic.Int32
	mgr.SetGraphSource(func() *knowledge.Graph {
		builds.Add(1)
		return knowledge.New()
	}, nil)
	settle := func() { mgr.KnowledgeGraph() } // consume dirtiness

	steps := []struct {
		name   string
		mutate func()
		marks  bool
	}{
		{"fact add", func() { mgr.Facts.AddFact("brand new fact content", "general", nil) }, true},
		{"fact exact-dup reinforce", func() { mgr.Facts.AddFact("brand new fact content", "general", nil) }, true},
		{"fact forget", func() { mgr.Facts.ForgetMatching("brand new fact") }, true},
		{"topic record", func() { mgr.Topics.Record([]string{"deployment"}) }, true},
		{"project upsert", func() { mgr.Projects.Upsert(map[string]string{"project_name": "chatcli"}) }, true},
		{"profile update", func() { mgr.Profile.Update(map[string]string{"role": "engineer"}) }, true},
		{"episode add", func() {
			mgr.Episodes.Add(Episode{Summary: "did the thing", Project: "/tmp/p", Date: time.Now()})
		}, true},
		{"project touch (non-tap)", func() { mgr.Projects.Touch("chatcli") }, false},
		{"fact mark-accessed (non-tap)", func() {
			if all := mgr.Facts.GetAll(); len(all) > 0 {
				mgr.Facts.MarkAccessed([]string{all[0].ID})
			}
		}, false},
	}
	for _, tt := range steps {
		settle()
		before := builds.Load()
		tt.mutate()
		mgr.KnowledgeGraph()
		rebuilt := builds.Load() > before
		if rebuilt != tt.marks {
			t.Fatalf("%s: rebuilt=%v, want %v", tt.name, rebuilt, tt.marks)
		}
	}
}
