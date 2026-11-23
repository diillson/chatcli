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
| `.github/workflows/dependabot-automerge.yml`   | Validates and merges Dependabot pull requests (see below). |
| `.github/quality-gate.yml`                     | All thresholds and enforcement modes (single source of truth). |
| `.github/labels.yml`                           | Bypass labels (large/wide PR, deps, breaking change, refactor, skip). |
| `.github/actions/setup-qg-tools/`              | Composite action — installs yq/gocyclo/apidiff/controller-gen/govulncheck and pre-builds the in-repo Go helpers. |
| `.golangci.yml`                                | Strict linter config; diff-only enforcement via `--new-from-rev`. |
| `scripts/qg/lib.sh`                            | Shared bash helpers (`qg_log`, `qg_set_output`, `qg_tool`, ...). |
| `scripts/qg/*.sh`                              | One script per floor. Each is independently runnable. |
| `tools/qg/diffcover/`                          | Go-native cover-profile + diff parser used by Floors 2 and 3. |
| `tools/qg/i18nparity/`                         | Go-native i18n locale and Go-source AST scanner used by Floor 10. |
| `tools/qg/providerparity/`                     | LLM provider parity matrix + exemption table used by Floor 15. |
| `tools/qg/depbot/`                             | Dependency-bump policy, check evaluation and merge sequencing for Dependabot pull requests. |
| `tools/qg/cmd/qg-*/`                           | CLI binaries — `qg-diffcover`, `qg-cover-total`, `qg-i18n-parity`, `qg-provider-parity`, `qg-fan-in`, `qg-verdict`. Pre-built by setup-qg-tools. `qg-depbot` is run by the auto-merge workflow. |

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
| `deps:approved`      | Allows new `go.mod` entries (Floor 4). Applied by the auto-merge workflow to a Dependabot pull request once its diff is proven to be version bumps only. |
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

## Dependabot auto-merge

Dependabot opens grouped pull requests every week (`.github/dependabot.yml`):
one for Go modules across the root and operator modules, one for GitHub
Actions, one for Docker base images. Major updates are not grouped and
arrive on their own.

`.github/workflows/dependabot-automerge.yml` runs `qg-depbot` whenever CI,
the Quality Gate or the Security Scan finishes on a Dependabot branch, and
hourly as a safety net. A pull request is merged only when all of these hold:

1. **The diff is version bumps and nothing else** (`tools/qg/depbot/policy.go`).
   Allowed: `go.sum` changes; `go.mod` require entries whose version moves
   forward; `uses:` references pinned to a commit with a version comment;
   Dockerfile `FROM` tags. Held for review: a new or removed direct
   dependency, a major version, a pre-release or pseudo-version on a direct
   dependency, a downgrade, any `go` / `toolchain` / `replace` / `exclude`
   change, an image whose name or variant changes, and any other file.
2. **Every check on the head commit passed** — including the security scans
   the branch ruleset does not require.
3. **The branch is up to date with `main`.**

Pull requests merge one at a time, oldest first; after each merge only the
next one is updated, so the batch costs one CI run per pull request. A held
or failing pull request gets a single comment explaining why and stays open.

The workflow needs the `DEPENDABOT_TIDY_TOKEN` secret (fine-grained token
with Contents, Pull requests and Workflows write access on this repository)
stored as both an Actions and a Dependabot secret; without it the workflow
does nothing. Try a pass without side effects with
`gh workflow run dependabot-automerge.yml -f dry_run=true`, or locally:

```bash
GH_REPO=diillson/chatcli go run ./tools/qg/cmd/qg-depbot -dry-run
gh pr diff <number> | go run ./tools/qg/cmd/qg-depbot -policy-only
```

## Behaviour matrix

| PR shape | Floors that run |
|----------|-----------------|
| Bot PR (release-please / github-actions) | none — gate skipped |
| `quality-gate-skip` label | none — gate skipped |
| Docs/CI only (no `.go`, `go.mod` or `go.sum` change) | 1, 2, 5, 7, 9, 10, 11, 15 |
| Dependency bump (`go.mod` / `go.sum` only, Dependabot included) | all 15 — Floor 4 needs `deps:approved` |
| Pure refactor (`refactor-only`) | all — Floor 3 thresholds relaxed |
| Normal PR | all 15 |
