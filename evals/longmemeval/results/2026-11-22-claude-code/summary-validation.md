# Claude Code 2.1.330 on LongMemEval-S (260 questions)

- Answer model: `claude-sonnet-5-5` (headless, claude -p) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Agent `Claude Code 2.1.330` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-11-22T19:56:08

**Accuracy: 238/260 = 91.5%** (95% CI 87.5%–94.3%) · task-averaged 92.7%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 36/40 | 90.0% | 76.9%–96.0% |
| multi-session | 63/73 | 86.3% | 76.6%–92.4% |
| single-session-assistant | 28/28 | 100.0% | 87.9%–100.0% |
| single-session-preference | 16/18 | 88.9% | 67.2%–96.9% |
| single-session-user | 34/34 | 100.0% | 89.8%–100.0% |
| temporal-reasoning | 61/67 | 91.0% | 81.8%–95.8% |

- Abstention (unanswerable): 12/13
- Cost: $11.23 answering + $0.88 judging · $0.043/question
- Latency: median 8.2s · p90 15.4s · max 23.6s
- Tokens: 61,180 input / 639 output per question (haystack ≈ 115k tokens)
- Tool calls: 2.1/question · date flags used in 0 questions (0 calls) · web in 0
- Evidence surfaced (any evidence session seen): 246/247 · all evidence sessions seen: 239/247
  - passed: 226/226 · failed: 20/21

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 13
- evidence surfaced but model said it is not there: 5
- partial evidence (multi-session aggregation): 2
- answered an unanswerable question: 1
- retrieval miss (evidence never surfaced): 1

## Accuracy by haystack size

- 400k-500k chars: 217/238
- 500k-600k chars: 21/22
