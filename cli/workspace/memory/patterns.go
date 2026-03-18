package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// PatternDetector tracks usage patterns, common errors, and skill evolution.
type PatternDetector struct {
	stats  UsageStats
	mu     sync.RWMutex
	path   string
	logger *zap.Logger
}

// NewPatternDetector creates a new pattern detector.
func NewPatternDetector(memoryDir string, logger *zap.Logger) *PatternDetector {
	pd := &PatternDetector{
		path:   memoryDir + "/usage_stats.json",
		logger: logger,
		stats: UsageStats{
			CommandFrequency: make(map[string]int),
			FeatureUsage:     make(map[string]int),
		},
	}
	pd.load()
	return pd
}

// RecordInteraction records a single interaction event.
func (pd *PatternDetector) RecordInteraction(event InteractionEvent) {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	pd.stats.TotalMessages++
	pd.stats.LastSession = event.Timestamp

	// Hour distribution
	hour := event.Timestamp.Hour()
	pd.stats.HourDistribution[hour]++

	// Command frequency
	if event.Command != "" {
		if pd.stats.CommandFrequency == nil {
			pd.stats.CommandFrequency = make(map[string]int)
		}
		pd.stats.CommandFrequency[event.Command]++
	}

	// Feature usage
	if event.Feature != "" {
		if pd.stats.FeatureUsage == nil {
			pd.stats.FeatureUsage = make(map[string]int)
		}
		pd.stats.FeatureUsage[event.Feature]++
	}

	// Error patterns
	if event.Error != "" {
		pd.recordError(event.Error)
	}

	pd.persist()
}

// RecordSessionStart increments session count and updates avg duration.
func (pd *PatternDetector) RecordSessionStart() {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	pd.stats.SessionCount++
	pd.stats.LastSession = time.Now()
	pd.persist()
}

// RecordSessionEnd updates average session duration.
func (pd *PatternDetector) RecordSessionEnd(duration time.Duration) {
	pd.mu.Lock()
	defer pd.mu.Unlock()

	// Incremental average
	n := float64(pd.stats.SessionCount)
	if n <= 0 {
		n = 1
	}
	pd.stats.AvgSessionSecs = pd.stats.AvgSessionSecs + (duration.Seconds()-pd.stats.AvgSessionSecs)/n
	pd.persist()
}

// GetStats returns a copy of the current stats.
func (pd *PatternDetector) GetStats() UsageStats {
	pd.mu.RLock()
	defer pd.mu.RUnlock()
	return pd.stats
}

// GetPeakHours returns the top N hours of activity.
func (pd *PatternDetector) GetPeakHours(n int) []int {
	pd.mu.RLock()
	defer pd.mu.RUnlock()

	type hourCount struct {
		hour  int
		count int
	}
	var hours []hourCount
	for h, c := range pd.stats.HourDistribution {
		if c > 0 {
			hours = append(hours, hourCount{h, c})
		}
	}
	sort.Slice(hours, func(i, j int) bool {
		return hours[i].count > hours[j].count
	})
	if n > len(hours) {
		n = len(hours)
	}
	result := make([]int, n)
	for i := 0; i < n; i++ {
		result[i] = hours[i].hour
	}
	return result
}

// GetTopCommands returns the N most used commands.
func (pd *PatternDetector) GetTopCommands(n int) []string {
	pd.mu.RLock()
	defer pd.mu.RUnlock()

	type cmdCount struct {
		cmd   string
		count int
	}
	var cmds []cmdCount
	for c, count := range pd.stats.CommandFrequency {
		cmds = append(cmds, cmdCount{c, count})
	}
	sort.Slice(cmds, func(i, j int) bool {
		return cmds[i].count > cmds[j].count
	})
	if n > len(cmds) {
		n = len(cmds)
	}
	result := make([]string, n)
	for i := 0; i < n; i++ {
		result[i] = cmds[i].cmd
	}
	return result
}

// FormatForPrompt returns a concise usage summary.
func (pd *PatternDetector) FormatForPrompt() string {
	pd.mu.RLock()
	defer pd.mu.RUnlock()

	s := pd.stats
	if s.SessionCount == 0 && s.TotalMessages == 0 {
		return ""
	}

	var parts []string

	if s.SessionCount > 0 {
		parts = append(parts, strings.Repeat("", 0)) // placeholder
		avgMin := s.AvgSessionSecs / 60.0
		if avgMin > 0 {
			parts = append(parts, formatStat("Sessions: %d (avg %.0f min)", s.SessionCount, avgMin))
		}
	}

	// Peak hours
	peakHours := pd.getPeakHoursLocked(3)
	if len(peakHours) > 0 {
		var hourStrs []string
		for _, h := range peakHours {
			hourStrs = append(hourStrs, formatStat("%02d:00", h))
		}
		parts = append(parts, "Peak hours: "+strings.Join(hourStrs, ", "))
	}

	// Top features
	if len(s.FeatureUsage) > 0 {
		type fu struct {
			f string
			c int
		}
		var features []fu
		for f, c := range s.FeatureUsage {
			features = append(features, fu{f, c})
		}
		sort.Slice(features, func(i, j int) bool {
			return features[i].c > features[j].c
		})
		limit := 3
		if len(features) < limit {
			limit = len(features)
		}
		var fList []string
		for _, f := range features[:limit] {
			fList = append(fList, f.f)
		}
		parts = append(parts, "Preferred modes: "+strings.Join(fList, ", "))
	}

	// Filter empty parts
	var filtered []string
	for _, p := range parts {
		if p != "" {
			filtered = append(filtered, p)
		}
	}

	if len(filtered) == 0 {
		return ""
	}
	return strings.Join(filtered, "\n")
}

// --- internal ---

func (pd *PatternDetector) recordError(errMsg string) {
	// Normalize: take first 100 chars
	if len(errMsg) > 100 {
		errMsg = errMsg[:100]
	}

	for i, ep := range pd.stats.CommonErrors {
		if strings.Contains(strings.ToLower(ep.Pattern), strings.ToLower(errMsg[:min(50, len(errMsg))])) {
			pd.stats.CommonErrors[i].Count++
			pd.stats.CommonErrors[i].LastSeen = time.Now()
			return
		}
	}

	pd.stats.CommonErrors = append(pd.stats.CommonErrors, ErrorPattern{
		Pattern:  errMsg,
		Count:    1,
		LastSeen: time.Now(),
	})

	// Keep max 50 error patterns
	if len(pd.stats.CommonErrors) > 50 {
		sort.Slice(pd.stats.CommonErrors, func(i, j int) bool {
			return pd.stats.CommonErrors[i].Count > pd.stats.CommonErrors[j].Count
		})
		pd.stats.CommonErrors = pd.stats.CommonErrors[:50]
	}
}

func (pd *PatternDetector) getPeakHoursLocked(n int) []int {
	type hourCount struct {
		hour  int
		count int
	}
	var hours []hourCount
	for h, c := range pd.stats.HourDistribution {
		if c > 0 {
			hours = append(hours, hourCount{h, c})
		}
	}
	sort.Slice(hours, func(i, j int) bool {
		return hours[i].count > hours[j].count
	})
	if n > len(hours) {
		n = len(hours)
	}
	result := make([]int, n)
	for i := 0; i < n; i++ {
		result[i] = hours[i].hour
	}
	return result
}

func formatStat(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}

func (pd *PatternDetector) load() {
	data, err := os.ReadFile(pd.path)
	if err != nil {
		return
	}
	var s UsageStats
	if err := json.Unmarshal(data, &s); err != nil {
		pd.logger.Warn("failed to parse usage stats", zap.Error(err))
		return
	}
	if s.CommandFrequency == nil {
		s.CommandFrequency = make(map[string]int)
	}
	if s.FeatureUsage == nil {
		s.FeatureUsage = make(map[string]int)
	}
	pd.stats = s
}

func (pd *PatternDetector) persist() {
	data, err := json.MarshalIndent(pd.stats, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(pd.path, data, 0o644)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
