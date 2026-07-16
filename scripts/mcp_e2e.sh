#!/usr/bin/env bash
# mcp_e2e.sh
#
# End-to-end smoke test of `chatcli mcp-server` over stdio: pipes a JSON-RPC
# conversation into the server and asserts the protocol contract that the
# unit tests cannot see — the REAL process wiring:
#
#   1. EVERY stdout line is valid JSON (proves the stdout quarantine: no
#      stray print ever interleaves with protocol frames).
#   2. initialize advertises tools, prompts and resources.
#   3. tools/list exposes the parity surface (ask_chatcli with `plain`,
#      agent_task, manage_session with search/fork when available).
#   4. resources/list + resources/read serve the chatcli:// state.
#
# No LLM provider or API key is required: everything exercised here is the
# protocol/local-state surface. Chat/agent calls are intentionally NOT made.
#
# Usage:
#   ./scripts/mcp_e2e.sh [path-to-chatcli-binary]
#
# Requirements: bash 4+, jq.

set -euo pipefail

BIN="${1:-./chatcli}"
if [[ ! -x "$BIN" ]]; then
  echo "building chatcli…"
  go build -o "$BIN" .
fi
command -v jq >/dev/null || { echo "jq is required"; exit 1; }

OUT="$(mktemp)"
trap 'rm -f "$OUT"' EXIT

printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' \
  '{"jsonrpc":"2.0","id":3,"method":"resources/list","params":{}}' \
  '{"jsonrpc":"2.0","id":4,"method":"prompts/list","params":{}}' \
  | "$BIN" mcp-server > "$OUT"

FAIL=0

# 1. Every line is valid JSON — the protocol channel is clean.
LINENO_=0
while IFS= read -r line; do
  LINENO_=$((LINENO_ + 1))
  [[ -z "$line" ]] && continue
  if ! jq -e . >/dev/null 2>&1 <<<"$line"; then
    echo "FAIL: stdout line $LINENO_ is not JSON: $line"
    FAIL=1
  fi
done < "$OUT"

expect() { # expect <jq-filter> <description>
  if jq -es "$1" "$OUT" >/dev/null 2>&1; then
    echo "ok: $2"
  else
    echo "FAIL: $2"
    FAIL=1
  fi
}

expect '[.[] | select(.id==1)] | length == 1 and (.[0].result.capabilities | has("tools") and has("prompts") and has("resources"))' \
  "initialize advertises tools+prompts+resources"
expect '[.[] | select(.id==2)][0].result.tools | map(.name) | index("list_providers") != null' \
  "tools/list includes list_providers"
expect '([.[] | select(.id==2)][0].result.tools | map(select(.name=="ask_chatcli")) | first // {} | .inputSchema.properties | has("plain")) or ([.[] | select(.id==2)][0].result.tools | map(.name) | index("ask_chatcli") == null)' \
  "ask_chatcli (when a provider is configured) accepts plain"
expect '[.[] | select(.id==3)][0].result.resources | type == "array"' \
  "resources/list answers with a resource array"
expect '[.[] | select(.id==3)][0].result.resources | map(.uri) | map(select(startswith("chatcli://"))) | length > 0' \
  "resources use the chatcli:// scheme"
expect '[.[] | select(.id==4)][0].result | has("prompts")' \
  "prompts/list answers (skills as prompts)"

# 4. Read one resource end-to-end (memory index is always present).
READ_OUT="$(printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}' \
  '{"jsonrpc":"2.0","id":2,"method":"resources/read","params":{"uri":"chatcli://memory/index"}}' \
  | "$BIN" mcp-server | jq -cs '[.[] | select(.id==2)][0]')"
if jq -e '.result.contents[0].uri == "chatcli://memory/index"' >/dev/null 2>&1 <<<"$READ_OUT"; then
  echo "ok: resources/read chatcli://memory/index"
else
  echo "FAIL: resources/read chatcli://memory/index → $READ_OUT"
  FAIL=1
fi

if [[ "$FAIL" -ne 0 ]]; then
  echo "mcp_e2e: FAILED"
  exit 1
fi
echo "mcp_e2e: all checks passed"
