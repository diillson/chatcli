# chatcli on LongMemEval-S (260 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `e8954a81a54d1b03…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-10-24T13:03:49

**Accuracy: 247/260 = 95.0%** (95% CI 91.6%–97.1%) · task-averaged 95.2%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 38/40 | 95.0% | 83.5%–98.6% |
| multi-session | 66/73 | 90.4% | 81.5%–95.3% |
| single-session-assistant | 28/28 | 100.0% | 87.9%–100.0% |
| single-session-preference | 16/18 | 88.9% | 67.2%–96.9% |
| single-session-user | 34/34 | 100.0% | 89.8%–100.0% |
| temporal-reasoning | 65/67 | 97.0% | 89.8%–99.2% |

- Abstention (unanswerable): 12/13
- Cost: $25.72 answering + $0.95 judging · $0.099/question
- Latency: median 20.9s · p90 29.2s · max 54.2s
- Tokens: 80,175 input / 812 output per question (haystack ≈ 115k tokens)
- Tool calls: 3.7/question · date flags used in 28 questions (29 calls) · web in 0
- Evidence surfaced (any evidence session seen): 247/247 · all evidence sessions seen: 243/247
  - passed: 235/235 · failed: 12/12

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 10
- evidence surfaced but model said it is not there: 1
- answered an unanswerable question: 1
- partial evidence (multi-session aggregation): 1

## Accuracy by haystack size

- 400k-500k chars: 227/238
- 500k-600k chars: 20/22
