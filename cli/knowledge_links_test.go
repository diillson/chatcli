/*
 * ChatCLI - Tests for explicit note links and reference resolution
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 */
package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/diillson/chatcli/cli/plugins"
	"github.com/diillson/chatcli/cli/workspace/memory"
	"github.com/diillson/chatcli/pkg/knowledge"
)

func TestResolveNoteRef(t *testing.T) {
	g := knowledge.New()
	g.AddNode(knowledge.Node{ID: "project:orbital", Kind: knowledge.KindProject, Title: "Orbital"})
	g.AddNode(knowledge.Node{ID: "tag:orbital", Kind: knowledge.KindTag, Title: "orbital"})
	g.AddNode(knowledge.Node{ID: "fact:abcdef0123", Kind: knowledge.KindFact, Title: "uses mTLS"})
	g.AddNode(knowledge.Node{ID: "fact:abcdef9999", Kind: knowledge.KindFact, Title: "uses gRPC"})
	g.AddNode(knowledge.Node{ID: "topic:a", Kind: knowledge.KindTopic, Title: "Same"})
	g.AddNode(knowledge.Node{ID: "topic:b", Kind: knowledge.KindTopic, Title: "same"})
	g.Freeze()

	cases := []struct {
		ref, want string
		err       error
	}{
		{"project:orbital", "project:orbital", nil},
		{"Orbital", "project:orbital", nil}, // project beats tag
		{"[[orbital|the service]]", "project:orbital", nil},
		{"abcdef0123", "fact:abcdef0123", nil},
		{"abcdef", "", errRefAmbiguous}, // two facts share the prefix
		{"Same", "", errRefAmbiguous},   // two topics share the name
		{"mTLS", "fact:abcdef0123", nil},
		{"nothing at all", "", errRefNotFound},
		{"", "", errRefNotFound},
	}
	for _, c := range cases {
		n, cands, err := resolveNoteRef(g, c.ref)
		if c.err != nil {
			if !errors.Is(err, c.err) {
				t.Errorf("%q: err = %v, want %v", c.ref, err, c.err)
			}
			if errors.Is(c.err, errRefAmbiguous) && len(cands) < 2 {
				t.Errorf("%q: ambiguous without candidates", c.ref)
			}
			continue
		}
		if err != nil || n.ID != c.want {
			t.Errorf("%q: got %v %v, want %s", c.ref, n, err, c.want)
		}
	}
}

func TestGraphLinkAndUnlink_EndToEnd(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()
	m.Projects.Upsert(map[string]string{"name": "Orbital", "project_path": "/repo/orbital"})
	m.Facts.AddFact("collectors retry with jitter", "pattern", nil)
	var fid string
	for _, f := range m.Facts.GetAll() {
		fid = f.ID
	}

	out, err := cli.graphLinkText(fid[:8], "[[Orbital]]", "Explains", "why the retries exist")
	if err != nil || !strings.Contains(out, "explains") {
		t.Fatalf("link: %q %v", out, err)
	}
	if again, _ := cli.graphLinkText(fid[:8], "Orbital", "explains", "why the retries exist"); !strings.Contains(again, "Already") && !strings.Contains(again, "Já") {
		t.Fatalf("relink must report existing: %q", again)
	}
	g := cli.buildKnowledgeGraph()
	if !hasLink(g.Outlinks("fact:"+fid), "project:orbital", "explains") {
		t.Fatalf("user link missing from graph: %+v", g.Outlinks("fact:"+fid))
	}
	for _, l := range g.Outlinks("fact:" + fid) {
		if l.To == "project:orbital" && l.Origin != knowledge.OriginUser {
			t.Fatalf("origin = %s, want user", l.Origin)
		}
	}
	// neighbors shows the relation and its direction.
	hood, _ := cli.graphNeighborsText("Orbital")
	if !strings.Contains(hood, "← explains") {
		t.Fatalf("neighbors lacks the backlink annotation:\n%s", hood)
	}

	if _, err := cli.graphLinkText("Orbital", "Orbital", "", ""); err == nil {
		t.Fatal("self link accepted")
	}
	if _, err := cli.graphLinkText("nope nope", "Orbital", "", ""); err == nil {
		t.Fatal("unknown endpoint accepted")
	}

	if out, _ := cli.graphUnlinkText("Orbital", fid, ""); !strings.Contains(out, "1") {
		t.Fatalf("unlink: %q", out)
	}
	if m.Links.Count() != 0 {
		t.Fatal("link not removed")
	}
	if out, _ := cli.graphUnlinkText("Orbital", fid, ""); out == "" {
		t.Fatal("unlink of nothing must explain")
	}
}

func TestGraphUnlink_DerivedLinkExplained(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()
	m.Projects.Upsert(map[string]string{"name": "Orbital", "project_path": "/repo/orbital"})
	m.Facts.AddFactWithSource("ingest runs on spot nodes", "architecture", nil, "/repo/orbital")
	var fid string
	for _, f := range m.Facts.GetAll() {
		fid = f.ID
	}
	out, err := cli.graphUnlinkText(fid, "Orbital", "")
	if err != nil || !strings.Contains(out, "in_project/structural") {
		t.Fatalf("derived unlink must name its origin: %q %v", out, err)
	}
}

func TestGraphLink_SurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	m := memory.NewManager(dir, memory.DefaultConfig(), nil)
	if ok, _ := m.Links.Add("fact:a", "project:b", "about", "user", ""); !ok {
		t.Fatal("add")
	}
	m2 := memory.NewManager(dir, memory.DefaultConfig(), nil)
	if m2.Links.Count() != 1 {
		t.Fatal("explicit link lost across restart")
	}
}

func TestMemoryPlugin_LinkDispatch(t *testing.T) {
	cli := newTestCLIWithMemory(t)
	m := cli.memoryStore.Manager()
	m.Projects.Upsert(map[string]string{"name": "Orbital", "project_path": "/repo/orbital"})
	m.Topics.Record([]string{"telemetry"})
	plugins.SetMemoryAdapter(&memoryPluginAdapter{cli: cli})
	t.Cleanup(func() { plugins.SetMemoryAdapter(nil) })
	p := plugins.NewBuiltinMemoryPlugin()

	out, err := p.Execute(context.Background(), []string{`{"cmd":"relate","args":{"source":"telemetry","target":"Orbital","relation":"part of"}}`})
	if err != nil || !strings.Contains(out, "part_of") {
		t.Fatalf("link via aliases: %q %v", out, err)
	}
	if _, err := p.Execute(context.Background(), []string{`{"cmd":"link","args":{"from":"telemetry"}}`}); err == nil {
		t.Fatal("missing to accepted")
	}
	if p.IsReadOnly([]string{`{"cmd":"link","args":{"from":"a","to":"b"}}`}) || p.IsReadOnly([]string{`{"cmd":"unlink","args":{"from":"a","to":"b"}}`}) {
		t.Fatal("link/unlink write memory: they must not be read-only")
	}
	if d := p.DescribeCall([]string{`{"cmd":"link","args":{"from":"telemetry","to":"Orbital"}}`}); !strings.Contains(d, "telemetry") {
		t.Fatalf("describe = %q", d)
	}
	out, err = p.Execute(context.Background(), []string{`{"cmd":"unlink","args":{"from":"Orbital","to":"telemetry"}}`})
	if err != nil || !strings.Contains(out, "1") {
		t.Fatalf("unlink: %q %v", out, err)
	}
}

func TestParseLinkArgs(t *testing.T) {
	cases := []struct {
		in       string
		from, to string
		rel      string
		note     string
		ok       bool
	}{
		{`telemetry security -> Orbital --rel about --note keeps it close`, "telemetry security", "Orbital", "about", "keeps it close", true},
		{`"a b" "c d"`, "a b", "c d", "", "", true},
		{`[[Setup]] → topic:auth`, "[[Setup]]", "topic:auth", "", "", true},
		{`only-one`, "", "", "", "", false},
		{`-> missing`, "", "missing", "", "", false},
	}
	for _, c := range cases {
		got, ok := parseLinkArgs(c.in)
		if ok != c.ok || (ok && (got.from != c.from || got.to != c.to || got.rel != c.rel || got.note != c.note)) {
			t.Errorf("parseLinkArgs(%q) = %+v %v", c.in, got, ok)
		}
	}
}
