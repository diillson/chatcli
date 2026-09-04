/*
 * ChatCLI - Long-term memory: multi-process reconciliation for the
 * profile, topic and project stores.
 * Copyright (c) 2024 Edilson Freitas
 * License: Apache-2.0
 *
 * Facts and episodes already reconcile with their shared file before every
 * rewrite (mergeFromDiskLocked); the profile, topics and projects were
 * last-writer-wins, so two ChatCLI processes (REPL + gateway, two
 * terminals) silently dropped each other's learning. Each store now folds
 * what the other process persisted into its own view before writing.
 * Callers hold the store's write lock.
 */
package memory

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"
)

// mergeFromDiskLocked adopts topics the other process recorded and keeps
// the stronger signal for topics both know.
func (tt *TopicTracker) mergeFromDiskLocked() {
	data, err := readStoreFile(tt.path)
	if tt.latch.lockIfSealed(err, tt.logger, "topics") || err != nil {
		return
	}
	var onDisk []Topic
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return
	}
	for i := range onDisk {
		d := onDisk[i]
		if d.Name == "" {
			continue
		}
		cur, ok := tt.topics[d.Name]
		if !ok {
			tt.topics[d.Name] = &d
			continue
		}
		if d.Mentions > cur.Mentions {
			cur.Mentions = d.Mentions
		}
		if d.LastSeen.After(cur.LastSeen) {
			cur.LastSeen = d.LastSeen
			if strings.TrimSpace(d.Summary) != "" {
				cur.Summary = d.Summary
			}
		} else if cur.Summary == "" {
			cur.Summary = d.Summary
		}
		if !d.FirstSeen.IsZero() && (cur.FirstSeen.IsZero() || d.FirstSeen.Before(cur.FirstSeen)) {
			cur.FirstSeen = d.FirstSeen
		}
		cur.RelatedFacts = unionStrings(cur.RelatedFacts, d.RelatedFacts)
	}
}

// mergeFromDiskLocked adopts projects the other process recorded; for a
// project both know, the more recently active record's scalars win and the
// lists and metadata are unioned.
func (pt *ProjectTracker) mergeFromDiskLocked() {
	data, err := readStoreFile(pt.path)
	if pt.latch.lockIfSealed(err, pt.logger, "projects") || err != nil {
		return
	}
	var onDisk []Project
	if err := json.Unmarshal(data, &onDisk); err != nil {
		return
	}
	for i := range onDisk {
		d := onDisk[i]
		key := strings.ToLower(d.Name)
		if key == "" {
			continue
		}
		cur, ok := pt.projects[key]
		if !ok {
			pt.projects[key] = &d
			continue
		}
		if d.LastActive.After(cur.LastActive) {
			cur.LastActive = d.LastActive
			if d.Description != "" {
				cur.Description = d.Description
			}
			if d.Status != "" {
				cur.Status = d.Status
			}
			if d.Path != "" {
				cur.Path = d.Path
			}
			if d.Priority != 0 {
				cur.Priority = d.Priority
			}
		} else {
			if cur.Description == "" {
				cur.Description = d.Description
			}
			if cur.Path == "" {
				cur.Path = d.Path
			}
		}
		cur.Technologies = mergeTechs(cur.Technologies, d.Technologies)
		cur.KeyFiles = unionStrings(cur.KeyFiles, d.KeyFiles)
		if len(d.Metadata) > 0 {
			if cur.Metadata == nil {
				cur.Metadata = map[string]string{}
			}
			for k, v := range d.Metadata {
				if _, exists := cur.Metadata[k]; !exists {
					cur.Metadata[k] = v
				}
			}
		}
	}
}

// mergeFromDiskLocked reconciles the profile with what another process
// persisted since this store last read the file: per field, the side with
// the fresher FieldMeta (or LastUpdated) wins; lists, maps, milestones and
// stances are unioned. A file older than the last read is skipped.
func (ps *UserProfileStore) mergeFromDiskLocked() {
	info, err := os.Stat(ps.path)
	if err != nil || !info.ModTime().After(ps.loadedAt) {
		return
	}
	data, err := readStoreFile(ps.path)
	if ps.latch.lockIfSealed(err, ps.logger, "profile") || err != nil {
		return
	}
	var d UserProfile
	if err := json.Unmarshal(data, &d); err != nil {
		return
	}
	ps.mergeProfileLocked(d)
}

// mergeProfileLocked folds profile d into the store's profile (fresher
// FieldMeta wins per scalar, lists/maps union). Caller holds the lock.
func (ps *UserProfileStore) mergeProfileLocked(d UserProfile) {
	cur := &ps.profile
	newer := func(field string) bool {
		dm, cm := d.FieldMeta[field], cur.FieldMeta[field]
		dt, ct := dm.UpdatedAt, cm.UpdatedAt
		if dt.IsZero() {
			dt = d.LastUpdated
		}
		if ct.IsZero() {
			ct = cur.LastUpdated
		}
		return dt.After(ct)
	}
	scalar := func(field string, curV *string, diskV string) {
		if diskV == "" {
			return
		}
		if *curV == "" || newer(field) {
			*curV = diskV
		}
	}
	scalar("name", &cur.Name, d.Name)
	scalar("role", &cur.Role, d.Role)
	scalar("expertise_level", &cur.ExpertiseLevel, d.ExpertiseLevel)
	scalar("preferred_language", &cur.PreferredLang, d.PreferredLang)
	scalar("communication_style", &cur.CommStyle, d.CommStyle)
	scalar("company", &cur.Company, d.Company)
	scalar("location", &cur.Location, d.Location)
	cur.Certifications = unionStringsFold(cur.Certifications, d.Certifications)
	cur.Skills = unionStringsFold(cur.Skills, d.Skills)
	cur.Goals = unionStringsFold(cur.Goals, d.Goals)
	cur.Interests = unionStringsFold(cur.Interests, d.Interests)
	cur.Directives = unionStringsFold(cur.Directives, d.Directives)
	for _, m := range d.Milestones {
		found := false
		for _, c := range cur.Milestones {
			if c.Text == m.Text && c.Date.Equal(m.Date) {
				found = true
				break
			}
		}
		if !found {
			cur.Milestones = append(cur.Milestones, m)
		}
	}
	sort.Slice(cur.Milestones, func(i, j int) bool { return cur.Milestones[i].Date.Before(cur.Milestones[j].Date) })
	for _, s := range d.Stances {
		replaced := false
		for i, c := range cur.Stances {
			if strings.EqualFold(c.Position, s.Position) {
				if s.UpdatedAt.After(c.UpdatedAt) {
					cur.Stances[i] = s
				}
				replaced = true
				break
			}
		}
		if !replaced {
			cur.Stances = append(cur.Stances, s)
		}
	}
	cur.Environment = unionMap(cur.Environment, d.Environment)
	cur.Preferences = unionMap(cur.Preferences, d.Preferences)
	if cur.TopCommands == nil {
		cur.TopCommands = map[string]int{}
	}
	for k, n := range d.TopCommands {
		if n > cur.TopCommands[k] {
			cur.TopCommands[k] = n
		}
	}
	if cur.FieldMeta == nil {
		cur.FieldMeta = map[string]FieldMeta{}
	}
	for k, m := range d.FieldMeta {
		c, ok := cur.FieldMeta[k]
		if !ok || m.UpdatedAt.After(c.UpdatedAt) {
			if ok && c.ConfirmedAt.After(m.ConfirmedAt) {
				m.ConfirmedAt = c.ConfirmedAt
			}
			cur.FieldMeta[k] = m
		}
	}
	if d.LastUpdated.After(cur.LastUpdated) {
		cur.LastUpdated = d.LastUpdated
	}
}

// unionStrings appends the items of b missing from a (exact match).
func unionStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a))
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if s != "" && !seen[s] {
			seen[s] = true
			a = append(a, s)
		}
	}
	return a
}

// unionStringsFold is unionStrings with case-insensitive, trimmed matching.
func unionStringsFold(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a))
	for _, s := range a {
		seen[strings.ToLower(strings.TrimSpace(s))] = true
	}
	for _, s := range b {
		k := strings.ToLower(strings.TrimSpace(s))
		if k != "" && !seen[k] {
			seen[k] = true
			a = append(a, s)
		}
	}
	return a
}

// unionMap fills keys of b missing from a (a's values win).
func unionMap(a, b map[string]string) map[string]string {
	if len(b) == 0 {
		return a
	}
	if a == nil {
		a = make(map[string]string, len(b))
	}
	for k, v := range b {
		if _, ok := a[k]; !ok {
			a[k] = v
		}
	}
	return a
}

// nowForMerge is the stamp a store records after reading or writing its file.
func nowForMerge() time.Time { return time.Now() }
