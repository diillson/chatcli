# LongMemEval-V2 for chatcli

[LongMemEval-V2](https://github.com/xiaowu0162/LongMemEval-V2) (Wu et al., 2026) asks whether a memory system can give an agent the experience of a colleague who has worked in a customized web environment for a while. The history is up to 500 multimodal web-agent trajectories (page text plus screenshots); a memory system reads it and returns compact evidence; a fixed reader answers from that evidence; 451 hand-written questions cover five abilities: static state recall, dynamic state tracking, workflow knowledge, environment gotchas and premise awareness (abstention). The benchmark reports accuracy and the latency of the memory query.

This directory runs chatcli through the benchmark's **own, unmodified scripts** and records what it scored. It is the second benchmark after [LongMemEval](../longmemeval).

## How chatcli is plugged in

The benchmark's `codex` baseline starts a coding agent in a sandbox that holds the haystack's `trajectories/` and expects it to leave `memory_module_output.json` (a short analysis plus the spans of states that matter). It takes the agent as a parameter (`CODEX_BINARY`). Two small files here make chatcli that agent and the reader and the judge, so nothing in the benchmark's repository changes:

| File | What it does |
|---|---|
| `chatcli_codex_compat.py` | Accepts the `codex exec` command line the baseline uses and runs `chatcli -p "/coder …"` in the sandbox, on its own empty `CHATCLI_STATE_DIR`, then returns the agent's final message and the token usage and cost from chatcli's run record. |
| `claude_bridge.py` | A local OpenAI-compatible `/v1/chat/completions` that answers the benchmark's reader and judge calls through chatcli's chat mode (images become `@file` attachments). The Anthropic login stays inside chatcli. |
| `env.example.sh` | The environment that points the benchmark's `run_codex.sh` at both. |

chatcli itself was not changed or tuned for this run: the binary is `main` at `6ece9b07`.

## Result (2026-10-22, tier small, 451 questions)

| | Accuracy |
|---|---|
| **All 451 questions** | **81.8%** (369 correct) |
| Web (240) | 82.9% |
| Enterprise (211) | 80.6% |
| Answerable questions (323) | 89.5% |
| **Abstention questions (128)** | **62.5%** |

By ability (answerable questions only, as the benchmark's metric files report them):

| Ability | Questions | Accuracy |
|---|---|---|
| Static state recall | 134 | 94.8% |
| Dynamic state tracking | 86 | 90.7% |
| Workflow (procedure) | 74 | 87.8% |
| Environment gotchas | 29 | **65.5%** |

Abstention questions, by what they ask about: static 70.9% (55), dynamic 68.3% (41), procedure **40.6%** (32). Counting them together with the answerable ones, the three abilities score 87.8% (static), 83.5% (dynamic) and 73.6% (procedure).

By environment: webarena-cms 89.2% (83), webarena-reddit 82.4% (91), workarena 80.6% (211), webarena-onestopshop 75.8% (66).

### Cost and speed

| | |
|---|---|
| Memory query (chatcli agent), average | **211.7 s** (web 194 s, enterprise 232 s); longest 1,285 s |
| Memory query attempts | 484 for 451 questions: 451 finished, 31 left no output file, 2 left a malformed one; the benchmark's retry recovered all of them, so **no question ended with an empty memory** |
| Evidence handed to the reader | 47k tokens on average; 8 questions hit the 200k-token cap |
| Cost, memory agent | US$125.06 (US$0.28 per question) |
| Cost, reader and judge | US$67.49 |
| Whole run | about US$192.55 and about 5 hours, six memory queries in parallel |

## Read this before comparing it with anything

**This number is not comparable with the benchmark's published results or its leaderboard.** The leaderboard fixes the reader (Qwen3.5-9B) and the judge (gpt-5.2) so that only the memory system varies, and its packaging step refuses runs that used anything else. Here:

- the **reader and the judge are Claude Sonnet 5.5**, far stronger than a 9B reader, so part of the score is the reader's;
- the **memory agent is Claude Sonnet 5.5** too (the published baselines use gpt-5.4-mini);
- the judge is only used for the 156 abstention and gotchas questions; the other 295 are scored by rule;
- the reader's sampling parameters are ignored by the bridge, and chatcli's own system prompt sits in front of the benchmark's.

For context only, the published small-tier results are: Codex 69.9% at 177 s, AgentRunbook-C 74.9% at 108 s and, on the page of its V2, 75.6% at 131 s. The leaderboard's own score, LAFS gain, counts latencies only up to 200 s; chatcli's average is 211.7 s, so its LAFS gain is **0.0** whatever the accuracy.

What the run does show, independently of the reader:

- **Gotchas and abstention are where it fails.** Of the 48 abstention questions chatcli's reader answered instead of declining, the memory agent's analysis flagged a false premise or missing evidence in only 3; among the 80 it got right, in 35. When the memory agent does not say "this does not exist", the reader answers.
- **The agent sometimes finishes without writing its result** (31 of 484 attempts, 6%), which costs a retry.
- **It is slow and not cheap**: about 3.5 minutes and US$0.28 per question, because it explores 100 trajectories of page text per question with shell scripts.

## Caveats

- One run; the model varies between runs.
- The memory agent runs with chatcli's automatic approval inside a throwaway state directory that holds only the benchmark's trajectories, the equivalent of the baseline's `--dangerously-bypass-approvals-and-sandbox`; chatcli's own guard against destructive commands stays on.
- The dataset's `README.md` differs from its published checksum; the data files, the screenshot archives and the haystacks match.
- Benchmark checkout `2cc8c54`; dataset downloaded with the benchmark's `download_data.py` (no revision pinned).

## Files

| File | What it holds |
|---|---|
| `results/2026-10-22/combined_metrics.json` | the benchmark's own combined metrics for web and enterprise |
| `results/2026-10-22/{web,enterprise}_aggregated_metrics.json` | per-domain metrics as the harness wrote them |
| `results/2026-10-22/per_question_compact.jsonl` | one line per question: ids, gold answer, chatcli's answer, verdict, memory latency and context size, and the memory agent's analysis (first 1,500 characters) |

## Reproduce

```bash
git clone https://github.com/xiaowu0162/LongMemEval-V2 && cd LongMemEval-V2
pip install -r requirements.txt torch torchvision && pip install -e .
python data/download_data.py --data-root data/longmemeval-v2
export DATA_ROOT="$PWD/data/longmemeval-v2"
python data/prepare_data.py --data-root "$DATA_ROOT" --mode symlink
python data/validate_data.py --data-root "$DATA_ROOT" --tier small

CHATCLI_BIN=/path/to/chatcli python3 /path/to/evals/longmemeval-v2/claude_bridge.py --port 8023 --workers 8 &
source /path/to/evals/longmemeval-v2/env.example.sh   # edit the paths first
evaluation/scripts/run_codex.sh --prompt-build-max-workers 6
python leaderboard/combine_aggregated_metrics.py runs/codex_enterprise_small/aggregated_metrics.json \
  runs/codex_web_small/aggregated_metrics.json -o runs/chatcli_small_combined_metrics.json
```

Requirements: a Claude login in `~/.chatcli`, Python 3.12, about 15 GB of disk and several hours.
