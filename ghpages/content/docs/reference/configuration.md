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
| `LLM_PROVIDER` | Define o provedor de IA padrão a ser usado. Valores válidos: `OPENAI`, `CLAUDEAI`, `GOOGLEAI`, `XAI`, `OLLAMA`, `STACKSPOT`.                      | `"OPENAI"`                 |
| `CHATCLI_LANG` | Define o idioma da interface. Valores: `pt-BR`, `en-US`. Se não definida, tentará detectar o idioma do sistema.                                   | `en-US`                    |
| `LOG_LEVEL` | Nível dos logs. Opções: `debug`, `info`, `warn`, `error`.                                                                                         | `"info"`                   |
| `LOG_FILE` | Caminho para o arquivo de log. Padrão: `$HOME/.chatcli/app.log`                                                                                   | `"$HOME/.chatcli/app.log"` |
| `LOG_MAX_SIZE` | Tamanho máximo do arquivo de log antes da rotação. Aceita `100MB`, `50KB`, etc.                                                                   | `"100MB"`                  |
| `HISTORY_MAX_SIZE` | Tamanho máximo do arquivo de histórico (`.chatcli_history`) antes da rotação.                                                                     | `"100MB"`                  |
| `HISTORY_FILE` | Caminho personalizado para o arquivo de histórico (suporta `~`, hoje ele cria o historico onde executou o chatcli).                               | `".chatcli_history"`       |
| `CHATCLI_DOTENV`| Caminho personalizado para o seu arquivo `.env`.                                                                                                  | `".env"`                   |

## Configuração de Provedores

### OpenAI

| Variável | Descrição                                                                        | Obrigatório? |
| :--- |:---------------------------------------------------------------------------------| :--- |
| `OPENAI_API_KEY` | Sua chave de API secreta da OpenAI.                                              | **Sim** |
| `OPENAI_MODEL` | O modelo a ser usado. Ex: `gpt-4o`, `gpt-4o-mini`, `gpt-4-turbo`.                | Não |
| `OPENAI_ASSISTANT_MODEL` | O modelo a ser usado especificamente para a API de Assistentes.                  | Não |
| `OPENAI_USE_RESPONSES` | Define `true` para usar a API de `v1/responses` em vez de `v1/chat/completions`. | Não |
| `OPENAI_MAX_TOKENS` | Define o maximo de tokens a ser utilizados na sessão (depende do modelo)         | Não

### Anthropic (Claude)

| Variável               | Descrição | Obrigatório? |
|:-----------------------| :--- | :--- |
| `CLAUDEAI_API_KEY`     | Sua chave de API secreta da Anthropic. | **Sim** |
| `CLAUDEAI_MODEL`       | O modelo a ser usado. Ex: `claude-3-5-sonnet-20240620`, `claude-3-opus-20240229`. | Não |
| `CLAUDEAI_API_VERSION` | A versão da API da Anthropic a ser usada nos cabeçalhos. | Não |
| `CLAUDEAI_MAX_TOKENS`  | Define o maximo de tokens a ser utilizados na sessão (depende do modelo)         | Não


### Google (Gemini)

| Variável              | Descrição | Obrigatório? |
|:----------------------| :--- | :--- |
| `GOOGLEAI_API_KEY`    | Sua chave de API do Google AI Studio. | **Sim** |
| `GOOGLEAI_MODEL`      | O modelo a ser usado. Ex: `gemini-1.5-pro-latest`, `gemini-1.5-flash-latest`. | Não |
| `GOOGLEAI_MAX_TOKENS` | Define o maximo de tokens a ser utilizados na sessão (depende do modelo)         | Não


### xAI (Grok)

| Variável         | Descrição | Obrigatório? |
|:-----------------| :--- | :--- |
| `XAI_API_KEY`    | Sua chave de API secreta da xAI. | **Sim** |
| `XAI_MODEL`      | O modelo a ser usado. Ex: `grok-1`. | Não |
| `XAI_MAX_TOKENS` | Define o maximo de tokens a ser utilizados na sessão (depende do modelo)         | Não


### Ollama (Local)

| Variável | Descrição                                                                                    | Obrigatório? |
| :--- |:---------------------------------------------------------------------------------------------| :--- |
| `OLLAMA_ENABLED` | Defina como `true` para habilitar o provedor Ollama.                                         | **Sim** |
| `OLLAMA_BASE_URL` | URL base do seu servidor Ollama local.                                                       | Não |
| `OLLAMA_MODEL` | O nome do modelo local a ser usado (ex: `llama3`, `codellama`).                              | Não |
| `OLLAMA_FILTER_THINKING` | Filtra raciocínio intermediário em respostas (ex.: para `Qwen3`, `llama3` padrão `true`...). | Não |


### StackSpot

| Variável | Descrição | Obrigatório? |
| :--- | :--- | :--- |
| `CLIENT_ID` | Credencial de ID de cliente da StackSpot. | **Sim** |
| `CLIENT_KEY` | Credencial de chave de cliente da StackSpot. | **Sim** |
| `STACKSPOT_REALM` | O `realm` (tenant) da sua organização na StackSpot. | **Sim** |
| `STACKSPOT_AGENT_ID` | O ID do agente específico a ser utilizado. | **Sim** |

---

## Configuração do Modo Agente

| Variável | Descrição |
| :--- | :--- |
| `CHATCLI_AGENT_ALLOW_SUDO` | Defina como `"true"` para permitir que o agente sugira e execute comandos com `sudo`. **Use com extrema cautela.** |
| `CHATCLI_AGENT_DENYLIST` | Lista de padrões regex (separados por `;`) para bloquear comandos adicionais no modo agente. |
| `CHATCLI_AGENT_CMD_TIMEOUT` | Timeout para a execução de um único comando pelo agente (padrão: `10m`). |


--------