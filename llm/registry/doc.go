// Package registry provides the auto-register mechanism for LLM providers.
//
// Each provider package (openai, claudeai, googleai, etc.) registers itself
// via an init() function, making it automatically available to the manager
// without explicit import wiring. This enables a plugin-like architecture
// where adding a new provider only requires implementing the LLMClient
// interface and calling registry.Register in init().
package registry
