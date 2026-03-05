package registry

import (
	"testing"
	"time"
)

func TestExtractTrigrams(t *testing.T) {
	tests := []struct {
		input    string
		expected int // number of unique trigrams
	}{
		{"golang", 4},     // "gol", "ola", "lan", "ang"
		{"go", 1},         // "go" (short string, stored as-is)
		{"a", 1},          // "a"
		{"", 0},           // empty
		{"abc", 1},        // "abc"
		{"kubernetes", 8}, // "kub", "ube", "ber", "ern", "rne", "net", "ete", "tes"
	}

	for _, tt := range tests {
		trigrams := ExtractTrigrams(tt.input)
		if len(trigrams) != tt.expected {
			t.Errorf("ExtractTrigrams(%q): got %d trigrams, want %d. trigrams: %v",
				tt.input, len(trigrams), tt.expected, trigrams)
		}
	}
}

func TestJaccardSimilarity(t *testing.T) {
	tests := []struct {
		a, b   string
		minSim float64
		maxSim float64
	}{
		{"golang", "golang", 1.0, 1.0},         // identical
		{"golang", "golan", 0.7, 1.0},          // prefix match
		{"golang", "python", 0.0, 0.1},         // completely different
		{"kubernetes", "kubernete", 0.7, 1.0},  // prefix match
		{"docker", "docker-compose", 0.3, 0.6}, // partial overlap
	}

	for _, tt := range tests {
		triA := ExtractTrigrams(tt.a)
		triB := ExtractTrigrams(tt.b)
		sim := JaccardSimilarity(triA, triB)
		if sim < tt.minSim || sim > tt.maxSim {
			t.Errorf("JaccardSimilarity(%q, %q): got %.3f, want [%.3f, %.3f]",
				tt.a, tt.b, sim, tt.minSim, tt.maxSim)
		}
	}
}

func TestJaccardSimilarityEdgeCases(t *testing.T) {
	// Both empty
	sim := JaccardSimilarity(map[string]bool{}, map[string]bool{})
	if sim != 1.0 {
		t.Errorf("JaccardSimilarity(empty, empty): got %.3f, want 1.0", sim)
	}

	// One empty
	a := ExtractTrigrams("golang")
	sim = JaccardSimilarity(a, map[string]bool{})
	if sim != 0.0 {
		t.Errorf("JaccardSimilarity(set, empty): got %.3f, want 0.0", sim)
	}
}

func TestTrigramCacheExactMatch(t *testing.T) {
	cache := NewTrigramCache(10, 5*time.Minute)

	skills := []SkillMeta{
		{Name: "golang-expert", Version: "1.0"},
	}

	cache.Put("golang", skills)

	// Exact match should work
	result := cache.Get("golang")
	if result == nil {
		t.Fatal("expected cache hit for exact match")
	}
	if len(result) != 1 || result[0].Name != "golang-expert" {
		t.Errorf("unexpected result: %v", result)
	}
}

func TestTrigramCacheFuzzyMatch(t *testing.T) {
	cache := NewTrigramCache(10, 5*time.Minute)

	skills := []SkillMeta{
		{Name: "golang-expert", Version: "1.0"},
	}

	cache.Put("golang", skills)

	// Fuzzy match — "golan" should match "golang" (high similarity)
	result := cache.Get("golan")
	if result == nil {
		t.Fatal("expected cache hit for fuzzy match 'golan' -> 'golang'")
	}
	if len(result) != 1 {
		t.Errorf("expected 1 result, got %d", len(result))
	}
}

func TestTrigramCacheNoMatch(t *testing.T) {
	cache := NewTrigramCache(10, 5*time.Minute)

	skills := []SkillMeta{
		{Name: "golang-expert", Version: "1.0"},
	}
	cache.Put("golang", skills)

	// No match — "python" is too different
	result := cache.Get("python")
	if result != nil {
		t.Errorf("expected cache miss for 'python', got %v", result)
	}
}

func TestTrigramCacheTTL(t *testing.T) {
	cache := NewTrigramCache(10, 50*time.Millisecond)

	skills := []SkillMeta{{Name: "test"}}
	cache.Put("query", skills)

	// Should hit before TTL
	if result := cache.Get("query"); result == nil {
		t.Fatal("expected cache hit before TTL")
	}

	// Wait for TTL
	time.Sleep(60 * time.Millisecond)

	// Should miss after TTL
	if result := cache.Get("query"); result != nil {
		t.Fatal("expected cache miss after TTL")
	}
}

func TestTrigramCacheLRUEviction(t *testing.T) {
	cache := NewTrigramCache(3, 5*time.Minute)

	cache.Put("aaa", []SkillMeta{{Name: "a"}})
	cache.Put("bbb", []SkillMeta{{Name: "b"}})
	cache.Put("ccc", []SkillMeta{{Name: "c"}})

	// All should be present
	if cache.Size() != 3 {
		t.Fatalf("expected 3 entries, got %d", cache.Size())
	}

	// Adding a 4th should evict the oldest (aaa)
	cache.Put("ddd", []SkillMeta{{Name: "d"}})

	if cache.Size() != 3 {
		t.Fatalf("expected 3 entries after eviction, got %d", cache.Size())
	}

	// "aaa" should be evicted
	if result := cache.Get("aaa"); result != nil {
		t.Error("expected 'aaa' to be evicted")
	}

	// "ddd" should be present
	if result := cache.Get("ddd"); result == nil {
		t.Error("expected 'ddd' to be present")
	}
}

func TestTrigramCacheInvalidate(t *testing.T) {
	cache := NewTrigramCache(10, 5*time.Minute)

	cache.Put("golang", []SkillMeta{{Name: "test"}})
	cache.Invalidate("golang")

	if result := cache.Get("golang"); result != nil {
		t.Error("expected nil after invalidation")
	}
	if cache.Size() != 0 {
		t.Errorf("expected 0 entries, got %d", cache.Size())
	}
}

func TestTrigramCacheClear(t *testing.T) {
	cache := NewTrigramCache(10, 5*time.Minute)

	cache.Put("a", []SkillMeta{{Name: "a"}})
	cache.Put("b", []SkillMeta{{Name: "b"}})
	cache.Clear()

	if cache.Size() != 0 {
		t.Errorf("expected 0 entries after clear, got %d", cache.Size())
	}
}
