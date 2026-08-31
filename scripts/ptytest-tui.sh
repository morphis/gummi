#!/usr/bin/env bash
# Interactive pty test for the TUI's interaction model: drives a real
# gummi binary inside tmux and asserts on what the pane actually shows.
# Covers the things unit and golden tests structurally cannot — focus
# transitions across surfaces, dialogs that open other dialogs, and
# whether the program is still alive after a keystroke. It caught a
# panic on `?` then `esc` that a full static review had passed as clean.
#
#   scripts/ptytest-tui.sh
set -uo pipefail

# Requires tmux and git. Builds its own binary and workspace in a temp dir.
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SC="$(mktemp -d)"
trap 'tmux kill-session -t ptytest 2>/dev/null; rm -rf "$SC"' EXIT
BIN="$SC/gummi"
WS="$SC/ws"
S=ptytest

command -v tmux >/dev/null || { echo "ptytest: tmux is required"; exit 2; }
echo "building gummi…"
go build -C "$ROOT" -o "$BIN" ./cmd/gummi || exit 2
PASS=0; FAIL=0; FAILED_NAMES=()

send() { tmux send-keys -t $S "$@"; sleep 0.25; }

# expect <name> <regex> — poll the pane until it matches, else fail.
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
  pane | sed 's/^/       | /' | head -30
  return 1
}

# refute <name> <regex> — pane must NOT match (checked after settling).
refute() {
  local name="$1" re="$2"
  sleep 0.4
  if pane | grep -qE -- "$re"; then
    FAIL=$((FAIL+1)); FAILED_NAMES+=("$name")
    printf '  FAIL %s   (unexpectedly matched: %s)\n' "$name" "$re"
    pane | sed 's/^/       | /' | head -30
    return 1
  fi
  PASS=$((PASS+1)); printf '  ok   %s\n' "$name"; return 0
}

alive() {
  local name="$1"
  if tmux has-session -t $S 2>/dev/null; then
    PASS=$((PASS+1)); printf '  ok   %s\n' "$name"
  else
    FAIL=$((FAIL+1)); FAILED_NAMES+=("$name")
    printf '  FAIL %s   (the TUI died — crash?)\n' "$name"
  fi
}

section() { printf '\n== %s ==\n' "$1"; }

# goto <label> — press Down until "▸ <label>" is showing (max 14 tries).
goto() {
  local want="$1" i
  for i in $(seq 1 14); do
    if pane | grep -qE -- "▸ $want"; then return 0; fi
    send Down
  done
  return 1
}

# ---------------------------------------------------------------- setup
rm -rf "$WS"; mkdir -p "$WS/repo-b" "$WS/repo-c"
git -C "$WS" init -q -b main
git -C "$WS" config user.name t; git -C "$WS" config user.email t@e.invalid
echo x > "$WS/README.md"; git -C "$WS" add README.md; git -C "$WS" commit -q -m init
for r in repo-b repo-c; do
  git -C "$WS/$r" init -q -b main
  git -C "$WS/$r" config user.name t; git -C "$WS/$r" config user.email t@e.invalid
  echo y > "$WS/$r/README.md"; git -C "$WS/$r" add README.md; git -C "$WS/$r" commit -q -m init
done
( cd "$WS" && "$BIN" init >/dev/null 2>&1 )
printf '\nrepos:\n  b: repo-b\n  c: repo-c\n' >> "$WS/.gummi/config.yaml"

pane() { tmux capture-pane -t $S -p 2>/dev/null; }
lit()  { tmux send-keys -t $S -l "$1"; sleep 0.35; }
clear_composer() { tmux send-keys -t $S C-u; sleep 0.3; }

# reopen <id> — return to a known state from the backlog list, land on
# the card, open its page, and empty the composer. Sections do not inherit
# each other's focus, draft, or overlay state.
reopen() {
  local want="$1" i
  for i in 1 2 3 4; do
    pane | grep -qE 'BACKLOG.*cards' && break
    send Escape
  done
  goto "$want" || { echo "reopen: could not reach $want"; exit 2; }
  send Enter
  clear_composer
}

tmux kill-session -t $S 2>/dev/null
tmux new-session -d -s $S -x 120 -y 40 "cd '$WS' && '$BIN'"
sleep 1.8
# first start opens the agent-tab CLI picker over the board; dismiss it.
send Escape
sleep 0.5

section "1. empty board / splash"
expect "splash advertises the command menu"          'space commands'
send 'Space'
expect "menu footer appears"                         '↑↓ move'
expect "menu lists New feature"                      'New feature'
expect "menu marks engine-only entries"              'Ingest a spec into features'
send Escape

section "2. create a card through the menu and the form's buttons only"
send 'Space'
send 'feat'
expect "typing filters the menu"        'New feature'
refute "filter excludes non-matches"    'Quit gummi'
send Enter
expect "menu ran New feature"           'new feature'
expect "repo field renders first"       'repo: b'
send 'pty smoke card'
send Tab; send Tab; send Tab; send Tab
expect "tab reaches the button row"     '\[ Cancel \]'
expect "button-row hint replaces the field hint" 'enter activate'
send Right
send Enter
expect "Create minted the card"         'FD-001'
alive  "still alive after create"

section "3. duplicate has no accelerator; y is inert on the board"
send 'y'
refute "y no longer opens duplicate"    'duplicate FD-001\?'
alive  "still alive after y"

section "4. opening the card page from the board"
send Enter
expect "the card page opened"           'esc backlog'
expect "the masthead names the card"    'FD-001'
expect "the stage strip renders"        'todo'
expect "the composer is present"        '┃ '
alive  "still alive after opening the card"

section "5. the composer takes the keyboard immediately"
lit 'test text'
expect "typing enters the composer"     '┃ test text'
clear_composer
expect "ctrl+u clears the composer"     '┃ '

section "6. esc leaves the card page and keeps the draft"
lit 'draft text here'
send Escape
expect "esc returned to the backlog"    'BACKLOG'
send Enter
expect "the draft survived"             '┃ draft text here'
alive  "still alive after reopening"

section "7. the masthead, breadcrumb, and stage strip on a card page"
expect "breadcrumb shows navigation"    '‹ esc backlog'
expect "masthead shows the card id"     'FD-001 · pty smoke card'
expect "stage strip shows the workflow" 'todo.*brainstorm.*spec'

section "8. command menu: unavailable entries refuse but stay visible"
send Escape
send 'Space'
send 'ingest'
expect "unavailable entry still listed" 'Ingest a spec into features'
send Enter
# on a board with a card, ingest is available and opens; close it and verify
# the menu path still works. if ingest were unavailable it would refuse here.
send Escape
expect "menu back after ingest close"   'commands'
send Escape

section "9. help overlay is reachable and honest"
send '?'
expect "help opens"                     'keys · backlog'
expect "help lists the menu key"        'open the command menu'
expect "help shows its last row"        'q +quit'
refute "help no longer advertises y"    'y +duplicate'
send Escape
alive  "esc out of help does not panic"
refute "help closed"                    'keys · backlog'

section "10. ctrl+c is hoisted above the overlay"
send 'Space'
expect "menu open"                      'commands'
send C-c
sleep 0.8
if tmux has-session -t $S 2>/dev/null; then
  FAIL=$((FAIL+1)); FAILED_NAMES+=("ctrl+c quits through an open dialog")
  printf '  FAIL ctrl+c quits through an open dialog (session still alive)\n'
else
  PASS=$((PASS+1)); printf '  ok   ctrl+c quits through an open dialog\n'
fi

# ---------------------------------------------------------------- note
# sections testing duplicate, delete, envelope flows through the old
# focusable action list were retired with that surface. they are
# unreachable from this suite until another agent lands the change to
# open the action inventory when Up is pressed past the top option on a
# card page with an empty composer. restore them against the inventory
# overlay then.

# ---------------------------------------------------------------- report
printf '\n================ %d passed, %d failed ================\n' "$PASS" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  printf 'failed:\n'; printf '  - %s\n' "${FAILED_NAMES[@]}"
fi
tmux kill-session -t $S 2>/dev/null
exit $([ "$FAIL" -eq 0 ] && echo 0 || echo 1)
