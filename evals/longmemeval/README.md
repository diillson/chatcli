# LongMemEval for chatcli

[LongMemEval](https://github.com/xiaowu0162/LongMemEval) (Wu et al., *LongMemEval: Benchmarking Chat Assistants on Long-Term Interactive Memory*, ICLR 2025) asks 500 questions about a user's past conversations. Each question comes with its own timestamped chat history; LongMemEval-S gives about 115k tokens of history per question. The questions cover five memory abilities: information extraction, multi-session reasoning, temporal reasoning, knowledge updates and abstention.

This directory runs chatcli against it the way a user meets it: the history is stored as **saved sessions**, and chatcli has to find the evidence through its own recall (`@session search`, `@session get`, `@memory recall`, session auto-recall). Nothing is pasted into the prompt.

## Results

Two full runs, same dataset, same judge prompts, same answering model (`claude-sonnet-5-5`).

| | 2026-10-20 | 2026-10-22 |
|---|---|---|
| Accuracy, 500 questions | 454/500 = 90.8% (95% CI 87.9–93.0%) | **472/500 = 94.4%** (92.0–96.1%) |
| Task-averaged accuracy | 92.2% | 95.4% |
| Questions never read before the run | 440 held out: 396/440 = 90.0% | **260 validation: 240/260 = 92.3%** (88.4–95.0%) |
| Evidence surfaced to the model | 469 of 470 | 470 of 470 |
| Cost per question (answer + judge) | US$0.087 | US$0.100 |
| Input tokens per question | 71.3k | 77.0k |

**Read the validation row, not the 500 row.** Run-to-run noise is real: 53 of the 500 questions flipped at least once across three full runs (454, 449 and 451 correct), and rerunning the 21 that regressed in one of them with the same binary made 15 pass. One run moves by about ±4 questions. The 2026-10-22 changes were designed after reading the failures of 240 questions (the *development* half); the other 260 (the *validation* half) were not looked at until the run finished. The development half scored 232/240 = 96.7%, which is optimistic by construction. The validation half is the number to quote, and its 95% interval (88.4–95.0%) still reaches below 92%.

| Type | 2026-10-20 | 2026-10-22 | of which validation |
|---|---|---|---|
| single-session-user | 70/70 | 70/70 | 34/34 |
| single-session-assistant | 56/56 | 55/56 | 27/28 |
| knowledge-update | 74/78 | 77/78 | 39/40 |
| temporal-reasoning | 117/133 | 124/133 | 63/67 |
| single-session-preference | 26/30 | 28/30 | 16/18 |
| multi-session | 111/133 | 118/133 | 61/73 |
| abstention (unanswerable, across types) | 27/30 | 26/30 | 10/13 |

What changed between the runs, in chatcli: session and memory results are no longer compressed on their way to the model ([#1869](https://github.com/diillson/chatcli/pull/1869); about one question in seven had the middle of a session replaced by a lines-omitted marker); session ages count calendar days; session search lists sessions that hold only some of a long query's terms after the full matches; the user's own turns take snippet slots; a new `@date` builtin does calendar arithmetic; and the memory card teaches a completeness protocol ([#1871](https://github.com/diillson/chatcli/pull/1871)).

The 2026-10-20 numbers, failure taxonomy and every answer stay in [`results/2026-10-20/`](results/2026-10-20/); the new ones are in [`results/2026-10-22/`](results/2026-10-22/) (`split.json` says which half each question belongs to; `summary-validation.md` scores the validation half alone).

## Setup

- **Answering:** chatcli `-p "/coder …"` one-shot, model `claude-sonnet-5-5`, built from the commit in `manifest.json`.
- **Judging:** the official LongMemEval answer-check prompts, copied verbatim into [`judge_prompts.py`](judge_prompts.py) (MIT), run through chatcli chat mode with `claude-sonnet-5-5`. The paper's reference judge is GPT-4o, so these scores are **not directly comparable** with numbers judged by GPT-4o.
- **Per question:** a fresh `CHATCLI_STATE_DIR` holds the haystack as saved sessions; each file's modification time is the session's date. `CHATCLI_NOW` is the question date, so "two weeks ago" means two weeks before the question. Credentials come from the real store through `CHATCLI_AUTH_DIR`, used in place.
- **Session names** are `chat-<date>-<index>`. The dataset's session ids mark evidence sessions (`answer_…`), so they are never used as names. The date in the name is the same information the search hits show.
- **The question** is wrapped in one neutral frame (see `FRAME` in [`lme.py`](lme.py)): answer from previous conversations, say so when they do not contain the answer, end with a short answer.

## Honest caveats

- **Development overlap.** In the first round a 60-question stratified sample (seed 42) was run and its failures led to fixes ([#1837](https://github.com/diillson/chatcli/pull/1837), [#1838](https://github.com/diillson/chatcli/pull/1838), [#1839](https://github.com/diillson/chatcli/pull/1839), [#1840](https://github.com/diillson/chatcli/pull/1840)); those 60 scored 96.7% and the other 440 scored 90.0%. In the second round the split is fixed up front by `sha1(question_id)` (even = development, odd = validation) and only development failures were read. The validation number is the conservative one.
- **Some questions are debatable.** Reading the failures turned up questions whose reference answer follows an arbitrary reading (a count that depends on which of two events "led" counts, a date that matches no stated date). They are left as they are and count as misses.
- **One run per question.** The model varies between runs; a question near the edge can flip (53 of the 500 flipped at least once across the first three runs).
- **"Evidence surfaced"** means an evidence session's name appeared in what the model saw (search results, auto-recall pointers, pages it read). It does not prove the model read the exact sentence.

## Files

| File | What it holds |
|---|---|
| `results/<run>/manifest.json` | commit, binary and dataset sha256, models, prompt frame, settings, start and end time |
| `results/<run>/results.jsonl` | one line per question: question, gold answer, chatcli's answer, verdict, latency, tokens, cost, tool calls, evidence sessions seen and opened |
| `results/<run>/summary.json` / `summary.md` | aggregates, confidence intervals, tool usage, retrieval, failure taxonomy |
| `results/<run>/failure-transcripts.jsonl.gz` | full chatcli transcripts of the failed questions (46 in 2026-10-20, 28 in 2026-10-22) |
| `results/2026-10-22/split.json` | development or validation, per question |
| `results/2026-10-22/summary-validation.*` | the validation half scored alone |
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
python3 -I evals/longmemeval/lme.py --data "$PWD/longmemeval_s_cleaned.json" --out "$PWD/run" \
  --bin "$PWD/chatcli-bench" --dotenv "$PWD/empty.env" --per-type 1000 --workers 4 < /dev/null
python3 -I evals/longmemeval/analyze.py run
```

Requirements: a Claude login in `~/.chatcli` (OAuth or API key). Concurrent runs are safe since [#1836](https://github.com/diillson/chatcli/pull/1836) serializes OAuth refresh across processes. Use absolute paths: the driver changes directory per question. The full 2026-10-20 run took about 1 hour with 4 workers and cost $41.39 answering plus $1.89 judging; 2026-10-22 cost $48.28 plus $1.86. To run only some questions pass `--only id1,id2,...` (the 2026-10-22 run was two `--only` passes, one per half). `LME_HOME` can point the child's HOME elsewhere, but chatcli then cannot reach the login keychain that holds the credential key and refuses to start, so a real run keeps the real HOME and isolates everything else through `CHATCLI_STATE_DIR`.
