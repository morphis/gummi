#!/usr/bin/env bash
# Records the gummi demo against canonical/lxd.
#
#   scripts/record-lxd-demo.sh              # full mp4 + README gif
#   scripts/record-lxd-demo.sh --smoke      # first two chapters only
#   scripts/record-lxd-demo.sh --no-setup   # reuse the existing workspace
#
# Needs: tmux, vhs, ttyd, ffmpeg, and a built bin/gummi.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
bin="${GUMMI_BIN:-$root/bin/gummi}"
work="${DEMO_WORK:-/tmp/gummi-demo}"
repo="$work/lxd"
out="$root/docs/assets"

smoke=0
setup=1
for arg in "$@"; do
    case "$arg" in
        --smoke) smoke=1 ;;
        --no-setup) setup=0 ;;
        *) echo "unknown flag: $arg" >&2; exit 1 ;;
    esac
done

fail() { echo "record FAIL: $*" >&2; exit 1; }
for c in tmux vhs ttyd ffmpeg; do
    command -v "$c" >/dev/null || fail "$c not installed"
done
[ -x "$bin" ] || fail "gummi not built at $bin"

if [ "$setup" = 1 ]; then
    GUMMI_BIN="$bin" "$root/scripts/demo-setup.sh" "$work"
fi
[ -d "$repo/.gummi" ] || fail "no workspace at $repo -- drop --no-setup"

mkdir -p "$out"
tape="$(mktemp "${TMPDIR:-/tmp}/gummi-tape.XXXXXX.tape")"
trap 'rm -f "$tape"' EXIT

target="$out/lxd-demo.mp4"
drive="$root/scripts/demo-drive.sh"
env_line="GUMMI_BIN='$bin' DEMO_SPEED='${DEMO_SPEED:-0.85}'"
[ "$smoke" = 1 ] && { target="$out/smoke.mp4"; env_line="$env_line DEMO_SMOKE=1"; }

cat > "$tape" <<TAPE
Output "$target"

Set Shell bash
Set FontFamily "DejaVu Sans Mono"
Set FontSize 14
Set Width 1500
Set Height 940
Set Padding 10
Set Framerate 30
Set TypingSpeed 1ms
Set WindowBar Colorful
Set BorderRadius 8

Hide
Type "clear"
Enter
Type "$env_line $drive '$repo'"
Enter
Show
Wait+Screen@25m /GUMMI-DEMO-DONE/
Hide
TAPE

echo "recording → $target"
( cd "$root" && vhs "$tape" )
echo "wrote $target"

# The README wants a short silent loop, not the whole film.
if [ "$smoke" = 0 ]; then
    gif="$out/demo.gif"
    [ -f "$gif" ] && cp "$gif" "$gif.bak"
    echo "rendering README gif → $gif"
    ffmpeg -y -loglevel error -t 65 -i "$target" \
        -vf "fps=10,scale=1000:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128[p];[b][p]paletteuse=dither=bayer:bayer_scale=4" \
        -loop 0 "$gif"
    echo "wrote $gif ($(du -h "$gif" | cut -f1))"
fi
