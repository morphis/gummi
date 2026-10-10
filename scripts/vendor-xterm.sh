#!/usr/bin/env bash
# Vendors xterm.js for the web face's Terminal tab into
# internal/web/assets/vendor, patched to run under the page's CSP.
#
#   scripts/vendor-xterm.sh
#
# The page allows no inline style (style-src 'self'), and xterm.js writes
# two kinds: <style> elements it fills from script, and style attributes
# on cells with a colour of their own. Both are rewritten here to go
# through the CSSOM, which the policy does not restrict:
#
#   - createElement("style") becomes createElement("xterm-style"), a custom
#     element terminal.js defines over a constructed stylesheet;
#   - setAttribute("style", ...) becomes an assignment to style.cssText.
#
# Each rewrite asserts how many places it changed, so a new xterm.js that
# moved one fails here rather than in a browser. Needs npm and python3.
set -euo pipefail

XTERM=6.0.0
FIT=0.11.0

root="$(cd "$(dirname "$0")/.." && pwd)"
out="$root/internal/web/assets/vendor"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cd "$tmp"
npm pack --silent "@xterm/xterm@$XTERM" "@xterm/addon-fit@$FIT" >/dev/null
mkdir xterm fit
tar xzf "xterm-xterm-$XTERM.tgz" -C xterm
tar xzf "xterm-addon-fit-$FIT.tgz" -C fit

mkdir -p "$out"
python3 - "$tmp" "$out" "$XTERM" "$FIT" <<'PY'
import re, sys
tmp, out, xterm, fit = sys.argv[1:]

def sub(text, old, new, want):
    got = text.count(old)
    if got != want:
        sys.exit(f"vendor-xterm: {old!r} found {got} times, want {want}")
    return text.replace(old, new)

def strip_map(text):
    return re.sub(r"\n//# sourceMappingURL=\S+\s*$", "\n", text)

js = open(f"{tmp}/xterm/package/lib/xterm.mjs").read()
js = sub(js, 'createElement("style")', 'createElement("xterm-style")', 3)
js = sub(js, 't.setAttribute("style",`${t.getAttribute("style")||""}${e};`)', 't.style.cssText+=`${e};`', 1)
head = f"// @xterm/xterm {xterm} (MIT, xterm-LICENSE.txt), patched by scripts/vendor-xterm.sh. Do not edit.\n"
open(f"{out}/xterm.js", "w").write(head + strip_map(js))

js = open(f"{tmp}/fit/package/lib/addon-fit.mjs").read()
head = f"// @xterm/addon-fit {fit} (MIT, xterm-LICENSE.txt), vendored by scripts/vendor-xterm.sh. Do not edit.\n"
open(f"{out}/xterm-addon-fit.js", "w").write(head + strip_map(js))

css = open(f"{tmp}/xterm/package/css/xterm.css").read()
open(f"{out}/xterm.css", "w").write(css)
open(f"{out}/xterm-LICENSE.txt", "w").write(open(f"{tmp}/xterm/package/LICENSE").read())
PY
echo "vendored @xterm/xterm $XTERM and @xterm/addon-fit $FIT into ${out#"$root"/}"
