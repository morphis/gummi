#!/usr/bin/env bash
# Records docs/assets/demo.gif: scaffolds a fresh demo repo, seeds the
# board with features at varied stages by driving the real TUI in a
# tmux PTY, dresses one feature up as mid-review work, then records a
# scripted drive with vhs.
#
# Needs: tmux, vhs (go install github.com/charmbracelet/vhs@latest),
# ttyd, ffmpeg. Run from anywhere; writes only docs/assets/demo.gif.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
bin="$root/bin/gummi"
sock="gummi-record-$$"
dir="$(mktemp -d "${TMPDIR:-/tmp}/gummi-record.XXXXXX")"

cleanup() {
    tmux -L "$sock" kill-server 2>/dev/null || true
    rm -rf "$dir"
}
trap cleanup EXIT

fail() { echo "record-demo FAIL: $*" >&2; exit 1; }

command -v tmux >/dev/null || fail "tmux not installed"
command -v vhs >/dev/null || fail "vhs not installed"
[ -x "$bin" ] || fail "bin/gummi not built — run 'make build' first"
"$root/scripts/demo.sh" "$dir" >/dev/null

k() { tmux -L "$sock" send-keys "$@"; sleep 0.4; }
pane() { tmux -L "$sock" capture-pane -p; }
await() {
    for _ in $(seq 1 50); do
        pane | grep -q "$1" && return 0
        sleep 0.2
    done
    fail "timed out waiting for: $1"
}

# --- seed the board: four items at four different stages ---------------
tmux -L "$sock" new-session -d -x 120 -y 34 "cd '$dir' && '$bin'"
await "nothing on the board yet"

k n; k 'Dark mode for the dashboard'; k Enter; await "FD-001"
k n; k 'Export reports to CSV'; k Enter; await "FD-002"
k n; k 'OAuth login with GitHub'; k Enter; await "FD-003"
k B; k 'Crash on empty config file'; k Enter; await "BG-004"

# Cards renumber as they change columns, so re-select by digit after
# each feature settles. Advancing the selected card follows it across
# columns (same behavior e2e.sh relies on).
k 1; k g; k g; k g; sleep 0.8; k g; k g; sleep 1   # FD-001 → review (creates worktree)
k 1; k g; k g; k g; sleep 1                        # FD-002 → plan (creates worktree)
k 1; k g; k g; sleep 1                             # FD-003 → spec
k q; sleep 0.5
tmux -L "$sock" kill-server 2>/dev/null || true

# --- dress FD-001 as real mid-review work ------------------------------
wt="$dir/.gummi/worktrees/FD-001"
[ -d "$wt" ] || fail "FD-001 worktree missing"
cat > "$wt/.gummi/specs/FD-001-dark-mode-for-the-dashboard.md" <<'SPEC'
# FD-001: Dark mode for the dashboard

## Problem

The dashboard is light-only. On-call engineers on night shifts report
eye strain and keep a userscript hack alive to invert colors — it breaks
on every release. We already ship a dark theme in the CLI; the web
dashboard should match.

## Considered approaches

1. **CSS custom properties + `prefers-color-scheme`** — smallest diff,
   no runtime code; but no manual override for users whose OS setting
   doesn't match their preference.
2. **Full theme system with runtime switching** — persisted preference,
   room for branded themes later; more code and a settings surface we
   don't otherwise need yet.

## Chosen approach

Approach 1 plus a manual toggle persisted in local storage; the media
query only sets the default. Colors become named tokens in `theme.go` —
components must never reference raw hex values.

## Implementation notes

Chart colors need their own dark variants; contrast-check both palettes.

## Progress

- [x] color tokens extracted into `theme.go`
- [x] toggle + persisted preference
- [ ] audit chart components for raw hex values

## Review

%% @gummi: reviewer findings land here; the implementer resolves each one

## Verification plan

```gummi-checks
go build ./...
go test ./...
```

- toggle flips every surface, preference survives reload
- both palettes pass WCAG AA contrast on the chart legend
SPEC
cat > "$wt/theme.go" <<'GO'
package demo

// Theme names a palette; components reference tokens, never raw hex.
type Theme struct {
	Name       string
	Background string
	Foreground string
	Accent     string
	ChartGrid  string
}

var (
	Light = Theme{"light", "#ffffff", "#1a1a2e", "#5b5bd6", "#e2e2ea"}
	Dark  = Theme{"dark", "#16161e", "#e6e6f0", "#7c7cf0", "#2a2a3a"}
)

// Resolve returns the theme for a stored preference, falling back to
// the OS default when the preference is unset.
func Resolve(pref string, osDark bool) Theme {
	switch pref {
	case "light":
		return Light
	case "dark":
		return Dark
	}
	if osDark {
		return Dark
	}
	return Light
}
GO
git -C "$wt" add -A
git -C "$wt" commit -qm "feat(dashboard): dark mode with persisted toggle"

# --- record ------------------------------------------------------------
mkdir -p "$root/docs/assets"
tape="$dir/demo.tape"
cat > "$tape" <<TAPE
Output docs/assets/demo.gif
Set Shell bash
Set FontSize 15
Set Width 1200
Set Height 680
Set Padding 16
Set TypingSpeed 60ms
Set Framerate 30

Hide
Type "cd $dir && GUMMI_NOTIFY=off '$bin'"
Enter
Wait+Screen@10s /REVIEW/
Show

Sleep 3s
Type "j"
Sleep 800ms
Type "j"
Sleep 800ms
Type "j"
Sleep 2s

Type "4"
Sleep 1s
Type "s"
Sleep 4s
Escape
Sleep 500ms
Type "d"
Sleep 3.5s
Escape
Sleep 1s

Type "n"
Sleep 800ms
Type "Rate-limit the public API"
Sleep 500ms
Enter
Sleep 1.5s
Type "2"
Sleep 800ms
Type "g"
Sleep 3s

Hide
Type "q"
TAPE
(cd "$root" && vhs "$tape")
echo "wrote docs/assets/demo.gif"
