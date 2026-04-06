// Package server provides a production-grade gRPC server for ChatCLI with
// enterprise security controls.
//
// The server acts as a centralized LLM gateway that teams can share,
// supporting multiple providers with automatic failover, persistent sessions,
// and Kubernetes-native observability.
//
// # Security
//
//   - JWT authentication with RBAC roles (admin, user, readonly)
//   - Legacy Bearer token authentication (backward compatible)
//   - Per-client token-bucket rate limiting
//   - SSRF prevention blocking private IPs and cloud metadata endpoints
//   - gRPC field validation interceptor for all request types
//   - TLS 1.3 with optional mTLS (mutual TLS)
//   - Structured audit logging in JSON lines format
//   - Log rotation via lumberjack
//   - Bind to localhost by default (configurable via CHATCLI_BIND_ADDRESS)
//
// # Features
//
//   - Multi-provider LLM support (11 providers)
//   - Streaming and unary prompt RPCs
//   - Interactive bidirectional sessions
//   - Remote plugin execution with role-based access control
//   - Agent and skill discovery for connected clients
//   - MCP (Model Context Protocol) integration
//   - Prometheus metrics (gRPC, LLM, session counters)
//   - Kubernetes watcher context injection
//   - Provider fallback chain with health monitoring
package server
