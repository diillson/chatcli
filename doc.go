// ChatCLI is a multi-provider AI platform for terminal, server, and Kubernetes.
//
// It connects 11 LLM providers (OpenAI, Anthropic Claude, Google Gemini, xAI Grok,
// ZAI, MiniMax, GitHub Copilot, GitHub Models, StackSpot AI, Ollama, and OpenAI
// Assistants) to a unified interface with autonomous agents, native tool calling,
// automatic provider failover, and a full AIOps pipeline.
//
// # Three Modes of Operation
//
//   - Interactive CLI: Terminal-based TUI (Bubble Tea) with context injection,
//     12 specialized agents running in parallel, and tool calling.
//   - gRPC Server: Centralized server with JWT + RBAC authentication, TLS 1.3,
//     rate limiting, Prometheus metrics, MCP integration, and plugin/agent discovery.
//   - Kubernetes Operator: 17 CRDs powering an autonomous AIOps pipeline with
//     anomaly detection, AI-driven root cause analysis, 54+ automated remediation
//     actions, approval workflows, SLO monitoring, and auto-generated post-mortems.
//
// # Key Features
//
//   - Multi-provider with automatic failover and exponential backoff
//   - ReAct engine with 12 specialized agents (File, Coder, Shell, Git, Search,
//     Planner, Reviewer, Tester, Refactor, Diagnostics, Formatter, Deps)
//   - Native tool calling via OpenAI, Anthropic, Google, ZAI, and MiniMax APIs
//   - MCP (Model Context Protocol) for extending LLM capabilities
//   - OAuth PKCE + Device Flow authentication with AES-256-GCM encrypted storage
//   - Plugin system with Ed25519 signature verification
//   - Persistent contexts, bootstrap files (SOUL.md, USER.md), and long-term memory
//   - Session management with AES-256-GCM encryption at rest
//   - Cost tracking per session and provider
//   - Internationalization (Portuguese and English)
//
// # Enterprise Security
//
//   - JWT authentication with RBAC roles (admin, user, readonly)
//   - AES-256-GCM encryption for credentials and sessions
//   - TLS 1.3 enforcement with mTLS support
//   - SSRF prevention blocking private IPs and cloud metadata endpoints
//   - Per-client token-bucket rate limiting
//   - Ed25519 plugin signature verification
//   - Agent command allowlist (150+ pre-approved commands)
//   - Structured audit logging (JSON lines)
//   - Automated security scanning (govulncheck, gosec, Dependabot)
//
// # Installation
//
//	brew tap diillson/chatcli && brew install chatcli
//
// Or:
//
//	go install github.com/diillson/chatcli@latest
//
// Full documentation: https://chatcli.edilsonfreitas.com
//
// Source code: https://github.com/diillson/chatcli
package main
