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
	logger     *zap.Logger
	mu         sync.RWMutex
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

	sub := extractCoderSubcommand(args)
	var fullCommand string
	if sub != "" {
		fullCommand = strings.TrimSpace(fmt.Sprintf("%s %s", toolName, sub))
	} else {
		fullCommand = strings.TrimSpace(fmt.Sprintf("%s %s", toolName, args))
	}

	var bestMatch Rule
	matched := false

	for _, rule := range pm.Rules {
		if strings.HasPrefix(fullCommand, rule.Pattern) {
			if !matched || len(rule.Pattern) > len(bestMatch.Pattern) {
				bestMatch = rule
				matched = true
			}
		}
	}

	if matched {
		return bestMatch.Action
	}

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

	return pm.save()
}

func (pm *PolicyManager) load() {
	if _, err := os.Stat(pm.configPath); os.IsNotExist(err) {
		pm.defaultRules()
		return
	}

	data, err := os.ReadFile(pm.configPath)
	if err != nil {
		pm.logger.Warn("Failed to read security policy", zap.Error(err))
		return
	}

	if err := json.Unmarshal(data, &pm); err != nil {
		pm.logger.Warn("Failed to parse security policy", zap.Error(err))
	}
}

func (pm *PolicyManager) save() error {
	data, err := json.MarshalIndent(pm, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(pm.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(pm.configPath, data, 0644)
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

func GetSuggestedPattern(toolName, args string) string {
	sub := extractCoderSubcommand(args)
	if sub != "" {
		return fmt.Sprintf("%s %s", toolName, sub)
	}
	return toolName
}

func extractCoderSubcommand(args string) string {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return ""
	}

	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var payload any
		if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
			switch v := payload.(type) {
			case map[string]any:
				if cmd, ok := v["cmd"].(string); ok && cmd != "" {
					return cmd
				}
				if argv, ok := v["argv"].([]any); ok && len(argv) > 0 {
					if first, ok := argv[0].(string); ok {
						return first
					}
				}
			case []any:
				if len(v) > 0 {
					if first, ok := v[0].(string); ok {
						return first
					}
				}
			}
		}
	}

	argsParts := strings.Fields(trimmed)
	if len(argsParts) > 0 {
		return argsParts[0]
	}
	return ""
}
