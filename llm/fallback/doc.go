// Package fallback implements the provider fallback chain for automatic
// failover between LLM providers.
//
// When the primary provider fails (rate limit, timeout, authentication error,
// context overflow, or server error), the chain automatically tries the next
// provider with intelligent error classification and exponential backoff.
//
// Each provider in the chain has independent health tracking with configurable
// cooldown periods. The chain supports ordered priority (first = highest)
// and per-provider model overrides.
package fallback
