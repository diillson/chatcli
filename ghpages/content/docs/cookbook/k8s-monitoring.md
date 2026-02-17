+++
title = "Receita: Monitoramento K8s com IA"
linkTitle = "Monitoramento K8s"
weight = 71
description = "Guia passo a passo para monitorar deployments Kubernetes com IA, diagnosticar problemas e automatizar analises usando o K8s Watcher."
icon = "troubleshoot"
+++

Nesta receita, voce vai configurar o ChatCLI para monitorar um deployment Kubernetes e usar IA para diagnosticar problemas em tempo real.

---

## Cenario

- Aplicacao "myapp" em producao no Kubernetes
- Equipe precisa diagnosticar problemas rapidamente
- Deseja usar IA para analisar logs, eventos e metricas
- Quer contexto K8s automatico em todas as perguntas

---

## Opcao 1: Monitoramento Local

Use esta opcao quando voce tem acesso direto ao cluster via `kubectl`.

### Passo 1: Verificar Acesso ao Cluster

```bash
# Verificar conectividade
kubectl get pods -n production

# Verificar permissoes
kubectl auth can-i get pods -n production
kubectl auth can-i get pods/log -n production
kubectl auth can-i list events -n production
```

### Passo 2: Iniciar o Watcher

```bash
chatcli watch --deployment myapp --namespace production
```

Voce vera:

```
K8s Watcher starting...
  Deployment: myapp
  Namespace:  production
  Interval:   30s
  Window:     2h

Collecting initial data...
Initial data collected. Starting interactive mode.
[watch] chatcli>
```

### Passo 3: Fazer Perguntas

```
[watch] chatcli> O deployment esta saudavel?

Com base nos dados coletados do Kubernetes:
- O deployment myapp tem 3/3 replicas disponiveis
- Todos os pods estao no estado Running e Ready
- Nao ha alertas ativos
- CPU media em 35%, memoria em 120Mi
O deployment esta saudavel e operando normalmente.

[watch] chatcli> /watch status

K8s Watcher Active
  Deployment:  myapp
  Namespace:   production
  Snapshots:   5
  Pods:        3
  Alerts:      0
```

### Passo 4: Diagnosticar Problemas

Quando algo da errado:

```
[watch] chatcli> Por que o pod myapp-abc12 esta reiniciando?

Analisando os dados do pod myapp-abc12:
- O pod teve 5 restarts na ultima hora
- Motivo do ultimo restart: OOMKilled
- Container estava usando 490Mi de 512Mi de limite
- Logs mostram: "java.lang.OutOfMemoryError: Java heap space"

Diagnostico: O container esta excedendo o limite de memoria.
Recomendacoes:
1. Aumente resources.limits.memory para 1Gi
2. Ajuste a JVM: -Xmx384m para caber no limite
3. Investigue possivel memory leak nos logs anteriores
```

---

## Opcao 2: Servidor com Watcher (Equipe)

Use esta opcao para que toda a equipe tenha acesso ao monitoramento via servidor centralizado.

### Passo 1: Deploy no Kubernetes

**Via Helm (single-target):**

```bash
helm install chatcli deploy/helm/chatcli \
  --namespace monitoring --create-namespace \
  --set llm.provider=CLAUDEAI \
  --set secrets.anthropicApiKey=sk-ant-xxx \
  --set server.token=equipe-token \
  --set watcher.enabled=true \
  --set watcher.deployment=myapp \
  --set watcher.namespace=production \
  --set watcher.interval=15s
```

**Via Operator (AIOps com remediacao autonoma):**

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-prod
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
      - deployment: frontend
        namespace: production
        metricsPort: 3000
      - deployment: backend
        namespace: production
        metricsPort: 9090
        metricsFilter: ["http_*", "db_*"]
      - deployment: worker
        namespace: batch
```

Com o Operator, alem do monitoramento via watcher, voce ganha o **pipeline AIOps completo**: deteccao automatica de anomalias, correlacao em incidentes, analise de causa raiz por IA e remediacao autonoma (scale, restart, rollback). Veja [K8s Operator](/docs/features/k8s-operator/) e [AIOps Platform](/docs/features/aiops-platform/) para documentacao completa.

### Passo 2: Equipe Conecta

```bash
# Cada dev configura
export CHATCLI_REMOTE_ADDR=chatcli.monitoring.svc:50051
export CHATCLI_REMOTE_TOKEN=equipe-token

# Via port-forward (desenvolvimento)
kubectl port-forward -n monitoring svc/chatcli 50051:50051
chatcli connect localhost:50051 --token equipe-token
```

### Passo 3: Contexto Automatico

Qualquer pergunta feita por qualquer dev ja inclui automaticamente o contexto K8s:

```
> O que esta acontecendo com o deployment?

[O servidor injeta automaticamente os dados do K8s Watcher]
```

---

## Fluxo de Trabalho: Incidente em Producao

### 1. Alerta Disparado

Voce recebe um alerta do Grafana/PagerDuty/Slack sobre problemas no deployment.

### 2. Conectar ao ChatCLI

```bash
chatcli connect prod-chatcli:50051 --token ops-token
```

### 3. Obter Visao Geral

```
> Resuma o estado atual do deployment para um post-mortem
```

### 4. Investigar Causa Raiz

```
> Quais eventos de Warning ocorreram nos ultimos 30 minutos?
> Mostre os logs de erro mais recentes
> O que mudou desde o ultimo deploy?
```

### 5. Receber Recomendacoes

```
> Baseado nos dados, qual a causa raiz mais provavel e o que
  devo fazer para resolver?
```

### 6. Validar Resolucao

```
> Apos aplicar o fix, os pods estao voltando ao normal?
> Compare o estado atual com 10 minutos atras
```

---

## Ajuste Fino dos Parametros

### Intervalo de Coleta

| Cenario | Intervalo Recomendado |
|---------|----------------------|
| Producao estavel | `30s` (padrao) |
| Investigacao ativa | `10s` |
| Desenvolvimento | `60s` |
| CI/CD monitoring | `15s` |

```bash
chatcli watch --deployment myapp --interval 10s
```

### Janela de Observacao

| Cenario | Janela Recomendada |
|---------|-------------------|
| Debugging rapido | `30m` |
| Analise normal | `2h` (padrao) |
| Post-mortem | `6h` |
| Analise historica | `24h` |

```bash
chatcli watch --deployment myapp --window 6h
```

### Linhas de Log

| Cenario | Linhas Recomendadas |
|---------|---------------------|
| Apps verbosas | `50` |
| Normal | `100` (padrao) |
| Debugging profundo | `500` |

```bash
chatcli watch --deployment myapp --max-log-lines 500
```

---

## One-Shot para Scripts e Alertas

Integre o ChatCLI com seu sistema de alertas:

```bash
#!/bin/bash
# alert-handler.sh - Chamado quando um alerta dispara

DEPLOYMENT=$1
NAMESPACE=$2

# Gerar analise automatica
ANALYSIS=$(chatcli watch \
  --deployment "$DEPLOYMENT" \
  --namespace "$NAMESPACE" \
  -p "Analise o estado atual do deployment e identifique a causa raiz do problema. Formato: markdown.")

# Enviar para Slack
curl -X POST "$SLACK_WEBHOOK" \
  -H 'Content-type: application/json' \
  -d "{\"text\": \"*ChatCLI K8s Analysis*\n\n$ANALYSIS\"}"
```

Ou via servidor remoto:

```bash
chatcli connect prod-server:50051 --token ops-token \
  -p "O deployment myapp esta com problemas. Analise e sugira solucao." --raw
```

---

## Dicas Avancadas

### Combinar com Contextos Persistentes

```bash
# Salvar documentacao do projeto como contexto
/context create myapp-docs ./docs --mode full --tags "k8s,ops"

# Anexar ao usar com o watcher
/context attach myapp-docs

# Agora a IA tem contexto do K8s + documentacao do projeto
> Com base na documentacao e no estado do cluster, o que pode estar errado?
```

### Multiplos Deployments

Use o modo multi-target para monitorar tudo em uma unica instancia:

```yaml
# targets.yaml
interval: "15s"
window: "2h"
maxContextChars: 32000
targets:
  - deployment: frontend
    namespace: production
    metricsPort: 3000
    metricsFilter: ["next_*", "http_*"]
  - deployment: backend
    namespace: production
    metricsPort: 9090
    metricsFilter: ["http_requests_*", "db_*", "cache_*"]
  - deployment: database
    namespace: production
```

```bash
# Local
chatcli watch --config targets.yaml

# Ou via servidor (toda a equipe tem acesso)
chatcli serve --watch-config targets.yaml
```

A IA recebe contexto detalhado dos targets com problemas e resumos compactos dos saudaveis, respeitando o budget de `maxContextChars`.

### Metricas Prometheus

Quando `metricsPort` esta configurado, o watcher scrapa automaticamente o endpoint `/metrics` dos pods e inclui as metricas na analise. Use `metricsFilter` com **glob patterns** para selecionar apenas metricas relevantes:

```yaml
metricsFilter:
  - "http_requests_total"        # Metrica exata
  - "http_request_duration_*"    # Todas de duracao HTTP
  - "process_*"                  # Metricas de processo
  - "*_errors_total"             # Qualquer contador de erros
```

---

## Opcao 3: AIOps Autonomo (Operator)

Use esta opcao para remediacao automatica de problemas sem intervencao humana.

### Passo 1: Instalar o Operator

```bash
# Instalar CRDs
kubectl apply -f operator/config/crd/bases/

# Instalar RBAC e Manager
kubectl apply -f operator/config/rbac/role.yaml
kubectl apply -f operator/config/manager/manager.yaml
```

### Passo 2: Criar Instance com Watcher

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Instance
metadata:
  name: chatcli-aiops
  namespace: monitoring
spec:
  provider: CLAUDEAI
  apiKeys:
    name: chatcli-api-keys
  server:
    port: 50051
  watcher:
    enabled: true
    interval: "15s"
    targets:
      - deployment: api-gateway
        namespace: production
        metricsPort: 9090
      - deployment: backend
        namespace: production
        metricsPort: 9090
      - deployment: worker
        namespace: batch
```

### Passo 3: Monitorar o Pipeline

```bash
# Verificar anomalias detectadas
kubectl get anomalies -A --watch

# Verificar issues criados
kubectl get issues -A --watch

# Verificar analises da IA
kubectl get aiinsights -A

# Verificar remediacoes
kubectl get remediationplans -A
```

### Passo 4: Fluxo Autonomo em Acao

Quando um pod comeca a crashar:

```
1. WatcherBridge detecta HighRestartCount → cria Anomaly
2. AnomalyReconciler correlaciona → cria Issue (risk: 20, severity: Low)
3. Se OOMKilled tambem → Issue atualizado (risk: 50, severity: Medium)
4. IssueReconciler cria AIInsight
5. AIInsightReconciler chama LLM → retorna: "restart + scale to 4"
6. IssueReconciler cria RemediationPlan com 2 acoes
7. RemediationReconciler executa: restart + scale
8. Issue → Resolved
```

Tudo acontece automaticamente sem intervencao humana.

### Passo 5: (Opcional) Adicionar Runbooks

Para cenarios especificos onde voce quer controlar exatamente o que fazer:

```yaml
apiVersion: platform.chatcli.io/v1alpha1
kind: Runbook
metadata:
  name: oom-standard-procedure
  namespace: production
spec:
  description: "Standard OOMKill recovery for production"
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
      description: "Add replicas for redundancy"
      params:
        replicas: "5"
  maxAttempts: 2
```

Runbooks tem prioridade sobre acoes da IA. Quando nao ha Runbook, a IA decide automaticamente.

---

## Checklist de Implantacao

### Monitoramento (Watch + Servidor)

- [ ] Verificar acesso ao cluster (`kubectl get pods`)
- [ ] Verificar permissoes RBAC para pods, logs, eventos
- [ ] Escolher modo: local (`chatcli watch`) ou servidor (`chatcli serve`)
- [ ] Definir targets: single (`--deployment`) ou multi (`--config targets.yaml`)
- [ ] (Opcional) Configurar `metricsPort` para Prometheus scraping
- [ ] Configurar intervalo e janela adequados ao cenario
- [ ] Ajustar `maxContextChars` se necessario (padrao: 32000)
- [ ] Testar com pergunta simples: "O deployment esta saudavel?"
- [ ] (Opcional) Integrar com alertas para analise automatica
- [ ] (Opcional) Distribuir acesso para a equipe via token

### AIOps Autonomo (Operator)

- [ ] Instalar CRDs: `kubectl apply -f operator/config/crd/bases/`
- [ ] Instalar RBAC e Manager do operator
- [ ] Criar Secret com API keys do provedor LLM
- [ ] Criar Instance CR com `watcher.enabled: true`
- [ ] Verificar anomalias sendo criadas: `kubectl get anomalies -A`
- [ ] Verificar issues sendo correlacionados: `kubectl get issues -A`
- [ ] Verificar IA analisando: `kubectl get aiinsights -A`
- [ ] Verificar remediacoes executando: `kubectl get remediationplans -A`
- [ ] (Opcional) Criar Runbooks para cenarios especificos
- [ ] Monitorar metricas do operator via Prometheus
