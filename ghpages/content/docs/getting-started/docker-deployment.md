+++
title = "Deploy com Docker e Kubernetes"
linkTitle = "Docker & Kubernetes"
weight = 20
description = "Como fazer deploy do ChatCLI como servidor usando Docker, Docker Compose ou Helm no Kubernetes."
icon = "deployed_code"
+++

O ChatCLI pode ser empacotado como container Docker e deployado no Kubernetes usando o Helm chart oficial. Esta página cobre todos os cenários de deployment.

---

## Imagens Oficiais (GHCR)

As imagens Docker oficiais são publicadas automaticamente no GitHub Container Registry a cada release:

| Imagem | Descrição |
|--------|-----------|
| `ghcr.io/diillson/chatcli:latest` | Servidor ChatCLI (gRPC) |
| `ghcr.io/diillson/chatcli-operator:latest` | Kubernetes Operator |

```bash
# Puxar a imagem do servidor
docker pull ghcr.io/diillson/chatcli:latest

# Ou uma versão específica
docker pull ghcr.io/diillson/chatcli:v1.57.0

# Puxar a imagem do operator
docker pull ghcr.io/diillson/chatcli-operator:latest
```

As imagens suportam **multi-arch** (`linux/amd64` e `linux/arm64`).

---

## Docker

### Build da Imagem (Local)

```bash
# Na raiz do projeto
docker build -t chatcli .
```

O Dockerfile usa multi-stage build para produzir uma imagem mínima (~20MB):
- **Build stage**: `golang:1.25-alpine` compila o binário
- **Runtime stage**: `alpine:3.21` com usuário não-root, health check integrado

### Build da Imagem do Operator (Local)

```bash
# IMPORTANTE: deve ser construído a partir da raiz do repositório
# (go.mod do operator usa replace directive apontando para ../)
docker build -f operator/Dockerfile -t ghcr.io/diillson/chatcli-operator:latest .
```

O Dockerfile do operator usa:
- **Build stage**: `golang:1.25` com suporte multi-arch (`TARGETARCH`)
- **Runtime stage**: `gcr.io/distroless/static:nonroot` (segurança máxima, sem shell)

### Rodar com Docker

```bash
# Modo mais simples
docker run -p 50051:50051 \
  -e LLM_PROVIDER=OPENAI \
  -e OPENAI_API_KEY=sk-xxx \
  chatcli

# Com autenticação
docker run -p 50051:50051 \
  -e CHATCLI_SERVER_TOKEN=meu-token \
  -e LLM_PROVIDER=CLAUDEAI \
  -e ANTHROPIC_API_KEY=sk-ant-xxx \
  chatcli

# Com volume para persistir sessões
docker run -p 50051:50051 \
  -v chatcli-sessions:/home/chatcli/.chatcli/sessions \
  -e LLM_PROVIDER=OPENAI \
  -e OPENAI_API_KEY=sk-xxx \
  chatcli
```

### Docker Compose

O projeto inclui um `docker-compose.yml` pronto para desenvolvimento:

```bash
# Defina as variáveis de ambiente
export LLM_PROVIDER=OPENAI
export OPENAI_API_KEY=sk-xxx

# Inicie
docker compose up -d

# Conecte do seu terminal
chatcli connect localhost:50051
```

O Docker Compose configura:
- Porta 50051 exposta
- Volumes persistentes para sessões e plugins
- Restart automático (`unless-stopped`)
- Todas as variáveis de LLM via environment
- **Hardening de segurança**: filesystem read-only, `no-new-privileges`, limites de CPU/memória, tmpfs para `/tmp`

#### Arquivo `docker-compose.yml`

```yaml
version: "3.9"

services:
  chatcli-server:
    build:
      context: .
      dockerfile: Dockerfile
    container_name: chatcli-server
    ports:
      - "50051:50051"
    environment:
      CHATCLI_SERVER_PORT: "50051"
      CHATCLI_SERVER_TOKEN: "${CHATCLI_SERVER_TOKEN:-}"
      LLM_PROVIDER: "${LLM_PROVIDER:-}"
      OPENAI_API_KEY: "${OPENAI_API_KEY:-}"
      ANTHROPIC_API_KEY: "${ANTHROPIC_API_KEY:-}"
      GOOGLEAI_API_KEY: "${GOOGLEAI_API_KEY:-}"
      OLLAMA_ENABLED: "${OLLAMA_ENABLED:-}"
      OLLAMA_BASE_URL: "${OLLAMA_BASE_URL:-}"
      LOG_LEVEL: "${LOG_LEVEL:-info}"
    volumes:
      - chatcli-sessions:/home/chatcli/.chatcli/sessions
      - chatcli-plugins:/home/chatcli/.chatcli/plugins
    restart: unless-stopped
    read_only: true
    tmpfs:
      - /tmp:size=100M
    security_opt:
      - no-new-privileges:true
    deploy:
      resources:
        limits:
          cpus: "2.0"
          memory: 1G

volumes:
  chatcli-sessions:
  chatcli-plugins:
```

> O container roda com filesystem **read-only** e `no-new-privileges` por padrão. O diretório `/tmp` usa tmpfs em memória (limitado a 100MB). Os volumes nomeados (`chatcli-sessions`, `chatcli-plugins`) são os únicos pontos graváveis. Veja a [documentação de segurança](/docs/features/security/) para detalhes.

---

## Kubernetes (Helm)

O ChatCLI inclui um Helm chart completo em `deploy/helm/chatcli/`.

### Pré-requisitos

- Cluster Kubernetes (kind, minikube, EKS, GKE, AKS, etc.)
- Helm 3.x instalado
- `kubectl` configurado para o cluster

### Instalação Básica

```bash
# Instalação mínima
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=sk-xxx

# Com autenticação
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=CLAUDEAI \
  --set secrets.anthropicApiKey=sk-ant-xxx \
  --set server.token=meu-token-secreto
```

### Instalação com K8s Watcher (Single-Target)

```bash
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=sk-xxx \
  --set watcher.enabled=true \
  --set watcher.deployment=myapp \
  --set watcher.namespace=production
```

### Instalação com Multi-Target + Prometheus

Para monitorar múltiplos deployments com métricas Prometheus, use um `values.yaml`:

```yaml
# values-multi.yaml
llm:
  provider: CLAUDEAI
secrets:
  anthropicApiKey: sk-ant-xxx
watcher:
  enabled: true
  interval: "15s"
  maxContextChars: 32000
  targets:
    - deployment: api-gateway
      namespace: production
      metricsPort: 9090
      metricsFilter: ["http_requests_*", "http_request_duration_*"]
    - deployment: auth-service
      namespace: production
      metricsPort: 9090
    - deployment: worker
      namespace: batch
```

```bash
helm install chatcli deploy/helm/chatcli -f values-multi.yaml
```

O chart automaticamente:
- Cria ServiceAccount com RBAC para o watcher ler pods, eventos, logs
- **Auto-detecta multi-namespace**: se targets estão em namespaces diferentes, usa `ClusterRole` em vez de `Role`
- Gera ConfigMap `<name>-watch-config` com o YAML multi-target
- Monta o config como volume e passa `--watch-config` ao container

### Valores do Helm Chart

#### Servidor

| Valor | Descrição | Padrão |
|-------|-----------|--------|
| `replicaCount` | Número de réplicas | `1` |
| `image.repository` | Repositório da imagem | `ghcr.io/diillson/chatcli` |
| `image.tag` | Tag da imagem | `latest` |
| `server.port` | Porta gRPC | `50051` |
| `server.metricsPort` | Porta HTTP para Prometheus metrics (0 = desabilitado) | `9090` |
| `server.token` | Token de autenticação | `""` |
| `serviceMonitor.enabled` | Criar ServiceMonitor (requer Prometheus Operator) | `false` |
| `serviceMonitor.interval` | Intervalo de scrape do Prometheus | `30s` |

#### TLS

| Valor | Descrição | Padrão |
|-------|-----------|--------|
| `tls.enabled` | Habilitar TLS | `false` |
| `tls.certFile` | Caminho do certificado | `""` |
| `tls.keyFile` | Caminho da chave | `""` |
| `tls.existingSecret` | Secret existente com certs | `""` |

#### LLM

| Valor | Descrição | Padrão |
|-------|-----------|--------|
| `llm.provider` | Provedor padrão | `""` |
| `llm.model` | Modelo padrão | `""` |

#### Secrets (API Keys)

| Valor | Descrição |
|-------|-----------|
| `secrets.existingSecret` | Secret existente (em vez de criar um novo) |
| `secrets.openaiApiKey` | Chave da OpenAI |
| `secrets.anthropicApiKey` | Chave da Anthropic |
| `secrets.googleaiApiKey` | Chave do Google AI |
| `secrets.xaiApiKey` | Chave da xAI |
| `secrets.stackspotClientId` | StackSpot Client ID |
| `secrets.stackspotClientKey` | StackSpot Client Key |
| `secrets.stackspotRealm` | StackSpot Realm |
| `secrets.stackspotAgentId` | StackSpot Agent ID |

#### Ollama

| Valor | Descrição | Padrão |
|-------|-----------|--------|
| `ollama.enabled` | Habilitar Ollama | `false` |
| `ollama.baseUrl` | URL base do Ollama | `http://ollama:11434` |
| `ollama.model` | Modelo Ollama | `""` |

#### K8s Watcher

| Valor | Descrição | Padrão |
|-------|-----------|--------|
| `watcher.enabled` | Habilitar o watcher | `false` |
| `watcher.targets` | Lista de targets multi-deployment (ver abaixo) | `[]` |
| `watcher.deployment` | Deployment único - legado | `""` |
| `watcher.namespace` | Namespace do deployment - legado | `""` |
| `watcher.interval` | Intervalo de coleta | `30s` |
| `watcher.window` | Janela de observação | `2h` |
| `watcher.maxLogLines` | Linhas de log por pod | `100` |
| `watcher.maxContextChars` | Budget de contexto LLM | `32000` |

**Campos de cada target** (`watcher.targets[].`):

| Campo | Descrição | Obrigatório |
|-------|-----------|:-----------:|
| `deployment` | Nome do deployment | Sim |
| `namespace` | Namespace (padrão: `default`) | Não |
| `metricsPort` | Porta Prometheus (0 = desabilitado) | Não |
| `metricsPath` | Path HTTP das métricas | Não (`/metrics`) |
| `metricsFilter` | Filtros glob para métricas | Não |

#### Persistência

| Valor | Descrição | Padrão |
|-------|-----------|--------|
| `persistence.enabled` | Persistir sessões em PVC | `true` |
| `persistence.storageClass` | Storage class | `""` |
| `persistence.size` | Tamanho do volume | `1Gi` |

#### Segurança

| Valor | Descrição | Padrão |
|-------|-----------|--------|
| `podSecurityContext.runAsNonRoot` | Obriga execução como não-root | `true` |
| `podSecurityContext.runAsUser` | UID do processo | `1000` |
| `podSecurityContext.seccompProfile.type` | Perfil seccomp | `RuntimeDefault` |
| `securityContext.allowPrivilegeEscalation` | Permite escalação de privilégios | `false` |
| `securityContext.readOnlyRootFilesystem` | Filesystem somente-leitura | `true` |
| `securityContext.capabilities.drop` | Capabilities removidas | `ALL` |
| `rbac.clusterWide` | Usa ClusterRole em vez de Role namespace-scoped | `false` |

> Quando `readOnlyRootFilesystem` está `true`, o chart monta automaticamente um tmpfs em `/tmp` e um emptyDir em `/home/chatcli/.chatcli` (200Mi) para dados de runtime. A variável `HOME=/home/chatcli` é definida automaticamente. Para monitorar múltiplos namespaces, habilite `rbac.clusterWide: true`. Veja a [documentação de segurança](/docs/features/security/) para detalhes.
>
> **Nota**: O ConfigMap e o Secret referenciados via `envFrom` são marcados como `optional: true`, permitindo criar o Instance/Deployment antes dos recursos dependentes. O operator observa Secrets automaticamente e dispara rolling updates quando são criados ou atualizados.

#### Rede

| Valor | Descrição | Padrão |
|-------|-----------|--------|
| `service.type` | Tipo do Service | `ClusterIP` |
| `service.port` | Porta do Service | `50051` |
| `service.headless` | Habilita Service headless para balanceamento gRPC client-side (recomendado quando `replicaCount > 1`) | `false` |
| `ingress.enabled` | Habilitar Ingress | `false` |

> **gRPC e múltiplas réplicas**: O gRPC usa conexões HTTP/2 persistentes que fixam em um único pod. Para `replicaCount > 1`, habilite `service.headless: true` para ativar balanceamento round-robin via DNS. O client já possui keepalive e round-robin integrados.

### Usando Secret Existente

Se você já tem um Secret com as API keys:

```bash
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=OPENAI \
  --set secrets.existingSecret=my-llm-keys
```

O Secret deve conter as chaves esperadas:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: my-llm-keys
type: Opaque
stringData:
  OPENAI_API_KEY: "sk-xxx"
  ANTHROPIC_API_KEY: "sk-ant-xxx"
```

### Acessar o Servidor

#### Port Forward (Desenvolvimento)

```bash
kubectl port-forward svc/chatcli 50051:50051
chatcli connect localhost:50051
```

#### NodePort

```bash
helm install chatcli deploy/helm/chatcli \
  --set service.type=NodePort
chatcli connect <node-ip>:<node-port>
```

#### LoadBalancer

```bash
helm install chatcli deploy/helm/chatcli \
  --set service.type=LoadBalancer

# Aguarde o IP externo
kubectl get svc chatcli -w
chatcli connect <external-ip>:50051
```

#### Ingress (com TLS)

```yaml
# values-prod.yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: chatcli.meudominio.com
      paths:
        - path: /
          pathType: ImplementationSpecific
  tls:
    - secretName: chatcli-tls
      hosts:
        - chatcli.meudominio.com
```

```bash
helm install chatcli deploy/helm/chatcli -f values-prod.yaml
```

### Upgrade e Rollback

```bash
# Atualizar
helm upgrade chatcli deploy/helm/chatcli --set llm.model=gpt-4-turbo

# Rollback
helm rollback chatcli 1
```

---

## Exemplo Completo: Produção

### Single-Target (Legado)

```bash
helm install chatcli deploy/helm/chatcli \
  --namespace chatcli --create-namespace \
  --set llm.provider=CLAUDEAI \
  --set secrets.anthropicApiKey=sk-ant-xxx \
  --set server.token=super-secret-token \
  --set tls.enabled=true \
  --set tls.existingSecret=chatcli-tls-certs \
  --set watcher.enabled=true \
  --set watcher.deployment=production-app \
  --set watcher.namespace=production \
  --set persistence.enabled=true \
  --set persistence.size=5Gi \
  --set resources.requests.memory=256Mi \
  --set resources.limits.memory=1Gi
```

### Multi-Target com Prometheus (Recomendado)

```yaml
# values-prod.yaml
llm:
  provider: CLAUDEAI
secrets:
  existingSecret: chatcli-llm-keys
server:
  token: super-secret-token
tls:
  enabled: true
  existingSecret: chatcli-tls-certs
watcher:
  enabled: true
  interval: "15s"
  maxContextChars: 10000
  targets:
    - deployment: api-gateway
      namespace: production
      metricsPort: 9090
      metricsFilter: ["http_requests_*", "http_request_duration_*"]
    - deployment: auth-service
      namespace: production
      metricsPort: 9090
    - deployment: payment-service
      namespace: production
      metricsPort: 9090
      metricsFilter: ["payment_*", "stripe_*"]
    - deployment: worker
      namespace: batch
persistence:
  enabled: true
  size: 5Gi
resources:
  requests:
    memory: 256Mi
  limits:
    memory: 1Gi
```

```bash
helm install chatcli deploy/helm/chatcli \
  --namespace chatcli --create-namespace \
  -f values-prod.yaml
```

> Quando targets estão em namespaces diferentes (ex: `production` e `batch`), o chart cria automaticamente um `ClusterRole` em vez de `Role` namespace-scoped.

---

## Próximo Passo

- [Configurar o servidor](/docs/features/server-mode/)
- [Conectar ao servidor](/docs/features/remote-connect/)
- [Monitorar Kubernetes](/docs/features/k8s-watcher/)
