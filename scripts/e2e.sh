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
await "no features yet"

# create a feature through the real form
k n; k 'Demo feature'; k Enter
await "FD-001"
[ -f "$dir/.gummi/state/gummi.db" ] || fail "state db missing"

# open the spec surface, leave a comment through the annotate popover
k s; await "open questions"
k Tab; k n; k c; k 'noted in e2e'; k Enter
grep -q "noted in e2e" "$dir/.gummi/state/drafts/FD-001-demo-feature.md" \
    || fail "annotation not persisted to draft"

# advance to spec, then try to approve — the open user annotation blocks it
k Escape
k g; k g          # todo→brainstorm→spec
k g; sleep 0.3    # spec→plan blocked by the open annotation
pane | grep -q "block" || fail "open annotation did not block spec approval"
git -C "$dir" worktree list | grep -q "FD-001" && fail "worktree created despite blocked approval"

# resolve the annotation (navigate to its marker first), then approval proceeds
k s; k Tab; k n; k c; k 'resolved — acknowledged'; k Enter; k Escape

# advance spec→plan: worktree + branch + committed spec
k g; sleep 0.5
git -C "$dir" worktree list | grep -q "FD-001" || fail "worktree not created at spec approval"
git -C "$dir" branch --list 'gummi/FD-001-*' | grep -q gummi || fail "branch missing"
spec_file="$dir/.gummi/worktrees/FD-001/.gummi/specs/FD-001-demo-feature.md"
[ -f "$spec_file" ] || fail "spec not in worktree"
grep -q "noted in e2e" "$spec_file" || fail "annotation lost in spec migration"
git -C "$dir/.gummi/worktrees/FD-001" log --oneline -1 | grep -q "docs(spec): FD-001" \
    || fail "spec commit missing"
[ ! -f "$dir/.gummi/state/drafts/FD-001-demo-feature.md" ] || fail "draft not retired"

# walk to done: plan→implement→review→verify→done
k g; k g; k g; k g
await "DONE"

# delete: confirm dialog → worktree, branch, and record gone
k x; k y
await "no features yet"
git -C "$dir" worktree list | grep -q "FD-001" && fail "worktree survived delete"
git -C "$dir" branch --list 'gummi/FD-001-*' | grep -q gummi && fail "branch survived delete"

k q
echo "e2e PASS"
