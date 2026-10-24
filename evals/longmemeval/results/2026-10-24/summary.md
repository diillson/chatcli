# chatcli on LongMemEval-S (500 questions)

- Answer model: `claude-sonnet-5-5` (coder one-shot) · judge: `claude-sonnet-5-5` with the official LongMemEval prompts
- Binary sha256 `18aeab5784d4bc20…` · dataset sha256 `d6f21ea9d60a0d56…` · started 2026-10-24T11:12:56

**Accuracy: 475/500 = 95.0%** (95% CI 92.7%–96.6%) · task-averaged 94.9%

| Type | Pass | Accuracy | 95% CI |
|---|---|---|---|
| knowledge-update | 74/78 | 94.9% | 87.5%–98.0% |
| multi-session | 123/133 | 92.5% | 86.7%–95.9% |
| single-session-assistant | 55/56 | 98.2% | 90.6%–99.7% |
| single-session-preference | 27/30 | 90.0% | 74.4%–96.5% |
| single-session-user | 69/70 | 98.6% | 92.3%–99.7% |
| temporal-reasoning | 127/133 | 95.5% | 90.5%–97.9% |

- Abstention (unanswerable): 28/30
- Cost: $47.56 answering + $1.84 judging · $0.095/question
- Latency: median 19.2s · p90 28.4s · max 114.0s
- Tokens: 77,034 input / 775 output per question (haystack ≈ 115k tokens)
- Tool calls: 4.7/question · date flags used in 93 questions (154 calls) · web in 0
- Evidence surfaced (any evidence session seen): 469/470 · all evidence sessions seen: 459/470
  - passed: 447/447 · failed: 22/23

## Failure taxonomy

- evidence surfaced, wrong answer (reasoning): 20
- answered an unanswerable question: 2
- partial evidence (multi-session aggregation): 1
- retrieval miss (evidence never surfaced): 1
- evidence surfaced but model said it is not there: 1

## Accuracy by haystack size

- 400k-500k chars: 426/449
- 500k-600k chars: 49/51
