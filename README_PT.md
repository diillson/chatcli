<p align="center">
  <a href="https://chatcli.edilsonfreitas.com">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

<h1 align="center">ChatCLI</h1>
<p align="center">
  <strong>Plataforma de IA unificada para terminal, servidor gRPC e Kubernetes.</strong><br>
  <sub>14 provedores · 14 agentes autônomos · pipeline de qualidade em 7 padrões · um único binário.</sub>
</p>

<div align="center">

<a href="https://github.com/diillson/chatcli/actions/workflows/1-ci.yml"><img src="https://github.com/diillson/chatcli/actions/workflows/1-ci.yml/badge.svg" alt="CI"/></a>
<a href="https://github.com/diillson/chatcli/actions/workflows/security-scan.yml"><img src="https://github.com/diillson/chatcli/actions/workflows/security-scan.yml/badge.svg" alt="Security Scan"/></a>
<a href="https://github.com/diillson/chatcli/releases"><img src="https://img.shields.io/github/v/release/diillson/chatcli" alt="Release"/></a>
<a href="https://artifacthub.io/packages/search?ts_query_web=chatcli&sort=relevance&page=1"><img src="https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/chatcli" alt="ArtifactHub"/></a>
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
  <a href="README.md">English</a> &bull;
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
| **Multi-provider com fallback** | 14 provedores de LLM (OpenAI · OpenAI Responses · Anthropic · Bedrock · Google · xAI · ZAI · MiniMax · Moonshot (Kimi) · Copilot · GitHub Models · StackSpot · OpenRouter · Ollama), com classificação inteligente de erros, backoff exponencial e cooldown por provider. |
| **Agentes autônomos** | 14 workers builtin coordenados por motor ReAct (Reason + Act): 12 specialists de orquestração executam em paralelo + 2 de qualidade (refiner, verifier), com pipeline de qualidade em 7 padrões. |
| **Quality pipeline** | Self-Refine, Chain-of-Verification (CoVe), Reflexion, RAG + HyDE, Plan-and-Solve (ReWOO), backbone de reasoning cross-provider — todos compostos por state machine thread-safe com circuit breakers e hot reload. |
| **Scheduler (Chronos)** | Agendamento durável com cron + wait-until + DAG + daemon mode. `/schedule`, `/wait`, `/jobs` + tool `@scheduler` para agents. WAL CRC32, snapshots, rate limiter, circuit breakers, audit JSONL, 13 métricas Prometheus. Jobs sobrevivem a crash e a fechar o CLI. |
| **Reflexion durável** | Fila WAL-backed com worker pool, dead letter queue, replay on boot, retry exponencial com jitter — lições sobrevivem a crash do processo. |
| **Convergência semântica** | Cascade char → Jaccard → embedding cosine para Self-Refine, com cache LRU/TTL e quality regression detection. |
| **Production-ready** | gRPC + TLS 1.3, JWT + RBAC, AES-256-GCM, rate limiting, audit logging, 50+ métricas Prometheus. |
| **Kubernetes-native** | Operador com 17 CRDs e pipeline AIOps autônomo (54+ ações de remediação), SLO monitoring, post-mortems. |
| **Extensível** | Plugins com verificação Ed25519, skills multi-registry (skills.sh, ClawHub, ChatCLI.dev), templates de slash commands com interop de 9 CLIs (Claude Code, Devin, Gemini, Codex, …), hooks de lifecycle, MCP client (stdio, SSE, HTTP + OAuth). |

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
LLM_PROVIDER=OPENAI    # OPENAI, CLAUDEAI, BEDROCK, GOOGLEAI, XAI, ZAI, MINIMAX, MOONSHOT,
                       # COPILOT, GITHUB_MODELS, OLLAMA, STACKSPOT, OPENROUTER
OPENAI_API_KEY=sk-xxx
```

<details>
<summary><strong>Referência completa de variáveis por provider</strong></summary>

| Provider | API Key | Model | Extras |
|---|---|---|---|
| OpenAI | `OPENAI_API_KEY` | `OPENAI_MODEL` | `OPENAI_MAX_TOKENS`, `OPENAI_USE_RESPONSES`, `OPENAI_API_URL` |
| Anthropic | `ANTHROPIC_API_KEY` | `ANTHROPIC_MODEL` | `ANTHROPIC_MAX_TOKENS` |
| AWS Bedrock | IAM / Profile / credentials chain | `BEDROCK_MODEL` | `AWS_REGION`, `BEDROCK_CROSS_REGION` |
| Google Gemini | `GOOGLEAI_API_KEY` | `GOOGLEAI_MODEL` | `GOOGLEAI_MAX_TOKENS` |
| xAI | `XAI_API_KEY` | `XAI_MODEL` | `XAI_MAX_TOKENS` |
| ZAI | `ZAI_API_KEY` | `ZAI_MODEL` | `ZAI_MAX_TOKENS` |
| MiniMax | `MINIMAX_API_KEY` | `MINIMAX_MODEL` | `MINIMAX_MAX_TOKENS` |
| Moonshot (Kimi) | `MOONSHOT_API_KEY` | `MOONSHOT_MODEL` | `MOONSHOT_MAX_TOKENS`, `MOONSHOT_THINKING` |
| GitHub Copilot | `GITHUB_COPILOT_TOKEN` | `COPILOT_MODEL` | ou `/auth login github-copilot` |
| GitHub Models | `GITHUB_TOKEN` | `GITHUB_MODELS_MODEL` | `GH_TOKEN`, `GITHUB_MODELS_TOKEN` |
| StackSpot | `CLIENT_ID`, `CLIENT_KEY` | — | `STACKSPOT_REALM`, `STACKSPOT_AGENT_ID` |
| OpenRouter | `OPENROUTER_API_KEY` | — | `OPENROUTER_MAX_TOKENS`, `OPENROUTER_FALLBACK_MODELS`, `OPENROUTER_API_URL` |
| Ollama | — | `OLLAMA_MODEL` | `OLLAMA_ENABLED=true`, `OLLAMA_BASE_URL` |
| OpenAI (Responses API) | `OPENAI_API_KEY` | `OPENAI_MODEL` | `OPENAI_RESPONSES_API_URL` |

#### Endpoints customizados e gateways compatíveis com OpenAI

- `OPENAI_API_URL` sobrescreve o endpoint de chat completions da OpenAI. Deve ser a URL **completa** de chat completions (ex.: `https://gateway.example.com/v1/chat/completions`) — a URL de listagem `/models` é derivada dela.
- A autenticação não muda: as requisições levam `Authorization: Bearer $OPENAI_API_KEY`, então ao redirecionar para um gateway configure `OPENAI_API_KEY` com a key **do gateway**. Não combine URL de terceiros com login OAuth (`/auth login openai`) — o token OAuth seria enviado ao gateway.
- `OPENAI_RESPONSES_API_URL` sobrescreve o endpoint da Responses API. Atenção: `OPENAI_USE_RESPONSES=false` não força chat completions — logins OAuth e modelos cuja entrada no catálogo prefere a Responses API (ex.: `gpt-5.4`, o default) continuam usando-a. Precedência efetiva: OAuth > `OPENAI_USE_RESPONSES=true` > preferência do catálogo do modelo > `OPENAI_USE_RESPONSES=false`. Ao redirecionar o provider OpenAI para um endpoint compatível, configure **as duas** URLs.
- Quando a URL do endpoint aponta para um host customizado, a listagem de modelos **não** é filtrada por família — todos os modelos que o gateway retornar em `/models` aparecem no autocomplete e no `/switch --model`. Contra o endpoint oficial, a listagem mantém apenas as famílias de chat (ocultando embeddings, whisper, tts, dall-e, moderation). A mesma regra vale para `ZAI_API_URL`, `MOONSHOT_API_URL` e `MINIMAX_API_URL`.
- Para usar um gateway compatível com OpenAI como provider **separado** da OpenAI (inclusive na fallback chain do modo servidor), aponte o preset OpenRouter para ele: `LLM_PROVIDER=OPENROUTER` com `OPENROUTER_API_KEY` e `OPENROUTER_API_URL=https://gateway.example.com/v1/chat/completions`.

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
  --namespace chatcli-system \
  --create-namespace
```

</td>
</tr>
</table>

### Scheduler autônomo (Chronos)

O scheduler roda embutido no CLI e, opcionalmente, como daemon. Jobs sobrevivem a reinícios via WAL + snapshot.

```bash
# Dispara um comando em 30s
/schedule ping --when +30s --do "/run curl https://api.example.com/health"

# Cron diário com retry
/schedule backup --cron "0 2 * * *" --do "shell: ./backup.sh" --max-retries 3

# Deploy + wait K8s + trigger smoke
/schedule deploy --when +0s --do "shell: terraform apply -auto-approve" \
  --wait "k8s:deployment/prod/api:Available" --timeout 15m \
  --triggers smoke-tests

# Daemon para rodar sozinho com o CLI fechado
chatcli daemon start --detach
chatcli daemon status

# Listar / inspecionar / cancelar
/jobs list
/jobs show <id>
/jobs tree
/jobs cancel <id>
```

Agents ganham a tool `@scheduler` e podem se auto-pausar esperando condições — veja [Cookbook: automações com Scheduler](https://chatcli.edilsonfreitas.com/cookbook/scheduler-automations) e [doc da feature](https://chatcli.edilsonfreitas.com/features/scheduler).

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

> 14 provedores com interface unificada. Fallback automático com classificação inteligente de erros, extended thinking cross-provider e cache de prompt onde disponível.

| Provider | Default Model | Tool Calling | Vision | Reasoning / Thinking |
|---|---|---|---|---|
| **OpenAI** | gpt-5.4 | Nativo | Sim | `reasoning_effort` (o-series / gpt-5) |
| **Anthropic (Claude)** | claude-sonnet-4-6 | Nativo | Sim | Extended thinking com cache |
| **AWS Bedrock** | claude-sonnet-4-5 | Nativo | Sim | Thinking budget (Anthropic models) |
| **Google Gemini** | gemini-2.5-flash | Nativo | Sim | — |
| **xAI (Grok)** | grok-4-1 | XML fallback | — | — |
| **ZAI (Zhipu AI)** | glm-5 | Nativo | Sim | — |
| **MiniMax** | MiniMax-M2.7 | Nativo | Sim | — |
| **Moonshot (Kimi)** | kimi-k2.6 | Nativo | Sim | `MOONSHOT_THINKING=enabled\|disabled\|auto` |
| **GitHub Copilot** | gpt-4o | Nativo | Sim | — |
| **GitHub Models** | gpt-4o | Nativo | Sim | — |
| **StackSpot AI** | StackSpotAI | — | — | — |
| **OpenRouter** | openai/gpt-5.2 | Nativo | Sim | Passthrough |
| **Ollama** | (local) | XML fallback | — | Tags `<thinking>` normalizadas |
| **OpenAI (Responses API)** | gpt-5.4 | Nativo | Sim | `reasoning_effort` |

```bash
# Fallback chain configurável
CHATCLI_FALLBACK_PROVIDERS=OPENAI,CLAUDEAI,BEDROCK,ZAI,MINIMAX,MOONSHOT,OPENROUTER
```

`/thinking on|off|auto` ativa extended thinking / reasoning_effort em qualquer provider que suporte — o mapeamento cross-provider é automático.

---

## Agentes autônomos

> Motor ReAct (Reason + Act) com **14 agents builtin**: 12 specialists de orquestração executando em paralelo (`file, coder, shell, git, search, planner, reviewer, tester, refactor, diagnostics, formatter, deps`) + 2 do harness de qualidade (`refiner`, `verifier`).

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
| **Sessões** | `/session {save,load,list,delete,new,fork,search}` · `/export` · `/newsession` · `/rewind` |
| **Contexto** | `/context {create,attach,list,remove}` · `@git` · `@file` · `@env` · `@history` · `@command` |
| **Config** | `/config [section]` · `/status` · `/settings` · `/switch <provider\|model>` |
| **Modo agente** | `/agent [task]` · `/run` · `/coder` · `/plan [query]` · `/moa <prompt>` |
| **Quality pipeline** | `/thinking [on\|off\|auto]` · `/refine [draft]` · `/verify [answer]` · `/reflect [list\|failed\|retry\|purge\|drain\|<texto>]` |
| **Memória & grafo** | `/memory {longterm,list,profile,facts,remember,forget,profile set,compact}` · `@memory` (remember/recall/forget/profile/neighbors/map) — perfil com ciclo de vida: campos de lista fazem upsert (reafirmar um item supera o antigo, não duplica) e sufixos `_replace`/`_done`/`_remove` reescrevem (ex.: `goals_done=` remove o objetivo concluído; registre junto `milestone=` e `certifications=`); novos campos `interests`, `directives` (regras duras vs preferências; escopo por projeto com `"[scope:<projeto>] regra"` — injetada só quando o workspace correspondente está ativo), `milestone` (linha do tempo datada), `stance` (posição técnica com o porquê, `"posição :: razão"`) e `env_<chave>` (ambiente estruturado); proveniência+frescor por campo (user vs extraction, reafirmação atualiza `confirmed_at`, campos velhos são sinalizados como possivelmente desatualizados) e camada de privacidade (chaves financeiras/saúde/família auto-marcadas `[sensitive]`: personalizam respostas mas nunca entram em código/exemplos/artefatos; `sensitive_mark`/`sensitive_unmark`); daily notes consolidam em digests semanais e mensais (seção Trajectory no contexto); atualização de perfil funciona também no chat (exceção sancionada, `/config chat memory`, `CHATCLI_CHAT_MEMORY`) · `/graph [assunto]` · `/compact [ratio]` |
| **Extensibilidade** | `/mcp {init,list,invoke,config}` · `/plugin {list,load,unload}` · `/skill <name>` · `/hooks {list,enable,disable,test}` |
| **Mensageria & Servidores** | `/gateway {start,status}` (Telegram/Slack/Discord/WhatsApp/webhook) · `chatcli mcp-server` · `chatcli acp` |
| **Remoto** | `/auth {login,logout,status}` · `/connect <server>` · `/disconnect` |
| **Ferramentas** | `/watch {pid\|file}` · `/worktree {create,list,remove}` · `/channel {create,switch}` · `/websearch <query>` · `/lsp <arquivo>` |
| **Scheduler** | `/schedule <nome> --when <t> --do <a>` · `/wait --until <cond>` · `/jobs {list,show,tree,cancel,pause,resume,logs,daemon}` · `chatcli daemon {start,stop,status,ping,install}` |
| **Diagnóstico** | `/metrics` · `/cost` · `/ratelimit` (`/limits`) |

---

## Funcionalidades

> Cada feature foi projetada para compor com as demais. Plugins descobrem skills. Hooks acionam tools. Contextos alimentam agentes.

| Feature | Descrição |
|---|---|
| **Tool calling nativo** | APIs nativas de OpenAI, Anthropic, Bedrock, Google, ZAI, MiniMax, Moonshot, OpenRouter. Cache `ephemeral` para Anthropic. XML fallback automático para providers sem suporte nativo. |
| **MCP (Model Context Protocol)** | Client via stdio, SSE e streamable HTTP para contexto expandido. Servidores remotos (HTTP/SSE) protegidos por OAuth 2.1 têm suporte fim-a-fim: num `401` o client descobre o authorization server (RFC 9728/8414), registra-se dinamicamente (RFC 7591), roda o fluxo PKCE no navegador e renova tokens de forma transparente (guardados com AES-256-GCM no cofre de auth criptografado). Autorize com `/mcp login <servidor>`, ou deixe o agente chamar a tool `@mcp-login` quando uma chamada reportar "autorização necessária". Server (`chatcli mcp-server`) expõe chat, agent, coder e built-in tools — e também toda tool descoberta dos servidores MCP aos quais o próprio ChatCLI está conectado, re-exportada com seu nome `mcp_*` e o JSON Schema original intacto (ChatCLI como hub MCP: vários servidores agregados num único endpoint, com notificações `tools/list_changed` conforme conectam). Política de exposição via `CHATCLI_MCP_TOOLS` (all/safe/allowlist; `safe` honra o `readOnlyHint` de origem, allowlists nomeiam tools proxied como `mcp_<tool>`). Clients também ganham `manage_session` (save/load/list/delete/clear/search/fork das conversas por trás do parâmetro `session` do `ask_chatcli`, compartilhando o store do `/session`) e `list_providers` com modelos vivos da API mesclados ao catálogo — a mesma descoberta do picker interativo. **Paridade de experiência completa**: `ask_chatcli` roda o MESMO pipeline de um turno interativo — memória longa e perfil do usuário, anexos de `/context` por sessão, skills fixadas e ativadas por trigger, retrieval de knowledge e compactação token-aware (`plain=true` mantém o passthrough cru) — e `agent_task` roda o loop ReAct real com contextos por sessão. O estado local é navegável como **MCP resources** sob URIs `chatcli://` (índice/longterm/perfil/projetos da memória, contextos, TOC + documentos paginados de knowledge, skills com triggers, sessões salvas; `CHATCLI_MCP_RESOURCES=off` desativa). O servidor entra no **hub de conversas em modo resume**, então um fio iniciado no REPL ou num canal do gateway continua de qualquer client MCP (`CHATCLI_MCP_HUB=off` desativa, `CHATCLI_MCP_HUB_PRINCIPAL` isola). Comandos perigosos auto-aprovam como no gateway daemon; use `CHATCLI_MCP_DANGER=block` para recusá-los in-band. Modo ACP (`chatcli acp`) para editores. |
| **Chat Gateway** | Roda como daemon de mensageria (Telegram, Slack, Discord, WhatsApp, webhook): cada mensagem passa pelo agent loop e o progresso é transmitido de volta ao chat. Mensagens de voz são transcritas (whisper local-first) e respondidas em voz por padrão (`CHATCLI_GATEWAY_VOICE_REPLY=auto\|always\|never`); cada conversa controla isso pedindo em linguagem natural ("responde em áudio" / "para de mandar áudio") via tool `@voice`, com preferência persistida. |
| **Voz embarcada (TTS)** | `CHATCLI_TTS_PROVIDER=embedded` — voz neural Kokoro offline, sem API key e sem cgo: baixa o engine sherpa-onnx + modelo uma única vez (~150MB) e funciona igual em Linux/macOS/Windows. Roteia pt-BR/inglês por idioma da resposta (`CHATCLI_TTS_VOICE=bm_george`, `CHATCLI_TTS_VOICE_PT=pm_alex`); demais backends (say/espeak, self-hosted, OpenAI/Groq/Gemini) seguem disponíveis. |
| **Transcrição embarcada (STT)** | Whisper multilíngue offline via sherpa-onnx, sem API key e sem cgo — é o fallback automático: sem nada configurado, o gateway baixa o engine + modelo ONNX uma única vez (~200MB no `base`; `CHATCLI_TRANSCRIPTION_MODEL=tiny\|base\|small\|…`) já no startup e transcreve notas de voz detectando o idioma falado. Voice notes OGG/Opus (Telegram/WhatsApp) são decodificadas em puro Go — sem ffmpeg; só formatos residuais (mp3/m4a) pedem ffmpeg, e o preflight do gateway + `/gateway status` avisam com o comando de instalação da sua plataforma. `CHATCLI_TRANSCRIPTION_PROVIDER=embedded` força-o sobre outros backends (whisper CLI local, self-hosted, Groq/OpenAI), que seguem disponíveis. |
| **Mixture-of-Agents** | `/moa` — vários modelos propõem em paralelo e um agregador sintetiza (Wang et al., 2406.04692). Cada participante recebe o mesmo briefing de um turno de chat (contextos anexados, memória do workspace, skills) mais recuperação read-only de knowledge, recall de CCR e recall da memória de longo prazo. |
| **Diagnósticos LSP** | `/lsp <arquivo>` — erros/avisos do compilador via Language Server Protocol (gopls, pyright, rust-analyzer, clangd, …). |
| **Rate limits** | `/ratelimit` — limites do provider parseados dos headers `x-ratelimit-*` (requests/tokens, % usado, reset). |
| **Exportar trajetória** | `/export` — conversa atual como JSONL ShareGPT para fine-tuning/análise. |
| **Contextos persistentes** | `/context create`, `/context attach` — injeta projetos inteiros no system prompt com cache hints. |
| **Knowledge base (RAG keyless)** | `/context create docs corpus.jsonl --mode knowledge` — corpora de docs ou de código/infra (ex.: JSONL da tool builtin `@docs-flatten`, que achata Markdown/MDX e — com `kind=code` — código-fonte, Terraform e YAML Kubernetes/Argo locais ou de um repo git, chunkando por estrutura) viram base de conhecimento: o attach injeta só um index card (~900 tokens fixos, mesmo com 6MB+) e trechos relevantes são recuperados por turno via BM25 puro-Go (sem API key) + embeddings quando configurados. A tool `@knowledge` (search/get/toc) consulta a base iterativamente no agent/coder e também no chat (exceção read-only, `/config chat knowledge`) — inclusive pra criar skills a partir da doc com `@skill`. |
| **Bootstrap e Memória** | `SOUL.md`, `USER.md`, `IDENTITY.md`, `RULES.md` + memória de longo prazo com facts (confiança + proveniência + reconciliação de contradições), tópicos com resumo rolante e decay. |
| **Auto-evolução (self-evolution)** | Skills se auto-criam e evoluem na própria passada de extração de memória (sem chamada extra de LLM): procedimentos reutilizáveis viram skills que auto-ativam; uma melhoria evolui a skill existente por merge aditivo, com backup reversível (`@skill restore`). `CHATCLI_SELFEVOLVE_MODE=off\|suggest\|auto`; observabilidade em `/config selfevolve`. |
| **Grafo de conhecimento (Obsidian no core)** | Facts, tópicos, projetos, skills e tags viram um grafo derivado on-demand: `@memory neighbors <assunto>` / `map` puxam backlinks e notas conectadas, um index card minúsculo entra por turno, e `/graph [assunto]` renderiza o grafo em imagem (go-graphviz embarcado). `CHATCLI_GRAPH_INDEX=on\|off`. |
| **Plugins** | Auto-detecção, schema validation, assinatura Ed25519, plugins remotos. |
| **Skills** | Auto-autoria (`@skill`), registry multi-source (skills.sh, ClawHub, ChatCLI.dev), busca fuzzy, auditorias de segurança, preferências e instalação atômica. |
| **Slash commands** | Templates de prompt em markdown invocados como `/nome args` em TODAS as superfícies (REPL, coder mid-run, one-shot, gateway, ACP, MCP prompts) e com qualquer provider — a expansão é pura reescrita de prompt. Projeto `.chatcli/commands/` + pessoal `~/.chatcli/commands/`, com **interop de migração zero** para arquivos de comando do Claude Code, Devin, Windsurf, Cursor, opencode, Codex, Gemini CLI, Qwen Code e GitHub Copilot (incluindo seus dialetos de placeholder e TOML). Pré-execução `!` passa pelo gate de segurança do coder; `allowed-tools` escopa o run; o modelo descobre o catálogo via `@commands`. Um command que precisa de tools declara `mode: coder` (inferido automaticamente quando há `allowed-tools`) e, invocado no chat, é **auto-roteado por um run one-shot do coder** — executa e volta ao chat em vez de ser recusado pelo chat mode sem tools (`mode: chat` veta a inferência; opt-out global com `CHATCLI_COMMANDS_AUTOROUTE=off`). Painel em `/config commands`. |
| **Personas customizáveis** | Markdown com frontmatter YAML (model, tools, skills). |
| **Hooks** | PreToolUse, PostToolUse, SessionStart/End, UserPromptSubmit, Compact pre/post — shell ou webhook. |
| **WebFetch / WebSearch** | DuckDuckGo + fetch com extração de texto. |
| **Cost tracking** | Uso real de API em todos os providers, `/cost` (+ `reset`, `last`, `sessions`, `export`), orçamento de sessão com hard stop opcional, snapshots persistidos. |
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
    mcp/                    MCP client (stdio, SSE, HTTP + OAuth)
    plugins/                Plugin manager + signature verification
    scheduler/              Chronos — scheduler durável (WAL + cron + DAG + daemon)
      condition/            10 evaluators (shell, http, k8s, docker, tcp, llm, ...)
      action/               8 executors (slash, shell, agent, webhook, ...)
      builtins/             Registro agregado de evaluators + executors
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
