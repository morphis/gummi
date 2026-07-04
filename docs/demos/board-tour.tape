# gummi board tour — create a feature, walk its stages, open the spec and
# diff surfaces. Render: vhs docs/demos/board-tour.tape
#
# Requires: ./bin/gummi built (make build), vhs on PATH.

Output docs/demos/board-tour.gif

Set Shell bash
Set FontSize 15
Set Width 1300
Set Height 760
Set Padding 12
Set TypingSpeed 60ms

# --- setup a throwaway demo repo (hidden) ---
# vhs starts in the repo root; capture the built binary before cd'ing away.
Hide
Type "export GUMMI=$PWD/bin/gummi" Enter
Type "export DIR=$(mktemp -d) && ./scripts/demo.sh $DIR >/dev/null && cd $DIR" Enter
Type "clear" Enter
Show

# --- launch the board ---
Type "$GUMMI board" Enter
Sleep 1.5s

# create a feature through the form
Type "n"
Sleep 500ms
Type "Dark mode toggle"
Sleep 400ms
Enter
Sleep 1s

# open the spec surface, glance at it, toggle annotate mode, back out
Type "s"
Sleep 1.5s
Tab
Sleep 1.5s
Escape
Sleep 800ms

# walk the workflow: brainstorm -> spec
Type "gg"
Sleep 1s

# help overlay
Type "?"
Sleep 2s
Escape
Sleep 800ms

Type "q"
Sleep 500ms
