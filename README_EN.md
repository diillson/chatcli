<p align="center">
  <a href="https://chatcli.edilsonfreitas.com">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

<h1 align="center">ChatCLI</h1>
<p align="center"><strong>Multi-provider AI platform for terminal, server, and Kubernetes</strong></p>

<div align="center">
  <img src="https://github.com/diillson/chatcli/actions/workflows/1-ci.yml/badge.svg"/>
  <a href="https://github.com/diillson/chatcli/releases">
    <img src="https://img.shields.io/github/v/release/diillson/chatcli"/>
  </a>
  <a href="https://artifacthub.io/packages/helm/chatcli/chatcli">
    <img src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/chatcli"/>
  </a>
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

## What is ChatCLI?

A CLI, gRPC server, and Kubernetes operator that connects **11 LLM providers** to a unified interface with autonomous agents, native tool calling, automatic failover, and a full AIOps pipeline.

### Supported providers

| Provider | Default Model | Tool Calling | Vision |
|---|---|---|---|
| **OpenAI** | gpt-5.4 | Native | Yes |
| **Anthropic (Claude)** | claude-sonnet-4-6 | Native | Yes |
| **Google Gemini** | gemini-2.5-flash | Native | Yes |
| **xAI (Grok)** | grok-4-1 | XML fallback | - |
| **ZAI (Zhipu AI)** | glm-5 | Native | Yes |
| **MiniMax** | MiniMax-M2.7 | Native | Yes |
| **GitHub Copilot** | gpt-4o | Native | Yes |
| **GitHub Models** | gpt-4o | Native | Yes |
| **StackSpot AI** | StackSpotAI | - | - |
| **Ollama** | (local) | XML fallback | - |
| **OpenAI Assistants** | gpt-4o | Assistants API | - |

---

## Installation

```bash
# Homebrew (macOS / Linux)
brew tap diillson/chatcli && brew install chatcli

# Go install
go install github.com/diillson/chatcli@latest

# Pre-built binaries
# https://github.com/diillson/chatcli/releases
```

<details>
<summary>Build from source</summary>

```bash
git clone https://github.com/diillson/chatcli.git && cd chatcli
go mod tidy && go build -o chatcli

# With version info
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
go build -ldflags "-X github.com/diillson/chatcli/version.Version=${VERSION}" -o chatcli
```
</details>

---

## Quick setup

Create a `.env` file or export the variables:

```bash
LLM_PROVIDER=OPENAI          # OPENAI, CLAUDEAI, GOOGLEAI, XAI, ZAI, MINIMAX, COPILOT, OLLAMA, STACKSPOT
OPENAI_API_KEY=sk-xxx         # Key for the chosen provider
```

<details>
<summary>All provider variables</summary>

| Provider | API Key | Model | Extras |
|---|---|---|---|
| OpenAI | `OPENAI_API_KEY` | `OPENAI_MODEL` | `OPENAI_MAX_TOKENS`, `OPENAI_USE_RESPONSES` |
| Anthropic | `ANTHROPIC_API_KEY` | `ANTHROPIC_MODEL` | `ANTHROPIC_MAX_TOKENS` |
| Google Gemini | `GOOGLEAI_API_KEY` | `GOOGLEAI_MODEL` | `GOOGLEAI_MAX_TOKENS` |
| xAI | `XAI_API_KEY` | `XAI_MODEL` | `XAI_MAX_TOKENS` |
| ZAI | `ZAI_API_KEY` | `ZAI_MODEL` | `ZAI_MAX_TOKENS` |
| MiniMax | `MINIMAX_API_KEY` | `MINIMAX_MODEL` | `MINIMAX_MAX_TOKENS` |
| GitHub Copilot | `GITHUB_COPILOT_TOKEN` | `COPILOT_MODEL` | or `/auth login github-copilot` |
| GitHub Models | `GITHUB_TOKEN` | `GITHUB_MODELS_MODEL` | `GH_TOKEN`, `GITHUB_MODELS_TOKEN` |
| StackSpot | `CLIENT_ID`, `CLIENT_KEY` | - | `STACKSPOT_REALM`, `STACKSPOT_AGENT_ID` |
| Ollama | - | `OLLAMA_MODEL` | `OLLAMA_ENABLED=true`, `OLLAMA_BASE_URL` |

</details>

---

## Three modes of operation

### 1. Interactive CLI

```bash
chatcli                              # Interactive mode
chatcli -p "Explain this repo"       # One-shot
git diff | chatcli -p "Summarize"    # Pipe stdin
```

**Context commands** — inject data directly into your prompt:

| Command | What it does |
|---|---|
| `@git` | Status, branches, and recent commits |
| `@file <path>` | File/directory contents |
| `@env` | Environment variables |
| `@history` | Recent shell commands |
| `@command <cmd>` | Execute and inject output |

### 2. gRPC Server

```bash
chatcli server --port 50051 --token my-token

# Remote client
chatcli connect --server host:50051 --token my-token
```

Automatic failover, TLS, Prometheus metrics, MCP, plugin/agent/skill discovery.

### 3. Kubernetes Operator

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

Autonomous AIOps pipeline: anomaly detection, issue correlation, AI analysis, automated remediation, post-mortems, and SLO monitoring. 17 CRDs, 54+ remediation actions.

```bash
# Helm install
helm install chatcli oci://ghcr.io/diillson/charts/chatcli \
  --namespace chatcli --create-namespace \
  --set llm.provider=OPENAI --set secrets.openaiApiKey=sk-xxx

helm install chatcli-operator oci://ghcr.io/diillson/charts/chatcli-operator \
  --namespace aiops-system --create-namespace
```

---

## Key features

### Agent Mode

ReAct engine (Reason + Act) with **12 specialized agents** running in parallel: File, Coder, Shell, Git, Search, Planner, Reviewer, Tester, Refactor, Diagnostics, Formatter, Deps.

```bash
/agent "Refactor the auth module to use JWT"
chatcli -p "Create tests for the utils package" --agent-auto-exec
```

### Native tool calling

Structured tool calls via OpenAI, Anthropic, Google, ZAI, and MiniMax native APIs. Ephemeral cache support for Anthropic. Automatic XML fallback for providers without native support.

### Built-in OAuth

```
/auth login openai-codex       # OAuth PKCE + local callback
/auth login anthropic          # OAuth PKCE + manual code
/auth login github-copilot     # Device Flow (RFC 8628)
/auth status                   # All provider status
```

Credentials stored with **AES-256-GCM** encryption in `~/.chatcli/auth-profiles.json`.

### Provider fallback

```bash
CHATCLI_FALLBACK_PROVIDERS=OPENAI,CLAUDEAI,ZAI,MINIMAX
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

### More features

| Feature | Description |
|---|---|
| **Persistent contexts** | `/context create`, `/context attach` — inject entire projects into system prompt with cache hints |
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

---

## Architecture

```
chatcli/
  cli/            TUI interface (Bubble Tea), agent mode, multi-agent workers
  llm/            11 providers, auto-register registry, fallback chain, catalog
  server/         gRPC server with TLS, auth, metrics, and MCP
  operator/       Kubernetes Operator — 17 CRDs, autonomous AIOps pipeline
  k8s/            Watcher (collectors, store, summarizer)
  models/         Shared types (ToolDefinition, ToolCall, LLMResponse)
  auth/           OAuth PKCE, Device Flow, token refresh, encrypted store
  config/         ConfigManager with versioned migration
  i18n/           Internationalization (embed.FS + golang.org/x/text)
```

---

## Contributing

1. Fork the repository
2. Create a branch: `git checkout -b feature/my-feature`
3. Commit and push
4. Open a Pull Request

---

## License

[Apache License 2.0](LICENSE)

---

## Links

- **Full documentation**: [chatcli.edilsonfreitas.com](https://chatcli.edilsonfreitas.com)
- **Releases**: [github.com/diillson/chatcli/releases](https://github.com/diillson/chatcli/releases)
- **Helm Charts**: [ArtifactHub](https://artifacthub.io/packages/helm/chatcli/chatcli)
- **Issues**: [github.com/diillson/chatcli/issues](https://github.com/diillson/chatcli/issues)
