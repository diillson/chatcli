# chatcli on LongMemEval-S (260 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `18aeab5784d4bc20…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-10-24T11:12:56

**Accuracy: 240/260 = 92.3%** (95% CI 88.4%–95.0%) · task-averaged 92.1%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 36/40 | 90.0% | 76.9%–96.0% |
| multi-session | 65/73 | 89.0% | 79.8%–94.3% |
| single-session-assistant | 27/28 | 96.4% | 82.3%–99.4% |
| single-session-preference | 15/18 | 83.3% | 60.8%–94.2% |
| single-session-user | 34/34 | 100.0% | 89.8%–100.0% |
| temporal-reasoning | 63/67 | 94.0% | 85.6%–97.7% |

- Abstention (unanswerable): 11/13
- Cost: $25.13 answering + $0.95 judging · $0.097/question
- Latency: median 20.1s · p90 29.8s · max 39.8s
- Tokens: 79,058 input / 816 output per question (haystack ≈ 115k tokens)
- Tool calls: 5.0/question · date flags used in 53 questions (83 calls) · web in 0
- Evidence surfaced (any evidence session seen): 246/247 · all evidence sessions seen: 244/247
  - passed: 229/229 · failed: 17/18

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 16
- answered an unanswerable question: 2
- retrieval miss (evidence never surfaced): 1
- evidence surfaced but model said it is not there: 1

## Accuracy by haystack size

- 400k-500k chars: 219/238
- 500k-600k chars: 21/22
