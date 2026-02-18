+++
title = "Autenticação OAuth"
linkTitle = "Autenticação OAuth"
weight = 5
description = "Autentique-se com provedores de IA via OAuth, sem precisar gerenciar chaves de API manualmente."
icon = "login"
+++

O ChatCLI suporta autenticação via **OAuth 2.0 com PKCE** para provedores que oferecém essa opção. Isso permite que você use seu plano existente (como **ChatGPT Plus**, **Codex** ou **Claude Pro**) diretamente no terminal, sem precisar gerar ou colar chaves de API.

---

## Métodos de Autenticação

| Método | Descrição | Quando Usar |
|--------|-----------|-------------|
| **Chave de API** | Variável de ambiente no `.env` (ex: `OPENAI_API_KEY`) | Acesso programático, CI/CD, API Keys da plataforma |
| **OAuth (login interativo)** | Comando `/auth login` no terminal | Planos ChatGPT Plus/Codex, Claude Pro, sem gerenciar keys |

> Os dois métodos podem coexistir. O ChatCLI prioriza credenciais OAuth quando ambas estão disponíveis.

---

## Comandos `/auth`

Todos os comandos de autenticação possuem **auto-completação** — basta digitar `/auth` e pressionar `Tab`.

### Ver Status

```bash
/auth status
```

Exibe o status de autenticação de todos os provedores configurados, incluindo tipo de credencial (API Key vs OAuth), validade do token e origem.

### Login via OAuth

```bash
/auth login openai-codex
/auth login anthropic
```

Inicia o fluxo de autenticação OAuth:

1. O navegador abre automaticamente na página de login do provedor
2. **OpenAI:** um servidor HTTP local captura o callback automaticamente (porta **1455**)
3. **Anthropic:** após autorizar, copie o código exibido na página e cole no terminal
4. O provedor aparece imediatamente no `/switch` — **sem reiniciar o app**

#### Provedores Suportados

| Provedor | Comando | Planos Compatíveis |
|----------|---------|-------------------|
| OpenAI | `/auth login openai-codex` | ChatGPT Plus, Codex, Team, Enterprise |
| Anthropic | `/auth login anthropic` | Claude Pro, Team |

### Logout

```bash
/auth logout openai-codex
/auth logout anthropic
```

Remove as credenciais OAuth armazenadas para o provedor específicado.

---

## Fluxo Completo: Do Zero ao Primeiro Prompt

Se você está começando **sem nenhuma credencial configurada**, siga estes passos:

```bash
# 1. Inicie o ChatCLI (funciona mesmo sem credenciais)
chatcli

# 2. Faça login via OAuth
/auth login openai-codex

# 3. Autorize no navegador (abre automaticamente)

# 4. Troque para o provedor recém autenticado
/switch

# 5. Pronto! Envie seu primeiro prompt
Olá, como você está?
```

---

## Roteamento Automático de Endpoints (OpenAI)

O ChatCLI detecta automaticamente o tipo de credencial e roteia as requisições para o endpoint correto:

| Credencial | Endpoint | API Format |
|------------|----------|------------|
| `OPENAI_API_KEY` | `api.openai.com/v1/responses` | Responses API (padrão) |
| `OPENAI_API_KEY` | `api.openai.com/v1/chat/completions` | Chat Completions (modelos legados) |
| OAuth (`/auth login`) | `chatgpt.com/backend-api/codex/responses` | Responses API (streaming SSE) |

> Você não precisa configurar nada — o roteamento é automático baseado no tipo de token.

---

## Armazenamento de Credenciais

As credenciais OAuth são salvas com **criptografia AES-256-GCM** em:

```
~/.chatcli/auth-profiles.json
```

A chave de criptografia é gerada automaticamente e armazenada em `~/.chatcli/.auth-key` (permissão 0600). Dados não-criptografados de versões anteriores são migrados transparentemente na primeira leitura.

O arquivo contém tokens de acesso, tokens de refresh e metadados de cada provedor. Os tokens são renovados automaticamente quando expiram.

Para personalizar o diretório de armazenamento, defina a variável de ambiente:

```bash
export CHATCLI_AUTH_DIR="~/.config/chatcli/auth"
```

---

## Solução de Problemas

### "Erro de autenticação" ao clicar no link OAuth (OpenAI)

Verifique se a porta de callback não está em uso por outra aplicação. O servidor de callback da OpenAI precisa da porta **1455** para capturar o retorno.

### Código não aparece após autorizar (Anthropic)

O fluxo da Anthropic redireciona para a página do console que exibe o código de autorização. Copie o código exibido e cole no terminal quando solicitado.

### Provedor não aparece no `/switch` após login

Execute `/auth status` para verificar se o token foi salvo corretamente. Se necessário, tente `/auth logout <provedor>` seguido de `/auth login <provedor>`.

### Token expirado

Os tokens OAuth são renovados automaticamente usando o refresh token. Se o refresh falhar, faça login novamente com `/auth login`.

---

## Próximos Passos

- [**Referência de Comandos**](/docs/reference/command-reference/) — Lista completa de todos os comandos
- [**Configuração (.env)**](/docs/reference/configuration/) — Todas as variáveis de ambiente disponíveis
- [**Uso Básico**](/docs/core-concepts/basic-usage/) — Aprenda os fundamentos do ChatCLI
