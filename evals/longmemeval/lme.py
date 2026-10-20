#!/usr/bin/env python3
"""LongMemEval driver for chatcli.

Each question gets its own CHATCLI_STATE_DIR whose sessions/ holds the
question's haystack as saved chat sessions (file mtime = session date).
chatcli answers in coder one-shot mode with CHATCLI_NOW = question date,
so recall has to find the evidence itself. A second chatcli call grades
the answer with the official LongMemEval judge prompts.

HOME stays the real one (macOS finds the login keychain through it);
CHATCLI_AUTH_DIR points at the real ~/.chatcli credential store, used in
place (never copied), so an OAuth refresh writes back where it belongs.
Parallel workers are safe: OAuth refresh is serialized across processes
(chatcli #1836). Run with stdin closed (< /dev/null): chatcli -p reads stdin.
"""
import argparse
import concurrent.futures as cf
import datetime as dt
import json
import os
import random
import subprocess
import sys
import threading
import time
from collections import defaultdict
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from judge_prompts import get_anscheck_prompt  # noqa: E402

REAL_AUTH_DIR = str(Path.home() / ".chatcli")
LME_DATE = "%Y/%m/%d (%a) %H:%M"

FRAME = (
    "Answer my question using what you know about me from our previous "
    "conversations (they are saved as chat sessions). If those conversations "
    "do not contain the answer, say so. End with a short, direct final answer.\n\n"
    "Question: {q}"
)


def parse_date(s):
    return dt.datetime.strptime(s, LME_DATE)


def sample(data, per_type, seed):
    by = defaultdict(list)
    for e in data:
        by[e["question_type"]].append(e)
    rng = random.Random(seed)
    out = []
    for t in sorted(by):
        out += rng.sample(by[t], min(per_type, len(by[t])))
    return out


def base_env(state, dotenv):
    env = {
        "HOME": str(Path.home()),
        "CHATCLI_STATE_DIR": str(state),
        "CHATCLI_PARK_DIR": str(Path(state) / "parked"),
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
        "TMPDIR": os.environ.get("TMPDIR", "/tmp"),
        "USER": os.environ.get("USER", ""),
        "LANG": "en_US.UTF-8",
        "CHATCLI_LANG": "en",
        "CHATCLI_DOTENV": str(dotenv),
        "CHATCLI_AUTH_DIR": REAL_AUTH_DIR,
        "CHATCLI_HOOKS_ENABLED": "false",
        "CHATCLI_DISABLE_VERSION_CHECK": "true",
        "CHATCLI_SESSION_AUTOSAVE": "false",
        "CHATCLI_CODER_CHECKPOINTS": "off",
        "CHATCLI_DISABLE_HISTORY": "true",
    }
    return env


def session_name(date, i):
    return f"chat-{parse_date(date):%Y%m%d-%H%M}-{i:03d}"


def evidence_names(entry):
    """Saved-session names that hold the answer, per the dataset's labels."""
    want = set(entry.get("answer_session_ids") or [])
    return [session_name(d, i) for i, (d, sid) in enumerate(zip(entry["haystack_dates"], entry["haystack_session_ids"])) if sid in want]


def build_home(entry, root):
    """Write the haystack as saved sessions. Names carry only the date and
    position: the dataset's session ids mark evidence ("answer_…")."""
    home = root / "state"
    sdir = home / "sessions"
    sdir.mkdir(parents=True, exist_ok=True)
    for i, (date, sess) in enumerate(zip(entry["haystack_dates"], entry["haystack_sessions"])):
        when = parse_date(date)
        name = session_name(date, i)
        doc = {
            "version": 2,
            "chat_history": [{"role": m["role"], "content": m["content"]} for m in sess],
        }
        p = sdir / f"{name}.json"
        p.write_text(json.dumps(doc, ensure_ascii=False))
        ts = when.timestamp()
        os.utime(p, (ts, ts))
    (root / "ws").mkdir(exist_ok=True)
    return home


def run_chatcli(bin_, prompt, env, cwd, model, timeout, record=None):
    if record:
        env = dict(env, CHATCLI_EVAL_RECORD=str(record))
    t0 = time.time()
    try:
        p = subprocess.run(
            [bin_, "-p", prompt, "--provider", "CLAUDEAI", "--model", model, "--timeout", f"{timeout}s"],
            env=env, cwd=cwd, stdin=subprocess.DEVNULL, capture_output=True, timeout=timeout + 60,
        )
        if record:
            # Raw bytes kept: chatcli output must be valid UTF-8, and a
            # violation is a finding, not a harness crash.
            Path(str(record) + ".stdout.bin").write_bytes(p.stdout)
        return (p.returncode, p.stdout.decode("utf-8", "replace"),
                p.stderr.decode("utf-8", "replace"), time.time() - t0)
    except subprocess.TimeoutExpired:
        return -1, "", "harness timeout", time.time() - t0


def answer(entry, args, root):
    home = build_home(entry, root)
    env = base_env(home, args.dotenv)
    if not args.no_clock:
        env["CHATCLI_NOW"] = parse_date(entry["question_date"]).strftime("%Y-%m-%dT%H:%M:%S")
    prompt = FRAME.format(q=entry["question"])
    if args.mode == "coder":
        prompt = "/coder " + prompt
    rec = root / "record.json"
    (root / "prompt.txt").write_text(prompt)
    code, out, err, secs = run_chatcli(args.bin, prompt, env, root / "ws", args.model, args.timeout, rec)
    (root / "stderr.txt").write_text(err)
    record = json.loads(rec.read_text()) if rec.exists() else {}
    final = (record.get("final") or out).strip()
    return {"exit": code, "final": final, "stdout": out, "stderr": err[-4000:], "secs": secs, "record": record}


def judge(entry, response, args, root):
    prompt = get_anscheck_prompt(
        entry["question_type"], entry["question"], entry["answer"], response,
        abstention="_abs" in entry["question_id"],
    )
    jhome = root / "judge-state"
    jhome.mkdir(exist_ok=True)
    env = base_env(jhome, args.dotenv)
    env.update({
        "CHATCLI_MEMORY_ENABLED": "false", "CHATCLI_BOOTSTRAP_ENABLED": "false",
        "CHATCLI_MEMORY_AUTORECALL": "false", "CHATCLI_SESSION_AUTORECALL": "false",
    })
    rec = root / "judge-record.json"
    (root / "judge-prompt.txt").write_text(prompt)
    code, out, err, _ = run_chatcli(args.bin, prompt, env, root / "ws", args.judge_model, 300, rec)
    record = json.loads(rec.read_text()) if rec.exists() else {}
    verdict = (record.get("final") or out).strip()
    return {"label": "yes" in verdict.lower(), "verdict": verdict[-500:], "exit": code, "record": record}


def cost_of(rec):
    for k in ("cost_usd", "cost", "total_cost_usd"):
        if isinstance(rec.get(k), (int, float)):
            return float(rec[k])
    return 0.0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--data", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--bin", required=True)
    ap.add_argument("--dotenv", required=True)
    ap.add_argument("--model", default="claude-sonnet-5-5")
    ap.add_argument("--judge-model", default="claude-sonnet-5-5")
    ap.add_argument("--mode", choices=["coder", "chat"], default="coder")
    ap.add_argument("--per-type", type=int, default=10)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--only", default="", help="comma-separated question ids")
    ap.add_argument("--workers", type=int, default=1)
    ap.add_argument("--timeout", type=int, default=600)
    ap.add_argument("--no-clock", action="store_true", help="do not set CHATCLI_NOW (control)")
    args = ap.parse_args()

    data = json.load(open(args.data))
    picks = sample(data, args.per_type, args.seed)
    if args.only:
        want = set(args.only.split(","))
        picks = [e for e in data if e["question_id"] in want]
    if args.limit:
        picks = picks[: args.limit]
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    results_path = out / "results.jsonl"
    done = set()
    if results_path.exists():
        done = {json.loads(l)["question_id"] for l in results_path.open()}
    lock = threading.Lock()

    def one(entry):
        root = out / "runs" / entry["question_id"]
        root.mkdir(parents=True, exist_ok=True)
        a = answer(entry, args, root)
        j = judge(entry, a["final"], args, root) if a["final"] else {"label": False, "verdict": "", "exit": None, "record": {}}
        ev = evidence_names(entry)
        transcript = json.dumps(a["record"].get("transcript") or [], ensure_ascii=False)
        tool_text = " ".join((t.get("args") or "") for t in (a["record"].get("tool_calls") or []))
        row_extra = {
            "question_date": entry["question_date"],
            "haystack_sessions": len(entry["haystack_sessions"]),
            "haystack_chars": sum(len(m["content"]) for sess in entry["haystack_sessions"] for m in sess),
            "evidence_sessions": ev,
            "evidence_opened": [n for n in ev if n in tool_text],
            "evidence_seen": [n for n in ev if n in transcript],
            "out_tok": a["record"].get("output_tokens"),
            "judge_full": j["verdict"],
        }
        row = {
            "question_id": entry["question_id"], "question_type": entry["question_type"],
            "abstention": "_abs" in entry["question_id"], "question": entry["question"],
            "answer": entry["answer"], "hypothesis": a["final"], "label": j["label"],
            "verdict": j["verdict"], "exit": a["exit"], "secs": round(a["secs"], 1),
            "turns": a["record"].get("turns"), "tools": [(t.get("name", "") + " " + (t.get("args") or "")[:120]) for t in (a["record"].get("tool_calls") or [])],
            "requests": a["record"].get("requests"), "in_tok": a["record"].get("input_tokens"), "err": a["record"].get("error"),
            "cost_answer": cost_of(a["record"]), "cost_judge": cost_of(j["record"]),
            "stderr_tail": a["stderr"][-800:],
            **row_extra,
        }
        with lock:
            with results_path.open("a") as f:
                f.write(json.dumps(row, ensure_ascii=False) + "\n")
            print(f"[{row['question_type']:<26}] {row['question_id']:<14} {'PASS' if row['label'] else 'fail'} "
                  f"exit={row['exit']} {row['secs']}s ${row['cost_answer']:.3f}", flush=True)

    manifest = out / "manifest.json"
    if not manifest.exists():
        import hashlib
        def sha(p):
            return hashlib.sha256(Path(p).read_bytes()).hexdigest()
        info = subprocess.run([args.bin, "--version"], capture_output=True, text=True, stdin=subprocess.DEVNULL).stdout
        manifest.write_text(json.dumps({
            "started": dt.datetime.now().isoformat(timespec="seconds"),
            "binary": str(args.bin), "binary_sha256": sha(args.bin), "binary_version": info[-800:],
            "dataset": str(args.data), "dataset_sha256": sha(args.data),
            "harness_sha256": sha(__file__), "judge_prompts_sha256": sha(Path(__file__).with_name("judge_prompts.py")),
            "answer_model": args.model, "judge_model": args.judge_model, "mode": args.mode,
            "question_frame": FRAME, "reference_clock": not args.no_clock,
            "workers": args.workers, "timeout_s": args.timeout, "picked": len(picks),
        }, indent=2, ensure_ascii=False))
    todo = [e for e in picks if e["question_id"] not in done]
    print(f"{len(picks)} picked, {len(todo)} to run", flush=True)
    with cf.ThreadPoolExecutor(args.workers) as ex:
        for f in cf.as_completed([ex.submit(one, e) for e in todo]):
            f.result()


if __name__ == "__main__":
    main()
