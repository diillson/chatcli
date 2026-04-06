// Package i18n provides internationalization support for ChatCLI with
// automatic locale detection and embedded translation files.
//
// Supported languages:
//   - Portuguese (pt-BR) — default
//   - English (en-US)
//
// Translations are embedded at compile time using Go's embed.FS directive,
// loading JSON files from the locales/ directory. The active locale is
// determined by the LANG environment variable with fallback to English.
//
// Usage:
//
//	msg := i18n.T("agent.status.thinking")
//	msgWithArgs := i18n.T("server.listening", ":50051")
package i18n
