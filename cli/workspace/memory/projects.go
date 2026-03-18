package memory

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// ProjectTracker tracks active projects with context.
type ProjectTracker struct {
	projects map[string]*Project
	mu       sync.RWMutex
	path     string
	logger   *zap.Logger
}

// NewProjectTracker creates a new project tracker.
func NewProjectTracker(memoryDir string, logger *zap.Logger) *ProjectTracker {
	pt := &ProjectTracker{
		projects: make(map[string]*Project),
		path:     memoryDir + "/projects.json",
		logger:   logger,
	}
	pt.load()
	return pt
}

// Upsert creates or updates a project.
func (pt *ProjectTracker) Upsert(updates map[string]string) bool {
	name := strings.TrimSpace(updates["project_name"])
	if name == "" {
		name = strings.TrimSpace(updates["name"])
	}
	if name == "" {
		return false
	}
	name = strings.ToLower(name)

	pt.mu.Lock()
	defer pt.mu.Unlock()

	p, exists := pt.projects[name]
	if !exists {
		p = &Project{
			Name:   name,
			Status: "active",
		}
		pt.projects[name] = p
	}

	changed := false
	if v := strings.TrimSpace(updates["project_path"]); v != "" && p.Path != v {
		p.Path = v
		changed = true
	}
	if v := strings.TrimSpace(updates["project_status"]); v != "" {
		normalized := normalizeStatus(v)
		if p.Status != normalized {
			p.Status = normalized
			changed = true
		}
	}
	if v := strings.TrimSpace(updates["project_description"]); v != "" && p.Description != v {
		p.Description = v
		changed = true
	}
	if v := strings.TrimSpace(updates["project_technologies"]); v != "" {
		techs := parseTechList(v)
		if !stringSliceEqual(p.Technologies, techs) {
			p.Technologies = mergeTechs(p.Technologies, techs)
			changed = true
		}
	}

	if changed || !exists {
		p.LastActive = time.Now()
		pt.persist()
	}
	return changed || !exists
}

// Touch updates the LastActive timestamp for a project.
func (pt *ProjectTracker) Touch(name string) {
	name = strings.ToLower(strings.TrimSpace(name))
	pt.mu.Lock()
	defer pt.mu.Unlock()

	if p, ok := pt.projects[name]; ok {
		p.LastActive = time.Now()
		pt.persist()
	}
}

// GetActive returns active projects sorted by last activity.
func (pt *ProjectTracker) GetActive() []Project {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	var active []Project
	for _, p := range pt.projects {
		if p.Status == "active" || p.Status == "paused" {
			active = append(active, *p)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].LastActive.After(active[j].LastActive)
	})
	return active
}

// GetAll returns all projects.
func (pt *ProjectTracker) GetAll() []Project {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	result := make([]Project, 0, len(pt.projects))
	for _, p := range pt.projects {
		result = append(result, *p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].LastActive.After(result[j].LastActive)
	})
	return result
}

// FormatForPrompt returns a summary of active projects for the system prompt.
func (pt *ProjectTracker) FormatForPrompt() string {
	active := pt.GetActive()
	if len(active) == 0 {
		return ""
	}

	var lines []string
	for _, p := range active {
		line := "- " + p.Name
		if p.Path != "" {
			line += " (" + p.Path + ")"
		}
		if p.Description != "" {
			line += ": " + p.Description
		}
		if len(p.Technologies) > 0 {
			line += " [" + strings.Join(p.Technologies, ", ") + "]"
		}
		lines = append(lines, line)
	}
	return "Active projects:\n" + strings.Join(lines, "\n")
}

// --- internal ---

func normalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "active", "ativo":
		return "active"
	case "paused", "pausado":
		return "paused"
	case "completed", "done", "concluido", "concluído":
		return "completed"
	default:
		return s
	}
}

func parseTechList(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == '|'
	})
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func mergeTechs(existing, incoming []string) []string {
	set := make(map[string]bool)
	for _, t := range existing {
		set[strings.ToLower(t)] = true
	}
	result := append([]string{}, existing...)
	for _, t := range incoming {
		if !set[strings.ToLower(t)] {
			result = append(result, t)
			set[strings.ToLower(t)] = true
		}
	}
	return result
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (pt *ProjectTracker) load() {
	data, err := os.ReadFile(pt.path)
	if err != nil {
		return
	}
	var projects []Project
	if err := json.Unmarshal(data, &projects); err != nil {
		pt.logger.Warn("failed to parse projects", zap.Error(err))
		return
	}
	for i := range projects {
		pt.projects[strings.ToLower(projects[i].Name)] = &projects[i]
	}
}

func (pt *ProjectTracker) persist() {
	projects := make([]Project, 0, len(pt.projects))
	for _, p := range pt.projects {
		projects = append(projects, *p)
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].LastActive.After(projects[j].LastActive)
	})

	data, err := json.MarshalIndent(projects, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(pt.path, data, 0o644)
}
