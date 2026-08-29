---
name: task-graph
description: Execute an approved multi-task plan as a verified DAG with @taskgraph — parallel squad workers per task, validation gates the engine runs itself, and an independent reviewer verdict before any task counts as done. Use for deliveries of 5+ tasks with real independence between them; for smaller work dispatch <agent_call> workers directly.
allowed-tools: ["@taskgraph", "@agents", "@board"]
triggers:
  - task graph
  - taskgraph
  - grafo de tarefas
  - orchestrate tasks
  - orquestrar tarefas
  - parallel plan
  - plano paralelo
  - dependent tasks
  - tarefas dependentes
  - dag
---

# Task Graph

Turn an **approved plan** into a persisted DAG executed by parallel squad workers,
where *done* is never a worker's self-report:

```
plan → task graph → parallel execution → engine-run gate → independent review → done | retry
```

The `@taskgraph` engine (deterministic Go code, not an LLM) owns all state. It refuses:
dependency cycles, starting a task before its deps are done, and — the core rule —
promoting a task to `done` without its validation gate passing AND a fresh reviewer
worker issuing `VERDICT: PASS`. Executor and reviewer are always distinct workers.

## When to use it (and when not)

Use it when the delivery has **5+ tasks with real independence** — the parallelism
must pay for the orchestration overhead. For 1–4 tasks, or strictly serial work,
dispatch `<agent_call>` workers directly or just do the work yourself.

The plan must already be agreed with the user. Do not invent a graph mid-conversation
for work the user asked you to do directly.

## The plan schema

```
<tool_call name="@taskgraph" args='{"cmd":"run","args":{"graph":{
  "name": "feature-x",
  "require_review": true,
  "phases": [{"id":"F1","title":"Server"},{"id":"F2","title":"Client"}],
  "tasks": [
    {"id":"T1","phase":"F1","title":"Add /foo endpoint","agent":"coder",
     "prompt":"Implement GET /foo in server/handler.go returning ...",
     "validation":[{"run":"go test ./server/...","expect":"all tests pass, includes a /foo case"}]},
    {"id":"T2","phase":"F2","title":"CLI client for /foo","deps":["T1"],
     "prompt":"Add the /foo client call. Server contract: #T1",
     "validation":[{"run":"go build ./...","expect":"builds clean"},
                   {"run":"go test ./cli/...","expect":"green"}]}
  ]}}}' />
```

Rules that matter:
- `deps` may only reference tasks declared **earlier** in the list (this also rules out cycles).
- `prompt` is what the executor worker receives — make it self-contained; workers do not
  see this conversation. `#<depID>` in a prompt is replaced with that dependency's output.
- `validation[].run` commands are executed by the **engine** (sandboxed, unsafe-command
  gated) — never by the executor. `expect` is prose for the reviewer. A bare string
  instead of the array is allowed (prose-only contract, reviewer verifies by inspection).
- `agent` defaults to `coder`; any squad worker type works. `max_attempts` defaults to 3.
- `require_review` (graph or per task) defaults to **true**. Waive it only for trivial
  mechanical tasks, and say so in the delivery summary.

## Subcommands

```
{"cmd":"run","args":{"graph":{...}}}     plan + execute in one call (streams progress)
{"cmd":"run","args":{"id":"tg-..."}}     run/resume a persisted plan (also resumes after failures)
{"cmd":"plan","args":{"graph":{...}}}    validate + persist only (returns the run id)
{"cmd":"status"}                         per-task status, attempts, verdicts, cost
{"cmd":"show","args":{"task":"T3"}}      one task in full: gate outputs, evidence, run ids
{"cmd":"retry","args":{"task":"T3"}}     re-open a failed task (+1 attempt) and resume
{"cmd":"cancel"}                         stop the active run
{"cmd":"list"}                           persisted runs
```

## Discipline

- **You never declare a task done.** The engine promotes it after gate + reviewer PASS.
  Relay the engine's report; do not soften a FAIL.
- `run` executes the whole graph in one tool call — watch the streamed events instead of
  polling. The user can watch too via `/taskgraph` (it is a mid-run side command).
- A failed run is not the end: fix what the verdicts name (or improve the prompts) and
  use `retry`/`run` to resume — completed tasks stay done.
- Tasks run in the SAME workspace. Design tasks to touch disjoint files; the engine
  snapshots a checkpoint before each executor, but overlapping edits still conflict.
- Costs are real (per-call provider usage attributed per task) — check `status` before
  scaling a graph up.
