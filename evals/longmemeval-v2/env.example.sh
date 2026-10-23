# Environment for running LongMemEval-V2's own scripts with chatcli as the memory
# agent and Claude (through chatcli) as reader and judge. Source it from the
# directory that holds your LongMemEval-V2 checkout, after starting the bridge:
#
#   CHATCLI_BIN=/path/to/chatcli python3 claude_bridge.py --port 8023 --workers 8 &
#
export DATA_ROOT=/path/to/longmemeval-v2            # python data/download_data.py --data-root ...
export OUTPUT_ROOT=runs
export TIER=small
export CHATCLI_BIN=/path/to/chatcli
export CODEX_BINARY=/path/to/evals/longmemeval-v2/chatcli_codex_compat.py
export CODEX_MODEL=claude-sonnet-5-5
export CODEX_REASONING_EFFORT=high                  # accepted and ignored by the compat front
export READER_BASE_URL=http://127.0.0.1:8023/v1
export READER_MODEL=claude-sonnet-5-5
export OPENAI_BASE_URL=http://127.0.0.1:8023/v1     # the judge client reads it
export OPENAI_API_KEY=local-bridge                  # never leaves the machine
export EVALUATOR_MODEL=claude-sonnet-5-5
# then, from the LongMemEval-V2 checkout:
#   evaluation/scripts/run_codex.sh --prompt-build-max-workers 6
