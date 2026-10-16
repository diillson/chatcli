## What and why

<!-- What this pull request changes and the problem it solves. Link the issue: Fixes #123 -->

## How it was verified

<!-- Tests added or run, and any manual check (commands, provider and model used). -->

## Checklist

- [ ] The title follows [Conventional Commits](https://www.conventionalcommits.org/) with a single type, for example `feat(moa): …` or `fix(xai): …`.
- [ ] Tests cover the change; a bug fix includes a test that fails without it.
- [ ] `gofmt`, `make lint` and `go test ./...` pass locally.
- [ ] No exported declaration was removed or changed (add a new one and delegate).
- [ ] New user-facing text goes through `i18n.T`, in `en`, `en-US` and `pt-BR`.
- [ ] A builtin tool whose description, usage, schema or behavior changed has its version bumped and recorded (`UPDATE_BUILTIN_VERSIONS=1 go test ./cli -run TestBuiltinVersions`).
- [ ] `README.md` and `README_PT.md`, `CONTRIBUTING.md` and `SECURITY.md` reflect the change where it touches them.
- [ ] The documentation at [chatcli.ai](https://github.com/diillson/chatcli.ai) is updated in English and Portuguese, or no update is needed.
- [ ] No secrets, tokens or personal data in code, tests, logs or screenshots.

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the full conventions and what CI checks.
