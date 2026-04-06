// Package cli implements the interactive terminal interface for ChatCLI,
// built on Bubble Tea (Charmbracelet) with a rich TUI experience.
//
// # Architecture
//
// The CLI operates in three primary modes:
//
//   - Interactive mode: Full TUI with streaming output, syntax highlighting
//     via glamour, and context-aware input with @file, @git, @env, @command.
//   - Agent mode: ReAct engine (Reason + Act) with 12 specialized agents
//     running in parallel — File, Coder, Shell, Git, Search, Planner,
//     Reviewer, Tester, Refactor, Diagnostics, Formatter, Deps.
//   - One-shot mode: Non-interactive prompt via -p flag with pipe support.
//
// # Security
//
//   - Command allowlist with 150+ pre-approved commands (strict/permissive modes)
//   - Sensitive read path blocking (SSH keys, cloud credentials, kubeconfig)
//   - Environment variable redaction before LLM submission
//   - Command output sanitization with prompt injection detection
//   - Ed25519 plugin signature verification
//   - Session encryption at rest (AES-256-GCM)
//   - History file sensitive content redaction
//
// # Key Components
//
//   - ChatCLI: Main struct managing the TUI lifecycle and LLM interaction
//   - AgentMode: ReAct loop with multi-agent orchestration
//   - SessionManager: Persistent session storage with encryption and TTL
//   - HistoryManager: Command history with sensitive content redaction
//   - EnvRedactor: Environment variable sanitization (60+ patterns)
package cli
