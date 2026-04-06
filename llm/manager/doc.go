// Package manager provides the LLM provider manager that creates and
// caches LLM clients based on provider name and model configuration.
//
// The manager supports creating clients with server-configured credentials,
// client-forwarded API keys, and provider-specific configuration maps.
// It integrates with the auto-register registry to discover available
// providers at runtime.
package manager
