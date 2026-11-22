# Claude Code 2.1.330 on LongMemEval-S (500 questions)

- Answer model: `claude-sonnet-5-5` (headless, claude -p) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Agent `Claude Code 2.1.330` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-11-22T19:56:08

**Accuracy: 463/500 = 92.6%** (95% CI 90.0%–94.6%) · task-averaged 93.9%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 73/78 | 93.6% | 85.9%–97.2% |
| multi-session | 117/133 | 88.0% | 81.4%–92.5% |
| single-session-assistant | 56/56 | 100.0% | 93.6%–100.0% |
| single-session-preference | 28/30 | 93.3% | 78.7%–98.2% |
| single-session-user | 69/70 | 98.6% | 92.3%–99.7% |
| temporal-reasoning | 120/133 | 90.2% | 84.0%–94.2% |

- Abstention (unanswerable): 29/30
- Cost: $21.33 answering + $1.71 judging · $0.043/question
- Latency: median 8.1s · p90 14.9s · max 36.3s
- Tokens: 60,349 input / 616 output per question (haystack ≈ 115k tokens)
- Tool calls: 2.0/question · date flags used in 0 questions (0 calls) · web in 0
- Evidence surfaced (any evidence session seen): 469/470 · all evidence sessions seen: 452/470
  - passed: 434/434 · failed: 35/36

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 25
- evidence surfaced but model said it is not there: 5
- partial evidence (multi-session aggregation): 5
- answered an unanswerable question: 1
- retrieval miss (evidence never surfaced): 1

## Accuracy by haystack size

- 400k-500k chars: 413/449
- 500k-600k chars: 50/51
