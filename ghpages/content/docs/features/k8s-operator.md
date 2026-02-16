+++
title = "Kubernetes Operator (CRD)"
linkTitle = "K8s Operator"
weight = 63
description = "Gerencie instancias ChatCLI no Kubernetes via Custom Resource Definition (CRD) com suporte a multi-target, Prometheus e RBAC automatico."
icon = "deployed_code"
+++

O **ChatCLI Operator** permite gerenciar instancias do ChatCLI como recursos nativos do Kubernetes usando um **Custom Resource Definition (CRD)**. O operator cria e gerencia automaticamente Deployments, Services, ConfigMaps, RBAC e PVCs.

---

## Instalacao do Operator

### Via Manifests

```bash
# Instalar CRD
kubectl apply -f operator/config/crd/bases/chatcli.diillson.com_chatcliinstances.yaml

# Instalar RBAC e Manager
kubectl apply -f operator/config/rbac/role.yaml
kubectl apply -f operator/config/manager/manager.yaml
```

### Via Docker Build

```bash
cd operator
make docker-build IMG=ghcr.io/diillson/chatcli-operator:latest
make docker-push IMG=ghcr.io/diillson/chatcli-operator:latest
```

---

## CRD: ChatCLIInstance

### Especificacao Completa

```yaml
apiVersion: chatcli.diillson.com/v1alpha1
kind: ChatCLIInstance
metadata:
  name: chatcli-prod
  namespace: default
spec:
  # Replicas do servidor ChatCLI
  replicas: 1

  # Provedor LLM (obrigatorio)
  provider: CLAUDEAI       # OPENAI, CLAUDEAI, GOOGLEAI, XAI, STACKSPOT, OLLAMA
  model: claude-sonnet-4-5  # Opcional

  # Imagem do container
  image:
    repository: ghcr.io/diillson/chatcli
    tag: latest
    pullPolicy: IfNotPresent

  # Servidor gRPC
  server:
    port: 50051
    tls:
      enabled: true
      secretName: chatcli-tls    # Secret com tls.crt e tls.key
    token:
      name: chatcli-auth          # Secret com o token
      key: token

  # K8s Watcher
  watcher:
    enabled: true
    interval: "30s"
    window: "2h"
    maxLogLines: 100
    maxContextChars: 8000

    # Multi-target (recomendado)
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
      - deployment: frontend
        namespace: production
        metricsPort: 3000
        metricsPath: "/custom-metrics"
        metricsFilter: ["next_*"]

    # Ou single-target (legado)
    # deployment: myapp
    # namespace: production

  # Resources
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 512Mi

  # Persistencia de sessoes
  persistence:
    enabled: true
    size: 1Gi
    storageClassName: standard

  # Security Context
  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    seccompProfile:
      type: RuntimeDefault

  # API Keys (Secret com as chaves dos provedores)
  apiKeys:
    name: chatcli-api-keys
```

---

## Campos do Spec

### Raiz

| Campo | Tipo | Obrigatorio | Padrao | Descricao |
|-------|------|:-----------:|--------|-----------|
| `replicas` | int32 | Nao | `1` | Numero de replicas do servidor |
| `provider` | string | **Sim** | | Provedor LLM |
| `model` | string | Nao | | Modelo LLM |
| `image` | ImageSpec | Nao | | Configuracao da imagem |
| `server` | ServerSpec | Nao | | Configuracao do servidor gRPC |
| `watcher` | WatcherSpec | Nao | | Configuracao do K8s Watcher |
| `resources` | ResourceRequirements | Nao | | Requests/limits de CPU e memoria |
| `persistence` | PersistenceSpec | Nao | | Persistencia de sessoes |
| `securityContext` | PodSecurityContext | Nao | nonroot/1000 | Security context do pod |
| `apiKeys` | SecretRefSpec | Nao | | Secret com API keys |

### WatcherSpec

| Campo | Tipo | Obrigatorio | Padrao | Descricao |
|-------|------|:-----------:|--------|-----------|
| `enabled` | bool | Nao | `false` | Ativa o watcher |
| `targets` | []WatchTargetSpec | Nao | | Lista de deployments (multi-target) |
| `deployment` | string | Nao | | Deployment unico (legado) |
| `namespace` | string | Nao | | Namespace do deployment (legado) |
| `interval` | string | Nao | `"30s"` | Intervalo de coleta |
| `window` | string | Nao | `"2h"` | Janela de observacao |
| `maxLogLines` | int32 | Nao | `100` | Max linhas de log por pod |
| `maxContextChars` | int32 | Nao | `8000` | Budget de contexto LLM |

### WatchTargetSpec

| Campo | Tipo | Obrigatorio | Padrao | Descricao |
|-------|------|:-----------:|--------|-----------|
| `deployment` | string | **Sim** | | Nome do deployment |
| `namespace` | string | **Sim** | | Namespace do deployment |
| `metricsPort` | int32 | Nao | `0` | Porta Prometheus (0 = desabilitado) |
| `metricsPath` | string | Nao | `/metrics` | Path do endpoint Prometheus |
| `metricsFilter` | []string | Nao | | Filtros glob para metricas |

---

## Recursos Criados pelo Operator

Quando voce aplica um `ChatCLIInstance`, o operator cria automaticamente:

| Recurso | Nome | Descricao |
|---------|------|-----------|
| **Deployment** | `<name>` | Pods do servidor ChatCLI |
| **Service** | `<name>` | ClusterIP para acesso gRPC |
| **ConfigMap** | `<name>` | Variaveis de ambiente (provider, model, etc.) |
| **ConfigMap** | `<name>-watch-config` | YAML multi-target (se `targets` definido) |
| **ServiceAccount** | `<name>` | Identity para RBAC |
| **Role/ClusterRole** | `<name>-watcher` | Permissoes K8s do watcher |
| **RoleBinding/CRB** | `<name>-watcher` | Binding da SA ao Role |
| **PVC** | `<name>-sessions` | Persistencia (se habilitada) |

### RBAC Automatico

- **Single-namespace** (todos os targets no mesmo namespace): Cria `Role` + `RoleBinding`
- **Multi-namespace** (targets em namespaces diferentes): Cria `ClusterRole` + `ClusterRoleBinding` automaticamente
- Na delecao do CR, cluster-scoped resources sao limpos pelo finalizer

---

## Exemplos

### Minimo (sem watcher)

```yaml
apiVersion: chatcli.diillson.com/v1alpha1
kind: ChatCLIInstance
metadata:
  name: chatcli-simple
spec:
  provider: OPENAI
  apiKeys:
    name: chatcli-api-keys
```

### Single-Target (legado)

```yaml
apiVersion: chatcli.diillson.com/v1alpha1
kind: ChatCLIInstance
metadata:
  name: chatcli-watcher
spec:
  provider: CLAUDEAI
  apiKeys:
    name: chatcli-api-keys
  watcher:
    enabled: true
    deployment: myapp
    namespace: production
    interval: "15s"
```

### Multi-Target com Prometheus

```yaml
apiVersion: chatcli.diillson.com/v1alpha1
kind: ChatCLIInstance
metadata:
  name: chatcli-multi
spec:
  provider: CLAUDEAI
  apiKeys:
    name: chatcli-api-keys
  server:
    port: 50051
    token:
      name: chatcli-auth
      key: token
  watcher:
    enabled: true
    interval: "30s"
    window: "2h"
    maxContextChars: 8000
    targets:
      - deployment: api-gateway
        namespace: production
        metricsPort: 9090
        metricsFilter: ["http_*", "grpc_*"]
      - deployment: auth-service
        namespace: production
        metricsPort: 9090
      - deployment: worker
        namespace: batch
      - deployment: ml-inference
        namespace: ml
        metricsPort: 8080
        metricsPath: "/prometheus"
        metricsFilter: ["model_*", "inference_*"]
  resources:
    requests:
      cpu: 200m
      memory: 256Mi
    limits:
      cpu: "1"
      memory: 1Gi
  persistence:
    enabled: true
    size: 5Gi
```

### Secret de API Keys

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: chatcli-api-keys
type: Opaque
stringData:
  ANTHROPIC_API_KEY: "sk-ant-xxx"
  # OPENAI_API_KEY: "sk-xxx"
  # GOOGLE_AI_API_KEY: "xxx"
```

---

## Status

O operator atualiza o status do CR com informacoes do Deployment:

```bash
kubectl get chatcliinstances
```

```
NAME            READY   REPLICAS   PROVIDER    AGE
chatcli-multi   true    1          CLAUDEAI    5m
```

### Conditions

| Condition | Status | Significado |
|-----------|--------|-------------|
| `Available` | True | Todas as replicas estao prontas |
| `Available` | False | Deployment nao esta totalmente disponivel |

```bash
kubectl describe chatcliinstance chatcli-multi
```

---

## Arquitetura do Operator

```
                  ┌─────────────────────────┐
                  │  ChatCLIInstance CR      │
                  │  (chatcli.diillson.com)  │
                  └────────────┬────────────┘
                               │
                  ┌────────────v────────────┐
                  │  ChatCLI Operator        │
                  │  (Reconciler)            │
                  └────────────┬────────────┘
                               │
          ┌────────────────────┼────────────────────┐
          │                    │                    │
    ┌─────v─────┐    ┌────────v────────┐   ┌──────v──────┐
    │ Deployment │    │  ConfigMaps     │   │   RBAC      │
    │ + Service  │    │ (env + watch    │   │ (Role or    │
    │ + SA + PVC │    │  config YAML)   │   │ ClusterRole)│
    └────────────┘    └─────────────────┘   └─────────────┘
```

O operator usa **OwnerReferences** para garbage collection automatica de recursos namespaced. Recursos cluster-scoped (ClusterRole/ClusterRoleBinding) sao limpos por um **finalizer**.

---

## Desenvolvimento

```bash
cd operator

# Build
make build

# Testes
make test

# Docker
make docker-build IMG=myregistry/chatcli-operator:dev

# Instalar CRD no cluster
make install

# Deploy o operator
make deploy IMG=myregistry/chatcli-operator:dev
```

---

## Proximo Passo

- [K8s Watcher (detalhes de coleta e budget)](/docs/features/k8s-watcher/)
- [Modo Servidor](/docs/features/server-mode/)
- [Receita: Monitoramento K8s](/docs/cookbook/k8s-monitoring/)
