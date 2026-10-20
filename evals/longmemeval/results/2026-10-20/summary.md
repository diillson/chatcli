# chatcli on LongMemEval-S (500 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `22093df22fdb2b99…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-10-20T12:56:32

**Accuracy: 454/500 = 90.8%** (95% CI 87.9%–93.0%) · task-averaged 92.2%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 74/78 | 94.9% | 87.5%–98.0% |
| multi-session | 111/133 | 83.5% | 76.2%–88.8% |
| single-session-assistant | 56/56 | 100.0% | 93.6%–100.0% |
| single-session-preference | 26/30 | 86.7% | 70.3%–94.7% |
| single-session-user | 70/70 | 100.0% | 94.8%–100.0% |
| temporal-reasoning | 117/133 | 88.0% | 81.4%–92.5% |

- Abstention (unanswerable): 27/30
- Cost: $41.39 answering + $1.89 judging · $0.083/question
- Latency: median 22.0s · p90 37.1s · max 80.0s
- Tokens: 71,266 input / 851 output per question (haystack ≈ 115k tokens)
- Tool calls: 4.9/question · date flags used in 66 questions (137 calls) · web in 0
- Evidence surfaced (any evidence session seen): 469/470 · all evidence sessions seen: 436/470
  - passed: 427/427 · failed: 42/43

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 27
- evidence surfaced but model said it is not there: 9
- partial evidence (multi-session aggregation): 6
- answered an unanswerable question: 3
- retrieval miss (evidence never surfaced): 1

## Accuracy by haystack size

- 400k-500k chars: 409/449
- 500k-600k chars: 45/51
