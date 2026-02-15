+++
title = "Conexao Remota (chatcli connect)"
linkTitle = "Conexao Remota"
weight = 61
description = "Conecte seu terminal local a um servidor ChatCLI remoto via gRPC para acessar a IA de qualquer lugar."
icon = "cloud"
+++

O comando `chatcli connect` transforma seu terminal local em um cliente que se conecta a um servidor ChatCLI remoto. Toda a experiencia interativa (sessoes, contextos, agente, coder) funciona transparentemente, como se o LLM estivesse rodando localmente.

---

## Conexao Basica

```bash
# Conectar usando endereco posicional
chatcli connect meuservidor:50051

# Conectar com flag explicita
chatcli connect --addr meuservidor:50051
```

Ao conectar, o ChatCLI exibe informacoes do servidor:

```
Connected to ChatCLI server (version: 1.2.0, provider: CLAUDEAI, model: claude-sonnet-4-5)
```

Se o servidor tiver um K8s Watcher ativo, tambem aparece:

```
K8s watcher active: deployment/myapp in namespace/production (context injected into all prompts)
```

---

## Todas as Flags

| Flag | Descricao | Env Var |
|------|-----------|---------|
| `--addr <host:port>` | Endereco do servidor | `CHATCLI_REMOTE_ADDR` |
| `--token <string>` | Token de autenticacao | `CHATCLI_REMOTE_TOKEN` |
| `--provider <nome>` | Sobrescreve o provedor LLM do servidor | |
| `--model <nome>` | Sobrescreve o modelo LLM do servidor | |
| `--llm-key <string>` | Sua propria API key (enviada ao servidor) | `CHATCLI_CLIENT_API_KEY` |
| `--use-local-auth` | Usa credenciais OAuth do auth store local | |
| `--tls` | Habilita conexao TLS | |
| `--ca-cert <path>` | Certificado CA para verificacao TLS | |
| `-p <prompt>` | Modo one-shot: envia prompt e sai | |
| `--raw` | Saida crua (sem formatacao Markdown/ANSI) | |
| `--max-tokens <int>` | Maximo de tokens na resposta | |

### Flags StackSpot

| Flag | Descricao |
|------|-----------|
| `--client-id` | StackSpot Client ID |
| `--client-key` | StackSpot Client Key |
| `--realm` | StackSpot Realm/Tenant |
| `--agent-id` | StackSpot Agent ID |

### Flags Ollama

| Flag | Descricao |
|------|-----------|
| `--ollama-url` | URL base do Ollama (ex: `http://gpu:11434`) |

---

## Modos de Credencial

Voce pode escolher como autenticar com o provedor de LLM:

### 1. Credenciais do Servidor (Padrao)

Nao envie nenhuma flag de credencial. O servidor usa suas proprias API keys:

```bash
chatcli connect meuservidor:50051
```

### 2. Sua Propria API Key

Envie sua chave diretamente. O servidor a usa para fazer a chamada ao LLM:

```bash
chatcli connect meuservidor:50051 --provider OPENAI --llm-key sk-minha-chave
```

### 3. OAuth Local (--use-local-auth)

Use credenciais OAuth ja salvas localmente (de `/auth login`):

```bash
# Pre-requisito: ter feito login OAuth
# /auth login anthropic  (dentro do chatcli interativo)

# Conectar usando essas credenciais
chatcli connect meuservidor:50051 --use-local-auth

# Com provedor especifico
chatcli connect meuservidor:50051 --use-local-auth --provider CLAUDEAI
```

A flag `--use-local-auth` le o token OAuth de `~/.chatcli/auth-profiles.json` e o envia ao servidor. Se voce nao especificar `--provider`, o ChatCLI tenta Anthropic primeiro, depois OpenAI.

### 4. StackSpot (Credenciais Completas)

```bash
chatcli connect meuservidor:50051 --provider STACKSPOT \
  --client-id <id> --client-key <key> --realm <realm> --agent-id <agent>
```

### 5. Ollama

```bash
chatcli connect meuservidor:50051 --provider OLLAMA --ollama-url http://gpu-server:11434
```

---

## Modo One-Shot via Connect

Envie um unico prompt ao servidor remoto e receba a resposta:

```bash
# Prompt simples
chatcli connect meuservidor:50051 -p "Explique K8s pods"

# Com suas credenciais
chatcli connect meuservidor:50051 --use-local-auth -p "Resuma o status do cluster"

# Saida crua (sem markdown) para uso em scripts
chatcli connect meuservidor:50051 -p "Liste os pods com problemas" --raw
```

---

## Modo Interativo

Sem a flag `-p`, o ChatCLI inicia o modo interativo completo:

```bash
chatcli connect meuservidor:50051
```

Voce tem acesso a **todas** as funcionalidades do ChatCLI:

- **Sessoes**: `/session save`, `/session load`, `/session list`
- **Agente**: `/agent <tarefa>` ou `/run <tarefa>`
- **Coder**: `/coder <tarefa>`
- **Contexto**: `@file`, `@git`, `@command`, `@env`, `@history`
- **Persistencia**: `/context create`, `/context attach`
- **Switch**: `/switch` para trocar provedor/modelo
- **Watcher**: `/watch status` para ver status do K8s Watcher

---

## Verificar Status do K8s Watcher

Se o servidor tem um K8s Watcher ativo, voce pode consultar o status remotamente:

```bash
# No modo interativo
/watch status
```

Saida de exemplo:

```
K8s Watcher Status (Remote Server)
  Deployment:  myapp
  Namespace:   production
  Snapshots:   42
  Alerts:      2
  Pods:        3

Status Summary:
  3/3 pods running, 2 restarts last 1h
  Recent Events: Readiness probe succeeded on all pods
```

---

## Variaveis de Ambiente

Configure valores padrao via variaveis de ambiente para evitar digitar flags toda vez:

```bash
# No seu .bashrc ou .zshrc
export CHATCLI_REMOTE_ADDR=meuservidor:50051
export CHATCLI_REMOTE_TOKEN=meu-token

# Agora basta:
chatcli connect
```

---

## TLS e Seguranca

### Conexao Insegura (Desenvolvimento)

```bash
chatcli connect localhost:50051
```

### Conexao com TLS

```bash
chatcli connect meuservidor:50051 --tls

# Com CA certificate customizado
chatcli connect meuservidor:50051 --tls --ca-cert /path/to/ca.pem
```

### Token + TLS (Producao)

```bash
chatcli connect meuservidor:50051 --tls --token meu-token-secreto
```

---

## Exemplos Praticos

```bash
# Desenvolvimento local: servidor sem auth
chatcli connect localhost:50051

# Producao: TLS + auth + suas credenciais
chatcli connect prod-server:50051 --tls --token secret --use-local-auth

# CI/CD: one-shot com provedor especifico
chatcli connect ci-server:50051 --provider GOOGLEAI --llm-key AIzaSy-xxx \
  -p "Analise este diff: $(git diff HEAD~1)" --raw

# GPU server com Ollama
chatcli connect gpu-box:50051 --provider OLLAMA --ollama-url http://localhost:11434

# StackSpot enterprise
chatcli connect corp-server:50051 --provider STACKSPOT \
  --client-id myid --client-key mykey --realm mytenant --agent-id myagent
```

---

## Proximo Passo

- [Configurar o servidor](/docs/features/server-mode/)
- [Deploy com Docker e Helm](/docs/getting-started/docker-deployment/)
- [Monitorar Kubernetes](/docs/features/k8s-watcher/)
