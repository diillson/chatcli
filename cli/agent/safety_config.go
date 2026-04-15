package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
)

// SafetyConfig holds configurable safety rules.
type SafetyConfig struct {
	Version           int           `json:"version"`
	DenyPatterns      []PatternRule `json:"deny_patterns,omitempty"`
	AllowPatterns     []PatternRule `json:"allow_patterns,omitempty"`
	WorkspaceBoundary string        `json:"workspace_boundary,omitempty"`
	AllowSudo         bool          `json:"allow_sudo"`
	MaxOutputBytes    int64         `json:"max_output_bytes,omitempty"`
}

// PatternRule is a configurable deny/allow pattern.
type PatternRule struct {
	Pattern     string         `json:"pattern"`
	Description string         `json:"description"`
	Severity    string         `json:"severity"` // "critical", "high", "medium", "low"
	compiled    *regexp.Regexp `json:"-"`
}

// Compile compiles the regex pattern.
func (pr *PatternRule) Compile() error {
	if pr.compiled != nil {
		return nil
	}
	r, err := regexp.Compile(pr.Pattern)
	if err != nil {
		return fmt.Errorf("compiling pattern %q: %w", pr.Pattern, err)
	}
	pr.compiled = r
	return nil
}

// Matches checks if the command matches this pattern.
func (pr *PatternRule) Matches(command string) bool {
	if pr.compiled == nil {
		if err := pr.Compile(); err != nil {
			return false
		}
	}
	return pr.compiled.MatchString(command)
}

// ValidationResult is the detailed result of command validation.
type ValidationResult struct {
	Allowed     bool
	Reason      string
	Severity    string
	Suggestions []string
	MatchedRule string
}

// DefaultSafetyConfig returns sensible defaults.
func DefaultSafetyConfig() *SafetyConfig {
	return &SafetyConfig{
		Version:        1,
		MaxOutputBytes: 1 << 20, // 1MB
		DenyPatterns: []PatternRule{
			{Pattern: `rm\s+-rf\s+/[^.]`, Description: "Recursive delete of root paths", Severity: "critical"},
			{Pattern: `mkfs\.`, Description: "Filesystem format commands", Severity: "critical"},
			{Pattern: `dd\s+if=`, Description: "Direct disk access", Severity: "critical"},
			{Pattern: `>(>?)\s*/dev/[sh]d`, Description: "Direct write to block device", Severity: "critical"},
			{Pattern: `chmod\s+-R\s+777`, Description: "Recursive world-writable permissions", Severity: "high"},
			{Pattern: `curl\s.*\|\s*(ba)?sh`, Description: "Remote code execution via pipe", Severity: "critical"},
			{Pattern: `wget\s.*\|\s*(ba)?sh`, Description: "Remote code execution via pipe", Severity: "critical"},
			{Pattern: `\bshutdown\b`, Description: "System shutdown", Severity: "critical"},
			{Pattern: `\breboot\b`, Description: "System reboot", Severity: "critical"},
			{Pattern: `\binit\s+[06]\b`, Description: "System halt/reboot via init", Severity: "critical"},
		},
	}
}

// LoadSafetyConfig loads and merges global + local safety configs.
func LoadSafetyConfig(globalPath, localPath string) (*SafetyConfig, error) {
	global := DefaultSafetyConfig()

	// Load global config
	if globalPath != "" {
		if data, err := os.ReadFile(globalPath); err == nil { //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
			var cfg SafetyConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				return nil, fmt.Errorf("parsing global safety config: %w", err)
			}
			global = &cfg
		}
	}

	// Load local config
	if localPath != "" {
		if data, err := os.ReadFile(localPath); err == nil { //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
			var local SafetyConfig
			if err := json.Unmarshal(data, &local); err != nil {
				return nil, fmt.Errorf("parsing local safety config: %w", err)
			}
			return MergeSafetyConfigs(global, &local), nil
		}
	}

	// Compile all patterns
	for i := range global.DenyPatterns {
		if err := global.DenyPatterns[i].Compile(); err != nil {
			return nil, err
		}
	}
	for i := range global.AllowPatterns {
		if err := global.AllowPatterns[i].Compile(); err != nil {
			return nil, err
		}
	}

	return global, nil
}

// MergeSafetyConfigs merges local into global.
// Local can only ADD deny patterns, not remove global ones.
// Local can add allow patterns for project-specific commands.
func MergeSafetyConfigs(global, local *SafetyConfig) *SafetyConfig {
	merged := &SafetyConfig{
		Version:           global.Version,
		AllowSudo:         global.AllowSudo, // Local cannot override sudo policy
		MaxOutputBytes:    global.MaxOutputBytes,
		WorkspaceBoundary: global.WorkspaceBoundary,
	}

	// Keep all global deny patterns
	merged.DenyPatterns = make([]PatternRule, len(global.DenyPatterns))
	copy(merged.DenyPatterns, global.DenyPatterns)

	// Add local deny patterns (cannot remove global ones)
	merged.DenyPatterns = append(merged.DenyPatterns, local.DenyPatterns...)

	// Merge allow patterns (global + local)
	merged.AllowPatterns = make([]PatternRule, len(global.AllowPatterns))
	copy(merged.AllowPatterns, global.AllowPatterns)
	merged.AllowPatterns = append(merged.AllowPatterns, local.AllowPatterns...)

	// Local workspace boundary overrides global (project-specific)
	if local.WorkspaceBoundary != "" {
		merged.WorkspaceBoundary = local.WorkspaceBoundary
	}

	// Compile all patterns
	for i := range merged.DenyPatterns {
		_ = merged.DenyPatterns[i].Compile()
	}
	for i := range merged.AllowPatterns {
		_ = merged.AllowPatterns[i].Compile()
	}

	return merged
}
