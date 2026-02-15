+++
title = "Monitoramento Kubernetes (K8s Watcher)"
linkTitle = "K8s Watcher"
weight = 62
description = "Monitore deployments Kubernetes em tempo real e use IA para diagnosticar problemas, analisar logs e entender o status do cluster."
icon = "monitoring"
+++

O **K8s Watcher** permite que o ChatCLI monitore continuamente um deployment Kubernetes, coletando metricas, logs, eventos e status de pods. Com essas informacoes, voce pode fazer perguntas em linguagem natural e receber analises contextualizadas pela IA.

---

## Como Funciona

O K8s Watcher coleta dados periodicamente e os armazena em um buffer circular com janela temporal. Quando voce faz uma pergunta, o contexto Kubernetes e automaticamente injetado no prompt, permitindo que a IA analise a situacao real do cluster.

```
                  +-----------------+
                  |  Kubernetes API |
                  +--------+--------+
                           |
              Coleta periodica (30s)
                           |
                  +--------v--------+
                  |  Data Collectors |
                  |  - Pod Status    |
                  |  - Events        |
                  |  - Logs          |
                  |  - Metrics       |
                  |  - HPA           |
                  |  - Deployment    |
                  +--------+--------+
                           |
                  +--------v--------+
                  | Observability   |
                  | Store           |
                  | (ring buffer)   |
                  +--------+--------+
                           |
                  +--------v--------+
                  |   Summarizer    |
                  | (gera contexto) |
                  +--------+--------+
                           |
              Injeta no prompt do LLM
                           |
                  +--------v--------+
                  |  LLM Provider   |
                  |  (resposta IA)  |
                  +-----------------+
```

---

## Duas Formas de Usar

### 1. Modo Watch Local (`chatcli watch`)

Roda diretamente na sua maquina, conectando ao cluster via kubeconfig:

```bash
# Monitorar deployment "nginx" no namespace "default"
chatcli watch --deployment nginx

# Namespace especifico
chatcli watch --deployment myapp --namespace production

# Intervalo de coleta mais rapido
chatcli watch --deployment myapp --namespace prod --interval 10s

# Kubeconfig customizado
chatcli watch --deployment myapp --kubeconfig ~/.kube/prod-config
```

### 2. Modo Servidor com Watcher (`chatcli serve`)

Roda o watcher integrado ao servidor gRPC. Todos os clientes remotos recebem contexto K8s automaticamente:

```bash
chatcli serve --watch-deployment myapp --watch-namespace production
```

Clientes conectados via `chatcli connect` recebem o contexto sem configuracao adicional.

---

## Flags Completas

### `chatcli watch`

| Flag | Descricao | Padrao | Env Var |
|------|-----------|--------|---------|
| `--deployment` | Deployment a monitorar (obrigatorio) | | `CHATCLI_WATCH_DEPLOYMENT` |
| `--namespace` | Namespace do deployment | `default` | `CHATCLI_WATCH_NAMESPACE` |
| `--interval` | Intervalo entre coletas | `30s` | `CHATCLI_WATCH_INTERVAL` |
| `--window` | Janela temporal de dados mantidos | `2h` | `CHATCLI_WATCH_WINDOW` |
| `--max-log-lines` | Linhas de log por pod | `100` | `CHATCLI_WATCH_MAX_LOG_LINES` |
| `--kubeconfig` | Caminho do kubeconfig | Auto-detectado | `CHATCLI_KUBECONFIG` |
| `--provider` | Provedor de LLM | `.env` | `LLM_PROVIDER` |
| `--model` | Modelo de LLM | `.env` | |
| `-p <prompt>` | One-shot: envia prompt com contexto K8s e sai | | |

---

## O que e Coletado

O K8s Watcher coleta informacoes de multiplas fontes em cada ciclo:

### Pod Status Collector

- Nome, status e fase de cada pod
- Contagem de restarts
- Condicoes (Ready, ContainersReady, etc.)
- Container status (Running, Waiting, Terminated)
- Motivo de termino (OOMKilled, Error, Completed)

### Event Collector

- Eventos do Kubernetes (Warning e Normal)
- Mensagem, razao, componente fonte
- Timestamp e contagem de ocorrencias

### Log Collector

- Ultimas N linhas de log de cada container em cada pod
- Filtrado para o deployment monitorado

### Resource Usage Collector

- CPU e memoria (quando metrics-server esta disponivel)
- Utilizacao por pod

### Deployment Collector

- Replicas desejadas vs disponiveis
- Rollout status
- Condicoes do deployment (Available, Progressing)

### HPA Collector

- Configuracao do Horizontal Pod Autoscaler (se existir)
- Metricas atuais vs targets
- Min/Max replicas

### Ingress Collector

- Regras de ingress associadas
- Endpoints de servico

---

## Deteccao de Anomalias

O watcher detecta automaticamente padroes problematicos e gera alertas:

| Anomalia | Condicao | Severidade |
|----------|----------|------------|
| **CrashLoopBackOff** | Pod com mais de 5 restarts | Critical |
| **OOMKilled** | Container terminado por falta de memoria | Critical |
| **PodNotReady** | Pod nao esta no estado Ready | Warning |
| **DeploymentFailing** | Deployment com Available=False | Critical |

Os alertas sao incluidos no contexto enviado ao LLM, permitindo que a IA priorize problemas criticos.

---

## Contexto Injetado no LLM

Quando voce faz uma pergunta, o Summarizer gera um bloco de contexto que e prepended ao prompt. Exemplo:

```
[K8s Context: deployment/myapp in namespace/production]
Deployment: myapp (production)
  Replicas: 3/3 available
  Conditions: Available=True, Progressing=True

Pods:
  myapp-7d9b4c5f6-abc12: Running (Ready, 0 restarts)
  myapp-7d9b4c5f6-def34: Running (Ready, 0 restarts)
  myapp-7d9b4c5f6-ghi56: Running (Ready, 2 restarts last 1h)

Recent Events (last 30m):
  [Warning] BackOff: Back-off restarting failed container (pod: ghi56)
  [Normal]  Pulled: Successfully pulled image "myapp:v2.1.0"

Alerts:
  [WARNING] Pod myapp-7d9b4c5f6-ghi56: 2 restarts detected

Recent Logs (myapp-7d9b4c5f6-ghi56):
  2024-02-14 10:23:45 ERROR database connection timeout
  2024-02-14 10:23:46 FATAL shutting down due to unrecoverable error

User Question: Por que o pod ghi56 esta reiniciando?
```

---

## Observability Store

Os dados coletados sao armazenados em um ring buffer com janela temporal configuravel:

- **Snapshots**: Fotos periodicas do estado completo (padrao: ultimas 2 horas)
- **Logs**: Logs recentes de cada pod
- **Alertas**: Anomalias detectadas com severidade e timestamps

### Rotacao Automatica

Dados mais antigos que a janela temporal (`--window`) sao automaticamente descartados, mantendo o uso de memoria constante.

---

## Comando `/watch status`

Dentro do ChatCLI interativo (local ou remoto), use `/watch status` para ver o estado atual do watcher:

```
/watch status
```

### Saida Local

```
K8s Watcher Active
  Deployment:  myapp
  Namespace:   production
  Snapshots:   42
  Pods:        3
  Alerts:      1
```

### Saida Remota (via `chatcli connect`)

```
K8s Watcher Status (Remote Server)
  Deployment:  myapp
  Namespace:   production
  Snapshots:   42
  Alerts:      2
  Pods:        3

Status Summary:
  3/3 pods running, 2 restarts last 1h
```

---

## One-Shot com Contexto K8s

Envie um unico prompt com contexto Kubernetes e receba a resposta:

```bash
# Modo watch local
chatcli watch --deployment myapp -p "O deployment esta saudavel?"

# Via servidor remoto
chatcli connect meuservidor:50051 -p "Por que os pods estao reiniciando?"
```

---

## Exemplos de Perguntas

Aqui estao exemplos de perguntas que voce pode fazer com o K8s Watcher ativo:

```
> O deployment esta saudavel?
> Por que o pod xyz esta reiniciando?
> Explique os erros nos logs
> Preciso escalar o deployment? O que as metricas indicam?
> Quais eventos de warning ocorreram na ultima hora?
> Resuma o status atual do cluster para um report
> O que pode estar causando o OOMKill no pod abc?
> Compare o estado atual com o de 30 minutos atras
```

---

## Requisitos

- **Kubernetes Cluster**: Acesso via kubeconfig ou in-cluster config
- **Permissoes RBAC**: O watcher precisa de permissoes para ler pods, eventos, logs, deployments, HPA e ingresses no namespace monitorado
- **metrics-server** (opcional): Para coleta de CPU/memoria

### RBAC Minimo

Se estiver rodando dentro do cluster (via Helm), o chart ja cria as permissoes necessarias automaticamente. **Por padrao, o chart usa Role (namespace-scoped)** em vez de ClusterRole, seguindo o principio de menor privilegio. Para monitorar multiplos namespaces, habilite `rbac.clusterWide: true` no Helm values.

Para uso local:

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
    resources: ["deployments", "replicasets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["autoscaling"]
    resources: ["horizontalpodautoscalers"]
    verbs: ["get", "list"]
  - apiGroups: ["networking.k8s.io"]
    resources: ["ingresses"]
    verbs: ["get", "list"]
  - apiGroups: ["metrics.k8s.io"]
    resources: ["pods"]
    verbs: ["get", "list"]
```

---

## Proximo Passo

- [Configurar o servidor com watcher](/docs/features/server-mode/)
- [Deploy no Kubernetes com Helm](/docs/getting-started/docker-deployment/)
- [Receita: Monitoramento K8s na pratica](/docs/cookbook/k8s-monitoring/)
