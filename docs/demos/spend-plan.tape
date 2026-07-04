# gummi spend plan — a feature with a credit envelope shows its per-stage
# budget on the dashboard. Render: vhs docs/demos/spend-plan.tape
#
# Requires: ./bin/gummi built (make build), vhs on PATH.

Output docs/demos/spend-plan.gif

Set Shell bash
Set FontSize 15
Set Width 1300
Set Height 760
Set Padding 12
Set TypingSpeed 60ms

# --- setup: a demo repo, board run with a spend-plan envelope ---
Hide
Type "export GUMMI=$PWD/bin/gummi" Enter
Type "export DIR=$(mktemp -d) && ./scripts/demo.sh $DIR >/dev/null && cd $DIR" Enter
Type "export GUMMI_ENVELOPE=300 COPILOT_CLI_PATH=" Enter
Type "clear" Enter
Show

Type "$GUMMI board" Enter
Sleep 1.5s

# create a feature — it is stamped with the 300-credit envelope
Type "n"
Sleep 400ms
Type "Rate limiter"
Sleep 400ms
Enter
Sleep 1s

# open its dashboard: the card shows spent / envelope, the current stage
# cap (allocation + rollover), and the protected reserve
Enter
Sleep 2.5s
Escape
Sleep 800ms

Type "q"
Sleep 500ms
