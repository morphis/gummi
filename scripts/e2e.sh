#!/usr/bin/env bash
# End-to-end integration check: drives the real TUI in a tmux PTY
# against a scripted demo repo, and asserts the full CRUD + worktree
# lifecycle on disk. Exits non-zero on any failed assertion.
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
await "nothing on the board yet"

# create a feature through the real form
k n; k 'Demo feature'; k Enter
await "FD-001"
[ -f "$dir/.gummi/state/gummi.db" ] || fail "state db missing"

# open the spec surface, leave a comment through the annotate popover
k s; await "informational (agent)"
k Tab; k n; k c; k 'noted in e2e'; k Enter
grep -q "noted in e2e" "$dir/.gummi/state/drafts/FD-001-demo-feature.md" \
    || fail "annotation not persisted to draft"

# try to advance — the open user annotation blocks every gate
k Escape
k g; sleep 0.3
pane | grep -q "block" || fail "open annotation did not block the gate"
git -C "$dir" worktree list | grep -q "FD-001" && fail "worktree created despite blocked approval"

# resolve the annotation (navigate to its marker first), then advancing proceeds
k s; k Tab; k n; k c; k 'resolved — acknowledged'; k Enter; k Escape

# walk the design stages (todo→brainstorm→spec); approving the spec creates
# the worktree + branch and promotes the spec to its workspace home
k g; k g
k g; sleep 0.5
git -C "$dir" worktree list | grep -q "FD-001" || fail "worktree not created at spec approval"
git -C "$dir" branch --list 'gummi/FD-001-*' | grep -q gummi || fail "branch missing"
spec_file="$dir/.gummi/specs/FD-001-demo-feature.md"
[ -f "$spec_file" ] || fail "spec not at its workspace home"
grep -q "noted in e2e" "$spec_file" || fail "annotation lost in spec promotion"
[ ! -e "$dir/.gummi/worktrees/FD-001/.gummi" ] || fail ".gummi content leaked into the worktree"
git -C "$dir/.gummi/worktrees/FD-001" log --oneline | grep -q "docs(spec)" \
    && fail "spec commit found on the branch"
[ ! -f "$dir/.gummi/state/drafts/FD-001-demo-feature.md" ] || fail "draft not retired"

# walk to done: plan→implement→review→verify→done
k g; k g; k g; k g
await "DONE"

# delete: confirm dialog → worktree, branch, and record gone
k x; k y
await "nothing on the board yet"
git -C "$dir" worktree list | grep -q "FD-001" && fail "worktree survived delete"
git -C "$dir" branch --list 'gummi/FD-001-*' | grep -q gummi && fail "branch survived delete"
[ ! -f "$spec_file" ] || fail "workspace spec survived delete"

k q
echo "e2e PASS"
