# Quality Gate

Single required check on every PR to `main` / `develop`. Bundles 9 floors,
each as an independent CI job, and an aggregator job that posts a sticky
verdict comment and is the only check that needs to be marked **required**
in branch protection.

## Layout

| File | Purpose |
|------|---------|
| `.github/workflows/quality-gate.yml`           | Orchestrator workflow — runs on `pull_request`. |
| `.github/workflows/quality-gate-baseline.yml`  | Publishes total-coverage baseline as a commit status on `main` / `develop`. |
| `.github/workflows/labels-sync.yml`            | Keeps bypass labels in sync with `.github/labels.yml`. |
| `.github/quality-gate.yml`                     | All thresholds and enforcement modes (single source of truth). |
| `.github/labels.yml`                           | Bypass labels (large/wide PR, deps, breaking change, refactor, skip). |
| `scripts/qg/lib.sh`                            | Shared bash helpers used by every script. |
| `scripts/qg/coverage-ratchet.sh`               | Floor 2 — total coverage ratchet vs. baseline. |
| `scripts/qg/patch-coverage.sh`                 | Floor 3 — patch coverage on the PR diff. |
| `scripts/qg/ai-smells.sh`                      | Floor 4 — anti-pattern scanner (TODOs, panic, secrets, …). |
| `scripts/qg/scope-budget.sh`                   | Floor 5 — file/LOC budget with bypass labels. |
| `scripts/qg/commit-lint.sh`                    | Floor 7 — commit hygiene (Conventional Commits, no Co-Author, no nested parens). |
| `scripts/qg/cyclo-new.sh`                      | Floor 8 — cyclomatic complexity on changed files. |

Floor 1 (build/static), Floor 6 (E2E) and Floor 9 (gitleaks) are inline in
the workflow — no helper script needed.

## The 9 floors

| # | Floor | What it gates | Bypass label |
|---|-------|--------------|--------------|
| 1 | Build & Static | `go build`, `go vet`, `gofmt`, `golangci-lint` | — |
| 2 | Coverage ratchet | total %, never regress vs. baseline on main | — |
| 3 | Patch coverage | added lines covered ≥ threshold (default 60%) | `refactor-only` (relaxed) |
| 4 | AI smells | TODO/panic/`any` in public API/secrets/new deps/removed exports | `deps:approved`, `breaking-change` |
| 5 | Scope budget | files ≤ 50 / LOC ≤ 2000 | `large-pr-approved`, `wide-pr-approved` |
| 6 | E2E + race | `go test -race ./e2e/...` | — |
| 7 | Commit lint | Conventional Commits, no `Co-Authored-By`, no nested parens in body | — |
| 8 | Cyclo (new code) | gocyclo ≤ 30 on changed files | (per-file `cyclo_new.exempt` in config) |
| 9 | Secrets scan | gitleaks on the PR diff | — |

## Bypass labels

- `large-pr-approved` — waives Floor 5 LOC threshold.
- `wide-pr-approved` — waives Floor 5 file-count threshold.
- `deps:approved` — allows new `go.mod` entries (Floor 4).
- `breaking-change` — allows removed exports (Floor 4).
- `refactor-only` — relaxes Floor 3 to `coverage_patch.refactor_threshold_pct`.
- `quality-gate-skip` — nuclear: skips every floor. Logged prominently.

## Tuning thresholds

Edit `.github/quality-gate.yml`. Examples:

```yaml
# Tighten patch coverage from 60 -> 70
coverage_patch:
  threshold_pct: 70

# Flip a specific floor to advisory mode (warn, don't block)
ai_smells:
  enforcement: warn
```

No workflow changes are needed — every script reads the YAML at run time.

## First run / bootstrap

The very first PR after this lands has no `quality-gate/coverage` status on
`main`, so:

* Floor 2 enters bootstrap mode — accepts whatever current coverage is.
* Once the merge to `main` triggers `quality-gate-baseline.yml`, every
  subsequent PR ratchets against that baseline.

The baseline workflow also runs on `develop` so feature branches that PR
into `develop` see a useful baseline immediately.

## Branch protection

After this is in place and one green run has been observed on a real PR,
the only branch-protection rule needed is:

```
Require status checks to pass before merging:
  - Quality Gate
```

Do **not** require the individual floor jobs — branch protection sees stale
job names if the workflow changes shape. The aggregator job has a stable name.

## Local dry-run

Every floor script can be executed locally:

```bash
# Compare your branch against main
export QG_BASE_REF=origin/main
go test -race -coverprofile=coverage.out ./...
bash scripts/qg/coverage-ratchet.sh coverage.out
bash scripts/qg/patch-coverage.sh coverage.out
bash scripts/qg/ai-smells.sh
bash scripts/qg/scope-budget.sh
bash scripts/qg/commit-lint.sh
bash scripts/qg/cyclo-new.sh
```

`yq` and bash ≥ 4 are required. On macOS: `brew install yq bash` and run
the scripts via `/opt/homebrew/bin/bash` (default macOS bash is 3.2).

## Behaviour matrix

| PR shape | Floors that run |
|----------|-----------------|
| Bot PR (release-please/dependabot/github-actions) | none — gate skipped |
| `quality-gate-skip` label | none — gate skipped |
| Docs/CI only (no `.go` files changed) | 1, 2, 5, 7, 9 — Go-coupled floors auto-skipped |
| Pure refactor (`refactor-only` label) | all, but Floor 3 uses relaxed threshold |
| Normal PR | all 9 |
