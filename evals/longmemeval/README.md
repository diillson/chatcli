# LongMemEval for chatcli

[LongMemEval](https://github.com/xiaowu0162/LongMemEval) (Wu et al., *LongMemEval: Benchmarking Chat Assistants on Long-Term Interactive Memory*, ICLR 2025) asks 500 questions about a user's past conversations. Each question comes with its own timestamped chat history; LongMemEval-S gives about 115k tokens of history per question. The questions cover five memory abilities: information extraction, multi-session reasoning, temporal reasoning, knowledge updates and abstention.

This directory runs chatcli against it the way a user meets it: the history is stored as **saved sessions**, and chatcli has to find the evidence through its own recall (`@session search`, `@session get`, `@memory recall`, session auto-recall). Nothing is pasted into the prompt.

## Results

Six full runs, same dataset, same judge prompts, same answering model (`claude-sonnet-5-5`). The last one is the [monthly check](#monthly-check) on the v1.232.0 release; the one before it, with the session search of [#1887](https://github.com/diillson/chatcli/pull/1887), is the last run that came with a change aimed at the benchmark.

| | 2026-10-22 on merged main | 2026-10-24 (main + memory graph) | 2026-10-24 + session search | **2026-11-22, v1.232.0** |
|---|---|---|---|---|
| Accuracy, 500 questions | 473 = 94.6% (92.3–96.3%) | 475 = 95.0% (92.7–96.6%) | 479 = 95.8% (93.7–97.2%) | **481 = 96.2%** (94.1–97.6%) |
| Task-averaged accuracy | 95.9% | 94.9% | 96.3% | 96.5% |
| **Validation half, 260** | 243 = 93.5% (89.8–95.9%) | 240 = 92.3% (88.4–95.0%) | 247 = 95.0% (91.6–97.1%) | **246 = 94.6%** (91.2–96.8%) |
| Validation without the 3 questions read on 2026-10-24 | 240/257 | 240/257 | 245/257 | 244/257 |
| Evidence surfaced to the model | 470 of 470 | 469 of 470 | 470 of 470 | 470 of 470 |
| Abstention (unanswerable, 30) | 25 | 28 | 28 | 29 |
| Cost per question (answer + judge) | US$0.101 | US$0.099 | US$0.100 | US$0.102 |
| Input tokens per question | 77.6k | 77.0k | 78.0k | 78.6k |
| Tool calls per question | 5.0 | 4.7 | 3.4 | 3.5 |

**Read the validation row.** Changes are designed only from the failures of the *development* half (240 questions, `sha1(question_id)` even); the *validation* half (260, odd) is not read. One exception, stated in the manifests: three validation failures of the 2026-10-24 base run (`07741c45`, `6a1eabeb`, `830ce83f`) were read while checking that run for regressions; no change was designed from them, and the row above scores the validation half without them too.

What the numbers say:

- **v1.232.0 is level with October.** Against the 2026-10-24 session-search run it gained 10 questions and lost 8 (sign test p = 0.81); on the validation half it gained 5 and lost 6 (p = 1.00). Both are inside the ±4 questions a single run moves by, so this is neither a regression nor an improvement. The evidence session reached the model in all 470 answerable questions, and cost and tool calls per question did not move. The 8 lost questions were rerun once with the same binary and 5 passed; the 3 that failed twice (`6d550036`, `gpt4_59149c78`, `gpt4_ab202e7f`) had each failed in 3 of the 5 earlier packs.
- **The memory graph ([#1877](https://github.com/diillson/chatcli/pull/1877)–[#1883](https://github.com/diillson/chatcli/pull/1883)) did not move recall.** Against 2026-10-22 it gained 13 questions and lost 11 (sign test p = 0.84). It was a regression check, not an improvement claim.
- **The session search is a gain on questions nobody read.** Against the 2026-10-24 base it went from 240 to 247 on the validation half (9 gained, 2 lost, p = 0.065), and from 475 to 479 on all 500 (11 gained, 7 lost, p = 0.48). The gain on validation is larger than the ±4 questions a single run moves by, but the confidence intervals still overlap.
- **It costs nothing extra.** Tool calls per question fell from 4.7 to 3.4, because one search now carries several phrasings. Cost per question stayed at US$0.100. The model used `queries` in all 500 questions.
- **The development half lost 3 (235 to 232).** All five development questions that flipped to a fail had their evidence session in front of the model. The misses are reasoning or readings the reference answer does not share: whether "got" means ordered or delivered, and whether booked flights count as flown. No retrieval regression.

| Type | 2026-10-22 merged | 2026-10-24 | 2026-10-24 + search | 2026-11-22, v1.232.0 | of which validation |
|---|---|---|---|---|---|
| single-session-user | 70/70 | 69/70 | 70/70 | 70/70 | 34/34 |
| single-session-assistant | 55/56 | 55/56 | 56/56 | 55/56 | 27/28 |
| knowledge-update | 77/78 | 74/78 | 76/78 | 77/78 | 39/40 |
| temporal-reasoning | 123/133 | 127/133 | 127/133 | 127/133 | 63/67 |
| single-session-preference | 29/30 | 27/30 | 28/30 | 28/30 | 16/18 |
| multi-session | 119/133 | 123/133 | 122/133 | 124/133 | 67/73 |
| abstention (unanswerable, across types) | 25/30 | 28/30 | 28/30 | 29/30 | 12/13 |

The validation column is the 2026-11-22 run.

**Why phrasings.** The user said things in their own words, not the question's: in the multi-session questions, 45 of 309 evidence sessions (15%) share no word with the question. Before this change the model found them by searching again with other words (3.3 searches per question for those, against 2.7). `@session search` now takes `queries`, several phrasings in one call fused by reciprocal rank. With an embedding provider configured, it also adds up to three sessions related by meaning. The benchmark runs with no embedding provider, so the result above comes from the phrasings alone.

## Monthly check

chatcli changes every week, and a change to the prompt, the tools or the search can cost recall without any test failing. So the full benchmark is run again once a month on the latest release, with the same dataset, judge prompts, answering model, driver and split. The question it answers is "did we get worse?", not "did we get better?".

1. Build the release tag and run all 500 questions (`lme.py`, see [Reproduce](#reproduce)).
2. Compare with the previous pack: `compare.py <run> results/<previous>`. It prints the totals for all 500 and for each half, the questions gained and lost with an exact sign test, accuracy by type, cost and tool calls per question, and the ids that flipped.
3. Read it against the noise. A single run moves by about ±4 questions with the same binary. A loss larger than that, with a sign test that agrees, is a regression to investigate; anything smaller is level.
4. Rerun the lost questions once with the same binary (`--only id1,id2,...`). A question that passes on the rerun was run-to-run variation.
5. Only if a regression holds: read the failures of the **development** half, fix the cause in chatcli, and prove the fix on the validation half. Validation failures are not read.
6. Publish the pack with `package.py`, whatever the result.

| Month | chatcli | All 500 | Validation, 260 | Against the previous run | Verdict |
|---|---|---|---|---|---|
| 2026-11 | v1.232.0 (`7f53b9ae`) | 481 = 96.2% | 246 = 94.6% | 10 gained, 8 lost (p = 0.81) | level |

Across the six published packs, 434 of the 500 questions were answered correctly every time, 59 changed between runs (product fixes in October, and run-to-run variation), and 7 were wrong every time.

### Earlier runs

| | 2026-10-20 (base) | 2026-10-22 (changes, before merge) | 2026-10-22 on merged main |
|---|---|---|---|
| Accuracy, 500 questions | 454 = 90.8% (87.9–93.0%) | 472 = 94.4% (92.0–96.1%) | 473 = 94.6% (92.3–96.3%) |
| Validation half, 260 | 237 = 91.2% | 240 = 92.3% (88.4–95.0%) | 243 = 93.5% (89.8–95.9%) |

Against the base run the 2026-10-22 changes gained 27 questions and lost 8 (p = 0.002). Across the three runs before those changes (454, 449 and 451 correct) 53 of the 500 questions flipped at least once, and rerunning the 21 that regressed in one of them with the same binary made 15 pass: a single run moves by about ±4 questions. What changed then, in chatcli: session and memory results are no longer compressed on their way to the model ([#1869](https://github.com/diillson/chatcli/pull/1869)); a park token prefix is no longer read as a number ([#1870](https://github.com/diillson/chatcli/pull/1870)); session ages count calendar days; session search lists partial matches after the full ones; the user's own turns take snippet slots; a new `@date` builtin does calendar arithmetic; and the memory card teaches a completeness protocol ([#1871](https://github.com/diillson/chatcli/pull/1871)). Abstention went from 27 of 30 to 25 and cost per question rose about 16% with those changes.

All packs are in [`results/`](results/): `2026-10-20/`, `2026-10-22/`, `2026-10-22-merged/`, `2026-10-24/`, `2026-10-24-session-search/` and `2026-11-22/` (latest). In each 2026-10-22 and later pack, `split.json` says which half each question belongs to and `summary-validation.md` scores the validation half alone.

## Same model, another harness: Claude Code

On 2026-11-22 the same 500 questions were also run through **Claude Code 2.1.330**, with the same answering model (`claude-sonnet-5-5`), the same judge and the same dataset, on the same day as the v1.232.0 run. The model is identical on both sides; what differs is the agent around it and what it puts in front of the model.

| | Claude Code 2.1.330 | chatcli v1.232.0 |
|---|---|---|
| Accuracy, 500 questions | 463 = 92.6% (90.0–94.6%) | **481 = 96.2%** (94.1–97.6%) |
| Validation half, 260 | 238 = 91.5% (87.5–94.3%) | **246 = 94.6%** (91.2–96.8%) |
| Task-averaged accuracy | 93.9% | 96.5% |
| Every evidence session surfaced | 452 of 470 | 462 of 470 |
| Abstention (unanswerable, 30) | 29 | 29 |
| Cost per question (answer + judge) | **US$0.046** | US$0.102 |
| Median time per question | **8.1 s** | 19.1 s |
| Input tokens per question | 60.3k | 78.6k |
| Tool calls per question | 2.0 | 3.5 |

| Type | Claude Code | chatcli |
|---|---|---|
| single-session-user | 69/70 | 70/70 |
| single-session-assistant | 56/56 | 55/56 |
| single-session-preference | 28/30 | 28/30 |
| knowledge-update | 73/78 | 77/78 |
| temporal-reasoning | 120/133 | 127/133 |
| multi-session | 117/133 | 124/133 |

- **Question by question:** chatcli answered 25 questions that Claude Code missed, and Claude Code answered 7 that chatcli missed (sign test p = 0.002). Both were right on 456 and both wrong on 12.
- **Where the gap is:** the three single-session types are level (153 of 156 each). The difference is in knowledge updates, temporal reasoning and multi-session questions, the ones that need every mention found and read.
- **How Claude Code searched:** 885 `Grep` calls, 78 `Bash`, 59 `Read`. It opened a session file with `Read` in 57 of the 500 questions and answered the rest from the matching lines. Of its 37 misses, 25 are reasoning with the evidence surfaced, 5 said the evidence was not there, 5 found only part of it, 1 never found it and 1 answered an unanswerable question.
- **Claude Code is cheaper and faster:** less than half the cost and the time per question.

Read it with these in mind:

- **chatcli was tuned on this benchmark and Claude Code was not.** The development half guided chatcli's changes, so the validation half is the fairer comparison: 246 against 238, 13 questions to 5 (p = 0.10), the same direction but not significant on its own.
- **Claude Code is not a conversation-memory product.** It has no store of past conversations to search, so each question's history was written as plain transcript files in `sessions/` of an empty working directory, and the prompt says where they are. chatcli read its own saved sessions.
- **The question date** comes from `CHATCLI_NOW` in chatcli; for Claude Code it is stated in the prompt.
- **Claude Code ran clean:** `--safe-mode`, no user settings, no CLAUDE.md, plugins or MCP servers of the machine, no web tools, no saved session.
- **One run each.** A run moves by about ±4 questions.

The pack is in [`results/2026-11-22-claude-code/`](results/2026-11-22-claude-code/), with `comparison.md` (question by question against the chatcli run) and the driver is [`lme_claude.py`](lme_claude.py).

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
| `results/<run>/failure-transcripts.jsonl.gz` | full chatcli transcripts of the failed questions (46 in 2026-10-20, 28 in 2026-10-22, 27 in 2026-10-22-merged, 25 in 2026-10-24, 21 in 2026-10-24-session-search, 19 in 2026-11-22) |
| `results/<run>/split.json` | development or validation, per question (2026-10-22 and later) |
| `results/<run>/summary-validation.*` | the validation half scored alone (2026-10-22 and later) |
| `results/<run>/comparison.md` | the regression check against the previous pack, written by `compare.py --write` (2026-11-22 and later) |
| `results/<run>/rerun-regressed.jsonl` | the rerun of the questions that passed in the previous pack and failed in this one, same binary (2026-11-22 and later) |
| `lme.py` | the driver |
| `lme_claude.py` | the same run through Claude Code: history as transcript files, same frame, same judge |
| `analyze.py` | builds the summary from a run directory |
| `compare.py` | compares a run with an earlier pack: gained, lost, sign test, cost and tools per question |
| `package.py` | turns a finished run into a published pack (manifest, results, failure transcripts, split, both summaries, local paths replaced) |
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
# the same questions through Claude Code (needs a Claude Code login; the chatcli binary only runs the judge):
python3 -I evals/longmemeval/lme_claude.py --data "$PWD/longmemeval_s_cleaned.json" --out "$PWD/claude-run" \
  --bin "$PWD/chatcli-bench" --dotenv "$PWD/empty.env" --per-type 1000 --workers 4 < /dev/null
# regression check against the previous pack:
python3 -I evals/longmemeval/compare.py "$PWD/run" evals/longmemeval/results/2026-11-22
# or, for a publishable pack:
python3 -I evals/longmemeval/package.py "$PWD/run" "$PWD/evals/longmemeval/results/<date>" <commit> "chatcli built from <commit>"
```

Requirements: a Claude login in `~/.chatcli` (OAuth or API key). Concurrent runs are safe since [#1836](https://github.com/diillson/chatcli/pull/1836) serializes OAuth refresh across processes. Use absolute paths: the driver changes directory per question. The full 2026-10-20 run took about 1 hour with 4 workers and cost $41.39 answering plus $1.89 judging; 2026-10-22 on merged main cost $48.47 plus $1.94, 2026-10-24 with the session search $48.37 plus $1.83 (about 55 minutes), and 2026-11-22 on v1.232.0 $48.92 plus $1.84 (48 minutes). The Claude Code comparison of 2026-11-22 cost $21.33 plus $1.71 and took 26 minutes. To run only some questions pass `--only id1,id2,...` (the 2026-10-22 run was two `--only` passes, one per half). `LME_HOME` can point the child's HOME elsewhere, but chatcli then cannot reach the login keychain that holds the credential key and refuses to start, so a real run keeps the real HOME and isolates everything else through `CHATCLI_STATE_DIR`.
