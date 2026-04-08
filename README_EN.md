<p align="center">
  <a href="https://chatcli.edilsonfreitas.com">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

<h1 align="center">ChatCLI</h1>
<p align="center"><strong>Multi-provider AI platform for terminal, server, and Kubernetes</strong></p>
<p align="center"><em>12 providers. 12 agents. One interface.</em></p>

<div align="center">
  <img src="https://github.com/diillson/chatcli/actions/workflows/1-ci.yml/badge.svg"/>
  <a href="https://github.com/diillson/chatcli/actions/workflows/security-scan.yml">
    <img src="https://github.com/diillson/chatcli/actions/workflows/security-scan.yml/badge.svg" alt="Security Scan"/>
  </a>
  <a href="https://github.com/diillson/chatcli/releases">
    <img src="https://img.shields.io/github/v/release/diillson/chatcli"/>
  </a>
  <a href="https://artifacthub.io/packages/search?ts_query_web=chatcli&sort=relevance&page=1">
    <img src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/chatcli"/>
  </a>
  <img src="https://img.shields.io/badge/Trivy-image%20scanning-00C9A7?logo=aquasecurity" alt="Trivy"/>
  <img src="https://img.shields.io/badge/Sigstore-cosign%20signed-4B32C3?logo=sigstore" alt="Cosign Signed"/>
  <img src="https://img.shields.io/github/go-mod/go-version/diillson/chatcli?label=Go"/>
  <img src="https://img.shields.io/github/license/diillson/chatcli"/>
  <img src="https://img.shields.io/github/last-commit/diillson/chatcli"/>
  <img src="https://img.shields.io/github/languages/code-size/diillson/chatcli"/>
</div>

<br>

<p align="center">
  <a href="README.md">Portugues</a> &bull;
  <a href="https://chatcli.edilsonfreitas.com">Full documentation and all functions</a>
</p>

---

<p align="center">
  <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli-demo.gif" alt="ChatCLI Demo" width="800">
</p>

<br>

## Overview

ChatCLI is a CLI, gRPC server, and Kubernetes operator that connects **12 LLM providers** to a single, unified interface. It ships with autonomous agents, native tool calling, automatic failover, enterprise-grade security, and a full AIOps pipeline -- all from your terminal.

> **Why ChatCLI?** &mdash; Most AI tools lock you into one provider. ChatCLI gives you a consistent experience across OpenAI, Anthropic, Google, xAI, and eight more -- including OpenRouter with access to 200+ models -- with transparent failover, cost tracking, and the ability to run entirely on-prem via Ollama.

<br>

## Supported Providers

| Provider | Default Model | Tool Calling | Vision |
|:--|:--|:--|:--|
| **OpenAI** | gpt-5.4 | Native | Yes |
| **Anthropic (Claude)** | claude-sonnet-4-6 | Native | Yes |
| **Google Gemini** | gemini-2.5-flash | Native | Yes |
| **xAI (Grok)** | grok-4-1 | XML fallback | -- |
| **ZAI (Zhipu AI)** | glm-5 | Native | Yes |
| **MiniMax** | MiniMax-M2.7 | Native | Yes |
| **GitHub Copilot** | gpt-4o | Native | Yes |
| **GitHub Models** | gpt-4o | Native | Yes |
| **StackSpot AI** | StackSpotAI | -- | -- |
| **OpenRouter** | openai/gpt-4o | Native | Yes |
| **Ollama** | (local) | XML fallback | -- |
| **OpenAI Assistants** | gpt-4o | Assistants API | -- |

<br>

---

## Getting Started

### Installation

```bash
# Homebrew (macOS / Linux)
brew tap diillson/chatcli && brew install chatcli

# Go install
go install github.com/diillson/chatcli@latest

# Pre-built binaries
# https://github.com/diillson/chatcli/releases
```

<details>
<summary><strong>Build from source</strong></summary>

```bash
git clone https://github.com/diillson/chatcli.git && cd chatcli
go mod tidy && go build -o chatcli

# With version metadata
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
go build -ldflags "-X github.com/diillson/chatcli/version.Version=${VERSION}" -o chatcli
```
</details>

### Quick Setup

Create a `.env` file or export the variables:

```bash
LLM_PROVIDER=OPENAI          # OPENAI, CLAUDEAI, GOOGLEAI, XAI, ZAI, MINIMAX, COPILOT, OLLAMA, STACKSPOT, OPENROUTER
OPENAI_API_KEY=sk-xxx         # Key for the chosen provider
```

<details>
<summary><strong>Full provider configuration reference</strong></summary>

| Provider | API Key | Model | Extras |
|:--|:--|:--|:--|
| OpenAI | `OPENAI_API_KEY` | `OPENAI_MODEL` | `OPENAI_MAX_TOKENS`, `OPENAI_USE_RESPONSES` |
| Anthropic | `ANTHROPIC_API_KEY` | `ANTHROPIC_MODEL` | `ANTHROPIC_MAX_TOKENS` |
| Google Gemini | `GOOGLEAI_API_KEY` | `GOOGLEAI_MODEL` | `GOOGLEAI_MAX_TOKENS` |
| xAI | `XAI_API_KEY` | `XAI_MODEL` | `XAI_MAX_TOKENS` |
| ZAI | `ZAI_API_KEY` | `ZAI_MODEL` | `ZAI_MAX_TOKENS` |
| MiniMax | `MINIMAX_API_KEY` | `MINIMAX_MODEL` | `MINIMAX_MAX_TOKENS` |
| GitHub Copilot | `GITHUB_COPILOT_TOKEN` | `COPILOT_MODEL` | or `/auth login github-copilot` |
| GitHub Models | `GITHUB_TOKEN` | `GITHUB_MODELS_MODEL` | `GH_TOKEN`, `GITHUB_MODELS_TOKEN` |
| StackSpot | `CLIENT_ID`, `CLIENT_KEY` | -- | `STACKSPOT_REALM`, `STACKSPOT_AGENT_ID` |
| OpenRouter | `OPENROUTER_API_KEY` | -- | `OPENROUTER_MAX_TOKENS`, `OPENROUTER_FALLBACK_MODELS` |
| Ollama | -- | `OLLAMA_MODEL` | `OLLAMA_ENABLED=true`, `OLLAMA_BASE_URL` |

</details>

<br>

---

## Three Modes of Operation

### 1. Interactive CLI

> Your terminal becomes an AI-powered workstation.

```bash
chatcli                              # Interactive mode
chatcli -p "Explain this repo"       # One-shot
git diff | chatcli -p "Summarize"    # Pipe stdin
```

**Context commands** -- inject data directly into your prompt:

| Command | Description |
|:--|:--|
| `@git` | Status, branches, and recent commits |
| `@file <path>` | File or directory contents |
| `@env` | Environment variables |
| `@history` | Recent shell commands |
| `@command <cmd>` | Execute a command and inject its output |

### 2. gRPC Server

> Deploy ChatCLI as a shared backend with authentication, metrics, and failover.

```bash
chatcli server --port 50051 --token my-token

# Remote client
chatcli connect --server host:50051 --token my-token
```

Includes automatic failover, TLS, Prometheus metrics, MCP support, and plugin/agent/skill discovery.

### 3. Kubernetes Operator

> AIOps at scale -- anomaly detection, AI-driven remediation, and SLO monitoring.

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

The operator delivers an autonomous AIOps pipeline: anomaly detection, issue correlation, AI analysis, automated remediation, post-mortem generation, and SLO monitoring. **17 CRDs** and **54+ remediation actions** out of the box.

```bash
# Helm install
helm install chatcli oci://ghcr.io/diillson/charts/chatcli \
  --namespace chatcli --create-namespace \
  --set llm.provider=OPENAI --set secrets.openaiApiKey=sk-xxx

helm install chatcli-operator oci://ghcr.io/diillson/charts/chatcli-operator \
  --namespace aiops-system --create-namespace
```

<br>

---

## Core Features

### Agent Mode

> 12 specialized agents coordinated by a ReAct engine (Reason + Act), running in parallel.

**File** &middot; **Coder** &middot; **Shell** &middot; **Git** &middot; **Search** &middot; **Planner** &middot; **Reviewer** &middot; **Tester** &middot; **Refactor** &middot; **Diagnostics** &middot; **Formatter** &middot; **Deps**

```bash
/coder "Refactor the auth module to use JWT"
chatcli -p "Create tests for the utils package" --agent-auto-exec
```

### Native Tool Calling

Structured tool calls via OpenAI, Anthropic, Google, ZAI, MiniMax, and OpenRouter native APIs. Ephemeral cache support for Anthropic. Automatic XML fallback for providers without native support.

### Built-in OAuth

```
/auth login openai-codex       # OAuth PKCE + local callback
/auth login anthropic          # OAuth PKCE + manual code
/auth login github-copilot     # Device Flow (RFC 8628)
/auth status                   # All provider status
```

Credentials stored with **AES-256-GCM** encryption in `~/.chatcli/auth-profiles.json`.

### Provider Fallback

```bash
CHATCLI_FALLBACK_PROVIDERS=OPENAI,CLAUDEAI,ZAI,MINIMAX,OPENROUTER
```

Error classification (rate limit, timeout, auth, context overflow), exponential backoff, and per-provider cooldown.

### MCP (Model Context Protocol)

```json
// ~/.chatcli/mcp_servers.json
{
  "servers": [
    {"name": "filesystem", "transport": "stdio", "command": "npx", "args": ["-y", "@anthropic/mcp-server-filesystem", "/workspace"]},
    {"name": "search", "transport": "sse", "url": "http://mcp-search:8080/sse"}
  ]
}
```

<details>
<summary><strong>All features at a glance</strong></summary>

| Feature | Description |
|:--|:--|
| **Persistent contexts** | `/context create`, `/context attach` -- inject entire projects into the system prompt with cache hints |
| **Bootstrap & Memory** | `SOUL.md`, `USER.md`, `IDENTITY.md`, `RULES.md` + long-term memory with facts and decay |
| **Plugins** | Extensible system with auto-detection, schema validation, and remote plugins |
| **Skills** | Multi-source registry with fuzzy search, moderation, and atomic installation |
| **Custom agents** | Markdown personas with YAML frontmatter (model, tools, skills) |
| **Hooks** | Lifecycle events (PreToolUse, PostToolUse, SessionStart) with shell commands and webhooks |
| **WebFetch / WebSearch** | DuckDuckGo search and page fetch with text extraction |
| **Cost tracking** | Per-session cost with pricing tables per provider |
| **Git Worktrees** | Isolated work on parallel branches |
| **K8s Watcher** | Multi-target monitoring with metrics, logs, events, and Prometheus scraping |
| **i18n** | Interface in Portuguese and English with automatic detection |
| **Session management** | Save, load, fork, and export conversations |

</details>

<br>

---

## Enterprise Security

> Security is not an afterthought. ChatCLI is hardened at every layer -- from the credential store on your laptop to the operator running in production.

### Authentication & Authorization

- **JWT-based authentication** with role-based access control (RBAC): `admin`, `user`, and `readonly` roles
- Per-client **rate limiting** to prevent abuse and ensure fair resource allocation
- Built-in **OAuth PKCE** and **Device Flow** for provider authentication without exposing secrets

### Encryption & Data Protection

- **AES-256-GCM encryption at rest** for sessions, credentials, and stored tokens
- **TLS 1.3 enforcement** for all gRPC server communication
- **SSRF prevention** with strict URL validation and private network blocking

### Code & Plugin Integrity

- **Ed25519 plugin signature verification** -- only cryptographically signed plugins are loaded
- **Agent command allowlist** with **150+ categorized commands** controlling what agents can execute
- **Schema validation** for all plugin manifests and tool definitions

### Auditing & Compliance

- **Structured audit logging** in JSON Lines format for every authenticated action
- **Automated security scanning** integrated into CI: `govulncheck`, `gosec`, and Dependabot
- Versioned configuration with migration support for safe upgrades

<br>

---

## Architecture

```
chatcli/
  cli/            TUI interface (Bubble Tea), agent mode, multi-agent workers
  llm/            12 providers, auto-register registry, fallback chain, catalog
  server/         gRPC server with TLS, auth, metrics, and MCP
  operator/       Kubernetes Operator -- 17 CRDs, autonomous AIOps pipeline
  k8s/            Watcher (collectors, store, summarizer)
  models/         Shared types (ToolDefinition, ToolCall, LLMResponse)
  auth/           OAuth PKCE, Device Flow, token refresh, encrypted store
  config/         ConfigManager with versioned migration
  i18n/           Internationalization (embed.FS + golang.org/x/text)
```

> **Design principles:** provider-agnostic abstractions, zero hard-coded credentials, structured event-driven TUI via Bubble Tea, and a plugin system that trusts nothing unsigned.

<br>

---

## Contributing

1. Fork the repository
2. Create a branch: `git checkout -b feature/my-feature`
3. Commit and push
4. Open a Pull Request

All contributions are welcome -- features, bug fixes, documentation, and security improvements.

<br>

---

## License

[Apache License 2.0](LICENSE)

<br>

---

<p align="center">
  <a href="https://chatcli.edilsonfreitas.com"><strong>Documentation</strong></a> &middot;
  <a href="https://github.com/diillson/chatcli/releases"><strong>Releases</strong></a> &middot;
  <a href="https://artifacthub.io/packages/search?ts_query_web=chatcli&sort=relevance&page=1"><strong>Helm Charts</strong></a> &middot;
  <a href="https://github.com/diillson/chatcli/issues"><strong>Issues</strong></a>
</p>
