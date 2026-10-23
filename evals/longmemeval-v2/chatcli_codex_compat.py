#!/usr/bin/env python3
"""A `codex exec`-compatible front for chatcli's coder mode.

LongMemEval-V2's `codex` baseline starts a coding agent in a sandbox directory
and expects it to leave `memory_module_output.json` there. The baseline takes
the agent binary as a parameter (`CODEX_BINARY`), so pointing it at this file
runs the same pipeline, unmodified, with chatcli as the coding agent.

What it accepts is exactly what the baseline passes:

    <this> exec -C <sandbox> --skip-git-repo-check
           --dangerously-bypass-approvals-and-sandbox --json -o <last_message>
           -m <model> -c model_reasoning_effort="<effort>" [-c k=v ...] <prompt>

and what it does:

  * runs `chatcli -p "/coder <prompt>"` with the sandbox as the working
    directory, on the model given by -m;
  * gives that run its own empty `CHATCLI_STATE_DIR` next to the sandbox, so
    nothing carries from one question to the next and nothing of the user's own
    memory, skills or history is visible;
  * answers the "bypass approvals" flag with CHATCLI_POLICY_AUTOMODE inside
    that throwaway state directory: the sandbox holds only the benchmark's own
    trajectories and the agent has no other way to act on the machine's data;
  * writes the agent's final message to the -o file and a `turn.completed`
    event with the token usage and cost from chatcli's own run record on
    stdout, the way `codex --json` does.

Environment (all optional):
    CHATCLI_BIN                 chatcli binary (default: chatcli on PATH)
    CHATCLI_BRIDGE_PROVIDER     provider override (default: CLAUDEAI)
    CHATCLI_BRIDGE_EXTRA_ENV    comma-separated KEY=VALUE pairs added to the run
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path

ANSI = re.compile(r"\x1b\[[0-9;?]*[A-Za-z]")


def parse(argv: list[str]) -> dict:
    out: dict = {"configs": []}
    i = 0
    prompt = None
    while i < len(argv):
        a = argv[i]
        if a == "exec":
            pass
        elif a in ("-C", "--cd"):
            i += 1
            out["cwd"] = argv[i]
        elif a == "-o":
            i += 1
            out["last_message"] = argv[i]
        elif a == "-m":
            i += 1
            out["model"] = argv[i]
        elif a == "-c":
            i += 1
            out["configs"].append(argv[i])
        elif a.startswith("-"):
            pass  # --skip-git-repo-check, --dangerously-bypass-..., --json, ...
        else:
            prompt = a
        i += 1
    out["prompt"] = prompt
    return out


def usage_from_record(record: Path) -> dict[str, float]:
    """Token usage and cost from the record chatcli wrote for this run."""
    usage: dict[str, float] = {"input_tokens": 0, "cached_input_tokens": 0, "output_tokens": 0}
    try:
        d = json.loads(record.read_text())
    except (OSError, ValueError):
        return usage
    usage["input_tokens"] = int(d.get("input_tokens") or 0)
    usage["output_tokens"] = int(d.get("output_tokens") or 0)
    usage["cost_usd"] = float(d.get("cost_usd") or d.get("cost") or 0.0)
    usage["requests"] = int(d.get("requests") or 0)
    usage["turns"] = int(d.get("turns") or 0)
    return usage


def main() -> int:
    args = parse(sys.argv[1:])
    cwd = Path(args.get("cwd") or ".").resolve()
    prompt = args.get("prompt") or ""
    model = args.get("model") or "claude-sonnet-5-5"
    binary = os.environ.get("CHATCLI_BIN", "chatcli")
    provider = os.environ.get("CHATCLI_BRIDGE_PROVIDER", "CLAUDEAI")
    state = cwd.parent / "chatcli-state"
    state.mkdir(parents=True, exist_ok=True)
    empty_env = cwd.parent / "empty.env"
    empty_env.write_text("")

    env = {
        "HOME": os.environ.get("HOME", ""),
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "TMPDIR": os.environ.get("TMPDIR", "/tmp"),
        "USER": os.environ.get("USER", ""),
        "LANG": "en_US.UTF-8",
        "CHATCLI_LANG": "en",
        "CHATCLI_STATE_DIR": str(state),
        "CHATCLI_PARK_DIR": str(state / "parked"),
        "CHATCLI_AUTH_DIR": os.environ.get("CHATCLI_AUTH_DIR", str(Path.home() / ".chatcli")),
        "CHATCLI_DOTENV": str(empty_env),
        "CHATCLI_POLICY_AUTOMODE": "true",
        "CHATCLI_HOOKS_ENABLED": "false",
        "CHATCLI_DISABLE_VERSION_CHECK": "true",
        "CHATCLI_SESSION_AUTOSAVE": "false",
        "CHATCLI_CODER_CHECKPOINTS": "off",
        "CHATCLI_DISABLE_HISTORY": "true",
        "CHATCLI_EVAL_RECORD": str(cwd.parent / "chatcli_record.json"),
    }
    for pair in filter(None, os.environ.get("CHATCLI_BRIDGE_EXTRA_ENV", "").split(",")):
        if "=" in pair:
            k, v = pair.split("=", 1)
            env[k.strip()] = v.strip()

    cmd = [binary, "-p", "/coder " + prompt, "--provider", provider, "--model", model,
           "--raw", "--no-anim", "--timeout", os.environ.get("CHATCLI_BRIDGE_TIMEOUT", "1700s")]
    try:
        proc = subprocess.run(cmd, cwd=str(cwd), env=env, stdin=subprocess.DEVNULL,
                              capture_output=True, text=True)
    except OSError as exc:
        print(json.dumps({"type": "error", "message": str(exc)}), flush=True)
        return 127

    final = ANSI.sub("", proc.stdout).strip()
    if args.get("last_message"):
        Path(args["last_message"]).write_text(final + "\n", encoding="utf-8")
    sys.stderr.write(ANSI.sub("", proc.stderr)[-4000:])
    print(json.dumps({"type": "turn.completed", "usage": usage_from_record(cwd.parent / "chatcli_record.json")}), flush=True)
    return proc.returncode


if __name__ == "__main__":
    sys.exit(main())
