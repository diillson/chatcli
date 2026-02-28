+++
title = "Monitoramento Kubernetes (K8s Watcher)"
linkTitle = "K8s Watcher"
weight = 62
description = "Monitore múltiplos deployments Kubernetes simultaneamente com métricas Prometheus, gestão de budget de contexto e análise por IA."
icon = "monitoring"
+++

O **K8s Watcher** permite que o ChatCLI monitore **múltiplos deployments simultaneamente**, coletando métricas de infra e aplicação, logs, eventos e status de pods. O contexto é automaticamente injetado nos prompts do LLM com **gestão inteligente de budget** para não estourar a janela de contexto.

---

## Arquitetura

### Single-Target (legado)

```
ChatCLI → ResourceWatcher → 6 Collectors → ObservabilityStore → Summarizer → LLM
```

### Multi-Target (atual)

```
                        ┌→ ResourceWatcher[0] → Store[0] ─┐
ChatCLI → MultiWatcher ─┼→ ResourceWatcher[1] → Store[1] ─┼→ MultiSummarizer → LLM
                        └→ ResourceWatcher[N] → Store[N] ─┘   (budget-controlled)
```

Cada `ResourceWatcher` possui seus próprios collectors (incluindo `PrometheusCollector` opcional) e todos compartilham **um único clientset Kubernetes**, minimizando conexões.

---

## Modos de Uso

### 1. Deployment Único (legado)

```bash
chatcli watch --deployment myapp --namespace production
chatcli watch --deployment myapp -p "O deployment está saudável?"
```

### 2. Multiplos Deployments (config YAML)

```bash
chatcli watch --config targets.yaml
chatcli watch --config targets.yaml -p "Quais deployments precisam de atenção?"
```

### 3. Servidor com Watcher

```bash
# Multi-target
chatcli server --watch-config targets.yaml

# Single-target (legado)
chatcli server --watch-deployment myapp --watch-namespace production
```

Clientes conectados via `chatcli connect` recebem o contexto K8s automaticamente.

---

## Arquivo de Configuração Multi-Target

```yaml
# targets.yaml
interval: "30s"           # Intervalo de coleta
window: "2h"              # Janela temporal de dados mantidos
maxLogLines: 100          # Linhas de log por pod por ciclo
maxContextChars: 32000     # Budget máximo de caracteres para contexto LLM

targets:
  - deployment: api-gateway
    namespace: production
    metricsPort: 9090                                        # Porta Prometheus
    metricsFilter: ["http_requests_total", "http_request_duration_*"]

  - deployment: auth-service
    namespace: production
    metricsPort: 9090

  - deployment: worker
    namespace: batch
    # Sem metricsPort = Prometheus desabilitado para este target

  - deployment: frontend
    namespace: production
    metricsPort: 3000
    metricsPath: "/custom-metrics"                           # Path customizado
    metricsFilter: ["next_*", "react_render_*"]
```

### Campos do Target

| Campo | Descrição | Obrigatório |
|-------|-----------|:-----------:|
| `deployment` | Nome do deployment | Sim |
| `namespace` | Namespace (padrão: `default`) | Nao |
| `metricsPort` | Porta do endpoint Prometheus (0 = desabilitado) | Nao |
| `metricsPath` | Path HTTP das métricas (padrão: `/metrics`) | Nao |
| `metricsFilter` | Filtros glob para métricas (vazio = todas) | Nao |

---

## Flags Completas

### `chatcli watch`

| Flag | Descrição | Padrão | Env Var |
|------|-----------|--------|---------|
| `--config` | Arquivo YAML multi-target | | |
| `--deployment` | Deployment único (legado) | | `CHATCLI_WATCH_DEPLOYMENT` |
| `--namespace` | Namespace do deployment | `default` | `CHATCLI_WATCH_NAMESPACE` |
| `--interval` | Intervalo entre coletas | `30s` | `CHATCLI_WATCH_INTERVAL` |
| `--window` | Janela temporal de dados | `2h` | `CHATCLI_WATCH_WINDOW` |
| `--max-log-lines` | Linhas de log por pod | `100` | `CHATCLI_WATCH_MAX_LOG_LINES` |
| `--kubeconfig` | Caminho do kubeconfig | Auto-detectado | `CHATCLI_KUBECONFIG` |
| `--provider` | Provedor de LLM | `.env` | `LLM_PROVIDER` |
| `--model` | Modelo de LLM | `.env` | |
| `-p <prompt>` | One-shot: envia e sai | | |
| `--max-tokens` | Limite de tokens na resposta | | |

### `chatcli server` (flags do watcher)

| Flag | Descrição | Padrão | Env Var |
|------|-----------|--------|---------|
| `--watch-config` | Arquivo YAML multi-target | | `CHATCLI_WATCH_CONFIG` |
| `--watch-deployment` | Deployment único (legado) | | `CHATCLI_WATCH_DEPLOYMENT` |
| `--watch-namespace` | Namespace | `default` | `CHATCLI_WATCH_NAMESPACE` |
| `--watch-interval` | Intervalo de coleta | `30s` | `CHATCLI_WATCH_INTERVAL` |
| `--watch-window` | Janela de observação | `2h` | `CHATCLI_WATCH_WINDOW` |
| `--watch-max-log-lines` | Max linhas de log | `100` | `CHATCLI_WATCH_MAX_LOG_LINES` |
| `--watch-kubeconfig` | Caminho do kubeconfig | Auto-detectado | `CHATCLI_KUBECONFIG` |

---

## O que e Coletado

### Collectors por Target

| Collector | Dados Coletados |
|-----------|----------------|
| **Deployment** | Replicas (ready/available/updated), strategy, conditions |
| **Pod Status** | Fase, readiness, restarts, termination info, container status |
| **Events** | Eventos K8s (Warning/Normal), mensagem, razao, timestamp |
| **Logs** | Ultimas N linhas por container por pod |
| **Metrics** | CPU e memória por pod (via metrics-server) |
| **HPA** | Min/max replicas, métricas atuais, replicas desejadas |
| **Prometheus** | Métricas de aplicação do endpoint `/metrics` dos pods |

### Prometheus Collector (Novo)

O `PrometheusCollector` scrapa métricas Prometheus diretamente dos pods:

- Descobre pods do deployment e seleciona **1 pod Ready**
- Faz HTTP GET em `http://podIP:port/path` (timeout: 5s)
- Parseia o formato Prometheus text exposition (stdlib, sem dependencias)
- Filtra por **glob patterns** configurados
- Ignora NaN, Inf e linhas de comentario

**Exemplos de filtros glob:**

```yaml
metricsFilter:
  - "http_requests_*"          # Todas as métricas HTTP
  - "process_*"                # Métricas de processo
  - "go_goroutines"            # Métrica específica
  - "*_duration_seconds_*"     # Qualquer métrica de duração
```

---

## Gestão de Budget de Contexto (MultiSummarizer)

Com múltiplos targets, o **MultiSummarizer** garante que o contexto não estoure a janela do LLM:

### Algoritmo

1. **Pontua** cada target: `0 = healthy`, `1 = warning`, `2 = critical`
   - **Critical**: CrashLoopBackOff, OOMKilled, alerts criticos
   - **Warning**: replicas < desired, error logs, alerts warning
   - **Healthy**: tudo ok
2. **Ordena**: critical primeiro, depois warning, depois healthy
3. **Aloca contexto**:
   - Score >= 1 → contexto completo (~1-3 KB por target)
   - Score == 0 → one-liner compacto (~80 chars por target)
4. **Se excede `maxContextChars`** → comprime targets saudáveis primeiro
5. **Se ainda excede** → omite targets saudáveis

### Exemplo com 20 Targets (2 com problemas)

```
[K8s Multi-Watcher: 20 targets monitored]

--- Targets Requiring Attention ---

[K8s Context: deployment/api-gateway in namespace/production]
Collected at: 2026-02-15T10:30:00Z

## Deployment Status
  Replicas: 2/3 ready, 3 updated, 2 available
  Strategy: RollingUpdate

## Pods (3 total)
  Total restarts: 12 (delta in window: 8)
  - api-gateway-abc12: Running [Ready] restarts=0 cpu=45m mem=128Mi
  - api-gateway-def34: Running [Ready] restarts=0 cpu=52m mem=135Mi
  - api-gateway-ghi56: Running [NOT READY] restarts=8 cpu=12m mem=95Mi
    Last terminated: OOMKilled (exit code 137) at 2026-02-15T10:28:00Z

## Application Metrics (4)
  http_request_duration_seconds_sum: 8453
  http_requests_total: 1.542e+06
  process_resident_memory_bytes: 1.34e+08
  go_goroutines: 245

## Active Alerts (2)
  [CRITICAL] CrashLoopBackOff: pod/api-gateway-ghi56
  [CRITICAL] OOMKilled: pod/api-gateway-ghi56

## Recent Error Logs (3)
  [10:27:45] api-gateway-ghi56/app: OutOfMemoryError: heap space
  [10:27:46] api-gateway-ghi56/app: Shutting down...
  [10:28:00] api-gateway-ghi56/app: Process exited with code 137

--- Healthy Targets ---
- production/auth-service: 3/3 pods ready | healthy | 0 alerts | 42 snapshots
- production/frontend: 2/2 pods ready | healthy | 0 alerts | 42 snapshots
- production/backend: 5/5 pods ready | healthy | 0 alerts | 42 snapshots
- batch/worker: 3/3 pods ready | healthy | 0 alerts | 42 snapshots
... (16 targets compactos)
```

**Budget total**: ~2 KB (detail) + 18 x 80 chars (compact) = ~3.5 KB, dentro do limite de 8 KB.

---

## Detecção de Anomalias

| Anomalia | Condição | Severidade |
|----------|----------|------------|
| **CrashLoopBackOff** | Pod com mais de 5 restarts | Critical |
| **OOMKilled** | Container terminado por falta de memória | Critical |
| **PodNotReady** | Pod não está no estado Ready | Warning |
| **DeploymentFailing** | Deployment com Available=False | Critical |

Os alertas sao incluidos no contexto enviado ao LLM e influenciam a **prioridade de budget** do MultiSummarizer.

---

## Observability Store

Os dados coletados sao armazenados em um **ring buffer** por target com janela temporal configuravel:

- **Snapshots**: Estado completo periodico (pods, deployment, HPA, events, metrics, app metrics)
- **Logs**: Logs recentes de cada pod com classificação (info/warning/error)
- **Alertas**: Anomalias detectadas com severidade e timestamps

### Rotação Automática

Dados mais antigos que a janela temporal (`--window`) sao automaticamente descartados, mantendo o uso de memória constante independente do número de targets.

---

## Comando `/watch`

Dentro do ChatCLI interativo (local ou remoto), use `/watch` para ver o estado:

### Single-Target

```
/watch
K8s Watcher Active
  Deployment:  myapp
  Namespace:   production
  Snapshots:   42
  Pods:        3
  Alerts:      1
```

### Multi-Target

```
/watch
K8s Multi-Watcher Active
  Watching 20 targets: 18 healthy, 1 warning, 1 critical
```

---

## One-Shot com Contexto K8s

```bash
# Deployment unico
chatcli watch --deployment myapp -p "O deployment está saudável?"

# Multi-target
chatcli watch --config targets.yaml -p "Resuma o status de todos os deployments"

# Via servidor remoto
chatcli connect meuservidor:50051 -p "Por que os pods estão reiniciando?"
```

---

## Exemplos de Perguntas

```
> O deployment está saudável?
> Quais deployments precisam de atenção?
> Por que o pod xyz está reiniciando?
> Analise as métricas HTTP do api-gateway. O latency está aceitável?
> Compare o estado do auth-service com 30 minutos atrás
> Quais eventos de warning ocorreram na última hora?
> Baseado nas métricas Prometheus, preciso escalar algum deployment?
> Resuma o status de todos os targets para um report de equipe
```

---

## Requisitos

- **Kubernetes Cluster**: Acesso via kubeconfig ou in-cluster config
- **Permissões RBAC**: Leitura de pods, eventos, logs, deployments, HPA, ingresses
- **metrics-server** (opcional): Para coleta de CPU/memória
- **Prometheus endpoints** (opcional): Apps que expoe `/metrics` no formato Prometheus text

### RBAC

**Single-namespace**: Use `Role` + `RoleBinding`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: chatcli-watcher
  namespace: production
rules:
  - apiGroups: [""]
    resources: ["pods", "pods/log", "events", "services", "endpoints"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["deployments", "replicasets", "statefulsets", "daemonsets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["autoscaling"]
    resources: ["horizontalpodautoscalers"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get", "list"]
  - apiGroups: ["metrics.k8s.io"]
    resources: ["pods"]
    verbs: ["get", "list"]
```

**Multi-namespace**: Quando os targets estão em namespaces diferentes, use `ClusterRole` + `ClusterRoleBinding` com as mesmas regras. O Operator faz isso automaticamente.

---

## Integração com AIOps

Os alertas do K8s Watcher alimentam automaticamente o **pipeline AIOps** do Operator. Quando o Operator detecta alertas via `GetAlerts` RPC, ele cria Anomaly CRs que são correlacionados em Issues, analisados por IA e remediados automaticamente.

Alertas detectados pelo Watcher → Anomaly → Issue → AIInsight → RemediationPlan → Resolução

Veja [AIOps Platform](/docs/features/aiops-platform/) para o fluxo completo.

---

## Próximo Passo

- [Configurar o servidor com watcher](/docs/features/server-mode/)
- [K8s Operator (AIOps)](/docs/features/k8s-operator/)
- [AIOps Platform (deep-dive)](/docs/features/aiops-platform/)
- [Deploy no Kubernetes](/docs/getting-started/docker-deployment/)
- [Receita: Monitoramento K8s na pratica](/docs/cookbook/k8s-monitoring/)
