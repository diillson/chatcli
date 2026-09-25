// Package server provides a production-grade gRPC server for ChatCLI with
// enterprise security controls.
//
// The server acts as a centralized LLM gateway that teams can share: remote
// clients (chatcli connect), the Kubernetes AIOps operator and the
// conversation hub all speak to the same process.
//
// # Security
//
//   - JWT authentication with RBAC roles (admin, user, readonly); HS256 via
//     CHATCLI_JWT_SECRET or RS256 via CHATCLI_JWT_PUBLIC_KEY
//   - Shared bearer token authentication (CHATCLI_SERVER_TOKEN)
//   - Per-client token-bucket rate limiting
//   - SSRF prevention for caller-supplied provider endpoints
//   - gRPC field validation interceptor for all request types
//   - TLS 1.3 with optional mTLS (client CA)
//   - Hash-chained audit logging in JSON lines format
//   - Log rotation via lumberjack
//   - Loopback bind by default; a reachable bind refuses to start without
//     a credential (CHATCLI_BIND_ADDRESS)
//
// # Features
//
//   - Every LLM provider registered by the shared manager (same catalog,
//     credentials and OAuth profiles as the CLI)
//   - Unary and streaming prompt RPCs, plus an interactive bidirectional
//     session
//   - Persistent named sessions (list, load, save, delete)
//   - Remote plugin execution and download with role-based access control
//   - Agent and skill discovery for connected clients, with skill
//     auto-activation on prompts
//   - AIOps RPCs (alerts, issue analysis, agentic remediation steps) for the
//     Kubernetes operator
//   - Cross-channel conversation hub (event log, subscriptions, bindings)
//   - Prometheus metrics (gRPC, LLM, session counters)
//   - Kubernetes watcher context injection
//
// The provider fallback chain and the MCP manager wired by the server
// subcommand are reported by the status command; request routing through
// them is being unified with the CLI turn pipeline.
package server
