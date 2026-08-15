/*
 * ChatCLI - Tests for the Related (graph) recall expansion (retriever.go)
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package memory

import (
	"strings"
	"testing"

	"github.com/diillson/chatcli/pkg/knowledge"
	"go.uber.org/zap"
)

// staticGraphSource is a fixed GraphSource for tests.
type staticGraphSource struct{ g *knowledge.Graph }

func (s staticGraphSource) Snapshot() *knowledge.Graph { return s.g }

// graphFixture builds a manager with two linked facts plus an episode and a
// topic hanging off the first fact, so expansion has something to surface.
func graphFixture(t *testing.T) (*Manager, string, string) {
	t.Helper()
	mgr := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	t.Cleanup(mgr.WaitGraphPersist)
	mgr.Facts.AddFact("the gateway daemon auto-approves tool calls", "architecture", nil)
	mgr.Facts.AddFact("bedrock payload cap is learned from rejections", "gotcha", nil)

	all := mgr.Facts.GetAll()
	if len(all) != 2 {
		t.Fatalf("fixture facts = %d, want 2", len(all))
	}
	var gatewayID, bedrockID string
	for _, f := range all {
		if strings.Contains(f.Content, "gateway") {
			gatewayID = f.ID
		} else {
			bedrockID = f.ID
		}
	}

	g := knowledge.New()
	g.AddNode(knowledge.Node{ID: "fact:" + gatewayID, Kind: knowledge.KindFact, Title: "gateway"})
	g.AddNode(knowledge.Node{ID: "fact:" + bedrockID, Kind: knowledge.KindFact, Title: "bedrock"})
	g.AddNode(knowledge.Node{ID: "episode:e1", Kind: knowledge.KindEpisode, Title: "2026-08-01 fixed gateway"})
	g.AddNode(knowledge.Node{ID: "topic:daemons", Kind: knowledge.KindTopic, Title: "daemons", Summary: "long-running processes"})
	g.AddNode(knowledge.Node{ID: "tag:go", Kind: knowledge.KindTag, Title: "go"})
	g.AddNode(knowledge.Node{ID: "profile:user", Kind: knowledge.KindProfile, Title: "user"})
	g.AddEdge("fact:"+gatewayID, "fact:"+bedrockID, 2)
	g.AddEdge("fact:"+gatewayID, "episode:e1", 1)
	g.AddEdge("fact:"+gatewayID, "topic:daemons", 2)
	g.AddEdge("fact:"+gatewayID, "tag:go", 1)
	g.AddEdge("fact:"+gatewayID, "profile:user", 1)

	mgr.retriever.SetGraph(staticGraphSource{g})
	return mgr, gatewayID, bedrockID
}

func TestRelatedGraphSection_NilGraphAbsent(t *testing.T) {
	mgr := NewManager(t.TempDir(), DefaultConfig(), zap.NewNop())
	t.Cleanup(mgr.WaitGraphPersist)
	mgr.Facts.AddFact("some fact", "general", nil)
	mgr.retriever.SetGraph(nil)
	out := mgr.GetRelevantContext([]string{"some"})
	if strings.Contains(out, "## Related (graph)") {
		t.Fatal("section rendered with nil graph")
	}
}

func TestRelatedGraphSection_ExpandsUnrankedNeighbors(t *testing.T) {
	mgr, gatewayID, _ := graphFixture(t)
	ranked, _ := mgr.Facts.GetByID(gatewayID)
	section, ok := mgr.retriever.relatedGraphSection([]*Fact{ranked}, 4000)
	if !ok {
		t.Fatal("section not rendered")
	}
	if !strings.Contains(section, "bedrock payload cap") {
		t.Fatalf("neighbor fact missing:\n%s", section)
	}
	if !strings.Contains(section, "[episode] 2026-08-01 fixed gateway") ||
		!strings.Contains(section, "@memory timeline") {
		t.Fatalf("episode pointer missing:\n%s", section)
	}
	if !strings.Contains(section, "[topic] daemons") ||
		!strings.Contains(section, "@memory neighbors daemons") {
		t.Fatalf("topic pointer missing:\n%s", section)
	}
}

func TestRelatedGraphSection_FiltersTagProfileAndRanked(t *testing.T) {
	mgr, gatewayID, bedrockID := graphFixture(t)
	f1, _ := mgr.Facts.GetByID(gatewayID)
	f2, _ := mgr.Facts.GetByID(bedrockID)
	// Both facts ranked → the fact neighbor must NOT reappear.
	section, ok := mgr.retriever.relatedGraphSection([]*Fact{f1, f2}, 4000)
	if !ok {
		t.Fatal("section not rendered (episode/topic still expandable)")
	}
	if strings.Contains(section, "bedrock payload cap") {
		t.Fatalf("already-ranked fact resurfaced:\n%s", section)
	}
	if strings.Contains(section, "[tag]") || strings.Contains(section, "tag:go") {
		t.Fatalf("tag glue leaked:\n%s", section)
	}
	if strings.Contains(section, "[profile]") {
		t.Fatalf("profile hub leaked:\n%s", section)
	}
}

func TestRelatedGraphSection_BudgetGuard(t *testing.T) {
	mgr, gatewayID, _ := graphFixture(t)
	f, _ := mgr.Facts.GetByID(gatewayID)
	if _, ok := mgr.retriever.relatedGraphSection([]*Fact{f}, 250); ok {
		t.Fatal("section rendered under the 250-char floor")
	}
}

func TestRelatedGraphSection_NeverMentionsSessionTool(t *testing.T) {
	mgr, gatewayID, _ := graphFixture(t)
	// Add a session neighbor: pointers must stay on @memory surfaces.
	g := mgr.retriever.graph.Snapshot()
	g.AddNode(knowledge.Node{ID: "session:aug-work", Kind: knowledge.KindSession, Title: "aug-work"})
	g.AddEdge("fact:"+gatewayID, "session:aug-work", 1.5)

	f, _ := mgr.Facts.GetByID(gatewayID)
	section, ok := mgr.retriever.relatedGraphSection([]*Fact{f}, 4000)
	if !ok {
		t.Fatal("section not rendered")
	}
	if !strings.Contains(section, "[session] aug-work") {
		t.Fatalf("session pointer missing:\n%s", section)
	}
	if strings.Contains(section, "@session") {
		t.Fatalf("chat mode has no @session tool — wording must stay universal:\n%s", section)
	}
}

func TestRelatedGraphSection_RendersInBothRetrievalPaths(t *testing.T) {
	mgr, _, _ := graphFixture(t)
	plain := mgr.GetRelevantContext([]string{"gateway", "daemon"})
	if !strings.Contains(plain, "## Related (graph)") {
		t.Fatalf("plain Retrieve path missing section:\n%s", plain)
	}
	hyde := mgr.retriever.RetrieveWithHyDE(t.Context(), "how does the gateway work", []string{"gateway"}, nil, nil)
	if !strings.Contains(hyde, "## Related (graph)") {
		t.Fatalf("RetrieveWithHyDE path missing section:\n%s", hyde)
	}
}

func TestTruncateRunes_MultibyteSafe(t *testing.T) {
	s := strings.Repeat("çã", 100)
	got := truncateRunes(s, 10)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("missing ellipsis: %q", got)
	}
	if strings.Count(got, "�") > 0 {
		t.Fatalf("mid-rune split: %q", got)
	}
	if truncateRunes("short", 10) != "short" {
		t.Fatal("short string modified")
	}
}
