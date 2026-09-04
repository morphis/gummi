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
	// sticky marks a bar row that names something consequential enough
	// that dropping it silently would mislead rather than merely
	// declutter — statusbar.Render sheds every other hint before it ever
	// touches one of these (F15: a pinned decision's "enter <option>" row
	// can attach an agent and spend credits, so the width squeeze must
	// never quietly leave "choose · esc" with no sign enter does anything
	// at all). Use sparingly — a bar where everything is sticky is a bar
	// that never sheds, which is exactly what the fits-most-terminals
	// contract depends on NOT happening for the ordinary rows.
	sticky bool
}

// barHints filters a table down to the status-bar subset.
func barHints(bs []binding) []statusbar.Hint {
	var hs []statusbar.Hint
	for _, b := range bs {
		if b.bar {
			hs = append(hs, statusbar.Hint{Key: b.key, Label: b.label, Sticky: b.sticky})
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

// agentBindings is the agent tab's key table. It used to be two states —
// locked/unlocked, for a hosted pty gummi only partly controlled — but
// the tab now hosts gummi's own board conversation (boardthread.go),
// answerable by one ordinary table like every other surface: there is no
// foreign keymap underneath it any more to carve exceptions around, and
// the two-state shape would be describing a mode (the input lock) that
// hostedKeyboard() can no longer even enter (it requires a live m.agent,
// and nothing spawns one now — gotoTab's own comment).
//
// withHelpKey, not a bare alt+/ row: the board composer takes every
// printable key including ?, the same reason threadInputBindings' own
// callers reach for it (cardPageBindings) rather than listing the key
// unconditionally — a literal question mark typed into a board message
// must not open the help overlay instead.
func (m *Shell) agentBindings() []binding {
	// With the completion popup open the bar describes the popup, because
	// that is what the next keystroke will act on: enter runs a command
	// rather than sending a sentence, tab completes a word rather than
	// leaving the tab, and esc closes the list rather than interrupting
	// the board. Naming the other set here would be the bar promising a
	// key the surface is not going to honour — the same rule
	// threadInputBindings follows for its own confirm chip.
	if m.boardComplete != nil {
		return withHelpKey([]binding{
			{key: "enter", label: "run", help: "run the highlighted command", bar: true},
			{key: "tab", label: "complete", help: "finish the word without running it", bar: true},
			{key: "↑↓", label: "move", help: "move through the matching commands", bar: true},
			{key: "esc", label: "dismiss", help: "close the list and keep the line as typed", bar: true},
		})
	}
	return withHelpKey([]binding{
		{key: "enter", label: "send", help: "send the line to the board — it can read and act on every card through the same tools a hosted agent reaches", bar: true},
		{key: "/", label: "commands", help: "on an empty line, open the command list and complete as you type", bar: true},
		{key: "esc", label: "interrupt", help: "interrupt the board's in-flight turn", bar: true},
		{key: "pgup/pgdn", label: "scroll", help: "scroll the conversation without leaving the line", bar: true},
		m.boardOutputsBinding(),
		// Typed, not pressed — the same shape the card thread's own table
		// gives its verb row ({key: "verb"}): the key column names what
		// you enter on the line, because the composer takes every
		// printable key and there is no chord to name instead.
		{key: boardClearCommand, label: "clear", help: "start a fresh conversation — the transcript, its context and the running spend all go with the old session"},
		{key: "tab", label: "next tab", help: "cycle the tabs (board, inbox, agent)", bar: true},
		{key: "alt+1/2/3", label: "tab", help: "jump straight to board / inbox / agent"},
	})
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
	peek := binding{key: "t", label: "open", help: "open the card's thread without starting or attaching anything"}
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
				peek.bar = true
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
		peek,
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
		// research cards carry no branch and never get a worktree: every
		// key below refuses with a notice (shell.go), so surfacing them
		// here — in both the status bar and the ? help overlay, the
		// single slice both render from — would mislead. "a" belongs in
		// this list for the same reason as the branch verbs and was
		// missed: raw-attach needs a worktree to attach into.
		filtered := bs[:0:0]
		for _, b := range bs {
			switch b.key {
			case "a", "d", "r", "m", "c", "z":
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
