/*
 * ChatCLI - Tests for the @graphview builtin.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package plugins

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/diillson/chatcli/i18n"
)

func TestParseGraphViewArgsJSONWithAliases(t *testing.T) {
	args := []string{`{"title":"T","nodes":[{"id":"a","title":"Alpha","type":"topic"},{"name":"Beta"}],"edges":[{"from":"a","to":"Beta","weight":3}]}`}
	cfg, err := parseGraphViewArgs(args)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Source != "json" {
		t.Fatalf("source = %q, want json", cfg.Source)
	}
	if len(cfg.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(cfg.Nodes))
	}
	// alias mapping: title→label, type→kind
	if cfg.Nodes[0].Label != "Alpha" || cfg.Nodes[0].Kind != "topic" {
		t.Fatalf("node0 = %+v", cfg.Nodes[0])
	}
	// name→id+label fallback
	if cfg.Nodes[1].ID != "Beta" || cfg.Nodes[1].Label != "Beta" {
		t.Fatalf("node1 = %+v", cfg.Nodes[1])
	}
	// from/to→source/target
	if cfg.Edges[0].Source != "a" || cfg.Edges[0].Target != "Beta" || cfg.Edges[0].Weight != 3 {
		t.Fatalf("edge0 = %+v", cfg.Edges[0])
	}
}

func TestParseGraphViewArgsEnvelopeAndOpenFlag(t *testing.T) {
	f := false
	args := []string{`{"cmd":"render","args":{"source":"knowledge","open":false}}`}
	cfg, err := parseGraphViewArgs(args)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Source != "knowledge" {
		t.Fatalf("source = %q", cfg.Source)
	}
	if cfg.Open == nil || *cfg.Open != f {
		t.Fatalf("open = %v, want false", cfg.Open)
	}
}

func TestParseGraphViewArgsRejectsUnknownSource(t *testing.T) {
	if _, err := parseGraphViewArgs([]string{`{"source":"bogus"}`}); err == nil {
		t.Fatal("expected error for unknown source")
	}
}

func TestSanitizeGraphData(t *testing.T) {
	in := GraphData{
		Nodes: []GraphNode{
			{ID: "a", Label: "A"},
			{ID: "a", Label: "dup"},    // duplicate id dropped
			{ID: "b"},                  // empty label → filled with id
			{ID: "  ", Label: "blank"}, // empty id dropped
		},
		Edges: []GraphEdge{
			{Source: "a", Target: "b"},
			{Source: "b", Target: "a"},     // undirected duplicate dropped
			{Source: "a", Target: "a"},     // self-loop dropped
			{Source: "a", Target: "ghost"}, // dangling dropped
		},
	}
	out := sanitizeGraphData(in)
	if len(out.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(out.Nodes))
	}
	for _, n := range out.Nodes {
		if n.ID == "b" && n.Label != "b" {
			t.Fatalf("empty label not filled: %+v", n)
		}
	}
	if len(out.Edges) != 1 {
		t.Fatalf("edges = %d, want 1 (%+v)", len(out.Edges), out.Edges)
	}
}

func TestRenderGraphViewHTMLInjectsData(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "g.html")
	data := GraphData{Title: "T", Theme: "dark", Nodes: []GraphNode{{ID: "x", Label: "OAuth", Kind: "topic"}}}
	path, err := renderGraphViewHTML(data, out)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	html := string(b)
	if strings.Contains(html, graphViewDataPlaceholder) {
		t.Fatal("data token was not replaced")
	}
	if !strings.Contains(html, `"label":"OAuth"`) {
		t.Fatalf("injected node label missing")
	}
}

func TestExecuteGraphViewJSONWritesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "graph.html")
	args := []string{`{"output":"` + out + `","open":false,"nodes":[{"id":"a","label":"A","kind":"topic"},{"id":"b","label":"B","kind":"topic"}],"edges":[{"source":"a","target":"b"}]}`}
	msg, err := (&BuiltinGraphViewPlugin{}).Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(msg, out) {
		t.Fatalf("message missing path: %q", msg)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output file not written: %v", err)
	}
}

func TestExecuteGraphViewEmpty(t *testing.T) {
	msg, err := (&BuiltinGraphViewPlugin{}).Execute(context.Background(), []string{`{"nodes":[]}`})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if msg != i18n.T("plugins.graphview.empty") {
		t.Fatalf("expected empty-graph message, got %q", msg)
	}
}

func TestResolveGraphDataKnowledgeNeedsProvider(t *testing.T) {
	SetGraphSourceProvider(nil)
	_, err := resolveGraphData(graphViewArgs{Source: "knowledge"})
	if err == nil {
		t.Fatal("expected error when no provider is wired")
	}
}

type fakeGraphProvider struct{}

func (fakeGraphProvider) KnowledgeGraph() (GraphData, error) {
	return GraphData{Nodes: []GraphNode{{ID: "k", Label: "Knowledge"}}}, nil
}
func (fakeGraphProvider) ConversationGraph() (GraphData, error) {
	return GraphData{Nodes: []GraphNode{{ID: "c", Label: "Conv"}}}, nil
}

func TestResolveGraphDataKnowledgeWithProvider(t *testing.T) {
	SetGraphSourceProvider(fakeGraphProvider{})
	t.Cleanup(func() { SetGraphSourceProvider(nil) })
	data, err := resolveGraphData(graphViewArgs{Source: "knowledge", Theme: "dark"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(data.Nodes) != 1 || data.Nodes[0].ID != "k" {
		t.Fatalf("unexpected data: %+v", data)
	}
	if data.Title == "" {
		t.Fatal("default title not applied")
	}
}

func TestResolveGraphDataConversationWithProvider(t *testing.T) {
	SetGraphSourceProvider(fakeGraphProvider{})
	t.Cleanup(func() { SetGraphSourceProvider(nil) })
	data, err := resolveGraphData(graphViewArgs{Source: "conversation", Theme: "dark"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(data.Nodes) != 1 || data.Nodes[0].ID != "c" {
		t.Fatalf("unexpected data: %+v", data)
	}
}

func TestGraphViewPluginMetadata(t *testing.T) {
	p := NewBuiltinGraphViewPlugin()
	if p.Name() != "@graphview" {
		t.Fatalf("name = %q", p.Name())
	}
	if !p.IsReadOnly(nil) || p.IsConcurrencySafe(nil) {
		t.Fatalf("capabilities: readonly=%v concurrencySafe=%v", p.IsReadOnly(nil), p.IsConcurrencySafe(nil))
	}
	for _, s := range []string{p.Description(), p.Usage(), p.Version(), p.Schema()} {
		if strings.TrimSpace(s) == "" {
			t.Fatal("empty metadata string")
		}
	}
	if p.Path() != "" {
		t.Fatalf("path = %q, want empty", p.Path())
	}
	if d := p.DescribeCall(nil); d == "" {
		t.Fatal("empty describe")
	}
	if d := p.DescribeCall([]string{`{"source":"knowledge"}`}); d == "" {
		t.Fatal("empty describe for source")
	}
	var js map[string]interface{}
	if err := json.Unmarshal([]byte(p.Schema()), &js); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
}

func TestGraphViewThemeAndOpenEnv(t *testing.T) {
	t.Setenv(graphViewThemeEnv, "light")
	if graphViewDefaultTheme() != "light" {
		t.Fatal("theme env not honored")
	}
	t.Setenv(graphViewThemeEnv, "")
	if graphViewDefaultTheme() != "dark" {
		t.Fatal("default theme should be dark")
	}
	t.Setenv(graphViewOpenEnv, "false")
	if graphViewOpenEnvEnabled() {
		t.Fatal("open env off not honored")
	}
	t.Setenv(graphViewOpenEnv, "true")
	if !graphViewOpenEnvEnabled() {
		t.Fatal("open env on not honored")
	}
}

func TestShouldOpenRespectsExplicitFalse(t *testing.T) {
	f := false
	if (graphViewArgs{Open: &f}).shouldOpen() {
		t.Fatal("explicit open=false must not open")
	}
	t.Setenv(graphViewOpenEnv, "true")
	_ = graphViewArgs{}.shouldOpen() // exercises the env+TTY path (no TTY in tests)
}

func TestParseGraphViewArgsArgvForm(t *testing.T) {
	cfg, err := parseGraphViewArgs([]string{"--source", "conversation", "--theme", "light"})
	if err != nil {
		t.Fatalf("parse argv: %v", err)
	}
	if cfg.Source != "conversation" || cfg.Theme != "light" {
		t.Fatalf("argv parse = %+v", cfg)
	}
}

func TestReadGraphDataFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "g.json")
	if err := os.WriteFile(path, []byte(`{"nodes":[{"id":"a","label":"A"},{"id":"b","name":"B"}],"edges":[{"from":"a","to":"b"}]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := readGraphDataFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(data.Nodes) != 2 || len(data.Edges) != 1 {
		t.Fatalf("parsed = %+v", data)
	}
}

func TestExecuteGraphViewFromFileSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.json")
	out := filepath.Join(dir, "out.html")
	if err := os.WriteFile(src, []byte(`{"nodes":[{"id":"a","label":"A"},{"id":"b","label":"B"}],"edges":[{"source":"a","target":"b"}]}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	args := []string{`{"file":"` + src + `","output":"` + out + `","open":false}`}
	if _, err := (&BuiltinGraphViewPlugin{}).Execute(context.Background(), args); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output not written: %v", err)
	}
}

func bigGraphArgs(t *testing.T, n int, out string) []string {
	t.Helper()
	var nodes, edges []map[string]any
	for i := 0; i < n; i++ {
		id := "n" + strconv.Itoa(i)
		nodes = append(nodes, map[string]any{"id": id, "label": id, "kind": "fact"})
		if i > 0 {
			edges = append(edges, map[string]any{"source": "n0", "target": id})
		}
	}
	raw, err := json.Marshal(map[string]any{"output": out, "open": false, "nodes": nodes, "edges": edges})
	if err != nil {
		t.Fatal(err)
	}
	return []string{string(raw)}
}

// A memory graph of several thousand notes renders; past the cap the error
// says so instead of writing a page the browser cannot hold.
func TestExecuteGraphViewLargeGraphs(t *testing.T) {
	out := filepath.Join(t.TempDir(), "big.html")
	if _, err := (&BuiltinGraphViewPlugin{}).Execute(context.Background(), bigGraphArgs(t, 6200, out)); err != nil {
		t.Fatalf("6200 nodes must render: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatal(err)
	}
	_, err := (&BuiltinGraphViewPlugin{}).Execute(context.Background(), bigGraphArgs(t, graphViewMaxNodes+1, filepath.Join(t.TempDir(), "x.html")))
	if err == nil || !strings.Contains(err.Error(), strconv.Itoa(graphViewMaxNodes+1)) {
		t.Fatalf("over the cap must fail naming the size: %v", err)
	}
}

// The layout must settle, not diverge: the page's own physics (extracted
// from the template) runs under Node on a 900-leaf star (exact mode — a hub
// used to fly to 1e68) and on a 3000-node graph with hubs (Barnes-Hut).
// Skipped where Node is not installed.
func TestGraphViewPhysicsSettles(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	start := strings.Index(graphViewTemplate, "// ---- physics")
	end := strings.Index(graphViewTemplate, "// ---- visibility")
	if start < 0 || end < start {
		t.Fatal("physics section markers not found in the template")
	}
	physics := graphViewTemplate[start:end]
	harness := `
const kindOn = {fact: true, project: true};
function visible(n) { return kindOn[n.kind]; }
let dragNode = null;
` + physics + `
function run(n, hubs) {
  nodes.length = 0; edges.length = 0; alpha = 1;
  for (let i = 0; i < n; i++) {
    const ang = i * 2.399963, r = 12 * Math.sqrt(i + 1);
    nodes.push({x: Math.cos(ang) * r, y: Math.sin(ang) * r, vx: 0, vy: 0, deg: 0, kind: i < hubs ? "project" : "fact", pinned: false, r: 6});
  }
  for (let i = hubs; i < n; i++) {
    const h = nodes[i % hubs]; const a = nodes[i];
    edges.push({a: h, b: a, w: 1}); h.deg++; a.deg++;
    if (i + 1 < n && i % 3 === 0) { edges.push({a, b: nodes[i + 1], w: 1}); a.deg++; nodes[i + 1].deg++; }
  }
  for (let t = 0; t < 600 && alpha >= 0.002; t++) step();
  let max = 0;
  for (const p of nodes) { if (!isFinite(p.x) || !isFinite(p.y)) return Infinity; max = Math.max(max, Math.abs(p.x), Math.abs(p.y)); }
  return max;
}
const nodes = [], edges = [];
console.log(JSON.stringify({star: run(901, 1), hubs: run(3000, 5)}));
`
	// the physics section declares no nodes/edges; they are globals here
	harness = strings.Replace(harness, "const nodes = [], edges = [];", "", 1)
	harness = "var nodes = [], edges = [];\n" + harness
	f := filepath.Join(t.TempDir(), "physics.js")
	if err := os.WriteFile(f, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	outB, err := exec.Command(node, f).CombinedOutput() // #nosec G204 -- node from PATH running a test harness
	if err != nil {
		t.Fatalf("node: %v\n%s", err, outB)
	}
	var got struct{ Star, Hubs float64 }
	if err := json.Unmarshal(outB, &got); err != nil {
		t.Fatalf("output %q: %v", outB, err)
	}
	for name, v := range map[string]float64{"star": got.Star, "hubs": got.Hubs} {
		if math.IsInf(v, 0) || math.IsNaN(v) || v > 1e7 {
			t.Fatalf("%s layout diverged: max |coord| = %g", name, v)
		}
	}
}
