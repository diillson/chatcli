<p align="center">
  <a href="https://chatcli.edilsonfreitas.com">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

<h1 align="center">ChatCLI</h1>
<p align="center">
  <strong>Unified AI platform for terminal, gRPC server, and Kubernetes.</strong><br>
  <sub>13 providers · 14 autonomous agents · 7-pattern quality pipeline · one binary.</sub>
</p>

<div align="center">

<a href="https://github.com/diillson/chatcli/actions/workflows/1-ci.yml"><img src="https://github.com/diillson/chatcli/actions/workflows/1-ci.yml/badge.svg" alt="CI"/></a>
<a href="https://github.com/diillson/chatcli/actions/workflows/security-scan.yml"><img src="https://github.com/diillson/chatcli/actions/workflows/security-scan.yml/badge.svg" alt="Security Scan"/></a>
<a href="https://github.com/diillson/chatcli/releases"><img src="https://img.shields.io/github/v/release/diillson/chatcli" alt="Release"/></a>
<a href="https://artifacthub.io/packages/search?ts_query_web=chatcli&sort=relevance&page=1"><img src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/chatcli" alt="ArtifactHub"/></a>
<a href="https://goreportcard.com/report/github.com/diillson/chatcli"><img src="https://goreportcard.com/badge/github.com/diillson/chatcli" alt="Go Report Card"/></a>
<a href="https://pkg.go.dev/github.com/diillson/chatcli"><img src="https://pkg.go.dev/badge/github.com/diillson/chatcli.svg" alt="Go Reference"/></a>

<br>

<img src="https://img.shields.io/github/go-mod/go-version/diillson/chatcli?label=Go" alt="Go version"/>
<img src="https://img.shields.io/github/license/diillson/chatcli" alt="License"/>
<img src="https://img.shields.io/github/last-commit/diillson/chatcli" alt="Last commit"/>
<img src="https://img.shields.io/github/languages/code-size/diillson/chatcli" alt="Code size"/>
<img src="https://img.shields.io/badge/platforms-linux%20%7C%20macOS%20%7C%20windows-informational" alt="Platforms"/>
<img src="https://img.shields.io/badge/Trivy-image%20scanning-00C9A7?logo=aquasecurity" alt="Trivy"/>
<img src="https://img.shields.io/badge/Sigstore-cosign%20signed-4B32C3?logo=sigstore" alt="Cosign Signed"/>
<img src="https://img.shields.io/badge/SBOM-CycloneDX-green" alt="SBOM"/>
<img src="https://img.shields.io/badge/observability-Prometheus-E6522C?logo=prometheus" alt="Prometheus"/>

</div>

<br>

<p align="center">
  <a href="README.md">Português</a> &bull;
  <a href="https://chatcli.edilsonfreitas.com">Full documentation</a> &bull;
  <a href="#architecture">Architecture</a> &bull;
  <a href="#observability">Observability</a>
</p>

---

<p align="center">
  <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli-demo.gif" alt="ChatCLI Demo" width="800">
</p>

<br>

> **ChatCLI** connects the industry's leading LLMs to a single, extensible interface — from `chatcli -p` in your terminal to a Kubernetes operator with an autonomous AIOps pipeline, passing through a production-ready gRPC server with authentication, failover, and Prometheus metrics.

<br>

## Highlights

| | |
|---|---|
| **Multi-provider with failover** | 13 LLM providers (OpenAI · Anthropic · Bedrock · Google · xAI · ZAI · MiniMax · Copilot · GitHub Models · StackSpot · OpenRouter · Ollama · OpenAI Assistants) with intelligent error classification, exponential backoff, and per-provider cooldown. |
| **Autonomous agents** | 14 specialized workers coordinated by a ReAct engine (Reason + Act), with parallel execution and a 7-pattern quality pipeline. |
| **Quality pipeline** | Self-Refine, Chain-of-Verification (CoVe), Reflexion, RAG + HyDE, Plan-and-Solve (ReWOO), cross-provider reasoning backbone — all composed via a thread-safe state machine with circuit breakers and hot reload. |
| **Scheduler (Chronos)** | Durable scheduling with cron + wait-until + DAG + daemon mode. `/schedule`, `/wait`, `/jobs` + `@scheduler` tool for agents. CRC32 WAL, snapshots, rate limiter, circuit breakers, JSONL audit, 13 Prometheus metrics. Jobs survive crashes and CLI exit. |
| **Durable Reflexion** | WAL-backed queue with worker pool, dead letter queue, boot replay, exponential retry with jitter — lessons survive process crashes. |
| **Semantic convergence** | char → Jaccard → embedding cosine cascade for Self-Refine, with LRU/TTL cache and quality regression detection. |
| **Production-ready** | gRPC + TLS 1.3, JWT + RBAC, AES-256-GCM, rate limiting, audit logging, 50+ Prometheus metrics. |
| **Kubernetes-native** | Operator with 17 CRDs and an autonomous AIOps pipeline (54+ remediation actions), SLO monitoring, post-mortems. |
| **Extensible** | Plugins with Ed25519 signature verification, multi-registry skills (skills.sh, ClawHub, ChatCLI.dev), lifecycle hooks, MCP client (stdio + SSE). |

---

## Installation

```bash
# Homebrew (macOS / Linux)
brew tap diillson/chatcli && brew install chatcli

# Go install
go install github.com/diillson/chatcli@latest

# Pre-built, cosign-signed binaries
# https://github.com/diillson/chatcli/releases
```

<details>
<summary><strong>Build from source</strong></summary>

```bash
git clone https://github.com/diillson/chatcli.git && cd chatcli
go mod tidy && go build -o chatcli

# With version metadata injected via ldflags
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
go build -ldflags "-X github.com/diillson/chatcli/version.Version=${VERSION}" -o chatcli
```

</details>

---

## Quick Setup

```bash
LLM_PROVIDER=OPENAI    # OPENAI, CLAUDEAI, BEDROCK, GOOGLEAI, XAI, ZAI, MINIMAX,
                       # COPILOT, GITHUB_MODELS, OLLAMA, STACKSPOT, OPENROUTER
OPENAI_API_KEY=sk-xxx
```

<details>
<summary><strong>Full provider configuration reference</strong></summary>

| Provider | API Key | Model | Extras |
|---|---|---|---|
| OpenAI | `OPENAI_API_KEY` | `OPENAI_MODEL` | `OPENAI_MAX_TOKENS`, `OPENAI_USE_RESPONSES` |
| Anthropic | `ANTHROPIC_API_KEY` | `ANTHROPIC_MODEL` | `ANTHROPIC_MAX_TOKENS` |
| AWS Bedrock | IAM / Profile / credentials chain | `BEDROCK_MODEL` | `AWS_REGION`, `BEDROCK_CROSS_REGION` |
| Google Gemini | `GOOGLEAI_API_KEY` | `GOOGLEAI_MODEL` | `GOOGLEAI_MAX_TOKENS` |
| xAI | `XAI_API_KEY` | `XAI_MODEL` | `XAI_MAX_TOKENS` |
| ZAI | `ZAI_API_KEY` | `ZAI_MODEL` | `ZAI_MAX_TOKENS` |
| MiniMax | `MINIMAX_API_KEY` | `MINIMAX_MODEL` | `MINIMAX_MAX_TOKENS` |
| GitHub Copilot | `GITHUB_COPILOT_TOKEN` | `COPILOT_MODEL` | or `/auth login github-copilot` |
| GitHub Models | `GITHUB_TOKEN` | `GITHUB_MODELS_MODEL` | `GH_TOKEN`, `GITHUB_MODELS_TOKEN` |
| StackSpot | `CLIENT_ID`, `CLIENT_KEY` | — | `STACKSPOT_REALM`, `STACKSPOT_AGENT_ID` |
| OpenRouter | `OPENROUTER_API_KEY` | — | `OPENROUTER_MAX_TOKENS`, `OPENROUTER_FALLBACK_MODELS` |
| Ollama | — | `OLLAMA_MODEL` | `OLLAMA_ENABLED=true`, `OLLAMA_BASE_URL` |
| OpenAI Assistants | `OPENAI_API_KEY` | `OPENAI_ASSISTANT_MODEL` | `OPENAI_ASSISTANT_ID` |

</details>

---

## Three Modes of Operation

<table>
<tr>
<td width="33%" valign="top">

### Interactive CLI

AI-powered terminal with a Bubble Tea TUI, project context, tool calling, and autonomous agents.

```bash
chatcli
chatcli -p "Explain this repo"
git diff | chatcli -p "Summarize"
```

</td>
<td width="33%" valign="top">

### gRPC Server

Shared backend with TLS 1.3, JWT/RBAC, failover, Prometheus metrics, MCP, and plugin discovery.

```bash
chatcli server --port 50051 \
  --token my-token
chatcli connect \
  --server host:50051 \
  --token my-token
```

</td>
<td width="33%" valign="top">

### Kubernetes Operator

Autonomous AIOps pipeline with 17 CRDs, 54+ remediation actions, SLO monitoring, and post-mortems.

```bash
helm install chatcli-operator \
  oci://ghcr.io/diillson/charts/chatcli-operator \
  --namespace aiops-system \
  --create-namespace
```

</td>
</tr>
</table>

### Autonomous scheduler (Chronos)

The scheduler runs embedded in the CLI and optionally as a daemon. Jobs survive restarts via WAL + snapshot.

```bash
# Fire a command in 30s
/schedule ping --when +30s --do "/run curl https://api.example.com/health"

# Daily cron with retry
/schedule backup --cron "0 2 * * *" --do "shell: ./backup.sh" --max-retries 3

# Deploy + K8s wait + trigger smoke
/schedule deploy --when +0s --do "shell: terraform apply -auto-approve" \
  --wait "k8s:deployment/prod/api:Available" --timeout 15m \
  --triggers smoke-tests

# Daemon to keep running with the CLI closed
chatcli daemon start --detach
chatcli daemon status

# List / inspect / cancel
/jobs list
/jobs show <id>
/jobs tree
/jobs cancel <id>
```

Agents get the `@scheduler` tool and can pause themselves waiting on conditions — see [Cookbook: scheduler automation](https://chatcli.edilsonfreitas.com/en/cookbook/scheduler-automations) and the [feature doc](https://chatcli.edilsonfreitas.com/en/features/scheduler).

<details>
<summary><strong>Context commands (CLI mode)</strong></summary>

Inject environment data directly into your prompt:

| Command | Description |
|---|---|
| `@git` | Status, branches, and recent commits |
| `@file <path>` | File or directory contents |
| `@env` | Environment variables |
| `@history` | Recent shell commands |
| `@command <cmd>` | Execute a command and inject its output |

</details>

<details>
<summary><strong>Kubernetes manifest example (Instance CRD)</strong></summary>

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-prod
spec:
  provider: ZAI
  model: glm-5
  replicas: 2
  fallback:
    enabled: true
    providers:
      - name: OPENAI
        model: gpt-5.4
      - name: MINIMAX
        model: MiniMax-M2.7
```

```bash
helm install chatcli oci://ghcr.io/diillson/charts/chatcli \
  --namespace chatcli --create-namespace \
  --set llm.provider=OPENAI --set secrets.openaiApiKey=sk-xxx
```

</details>

---

## Supported Providers

> 13 providers with a unified interface. Automatic failover with intelligent error classification, cross-provider extended thinking, and prompt caching where available.

| Provider | Default Model | Tool Calling | Vision | Reasoning / Thinking |
|---|---|---|---|---|
| **OpenAI** | gpt-5.4 | Native | Yes | `reasoning_effort` (o-series / gpt-5) |
| **Anthropic (Claude)** | claude-sonnet-4-6 | Native | Yes | Extended thinking with cache |
| **AWS Bedrock** | claude-sonnet-4-6 | Native | Yes | Thinking budget (Anthropic models) |
| **Google Gemini** | gemini-2.5-flash | Native | Yes | — |
| **xAI (Grok)** | grok-4-1 | XML fallback | — | — |
| **ZAI (Zhipu AI)** | glm-5 | Native | Yes | — |
| **MiniMax** | MiniMax-M2.7 | Native | Yes | — |
| **GitHub Copilot** | gpt-4o | Native | Yes | — |
| **GitHub Models** | gpt-4o | Native | Yes | — |
| **StackSpot AI** | StackSpotAI | — | — | — |
| **OpenRouter** | openai/gpt-4o | Native | Yes | Passthrough |
| **Ollama** | (local) | XML fallback | — | `<thinking>` tag normalization |
| **OpenAI Assistants** | gpt-4o | Assistants API | — | — |

```bash
# Configurable fallback chain
CHATCLI_FALLBACK_PROVIDERS=OPENAI,CLAUDEAI,BEDROCK,ZAI,MINIMAX,OPENROUTER
```

`/thinking on|off|auto` enables extended thinking / reasoning_effort on any provider that supports it — the cross-provider mapping is automatic.

---

## Autonomous Agents

> ReAct engine (Reason + Act) with **14 specialized agents** running in parallel.

```bash
/coder "Refactor the auth module to use JWT"
chatcli -p "Create tests for the utils package" --agent-auto-exec
```

| Agent | Responsibility |
|---|---|
| **File** | File reading, writing, and manipulation |
| **Coder** | Code generation and editing |
| **Shell** | System command execution |
| **Git** | Version control operations |
| **Search** | Code and file search |
| **Planner** | Complex task decomposition (Plan-and-Solve / ReWOO) |
| **Reviewer** | Automated code review |
| **Tester** | Test generation and execution |
| **Refactor** | Safe code refactoring |
| **Diagnostics** | Problem analysis and debugging |
| **Formatter** | Formatting and linting |
| **Deps** | Dependency management |
| **Refiner** | Self-Refine post-hook (critique → revise) |
| **Verifier** | Chain-of-Verification (questions + final answer) |

Workers are coordinated by the **dispatcher** with a configurable semaphore (`CHATCLI_AGENT_MAX_WORKERS`), retry policy, and `FileLockManager` synchronization.

---

## Harness/Quality Pipeline

> Seven prompting/execution patterns composed via a pluggable pipeline with state machine, hot reload, and per-hook isolation.

| # | Pattern | Status | Opt-in |
|---|---|---|---|
| 1 | **ReAct** (Reason + Act) | ✅ agent core | — |
| 2 | **Plan-and-Solve / ReWOO** | ✅ | `/plan`, `CHATCLI_QUALITY_PLAN_FIRST_MODE` |
| 3 | **Reflexion** (with durable queue) | ✅ | on by default |
| 4 | **RAG + HyDE** | ✅ | `CHATCLI_QUALITY_HYDE_ENABLED=1` |
| 5 | **Self-Refine** (with semantic convergence) | ✅ | `CHATCLI_QUALITY_REFINE_ENABLED=1` |
| 6 | **Chain-of-Verification** (CoVe) | ✅ | `CHATCLI_QUALITY_VERIFY_ENABLED=1` |
| 7 | **Cross-provider reasoning backbone** | ✅ | `CHATCLI_QUALITY_REASONING_MODE=auto` |

### Pipeline Architecture

- **State machine** (Active → Draining → Closed) with atomic CAS transitions.
- **Copy-on-Write** via `atomic.Pointer[snapshot]` — `AddPre/AddPost/SwapConfig` are atomic, zero locks on the hot path.
- **Per-hook isolation**: panic recovery, timeout enforcement (default 30s), circuit breaker (5 failures → open for 30s).
- **Priority-based ordering** via optional `Prioritized` interface (backward-compatible — unmarked hooks default to 100).
- **Short-circuit sentinels**: `ErrSkipExecution` (cache-hit before `agent.Execute`) and `ErrSkipRemainingHooks` (ensemble patterns).
- **Graceful shutdown** via `DrainAndClose(timeout)` honoring in-flight calls.

### Durable Reflexion (WAL + DLQ)

Reflexion triggers (error, hallucination flagged by CoVe, low quality) flow through a lesson queue with enterprise guarantees — lessons survive process crashes:

- **WAL** with double CRC32, atomic rename, dir fsync — torn writes detected automatically.
- **Worker pool** (default 2) with per-job timeout, exponential backoff with jitter, configurable `MaxAttempts`.
- **Persistent DLQ** (same WAL format) with `/reflect failed`, `/reflect retry <id>`, `/reflect purge <id>`.
- **Drain-on-boot**: pending lessons from a previous session are reprocessed automatically.
- **Idempotency** via `sha256(task | trigger | attempt)` — re-triggering the same situation is a no-op.
- **Stale discard** (default 7d) — old lessons dropped at replay time.

```bash
/reflect list              # current queue + DLQ
/reflect failed            # DLQ with last error per entry
/reflect retry <job-id>    # re-queue a failed lesson
/reflect purge <job-id>    # permanently remove a DLQ entry
/reflect drain             # force WAL replay
```

### Semantic Convergence (Self-Refine)

Self-Refine uses a char → Jaccard → embedding cascade to detect when to stop iterating. Catches "same meaning, different words" that the char-level heuristic missed:

| Stage | Cost | When it fires |
|---|---|---|
| **Char** | μs | Always. Early-exit when sim > 0.99 (identical) or sim < 0.3 (diverged) |
| **Jaccard** | ms | Borderline, normalized token sets with EN/PT stop-words |
| **Embedding** | ms + $ | Borderline after Jaccard. Opt-in via `CHATCLI_QUALITY_REFINE_CONVERGENCE_EMBEDDING=1` |

- **LRU cache with TTL** (default 256 entries / 5min) avoids re-embedding identical text.
- **Per-scorer circuit breaker** — provider outage degrades to Jaccard without blocking refine.
- **Quality regression detection**: when pass N gets worse (>15% sim loss vs best) → revert to best draft + set `refine_rolled_back` metadata so Reflexion can learn.
- **Strict mode**: refuses to declare convergence without embedding when stakes are high.

<details>
<summary><strong>Full quality pipeline config</strong></summary>

```bash
# Master switch
CHATCLI_QUALITY_ENABLED=true

# Self-Refine (#5) + semantic convergence
CHATCLI_QUALITY_REFINE_ENABLED=false            # opt-in
CHATCLI_QUALITY_REFINE_MAX_PASSES=1
CHATCLI_QUALITY_REFINE_CONVERGENCE_ENABLED=true
CHATCLI_QUALITY_REFINE_CONVERGENCE_EMBEDDING=false
CHATCLI_QUALITY_REFINE_CONVERGENCE_STRICT=false

# Chain-of-Verification (#6)
CHATCLI_QUALITY_VERIFY_ENABLED=false
CHATCLI_QUALITY_VERIFY_NUM_QUESTIONS=3
CHATCLI_QUALITY_VERIFY_REWRITE=true

# Reflexion (#3) + durable queue
CHATCLI_QUALITY_REFLEXION_ENABLED=true
CHATCLI_QUALITY_REFLEXION_QUEUE_ENABLED=true    # WAL + worker pool + DLQ
CHATCLI_QUALITY_REFLEXION_QUEUE_WORKERS=2
CHATCLI_QUALITY_REFLEXION_QUEUE_MAX_ATTEMPTS=5
CHATCLI_QUALITY_REFLEXION_QUEUE_STALE_AFTER=168h

# Plan-and-Solve / ReWOO (#2)
CHATCLI_QUALITY_PLAN_FIRST_MODE=auto             # off|auto|always

# HyDE (#4)
CHATCLI_QUALITY_HYDE_ENABLED=false
CHATCLI_QUALITY_HYDE_USE_VECTORS=false

# Reasoning backbone (#7)
CHATCLI_QUALITY_REASONING_MODE=auto              # off|on|auto
CHATCLI_QUALITY_REASONING_BUDGET=8000
```

All exposed via `/config quality` with runtime state (registered hooks, queue depth, DLQ size).

</details>

---

## Observability

> End-to-end Prometheus integration in the `chatcli` namespace. 50+ metrics covering LLM, agents, pipeline, queue, and lesson queue.

```bash
chatcli server --port 50051 --metrics-port 9090
curl http://localhost:9090/metrics | grep chatcli_
curl http://localhost:9090/healthz
```

### Key metrics

| Subsystem | Metric | Type |
|---|---|---|
| `chatcli_llm_*` | `requests_total`, `request_duration_seconds`, `tokens_used_total`, `errors_total` | Counter, Histogram |
| `chatcli_quality_pipeline_*` | `dispatch_total`, `hook_duration_seconds`, `hook_errors_total`, `hook_circuit_state`, `generation` | Counter, Histogram, Gauge |
| `chatcli_lessonq_*` | `enqueue_total`, `queue_depth`, `dlq_size`, `processing_duration_seconds`, `wal_corruption_total`, `retry_total` | Counter, Gauge, Histogram |
| `chatcli_session_*` | duration, commands executed, signals | Counter, Gauge |
| `chatcli_grpc_*` | unary + stream interceptors | Counter, Histogram |

Standard Go runtime and `process_*` collectors are registered automatically.

---

## Enterprise Security

> Security is not a feature flag. It is the foundation of every layer of ChatCLI.

<table>
<tr>
<td width="50%" valign="top">

**Authentication & authorization**
- JWT with RBAC (admin / user / readonly)
- OAuth PKCE + Device Flow (RFC 8628)
- Automatic token refresh per provider

**Encryption**
- AES-256-GCM for credentials at rest
- TLS 1.3 for gRPC communication
- Encrypted session store on disk

**Network**
- Built-in SSRF prevention
- Per-client rate limiting
- Operator webhook validation

</td>
<td width="50%" valign="top">

**Plugin & agent security**
- Ed25519 plugin signature verification
- Agent command allowlist (150+ approved commands)
- Schema validation during plugin discovery

**Auditing & compliance**
- Structured audit logging (JSON Lines)
- Per-session cost tracking per provider
- Prometheus metrics for observability

**CI/CD security**
- `govulncheck` + `gosec` on every PR
- Automated Trivy image scanning
- Cosign-signed releases + CycloneDX SBOM

</td>
</tr>
</table>

<details>
<summary><strong>Built-in OAuth</strong></summary>

```
/auth login openai-codex       # OAuth PKCE + local callback
/auth login anthropic          # OAuth PKCE + manual code
/auth login github-copilot     # Device Flow (RFC 8628)
/auth status                   # All provider status
```

Credentials are stored with **AES-256-GCM** at `~/.chatcli/auth-profiles.json`.

</details>

---

## Command Reference

| Category | Commands |
|---|---|
| **Core** | `/help` · `/version` · `/reload` · `/exit` · `/reset` |
| **Sessions** | `/session {save,load,list,delete,new,fork}` · `/newsession` · `/rewind` |
| **Context** | `/context {create,attach,list,remove}` · `@git` · `@file` · `@env` · `@history` · `@command` |
| **Config** | `/config [section]` · `/status` · `/settings` · `/switch <provider\|model>` |
| **Agent mode** | `/agent [task]` · `/run` · `/coder` · `/plan [query]` |
| **Quality pipeline** | `/thinking [on\|off\|auto]` · `/refine [draft]` · `/verify [answer]` · `/reflect [list\|failed\|retry\|purge\|drain\|<text>]` |
| **Memory** | `/memory {record,list,search,clear}` · `/compact [ratio]` |
| **Extensibility** | `/mcp {init,list,invoke,config}` · `/plugin {list,load,unload}` · `/skill <name>` · `/hooks {list,enable,disable,test}` |
| **Remote** | `/auth {login,logout,status}` · `/connect <server>` · `/disconnect` |
| **Tools** | `/watch {pid\|file}` · `/worktree {create,list,remove}` · `/channel {create,switch}` · `/websearch <query>` |
| **Scheduler** | `/schedule <name> --when <t> --do <a>` · `/wait --until <cond>` · `/jobs {list,show,tree,cancel,pause,resume,logs,daemon}` · `chatcli daemon {start,stop,status,ping,install}` |
| **Diagnostics** | `/metrics` · `/cost` |

---

## Core Features

> Every feature is designed to compose with the others. Plugins discover skills. Hooks drive tools. Contexts feed agents.

| Feature | Description |
|---|---|
| **Native tool calling** | Native APIs from OpenAI, Anthropic, Bedrock, Google, ZAI, MiniMax, OpenRouter. `ephemeral` cache for Anthropic. Automatic XML fallback for providers without native support. |
| **MCP (Model Context Protocol)** | Client via stdio and SSE for expanded context. |
| **Persistent contexts** | `/context create`, `/context attach` — inject whole projects into the system prompt with cache hints. |
| **Bootstrap & Memory** | `SOUL.md`, `USER.md`, `IDENTITY.md`, `RULES.md` + long-term memory with facts and decay. |
| **Plugins** | Auto-detection, schema validation, Ed25519 signatures, remote plugins. |
| **Skills** | Multi-registry (skills.sh, ClawHub, ChatCLI.dev), fuzzy search, security audits, source preferences, atomic install. |
| **Custom personas** | Markdown with YAML frontmatter (model, tools, skills). |
| **Hooks** | PreToolUse, PostToolUse, SessionStart/End, UserPromptSubmit, Pre/PostCompact — shell or webhook. |
| **WebFetch / WebSearch** | DuckDuckGo + fetch with text extraction. |
| **Cost tracking** | Per-session cost with per-provider pricing tables. |
| **Git Worktrees** | Isolated work on parallel branches. |
| **K8s Watcher** | Multi-target: metrics, logs, events, Prometheus scraping. |
| **i18n** | Portuguese and English with automatic detection. |
| **Session management** | Save, load, fork, export. |

---

## Architecture

```
chatcli/
  cli/
    agent/
      quality/              7-pattern pipeline (state machine + COW snapshots)
        convergence/        Semantic convergence (char → jaccard → embedding)
        lessonq/            Reflexion durable queue (WAL + worker pool + DLQ)
      workers/              14 agents + dispatcher + FileLockManager
    hooks/                  Lifecycle events (shell/webhook)
    mcp/                    MCP client (stdio + SSE)
    plugins/                Plugin manager + signature verification
    scheduler/              Chronos — durable scheduler (WAL + cron + DAG + daemon)
      condition/            10 evaluators (shell, http, k8s, docker, tcp, llm, ...)
      action/               8 executors (slash, shell, agent, webhook, ...)
      builtins/             Aggregated registry for evaluators + executors
    workspace/memory/       Facts, topics, patterns, vector index (HyDE)
    tui/                    Bubble Tea adapters
  llm/
    openai/  openai_responses/  openai_assistant/
    claudeai/  bedrock/
    googleai/  xai/  zai/  minimax/
    copilot/  github_models/  stackspotai/  openrouter/  ollama/
    fallback/  catalog/  registry/  token/  toolshim/  embedding/
  metrics/                  Prometheus registry + /metrics + /healthz
  server/                   gRPC + TLS + JWT + MCP + plugin discovery
  operator/                 Kubernetes Operator (17 CRDs, AIOps pipeline)
  k8s/                      Watcher (collectors, store, summarizer)
  models/                   ToolDefinition, ToolCall, LLMResponse, Message
  auth/                     OAuth PKCE, Device Flow, AES-256-GCM store
  config/                   ConfigManager with versioned migration
  i18n/                     embed.FS + golang.org/x/text (PT / EN)
```

> **Design principle:** each package declares its own interfaces and self-registers. The `llm/` registry lets you add a new provider by implementing a single interface. The quality pipeline is pluggable via `AddPre`/`AddPost` with atomic swap. The operator coordinates independent CRDs via the controller pattern.

---

## CI/CD & Releases

- **CI** (`.github/workflows/1-ci.yml`): golangci-lint, gofmt, `go vet`, `go test -race -coverprofile`, coverage HTML as artifact.
- **Security scan** (`security-scan.yml`): continuous Trivy image scanning.
- **Release automation** (`release-please` + `publish-release.yml`): multi-platform builds, cosign signatures, CycloneDX SBOM, ArtifactHub publishing.
- **Makefile**: `make build`, `make test`, `make lint`, `make install` with `Version`, `CommitHash`, `BuildDate` injected via ldflags.

---

## Contributing

1. Fork the repository
2. Create a branch from `main`: `git checkout -b feature/my-feature`
3. Commit and push
4. Open a Pull Request

See [`docs/`](docs/) for detailed architecture, quality pipeline, and operator guides.

---

## License

[Apache License 2.0](LICENSE)

---

<p align="center">
  <a href="https://chatcli.edilsonfreitas.com"><strong>Documentation</strong></a> &bull;
  <a href="https://github.com/diillson/chatcli/releases"><strong>Releases</strong></a> &bull;
  <a href="https://artifacthub.io/packages/search?ts_query_web=chatcli&sort=relevance&page=1"><strong>Helm Charts</strong></a> &bull;
  <a href="https://pkg.go.dev/github.com/diillson/chatcli"><strong>Go Reference</strong></a> &bull;
  <a href="https://github.com/diillson/chatcli/issues"><strong>Issues</strong></a>
</p>
