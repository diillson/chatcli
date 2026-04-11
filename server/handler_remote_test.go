package server

import (
	"reflect"
	"testing"

	"github.com/diillson/chatcli/pkg/persona"
)

// TestAgentToProto verifies that every persona.Agent field the wire cares
// about is carried over to the pb.AgentInfo, including the advanced
// frontmatter fields added for per-agent LLM routing.
//
// Guards against silent drift if someone adds a field to persona.Agent and
// forgets to update the mapper — the gap we just closed would reopen.
func TestAgentToProto(t *testing.T) {
	a := &persona.Agent{
		Name:        "my-reviewer",
		Description: "strict go reviewer",
		Skills:      persona.StringList{"go-testing", "golangci"},
		Plugins:     persona.StringList{"chatcli-gobenchrun"},
		Tools:       persona.StringList{"Read", "Grep", "Glob"},
		Model:       "claude-opus-4-6",
		Effort:      "high",
		Category:    "review",
		Version:     "2.1.0",
		Author:      "Edilson Freitas",
		Tags:        persona.StringList{"go", "review", "quality"},
		Content:     "# You are a strict reviewer...",
	}

	p := agentToProto(a)

	cases := []struct {
		name string
		want interface{}
		got  interface{}
	}{
		{"Name", a.Name, p.Name},
		{"Description", a.Description, p.Description},
		{"Model", a.Model, p.Model},
		{"Content", a.Content, p.Content},
		{"Effort", a.Effort, p.Effort},
		{"Category", a.Category, p.Category},
		{"Version", a.Version, p.Version},
		{"Author", a.Author, p.Author},
	}
	for _, tc := range cases {
		if !reflect.DeepEqual(tc.want, tc.got) {
			t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	if !reflect.DeepEqual([]string(a.Skills), p.Skills) {
		t.Errorf("Skills: got %v, want %v", p.Skills, a.Skills)
	}
	if !reflect.DeepEqual([]string(a.Plugins), p.Plugins) {
		t.Errorf("Plugins: got %v, want %v", p.Plugins, a.Plugins)
	}
	if !reflect.DeepEqual([]string(a.Tags), p.Tags) {
		t.Errorf("Tags: got %v, want %v", p.Tags, a.Tags)
	}
}

// TestAgentToProto_ZeroValues verifies that an empty persona.Agent serializes
// to a pb.AgentInfo where the advanced fields are all zero (not nil-panicking,
// not returning "unset markers" — just empty strings and empty slices). This
// is the contract old clients will see when a server populates nothing.
func TestAgentToProto_ZeroValues(t *testing.T) {
	p := agentToProto(&persona.Agent{Name: "minimal"})
	if p.Name != "minimal" {
		t.Fatalf("Name = %q", p.Name)
	}
	if p.Effort != "" || p.Category != "" || p.Version != "" || p.Author != "" {
		t.Errorf("empty agent should produce empty advanced fields, got effort=%q category=%q version=%q author=%q",
			p.Effort, p.Category, p.Version, p.Author)
	}
	if len(p.Tags) != 0 {
		t.Errorf("empty agent should produce empty tags, got %v", p.Tags)
	}
}
