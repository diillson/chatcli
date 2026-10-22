# chatcli on LongMemEval-S (500 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `e8693ec1e7fdd377…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-10-22T13:54:59

**Accuracy: 473/500 = 94.6%** (95% CI 92.3%–96.3%) · task-averaged 95.9%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 77/78 | 98.7% | 93.1%–99.8% |
| multi-session | 119/133 | 89.5% | 83.1%–93.6% |
| single-session-assistant | 55/56 | 98.2% | 90.6%–99.7% |
| single-session-preference | 29/30 | 96.7% | 83.3%–99.4% |
| single-session-user | 70/70 | 100.0% | 94.8%–100.0% |
| temporal-reasoning | 123/133 | 92.5% | 86.7%–95.9% |

- Abstention (unanswerable): 25/30
- Cost: $48.47 answering + $1.94 judging · $0.097/question
- Latency: median 22.1s · p90 32.5s · max 68.4s
- Tokens: 77,626 input / 859 output per question (haystack ≈ 115k tokens)
- Tool calls: 5.0/question · date flags used in 94 questions (162 calls) · web in 0
- Evidence surfaced (any evidence session seen): 470/470 · all evidence sessions seen: 459/470
  - passed: 448/448 · failed: 22/22

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 17
- answered an unanswerable question: 5
- evidence surfaced but model said it is not there: 4
- partial evidence (multi-session aggregation): 1

## Accuracy by haystack size

- 400k-500k chars: 425/449
- 500k-600k chars: 48/51
