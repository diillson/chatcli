/*
 * ChatCLI - Long-term memory: export, import and recall explanations.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * The memory stores are a user's accumulated context; they must be able
 * to leave one machine and land on another, or be backed up, without
 * reaching into ~/.chatcli by hand. Export writes one JSON record per
 * line (facts, episodes, topics, projects, profile), sealed line by line
 * when encryption at rest is on; Import merges such a file into the live
 * stores through the same paths a session uses, so nothing bypasses
 * dedup, tombstones or confidence rules.
 */
package memory

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/diillson/chatcli/pkg/atrest"
)

// exportSealedPrefix marks a sealed export line (same scheme as the
// transcript journal).
const exportSealedPrefix = "enc:"

// ExportRecord is one JSONL line of a memory export.
type ExportRecord struct {
	Kind    string       `json:"kind"` // fact | episode | topic | project | profile | header
	Version int          `json:"version,omitempty"`
	At      time.Time    `json:"at,omitempty"`
	Fact    *Fact        `json:"fact,omitempty"`
	Episode *Episode     `json:"episode,omitempty"`
	Topic   *Topic       `json:"topic,omitempty"`
	Project *Project     `json:"project,omitempty"`
	Profile *UserProfile `json:"profile,omitempty"`
}

// ExportReport counts what an export wrote.
type ExportReport struct {
	Facts, Episodes, Topics, Projects int
	Profile                           bool
	Sealed                            bool
}

// Total is the number of data records.
func (r ExportReport) Total() int {
	n := r.Facts + r.Episodes + r.Topics + r.Projects
	if r.Profile {
		n++
	}
	return n
}

// Export writes every store as JSONL to w.
func (m *Manager) Export(w io.Writer) (ExportReport, error) {
	var rep ExportReport
	rep.Sealed = atrest.Enabled()
	bw := bufio.NewWriter(w)
	write := func(rec ExportRecord) error {
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if rep.Sealed {
			sealed, err := atrest.Seal(line)
			if err != nil {
				return err
			}
			line = []byte(exportSealedPrefix + base64.StdEncoding.EncodeToString(sealed))
		}
		if _, err := bw.Write(line); err != nil {
			return err
		}
		return bw.WriteByte('\n')
	}
	if err := write(ExportRecord{Kind: "header", Version: 1, At: time.Now()}); err != nil {
		return rep, err
	}
	if m.Profile != nil && !m.Profile.IsEmpty() {
		p := m.Profile.Get()
		if err := write(ExportRecord{Kind: "profile", Profile: &p}); err != nil {
			return rep, err
		}
		rep.Profile = true
	}
	if m.Facts != nil {
		facts := m.Facts.GetAll()
		sort.Slice(facts, func(i, j int) bool { return facts[i].CreatedAt.Before(facts[j].CreatedAt) })
		for _, f := range facts {
			if err := write(ExportRecord{Kind: "fact", Fact: f}); err != nil {
				return rep, err
			}
			rep.Facts++
		}
	}
	if m.Episodes != nil {
		for _, e := range m.Episodes.Range(time.Time{}, time.Time{}, "", "", 0) {
			if err := write(ExportRecord{Kind: "episode", Episode: e}); err != nil {
				return rep, err
			}
			rep.Episodes++
		}
	}
	if m.Topics != nil {
		for _, t := range m.Topics.GetAll() {
			t := t
			if err := write(ExportRecord{Kind: "topic", Topic: &t}); err != nil {
				return rep, err
			}
			rep.Topics++
		}
	}
	if m.Projects != nil {
		for _, p := range m.Projects.GetAll() {
			p := p
			if err := write(ExportRecord{Kind: "project", Project: &p}); err != nil {
				return rep, err
			}
			rep.Projects++
		}
	}
	return rep, bw.Flush()
}

// ExportToFile writes the export to path (0600), creating parent dirs.
func (m *Manager) ExportToFile(path string) (ExportReport, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return ExportReport{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- user-chosen export path
	if err != nil {
		return ExportReport{}, err
	}
	rep, err := m.Export(f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return rep, err
}

// ImportReport counts what an import merged.
type ImportReport struct {
	Facts, FactsSkipped, Episodes, EpisodesSkipped, Topics, Projects int
	Profile                                                          bool
	Lines                                                            int
}

// Total is the number of records merged.
func (r ImportReport) Total() int {
	n := r.Facts + r.Episodes + r.Topics + r.Projects
	if r.Profile {
		n++
	}
	return n
}

// ErrSealedImport marks a sealed export read without the key.
var ErrSealedImport = errors.New("memory import: file is sealed and no CHATCLI_ENCRYPTION_KEY is set")

// Import merges a JSONL export into the stores. Facts keep their ids
// (an existing fact keeps the stronger metadata), episodes dedup by
// content, topics and projects merge like the multi-process path, and
// the profile merges field by field. Nothing is deleted.
func (m *Manager) Import(r io.Reader) (ImportReport, error) {
	var rep ImportReport
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	var facts []*Fact
	for sc.Scan() {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		rep.Lines++
		if strings.HasPrefix(string(line), exportSealedPrefix) {
			if !atrest.Enabled() {
				return rep, ErrSealedImport
			}
			raw, err := base64.StdEncoding.DecodeString(string(line[len(exportSealedPrefix):]))
			if err != nil {
				return rep, fmt.Errorf("memory import: corrupt sealed line %d: %w", rep.Lines, err)
			}
			if line, err = atrest.Open(raw); err != nil {
				return rep, fmt.Errorf("memory import: line %d: %w", rep.Lines, err)
			}
		}
		var rec ExportRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			return rep, fmt.Errorf("memory import: corrupt line %d: %w", rep.Lines, err)
		}
		switch rec.Kind {
		case "fact":
			if rec.Fact != nil {
				facts = append(facts, rec.Fact)
			}
		case "episode":
			if rec.Episode != nil && m.Episodes != nil {
				if m.Episodes.Add(*rec.Episode) {
					rep.Episodes++
				} else {
					rep.EpisodesSkipped++
				}
			}
		case "topic":
			if rec.Topic != nil && m.Topics != nil {
				m.Topics.MergeTopic(*rec.Topic)
				rep.Topics++
			}
		case "project":
			if rec.Project != nil && m.Projects != nil {
				m.Projects.MergeProject(*rec.Project)
				rep.Projects++
			}
		case "profile":
			if rec.Profile != nil && m.Profile != nil {
				m.Profile.MergeProfile(*rec.Profile)
				rep.Profile = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return rep, err
	}
	if m.Facts != nil && len(facts) > 0 {
		added, skipped := m.Facts.ImportFacts(facts)
		rep.Facts, rep.FactsSkipped = added, skipped
	}
	return rep, nil
}

// ImportFromFile merges the export at path.
func (m *Manager) ImportFromFile(path string) (ImportReport, error) {
	f, err := os.Open(path) // #nosec G304 -- user-chosen import path
	if err != nil {
		return ImportReport{}, err
	}
	defer func() { _ = f.Close() }()
	return m.Import(f)
}

// ImportFacts merges exported facts: unknown ids are adopted with their
// metadata intact, known ids keep the stronger access/confidence data.
// Returns how many were added and how many were already known.
func (fi *FactIndex) ImportFacts(facts []*Fact) (added, known int) {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	fi.mergeFromDiskLocked()
	for _, f := range facts {
		if f == nil || strings.TrimSpace(f.Content) == "" {
			continue
		}
		if f.ID == "" {
			f.ID = fi.hashContent(f.Content)
		}
		if ts, dead := fi.tombstones[f.ID]; dead && ts.After(f.CreatedAt) {
			known++
			continue
		}
		cur, ok := fi.facts[f.ID]
		if !ok {
			if f.CreatedAt.IsZero() {
				f.CreatedAt = time.Now()
			}
			if f.Category == "" {
				f.Category = "general"
			}
			fi.facts[f.ID] = f
			added++
			continue
		}
		known++
		if f.LastAccessed.After(cur.LastAccessed) {
			cur.LastAccessed = f.LastAccessed
		}
		if f.AccessCount > cur.AccessCount {
			cur.AccessCount = f.AccessCount
		}
		if f.Confidence > cur.Confidence {
			cur.Confidence = f.Confidence
		}
		if cur.SourceProject == "" {
			cur.SourceProject = f.SourceProject
		}
	}
	if excess := len(fi.facts) - fi.config.MaxFactsCount; excess > 0 {
		fi.pruneLowestLocked(excess)
	}
	if added > 0 {
		fi.notifyChanged()
	}
	fi.persistLocked()
	return added, known
}

// MergeTopic folds one topic record in (import path).
func (tt *TopicTracker) MergeTopic(t Topic) {
	if strings.TrimSpace(t.Name) == "" {
		return
	}
	tt.mu.Lock()
	defer tt.mu.Unlock()
	cur, ok := tt.topics[t.Name]
	if !ok {
		c := t
		tt.topics[t.Name] = &c
	} else {
		if t.Mentions > cur.Mentions {
			cur.Mentions = t.Mentions
		}
		if t.LastSeen.After(cur.LastSeen) {
			cur.LastSeen = t.LastSeen
			if t.Summary != "" {
				cur.Summary = t.Summary
			}
		}
		if !t.FirstSeen.IsZero() && (cur.FirstSeen.IsZero() || t.FirstSeen.Before(cur.FirstSeen)) {
			cur.FirstSeen = t.FirstSeen
		}
		cur.RelatedFacts = unionStrings(cur.RelatedFacts, t.RelatedFacts)
	}
	tt.notifyChanged()
	tt.persist()
}

// MergeProject folds one project record in (import path).
func (pt *ProjectTracker) MergeProject(p Project) {
	key := strings.ToLower(strings.TrimSpace(p.Name))
	if key == "" {
		return
	}
	pt.mu.Lock()
	defer pt.mu.Unlock()
	cur, ok := pt.projects[key]
	if !ok {
		c := p
		pt.projects[key] = &c
	} else {
		if p.LastActive.After(cur.LastActive) {
			cur.LastActive = p.LastActive
			if p.Description != "" {
				cur.Description = p.Description
			}
			if p.Status != "" {
				cur.Status = p.Status
			}
		}
		if cur.Path == "" {
			cur.Path = p.Path
		}
		cur.Technologies = mergeTechs(cur.Technologies, p.Technologies)
		cur.KeyFiles = unionStrings(cur.KeyFiles, p.KeyFiles)
		cur.Metadata = unionMap(cur.Metadata, p.Metadata)
	}
	pt.notifyChanged()
	pt.persist()
}

// MergeProfile folds an exported profile in, field by field (fresher
// FieldMeta wins, lists union).
func (ps *UserProfileStore) MergeProfile(p UserProfile) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.mergeProfileLocked(p)
	ps.notifyChanged()
	ps.persist()
}

// RankedFact is a recalled fact with the signals that ranked it, so the
// UI can say why it was recalled.
type RankedFact struct {
	Fact     *Fact
	Lexical  float64 // raw keyword relevance (0 = no keyword hit)
	Semantic float64 // raw cosine (0 = no vector hit)
	Temporal float64 // recency/reinforcement score
	Final    float64 // fused score used for ordering
}

// Why renders the reason compactly: which signals fired and the fact's source.
func (r RankedFact) Why() string {
	parts := make([]string, 0, 4)
	if r.Lexical > 0 {
		parts = append(parts, fmt.Sprintf("keywords %.2f", r.Lexical))
	}
	if r.Semantic > 0 {
		parts = append(parts, fmt.Sprintf("semantic %.2f", r.Semantic))
	}
	parts = append(parts, fmt.Sprintf("recency %.2f", r.Temporal))
	if r.Fact != nil {
		if r.Fact.AccessCount > 0 {
			parts = append(parts, fmt.Sprintf("used %d×", r.Fact.AccessCount))
		}
		if src := strings.TrimSpace(r.Fact.Provenance); src != "" {
			parts = append(parts, "via "+src)
		} else if r.Fact.Source != "" {
			parts = append(parts, "via "+r.Fact.Source)
		}
	}
	return strings.Join(parts, " · ")
}

// SearchBlendedMinRanked is SearchBlendedMin returning the ranking signals.
func (fi *FactIndex) SearchBlendedMinRanked(keywords []string, semantic map[string]float64, w RankWeights, minLexical float64) []RankedFact {
	fi.mu.RLock()
	defer fi.mu.RUnlock()
	list := fi.blendedCandidatesLocked(keywords, semantic, w, minLexical)
	out := make([]RankedFact, len(list))
	for i, c := range list {
		out[i] = RankedFact{Fact: c.fact, Lexical: c.lexical, Semantic: c.semantic, Temporal: c.temporal, Final: c.final}
	}
	return out
}

// ProjectLabel renders a fact's source project for humans: "~/…" under
// the home directory, the base name elsewhere, empty for the workspace
// itself (or any directory inside it — a git root and its packages are
// one project).
func ProjectLabel(sourceProject, workspaceDir string) string {
	if sourceProject == "" || SameProject(sourceProject, workspaceDir) {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, sourceProject); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.Base(sourceProject)
}

// SameProject reports whether two paths belong to the same project: equal,
// or one inside the other (a package directory under the git root).
func SameProject(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(a, b+sep) || strings.HasPrefix(b, a+sep)
}
