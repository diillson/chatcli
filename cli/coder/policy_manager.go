package coder

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/diillson/chatcli/utils"
	"go.uber.org/zap"
)

type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
	ActionAsk   Action = "ask"
)

type Rule struct {
	Pattern string `json:"pattern"`
	Action  Action `json:"action"`
}

type PolicyManager struct {
	Rules      []Rule `json:"rules"`
	configPath string
	localPath  string
	mergeLocal bool
	lastRule   *Rule
	logger     *zap.Logger
	mu         sync.RWMutex
}

type policyFile struct {
	Rules []Rule `json:"rules"`
	Merge bool   `json:"merge"`
}

func NewPolicyManager(logger *zap.Logger) (*PolicyManager, error) {
	home, err := utils.GetHomeDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(home, ".chatcli", "coder_policy.json")

	pm := &PolicyManager{
		Rules:      make([]Rule, 0),
		configPath: configPath,
		logger:     logger,
	}
	pm.load()
	return pm, nil
}

func (pm *PolicyManager) Check(toolName, args string) Action {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	sub, normalized := NormalizeCoderArgs(args)
	var fullCommand string
	if normalized != "" {
		fullCommand = strings.TrimSpace(fmt.Sprintf("%s %s", toolName, normalized))
	} else if sub != "" {
		fullCommand = strings.TrimSpace(fmt.Sprintf("%s %s", toolName, sub))
	} else {
		// Cannot determine subcommand — use just toolName so no allow rule
		// like "@coder read" matches. Falls through to ActionAsk (safe default).
		fullCommand = strings.TrimSpace(toolName)
	}

	var bestMatch Rule
	matched := false

	for _, rule := range pm.Rules {
		if matchesWithBoundary(fullCommand, rule.Pattern) {
			if !matched || len(rule.Pattern) > len(bestMatch.Pattern) {
				bestMatch = rule
				matched = true
			}
		}
	}

	if matched {
		pm.lastRule = &bestMatch
		return bestMatch.Action
	}

	pm.lastRule = nil
	return ActionAsk
}

func (pm *PolicyManager) AddRule(pattern string, action Action) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	pm.logger.Info("Updating security policy", zap.String("pattern", pattern), zap.String("action", string(action)))

	pattern = strings.TrimSpace(pattern)

	updated := false
	for i, rule := range pm.Rules {
		if rule.Pattern == pattern {
			pm.Rules[i].Action = action
			updated = true
			break
		}
	}

	if !updated {
		pm.Rules = append(pm.Rules, Rule{Pattern: pattern, Action: action})
	}

	pm.lastRule = nil
	return pm.save()
}

func (pm *PolicyManager) load() {
	globalPath := pm.configPath
	globalRules := []Rule{}

	if _, err := os.Stat(globalPath); os.IsNotExist(err) {
		pm.defaultRules()
		globalRules = pm.Rules
	} else {
		data, err := os.ReadFile(globalPath)
		if err != nil {
			pm.logger.Warn("Failed to read security policy", zap.Error(err))
		} else {
			var pf policyFile
			if err := json.Unmarshal(data, &pf); err != nil {
				pm.logger.Warn("Failed to parse security policy", zap.Error(err))
			} else {
				globalRules = pf.Rules
			}
		}
	}

	localPath := findLocalPolicyPath()
	if localPath == "" {
		pm.Rules = globalRules
		pm.localPath = ""
		pm.mergeLocal = false
		return
	}

	localData, err := os.ReadFile(localPath)
	if err != nil {
		pm.logger.Warn("Failed to read local security policy", zap.Error(err))
		pm.Rules = globalRules
		pm.localPath = ""
		pm.mergeLocal = false
		return
	}

	var local policyFile
	if err := json.Unmarshal(localData, &local); err != nil {
		pm.logger.Warn("Failed to parse local security policy", zap.Error(err))
		pm.Rules = globalRules
		pm.localPath = ""
		pm.mergeLocal = false
		return
	}

	if local.Merge {
		pm.Rules = mergeRules(globalRules, local.Rules)
	} else {
		pm.Rules = local.Rules
	}

	// Se existe policy local, persistir alterações nela.
	pm.configPath = localPath
	pm.localPath = localPath
	pm.mergeLocal = local.Merge
}

func (pm *PolicyManager) save() error {
	data, err := json.MarshalIndent(pm, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(pm.configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	// M1: Use restrictive permissions so only the owner can read security rules
	return os.WriteFile(pm.configPath, data, 0o600)
}

func (pm *PolicyManager) defaultRules() {
	pm.Rules = []Rule{
		{Pattern: "@coder read", Action: ActionAllow},
		{Pattern: "@coder tree", Action: ActionAllow},
		{Pattern: "@coder search", Action: ActionAllow},
		{Pattern: "@coder git-status", Action: ActionAllow},
		{Pattern: "@coder git-diff", Action: ActionAllow},
		{Pattern: "@coder git-log", Action: ActionAllow},
		{Pattern: "@coder git-changed", Action: ActionAllow},
		{Pattern: "@coder git-branch", Action: ActionAllow},
	}
	_ = pm.save()
}

func findLocalPolicyPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	path := filepath.Join(wd, "coder_policy.json")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// matchesWithBoundary checks if fullCommand starts with pattern, applying a
// word-boundary check only when the pattern ends with a word character (letter,
// digit, hyphen, underscore). This prevents "@coder read" from matching
// "@coder readlink" while still allowing path-prefix patterns like
// "@coder read --file /etc" to match "@coder read --file /etc/passwd".
func matchesWithBoundary(fullCommand, pattern string) bool {
	if !strings.HasPrefix(fullCommand, pattern) {
		return false
	}
	if len(fullCommand) == len(pattern) || len(pattern) == 0 {
		return true
	}
	// Only enforce word boundary when the pattern ends with a word character.
	// If it ends with '/', ' ', '=', etc., standard prefix matching applies.
	last := pattern[len(pattern)-1]
	isWordChar := (last >= 'a' && last <= 'z') || (last >= 'A' && last <= 'Z') ||
		(last >= '0' && last <= '9') || last == '_' || last == '-'
	if !isWordChar {
		return true
	}
	// When pattern ends with a word char, the next char must NOT be a word
	// char to avoid partial-word matches (e.g., "read" vs "readlink").
	// We allow space, '/', '=', and other non-word separators.
	next := fullCommand[len(pattern)]
	nextIsWord := (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') ||
		(next >= '0' && next <= '9') || next == '_' || next == '-'
	return !nextIsWord
}

// mergeRules combina regras globais e locais, onde a regra local com mesmo pattern
// substitui a global. Regras locais novas são adicionadas ao final.
func mergeRules(globalRules, localRules []Rule) []Rule {
	localMap := make(map[string]Action, len(localRules))
	for _, r := range localRules {
		localMap[r.Pattern] = r.Action
	}

	merged := make([]Rule, 0, len(globalRules)+len(localRules))
	for _, r := range globalRules {
		if action, ok := localMap[r.Pattern]; ok {
			merged = append(merged, Rule{Pattern: r.Pattern, Action: action})
			delete(localMap, r.Pattern)
		} else {
			merged = append(merged, r)
		}
	}

	for _, r := range localRules {
		if action, ok := localMap[r.Pattern]; ok {
			merged = append(merged, Rule{Pattern: r.Pattern, Action: action})
			delete(localMap, r.Pattern)
		}
	}

	return merged
}

func GetSuggestedPattern(toolName, args string) string {
	sub, _ := NormalizeCoderArgs(args)
	if sub != "" {
		return fmt.Sprintf("%s %s", toolName, sub)
	}
	return toolName
}

func (pm *PolicyManager) ActivePolicyPath() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.configPath
}

func (pm *PolicyManager) LocalPolicyPath() string {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.localPath
}

func (pm *PolicyManager) LocalMergeEnabled() bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return pm.mergeLocal
}

func (pm *PolicyManager) RulesCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.Rules)
}

func (pm *PolicyManager) LastMatchedRule() (Rule, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	if pm.lastRule == nil {
		return Rule{}, false
	}
	return *pm.lastRule, true
}
