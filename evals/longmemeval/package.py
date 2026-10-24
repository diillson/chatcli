#!/usr/bin/env python3
"""Package a finished LongMemEval run as a published evidence pack.

usage: package.py RUN_DIR OUT_DIR COMMIT "binary description" [NOTE]

Writes manifest.json, results.jsonl, failure-transcripts.jsonl.gz, the
development/validation split, summary.* (all 500) and summary-validation.*
(the validation half alone) into OUT_DIR, with local paths replaced by
<run>, <scratch> and ~.
"""
import datetime
import gzip
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

here = Path(__file__).resolve().parent
run, out = Path(sys.argv[1]).resolve(), Path(sys.argv[2]).resolve()
commit, binary_desc = sys.argv[3], sys.argv[4]
note = sys.argv[5] if len(sys.argv) > 5 else ""
# The split is fixed once (sha1 of the question id) and the dataset label
# is the published one: both come from the definitive 2026-10-22 pack.
ref = here / "results" / "2026-10-22-merged"
home = str(Path.home())
scratch = os.environ.get("LME_SCRATCH", "/private/tmp/claude-501")


def clean(s: str) -> str:
    s = s.replace(str(run.parent), "<run>").replace(str(run), "<run>")
    s = s.replace(scratch, "<scratch>").replace("/tmp/claude-501", "<scratch>")
    return s.replace(home, "~")


out.mkdir(parents=True, exist_ok=True)
man = json.loads((run / "manifest.json").read_text())
refman = json.loads((ref / "manifest.json").read_text())
man["binary"] = binary_desc
man["dataset"] = refman["dataset"]
man["split"] = refman["split"]
man["finished"] = datetime.datetime.fromtimestamp((run / "results.jsonl").stat().st_mtime).isoformat(timespec="seconds")
man["commit"] = commit
if note:
    man["note"] = note
(out / "manifest.json").write_text(clean(json.dumps(man, indent=2, ensure_ascii=False)) + "\n")

rows = [json.loads(l) for l in (run / "results.jsonl").open()]
assert len(rows) == 500, len(rows)
(out / "results.jsonl").write_text("".join(clean(json.dumps(r, ensure_ascii=False)) + "\n" for r in rows))

failed = [r for r in rows if not r.get("label")]
with gzip.open(out / "failure-transcripts.jsonl.gz", "wt", encoding="utf-8") as f:
    for r in failed:
        rec = run / "runs" / r["question_id"] / "record.json"
        record = json.loads(rec.read_text()) if rec.exists() else None
        f.write(clean(json.dumps({"question_id": r["question_id"], "record": record}, ensure_ascii=False)) + "\n")

shutil.copy(ref / "split.json", out / "split.json")
analyze = here / "analyze.py"
subprocess.run([sys.executable, str(analyze), str(out)], check=True)

split = json.loads((ref / "split.json").read_text())
val = split.get("validation") or [q for q, h in split.items() if h == "validation"]
with tempfile.TemporaryDirectory() as td:
    t = Path(td)
    shutil.copy(out / "manifest.json", t / "manifest.json")
    (t / "results.jsonl").write_text("".join(json.dumps(r, ensure_ascii=False) + "\n" for r in rows if r["question_id"] in set(val)))
    subprocess.run([sys.executable, str(analyze), str(t)], check=True)
    shutil.copy(t / "summary.json", out / "summary-validation.json")
    shutil.copy(t / "summary.md", out / "summary-validation.md")
print(len(rows), "rows,", len(failed), "failures,", len(val), "validation")
