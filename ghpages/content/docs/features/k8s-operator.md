+++
title = "Kubernetes Operator (AIOps)"
linkTitle = "K8s Operator"
weight = 63
description = "Gerencie instâncias ChatCLI e uma plataforma AIOps autônoma no Kubernetes com 6 CRDs, correlação de anomalias, análise por IA e remediação automática."
icon = "deployed_code"
+++

O **ChatCLI Operator** vai além do gerenciamento de instâncias. Ele implementa uma **plataforma AIOps completa** que detecta anomalias autônomamente, correlaciona sinais, solicita análise de IA e executa remediação — tudo sem dependências externas além do provedor LLM.

---

## API Group e CRDs

O operator usa o API group `platform.chatcli.io/v1alpha1` com 6 Custom Resource Definitions:

| CRD | Short Name | Descrição |
|-----|-----------|-----------|
| **Instance** | `inst` | Instancia do servidor ChatCLI (Deployment, Service, RBAC, PVC) |
| **Anomaly** | `anom` | Sinal bruto do K8s Watcher (restarts, OOM, falhas de deploy) |
| **Issue** | `iss` | Incidente correlacionado agrupando múltiplas anomalias |
| **AIInsight** | `ai` | Análise de causa raiz gerada por IA com ações sugeridas |
| **RemediationPlan** | `rp` | Ações concretas para resolver o problema |
| **Runbook** | `rb` | Procedimentos operacionais manuais (opcional) |

---

## Instalação do Operator

### Via Manifests

```bash
# Instalar todos os CRDs
kubectl apply -f operator/config/crd/bases/

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

## Arquitetura da Plataforma AIOps

```mermaid
graph TD
    subgraph "ChatCLI Server"
        W[K8s Watcher<br/>collectors] --> GA[GetAlerts RPC]
        AI_RPC[AnalyzeIssue RPC<br/>LLM call]
    end

    subgraph "Operator"
        WB[WatcherBridge<br/>polls cada 30s] -->|gRPC| GA
        WB -->|cria| ANOM[Anomaly CR]
        ANOM -->|AnomalyReconciler<br/>correlação| ISS[Issue CR]
        ISS -->|IssueReconciler<br/>cria| INSIGHT[AIInsight CR]
        INSIGHT -->|AIInsightReconciler<br/>gRPC| AI_RPC
        AI_RPC -->|análise + ações| INSIGHT
        ISS -->|cria| PLAN[RemediationPlan CR]
        PLAN -->|RemediationReconciler<br/>executa| ACTIONS[Scale / Restart<br/>Rollback / Patch]
        ACTIONS -->|sucesso| RESOLVED[Issue Resolved]
        ACTIONS -->|falha| RETRY[Retry ou Escalate]
    end

    style ANOM fill:#f9e2af,color:#000
    style ISS fill:#fab387,color:#000
    style INSIGHT fill:#89b4fa,color:#000
    style PLAN fill:#a6e3a1,color:#000
    style RESOLVED fill:#a6e3a1,color:#000
    style RETRY fill:#f38ba8,color:#000
```

### Pipeline Autônomo

| Fase | Componente | O que Faz |
|------|-----------|-----------|
| **1. Detecção** | WatcherBridge | Consulta `GetAlerts` do servidor a cada 30s. Cria Anomaly CRs para cada alerta novo (dedup via SHA256 com bucket de minuto). |
| **2. Correlação** | AnomalyReconciler + CorrelationEngine | Agrupa anomalias por recurso + janela temporal. Calcula risk score e severidade. Cria/atualiza Issue CRs. |
| **3. Analise** | IssueReconciler + AIInsightReconciler | Cria AIInsight CR. Chama `AnalyzeIssue` RPC que envia contexto ao LLM. Retorna: análise, confiança, recomendações e ações sugeridas. |
| **4. Remediação** | IssueReconciler | Cria RemediationPlan a partir de: **(a)** Runbook existente, **(b)** ações sugeridas pela IA (fallback automático), ou **(c)** escalona se nenhum disponível. |
| **5. Execução** | RemediationReconciler | Executa ações no cluster: ScaleDeployment, RestartDeployment, RollbackDeployment, PatchConfig. |
| **6. Resolução** | IssueReconciler | Sucesso → Issue resolvido. Falha → retry (até maxAttempts) → escalona. |

### Máquina de Estados do Issue

```mermaid
stateDiagram-v2
    [*] --> Detected
    Detected --> Analyzing : AIInsight criado
    Analyzing --> Remediating : RemediationPlan criado
    Analyzing --> Escalated : Sem Runbook e sem ações AI
    Remediating --> Resolved : Remediação bem-sucedida
    Remediating --> Remediating : Retry (attempt < max)
    Remediating --> Escalated : Max tentativas ou sem plano de retry
    Resolved --> [*]
    Escalated --> [*]
```

---

## CRD: Instance

O `Instance` gerencia instâncias do servidor ChatCLI no cluster. Substitui o antigo `ChatCLIInstance` (`chatcli.diillson.com`).

### Especificação Completa

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-prod
  namespace: default
spec:
  replicas: 1
  provider: CLAUDEAI       # OPENAI, CLAUDEAI, GOOGLEAI, XAI, STACKSPOT, OLLAMA
  model: claude-sonnet-4-5

  image:
    repository: ghcr.io/diillson/chatcli
    tag: latest
    pullPolicy: IfNotPresent

  server:
    port: 50051
    tls:
      enabled: true
      secretName: chatcli-tls
    token:
      name: chatcli-auth
      key: token

  watcher:
    enabled: true
    interval: "30s"
    window: "2h"
    maxLogLines: 100
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

  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 500m
      memory: 512Mi

  persistence:
    enabled: true
    size: 1Gi
    storageClassName: standard

  securityContext:
    runAsNonRoot: true
    runAsUser: 1000
    seccompProfile:
      type: RuntimeDefault

  apiKeys:
    name: chatcli-api-keys
```

### Campos do Spec

#### Raiz

| Campo | Tipo | Obrigatório | Padrão | Descrição |
|-------|------|:-----------:|--------|-----------|
| `replicas` | int32 | Não | `1` | Número de réplicas do servidor |
| `provider` | string | **Sim** | | Provedor LLM |
| `model` | string | Não | | Modelo LLM |
| `image` | ImageSpec | Não | | Configuração da imagem |
| `server` | ServerSpec | Não | | Configuração do servidor gRPC |
| `watcher` | WatcherSpec | Não | | Configuração do K8s Watcher |
| `resources` | ResourceRequirements | Não | | Requests/limits de CPU e memória |
| `persistence` | PersistenceSpec | Não | | Persistência de sessões |
| `securityContext` | PodSecurityContext | Não | nonroot/1000 | Security context do pod |
| `apiKeys` | SecretRefSpec | Não | | Secret com API keys |

#### WatcherSpec

| Campo | Tipo | Obrigatório | Padrão | Descrição |
|-------|------|:-----------:|--------|-----------|
| `enabled` | bool | Não | `false` | Ativa o watcher |
| `targets` | []WatchTargetSpec | Não | | Lista de deployments (multi-target) |
| `deployment` | string | Não | | Deployment único (legado) |
| `namespace` | string | Não | | Namespace do deployment (legado) |
| `interval` | string | Não | `"30s"` | Intervalo de coleta |
| `window` | string | Não | `"2h"` | Janela de observação |
| `maxLogLines` | int32 | Não | `100` | Max linhas de log por pod |
| `maxContextChars` | int32 | Não | `32000` | Budget de contexto LLM |

#### WatchTargetSpec

| Campo | Tipo | Obrigatório | Padrão | Descrição |
|-------|------|:-----------:|--------|-----------|
| `deployment` | string | **Sim** | | Nome do deployment |
| `namespace` | string | **Sim** | | Namespace do deployment |
| `metricsPort` | int32 | Não | `0` | Porta Prometheus (0 = desabilitado) |
| `metricsPath` | string | Não | `/metrics` | Path do endpoint Prometheus |
| `metricsFilter` | []string | Não | | Filtros glob para métricas |

### Recursos Criados pelo Instance

| Recurso | Nome | Descrição |
|---------|------|-----------|
| **Deployment** | `<name>` | Pods do servidor ChatCLI |
| **Service** | `<name>` | Service gRPC (headless automático quando réplicas > 1 para LB client-side) |
| **ConfigMap** | `<name>` | Variáveis de ambiente (provider, model, etc.) |
| **ConfigMap** | `<name>-watch-config` | YAML multi-target (se `targets` definido) |
| **ServiceAccount** | `<name>` | Identity para RBAC |
| **Role/ClusterRole** | `<name>-watcher` | Permissões K8s do watcher |
| **RoleBinding/CRB** | `<name>-watcher` | Binding da SA ao Role |
| **PVC** | `<name>-sessions` | Persistência (se habilitada) |

### Balanceamento gRPC

O gRPC usa conexões HTTP/2 persistentes que fixam em um único pod via kube-proxy, deixando réplicas extras ociosas.

- **1 réplica** (padrão): Service ClusterIP padrão
- **Múltiplas réplicas**: Service headless (`ClusterIP: None`) é criado automaticamente, habilitando round-robin client-side via resolver `dns:///` do gRPC
- **Keepalive**: WatcherBridge faz ping a cada 10s (timeout de 3s) para detectar pods inativos rapidamente
- **Transição**: Ao escalar de 1 para 2+ réplicas (ou voltar), o operator deleta e recria o Service automaticamente (ClusterIP é imutável no Kubernetes)

### RBAC Automático

- **Single-namespace** (todos os targets no mesmo namespace): Cria `Role` + `RoleBinding`
- **Multi-namespace** (targets em namespaces diferentes): Cria `ClusterRole` + `ClusterRoleBinding` automaticamente
- Na deleção do CR, cluster-scoped resources são limpos pelo finalizer

---

## CRDs da Plataforma AIOps

### Anomaly

Representa um sinal bruto detectado pelo WatcherBridge.

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Anomaly
metadata:
  name: watcher-highrestartcount-api-gateway-1234567890
  namespace: production
spec:
  signalType: pod_restart    # pod_restart | oom_kill | pod_not_ready | deploy_failing | error_rate | latency_spike
  source: watcher            # watcher | prometheus | manual
  severity: warning          # critical | high | medium | low | warning
  resource:
    kind: Deployment
    name: api-gateway
    namespace: production
  description: "HighRestartCount on api-gateway: container app restarted 8 times"
  detectedAt: "2026-02-16T10:30:00Z"
status:
  correlated: true
  issueRef:
    name: api-gateway-pod-restart-1771276354
```

#### Campos do Anomaly Spec

| Campo | Tipo | Descrição |
|-------|------|-----------|
| `signalType` | AnomalySignalType | Tipo do sinal detectado |
| `source` | AnomalySource | Origem da detecção (watcher, prometheus, manual) |
| `severity` | IssueSeverity | Severidade do sinal |
| `resource` | ResourceRef | Recurso K8s afetado (kind, name, namespace) |
| `description` | string | Descrição legível do problema |
| `detectedAt` | Time | Timestamp da detecção |

#### Sinais Detectados pelo Watcher

| AlertType (Server) | SignalType (Anomaly) | Descrição |
|--------------------|---------------------|-----------|
| `HighRestartCount` | `pod_restart` | Pod com muitos restarts (CrashLoopBackOff) |
| `OOMKilled` | `oom_kill` | Container terminado por falta de memória |
| `PodNotReady` | `pod_not_ready` | Pod não está no estado Ready |
| `DeploymentFailing` | `deploy_failing` | Deployment com Available=False |

### Issue

Incidente correlacionado que agrupa anomalias e gerencia o ciclo de vida da remediação.

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Issue
metadata:
  name: api-gateway-pod-restart-1771276354
  namespace: production
spec:
  severity: high
  source: watcher
  description: "Correlated incident: pod_restart on api-gateway"
  resource:
    kind: Deployment
    name: api-gateway
    namespace: production
  riskScore: 65
  correlatedAnomalies:
    - name: watcher-highrestartcount-api-gateway-1234567890
    - name: watcher-oomkilled-api-gateway-1234567891
status:
  state: Analyzing          # Detected | Analyzing | Remediating | Resolved | Escalated | Failed
  remediationAttempts: 0
  maxRemediationAttempts: 3
  detectedAt: "2026-02-16T10:30:00Z"
  conditions:
    - type: Analyzing
      status: "True"
      reason: AIInsightCreated
```

#### Estados do Issue

| Estado | Descrição |
|--------|-----------|
| `Detected` | Issue recém-criado, aguardando análise |
| `Analyzing` | AIInsight criado, aguardando resposta da IA |
| `Remediating` | RemediationPlan em execução |
| `Resolved` | Remediação bem-sucedida |
| `Escalated` | Max tentativas atingido ou sem ações disponíveis |
| `Failed` | Falha terminal |

### AIInsight

Análise de causa raiz gerada por IA com ações sugeridas para remediação automática.

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: AIInsight
metadata:
  name: api-gateway-pod-restart-1771276354-insight
  namespace: production
spec:
  issueRef:
    name: api-gateway-pod-restart-1771276354
  provider: CLAUDEAI
  model: claude-sonnet-4-5
status:
  analysis: "High restart count caused by OOMKilled. Container memory limit (512Mi) is insufficient for the current workload pattern."
  confidence: 0.87
  recommendations:
    - "Increase memory limit to 1Gi"
    - "Investigate possible memory leak in the application"
    - "Monitor GC pressure metrics"
  suggestedActions:
    - name: "Restart deployment"
      action: RestartDeployment
      description: "Restart pods to reclaim leaked memory immediately"
    - name: "Scale up replicas"
      action: ScaleDeployment
      description: "Add more replicas to distribute memory pressure"
      params:
        replicas: "4"
  generatedAt: "2026-02-16T10:31:00Z"
```

#### Campos do AIInsight Status

| Campo | Tipo | Descrição |
|-------|------|-----------|
| `analysis` | string | Análise de causa raiz gerada pela IA |
| `confidence` | float64 | Nível de confiança da análise (0.0-1.0) |
| `recommendations` | []string | Recomendações legiveis para humanos |
| `suggestedActions` | []SuggestedAction | Ações estruturadas para remediação automática |
| `generatedAt` | Time | Quando a análise foi gerada |

#### SuggestedAction

| Campo | Tipo | Descrição |
|-------|------|-----------|
| `name` | string | Nome legível da ação |
| `action` | string | Tipo da ação: `ScaleDeployment`, `RestartDeployment`, `RollbackDeployment`, `PatchConfig` |
| `description` | string | Explicação do motivo desta ação |
| `params` | map[string]string | Parâmetros da ação (ex: `replicas: "4"`) |

### RemediationPlan

Plano concreto de remediação gerado automaticamente a partir de Runbook ou ações da IA.

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: RemediationPlan
metadata:
  name: api-gateway-pod-restart-1771276354-plan-1
  namespace: production
spec:
  issueRef:
    name: api-gateway-pod-restart-1771276354
  attempt: 1
  strategy: "Attempt 1 (AI-generated): High restart count caused by OOMKilled"
  actions:
    - type: RestartDeployment
    - type: ScaleDeployment
      params:
        replicas: "4"
  safetyConstraints:
    - "No delete operations"
    - "No destructive changes"
    - "Rollback on failure"
status:
  state: Completed           # Pending | Executing | Completed | Failed | RolledBack
  result: "Deployment restarted and scaled to 4 replicas successfully"
  startedAt: "2026-02-16T10:31:30Z"
  completedAt: "2026-02-16T10:32:15Z"
```

#### Tipos de Ação

| Tipo | Descrição | Parâmetros |
|------|-----------|-----------|
| `ScaleDeployment` | Ajusta o número de réplicas | `replicas` |
| `RestartDeployment` | Rollout restart do deployment | — |
| `RollbackDeployment` | Desfaz o último rollout | — |
| `PatchConfig` | Atualiza chaves de um ConfigMap | `configmap`, `key=value` |
| `Custom` | Ação personalizada (bloqueada por safety checks) | — |

### Runbook (Opcional)

Procedimentos operacionais manuais. Quando um Runbook corresponde ao issue, ele tem prioridade sobre as ações da IA.

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Runbook
metadata:
  name: high-error-rate-deployment
  namespace: production
spec:
  description: "Standard procedure for high error rate incidents on Deployments"
  trigger:
    signalType: error_rate
    severity: high
    resourceKind: Deployment
  steps:
    - name: Scale up
      action: ScaleDeployment
      description: "Increase replicas to absorb the error spike"
      params:
        replicas: "4"
    - name: Rollback
      action: RollbackDeployment
      description: "Revert to previous stable version if scaling doesn't help"
  maxAttempts: 3
```

#### Prioridade de Remediação

```
1. Runbook existente que corresponde (signalType + severity + resourceKind)
2. Ações sugeridas pela IA (suggestedActions do AIInsight)
3. Escalonamento (se nenhum dos dois disponíveis)
```

---

## Correlation Engine

O motor de correlação agrupa anomalias em issues usando:

### Risk Scoring

Cada tipo de sinal tem um peso:

| Sinal | Peso |
|-------|------|
| `oom_kill` | 30 |
| `error_rate` | 25 |
| `deploy_failing` | 25 |
| `latency_spike` | 20 |
| `pod_restart` | 20 |
| `pod_not_ready` | 20 |

O risk score é a soma dos pesos das anomalias correlacionadas (maximo 100).

### Classificação de Severidade

| Risk Score | Severidade |
|-----------|-----------|
| >= 80 | Critical |
| >= 60 | High |
| >= 40 | Medium |
| < 40 | Low |

### Agrupamento

- Anomalias no **mesmo recurso** (deployment + namespace) dentro da **mesma janela temporal** são agrupadas no mesmo Issue
- **Incident ID** deterministico: hash do recurso + tipo de sinal (evita duplicatas)

---

## WatcherBridge

O `WatcherBridge` e o componente que conecta o servidor ChatCLI ao operator:

- **Polling**: Consulta `GetAlerts` do servidor a cada 30 segundos
- **Descoberta**: Localiza o servidor via Instance CRs (primeiro Instance com endpoint gRPC pronto)
- **Dedup**: Hash SHA256 com bucket de minuto + TTL de 2 horas
- **Poda**: Remove hashes expirados automaticamente (> 2h)
- **Criação**: Converte alertas em Anomaly CRs com nomes K8s válidos

---

## Exemplos de Uso

### Minimo (sem AIOps)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-simple
spec:
  provider: OPENAI
  apiKeys:
    name: chatcli-api-keys
```

### AIOps Completo

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-aiops
spec:
  provider: CLAUDEAI
  apiKeys:
    name: chatcli-api-keys
  server:
    port: 50051
  watcher:
    enabled: true
    interval: "15s"
    maxContextChars: 32000
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

### Runbook Manual (opcional)

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Runbook
metadata:
  name: oom-kill-runbook
  namespace: production
spec:
  description: "Procedure for OOMKilled containers"
  trigger:
    signalType: oom_kill
    severity: critical
    resourceKind: Deployment
  steps:
    - name: Restart pods
      action: RestartDeployment
      description: "Restart to reclaim leaked memory"
    - name: Scale up
      action: ScaleDeployment
      description: "Add replicas to distribute memory pressure"
      params:
        replicas: "5"
  maxAttempts: 2
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

## Status e Monitoramento

### Verificar Instâncias

```bash
kubectl get instances
```
```
NAME            READY   REPLICAS   PROVIDER    AGE
chatcli-aiops   true    1          CLAUDEAI    5m
```

### Verificar Issues Ativos

```bash
kubectl get issues -A
```
```
NAME                                    SEVERITY   STATE         RISK   AGE
api-gateway-pod-restart-1771276354      high       Remediating   65     2m
worker-oom-kill-3847291023              critical   Analyzing     90     30s
```

### Verificar Insights da IA

```bash
kubectl get aiinsights -A
```
```
NAME                                           ISSUE                                   PROVIDER   CONFIDENCE   AGE
api-gateway-pod-restart-1771276354-insight      api-gateway-pod-restart-1771276354      CLAUDEAI   0.87         1m
```

### Verificar Planos de Remediação

```bash
kubectl get remediationplans -A
```
```
NAME                                          ISSUE                                   ATTEMPT   STATE       AGE
api-gateway-pod-restart-1771276354-plan-1      api-gateway-pod-restart-1771276354      1         Completed   1m
```

### Verificar Anomalias

```bash
kubectl get anomalies -A
```
```
NAME                                               SIGNAL        SOURCE    SEVERITY   AGE
watcher-highrestartcount-api-gateway-1234567890     pod_restart   watcher   warning    3m
watcher-oomkilled-worker-9876543210                 oom_kill      watcher   critical   1m
```

---

## Desenvolvimento

```bash
cd operator

# Build
go build ./...

# Testes (86 funções, 115 com subtests)
go test ./... -v

# Docker (deve ser construído a partir do root do repositório)
docker build -f operator/Dockerfile -t myregistry/chatcli-operator:dev .

# Instalar CRDs no cluster
kubectl apply -f config/crd/bases/

# Deploy o operator
make deploy IMG=myregistry/chatcli-operator:dev
```

---

## Próximo Passo

- [AIOps Platform (deep-dive arquitetura)](/docs/features/aiops-platform/)
- [K8s Watcher (detalhes de coleta e budget)](/docs/features/k8s-watcher/)
- [Modo Servidor (RPCs GetAlerts e AnalyzeIssue)](/docs/features/server-mode/)
- [Receita: Monitoramento K8s com IA](/docs/cookbook/k8s-monitoring/)
