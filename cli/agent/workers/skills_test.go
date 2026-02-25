package workers

import (
	"context"
	"testing"

	"github.com/diillson/chatcli/pkg/coder/engine"
)

func TestSkillSet_RegisterAndGet(t *testing.T) {
	ss := NewSkillSet()

	sk := &Skill{
		Name:        "test-skill",
		Description: "A test skill",
		Type:        SkillDescriptive,
	}
	ss.Register(sk)

	got, ok := ss.Get("test-skill")
	if !ok {
		t.Fatal("expected to find registered skill")
	}
	if got.Name != "test-skill" {
		t.Errorf("expected name 'test-skill', got '%s'", got.Name)
	}
}

func TestSkillSet_GetNotFound(t *testing.T) {
	ss := NewSkillSet()

	_, ok := ss.Get("nonexistent")
	if ok {
		t.Fatal("expected skill not found")
	}
}

func TestSkillSet_List(t *testing.T) {
	ss := NewSkillSet()

	ss.Register(&Skill{Name: "a", Description: "skill a", Type: SkillDescriptive})
	ss.Register(&Skill{Name: "b", Description: "skill b", Type: SkillExecutable})

	list := ss.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 skills, got %d", len(list))
	}
}

func TestSkillSet_ExecuteScript(t *testing.T) {
	ss := NewSkillSet()

	ss.Register(&Skill{
		Name:        "echo",
		Description: "Returns input",
		Type:        SkillExecutable,
		Script: func(ctx context.Context, input map[string]string, eng *engine.Engine) (string, error) {
			return "hello " + input["name"], nil
		},
	})

	eng := engine.NewEngine(nil, nil)
	result, err := ss.Execute(context.Background(), "echo", map[string]string{"name": "world"}, eng)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", result)
	}
}

func TestSkillSet_ExecuteDescriptiveFails(t *testing.T) {
	ss := NewSkillSet()

	ss.Register(&Skill{
		Name:        "desc-only",
		Description: "Not executable",
		Type:        SkillDescriptive,
	})

	eng := engine.NewEngine(nil, nil)
	_, err := ss.Execute(context.Background(), "desc-only", nil, eng)
	if err == nil {
		t.Fatal("expected error when executing descriptive skill")
	}
}

func TestSkillSet_ExecuteNotFound(t *testing.T) {
	ss := NewSkillSet()

	eng := engine.NewEngine(nil, nil)
	_, err := ss.Execute(context.Background(), "missing", nil, eng)
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
}

func TestSkillSet_CatalogString(t *testing.T) {
	ss := NewSkillSet()

	ss.Register(&Skill{Name: "read-all", Description: "Read all files", Type: SkillExecutable})
	ss.Register(&Skill{Name: "analyze", Description: "Analyze code", Type: SkillDescriptive})

	catalog := ss.CatalogString()
	if catalog == "" {
		t.Fatal("expected non-empty catalog")
	}
	if !contains(catalog, "read-all") || !contains(catalog, "script") {
		t.Errorf("catalog should contain 'read-all' and 'script': %s", catalog)
	}
	if !contains(catalog, "analyze") || !contains(catalog, "descriptive") {
		t.Errorf("catalog should contain 'analyze' and 'descriptive': %s", catalog)
	}
}

func TestSkillSet_EmptyCatalog(t *testing.T) {
	ss := NewSkillSet()
	if ss.CatalogString() != "" {
		t.Error("expected empty catalog for empty skill set")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
