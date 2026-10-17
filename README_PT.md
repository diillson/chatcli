<p align="center">
  <a href="https://chatcli.edilsonfreitas.com/pt">
    <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli.png" alt="ChatCLI" width="280">
  </a>
</p>

<h1 align="center">ChatCLI</h1>

<p align="center">
  <strong>Um binário para IA em todo lugar em que você trabalha:<br>
  o terminal, o seu editor, os seus apps de chat e webhooks, um servidor gRPC e o Kubernetes.</strong>
</p>

<p align="center">
  <sub>14 providers de LLM · um agente coder autônomo · um harness de qualidade com sete padrões<br>
  Mixture-of-Agents com revisão cruzada · uma dash ao vivo no estilo APM</sub>
</p>

<p align="center">
  <a href="https://chatcli.edilsonfreitas.com/pt"><img src="https://img.shields.io/static/v1?label=%F0%9F%93%96%20Leia%20a%20documenta%C3%A7%C3%A3o&message=https%3A%2F%2Fchatcli.edilsonfreitas.com&color=0d9488&style=for-the-badge" alt="Leia a documentação em https://chatcli.edilsonfreitas.com"></a><br>
  <a href="https://chatcli.edilsonfreitas.com/pt"><strong>https://chatcli.edilsonfreitas.com</strong></a><br>
  <sub>O site oficial: guias, cookbooks, configuração e a referência completa de cada recurso, em português e inglês.</sub>
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
  <a href="https://chatcli.edilsonfreitas.com/pt"><strong>Documentação</strong></a> ·
  <a href="#começo-rápido"><strong>Começo rápido</strong></a> ·
  <a href="#destaques"><strong>Destaques</strong></a> ·
  <a href="#segurança"><strong>Segurança</strong></a> ·
  <a href="CONTRIBUTING.md"><strong>Contribuindo</strong></a> ·
  <a href="README.md"><strong>English</strong></a>
</p>

<p align="center">
  <img src="https://raw.githubusercontent.com/diillson/chatcli/main/assets/chatcli-demo.gif" alt="ChatCLI em ação" width="820">
</p>

## Por que o ChatCLI

- **Todos os grandes modelos, uma interface.** Troque de provider ou modelo no meio da conversa, faça failover automático e acompanhe custo, tokens e uso de cache de prompt.
- **Um agente que faz o trabalho.** O `/coder` lê, edita, executa e testa código sob uma política de segurança que você controla, com checkpoints que dá para desfazer.
- **Respostas mais confiáveis.** Self-Refine, Chain-of-Verification, Reflexion, RAG com HyDE e Mixture-of-Agents com revisão cruzada conferem o que os modelos dizem.
- **O mesmo cérebro em todo lugar.** O terminal, uma UI no navegador, editores via ACP, qualquer cliente MCP, Telegram, Slack, Discord, WhatsApp, webhooks, um servidor gRPC e um operator do Kubernetes compartilham sessões, memória e tools.
- **Feito para rodar onde importa.** Releases assinadas, padrões que falham fechado, confiança de workspace, logs de auditoria e um processo de segurança publicado.

## Começo rápido

```bash
# Homebrew (macOS, Linux)
brew tap diillson/chatcli && brew install chatcli

# ou Go
go install github.com/diillson/chatcli@latest

# ou um binário assinado da página de releases
# https://github.com/diillson/chatcli/releases/latest
```

Aponte para um modelo, com API key ou login OAuth:

```bash
export LLM_PROVIDER=OPENAI OPENAI_API_KEY=sk-...   # ou CLAUDEAI, GOOGLEAI, BEDROCK, XAI, OLLAMA, …
chatcli                                            # depois /auth login <provider> para OAuth
```

E então:

```bash
chatcli -p "Explique este repositório"              # one-shot, funciona com pipes
git diff | chatcli -p "Escreva a mensagem de commit"
chatcli                                            # interativo
  > /coder adicione testes no pacote utils          # o coder autônomo
  > /moa --layers 2 Devemos migrar para o Karpenter? # vários modelos, com revisão cruzada
  > /dash                                          # acompanhe tudo ao vivo
```

O ChatCLI se mantém atualizado com `/update` (Homebrew, `go install` ou auto-substituição com assinatura verificada). [Guia de instalação →](https://chatcli.edilsonfreitas.com/pt/start/installation)

## Destaques

<table>
<tr>
<td width="50%" valign="top">

### 🤖 Agente coder
Um loop ReAct com 46 tools builtin: arquivos, busca, shell, git, testes, diagnósticos de LSP, um navegador de verdade, forges Git, processos em segundo plano. Toda chamada passa por uma política allow/ask/deny; as edições ganham checkpoint num repositório sombra; um sandbox opcional do sistema operacional confina os comandos. [Modo coder →](https://chatcli.edilsonfreitas.com/pt/usage/coder-mode)

</td>
<td width="50%" valign="top">

### 🧠 Mixture-of-Agents com revisão cruzada
O `/moa` pergunta a vários modelos ao mesmo tempo. Com `--layers 2`, cada modelo lê as respostas dos outros e revisa a sua, e a síntese mantém uma seção com o que ainda diverge. A IA também pode chamá-lo, como `@moa`. [MoA →](https://chatcli.edilsonfreitas.com/pt/agents/mixture-of-agents)

</td>
</tr>
<tr>
<td valign="top">

### ✅ Harness de qualidade
Sete padrões, combináveis e observáveis: ReAct, Plan-and-Solve, Reflexion com fila durável, RAG + HyDE, Self-Refine com convergência semântica, Chain-of-Verification e um backbone de raciocínio entre providers. [Harness →](https://chatcli.edilsonfreitas.com/pt/agents/harness/overview)

</td>
<td valign="top">

### 📈 Dash ao vivo, como um APM
O `/dash` desenha o runtime inteiro como um mapa de serviços, em todos os processos ChatCLI da máquina: quem chamou quem, do turno ao agente, à tool, ao modelo e ao host, com custo, latência, erros, decisões de política e fallbacks. Só metadados. [Dash ao vivo →](https://chatcli.edilsonfreitas.com/pt/usage/live-dashboard)

</td>
</tr>
<tr>
<td valign="top">

### 👥 Squads e task graphs
Workers especialistas rodam em paralelo sob um dispatcher; o `@taskgraph` transforma um plano aprovado num DAG verificado, em que o próprio engine roda as checagens de cada tarefa e um revisor novo dá o veredito. [Task graph →](https://chatcli.edilsonfreitas.com/pt/agents/task-graph)

</td>
<td valign="top">

### 🔌 MCP, ACP e plugins
Um cliente MCP (stdio, SSE, HTTP com OAuth) e um servidor MCP que reexporta tudo; um servidor ACP para editores; plugins assinados; plugins de provider que adicionam um provider de LLM inteiro; skills de vários registries; slash commands compatíveis com outras nove CLIs. [Extensões →](https://chatcli.edilsonfreitas.com/pt/extensions/plugin-system)

</td>
</tr>
<tr>
<td valign="top">

### 💬 Gateway de chat
Telegram, Slack, Discord, WhatsApp e webhooks rodam o mesmo agente, com áudios transcritos offline e respondidos em voz, allowlists de remetentes que falham fechado e uma conversa única entre canais. [Gateway →](https://chatcli.edilsonfreitas.com/pt/gateway/chat-gateway)

</td>
<td valign="top">

### ☸️ Kubernetes e AIOps
Um servidor gRPC com TLS 1.3, JWT e papéis, e um operator com 17 CRDs que observa workloads, abre incidentes, propõe e aplica remediações com aprovação e escreve post-mortems. [Kubernetes →](https://chatcli.edilsonfreitas.com/pt/kubernetes/k8s-operator)

</td>
</tr>
<tr>
<td valign="top">

### 📄 Documentos e mídia, em todo provider
PDFs e imagens chegam a todos os modelos, de forma nativa onde o modelo lê e como texto onde não lê; geração de imagens, transcrição e síntese de voz offline, diagramas e um grafo de conhecimento interativo. [Entrada de PDF →](https://chatcli.edilsonfreitas.com/pt/usage/pdf-input)

</td>
<td valign="top">

### 🗂️ Memória e contexto
Memória de longo prazo com perfil e fatos, base de conhecimento com busca BM25 sem API key, compressão reversível em que nada se perde (o `@recall` pagina originais longos) e sessões que continuam entre superfícies. [Contexto →](https://chatcli.edilsonfreitas.com/pt/context/bootstrap-memory)

</td>
</tr>
<tr>
<td valign="top">

### ⏱️ Scheduler
Jobs duráveis com cron, condições de espera e dependências; um daemon os mantém rodando com o terminal fechado; os agentes podem se estacionar e acordar numa condição. [Scheduler →](https://chatcli.edilsonfreitas.com/pt/tools/scheduler)

</td>
<td valign="top">

### 🧪 Evals
O `chatcli eval` roda suítes YAML contra o binário real num sandbox, avaliadas por checagens determinísticas e por um juiz LLM, com pass@k, baselines e códigos de saída prontos para CI. [Evals →](https://chatcli.edilsonfreitas.com/pt/agents/harness/evals)

</td>
</tr>
</table>

## Onde ele roda

| Superfície | Como iniciar | |
|---|---|---|
| Terminal interativo | `chatcli` | chat, `/coder`, `/agent`, `/moa`, todos os slash commands |
| One-shot e pipes | `chatcli -p "…"` | scripts, CI, `git diff \| chatcli -p …` |
| UI no navegador | `/web` ou `chatcli web` | a mesma sessão do terminal |
| Editores (ACP) | `chatcli acp` | Zed e outros clientes ACP |
| Qualquer cliente MCP | `chatcli mcp-server` | todas as tools, os loops de agente, skills como prompts |
| Apps de chat | `/gateway start` | Telegram, Slack, Discord, WhatsApp, webhooks |
| Backend compartilhado | `chatcli server` | gRPC com TLS 1.3, JWT e papéis |
| Kubernetes | chart Helm `chatcli-operator` | operator de AIOps |
| Dash ao vivo | `/dash` ou `chatcli dash` | todos os processos ChatCLI da máquina |

## Providers

14 providers embutidos atrás de uma interface, com cadeia de fallback, controle de raciocínio entre providers (`/thinking`) e cache de prompt onde o provider oferece. Plugins de provider adicionam mais sem recompilar o ChatCLI.

| Provider | `LLM_PROVIDER` | Autenticação |
|---|---|---|
| OpenAI | `OPENAI` | `OPENAI_API_KEY`, ou OAuth com `/auth login openai-codex` (Responses API) |
| OpenAI Assistants | `OPENAI_ASSISTANT` | `OPENAI_API_KEY` |
| Anthropic Claude | `CLAUDEAI` | `ANTHROPIC_API_KEY`, ou OAuth com `/auth login anthropic` |
| AWS Bedrock | `BEDROCK` | perfil AWS, SSO, `aws login` ou a cadeia de credenciais |
| Google Gemini | `GOOGLEAI` | `GOOGLEAI_API_KEY` |
| xAI Grok | `XAI` | `XAI_API_KEY` |
| Z.AI (GLM) | `ZAI` | `ZAI_API_KEY`, incluindo o GLM Coding Plan |
| Moonshot Kimi | `MOONSHOT` | `MOONSHOT_API_KEY` |
| MiniMax | `MINIMAX` | `MINIMAX_API_KEY` |
| OpenRouter | `OPENROUTER` | `OPENROUTER_API_KEY` |
| GitHub Copilot | `COPILOT` | `/auth login github-copilot` (device flow) ou `GITHUB_COPILOT_TOKEN` |
| Ollama | `OLLAMA` | local, sem chave |
| StackSpot AI | `STACKSPOT` | `CLIENT_ID` e `CLIENT_KEY` |
| Devin (Cognition) | `DEVIN` | a CLI local do Devin |

Modelos, preços e janelas de contexto vêm de um catálogo vivo: o `/switch --model` lista o que cada conta realmente atende. [Modelos suportados →](https://chatcli.edilsonfreitas.com/pt/providers/supported-models) · [Todas as configurações →](https://chatcli.edilsonfreitas.com/pt/reference/environment-variables)

## Segurança

O ChatCLI executa comandos e guarda credenciais, então segurança é o padrão, não uma opção:

- **Tools sob política**: toda ação do agente é permitida, perguntada ou negada por uma política que você controla; superfícies sem ninguém para aprovar recusam em vez de aprovar.
- **Confiança de workspace**: o `.env`, os hooks e a política de um projeto só carregam depois do `chatcli trust`.
- **Integrações que falham fechado**: os canais de chat não sobem sem allowlist de remetentes; webhooks assinados são verificados com HMAC.
- **Criptografia em repouso por padrão**: conversas, memória, contextos, anexos e credenciais são cifrados com AES-256-GCM, com a chave no keychain do sistema (macOS, Linux, Windows) ou, no Kubernetes, num Secret; o OAuth usa PKCE.
- **Cadeia de suprimentos assinada**: checksums das releases assinados com Ed25519 e verificados pelo `chatcli update`, imagens assinadas com Cosign, SBOM, e Trivy, CodeQL, gosec e govulncheck em toda mudança.

Reporte vulnerabilidades de forma privada: veja o [SECURITY.md](SECURITY.md). [Visão geral de segurança →](https://chatcli.edilsonfreitas.com/pt/security/overview)

## Documentação

| | |
|---|---|
| **Começar** | [Instalação](https://chatcli.edilsonfreitas.com/pt/start/installation) · [Quickstart](https://chatcli.edilsonfreitas.com/pt/start/quickstart) · [Uso básico](https://chatcli.edilsonfreitas.com/pt/usage/basic-usage) |
| **Agentes** | [Modo coder](https://chatcli.edilsonfreitas.com/pt/usage/coder-mode) · [Squads](https://chatcli.edilsonfreitas.com/pt/agents/agent-squad) · [Task graph](https://chatcli.edilsonfreitas.com/pt/agents/task-graph) · [Mixture-of-Agents](https://chatcli.edilsonfreitas.com/pt/agents/mixture-of-agents) · [Harness](https://chatcli.edilsonfreitas.com/pt/agents/harness/overview) |
| **Contexto** | [Memória](https://chatcli.edilsonfreitas.com/pt/context/bootstrap-memory) · [Base de conhecimento](https://chatcli.edilsonfreitas.com/pt/context/knowledge-base) · [Compressão](https://chatcli.edilsonfreitas.com/pt/context/context-compression) · [Sessões](https://chatcli.edilsonfreitas.com/pt/context/session-management) |
| **Estender** | [Plugins](https://chatcli.edilsonfreitas.com/pt/extensions/plugin-system) · [Plugins de provider](https://chatcli.edilsonfreitas.com/pt/extensions/provider-plugins) · [MCP](https://chatcli.edilsonfreitas.com/pt/extensions/mcp-integration) · [Skills](https://chatcli.edilsonfreitas.com/pt/extensions/skill-registry) · [Hooks](https://chatcli.edilsonfreitas.com/pt/extensions/hooks-system) · [Slash commands](https://chatcli.edilsonfreitas.com/pt/extensions/slash-commands) |
| **Rodar** | [Modo servidor](https://chatcli.edilsonfreitas.com/pt/server/server-mode) · [Gateway](https://chatcli.edilsonfreitas.com/pt/gateway/chat-gateway) · [Operator Kubernetes](https://chatcli.edilsonfreitas.com/pt/kubernetes/k8s-operator) · [Docker](https://chatcli.edilsonfreitas.com/pt/start/docker-deployment) |
| **Referência** | [Comandos](https://chatcli.edilsonfreitas.com/pt/reference/command-reference) · [Variáveis de ambiente](https://chatcli.edilsonfreitas.com/pt/reference/environment-variables) · [Arquitetura](https://chatcli.edilsonfreitas.com/pt/reference/architecture) · [Releases](https://chatcli.edilsonfreitas.com/pt/releases) |

## Comunidade

- **Contribua**: leia o [CONTRIBUTING.md](CONTRIBUTING.md) para o setup, as convenções e o que o CI verifica.
- **Conduta**: todos que participam seguem o [Código de Conduta](CODE_OF_CONDUCT.md).
- **Segurança**: reporte vulnerabilidades de forma privada como descrito no [SECURITY.md](SECURITY.md).
- **Bugs e ideias**: [abra uma issue](https://github.com/diillson/chatcli/issues).

Se o ChatCLI é útil para você, uma ⭐ ajuda outras pessoas a encontrá-lo.

## Licença

[Apache License 2.0](LICENSE) © Edilson Freitas
