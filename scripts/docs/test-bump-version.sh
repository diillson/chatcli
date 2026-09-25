#!/usr/bin/env bash
# Self-test for scripts/docs/bump-version.pl: current-release tokens move,
# historical prose and unrelated identifiers stay untouched.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cat > "$TMP/page.mdx" <<'MDX'
**1.2.3** — current release card
Image: `ghcr.io/diillson/chatcli:1.2.3` and `ghcr.io/diillson/chatcli-operator:1.2.3`
**Latest version: 1.2.3** <br />
Download: https://github.com/diillson/chatcli/releases/download/v1.2.3/chatcli-darwin-arm64
As of v1.2.3, the operator no longer creates ClusterRoles at runtime.
Regression seen after 1.2.3 (history, must stay).
| `us.anthropic.claude-opus-4-1-20250805-v1:0` | id with digits, must stay |
|-- config_backup_20260301_100000.json
```bash
helm install chatcli oci://ghcr.io/diillson/charts/chatcli --version 1.2.3
docker pull ghcr.io/diillson/chatcli:1.2.3
```
Version 11.2.30 is a different token and must stay.
MDX

OLD_VERSION=1.2.3 NEW_VERSION=1.2.4 perl "$DIR/bump-version.pl" "$TMP/page.mdx" >/dev/null

expect() { grep -qF -- "$1" "$TMP/page.mdx" || { echo "FAIL: expected: $1"; cat "$TMP/page.mdx"; exit 1; }; }
reject() { grep -qF -- "$1" "$TMP/page.mdx" && { echo "FAIL: unexpected: $1"; cat "$TMP/page.mdx"; exit 1; } || true; }

expect '**1.2.4** — current release card'
expect '`ghcr.io/diillson/chatcli:1.2.4` and `ghcr.io/diillson/chatcli-operator:1.2.4`'
expect '**Latest version: 1.2.4**'
expect 'releases/download/v1.2.4/chatcli-darwin-arm64'
expect '--version 1.2.4'
expect 'docker pull ghcr.io/diillson/chatcli:1.2.4'
expect 'As of v1.2.3, the operator'
expect 'Regression seen after 1.2.3'
expect 'claude-opus-4-1-20250805-v1:0'
expect 'config_backup_20260301_100000.json'
expect 'Version 11.2.30 is a different token'
reject '1.2.3`'
echo "bump-version self-test: ok"
