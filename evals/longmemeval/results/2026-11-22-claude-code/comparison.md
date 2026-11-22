# LongMemEval-S, same model and judge: Claude Code 2.1.330 (new) against chatcli v1.232.0 (base)

| | Base | New | Gained | Lost | Sign test p |
|---|---|---|---|---|---|
| All questions | 481/500 = 96.2% (94.1–97.6%) | 463/500 = 92.6% (90.0–94.6%) | 7 | 25 | 0.002 |
| Development half | 235/240 = 97.9% (95.2–99.1%) | 225/240 = 93.8% (89.9–96.2%) | 2 | 12 | 0.01 |
| Validation half | 246/260 = 94.6% (91.2–96.8%) | 238/260 = 91.5% (87.5–94.3%) | 5 | 13 | 0.10 |

| Type | Base | New | of |
|---|---|---|---|
| knowledge-update | 77 | 73 | 78 |
| multi-session | 124 | 117 | 133 |
| single-session-assistant | 55 | 56 | 56 |
| single-session-preference | 28 | 28 | 30 |
| single-session-user | 70 | 69 | 70 |
| temporal-reasoning | 127 | 120 | 133 |
| abstention (across types) | 29 | 29 | 30 |
| task-averaged accuracy | 96.5% | 93.9% | |

| Per question | Base | New |
|---|---|---|
| Cost, answer + judge (US$) | 0.102 | 0.046 |
| Input tokens | 78,619 | 60,349 |
| Output tokens | 807 | 616 |
| Tool calls | 3.5 | 2.0 |
| Median seconds | 19.1 | 8.1 |
| Evidence surfaced | 470/470 | 469/470 |
| Run health | exit 0: 500; errors 0; empty answers 0 | exit 0: 500; errors 0; empty answers 0 |

## Lost (passed in the base, failed in the new run): 25

| Question | Half | Type | Evidence sessions seen |
|---|---|---|---|
| `7401057b` | development | knowledge-update | 1/2 |
| `7405e8b1` | development | multi-session | 2/2 |
| `a11281a2` | development | multi-session | 1/2 |
| `edced276` | development | multi-session | 2/2 |
| `efc3f7c2` | development | multi-session | 2/2 |
| `gpt4_372c3eed` | development | multi-session | 2/3 |
| `4100d0a0` | development | single-session-user | 1/1 |
| `71017277` | development | temporal-reasoning | 1/1 |
| `eac54adc` | development | temporal-reasoning | 2/2 |
| `gpt4_85da3956` | development | temporal-reasoning | 1/1 |
| `gpt4_f420262c` | development | temporal-reasoning | 5/5 |
| `gpt4_fe651585` | development | temporal-reasoning | 2/2 |
| `0f05491a` | validation | knowledge-update | 2/2 |
| `2133c1b5` | validation | knowledge-update | 2/2 |
| `852ce960` | validation | knowledge-update | 2/2 |
| `cc5ded98` | validation | knowledge-update | 1/2 |
| `129d1232` | validation | multi-session | 3/3 |
| `gpt4_15e38248` | validation | multi-session | 4/4 |
| `gpt4_31ff4165` | validation | multi-session | 4/5 |
| `gpt4_e05b82a6` | validation | multi-session | 4/4 |
| `195a1a1b` | validation | single-session-preference | 0/1 |
| `eac54add` | validation | temporal-reasoning | 2/2 |
| `gpt4_7abb270c` | validation | temporal-reasoning | 6/6 |
| `gpt4_93159ced` | validation | temporal-reasoning | 1/2 |
| `gpt4_d6585ce8` | validation | temporal-reasoning | 5/5 |

## Gained (failed in the base, passed in the new run): 7

| Question | Half | Type | Evidence sessions seen |
|---|---|---|---|
| `6d550036` | development | multi-session | 4/4 |
| `gpt4_7fce9456` | development | multi-session | 5/5 |
| `830ce83f` | validation | knowledge-update | 2/2 |
| `778164c6` | validation | single-session-assistant | 1/1 |
| `75832dbd` | validation | single-session-preference | 1/1 |
| `gpt4_468eb063` | validation | temporal-reasoning | 1/1 |
| `gpt4_e414231f` | validation | temporal-reasoning | 2/2 |
