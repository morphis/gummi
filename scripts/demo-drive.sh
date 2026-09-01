#!/usr/bin/env bash
# Drives the gummi demo inside tmux and captions it as it goes.
#
# vhs cannot draw a keystroke overlay, so the caption strip is tmux's own
# two-row status bar: the chapter on top, and the current narration plus
# the key about to be pressed below it. This script sends every keystroke
# itself (rather than letting vhs type), so the strip and the keypress
# stay in lockstep.
#
# Usage: scripts/demo-drive.sh <repo>     (the workspace demo-setup.sh made)
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
bin="${GUMMI_BIN:-$root/bin/gummi}"
agent="$root/scripts/demo-agent.py"
repo="${1:?usage: demo-drive.sh <repo>}"
sock="gummidemo"

# Pacing. SPEED scales every pause, so one knob retimes the whole video.
SPEED="${DEMO_SPEED:-1.0}"

BG="#1b1f2e"      # slightly lifted off the terminal so the strip reads as an overlay
FG="#d2d5e8"
DIM="#6b7290"
ACC="#8f8ff5"
KEYBG="#8f8ff5"
KEYFG="#12141c"

t() { # scaled sleep
    awk -v a="$1" -v s="$SPEED" 'BEGIN{printf "%.3f", a*s}' | xargs sleep
}

tm() { tmux -L "$sock" "$@"; }

cleanup() { tm kill-server 2>/dev/null || true; }
trap cleanup EXIT

# --------------------------------------------------------------------------
# the caption strip
# --------------------------------------------------------------------------

setup_strip() {
    tm set -g status 2
    tm set -g status-interval 1
    tm set -g status-style "bg=$BG,fg=$FG"
    tm set -g pane-border-style "fg=$BG"
    tm set -g pane-active-border-style "fg=$BG"
    tm set -g message-style "bg=$ACC,fg=$KEYFG"
    tm set -g @chapter ""
    tm set -g @note ""
    tm set -g @key ""
    # row 0: the chapter, row 1: narration on the left, keycap on the right
    tm set -g 'status-format[0]' \
        "#[bg=$BG,fg=$ACC,bold] #{@chapter}#[fg=$DIM,nobold]#{@sub}"
    tm set -g 'status-format[1]' \
        "#[bg=$BG,fg=$FG] #{?#{@note},▸ #{@note},}#[align=right]#{?#{@key},#[bg=$KEYBG#,fg=$KEYFG#,bold] #{@key} #[bg=$BG]   ,}"
}

chapter() { # chapter <title> [subtitle]
    tm set -g @chapter "$1"
    tm set -g @sub "${2:+   $2}"
    tm set -g @note ""
    tm set -g @key ""
}

say() { # say <narration> [pause]
    tm set -g @note "$1"
    tm set -g @key ""
    t "${2:-2.2}"
}

# press <tmux-key> <keycap-label> <narration> [pause-after]
press() {
    local key="$1" cap="$2" note="$3" after="${4:-1.4}"
    tm set -g @note "$note"
    tm set -g @key "$cap"
    t 1.5
    tm send-keys "$key"
    t "$after"
    tm set -g @key ""
}

# repeat <tmux-key> <keycap> <narration> <times> [gap]
repeat() {
    local key="$1" cap="$2" note="$3" n="$4" gap="${5:-0.35}"
    tm set -g @note "$note"
    tm set -g @key "$cap"
    t 1.0
    for _ in $(seq 1 "$n"); do
        tm send-keys "$key"
        t "$gap"
    done
    t 0.6
    tm set -g @key ""
}

# typeline <text> <narration> -- types it a character at a time
typeline() {
    local text="$1" note="$2" i ch
    tm set -g @note "$note"
    tm set -g @key "type"
    t 1.0
    for ((i = 0; i < ${#text}; i++)); do
        ch="${text:i:1}"
        tm send-keys -l -- "$ch"
        t 0.035
    done
    t 0.8
    tm set -g @key ""
}

# press_when <screen-regex> <key> <keycap> <narration> [wait] [after]
# Press only if the thing the key is meant for is actually on screen. An
# autonomous card can cross a gate by itself between two cues, and a blind
# Enter then lands on whatever decision replaced it -- which is how a
# stray squash-merge dialog once opened three chapters early.
press_when() {
    local re="$1" key="$2" cap="$3" note="$4" waitfor="${5:-10}" after="${6:-2.0}" i
    tm set -g @note "$note"
    tm set -g @key "$cap"
    t 1.2
    for ((i = 0; i < waitfor * 4; i++)); do
        if tm capture-pane -p 2>/dev/null | grep -qE "$re"; then
            tm send-keys "$key"
            t "$after"
            tm set -g @key ""
            return 0
        fi
        sleep 0.25
    done
    echo "demo-drive: skipped $cap -- /$re/ never appeared" >&2
    tm set -g @key ""
    return 0
}

# to_board <narration> -- esc until the backlog is actually on screen.
# One esc is not enough when a modal is stacked over the card page, and
# esc-ing one time too many from the board is harmless.
to_board() {
    local i
    tm set -g @note "$1"
    tm set -g @key "esc"
    t 1.4
    for ((i = 0; i < 4; i++)); do
        tm capture-pane -p 2>/dev/null | grep -q "BACKLOG" && break
        tm send-keys Escape
        t 0.7
    done
    t 1.0
    tm set -g @key ""
}

# await <regex> [timeout-seconds] -- keeps the video in step with the app
# rather than with a stopwatch. Never fatal: a timed-out cue should cost
# one awkward pause, not the rest of the take.
await() {
    local re="$1" limit="${2:-90}" i
    for ((i = 0; i < limit * 4; i++)); do
        if tm capture-pane -p 2>/dev/null | grep -qE "$re"; then
            return 0
        fi
        sleep 0.25
    done
    echo "demo-drive: timed out waiting for /$re/" >&2
    return 0
}

# The card page draws a stage rail and pads the active stage with an extra
# space on each side ("─ spec ─  plan  ─ implement ─"). That is the one
# progress signal that stays on screen, so stage waits anchor to it rather
# than to thread text, which scrolls away between two polls.
STAGES=(brainstorm spec plan implement review verify done)

# await_stage <stage> [timeout] -- wait until the card is AT that stage or
# past it. "Or past it" is the load-bearing half: an autonomous tail can
# outrun the narration, and a wait for a stage that already went by would
# otherwise burn its whole timeout as dead air.
await_stage() {
    local want="$1" limit="${2:-120}" re="" seen=0 s
    for s in "${STAGES[@]}"; do
        [ "$s" = "$want" ] && seen=1
        [ "$seen" = 1 ] && re="${re}${re:+|}─  ${s}  ─"
    done
    # "done" is the last rung and has no trailing "─" on the rail.
    re="${re}|─  done"
    await "$re" "$limit"
}

# --------------------------------------------------------------------------
# the scenario
# --------------------------------------------------------------------------

cues() {
    t 1.5

    # -- 1 ------------------------------------------------------------------
    chapter "1 · One board, not five terminals" "canonical/lxd"
    say "Every unit of work is a card. Each one owns a git worktree and a branch." 3.2
    say "They are grouped by how far along they are — todo, in progress, review." 3.0
    repeat Down "j" "j and k walk the backlog. There is only ever one list on screen." 3 0.7
    say "A bug, two features mid-flight, and one already at a verified branch." 3.0

    # -- 2 ------------------------------------------------------------------
    chapter "2 · A new feature" "lxc list is missing a column"
    press n "n" "n opens the whole creation form. There is nothing else to fill in." 1.6
    typeline "lxc list: add a disk usage percent column" \
        "The first line becomes the title; anything past it seeds the spec's Problem."
    say "The quiet row underneath picks the model profile and the route." 2.6
    press Enter "enter" "Create it." 2.5
    await "disk usage percent column" 20

    # A smoke run stops here: enough to prove the pipeline, cheap to redo.
    if [ -n "${DEMO_SMOKE:-}" ]; then
        chapter "gummi" "smoke run"
        say "Smoke run complete." 2.0
        return 0
    fi

    # -- 3 ------------------------------------------------------------------
    chapter "3 · Design is a conversation" "brainstorm"
    # Selection does not follow a newly created card, and chapter 1 left it
    # further down the list -- so re-anchor on card 1 and step to the new
    # one rather than assuming where the cursor is.
    press 1 "1" "Digits jump straight to a card." 1.2
    press Down "j" "The new feature is the second card in todo." 1.4
    press Enter "enter" "Open it. The card page is a thread." 2.6
    await "disk usage percent column" 20
    say "Nothing has run yet, so gummi pins the one decision that is open." 2.8
    press Enter "enter" "Start the design flow." 2.4
    press Enter "enter" "Now start the architect." 1.5
    await "which denominator" 90
    say "It read lxc/list.go first, then wrote the Problem section from what it found." 3.4
    say "One question per turn, with its recommendation attached — so you can agree in a word." 3.6
    typeline "(a) — report against the pool total, not the quota" \
        "Answer it in the composer, like any other chat."
    press Enter "enter" "Send." 1.2
    await "shorthand char" 90
    say "Second decision: d and D are both taken, so the disk pair cannot mirror m and M." 3.6
    typeline "U it is" "Agree, and it moves on."
    press Enter "enter" "Send." 1.2
    await "stop here" 90

    # -- the spec ----------------------------------------------------------
    chapter "3 · Design is a conversation" "the spec is the artifact"
    say "The transcript is not the deliverable — the spec is. It lives in the repo." 3.2
    press Up "↑" "The composer keeps the keyboard, so the card's verbs live behind ↑." 2.4
    say "Every verb this card has, in one list." 2.4
    repeat Down "j" "Walk down to the spec." 4 0.45
    press Enter "enter" "Open it." 2.8
    say "Open questions are tracked as %% threads, listed above the document." 3.2
    repeat Down "j" "Move to the line you want to say something about." 9 0.16
    press c "c" "c comments on the line under the cursor." 2.0
    typeline "worth saying explicitly: a blank cell, never 0.0%" \
        "Your words go into the spec, not into a chat that scrolls away."
    press Enter "enter" "Save it." 2.4
    say "It landed as a %% marker — and notice it now blocks approval." 3.4
    press Down "j" "Your open question is a gate, not a note." 1.2
    press x "x" "x resolves the thread once you are satisfied." 2.6

    # -- the gate ----------------------------------------------------------
    chapter "3 · Design is a conversation" "crossing a gate"
    press g "g" "g crosses the gate. Brainstorm is done; the spec stage converges." 3.0
    press Enter "enter" "Run the architect once more." 1.5
    await "your gate" 90
    say "It converged, and resolved its own open thread while it was there." 3.2
    say "Approving is the moment gummi creates the worktree and the branch." 3.2
    press Enter "enter" "Approve." 2.0
    await_stage plan 60

    # -- 4 ------------------------------------------------------------------
    # From here the card is running, so the stages advance themselves and
    # the cues only narrate. Await the stage rail, not thread text: the
    # thread scrolls, and a line can pass by between two polls.
    chapter "4 · Implementation runs alone" "plan → critique → implement"
    say "A worktree and a branch now exist. The spec settled into .gummi/specs." 3.4
    press_when "run the planner" Enter "enter" "Run the planner." 20 1.8
    say "Tracer-bullet steps, each naming the files it touches and the test that proves it." 3.6
    say "Before the plan reaches you, a fresh reviewer tries to refute it." 3.4
    say "It passed with one non-blocking nit, filed as its own thread." 3.0
    await_stage implement 120
    say "This card is on gates — the everyday default — so design gates cross themselves." 3.8
    say "Implement runs alone in the worktree, streaming what it does into the card." 3.6
    await "untouched" 150
    say "Mid-turn the implementer needs a decision, so it asks — inline, with options." 3.8
    say "The blocked turn spends no tokens while it waits for you." 3.0
    press_when "untouched" Enter "enter" "Take the recommendation." 15 2.4
    say "Three steps, real edits to lxc/list.go, and a commit on the card's branch." 3.6

    # -- 5 ------------------------------------------------------------------
    chapter "5 · Review has no shared context" "a fresh session, the spec and the diff"
    await_stage review 150
    say "The reviewer never saw the implementer's session. Only the spec and the diff." 3.8
    say "It found the missing test — blocking — so the work bounces back automatically." 3.8
    say "The implementer fixes it and resolves the thread with how, then review runs again." 4.0
    await_stage verify 180
    say "Round two passed. Verify now runs the repo's own checks from the spec." 3.8
    say "go build and go vet really ran, against the real LXD tree." 3.6
    say "Verification passed — and gummi stops here to ask what happens next." 3.4

    # -- 6 ------------------------------------------------------------------
    # Autopilot comes before the landing beat because the verify decision's
    # own default IS the landing dialog: going to the board first keeps the
    # two ideas from colliding in one modal.
    chapter "6 · Autopilot" "how far a card runs on its own"
    to_board "Back to the board — esc leaves a card page in one press."
    say "Single-letter verbs work here: the board has the keyboard, not a composer." 3.2
    press 1 "1" "Take the untriaged bug at the top." 1.6
    press_when "BACKLOG" A "A" "A sets how far a card runs unattended — and starts it where it sits." 10 3.2
    say "off stops at every gate · gates crosses the design gates · full runs to a verified branch." 4.4
    say "On full it answers its own questions and bounces its own failed verify." 3.4
    say "What it never does is widen its own reach. And it never lands on main." 3.6
    press Escape "esc" "Leave this one as it was." 2.0

    # -- 7 ------------------------------------------------------------------
    chapter "7 · Done means a verified branch" "landing is still yours"
    to_board "The feature we drove is finished: reviewed, verified, on its own branch."
    # A digit, not four j's: a dialog closing above swallows the first
    # keystroke after it, which silently lands the cursor one card short.
    press 5 "5" "Jump straight to the card we drove." 2.0
    press_when "BACKLOG" m "m" "m squash-merges it into main." 10 3.4
    say "gummi drafts the landing message from the spec and the branch's own commits…" 3.8
    say "…and then stops. You read it, edit it, approve it. Nothing lands unreviewed." 4.2
    press Escape "esc" "Not today — the branch keeps." 2.2

    chapter "gummi" "a meta-harness for coding agents"
    say "One board. Every stage gated. Frontier models only where they earn them." 4.2
    t 2.0
}

# --------------------------------------------------------------------------

main() {
    [ -x "$bin" ] || { echo "gummi not built at $bin" >&2; exit 1; }
    [ -d "$repo/.gummi" ] || { echo "no .gummi in $repo -- run demo-setup.sh" >&2; exit 1; }

    cleanup
    tm new-session -d -s demo -x "${DEMO_COLS:-132}" -y "${DEMO_ROWS:-38}" \
        "cd '$repo' && GUMMI_AGENT=headless GUMMI_AGENT_CMD='$agent' \
         GUMMI_ENVELOPE=2400 GUMMI_NOTIFY=off GOTOOLCHAIN=auto '$bin'"
    tm set -g window-size latest
    tm set -g aggressive-resize on
    tm set -g status-keys emacs
    setup_strip

    # DEMO_NO_ATTACH drives the session with no client attached: it proves
    # the whole cue sequence and its awaits without paying for a render.
    if [ -n "${DEMO_NO_ATTACH:-}" ]; then
        sleep 2.5
        cues
        tm kill-server 2>/dev/null || true
        echo "GUMMI-DEMO-DONE"
        return 0
    fi

    ( sleep 2.5; cues; sleep 1; tm kill-server 2>/dev/null ) &

    # The cue runner kills the server when it is finished, which makes
    # attach exit non-zero -- that is the normal ending, not a failure.
    tm attach -t demo || true

    # The recorder watches for this line to know the take is over; it is
    # deliberately not part of the command vhs types, so the sentinel can
    # never match before the run actually finishes.
    clear || true
    echo "GUMMI-DEMO-DONE"
}

main "$@"
