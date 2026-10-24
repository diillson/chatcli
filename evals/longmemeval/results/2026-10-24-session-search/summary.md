# chatcli on LongMemEval-S (500 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `e8954a81a54d1b03…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-10-24T13:03:49

**Accuracy: 479/500 = 95.8%** (95% CI 93.7%–97.2%) · task-averaged 96.3%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 76/78 | 97.4% | 91.1%–99.3% |
| multi-session | 122/133 | 91.7% | 85.8%–95.3% |
| single-session-assistant | 56/56 | 100.0% | 93.6%–100.0% |
| single-session-preference | 28/30 | 93.3% | 78.7%–98.2% |
| single-session-user | 70/70 | 100.0% | 94.8%–100.0% |
| temporal-reasoning | 127/133 | 95.5% | 90.5%–97.9% |

- Abstention (unanswerable): 28/30
- Cost: $48.37 answering + $1.83 judging · $0.097/question
- Latency: median 20.2s · p90 28.5s · max 54.2s
- Tokens: 77,971 input / 769 output per question (haystack ≈ 115k tokens)
- Tool calls: 3.4/question · date flags used in 50 questions (51 calls) · web in 0
- Evidence surfaced (any evidence session seen): 470/470 · all evidence sessions seen: 460/470
  - passed: 451/451 · failed: 19/19

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 17
- answered an unanswerable question: 2
- evidence surfaced but model said it is not there: 1
- partial evidence (multi-session aggregation): 1

## Accuracy by haystack size

- 400k-500k chars: 430/449
- 500k-600k chars: 49/51
