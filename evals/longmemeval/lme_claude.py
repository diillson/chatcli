#!/usr/bin/env python3
"""LongMemEval driver for Claude Code, built to mirror lme.py (the chatcli
driver) as closely as the two products allow.

Same dataset, same question frame, same answering model, same judge (the
official LongMemEval prompts run through chatcli chat mode, exactly as in
the chatcli runs), same session names (date and position only, never the
dataset ids that mark evidence), nothing pasted into the prompt.

What differs, because the products differ:

- chatcli keeps conversations as saved sessions and has its own tools to
  search them. Claude Code has no store of past conversations to search, so
  each question's history is written as plain transcript files in
  sessions/ of an otherwise empty working directory, and the prompt says
  where they are. Claude Code finds the evidence with its own tools
  (Grep, Read, Glob, Bash, subagents).
- chatcli takes the question date from CHATCLI_NOW. Claude Code has no
  reference clock, so the prompt states the date.

Claude Code runs with --safe-mode (no CLAUDE.md, skills, plugins, hooks or
MCP servers of the machine), no user settings, no web tools and no saved
session, in a working directory of its own per question.
"""
import argparse
import concurrent.futures as cf
import datetime as dt
import hashlib
import json
import os
import subprocess
import sys
import threading
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import lme  # noqa: E402  (the chatcli driver: sampling, names, judge)

FRAME = (
    "Answer my question using what you know about me from our previous "
    "conversations (they are saved as chat session files in the sessions/ "
    "directory, one file per conversation). If those conversations "
    "do not contain the answer, say so. End with a short, direct final answer.\n\n"
    "Today is {today}; take that as the current date, whatever the system clock says.\n\n"
    "Question: {q}"
)
TOOLS_DENIED = "WebSearch,WebFetch"
TOOLS_ALLOWED = "Read,Grep,Glob,Bash"


def build_workspace(entry, root):
    ws = root / "ws"
    sdir = ws / "sessions"
    sdir.mkdir(parents=True, exist_ok=True)
    for i, (date, sess) in enumerate(zip(entry["haystack_dates"], entry["haystack_sessions"])):
        name = lme.session_name(date, i)
        lines = [f"# Chat session {name}", f"Date: {date}", ""]
        for m in sess:
            lines += [f"## {m['role']}", "", m["content"], ""]
        p = sdir / f"{name}.md"
        p.write_text("\n".join(lines))
        ts = lme.parse_date(date).timestamp()
        os.utime(p, (ts, ts))
    return ws


def text_of(content):
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        return " ".join(text_of(c.get("text") or c.get("content") or "") if isinstance(c, dict) else str(c) for c in content)
    return ""


def run_claude(args, prompt, ws, root):
    env = {k: v for k, v in os.environ.items() if not k.startswith(("CLAUDE", "ANTHROPIC"))}
    env["LANG"] = "en_US.UTF-8"
    cmd = [args.claude, "-p", prompt, "--model", args.model, "--safe-mode", "--setting-sources", "project,local",
           "--strict-mcp-config", "--no-session-persistence", "--disallowedTools", TOOLS_DENIED,
           "--allowedTools", TOOLS_ALLOWED, "--permission-prompts", "none",
           "--output-format", "stream-json", "--verbose"]
    t0 = time.time()
    try:
        p = subprocess.run(cmd, env=env, cwd=ws, stdin=subprocess.DEVNULL, capture_output=True, timeout=args.timeout)
        code, out, err = p.returncode, p.stdout.decode("utf-8", "replace"), p.stderr.decode("utf-8", "replace")
    except subprocess.TimeoutExpired as e:
        code, out, err = -1, (e.stdout or b"").decode("utf-8", "replace"), "harness timeout"
    secs = time.time() - t0
    (root / "stream.jsonl").write_text(out)
    (root / "stderr.txt").write_text(err)
    tool_calls, seen_text, result, models = [], [], {}, {}
    for line in out.splitlines():
        try:
            ev = json.loads(line)
        except ValueError:
            continue
        msg = ev.get("message") or {}
        if ev.get("type") == "assistant":
            for c in msg.get("content") or []:
                if c.get("type") == "tool_use":
                    tool_calls.append({"name": c.get("name", ""), "args": json.dumps(c.get("input") or {}, ensure_ascii=False)})
        elif ev.get("type") == "user":
            for c in msg.get("content") or []:
                if isinstance(c, dict) and c.get("type") == "tool_result":
                    seen_text.append(text_of(c.get("content")))
        elif ev.get("type") == "result":
            result = ev
            models = ev.get("modelUsage") or {}
    return {"exit": code, "secs": secs, "final": (result.get("result") or "").strip(), "result": result,
            "tool_calls": tool_calls, "seen_text": " ".join(seen_text), "models": models, "stderr": err[-800:]}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--claude", default="claude")
    ap.add_argument("--bin", required=True, help="chatcli binary, used only to run the judge")
    ap.add_argument("--dotenv", required=True)
    ap.add_argument("--model", default="claude-sonnet-5-5")
    ap.add_argument("--judge-model", default="claude-sonnet-5-5")
    ap.add_argument("--per-type", type=int, default=10)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--only", default="")
    ap.add_argument("--workers", type=int, default=1)
    ap.add_argument("--timeout", type=int, default=600)
    args = ap.parse_args()

    data = json.load(open(args.data))
    picks = lme.sample(data, args.per_type, args.seed)
    if args.only:
        want = set(args.only.split(","))
        picks = [e for e in data if e["question_id"] in want]
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    results_path = out / "results.jsonl"
    done = {json.loads(l)["question_id"] for l in results_path.open()} if results_path.exists() else set()
    lock = threading.Lock()

    def one(entry):
        root = out / "runs" / entry["question_id"]
        root.mkdir(parents=True, exist_ok=True)
        ws = build_workspace(entry, root)
        today = lme.parse_date(entry["question_date"]).strftime("%A, %B %d, %Y, %H:%M")
        prompt = FRAME.format(q=entry["question"], today=today)
        (root / "prompt.txt").write_text(prompt)
        a = run_claude(args, prompt, ws, root)
        j = lme.judge(entry, a["final"], args, root) if a["final"] else {"label": False, "verdict": "", "exit": None, "record": {}}
        ev = lme.evidence_names(entry)
        tool_text = " ".join(t["args"] for t in a["tool_calls"])
        res = a["result"]
        usage = res.get("usage") or {}
        in_tok = sum(usage.get(k) or 0 for k in ("input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"))
        row = {
            "question_id": entry["question_id"], "question_type": entry["question_type"],
            "abstention": "_abs" in entry["question_id"], "question": entry["question"],
            "answer": entry["answer"], "hypothesis": a["final"], "label": j["label"],
            "verdict": j["verdict"], "exit": a["exit"], "secs": round(a["secs"], 1),
            "turns": res.get("num_turns"), "tools": [(t["name"] + " " + t["args"][:120]) for t in a["tool_calls"]],
            "requests": None, "in_tok": in_tok, "err": res.get("subtype") if res.get("is_error") else (None if res else "no result event"),
            "cost_answer": float(res.get("total_cost_usd") or 0.0), "cost_judge": lme.cost_of(j["record"]),
            "stderr_tail": a["stderr"],
            "question_date": entry["question_date"],
            "haystack_sessions": len(entry["haystack_sessions"]),
            "haystack_chars": sum(len(m["content"]) for sess in entry["haystack_sessions"] for m in sess),
            "evidence_sessions": ev,
            "evidence_opened": [n for n in ev if n in tool_text],
            "evidence_seen": [n for n in ev if n in a["seen_text"] or n in tool_text],
            "out_tok": usage.get("output_tokens"),
            "judge_full": j["verdict"],
            "models": {k: round(float(v.get("costUSD") or 0), 4) for k, v in a["models"].items()},
        }
        with lock:
            with results_path.open("a") as f:
                f.write(json.dumps(row, ensure_ascii=False) + "\n")
            print(f"[{row['question_type']:<26}] {row['question_id']:<14} {'PASS' if row['label'] else 'fail'} "
                  f"exit={row['exit']} {row['secs']}s ${row['cost_answer']:.3f} tools={len(row['tools'])}", flush=True)

    manifest = out / "manifest.json"
    if not manifest.exists():
        def sha(p):
            return hashlib.sha256(Path(p).read_bytes()).hexdigest()
        ver = subprocess.run([args.claude, "--version"], capture_output=True, text=True, stdin=subprocess.DEVNULL).stdout.strip()
        manifest.write_text(json.dumps({
            "started": dt.datetime.now().isoformat(timespec="seconds"),
            "agent": "Claude Code " + ver, "judge_binary_sha256": sha(args.bin),
            "dataset_sha256": sha(args.data), "harness_sha256": sha(__file__),
            "judge_prompts_sha256": sha(Path(__file__).with_name("judge_prompts.py")),
            "answer_model": args.model, "judge_model": args.judge_model,
            "question_frame": FRAME, "tools_denied": TOOLS_DENIED, "tools_pre_approved": TOOLS_ALLOWED,
            "flags": "--safe-mode --setting-sources project,local --strict-mcp-config --no-session-persistence --permission-prompts none",
            "workers": args.workers, "timeout_s": args.timeout, "picked": len(picks),
        }, indent=2, ensure_ascii=False))
    todo = [e for e in picks if e["question_id"] not in done]
    print(f"{len(picks)} picked, {len(todo)} to run", flush=True)
    with cf.ThreadPoolExecutor(args.workers) as ex:
        for f in cf.as_completed([ex.submit(one, e) for e in todo]):
            f.result()


if __name__ == "__main__":
    main()
