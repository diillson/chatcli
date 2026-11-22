#!/usr/bin/env python3
"""Compare a LongMemEval run with an earlier published pack: the regression
check of the monthly run.

usage: compare.py NEW BASE [--write] [--title TEXT]

NEW and BASE are directories holding results.jsonl (a run directory or a
pack under results/). Prints Markdown: totals with confidence intervals for
all 500 and for each half of the fixed split, questions gained and lost with
an exact two-sided sign test, accuracy by type, per-question cost and tool
use, run health, and the ids that flipped. --write also saves it as
NEW/comparison.md. --title replaces the heading, for a comparison that is
not a regression check (two agents on the same questions).

A single run moves by about four questions with the same binary, so a
difference counts only when it is larger than that and the sign test agrees.
"""
import json
import statistics
import sys
from collections import Counter
from math import comb, sqrt
from pathlib import Path

here = Path(__file__).resolve().parent
args = [a for a in sys.argv[1:] if a != "--write"]
title = ""
if "--title" in args:
    i = args.index("--title")
    title = args[i + 1]
    del args[i:i + 2]
if len(args) != 2:
    sys.exit(__doc__)
new_dir, base_dir = Path(args[0]), Path(args[1])
# The split is fixed once (sha1 of the question id), as in package.py.
split = json.loads((here / "results" / "2026-10-22-merged" / "split.json").read_text())


def load(d):
    return {r["question_id"]: r for r in map(json.loads, (d / "results.jsonl").open())}


def wilson(k, n, z=1.96):
    p = k / n
    d = 1 + z * z / n
    c = p + z * z / (2 * n)
    m = z * sqrt(p * (1 - p) / n + z * z / (4 * n * n))
    return 100 * (c - m) / d, 100 * (c + m) / d


def sign_test(gained, lost):
    n, k = gained + lost, min(gained, lost)
    if n == 0:
        return 1.0
    return min(1.0, 2 * sum(comb(n, i) for i in range(k + 1)) / 2 ** n)


def fmt_p(p):
    return f"{p:.3f}" if p < 0.01 else f"{p:.2f}"


new, base = load(new_dir), load(base_dir)
if set(new) != set(base):
    sys.exit(f"the two runs do not hold the same questions ({len(new)} and {len(base)})")
ids = sorted(new)
out = []


def score(rows, sel):
    k = sum(bool(rows[i]["label"]) for i in sel)
    lo, hi = wilson(k, len(sel))
    return f"{k}/{len(sel)} = {100 * k / len(sel):.1f}% ({lo:.1f}–{hi:.1f}%)"


def flips(sel):
    gained = [i for i in sel if new[i]["label"] and not base[i]["label"]]
    lost = [i for i in sel if base[i]["label"] and not new[i]["label"]]
    return gained, lost


out.append(f"# {title or f'LongMemEval regression check: `{new_dir.name}` against `{base_dir.name}`'}\n")
out.append("| | Base | New | Gained | Lost | Sign test p |")
out.append("|---|---|---|---|---|---|")
for name, sel in (("All questions", ids),
                  ("Development half", [i for i in ids if split[i] == "development"]),
                  ("Validation half", [i for i in ids if split[i] == "validation"])):
    g, l = flips(sel)
    out.append(f"| {name} | {score(base, sel)} | {score(new, sel)} | {len(g)} | {len(l)} | {fmt_p(sign_test(len(g), len(l)))} |")

out.append("\n| Type | Base | New | of |")
out.append("|---|---|---|---|")
types = sorted({r["question_type"] for r in new.values()})
avg = {"base": [], "new": []}
for t in types:
    sel = [i for i in ids if new[i]["question_type"] == t]
    b, n = (sum(bool(rows[i]["label"]) for i in sel) for rows in (base, new))
    avg["base"].append(b / len(sel))
    avg["new"].append(n / len(sel))
    out.append(f"| {t} | {b} | {n} | {len(sel)} |")
sel = [i for i in ids if new[i]["abstention"]]
out.append(f"| abstention (across types) | {sum(bool(base[i]['label']) for i in sel)} | {sum(bool(new[i]['label']) for i in sel)} | {len(sel)} |")
out.append(f"| task-averaged accuracy | {100 * statistics.mean(avg['base']):.1f}% | {100 * statistics.mean(avg['new']):.1f}% | |")

out.append("\n| Per question | Base | New |")
out.append("|---|---|---|")
measures = (
    ("Cost, answer + judge (US$)", lambda r: r["cost_answer"] + r["cost_judge"], "{:.3f}"),
    ("Input tokens", lambda r: r["in_tok"] or 0, "{:,.0f}"),
    ("Output tokens", lambda r: r["out_tok"] or 0, "{:,.0f}"),
    ("Tool calls", lambda r: len(r["tools"]), "{:.1f}"),
)
for label, f, fmt in measures:
    out.append(f"| {label} | {fmt.format(statistics.mean(map(f, base.values())))} | {fmt.format(statistics.mean(map(f, new.values())))} |")
out.append(f"| Median seconds | {statistics.median(r['secs'] for r in base.values()):.1f} | {statistics.median(r['secs'] for r in new.values()):.1f} |")


def seen(rows):
    answerable = [r for r in rows.values() if not r["abstention"]]
    return f"{sum(1 for r in answerable if r['evidence_seen'])}/{len(answerable)}"


def health(rows):
    exits = ", ".join(f"exit {k}: {v}" for k, v in sorted(Counter(str(r["exit"]) for r in rows.values()).items()))
    return f"{exits}; errors {sum(1 for r in rows.values() if r['err'])}; empty answers {sum(1 for r in rows.values() if not r['hypothesis'])}"


out.append(f"| Evidence surfaced | {seen(base)} | {seen(new)} |")
out.append(f"| Run health | {health(base)} | {health(new)} |")

gained, lost = flips(ids)
for title, sel in (("Lost (passed in the base, failed in the new run)", lost),
                   ("Gained (failed in the base, passed in the new run)", gained)):
    out.append(f"\n## {title}: {len(sel)}\n")
    if sel:
        out.append("| Question | Half | Type | Evidence sessions seen |")
        out.append("|---|---|---|---|")
    for i in sorted(sel, key=lambda i: (split[i], new[i]["question_type"], i)):
        r = new[i]
        out.append(f"| `{i}` | {split[i]} | {r['question_type']} | {len(r['evidence_seen'])}/{len(r['evidence_sessions'])} |")

text = "\n".join(out) + "\n"
print(text, end="")
if "--write" in sys.argv[1:]:
    (new_dir / "comparison.md").write_text(text)
