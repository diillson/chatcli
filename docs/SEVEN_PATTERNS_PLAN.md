# Seven Agent Patterns — Implementation Plan

> Status: **Draft → In Execution**
> Owner: Edilson Freitas
> Started: 2026-04-20
> Branch: `feat/seven-patterns` (a partir de `main`)

## 0. Objetivo

Levar o chatcli de "ReAct + multi-agente" para **suite completa de 7 padrões de agente**, sem regressão e sem código morto. Cada padrão precisa estar:

1. **Real** — não stub, não TODO, não "feature flag desligada por padrão".
2. **Integrado** — no chat mode, coder mode e agent mode quando faz sentido.
3. **Observável** — logs/métricas/traces.
4. **Configurável** — via `/config <section>`, slash command e env var (CHATCLI_*).
5. **Testado** — testes unitários + integração mínima.
6. **Documentado** — README + `docs/AGENT_PATTERNS.md` para usuário final.
7. **i18n** — toda string user-facing em `i18n/locales/*.json`.

## 1. Premissas (não-negociáveis)

| # | Premissa | Implicação prática |
|---|----------|-------------------|
| P1 | Sem regressão | Todo teste existente continua verde (`go test ./...`). Build mantém-se em `go build ./...`. |
| P2 | Sem gaps | Cada padrão tem CLI surface, config, testes e docs. Nada "pela metade". |
| P3 | Sem código morto | Toda função criada é chamada por código de produção (não só por teste). Funções não-usadas são deletadas. |
| P4 | Sem hard-code | Constantes mágicas viram config. Defaults sensatos via `config/defaults.go`. |
| P5 | Sem regressão de latência por padrão | Padrões caros (Refine/Verify/Reflexion) são **opt-in via /config** mas habilitados em modos onde fazem sentido (ex: Coder strict). |
| P6 | i18n obrigatório | Nenhuma string hardcoded em pt/en em handlers. |
| P7 | /config sections | Toda nova env/feature exposta em `/config quality`. |
| P8 | Branch único | `feat/seven-patterns`, sub-features no mesmo branch. |
| P9 | No co-author | Commits sem `Co-Authored-By`. |
| P10 | Branch from main | Partir de `main`, nunca de `develop`. |

## 2. Mapa atual → alvo

| # | Padrão | Estado atual | Estado alvo |
|---|--------|--------------|-------------|
| 1 | **ReAct** | ✅ `worker_react.go` (default 30 turns) | + tracing estruturado, + métricas |
| 2 | **Plan-and-Solve / ReWOO** | 🟡 `PlannerAgent` opcional, output texto | Planner-first mode, structured plan (JSON), `PlanRunner`, ReWOO placeholders |
| 3 | **Reflexion** | 🟡 `context_recovery.go` (só erro de contexto) | Loop completo: outcome → lesson → memory.Fact → retrieval futuro |
| 4 | **RAG + HyDE** | 🟡 RAG keyword (`retriever.go`); HyDE ausente | HyDE-as-keyword-expansion **+** vector embeddings backend (Voyage/OpenAI) com hybrid retrieval |
| 5 | **Self-Refine** | ❌ ausente | `RefinerAgent` + `QualityPipeline` middleware (multi-pass) |
| 6 | **CoVe** | ❌ ausente | `VerifierAgent` + verification questions loop |
| 7 | **Reasoning backbone** | 🟡 Claude `interleaved-thinking-2025-05-14` em `claude_client.go:46` ativado por effort hint apenas | Cross-provider abstraction `WithThinking(budget)`, auto-enable para Planner/Refiner/Verifier/Reflexion, `/thinking` slash, expor em `/config` |

## 3. Arquitetura — visão integrada

```
                       ┌────────────────────────────────────┐
                       │  Chat / Coder / Agent mode entries │
                       └────────────────┬───────────────────┘
                                        │
                       ┌────────────────▼─────────────────┐
       (#3 RAG+HyDE)──▶│   ContextBuilder (system prompt) │
                       │   - memory.Retrieve              │
                       │   - HyDE.AugmentHints            │
                       │   - HyDE.VectorSearch (optional) │
                       └────────────────┬─────────────────┘
                                        │
                       ┌────────────────▼─────────────────┐
                       │   Orchestrator LLM               │
                       │   (parses <agent_call>)          │
                       └────────────────┬─────────────────┘
                                        │
                       ┌────────────────▼─────────────────┐
                       │   Dispatcher.Dispatch            │
                       │     (existing, unchanged API)    │
                       └────────────────┬─────────────────┘
                                        │
                       ┌────────────────▼─────────────────┐
                       │   QualityPipeline (NEW)          │
                       │   PreHook → executeAgent →       │
                       │   PostHook[Refine] →             │
                       │   PostHook[Verify] →             │
                       │   PostHook[Reflexion]            │
                       └────────────────┬─────────────────┘
                                        │
                       ┌────────────────▼─────────────────┐
                       │   Worker.Execute (ReAct loop)    │
                       │   (existing, unchanged)          │
                       │                                  │
                       │   Reasoning backbone (#7)        │
                       │   auto-enabled for select agents │
                       └──────────────────────────────────┘

   PlannerFirst (#2) wraps Dispatcher.Dispatch:
     plan := PlannerAgent.Execute(task)
     plan = parsePlan(plan)             // structured
     for step in plan: Dispatcher.Dispatch([step])
                                  │
                                  └─▶ ReWOO mode: resolve #E1, #E2 placeholders

   Reflexion (#3 in pipeline view):
     - Trigger: PostHook detects error OR Verifier flagged hallucination
     - Action: generate Lesson, persist to memory.Fact (category=lesson)
     - Future runs: lesson auto-retrieved by RAG+HyDE
```

**Princípio chave:** todas as novidades grudam-se ao **dispatcher** ou ao **context builder**. O loop ReAct interno (`worker_react.go`) **não muda** — ele já é sólido.

## 4. Fase 0 — Foundations

### Entregáveis

| Arquivo | Tipo | O que faz |
|---|---|---|
| `cli/agent/quality/config.go` | NOVO | `Config` struct + load from env + defaults |
| `cli/agent/quality/hooks.go` | NOVO | `PreHook`, `PostHook` interfaces |
| `cli/agent/quality/pipeline.go` | NOVO | `Pipeline.Run(ctx, agent, task, deps)` que aplica hooks ao redor de `agent.Execute` |
| `cli/agent/quality/pipeline_test.go` | NOVO | Testes unitários: empty pipeline = no-op; pre falha → execute não roda; post pode reescrever output |
| `cli/agent/workers/dispatcher.go` | MOD | `executeAgent` consulta `d.pipeline` se setado; senão segue como hoje |
| `config/defaults.go` | MOD | Bloco `Quality` com defaults |
| `cli/command_handler.go` | MOD | `/config quality` route |
| `i18n/locales/{en,pt-BR}.json` | MOD | Chaves `quality.*` |

### Decisões de design

1. **Hooks como interface, não closure** — mais testáveis, registráveis externamente (futuro: plugins).

   ```go
   type PreHook interface {
       Name() string
       Run(ctx context.Context, agent workers.WorkerAgent, task *string) error  // pode mutar task
   }
   type PostHook interface {
       Name() string
       Run(ctx context.Context, agent workers.WorkerAgent, task string, result *workers.AgentResult) error
   }
   ```

2. **Pipeline é opt-in por agente** — alguns workers (Formatter, Deps) não se beneficiam de Refine/Verify; pipeline checa `agent.Type()` contra allowlist da Config.

3. **Pipeline não cria import cycle** — `cli/agent/quality` importa `cli/agent/workers`, não o contrário. Dispatcher chama pipeline via interface mínima:

   ```go
   // em workers/types.go (NOVO)
   type ExecutionPipeline interface {
       Run(ctx context.Context, agent WorkerAgent, task string, deps *WorkerDeps) (*AgentResult, error)
   }
   ```

   `Dispatcher.config.Pipeline` (nullable). `nil` → comportamento atual idêntico (P1: sem regressão).

4. **Config.Quality**:
   ```go
   type Quality struct {
       Refine        RefineConfig   // {Enabled bool, MaxPasses int, MinDraftBytes int, ExcludeAgents []string}
       Verify        VerifyConfig   // {Enabled bool, NumQuestions int, RewriteOnDiscrepancy bool}
       Reflexion     ReflexionConfig // {Enabled bool, OnError bool, OnHallucination bool, Persist bool}
       PlanFirst     PlanFirstConfig // {Mode "off"|"auto"|"always", ComplexityThreshold int}
       HyDE          HyDEConfig      // {Enabled bool, UseVectors bool, EmbedProvider string, NumKeywords int}
       Reasoning     ReasoningConfig // {Mode "off"|"on"|"auto", Budget int, AutoAgents []string}
   }
   ```

### Critérios de aceitação Fase 0

- [ ] `go build ./...` passa
- [ ] `go test ./cli/agent/quality/...` passa
- [ ] `go test ./...` passa (regressão zero)
- [ ] `chatcli /config quality` lista campos
- [ ] Pipeline `nil` → comportamento idêntico ao branch `main`

## 5. Fase 1 — ReAct observability + Reasoning backbone (#1, #7)

### Entregáveis

| Arquivo | Tipo | O que faz |
|---|---|---|
| `llm/client/reasoning.go` | NOVO | `WithThinking(ctx, budget)` cross-provider; lê em `claude_client.go` e `openai_client.go` |
| `llm/claudeai/claude_client.go` | MOD | Lê `ThinkingFromContext`, escreve `reqBody["thinking"]` se setado |
| `llm/openai_assistant/...` ou `llm/openai/...` | MOD | Para o3/o4-mini: traduz para `reasoning_effort` |
| `cli/agent/workers/worker_react.go` | MOD | Aplica `WithThinking` se `deps.Config.Reasoning.AutoAgents` contém este agent |
| `cli/agent/workers/agents_planner.go` | MOD | Default reasoning ON |
| `cli/command_handler.go` | MOD | `/thinking on|off|auto|budget=N` |
| `cli/agent/quality/reasoning.go` | NOVO | Helper que decide se ativar thinking baseado em config + agent + task complexity |

### Decisões

1. **Cross-provider abstraction** vive em `llm/client/reasoning.go`:
   ```go
   type ThinkingConfig struct {
       Enabled bool
       Budget  int    // Anthropic: thinking budget tokens; OpenAI: maps to "high"
   }
   func WithThinking(ctx context.Context, cfg ThinkingConfig) context.Context
   func ThinkingFromContext(ctx context.Context) (ThinkingConfig, bool)
   ```

2. **Auto-enable list** (configurável):
   - `planner` (sempre)
   - `refiner` (Fase 5)
   - `verifier` (Fase 6)
   - `reflexion` (Fase 4)

3. **Budget defaults**: Anthropic 8000 tokens (mid-range); auto-scale baseado em `task length`.

4. **Slash `/thinking`**:
   - `/thinking on` → habilita p/ próximas chamadas user
   - `/thinking off` → desabilita
   - `/thinking auto` → política da config
   - `/thinking budget=12000` → ajusta budget

5. **Trace events**: novo `AgentEventType = AgentEventReasoning` emitido quando worker entra em modo thinking. UI pode renderizar diferente.

### Critérios de aceitação Fase 1

- [ ] PlannerAgent envia `reqBody["thinking"]` em chamada Claude
- [ ] Skill effort hints continuam funcionando (compat)
- [ ] `/thinking on` + chat envia thinking no próximo prompt
- [ ] `/config quality.reasoning` mostra estado
- [ ] Teste: `WithThinking + ThinkingFromContext` round-trip

## 6. Fase 2 — Plan-and-Solve / ReWOO (#2)

### Entregáveis

| Arquivo | Tipo | O que faz |
|---|---|---|
| `cli/agent/workers/agents_planner.go` | MOD | Adiciona modo "structured": output JSON com `Steps[]{Agent, Task, Deps, OutputVar}` |
| `cli/agent/quality/plan_runner.go` | NOVO | `PlanRunner.Execute(ctx, plan, dispatcher)` resolve placeholders e dispatcha |
| `cli/agent/quality/plan_parser.go` | NOVO | Parser do output do planner (tolerante a markdown ao redor) |
| `cli/agent/quality/plan_complexity.go` | NOVO | Heurística: quantos verbos, quantos arquivos, etc — retorna score 0..10 |
| `cli/agent_mode.go` | MOD | Antes de dispatch normal, se `Quality.PlanFirst.Mode == "always"` ou (auto && score >= threshold), roda PlanRunner |
| `cli/command_handler.go` | MOD | `/plan` slash → força planejamento da próxima task |

### Plan format (structured)

```json
{
  "task_summary": "Add Oauth login flow with Google",
  "steps": [
    {"id": "E1", "agent": "search", "task": "Find existing auth code in cli/auth"},
    {"id": "E2", "agent": "planner", "task": "Design integration plan based on #E1", "deps": ["E1"]},
    {"id": "E3", "agent": "coder",   "task": "Implement based on #E2", "deps": ["E2"]},
    {"id": "E4", "agent": "tester",  "task": "Write tests for #E3", "deps": ["E3"]}
  ],
  "parallel_groups": [["E1"], ["E2"], ["E3", "E4"]]
}
```

### ReWOO

- Placeholders `#E1` no `task` são substituídos pelo `Output` do step `E1` antes da dispatch.
- Suporta `#E1.summary`, `#E1.head=200` (extrai primeiros N chars).

### Critérios de aceitação Fase 2

- [ ] PlannerAgent: nova flag `StructuredOutput=true` produz JSON válido
- [ ] PlanRunner executa plano sequencial e paralelo (respeitando `parallel_groups`)
- [ ] Placeholders `#E1` resolvidos no momento da dispatch
- [ ] `/plan` slash funciona
- [ ] Teste: planner mock retorna JSON, PlanRunner dispatcha 3 agentes na ordem certa
- [ ] Auto trigger: tarefa com 4+ verbos OU menção a 3+ arquivos → roda planner-first

## 7. Fase 3 — HyDE retrieval (#4)

Dividida em 3a (sem dependências externas) e 3b (vector backend).

### 3a — HyDE como expansão de keywords

| Arquivo | Tipo | O que faz |
|---|---|---|
| `cli/workspace/memory/hyde.go` | NOVO | `HyDEAugmenter.Augment(ctx, query, llm) []string` — gera hipótese, extrai keywords, retorna superset |
| `cli/workspace/memory/retriever.go` | MOD | `RetrieveWithHyDE(ctx, query, llm, augmenter)` |
| `cli/cli.go` | MOD | Quando `Quality.HyDE.Enabled`, retrieval usa HyDE path |

Algoritmo:
1. Receber query/hints originais
2. LLM cheap call (Haiku/gpt-4o-mini): "Write a 3-sentence hypothetical answer to: {query}"
3. Extrair top-N nouns + technical terms da hipótese (regex + stopwords)
4. Augmented hints = original_hints ∪ extracted
5. `facts.Search(augmented_hints)`

### 3b — Vector embeddings

| Arquivo | Tipo | O que faz |
|---|---|---|
| `llm/embedding/embedding.go` | NOVO | `Provider` interface: `Embed(ctx, []string) [][]float32` |
| `llm/embedding/voyage.go` | NOVO | Voyage AI backend (Anthropic-recommended) |
| `llm/embedding/openai.go` | NOVO | OpenAI text-embedding-3-small |
| `llm/embedding/null.go` | NOVO | No-op (default quando não configurado) |
| `llm/embedding/factory.go` | NOVO | Constrói provider baseado em env: `CHATCLI_EMBED_PROVIDER` |
| `cli/workspace/memory/vector_store.go` | NOVO | SQLite-backed vector store; cosine similarity em Go puro |
| `cli/workspace/memory/facts.go` | MOD | Quando provider != null, embeddings de facts persistidos junto com fact |
| `cli/workspace/memory/hyde.go` | MOD | Se vector store ativo, hipótese embed-ada → top-K cosine |
| `cli/workspace/memory/retriever.go` | MOD | Hybrid score: BM25-ish (existente) + cosine |

### Critérios de aceitação Fase 3

- [ ] Sem provider de embedding configurado: tudo continua keyword (regressão zero)
- [ ] Com provider: facts existentes têm embeddings backfilled lazy (no primeiro retrieve)
- [ ] HyDE-3a sempre funciona (não depende de provider)
- [ ] `/config quality.hyde` mostra provider, se vector ativo, num_keywords
- [ ] Teste: hipótese gerada extrai 5+ keywords técnicos
- [ ] Teste: cosine search top-3 retorna esperado

## 8. Fase 4 — Reflexion (#3)

### Entregáveis

| Arquivo | Tipo | O que faz |
|---|---|---|
| `cli/agent/quality/reflexion.go` | NOVO | `ReflexionHook` PostHook que detecta gatilho e gera lesson |
| `cli/agent/quality/lesson.go` | NOVO | `Lesson` struct + `Generate(ctx, llm, task, attempt, outcome) (*Lesson, error)` |
| `cli/workspace/memory/facts.go` | MOD | Categoria `lesson` reconhecida; tag `reflexion` |
| `cli/command_handler.go` | MOD | `/reflect [task]` força reflexão na última task |

### Triggers

| Gatilho | Quando |
|---------|--------|
| Erro de tool | `result.Error != nil` |
| Hallucination flagged | Verifier (Fase 6) marca `result.Metadata["verified_with_discrepancy"]=true` |
| Self-eval baixo | Refiner (Fase 5) deu nota <= 3/5 |
| Manual | `/reflect` |

### Lesson format

```go
type Lesson struct {
    ID         string
    Situation  string  // "Quando preciso editar arquivo Go grande..."
    Mistake    string  // "Tentei reescrever o arquivo todo de uma vez"
    Correction string  // "Use Edit tool com old_string/new_string específicos"
    Evidence   string  // task + outcome resumidos
    Tags       []string // ["go", "edit-file", "large-file"]
    Created    time.Time
}
```

Persiste como `memory.Fact{Category: "lesson", Content: formatLesson(l), Tags: l.Tags}`.

### Critérios de aceitação Fase 4

- [ ] Reflexion roda apenas quando trigger atendido (não em sucesso normal)
- [ ] Lesson persistida em memory.Fact
- [ ] Próxima task que retrieve por tag matching → lesson aparece no system prompt
- [ ] `/reflect` força mesmo sem trigger
- [ ] Teste: erro forçado → lesson criada → fact existe com category=lesson

## 9. Fase 5 — Self-Refine (#5)

### Entregáveis

| Arquivo | Tipo | O que faz |
|---|---|---|
| `cli/agent/workers/agents_refiner.go` | NOVO | `RefinerAgent` registrado |
| `cli/agent/quality/refine_hook.go` | NOVO | `RefineHook` PostHook que invoca RefinerAgent sobre `result.Output` |
| `cli/command_handler.go` | MOD | `/refine [N]` (N = passes) |

### Algoritmo

```
1. Receber (task, draft) do output de qualquer worker
2. Se len(draft) < MinDraftBytes (default 200) → skip (não vale o overhead)
3. Para pass = 1..MaxPasses (default 1):
   a. LLM call: "Critique this draft against the task. List specific issues."
   b. LLM call: "Rewrite the draft addressing the critique."
   c. Se diff(rewrite, draft) < EpsilonChars → break (converged)
   d. draft = rewrite
4. result.Output = draft
5. Adiciona result.Metadata["refined_passes"] = N
```

### Quando pular

- Agentes mecânicos (Formatter, Deps) — output não é prosa
- Saídas que parecem apenas tool output bruto (heurística simples)
- Erros (deixa Reflexion lidar)

### Critérios de aceitação Fase 5

- [ ] RefinerAgent registrado em `SetupDefaultRegistry`
- [ ] `RefineHook` aplicado quando `Quality.Refine.Enabled`
- [ ] Teste: draft com erro óbvio → refined output corrige
- [ ] Teste: agentes em ExcludeAgents nunca chamam refiner
- [ ] Convergência (rewrite ≈ draft) interrompe loop

## 10. Fase 6 — CoVe / Chain-of-Verification (#6)

### Entregáveis

| Arquivo | Tipo | O que faz |
|---|---|---|
| `cli/agent/workers/agents_verifier.go` | NOVO | `VerifierAgent` registrado |
| `cli/agent/quality/verify_hook.go` | NOVO | `VerifyHook` PostHook que invoca VerifierAgent |
| `cli/command_handler.go` | MOD | `/verify` slash |

### Algoritmo (CoVe canônico)

```
1. Receber (task, answer)
2. LLM call: "Generate {N} verification questions about claims in this answer."
3. Para cada question (paralelo):
   a. LLM call (independente, sem ver answer original): "Answer: {question}"
4. LLM call: "Original answer: {answer}. Verification Q&A: {qa_pairs}.
   Identify discrepancies. If any, rewrite the answer addressing them."
5. Se houve rewrite: result.Output = rewrite, Metadata["verified_with_discrepancy"]=true
   Senão: Metadata["verified_clean"]=true
```

### Critérios de aceitação Fase 6

- [ ] VerifierAgent registrado
- [ ] `VerifyHook` aplicado quando `Quality.Verify.Enabled`
- [ ] Teste: answer com fato errado → verifier flagga e reescreve
- [ ] N verification questions configurável (default 3)
- [ ] Discrepância dispara Reflexion (integração c/ Fase 4)

## 11. Fase 7 — Glue final

| Arquivo | Tipo | O que faz |
|---|---|---|
| `cli/config_command.go` (ou onde estiverem sections) | MOD | Adiciona seção `quality` com sub-paths: refine, verify, reflexion, plan-first, hyde, reasoning |
| `i18n/locales/en.json` | MOD | ~30 novas chaves |
| `i18n/locales/pt-BR.json` | MOD | espelha pt-BR |
| `docs/AGENT_PATTERNS.md` | NOVO | User-facing: o que é cada padrão, quando ligar/desligar, exemplos |
| `README.md` / `README_EN.md` | MOD | Seção "Agent Patterns" linkando para o doc |
| `docs/SEVEN_PATTERNS_PLAN.md` | MOD | Marcar checklists ✅ ao concluir cada fase |
| `CHANGELOG.md` | MOD | Entrada `## [Unreleased]` com 7 padrões |

### Tests roll-up

- `go test ./...` deve passar
- Coverage mínima nos novos pacotes: 60%
- Adicionar `cli/agent/quality/integration_test.go` que exerce: planner-first → dispatch → refine → verify → reflexion (com LLM mock)

## 12. Ordem de execução & dependências

```
Fase 0 (Foundations)
   ├─▶ Fase 1 (ReAct obs + Reasoning) — independente
   ├─▶ Fase 2 (Plan-and-Solve)         — independente
   ├─▶ Fase 3 (HyDE)                   — independente
   ├─▶ Fase 5 (Self-Refine)            — usa Pipeline (Fase 0)
   ├─▶ Fase 6 (CoVe)                   — usa Pipeline (Fase 0)
   │      │
   │      └─▶ Fase 4 (Reflexion)       — gatilhada por #6 (e por erros)
   │
   └─▶ Fase 7 (Glue, docs, i18n)       — depende de tudo acima
```

## 13. Critérios de aceitação globais

- [ ] `go build ./...` ✅
- [ ] `go test ./...` ✅ (não diminui número de testes passando vs main)
- [ ] `golangci-lint run` sem novos warnings
- [ ] Cada padrão tem ao menos 1 teste unitário e 1 doc paragraph
- [ ] `/config quality` lista todas as 6 sub-seções
- [ ] CHANGELOG atualizado
- [ ] README com seção Agent Patterns
- [ ] Zero TODO no código novo
- [ ] Zero `_ = unused` ou variáveis não usadas
- [ ] Zero arquivos `*_old.go` ou `*_v2.go`

## 14. Riscos & mitigações

| Risco | Mitigação |
|-------|-----------|
| Latência cumulativa (Refine + Verify + Reflexion + thinking = lento) | Defaults: Refine off, Verify off em chat; Coder mode strict liga ambos. Async Reflexion (não bloqueia resposta). |
| Custo LLM | Cada hook usa cheapest model (Haiku/4o-mini) por default; configurável. |
| Import cycle workers ↔ quality | Pipeline interface vive em `workers/types.go`; quality importa workers. Verificado. |
| Vector store complexity | SQLite + cosine em Go puro (sem CGO, sem deps externas). Backfill lazy. |
| HyDE alucina keywords | LLM cheap → impacto baixo se errar; expansão é union, não replace. |
| Quebra do agent_mode pesado (3832 linhas) | Mudanças mínimas: só wire pipeline + planner-first guard. Sem refactor estrutural. |

## 15. Rollback plan

- Branch único `feat/seven-patterns`. Se algo regressar em testes-de-fumaça pós-merge:
- `git revert <merge-sha>` reverte tudo. Pipeline `nil` → comportamento idêntico ao main.
- Cada `Quality.X.Enabled = false` desliga aquele padrão sem revert.

---

## 16. Tracking — checklist por fase

### Fase 0
- [ ] `cli/agent/quality/config.go`
- [ ] `cli/agent/quality/hooks.go`
- [ ] `cli/agent/quality/pipeline.go`
- [ ] `cli/agent/quality/pipeline_test.go`
- [ ] Wire pipeline em `dispatcher.go`
- [ ] `config/defaults.go` Quality block
- [ ] `/config quality` route
- [ ] i18n keys `quality.*`

### Fase 1
- [ ] `llm/client/reasoning.go`
- [ ] Claude wire `ThinkingFromContext`
- [ ] OpenAI wire reasoning_effort
- [ ] `/thinking` slash
- [ ] Auto-enable list em quality.reasoning
- [ ] Trace event AgentEventReasoning

### Fase 2
- [ ] PlannerAgent structured output
- [ ] `plan_parser.go`
- [ ] `plan_runner.go`
- [ ] `plan_complexity.go`
- [ ] Wire em agent_mode.go (auto/always)
- [ ] `/plan` slash
- [ ] ReWOO placeholder resolution
- [ ] Tests

### Fase 3
- [ ] `memory/hyde.go` (3a)
- [ ] `RetrieveWithHyDE`
- [ ] `llm/embedding/{embedding,voyage,openai,null,factory}.go`
- [ ] `memory/vector_store.go`
- [ ] Hybrid retrieval em retriever.go
- [ ] Lazy embedding backfill
- [ ] Tests

### Fase 4
- [ ] `quality/lesson.go`
- [ ] `quality/reflexion.go` (PostHook)
- [ ] Persist lesson em memory.Fact
- [ ] `/reflect` slash
- [ ] Tests

### Fase 5
- [ ] `agents_refiner.go`
- [ ] `quality/refine_hook.go`
- [ ] Multi-pass loop
- [ ] `/refine` slash
- [ ] Tests

### Fase 6
- [ ] `agents_verifier.go`
- [ ] `quality/verify_hook.go`
- [ ] Q&A loop
- [ ] `/verify` slash
- [ ] Integração com Reflexion
- [ ] Tests

### Fase 7
- [ ] `/config quality` UI completa
- [ ] i18n pt-BR + en
- [ ] `docs/AGENT_PATTERNS.md`
- [ ] README sections
- [ ] CHANGELOG
- [ ] Integration test end-to-end

---

> Última atualização: 2026-04-20 — todas as 7 fases entregues. Build verde, regressão zero.

## 17. Conclusão

✅ **Fase 0** — `cli/agent/quality/` (config, hooks, pipeline, builder); `workers.ExecutionPipeline`; dispatcher.SetPipeline; `/config quality`; i18n.
✅ **Fase 1** — `applyAutoReasoning` em pipeline; cross-provider via `SkillEffort`; `/thinking on|off|auto|budget=N`; auto-enable para Planner/Refiner/Verifier/Reflexion.
✅ **Fase 2** — Plan-and-Solve / ReWOO: `PlannerAgent` em modo JSON estruturado, `PlanRunner`, `ParsePlan` com `#E1.head=N` placeholders, `ComplexityScore` (en + pt-BR); `/plan`; auto trigger.
✅ **Fase 3** — RAG + HyDE: `HyDEAugmenter` (3a keyword expansion), `llm/embedding/{voyage,openai,null,factory}`, `VectorIndex` (3b cosine, pure Go, JSON persistido). Lazy backfill.
✅ **Fase 4** — Reflexion: `Lesson`, `GenerateLesson`, `ReflexionHook` com gatilhos por erro/hallucination/low_quality/manual; `/reflect`; persiste em `memory.Fact` category=lesson.
✅ **Fase 5** — Self-Refine: `RefinerAgent`, `RefineHook` multi-pass com convergência; `/refine on|off|auto`.
✅ **Fase 6** — CoVe: `VerifierAgent`, `VerifyHook` com discrepancy → metadata → Reflexion; `/verify on|off|auto`.
✅ **Fase 7** — Glue: `/config quality` lista todas as 6 sub-seções incluindo provider/contagem de vetores; i18n en + en-US + pt-BR; CHANGELOG; `docs/AGENT_PATTERNS.md` (user-facing).

**Métricas finais:**
- Arquivos novos: 21
- Pacotes novos: `cli/agent/quality/`, `llm/embedding/`
- Slashes novos: `/thinking`, `/plan`, `/refine`, `/verify`, `/reflect`
- Testes novos: ~70 (quality, embedding, hyde, vector_store, plan, plan_runner, refine_hook, verify_hook, reflexion, thinking_command)
- Regressão: zero (`go test ./...` continua verde)
- Build: limpo (`go build ./...`)
