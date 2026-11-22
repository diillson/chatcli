# chatcli on LongMemEval-S (260 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `77588b46fef17f65…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-11-22T15:19:22

**Accuracy: 246/260 = 94.6%** (95% CI 91.2%–96.8%) · task-averaged 94.8%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 39/40 | 97.5% | 87.1%–99.6% |
| multi-session | 67/73 | 91.8% | 83.2%–96.2% |
| single-session-assistant | 27/28 | 96.4% | 82.3%–99.4% |
| single-session-preference | 16/18 | 88.9% | 67.2%–96.9% |
| single-session-user | 34/34 | 100.0% | 89.8%–100.0% |
| temporal-reasoning | 63/67 | 94.0% | 85.6%–97.7% |

- Abstention (unanswerable): 12/13
- Cost: $25.95 answering + $0.97 judging · $0.100/question
- Latency: median 19.6s · p90 27.3s · max 51.9s
- Tokens: 80,667 input / 853 output per question (haystack ≈ 115k tokens)
- Tool calls: 3.7/question · date flags used in 35 questions (40 calls) · web in 0
- Evidence surfaced (any evidence session seen): 247/247 · all evidence sessions seen: 245/247
  - passed: 234/234 · failed: 13/13

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 13
- answered an unanswerable question: 1

## Accuracy by haystack size

- 400k-500k chars: 225/238
- 500k-600k chars: 21/22
