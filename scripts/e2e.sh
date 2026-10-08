#!/usr/bin/env bash
# End-to-end integration check: drives the real TUI in a tmux PTY
# against a scripted demo repo, and asserts card create, spec draft and delete
# on disk (no agent runs). Exits non-zero on any failed assertion.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
bin="$root/bin/gummi"
sock="gummi-e2e-$$"
dir="$(mktemp -d "${TMPDIR:-/tmp}/gummi-e2e.XXXXXX")"

cleanup() {
    tmux -L "$sock" kill-server 2>/dev/null || true
    rm -rf "$dir"
}
trap cleanup EXIT

fail() { echo "e2e FAIL: $*" >&2; exit 1; }

[ -x "$bin" ] || fail "bin/gummi not built"
"$root/scripts/demo.sh" "$dir" >/dev/null

k() { tmux -L "$sock" send-keys "$@"; sleep 0.3; }
pane() { tmux -L "$sock" capture-pane -p; }

# wait (bounded) until the pane shows a marker string
await() {
    for _ in $(seq 1 50); do
        pane | grep -q "$1" && return 0
        sleep 0.2
    done
    fail "timed out waiting for: $1"
}

tmux -L "$sock" new-session -d -x 120 -y 34 "cd '$dir' && '$bin'"
await "no cards yet"

# create a feature through the real form
k n; k 'Demo feature'; k Enter
await "FD-001"
[ -f "$dir/.gummi/state/gummi.db" ] || fail "state db missing"

# the card's design draft lives in the workspace, not on a branch, and the
# spec surface shows it
draft="$dir/.gummi/state/drafts/FD-001-demo-feature.md"
k s; await "FD-001 · spec"
[ -f "$draft" ] || fail "draft missing"
k Escape; await "Demo feature"
git -C "$dir" worktree list | grep -q "FD-001" && fail "worktree created before any stage ran"

# delete: confirm dialog -> record and draft gone
k D; await "delete FD-001"
k y
await "no cards yet"
[ ! -f "$draft" ] || fail "draft survived delete"

k q
echo "e2e PASS"
