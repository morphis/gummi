#!/usr/bin/env bash
# regression suite for the CARD THREAD view — the surface the card
# page opens onto (internal/ui/thread.go, threadinput.go, decision.go).
# Everything here runs against a real gummi binary inside tmux and
# asserts on what the pane actually shows; nothing is stubbed at the
# render layer, because the whole class of defect this hunts (a key the
# composer swallows, a hint the bar sheds, a row a short terminal drops)
# only exists once a real terminal is holding the frame.
#
#   scripts/ptytest-thread.sh
#
# It is deliberately separate from ptytest-tui.sh, which owns the board,
# the command menu and the creation forms. This one owns the thread.
#
# It began as a defect ledger — twenty-one findings from a pty drive of
# the new card page, each one asserted as an xfail so that fixing it
# would say so out loud. All of them are fixed, and every xfail has been
# promoted to a real assertion. The `known` helpers below are kept
# rather than deleted: the next drive that finds something will want
# them, and a defect recorded as a passing-by-omission comment is a
# defect nobody re-checks.
#
# Three verdicts, not two:
#   ok        the behaviour is right and asserted
#   FAIL      a regression — the run exits non-zero
#   KNOWN     a defect recorded on purpose (xfail). It does not fail the
#             run; when it stops reproducing the line flips to "FIXED,
#             promote this assertion", which is the signal to turn it
#             into an expect.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SC="$(mktemp -d)"
S=ptythread
trap 'tmux kill-session -t $S 2>/dev/null; rm -rf "$SC"' EXIT
BIN="$SC/gummi"
WS="$SC/ws"

command -v tmux   >/dev/null || { echo "ptytest-thread: tmux is required";    exit 2; }
command -v sqlite3 >/dev/null || { echo "ptytest-thread: sqlite3 is required"; exit 2; }

echo "building gummi…"
go build -C "$ROOT" -o "$BIN" ./cmd/gummi || exit 2

PASS=0; FAIL=0; KNOWN=0; FAILED_NAMES=()

pane()  { tmux capture-pane -t $S -p 2>/dev/null; }
bar()   { pane | tail -1; }
send()  { tmux send-keys -t $S "$@"; sleep 0.3; }
lit()   { tmux send-keys -t $S -l "$1"; sleep 0.35; }
clear_composer() { tmux send-keys -t $S C-u; sleep 0.3; }
size()  { tmux resize-window -t $S -x "$1" -y "$2" 2>/dev/null ||
          tmux resize-pane   -t $S -x "$1" -y "$2"; sleep 0.7; }
section() { printf '\n== %s ==\n' "$1"; }
dump()  { pane | sed 's/^/       | /' | head -40; }

# expect <name> <regex> — poll until the pane matches.
expect() {
  local name="$1" re="$2" i
  for i in $(seq 1 20); do
    pane | grep -qE -- "$re" && { PASS=$((PASS+1)); printf '  ok    %s\n' "$name"; return 0; }
    sleep 0.25
  done
  FAIL=$((FAIL+1)); FAILED_NAMES+=("$name")
  printf '  FAIL  %s   (no match for: %s)\n' "$name" "$re"; dump; return 1
}

# refute <name> <regex> — pane must NOT match, checked after settling.
refute() {
  local name="$1" re="$2"
  sleep 0.4
  if pane | grep -qE -- "$re"; then
    FAIL=$((FAIL+1)); FAILED_NAMES+=("$name")
    printf '  FAIL  %s   (unexpectedly matched: %s)\n' "$name" "$re"; dump; return 1
  fi
  PASS=$((PASS+1)); printf '  ok    %s\n' "$name"; return 0
}

# known <name> <regex> <note> — a defect this suite documents. Matching
# means the defect is still present (reported, run stays green); NOT
# matching means it was fixed and the assertion should be promoted.
known() {
  local name="$1" re="$2" note="$3"
  sleep 0.4
  if pane | grep -qE -- "$re"; then
    KNOWN=$((KNOWN+1)); printf '  KNOWN %s\n        ↳ %s\n' "$name" "$note"
  else
    PASS=$((PASS+1)); printf '  ok    %s — FIXED, promote this assertion\n' "$name"
  fi
}

# known_count <name> <regex> <min> <note> — the defect is "this line
# appears more than once", which a line-oriented grep can only see as a
# count.
known_count() {
  local name="$1" re="$2" min="$3" note="$4" n
  sleep 0.4
  n=$(pane | grep -cE -- "$re")
  if [ "$n" -ge "$min" ]; then
    KNOWN=$((KNOWN+1)); printf '  KNOWN %s   (%d occurrences)\n        ↳ %s\n' "$name" "$n" "$note"
  else
    PASS=$((PASS+1)); printf '  ok    %s — FIXED, promote this assertion\n' "$name"
  fi
}

# known_absent <name> <regex> <note> — the defect is something MISSING.
known_absent() {
  local name="$1" re="$2" note="$3"
  sleep 0.4
  if pane | grep -qE -- "$re"; then
    PASS=$((PASS+1)); printf '  ok    %s — FIXED, promote this assertion\n' "$name"
  else
    KNOWN=$((KNOWN+1)); printf '  KNOWN %s\n        ↳ %s\n' "$name" "$note"
  fi
}

alive() {
  if tmux has-session -t $S 2>/dev/null; then
    PASS=$((PASS+1)); printf '  ok    %s\n' "$1"
  else
    FAIL=$((FAIL+1)); FAILED_NAMES+=("$1"); printf '  FAIL  %s   (the TUI died)\n' "$1"
  fi
}

# ------------------------------------------------------------- workspace
rm -rf "$WS"; mkdir -p "$WS"
git -C "$WS" init -q -b main
git -C "$WS" config user.name t; git -C "$WS" config user.email t@e.invalid
printf '# demo\n' > "$WS/README.md"; printf 'package demo\n' > "$WS/demo.go"
git -C "$WS" add .; git -C "$WS" commit -q -m init
( cd "$WS" && "$BIN" init >/dev/null 2>&1 )
for i in 1 2 3; do
  ( cd "$WS" && "$BIN" bugs new --title "Seeded bug $i for the thread drive" \
      --desc "d$i" --repro "r$i" --expected "e$i" --actual "a$i" \
      --severity medium --yes >/dev/null 2>&1 )
done

DB="$WS/.gummi/state/gummi.db"

# BG-001 gets a deep history: nine finished stage sessions (four of them
# the same `fix` stage, bounced back by review — the loop every real card
# runs), then an open one. That is what makes the folded receipts, the
# live-stage block and a body taller than any window all exist at once.
n=0; AT=""
tick() { local m=$((n*2)); AT=$(printf '2026-08-30T%02d:%02d:00Z' $((9+m/60)) $((m%60))); n=$((n+1)); }
ins() { tick; sqlite3 "$DB" "INSERT INTO card_events (feature_id,stage,kind,status,at,payload,output,dedupe)
        VALUES ('BG-001','$1','$2','$3','$AT',json('$4'),'$5','');"; }
seg() {
  local stage="$1" role="$2" verdict="$3" credits="$4" nev="$5" i
  ins "$stage" stage_enter '' "{\"role\":\"$role\",\"model\":\"sonnet\",\"flavor\":\"sonnet\"}" ''
  for i in $(seq 1 "$nev"); do
    case $((i % 3)) in
      0) ins "$stage" tool ok "{\"label\":\"Read internal/ui/thread.go:$i\"}" "captured output line for $stage $i" ;;
      1) ins "$stage" message '' "{\"author\":\"$role\",\"content\":\"$stage turn $i — a long enough sentence to exercise wrapping and truncation at narrow widths.\"}" '' ;;
      2) ins "$stage" message '' "{\"author\":\"user\",\"content\":\"$stage user turn $i\"}" '' ;;
    esac
  done
  ins "$stage" stage_exit "$([ "$verdict" = fail ] && echo fail || echo ok)" "{\"verdict\":\"$verdict\",\"credits\":$credits}" ''
  # stage_spend accumulates across generations of a stage, the way the
  # meter really writes it (PK is feature+stage+model+role).
  sqlite3 "$DB" "INSERT INTO stage_spend (feature_id,stage,model,role,credits,updated_at)
    VALUES ('BG-001','$stage','sonnet','$role',$credits,'$AT')
    ON CONFLICT(feature_id,stage,model,role) DO UPDATE SET credits = credits + $credits;"
}
# BG-003 carries a period it ran itself: handed over, one gate crossed
# and one question answered without anyone watching, parked, and then a
# turn typed by a person after it stopped. That last turn is the point —
# the record of the period has to sit ABOVE it, because that is when it
# happened. The block this replaced was appended after the live stage and
# so was permanently below everything, including turns typed hours later.
a=0
atick() { local m=$((a*2)); AP=$(printf '2026-08-30T%02d:%02d:00Z' $((7+m/60)) $((m%60))); a=$((a+1)); }
apins() { atick; sqlite3 "$DB" "INSERT INTO card_events (feature_id,stage,kind,status,at,payload,output,dedupe)
          VALUES ('BG-003','$1','$2','','$AP',json('$3'),'','');"; }
apins diagnose autopilot   '{"event":"took-over","mode":"full"}'
apins diagnose stage_enter '{"role":"diagnoser","model":"sonnet","flavor":"sonnet"}'
apins triage   gate        '{"from":"triage","to":"diagnose","actor":"autopilot"}'
apins diagnose ask         '{"question":"buffer or stream?","answer":"stream rows","by":"autopilot"}'
apins diagnose message     '{"author":"diagnoser","content":"root cause is the theme layer"}'
apins diagnose park        '{"reason":"needs-you","detail":"diagnose finished, review it"}'
apins diagnose message     '{"author":"user","content":"hold on, let me read that"}'

sqlite3 "$DB" "UPDATE features SET stage='review', spend_credits=53.5 WHERE id='BG-001';
               UPDATE features SET stage='done'   WHERE id='BG-002';
               UPDATE features SET stage='diagnose' WHERE id='BG-003';"
seg triage   triager     ok   3.25 5
seg diagnose diagnoser   ok   7.5  7
seg fix      implementer ok  12.0  9
seg review   reviewer    fail 4.0  6
seg fix      implementer ok   9.25 8
seg review   reviewer    fail 3.5  5
seg fix      implementer ok   6.0  7
seg review   reviewer    fail 2.5  4
seg fix      implementer ok   5.5  6
ins review stage_enter '' '{"role":"reviewer","model":"sonnet","flavor":"sonnet"}' ''
for i in 1 2 3 4 5 6 7 8; do
  ins review message '' "{\"author\":\"reviewer\",\"content\":\"open review turn $i — the newest events sit at the bottom\"}" ''
done

tmux kill-session -t $S 2>/dev/null
tmux new-session -d -s $S -x 120 -y 40 "cd '$WS' && '$BIN'"
sleep 1.8
# first start opens the agent-tab CLI picker over the board; dismiss it.
send Escape

# goto <id> — move the backlog cursor onto a card by id.
goto() {
  local want="$1" i
  for i in $(seq 1 8); do
    pane | grep -qE -- "▸[0-9]+ .* $want " && return 0
    send Down
  done
  return 1
}

# reopen <id> — return to a known state from wherever the last section
# left the UI: back out to the backlog list, land on the card, open its
# page, and empty the composer. Sections are not allowed to inherit each
# other's focus, draft, or overlay stack — one of the defects below is
# precisely a flag that leaks between lines, and a suite that let state
# leak between sections could not tell that apart from its own sloppiness.
reopen() {
  local want="$1" i
  for i in 1 2 3 4; do
    pane | grep -qE 'BACKLOG  3 cards' && break
    send Escape
  done
  goto "$want" || { echo "reopen: could not reach $want"; exit 2; }
  send Enter
  clear_composer
}

# ============================================================ 1. opening
section "1. opening a card lands on the composer, at the newest event"
goto BG-001 || { echo "could not reach BG-001"; exit 2; }
send Enter
expect "the card page opened"                 'esc backlog'
expect "the crumb names the card's position"  '·  [0-9]+ of 3'
expect "the crumb names the step key in force" 'alt\+j/k prev/next card'
expect "the masthead names the card"          'BG-001 · Seeded bug 1'
expect "the stage strip renders"              'todo ─ triage ─ diagnose ─ fix'
expect "the pinned spec line renders"         '⌄ bug report · Review'
expect "the composer is present and focused"  '┃ '
expect "it opens anchored at the newest event" 'open review turn 8'
alive  "still alive after opening a card"

section "2. the composer holds the keyboard — typing is text, not accelerators"
lit 't'
expect "t types rather than toggling the transcript" '┃ t'
expect "t types — the transcript mode is gone" '┃ t'
clear_composer
lit 'gsA'
expect "the picker's own accelerators type too"     '┃ gsA'
refute "picker options have no key column"     '▸ \d+\. .* [a-z]\s*$'
clear_composer

section "3. esc leaves in one press, and the draft is kept"
lit 'a draft that must survive'
send Escape
expect "esc left the card page"                'BACKLOG  3 cards'
send Enter
expect "reopening finds the draft intact"      '┃ a draft that must survive'

section "4. the draft is scoped to its own card — alt+j never carries it forward"
clear_composer
lit 'a draft for this card'
expect "a draft types into the composer"       '┃ a draft for this card'
tmux send-keys -t $S M-j; sleep 0.8
expect "alt+j steps to the next card"          'BG-002 · Seeded bug 2'
refute "a draft does not follow to the next card" '┃ a draft for this card'
tmux send-keys -t $S M-k; sleep 0.8
expect "alt+k steps back"                      'BG-001 · Seeded bug 1'
clear_composer

section "5. paging the conversation, mid-draft, and its clamps"
size 80 24
lit 'mid-draft text'
expect "the window shows the newest events"    'open review turn 8'
for _ in 1 2 3 4 5 6 7 8 9 10; do send PPage; done
expect "pgup reaches the oldest receipt" 'diagnose · diagnoser'
expect "pgup does not disturb the draft"        '┃ mid-draft text'
send PPage
expect "the up clamp holds"   'diagnose · diagnoser'
for _ in 1 2 3 4 5 6 7 8 9 10; do send NPage; done
expect "pgdn returns to the newest"             'open review turn 8'
expect "at the newest end, scroll marker shows" '↑ .* more'
send PPage
expect "after pgup, scroll marker shows"        '↓ .* more'
send NPage
send NPage
clear_composer
size 120 40

section "6. the decision picker: movement, numbering, and the aimed word"
send Escape
goto BG-003 || { echo "could not reach BG-003"; exit 2; }
send Enter
expect "a multi-option decision is pinned"     '▸ 1\. start the architect'
expect "option 2 is the gate"                  '2\. approve'
expect "option 4 is the autopilot hand-over"   '4\. let autopilot finish'
send Down; send Down
expect "↓ moves the highlight"                 '▸ 3\. read the bug report first'
send Up
expect "↑ moves it back"                       '▸ 2\. approve'
lit '2'
expect "digits select the option"              '▸ 2\. approve'
expect "the user-chosen position survives"    '▸ 2\. approve'
clear_composer
lit 'the repro steps are too thin'
expect "prose aims at the word-consuming option" '▸ 1\. start the architect with your words'
expect "at 120 cols the bar hints what enter does" 'enter start the architect'
size 140 40
expect "…and it is there once the bar has room" 'enter start the architect'
size 120 40
clear_composer

section "7. verbs, the confirm chip, and what the bar promises"
reopen BG-003
lit 'verify the CSV path is right'
expect "the bar shows 'confirm' for a verb line" 'enter confirm'
send Enter
expect "enter raised the confirm chip"          'verify · run the checks\?'
expect "the chip names both ways out"           'enter yes · esc no, send as a message'
tmux send-keys -t $S M-o; sleep 0.7
expect "alt+o leaves the chip standing"        'verify · run the checks\?'
send PPage
expect "pgup leaves the chip standing"         'verify · run the checks\?'
send NPage
send Escape
expect "esc cancels the chip"                  '┃ verify the CSV path is right'

section "8. after esc on a chip, the next verb parses"
reopen BG-003
lit 'verify test'
send Enter
expect "chip raised"                           'verify · run the checks\?'
send Escape
clear_composer
lit 'verify another test'
send Enter
expect "verify verb parses and raises chip"  'verify · run the checks\?'

section "9. the verb vocabulary's own wiring"
reopen BG-001
lit 'park'
send Enter
expect "park raises a confirm chip"            'park · '
# one esc, not two: the first cancels the chip and hands the line back,
# the second would leave the card page and type the next line into the
# backlog.
send Escape
clear_composer
lit '/park'
send Enter
# the two routes to an action used to run off different vocabularies:
# a bare verb fired, while the same word behind a "/" filtered the
# board-level command menu and dead-ended on "no commands match". They
# share one route now, so "/park" has to reach the same chip bare "park"
# reached three lines up.
expect "/<verb> routes exactly as the bare verb does" 'park · '
refute "…and never dead-ends in the menu"      'no commands match'
send Escape
clear_composer
# a word the menu owns rather than the verb vocabulary — envelope lives
# only in the card's action inventory — has to find the card's own entry
# now that the menu carries it while a card page is open.
lit '/envelope'
send Enter
expect "/<card action> finds the card's own entry" 'envelope +u'
send Escape
clear_composer

section "10. reaching the card's actions"
reopen BG-002
expect "a done card has no open decision"      'or ↑ for actions'
send Up
expect "↑ on a done card opens actions"        'actions · BG-002'
expect "it carries the keyless actions"        'duplicate'
expect "…and the ones no verb covers"          'dependencies'
send Escape
reopen BG-003
expect "a working card pins a decision instead" 'nothing is running|the agent is waiting'
send Up
expect "↑ on first option opens actions"       'actions · BG-003'
expect "actions are reachable from the card"   'dependencies'

section "11. folded receipts"
reopen BG-001
expect "a finished session folds to one line"  'triage · triager · 4 turns · 3\.3 credits'
expect "a failed session is marked ✗"          'review · reviewer .* ✗'
expect "four fix receipts show different spend" 'fix · implementer'
refute "receipt chevrons do not render"        '⌄ triage · triager|⌄ diagnose|⌄ fix|⌄ review'

section "12. a period the card ran itself"
reopen BG-003
expect "the period opens where it began"        '── autopilot took over'
expect "its crossing keeps its position"        'autopilot crossed triage → diagnose'
expect "its answer keeps its position"          'autopilot answered'
expect "the period closes where it stopped"     '── autopilot parked it'
expect "the close carries the park's own words" 'diagnose finished, review it'
expect "the tally counts what it decided"       '1 gate · 1 answer'
refute "no rollup pinned below the thread"      'while you were away'
refute "no raw event kinds leak through"        '^ *(park|decision_open|autopilot)$'
# the whole point: the record sits above the turn typed after it
ap_close=$(pane | grep -n 'autopilot parked it' | head -1 | cut -d: -f1)
ap_turn=$(pane  | grep -n 'hold on, let me read that' | head -1 | cut -d: -f1)
if [ -n "$ap_close" ] && [ -n "$ap_turn" ] && [ "$ap_close" -lt "$ap_turn" ]; then
  PASS=$((PASS+1)); printf '  ok    %s\n' "the period sits above the turn that followed it"
else
  FAIL=$((FAIL+1)); FAILED_NAMES+=("the period sits above the turn that followed it")
  printf '  FAIL  %s   (close=%s turn=%s)\n' "the period sits above the turn that followed it" "$ap_close" "$ap_turn"; dump
fi

section "13. the layout under pressure"
for dim in "120 40" "100 30" "80 24" "60 16" "36 9" "24 6" "20 5" "18 4" "120 3"; do
  set -- $dim
  size "$1" "$2"
  alive "survives ${1}x${2}"
done
size 36 9
expect "36x9 keeps the card named"             'BG-001 · Seeded'
expect "36x9 keeps the composer"               '┃ '
expect "36x9 keeps the decision"               'nothing is running|run review'
refute "36x9 spends nothing on the crumb"      'esc backlog  ·'
size 20 5
expect "at h=5 the layout survives"  'BG-001'
size 120 40
alive "still alive after the resize matrix"

# ----------------------------------------------------------------- report
printf '\n============ %d passed, %d failed, %d known defects ============\n' "$PASS" "$FAIL" "$KNOWN"
if [ "$FAIL" -gt 0 ]; then printf 'failed:\n'; printf '  - %s\n' "${FAILED_NAMES[@]}"; fi
tmux kill-session -t $S 2>/dev/null
exit $([ "$FAIL" -eq 0 ] && echo 0 || echo 1)
