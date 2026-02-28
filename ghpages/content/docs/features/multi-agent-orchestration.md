+++
title = "Orquestração Multi-Agent"
linkTitle = "Multi-Agent"
weight = 42
description = "Sistema multi-agent com agents especialistas embarcados + agents customizados que trabalham em paralelo, cada um com skills próprias e scripts aceleradores, orquestrados automaticamente pelo LLM."
icon = "hub"
+++

O **modo Multi-Agent** transforma o `/coder` e o `/agent` em um sistema de orquestração onde o LLM despacha **agents especialistas em paralelo** para resolver tarefas complexas de forma mais rápida e eficiente.

## Ativação

O modo multi-agent é **ativado por padrão**. Para desativá-lo, defina:

```bash
CHATCLI_AGENT_PARALLEL_MODE=false
```

Quando desativado, o `/coder` e o `/agent` funcionam exatamente como antes — sem nenhum impacto.

---

## Arquitetura

```
User Query
    │
    ▼
AgentMode (ReAct loop existente)
    │
    ▼  (LLM responde com <agent_call> ou <tool_call> tags)
Dispatcher (fan-out via semaphore)
    │
    ├── FileAgent       ├── CoderAgent      ├── ShellAgent
    ├── GitAgent        ├── SearchAgent     ├── PlannerAgent
    ├── ReviewerAgent   ├── TesterAgent     ├── RefactorAgent
    ├── DiagnosticsAgent├── FormatterAgent  ├── DepsAgent
    └── CustomAgent(s)  (devops, security-auditor, etc.)
    │
    ▼
Results Aggregator → Feedback para o LLM orquestrador
```

O LLM orquestrador recebe um **catálogo de agents** no system prompt e aprende a rotear tarefas usando tags `<agent_call>`:

```xml
<agent_call agent="file" task="Read all .go files in pkg/coder/engine/" />
<agent_call agent="coder" task="Add Close method to Engine struct" />
<agent_call agent="devops" task="Configure CI/CD pipeline with GitHub Actions" />
```

Múltiplas tags `<agent_call>` na mesma resposta = **execução paralela**.

---

## Dois Modos de Execução

O orquestrador possui dois mecanismos de execução, escolhendo o mais adequado por contexto:

| Modo | Sintaxe | Quando Usar |
|------|---------|-------------|
| **agent_call** | `<agent_call agent="..." task="..." />` | Novas fases de trabalho, tarefas paralelas, leitura exploratória, refatoração multi-arquivo |
| **tool_call** | `<tool_call name="@coder" args="..." />` | Fixes rápidos, diagnóstico de erros, patches pontuais, validação pós-agent |

### Guia de Decisão

| Situação | Modo |
|----------|------|
| Ler múltiplos arquivos + buscar referências | `agent_call` (file + search em paralelo) |
| Corrigir um erro de compilação | `tool_call` (patch direto) |
| Escrever novo módulo + testes | `agent_call` (coder + shell) |
| Verificar resultado de um agent | `tool_call` (read/exec rápido) |
| Fix após falha de agent | `tool_call` (diagnóstico preciso) |
| Retomar após fix aplicado | `agent_call` (próxima fase) |

---

## Agents Especialistas Embarcados

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

### ReviewerAgent (Revisão de Código e Qualidade)
- **Acesso:** Somente leitura (`read`, `search`, `tree`)
- **Skills:**
  - `review-file` — Analisa arquivo para bugs, code smells, violações SOLID e issues de segurança
  - `diff-review` — *Script acelerador:* revisa alterações staged via git-diff e git-changed
  - `scan-lint` — *Script acelerador:* executa `go vet` e `staticcheck` e categoriza issues

### TesterAgent (Testes e Cobertura)
- **Acesso:** Leitura/Escrita/Execução (`read`, `write`, `patch`, `exec`, `test`, `search`, `tree`)
- **Skills:**
  - `generate-tests` — Geração de testes abrangentes para funções e pacotes (LLM-driven)
  - `run-coverage` — *Script acelerador:* executa `go test -coverprofile` e parseia cobertura por função
  - `find-untested` — *Script acelerador:* encontra funções exportadas sem testes correspondentes
  - `generate-table-test` — Geração de table-driven tests idiomáticos em Go

### RefactorAgent (Transformações Estruturais)
- **Acesso:** Leitura/Escrita (`read`, `write`, `patch`, `search`, `tree`)
- **Skills:**
  - `rename-symbol` — *Script acelerador:* renomeia símbolo em todos os `.go`, ignorando strings e comentários
  - `extract-interface` — Extrai interface a partir dos métodos de um tipo concreto
  - `move-function` — Move função entre pacotes ajustando imports
  - `inline-variable` — Substitui variável pelo seu valor em todos os pontos de uso

### DiagnosticsAgent (Troubleshooting e Investigação)
- **Acesso:** Leitura/Execução (`read`, `search`, `tree`, `exec`)
- **Skills:**
  - `analyze-error` — Parseia mensagens de erro e stack traces mapeando para localizações no código
  - `check-deps` — *Script acelerador:* executa `go mod tidy`, `go mod verify` e verifica saúde das dependências
  - `bisect-bug` — Guia investigação para encontrar o commit que introduziu um bug
  - `profile-bottleneck` — Executa benchmarks ou pprof e analisa hotspots de performance

### FormatterAgent (Formatação e Estilo)
- **Acesso:** Escrita/Execução (`read`, `patch`, `exec`, `tree`)
- **Skills:**
  - `format-code` — *Script acelerador:* executa `gofmt -w` (ou `goimports -w`) nos arquivos Go
  - `fix-imports` — *Script acelerador:* executa `goimports` para organizar imports
  - `normalize-style` — Aplica convenções de naming e estilo consistentes (LLM-driven)

### DepsAgent (Gerenciamento de Dependências)
- **Acesso:** Leitura/Execução (`read`, `exec`, `search`, `tree`)
- **Skills:**
  - `audit-deps` — *Script acelerador:* executa `go mod verify` e `govulncheck` para auditoria
  - `update-deps` — *Script acelerador:* lista dependências desatualizadas com atualizações disponíveis (dry-run)
  - `why-dep` — *Script acelerador:* explica por que uma dependência existe via `go mod why` e `go mod graph`
  - `find-outdated` — Encontra todas as dependências com versões mais novas disponíveis

---

## Agents Customizados como Workers

Agents personas definidos em `~/.chatcli/agents/` são **automaticamente carregados** como workers no sistema de orquestração ao iniciar o `/coder` ou `/agent`. O LLM pode despachá-los via `<agent_call>` com o **mesmo ReAct loop**, leitura paralela e recuperação de erros dos agents embarcados.

### Como Funciona

1. Ao iniciar o modo multi-agent, o sistema escaneia `~/.chatcli/agents/`
2. Para cada agent encontrado, cria um `CustomAgent` que implementa a interface `WorkerAgent`
3. O campo `tools` do frontmatter YAML define quais comandos o agent pode usar
4. Skills associadas são carregadas e incluídas no system prompt do worker
5. O agent aparece no catálogo do orquestrador e pode ser despachado

### Mapeamento de Tools

O campo `tools` do YAML frontmatter mapeia ferramentas estilo Claude Code para subcomandos do @coder:

| Tool no YAML | Comando(s) @coder | Descrição |
|--------------|-------------------|-----------|
| `Read` | `read` | Ler conteúdo de arquivos |
| `Grep` | `search` | Buscar padrões em arquivos |
| `Glob` | `tree` | Listar diretórios |
| `Bash` | `exec`, `test`, `git-status`, `git-diff`, `git-log`, `git-changed`, `git-branch` | Execução e operações git |
| `Write` | `write` | Criar/sobrescrever arquivos |
| `Edit` | `patch` | Edição precisa (search/replace) |

### Exemplo de Agent Customizado

```yaml
---
name: "security-auditor"
description: "Especialista em segurança com foco em OWASP Top 10"
tools: Read, Grep, Glob
skills:
  - owasp-rules
  - compliance
---
# Personalidade Base

Você é um Security Auditor especialista. Analise código buscando
vulnerabilidades OWASP Top 10, injection, XSS, e más práticas.
```

Este agent será **somente leitura** (apenas Read/Grep/Glob) e o LLM poderá despachá-lo assim:

```xml
<agent_call agent="security-auditor" task="Audit the authentication module for OWASP vulnerabilities" />
```

### Regras de Proteção

- **Names reservados**: Os 12 nomes de agents embarcados (file, coder, shell, git, search, planner, reviewer, tester, refactor, diagnostics, formatter, deps) são protegidos e não podem ser sobrescritos por agents customizados
- **Sem tools = read-only**: Agents sem campo `tools` recebem automaticamente `read`, `search`, `tree` e são marcados como read-only
- **Duplicatas ignoradas**: Se dois agents tiverem o mesmo nome, apenas o primeiro é registrado

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

### Skills V2 (Pacotes)

Skills V2 são diretórios contendo:
- `SKILL.md` — Conteúdo principal com frontmatter
- Subskills (`.md`) — Documentos de conhecimento adicional
- `scripts/` — Scripts executáveis registrados automaticamente no worker

```
skills/
└── clean-code/
    ├── SKILL.md            # Conteúdo principal
    ├── naming-rules.md     # Subskill: regras de nomenclatura
    ├── formatting.md       # Subskill: regras de formatação
    └── scripts/
        └── lint_check.py   # Script executável (registrado como skill)
```

O worker pode ler subskills com o comando `read` e executar scripts com `exec` durante sua operação autônoma.

---

## Estratégia de Recuperação de Erros

Quando um `agent_call` **falha**, o orquestrador segue um protocolo de recuperação inteligente:

1. **Diagnóstico via tool_call**: Usa `tool_call` direto para ler arquivos relevantes e entender o erro (já tem o contexto)
2. **Fix via tool_call**: Patches, correções de arquivo e retentativas são mais rápidos e seguros via `tool_call`
3. **Retoma via agent_call**: Após fix aplicado e verificado, retoma usando `agent_call` para a próxima fase

**Regra chave**: Recuperação de erros = `tool_call` (rápido, preciso). Novas fases de trabalho = `agent_call` (paralelo, escalável).

```
agent_call → FALHA
    │
    ▼
tool_call: read (diagnosticar o erro)
    │
    ▼
tool_call: patch (aplicar fix)
    │
    ▼
tool_call: exec (verificar fix)
    │
    ▼
agent_call → PRÓXIMA FASE (sucesso)
```

---

## Configuração

| Variável | Padrão | Descrição |
|----------|--------|-----------|
| `CHATCLI_AGENT_PARALLEL_MODE` | `true` | Ativa/desativa o modo multi-agent |
| `CHATCLI_AGENT_MAX_WORKERS` | `4` | Máximo de goroutines simultâneas |
| `CHATCLI_AGENT_WORKER_MAX_TURNS` | `10` | Máximo de turnos por worker |
| `CHATCLI_AGENT_WORKER_TIMEOUT` | `5m` | Timeout por worker |

### Exemplo de `.env`

```bash
# Multi-Agent (Orquestração Paralela)
CHATCLI_AGENT_PARALLEL_MODE=true    # Desative com false se necessário
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

## Extensibilidade Programática

Além dos agents customizados via persona (carregados automaticamente), o sistema de Registry permite extensão programática:

```go
// Registrar um agent customizado via código
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

7. Se testes falharem → tool_call para diagnóstico e fix rápido

8. Orquestrador valida resultado final e reporta ao usuário
```

---

## Compatibilidade

- `CHATCLI_AGENT_PARALLEL_MODE=false`: **tudo funciona exatamente como antes**
- Tags `<tool_call>` continuam funcionando mesmo com parallel mode ativo
- Nenhuma assinatura de função existente foi alterada
- O package `cli/agent/workers/` é completamente isolado e não impacta funcionalidades existentes
