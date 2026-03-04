package registry

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.uber.org/zap"
)

// mockRegistry implements SkillRegistry for testing.
type mockRegistry struct {
	name    string
	enabled bool
	skills  []SkillMeta
	err     error
	delay   time.Duration
}

func (m *mockRegistry) Name() string  { return m.name }
func (m *mockRegistry) Enabled() bool { return m.enabled }

func (m *mockRegistry) Search(ctx context.Context, query string) ([]SkillMeta, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.err != nil {
		return nil, m.err
	}
	// Filter by query (simple substring match)
	var results []SkillMeta
	for _, s := range m.skills {
		results = append(results, s)
	}
	return results, nil
}

func (m *mockRegistry) GetSkillMeta(ctx context.Context, nameOrSlug string) (*SkillMeta, error) {
	for _, s := range m.skills {
		if s.Name == nameOrSlug || s.Slug == nameOrSlug {
			return &s, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (m *mockRegistry) DownloadSkill(ctx context.Context, meta *SkillMeta) ([]byte, error) {
	return []byte("# " + meta.Name + "\n\nSkill content."), nil
}

func TestManagerSearchAllFanOut(t *testing.T) {
	logger := zap.NewNop()

	reg1 := &mockRegistry{
		name:    "registry-a",
		enabled: true,
		skills: []SkillMeta{
			{Name: "golang", Slug: "golang", Version: "1.0", RegistryName: "registry-a"},
			{Name: "python", Slug: "python", Version: "1.0", RegistryName: "registry-a"},
		},
	}

	reg2 := &mockRegistry{
		name:    "registry-b",
		enabled: true,
		skills: []SkillMeta{
			{Name: "golang", Slug: "golang", Version: "2.0", RegistryName: "registry-b"}, // duplicate
			{Name: "rust", Slug: "rust", Version: "1.0", RegistryName: "registry-b"},
		},
	}

	rm := &RegistryManager{
		registries:    []SkillRegistry{reg1, reg2},
		installer:     NewInstaller(t.TempDir(), logger),
		searchCache:   NewTrigramCache(10, 5*time.Minute),
		maxConcurrent: 3,
		logger:        logger,
	}

	merged, results := rm.SearchAll(context.Background(), "test")

	if len(results) != 2 {
		t.Fatalf("expected 2 search results, got %d", len(results))
	}

	// Should have 3 unique skills (golang deduplicated)
	if len(merged) != 3 {
		t.Fatalf("expected 3 merged skills, got %d", len(merged))
	}

	// First registry wins for dedup — golang should be from registry-a
	for _, s := range merged {
		if s.Name == "golang" && s.RegistryName != "registry-a" {
			t.Errorf("expected golang from registry-a, got from %s", s.RegistryName)
		}
	}
}

func TestManagerSearchAllWithError(t *testing.T) {
	logger := zap.NewNop()

	reg1 := &mockRegistry{
		name:    "working",
		enabled: true,
		skills:  []SkillMeta{{Name: "skill-a", Slug: "skill-a", RegistryName: "working"}},
	}

	reg2 := &mockRegistry{
		name:    "broken",
		enabled: true,
		err:     fmt.Errorf("connection refused"),
	}

	rm := &RegistryManager{
		registries:    []SkillRegistry{reg1, reg2},
		installer:     NewInstaller(t.TempDir(), logger),
		searchCache:   NewTrigramCache(10, 5*time.Minute),
		maxConcurrent: 3,
		logger:        logger,
	}

	merged, results := rm.SearchAll(context.Background(), "test")

	// Should still get results from working registry
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged skill, got %d", len(merged))
	}
	if merged[0].Name != "skill-a" {
		t.Errorf("expected skill-a, got %s", merged[0].Name)
	}

	// Broken registry should have error
	if results[1].Error == nil {
		t.Error("expected error from broken registry")
	}
}

func TestManagerSearchAllDisabledRegistry(t *testing.T) {
	logger := zap.NewNop()

	reg1 := &mockRegistry{
		name:    "enabled",
		enabled: true,
		skills:  []SkillMeta{{Name: "visible", Slug: "visible", RegistryName: "enabled"}},
	}

	reg2 := &mockRegistry{
		name:    "disabled",
		enabled: false,
		skills:  []SkillMeta{{Name: "hidden", Slug: "hidden", RegistryName: "disabled"}},
	}

	rm := &RegistryManager{
		registries:    []SkillRegistry{reg1, reg2},
		installer:     NewInstaller(t.TempDir(), logger),
		searchCache:   NewTrigramCache(10, 5*time.Minute),
		maxConcurrent: 3,
		logger:        logger,
	}

	merged, _ := rm.SearchAll(context.Background(), "test")

	if len(merged) != 1 {
		t.Fatalf("expected 1 merged skill, got %d", len(merged))
	}
	if merged[0].Name != "visible" {
		t.Errorf("expected 'visible', got %s", merged[0].Name)
	}
}

func TestManagerSearchCaching(t *testing.T) {
	logger := zap.NewNop()

	reg := &mockRegistry{
		name:    "test",
		enabled: true,
		skills:  []SkillMeta{{Name: "cached", Slug: "cached", RegistryName: "test"}},
	}

	rm := &RegistryManager{
		registries:    []SkillRegistry{reg},
		installer:     NewInstaller(t.TempDir(), logger),
		searchCache:   NewTrigramCache(10, 5*time.Minute),
		maxConcurrent: 3,
		logger:        logger,
	}

	// First call populates the cache
	merged1, _ := rm.SearchAll(context.Background(), "test-query")
	if len(merged1) != 1 {
		t.Fatalf("expected 1 result, got %d", len(merged1))
	}

	// Second call should hit the trigram cache (returns results directly, nil for SearchResult)
	merged2, results2 := rm.SearchAll(context.Background(), "test-query")
	if len(merged2) != 1 {
		t.Fatalf("expected 1 cached result, got %d", len(merged2))
	}
	// When cache hits, results slice is nil (no network calls)
	if results2 != nil {
		t.Error("expected nil results on cache hit")
	}
}

func TestManagerInstallAndUninstall(t *testing.T) {
	logger := zap.NewNop()
	tmpDir := t.TempDir()

	reg := &mockRegistry{
		name:    "test-reg",
		enabled: true,
		skills: []SkillMeta{
			{
				Name:         "installable",
				Slug:         "installable",
				Version:      "1.0",
				RegistryName: "test-reg",
			},
		},
	}

	cfg := RegistriesConfig{
		InstallDir:    tmpDir,
		MaxConcurrent: 3,
	}

	rm := &RegistryManager{
		registries:    []SkillRegistry{reg},
		installer:     NewInstaller(tmpDir, logger),
		searchCache:   NewTrigramCache(10, 5*time.Minute),
		config:        cfg,
		maxConcurrent: 3,
		logger:        logger,
	}

	// Install
	result, err := rm.Install(context.Background(), "installable")
	if err != nil {
		t.Fatalf("install failed: %v", err)
	}
	if result.Name != "installable" {
		t.Errorf("expected name 'installable', got %q", result.Name)
	}

	// Verify installed
	if !rm.IsInstalled("installable") {
		t.Error("expected skill to be installed")
	}

	// Uninstall
	if err := rm.Uninstall("installable"); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}
	if rm.IsInstalled("installable") {
		t.Error("expected skill to be uninstalled")
	}
}

func TestManagerGetRegistries(t *testing.T) {
	cfg := RegistriesConfig{
		Registries: []RegistryEntry{
			{Name: "a", URL: "https://a.dev", IsActive: true},
			{Name: "b", URL: "https://b.ai", IsActive: false},
		},
	}

	rm := &RegistryManager{
		config: cfg,
		logger: zap.NewNop(),
	}

	infos := rm.GetRegistries()
	if len(infos) != 2 {
		t.Fatalf("expected 2 registries, got %d", len(infos))
	}
	if infos[0].Name != "a" || !infos[0].Enabled {
		t.Errorf("unexpected registry[0]: %+v", infos[0])
	}
	if infos[1].Name != "b" || infos[1].Enabled {
		t.Errorf("unexpected registry[1]: %+v", infos[1])
	}
}
