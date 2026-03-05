package registry

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/diillson/chatcli/llm/client"
	"go.uber.org/zap"
)

// ProviderConfig holds configuration for creating a provider client.
type ProviderConfig struct {
	APIKey      string
	Model       string
	Logger      *zap.Logger
	MaxRetries  int
	Backoff     time.Duration
	ExtraConfig map[string]string // Provider-specific (realm, agent-id, base-url, etc.)
}

// ProviderFactory creates an LLMClient for a given configuration.
type ProviderFactory func(cfg ProviderConfig) (client.LLMClient, error)

// ProviderInfo describes a registered provider.
type ProviderInfo struct {
	Name         string
	DisplayName  string
	Factory      ProviderFactory
	EnvKeys      []string // Environment variable names for API key discovery
	RequiresAuth bool
}

// Registry is a provider registry.
type Registry struct {
	providers map[string]ProviderInfo
	mu        sync.RWMutex
}

var globalRegistry = &Registry{providers: make(map[string]ProviderInfo)}

// NewRegistry creates a new empty registry (for testing).
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]ProviderInfo)}
}

// Global returns the global registry instance.
func Global() *Registry {
	return globalRegistry
}

// Register adds a provider to the global registry. Called from init() in each provider package.
func Register(info ProviderInfo) {
	globalRegistry.Register(info)
}

// Get returns the provider info for a given name (case-insensitive).
func Get(name string) (ProviderInfo, bool) {
	return globalRegistry.Get(name)
}

// List returns all registered provider names sorted alphabetically.
func List() []string {
	return globalRegistry.List()
}

// CreateClient creates a client using the global registry.
func CreateClient(provider string, cfg ProviderConfig) (client.LLMClient, error) {
	return globalRegistry.CreateClient(provider, cfg)
}

// Register adds a provider to this registry.
func (r *Registry) Register(info ProviderInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := strings.ToUpper(info.Name)
	r.providers[key] = info
}

// Get returns the provider info by name (case-insensitive).
func (r *Registry) Get(name string) (ProviderInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.providers[strings.ToUpper(name)]
	return info, ok
}

// List returns all registered provider names sorted.
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// CreateClient creates a client using this registry.
func (r *Registry) CreateClient(provider string, cfg ProviderConfig) (client.LLMClient, error) {
	info, ok := r.Get(provider)
	if !ok {
		return nil, fmt.Errorf("provider %q not registered; available: %v", provider, r.List())
	}
	if info.RequiresAuth && cfg.APIKey == "" {
		return nil, fmt.Errorf("provider %q requires authentication; set one of: %v", provider, info.EnvKeys)
	}
	return info.Factory(cfg)
}

// ResolveAPIKeyFromEnv attempts to find an API key from the provider's known env vars.
func ResolveAPIKeyFromEnv(provider string) string {
	info, ok := Get(provider)
	if !ok {
		return ""
	}
	for _, envKey := range info.EnvKeys {
		if val := os.Getenv(envKey); val != "" {
			return val
		}
	}
	return ""
}
