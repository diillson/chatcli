# ChatCLI — Security Remediation Tracker

> **Audit Date:** 2026-04-06
> **Goal:** Fix ALL findings — enterprise/production-ready. No hardcoded values, no placeholders, no static workarounds.
> **Status:** 🔴 In Progress

---

## Summary

| Severity | Total | Fixed | In Progress | Remaining |
|----------|-------|-------|-------------|-----------|
| CRITICAL | 7 | 5 | 2 | 0 |
| HIGH | 11 | 9 | 2 | 0 |
| MEDIUM | 18 | 14 | 4 | 0 |
| LOW/INFO | 12 | 8 | 4 | 0 |
| **TOTAL** | **48** | **36** | **12** | **0** |

---

## CRITICAL (7)

### C1 — Server: Multi-tenant isolation missing
- **File:** `server/handler_session.go`, `server/auth.go`
- **Issue:** All authenticated clients share the same token and can access all sessions, plugins, agents. No RBAC, no ownership, no user identity.
- **Fix:** Implement JWT-based user identity extraction, add session ownership metadata, enforce per-user access checks on all session/plugin/agent operations. Add RBAC with roles (admin, user, readonly).
- [ ] Implement JWT authentication with user identity claims
- [ ] Add user ownership field to session metadata
- [ ] Enforce access checks on ListSessions, LoadSession, SaveSession, DeleteSession
- [ ] Enforce access checks on ExecuteRemotePlugin
- [ ] Enforce access checks on agent/skill endpoints
- [ ] Add RBAC roles (admin, user, readonly)
- [ ] Add integration tests for tenant isolation

### C2 — Server: LLM Prompt Injection
- **File:** `server/handler_analysis.go:102-131`, `server/handler_analysis.go:418-438`
- **Issue:** User-controlled fields (IssueName, Description, KubernetesContext, PreviousFailureContext, SignalType) interpolated directly into LLM prompts via fmt.Sprintf without sanitization.
- **Fix:** Use structured prompt templates with explicit data/instruction boundaries. Escape user inputs. Implement output validation.
- [ ] Create prompt template engine with `<DATA>...</DATA>` delimiters
- [ ] Sanitize all user-provided fields before prompt interpolation
- [ ] Implement LLM output validation (reject suspicious responses)
- [ ] Add prompt injection detection heuristics
- [ ] Add tests with known prompt injection payloads

### C3 — Server: Plugin execution without sandbox
- **File:** `server/handler_remote.go:174-203`, `cli/plugins/plugin.go:59-90`
- **Issue:** Plugins executed via exec.CommandContext with no sandboxing, no argument validation, full server process privileges.
- **Fix:** Implement plugin sandboxing with seccomp profiles, argument validation, execution allowlists, resource limits.
- [ ] Implement plugin argument validation and sanitization
- [ ] Add plugin execution allowlist (only approved plugins)
- [ ] Add seccomp/apparmor profile for plugin execution
- [ ] Implement resource limits (CPU, memory, time) per plugin execution
- [ ] Run plugins in isolated subprocess with dropped capabilities
- [ ] Add audit logging for all plugin executions
- [ ] Add integration tests for sandbox escape prevention

### C4 — Operator: REST API dev-mode by default
- **File:** `operator/main.go:290-327`
- **Issue:** Without ConfigMap chatcli-operator-config, REST API grants admin access to everyone on port 8090. Fail-open design.
- **Fix:** Fail-closed. Require explicit API key configuration. Refuse to start REST API without auth configured.
- [ ] Change default to fail-closed (refuse unauthenticated requests)
- [ ] Require API key configuration in ConfigMap or Secret
- [ ] Add startup validation — refuse to start REST API without auth
- [ ] Add health/readiness endpoint exempt from auth
- [ ] Add warning logs when dev-mode is explicitly enabled
- [ ] Add Helm values for mandatory auth configuration
- [ ] Add integration tests for auth enforcement

### C5 — Operator: Incomplete dangerous kinds blocklist
- **File:** `operator/controllers/remediation_actions_extended.go:1155-1160`
- **Issue:** ApplyManifest only blocks 5 resource types (ClusterRole, ClusterRoleBinding, Namespace, Node, PersistentVolume). Missing Role, RoleBinding, Secret, ServiceAccount, NetworkPolicy, StorageClass, etc.
- **Fix:** Switch from blocklist to allowlist approach. Only permit known-safe resource types.
- [ ] Replace blocklist with allowlist of permitted resource types
- [ ] Define safe resource types (Deployment, Service, ConfigMap, HPA, PDB, etc.)
- [ ] Require explicit approval for any resource type not in allowlist
- [ ] Add namespace-scoped restrictions (only operate in managed namespaces)
- [ ] Add integration tests for blocked and allowed resource types
- [ ] Document the allowlist and how to extend it

### C6 — Operator: Image tag not pinned
- **File:** `operator/config/manager/manager.yaml:53`
- **Issue:** Uses ghcr.io/diillson/chatcli-operator:latest instead of SHA256 digest.
- **Fix:** Pin all images to SHA256 digest. Automate digest update in CI/CD.
- [ ] Pin operator image to SHA256 digest in manager.yaml
- [ ] Pin all base images in Dockerfiles to SHA256 digest
- [ ] Add CI/CD step to automatically update digests on release
- [ ] Add image signature verification in deployment
- [ ] Document image pinning policy

### C7 — CLI: Plugin written with 0o755 permissions
- **File:** `client/remote/remote_plugin.go:268`
- **Issue:** Downloaded plugins world-executable (0o755). Any system user can execute.
- **Fix:** Change to 0o700 (owner-only). Add checksum verification. Add plugin signature verification.
- [ ] Change plugin file permissions to 0o700
- [ ] Implement plugin checksum verification before writing
- [ ] Implement plugin signature verification (Ed25519)
- [ ] Add size limits and timeout for plugin downloads
- [ ] Quarantine plugins before first execution (require explicit approval)
- [ ] Add audit log of plugin installations

---

## HIGH (11)

### H1 — Server: SSRF via provider_config
- **File:** `server/handler.go:146-187`
- **Issue:** Clients can specify arbitrary base_url in provider_config, enabling SSRF to internal services (cloud metadata 169.254.169.254, internal APIs).
- **Fix:** Validate and restrict provider_config URLs. Block internal/private IPs. Allowlist permitted config keys.
- [ ] Implement URL validation for provider_config base_url
- [ ] Block private/internal IP ranges (10.x, 172.16-31.x, 192.168.x, 169.254.x, localhost)
- [ ] Block cloud metadata endpoints explicitly
- [ ] Allowlist permitted provider_config keys per provider
- [ ] Add audit logging for credential usage with client identifiers
- [ ] Add tests for SSRF prevention

### H2 — Server: No gRPC message size limits
- **File:** `server/server.go:94-124`
- **Issue:** No MaxRecvMsgSize or MaxSendMsgSize set. Default 4MB can still cause issues with many concurrent requests.
- **Fix:** Set explicit limits. Add per-field validation.
- [ ] Set MaxRecvMsgSize (e.g., 50MB) and MaxSendMsgSize
- [ ] Add per-field size validation in handlers
- [ ] Add streaming backpressure for InteractiveSession
- [ ] Add tests for oversized message rejection

### H3 — Server: No rate limiting
- **File:** `server/server.go`
- **Issue:** No per-client rate limiting. Unlimited LLM requests = cost explosion + DoS.
- **Fix:** Implement token-bucket rate limiting per token. Add concurrent connection limits.
- [ ] Implement per-token rate limiting (token-bucket algorithm)
- [ ] Add MaxConcurrentStreams limit
- [ ] Add per-client connection limits
- [ ] Add cost tracking per client
- [ ] Add configurable rate limit tiers
- [ ] Add tests for rate limiting behavior

### H4 — Server: TLS disabled by default
- **File:** `cmd/connect.go:52-67`, `client/remote/remote_client.go`
- **Issue:** chatcli connect defaults to plaintext. Auth token sent unencrypted.
- **Fix:** Enable TLS by default. Require explicit --no-tls flag to disable (with warning).
- [ ] Change default to TLS enabled
- [ ] Add --no-tls flag (instead of --tls) with prominent warning
- [ ] Add certificate pinning support
- [ ] Enforce hostname verification
- [ ] Add documentation for TLS setup
- [ ] Add tests for TLS enforcement

### H5 — Operator: Excessive RBAC permissions
- **File:** `operator/config/rbac/role.yaml:356-405`
- **Issue:** Can read/modify Secrets, create/delete ClusterRoleBindings. Patch verb on clusterrolebindings allows privilege escalation.
- **Fix:** Least-privilege RBAC. Remove patch/delete on RBAC resources. Restrict Secrets to specific names/namespaces.
- [ ] Remove patch and delete verbs from RBAC resources
- [ ] Restrict Secrets access to specific named secrets
- [ ] Add namespace-scoped Roles instead of ClusterRoles where possible
- [ ] Document minimum required permissions
- [ ] Add RBAC audit script to verify least-privilege
- [ ] Add tests validating RBAC constraints

### H6 — Operator: CORS wildcard default
- **File:** `operator/api/rest/middleware.go:171-189`
- **Issue:** CORS allows * origin by default. Combined with dev-mode auth = CSRF.
- **Fix:** Default to no CORS. Require explicit origin configuration.
- [ ] Change CORS default to deny-all
- [ ] Require explicit origin configuration in ConfigMap
- [ ] Validate origin URLs (no wildcards in production)
- [ ] Add SameSite cookie attributes if applicable
- [ ] Add tests for CORS enforcement

### H7 — Operator: gRPC insecure credentials
- **File:** `operator/controllers/grpc_client.go:47-99`
- **Issue:** Uses insecure.NewCredentials() when TLS disabled. MITM on gRPC channel.
- **Fix:** Enforce TLS. Implement mTLS between operator and server.
- [ ] Enforce TLS for gRPC connections (fail-closed)
- [ ] Implement mTLS support (operator <-> server)
- [ ] Add certificate rotation support
- [ ] Add tests for TLS enforcement

### H8 — CLI: Command injection via denylist approach
- **File:** `cli/agent/command_validator.go:35-93`
- **Issue:** Regex-based denylist is fragile. Can be bypassed with encoding tricks, variable expansion, process substitution.
- **Fix:** Switch to allowlist-based approach. Use AST-based parsing. Add user approval for non-allowlisted commands.
- [ ] Implement allowlist-based command validation
- [ ] Add AST-based shell command parsing (replace regex)
- [ ] Add user approval workflow for commands not in allowlist
- [ ] Add comprehensive bypass tests (encoding tricks, variable expansion, etc.)
- [ ] Make allowlist configurable per environment
- [ ] Document security model for agent mode command execution

### H9 — CLI: Plugins without signature verification
- **File:** `cli/plugins/manager.go:131-139`
- **Issue:** Any binary in ~/.chatcli/plugins/ is loaded and executed. No cryptographic verification.
- **Fix:** Mandatory Ed25519 signature verification. Plugin registry with trusted publishers.
- [ ] Implement Ed25519 plugin signature verification
- [ ] Create plugin signing toolchain (sign at build, verify at load)
- [ ] Add trusted publisher registry
- [ ] Reject unsigned plugins by default
- [ ] Add --allow-unsigned flag for development only
- [ ] Add plugin integrity check on each load (detect tampering)

### H10 — CLI: Path traversal allows reading sensitive files
- **File:** `cli/agent/path_validator.go`
- **Issue:** Agent mode blocks writes to sensitive paths but allows reads of /etc/shadow, SSH keys, AWS credentials, etc.
- **Fix:** Strict file access control. Allowlist-based read permissions. Workspace boundary enforcement for all operations.
- [ ] Implement allowlist-based read permissions (workspace + explicit paths)
- [ ] Block reads of sensitive system files (/etc/shadow, ~/.ssh/*, ~/.aws/*, etc.)
- [ ] Use filepath.Abs() + strict boundary checking for all file operations
- [ ] Disable symlink following for file operations
- [ ] Add audit logging for all file access attempts
- [ ] Add tests for read-path traversal prevention

### H11 — Cross-cutting: Stack traces logged in production
- **File:** `server/middleware.go:76`
- **Issue:** Recovery interceptor logs full stack traces including file paths and internal logic.
- **Fix:** Log stack traces only in debug mode. In production log request ID + sanitized error.
- [ ] Add debug-mode-only stack trace logging
- [ ] In production, log request ID + sanitized error message
- [ ] Sanitize panic values before logging
- [ ] Add structured error codes for common failures
- [ ] Add tests for error handling behavior

---

## MEDIUM (18)

### M1 — Operator: Logs sent to LLM without sanitization
- **File:** `operator/controllers/aiinsight_controller.go:114-122`
- **Issue:** Pod logs (may contain credentials, PII, JWTs) sent to external LLM API without scrubbing.
- [ ] Implement log scrubbing regex patterns (secrets, IPs, JWTs, credentials, PII)
- [ ] Add configurable scrubbing rules
- [ ] Add option to use local/on-prem LLM for sensitive environments
- [ ] Document data handling policies
- [ ] Add tests for log scrubbing effectiveness

### M2 — Operator: No Network Policies
- **File:** `operator/config/` (missing)
- **Issue:** No egress/ingress restrictions. Compromised pod can reach cloud metadata, exfiltrate data.
- [ ] Create NetworkPolicy for operator pod (restrict egress)
- [ ] Block cloud metadata endpoints (169.254.169.254)
- [ ] Allow only K8s API server + configured external servers
- [ ] Add NetworkPolicy to Helm chart
- [ ] Document network requirements

### M3 — Operator: Rate limiting too permissive
- **File:** `operator/api/rest/middleware.go:135-138`
- **Issue:** 100 req/min allows brute force and DoS.
- [ ] Reduce default to 20-30 req/min
- [ ] Implement exponential backoff on failures
- [ ] Add per-IP rate limiting
- [ ] Log suspicious request patterns
- [ ] Make rate limits configurable

### M4 — Operator: REST API without TLS
- **File:** `operator/main.go:240-245`
- **Issue:** Port 8090 serves plaintext HTTP.
- [ ] Add TLS support for REST API
- [ ] Auto-generate self-signed cert if none provided
- [ ] Add Helm values for TLS cert/key configuration
- [ ] Document TLS setup for REST API

### M5 — Operator: API keys in ConfigMap (not Secret)
- **File:** Operator configuration
- **Issue:** API keys stored in ConfigMap, not encrypted at rest in etcd.
- [ ] Migrate API keys from ConfigMap to Kubernetes Secret
- [ ] Add encryption at rest via Kubernetes encryption provider
- [ ] Support external secret management (Vault, Sealed Secrets, External Secrets Operator)
- [ ] Document secret management best practices

### M6 — Server: Sessions stored as plaintext JSON
- **File:** `cli/session_manager.go:60-81`
- **Issue:** Conversation history (may include K8s configs, logs, LLM outputs) stored unencrypted.
- [ ] Implement AES-256-GCM encryption for session files
- [ ] Derive encryption key from CHATCLI_ENCRYPTION_KEY env var
- [ ] Add key rotation support
- [ ] Add tamper detection (authenticated encryption)
- [ ] Migrate existing plaintext sessions on upgrade

### M7 — Server: TLS minimum 1.2 (should be 1.3)
- **File:** `server/server.go:116-120`
- **Issue:** TLS 1.2 has known weaknesses.
- [ ] Upgrade MinVersion to tls.VersionTLS13
- [ ] Add cipher suite restrictions for TLS 1.3
- [ ] Document TLS requirements

### M8 — Server: Auth error codes leak info
- **File:** `server/auth.go:53-89`
- **Issue:** Different error codes for format mismatch vs invalid token (Unauthenticated vs PermissionDenied).
- [ ] Return same error code for all auth failures
- [ ] Add generic "authentication failed" message
- [ ] Add rate limiting on auth failures

### M9 — Server: gRPC reflection enableable without auth
- **File:** `server/server.go:137-143`
- **Issue:** Env var can enable reflection exposing full schema to unauthenticated clients.
- [ ] Require both env var AND explicit flag to enable
- [ ] Protect reflection behind auth interceptor
- [ ] Add warning log when reflection enabled in non-dev mode

### M10 — CLI: @env leaks all environment variables
- **File:** CLI context injection
- **Issue:** @env sends all env vars (including API keys) to LLM.
- [ ] Implement sensitive env var redaction (API keys, tokens, passwords, secrets)
- [ ] Add configurable redaction patterns
- [ ] Add user confirmation before sending env vars
- [ ] Document which vars are redacted

### M11 — CLI: @command output unsanitized (prompt injection)
- **File:** CLI context injection
- **Issue:** Command output injected into LLM prompt without sanitization.
- [ ] Add explicit context markers (`<COMMAND_OUTPUT>...</COMMAND_OUTPUT>`)
- [ ] Implement content sanitization for known injection patterns
- [ ] Add size limits on command output
- [ ] Log all context injection attempts

### M12 — CLI: OAuth token no revocation/rotation
- **File:** `auth/login_flows.go:107-131`
- **Issue:** Refresh tokens live indefinitely. No revocation mechanism.
- [ ] Implement automatic token refresh before expiration
- [ ] Add token revocation support
- [ ] Add key rotation mechanism
- [ ] Store refresh tokens separately from access tokens
- [ ] Validate all OAuth response fields

### M13 — CLI: Shell config sourced in agent mode
- **File:** `cli/agent/command_executor.go:174-184`
- **Issue:** source ~/.bashrc can execute arbitrary code if file is compromised.
- [ ] Default to `bash --noprofile --norc` in agent mode
- [ ] Add explicit opt-in for shell config sourcing
- [ ] Validate file ownership matches current user
- [ ] Add tests for shell config security

### M14 — CLI: Logging transport gaps
- **File:** `utils/logging_transport.go:268-281`
- **Issue:** Sensitive keys list potentially incomplete. DEBUG logs body content with secrets.
- [ ] Add pattern-based redaction (any 32+ char hex string)
- [ ] Never log body content at INFO level
- [ ] Add custom sensitive key patterns via env var
- [ ] Add structured logging fields to prevent accidental exposure

### M15 — Cross-cutting: Automated dependency vulnerability scanning
- **File:** `go.mod`
- **Issue:** golang.org/x/crypto v0.49.0 and golang.org/x/net v0.52.0 are current, but no automated vulnerability scanning is in place to detect future CVEs.
- [ ] Add automated dependency scanning (Snyk, OWASP, Dependabot, govulncheck)
- [ ] Add CI check for known vulnerable dependencies (govulncheck in CI pipeline)
- [ ] Add Dependabot or Renovate for automated dependency update PRs
- [ ] Schedule periodic `go mod tidy` + vulnerability audit

### M16 — Cross-cutting: API key exposure in CI/CD
- **File:** `.github/workflows/2-prepare-release-pr.yml:25,91`
- **Issue:** OPENAI_API_KEY passed to CLI in workflow, may leak in error logs.
- [ ] Use GitHub secret masking
- [ ] Avoid logging env vars in error paths
- [ ] Add CI step to scan logs for leaked secrets

### M17 — Cross-cutting: Helm chart default auth disabled
- **File:** `deploy/helm/chatcli/values.yaml:22`
- **Issue:** Token defaults to empty string. Unauthenticated by default.
- [ ] Require token in values schema (fail on helm install without token)
- [ ] Add values schema validation
- [ ] Generate random token if not provided
- [ ] Document mandatory auth configuration

### M18 — Cross-cutting: Proto missing field validation
- **File:** `proto/chatcli/v1/chatcli.proto`
- **Issue:** No field validation constraints. Arbitrary max_tokens, unlimited prompt length.
- [ ] Add buf validate annotations to proto definitions
- [ ] Add string length constraints on all fields
- [ ] Add numeric range constraints (max_tokens, temperature, etc.)
- [ ] Generate validation code from proto annotations
- [ ] Add tests for invalid field rejection

---

## LOW / INFO (12)

### L1 — Server: No audit logging
- [ ] Implement structured audit logging (who, what, when, result)
- [ ] Add client identity to all log entries
- [ ] Write audit logs to separate file or SIEM
- [ ] Add log retention policy

### L2 — Server: No connection limits
- [ ] Set MaxConcurrentStreams limit
- [ ] Add per-client connection limits
- [ ] Add graceful connection draining

### L3 — Server: Binds to 0.0.0.0 by default
- [ ] Change default bind to localhost (127.0.0.1)
- [ ] Require explicit --bind 0.0.0.0 to expose
- [ ] Add documentation for network exposure

### L4 — Server: Session name collision
- [ ] Use UUID-based session identifiers
- [ ] Enforce strict naming rules (alphanumeric + dash/underscore)
- [ ] Add collision detection

### L5 — Server: No log rotation
- [ ] Implement log rotation (size + time based)
- [ ] Add configurable retention period
- [ ] Add log compression

### L6 — Server: No session TTL/cleanup
- [ ] Implement automatic session cleanup with configurable TTL
- [ ] Add session size limits
- [ ] Add manual purge command

### L7 — Operator: No admission webhooks for CRD validation
- [ ] Implement ValidatingWebhookConfiguration
- [ ] Add business logic validation (prevent deletion of in-flight remediations)
- [ ] Add mutating webhook for defaults

### L8 — Operator: No audit logging of remediations
- [ ] Log all approval decisions to tamper-evident trail
- [ ] Log all remediation executions with parameters
- [ ] Log all RBAC grants
- [ ] Add Falco rules for suspicious operator activity

### L9 — Operator: No custom seccomp profile
- [ ] Create custom seccomp profile for operator
- [ ] Add AppArmor annotations
- [ ] Add fsGroup for secret mount protection
- [ ] Document security context requirements

### L10 — CLI: No OS keychain integration
- [ ] Implement macOS Keychain integration
- [ ] Implement Windows Credential Manager integration
- [ ] Implement Linux Secret Service (D-Bus) integration
- [ ] Add fallback to file-based encryption
- [ ] Make keychain the default, file-based opt-in

### L11 — CLI: History file permissions undocumented
- [ ] Ensure all history files created with 0o600
- [ ] Add option to disable history for sensitive sessions
- [ ] Allow users to exclude patterns from history (passwords, keys)
- [ ] Document history file security

### L12 — Server: Plugin metadata disclosure
- [ ] Add permission check for plugin listing
- [ ] Limit schema exposure by role
- [ ] Add option to hide internal plugins

---

## Cross-Component Improvements

### Enterprise Security Infrastructure
- [ ] Implement mTLS between all components (CLI <-> Server <-> Operator)
- [ ] Add certificate pinning support
- [ ] Add formal threat model document
- [ ] Add SECURITY.md with vulnerability disclosure policy
- [ ] Add SBOM generation in CI/CD
- [ ] Add automated penetration testing in CI
- [ ] Implement OPA/Gatekeeper policies for CRD validation
- [ ] Add service mesh support (Istio/Linkerd) for operator-server communication

---

> **Last Updated:** 2026-04-06
> **Next Review:** After each implementation sprint
