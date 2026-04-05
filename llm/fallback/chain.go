package fallback

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/i18n"
	"github.com/diillson/chatcli/llm/client"
	"github.com/diillson/chatcli/models"
	"go.uber.org/zap"
)

// ErrorClass categorizes errors for fallback decisions.
type ErrorClass int

const (
	ErrorClassUnknown ErrorClass = iota
	ErrorClassRateLimit
	ErrorClassTimeout
	ErrorClassAuth
	ErrorClassServerError
	ErrorClassModelNotFound
	ErrorClassContextTooLong
)

// String returns the human-readable name.
func (ec ErrorClass) String() string {
	switch ec {
	case ErrorClassRateLimit:
		return "rate_limit"
	case ErrorClassTimeout:
		return "timeout"
	case ErrorClassAuth:
		return "auth_error"
	case ErrorClassServerError:
		return "server_error"
	case ErrorClassModelNotFound:
		return "model_not_found"
	case ErrorClassContextTooLong:
		return "context_too_long"
	default:
		return "unknown"
	}
}

// ProviderHealth tracks the health of a provider.
type ProviderHealth struct {
	Name             string
	Available        bool
	LastError        error
	LastErrorClass   ErrorClass
	LastErrorAt      time.Time
	CooldownUntil    time.Time
	ConsecutiveFails int
}

// FallbackEntry is a provider in the fallback chain.
type FallbackEntry struct {
	Provider string
	Model    string
	Client   client.LLMClient
	Priority int // lower = higher priority
}

// Chain manages automatic failover between LLM providers.
type Chain struct {
	entries []FallbackEntry
	health  map[string]*ProviderHealth
	mu      sync.RWMutex
	logger  *zap.Logger

	// Configuration
	maxRetries     int
	cooldownBase   time.Duration
	cooldownMax    time.Duration
	cooldownFactor float64
}

// Option configures the fallback chain.
type Option func(*Chain)

// WithMaxRetries sets the maximum retry count per provider.
func WithMaxRetries(n int) Option {
	return func(c *Chain) { c.maxRetries = n }
}

// WithCooldown configures the cooldown parameters.
func WithCooldown(base, max time.Duration, factor float64) Option {
	return func(c *Chain) {
		c.cooldownBase = base
		c.cooldownMax = max
		c.cooldownFactor = factor
	}
}

// NewChain creates a fallback chain from the given entries.
func NewChain(logger *zap.Logger, entries []FallbackEntry, opts ...Option) *Chain {
	c := &Chain{
		entries:        entries,
		health:         make(map[string]*ProviderHealth),
		logger:         logger,
		maxRetries:     2,
		cooldownBase:   30 * time.Second,
		cooldownMax:    5 * time.Minute,
		cooldownFactor: 2.0,
	}
	for _, opt := range opts {
		opt(c)
	}
	for _, e := range entries {
		c.health[e.Provider] = &ProviderHealth{
			Name:      e.Provider,
			Available: true,
		}
	}
	return c
}

// SendPrompt attempts the request through the chain, falling back on failure.
func (c *Chain) SendPrompt(ctx context.Context, prompt string, history []models.Message, maxTokens int) (string, error) {
	var lastErr error

	for _, entry := range c.entries {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		if !c.isAvailable(entry.Provider) {
			c.logger.Debug(i18n.T("llm.fallback.skipping_cooldown"),
				zap.String("provider", entry.Provider))
			continue
		}

		for attempt := 0; attempt <= c.maxRetries; attempt++ {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}

			resp, err := entry.Client.SendPrompt(ctx, prompt, history, maxTokens)
			if err == nil {
				c.markSuccess(entry.Provider)
				return resp, nil
			}

			errClass := ClassifyError(err)
			c.logger.Warn(i18n.T("llm.fallback.request_failed"),
				zap.String("provider", entry.Provider),
				zap.Int("attempt", attempt+1),
				zap.String("error_class", errClass.String()),
				zap.Error(err),
			)

			// Don't retry auth errors or context-too-long
			if errClass == ErrorClassAuth || errClass == ErrorClassModelNotFound || errClass == ErrorClassContextTooLong {
				c.markFailure(entry.Provider, err, errClass)
				lastErr = err
				break
			}

			// Rate limit: wait before retry
			if errClass == ErrorClassRateLimit && attempt < c.maxRetries {
				select {
				case <-time.After(time.Duration(attempt+1) * time.Second):
				case <-ctx.Done():
					return "", ctx.Err()
				}
				continue
			}

			lastErr = err
		}

		c.markFailure(entry.Provider, lastErr, ClassifyError(lastErr))
	}

	if lastErr != nil {
		return "", fmt.Errorf("%s: %w", i18n.T("llm.fallback.all_failed"), lastErr)
	}
	return "", errors.New(i18n.T("llm.fallback.no_providers"))
}

// SendPromptWithTools attempts tool-aware requests through the chain.
func (c *Chain) SendPromptWithTools(ctx context.Context, prompt string, history []models.Message, tools []models.ToolDefinition, maxTokens int) (*models.LLMResponse, error) {
	var lastErr error

	for _, entry := range c.entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !c.isAvailable(entry.Provider) {
			continue
		}

		tac, ok := client.AsToolAware(entry.Client)
		if !ok {
			c.logger.Debug(i18n.T("llm.fallback.no_native_tools"),
				zap.String("provider", entry.Provider))
			continue
		}

		for attempt := 0; attempt <= c.maxRetries; attempt++ {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			resp, err := tac.SendPromptWithTools(ctx, prompt, history, tools, maxTokens)
			if err == nil {
				c.markSuccess(entry.Provider)
				return resp, nil
			}

			errClass := ClassifyError(err)
			if errClass == ErrorClassAuth || errClass == ErrorClassModelNotFound || errClass == ErrorClassContextTooLong {
				c.markFailure(entry.Provider, err, errClass)
				lastErr = err
				break
			}

			if errClass == ErrorClassRateLimit && attempt < c.maxRetries {
				select {
				case <-time.After(time.Duration(attempt+1) * time.Second):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			}

			lastErr = err
		}
		c.markFailure(entry.Provider, lastErr, ClassifyError(lastErr))
	}

	if lastErr != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T("llm.fallback.all_failed"), lastErr)
	}
	return nil, errors.New(i18n.T("llm.fallback.no_tool_providers"))
}

// GetHealth returns health status for all providers.
func (c *Chain) GetHealth() []ProviderHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]ProviderHealth, 0, len(c.health))
	for _, h := range c.health {
		result = append(result, *h)
	}
	return result
}

// ResetCooldowns clears all cooldowns (e.g., after credential update).
func (c *Chain) ResetCooldowns() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, h := range c.health {
		h.Available = true
		h.CooldownUntil = time.Time{}
		h.ConsecutiveFails = 0
	}
}

func (c *Chain) isAvailable(provider string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	h, ok := c.health[provider]
	if !ok {
		return false
	}
	if !h.CooldownUntil.IsZero() && time.Now().Before(h.CooldownUntil) {
		return false
	}
	return true
}

func (c *Chain) markSuccess(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.health[provider]
	h.Available = true
	h.ConsecutiveFails = 0
	h.CooldownUntil = time.Time{}
}

func (c *Chain) markFailure(provider string, err error, errClass ErrorClass) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := c.health[provider]
	h.LastError = err
	h.LastErrorClass = errClass
	h.LastErrorAt = time.Now()
	h.ConsecutiveFails++

	// Calculate cooldown with exponential backoff
	cooldown := c.cooldownBase
	for i := 1; i < h.ConsecutiveFails; i++ {
		cooldown = time.Duration(float64(cooldown) * c.cooldownFactor)
		if cooldown > c.cooldownMax {
			cooldown = c.cooldownMax
			break
		}
	}
	h.CooldownUntil = time.Now().Add(cooldown)

	// Auth errors get longer cooldown
	if errClass == ErrorClassAuth {
		h.CooldownUntil = time.Now().Add(c.cooldownMax)
	}
}

// ClassifyError categorizes an error for fallback decisions.
func ClassifyError(err error) ErrorClass {
	if err == nil {
		return ErrorClassUnknown
	}
	msg := strings.ToLower(err.Error())

	switch {
	case strings.Contains(msg, "rate limit") || strings.Contains(msg, "429") || strings.Contains(msg, "too many requests"):
		return ErrorClassRateLimit
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return ErrorClassTimeout
	case strings.Contains(msg, "401") || strings.Contains(msg, "403") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "invalid api key"):
		return ErrorClassAuth
	case strings.Contains(msg, "500") || strings.Contains(msg, "502") || strings.Contains(msg, "503") || strings.Contains(msg, "internal server error") || strings.Contains(msg, "service unavailable"):
		return ErrorClassServerError
	case strings.Contains(msg, "model not found") || strings.Contains(msg, "404"):
		return ErrorClassModelNotFound
	case strings.Contains(msg, "context length") || strings.Contains(msg, "too long") || strings.Contains(msg, "max tokens"):
		return ErrorClassContextTooLong
	default:
		return ErrorClassUnknown
	}
}

// GetModelName returns the model name of the first available provider.
func (c *Chain) GetModelName() string {
	for _, e := range c.entries {
		if c.isAvailable(e.Provider) {
			return e.Client.GetModelName()
		}
	}
	if len(c.entries) > 0 {
		return c.entries[0].Client.GetModelName()
	}
	return "unknown"
}
