<p align="center">
  <a href="https://chatcli.edilsonfreitas.com">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI" width="280">
  </a>
</p>

<h1 align="center">ChatCLI</h1>

<p align="center">
  <strong>One binary for AI everywhere you work:<br>
  the terminal, your editor, your chat apps and webhooks, a gRPC server and Kubernetes.</strong>
</p>

<p align="center">
  <sub>14 LLM providers · an autonomous coder agent · a seven-pattern quality harness<br>
  Mixture-of-Agents with cross-review · a live APM-style dashboard</sub>
</p>

<p align="center">
  <a href="https://chatcli.edilsonfreitas.com"><img src="https://img.shields.io/static/v1?label=%F0%9F%93%96%20Read%20the%20docs&message=https%3A%2F%2Fchatcli.edilsonfreitas.com&color=0d9488&style=for-the-badge" alt="Read the documentation at https://chatcli.edilsonfreitas.com"></a><br>
  <a href="https://chatcli.edilsonfreitas.com"><strong>https://chatcli.edilsonfreitas.com</strong></a><br>
  <sub>The official website: guides, cookbooks, configuration and the full reference for every feature, in English and Portuguese.</sub>
</p>

<p align="center">
  <a href="https://github.com/diillson/chatcli/actions/workflows/1-ci.yml"><img src="https://img.shields.io/github/actions/workflow/status/diillson/chatcli/1-ci.yml?label=CI&logo=github" alt="CI"></a>
  <a href="https://github.com/diillson/chatcli/actions/workflows/quality-gate.yml"><img src="https://img.shields.io/github/actions/workflow/status/diillson/chatcli/quality-gate.yml?label=quality%20gate&logo=github" alt="Quality gate"></a>
  <a href="https://github.com/diillson/chatcli/actions/workflows/security-scan.yml"><img src="https://img.shields.io/github/actions/workflow/status/diillson/chatcli/security-scan.yml?label=security%20scan&logo=github" alt="Security scan"></a>
  <a href="https://github.com/diillson/chatcli/releases/latest"><img src="https://img.shields.io/github/v/release/diillson/chatcli" alt="Latest release"></a>
  <br>
  <img src="https://img.shields.io/github/go-mod/go-version/diillson/chatcli?logo=go&label=Go" alt="Go version">
  <a href="https://pkg.go.dev/github.com/diillson/chatcli"><img src="https://img.shields.io/badge/Go-reference-007d9c?logo=go&logoColor=white" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue" alt="License: Apache-2.0"></a>
  <a href="https://artifacthub.io/packages/search?ts_query_web=chatcli"><img src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/chatcli" alt="Artifact Hub"></a>
  <br>
  <a href="SECURITY.md#verifying-what-you-install"><img src="https://img.shields.io/badge/releases-Ed25519%20signed-1f6feb" alt="Signed releases"></a>
  <a href="SECURITY.md#verifying-what-you-install"><img src="https://img.shields.io/badge/images-cosign%20signed-4B32C3?logo=sigstore&logoColor=white" alt="Cosign-signed images"></a>
  <img src="https://img.shields.io/badge/SBOM-included-3fb950" alt="SBOM">
  <img src="https://img.shields.io/badge/scanned-Trivy%20%C2%B7%20CodeQL%20%C2%B7%20gosec-00C9A7" alt="Security scans">
  <br>
  <img src="https://img.shields.io/badge/platforms-Linux%20%C2%B7%20macOS%20%C2%B7%20Windows-informational" alt="Platforms">
  <img src="https://img.shields.io/badge/i18n-English%20%C2%B7%20Portugu%C3%AAs-informational" alt="Languages">
  <a href="https://github.com/diillson/chatcli/stargazers"><img src="https://img.shields.io/github/stars/diillson/chatcli?style=flat&logo=github&label=stars" alt="Stars"></a>
</p>

<p align="center">
  <a href="https://chatcli.edilsonfreitas.com"><strong>Documentation</strong></a> ·
  <a href="#quick-start"><strong>Quick start</strong></a> ·
  <a href="#highlights"><strong>Highlights</strong></a> ·
  <a href="#security"><strong>Security</strong></a> ·
  <a href="CONTRIBUTING.md"><strong>Contributing</strong></a> ·
  <a href="README_PT.md"><strong>Português</strong></a>
</p>

<p align="center">
  <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli-demo.gif" alt="ChatCLI in action" width="820">
</p>

## Why ChatCLI

- **Every major model, one interface.** Switch provider or model mid-conversation; fail over automatically; keep cost, tokens and prompt-cache use in view.
- **An agent that does the work.** `/coder` reads, edits, runs and tests code under a security policy you control, with checkpoints you can rewind.
- **Answers you can trust more.** Self-Refine, Chain-of-Verification, Reflexion, RAG with HyDE and Mixture-of-Agents with cross-review cross-check what models say.
- **The same brain everywhere.** The terminal, a browser UI, editors over ACP, any MCP client, Telegram, Slack, Discord, WhatsApp, webhooks, a gRPC server and a Kubernetes operator share sessions, memory and tools.
- **Built to run where it matters.** Signed releases, fail-closed defaults, workspace trust, audit logs and a published security process.

## Quick start

```bash
# Homebrew (macOS, Linux)
brew tap diillson/chatcli && brew install chatcli

# or Go
go install github.com/diillson/chatcli@latest

# or a signed binary from the releases page
# https://github.com/diillson/chatcli/releases/latest
```

Point it at a model, with an API key or an OAuth login:

```bash
export LLM_PROVIDER=OPENAI OPENAI_API_KEY=sk-...   # or CLAUDEAI, GOOGLEAI, BEDROCK, XAI, OLLAMA, …
chatcli                                            # then /auth login <provider> for OAuth
```

Then:

```bash
chatcli -p "Explain this repository"               # one-shot, pipeable
git diff | chatcli -p "Write the commit message"
chatcli                                            # interactive
  > /coder add tests for the utils package         # the autonomous coder
  > /moa --layers 2 Should we migrate to Karpenter? # several models, cross-reviewed
  > /dash                                          # watch everything live
```

ChatCLI keeps itself current with `/update` (Homebrew, `go install` or a signature-verified self-replace). [Installation guide →](https://chatcli.edilsonfreitas.com/start/installation)

## Highlights

<table>
<tr>
<td width="50%" valign="top">

### 🤖 Coder agent
A ReAct loop with 46 builtin tools: files, search, shell, git, tests, LSP diagnostics, a real browser, Git forges, background processes. Every call goes through an allow/ask/deny policy; edits are checkpointed in a shadow repository; an optional OS sandbox confines commands. [Coder mode →](https://chatcli.edilsonfreitas.com/usage/coder-mode)

</td>
<td width="50%" valign="top">

### 🧠 Mixture-of-Agents with cross-review
`/moa` asks several models at once. With `--layers 2`, each model reads the others' answers and revises its own, and the synthesis keeps a section on where they still disagree. The AI can call it too, as `@moa`. [MoA →](https://chatcli.edilsonfreitas.com/agents/mixture-of-agents)

</td>
</tr>
<tr>
<td valign="top">

### ✅ Quality harness
Seven patterns, composable and observable: ReAct, Plan-and-Solve, Reflexion with a durable queue, RAG + HyDE, Self-Refine with semantic convergence, Chain-of-Verification and a cross-provider reasoning backbone. [Harness →](https://chatcli.edilsonfreitas.com/agents/harness/overview)

</td>
<td valign="top">

### 📈 Live dashboard, like an APM
`/dash` draws the whole runtime as a service map, across every ChatCLI process on the machine: who called whom, from the turn to the agent, the tool, the model and the host, with cost, latency, errors, policy decisions and fallbacks. Metadata only. [Live dashboard →](https://chatcli.edilsonfreitas.com/usage/live-dashboard)

</td>
</tr>
<tr>
<td valign="top">

### 👥 Squads and task graphs
Specialist workers run in parallel under a dispatcher; `@taskgraph` turns an approved plan into a verified DAG where the engine runs each task's checks and a fresh reviewer gives the verdict. [Task graph →](https://chatcli.edilsonfreitas.com/agents/task-graph)

</td>
<td valign="top">

### 🔌 MCP, ACP and plugins
An MCP client (stdio, SSE, HTTP with OAuth) and an MCP server that re-exports everything; an ACP server for editors; signed plugins; provider plugins that add a whole LLM provider; skills from several registries; slash commands compatible with nine other CLIs. [Extensions →](https://chatcli.edilsonfreitas.com/extensions/plugin-system)

</td>
</tr>
<tr>
<td valign="top">

### 💬 Chat gateway
Telegram, Slack, Discord, WhatsApp and webhooks run the same agent, with voice notes transcribed offline and answered in voice, sender allowlists that fail closed and one conversation across channels. [Gateway →](https://chatcli.edilsonfreitas.com/gateway/chat-gateway)

</td>
<td valign="top">

### ☸️ Kubernetes and AIOps
A gRPC server with TLS 1.3, JWT and roles, and an operator with 17 CRDs that watches workloads, opens incidents, proposes and applies remediations behind approvals, and writes post-mortems. [Kubernetes →](https://chatcli.edilsonfreitas.com/kubernetes/k8s-operator)

</td>
</tr>
<tr>
<td valign="top">

### 📄 Documents and media, on every provider
PDFs and images reach every model, natively where it can read them and as text where it cannot; image generation, offline speech-to-text and text-to-speech, diagrams and an interactive knowledge graph. [PDF input →](https://chatcli.edilsonfreitas.com/usage/pdf-input)

</td>
<td valign="top">

### 🗂️ Memory and context
Long-term memory with a profile and facts, a knowledge base with keyless BM25 retrieval, reversible compression where nothing is lost (`@recall` pages long originals), and sessions that continue across surfaces. [Context →](https://chatcli.edilsonfreitas.com/context/bootstrap-memory)

</td>
</tr>
<tr>
<td valign="top">

### ⏱️ Scheduler
Durable jobs with cron, wait-until conditions and dependencies; a daemon keeps them running with the terminal closed; agents can park themselves and wake on a condition. [Scheduler →](https://chatcli.edilsonfreitas.com/tools/scheduler)

</td>
<td valign="top">

### 🧪 Evals
`chatcli eval` runs YAML suites against the real binary in a sandbox, graded by deterministic checks and an LLM judge, with pass@k, baselines and CI-friendly exit codes. [Evals →](https://chatcli.edilsonfreitas.com/agents/harness/evals)

</td>
</tr>
</table>

## Where it runs

| Surface | Start it with | |
|---|---|---|
| Interactive terminal | `chatcli` | chat, `/coder`, `/agent`, `/moa`, every slash command |
| One-shot and pipes | `chatcli -p "…"` | scripts, CI, `git diff \| chatcli -p …` |
| Browser UI | `/web` or `chatcli web` | the same session as the terminal |
| Editors (ACP) | `chatcli acp` | Zed and other ACP clients |
| Any MCP client | `chatcli mcp-server` | every tool, the agent loops, skills as prompts |
| Chat apps | `/gateway start` | Telegram, Slack, Discord, WhatsApp, webhooks |
| Shared backend | `chatcli server` | gRPC with TLS 1.3, JWT and roles |
| Kubernetes | Helm chart `chatcli-operator` | AIOps operator |
| Live dashboard | `/dash` or `chatcli dash` | every ChatCLI process on the machine |

## Providers

14 built-in providers behind one interface, with a fallback chain, cross-provider reasoning control (`/thinking`) and prompt caching where the provider offers it. Provider plugins add more without rebuilding ChatCLI.

| Provider | `LLM_PROVIDER` | Auth |
|---|---|---|
| OpenAI | `OPENAI` | `OPENAI_API_KEY`, or OAuth with `/auth login openai-codex` (Responses API) |
| OpenAI Assistants | `OPENAI_ASSISTANT` | `OPENAI_API_KEY` |
| Anthropic Claude | `CLAUDEAI` | `ANTHROPIC_API_KEY`, or OAuth with `/auth login anthropic` |
| AWS Bedrock | `BEDROCK` | AWS profile, SSO, `aws login` or the credentials chain |
| Google Gemini | `GOOGLEAI` | `GOOGLEAI_API_KEY` |
| xAI Grok | `XAI` | `XAI_API_KEY` |
| Z.AI (GLM) | `ZAI` | `ZAI_API_KEY`, including the GLM Coding Plan |
| Moonshot Kimi | `MOONSHOT` | `MOONSHOT_API_KEY` |
| MiniMax | `MINIMAX` | `MINIMAX_API_KEY` |
| OpenRouter | `OPENROUTER` | `OPENROUTER_API_KEY` |
| GitHub Copilot | `COPILOT` | `/auth login github-copilot` (device flow) or `GITHUB_COPILOT_TOKEN` |
| Ollama | `OLLAMA` | local, no key |
| StackSpot AI | `STACKSPOT` | `CLIENT_ID` and `CLIENT_KEY` |
| Devin (Cognition) | `DEVIN` | the local Devin CLI |

Models, prices and context windows come from a live catalog: `/switch --model` lists what each account really serves. [Supported models →](https://chatcli.edilsonfreitas.com/providers/supported-models) · [All settings →](https://chatcli.edilsonfreitas.com/reference/environment-variables)

## Security

ChatCLI runs commands and holds credentials, so security is the default, not an option:

- **Policy-gated tools**: every agent action is allowed, asked or denied by a policy you control; unattended surfaces refuse instead of approving.
- **Workspace trust**: a project's `.env`, hooks and policy load only after `chatcli trust`.
- **Fail-closed integrations**: chat channels refuse to start without a sender allowlist; signed webhooks are verified with HMAC.
- **Encrypted at rest by default**: conversations, memory, contexts, attachments and credentials are sealed with AES-256-GCM, with the key in the OS keychain (macOS, Linux, Windows) or, in Kubernetes, in a Secret; OAuth uses PKCE.
- **Signed supply chain**: Ed25519-signed release checksums verified by `chatcli update`, Cosign-signed images, an SBOM, and Trivy, CodeQL, gosec and govulncheck on every change.

Report vulnerabilities privately: see [SECURITY.md](SECURITY.md). [Security overview →](https://chatcli.edilsonfreitas.com/security/overview)

## Documentation

| | |
|---|---|
| **Get started** | [Installation](https://chatcli.edilsonfreitas.com/start/installation) · [Quickstart](https://chatcli.edilsonfreitas.com/start/quickstart) · [Basic usage](https://chatcli.edilsonfreitas.com/usage/basic-usage) |
| **Agents** | [Coder mode](https://chatcli.edilsonfreitas.com/usage/coder-mode) · [Squads](https://chatcli.edilsonfreitas.com/agents/agent-squad) · [Task graph](https://chatcli.edilsonfreitas.com/agents/task-graph) · [Mixture-of-Agents](https://chatcli.edilsonfreitas.com/agents/mixture-of-agents) · [Harness](https://chatcli.edilsonfreitas.com/agents/harness/overview) |
| **Context** | [Memory](https://chatcli.edilsonfreitas.com/context/bootstrap-memory) · [Knowledge base](https://chatcli.edilsonfreitas.com/context/knowledge-base) · [Compression](https://chatcli.edilsonfreitas.com/context/context-compression) · [Sessions](https://chatcli.edilsonfreitas.com/context/session-management) |
| **Extend** | [Plugins](https://chatcli.edilsonfreitas.com/extensions/plugin-system) · [Provider plugins](https://chatcli.edilsonfreitas.com/extensions/provider-plugins) · [MCP](https://chatcli.edilsonfreitas.com/extensions/mcp-integration) · [Skills](https://chatcli.edilsonfreitas.com/extensions/skill-registry) · [Hooks](https://chatcli.edilsonfreitas.com/extensions/hooks-system) · [Slash commands](https://chatcli.edilsonfreitas.com/extensions/slash-commands) |
| **Run it** | [Server mode](https://chatcli.edilsonfreitas.com/server/server-mode) · [Gateway](https://chatcli.edilsonfreitas.com/gateway/chat-gateway) · [Kubernetes operator](https://chatcli.edilsonfreitas.com/kubernetes/k8s-operator) · [Docker](https://chatcli.edilsonfreitas.com/start/docker-deployment) |
| **Reference** | [Commands](https://chatcli.edilsonfreitas.com/reference/command-reference) · [Environment variables](https://chatcli.edilsonfreitas.com/reference/environment-variables) · [Architecture](https://chatcli.edilsonfreitas.com/reference/architecture) · [Releases](https://chatcli.edilsonfreitas.com/releases) |

## Community

- **Contribute**: read [CONTRIBUTING.md](CONTRIBUTING.md) for the setup, the conventions and what CI checks.
- **Conduct**: everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).
- **Security**: report vulnerabilities privately as described in [SECURITY.md](SECURITY.md).
- **Bugs and ideas**: [open an issue](https://github.com/diillson/chatcli/issues).

If ChatCLI is useful to you, a ⭐ helps others find it.

## License

[Apache License 2.0](LICENSE) © Edilson Freitas
