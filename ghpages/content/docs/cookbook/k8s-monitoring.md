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

### Passo 1: Deploy no Kubernetes com Helm

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

Rode multiplos watchers em terminais separados:

```bash
# Terminal 1
chatcli watch --deployment frontend --namespace production

# Terminal 2
chatcli watch --deployment backend --namespace production

# Terminal 3
chatcli watch --deployment database --namespace production
```

Ou use o servidor com watcher para o deployment principal e faca perguntas sobre os demais via contexto.

---

## Checklist de Implantacao

- [ ] Verificar acesso ao cluster (`kubectl get pods`)
- [ ] Verificar permissoes RBAC para pods, logs, eventos
- [ ] Escolher modo: local (`chatcli watch`) ou servidor (`chatcli serve --watch-deployment`)
- [ ] Configurar intervalo e janela adequados ao cenario
- [ ] Testar com pergunta simples: "O deployment esta saudavel?"
- [ ] (Opcional) Integrar com alertas para analise automatica
- [ ] (Opcional) Distribuir acesso para a equipe via token
