+++
title = "Orquestração Multi-Agent"
linkTitle = "Multi-Agent"
weight = 42
description = "Sistema multi-agent com 6 agents especialistas que trabalham em paralelo, cada um com skills próprias e scripts aceleradores, orquestrados automaticamente pelo LLM."
icon = "hub"
+++

O **modo Multi-Agent** transforma o `/coder` em um sistema de orquestração onde o LLM despacha **agents especialistas em paralelo** para resolver tarefas complexas de forma mais rápida e eficiente.

## Ativação

O modo multi-agent é **desativado por padrão** — quando desativado, o `/coder` funciona exatamente como antes, sem nenhum impacto.

Para ativar, defina a variável de ambiente:

```bash
CHATCLI_AGENT_PARALLEL_MODE=true
```

---

## Arquitetura

```
User Query
    │
    ▼
AgentMode (ReAct loop existente)
    │
    ▼  (LLM responde com <agent_call> tags)
Dispatcher (fan-out via semaphore)
    │
    ├── FileAgent      ├── CoderAgent     ├── ShellAgent
    ├── GitAgent       ├── SearchAgent    └── PlannerAgent
    │
    ▼
Results Aggregator → Feedback para o LLM orquestrador
```

O LLM orquestrador recebe um **catálogo de agents** no system prompt e aprende a rotear tarefas usando tags `<agent_call>`:

```xml
<agent_call agent="file" task="Read all .go files in pkg/coder/engine/" />
<agent_call agent="coder" task="Add Close method to Engine struct" />
```

Múltiplas tags `<agent_call>` na mesma resposta = **execução paralela**.

---

## Os 6 Agents Especialistas

### FileAgent (Leitura e Análise)
- **Acesso:** Somente leitura (`read`, `tree`, `search`)
- **Skills:**
  - `batch-read` — *Script acelerador:* lê N arquivos em goroutines paralelas sem chamar o LLM
  - `find-pattern` — Busca padrões em arquivos
  - `analyze-structure` — Analisa estrutura de código
  - `map-deps` — Mapeia dependências entre módulos

### CoderAgent (Escrita e Modificação)
- **Acesso:** Leitura/Escrita (`write`, `patch`, `read`, `tree`)
- **Skills:**
  - `write-file` — Criação de novos arquivos
  - `patch-file` — Modificação precisa de código existente
  - `create-module` — Geração de boilerplate
  - `refactor` — Renomeação e refatoração segura

### ShellAgent (Execução e Testes)
- **Acesso:** Execução (`exec`, `test`)
- **Skills:**
  - `run-tests` — *Script acelerador:* executa `go test ./... -json` e parseia resultados
  - `build-check` — *Script acelerador:* executa `go build ./... && go vet ./...`
  - `lint-fix` — Correção automática de lint

### GitAgent (Controle de Versão)
- **Acesso:** Git ops (`git-status`, `git-diff`, `git-log`, `git-changed`, `git-branch`, `exec`)
- **Skills:**
  - `smart-commit` — *Script acelerador:* coleta status + diff para commit inteligente
  - `review-changes` — *Script acelerador:* analisa alterações com changed + diff + log
  - `create-branch` — Criação de branches

### SearchAgent (Busca no Codebase)
- **Acesso:** Somente leitura (`search`, `tree`, `read`)
- **Skills:**
  - `find-usages` — Encontra usos de símbolos
  - `find-definition` — Encontra definições
  - `find-dead-code` — Detecta código morto
  - `map-project` — *Script acelerador:* mapeia projeto em paralelo (tree + interfaces + structs + funcs)

### PlannerAgent (Raciocínio Puro)
- **Acesso:** Nenhum (sem tools — puro raciocínio LLM)
- **Skills:**
  - `analyze-task` — Análise de complexidade e riscos
  - `create-plan` — Criação de plano de execução
  - `decompose` — Decomposição de tarefas complexas

---

## Skills: Scripts vs Descritivas

Cada agent possui dois tipos de skills:

### Skills Executáveis (Scripts Aceleradores)
Sequências pré-definidas de comandos que **bypassam o LLM** para operações mecânicas e repetitivas, executando diretamente no engine:

```
batch-read  → Lê N arquivos em goroutines paralelas (sem LLM call)
run-tests   → go test ./... -json | parse automático
build-check → go build ./... && go vet ./...
smart-commit→ git status + git diff --cached → resumo
map-project → tree + search interfaces/structs em paralelo
```

### Skills Descritivas
Informam o agent sobre suas capacidades — o agent resolve via seu **mini ReAct loop** com chamadas ao LLM:

```
refactor       → Renomeação segura com verificação de referências
find-dead-code → Análise de código não utilizado
create-plan    → Plano estruturado de execução
```

---

## Configuração

| Variável | Padrão | Descrição |
|----------|--------|-----------|
| `CHATCLI_AGENT_PARALLEL_MODE` | `false` | Ativa o modo multi-agent |
| `CHATCLI_AGENT_MAX_WORKERS` | `4` | Máximo de goroutines simultâneas |
| `CHATCLI_AGENT_WORKER_MAX_TURNS` | `10` | Máximo de turnos por worker |
| `CHATCLI_AGENT_WORKER_TIMEOUT` | `5m` | Timeout por worker |

### Exemplo de `.env`

```bash
# Multi-Agent (Orquestração Paralela)
CHATCLI_AGENT_PARALLEL_MODE=true
CHATCLI_AGENT_MAX_WORKERS=4
CHATCLI_AGENT_WORKER_MAX_TURNS=10
CHATCLI_AGENT_WORKER_TIMEOUT=5m
```

---

## Segurança Anti-Race

O sistema implementa múltiplas camadas de proteção contra condições de corrida:

1. **FileLockManager** — Mutex per-filepath (caminhos absolutos normalizados). Operações de escrita adquirem lock; leituras não bloqueiam.
2. **Histórico isolado** — Cada worker mantém seu próprio `[]models.Message`, sem compartilhamento.
3. **LLM clients independentes** — Cada worker cria sua própria instância de LLM client via factory pattern.
4. **Engine stateless** — Cada worker instancia seu próprio `engine.Engine` fresh.
5. **Context tree** — O contexto pai pode cancelar todos os workers via `context.WithCancel`.
6. **Policy "Ask"** — Workers nunca auto-permitem ações sensíveis; escalam para o orquestrador.

---

## Extensibilidade (Agents Customizados)

O sistema de Registry permite que usuários registrem seus próprios agents:

```go
// Registrar um agent customizado
registry.Register(myCustomAgent)

// Substituir um agent builtin
registry.Unregister(workers.AgentTypeFile)
registry.Register(myBetterFileAgent)
```

Qualquer tipo que implemente a interface `WorkerAgent` pode ser registrado:

```go
type WorkerAgent interface {
    Type() AgentType
    Name() string
    Description() string
    SystemPrompt() string
    Skills() *SkillSet
    AllowedCommands() []string
    IsReadOnly() bool
    Execute(ctx context.Context, task string, deps *WorkerDeps) (*AgentResult, error)
}
```

---

## Fluxo de Execução (Exemplo)

```
1. Usuário: "refatore o módulo coder, separe read e write"

2. LLM orquestrador despacha agents paralelos:
   <agent_call agent="file" task="Read all .go files in pkg/coder/engine/" />
   <agent_call agent="search" task="Find references to handleRead and handleWrite" />

3. Dispatcher cria 2 goroutines (dentro do limite maxWorkers):
   - FileAgent e SearchAgent rodam em paralelo
   - Cada um com seu LLM client e mini ReAct loop isolado

4. Resultados agregados → feedback para o orquestrador

5. Orquestrador despacha CoderAgent para a refatoração
   (com FileLock nos arquivos sendo escritos)

6. Após escrita, despacha ShellAgent para rodar testes

7. Orquestrador valida resultado final e reporta ao usuário
```

---

## Compatibilidade

- `CHATCLI_AGENT_PARALLEL_MODE=false` (padrão): **tudo funciona exatamente como antes**
- Tags `<tool_call>` continuam funcionando mesmo com parallel mode ativo
- Nenhuma assinatura de função existente foi alterada
- O package `cli/agent/workers/` é completamente isolado e não impacta funcionalidades existentes
