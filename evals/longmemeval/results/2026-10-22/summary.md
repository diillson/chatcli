# chatcli on LongMemEval-S (500 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `1e64fd298e28d23b…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-10-22T11:51:45

**Accuracy: 472/500 = 94.4%** (95% CI 92.0%–96.1%) · task-averaged 95.4%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 77/78 | 98.7% | 93.1%–99.8% |
| multi-session | 118/133 | 88.7% | 82.2%–93.0% |
| single-session-assistant | 55/56 | 98.2% | 90.6%–99.7% |
| single-session-preference | 28/30 | 93.3% | 78.7%–98.2% |
| single-session-user | 70/70 | 100.0% | 94.8%–100.0% |
| temporal-reasoning | 124/133 | 93.2% | 87.6%–96.4% |

- Abstention (unanswerable): 26/30
- Cost: $48.28 answering + $1.86 judging · $0.097/question
- Latency: median 21.7s · p90 31.0s · max 96.3s
- Tokens: 77,045 input / 868 output per question (haystack ≈ 115k tokens)
- Tool calls: 4.9/question · date flags used in 109 questions (189 calls) · web in 0
- Evidence surfaced (any evidence session seen): 470/470 · all evidence sessions seen: 459/470
  - passed: 446/446 · failed: 24/24

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 22
- answered an unanswerable question: 4
- evidence surfaced but model said it is not there: 1
- partial evidence (multi-session aggregation): 1

## Accuracy by haystack size

- 400k-500k chars: 424/449
- 500k-600k chars: 48/51
