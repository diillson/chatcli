# LongMemEval for chatcli

[LongMemEval](https://github.com/xiaowu0162/LongMemEval) (Wu et al., *LongMemEval: Benchmarking Chat Assistants on Long-Term Interactive Memory*, ICLR 2025) asks 500 questions about a user's past conversations. Each question comes with its own timestamped chat history; LongMemEval-S gives about 115k tokens of history per question. The questions cover five memory abilities: information extraction, multi-session reasoning, temporal reasoning, knowledge updates and abstention.

This directory runs chatcli against it the way a user meets it: the history is stored as **saved sessions**, and chatcli has to find the evidence through its own recall (`@session search`, `@session get`, `@memory recall`, session auto-recall). Nothing is pasted into the prompt.

## Result (2026-10-20, LongMemEval-S, 500 questions)

| | |
|---|---|
| Accuracy | **454/500 = 90.8%** (95% CI 87.9–93.0%) |
| Task-averaged accuracy | **92.2%** |
| Held-out (the 440 questions not used while fixing chatcli) | 396/440 = 90.0% (95% CI 86.8–92.5%) |
| Evidence surfaced to the model | 469 of 470 answerable questions |

| Type | Pass | Accuracy |
|---|---|---|
| single-session-user | 70/70 | 100.0% |
| single-session-assistant | 56/56 | 100.0% |
| knowledge-update | 74/78 | 94.9% |
| temporal-reasoning | 117/133 | 88.0% |
| single-session-preference | 26/30 | 86.7% |
| multi-session | 111/133 | 83.5% |
| abstention (unanswerable, across types) | 27/30 | 90.0% |

Full numbers, the failure taxonomy and every answer are in [`results/2026-10-20/`](results/2026-10-20/).

## Setup

- **Answering:** chatcli `-p "/coder …"` one-shot, model `claude-sonnet-5-5`, built from the commit in `manifest.json`.
- **Judging:** the official LongMemEval answer-check prompts, copied verbatim into [`judge_prompts.py`](judge_prompts.py) (MIT), run through chatcli chat mode with `claude-sonnet-5-5`. The paper's reference judge is GPT-4o, so these scores are **not directly comparable** with numbers judged by GPT-4o.
- **Per question:** a fresh `CHATCLI_STATE_DIR` holds the haystack as saved sessions; each file's modification time is the session's date. `CHATCLI_NOW` is the question date, so "two weeks ago" means two weeks before the question. Credentials come from the real store through `CHATCLI_AUTH_DIR`, used in place.
- **Session names** are `chat-<date>-<index>`. The dataset's session ids mark evidence sessions (`answer_…`), so they are never used as names. The date in the name is the same information the search hits show.
- **The question** is wrapped in one neutral frame (see `FRAME` in [`lme.py`](lme.py)): answer from previous conversations, say so when they do not contain the answer, end with a short answer.

## Honest caveats

- **Development overlap.** A 60-question stratified sample (seed 42) was run first. Its failures led to fixes in chatcli: [#1837](https://github.com/diillson/chatcli/pull/1837), [#1838](https://github.com/diillson/chatcli/pull/1838), [#1839](https://github.com/diillson/chatcli/pull/1839) and [#1840](https://github.com/diillson/chatcli/pull/1840). Those 60 scored 96.7% in the final run; the other 440 scored 90.0%. The held-out number is the conservative one.
- **One run per question.** The model varies between runs; a question near the edge can flip.
- **"Evidence surfaced"** means an evidence session's name appeared in what the model saw (search results, auto-recall pointers, pages it read). It does not prove the model read the exact sentence.

## Files

| File | What it holds |
|---|---|
| `results/2026-10-20/manifest.json` | commit, binary and dataset sha256, models, prompt frame, settings, start and end time |
| `results/2026-10-20/results.jsonl` | one line per question: question, gold answer, chatcli's answer, verdict, latency, tokens, cost, tool calls, evidence sessions seen and opened |
| `results/2026-10-20/summary.json` / `summary.md` | aggregates, confidence intervals, tool usage, retrieval, failure taxonomy |
| `results/2026-10-20/failure-transcripts.jsonl.gz` | full chatcli transcripts of the 46 failed questions |
| `lme.py` | the driver |
| `analyze.py` | builds the summary from a run directory |
| `judge_prompts.py` | official LongMemEval judge prompts (MIT) |

Local paths in the published files are replaced with `<run>`, `<scratch>` and `~`.

## Reproduce

```bash
# dataset (MIT): longmemeval_s_cleaned.json
curl -LO https://huggingface.co/datasets/xiaowu0162/longmemeval-cleaned/resolve/main/longmemeval_s_cleaned.json
go build -o chatcli-bench .            # from the commit in manifest.json
: > empty.env                          # keeps your own .env out of the run
python3 -I evals/longmemeval/lme.py --data longmemeval_s_cleaned.json --out run \
  --bin ./chatcli-bench --dotenv empty.env --per-type 1000 --workers 4 < /dev/null
python3 -I evals/longmemeval/analyze.py run
```

Requirements: a Claude login in `~/.chatcli` (OAuth or API key). Concurrent runs are safe since [#1836](https://github.com/diillson/chatcli/pull/1836) serializes OAuth refresh across processes. The full run took about 1 hour with 4 workers and cost $41.39 answering plus $1.89 judging.
