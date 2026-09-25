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
//   - Every LLM provider registered by the shared manager (same catalog and
//     credentials as the CLI); callers may forward their own credential
//   - Unary and real streaming prompt RPCs; responses name the provider and
//     model that answered and carry the reported token usage
//   - Provider fallback chain on the request path, with health monitoring
//   - max_tokens from the request, the provider env override or the catalog
//   - Expired server credentials refreshed once (throttled) and retried;
//     classifier refusals resent once on the sibling model
//   - Interactive bidirectional sessions
//   - Persistent named sessions (list, load, save, delete)
//   - Remote plugin execution and download with role-based access control
//   - Agent and skill discovery, with skill auto-activation on prompts
//   - AIOps RPCs (alerts, issue analysis, agentic remediation steps)
//   - Cross-channel conversation hub (event log, subscriptions, bindings)
//   - Prometheus metrics (gRPC, LLM, session counters)
//   - Kubernetes watcher context injection
//
// The MCP manager wired by the server subcommand is reported by the status
// command; its tools are not yet offered on the request path.
package server
