#!/usr/bin/env python3
"""Evidence pack for a LongMemEval run of chatcli: scores, costs, latency,
tool usage, evidence retrieval and a failure taxonomy, as JSON + Markdown."""
import collections
import json
import math
import re
import statistics
import sys
from pathlib import Path

run = Path(sys.argv[1])
rows = [json.loads(l) for l in (run / "results.jsonl").open()]
manifest = json.loads((run / "manifest.json").read_text())


def wilson(k, n, z=1.96):
    if n == 0:
        return (0.0, 0.0)
    p = k / n
    d = 1 + z * z / n
    c = p + z * z / (2 * n)
    m = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n))
    return ((c - m) / d, (c + m) / d)


def pct(x):
    return f"{100 * x:.1f}%"


def q(v, p):
    v = sorted(v)
    return v[min(len(v) - 1, int(p * len(v)))] if v else 0


def tool_kind(t):
    m = re.match(r'(\S+)\s+\{"cmd":"(\w+)"', t)
    return f"{m.group(1)} {m.group(2)}" if m else t.split(" ")[0]


n = len(rows)
k = sum(r["label"] for r in rows)
lo, hi = wilson(k, n)
by = collections.defaultdict(list)
for r in rows:
    by[r["question_type"]].append(r)
abst = [r for r in rows if r["abstention"]]
non_abst = [r for r in rows if not r["abstention"]]

types = {}
for t, v in sorted(by.items()):
    kk = sum(r["label"] for r in v)
    types[t] = {"n": len(v), "pass": kk, "acc": kk / len(v), "ci95": wilson(kk, len(v))}
task_avg = statistics.mean(x["acc"] for x in types.values())

secs = [r["secs"] for r in rows]
cost = [r["cost_answer"] for r in rows]
jcost = [r["cost_judge"] for r in rows]
intok = [r["in_tok"] or 0 for r in rows]
outtok = [r["out_tok"] or 0 for r in rows]
ntools = [len(r["tools"] or []) for r in rows]
tools = collections.Counter(tool_kind(t) for r in rows for t in (r["tools"] or []))
flag_calls = [t for r in rows for t in (r["tools"] or []) if re.search(r'"(from|to|as_of)"', t)]
flag_q = sum(1 for r in rows if any(re.search(r'"(from|to|as_of)"', t) for t in (r["tools"] or [])))
web_q = sum(1 for r in rows if any(t.startswith(("@websearch", "@webfetch")) for t in (r["tools"] or [])))
zero_tool = sum(1 for x in ntools if x == 0)
exits = collections.Counter(r["exit"] for r in rows)


def retrieval(sub):
    with_ev = [r for r in sub if r.get("evidence_sessions")]
    seen = sum(1 for r in with_ev if r.get("evidence_seen"))
    opened = sum(1 for r in with_ev if r.get("evidence_opened"))
    full = sum(1 for r in with_ev if set(r.get("evidence_seen") or []) >= set(r["evidence_sessions"]))
    return {"n": len(with_ev), "any_seen": seen, "any_opened": opened, "all_seen": full}


ret_all = retrieval(non_abst)
ret_pass = retrieval([r for r in non_abst if r["label"]])
ret_fail = retrieval([r for r in non_abst if not r["label"]])

# Failure taxonomy (heuristic, from the run's own evidence fields).
tax = collections.Counter()
fails = []
for r in rows:
    if r["label"]:
        continue
    h = (r["hypothesis"] or "").lower()
    if r["exit"] != 0 or not r["hypothesis"]:
        cat = "run error"
    elif r["abstention"]:
        cat = "answered an unanswerable question"
    elif any(t.startswith(("@websearch", "@webfetch")) for t in (r["tools"] or [])):
        cat = "went to the web instead of memory"
    elif not r.get("evidence_seen"):
        cat = "retrieval miss (evidence never surfaced)"
    elif re.search(r"can't (tell|determine|find)|cannot (tell|determine)|not (in|mentioned)|don't (have|see)|no record|não (sei|há|encontr)", h):
        cat = "evidence surfaced but model said it is not there"
    elif set(r.get("evidence_seen") or []) < set(r["evidence_sessions"]):
        cat = "partial evidence (multi-session aggregation)"
    else:
        cat = "evidence surfaced, wrong answer (reasoning)"
    tax[cat] += 1
    fails.append({"id": r["question_id"], "type": r["question_type"], "cat": cat,
                  "question": r["question"], "answer": str(r["answer"]),
                  "hypothesis_tail": (r["hypothesis"] or "")[-300:]})

# Haystack size vs accuracy.
buckets = collections.defaultdict(list)
for r in rows:
    buckets[min(r["haystack_chars"] // 100_000, 7)].append(r["label"])
size_acc = {f"{b*100}k-{(b+1)*100}k chars": f"{sum(v)}/{len(v)}" for b, v in sorted(buckets.items())}

summary = {
    "manifest": manifest,
    "overall": {"n": n, "pass": k, "acc": k / n, "ci95": [lo, hi], "task_averaged_acc": task_avg},
    "by_type": types,
    "abstention": {"n": len(abst), "pass": sum(r["label"] for r in abst)},
    "non_abstention": {"n": len(non_abst), "pass": sum(r["label"] for r in non_abst)},
    "cost_usd": {"answer_total": sum(cost), "judge_total": sum(jcost), "answer_mean": statistics.mean(cost), "answer_p95": q(cost, .95)},
    "latency_s": {"median": statistics.median(secs), "p90": q(secs, .9), "p99": q(secs, .99), "max": max(secs)},
    "tokens": {"input_mean": statistics.mean(intok), "input_p95": q(intok, .95), "output_mean": statistics.mean(outtok)},
    "tools": {"calls_mean": statistics.mean(ntools), "calls_p95": q(ntools, .95), "zero_tool_questions": zero_tool,
              "by_kind": tools.most_common(), "date_flag_calls": len(flag_calls), "date_flag_questions": flag_q,
              "web_questions": web_q},
    "exits": dict(exits),
    "retrieval": {"all": ret_all, "passed": ret_pass, "failed": ret_fail},
    "failure_taxonomy": tax.most_common(),
    "accuracy_by_haystack_size": size_acc,
    "failures": fails,
}
(run / "summary.json").write_text(json.dumps(summary, indent=2, ensure_ascii=False))

md = [f"# chatcli on LongMemEval-S ({n} questions)", "",
      f"- Answer model: `{manifest['answer_model']}` (coder one-shot) · judge: `{manifest['judge_model']}` with the official LongMemEval prompts",
      f"- Binary sha256 `{manifest['binary_sha256'][:16]}…` · dataset sha256 `{manifest['dataset_sha256'][:16]}…` · started {manifest['started']}",
      "", f"**Accuracy: {k}/{n} = {pct(k/n)}** (95% CI {pct(lo)}–{pct(hi)}) · task-averaged {pct(task_avg)}", "",
      "| Type | Pass | Accuracy | 95% CI |", "|---|---|---|---|"]
for t, x in types.items():
    md.append(f"| {t} | {x['pass']}/{x['n']} | {pct(x['acc'])} | {pct(x['ci95'][0])}–{pct(x['ci95'][1])} |")
md += ["", f"- Abstention (unanswerable): {summary['abstention']['pass']}/{summary['abstention']['n']}",
       f"- Cost: ${sum(cost):.2f} answering + ${sum(jcost):.2f} judging · ${statistics.mean(cost):.3f}/question",
       f"- Latency: median {statistics.median(secs):.1f}s · p90 {q(secs,.9):.1f}s · max {max(secs):.1f}s",
       f"- Tokens: {statistics.mean(intok):,.0f} input / {statistics.mean(outtok):,.0f} output per question (haystack ≈ 115k tokens)",
       f"- Tool calls: {statistics.mean(ntools):.1f}/question · date flags used in {flag_q} questions ({len(flag_calls)} calls) · web in {web_q}",
       f"- Evidence surfaced (any evidence session seen): {ret_all['any_seen']}/{ret_all['n']} · all evidence sessions seen: {ret_all['all_seen']}/{ret_all['n']}",
       f"  - passed: {ret_pass['any_seen']}/{ret_pass['n']} · failed: {ret_fail['any_seen']}/{ret_fail['n']}",
       "", "## Failure taxonomy", ""]
md += [f"- {c}: {v}" for c, v in tax.most_common()]
md += ["", "## Accuracy by haystack size", ""] + [f"- {b}: {v}" for b, v in size_acc.items()]
(run / "summary.md").write_text("\n".join(md) + "\n")
print("\n".join(md))
