#!/usr/bin/env bash
# Builds the demo workspace the recording runs against: a shallow clone of
# canonical/lxd with a .gummi workspace, a headless profile pointed at
# scripts/demo-agent.py, and a board seeded with cards at four different
# stages. The hero card is NOT seeded -- the recording creates it live.
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
# fetch it once here so the recorded verify stage does not stall on it.
export GOTOOLCHAIN=auto
(cd "$repo" && go build ./lxc/... >/dev/null 2>&1) || \
    echo "warning: go build ./lxc/... did not succeed; verify may fail" >&2

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
    for b in $(git -C "$repo" branch --list 'gummi/*' --format='%(refname:short)'); do
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
autopilot_lanes: 2

# Pre-answer the agent tab's first-run picker so the recording never sees it.
agent: claude
YAML

cat > "$repo/.gummi/profiles.yaml" <<'YAML'
default: demo
profiles:
  demo: # every role on the scripted demo agent (GUMMI_AGENT_CMD)
    architect: { model: demo-architect }
    implementer: { model: demo-implementer }
    reviewer: { model: demo-reviewer }
    scribe: { model: demo-scribe }
YAML

# --- seed the board -----------------------------------------------------
export GUMMI_AGENT=headless
export GUMMI_AGENT_CMD="$agent"
export GUMMI_ENVELOPE=2400
export GUMMI_NOTIFY=off
export GUMMI_DEMO_FAST=1     # the seed runs offscreen; no need for the pacing

cd "$repo"

seed() { # seed <until> <description>
    local until="$1"; shift
    echo "  seeding (--until $until): $1"
    "$bin" run --full --gate-approval gates --until "$until" "$@" \
        >/dev/null 2>&1 || echo "    (stopped early -- fine for a seed)"
}

echo "seeding the board…"

# A card still in the design conversation.
seed spec "lxc storage volume: --format json misses snapshot expiry"

# A card with an approved spec, a worktree and a plan on the table.
seed plan "Warn when a profile is applied across projects"

# A card already at a verified branch, so the board shows the far end.
echo "  seeding (full run to done): cluster failure domain column"
"$bin" run --full --gate-approval full --autonomous \
    "lxc cluster list: show each member's failure domain" >/dev/null 2>&1 || \
    echo "    (stopped early -- fine for a seed)"

# An untriaged bug, so the board opens on a todo item too.
echo "  seeding (bug at todo): lxc file pull truncation"
"$bin" bugs new --yes \
    --title "lxc file pull truncates files larger than 2GiB" \
    --one-liner "large file pulls stop at the 2GiB mark with no error" \
    --severity high \
    --desc "Pulling a file larger than 2GiB out of a container writes a
truncated file to the host and exits 0, so the truncation is silent." \
    --repro "1. lxc exec c1 -- dd if=/dev/urandom of=/big bs=1M count=3000
2. lxc file pull c1/big ./big
3. ls -l ./big" \
    --expected "./big is 3000MiB, matching the file in the container" \
    --actual "./big is exactly 2048MiB and the command exits 0" \
    --env "LXD 5.21 LTS, ext4 host filesystem, container on a dir pool" \
    >/dev/null 2>&1 || echo "    (bug seed failed -- fine)"

echo
echo "demo workspace ready: $repo"
echo "drive it with:"
echo "  cd $repo && GUMMI_AGENT=headless GUMMI_AGENT_CMD=$agent \\"
echo "    GUMMI_ENVELOPE=2400 GUMMI_NOTIFY=off $bin"
