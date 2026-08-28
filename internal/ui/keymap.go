package ui

import (
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/statusbar"
)

// binding is one key → action pair. Each surface declares its keys as a
// binding table next to its key handler — the single source of truth
// that both the status bar's hint row and the ? help overlay render
// from, so neither can drift from what the handler actually answers to.
type binding struct {
	key   string // as displayed: "j/k", "enter", "1..9"
	label string // short action name for the status-bar hint row
	help  string // fuller phrasing for the help overlay; label when empty
	bar   bool   // curated into the status bar (the row fits ~6 hints)
}

// barHints filters a table down to the status-bar subset.
func barHints(bs []binding) []statusbar.Hint {
	var hs []statusbar.Hint
	for _, b := range bs {
		if b.bar {
			hs = append(hs, statusbar.Hint{Key: b.key, Label: b.label})
		}
	}
	return hs
}

// helpRows renders a table into help-overlay rows.
func helpRows(bs []binding) [][2]string {
	rows := make([][2]string, 0, len(bs))
	for _, b := range bs {
		h := b.help
		if h == "" {
			h = b.label
		}
		rows = append(rows, [2]string{b.key, h})
	}
	return rows
}

// activeSurface names the surface that owns the main pane and returns
// its key table — the same precedence mainView paints by.
func (m *Shell) activeSurface() (string, []binding) {
	switch {
	case m.chat != nil:
		return "chat", m.chat.bindings()
	case m.spec != nil:
		return "spec", m.spec.bindings()
	case m.diff != nil:
		return "diff", m.diff.bindings()
	case m.ingest != nil:
		return "ingest", m.ingest.bindings()
	case m.bugIngest != nil:
		return "import bugs", m.bugIngest.bindings()
	case m.deps != nil:
		return "deps", m.deps.bindings()
	case m.ingestRun != nil && !m.ingestRun.hidden:
		return "ingest", ingestRunBindings
	case len(m.rows) > 0:
		return "board", m.boardBindings()
	default:
		return "board", m.splashBindings()
	}
}

// helpOverlay builds the ? dialog for whichever surface is active.
func (m *Shell) helpOverlay() helpDialog {
	name, bs := m.activeSurface()
	return helpDialog{title: "keys · " + name, rows: helpRows(bs)}
}

// boardBindings is the board's key table. The bar subset adapts to the
// selected card: enter reads "chat" or "run" by stage, becomes "watch"
// while that feature's agent is running (attach the transcript), and
// "p pause" joins the bar alongside it.
func (m *Shell) boardBindings() []binding {
	enter := binding{key: "enter", label: "chat", help: "chat (brainstorm/spec) · run (autonomous)", bar: true}
	pause := binding{key: "p", label: "pause", help: "pause the running agent; else open the dependency picker"}
	transcript := binding{key: "t", label: "transcript", help: "read the session transcript (tool calls and their outputs)"}
	advance := binding{key: "g", label: "advance", help: "advance stage (gate; from verify it lands the branch on main)", bar: true}
	if r, ok := m.selected(); ok && r.F.Kind == domain.KindResearch && r.F.Stage == domain.StageDone {
		// FD-081: a done RS card has nothing left to advance — g re-runs
		// decompose instead.
		advance.label = "decompose"
		advance.help = "on a done RS: re-run decompose"
	}
	if r, ok := m.selected(); ok && autonomousStage(r.F.Stage) {
		enter.label = "run"
		if s := m.sessionFor(r.F.ID); s != nil {
			switch s.State() {
			case engine.StateRunning:
				enter.label = "watch"
				enter.help = "watch the running agent (scrollable transcript)"
			case engine.StateDone, engine.StatePaused:
				// a finished/paused run: reading what happened is the
				// draw, so surface it (enter would re-run the stage)
				transcript.bar = true
			}
			pause.bar = true
		}
	}
	bs := []binding{
		{key: "j/k ↓↑", label: "select", help: "select feature"},
		{key: "→", label: "actions", help: "focus the card's action list (↑↓ move, enter runs, ← back)", bar: true},
		{key: "space", label: "commands", help: "open the command menu — everything that belongs to no card", bar: true},
		{key: "pgup/pgdn", label: "ends", help: "jump to the first/last card"},
		{key: "1..9", label: "jump", help: "jump to feature"},
		enter,
		pause,
		transcript,
		{key: "s", label: "spec", help: "spec (tab: read ⇄ annotate)", bar: true},
		{key: "d", label: "diff", help: "diff (tab: read ⇄ annotate)", bar: true},
		advance,
		{key: "b", label: "bounce", help: "bounce back to implement/fix"},
		{key: "P", label: "add plan", help: "restore the plan stage on a quick/skip-plan feature (design phase only)"},
		{key: "v", label: "verify", help: "run verify checks"},
		{key: "u", label: "envelope", help: "set the budget envelope (credits; 0 = uncapped)"},
		{key: "o", label: "repo", help: "change the card's managed repository (before worktree)"},
		{key: "a", label: "attach", help: "raw-attach the agent CLI in the worktree"},
		{key: "tab", label: "attention", help: "cycle needs-attention queue"},
		{key: "i", label: "inbox", help: "open needs-attention inbox"},
		{key: "r", label: "rebase", help: "rebase branch onto main (conflicts hand off to an agent)"},
		{key: "m", label: "merge", help: "squash-merge branch into main (review & approve the drafted message)"},
		{key: "z", label: "squash", help: "collapse the branch to one commit in place (review & approve the drafted message)"},
		{key: "c", label: "clean up", help: "clean up a landed branch"},
		{key: "n", label: "new", help: "new feature", bar: true},
		{key: "B", label: "bug", help: "new bug"},
		{key: "R", label: "research", help: "new research card"},
		{key: "I", label: "ingest", help: "ingest a spec into features"},
		{key: "G", label: "import", help: "import bugs from GitHub"},
		{key: "S", label: "sort", help: "toggle severity sort (todo only)"},
		{key: "x", label: "delete", help: "delete feature"},
		{key: "?", label: "help", bar: true},
		{key: "q", label: "quit"},
	}
	if r, ok := m.selected(); ok && r.F.Kind == domain.KindResearch {
		// research cards carry no branch: the four worktree-verb keys
		// refuse with a notice (shell.go), so surfacing them here — in
		// both the status bar and the ? help overlay, the single slice
		// both render from — would mislead.
		filtered := bs[:0:0]
		for _, b := range bs {
			switch b.key {
			case "d", "r", "m", "c", "z":
				continue
			}
			filtered = append(filtered, b)
		}
		bs = filtered
	}
	return bs
}

// splashBindings is the empty-board table: only creation and global
// keys apply before the first card exists (fewer still when detached).
func (m *Shell) splashBindings() []binding {
	if !m.attached() {
		return []binding{
			{key: "?", label: "help", bar: true},
			{key: "q", label: "quit", bar: true},
		}
	}
	return []binding{
		{key: "space", label: "commands", help: "open the command menu — everything that belongs to no card", bar: true},
		{key: "n", label: "new", help: "new feature", bar: true},
		{key: "B", label: "bug", help: "new bug", bar: true},
		{key: "I", label: "ingest", help: "ingest a spec into features", bar: true},
		{key: "G", label: "import", help: "import bugs from GitHub"},
		{key: "?", label: "help", bar: true},
		{key: "q", label: "quit", bar: true},
	}
}

// ingestRunBindings is the live ingest feed's table. The feed is
// watch-only; these keys background and re-foreground it.
var ingestRunBindings = []binding{
	{key: "esc", label: "board", help: "background the feed — the pass keeps running", bar: true},
	{key: "I", label: "ingest feed", help: "bring the backgrounded feed forward", bar: true},
	{key: "?", label: "help", bar: true},
}
