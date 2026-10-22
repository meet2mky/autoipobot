#!/usr/bin/env bash
# Parses every JS file with headless Chrome over file:// and prints script errors
# (runtime ReferenceErrors for chrome/importScripts are expected; SyntaxErrors are not).
set -u
ROOT="$(cd "$(dirname "$0")" && pwd)/root"
CHROME="${CHROME:-$(command -v google-chrome || command -v google-chrome-stable || command -v chromium || command -v chromium-browser || true)}"
if [[ -z "$CHROME" ]]; then echo "no Chrome binary found; set CHROME=…" >&2; exit 2; fi
PROFILE="$(mktemp -d)"
for f in ../ext/shared.js ../ext/page-scan.js ../ext/service-worker.js ../ext/popup.js ../ext/options.js chrome-stub.js harness.js sw-host.js; do
  echo "== $f"
  "$CHROME" --headless=new --no-sandbox --disable-gpu --no-first-run --disable-extensions \
    --allow-file-access-from-files --user-data-dir="$PROFILE" --virtual-time-budget=1000 --timeout=15000 --dump-dom \
    "file://$ROOT/h/syntax.html?file=$f" 2>/dev/null | python3 -c '
import html, re, sys
text = sys.stdin.read()
match = re.search(r"<pre id=\"out\">(.*?)</pre>", text, re.S)
print(html.unescape(match.group(1)).strip() if match else "NO OUTPUT: " + text[:200])
'
done
rm -rf "$PROFILE"
