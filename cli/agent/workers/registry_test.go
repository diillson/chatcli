package workers

import (
	"strings"
	"testing"
)

func TestRegistry_All(t *testing.T) {
	r := NewRegistry()
	r.Register(NewFileAgent())
	r.Register(NewCoderAgent())
	r.Register(NewSearchAgent())

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(all))
	}
	// Should be sorted by type name
	for i := 1; i < len(all); i++ {
		if string(all[i-1].Type()) > string(all[i].Type()) {
			t.Errorf("agents not sorted: %s > %s", all[i-1].Type(), all[i].Type())
		}
	}
}

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	r.Register(NewFileAgent())
	r.Register(NewCoderAgent())

	r.Unregister(AgentTypeFile)

	_, ok := r.Get(AgentTypeFile)
	if ok {
		t.Error("expected file agent to be unregistered")
	}

	_, ok = r.Get(AgentTypeCoder)
	if !ok {
		t.Error("expected coder agent to still be registered")
	}

	all := r.All()
	if len(all) != 1 {
		t.Errorf("expected 1 agent after unregister, got %d", len(all))
	}
}

func TestRegistry_CatalogString(t *testing.T) {
	r := NewRegistry()
	r.Register(NewFileAgent())
	r.Register(NewShellAgent())

	catalog := r.CatalogString()
	if catalog == "" {
		t.Fatal("expected non-empty catalog")
	}
	if !strings.Contains(catalog, "Available Specialized Agents") {
		t.Error("expected 'Available Specialized Agents' in catalog")
	}
	if !strings.Contains(catalog, "file") {
		t.Error("expected 'file' agent in catalog")
	}
	if !strings.Contains(catalog, "shell") {
		t.Error("expected 'shell' agent in catalog")
	}
	if !strings.Contains(catalog, "READ-ONLY") {
		t.Error("expected READ-ONLY for file agent in catalog")
	}
	if !strings.Contains(catalog, "Allowed commands:") {
		t.Error("expected 'Allowed commands:' in catalog")
	}
}

func TestRegistry_CatalogString_Empty(t *testing.T) {
	r := NewRegistry()
	catalog := r.CatalogString()
	if catalog != "" {
		t.Errorf("expected empty catalog for empty registry, got: %s", catalog)
	}
}

func TestSetupDefaultRegistry(t *testing.T) {
	r := SetupDefaultRegistry()
	all := r.All()
	if len(all) != 12 {
		t.Fatalf("expected 6 default agents, got %d", len(all))
	}

	expectedTypes := []AgentType{AgentTypeFile, AgentTypeCoder, AgentTypeShell, AgentTypeGit, AgentTypeSearch, AgentTypePlanner}
	for _, at := range expectedTypes {
		_, ok := r.Get(at)
		if !ok {
			t.Errorf("expected agent type %s in default registry", at)
		}
	}
}

func TestRegistry_RegisterReplace(t *testing.T) {
	r := NewRegistry()
	r.Register(NewFileAgent())

	// Replace with a mock agent of the same type
	mock := &mockAgent{agentType: AgentTypeFile}
	r.Register(mock)

	got, ok := r.Get(AgentTypeFile)
	if !ok {
		t.Fatal("expected file agent")
	}
	if got.Name() != mock.Name() {
		t.Errorf("expected replaced agent, got %s", got.Name())
	}
}
