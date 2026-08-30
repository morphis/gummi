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
// its key table — the same precedence mainView paints by, including the
// board-tab scope (boardSurfacesLive), so the status bar's hint row and
// the ? overlay describe the surface actually on screen rather than one
// parked on a tab you left.
func (m *Shell) activeSurface() (string, []binding) {
	live := m.boardSurfacesLive()
	switch {
	case live && m.spec != nil:
		return "spec", m.spec.bindings()
	case live && m.diff != nil:
		return "diff", m.diff.bindings()
	case live && m.ingest != nil:
		return "ingest", m.ingest.bindings()
	case live && m.bugIngest != nil:
		return "import bugs", m.bugIngest.bindings()
	case live && m.deps != nil:
		return "deps", m.deps.bindings()
	case live && m.ingestRun != nil && !m.ingestRun.hidden:
		return "ingest", ingestRunBindings
	// the inbox and agent tabs own the main pane whenever they're active.
	// The inbox has its own table now (inboxview.go); the agent tab is
	// still stage 3's placeholder — it still has to answer ? and say how
	// to get back to the board.
	case m.tab == TabInbox:
		return "inbox", m.inboxBindings()
	case m.foreignTab(m.tab):
		return "agent", m.agentBindings()
	case m.tab == TabBoard && len(m.rows) > 0 && m.cardOpen:
		return "card", m.cardPageBindings()
	case m.tab == TabBoard && len(m.rows) > 0:
		return "backlog", m.backlogBindings()
	default:
		return "board", m.splashBindings()
	}
}

// agentBindings is the agent tab's key table, in whichever of the two
// keyboard states it is in. It is deliberately almost empty in both:
// listing the keys gummi does NOT take would be a lie the moment the
// hosted CLI binds one, so each table names only what is actually true.
func (m *Shell) agentBindings() []binding {
	if m.keyboardLocked() {
		return []binding{
			{key: "ctrl+g", label: "unlock", help: "give the input back to gummi — the only key it keeps while locked", bar: true},
			{key: "…", label: "to agent", help: "every other key goes to the hosted CLI, tab and alt+1/2/3 included", bar: true},
			{key: "mouse", label: "to agent", help: "clicks, drags and the wheel reach the CLI (if it asked for them)"},
		}
	}
	return []binding{
		{key: "tab", label: "next tab", help: "cycle the tabs (board, inbox, agent)", bar: true},
		{key: "alt+1/2/3", label: "tab", help: "jump straight to board / inbox / agent", bar: true},
		{key: "ctrl+g", label: "tab→agent", help: "hand tab, alt+1/2/3 and the mouse to the hosted CLI too", bar: true},
		{key: "alt+/", label: "help", help: "this table — ? belongs to the CLI here, being ordinary punctuation", bar: true},
		{key: "…", label: "to agent", help: "every other key already goes to the hosted CLI, including esc, ? and ctrl+c"},
		{key: "mouse", label: "terminal", help: "left to the terminal's own selection until you lock"},
	}
}

// withHelpKey appends the alt+/ row to a surface's table.
//
// It exists for the surfaces that cannot spend ? on help — the chat's
// message box, the bug-import filter — because there a question mark is
// ordinary punctuation the user is trying to type. Those are exactly the
// surfaces whose key rules are least guessable, so leaving them with no
// route to their own table was the worst place to leave one.
func withHelpKey(bs []binding) []binding {
	return append(bs, binding{
		key: "alt+/", label: "help",
		help: "this table — ? types here rather than opening help",
	})
}

// helpOverlay builds the ? dialog for whichever surface is active.
func (m *Shell) helpOverlay() *helpDialog {
	name, bs := m.activeSurface()
	return &helpDialog{title: "keys · " + name, rows: helpRows(bs)}
}

// boardBindings is the board's key table. The bar subset adapts to the
// selected card: enter reads "chat" or "run" by stage, becomes "watch"
// while that feature's agent is running (attach the transcript), and
// "p pause" joins the bar alongside it.
func (m *Shell) boardBindings() []binding {
	enter := binding{key: "enter", label: "chat", help: "chat (brainstorm/spec) · run (autonomous)", bar: true}
	pause := binding{key: "p", label: "pause", help: "pause the running agent; else open the dependency picker"}
	transcript := binding{key: "t", label: "transcript", help: "open the thread's transcript view — events and tool outputs"}
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
		{key: "space", label: "commands", help: "open the command menu — everything that belongs to no card", bar: true},
		{key: "pgup/pgdn", label: "ends", help: "jump to the first/last card"},
		{key: "1..9", label: "jump", help: "jump to feature"},
		enter,
		pause,
		transcript,
		// s and d are off the bar: the action list reaches both without a
		// key, so the bar can spend its width on the two ways in instead.
		{key: "s", label: "spec", help: "spec — comment, resolve and approve in place"},
		{key: "d", label: "diff", help: "diff — comment, resolve and approve in place"},
		advance,
		{key: "b", label: "bounce", help: "bounce back to implement/fix"},
		{key: "P", label: "add plan", help: "restore the plan stage on a quick/skip-plan feature (design phase only)"},
		{key: "v", label: "verify", help: "run verify checks"},
		{key: "u", label: "envelope", help: "set the budget envelope (credits; 0 = uncapped)"},
		{key: "o", label: "repo", help: "change the card's managed repository (before worktree)"},
		{key: "a", label: "attach", help: "raw-attach the agent CLI in the worktree"},
		{key: "A", label: "autopilot", help: "set how far this card runs on its own, and start it"},
		{key: "tab", label: "next tab", help: "cycle the tabs (board, inbox, agent)"},
		{key: "alt+1/2/3", label: "tab", help: "jump straight to board / inbox / agent"},
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
		{key: "D", label: "delete", help: "delete feature (uppercase: it destroys work)"},
		{key: "?", label: "help", bar: true},
		{key: "q", label: "quit"},
	}
	if r, ok := m.selected(); ok && r.DrivenAbroad {
		// another gummi process is driving this card: every verb that
		// would write to it is refused (shell.go's boardVerb), so the bar
		// and the ? help overlay must stop offering them — the same
		// reasoning as the research-card filter below. enter still works,
		// as the way to watch the other process's stream.
		filtered := bs[:0:0]
		for _, b := range bs {
			if foreignBlockedKeys[b.key] {
				continue
			}
			if b.key == "enter" {
				b.label = "watch"
				b.help = "follow the live agent stream of the process driving this card"
			}
			filtered = append(filtered, b)
		}
		return filtered
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
