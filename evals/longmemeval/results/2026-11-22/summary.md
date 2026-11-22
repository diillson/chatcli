# chatcli on LongMemEval-S (500 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `77588b46fef17f65…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-11-22T15:19:22

**Accuracy: 481/500 = 96.2%** (95% CI 94.1%–97.6%) · task-averaged 96.5%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 77/78 | 98.7% | 93.1%–99.8% |
| multi-session | 124/133 | 93.2% | 87.6%–96.4% |
| single-session-assistant | 55/56 | 98.2% | 90.6%–99.7% |
| single-session-preference | 28/30 | 93.3% | 78.7%–98.2% |
| single-session-user | 70/70 | 100.0% | 94.8%–100.0% |
| temporal-reasoning | 127/133 | 95.5% | 90.5%–97.9% |

- Abstention (unanswerable): 29/30
- Cost: $48.92 answering + $1.84 judging · $0.098/question
- Latency: median 19.1s · p90 26.2s · max 51.9s
- Tokens: 78,619 input / 807 output per question (haystack ≈ 115k tokens)
- Tool calls: 3.5/question · date flags used in 56 questions (63 calls) · web in 0
- Evidence surfaced (any evidence session seen): 470/470 · all evidence sessions seen: 462/470
  - passed: 452/452 · failed: 18/18

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 18
- answered an unanswerable question: 1

## Accuracy by haystack size

- 400k-500k chars: 431/449
- 500k-600k chars: 50/51
