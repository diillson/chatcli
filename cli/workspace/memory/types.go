package memory

import (
	"time"
)

// UserProfile tracks who the user is and how they interact.
//
// The scalar fields (Name, Role, …) are the "fast path" identity slots the
// extraction prompt knows by name. The list fields (Certifications, Skills,
// Goals) capture accreting personal facts that previously had nowhere to
// live and were silently dropped. Anything the model reports that does not
// match a typed field lands in Preferences (free-form key/value), so the
// profile can grow without a schema change — see UserProfileStore.Update.
type UserProfile struct {
	Name           string            `json:"name,omitempty"`
	Role           string            `json:"role,omitempty"`
	ExpertiseLevel string            `json:"expertise_level,omitempty"` // beginner, intermediate, expert
	PreferredLang  string            `json:"preferred_language,omitempty"`
	CommStyle      string            `json:"communication_style,omitempty"`
	Company        string            `json:"company,omitempty"`
	Location       string            `json:"location,omitempty"`
	Certifications []string          `json:"certifications,omitempty"`
	Skills         []string          `json:"skills,omitempty"`
	Goals          []string          `json:"goals,omitempty"`
	TopCommands    map[string]int    `json:"top_commands,omitempty"`
	Preferences    map[string]string `json:"preferences,omitempty"`
	LastUpdated    time.Time         `json:"last_updated"`
}

// Fact is a single unit of long-term memory with scoring metadata.
type Fact struct {
	ID            string    `json:"id"`
	Content       string    `json:"content"`
	Category      string    `json:"category"` // architecture, pattern, preference, gotcha, project, personal
	Tags          []string  `json:"tags,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	LastAccessed  time.Time `json:"last_accessed"`
	AccessCount   int       `json:"access_count"`
	Score         float64   `json:"score"`
	Source        string    `json:"source,omitempty"`
	SourceProject string    `json:"source_project,omitempty"` // project path where this fact was learned
	// Confidence ∈ (0,1] is how much to trust this fact: deterministic
	// user-stated facts rank above background-extracted guesses, and a fact
	// reinforced by re-observation climbs. Zero means "unset" (legacy facts)
	// and is treated as the default; see Fact.confidence().
	Confidence float64 `json:"confidence,omitempty"`
	// Provenance records HOW the fact was learned (e.g. "user", "extraction",
	// "correction", or a supersession note), for auditability.
	Provenance string `json:"provenance,omitempty"`
}

// Topic tracks a recurring subject across conversations.
type Topic struct {
	Name         string    `json:"name"`
	Mentions     int       `json:"mentions"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	RelatedFacts []string  `json:"related_fact_ids,omitempty"`
}

// Project tracks an active project with context.
type Project struct {
	Name         string            `json:"name"`
	Path         string            `json:"path,omitempty"`
	Description  string            `json:"description,omitempty"`
	Status       string            `json:"status"` // active, paused, completed
	Technologies []string          `json:"technologies,omitempty"`
	KeyFiles     []string          `json:"key_files,omitempty"`
	LastActive   time.Time         `json:"last_active"`
	Priority     int               `json:"priority"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

// UsageStats tracks interaction patterns.
type UsageStats struct {
	SessionCount     int            `json:"session_count"`
	TotalMessages    int            `json:"total_messages"`
	AvgSessionSecs   float64        `json:"avg_session_secs"`
	HourDistribution [24]int        `json:"hour_distribution"`
	CommandFrequency map[string]int `json:"command_frequency"`
	FeatureUsage     map[string]int `json:"feature_usage"`
	CommonErrors     []ErrorPattern `json:"common_errors,omitempty"`
	LastSession      time.Time      `json:"last_session"`
}

// ErrorPattern records a recurring error and how it was resolved.
type ErrorPattern struct {
	Pattern    string    `json:"pattern"`
	Count      int       `json:"count"`
	Resolution string    `json:"resolution,omitempty"`
	LastSeen   time.Time `json:"last_seen"`
}

// Config holds tunable parameters for the memory system.
type Config struct {
	MaxMemoryMDSize    int     `json:"max_memory_md_size"`    // bytes, default 32KB
	DailyNoteRetention int     `json:"daily_note_retention"`  // days, default 30
	MaxFactsCount      int     `json:"max_facts_count"`       // default 500
	CompactionInterval int     `json:"compaction_interval_h"` // hours, default 24
	RetrievalBudget    int     `json:"retrieval_budget"`      // max chars for system prompt, default 4000
	DecayHalfLifeDays  float64 `json:"decay_half_life_days"`  // default 30

	// Blended-ranking tunables (HyDE retrieval path). These govern how the
	// semantic (cosine), lexical (keyword) and temporal (recency) signals are
	// fused into a single fact ranking. Provider-agnostic by construction:
	// cosine is computed identically for every embedding backend, so the same
	// defaults hold whether the operator runs Voyage, OpenAI, Bedrock or any
	// of the other supported providers.
	MinCosineScore   float64     `json:"min_cosine_score"`   // floor for vector hits, default 0.25
	VectorTopK       int         `json:"vector_top_k"`       // candidate vectors pulled per query, default 12
	BackfillBatchMax int         `json:"backfill_batch_max"` // max facts embedded per retrieval, default 500
	RankWeights      RankWeights `json:"rank_weights"`       // signal fusion weights
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxMemoryMDSize:    32 * 1024,
		DailyNoteRetention: 30,
		MaxFactsCount:      500,
		CompactionInterval: 24,
		RetrievalBudget:    4000,
		DecayHalfLifeDays:  30.0,
		MinCosineScore:     0.25,
		VectorTopK:         12,
		BackfillBatchMax:   500,
		RankWeights:        DefaultRankWeights(),
	}
}

// DailyNote represents a single day's note (same as parent package).
type DailyNote struct {
	Date    time.Time
	Path    string
	Content string
}

// InteractionEvent records a single interaction for stats tracking.
type InteractionEvent struct {
	Timestamp time.Time
	Command   string
	Feature   string // chat, agent, coder, plugin
	Duration  time.Duration
	Error     string
}

// LLMClient is the interface the memory system needs from the LLM.
// Matches the subset of client.LLMClient used by memory operations.
type LLMClient interface {
	SendPrompt(ctx interface{}, prompt string, history interface{}, maxTokens int) (string, error)
}
