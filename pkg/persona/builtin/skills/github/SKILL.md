---
name: github
description: Manage the full GitHub workflow from the terminal — auth, issues, pull request lifecycle (branch/commit/open/CI/merge), code review, and repo management. Uses the gh CLI when present, with a git + REST API fallback. Use when asked to "open a PR", "create an issue", "review this PR", "check CI", "list issues".
allowed-tools: ["@coder", "Bash", "@read"]
triggers:
  - open a pr
  - create a pull request
  - create an issue
  - review this pr
  - check ci
  - list issues
  - merge the pr
  - github
  - abrir um pr
  - criar uma issue
  - revisar o pr
---

# GitHub Workflow

Drive GitHub from the terminal. Prefer the **`gh` CLI** (handles auth + API); fall back to
**`git` + the REST API via `curl`** with a `GITHUB_TOKEN`/`GH_TOKEN` when `gh` is absent.

## Step 0 — Detect auth method (run once)

- **macOS / Linux**: `command -v gh >/dev/null && gh auth status >/dev/null 2>&1 && echo gh || echo git`
- **Windows**: `if (Get-Command gh -ErrorAction SilentlyContinue) { gh auth status }`

If neither `gh` nor a token is available, tell the user to run `gh auth login` or set
`GITHUB_TOKEN`, and stop.

## Issues

```
gh issue list --limit 20
gh issue create --title "Bug: X fails on Y" --body "..."
gh issue view 123 --comments
gh issue close 123
```
REST fallback: `curl -H "Authorization: Bearer $GITHUB_TOKEN" https://api.github.com/repos/OWNER/REPO/issues`

## Pull request lifecycle

```
git checkout -b fix/the-thing
# ...edits, then commit (follow repo conventions)...
git push -u origin fix/the-thing
gh pr create --fill                       # or --title/--body
gh pr checks                              # watch CI
gh pr view --web                          # open in browser
gh pr merge --squash --delete-branch      # when green + approved
```

## Code review

```
gh pr diff 456                # read the diff
gh pr view 456 --comments     # existing discussion
gh pr review 456 --approve    # or --request-changes --body "..."
```
For a thorough review, read the diff with `@read`/`gh pr diff`, check tests/CI, and comment on
concrete lines. Don't approve unseen.

## Repo management

```
gh repo view OWNER/REPO
gh release list
gh workflow list && gh run list --limit 10
```

## Rules

- Respect the repo's branch/commit conventions — read CONTRIBUTING/recent history first.
- Never force-push shared branches or merge without CI green + approval, unless told to.
- Quote the PR/issue number and URL back to the user after acting.
