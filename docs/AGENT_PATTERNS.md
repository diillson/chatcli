# Agent Patterns

ChatCLI implements **seven LLM-agent patterns** end-to-end. Most run automatically; all are configurable.

| # | Pattern | Slash command | Config section | Default |
|---|---------|--------------|----------------|---------|
| 1 | **ReAct** | (always on, in `/agent` and `/coder`) | `/config agent` | on |
| 2 | **Plan-and-Solve / ReWOO** | `/plan` | `/config quality` → plan-first | auto |
| 3 | **Reflexion** | `/reflect <lesson>` | `/config quality` → reflexion | on (error trigger) |
| 4 | **RAG + HyDE** | (transparent in chat / agent) | `/config quality` → hyde | off (opt-in) |
| 5 | **Self-Refine** | `/refine on\|off\|auto` | `/config quality` → refine | off (opt-in) |
| 6 | **Chain-of-Verification (CoVe)** | `/verify on\|off\|auto` | `/config quality` → verify | off (opt-in) |
| 7 | **Reasoning backbone** | `/thinking on\|off\|auto\|budget=N` | `/config quality` → reasoning | auto for Planner/Refiner/Verifier/Reflexion |

---

## How they fit together

```
                 ┌────────────────────────────────────┐
                 │   /agent or /coder user task       │
                 └────────────────┬───────────────────┘
                                  │
       (#4 RAG+HyDE) ─────────────▼───────────────
       memory.Retriever expands hints with the
       hypothetical answer (HyDE) and optionally
       searches the vector store before assembling
       the system prompt.
                                  │
       (#2 Plan-and-Solve) ───────▼───────────────
       quality.runPlanFirst calls PlannerAgent
       with the structured-JSON directive, parses
       the plan, dispatches each step (resolving
       #E1, #E2 placeholders), and injects the
       deterministic report into history.
                                  │
                  ┌───────────────▼───────────────┐
                  │   ReAct loop (workers)        │
                  │   (#1, always on)             │
                  └───────────────┬───────────────┘
                                  │
                  ┌───────────────▼───────────────┐
                  │   QualityPipeline (per call)  │
                  │   - Pre: applyAutoReasoning   │ (#7)
                  │   - Execute worker            │
                  │   - Post: RefineHook          │ (#5)
                  │   - Post: VerifyHook          │ (#6)
                  │   - Post: ReflexionHook       │ (#3)
                  └───────────────┬───────────────┘
                                  │
       Lessons from Reflexion are persisted into
       memory.Fact and surface again via #4 on
       similar future tasks — closing the loop.
```

---

## #1 — ReAct

The base loop. Lives in `cli/agent/workers/worker_react.go`. Every agent runs Reason → Act → Observe up to `CHATCLI_AGENT_WORKER_MAX_TURNS` (default 30). No user-facing toggle: ReAct is the runtime.

## #2 — Plan-and-Solve / ReWOO

Synthesizes a structured plan before the orchestrator dispatches. The planner emits JSON like:

```json
{
  "task_summary": "Add OAuth login",
  "steps": [
    {"id": "E1", "agent": "search", "task": "Find existing auth code"},
    {"id": "E2", "agent": "coder",  "task": "Implement based on #E1", "deps": ["E1"]},
    {"id": "E3", "agent": "tester", "task": "Write tests for #E2",    "deps": ["E2"]}
  ]
}
```

`#E1` placeholders are resolved at dispatch time. Modifiers: `#E1.summary`, `#E1.head=200`, `#E1.last=200`.

**Triggers**:
- `/plan <task>` — force on this turn
- `CHATCLI_QUALITY_PLAN_FIRST_MODE=always` — every turn
- `CHATCLI_QUALITY_PLAN_FIRST_MODE=auto` (default) — when complexity ≥ threshold (default 6)

Complexity heuristic: action verbs + file mentions + sequencer tokens (en + pt-BR vocabularies).

## #3 — Reflexion

After a failure, hallucination, or low-quality result, a small LLM call distills a structured lesson:

```
LESSON: <when this applies>
MISTAKE: <what went wrong>
CORRECTION: <what to do next time>
TRIGGER: error|hallucination|low_quality|manual
```

Persisted to `memory.Fact` under category `lesson`. Future RAG+HyDE retrievals surface it automatically.

**Triggers** (combine with `CHATCLI_QUALITY_REFLEXION_*`):
- `OnError` — agent returned `Error != nil` (default on)
- `OnHallucination` — VerifyHook flagged a discrepancy (default on)
- `OnLowQuality` — Refiner gave a low score (default off)
- Manual: `/reflect <free-text lesson>`

Reflexion runs in a background goroutine — never blocks the user-facing turn.

## #4 — RAG + HyDE

Long-term memory retrieval with two enhancements:

- **3a — Hypothesis-as-keyword-expansion**: a cheap LLM call generates a 2-4 sentence "hypothetical answer" to the user query; keywords extracted from it widen the retrieval net.
- **3b — Vector embeddings**: optional. When `CHATCLI_EMBED_PROVIDER=voyage` or `=openai` is set, the user query is embedded and cosine-matched against fact vectors. Lazy backfill: facts get embedded as they surface.

Vectors persisted as JSON (`~/.chatcli/memory/vector_index.json`) — pure-Go cosine, no CGO, no external deps.

**Enable**:
```bash
export CHATCLI_QUALITY_HYDE_ENABLED=true
export CHATCLI_QUALITY_HYDE_USE_VECTORS=true        # optional
export CHATCLI_EMBED_PROVIDER=voyage                # voyage|openai
export VOYAGE_API_KEY=...                            # provider-specific
```

## #5 — Self-Refine

A `RefinerAgent` critiques the just-finished worker's draft and produces a revised version. Multi-pass with convergence (stops when the rewrite differs from the draft by fewer than `EpsilonChars`).

**Skipped for**: agents in `ExcludeAgents` (default: formatter, deps, refiner, verifier — last two prevent infinite recursion).

```bash
/refine on        # session toggle
/refine off
/refine auto      # defer to /config quality
```

Or via env: `CHATCLI_QUALITY_REFINE_ENABLED=true CHATCLI_QUALITY_REFINE_MAX_PASSES=2`.

## #6 — Chain-of-Verification (CoVe)

A `VerifierAgent` generates N independent verification questions about claims in the draft, answers each, and either confirms the draft or rewrites it to address discrepancies. Discrepancy state is recorded on `AgentResult.Metadata` so Reflexion picks it up.

```bash
/verify on
/verify off
/verify auto
```

Env: `CHATCLI_QUALITY_VERIFY_ENABLED=true CHATCLI_QUALITY_VERIFY_NUM_QUESTIONS=3`.

## #7 — Reasoning backbone

Cross-provider thinking abstraction wired into `llm/client/skill_hints.go`:

- **Anthropic Claude**: `thinking_budget` via the `interleaved-thinking-2025-05-14` beta header
- **OpenAI o1/o3/o4**: `reasoning.effort = low|medium|high`

The QualityPipeline auto-attaches an effort hint to ctx for agents in `cfg.Reasoning.AutoAgents` (default: planner, refiner, verifier, reflexion). Users override per-turn:

```bash
/thinking on            # force high for next turn (cross-provider)
/thinking off           # force off for next turn
/thinking budget=12000  # tier closest to 12k tokens
/thinking auto          # clear override
```

---

## Priority of overrides

For a given turn, the effort hint is resolved in this order (later wins):

1. Skill frontmatter (`effort: high`)
2. Agent default (e.g. PlannerAgent → "high")
3. `CHATCLI_QUALITY_REASONING_*` (auto-enable for AutoAgents)
4. `/thinking` session override

For Refine / Verify / Reflexion enabled state:

1. `/config quality` (env vars `CHATCLI_QUALITY_*`)
2. `/refine` and `/verify` session toggles

For Plan-First:

1. `/plan` one-shot flag
2. `CHATCLI_QUALITY_PLAN_FIRST_MODE` + complexity heuristic

---

## Inspecting state

```bash
/config quality            # all six sub-sections
```

Shows:
- master switch (`CHATCLI_QUALITY_ENABLED`)
- hooks registered (`pre=N, post=M`)
- per-pattern config (refine, verify, reflexion, plan-first, hyde, reasoning)
- vector index state (provider name + entry count, when wired)

## Cost & latency notes

| Pattern | Extra LLM calls per turn | Notes |
|---------|--------------------------|-------|
| ReAct | 0 (already part of the loop) | — |
| Plan-First (auto) | +1 (planner) when triggered | Steps reuse the dispatcher |
| Reflexion | +1 (lesson generator), background | Never blocks the turn |
| HyDE 3a | +1 (hypothesis), cheap | 200 tokens budget |
| HyDE 3b | +1 (query embed) per turn + lazy backfill | embedding API ~$0.00002/1k tokens |
| Self-Refine | +N (one per pass, default 1) | Convergence cuts it short |
| CoVe | +1 (verifier) per call site | Internally uses N=3 questions |
| Reasoning auto | 0 calls; +tokens on hosted thinking | Anthropic budget = 8k by default |

Defaults keep the heavy patterns (Refine, Verify, HyDE) off so steady-state cost is identical to pre-pattern chatcli. Opt in via `/config quality`, env, or session slashes when you want them.
