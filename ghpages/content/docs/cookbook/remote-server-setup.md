+++
title = "Receita: Servidor Remoto para Equipe"
linkTitle = "Servidor para Equipe"
weight = 70
description = "Guia passo a passo para configurar um servidor ChatCLI centralizado para toda a equipe, com autenticação, TLS e múltiplos provedores."
icon = "groups"
+++

Nesta receita, você vai configurar um servidor ChatCLI centralizado que atende toda a equipe de desenvolvimento. Cada membro pode conectar do seu terminal e usar a IA sem precisar gerenciar API keys individuais.

---

## Cenário

- Equipe de 5-10 desenvolvedores
- Servidor central com API keys corporativas
- Cada dev conecta do seu terminal local
- Autenticação via token compartilhado
- Opção de usar credenciais próprias

---

## Passo 1: Configurar o Servidor

### Opção A: Docker Compose (Simples)

Crie um arquivo `.env` no servidor:

```bash
# .env
CHATCLI_SERVER_TOKEN=equipe-token-2024
LLM_PROVIDER=CLAUDEAI
ANTHROPIC_API_KEY=sk-ant-xxx-chave-corporativa
ANTHROPIC_MODEL=claude-sonnet-4-5
LOG_LEVEL=info
```

Inicie com Docker Compose:

```bash
docker compose up -d
```

### Opção B: Binario Direto

```bash
export CHATCLI_SERVER_TOKEN=equipe-token-2024
export LLM_PROVIDER=CLAUDEAI
export ANTHROPIC_API_KEY=sk-ant-xxx
chatcli server --port 50051
```

### Opção C: Kubernetes (Helm)

```bash
helm install chatcli deploy/helm/chatcli \
  --namespace tools --create-namespace \
  --set llm.provider=CLAUDEAI \
  --set secrets.anthropicApiKey=sk-ant-xxx \
  --set server.token=equipe-token-2024 \
  --set service.type=LoadBalancer
```

---

## Passo 2: Distribuir Acesso

Compartilhe com a equipe:

```bash
# Adicione ao .bashrc ou .zshrc de cada dev
export CHATCLI_REMOTE_ADDR=servidor-ia:50051
export CHATCLI_REMOTE_TOKEN=equipe-token-2024

# Alias para acesso rapido
alias cia='chatcli connect'
```

---

## Passo 3: Cada Dev Conecta

```bash
# Modo interativo
chatcli connect

# One-shot rapido
chatcli connect -p "Explique o padrão Repository em Go"
```

---

## Passo 4: Permitir Credenciais Próprias (Opcional)

Devs que preferem usar suas próprias credenciais podem faze-lo:

```bash
# Dev que tem assinatura Claude Pro
chatcli connect --use-local-auth

# Dev que prefere OpenAI
chatcli connect --provider OPENAI --llm-key sk-minha-chave-pessoal
```

O servidor aceita ambos os modos simultaneamente.

---

## Passo 5: Adicionar TLS (Produção)

Para ambientes de produção, adicione TLS:

```bash
# Gerar certificados (ex: com Let's Encrypt ou certbot)
# Ou usar mkcert para desenvolvimento

# Iniciar com TLS
chatcli server \
  --tls-cert /etc/chatcli/cert.pem \
  --tls-key /etc/chatcli/key.pem \
  --token equipe-token-2024
```

Devs conectam com:

```bash
chatcli connect servidor:50051 --tls --token equipe-token-2024
```

---

## Passo 6: Multiplos Provedores

Configure o servidor com múltiplas API keys. Devs podem escolher o provedor:

```bash
# Servidor com OpenAI, Claude e Google AI
export OPENAI_API_KEY=sk-xxx
export ANTHROPIC_API_KEY=sk-ant-xxx
export GOOGLEAI_API_KEY=AIzaSy-xxx
export LLM_PROVIDER=CLAUDEAI  # padrão
chatcli server
```

```bash
# Dev escolhe o provedor
chatcli connect --provider OPENAI
chatcli connect --provider GOOGLEAI
chatcli connect  # usa o padrão (CLAUDEAI)
```

---

## Passo 7: Compartilhar Agents, Skills e Plugins (Opcional)

Provisione agents, skills e plugins no servidor para que toda a equipe tenha acesso aos mesmos recursos:

### Via Helm

```bash
helm install chatcli deploy/helm/chatcli \
  --namespace tools --create-namespace \
  --set llm.provider=CLAUDEAI \
  --set secrets.anthropicApiKey=sk-ant-xxx \
  --set server.token=equipe-token-2024 \
  --set agents.enabled=true \
  --set-file agents.definitions.devops-senior\\.md=agents/devops-senior.md \
  --set skills.enabled=true \
  --set-file skills.definitions.k8s-best-practices\\.md=skills/k8s-best-practices.md \
  --set plugins.enabled=true \
  --set plugins.initImage=myregistry/chatcli-plugins:latest
```

### Via ConfigMaps Existentes

```bash
# Criar ConfigMap com agents
kubectl create configmap chatcli-agents \
  --from-file=devops-senior.md=agents/devops-senior.md \
  --from-file=go-expert.md=agents/go-expert.md \
  -n tools

# Referenciar no Helm
helm install chatcli deploy/helm/chatcli \
  --set agents.enabled=true \
  --set agents.existingConfigMap=chatcli-agents
```

Quando os devs conectam, eles veem automaticamente os recursos do servidor:

```
Connected to ChatCLI server (version: 1.3.0, provider: CLAUDEAI, model: claude-sonnet-4-5)
 Server has 2 plugins, 2 agents, 3 skills available
```

---

## Dicas de Operação

### Logs do Servidor

```bash
# Docker
docker logs chatcli-server -f

# Kubernetes
kubectl logs -f deployment/chatcli -n tools
```

### Health Check

```bash
# O Dockerfile inclui health check integrado
docker inspect chatcli-server --format='{{.State.Health.Status}}'
```

### Monitoramento de Uso

Configure `LOG_LEVEL=info` no servidor para registrar cada request com provider e modelo utilizado.

### Backup de Sessões

```bash
# Docker: volumes persistentes
docker cp chatcli-server:/home/chatcli/.chatcli/sessions ./backup/

# Kubernetes: PVCjá configurado no Helm chart
```

---

## Resumo

| Componente | Configuração |
|------------|-------------|
| Servidor | `chatcli server --token X` |
| Cliente | `chatcli connect --token X` |
| Env Vars | `CHATCLI_REMOTE_ADDR`, `CHATCLI_REMOTE_TOKEN` |
| TLS | `--tls-cert`, `--tls-key` (servidor) / `--tls` (cliente) |
| Credenciais | Servidor (padrão) ou cliente (`--llm-key` / `--use-local-auth`) |
| Agents/Skills | `agents.enabled=true` + ConfigMap (Helm) ou `agents.configMapRef` (Operator) |
| Plugins | `plugins.enabled=true` + init container ou PVC (Helm) ou `plugins.image` (Operator) |
