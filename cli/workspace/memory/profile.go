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

// IsEmpty reports whether the profile holds no user-supplied data yet
// (command counts in TopCommands do not count as "data about the user").
func (ps *UserProfileStore) IsEmpty() bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.isEmptyLocked()
}

// isEmptyLocked is the lock-free core of IsEmpty; callers must hold at
// least a read lock.
func (ps *UserProfileStore) isEmptyLocked() bool {
	p := ps.profile
	return p.Name == "" && p.Role == "" && p.ExpertiseLevel == "" &&
		p.PreferredLang == "" && p.CommStyle == "" && p.Company == "" &&
		p.Location == "" && len(p.Certifications) == 0 && len(p.Skills) == 0 &&
		len(p.Goals) == 0 && len(p.Interests) == 0 && len(p.Directives) == 0 &&
		len(p.Milestones) == 0 && len(p.Preferences) == 0
}

// Update applies partial updates to the profile.
//
// Scalar fields overwrite. List fields (certifications/skills/goals/interests/
// directives) default to UPSERT — new items append, and an item whose stem
// matches an existing entry supersedes it in place, so progress restatements
// stop accumulating. Keys may carry an operation affix to change that:
// "goals_replace" overwrites the whole list (empty value clears it),
// "goals_remove"/"goals_done" removes matching entries. "milestone" appends a
// dated event to the timeline, and "preferences_remove" deletes preference
// keys. Empty values are ignored except for *_replace.
func (ps *UserProfileStore) Update(updates map[string]string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	changed := false
	for key, value := range updates {
		if ps.applyUpdate(key, strings.TrimSpace(value)) {
			changed = true
		}
	}

	if changed {
		ps.profile.LastUpdated = time.Now()
		ps.persist()
	}
	return changed
}

// applyUpdate applies one key/value update; callers must hold the write lock.
func (ps *UserProfileStore) applyUpdate(key, value string) bool {
	if field, op, ok := resolveListKey(key); ok {
		return ps.applyListOp(field, op, value)
	}
	if value == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "name":
		return ps.setScalar(&ps.profile.Name, value)
	case "role":
		return ps.setScalar(&ps.profile.Role, value)
	case "expertise_level", "expertise", "level":
		return ps.setScalar(&ps.profile.ExpertiseLevel, normalizeExpertise(value))
	case "preferred_language", "language", "lang":
		return ps.setScalar(&ps.profile.PreferredLang, value)
	case "communication_style", "comm_style", "style":
		return ps.setScalar(&ps.profile.CommStyle, value)
	case "company", "employer", "organization", "org":
		return ps.setScalar(&ps.profile.Company, value)
	case "location", "city", "country", "timezone", "tz":
		return ps.setScalar(&ps.profile.Location, value)
	case "milestone", "milestones":
		return ps.addMilestones(value)
	case "preferences_remove", "preference_remove", "remove_preference", "remove_preferences":
		return ps.removePreferences(value)
	default:
		// Store as generic preference. This is the escape hatch that
		// keeps the profile open-ended: any personal fact the model
		// reports with a novel key is preserved instead of dropped.
		if ps.profile.Preferences == nil {
			ps.profile.Preferences = make(map[string]string)
		}
		if ps.profile.Preferences[key] != value {
			ps.profile.Preferences[key] = value
			return true
		}
		return false
	}
}

func (ps *UserProfileStore) setScalar(target *string, value string) bool {
	if *target == value {
		return false
	}
	*target = value
	return true
}

// listRef resolves a canonical list-field name to its slice.
func (ps *UserProfileStore) listRef(field string) *[]string {
	switch field {
	case "certifications":
		return &ps.profile.Certifications
	case "skills":
		return &ps.profile.Skills
	case "goals":
		return &ps.profile.Goals
	case "interests":
		return &ps.profile.Interests
	case "directives":
		return &ps.profile.Directives
	}
	return nil
}

// applyListOp executes one list operation; callers must hold the write lock.
func (ps *UserProfileStore) applyListOp(field string, op listOp, value string) bool {
	list := ps.listRef(field)
	if list == nil {
		return false
	}
	switch op {
	case opReplace:
		return replaceItems(list, splitListItems(value))
	case opRemove:
		if value == "" {
			return false
		}
		return removeItems(list, splitListItems(value))
	default:
		if value == "" {
			return false
		}
		return upsertItems(list, splitListItems(value))
	}
}

// addMilestones appends dated timeline events, skipping restatements of an
// already-recorded milestone (same stem). Milestones split only on ';' and
// newlines — they are sentences and legitimately contain commas.
func (ps *UserProfileStore) addMilestones(value string) bool {
	changed := false
	for _, item := range splitSentenceItems(value) {
		if item == "" || isFragmentEntry(item) {
			continue
		}
		st := stemOf(item)
		dup := false
		for _, m := range ps.profile.Milestones {
			if stemOf(m.Text) == st {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		ps.profile.Milestones = append(ps.profile.Milestones, Milestone{Date: time.Now(), Text: item})
		changed = true
	}
	return changed
}

// removePreferences deletes the named preference keys (comma-separated).
func (ps *UserProfileStore) removePreferences(value string) bool {
	changed := false
	for _, k := range splitListItems(value) {
		if _, ok := ps.profile.Preferences[k]; ok {
			delete(ps.profile.Preferences, k)
			changed = true
		}
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
	if ps.isEmptyLocked() {
		return ""
	}

	parts := make([]string, 0, 11+len(p.Preferences))
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
	if p.Company != "" {
		parts = append(parts, "Company: "+p.Company)
	}
	if p.Location != "" {
		parts = append(parts, "Location: "+p.Location)
	}
	if len(p.Certifications) > 0 {
		parts = append(parts, "Certifications: "+strings.Join(p.Certifications, ", "))
	}
	if len(p.Skills) > 0 {
		parts = append(parts, "Skills: "+strings.Join(p.Skills, ", "))
	}
	if len(p.Goals) > 0 {
		parts = append(parts, "Goals: "+strings.Join(p.Goals, ", "))
	}
	if len(p.Interests) > 0 {
		parts = append(parts, "Interests: "+strings.Join(p.Interests, ", "))
	}
	if len(p.Directives) > 0 {
		parts = append(parts, "Directives (standing user instructions — follow them): "+strings.Join(p.Directives, "; "))
	}
	if len(p.Milestones) > 0 {
		// Most recent milestones last (chronological), bounded so the prompt
		// stays lean while long-range history remains on disk.
		ms := p.Milestones
		if len(ms) > 8 {
			ms = ms[len(ms)-8:]
		}
		entries := make([]string, 0, len(ms))
		for _, m := range ms {
			entries = append(entries, "["+m.Date.Format("2006-01-02")+"] "+m.Text)
		}
		parts = append(parts, "Milestones: "+strings.Join(entries, "; "))
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
		// Quarantine for recovery instead of leaving the corrupt file in place
		// for the next persist to overwrite (see persist.go).
		if qpath, qerr := quarantineCorrupt(ps.path); qerr == nil {
			ps.logger.Warn("user profile corrupt; quarantined for recovery",
				zap.String("quarantine", qpath), zap.Error(err))
		} else {
			ps.logger.Warn("user profile corrupt and quarantine failed", zap.Error(qerr))
		}
		return
	}
	if p.TopCommands == nil {
		p.TopCommands = make(map[string]int)
	}
	if p.Preferences == nil {
		p.Preferences = make(map[string]string)
	}
	// Self-heal list fields polluted by earlier append-only versions and
	// persist the healed profile, so legacy damage is fixed on the next run
	// and never resurfaces.
	healed := normalizeLoadedProfile(&p)
	ps.profile = p
	if healed {
		ps.profile.LastUpdated = time.Now()
		ps.persist()
	}
}

func (ps *UserProfileStore) persist() {
	data, err := json.MarshalIndent(ps.profile, "", "  ")
	if err != nil {
		ps.logger.Warn("failed to marshal user profile", zap.Error(err))
		return
	}
	if err := atomicWriteFile(ps.path, data, 0o600); err != nil {
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
