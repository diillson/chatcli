+++
title = "Deploy com Docker e Kubernetes"
linkTitle = "Docker & Kubernetes"
weight = 20
description = "Como fazer deploy do ChatCLI como servidor usando Docker, Docker Compose ou Helm no Kubernetes."
icon = "deployed_code"
+++

O ChatCLI pode ser empacotado como container Docker e deployado no Kubernetes usando o Helm chart oficial. Esta pagina cobre todos os cenarios de deployment.

---

## Imagens Oficiais (GHCR)

As imagens Docker oficiais sao publicadas automaticamente no GitHub Container Registry a cada release:

| Imagem | Descricao |
|--------|-----------|
| `ghcr.io/diillson/chatcli:latest` | Servidor ChatCLI (gRPC) |
| `ghcr.io/diillson/chatcli-operator:latest` | Kubernetes Operator |

```bash
# Puxar a imagem do servidor
docker pull ghcr.io/diillson/chatcli:latest

# Ou uma versao especifica
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

O Dockerfile usa multi-stage build para produzir uma imagem minima (~20MB):
- **Build stage**: `golang:1.24-alpine` compila o binario
- **Runtime stage**: `alpine:3.21` com usuario nao-root, health check integrado

### Rodar com Docker

```bash
# Modo mais simples
docker run -p 50051:50051 \
  -e LLM_PROVIDER=OPENAI \
  -e OPENAI_API_KEY=sk-xxx \
  chatcli

# Com autenticacao
docker run -p 50051:50051 \
  -e CHATCLI_SERVER_TOKEN=meu-token \
  -e LLM_PROVIDER=CLAUDEAI \
  -e ANTHROPIC_API_KEY=sk-ant-xxx \
  chatcli

# Com volume para persistir sessoes
docker run -p 50051:50051 \
  -v chatcli-sessions:/home/chatcli/.chatcli/sessions \
  -e LLM_PROVIDER=OPENAI \
  -e OPENAI_API_KEY=sk-xxx \
  chatcli
```

### Docker Compose

O projeto inclui um `docker-compose.yml` pronto para desenvolvimento:

```bash
# Defina as variaveis de ambiente
export LLM_PROVIDER=OPENAI
export OPENAI_API_KEY=sk-xxx

# Inicie
docker compose up -d

# Conecte do seu terminal
chatcli connect localhost:50051
```

O Docker Compose configura:
- Porta 50051 exposta
- Volumes persistentes para sessoes e plugins
- Restart automatico (`unless-stopped`)
- Todas as variaveis de LLM via environment
- **Hardening de seguranca**: filesystem read-only, `no-new-privileges`, limites de CPU/memoria, tmpfs para `/tmp`

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

> O container roda com filesystem **read-only** e `no-new-privileges` por padrao. O diretorio `/tmp` usa tmpfs em memoria (limitado a 100MB). Os volumes nomeados (`chatcli-sessions`, `chatcli-plugins`) sao os unicos pontos graváveis. Veja a [documentacao de seguranca](/docs/features/security/) para detalhes.

---

## Kubernetes (Helm)

O ChatCLI inclui um Helm chart completo em `deploy/helm/chatcli/`.

### Pre-requisitos

- Cluster Kubernetes (kind, minikube, EKS, GKE, AKS, etc.)
- Helm 3.x instalado
- `kubectl` configurado para o cluster

### Instalacao Basica

```bash
# Instalacao minima
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=sk-xxx

# Com autenticacao
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=CLAUDEAI \
  --set secrets.anthropicApiKey=sk-ant-xxx \
  --set server.token=meu-token-secreto
```

### Instalacao com K8s Watcher (Single-Target)

```bash
helm install chatcli deploy/helm/chatcli \
  --set llm.provider=OPENAI \
  --set secrets.openaiApiKey=sk-xxx \
  --set watcher.enabled=true \
  --set watcher.deployment=myapp \
  --set watcher.namespace=production
```

### Instalacao com Multi-Target + Prometheus

Para monitorar multiplos deployments com metricas Prometheus, use um `values.yaml`:

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
- **Auto-detecta multi-namespace**: se targets estao em namespaces diferentes, usa `ClusterRole` em vez de `Role`
- Gera ConfigMap `<name>-watch-config` com o YAML multi-target
- Monta o config como volume e passa `--watch-config` ao container

### Valores do Helm Chart

#### Servidor

| Valor | Descricao | Padrao |
|-------|-----------|--------|
| `replicaCount` | Numero de replicas | `1` |
| `image.repository` | Repositorio da imagem | `ghcr.io/diillson/chatcli` |
| `image.tag` | Tag da imagem | `latest` |
| `server.port` | Porta gRPC | `50051` |
| `server.token` | Token de autenticacao | `""` |

#### TLS

| Valor | Descricao | Padrao |
|-------|-----------|--------|
| `tls.enabled` | Habilitar TLS | `false` |
| `tls.certFile` | Caminho do certificado | `""` |
| `tls.keyFile` | Caminho da chave | `""` |
| `tls.existingSecret` | Secret existente com certs | `""` |

#### LLM

| Valor | Descricao | Padrao |
|-------|-----------|--------|
| `llm.provider` | Provedor padrao | `""` |
| `llm.model` | Modelo padrao | `""` |

#### Secrets (API Keys)

| Valor | Descricao |
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

| Valor | Descricao | Padrao |
|-------|-----------|--------|
| `ollama.enabled` | Habilitar Ollama | `false` |
| `ollama.baseUrl` | URL base do Ollama | `http://ollama:11434` |
| `ollama.model` | Modelo Ollama | `""` |

#### K8s Watcher

| Valor | Descricao | Padrao |
|-------|-----------|--------|
| `watcher.enabled` | Habilitar o watcher | `false` |
| `watcher.targets` | Lista de targets multi-deployment (ver abaixo) | `[]` |
| `watcher.deployment` | Deployment unico - legado | `""` |
| `watcher.namespace` | Namespace do deployment - legado | `""` |
| `watcher.interval` | Intervalo de coleta | `30s` |
| `watcher.window` | Janela de observacao | `2h` |
| `watcher.maxLogLines` | Linhas de log por pod | `100` |
| `watcher.maxContextChars` | Budget de contexto LLM | `32000` |

**Campos de cada target** (`watcher.targets[].`):

| Campo | Descricao | Obrigatorio |
|-------|-----------|:-----------:|
| `deployment` | Nome do deployment | Sim |
| `namespace` | Namespace (padrao: `default`) | Nao |
| `metricsPort` | Porta Prometheus (0 = desabilitado) | Nao |
| `metricsPath` | Path HTTP das metricas | Nao (`/metrics`) |
| `metricsFilter` | Filtros glob para metricas | Nao |

#### Persistencia

| Valor | Descricao | Padrao |
|-------|-----------|--------|
| `persistence.enabled` | Persistir sessoes em PVC | `true` |
| `persistence.storageClass` | Storage class | `""` |
| `persistence.size` | Tamanho do volume | `1Gi` |

#### Seguranca

| Valor | Descricao | Padrao |
|-------|-----------|--------|
| `podSecurityContext.runAsNonRoot` | Obriga execucao como nao-root | `true` |
| `podSecurityContext.runAsUser` | UID do processo | `1000` |
| `podSecurityContext.seccompProfile.type` | Perfil seccomp | `RuntimeDefault` |
| `securityContext.allowPrivilegeEscalation` | Permite escalacao de privilegios | `false` |
| `securityContext.readOnlyRootFilesystem` | Filesystem somente-leitura | `true` |
| `securityContext.capabilities.drop` | Capabilities removidas | `ALL` |
| `rbac.clusterWide` | Usa ClusterRole em vez de Role namespace-scoped | `false` |

> Quando `readOnlyRootFilesystem` esta `true`, o chart monta automaticamente um tmpfs em `/tmp`. Para monitorar multiplos namespaces, habilite `rbac.clusterWide: true`. Veja a [documentacao de seguranca](/docs/features/security/) para detalhes.

#### Rede

| Valor | Descricao | Padrao |
|-------|-----------|--------|
| `service.type` | Tipo do Service | `ClusterIP` |
| `service.port` | Porta do Service | `50051` |
| `ingress.enabled` | Habilitar Ingress | `false` |

### Usando Secret Existente

Se voce ja tem um Secret com as API keys:

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

## Exemplo Completo: Producao

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

> Quando targets estao em namespaces diferentes (ex: `production` e `batch`), o chart cria automaticamente um `ClusterRole` em vez de `Role` namespace-scoped.

---

## Proximo Passo

- [Configurar o servidor](/docs/features/server-mode/)
- [Conectar ao servidor](/docs/features/remote-connect/)
- [Monitorar Kubernetes](/docs/features/k8s-watcher/)
