# chatcli on LongMemEval-S (260 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `1e64fd298e28d23b…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-10-22T11:51:45

**Accuracy: 240/260 = 92.3%** (95% CI 88.4%–95.0%) · task-averaged 93.4%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 39/40 | 97.5% | 87.1%–99.6% |
| multi-session | 61/73 | 83.6% | 73.4%–90.3% |
| single-session-assistant | 27/28 | 96.4% | 82.3%–99.4% |
| single-session-preference | 16/18 | 88.9% | 67.2%–96.9% |
| single-session-user | 34/34 | 100.0% | 89.8%–100.0% |
| temporal-reasoning | 63/67 | 94.0% | 85.6%–97.7% |

- Abstention (unanswerable): 10/13
- Cost: $25.63 answering + $0.95 judging · $0.099/question
- Latency: median 22.9s · p90 32.9s · max 96.3s
- Tokens: 79,692 input / 919 output per question (haystack ≈ 115k tokens)
- Tool calls: 5.3/question · date flags used in 64 questions (105 calls) · web in 0
- Evidence surfaced (any evidence session seen): 247/247 · all evidence sessions seen: 244/247
  - passed: 230/230 · failed: 17/17

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 15
- answered an unanswerable question: 3
- evidence surfaced but model said it is not there: 1
- partial evidence (multi-session aggregation): 1

## Accuracy by haystack size

- 400k-500k chars: 221/238
- 500k-600k chars: 19/22
