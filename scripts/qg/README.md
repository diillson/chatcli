# Quality Gate

The single required check on every PR to `main` / `develop`. The aggregator
job (`Quality Gate`) runs after the 15 individual floor jobs and posts a
sticky comment with a structured verdict table; only the aggregator needs
to be marked **required** in branch protection.

## Architecture

```
                          ┌─────────────────────────────────┐
                          │  .github/workflows/             │
                          │  quality-gate.yml               │
                          │                                 │
   meta ───┬─► build      │  Each floor is its own job;     │
            ├─► coverage  │  every job calls a script in    │
            ├─► patch     │  scripts/qg/ (or runs inline    │
            ├─► smells    │  for the lightweight floors).   │
            ├─► scope     │                                 │
            ├─► e2e       │  Floor scripts shell out to     │
            ├─► commit    │  the Go helpers under           │
            ├─► cyclo     │  tools/qg/cmd/ for the          │
            ├─► secrets   │  parsing-heavy work, then       │
            ├─► i18n      │  emit key=value lines to        │
            ├─► crd       │  $GITHUB_OUTPUT.                │
            ├─► license   │                                 │
            ├─► apidiff   │  The aggregator collects every  │
            ├─► binary    │  job's outputs and runs         │
            ├─► provider  │  qg-verdict to render the       │
            │             │  sticky-comment markdown.       │
            └─► AGGREGATOR ─────► qg-verdict ─► sticky comment + verdict line
                          └─────────────────────────────────┘
```

## Files

| File | Purpose |
|------|---------|
| `.github/workflows/quality-gate.yml`           | Orchestrator workflow. Runs on `pull_request`. |
| `.github/workflows/quality-gate-baseline.yml`  | Publishes total-coverage baseline on pushes to `main`/`develop`. |
| `.github/workflows/security-scan.yml`          | Companion workflow — govulncheck, gosec, trivy. |
| `.github/quality-gate.yml`                     | All thresholds and enforcement modes (single source of truth). |
| `.github/labels.yml`                           | Bypass labels (large/wide PR, deps, breaking change, refactor, skip). |
| `.github/actions/setup-qg-tools/`              | Composite action — installs yq/gocyclo/apidiff/controller-gen/govulncheck and pre-builds the in-repo Go helpers. |
| `.golangci.yml`                                | Strict linter config; diff-only enforcement via `--new-from-rev`. |
| `scripts/qg/lib.sh`                            | Shared bash helpers (`qg_log`, `qg_set_output`, `qg_tool`, ...). |
| `scripts/qg/*.sh`                              | One script per floor. Each is independently runnable. |
| `tools/qg/diffcover/`                          | Go-native cover-profile + diff parser used by Floors 2 and 3. |
| `tools/qg/i18nparity/`                         | Go-native i18n locale and Go-source AST scanner used by Floor 10. |
| `tools/qg/providerparity/`                     | LLM provider parity matrix + exemption table used by Floor 15. |
| `tools/qg/cmd/qg-*/`                           | CLI binaries — `qg-diffcover`, `qg-cover-total`, `qg-i18n-parity`, `qg-provider-parity`, `qg-fan-in`, `qg-verdict`. Pre-built by setup-qg-tools. |

## The 15 floors

| # | Floor | Runs | Gates | Bypass label |
|---|-------|------|-------|--------------|
| 1 | Build & Static     | root + operator | `go build`, `go vet`, `gofmt`, `golangci-lint --new-from-rev` | — |
| 2 | Coverage ratchet   | root + operator merged | total % never regresses vs. baseline | — |
| 3 | Patch coverage     | diff only       | added lines covered ≥ global + per-path thresholds | `refactor-only` |
| 4 | AI smells          | diff only       | TODO/panic/`any` in API/secrets/new deps/removed exports | `deps:approved`, `breaking-change` |
| 5 | Scope budget       | diff only       | files ≤ 50 / LOC ≤ 2000 / blast-radius surfaced | `large-pr-approved`, `wide-pr-approved` |
| 6 | E2E + race         | full repo       | `go test -race ./e2e/...` | — |
| 7 | Commit lint        | per commit      | Conventional Commits, no `Co-Authored-By`, no nested parens in body | — |
| 8 | Cyclo (new code)   | diff only       | gocyclo ≤ 30 on changed files | `cyclo_new.exempt` config |
| 9 | Secrets scan       | diff only       | gitleaks | — |
| 10 | i18n parity       | full repo       | every locale has every key + every `i18n.T(...)` resolves | — |
| 11 | CRD drift         | conditional     | controller-gen output matches checked-in YAML | — |
| 12 | License headers   | new files only  | Copyright + License preamble on new Go files | — |
| 13 | API breaking      | diff only       | `apidiff` reports no incompatible changes | `breaking-change` |
| 14 | Binary size       | full build      | `chatcli` ≤ 100 MB, operator ≤ 100 MB | — |
| 15 | Provider parity   | full repo       | every catalog provider wired through every touch point | (config exemptions) |

## Sticky comment table

After every PR the aggregator posts a comment with this layout:

```
| Floor                | Status | Result                | Δ vs main | Budget        |
|----------------------|--------|-----------------------|-----------|---------------|
| 1 · Build & Static   | ✅     | go build / vet / lint | —         | —             |
| 2 · Coverage         | ✅     | 33.7% (baseline 33.3) | +0.4      | ≥ baseline    |
| 3 · Patch coverage   | ✅     | 81% (req ≥ 60%)       | —         | ≥ 60%         |
| ...                                                                                  |
```

- **Status** uses ✅ / ❌ / ⏭️. A passing floor close to its budget gets ⚠️
  (e.g. patch coverage ≤ 10 points above its bar; scope LOC over the warn line).
- **Result** is the actual measurement, not the command that produced it.
- **Δ vs main** highlights regression direction on coverage.
- **Budget** restates the threshold so reviewers don't open `quality-gate.yml`.

## Bypass labels

| Label | Effect |
|-------|--------|
| `large-pr-approved`  | Waives Floor 5 LOC threshold. |
| `wide-pr-approved`   | Waives Floor 5 file-count threshold. |
| `deps:approved`      | Allows new `go.mod` entries (Floor 4). |
| `breaking-change`    | Allows removed exports and incompatible API diffs (Floors 4 and 13). |
| `refactor-only`      | Relaxes Floor 3 global + per-path bars to `refactor_threshold_pct`. |
| `quality-gate-skip`  | Nuclear: skips every floor. Logged prominently in the sticky comment. |

## Tuning thresholds

Every threshold and enforcement mode lives in `.github/quality-gate.yml`.

```yaml
# Tighten patch coverage from 60 to 70 globally
coverage_patch:
  threshold_pct: 70

# Require 90% coverage on a new security-critical area
coverage_patch:
  per_path:
    - path: "auth/oauth/**"
      threshold_pct: 90

# Demote a noisy floor to warn-only without removing it
ai_smells:
  enforcement: warn

# Raise the binary size budget after an intentional dep addition
binary_size:
  chatcli_mb: 120
```

No workflow changes are needed — scripts read the YAML at run time.

## Provider parity exemptions

The list of LLM providers comes from `Provider*` constants in
`llm/catalog/catalog.go`. The touch-point matrix and per-provider
exemption table live in `tools/qg/providerparity/providers.go` so the
matrix is the single source of truth. Add a new exemption when the
mismatch is legitimate (Bedrock uses AWS auth, not `BEDROCK_API_KEY`),
and the gate explains itself when it fires.

## Coverage methodology

Floor 2 runs

```bash
go test -race -coverpkg=./... -coverprofile=coverage-root.out ./...
( cd operator && go test -race -coverpkg=./... -coverprofile=../coverage-operator.out ./... )
# merge: keep one mode header, append records
```

`-coverpkg=./...` is required so packages without `_test.go` files still
appear in the profile. Without it, a brand-new package added in a PR has
zero entries and looks "100% covered" to any diff-cover tool. Floor 3
catches this case explicitly and refuses to pass when a Go file in the
diff has zero profile entries.

The merged profile contains duplicate records (one per test binary that
touched a given block); the in-house Go parser dedupes by keeping the
max count across duplicates, matching `go tool cover` semantics.

`go tool cover -func` cannot read the merged multi-module profile (it
errors on packages outside the current go.mod). The Floor 2 ratchet
script extracts the total via `qg-cover-total`, which parses the
profile directly with no compilation step.

## Adding a new floor

1. Add a wrapper to `scripts/qg/<name>.sh`. Source `lib.sh`, do the work,
   emit `qg_set_output passed true|false` and a summary line.
2. Add a config block to `.github/quality-gate.yml`.
3. Add a job to `.github/workflows/quality-gate.yml`. Use the
   `setup-qg-tools` composite action where possible.
4. Wire the job's `result` and outputs into the aggregator's `env`.
5. Add a row in `tools/qg/cmd/qg-verdict/main.go:loadFloors` with the
   Floor's number, name, result formatter and optional budget.
6. Test:
   ```bash
   go test -race ./tools/qg/...
   bash scripts/qg/<name>.sh
   ```

## Local dry-run

Every floor script is independently runnable:

```bash
export QG_BASE_REF=origin/main
go test -race -coverpkg=./... -coverprofile=coverage.out ./...

bash scripts/qg/coverage-ratchet.sh   coverage.out
bash scripts/qg/patch-coverage.sh     coverage.out
bash scripts/qg/ai-smells.sh
bash scripts/qg/scope-budget.sh
bash scripts/qg/commit-lint.sh
bash scripts/qg/cyclo-new.sh
bash scripts/qg/i18n-parity.sh
bash scripts/qg/crd-drift.sh
bash scripts/qg/license-headers.sh
bash scripts/qg/api-breaking.sh
bash scripts/qg/binary-size.sh
bash scripts/qg/provider-parity.sh
```

Render the verdict markdown locally:

```bash
R_BUILD=success R_COV=success ... \
  go run ./tools/qg/cmd/qg-verdict -body /tmp/verdict.md
cat /tmp/verdict.md
```

`yq` and bash ≥ 4 are required (macOS: `brew install yq bash`, run via
`/opt/homebrew/bin/bash`). Every Go helper is buildable with the
stdlib only — no external dependencies in `tools/qg/`.

## Branch protection

Mark **Quality Gate** (aggregator job) as required. Do not require the
individual floor jobs — their names can change when the workflow is
restructured, and a renamed required check blocks merges silently.

## Behaviour matrix

| PR shape | Floors that run |
|----------|-----------------|
| Bot PR (release-please / dependabot / github-actions) | none — gate skipped |
| `quality-gate-skip` label | none — gate skipped |
| Docs/CI only (no Go files) | 1, 2, 5, 7, 9, 10, 11, 15 |
| Pure refactor (`refactor-only`) | all — Floor 3 thresholds relaxed |
| Normal PR | all 15 |
