# vhs tape: the M0 walkthrough — create a feature, annotate its spec,
# walk it through the workflow, clean it up.
#
# Prereqs: vhs + ttyd + ffmpeg installed; run from the repo root:
#   make build && ./scripts/demo.sh /tmp/gummi-demo
#   cd /tmp/gummi-demo && vhs < $OLDPWD/demo/gummi.tape

Output demo/gummi.gif
Set FontSize 14
Set Width 1200
Set Height 700
Set Theme "Catppuccin Mocha"

Type "gummi board"
Enter
Sleep 1.5s

# new feature
Type "n"
Sleep 500ms
Type "Dark mode toggle"
Sleep 500ms
Enter
Sleep 1s

# open the spec, peek at annotate mode, leave a comment
Type "s"
Sleep 1.5s
Tab
Sleep 1s
Type "n"
Sleep 500ms
Type "c"
Sleep 500ms
Type "users want dark mode at night"
Sleep 500ms
Enter
Sleep 1s
Escape
Sleep 500ms

# walk the workflow: brainstorm → spec → plan (worktree appears) → …
Type "g"
Sleep 800ms
Type "g"
Sleep 800ms
Type "g"
Sleep 1.2s
Type "g"
Sleep 800ms
Type "g"
Sleep 800ms
Type "g"
Sleep 800ms
Type "g"
Sleep 1.5s

# help, then quit
Type "?"
Sleep 2s
Escape
Sleep 500ms
Type "q"
Sleep 500ms
