package memory

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// UserProfileStore manages the user profile on disk.
type UserProfileStore struct {
	profile UserProfile
	mu      sync.RWMutex
	path    string
	logger  *zap.Logger
}

// NewUserProfileStore creates a new profile store and loads existing data.
func NewUserProfileStore(memoryDir string, logger *zap.Logger) *UserProfileStore {
	ps := &UserProfileStore{
		path:   memoryDir + "/user_profile.json",
		logger: logger,
		profile: UserProfile{
			TopCommands: make(map[string]int),
			Preferences: make(map[string]string),
		},
	}
	ps.load()
	return ps
}

// Get returns a copy of the current profile.
func (ps *UserProfileStore) Get() UserProfile {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.profile
}

// Update applies partial updates to the profile.
// Only non-empty fields in the update are applied.
func (ps *UserProfileStore) Update(updates map[string]string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	changed := false
	for key, value := range updates {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch strings.ToLower(key) {
		case "name":
			if ps.profile.Name != value {
				ps.profile.Name = value
				changed = true
			}
		case "role":
			if ps.profile.Role != value {
				ps.profile.Role = value
				changed = true
			}
		case "expertise_level", "expertise", "level":
			normalized := normalizeExpertise(value)
			if ps.profile.ExpertiseLevel != normalized {
				ps.profile.ExpertiseLevel = normalized
				changed = true
			}
		case "preferred_language", "language", "lang":
			if ps.profile.PreferredLang != value {
				ps.profile.PreferredLang = value
				changed = true
			}
		case "communication_style", "comm_style", "style":
			if ps.profile.CommStyle != value {
				ps.profile.CommStyle = value
				changed = true
			}
		default:
			// Store as generic preference
			if ps.profile.Preferences == nil {
				ps.profile.Preferences = make(map[string]string)
			}
			if ps.profile.Preferences[key] != value {
				ps.profile.Preferences[key] = value
				changed = true
			}
		}
	}

	if changed {
		ps.profile.LastUpdated = time.Now()
		ps.persist()
	}
	return changed
}

// RecordCommand increments command usage counter.
func (ps *UserProfileStore) RecordCommand(cmd string) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	if ps.profile.TopCommands == nil {
		ps.profile.TopCommands = make(map[string]int)
	}
	ps.profile.TopCommands[cmd]++
	ps.persist()
}

// FormatForPrompt returns a concise summary for system prompt injection.
func (ps *UserProfileStore) FormatForPrompt() string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	p := ps.profile
	if p.Name == "" && p.Role == "" && p.ExpertiseLevel == "" && p.PreferredLang == "" {
		return ""
	}

	var parts []string
	if p.Name != "" {
		parts = append(parts, "Name: "+p.Name)
	}
	if p.Role != "" {
		parts = append(parts, "Role: "+p.Role)
	}
	if p.ExpertiseLevel != "" {
		parts = append(parts, "Expertise: "+p.ExpertiseLevel)
	}
	if p.PreferredLang != "" {
		parts = append(parts, "Language: "+p.PreferredLang)
	}
	if p.CommStyle != "" {
		parts = append(parts, "Style: "+p.CommStyle)
	}

	// Top 5 commands
	if len(p.TopCommands) > 0 {
		type cmdCount struct {
			cmd   string
			count int
		}
		var cmds []cmdCount
		for c, n := range p.TopCommands {
			cmds = append(cmds, cmdCount{c, n})
		}
		// Sort by count descending
		for i := 0; i < len(cmds); i++ {
			for j := i + 1; j < len(cmds); j++ {
				if cmds[j].count > cmds[i].count {
					cmds[i], cmds[j] = cmds[j], cmds[i]
				}
			}
		}
		limit := 5
		if len(cmds) < limit {
			limit = len(cmds)
		}
		var topList []string
		for _, c := range cmds[:limit] {
			topList = append(topList, c.cmd)
		}
		parts = append(parts, "Most used: "+strings.Join(topList, ", "))
	}

	// Key preferences
	for k, v := range p.Preferences {
		parts = append(parts, k+": "+v)
	}

	return strings.Join(parts, "\n")
}

// --- internal ---

func (ps *UserProfileStore) load() {
	data, err := os.ReadFile(ps.path)
	if err != nil {
		return
	}
	var p UserProfile
	if err := json.Unmarshal(data, &p); err != nil {
		ps.logger.Warn("failed to parse user profile", zap.Error(err))
		return
	}
	if p.TopCommands == nil {
		p.TopCommands = make(map[string]int)
	}
	if p.Preferences == nil {
		p.Preferences = make(map[string]string)
	}
	ps.profile = p
}

func (ps *UserProfileStore) persist() {
	data, err := json.MarshalIndent(ps.profile, "", "  ")
	if err != nil {
		ps.logger.Warn("failed to marshal user profile", zap.Error(err))
		return
	}
	if err := os.WriteFile(ps.path, data, 0o644); err != nil {
		ps.logger.Warn("failed to write user profile", zap.Error(err))
	}
}

func normalizeExpertise(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "beginner", "novice", "iniciante":
		return "beginner"
	case "intermediate", "mid", "intermediario", "intermediário":
		return "intermediate"
	case "expert", "advanced", "senior", "avançado", "avancado":
		return "expert"
	default:
		return level
	}
}
