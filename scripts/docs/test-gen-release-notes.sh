#!/usr/bin/env bash
# Self-test for scripts/docs/gen-release-notes.py: release pages, the index,
# the home strip and the sidebar come out right from a release-please
# changelog, MDX-significant text is escaped, and a second run is a no-op.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
DOCS="$TMP/docs"
mkdir -p "$DOCS/pt" "$DOCS/usage" "$DOCS/pt/usage"
touch "$DOCS/usage/coder-mode.mdx" "$DOCS/pt/usage/coder-mode.mdx"

for f in "$DOCS/index.mdx" "$DOCS/pt/index.mdx"; do
  printf -- '---\ntitle: "Home"\n---\n\n{/* release-strip:start */}\nstale\n{/* release-strip:end */}\n\nBody stays.\n' > "$f"
done

cat > "$DOCS/docs.json" <<'JSON'
{
  "navigation": {
    "languages": [
      {"language": "en", "groups": [{"group": "Docs", "pages": [{"group": "Releases", "pages": ["releases"]}]}]},
      {"language": "pt-BR", "groups": [{"group": "Docs", "pages": [{"group": "Releases", "pages": ["pt/releases"]}]}]}
    ]
  }
}
JSON

ESC=$'\033'
cat > "$TMP/CHANGELOG.md" <<MD
# Changelog

## [1.3.0](https://github.com/diillson/chatcli/compare/v1.2.1...v1.3.0) (2026-10-09)


### ⚠ BREAKING CHANGES

* **catalog:** the OLD value is gone; migrate before upgrading.

### Features

* **coder:** add the --raw flag and a {placeholder} for <tag> values ([#12](https://github.com/diillson/chatcli/issues/12)) ([abc1234](https://github.com/diillson/chatcli/commit/abc1234def))
* **plugins:** new \`[@kind](https://github.com/kind)\` plugin example ([#13](https://github.com/diillson/chatcli/issues/13)) ([bcd2345](https://github.com/diillson/chatcli/commit/bcd2345ef0))
* plain unscoped entry about${ESC}the cli ([cde3456](https://github.com/diillson/chatcli/commit/cde3456f01))


### Bug Fixes

* **web:** keep the session ([#14](https://github.com/diillson/chatcli/issues/14)) ([def4567](https://github.com/diillson/chatcli/commit/def4567012)), closes [#9](https://github.com/diillson/chatcli/issues/9) [#10](https://github.com/diillson/chatcli/issues/10)

## [1.2.1](https://github.com/diillson/chatcli/compare/v1.2.0...v1.2.1) (2026-09-30)


### Bug Fixes

* **coder:** first copy of a duplicated section ([#11](https://github.com/diillson/chatcli/issues/11)) ([aaa1111](https://github.com/diillson/chatcli/commit/aaa1111bbb))

## [1.2.1](https://github.com/diillson/chatcli/compare/v1.2.0...v1.2.1) (2026-09-30)


### Bug Fixes

* **coder:** first copy of a duplicated section ([#11](https://github.com/diillson/chatcli/issues/11)) ([aaa1111](https://github.com/diillson/chatcli/commit/aaa1111bbb))
* **coder:** second entry only in the second copy ([fff9999](https://github.com/diillson/chatcli/commit/fff9999aaa))
MD

python3 "$DIR/gen-release-notes.py" --changelog "$TMP/CHANGELOG.md" --docs "$DOCS" >/dev/null

fail() { echo "FAIL: $*"; exit 1; }
has() { grep -qF -- "$2" "$1" || { echo "--- $1"; cat "$1"; fail "expected in $1: $2"; }; }
hasnt() { if grep -qF -- "$2" "$1"; then echo "--- $1"; cat "$1"; fail "unexpected in $1: $2"; fi; }

N="$DOCS/releases/1.3.0.mdx"
P="$DOCS/releases/1.2.1.mdx"
[ -f "$N" ] && [ -f "$P" ] && [ -f "$DOCS/pt/releases/1.3.0.mdx" ] || fail "release pages missing"
[ -f "$DOCS/releases.mdx" ] && [ -f "$DOCS/pt/releases.mdx" ] || fail "index pages missing"

has "$N" 'title: "v1.3.0"'
has "$N" 'tag: "Latest"'
hasnt "$P" 'tag: "Latest"'
has "$DOCS/pt/releases/1.3.0.mdx" 'tag: "Mais recente"'
# sections, breaking first, with the warning
has "$N" '## Breaking changes'
has "$N" '## New features'
has "$N" '## Fixes'
has "$DOCS/pt/releases/1.3.0.mdx" '## Novidades'
# a scope with a docs page links to it; one without renders a plain chip
has "$N" '<a className="cc-scope" href="/usage/coder-mode">coder</a>'
has "$DOCS/pt/releases/1.3.0.mdx" 'href="/pt/usage/coder-mode"'
has "$N" '<span className="cc-scope">web</span>'
# MDX escaping, flags as code, mention links and control characters dropped
has "$N" '`--raw`'
has "$N" '&#123;placeholder&#125; for &lt;tag&gt; values'
has "$N" '`@kind`'
hasnt "$N" 'github.com/kind'
has "$N" 'about the cli'
grep -q "$ESC" "$N" && fail "control character leaked into $N"
# refs kept, "closes" tail dropped
has "$N" '[#14](https://github.com/diillson/chatcli/issues/14) · [def4567]'
hasnt "$N" 'closes'
# the duplicated 1.2.1 section merges without repeating the shared entry
[ "$(grep -c '^- .*first copy of a duplicated section' "$P")" = 1 ] || fail "duplicate entry not merged"
has "$P" 'second entry only in the second copy'
# description: plain text, "--" kept as non-breaking hyphens
has "$N" 'description: "the OLD value is gone; migrate before upgrading; add the ‑‑raw flag'
# prev/next links
has "$N" 'href="/releases/1.2.1"'
has "$P" 'href="/releases/1.3.0"'
# index, strip and sidebar
has "$DOCS/releases.mdx" '[**v1.3.0**](/releases/1.3.0)'
has "$DOCS/releases.mdx" '### October'
has "$DOCS/pt/releases.mdx" '### Setembro'
has "$DOCS/index.mdx" '<a className="cc-release" href="/releases/1.3.0">'
hasnt "$DOCS/index.mdx" 'stale'
has "$DOCS/index.mdx" 'Body stays.'
has "$DOCS/pt/index.mdx" 'href="/pt/releases/1.3.0"'
python3 - "$DOCS/docs.json" <<'PY'
import json, sys
d = json.load(open(sys.argv[1]))
en, pt = (l["groups"][0]["pages"][0] for l in d["navigation"]["languages"])
assert en["pages"] == ["releases", "releases/1.3.0", "releases/1.2.1"], en["pages"]
assert pt["pages"] == ["pt/releases", "pt/releases/1.3.0", "pt/releases/1.2.1"], pt["pages"]
assert en["tag"] == pt["tag"] == "v1.3.0", (en.get("tag"), pt.get("tag"))
PY

# a second run changes nothing
out="$(python3 "$DIR/gen-release-notes.py" --changelog "$TMP/CHANGELOG.md" --docs "$DOCS")"
case "$out" in *", 0 file(s) changed") ;; *) fail "second run not idempotent: $out" ;; esac

# a page for a version no longer in the changelog is removed
touch "$DOCS/releases/0.0.1.mdx"
python3 "$DIR/gen-release-notes.py" --changelog "$TMP/CHANGELOG.md" --docs "$DOCS" >/dev/null
[ ! -f "$DOCS/releases/0.0.1.mdx" ] || fail "orphan release page kept"

echo "gen-release-notes self-test: ok"
