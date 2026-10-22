# chatcli on LongMemEval-S (260 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `e8693ec1e7fdd377…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-10-22T13:54:59

**Accuracy: 243/260 = 93.5%** (95% CI 89.8%–95.9%) · task-averaged 94.8%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 39/40 | 97.5% | 87.1%–99.6% |
| multi-session | 63/73 | 86.3% | 76.6%–92.4% |
| single-session-assistant | 27/28 | 96.4% | 82.3%–99.4% |
| single-session-preference | 17/18 | 94.4% | 74.2%–99.0% |
| single-session-user | 34/34 | 100.0% | 89.8%–100.0% |
| temporal-reasoning | 63/67 | 94.0% | 85.6%–97.7% |

- Abstention (unanswerable): 10/13
- Cost: $25.88 answering + $1.00 judging · $0.100/question
- Latency: median 23.4s · p90 34.6s · max 68.4s
- Tokens: 80,430 input / 919 output per question (haystack ≈ 115k tokens)
- Tool calls: 5.3/question · date flags used in 58 questions (99 calls) · web in 0
- Evidence surfaced (any evidence session seen): 247/247 · all evidence sessions seen: 245/247
  - passed: 233/233 · failed: 14/14

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 9
- evidence surfaced but model said it is not there: 4
- answered an unanswerable question: 3
- partial evidence (multi-session aggregation): 1

## Accuracy by haystack size

- 400k-500k chars: 224/238
- 500k-600k chars: 19/22
