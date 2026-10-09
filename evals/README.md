# ChatCLI evals

Offline evaluation suite for ChatCLI. Each case runs the **real `chatcli` binary** (`chatcli -p`, in chat or `/coder` mode) inside a throwaway sandbox. Its output is graded by deterministic checks and, where a judgment call is needed, by an LLM judge with a rubric. Results from repeated trials are aggregated and can be compared against a saved baseline, so a change that makes ChatCLI worse fails CI instead of shipping.

```bash
chatcli eval validate evals/              # parse and validate only, no LLM and no cost
chatcli eval list evals/ --filter tag:coder

# Use a judge that is a different model from the candidate: self-grading is biased.
chatcli eval run evals/ --provider CLAUDEAI --model claude-sonnet-5-5 \
  --judge-provider OPENAI --judge-model gpt-6.1-sol \
  --trials 3 --max-cost 2 --out runs/sonnet.json --markdown runs/sonnet.md

# CI gate: fail if anything that passed in the baseline fails now.
chatcli eval run evals/ --baseline runs/main.json --max-regressions 0 --min-pass-rate 0.9
chatcli eval compare runs/main.json runs/sonnet.json
```

Exit codes: `0` ok, `1` pass rate below `--min-pass-rate` (default 1.0), `2` usage error or invalid suite, `3` regression against `--baseline`.

## How a trial runs

1. A temp directory is created. The case's `fixture` is copied into it, its inline `files` are written, and its `setup` commands are run.
2. For `coder` cases, a workspace-local `coder_policy.json` (merged over your global policy) is written. It allows `@coder`, including `exec`, inside the sandbox; set `policy:` on a case to narrow it. Safety-immune operations still require approval, and with nobody there to approve they are denied.
3. `chatcli -p "<prompt>" --raw --no-anim` runs with no stdin. By default the run is **hermetic**: long-term memory, bootstrap, memory and session recall, session autosave, coder checkpoints and history are all off. `--with-memory` evaluates with your real memory instead, and that run may also write to it.
4. The binary reports its final answer, tool calls, turns, tokens and cost through `CHATCLI_EVAL_RECORD`, a file outside the sandbox.
5. Checks run against the answer and the sandbox. The sandbox is deleted afterwards unless you pass `--keep`.

## Suite format

```yaml
name: my-suite
judge: {provider: OPENAI, model: gpt-6.1-sol}   # default judge (flags override)
defaults:                                        # inherited by every case
  mode: chat            # chat | coder
  timeout: 4m
  trials: 1
  pass_policy: all      # all (pass^k, default) | any (pass@k) | majority
  env: {KEY: value}
  setup: ["git init -q"]
  checks: [...]         # appended to every case
cases:
  - id: my-case                # letters, digits, . _ -
    tags: [chat]
    prompt: "..."
    fixture: fixtures/dir      # copied into the sandbox (relative to the suite file)
    files: {notes.md: "..."}   # inline files
    policy: [{pattern: "@coder exec", action: deny}]
    skip: "reason"             # keep the case but do not run it
    checks: [...]
```

A directory argument loads every `*.yaml` / `*.yml` file directly inside it, without recursing, so fixtures can contain YAML of their own. Keys are strict: a misspelled key is an error, never a check that is silently ignored.

| Check | Passes when |
|---|---|
| `contains` / `not_contains` / `equals` | the final answer contains, does not contain, or equals the text (`ignore_case: true` available) |
| `regex` / `not_regex` | the answer matches, or does not match, the pattern |
| `json: {require_keys: [...]}` | the answer (or its first fenced or embedded JSON value) parses and has the keys |
| `file_exists` / `file_absent` | the sandbox file exists, or does not |
| `file_contains` / `file_regex: {path, text}` | the sandbox file contains the text, or matches the pattern |
| `command: {run, expect_exit, contains, timeout}` | a shell command run in the sandbox exits as expected (e.g. `go test ./...`) |
| `tool_called` / `tool_not_called` | a tool was, or was not, called. Matches the name (`@coder`) or name plus subcommand (`@coder write`) |
| `max_cost_usd` / `max_turns` / `max_duration` | the run stayed within the budget |
| `judge: {rubric, reference, threshold, samples}` | the LLM judge's median score over `samples` calls is at least `threshold` (default 0.7) |

Checks accept `name:` (shown in reports) and `weight:`, which weights the trial score. A trial passes only if every check passes. File paths in checks must be relative and must stay inside the sandbox.

Fixtures that are Go modules need their own `go.mod`. That keeps them out of ChatCLI's `go test ./...`, which matters because a fixture is often deliberately broken.
