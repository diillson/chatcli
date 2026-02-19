+++
title = "Receita: Monitoramento K8s com IA"
linkTitle = "Monitoramento K8s"
weight = 71
description = "Guia passo a passo para monitorar deployments Kubernetes com IA, diagnosticar problemas e automatizar análises usando o K8s Watcher."
icon = "troubleshoot"
+++

Nesta receita, você vai configurar o ChatCLI para monitorar um deployment Kubernetes e usar IA para diagnosticar problemas em tempo real.

---

## Cenário

- Aplicação "myapp" em produção no Kubernetes
- Equipe precisa diagnosticar problemas rapidamente
- Deseja usar IA para analisar logs, eventos e métricas
- Quer contexto K8s automático em todas as perguntas

---

## Opção 1: Monitoramento Local

Use esta opção quando você tem acesso direto ao cluster via `kubectl`.

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

Você verá:

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
[watch] chatcli> O deployment está saudável?

Com base nos dados coletados do Kubernetes:
- O deployment myapp tem 3/3 replicas disponíveis
- Todos os pods estão no estado Running e Ready
- Não há alertas ativos
- CPU média em 35%, memória em 120Mi
O deployment está saudável e operando normalmente.

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
[watch] chatcli> Por que o pod myapp-abc12 está reiniciando?

Analisando os dados do pod myapp-abc12:
- O pod teve 5 restarts na última hora
- Motivo do último restart: OOMKilled
- Container estava usando 490Mi de 512Mi de limite
- Logs mostram: "java.lang.OutOfMemoryError: Java heap space"

Diagnóstico: O container está excedendo o limite de memória.
Recomendações:
1. Aumente resources.limits.memory para 1Gi
2. Ajuste a JVM: -Xmx384m para caber no limite
3. Investigue possível memory leak nos logs anteriores
```

---

## Opção 2: Servidor com Watcher (Equipe)

Use esta opção para que toda a equipe tenha acesso ao monitoramento via servidor centralizado.

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

**Via Operator (AIOps com remediação autônoma):**

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

Com o Operator, além do monitoramento via watcher, você ganha o **pipeline AIOps completo**: detecção automática de anomalias, correlação em incidentes, análise de causa raiz por IA e remediação autônoma (scale, restart, rollback). Veja [K8s Operator](/docs/features/k8s-operator/) e [AIOps Platform](/docs/features/aiops-platform/) para documentação completa.

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
> O que está acontecendo com o deployment?

[O servidor injeta automaticamente os dados do K8s Watcher]
```

---

## Fluxo de Trabalho: Incidente em Produção

### 1. Alerta Disparado

Você recebe um alerta do Grafana/PagerDuty/Slack sobre problemas no deployment.

### 2. Conectar ao ChatCLI

```bash
chatcli connect prod-chatcli:50051 --token ops-token
```

### 3. Obter Visão Geral

```
> Resuma o estado atual do deployment para um post-mortem
```

### 4. Investigar Causa Raiz

```
> Quais eventos de Warning ocorreram nos últimos 30 minutos?
> Mostre os logs de erro mais recentes
> O que mudou desde o último deploy?
```

### 5. Receber Recomendações

```
> Baseado nos dados, qual a causa raiz mais provável e o que
  devo fazer para resolver?
```

### 6. Validar Resolução

```
> Após aplicar o fix, os pods estão voltando ao normal?
> Compare o estado atual com 10 minutos atrás
```

---

## Ajuste Fino dos Parametros

### Intervalo de Coleta

| Cenário | Intervalo Recomendado |
|---------|----------------------|
| Produção estável | `30s` (padrão) |
| Investigação ativa | `10s` |
| Desenvolvimento | `60s` |
| CI/CD monitoring | `15s` |

```bash
chatcli watch --deployment myapp --interval 10s
```

### Janela de Observação

| Cenário | Janela Recomendada |
|---------|-------------------|
| Debugging rápido | `30m` |
| Análise normal | `2h` (padrão) |
| Post-mortem | `6h` |
| Analise histórica | `24h` |

```bash
chatcli watch --deployment myapp --window 6h
```

### Linhas de Log

| Cenário | Linhas Recomendadas |
|---------|---------------------|
| Apps verbosas | `50` |
| Normal | `100` (padrão) |
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

# Gerar análise automática
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
  -p "O deployment myapp está com problemas. Analise e sugira solução." --raw
```

---

## Dicas Avançadas

### Combinar com Contextos Persistentes

```bash
# Salvar documentação do projeto como contexto
/context create myapp-docs ./docs --mode full --tags "k8s,ops"

# Anexar ao usar com o watcher
/context attach myapp-docs

# Agora a IA tem contexto do K8s + documentação do projeto
> Com base na documentação e no estado do cluster, o que pode estar errado?
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

A IA recebe contexto detalhado dos targets com problemas e resumos compactos dos saudáveis, respeitando o budget de `maxContextChars`.

### Métricas Prometheus

Quando `metricsPort` está configurado, o watcher scrapa automaticamente o endpoint `/metrics` dos pods e inclui as métricas na analise. Use `metricsFilter` com **glob patterns** para selecionar apenas métricas relevantes:

```yaml
metricsFilter:
  - "http_requests_total"        # Métrica exata
  - "http_request_duration_*"    # Todas de duração HTTP
  - "process_*"                  # Métricas de processo
  - "*_errors_total"             # Qualquer contador de erros
```

---

## Opção 3: AIOps Autônomo (Operator)

Use esta opção para remediação automática de problemas sem intervenção humana.

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

# Verificar análises da IA
kubectl get aiinsights -A

# Verificar remediações
kubectl get remediationplans -A

# Verificar runbooks (manuais e auto-gerados)
kubectl get runbooks -A

# Verificar post-mortems (gerados após resolução agêntica)
kubectl get postmortems -A
```

### Passo 4: Fluxo Autônomo em Acao

Quando um pod comeca a crashar:

```
1. WatcherBridge detecta HighRestartCount → cria Anomaly
2. AnomalyReconciler correlaciona → cria Issue (risk: 20, severity: Low, signalType: pod_restart)
3. Se OOMKilled também → Issue atualizado (risk: 50, severity: Medium)
4. IssueReconciler cria AIInsight
5. AIInsightReconciler coleta contexto K8s (pods, eventos, revisões)
   → chama LLM com contexto enriquecido → retorna: "restart + scale to 4"
6. IssueReconciler busca Runbook manual (tiered matching)
   → se não encontra, gera Runbook auto da IA (reutilizável)
   → se nenhum Runbook e nenhuma ação IA → modo Agêntico
   → cria RemediationPlan com ações
7. RemediationReconciler executa:
   → Modo normal: executa ações do plano (restart, scale, etc.)
   → Modo agêntico: loop observe-decide-act (IA decide cada passo)
     - Cada reconcile = 1 step (max 10 steps, timeout 10min)
     - IA observa estado K8s → decide ação → executa → próximo step
     - Ações: Scale, Restart, Rollback, PatchConfig, AdjustResources, DeletePod
8. Issue → Resolved (dedup invalidado para detecção de recorrência)
   → Se modo agêntico: PostMortem CR auto-gerado (timeline, root cause, lições)
   → Runbook gerado automaticamente dos steps agênticos bem-sucedidos
   Se falhar → re-análise com contexto de falha → estratégia diferente
```

Tudo acontece automaticamente sem intervenção humana. Runbooks auto-gerados são reutilizados para futuras ocorrências do mesmo tipo. No modo agêntico, a IA atua como agente autônomo com "skills" K8s, e ao resolver o problema gera um PostMortem CR com timeline completa e um Runbook reutilizável para futuras ocorrências.

### Passo 5: (Opcional) Adicionar Runbooks

Para cenários específicos onde você quer controlar exatamente o que fazer:

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

**Prioridade de remediação:** Runbook manual > Runbook auto-gerado > **Remediação agêntica** > Escalação. Quando não há Runbook manual, a IA gera automaticamente um Runbook CR reutilizável. Se nem Runbook nem ações de IA estão disponíveis, o operator entra em **modo agêntico**: a IA atua como agente autônomo num loop observe-decide-act, e ao resolver gera um PostMortem CR e um Runbook reutilizável.

---

## Checklist de Implantacao

### Monitoramento (Watch + Servidor)

- [ ] Verificar acesso ao cluster (`kubectl get pods`)
- [ ] Verificar permissoes RBAC para pods, logs, eventos
- [ ] Escolher modo: local (`chatcli watch`) ou servidor (`chatcli serve`)
- [ ] Definir targets: single (`--deployment`) ou multi (`--config targets.yaml`)
- [ ] (Opcional) Configurar `metricsPort` para Prometheus scraping
- [ ] Configurar intervalo e janela adequados ao cenário
- [ ] Ajustar `maxContextChars` se necessario (padrão: 32000)
- [ ] Testar com pergunta simples: "O deployment está saudável?"
- [ ] (Opcional) Integrar com alertas para análise automática
- [ ] (Opcional) Distribuir acesso para a equipe via token

### AIOps Autônomo (Operator)

- [ ] Instalar CRDs: `kubectl apply -f operator/config/crd/bases/`
- [ ] Instalar RBAC e Manager do operator
- [ ] Criar Secret com API keys do provedor LLM
- [ ] Criar Instance CR com `watcher.enabled: true`
- [ ] Verificar anomalias sendo criadas: `kubectl get anomalies -A`
- [ ] Verificar issues sendo correlacionados: `kubectl get issues -A`
- [ ] Verificar IA analisando: `kubectl get aiinsights -A`
- [ ] Verificar remediações executando: `kubectl get remediationplans -A`
- [ ] Verificar post-mortems gerados: `kubectl get postmortems -A`
- [ ] (Opcional) Criar Runbooks para cenários específicos
- [ ] Monitorar métricas do operator via Prometheus
