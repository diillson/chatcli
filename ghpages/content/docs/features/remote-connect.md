+++
title = "Conexão Remota (chatcli connect)"
linkTitle = "Conexão Remota"
weight = 61
description = "Conecte seu terminal local a um servidor ChatCLI remoto via gRPC para acessar a IA de qualquer lugar."
icon = "cloud"
+++

O comando `chatcli connect` transforma seu terminal local em um cliente que se conecta a um servidor ChatCLI remoto. Toda a experiência interativa (sessões, contextos, agente, coder) funciona transparentemente, como se o LLM estivesse rodando localmente.

---

## Conexão Básica

```bash
# Conectar usando endereço posicional
chatcli connect meuservidor:50051

# Conectar com flag explícita
chatcli connect --addr meuservidor:50051
```

Ao conectar, o ChatCLI exibe informações do servidor:

```
Connected to ChatCLI server (version: 1.2.0, provider: CLAUDEAI, model: claude-sonnet-4-5)
```

Se o servidor tiver um K8s Watcher ativo, também aparece:

```
K8s watcher active: deployment/myapp in namespace/production (context injected into all prompts)
```

---

## Todas as Flags

| Flag | Descrição | Env Var |
|------|-----------|---------|
| `--addr <host:port>` | Endereço do servidor | `CHATCLI_REMOTE_ADDR` |
| `--token <string>` | Token de autenticação | `CHATCLI_REMOTE_TOKEN` |
| `--provider <nome>` | Sobrescreve o provedor LLM do servidor | |
| `--model <nome>` | Sobrescreve o modelo LLM do servidor | |
| `--llm-key <string>` | Sua própria API key (enviada ao servidor) | `CHATCLI_CLIENT_API_KEY` |
| `--use-local-auth` | Usa credenciais OAuth do auth store local | |
| `--tls` | Habilita conexão TLS | |
| `--ca-cert <path>` | Certificado CA para verificação TLS | |
| `-p <prompt>` | Modo one-shot: envia prompt e sai | |
| `--raw` | Saída crua (sem formatação Markdown/ANSI) | |
| `--max-tokens <int>` | Máximo de tokens na resposta | |

### Flags StackSpot

| Flag | Descrição |
|------|-----------|
| `--client-id` | StackSpot Client ID |
| `--client-key` | StackSpot Client Key |
| `--realm` | StackSpot Realm/Tenant |
| `--agent-id` | StackSpot Agent ID |

### Flags Ollama

| Flag | Descrição |
|------|-----------|
| `--ollama-url` | URL base do Ollama (ex: `http://gpu:11434`) |

---

## Modos de Credencial

Você pode escolher como autenticar com o provedor de LLM:

### 1. Credenciais do Servidor (Padrão)

Não envie nenhuma flag de credencial. O servidor usa suas próprias API keys:

```bash
chatcli connect meuservidor:50051
```

### 2. Sua Própria API Key

Envie sua chave diretamente. O servidor a usa para fazer a chamada ao LLM:

```bash
chatcli connect meuservidor:50051 --provider OPENAI --llm-key sk-minha-chave
```

### 3. OAuth Local (--use-local-auth)

Use credenciais OAuth já salvas localmente (de `/auth login`):

```bash
# Pre-requisito: ter feito login OAuth
# /auth login anthropic  (dentro do chatcli interativo)

# Conectar usando essas credenciais
chatcli connect meuservidor:50051 --use-local-auth

# Com provedor específico
chatcli connect meuservidor:50051 --use-local-auth --provider CLAUDEAI
```

A flag `--use-local-auth` lê o token OAuth de `~/.chatcli/auth-profiles.json` e o envia ao servidor. Se você não especificar `--provider`, o ChatCLI tenta Anthropic primeiro, depois OpenAI.

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

Envie um único prompt ao servidor remoto e receba a resposta:

```bash
# Prompt simples
chatcli connect meuservidor:50051 -p "Explique K8s pods"

# Com suas credenciais
chatcli connect meuservidor:50051 --use-local-auth -p "Resuma o status do cluster"

# Saída crua (sem markdown) para uso em scripts
chatcli connect meuservidor:50051 -p "Liste os pods com problemas" --raw
```

---

## Modo Interativo

Sem a flag `-p`, o ChatCLI inicia o modo interativo completo:

```bash
chatcli connect meuservidor:50051
```

Você tem acesso a **todas** as funcionalidades do ChatCLI:

- **Sessões**: `/session save`, `/session load`, `/session list`
- **Agente**: `/agent <tarefa>` ou `/run <tarefa>`
- **Coder**: `/coder <tarefa>`
- **Contexto**: `@file`, `@git`, `@command`, `@env`, `@history`
- **Persistência**: `/context create`, `/context attach`
- **Switch**: `/switch` para trocar provedor/modelo
- **Watcher**: `/watch status` para ver status do K8s Watcher

---

## Descoberta de Recursos Remotos

Ao conectar, o client descobre automaticamente plugins, agents e skills disponíveis no servidor:

```
Connected to ChatCLI server (version: 1.3.0, provider: CLAUDEAI, model: claude-sonnet-4-5)
 Server has 3 plugins, 2 agents, 4 skills available
```

### Plugins Remotos

Plugins do servidor aparecem em `/plugin list` com a tag `[remote]`. Eles são executados no servidor — o client envia o comando via gRPC e recebe o resultado:

```bash
# Listar plugins (locais + remotos)
/plugin list

📦 Plugins Instalados (2):
  • @hello          - Plugin de exemplo                    [local]
  • @k8s-diagnose   - Diagnóstico de clusters K8s          [remote]
```

### Agents e Skills Remotos

Agents e skills do servidor são transferidos ao client e compostos localmente, permitindo merge com resources locais:

```bash
# Listar agents (locais + remotos)
/agent list

🤖 Available Agents:
  • go-expert       - Especialista em Go/Golang            [local]
  • devops-senior   - DevOps Senior com foco em K8s        [remote]

# Carregar um agent remoto
/agent load devops-senior
```

Quando um agent remoto é carregado, suas skills são buscadas do servidor e compostas no prompt local — exatamente como agents locais.

### Modo Híbrido

- Plugins locais e remotos coexistem; o prefixo `[remote]` indica a origem
- Agents locais e remotos são listados juntos; ao carregar, a resolução é transparente
- Ao desconectar (`/disconnect`), recursos remotos são removidos automaticamente

---

## Verificar Status do K8s Watcher

Se o servidor tem um K8s Watcher ativo, você pode consultar o status remotamente:

```bash
# No modo interativo
/watch status
```

Saída de exemplo:

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

## Variáveis de Ambiente

Configure valores padrão via variáveis de ambiente para evitar digitar flags toda vez:

```bash
# No seu .bashrc ou .zshrc
export CHATCLI_REMOTE_ADDR=meuservidor:50051
export CHATCLI_REMOTE_TOKEN=meu-token

# Agora basta:
chatcli connect
```

---

## TLS e Segurança

### Conexão Insegura (Desenvolvimento)

```bash
chatcli connect localhost:50051
```

> Quando TLS está desabilitado, um **warning é logado** pelo cliente para lembrar que a conexão não está encriptada. Isso é perfeitamente aceitável para desenvolvimento local, mas em produção recomendamos habilitar TLS.

### Conexão com TLS

```bash
chatcli connect meuservidor:50051 --tls

# Com CA certificate customizado
chatcli connect meuservidor:50051 --tls --ca-cert /path/to/ca.pem
```

### Token + TLS (Produção)

```bash
chatcli connect meuservidor:50051 --tls --token meu-token-secreto
```

> Para um guia completo de segurança (autenticação, hardening de containers, RBAC, etc.), veja a [documentação de segurança](/docs/features/security/).

---

## Balanceamento com Múltiplas Réplicas

Quando o servidor ChatCLI roda com múltiplas réplicas no Kubernetes, o client distribui automaticamente as conexões entre os pods disponíveis:

- O client usa **round-robin client-side** via resolver `dns:///` do gRPC
- Requer um Service **headless** (`ClusterIP: None`) no Kubernetes para que o DNS retorne os IPs individuais dos pods
- **Keepalive** integrado (ping a cada 10s) detecta pods inativos e reconecta rapidamente
- No Helm chart, habilite `service.headless: true` quando `replicaCount > 1`
- No Operator, o headless é ativado automaticamente quando `spec.réplicas > 1`

> Sem o Service headless, o gRPC fixa a conexão HTTP/2 em um único pod, deixando as demais réplicas ociosas.

---

## Exemplos Práticos

```bash
# Desenvolvimento local: servidor sem auth
chatcli connect localhost:50051

# Produção: TLS + auth + suas credenciais
chatcli connect prod-server:50051 --tls --token secret --use-local-auth

# CI/CD: one-shot com provedor específico
chatcli connect ci-server:50051 --provider GOOGLEAI --llm-key AIzaSy-xxx \
  -p "Analise este diff: $(git diff HEAD~1)" --raw

# GPU server com Ollama
chatcli connect gpu-box:50051 --provider OLLAMA --ollama-url http://localhost:11434

# StackSpot enterprise
chatcli connect corp-server:50051 --provider STACKSPOT \
  --client-id myid --client-key mykey --realm mytenant --agent-id myagent
```

---

## Próximo Passo

- [Configurar o servidor](/docs/features/server-mode/)
- [Deploy com Docker e Helm](/docs/getting-started/docker-deployment/)
- [Monitorar Kubernetes](/docs/features/k8s-watcher/)
