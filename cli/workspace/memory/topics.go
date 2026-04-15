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

// TopicTracker tracks recurring topics across conversations.
type TopicTracker struct {
	topics map[string]*Topic
	mu     sync.RWMutex
	path   string
	logger *zap.Logger
}

// NewTopicTracker creates a new topic tracker.
func NewTopicTracker(memoryDir string, logger *zap.Logger) *TopicTracker {
	tt := &TopicTracker{
		topics: make(map[string]*Topic),
		path:   memoryDir + "/topics.json",
		logger: logger,
	}
	tt.load()
	return tt
}

// Record records one or more topic mentions.
func (tt *TopicTracker) Record(topicNames []string) {
	if len(topicNames) == 0 {
		return
	}

	tt.mu.Lock()
	defer tt.mu.Unlock()

	now := time.Now()
	changed := false

	for _, name := range topicNames {
		name = normalizeTopic(name)
		if name == "" {
			continue
		}

		if t, ok := tt.topics[name]; ok {
			t.Mentions++
			t.LastSeen = now
		} else {
			tt.topics[name] = &Topic{
				Name:      name,
				Mentions:  1,
				FirstSeen: now,
				LastSeen:  now,
			}
		}
		changed = true
	}

	if changed {
		tt.persist()
	}
}

// LinkFact associates a fact ID with a topic.
func (tt *TopicTracker) LinkFact(topicName string, factID string) {
	topicName = normalizeTopic(topicName)
	if topicName == "" {
		return
	}

	tt.mu.Lock()
	defer tt.mu.Unlock()

	t, ok := tt.topics[topicName]
	if !ok {
		return
	}

	for _, id := range t.RelatedFacts {
		if id == factID {
			return // already linked
		}
	}
	t.RelatedFacts = append(t.RelatedFacts, factID)
	tt.persist()
}

// GetTopTopics returns the most active topics (by recency-weighted mentions).
func (tt *TopicTracker) GetTopTopics(limit int) []Topic {
	tt.mu.RLock()
	defer tt.mu.RUnlock()

	type scored struct {
		topic Topic
		score float64
	}

	now := time.Now()
	var all []scored
	for _, t := range tt.topics {
		daysSince := now.Sub(t.LastSeen).Hours() / 24.0
		if daysSince < 0 {
			daysSince = 0
		}
		// Recency-weighted: mentions * decay
		decay := 1.0 / (1.0 + daysSince/30.0)
		score := float64(t.Mentions) * decay
		all = append(all, scored{topic: *t, score: score})
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].score > all[j].score
	})

	if limit > len(all) {
		limit = len(all)
	}

	result := make([]Topic, limit)
	for i := 0; i < limit; i++ {
		result[i] = all[i].topic
	}
	return result
}

// GetAll returns all topics.
func (tt *TopicTracker) GetAll() []Topic {
	tt.mu.RLock()
	defer tt.mu.RUnlock()

	result := make([]Topic, 0, len(tt.topics))
	for _, t := range tt.topics {
		result = append(result, *t)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Mentions > result[j].Mentions
	})
	return result
}

// FormatForPrompt returns a summary of active topics for the system prompt.
func (tt *TopicTracker) FormatForPrompt(limit int) string {
	top := tt.GetTopTopics(limit)
	if len(top) == 0 {
		return ""
	}

	var parts []string
	for _, t := range top {
		parts = append(parts, t.Name)
	}
	return "Active topics: " + strings.Join(parts, ", ")
}

// --- internal ---

func normalizeTopic(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "-•*#")
	name = strings.TrimSpace(name)
	if len(name) < 2 {
		return ""
	}
	return name
}

func (tt *TopicTracker) load() {
	data, err := os.ReadFile(tt.path)
	if err != nil {
		return
	}
	var topics []Topic
	if err := json.Unmarshal(data, &topics); err != nil {
		tt.logger.Warn("failed to parse topics", zap.Error(err))
		return
	}
	for i := range topics {
		tt.topics[topics[i].Name] = &topics[i]
	}
}

func (tt *TopicTracker) persist() {
	topics := make([]Topic, 0, len(tt.topics))
	for _, t := range tt.topics {
		topics = append(topics, *t)
	}
	sort.Slice(topics, func(i, j int) bool {
		return topics[i].Mentions > topics[j].Mentions
	})

	data, err := json.MarshalIndent(topics, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(tt.path, data, 0o600)
}
