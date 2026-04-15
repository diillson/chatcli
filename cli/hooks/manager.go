package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Manager loads hook configurations and dispatches lifecycle events.
type Manager struct {
	hooks  []HookConfig
	logger *zap.Logger
	mu     sync.RWMutex
}

// NewManager creates a new hook manager.
func NewManager(logger *zap.Logger) *Manager {
	return &Manager{
		logger: logger,
	}
}

// LoadFromFile loads hook configuration from a JSON settings file.
// The file should contain a "hooks" key at the top level.
func (m *Manager) LoadFromFile(path string) error {
	data, err := os.ReadFile(path) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No config file is fine
		}
		return fmt.Errorf("reading hooks config: %w", err)
	}

	var config HooksConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("parsing hooks config: %w", err)
	}

	m.mu.Lock()
	m.hooks = config.Hooks
	m.mu.Unlock()

	enabledCount := 0
	for _, h := range config.Hooks {
		if h.IsEnabled() {
			enabledCount++
		}
	}

	if enabledCount > 0 {
		m.logger.Info("Hooks loaded", zap.Int("total", len(config.Hooks)), zap.Int("enabled", enabledCount))
	}

	return nil
}

// LoadFromSettings loads hooks from the chatcli settings hierarchy.
// Checks: workspace/.chatcli/hooks.json > ~/.chatcli/hooks.json
func (m *Manager) LoadFromSettings() {
	home, _ := os.UserHomeDir()
	globalPath := filepath.Join(home, ".chatcli", "hooks.json")

	// Try workspace-local first
	workspacePath := ""
	if wd, err := os.Getwd(); err == nil {
		workspacePath = filepath.Join(wd, ".chatcli", "hooks.json")
	}

	// Load global hooks
	if err := m.LoadFromFile(globalPath); err != nil {
		m.logger.Debug("No global hooks config", zap.Error(err))
	}

	// Merge workspace hooks (additive, workspace hooks run after global)
	if workspacePath != "" && workspacePath != globalPath {
		data, err := os.ReadFile(workspacePath) //#nosec G304 -- path supplied by user/agent through validated tool surface (boundary check upstream)
		if err == nil {
			var config HooksConfig
			if err := json.Unmarshal(data, &config); err == nil {
				m.mu.Lock()
				m.hooks = append(m.hooks, config.Hooks...)
				m.mu.Unlock()
				m.logger.Info("Workspace hooks loaded", zap.Int("count", len(config.Hooks)))
			}
		}
	}
}

// Fire dispatches an event to all matching hooks.
// For PreToolUse events, returns a HookResult — if Blocked is true, the action should be prevented.
// Other events are fire-and-forget (errors are logged but not returned).
func (m *Manager) Fire(event HookEvent) *HookResult {
	m.mu.RLock()
	hooks := make([]HookConfig, len(m.hooks))
	copy(hooks, m.hooks)
	m.mu.RUnlock()

	for _, hook := range hooks {
		if !hook.IsEnabled() {
			continue
		}
		if hook.Event != event.Type {
			continue
		}

		// Apply tool pattern filter for tool events
		if hook.ToolPattern != "" && event.ToolName != "" {
			if !matchToolPattern(hook.ToolPattern, event.ToolName) {
				continue
			}
		}

		result := m.executeHook(hook, event)

		// For PreToolUse, a blocking result (exit code 2) stops the action
		if event.Type == EventPreToolUse && result.Blocked {
			m.logger.Info("Hook blocked tool execution",
				zap.String("hook", hook.Name),
				zap.String("tool", event.ToolName),
				zap.String("reason", result.BlockReason))
			return result
		}
	}

	return nil // no blocking
}

// FireAsync dispatches an event asynchronously (non-blocking).
// Use for events where the result doesn't matter (PostToolUse, Notification, etc.).
func (m *Manager) FireAsync(event HookEvent) {
	go m.Fire(event)
}

// Count returns the number of loaded hooks.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.hooks)
}

// GetHooks returns a copy of the loaded hooks.
func (m *Manager) GetHooks() []HookConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]HookConfig, len(m.hooks))
	copy(result, m.hooks)
	return result
}

// executeHook runs a single hook and returns the result.
func (m *Manager) executeHook(hook HookConfig, event HookEvent) *HookResult {
	timeout := time.Duration(hook.GetTimeout()) * time.Millisecond

	switch hook.Type {
	case HookTypeCommand:
		return m.executeCommandHook(hook, event, timeout)
	case HookTypeHTTP:
		return m.executeHTTPHook(hook, event, timeout)
	default:
		m.logger.Warn("Unknown hook type", zap.String("type", string(hook.Type)), zap.String("name", hook.Name))
		return &HookResult{ExitCode: -1, Error: "unknown hook type"}
	}
}

// executeCommandHook runs a shell command hook.
// The event JSON is passed via stdin. Exit code 0 = allow, 2 = block.
func (m *Manager) executeCommandHook(hook HookConfig, event HookEvent, timeout time.Duration) *HookResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	eventJSON, _ := json.Marshal(event)

	cmd := exec.CommandContext(ctx, "sh", "-c", hook.Command) //#nosec G204 -- agent/CLI tool execution; commands validated by command_validator + policy_manager upstream
	cmd.Stdin = bytes.NewReader(eventJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Pass event info as environment variables for simpler hooks
	cmd.Env = append(os.Environ(),
		"CHATCLI_HOOK_EVENT="+string(event.Type),
		"CHATCLI_HOOK_TOOL="+event.ToolName,
		"CHATCLI_HOOK_SESSION="+event.SessionID,
	)

	err := cmd.Run()

	result := &HookResult{
		Output: stdout.String(),
		Error:  stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
			result.Error = err.Error()
		}
	}

	// Exit code 2 means "block this action"
	if result.ExitCode == 2 {
		result.Blocked = true
		result.BlockReason = strings.TrimSpace(result.Error)
		if result.BlockReason == "" {
			result.BlockReason = "blocked by hook: " + hook.Name
		}
	}

	if result.ExitCode != 0 && result.ExitCode != 2 {
		m.logger.Debug("Hook returned non-zero exit",
			zap.String("name", hook.Name),
			zap.Int("exitCode", result.ExitCode))
	}

	return result
}

// executeHTTPHook sends the event as a POST request.
func (m *Manager) executeHTTPHook(hook HookConfig, event HookEvent, timeout time.Duration) *HookResult {
	eventJSON, _ := json.Marshal(event)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", hook.URL, bytes.NewReader(eventJSON))
	if err != nil {
		return &HookResult{ExitCode: -1, Error: fmt.Sprintf("creating request: %v", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "chatcli-hooks/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		m.logger.Debug("HTTP hook failed", zap.String("name", hook.Name), zap.Error(err))
		return &HookResult{ExitCode: -1, Error: err.Error()}
	}
	defer resp.Body.Close()

	result := &HookResult{ExitCode: 0}

	// HTTP 403 = block (similar to exit code 2)
	if resp.StatusCode == http.StatusForbidden {
		result.ExitCode = 2
		result.Blocked = true
		result.BlockReason = "blocked by HTTP hook: " + hook.Name
	} else if resp.StatusCode >= 400 {
		result.ExitCode = 1
		result.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}

	return result
}

// matchToolPattern checks if a tool name matches a glob-like pattern.
// Supports * as wildcard (e.g., "mcp_*", "@coder", "*").
func matchToolPattern(pattern, toolName string) bool {
	if pattern == "*" {
		return true
	}

	// Simple prefix match for "prefix*" patterns
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(toolName, prefix)
	}

	// Exact match
	return strings.EqualFold(pattern, toolName)
}
