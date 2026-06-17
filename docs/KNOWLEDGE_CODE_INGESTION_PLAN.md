# Knowledge over code & infra repos — `@docs-flatten kind=code`

**Status:** in progress (branch `feat/knowledge-code-ingestion`)
**Goal:** let a user (or the agent) ingest **source-code, Terraform and GitOps/YAML
repositories** into knowledge bases so the AI can connect knowledge **across
layers** (app code ⇄ infra ⇄ deployment) to diagnose and solve a problem.

## Why this works with almost no downstream change

The retrieval side is already there and is **content-agnostic**:

- Multiple knowledge bases attach to a session at once, and `@knowledge search`
  (no `kb` arg) **fans out across all of them**, tagging each hit with its base
  (`cli/ctxmgr/knowledge_query.go` — `knowledgeTargets` / `KnowledgeSearch`).
- Retrieval is hybrid **BM25 (keyless) + optional embeddings**
  (`cli/ctxmgr/retrieval.go`), which works on any text — code identifiers,
  Terraform resource names, YAML keys — not just prose.
- Knowledge mode ingests a **JSONL corpus** whose schema is the contract with the
  context manager (`cli/ctxmgr/knowledge.go` — `docFlattenChunk`). Anything that
  emits that schema is ingestible **today**, with zero ctxmgr changes.

The only gap is **ingestion**: `@docs-flatten` accepts only `.md/.mdx/.markdown`
and chunks Markdown by heading. This plan adds code/config awareness **inside the
existing tool**, keeping the JSONL contract intact.

## Decision: extend `@docs-flatten`, do not add a new builtin

Reuses the existing plumbing (git clone, directory walk, glob include/exclude,
JSONL/json/yaml/text emit, web crawl), keeps a single tool in the catalog, keeps
the JSONL schema, and plugs straight into the autonomous `@context` flow that
already orchestrates `@docs-flatten`. A new builtin would duplicate all of that.

## New parameter: `kind`

| `kind`  | Behavior |
|---------|----------|
| `docs` (default) | Legacy behavior — only `.md/.mdx/.markdown`, Markdown chunking. **Unchanged**, so existing docs workflows never silently start pulling in stray source/config files. |
| `code`  | Ingest source/Terraform/YAML/shell + Markdown, all structure-aware. The "I'm indexing a code/infra repo" mode. |
| `auto`  | Per-file: Markdown files use the Markdown path; recognized code/config files use the structure-aware path; other text files use the generic windowed fallback. |

Code ingestion is **opt-in** (`kind=code` or `kind=auto`) — backward compatible
by default. `kind` only gates the **extension allowlist + default fallback**. The actual
chunker is always chosen **per file, by extension** — so a mixed repo (Terraform
+ YAML + shell + Go) just routes each file to its own chunker. That is the
designed, normal case, not an exception.

## Extension → chunker dispatch

| Extensions | Chunker | Split unit | Title |
|------------|---------|------------|-------|
| `.md .mdx .markdown` | Markdown (existing) | ATX heading (fence-aware) | front-matter title |
| `.go .java .js .jsx .ts .tsx .c .h .cpp .hpp .cs .kt .scala .swift .rs .php .dart .groovy` | brace-block | top-level `{…}` declaration, balanced | symbol on the opening line |
| `.tf .tfvars .hcl` | HCL block | top-level `block "type" "name" {…}` | `type.name` (e.g. `aws_eks_cluster.main`) |
| `.yaml .yml` | YAML doc | `---` document separator | `kind/metadata.name` when present, else first key |
| `.sh .bash .zsh` | shell | `name() {…}` / `function name` + top-level run | function name or `script:<path>` |
| `.py .rb` | indent-block | top-level `def`/`class` (indent-based) | symbol name |
| `.json .toml .ini .env .properties` | generic windowed | size window + overlap | source path |
| anything else textual | generic windowed | size window + overlap | source path |

All chunkers fall back to the **universal line-window packer** (`chunkLines`,
reused) when a single unit exceeds `maxChars`, so no chunk is ever unbounded.
Embedded content (bash inside a YAML `command:`, a heredoc inside `.tf`) stays
**inside** its file-type chunk as searchable text — ~90% structural precision,
the deliberate trade vs. shipping a per-language AST.

## Skip defaults (noise & binaries)

Walk always skips dot-directories (already true) and, for code/auto, also skips
by default: `vendor/`, `node_modules/`, `.terraform/`, `.git/`, `dist/`,
`build/`, `target/`, `.venv/`, `__pycache__/`, lockfiles (`*.lock`,
`go.sum`, `package-lock.json`, `yarn.lock`), minified assets (`*.min.js`,
`*.min.css`), and binary/asset extensions (images, archives, fonts, `.pdf`,
`.so`, `.a`, `.bin`, …). User `include`/`exclude` globs still apply on top.
Files over a size cap (default 1 MiB) are skipped as likely generated/vendored.

## Phase 3 — identifier recall in the lexical index

`tokenizeLexical` (`cli/ctxmgr/lexical.go`) lowercases **before** splitting, so
`snake_case` and `kebab-case` already split on `_`/`-`, but **camelCase /
PascalCase do not** (`getUserName` → `getusername`, one token). Phase 3 splits
camelCase/PascalCase (and letter↔digit) boundaries **before** lowercasing and
emits the sub-tokens **plus** the full joined token, so `eks`, `user`,
`getUserName` and `getusername` all match. Affects all corpora; prose impact is
negligible (camelCase is rare in prose) and BM25 idf absorbs the rest.

## How the AI knows to use `kind=code` (default is `docs`)

Because the default is `docs` (backward compatible), three layers make the agent
pick `kind=code` for a source/infra repo without the user spelling it out every
time:

1. **Tool schema intent.** The `@docs-flatten` schema documents `kind=code` as
   "use this for an app/infra/GitOps repo". When the user's request implies code
   ("index my Terraform repo", "build a base from this service"), the model
   selects `kind=code` from the schema — the same way it already chooses `repo`
   vs `url`.
2. **Autonomous-pipeline guidance.** The cached prompt prefix
   (`contextPipelineHint`) tells the agent: if the source is a code/infra repo
   (contains `.go/.tf/.yaml/.sh/…`), add `kind=code`, and build one base per
   layer (app, infra, gitops) attached together.
3. **Self-correcting hint (safety net).** If the agent (or a human) runs the
   default `docs` on a repo with no Markdown, the tool detects the code/config
   files present and returns an actionable message — "looks like a source/infra
   repo, re-run with `kind=code`" — so it recovers in one turn instead of a
   dead-end "no Markdown matched".

So: the user does **not** have to classify the repo manually. They can, for
precision ("index this as code"), but normal intent + the guidance + the hint
get there on their own. The default stays `docs` only to protect existing
doc workflows from silently absorbing stray source files.

## Workflow (after this lands)

```text
@docs-flatten root=./app   kind=code output=app.jsonl
@docs-flatten root=./infra kind=code output=infra.jsonl     # .tf blocks
@docs-flatten root=./argo  kind=code output=argo.jsonl      # .yaml docs
/context create app   app.jsonl   --mode knowledge
/context create infra infra.jsonl --mode knowledge
/context create argo  argo.jsonl  --mode knowledge
/context attach app && /context attach infra && /context attach argo
# /coder: "Rollout checkout-api won't go ready — connect the argo manifest,
#          the terraform node group, and the service health check in code"
```

A mixed repo can also go into **one** base (the AI sees all layers together).

## Phases / tracking

1. **Phase 1** — `kind` param (parse + validate), extension allowlist, skip
   defaults, size cap.
2. **Phase 2** — structure-aware chunkers (new `docsflatten_code.go`), per-file
   dispatch wired into `docsFlattenFile`, titles.
3. **Phase 3** — camelCase-aware `tokenizeLexical`.
4. **Phase 4** — i18n (pt+en), tool Schema/Usage/Description, golden tests per
   chunker + tokenizer, docs sync (pt+en), build/vet/lint/tests green.

## Non-goals (now)

- Per-language AST parsing / go-to-definition symbol graph.
- Re-indexing on file change / live repo watch (knowledge bases are snapshots).
- Editing via knowledge mode — it is read-only retrieval. The actual fix is done
  in `/coder` with `@read`/`@search`/`@coder` on the live repo.
