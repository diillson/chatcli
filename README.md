<p align="center">
  <a href="https://chatcli.edilsonfreitas.com">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

<h1 align="center">ChatCLI</h1>
<p align="center">
  <strong>Plataforma de IA unificada para terminal, servidor gRPC e Kubernetes.</strong><br>
  <sub>13 provedores · 14 agentes autônomos · pipeline de qualidade em 7 padrões · um único binário.</sub>
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
  <a href="README_EN.md">English</a> &bull;
  <a href="https://chatcli.edilsonfreitas.com">Documentação completa</a> &bull;
  <a href="#arquitetura">Arquitetura</a> &bull;
  <a href="#observabilidade">Observabilidade</a>
</p>

---

<p align="center">
  <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli-demo.gif" alt="ChatCLI Demo" width="800">
</p>

<br>

> **ChatCLI** conecta os maiores modelos de linguagem do mercado a uma interface única e extensível — do `chatcli -p` no terminal até um operador Kubernetes com pipeline AIOps autônomo, passando por um servidor gRPC production-ready com autenticação, fallback e métricas Prometheus.

<br>

## Destaques

| | |
|---|---|
| **Multi-provider com fallback** | 13 provedores de LLM (OpenAI · Anthropic · Bedrock · Google · xAI · ZAI · MiniMax · Copilot · GitHub Models · StackSpot · OpenRouter · Ollama · OpenAI Assistants), com classificação inteligente de erros, backoff exponencial e cooldown por provider. |
| **Agentes autônomos** | 14 workers especializados coordenados por motor ReAct (Reason + Act), com execução paralela e pipeline de qualidade em 7 padrões. |
| **Quality pipeline** | Self-Refine, Chain-of-Verification (CoVe), Reflexion, RAG + HyDE, Plan-and-Solve (ReWOO), backbone de reasoning cross-provider — todos compostos por state machine thread-safe com circuit breakers e hot reload. |
| **Reflexion durável** | Fila WAL-backed com worker pool, dead letter queue, replay on boot, retry exponencial com jitter — lições sobrevivem a crash do processo. |
| **Convergência semântica** | Cascade char → Jaccard → embedding cosine para Self-Refine, com cache LRU/TTL e quality regression detection. |
| **Production-ready** | gRPC + TLS 1.3, JWT + RBAC, AES-256-GCM, rate limiting, audit logging, 50+ métricas Prometheus. |
| **Kubernetes-native** | Operador com 17 CRDs e pipeline AIOps autônomo (54+ ações de remediação), SLO monitoring, post-mortems. |
| **Extensível** | Plugins com verificação Ed25519, skills multi-registry (skills.sh, ClawHub, ChatCLI.dev), hooks de lifecycle, MCP client (stdio + SSE). |

---

## Instalação

```bash
# Homebrew (macOS / Linux)
brew tap diillson/chatcli && brew install chatcli

# Go install
go install github.com/diillson/chatcli@latest

# Binários pre-compilados assinados (cosign)
# https://github.com/diillson/chatcli/releases
```

<details>
<summary><strong>Compilação a partir do código-fonte</strong></summary>

```bash
git clone https://github.com/diillson/chatcli.git && cd chatcli
go mod tidy && go build -o chatcli

# Com informações de versão injetadas via ldflags
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
go build -ldflags "-X github.com/diillson/chatcli/version.Version=${VERSION}" -o chatcli
```

</details>

---

## Configuração rápida

```bash
LLM_PROVIDER=OPENAI    # OPENAI, CLAUDEAI, BEDROCK, GOOGLEAI, XAI, ZAI, MINIMAX,
                       # COPILOT, GITHUB_MODELS, OLLAMA, STACKSPOT, OPENROUTER
OPENAI_API_KEY=sk-xxx
```

<details>
<summary><strong>Referência completa de variáveis por provider</strong></summary>

| Provider | API Key | Model | Extras |
|---|---|---|---|
| OpenAI | `OPENAI_API_KEY` | `OPENAI_MODEL` | `OPENAI_MAX_TOKENS`, `OPENAI_USE_RESPONSES` |
| Anthropic | `ANTHROPIC_API_KEY` | `ANTHROPIC_MODEL` | `ANTHROPIC_MAX_TOKENS` |
| AWS Bedrock | IAM / Profile / credentials chain | `BEDROCK_MODEL` | `AWS_REGION`, `BEDROCK_CROSS_REGION` |
| Google Gemini | `GOOGLEAI_API_KEY` | `GOOGLEAI_MODEL` | `GOOGLEAI_MAX_TOKENS` |
| xAI | `XAI_API_KEY` | `XAI_MODEL` | `XAI_MAX_TOKENS` |
| ZAI | `ZAI_API_KEY` | `ZAI_MODEL` | `ZAI_MAX_TOKENS` |
| MiniMax | `MINIMAX_API_KEY` | `MINIMAX_MODEL` | `MINIMAX_MAX_TOKENS` |
| GitHub Copilot | `GITHUB_COPILOT_TOKEN` | `COPILOT_MODEL` | ou `/auth login github-copilot` |
| GitHub Models | `GITHUB_TOKEN` | `GITHUB_MODELS_MODEL` | `GH_TOKEN`, `GITHUB_MODELS_TOKEN` |
| StackSpot | `CLIENT_ID`, `CLIENT_KEY` | — | `STACKSPOT_REALM`, `STACKSPOT_AGENT_ID` |
| OpenRouter | `OPENROUTER_API_KEY` | — | `OPENROUTER_MAX_TOKENS`, `OPENROUTER_FALLBACK_MODELS` |
| Ollama | — | `OLLAMA_MODEL` | `OLLAMA_ENABLED=true`, `OLLAMA_BASE_URL` |
| OpenAI Assistants | `OPENAI_API_KEY` | `OPENAI_ASSISTANT_MODEL` | `OPENAI_ASSISTANT_ID` |

</details>

---

## Três modos de operação

<table>
<tr>
<td width="33%" valign="top">

### CLI Interativa

Terminal inteligente com TUI (Bubble Tea), contexto de projeto, tool calling e agentes autônomos.

```bash
chatcli
chatcli -p "Explique este repo"
git diff | chatcli -p "Resuma"
```

</td>
<td width="33%" valign="top">

### Servidor gRPC

Backend compartilhado com TLS 1.3, JWT/RBAC, fallback, métricas Prometheus, MCP e discovery de plugins.

```bash
chatcli server --port 50051 \
  --token meu-token
chatcli connect \
  --server host:50051 \
  --token meu-token
```

</td>
<td width="33%" valign="top">

### Kubernetes Operator

Pipeline AIOps autônomo com 17 CRDs, 54+ ações de remediação, SLO monitoring e post-mortems.

```bash
helm install chatcli-operator \
  oci://ghcr.io/diillson/charts/chatcli-operator \
  --namespace aiops-system \
  --create-namespace
```

</td>
</tr>
</table>

<details>
<summary><strong>Comandos contextuais (modo CLI)</strong></summary>

Injete dados do ambiente diretamente no prompt:

| Comando | O que faz |
|---|---|
| `@git` | Status, branches e commits recentes |
| `@file <path>` | Conteúdo de arquivos/diretórios |
| `@env` | Variáveis de ambiente |
| `@history` | Últimos comandos do shell |
| `@command <cmd>` | Executa e injeta a saída |

</details>

<details>
<summary><strong>Exemplo de manifesto Kubernetes (Instance CRD)</strong></summary>

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

## Provedores suportados

> 13 provedores com interface unificada. Fallback automático com classificação inteligente de erros, extended thinking cross-provider e cache de prompt onde disponível.

| Provider | Default Model | Tool Calling | Vision | Reasoning / Thinking |
|---|---|---|---|---|
| **OpenAI** | gpt-5.4 | Nativo | Sim | `reasoning_effort` (o-series / gpt-5) |
| **Anthropic (Claude)** | claude-sonnet-4-6 | Nativo | Sim | Extended thinking com cache |
| **AWS Bedrock** | claude-sonnet-4-6 | Nativo | Sim | Thinking budget (Anthropic models) |
| **Google Gemini** | gemini-2.5-flash | Nativo | Sim | — |
| **xAI (Grok)** | grok-4-1 | XML fallback | — | — |
| **ZAI (Zhipu AI)** | glm-5 | Nativo | Sim | — |
| **MiniMax** | MiniMax-M2.7 | Nativo | Sim | — |
| **GitHub Copilot** | gpt-4o | Nativo | Sim | — |
| **GitHub Models** | gpt-4o | Nativo | Sim | — |
| **StackSpot AI** | StackSpotAI | — | — | — |
| **OpenRouter** | openai/gpt-4o | Nativo | Sim | Passthrough |
| **Ollama** | (local) | XML fallback | — | Tags `<thinking>` normalizadas |
| **OpenAI Assistants** | gpt-4o | Assistants API | — | — |

```bash
# Fallback chain configurável
CHATCLI_FALLBACK_PROVIDERS=OPENAI,CLAUDEAI,BEDROCK,ZAI,MINIMAX,OPENROUTER
```

`/thinking on|off|auto` ativa extended thinking / reasoning_effort em qualquer provider que suporte — o mapeamento cross-provider é automático.

---

## Agentes autônomos

> Motor ReAct (Reason + Act) com **14 agentes especializados** executando em paralelo.

```bash
/coder "Refatore o módulo auth para usar JWT"
chatcli -p "Crie testes para o pacote utils" --agent-auto-exec
```

| Agente | Responsabilidade |
|---|---|
| **File** | Leitura, escrita e manipulação de arquivos |
| **Coder** | Geração e edição de código |
| **Shell** | Execução de comandos no sistema |
| **Git** | Operações de versionamento |
| **Search** | Busca em código e arquivos |
| **Planner** | Decomposição de tarefas complexas (Plan-and-Solve / ReWOO) |
| **Reviewer** | Code review automatizado |
| **Tester** | Geração e execução de testes |
| **Refactor** | Refatoração segura de código |
| **Diagnostics** | Análise e debug de problemas |
| **Formatter** | Formatação e linting |
| **Deps** | Gerenciamento de dependências |
| **Refiner** | Self-Refine post-hook (critique → revise) |
| **Verifier** | Chain-of-Verification (perguntas + resposta final) |

Workers são coordenados pelo **dispatcher** com semáforo configurável (`CHATCLI_AGENT_MAX_WORKERS`), política de retry e sincronização por `FileLockManager`.

---

## Harness/Quality Pipeline

> Sete padrões de prompting e execução compostos por uma pipeline pluggable com state machine, hot reload e isolamento por hook.

| # | Padrão | Status | Opt-in |
|---|---|---|---|
| 1 | **ReAct** (Reason + Act) | ✅ core do agente | — |
| 2 | **Plan-and-Solve / ReWOO** | ✅ | `/plan`, `CHATCLI_QUALITY_PLAN_FIRST_MODE` |
| 3 | **Reflexion** (com fila durável) | ✅ | ligada por padrão |
| 4 | **RAG + HyDE** | ✅ | `CHATCLI_QUALITY_HYDE_ENABLED=1` |
| 5 | **Self-Refine** (com convergência semântica) | ✅ | `CHATCLI_QUALITY_REFINE_ENABLED=1` |
| 6 | **Chain-of-Verification** (CoVe) | ✅ | `CHATCLI_QUALITY_VERIFY_ENABLED=1` |
| 7 | **Reasoning backbone** cross-provider | ✅ | `CHATCLI_QUALITY_REASONING_MODE=auto` |

### Arquitetura do Pipeline

- **State machine** (Active → Draining → Closed) com transições via CAS atômico.
- **Copy-on-Write** via `atomic.Pointer[snapshot]` — `AddPre/AddPost/SwapConfig` atômicos, zero lock no hot path.
- **Isolamento por hook**: panic recovery, timeout enforcement (default 30s), circuit breaker (5 falhas → open por 30s).
- **Priority-based ordering** via interface opcional `Prioritized` (backward compat — hooks sem prioridade ficam em 100).
- **Short-circuit sentinels**: `ErrSkipExecution` (cache-hit antes do `agent.Execute`) e `ErrSkipRemainingHooks` (ensemble patterns).
- **Graceful shutdown** com `DrainAndClose(timeout)` respeitando in-flight.

### Reflexion durável (WAL + DLQ)

Triggers de reflexion (erro, alucinação detectada pelo CoVe, baixa qualidade) passam por uma fila de lessons com garantia enterprise — lições sobrevivem a crash do processo:

- **WAL** com CRC32 duplo, atomic rename, dir fsync — detecta torn writes automaticamente.
- **Worker pool** (default 2) com per-job timeout, exponential backoff + jitter, `MaxAttempts` configurável.
- **DLQ** persistente (mesmo formato WAL) com subcomandos `/reflect failed`, `/reflect retry <id>`, `/reflect purge <id>`.
- **Drain-on-boot**: lições pendentes de uma sessão anterior são reprocessadas automaticamente.
- **Idempotência** via `sha256(task | trigger | attempt)` — re-trigger da mesma situação é no-op.
- **Stale discard** (default 7d) — lições velhas descartadas no replay.

```bash
/reflect list              # fila atual + DLQ
/reflect failed            # DLQ com último erro por entrada
/reflect retry <job-id>    # reenfileira uma lição que falhou
/reflect purge <job-id>    # remove definitivamente da DLQ
/reflect drain             # força replay do WAL
```

### Convergência semântica (Self-Refine)

O Self-Refine usa cascade char → Jaccard → embedding para detectar quando parar iterando. Resolve "same meaning, different words" que o heurístico char-level não pegava:

| Etapa | Custo | Quando dispara |
|---|---|---|
| **Char** | μs | Sempre. Early-exit quando sim > 0.99 (idêntico) ou sim < 0.3 (divergiu) |
| **Jaccard** | ms | Borderline, sets de tokens normalizados com stop-words PT/EN |
| **Embedding** | ms + $ | Borderline pós-Jaccard. Opt-in via `CHATCLI_QUALITY_REFINE_CONVERGENCE_EMBEDDING=1` |

- **Cache LRU com TTL** (default 256 entries / 5min) evita chamar embedder duas vezes pelo mesmo texto.
- **Circuit breaker** por scorer — provider fora do ar degrada pra Jaccard sem travar refine.
- **Quality regression detection**: se pass N piora (>15% sim loss vs melhor) → reverte pro melhor draft visto + marca `refine_rolled_back` pra Reflexion aprender.
- **Modo strict**: recusa declarar convergência sem embedding quando a stakes for alta.

<details>
<summary><strong>Config completo do quality pipeline</strong></summary>

```bash
# Master switch
CHATCLI_QUALITY_ENABLED=true

# Self-Refine (#5) + convergência semântica
CHATCLI_QUALITY_REFINE_ENABLED=false            # opt-in
CHATCLI_QUALITY_REFINE_MAX_PASSES=1
CHATCLI_QUALITY_REFINE_CONVERGENCE_ENABLED=true
CHATCLI_QUALITY_REFINE_CONVERGENCE_EMBEDDING=false
CHATCLI_QUALITY_REFINE_CONVERGENCE_STRICT=false

# Chain-of-Verification (#6)
CHATCLI_QUALITY_VERIFY_ENABLED=false
CHATCLI_QUALITY_VERIFY_NUM_QUESTIONS=3
CHATCLI_QUALITY_VERIFY_REWRITE=true

# Reflexion (#3) + fila durável
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

Todos expostos no `/config quality` com estado em tempo real (hooks registrados, queue depth, DLQ size).

</details>

---

## Observabilidade

> Prometheus end-to-end em namespace `chatcli`. 50+ métricas cobrindo LLM, agentes, pipeline, queue e fila de lições.

```bash
chatcli server --port 50051 --metrics-port 9090
curl http://localhost:9090/metrics | grep chatcli_
curl http://localhost:9090/healthz
```

### Métricas principais

| Subsystem | Métrica | Tipo |
|---|---|---|
| `chatcli_llm_*` | `requests_total`, `request_duration_seconds`, `tokens_used_total`, `errors_total` | Counter, Histogram |
| `chatcli_quality_pipeline_*` | `dispatch_total`, `hook_duration_seconds`, `hook_errors_total`, `hook_circuit_state`, `generation` | Counter, Histogram, Gauge |
| `chatcli_lessonq_*` | `enqueue_total`, `queue_depth`, `dlq_size`, `processing_duration_seconds`, `wal_corruption_total`, `retry_total` | Counter, Gauge, Histogram |
| `chatcli_session_*` | duração, comandos executados, sinais | Counter, Gauge |
| `chatcli_grpc_*` | unary + stream interceptors | Counter, Histogram |

Collectors padrão do Go runtime e `process_*` também registrados automaticamente.

---

## Enterprise Security

> Segurança não é um feature flag. É a fundação de cada camada do ChatCLI.

<table>
<tr>
<td width="50%" valign="top">

**Autenticação e autorização**
- JWT com RBAC (admin / user / readonly)
- OAuth PKCE + Device Flow (RFC 8628)
- Token refresh automático por provider

**Criptografia**
- AES-256-GCM para credenciais at rest
- TLS 1.3 para comunicação gRPC
- Sessões encriptadas em disco

**Rede**
- Prevenção de SSRF integrada
- Rate limiting por client/endpoint
- Webhook validation no operator

</td>
<td width="50%" valign="top">

**Plugin e agent security**
- Verificação de assinatura Ed25519 para plugins
- Agent command allowlist (150+ comandos aprovados)
- Schema validation em plugin discovery

**Auditoria e compliance**
- Structured audit logging (JSON Lines)
- Cost tracking por sessão e provider
- Prometheus metrics para observabilidade

**CI/CD security**
- `govulncheck` + `gosec` em cada PR
- Trivy image scanning automatizado
- Cosign signature nas releases + SBOM CycloneDX

</td>
</tr>
</table>

<details>
<summary><strong>Autenticação OAuth integrada</strong></summary>

```
/auth login openai-codex       # OAuth PKCE + callback local
/auth login anthropic          # OAuth PKCE + code manual
/auth login github-copilot     # Device Flow (RFC 8628)
/auth status                   # Status de todos os providers
```

Credenciais armazenadas com **AES-256-GCM** em `~/.chatcli/auth-profiles.json`.

</details>

---

## Referência de comandos

| Categoria | Comandos |
|---|---|
| **Core** | `/help` · `/version` · `/reload` · `/exit` · `/reset` |
| **Sessões** | `/session {save,load,list,delete,new,fork}` · `/newsession` · `/rewind` |
| **Contexto** | `/context {create,attach,list,remove}` · `@git` · `@file` · `@env` · `@history` · `@command` |
| **Config** | `/config [section]` · `/status` · `/settings` · `/switch <provider\|model>` |
| **Modo agente** | `/agent [task]` · `/run` · `/coder` · `/plan [query]` |
| **Quality pipeline** | `/thinking [on\|off\|auto]` · `/refine [draft]` · `/verify [answer]` · `/reflect [list\|failed\|retry\|purge\|drain\|<texto>]` |
| **Memória** | `/memory {record,list,search,clear}` · `/compact [ratio]` |
| **Extensibilidade** | `/mcp {init,list,invoke,config}` · `/plugin {list,load,unload}` · `/skill <name>` · `/hooks {list,enable,disable,test}` |
| **Remoto** | `/auth {login,logout,status}` · `/connect <server>` · `/disconnect` |
| **Ferramentas** | `/watch {pid\|file}` · `/worktree {create,list,remove}` · `/channel {create,switch}` · `/websearch <query>` |
| **Diagnóstico** | `/metrics` · `/cost` |

---

## Funcionalidades

> Cada feature foi projetada para compor com as demais. Plugins descobrem skills. Hooks acionam tools. Contextos alimentam agentes.

| Feature | Descrição |
|---|---|
| **Tool calling nativo** | APIs nativas de OpenAI, Anthropic, Bedrock, Google, ZAI, MiniMax, OpenRouter. Cache `ephemeral` para Anthropic. XML fallback automático para providers sem suporte nativo. |
| **MCP (Model Context Protocol)** | Client via stdio e SSE para contexto expandido. |
| **Contextos persistentes** | `/context create`, `/context attach` — injeta projetos inteiros no system prompt com cache hints. |
| **Bootstrap e Memória** | `SOUL.md`, `USER.md`, `IDENTITY.md`, `RULES.md` + memória de longo prazo com facts e decay. |
| **Plugins** | Auto-detecção, schema validation, assinatura Ed25519, plugins remotos. |
| **Skills** | Registry multi-source (skills.sh, ClawHub, ChatCLI.dev), busca fuzzy, auditorias de segurança, preferências e instalação atômica. |
| **Personas customizáveis** | Markdown com frontmatter YAML (model, tools, skills). |
| **Hooks** | PreToolUse, PostToolUse, SessionStart/End, UserPromptSubmit, Compact pre/post — shell ou webhook. |
| **WebFetch / WebSearch** | DuckDuckGo + fetch com extração de texto. |
| **Cost tracking** | Custo por sessão com pricing tables por provider. |
| **Git Worktrees** | Trabalho isolado em branches paralelas. |
| **K8s Watcher** | Multi-target: metrics, logs, events, Prometheus scraping. |
| **i18n** | Português e Inglês com detecção automática. |
| **Session management** | Save, load, fork, export. |

---

## Arquitetura

```
chatcli/
  cli/
    agent/
      quality/              Pipeline 7 patterns (state machine + COW snapshots)
        convergence/        Semantic convergence (char → jaccard → embedding)
        lessonq/            Reflexion durable queue (WAL + worker pool + DLQ)
      workers/              14 agentes + dispatcher + FileLockManager
    hooks/                  Lifecycle events (shell/webhook)
    mcp/                    MCP client (stdio + SSE)
    plugins/                Plugin manager + signature verification
    workspace/memory/       Facts, topics, patterns, vector index (HyDE)
    tui/                    Bubble Tea adapters
  llm/
    openai/  openai_responses/  openai_assistant/
    claudeai/  bedrock/
    googleai/  xai/  zai/  minimax/
    copilot/  github_models/  stackspotai/  openrouter/  ollama/
    fallback/  catalog/  registry/  token/  toolshim/  embedding/
  metrics/                  Prometheus registry + /metrics + /healthz
  server/                   gRPC + TLS + JWT + MCP + Plugin discovery
  operator/                 Kubernetes Operator (17 CRDs, AIOps pipeline)
  k8s/                      Watcher (collectors, store, summarizer)
  models/                   ToolDefinition, ToolCall, LLMResponse, Message
  auth/                     OAuth PKCE, Device Flow, AES-256-GCM store
  config/                   ConfigManager com migração versionada
  i18n/                     embed.FS + golang.org/x/text (PT / EN)
```

> **Princípio de design:** cada pacote define suas interfaces e se auto-registra no sistema. O `llm/` registry permite adicionar um novo provider implementando uma única interface. O pipeline de qualidade é pluggable via `AddPre`/`AddPost` com swap atômico. O operator coordena CRDs independentes via controller pattern.

---

## CI/CD & Releases

- **CI** (`.github/workflows/1-ci.yml`): golangci-lint, gofmt, `go vet`, `go test -race -coverprofile`, coverage HTML como artifact.
- **Security scan** (`security-scan.yml`): Trivy image scanning contínuo.
- **Release automation** (`release-please` + `publish-release.yml`): multi-platform builds, assinaturas cosign, SBOM CycloneDX, publish em ArtifactHub.
- **Makefile**: `make build`, `make test`, `make lint`, `make install` com injeção de `Version`, `CommitHash`, `BuildDate` via ldflags.

---

## Contribuição

1. Fork o repositório
2. Crie uma branch a partir da `main`: `git checkout -b feature/minha-feature`
3. Commit e push
4. Abra um Pull Request

Veja [`docs/`](docs/) para guias detalhados de arquitetura, quality pipeline e operator.

---

## Licença

[Apache License 2.0](LICENSE)

---

<p align="center">
  <a href="https://chatcli.edilsonfreitas.com"><strong>Documentação</strong></a> &bull;
  <a href="https://github.com/diillson/chatcli/releases"><strong>Releases</strong></a> &bull;
  <a href="https://artifacthub.io/packages/search?ts_query_web=chatcli&sort=relevance&page=1"><strong>Helm Charts</strong></a> &bull;
  <a href="https://pkg.go.dev/github.com/diillson/chatcli"><strong>Go Reference</strong></a> &bull;
  <a href="https://github.com/diillson/chatcli/issues"><strong>Issues</strong></a>
</p>
