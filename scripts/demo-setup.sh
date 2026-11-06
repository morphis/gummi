#!/usr/bin/env bash
# Builds the demo workspace the recording runs against: a shallow clone of
# canonical/lxd with a .gummi workspace, a headless profile pointed at
# scripts/demo-agent.py, and a board seeded with a few sessions: one
# finished, one waiting on its person's answer. The hero session is NOT
# seeded -- the recording creates it live.
#
# Usage: scripts/demo-setup.sh [workdir]   (default: /tmp/gummi-demo)
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
bin="${GUMMI_BIN:-$root/bin/gummi}"
agent="$root/scripts/demo-agent.py"
work="${1:-/tmp/gummi-demo}"
repo="$work/lxd"

fail() { echo "demo-setup FAIL: $*" >&2; exit 1; }

[ -x "$bin" ] || fail "gummi not built at $bin -- run 'make build' first"
[ -x "$agent" ] || fail "demo agent missing or not executable: $agent"

mkdir -p "$work"

# --- the base repository ------------------------------------------------
if [ ! -d "$repo/.git" ]; then
    echo "cloning canonical/lxd (shallow)…"
    git clone --depth 50 --branch main --quiet \
        https://github.com/canonical/lxd.git "$repo"
fi

git -C "$repo" config user.name "LXD Demo"
git -C "$repo" config user.email "demo@example.invalid"
git -C "$repo" checkout --quiet main
git -C "$repo" reset --hard --quiet origin/main 2>/dev/null || true
git -C "$repo" clean -qfd -e .gummi

# LXD's go.mod asks for a newer toolchain than the container ships; let Go
# fetch it once here so the workflow run's checks do not stall on it.
export GOTOOLCHAIN=auto
(cd "$repo" && go build ./lxc/... >/dev/null 2>&1) || \
    echo "warning: go build ./lxc/... did not succeed; the run's checks may fail" >&2

# --- a fresh gummi workspace -------------------------------------------
# Drop any previous run's worktrees and branches before wiping the state
# db, or the next run trips over a leftover branch of the same name. The
# worktrees have to go first: git refuses to delete a branch that one of
# them still has checked out.
if [ -d "$repo/.gummi" ]; then
    for wt in "$repo"/.gummi/worktrees/*; do
        [ -d "$wt" ] && git -C "$repo" worktree remove --force "$wt" 2>/dev/null || true
    done
    git -C "$repo" worktree prune 2>/dev/null || true
    # Every branch but main is one an earlier take's sessions cut (ff/),
    # and a leftover one collides with the next take's.
    for b in $(git -C "$repo" branch --format='%(refname:short)' | grep -vx main); do
        git -C "$repo" branch -D "$b" >/dev/null 2>&1 || true
    done
fi
rm -rf "$repo/.gummi"
(cd "$repo" && "$bin" init >/dev/null)
[ -d "$repo/.gummi" ] || fail "gummi init did not create $repo/.gummi"

cat > "$repo/.gummi/config.yaml" <<'YAML'
# gummi configuration -- demo workspace.
permissions: allow-all
sandbox: off
workflows: on
YAML

cat > "$repo/.gummi/profiles.yaml" <<'YAML'
default: demo
profiles:
  demo: # every role on the scripted demo agent (GUMMI_AGENT_CMD)
    implementer: { model: demo-session }
    planner: { model: demo-planner }
    step-cheap: { model: demo-cheap }
    step-standard: { model: demo-standard }
    step-strong: { model: demo-strong }
    reviewer: { model: demo-reviewer }
    scribe: { model: demo-scribe }
YAML

# --- seed the board -----------------------------------------------------
export GUMMI_AGENT=headless
export GUMMI_AGENT_CMD="$agent"
export GUMMI_NOTIFY=off
export GUMMI_DEMO_FAST=1     # the seed runs offscreen; no need for the pacing

cd "$repo"

seed() { # seed <prompt>: a session driven headlessly until it stops
    echo "  seeding: $1"
    "$bin" run --envelope 40 "$1" >/dev/null 2>&1 || \
        echo "    (stopped on a question -- fine for a seed)"
}

echo "seeding the board…"

# A finished session, with a commit on its branch.
seed "lxc cluster list: show each member's failure domain"
# A session parked on its person's answer: the demo agent asks first
# whenever a title ends in a question mark.
seed "Warn when a profile is applied across projects?"
# Another finished one, so the board is not two cards long.
seed "lxc storage volume: --format json misses snapshot expiry"

echo
echo "demo workspace ready: $repo"
echo "drive it with:"
echo "  cd $repo && GUMMI_AGENT=headless GUMMI_AGENT_CMD=$agent \\"
echo "    GUMMI_NOTIFY=off $bin"
