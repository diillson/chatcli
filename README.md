<p align="center">
  <a href="https://chatcli.edilsonfreitas.com">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

<h1 align="center">ChatCLI</h1>
<p align="center"><strong>Plataforma multi-provider de IA para terminal, servidor e Kubernetes</strong></p>

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
  <a href="README_EN.md">English</a> &bull;
  <a href="https://chatcli.edilsonfreitas.com">Documentacao completa com todas funcionalidade e comandos</a>
</p>

---

<p align="center">
  <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli-demo.gif" alt="ChatCLI Demo" width="800">
</p>

## O que e o ChatCLI?

CLI, servidor gRPC e operador Kubernetes que conecta **11 provedores de LLM** a uma interface unificada com agentes autonomos, tool calling nativo, fallback automatico e pipeline AIOps completo.

### Provedores suportados

| Provider | Default Model | Tool Calling | Vision |
|---|---|---|---|
| **OpenAI** | gpt-5.4 | Nativo | Sim |
| **Anthropic (Claude)** | claude-sonnet-4-6 | Nativo | Sim |
| **Google Gemini** | gemini-2.5-flash | Nativo | Sim |
| **xAI (Grok)** | grok-4-1 | XML fallback | - |
| **ZAI (Zhipu AI)** | glm-5 | Nativo | Sim |
| **MiniMax** | MiniMax-M2.7 | Nativo | Sim |
| **GitHub Copilot** | gpt-4o | Nativo | Sim |
| **GitHub Models** | gpt-4o | Nativo | Sim |
| **StackSpot AI** | StackSpotAI | - | - |
| **Ollama** | (local) | XML fallback | - |
| **OpenAI Assistants** | gpt-4o | Assistants API | - |

---

## Instalacao

```bash
# Homebrew (macOS / Linux)
brew tap diillson/chatcli && brew install chatcli

# Go install
go install github.com/diillson/chatcli@latest

# Binarios pre-compilados
# https://github.com/diillson/chatcli/releases
```

<details>
<summary>Compilacao a partir do codigo-fonte</summary>

```bash
git clone https://github.com/diillson/chatcli.git && cd chatcli
go mod tidy && go build -o chatcli

# Com informacoes de versao
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
go build -ldflags "-X github.com/diillson/chatcli/version.Version=${VERSION}" -o chatcli
```
</details>

---

## Configuracao rapida

Crie um arquivo `.env` na raiz ou exporte as variaveis:

```bash
LLM_PROVIDER=OPENAI          # OPENAI, CLAUDEAI, GOOGLEAI, XAI, ZAI, MINIMAX, COPILOT, OLLAMA, STACKSPOT
OPENAI_API_KEY=sk-xxx         # Chave do provider escolhido
```

<details>
<summary>Variaveis de todos os providers</summary>

| Provider | API Key | Model | Extras |
|---|---|---|---|
| OpenAI | `OPENAI_API_KEY` | `OPENAI_MODEL` | `OPENAI_MAX_TOKENS`, `OPENAI_USE_RESPONSES` |
| Anthropic | `ANTHROPIC_API_KEY` | `ANTHROPIC_MODEL` | `ANTHROPIC_MAX_TOKENS` |
| Google Gemini | `GOOGLEAI_API_KEY` | `GOOGLEAI_MODEL` | `GOOGLEAI_MAX_TOKENS` |
| xAI | `XAI_API_KEY` | `XAI_MODEL` | `XAI_MAX_TOKENS` |
| ZAI | `ZAI_API_KEY` | `ZAI_MODEL` | `ZAI_MAX_TOKENS` |
| MiniMax | `MINIMAX_API_KEY` | `MINIMAX_MODEL` | `MINIMAX_MAX_TOKENS` |
| GitHub Copilot | `GITHUB_COPILOT_TOKEN` | `COPILOT_MODEL` | ou `/auth login github-copilot` |
| GitHub Models | `GITHUB_TOKEN` | `GITHUB_MODELS_MODEL` | `GH_TOKEN`, `GITHUB_MODELS_TOKEN` |
| StackSpot | `CLIENT_ID`, `CLIENT_KEY` | - | `STACKSPOT_REALM`, `STACKSPOT_AGENT_ID` |
| Ollama | - | `OLLAMA_MODEL` | `OLLAMA_ENABLED=true`, `OLLAMA_BASE_URL` |

</details>

---

## Tres modos de operacao

### 1. CLI interativa

```bash
chatcli                              # Modo interativo
chatcli -p "Explique este repo"      # One-shot
git diff | chatcli -p "Resuma"       # Pipe stdin
```

**Comandos contextuais** — injete dados direto no prompt:

| Comando | O que faz |
|---|---|
| `@git` | Status, branches e commits recentes |
| `@file <path>` | Conteudo de arquivos/diretorios |
| `@env` | Variaveis de ambiente |
| `@history` | Ultimos comandos do shell |
| `@command <cmd>` | Executa e injeta a saida |

### 2. Servidor gRPC

```bash
chatcli server --port 50051 --token meu-token

# Cliente remoto
chatcli connect --server host:50051 --token meu-token
```

Fallback automatico, TLS, Prometheus metrics, MCP, discovery de plugins/agents/skills.

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

Pipeline AIOps autonomo: deteccao de anomalias, correlacao de issues, analise AI, remediacao automatizada, post-mortems e SLO monitoring. 17 CRDs, 54+ acoes de remediacao.

```bash
# Helm install
helm install chatcli oci://ghcr.io/diillson/charts/chatcli \
  --namespace chatcli --create-namespace \
  --set llm.provider=OPENAI --set secrets.openaiApiKey=sk-xxx

helm install chatcli-operator oci://ghcr.io/diillson/charts/chatcli-operator \
  --namespace aiops-system --create-namespace
```

---

## Funcionalidades principais

### Modo Agente

Motor ReAct (Reason + Act) com **12 agentes especializados** executando em paralelo: File, Coder, Shell, Git, Search, Planner, Reviewer, Tester, Refactor, Diagnostics, Formatter, Deps.

```bash
/agent "Refatore o modulo auth para usar JWT"
chatcli -p "Crie testes para o pacote utils" --agent-auto-exec
```

### Tool calling nativo

Chamadas de ferramentas via API estruturada da OpenAI, Anthropic, Google, ZAI e MiniMax. Cache `ephemeral` para Anthropic. XML fallback automatico para providers sem suporte nativo.

### OAuth integrado

```
/auth login openai-codex       # OAuth PKCE + callback local
/auth login anthropic          # OAuth PKCE + code manual
/auth login github-copilot     # Device Flow (RFC 8628)
/auth status                   # Status de todos os providers
```

Credenciais armazenadas com **AES-256-GCM** em `~/.chatcli/auth-profiles.json`.

### Fallback de provedores

```bash
CHATCLI_FALLBACK_PROVIDERS=OPENAI,CLAUDEAI,ZAI,MINIMAX
```

Classificacao de erros (rate limit, timeout, auth, context overflow), backoff exponencial e cooldown por provider.

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

### Mais funcionalidades

| Feature | Descricao |
|---|---|
| **Contextos persistentes** | `/context create`, `/context attach` — injeta projetos inteiros no system prompt com cache hints |
| **Bootstrap e Memoria** | `SOUL.md`, `USER.md`, `IDENTITY.md`, `RULES.md` + memoria de longo prazo com facts e decay |
| **Plugins** | Sistema extensivel com auto-deteccao, schema validation e plugins remotos |
| **Skills** | Registry multi-source com busca fuzzy, moderacao e instalacao atomica |
| **Agentes customizaveis** | Personas em Markdown com frontmatter YAML (model, tools, skills) |
| **Hooks** | Lifecycle events (PreToolUse, PostToolUse, SessionStart) com shell commands e webhooks |
| **WebFetch / WebSearch** | Busca DuckDuckGo e fetch de paginas com extracao de texto |
| **Cost tracking** | Custo por sessao com pricing tables por provider |
| **Git Worktrees** | Trabalho isolado em branches paralelas |
| **K8s Watcher** | Monitoramento multi-target com metricas, logs, events e Prometheus scraping |
| **i18n** | Interface em Portugues e Ingles com deteccao automatica |
| **Session management** | Save, load, fork e export de conversas |

---

## Arquitetura

```
chatcli/
  cli/            Interface TUI (Bubble Tea), modo agente, multi-agent workers
  llm/            11 providers, registry auto-registro, fallback chain, catalog
  server/         Servidor gRPC com TLS, auth, metrics e MCP
  operator/       Kubernetes Operator — 17 CRDs, pipeline AIOps autonomo
  k8s/            Watcher (collectors, store, summarizer)
  models/         Tipos compartilhados (ToolDefinition, ToolCall, LLMResponse)
  auth/           OAuth PKCE, Device Flow, token refresh, store encriptado
  config/         ConfigManager com migracao versionada
  i18n/           Internacionalizacao (embed.FS + golang.org/x/text)
```

---

## Contribuicao

1. Fork o repositorio
2. Crie uma branch: `git checkout -b feature/minha-feature`
3. Commit e push
4. Abra um Pull Request

---

## Licenca

[Apache License 2.0](LICENSE)

---

## Links

- **Documentacao completa**: [chatcli.edilsonfreitas.com](https://chatcli.edilsonfreitas.com)
- **Releases**: [github.com/diillson/chatcli/releases](https://github.com/diillson/chatcli/releases)
- **Helm Charts**: [ArtifactHub](https://artifacthub.io/packages/helm/chatcli/chatcli)
- **Issues**: [github.com/diillson/chatcli/issues](https://github.com/diillson/chatcli/issues)
