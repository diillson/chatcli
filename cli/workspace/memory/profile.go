package memory

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// UserProfileStore manages the user profile on disk.
type UserProfileStore struct {
	latch   storeLatch // read-only once a sealed file could not be opened
	profile UserProfile
	mu      sync.RWMutex
	path    string
	logger  *zap.Logger
	// loadedAt is when the file was last read or written by this process;
	// a file newer than that carries another process's changes to merge.
	loadedAt time.Time

	// changeNotifier marks derived caches (knowledge graph) stale on
	// content mutations. See change_notify.go for the non-caller list.
	changeNotifier
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
		len(p.Milestones) == 0 && len(p.Stances) == 0 && len(p.Environment) == 0 &&
		len(p.Preferences) == 0
}

// Sources recorded in FieldMeta: deterministic user-stated updates (tool,
// /memory profile set, chat exception) rank above background extraction.
const (
	FieldSourceUser       = "user"
	FieldSourceExtraction = "extraction"
)

// staleAfter is how long a field may go without re-affirmation before the
// prompt flags it as possibly stale, prompting a casual re-confirmation.
const staleAfter = 120 * 24 * time.Hour

// Update applies partial updates to the profile with the deterministic
// "user" source (see UpdateWithSource for semantics).
func (ps *UserProfileStore) Update(updates map[string]string) bool {
	return ps.UpdateWithSource(updates, FieldSourceUser)
}

// UpdateWithSource applies partial updates to the profile, recording per-field
// provenance and freshness in FieldMeta.
//
// Scalar fields overwrite. List fields (certifications/skills/goals/interests/
// directives) default to UPSERT — new items append, and an item whose stem
// matches an existing entry supersedes it in place, so progress restatements
// stop accumulating. Keys may carry an operation affix to change that:
// "goals_replace" overwrites the whole list (empty value clears it),
// "goals_remove"/"goals_done" removes matching entries. "milestone" appends a
// dated event to the timeline, "stance" records "position :: reason",
// "env_<key>" fills the structured environment, "preferences_remove" deletes
// preference keys, and "sensitive_mark"/"sensitive_unmark" flip a field's
// privacy flag. Empty values are ignored except for *_replace.
//
// A user-sourced update that changes nothing still counts as re-affirmation:
// ConfirmedAt is bumped so freshness reflects "recently confirmed", not just
// "recently changed".
func (ps *UserProfileStore) UpdateWithSource(updates map[string]string, source string) bool {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	changed, metaDirty := false, false
	for key, value := range updates {
		res := ps.applyUpdate(key, strings.TrimSpace(value))
		if res.changed {
			changed = true
		}
		if ps.touchFieldMeta(res, key, source) {
			metaDirty = true
		}
	}

	if changed {
		ps.profile.LastUpdated = time.Now()
	}
	if changed || metaDirty {
		ps.notifyChanged()
		ps.persist()
	}
	return changed
}

// updateResult describes one applied key/value: which canonical field it
// touched (empty when nothing applies), whether data changed, and whether an
// unchanged write still counts as the user re-affirming the current value.
type updateResult struct {
	field      string
	changed    bool
	affirmable bool
}

// applyUpdate applies one key/value update; callers must hold the write lock.
func (ps *UserProfileStore) applyUpdate(key, value string) updateResult {
	if field, op, ok := resolveListKey(key); ok {
		return updateResult{field: field, changed: ps.applyListOp(field, op, value), affirmable: op == opUpsert}
	}
	lk := strings.ToLower(strings.TrimSpace(key))
	switch lk {
	case "sensitive_mark", "mark_sensitive":
		return updateResult{changed: ps.setSensitive(value, true)}
	case "sensitive_unmark", "unmark_sensitive":
		return updateResult{changed: ps.setSensitive(value, false)}
	}
	if value == "" {
		return updateResult{}
	}
	switch lk {
	case "name":
		return updateResult{field: "name", changed: ps.setScalar(&ps.profile.Name, value), affirmable: true}
	case "role":
		return updateResult{field: "role", changed: ps.setScalar(&ps.profile.Role, value), affirmable: true}
	case "expertise_level", "expertise", "level":
		return updateResult{field: "expertise_level", changed: ps.setScalar(&ps.profile.ExpertiseLevel, normalizeExpertise(value)), affirmable: true}
	case "preferred_language", "language", "lang":
		return updateResult{field: "preferred_language", changed: ps.setScalar(&ps.profile.PreferredLang, value), affirmable: true}
	case "communication_style", "comm_style", "style":
		return updateResult{field: "communication_style", changed: ps.setScalar(&ps.profile.CommStyle, value), affirmable: true}
	case "company", "employer", "organization", "org":
		return updateResult{field: "company", changed: ps.setScalar(&ps.profile.Company, value), affirmable: true}
	case "location", "city", "country", "timezone", "tz":
		return updateResult{field: "location", changed: ps.setScalar(&ps.profile.Location, value), affirmable: true}
	case "milestone", "milestones":
		return updateResult{field: "milestones", changed: ps.addMilestones(value)}
	case "stance", "stances", "position", "opinion":
		return updateResult{field: "stances", changed: ps.upsertStances(value), affirmable: true}
	case "environment_remove", "env_remove":
		return updateResult{field: "environment", changed: ps.removeEnvironment(value)}
	case "preferences_remove", "preference_remove", "remove_preference", "remove_preferences":
		return updateResult{field: "preferences", changed: ps.removePreferences(value)}
	}
	if envKey, ok := strings.CutPrefix(lk, "env_"); ok && envKey != "" {
		return updateResult{field: "environment", changed: ps.setEnvironment(envKey, value), affirmable: true}
	}
	// Store as generic preference. This is the escape hatch that keeps the
	// profile open-ended: any personal fact the model reports with a novel
	// key is preserved instead of dropped. Meta is tracked per key so
	// sensitivity and staleness work at preference granularity.
	if ps.profile.Preferences == nil {
		ps.profile.Preferences = make(map[string]string)
	}
	if ps.profile.Preferences[key] != value {
		ps.profile.Preferences[key] = value
		return updateResult{field: "pref:" + key, changed: true, affirmable: true}
	}
	return updateResult{field: "pref:" + key, changed: false, affirmable: true}
}

// touchFieldMeta maintains provenance/freshness/sensitivity for one applied
// update; callers must hold the write lock. Returns whether meta changed.
func (ps *UserProfileStore) touchFieldMeta(res updateResult, rawKey, source string) bool {
	if res.field == "" {
		return false
	}
	if ps.profile.FieldMeta == nil {
		ps.profile.FieldMeta = make(map[string]FieldMeta)
	}
	m := ps.profile.FieldMeta[res.field]
	dirty := false
	now := time.Now()
	switch {
	case res.changed:
		m.Source, m.UpdatedAt, m.ConfirmedAt = source, now, now
		dirty = true
	case res.affirmable && source == FieldSourceUser:
		// The user restating the current value is a confirmation — and it
		// upgrades trust when the value had only been inferred by extraction.
		m.ConfirmedAt = now
		m.Source = source
		dirty = true
	}
	if !m.Sensitive && isSensitiveField(res.field, rawKey) {
		m.Sensitive = true
		dirty = true
	}
	if dirty {
		ps.profile.FieldMeta[res.field] = m
	}
	return dirty
}

// setSensitive flips the privacy flag on the named fields (comma-separated;
// preference keys may be given bare or as "pref:<key>").
func (ps *UserProfileStore) setSensitive(value string, sensitive bool) bool {
	if ps.profile.FieldMeta == nil {
		ps.profile.FieldMeta = make(map[string]FieldMeta)
	}
	changed := false
	for _, name := range splitListItems(value) {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if _, isPref := ps.profile.Preferences[key]; isPref && !strings.HasPrefix(key, "pref:") {
			key = "pref:" + key
		}
		m := ps.profile.FieldMeta[key]
		if m.Sensitive != sensitive {
			m.Sensitive = sensitive
			ps.profile.FieldMeta[key] = m
			changed = true
		}
	}
	return changed
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

// removePreferences deletes the named preference keys (comma-separated),
// along with their per-key meta.
func (ps *UserProfileStore) removePreferences(value string) bool {
	changed := false
	for _, k := range splitListItems(value) {
		if _, ok := ps.profile.Preferences[k]; ok {
			delete(ps.profile.Preferences, k)
			delete(ps.profile.FieldMeta, "pref:"+k)
			changed = true
		}
	}
	return changed
}

// upsertStances records "position :: reason" entries. A stance restating an
// existing position (same stem) supersedes it — positions evolve, they do
// not accumulate. Multiple stances split on ';'/newlines.
func (ps *UserProfileStore) upsertStances(value string) bool {
	changed := false
	for _, item := range splitSentenceItems(value) {
		position, reason := item, ""
		if i := strings.Index(item, "::"); i >= 0 {
			position = strings.TrimSpace(item[:i])
			reason = strings.TrimSpace(item[i+2:])
		}
		if position == "" || isFragmentEntry(position) {
			continue
		}
		st := stemOf(position)
		found := false
		for i := range ps.profile.Stances {
			if stemOf(ps.profile.Stances[i].Position) != st {
				continue
			}
			found = true
			if ps.profile.Stances[i].Position != position || ps.profile.Stances[i].Reason != reason {
				ps.profile.Stances[i] = Stance{Position: position, Reason: reason, UpdatedAt: time.Now()}
				changed = true
			}
			break
		}
		if !found {
			ps.profile.Stances = append(ps.profile.Stances, Stance{Position: position, Reason: reason, UpdatedAt: time.Now()})
			changed = true
		}
	}
	return changed
}

// setEnvironment sets one structured environment attribute (machine, OS,
// shell, editor, …) keyed without the env_ prefix.
func (ps *UserProfileStore) setEnvironment(key, value string) bool {
	if ps.profile.Environment == nil {
		ps.profile.Environment = make(map[string]string)
	}
	if ps.profile.Environment[key] == value {
		return false
	}
	ps.profile.Environment[key] = value
	return true
}

// removeEnvironment deletes the named environment keys (comma-separated).
func (ps *UserProfileStore) removeEnvironment(value string) bool {
	changed := false
	for _, k := range splitListItems(value) {
		k = strings.ToLower(strings.TrimSpace(k))
		if _, ok := ps.profile.Environment[k]; ok {
			delete(ps.profile.Environment, k)
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

// FormatForPrompt returns a concise summary for system prompt injection,
// with every scoped directive visible (labeled). Prefer FormatForPromptScoped
// when the caller knows the active workspace.
func (ps *UserProfileStore) FormatForPrompt() string {
	return ps.FormatForPromptScoped("")
}

// FormatForPromptScoped is FormatForPrompt filtered by the active workspace:
// directives scoped to another project are omitted, matching ones appear with
// their scope label, and globals always show. An empty workspaceDir hides
// nothing — no context means no basis to filter.
func (ps *UserProfileStore) FormatForPromptScoped(workspaceDir string) string {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	p := ps.profile
	if ps.isEmptyLocked() {
		return ""
	}

	parts := make([]string, 0, 16+len(p.Preferences))
	parts = appendIdentityParts(parts, p)
	parts = appendListParts(parts, p)
	parts = appendDirectiveParts(parts, p, workspaceDir)
	parts = appendStanceParts(parts, p)
	parts = appendEnvironmentParts(parts, p)
	parts = appendMilestoneParts(parts, p)
	parts = appendTopCommandParts(parts, p)
	parts = appendPreferenceParts(parts, p)

	if stale := staleFieldsLine(p); stale != "" {
		parts = append(parts, stale)
	}
	if hasSensitiveMeta(p) {
		parts = append(parts, "Privacy: fields tagged [sensitive] are private context — use them to inform answers, but NEVER quote them into code, tests, examples, commits, documents or any generated artifact.")
	}

	return strings.Join(parts, "\n")
}

func appendIdentityParts(parts []string, p UserProfile) []string {
	for _, f := range []struct{ label, value string }{
		{"Name", p.Name}, {"Role", p.Role}, {"Expertise", p.ExpertiseLevel},
		{"Language", p.PreferredLang}, {"Style", p.CommStyle},
		{"Company", p.Company}, {"Location", p.Location},
	} {
		if f.value != "" {
			parts = append(parts, f.label+": "+f.value)
		}
	}
	return parts
}

func appendListParts(parts []string, p UserProfile) []string {
	for _, f := range []struct {
		label string
		items []string
	}{
		{"Certifications", p.Certifications}, {"Skills", p.Skills},
		{"Goals", p.Goals}, {"Interests", p.Interests},
	} {
		if len(f.items) > 0 {
			parts = append(parts, f.label+": "+strings.Join(f.items, ", "))
		}
	}
	return parts
}

// appendDirectiveParts partitions directives into hard rules and preferences,
// applying workspace scoping: a "[scope:<name>]" directive only renders when
// the workspace matches (or no workspace context exists), labeled with its
// scope so the model knows where the rule comes from.
func appendDirectiveParts(parts []string, p UserProfile, workspaceDir string) []string {
	var hard, soft []string
	for _, d := range p.Directives {
		scope, text := parseDirectiveScope(d)
		if scope != "" {
			if workspaceDir != "" && !directiveMatchesWorkspace(scope, workspaceDir) {
				continue
			}
			text = "[" + scope + "] " + text
		}
		if isHardDirective(text) {
			hard = append(hard, text)
		} else {
			soft = append(soft, text)
		}
	}
	if len(hard) > 0 {
		parts = append(parts, "Directives — hard rules (MUST follow): "+strings.Join(hard, "; "))
	}
	if len(soft) > 0 {
		parts = append(parts, "Directives — preferences: "+strings.Join(soft, "; "))
	}
	return parts
}

func appendStanceParts(parts []string, p UserProfile) []string {
	if len(p.Stances) == 0 {
		return parts
	}
	entries := make([]string, 0, len(p.Stances))
	for _, s := range p.Stances {
		e := s.Position
		if s.Reason != "" {
			e += " (why: " + s.Reason + ")"
		}
		entries = append(entries, e)
	}
	return append(parts, "Stances (positions the user holds — apply their reasoning to new cases): "+strings.Join(entries, "; "))
}

func appendEnvironmentParts(parts []string, p UserProfile) []string {
	if len(p.Environment) == 0 {
		return parts
	}
	entries := make([]string, 0, len(p.Environment))
	for _, k := range sortedKeys(p.Environment) {
		entries = append(entries, k+"="+p.Environment[k])
	}
	return append(parts, "Environment: "+strings.Join(entries, ", "))
}

// appendMilestoneParts renders the most recent milestones (chronological,
// bounded) so the prompt stays lean while history remains on disk.
func appendMilestoneParts(parts []string, p UserProfile) []string {
	if len(p.Milestones) == 0 {
		return parts
	}
	ms := p.Milestones
	if len(ms) > 8 {
		ms = ms[len(ms)-8:]
	}
	entries := make([]string, 0, len(ms))
	for _, m := range ms {
		entries = append(entries, "["+m.Date.Format("2006-01-02")+"] "+m.Text)
	}
	return append(parts, "Milestones: "+strings.Join(entries, "; "))
}

func appendTopCommandParts(parts []string, p UserProfile) []string {
	if len(p.TopCommands) == 0 {
		return parts
	}
	type cmdCount struct {
		cmd   string
		count int
	}
	cmds := make([]cmdCount, 0, len(p.TopCommands))
	for c, n := range p.TopCommands {
		cmds = append(cmds, cmdCount{c, n})
	}
	sort.Slice(cmds, func(i, j int) bool { return cmds[i].count > cmds[j].count })
	limit := 5
	if len(cmds) < limit {
		limit = len(cmds)
	}
	topList := make([]string, 0, limit)
	for _, c := range cmds[:limit] {
		topList = append(topList, c.cmd)
	}
	return append(parts, "Most used: "+strings.Join(topList, ", "))
}

// appendPreferenceParts renders preferences in stable order; sensitive ones
// carry the privacy tag.
func appendPreferenceParts(parts []string, p UserProfile) []string {
	for _, k := range sortedKeys(p.Preferences) {
		label := k
		if p.FieldMeta["pref:"+k].Sensitive {
			label += " [sensitive]"
		}
		parts = append(parts, label+": "+p.Preferences[k])
	}
	return parts
}

// staleFieldsLine lists fields not re-affirmed within staleAfter, so the
// model casually re-confirms them instead of asserting old data. Fields
// written before meta tracking existed have no timestamps and are skipped —
// their age is unknown, not provably stale.
func staleFieldsLine(p UserProfile) string {
	stale := make([]string, 0, len(p.FieldMeta))
	for _, field := range sortedKeys(p.FieldMeta) {
		m := p.FieldMeta[field]
		ref := m.ConfirmedAt
		if ref.IsZero() {
			ref = m.UpdatedAt
		}
		if ref.IsZero() || time.Since(ref) < staleAfter {
			continue
		}
		stale = append(stale, strings.TrimPrefix(field, "pref:")+" ("+ref.Format("2006-01-02")+")")
	}
	if len(stale) == 0 {
		return ""
	}
	return "Possibly stale (not re-confirmed since): " + strings.Join(stale, ", ") +
		" — when naturally relevant, casually re-confirm instead of asserting."
}

// hasSensitiveMeta reports whether any field carries the privacy flag.
func hasSensitiveMeta(p UserProfile) bool {
	for _, m := range p.FieldMeta {
		if m.Sensitive {
			return true
		}
	}
	return false
}

// sortedKeys returns map keys in stable order for deterministic prompts.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- internal ---

func (ps *UserProfileStore) load() {
	data, err := readStoreFile(ps.path)
	if ps.latch.lockIfSealed(err, ps.logger, "profile") || err != nil {
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
	ps.loadedAt = nowForMerge()
	if healed {
		ps.profile.LastUpdated = time.Now()
		ps.persist()
	}
}

func (ps *UserProfileStore) persist() {
	if ps.latch.locked() {
		return // sealed file we cannot read: never overwrite it
	}
	ps.mergeFromDiskLocked()
	data, err := json.MarshalIndent(ps.profile, "", "  ")
	if err != nil {
		ps.logger.Warn("failed to marshal user profile", zap.Error(err))
		return
	}
	if err := atomicWriteFile(ps.path, data, 0o600); err != nil {
		ps.logger.Warn("failed to write user profile", zap.Error(err))
		return
	}
	ps.loadedAt = nowForMerge()
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
