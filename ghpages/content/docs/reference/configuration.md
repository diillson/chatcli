+++
title = "Referência de Configuração (.env)"
linkTitle = "Configuração (.env)"
weight = 10
description = "Um guia completo de todas as variáveis de ambiente que você pode configurar em seu arquivo .env para personalizar o ChatCLI."
+++

O **ChatCLI** é amplamente configurável através de variáveis de ambiente. A maneira mais fácil de gerenciá-las é criando um arquivo `.env` na raiz do seu projeto ou no seu diretório `HOME`.

A ordem de prioridade para as configurações é:
1.  Flags de linha de comando (ex: `--provider`)
2.  Variáveis de Ambiente do Sistema
3.  Variáveis no arquivo `.env`
4.  Valores Padrão

---

## Configuração Geral

| Variável | Descrição                                                                                                                                         | Padrão                     |
| :--- |:--------------------------------------------------------------------------------------------------------------------------------------------------|:---------------------------|
| `ENV` | Define o ambiente, caso `dev` os logs são mostrados no terminal e salvo no log da app, caso `prod` somente no log. Valores válidos: `dev`, `prod` | `dev`                      
| `LLM_PROVIDER` | Define o provedor de IA padrão a ser usado. Valores válidos: `OPENAI`, `CLAUDEAI`, `GOOGLEAI`, `XAI`, `OLLAMA`, `STACKSPOT`, `COPILOT`.             | `"OPENAI"`                 |
| `CHATCLI_LANG` | Define o idioma da interface. Valores: `pt-BR`, `en-US`. Se não definida, tentará detectar o idioma do sistema.                                   | `en-US`                    |
| `LOG_LEVEL` | Nível dos logs. Opções: `debug`, `info`, `warn`, `error`.                                                                                         | `"info"`                   |
| `LOG_FILE` | Caminho para o arquivo de log. Padrão: `$HOME/.chatcli/app.log`                                                                                   | `"$HOME/.chatcli/app.log"` |
| `LOG_MAX_SIZE` | Tamanho máximo do arquivo de log antes da rotação. Aceita `100MB`, `50KB`, etc.                                                                   | `"100MB"`                  |
| `HISTORY_MAX_SIZE` | Tamanho máximo do arquivo de histórico (`.chatcli_history`) antes da rotação.                                                                     | `"100MB"`                  |
| `HISTORY_FILE` | Caminho personalizado para o arquivo de histórico (suporta `~`, hoje ele cria o histórico onde executou o chatcli).                               | `".chatcli_history"`       |
| `CHATCLI_DOTENV`| Caminho personalizado para o seu arquivo `.env`.                                                                                                  | `".env"`                   |
| `CHATCLI_IGNORE` | Caminho para arquivo de ignore (ex.: `.chatignore`). Quando definido, ele tem prioridade sobre o ignore do projeto/global.                        | `""`                       |
| `CHATCLI_CODER_UI` | Estilo da UI do modo `/coder`: `full` (padrão) ou `mínimal`.                                                                                    | `"full"`                   |
| `CHATCLI_CODER_BANNER` | Exibe o cheat sheet rápido do `/coder` na entrada da sessão (`true`/`false`).                                                            | `"true"`                   |

## Autenticação OAuth

Além das chaves de API tradicionais, o ChatCLI suporta **autenticação via OAuth** para OpenAI, Anthropic e GitHub Copilot. Com o OAuth, você pode usar seu plano existente (ChatGPT Plus, Codex, Claude Pro, GitHub Copilot) sem gerar API keys.

| Variável | Descrição | Padrão |
| :--- | :--- | :--- |
| `CHATCLI_AUTH_DIR` | Diretório onde as credenciais OAuth são armazenadas. | `~/.chatcli/` |
| `CHATCLI_OPENAI_CLIENT_ID` | Permite sobrescrever o client ID do OAuth da OpenAI. | (interno) |
| `CHATCLI_COPILOT_CLIENT_ID` | Permite sobrescrever o client ID do OAuth do GitHub Copilot. | (interno) |

As credenciais são armazenadas com **criptografia AES-256-GCM** em `~/.chatcli/auth-profiles.json`. A chave de criptografia é gerada automaticamente e salva em `~/.chatcli/.auth-key` (permissão 0600).

> Use `/auth login openai-codex`, `/auth login anthropic` ou `/auth login github-copilot` no modo interativo para iniciar o fluxo OAuth. Consulte a [documentação completa de OAuth](/docs/features/oauth-authentication/) para mais detalhes.

---

## Configuração de Provedores

### OpenAI

| Variável | Descrição                                                                        | Obrigatório? |
| :--- |:---------------------------------------------------------------------------------| :--- |
| `OPENAI_API_KEY` | Sua chave de API secreta da OpenAI. Alternativa: use `/auth login openai-codex` para OAuth. | **Sim*** |
| `OPENAI_MODEL` | O modelo a ser usado. Ex: `gpt-4o`, `gpt-4o-mini`, `gpt-4-turbo`.                | Não |
| `OPENAI_ASSISTANT_MODEL` | O modelo a ser usado específicamente para a API de Assistentes.                  | Não |
| `OPENAI_USE_RESPONSES` | Define `true` para usar a API de `v1/responses` em vez de `v1/chat/completions`. | Não |
| `OPENAI_MAX_TOKENS` | Define o maximo de tokens a ser utilizados na sessão (depende do modelo)         | Não

### Anthropic (Claude)

| Variável               | Descrição | Obrigatório? |
|:-----------------------| :--- | :--- |
| `ANTHROPIC_API_KEY`     | Sua chave de API secreta da Anthropic. Alternativa: use `/auth login anthropic` para OAuth. | **Sim*** |
| `ANTHROPIC_MODEL`       | O modelo a ser usado. Ex: `claude-sonnet-4-5`, `claude-opus-4-6`, `claude-sonnet-4`. | Não |
| `ANTHROPIC_API_VERSION` | A versão da API da Anthropic a ser usada nos cabeçalhos. | Não |
| `ANTHROPIC_MAX_TOKENS`  | Define o maximo de tokens a ser utilizados na sessão (depende do modelo)         | Não


### Google (Gemini)

| Variável              | Descrição | Obrigatório? |
|:----------------------| :--- | :--- |
| `GOOGLEAI_API_KEY`    | Sua chave de API do Google AI Studio. | **Sim** |
| `GOOGLEAI_MODEL`      | O modelo a ser usado. Ex: `gemini-2.5-pro`, `gemini-2.5-flash`. | Não |
| `GOOGLEAI_MAX_TOKENS` | Define o maximo de tokens a ser utilizados na sessão (depende do modelo)         | Não


### xAI (Grok)

| Variável         | Descrição | Obrigatório? |
|:-----------------| :--- | :--- |
| `XAI_API_KEY`    | Sua chave de API secreta da xAI. | **Sim** |
| `XAI_MODEL`      | O modelo a ser usado. Ex: `grok-4-fast`, `grok-3`. | Não |
| `XAI_MAX_TOKENS` | Define o maximo de tokens a ser utilizados na sessão (depende do modelo)         | Não


### Ollama (Local)

| Variável | Descrição                                                                                    | Obrigatório? |
| :--- |:---------------------------------------------------------------------------------------------| :--- |
| `OLLAMA_ENABLED` | Defina como `true` para habilitar o provedor Ollama.                                         | **Sim** |
| `OLLAMA_BASE_URL` | URL base do seu servidor Ollama local.                                                       | Não |
| `OLLAMA_MODEL` | O nome do modelo local a ser usado (ex: `llama3`, `codellama`).                              | Não |
| `OLLAMA_FILTER_THINKING` | Filtra raciocínio intermediário em respostas (ex.: para `Qwen3`, `llama3` padrão `true`...). | Não |
| `OLLAMA_MAX_TOKENS` | Define o máximo de tokens para o provedor Ollama. | Não |


### StackSpot

| Variável | Descrição | Obrigatório? |
| :--- | :--- | :--- |
| `CLIENT_ID` | Credencial de ID de cliente da StackSpot. | **Sim** |
| `CLIENT_KEY` | Credencial de chave de cliente da StackSpot. | **Sim** |
| `STACKSPOT_REALM` | O `realm` (tenant) da sua organização na StackSpot. | **Sim** |
| `STACKSPOT_AGENT_ID` | O ID do agente específico a ser utilizado. | **Sim** |

### GitHub Copilot

| Variável | Descrição | Obrigatório? |
| :--- | :--- | :--- |
| `GITHUB_COPILOT_TOKEN` | Token OAuth do GitHub Copilot. Alternativa: use `/auth login github-copilot` para Device Flow. | **Sim*** |
| `COPILOT_MODEL` | O modelo a ser usado. Ex: `gpt-4o`, `claude-sonnet-4`, `gemini-2.0-flash`. | Não |
| `COPILOT_MAX_TOKENS` | Define o máximo de tokens para a resposta. | Não |
| `COPILOT_API_BASE_URL` | URL base da API do Copilot (para ambientes enterprise). | Não |

> **\*** Para OpenAI, Anthropic e GitHub Copilot, a chave de API é obrigatória apenas se você **não** utilizar autenticação OAuth (`/auth login`). Ambos os métodos podem coexistir.

---

## Configuração do Modo Agente

| Variável | Descrição |
| :--- | :--- |
| `CHATCLI_AGENT_ALLOW_SUDO` | Defina como `"true"` para permitir que o agente sugira e execute comandos com `sudo`. **Use com extrema cautela.** |
| `CHATCLI_AGENT_DENYLIST` | Lista de padrões regex (separados por `;`) para bloquear comandos adicionais no modo agente. |
| `CHATCLI_AGENT_CMD_TIMEOUT` | Timeout para a execução de um único comando pelo agente (padrão: `10m`, máximo: `1h`). |
| `CHATCLI_AGENT_PLUGIN_MAX_TURNS` | Limite máximo de turnos do agente no modo `/agent`/`/coder` (padrão: `50`, máximo: `200`). |
| `CHATCLI_AGENT_PLUGIN_TIMEOUT` | Timeout total do plugin do agente (padrão: `15m`). |

### Multi-Agent (Orquestração Paralela)

| Variável | Descrição | Padrão |
| :--- | :--- | :--- |
| `CHATCLI_AGENT_PARALLEL_MODE` | Ativa o modo multi-agent com orquestração paralela. O LLM orquestrador despacha agents especialistas em paralelo. | `false` |
| `CHATCLI_AGENT_MAX_WORKERS` | Número máximo de workers (goroutines) executando agents simultaneamente. | `4` |
| `CHATCLI_AGENT_WORKER_MAX_TURNS` | Máximo de turnos do mini ReAct loop de cada worker agent. | `10` |
| `CHATCLI_AGENT_WORKER_TIMEOUT` | Timeout por worker agent individual. Aceita durações Go (ex: `30s`, `2m`, `10m`). | `5m` |

> Para detalhes completos sobre o sistema multi-agent, consulte a [documentação de Orquestração Multi-Agent](/docs/features/multi-agent-orchestration/).

---

## Configuração do Modo Servidor (`chatcli server`)

| Variável | Descrição | Padrão |
| :--- | :--- | :--- |
| `CHATCLI_SERVER_PORT` | Porta do servidor gRPC. | `50051` |
| `CHATCLI_SERVER_TOKEN` | Token de autenticação para o servidor. Vazio = sem autenticação. | `""` |
| `CHATCLI_SERVER_TLS_CERT` | Caminho para o certificado TLS do servidor. | `""` |
| `CHATCLI_SERVER_TLS_KEY` | Caminho para a chave TLS do servidor. | `""` |
| `CHATCLI_GRPC_REFLECTION` | Habilita gRPC reflection para debugging. **Mantenha desabilitado em produção.** | `false` |

---

## Fallback de Provedores

| Variável | Descrição | Padrão |
| :--- | :--- | :--- |
| `CHATCLI_FALLBACK_PROVIDERS` | Lista de provedores separados por vírgula para failover automático. Ex.: `OPENAI,CLAUDEAI,GOOGLEAI`. | `""` |
| `CHATCLI_FALLBACK_MODEL_<PROVIDER>` | Modelo específico por provedor na cadeia. Ex.: `CHATCLI_FALLBACK_MODEL_CLAUDEAI=claude-sonnet-4-20250514`. | (modelo padrão) |
| `CHATCLI_FALLBACK_MAX_RETRIES` | Tentativas por provedor antes de avançar para o próximo na cadeia. | `2` |
| `CHATCLI_FALLBACK_COOLDOWN_BASE` | Duração base do cooldown após falha de um provedor. | `30s` |
| `CHATCLI_FALLBACK_COOLDOWN_MAX` | Duração máxima do cooldown (backoff exponencial). | `5m` |

> Para detalhes completos, consulte a [documentação de Fallback de Provedores](/docs/features/provider-fallback/).

---

## MCP (Model Context Protocol)

| Variável | Descrição | Padrão |
| :--- | :--- | :--- |
| `CHATCLI_MCP_ENABLED` | Ativa o gerenciador de servidores MCP. | `false` |
| `CHATCLI_MCP_CONFIG` | Caminho para o arquivo JSON de configuração dos servidores MCP. | `~/.chatcli/mcp_servers.json` |

> Para detalhes completos, consulte a [documentação de MCP](/docs/features/mcp-integration/).

---

## Bootstrap e Memória

| Variável | Descrição | Padrão |
| :--- | :--- | :--- |
| `CHATCLI_BOOTSTRAP_ENABLED` | Ativa o carregamento de arquivos bootstrap (SOUL.md, USER.md, etc.) no system prompt. | `false` |
| `CHATCLI_BOOTSTRAP_DIR` | Diretório contendo os arquivos bootstrap. | `~/.chatcli/bootstrap/` |
| `CHATCLI_MEMORY_ENABLED` | Ativa o sistema de memória persistente (MEMORY.md + notas diárias). | `false` |
| `CHATCLI_SAFETY_ENABLED` | Ativa regras de segurança configuráveis no shell do agente. | `false` |

> Para detalhes completos, consulte a [documentação de Bootstrap e Memória](/docs/features/bootstrap-memory/).

---

## Segurança e Controle

| Variável | Descrição | Padrão |
| :--- | :--- | :--- |
| `CHATCLI_DISABLE_VERSION_CHECK` | Desabilita a verificação automática de versão no startup. Útil para ambientes air-gapped ou CI/CD. | `false` |
| `CHATCLI_GRPC_REFLECTION` | Habilita gRPC server reflection (expõe schema do serviço). | `false` |

> Para detalhes completos sobre segurança, consulte a [documentação de Segurança e Hardening](/docs/features/security/).

---

## Configuração do Cliente Remoto (`chatcli connect`)

| Variável | Descrição | Padrão |
| :--- | :--- | :--- |
| `CHATCLI_REMOTE_ADDR` | Endereço do servidor remoto (`host:port`). | `""` |
| `CHATCLI_REMOTE_TOKEN` | Token de autenticação para conectar ao servidor. | `""` |
| `CHATCLI_CLIENT_API_KEY` | Sua própria API key/OAuth token, enviada ao servidor. | `""` |

---

## Configuração do K8s Watcher (`chatcli watch` / `chatcli server --watch-*`)

| Variável | Descrição | Padrão |
| :--- | :--- | :--- |
| `CHATCLI_WATCH_DEPLOYMENT` | Nome do deployment Kubernetes a monitorar. | `""` |
| `CHATCLI_WATCH_NAMESPACE` | Namespace do deployment. | `"default"` |
| `CHATCLI_WATCH_INTERVAL` | Intervalo entre coletas de dados. Aceita durações Go (ex: `10s`, `1m`). | `"30s"` |
| `CHATCLI_WATCH_WINDOW` | Janela temporal de dados mantidos em memória. | `"2h"` |
| `CHATCLI_WATCH_MAX_LOG_LINES` | Número máximo de linhas de log coletadas por pod. | `100` |
| `CHATCLI_KUBECONFIG` | Caminho para o kubeconfig (opcional, usa default se não definido). | Auto-detectado |
