# Contributing to ChatCLI

Thank you for helping make ChatCLI better. Every contribution is welcome, from a typo in the documentation to a new provider or agent capability. Issues and pull requests may be written in English or Portuguese; code, comments, commit messages and documentation are in English.

By taking part you agree to follow our [Code of Conduct](CODE_OF_CONDUCT.md).

**Found a security problem?** Do not open an issue or a pull request: follow [SECURITY.md](SECURITY.md) to report it privately.

## Where to start

- **Bugs and ideas**: search the [issues](https://github.com/diillson/chatcli/issues) first, then open one with the version (`chatcli --version`), your OS, the provider and model, and the steps to reproduce.
- **Larger changes** (a new provider, a new builtin tool, a new surface, anything that changes defaults or security behavior): open an issue to agree on the approach before writing the code.
- **Documentation** lives in its own repository, [diillson/chatcli.ai](https://github.com/diillson/chatcli.ai), published at [chatcli.edilsonfreitas.com](https://chatcli.edilsonfreitas.com). Every page exists in English and Portuguese; change both.

## Development setup

You need:

- **Go** at the version in [`go.mod`](go.mod).
- **golangci-lint** v2 (the version CI pins is in [`.github/workflows/1-ci.yml`](.github/workflows/1-ci.yml)).
- **git**.

```bash
git clone https://github.com/<you>/chatcli.git
cd chatcli
make build          # bin/chatcli, with version information
go test ./...       # the whole suite; it never touches your real ~/.chatcli
make lint           # golangci-lint run
```

To try your build against a model, log in from the prompt with `/auth login <provider>` (OAuth, for example `/auth login openai-codex`) or put an API key in a `.env` file in the project root:

```env
LLM_PROVIDER=OPENAI
OPENAI_API_KEY=sk-...
```

The Kubernetes operator is a separate Go module: run `go test ./...` inside [`operator/`](operator) for changes there.

## Making a change

1. Branch from `main`. `develop` is a mirror that is synced automatically; never target it.
   ```bash
   git checkout -b feat/short-description main
   ```
2. Write the change and its tests. A bug fix comes with a test that fails without it.
3. Run `gofmt -w`, `make lint` and `go test ./...` before pushing.
4. Open a pull request against `main` with a [Conventional Commits](https://www.conventionalcommits.org/) title. Describe what changed and why, and how you verified it.

Pull requests are squash-merged, so the PR title becomes the commit on `main` and feeds the changelog. Releases are cut by the maintainers with release-please: do not bump the project version yourself.

## Conventions

### Go code

- Format with `gofmt`. `golangci-lint` must report no issues, and lint suppressions (`//nolint`) are not accepted: fix the cause.
- New Go files carry the license header used across the project:
  ```go
  /*
   * ChatCLI - Command Line Interface for LLM interaction
   * Copyright (c) 2024 Edilson Freitas
   * License: Apache-2.0
   */
  ```
- No `TODO`, `FIXME` or `panic` in new production code, and no hard-coded secrets, even fake-looking ones. Test fixtures use synthetic data, never real personal data.
- Keep new functions under a cyclomatic complexity of 30.
- **Public API is stable.** Do not remove or change the signature of an exported function, type, constant or variable. Add a new one and keep the old one delegating to it. A breaking change needs maintainer approval.
- **New dependencies** need maintainer approval: say in the PR why the standard library or an existing dependency does not do the job.

### User-facing text (i18n)

Every string a user sees goes through `i18n.T` and exists in `en`, `en-US` and `pt-BR`.

- Add new keys in a fragment file for your feature, `i18n/locales/<tag>.<feature>.json`, rather than in the main catalog.
- Check parity locally:
  ```bash
  go run ./tools/qg/cmd/qg-i18n-parity -locales-dir i18n/locales -source-root . \
    -exclude vendor -exclude tools/docgen -exclude proto -exclude node_modules -exclude .git
  ```

Text sent to a model (system prompts, tool descriptions, instructions) stays in English and is never translated.

### Providers

ChatCLI treats every provider as first class: a capability added for one provider is expected on all of them that can support it. A new provider must be wired through every touch point listed in [`tools/qg/providerparity/providers.go`](tools/qg/providerparity/providers.go): catalog, manager, cost tracking, `/config`, redaction, i18n and the operator CRD. CI checks this.

### Builtin tools

Every builtin tool (`cli/plugins/builtin_*.go`) has a semantic version in `Version()`. The model sees it in the tool catalog and in `@tools describe`.

- **When to bump:** whenever the description, usage, schema, or a behavior the model relies on changes:
  - **patch** for wording, examples or a fix;
  - **minor** for a new subcommand, argument or capability;
  - **major** for one removed or renamed.
- **How CI checks it:** `TestBuiltinVersions` fails when a tool's surface changes without a bump.
- **After bumping,** record the new version:
  ```bash
  UPDATE_BUILTIN_VERSIONS=1 go test ./cli -run TestBuiltinVersions
  ```

Make sure every argument a tool accepts appears in its `Schema()`: that is what the model reads.

### Tests

- **The suite is hermetic.** Nothing is written to your real `~/.chatcli`, and the system keychain is never queried.
- **Packages that touch the home store or the credentials key** need setup. This covers the cost tracker, checkpoints, memory, sessions and logs. Such a package declares a `TestMain` that calls `testenv.Main(m)` from [`pkg/testenv`](pkg/testenv). It points `HOME` at a throwaway directory and sets `CHATCLI_KEYCHAIN_BACKEND=file`.
- **If a new test writes outside `t.TempDir()`,** fix it in `TestMain`, never with a real path.
- **To check,** run this; it must list nothing:
  ```bash
  touch marker; go test ./...; find ~/.chatcli -newer marker -type f
  ```

### Commits

- Use [Conventional Commits](https://www.conventionalcommits.org/) with a single type and an optional scope, for example `feat(moa): …` or `fix(xai): …`.
- Allowed types: `feat`, `fix`, `refactor`, `perf`, `docs`, `test`, `build`, `ci`, `chore`, `revert`, `style`.
- Do not add `Co-Authored-By` trailers, and do not use nested parentheses in the commit body.

## What CI checks

Every pull request runs the quality gate. Each floor below must pass:

| Floor | What it checks |
|---|---|
| Build & static | `go build`, `go vet`, `gofmt`, `golangci-lint` |
| Coverage | total coverage may not drop; new code needs **60%** patch coverage (80% in `auth/`, 70% in `llm/` and the operator controllers) |
| AI smells | `TODO`/`FIXME`, `panic`, `any` in public APIs, hard-coded user strings, secrets, unapproved dependencies, removed exports |
| Scope | about 800 changed lines or 25 files triggers a warning; 2,000 lines or 50 files needs maintainer approval: split large changes |
| E2E | end-to-end tests |
| Commit lint | Conventional Commits rules above |
| Complexity | new functions up to cyclomatic complexity 30 |
| Secrets | gitleaks over the PR's commits |
| i18n parity | every key in every locale |
| CRD drift | operator CRDs match the generated ones |
| License headers | new Go files carry the header |
| API breaking | no removed or changed exported declarations |
| Binary size | `chatcli` stays under 150 MB |
| Provider parity | every provider wired through every touch point |

Security scans (`govulncheck`, `gosec`, CodeQL, Trivy and dependency review) also run on every pull request. The scripts behind each floor are in [`scripts/qg`](scripts/qg) and can be run locally; see [`scripts/qg/README.md`](scripts/qg/README.md).

## License

By contributing, you agree that your contributions are licensed under the [Apache License 2.0](LICENSE).
