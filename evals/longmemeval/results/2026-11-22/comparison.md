# LongMemEval regression check: `2026-11-22` against `2026-10-24-session-search`

| | Base | New | Gained | Lost | Sign test p |
|---|---|---|---|---|---|
| All questions | 479/500 = 95.8% (93.7–97.2%) | 481/500 = 96.2% (94.1–97.6%) | 10 | 8 | 0.81 |
| Development half | 232/240 = 96.7% (93.6–98.3%) | 235/240 = 97.9% (95.2–99.1%) | 5 | 2 | 0.45 |
| Validation half | 247/260 = 95.0% (91.6–97.1%) | 246/260 = 94.6% (91.2–96.8%) | 5 | 6 | 1.00 |

| Type | Base | New | of |
|---|---|---|---|
| knowledge-update | 76 | 77 | 78 |
| multi-session | 122 | 124 | 133 |
| single-session-assistant | 56 | 55 | 56 |
| single-session-preference | 28 | 28 | 30 |
| single-session-user | 70 | 70 | 70 |
| temporal-reasoning | 127 | 127 | 133 |
| abstention (across types) | 28 | 29 | 30 |
| task-averaged accuracy | 96.3% | 96.5% | |

| Per question | Base | New |
|---|---|---|
| Cost, answer + judge (US$) | 0.100 | 0.102 |
| Input tokens | 77,971 | 78,619 |
| Output tokens | 769 | 807 |
| Tool calls | 3.4 | 3.5 |
| Median seconds | 20.2 | 19.1 |
| Evidence surfaced | 470/470 | 470/470 |
| Run health | exit 0: 500; errors 0; empty answers 0 | exit 0: 500; errors 0; empty answers 0 |

## Lost (passed in the base, failed in the new run): 8

| Question | Half | Type | Evidence sessions seen |
|---|---|---|---|
| `6d550036` | development | multi-session | 4/4 |
| `gpt4_59149c78` | development | temporal-reasoning | 2/2 |
| `830ce83f` | validation | knowledge-update | 2/2 |
| `09ba9854_abs` | validation | multi-session | 2/2 |
| `gpt4_ab202e7f` | validation | multi-session | 5/5 |
| `778164c6` | validation | single-session-assistant | 1/1 |
| `gpt4_468eb063` | validation | temporal-reasoning | 1/1 |
| `gpt4_e414231f` | validation | temporal-reasoning | 2/2 |

## Gained (failed in the base, passed in the new run): 10

| Question | Half | Type | Evidence sessions seen |
|---|---|---|---|
| `6456829e` | development | multi-session | 2/2 |
| `80ec1f4f_abs` | development | multi-session | 3/3 |
| `71017277` | development | temporal-reasoning | 1/1 |
| `gpt4_2312f94c` | development | temporal-reasoning | 2/2 |
| `gpt4_f420262c` | development | temporal-reasoning | 5/5 |
| `031748ae_abs` | validation | knowledge-update | 2/2 |
| `07741c45` | validation | knowledge-update | 2/2 |
| `37f165cf` | validation | multi-session | 2/2 |
| `88432d0a` | validation | multi-session | 4/4 |
| `gpt4_2f8be40d` | validation | multi-session | 3/3 |
