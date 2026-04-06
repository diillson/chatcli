<p align="center">
  <a href="https://chatcli.edilsonfreitas.com">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI Logo" width="300">
  </a>
</p>

<h1 align="center">ChatCLI</h1>
<p align="center">
  <strong>A plataforma de IA unificada para terminal, servidor e Kubernetes.</strong><br>
  <sub>11 provedores. 12 agentes autonomos. Um unico binario.</sub>
</p>

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
  <a href="https://chatcli.edilsonfreitas.com">Documentacao completa</a>
</p>

---

<p align="center">
  <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli-demo.gif" alt="ChatCLI Demo" width="800">
</p>

<br>

> **ChatCLI** conecta os maiores modelos de linguagem do mercado a uma interface unica e extensivel — do `chatcli -p` no seu terminal ate um operador Kubernetes com pipeline AIOps autonomo, passando por um servidor gRPC production-ready com autenticacao, fallback e metricas.

<br>

## Por que o ChatCLI?

| | |
|---|---|
| **Multi-provider de verdade** | 11 provedores de LLM com fallback automatico, backoff exponencial e cooldown inteligente. |
| **Agentes autonomos** | 12 agentes especializados com motor ReAct (Reason + Act) e execucao em paralelo. |
| **Production-ready** | gRPC + TLS, JWT + RBAC, AES-256-GCM, rate limiting, audit logging, Prometheus metrics. |
| **Kubernetes-native** | Operador com 17 CRDs e pipeline AIOps autonomo: 54+ acoes de remediacao automatizada. |
| **Extensivel** | Plugins com verificacao de assinatura, skills com busca fuzzy, hooks de lifecycle, MCP. |

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
<summary><strong>Compilacao a partir do codigo-fonte</strong></summary>

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
<summary><strong>Referencia completa de variaveis por provider</strong></summary>

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

O ChatCLI adapta-se ao seu contexto — da maquina de um desenvolvedor a um cluster Kubernetes em producao.

<br>

<table>
<tr>
<td width="33%" valign="top">

### CLI Interativa

Para desenvolvedores individuais. Terminal inteligente com TUI (Bubble Tea), contexto de projeto, tool calling e agentes.

```bash
chatcli
chatcli -p "Explique este repo"
git diff | chatcli -p "Resuma"
```

</td>
<td width="33%" valign="top">

### Servidor gRPC

Para equipes e plataformas. Servidor centralizado com TLS, autenticacao, fallback, metricas Prometheus, MCP e discovery de plugins.

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

Para operacoes em escala. Pipeline AIOps autonomo com 17 CRDs, 54+ acoes de remediacao, SLO monitoring e post-mortems automatizados.

```bash
helm install chatcli-operator \
  oci://ghcr.io/diillson/charts/chatcli-operator \
  --namespace aiops-system \
  --create-namespace
```

</td>
</tr>
</table>

<br>

<details>
<summary><strong>Comandos contextuais do modo CLI</strong></summary>

Injete dados do ambiente diretamente no prompt:

| Comando | O que faz |
|---|---|
| `@git` | Status, branches e commits recentes |
| `@file <path>` | Conteudo de arquivos/diretorios |
| `@env` | Variaveis de ambiente |
| `@history` | Ultimos comandos do shell |
| `@command <cmd>` | Executa e injeta a saida |

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

> 11 provedores com interface unificada. Fallback automatico entre providers com classificacao inteligente de erros.

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

```bash
# Fallback chain configuravel
CHATCLI_FALLBACK_PROVIDERS=OPENAI,CLAUDEAI,ZAI,MINIMAX
```

Classificacao de erros (rate limit, timeout, auth, context overflow), backoff exponencial e cooldown por provider.

---

## Agentes autonomos

> Motor ReAct (Reason + Act) com **12 agentes especializados** executando em paralelo.

```bash
/coder "Refatore o modulo auth para usar JWT"
chatcli -p "Crie testes para o pacote utils" --agent-auto-exec
```

Os agentes operam de forma coordenada, decompondo tarefas complexas em subtarefas e delegando para os workers mais adequados:

| Agente | Responsabilidade |
|---|---|
| **File** | Leitura, escrita e manipulacao de arquivos |
| **Coder** | Geracao e edicao de codigo |
| **Shell** | Execucao de comandos no sistema |
| **Git** | Operacoes de versionamento |
| **Search** | Busca em codigo e arquivos |
| **Planner** | Decomposicao de tarefas complexas |
| **Reviewer** | Code review automatizado |
| **Tester** | Geracao e execucao de testes |
| **Refactor** | Refatoracao segura de codigo |
| **Diagnostics** | Analise e debug de problemas |
| **Formatter** | Formatacao e linting |
| **Deps** | Gerenciamento de dependencias |

---

## Enterprise Security

> Segurança não é um feature flag. E a fundação de cada camada do ChatCLI.

O ChatCLI implementa segurança defense-in-depth, da autenticação ao armazenamento, da rede ao plugin system.

<table>
<tr>
<td width="50%" valign="top">

**Autenticação e autorizacao**
- JWT com RBAC (Role-Based Access Control)
- OAuth PKCE + Device Flow (RFC 8628)
- Token refresh automatico por provider

**Criptografia**
- AES-256-GCM para credenciais at rest
- TLS 1.3 para comunicacao gRPC
- Sessoes encriptadas em disco

**Rede**
- Prevencao de SSRF integrada
- Rate limiting por client/endpoint
- Webhook validation no operator

</td>
<td width="50%" valign="top">

**Plugin e agent security**
- Verificacao de assinatura Ed25519 para plugins
- Agent command allowlist (150+ comandos aprovados)
- Schema validation em plugin discovery

**Auditoria e compliance**
- Structured audit logging (JSON)
- Cost tracking por sessao e provider
- Prometheus metrics para observabilidade

**CI/CD security**
- `govulncheck` em pipeline CI
- `gosec` para analise estatica de seguranca
- Scanning automatico de dependencias

</td>
</tr>
</table>

<details>
<summary><strong>Autenticacao OAuth integrada</strong></summary>

```
/auth login openai-codex       # OAuth PKCE + callback local
/auth login anthropic          # OAuth PKCE + code manual
/auth login github-copilot     # Device Flow (RFC 8628)
/auth status                   # Status de todos os providers
```

Credenciais armazenadas com **AES-256-GCM** em `~/.chatcli/auth-profiles.json`.

</details>

---

## Funcionalidades

> Cada feature foi projetada para compor com as demais. Plugins descobrem skills. Hooks acionam tools. Contextos alimentam agentes.

| Feature | Descricao |
|---|---|
| **Tool calling nativo** | Chamadas estruturadas via API da OpenAI, Anthropic, Google, ZAI e MiniMax. Cache `ephemeral` para Anthropic. XML fallback automatico para providers sem suporte nativo. |
| **MCP (Model Context Protocol)** | Integracao com servidores MCP via stdio e SSE para contexto expandido. |
| **Contextos persistentes** | `/context create`, `/context attach` — injeta projetos inteiros no system prompt com cache hints. |
| **Bootstrap e Memoria** | `SOUL.md`, `USER.md`, `IDENTITY.md`, `RULES.md` + memoria de longo prazo com facts e decay. |
| **Plugins** | Sistema extensivel com auto-deteccao, schema validation, verificacao de assinatura e plugins remotos. |
| **Skills** | Registry multi-source com busca fuzzy, moderacao e instalacao atomica. |
| **Agentes customizaveis** | Personas em Markdown com frontmatter YAML (model, tools, skills). |
| **Hooks** | Lifecycle events (PreToolUse, PostToolUse, SessionStart) com shell commands e webhooks. |
| **WebFetch / WebSearch** | Busca DuckDuckGo e fetch de paginas com extracao de texto. |
| **Cost tracking** | Custo por sessao com pricing tables por provider. |
| **Git Worktrees** | Trabalho isolado em branches paralelas. |
| **K8s Watcher** | Monitoramento multi-target com metricas, logs, events e Prometheus scraping. |
| **i18n** | Interface em Portugues e Ingles com deteccao automatica. |
| **Session management** | Save, load, fork e export de conversas. |

<details>
<summary><strong>Exemplo de configuracao MCP</strong></summary>

```json
// ~/.chatcli/mcp_servers.json
{
  "servers": [
    {
      "name": "filesystem",
      "transport": "stdio",
      "command": "npx",
      "args": ["-y", "@anthropic/mcp-server-filesystem", "/workspace"]
    },
    {
      "name": "search",
      "transport": "sse",
      "url": "http://mcp-search:8080/sse"
    }
  ]
}
```

</details>

---

## Arquitetura

```
chatcli/
  cli/            Interface TUI (Bubble Tea), modo agente, multi-agent workers
  llm/            11 providers, registry auto-registro, fallback chain, catalog
  server/         Servidor gRPC com TLS, JWT auth, metrics e MCP
  operator/       Kubernetes Operator — 17 CRDs, pipeline AIOps autonomo
  k8s/            Watcher (collectors, store, summarizer)
  models/         Tipos compartilhados (ToolDefinition, ToolCall, LLMResponse)
  auth/           OAuth PKCE, Device Flow, token refresh, store encriptado (AES-256-GCM)
  config/         ConfigManager com migracao versionada
  i18n/           Internacionalizacao (embed.FS + golang.org/x/text)
```

> **Principio de design:** cada pacote define suas interfaces e se auto-registra no sistema. O `llm/` registry permite adicionar um novo provider implementando uma unica interface. O `operator/` coordena CRDs independentes via controller pattern.

---

## Contribuicao

1. Fork o repositorio
2. Crie uma branch a partir da `main`: `git checkout -b feature/minha-feature`
3. Commit e push
4. Abra um Pull Request

---

## Licenca

[Apache License 2.0](LICENSE)

---

<p align="center">
  <a href="https://chatcli.edilsonfreitas.com"><strong>Documentacao</strong></a> &bull;
  <a href="https://github.com/diillson/chatcli/releases"><strong>Releases</strong></a> &bull;
  <a href="https://artifacthub.io/packages/helm/chatcli/chatcli"><strong>Helm Charts</strong></a> &bull;
  <a href="https://github.com/diillson/chatcli/issues"><strong>Issues</strong></a>
</p>
