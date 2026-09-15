#!/usr/bin/env bash
# Interactive pty test for the diagnosis mode (DESIGN §13.6): drives a
# real gummi binary inside tmux and asserts on what the pane actually
# shows. The unit tests cover the template, the gates and the layout;
# this covers the things they structurally cannot — that the type row
# reaches diagnosis at all, that the readout names it before the card
# is minted, that the board tells it apart from a survey, and that the
# document a person opens is the diagnosis one.
#
#   scripts/ptytest-diagnosis.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SC="$(mktemp -d)"
trap 'tmux kill-session -t dxtest 2>/dev/null; rm -rf "$SC"' EXIT
BIN="$SC/gummi"
WS="$SC/ws"
S=dxtest

command -v tmux >/dev/null || { echo "ptytest: tmux is required"; exit 2; }
echo "building gummi…"
go build -C "$ROOT" -o "$BIN" ./cmd/gummi || exit 2
PASS=0; FAIL=0; FAILED_NAMES=()

pane() { tmux capture-pane -t $S -p 2>/dev/null; }
send() { tmux send-keys -t $S "$@"; sleep 0.3; }
lit()  { tmux send-keys -t $S -l "$1"; sleep 0.35; }
section() { printf '\n== %s ==\n' "$1"; }

expect() {
  local name="$1" re="$2" i
  for i in $(seq 1 24); do
    if pane | grep -qE -- "$re"; then
      PASS=$((PASS+1)); printf '  ok   %s\n' "$name"; return 0
    fi
    sleep 0.25
  done
  FAIL=$((FAIL+1)); FAILED_NAMES+=("$name")
  printf '  FAIL %s   (no match for: %s)\n' "$name" "$re"
  pane | sed 's/^/       | /' | head -40
  return 1
}
alive() {
  local name="$1"
  if tmux has-session -t $S 2>/dev/null; then
    PASS=$((PASS+1)); printf '  ok   %s\n' "$name"
  else
    FAIL=$((FAIL+1)); FAILED_NAMES+=("$name"); printf '  FAIL %s   (the TUI died)\n' "$name"
  fi
}

mkdir -p "$WS"
git -C "$WS" init -q -b main
git -C "$WS" config user.name t; git -C "$WS" config user.email t@e.invalid
echo x > "$WS/README.md"; git -C "$WS" add README.md; git -C "$WS" commit -q -m init
( cd "$WS" && "$BIN" init >/dev/null 2>&1 )

tmux kill-session -t $S 2>/dev/null
tmux new-session -d -s $S -x 130 -y 42 "cd '$WS' && '$BIN'"
sleep 2.0
send Escape
sleep 0.6

section "1. the door offers diagnosis"
send 'n'
expect "the new-card dialog opened"            'new card'
expect "the kind row carries diagnosis"        'feature.*bug.*research.*diagnosis.*goal'

section "2. selecting it"
# focus starts in the text with a single repo; one shift+tab lands on the
# kind row, then → walks it: feature → bug → research → diagnosis
send S-Tab
send Right; send Right; send Right
expect "the kind row lands on diagnosis"       '▸ diagnosis'
# the placeholder is the box's manual, shown only while it is empty
expect "the box teaches what the text becomes" 'the rest becomes the symptom'
# tab into the box first: the kind row's fall-through is exercised by a
# unit test, and racing it through a pty only tests tmux
send Tab
lit "Picker drops answers and parks the card"
expect "becomes says RS (diagnosis)"           'RS \(diagnosis\) · Picker drops answers'

section "3. creating it"
send Enter
sleep 1.2
alive "the board survived the mint"
expect "an RS card is on the board"            'RS-001'
expect "the diagnosis badge distinguishes it"  'RS-001.*dx'

section "4. the document it opened on"
send Enter
sleep 0.8
send M-s
sleep 1.2
expect "the diagnosis document opened"         'Symptom|Reproduction'
for i in $(seq 1 10); do
  pane | grep -qE 'Ruled out' && break
  send Down; send Down; send Down
done
expect "it carries Ruled out"                  'Ruled out'
for i in $(seq 1 10); do
  pane | grep -qE '## Causes|Causes' && break
  send Down; send Down; send Down
done
expect "it carries Causes"                     'Causes'
alive "still alive after reading the document"

printf '\n%d passed, %d failed\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then printf 'failed: %s\n' "${FAILED_NAMES[*]}"; exit 1; fi
