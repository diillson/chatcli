package memory

import (
	"time"
)

// UserProfile tracks who the user is and how they interact.
type UserProfile struct {
	Name           string            `json:"name,omitempty"`
	Role           string            `json:"role,omitempty"`
	ExpertiseLevel string            `json:"expertise_level,omitempty"` // beginner, intermediate, expert
	PreferredLang  string            `json:"preferred_language,omitempty"`
	CommStyle      string            `json:"communication_style,omitempty"`
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
