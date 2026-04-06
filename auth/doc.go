// Package auth provides OAuth authentication flows and secure credential
// storage for ChatCLI's multi-provider LLM integration.
//
// # Supported Flows
//
//   - OAuth PKCE with local callback (OpenAI Codex, Anthropic)
//   - Device Flow (RFC 8628) for GitHub Copilot
//   - Token-based authentication for direct API key usage
//
// # Security
//
//   - AES-256-GCM encryption for all stored credentials
//   - Encryption key stored at ~/.chatcli/.auth-key with 0600 permissions
//   - Cross-platform OS keychain integration (macOS Keychain, Linux secret-tool)
//   - Token validation with expiry checking and automatic refresh
//   - Constant-time token comparison to prevent timing attacks
//
// # Storage
//
// Credentials are stored in ~/.chatcli/auth-profiles.json, encrypted with
// AES-256-GCM using a randomly generated 32-byte key. The key itself is
// stored in ~/.chatcli/.auth-key with restrictive file permissions (0600).
package auth
