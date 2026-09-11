package ui

import (
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/statusbar"
	"github.com/morphis/gummi/internal/worktree"
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
		// with a card page underneath, the artifact is one of that page's
		// tabs (cardtabs.go) and its table has to name the way to the
		// other two. Opened from the backlog list there is no page and no
		// bar, so the table stays the surface's own.
		return "spec", m.withCardTabsIf(m.cardOpen, m.spec.bindings())
	case live && m.diff != nil:
		return "diff", m.withCardTabsIf(m.cardOpen, m.diff.bindings())
	case live && m.ingest != nil:
		return "ingest", m.ingest.bindings()
	case live && m.bugIngest != nil:
		return "import bugs", m.bugIngest.bindings()
	case live && m.deps != nil:
		return "dependencies", m.deps.bindings()
	case live && m.ingestRun != nil && !m.ingestRun.hidden:
		return "ingest", ingestRunBindings
	// the inbox and agent tabs own the main pane whenever they're active.
	// The inbox has its own table now (inboxview.go); the agent tab is
	// still stage 3's placeholder — it still has to answer ? and say how
	// to get back to the board.
	case m.tab == TabInbox:
		return "inbox", m.inboxBindings()
	case m.tab == TabAgent:
		return "agent", m.agentBindings()
	case m.tab == TabBoard && len(m.rows) > 0 && m.cardOpen:
		return "card", m.cardPageBindings()
	case m.tab == TabBoard && len(m.rows) > 0:
		return "board", m.backlogBindings()
	default:
		return "board", m.splashBindings()
	}
}

// agentBindings is the agent tab's key table. The tab hosts gummi's own
// board conversation (boardthread.go), answerable by one ordinary table
// like every other surface: there is no foreign keymap underneath it to
// carve exceptions around.
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
// bar: true, so the row reaches the STATUS BAR and not only the table it
// describes. Without it the card page offered no route to help anywhere on
// screen: ? types into the composer, the bar never named an alternative,
// and the space menu's "Show the keys for this surface" row advertised ? —
// so alt+/ was documented in exactly one place, the last row of the table
// you needed it to open (round 3 §2.3). It goes last in the table, which
// is what makes it the first hint the bar sheds when it is tight
// (statusbar.Render drops from the second-to-last backwards): help earns a
// slot when there is room, never at the cost of what enter does.
func withHelpKey(bs []binding) []binding {
	help := binding{
		key: "alt+/", label: "help",
		help: "this table — ? types here rather than opening help",
		bar:  true,
	}
	if len(bs) == 0 {
		return []binding{help}
	}
	// SPLICED ABOVE THE LAST ROW, not appended after it — withCardTabs'
	// convention, for withCardTabs' reason. Every table here ends with its
	// way out, and the status bar sheds hints from the second-to-last
	// backwards precisely so that row outlives the rest. Appending help
	// made IT last and pushed esc into the first slot to be dropped: the
	// card page's bar went from "… · esc board" to "… · alt+/ help" with
	// no way out named at all. Second-to-last is the right place for help
	// anyway — it is the first thing a tight bar should give up.
	out := make([]binding, 0, len(bs)+1)
	out = append(out, bs[:len(bs)-1]...)
	out = append(out, help)
	return append(out, bs[len(bs)-1])
}

// helpOverlay builds the ? dialog for whichever surface is active.
//
// The board's own table (boardBindings, reached here through backlog.go's
// backlogBindings — the list level's own minor relabeling of it) used to
// render through
// helpRows as one flat, roughly-alphabetical list of ~30 keys, with
// nothing to tell a first-time reader "how do I start this card" from
// "z collapses the branch in place" (round 2 UX drive §7). boardHelpRows
// below is that same slice grouped by what the key is FOR, so the two
// or three keys that actually move a card lead the table instead of
// hiding in it alphabetically.
func (m *Shell) helpOverlay() *helpDialog {
	name, bs := m.activeSurface()
	rows := helpRows(bs)
	if name == "board" {
		rows = boardHelpRows(bs)
	}
	return &helpDialog{title: "keys · " + name, rows: rows}
}

// boardHelpKeyGroup buckets a board-table key by what pressing it is
// for — the grouping boardHelpRows renders as headed sections. Anything
// not named here (there should be nothing: every boardBindings() key is
// covered) reads as the last group rather than silently vanishing.
var boardHelpKeyGroup = map[string]int{
	// moves the card through its workflow, or spends credits on it now
	"enter": 0, "p": 0, "g": 0, "b": 0, "v": 0, "u": 0, "A": 0,
	// reads the card without changing anything
	"s": 1, "d": 1, "t": 1, "i": 1,
	// branch/worktree plumbing
	"r": 2, "m": 2, "z": 2, "c": 2, "o": 2,
}

// boardHelpGroupCount is len(the distinct groups boardHelpKeyGroup
// assigns) plus the unnamed catch-all every other key falls into.
const boardHelpGroupCount = 4

// boardHelpGroupTitles names the sections in boardHelpRows' render
// order; index boardHelpGroupCount-1 (last) is the catch-all — creation,
// navigation, tabs, delete, help, quit — deliberately unnamed as a
// "workflow" or "reading" concern of its own, the way §7 asked for "the
// few keys that move a card, the reading keys, the git plumbing, the
// rest" rather than a fifth invented category.
var boardHelpGroupTitles = [boardHelpGroupCount]string{
	"move this card",
	"read",
	"git",
	"everything else",
}

// boardHelpRows renders bs (boardBindings, or backlogBindings' minor
// relabeling of it) as headed sections instead of one flat list —
// boardHelpKeyGroup decides which, each section keeping bs' own key
// order — followed by the board's glyph legend (boardGlyphLegend,
// board.go), which that file's own owner built for exactly this pass
// (its doc comment: "keymap.go ... outside this package's write scope
// for this pass, so the wiring itself is left to their owner") because
// the ? overlay explained keys only and never the marks a board row can
// carry (§7's other finding). helpDialog (dialogs.go) renders a flat
// [2]string list with no header styling of its own, so a section title
// is a row with an empty key column — the same shape a plain binding
// renders, just with nothing in the key gutter to draw the eye there —
// set off by a blank row rather than true weight; dialogs.go is outside
// this pass's file list, so this is what a heading can be without
// touching it.
//
// ⟲ (the corrective-round counter) and ~ (the estimated-spend prefix)
// are board.go's own two gaps in boardGlyphLegend — thread.go and
// spendformat.go own their meaning, not board.go, so it left them out
// rather than guess. Both are spelled out here anyway: the pass that
// owns those two files can fold them into boardGlyphLegend itself later
// and these two rows become redundant, but until then "? never explains
// ⟲" is exactly the gap this section exists to close.
func boardHelpRows(bs []binding) [][2]string {
	groups := make([][]binding, boardHelpGroupCount)
	for _, b := range bs {
		g, ok := boardHelpKeyGroup[b.key]
		if !ok {
			g = boardHelpGroupCount - 1
		}
		groups[g] = append(groups[g], b)
	}
	var rows [][2]string
	heading := func(title string) {
		if len(rows) > 0 {
			rows = append(rows, [2]string{"", ""})
		}
		rows = append(rows, [2]string{"", "— " + strings.ToUpper(title) + " —"})
	}
	for i, title := range boardHelpGroupTitles {
		if len(groups[i]) == 0 {
			continue
		}
		heading(title)
		rows = append(rows, helpRows(groups[i])...)
	}
	heading("glyphs")
	rows = append(rows, boardGlyphLegend()...)
	rows = append(rows,
		[2]string{"⟲", "rounds burned so far — the badge names which loop it counts (plan, review, or corrective)"},
		[2]string{"~", "estimated — not yet the metered spend"},
	)
	return rows
}

// boardBindings is the board's key table. The bar subset adapts to the
// selected card: enter reads "chat" or "run" by stage, becomes "watch"
// while that feature's agent is running (attach the transcript), and
// "p pause" joins the bar alongside it.
func (m *Shell) boardBindings() []binding {
	// base is the branch f actually lands on — resolved from the selected
	// row when there is one, worktree.DefaultBaseBranchName otherwise
	// (featureRow.baseBranch's own fallback), so the rebase/merge/advance
	// rows below say the trunk's real name instead of asserting "main" on
	// a repo checked out on something else (REVIEW-ux-drive-2026-09-10-
	// round2.md §3.4).
	base := worktree.DefaultBaseBranchName
	if r, ok := m.selected(); ok {
		base = r.baseBranch()
	}
	enter := binding{key: "enter", label: "chat", help: "chat (brainstorm/spec) · run (autonomous)", bar: true}
	pause := binding{key: "p", label: "pause", help: "pause the running agent; else open the dependency picker"}
	peek := binding{key: "t", label: "open", help: "open the card's thread without starting or attaching anything"}
	// "next stage", not "advance". g wore four names at once — "advance" in
	// the board footer, the action inventory and the slash menu, "approve" in
	// the artifact and diff footers, "land on <base>" in the decision block —
	// and this table's own help row was the only place that said it in words
	// (round 3 §5.3). The label now says what the help says.
	advance := binding{key: "g", label: "next stage", help: "move the card to its next stage", bar: true}
	if r, ok := m.selected(); ok && r.F.Stage == domain.StageVerify {
		advance.help = "approve — squash-merge the branch and land it on " + base
	}
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
	// "bounce" is the accelerator's old name for "send it back" — the
	// decision block (decision.go) already says "send it back" for the
	// identical act, and the table §5 settled on that one name everywhere.
	bounce := binding{key: "b", label: "send back", help: "send it back to implement"}
	if r, ok := m.selected(); ok && r.F.Stage == domain.StageImplement {
		bounce.help = "send it back to plan"
	}
	bs := []binding{
		{key: "j/k ↓↑", label: "select", help: "select card"},
		{key: "space", label: "commands", help: "open the command menu — everything that belongs to no card", bar: true},
		{key: "pgup/pgdn", label: "ends", help: "jump to the first/last card"},
		{key: "1..9", label: "jump", help: "jump to card"},
		enter,
		pause,
		peek,
		// s and d are off the bar: the action list reaches both without a
		// key, so the bar can spend its width on the two ways in instead.
		{key: "s", label: "spec", help: "spec — comment, resolve and approve in place"},
		{key: "d", label: "diff", help: "diff — comment, resolve and approve in place"},
		advance,
		bounce,
		{key: "v", label: "verify", help: "run verify checks"},
		{key: "u", label: "budget", help: "set the card's budget (credits; 0 = uncapped)"},
		{key: "o", label: "repo", help: "change the card's managed repository (before worktree)"},
		{key: "a", label: "attach", help: "open a terminal agent in this card's worktree"},
		{key: "A", label: "autopilot", help: "set how far this card runs on its own, and start it"},
		{key: "tab", label: "next tab", help: "cycle the tabs (board, inbox, agent)"},
		{key: "alt+1/2/3", label: "tab", help: "jump straight to board / inbox / agent"},
		{key: "i", label: "inbox", help: "open the needs-you inbox"},
		{key: "r", label: "rebase", help: "rebase branch onto " + base + " (conflicts hand off to an agent)"},
		{key: "m", label: "merge", help: "squash-merge branch into " + base + " (review & approve the drafted message)"},
		{key: "z", label: "squash", help: "collapse the branch to one commit in place (review & approve the drafted message)"},
		{key: "c", label: "clean up", help: "clean up a landed branch"},
		{key: "n", label: "new", help: "new card — feature, bug or research, or paste an issue link to import one", bar: true},
		{key: "B", label: "bug", help: "same screen as n, straight to the bug preset"},
		{key: "R", label: "research", help: "same screen as n, straight to the research preset"},
		{key: "I", label: "ingest", help: "split a document into cards"},
		{key: "G", label: "import", help: "same screen as n, straight to importing a bug from a GitHub issue — browse the repo's issues"},
		{key: "S", label: "sort", help: "toggle severity sort (todo only)"},
		{key: "D", label: "delete", help: "delete card (uppercase: it destroys work)"},
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
		{key: "n", label: "new", help: "new card — feature, bug or research, or paste an issue link to import one", bar: true},
		{key: "B", label: "bug", help: "same screen as n, straight to the bug preset"},
		{key: "R", label: "research", help: "same screen as n, straight to the research preset"},
		{key: "I", label: "ingest", help: "split a document into cards"},
		{key: "G", label: "import", help: "same screen as n, straight to importing a bug from a GitHub issue — browse the repo's issues"},
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
