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
# Every agent turn here is the scripted fake the web suite uses
# (scripts/web-e2e-agent.py), never a real backend: the seed's `gummi run`
# and the TUI below both see these two variables.
export GUMMI_AGENT=headless
export GUMMI_AGENT_CMD="$ROOT/scripts/web-e2e-agent.py"

rm -rf "$WS"; mkdir -p "$WS"
git -C "$WS" init -q -b main
git -C "$WS" config user.name t; git -C "$WS" config user.email t@e.invalid
printf '# demo\n' > "$WS/README.md"; printf 'package demo\n' > "$WS/demo.go"
git -C "$WS" add .; git -C "$WS" commit -q -m init
( cd "$WS" && "$BIN" init >/dev/null 2>&1 )
# workflows are off by default; section 12 drives [workflow] turns
printf 'workflows: on\n' >> "$WS/.gummi/config.yaml"
# a profiles.yaml already there keeps the first-run wizard off the board
mkdir -p "$WS/.gummi"
printf 'default: e2e\nprofiles:\n  e2e:\n    implementer: { model: demo-session }\n' > "$WS/.gummi/profiles.yaml"

# FF-001 gets a deep history — its opening turn and fourteen follow-ups,
# each a real turn through the headless driver — so a body taller than any
# window exists. FF-002 is one quiet turn.
seed() { ( cd "$WS" && "$BIN" "$@" >/dev/null 2>&1 ) || { echo "seed failed: gummi $*"; exit 2; }; }
seed run --envelope 5 "Seeded session 1 for the thread drive"
for i in $(seq 1 14); do seed resume FF-001 "follow-up turn $i"; done
seed run --envelope 5 "Seeded session 2 for the thread drive"

tmux kill-session -t $S 2>/dev/null
tmux new-session -d -s $S -x 120 -y 40 \
  "cd '$WS' && GUMMI_AGENT=headless GUMMI_AGENT_CMD='$GUMMI_AGENT_CMD' '$BIN'"
sleep 1.8
# first start opens the agent-tab CLI picker over the board; dismiss it.
send Escape

# FF-003 asks a question with options. It is created here, through the
# form, rather than seeded: an ask's options live in the process that
# asked it, and one seeded by an earlier `gummi run` comes back as a bare
# "answer in your own words".
send n
lit 'Live question for the thread drive [ask-where]'
send Enter
expect "the asking session was created"       'Where should FF-003.s helper live\?'
send Escape

# goto <title> — move the board cursor onto a card by its title.
goto() {
  local want="$1" i
  for i in $(seq 1 8); do
    pane | grep -qE -- "▸[0-9]+ .* $want" && return 0
    send Up
  done
  for i in $(seq 1 8); do
    pane | grep -qE -- "▸[0-9]+ .* $want" && return 0
    send Down
  done
  return 1
}

# reopen <title> — return to a known state from wherever the last section
# left the UI: back out to the board, land on the card, open its page,
# and empty the composer. Sections are not allowed to inherit each
# other's focus, draft, or overlay stack — a suite that let state leak
# between sections could not tell a leaking flag apart from its own
# sloppiness.
reopen() {
  local want="$1" i
  for i in 1 2 3 4; do
    pane | grep -qE 'BOARD  3 cards' && break
    send Escape
  done
  goto "$want" || { echo "reopen: could not reach $want"; exit 2; }
  send Enter
  clear_composer
}

# ============================================================ 1. opening
section "1. opening a card lands on the composer, at the newest event"
reopen 'Seeded session 1'
expect "the card page opened"                 'esc board'
expect "the crumb names the card's position"  '·  [0-9]+ of 3'
expect "the crumb names the step key in force" 'alt\+j/k prev/next card'
expect "the masthead names the card"          'FF-001 · Seeded session 1'
expect "the masthead names the branch"        'ff/seeded-session-1-for-the-thread-drive'
expect "the composer is present and focused"  '┃ '
expect "it opens anchored at the newest event" 'follow-up turn 14'
alive  "still alive after opening a card"

section "2. the composer holds the keyboard — typing is text, not accelerators"
lit 't'
expect "t types rather than toggling anything" '┃ t'
clear_composer
lit 'dmh'
expect "the board's own accelerators type too" '┃ dmh'
clear_composer

section "3. esc leaves in one press, and the draft is kept"
lit 'a draft that must survive'
send Escape
expect "esc left the card page"                'BOARD  3 cards'
send Enter
expect "reopening finds the draft intact"      '┃ a draft that must survive'

section "4. the draft is scoped to its own card — alt+j never carries it forward"
clear_composer
lit 'a draft for this card'
expect "a draft types into the composer"       '┃ a draft for this card'
tmux send-keys -t $S M-j; sleep 0.8
expect "alt+j steps to the next card"          'FF-002 · Seeded session 2'
refute "a draft does not follow to the next card" '┃ a draft for this card'
tmux send-keys -t $S M-k; sleep 0.8
expect "alt+k steps back"                      'FF-001 · Seeded session 1'
clear_composer

section "5. paging the conversation, mid-draft, and its clamps"
size 80 24
lit 'mid-draft text'
expect "the window shows the newest events"    'follow-up turn 14'
for _ in $(seq 1 20); do send PPage; done
expect "pgup reaches the opening turn"         '^    Seeded session 1 for the thread drive'
expect "pgup does not disturb the draft"       '┃ mid-draft text'
send PPage
expect "the up clamp holds"                    '^    Seeded session 1 for the thread drive'
expect "at the oldest end, scroll marker shows" '↓ .* more'
for _ in $(seq 1 20); do send NPage; done
expect "pgdn returns to the newest"            'follow-up turn 14'
expect "at the newest end, scroll marker shows" '↑ .* more'
clear_composer
size 120 40

section "6. the decision picker: movement, numbering, and the aimed word"
reopen 'Live question'
expect "a multi-option decision is pinned"     '▸ 1\. A new file \(recommended\)'
expect "option 2 is the other way"             '2\. Extend the existing file'
expect "the last option is your own words"     '3\. Chat about this'
send Down
expect "↓ moves the highlight"                 '▸ 2\. Extend the existing file'
send Up
expect "↑ moves it back"                       '▸ 1\. A new file'
lit '2'
expect "digits select the option"              '▸ 2\. Extend the existing file'
clear_composer
lit 'the repro steps are too thin'
expect "prose aims at the word-consuming option" '▸ 3\. Chat about this'
expect "the decision says the line is the answer" 'your line is the answer'
expect "the bar says what enter does"          'enter answer'
clear_composer

section "7. a bare word is prose; only /verb acts"
reopen 'Seeded session 2'
lit 'park the CSV path for now'
expect "a line starting with a verb word sends" 'enter send'
refute "…and raises no confirm"                'enter confirm'
clear_composer
lit '/park'
send Enter
expect "/park on an idle session says so"      'nothing running to park'
clear_composer
lit '/close'
send Enter
expect "/close opens the hand-off confirm"     'hand off “Seeded session 2'
expect "…which says nothing lands"             'nothing lands'
send Escape
refute "esc cancels it"                        'hand off “Seeded session 2'
alive  "still alive after the verbs"

section "8. a word no verb covers routes through the command menu"
reopen 'Seeded session 2'
# a word the menu owns rather than the verb vocabulary — budget lives only
# in the card's action inventory — has to find the card's own entry now
# that the menu carries it while a card page is open.
lit '/budget'
send Enter
expect "/<card action> finds the card's own entry" '▸ budget +u'
send Escape
clear_composer
lit '/nosuchverb'
send Enter
expect "an unknown word dead-ends in the menu, said so" 'no commands match'
send Escape
clear_composer

section "9. reaching the card's actions"
reopen 'Seeded session 2'
expect "an idle session pins no decision"      '┃ say what to do next'
send Up
expect "↑ on an empty composer opens actions"  'actions · FF-002'
expect "it carries the keyless actions"        'duplicate'
expect "…and the keyed ones"                   'budget +u'
send Escape
reopen 'Live question'
send Up
expect "↑ on the first option opens actions"   'actions · FF-003'
send Escape

section "10. the outputs toggle"
reopen 'Seeded session 1'
expect "outputs are folded by default"         'alt\+o outputs'
refute "…so a tool's row is hidden"            '✓ edit  NOTES\.md'
tmux send-keys -t $S M-o; sleep 0.7
expect "alt+o unfolds the outputs"             '✓ edit  NOTES\.md'
expect "…and the bar offers to fold them"      'alt\+o fold'
tmux send-keys -t $S M-o; sleep 0.7
refute "alt+o again folds them"                '✓ edit  NOTES\.md'

section "11. the layout under pressure"
for dim in "120 40" "100 30" "80 24" "60 16" "36 9" "24 6" "20 5" "18 4" "120 3"; do
  set -- $dim
  size "$1" "$2"
  alive "survives ${1}x${2}"
done
size 36 9
expect "36x9 keeps the card named"             'FF-001 · Seede'
expect "36x9 keeps the composer"               '┃ '
refute "36x9 spends nothing on the crumb"      'esc board  ·'
size 20 5
expect "at h=5 the layout survives"            'FF-001'
size 120 40
alive "still alive after the resize matrix"

section "12. a workflow run in the thread"
# expect_long <name> <regex> <seconds> — expect, for something that takes
# a run's worth of turns.
expect_long() {
  local name="$1" re="$2" secs="$3" i
  for i in $(seq 1 $((secs * 4))); do
    pane | grep -qE -- "$re" && { PASS=$((PASS+1)); printf '  ok    %s\n' "$name"; return 0; }
    sleep 0.25
  done
  FAIL=$((FAIL+1)); FAILED_NAMES+=("$name")
  printf '  FAIL  %s   (no match for: %s)\n' "$name" "$re"; dump; return 1
}
for i in 1 2 3 4; do
  pane | grep -qE 'BOARD  [0-9]+ cards' && break
  send Escape
done
# the scripted session writes a spec and proposes the bundled deliver on
# it; its implementer asks its person before it writes
send n
lit 'Workflow drive [workflow] [step-asks]'
send Enter
expect_long "the proposal is the pinned decision"  'workflow run 1 · allow run' 20
expect "it draws the recipe's step tree"       'build ↺ ≤3'
expect "…the checks it will run"               'test -f made\.txt'
expect "…and what the pool has left"           'left in the session; the run spends from it'
expect "Allow is the first answer"             '▸ 1\. Allow'
expect "Always allow"                          '2\. Always allow'
refute "…with no amount"                       'Always allow up to'
expect "Deny"                                  '3\. Deny'
expect "Feedback takes the person's words"     '4\. Feedback'
send Enter
expect_long "the run is one block in the thread" '┌ workflow deliver · run 1' 20
expect_long "the implementer's question is drawn under its step" 'implement asks: Keep made\.txt plain' 30
expect "the step's row asks"                   '\? ↺ 1 +implement +implementer'
expect "the live strip says the step asks"     '\? deliver · run 1 +implement asks: Keep made\.txt plain.*alt\+w jump'
refute "the run's notes stay out of the transcript" 'workflow: run 1'
tmux send-keys -t $S M-w; sleep 0.6
expect "alt+w puts the cursor on the asking step" '▸ \? ↺ 1 +implement'
expect "the bar names the run's keys"          'p pause/resume'
send Enter
expect "enter opens the step, with its crumb"  'Session › deliver run 1 › build ↺ 1 › implement'
expect "the step's head names its role and state" 'implementer +implement +build/1/implement'
expect "its question is answerable there"      '▸ 1\. Keep it plain \(recommended\)'
send Enter
expect "the answer reaches the step"           'answered implement: Keep it plain'
send d
expect_long "the step's own diff"              '\+made by step build/1/implement' 15
send b
expect "its brief is the prompt as filled in"  'prompt'
send Escape
expect_long "the run ends done"               'deliver · run 1 .*done · \$' 60
expect "an ended run folds to its results"     '│ ✓ DW-1 failed → passed'
expect "…and the files it left"                '1 file left changed in the tree: made\.txt'
refute "the head carries no validated mark"    'validated at tree|re-check'
send v
expect "v asks before it reverts"              'Revert run 1\?'
expect "…listing every path it puts back"      '^.*│ +made\.txt'
send n
refute "n leaves the tree alone"               'Revert run 1\?'
send Escape
tmux send-keys -t $S M-s; sleep 1
expect "the spec view lists the session's specs" '▸ S1 Make the workflow file +met'
expect "…and the spec's runs, newest first"     '1 ✓ deliver · run 1 · done · met'
send Escape
# the person speaks after the run ended: it is no longer the latest news,
# and folds to one line where it stood (alt+w brings it into view)
lit 'Thanks, that is all for now'
send Enter
expect_long "the person's line is answered"     'Done: noted it in NOTES\.md' 30
tmux send-keys -t $S M-w; sleep 0.6
expect "the ended run folds to one line"        '▸ ✓ deliver · run 1 · spec 1 .* ▸'
send Escape
# the spec's row jumps to it, unfolded whole
tmux send-keys -t $S M-s; sleep 1
send 1
expect "a run's row jumps to its block, whole"  '▸ workflow deliver · run 1'
send Escape
alive "still alive after the run"

section "13. steps at work side by side"
for i in 1 2 3 4; do
  pane | grep -qE 'BOARD  [0-9]+ cards' && break
  send Escape
done
# every step of this run works a few seconds before it answers, so the two
# reviews of deliver's parallel are seen at work together
send n
lit 'Side by side [workflow] [step-slow]'
send Enter
expect_long "its proposal is the pinned decision"  'workflow run 1 · allow run' 20
send Enter
expect_long "a step at work is lit and spins"      '┃ [^ ] +design +contractor' 30
# the live strip stands on the composer while the run is open
expect "the live strip names the run and its step" '[^ ] deliver · run 1 +design ✓? ?›.*· design .*alt\+w jump'
tmux send-keys -t $S M-w; sleep 0.6
expect "alt+w jumps to the block, the cursor on the step at work" '▸ [^ ] +design +contractor'
send Escape
# both reviews at once: two lit rows, one each
both=0
for i in $(seq 1 200); do
  p=$(pane)
  if echo "$p" | grep -qE '┃ [^ ] ↺ 1 +review-a +reviewer' && echo "$p" | grep -qE '┃ [^ ] ↺ 1 +review-b +reviewer'; then both=1; break; fi
  sleep 0.25
done
if [ "$both" = 1 ]; then PASS=$((PASS+1)); printf '  ok    %s\n' "both reviews read as at work, side by side"
else FAIL=$((FAIL+1)); FAILED_NAMES+=("both reviews at work"); printf '  FAIL  %s\n' "both reviews read as at work, side by side"; dump; fi
expect "…while the step before them reads done"  '│ ✓ ↺ 1 +implement +implementer'
expect_long "the run reports to the session"     'has reported' 90
refute "the live strip goes with the run"       'alt\+w jump$|alt\+w jump '
# its report has pushed the block up the thread: alt+w brings it back
tmux send-keys -t $S M-w; sleep 0.6
expect "the ended run's block"                   'deliver · run 1 .*done · \$'
send Enter
expect "its steps unfolded"                      '│ ✓ +validate +reviewer'
refute "and nothing reads as at work after it"   '┃ [^ ] .*(reviewer|contractor|implementer)'
send Escape
alive "still alive after the side-by-side run"

# ----------------------------------------------------------------- report
printf '\n============ %d passed, %d failed, %d known defects ============\n' "$PASS" "$FAIL" "$KNOWN"
if [ "$FAIL" -gt 0 ]; then printf 'failed:\n'; printf '  - %s\n' "${FAILED_NAMES[@]}"; fi
tmux kill-session -t $S 2>/dev/null
exit $([ "$FAIL" -eq 0 ] && echo 0 || echo 1)
