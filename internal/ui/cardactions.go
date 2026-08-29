package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/workflow"
)

// The dashboard's "next" block (nextsteps.go) is read-only guidance; this
// file turns it into the thing you actually operate — the full menu of
// what → can do to the selected card, focusable and runnable. cardAction
// is deliberately label-first (the interface) with the key demoted to a
// right-aligned accelerator, so the list teaches the shortcut instead of
// requiring it memorized up front.

// sepPrefix marks the danger separator row so the windowing can tell a
// structural line from an action when counting what is hidden. It is a
// zero-width joiner: invisible in the rendered pane, and no action label
// starts with one.
const sepPrefix = "\u200d"

// cardAction is one invokable action on the selected card.
type cardAction struct {
	id     string // stable identifier the Shell switches on to run it
	key    string // accelerator shown right-aligned; "" when there is none
	label  string // imperative, user-facing; the interface
	why    string // one-line explainer shown under the list for the focused row
	danger bool   // destroys work / spends irrecoverably: tinted, sorted last, label gets "…"
	folded bool   // legal here but not advice: hidden behind the fold row
}

// expandID is the synthetic row that opens and closes the folded tail.
// It is not a card verb, so runCardAction answers it directly rather
// than routing it through boardVerb.
const expandID = "expand"

// foldMin is the shortest tail worth folding. Below it the fold row
// would cost as many lines as it saves, and "2 more actions" is a worse
// row than the two actions themselves.
const foldMin = 3

// cardActionList is the focusable list rendered under the selected card
// (→ to focus, ↑/↓ to move, enter to run, ←/esc back to the card list).
type cardActionList struct {
	actions  []cardAction
	cursor   int
	expanded bool // the folded tail is showing
}

// newCardActionList wraps a pre-built action set for focus/cursor
// tracking.
func newCardActionList(actions []cardAction) *cardActionList {
	return &cardActionList{actions: actions}
}

// foldRow builds the row that stands in for the folded tail. Its why
// carries the fact that makes folding safe: nothing was taken away, and
// every hidden action still answers its accelerator from the board.
func foldRow(n int, expanded bool) cardAction {
	if expanded {
		return cardAction{
			id:    expandID,
			label: "fewer actions",
			why:   "fold the rarely-used actions away again",
		}
	}
	return cardAction{
		id:    expandID,
		label: itoa(n) + " more actions",
		why:   "legal here but rarely the move — each still runs from its key without expanding",
	}
}

// rows is what the list navigates and draws: the promoted actions, then
// the fold row whenever there is a tail, then the tail itself once
// expanded. Every cursor operation goes through it, so the fold row is a
// row you land on and press enter — not a caption the cursor skips.
func (l *cardActionList) rows() []cardAction {
	tail := 0
	for _, a := range l.actions {
		if a.folded {
			tail++
		}
	}
	if tail == 0 {
		return l.actions
	}
	out := make([]cardAction, 0, len(l.actions)+1)
	for _, a := range l.actions {
		if !a.folded {
			out = append(out, a)
		}
	}
	out = append(out, foldRow(tail, l.expanded))
	if l.expanded {
		for _, a := range l.actions {
			if a.folded {
				out = append(out, a)
			}
		}
	}
	return out
}

// Move shifts the cursor by delta, clamped to the list's bounds — a list
// you can overshoot into is worse than one that just stops.
func (l *cardActionList) Move(delta int) {
	n := len(l.rows())
	if n == 0 {
		return
	}
	l.cursor += delta
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.cursor > n-1 {
		l.cursor = n - 1
	}
}

// Selected returns the cursor's action, or false when the list is empty.
func (l *cardActionList) Selected() (cardAction, bool) {
	rows := l.rows()
	if l.cursor < 0 || l.cursor >= len(rows) {
		return cardAction{}, false
	}
	return rows[l.cursor], true
}

func (l *cardActionList) Len() int { return len(l.rows()) }

// actionSpec is one candidate action before validity/ordering are
// resolved — the fixed half of a cardAction plus the gate that decides
// whether this card, right now, actually offers it.
type actionSpec struct {
	id     string
	key    string
	label  string
	why    string // fallback why, used when nextActions(in) has nothing better
	danger bool
	valid  bool
}

// promotedActions are the ids that stay above the fold whatever the
// stage: the marquee run/pause pair and the two artifact readers.
// cardActionsFor promotes the rest of the visible tier from its rank map
// — whatever nextActions recommends for this exact state — so the tier
// reads as "what to do now" and the fold holds what is merely legal.
//
// This is also how an action that is valid but never the advice stays
// out of the way without being gated out: merge and rebase on a
// mid-implement card, the envelope, attach, duplicate. Gating them out
// would put the table back at odds with the key handler (see below);
// folding leaves them in the list, on their key, one row away.
var promotedActions = map[string]bool{
	"run": true, "pause": true, "spec": true, "diff": true,
}

// foreignSafeActions are the actions that survive on a card another
// gummi process is driving: reading it, and watching its live stream.
// Everything else would write to a card this board does not own — the
// other process would either fight the change or never see it — so it is
// withheld while the drive lasts, not disabled forever.
//
// foreignBlockedKeys is the same rule stated as accelerators, for the
// board's key handler (shell.go's boardVerb). The two must stay in
// lockstep: an action absent from the list but answered by its key is
// exactly the divergence this table exists to prevent. Board-level keys
// (new card, ingest, inbox, navigation) are not card verbs and are not
// listed — they never touch the driven card.
var foreignSafeActions = map[string]bool{
	"run": true, "transcript": true, "spec": true, "diff": true,
	"inbox": true, "duplicate": true,
}

var foreignBlockedKeys = map[string]bool{
	"p": true, // pause / dependencies
	"v": true, // verify
	"a": true, // raw attach
	"g": true, // advance / decompose
	"b": true, // bounce
	"u": true, // envelope
	"o": true, // repo picker
	"P": true, // route via plan
	"r": true, // rebase
	"m": true, // merge
	"z": true, // squash
	"c": true, // clean
	"x": true, // delete
}

// cardActionsFor is the full set of actions valid for this card right
// now — the focusable list's contents. Ordering: recommended first (the
// same entries nextActions surfaces in the read-only block, in its
// order, with its why text), then the remaining valid actions in board
// order, then danger actions last regardless of recommendation — a
// destructive action never gets to be the thing enter runs by default.
// Promoted actions come before folded ones, each half keeping that
// normal-then-danger shape.
//
// Validity mirrors boardBindings() and the board's key handler exactly
// (shell.go's handleKey): a research card carries no branch, so
// diff/rebase/merge/clean — worktree-gated via workflow.NeedsWorktree —
// never appear for one, the same way keymap.go filters them from the
// status bar and help overlay. Here that filtering doubles as the
// availability check itself, so "not shown" and "not available" can't
// diverge the way they used to when the handler answered a key the
// table didn't advertise.
func cardActionsFor(in nextInput, r featureRow) []cardAction {
	work := workflow.WorkStage(in.kind)
	research := in.kind == domain.KindResearch
	doneStage := in.stage == domain.StageDone
	needsWT := workflow.NeedsWorktree(in.kind, in.stage)

	runLabel, runWhy := runLabelWhy(in)
	pauseLabel, pauseWhy := pauseLabelWhy(in)
	// on a card another process drives, enter can only watch — and can do
	// so at any stage, since what it opens is that run's live stream
	// rather than this board's session.
	if r.DrivenAbroad {
		runLabel = "watch"
		runWhy = fmt.Sprintf("follow the live agent stream — pid %d owns this run", r.Foreign.PID)
	}

	advanceLabel, advanceWhy := "advance", "advance stage (gate; from verify it lands the branch on main)"
	if research && doneStage {
		// FD-081: a done RS card has nothing left to advance — g re-runs
		// decompose instead (keymap.go).
		advanceLabel, advanceWhy = "decompose", "on a done RS: re-run decompose"
	}

	specs := []actionSpec{
		{"run", "enter", runLabel, runWhy, false,
			r.DrivenAbroad || workflow.Interactive(in.stage) || autonomousStage(in.stage)},
		// the gate must stay in lockstep with boardVerb's `p`, which pauses
		// whenever a non-interactive session exists — including a finished
		// one, which p parks. Only the wording varies: "pause the running
		// agent" was a lie on a session that had already stopped.
		{"pause", "p", pauseLabel, pauseWhy, false,
			in.sess != ""},
		{"deps", "p", "dependencies", "open the dependency picker for this card", false,
			in.sess == ""},
		{"transcript", "t", "transcript", "read the session transcript (tool calls and their outputs)", false,
			in.sess != ""},
		{"spec", "s", "spec", "read or annotate the " + artifactNoun(in.kind) + " (tab toggles annotate)", false,
			true},
		{"diff", "d", "diff", "read or annotate the diff (tab toggles annotate)", false,
			needsWT},
		{"advance", "g", advanceLabel, advanceWhy, false,
			!doneStage || research},
		{"bounce", "b", "bounce", "send it back to " + string(work) + " for rework", false,
			in.stage == domain.StageReview || in.stage == domain.StageVerify},
		{"addplan", "P", "add plan", "restore the plan stage on a quick/skip-plan feature (design phase only)", false,
			in.kind != domain.KindBug && r.F.Skip.Plan &&
				(in.stage == domain.StageTodo || in.stage == domain.StageBrainstorm || in.stage == domain.StageSpec)},
		{"verify", "v", "verify", "run verify checks", false,
			research || needsWT},
		{"envelope", "u", "envelope", "set the budget envelope (credits; 0 = uncapped)", false,
			true},
		// the inbox is global, but it is also the recommended action for a
		// card that stopped on budget — nextActions returns exactly one
		// entry there, keyed `i`. Without a spec to land on, that
		// recommendation (and its "top up or park from there" why) was
		// dropped and the card said nothing about why it had stalled.
		{"inbox", "i", "inbox", "open the needs-you inbox", false,
			in.attn != ""},
		{"attach", "a", "attach", "raw-attach the agent CLI in the worktree", false,
			r.HasWorktree},
		{"rebase", "r", "rebase", "rebase branch onto main (conflicts hand off to an agent)", false,
			needsWT},
		{"merge", "m", "merge", "squash-merge branch into main (review & approve the drafted message)", false,
			needsWT && r.HasWorktree && !r.Landed},
		{"clean", "c", "clean up", "branch landed on main — remove the worktree and branch", true,
			needsWT && r.Landed},
		// no accelerator: y is "yes" in the confirm this very action raises,
		// so binding it here made one letter mean two things one keystroke
		// apart. The list and the command menu are how you reach it now.
		{"duplicate", "", "duplicate", "duplicate as a fresh card in todo (this card stays)", false,
			true},
		{"delete", "x", "delete", "remove the worktree, branch, and record — irrecoverable", true,
			true},
	}

	// seed ordering and why text from the ranked suggestions: first
	// occurrence of a key wins, so the recommendation (steps[0]) always
	// out-ranks a same-keyed entry appended later in the same call.
	// a card another process drives has no local recommendation to make:
	// every step nextActions ranks is about driving it here. Watching is
	// the only move, and the run action already says so.
	var steps []nextAction
	if !r.DrivenAbroad {
		steps = nextActions(in)
	}
	rank := make(map[string]int, len(steps))
	why := make(map[string]string, len(steps))
	for i, st := range steps {
		if _, ok := rank[st.key]; !ok {
			rank[st.key] = i
			why[st.key] = st.why
		}
	}

	type ordered struct {
		action cardAction
		order  int
	}
	var normal, danger []ordered
	for i, sp := range specs {
		if !sp.valid {
			continue
		}
		if r.DrivenAbroad && !foreignSafeActions[sp.id] {
			continue
		}
		w := sp.why
		order := len(steps) + i // canonical fallback order, after every recommendation
		folded := !promotedActions[sp.id]
		if r, ok := rank[sp.key]; ok {
			w = why[sp.key]
			order = r
			folded = false // recommended for this state: it belongs on screen
		}
		label := sp.label
		if sp.danger {
			label += "…"
		}
		o := ordered{cardAction{id: sp.id, key: sp.key, label: label, why: w, danger: sp.danger, folded: folded}, order}
		if sp.danger {
			danger = append(danger, o)
		} else {
			normal = append(normal, o)
		}
	}
	sort.SliceStable(normal, func(a, b int) bool { return normal[a].order < normal[b].order })
	sort.SliceStable(danger, func(a, b int) bool { return danger[a].order < danger[b].order })

	out := make([]cardAction, 0, len(normal)+len(danger))
	for _, o := range normal {
		out = append(out, o.action)
	}
	for _, o := range danger {
		out = append(out, o.action)
	}

	// hoist the promoted half in front, each half keeping the ordering
	// settled above. A tail too short to be worth a fold row is simply
	// unfolded — a card in todo offers little enough that hiding any of
	// it buys nothing.
	head := make([]cardAction, 0, len(out))
	tail := make([]cardAction, 0, len(out))
	for _, a := range out {
		if a.folded {
			tail = append(tail, a)
		} else {
			head = append(head, a)
		}
	}
	if len(tail) < foldMin {
		for i := range out {
			out[i].folded = false
		}
		return out
	}
	return append(head, tail...)
}

// runLabelWhy derives the enter action's label and fallback why — the
// same chat/run/watch adaptation boardBindings() makes for the status bar
// (shell.go), plus the hasAsk nuance nextsteps.go's StateRunning branch
// carries. The fallback is only seen when nextActions(in) has nothing for
// this state (a queued or busy-without-ask run, or a finished autonomous
// stage with no live session) — everywhere else cardActionsFor's why
// override wins.
func runLabelWhy(in nextInput) (label, why string) {
	switch {
	case workflow.Interactive(in.stage):
		return "chat", "talk through the draft with the agent"
	case in.hasAsk:
		return "answer the agent", "it asked a question and is blocked on your reply"
	case in.sess == engine.StateRunning:
		return "watch", "watch the running agent (scrollable transcript)"
	case in.sess == engine.StateQueued:
		return "run", "queued — waiting for a free slot"
	default:
		return "run", "re-run " + string(in.stage) + " (starts a fresh session)"
	}
}

// pauseLabelWhy words the p action for the session state it will act on.
// boardVerb pauses any non-interactive session, so this row appears for
// queued, running, paused and finished sessions alike — and describing
// all four as "pause the running agent" was wrong for three of them.
func pauseLabelWhy(in nextInput) (label, why string) {
	switch in.sess {
	case engine.StateQueued:
		return "pause", "drop it out of the queue before it starts"
	case engine.StateRunning:
		return "pause", "stop the running agent mid-turn, freeing its slot"
	default:
		// paused or finished: p parks it so the loop stops offering it
		return "park", "park the settled session so it stops asking"
	}
}

// markerWidth is the width of the per-row cursor marker ("▸ " / "  ");
// keyGap is the minimum space between the widest label and the key
// column. Together with the widest label and key they give the list its
// own width, independent of the pane it is drawn into.
const (
	markerWidth = 2
	keyGap      = 2
)

// keyColumn is the width a label/accelerator list needs to align its keys
// against its own content: marker + widest label + gap + widest key,
// never wider than the available width w.
//
// Aligning to w instead — the pane the list happens to sit in — is what
// this replaces. The dashboard's main pane is 60+ columns, so "spec" sat
// at column 2 and its "s" at column 68, with nothing in between for the
// eye to follow. The column is worth keeping (it is what lets you scan
// the accelerators vertically and graduate from reading labels to typing
// letters); it just has to be a column of the list, not of the terminal.
func keyColumn(w int, labels, keys []string) int {
	labelW, keyW := 0, 0
	for _, l := range labels {
		labelW = max(labelW, ansi.StringWidth(l))
	}
	for _, k := range keys {
		keyW = max(keyW, ansi.StringWidth(k))
	}
	return min(markerWidth+labelW+keyGap+keyW, w)
}

// View renders the action list: cursor marker left, label left, key
// right-aligned in a column sized to the list's own content (keyColumn,
// capped at w), danger rows tinted and set off by a separator,
// and a trailing explainer for whatever row the cursor sits on (shown
// whether or not the list itself has focus — the same continuity the old
// read-only "next" block gave the top suggestion).
//
// maxRows caps the rendered rows: the dashboard also carries stage,
// branch, budget, spend, the live activity feed and the history, and a
// card mid-implement offers a dozen actions — unbounded, the list pushed
// all of that off a short terminal. The window follows the cursor so the
// row you are on is always visible, and the count of what is hidden is
// stated rather than silently dropped. maxRows <= 0 means no cap.
func (l *cardActionList) View(s *theme.Styles, w, maxRows int, focused bool) string {
	rows := l.rows()
	if len(rows) == 0 {
		return ""
	}
	labels := make([]string, len(rows))
	keys := make([]string, len(rows))
	for i, a := range rows {
		labels[i], keys[i] = a.label, a.key
	}
	width := keyColumn(w, labels, keys)

	var lines []string
	cursorLine := 0
	for i, a := range rows {
		// a rule opens each contiguous danger run rather than only the
		// first: the fold can put a recommended danger (clean up, on a
		// landed card) above the fold row and delete below it, and the
		// second run needs the boundary just as much as the first.
		if a.danger && (i == 0 || !rows[i-1].danger) {
			// the rule marks the danger boundary within the list, so it spans
			// the list's width rather than the pane's.
			lines = append(lines, sepPrefix+s.Separator.Render(strings.Repeat("─", max(min(width, 40), 0))))
		}
		// the marker shows on the cursor row either way — the explainer
		// below describes that row, so hiding the marker while unfocused
		// left the explanation pointing at nothing. The band behind it is
		// what says whether this list holds focus or is only remembering
		// where the cursor was.
		marker := "  "
		banded := false
		if i == l.cursor {
			cursorLine = len(lines) // the separator shifts rows, so track it here
			marker = s.BandMarker(focused)
			banded = true
		}
		keyStr := s.KeyHint.Render(a.key)
		avail := width - ansi.StringWidth(marker) - ansi.StringWidth(keyStr) - 1
		label := a.label
		if ansi.StringWidth(label) > avail {
			label = ansi.Truncate(label, max(avail, 0), "…")
		}
		pad := avail - ansi.StringWidth(label)
		if pad < 0 {
			pad = 0
		}
		labelStyle := s.Base
		switch {
		case a.danger:
			labelStyle = s.Destructive
		case a.id == expandID:
			// structural, not an action: it reads one tier down from the
			// rows it stands for. On the band s.Faint lands near 1.2:1, so
			// the banded row steps up to Muted rather than vanishing.
			labelStyle = s.Faint
			if banded {
				labelStyle = s.Muted
			}
		}
		row := marker + labelStyle.Render(label) + strings.Repeat(" ", pad) + " " + keyStr
		if banded {
			row = s.Band(row, width, focused)
		}
		lines = append(lines, row)
	}
	// the "…N more" and "↳ why" lines are part of the block, so they come
	// out of the same budget the caller granted — otherwise the block
	// renders maxRows+2 and overruns the reserve the dashboard computed.
	rowBudget := maxRows
	if rowBudget > 0 {
		rowBudget = max(rowBudget-2, 1)
	}
	shown := lines
	hidden := 0
	if rowBudget > 0 && len(lines) > rowBudget {
		shown = windowLines(lines, cursorLine, rowBudget)
		// count hidden *actions*, not hidden lines: the danger separator
		// is a line but not something ↑↓ can reach, so counting it made
		// the tally one too many whenever it fell outside the window.
		shownActions := 0
		for _, l := range shown {
			if !strings.HasPrefix(l, sepPrefix) {
				shownActions++
			}
		}
		hidden = len(rows) - shownActions
	}
	// copy before appending: windowLines returns a slice of lines, so
	// appending in place would scribble over the row after the window.
	out := make([]string, 0, len(shown)+2)
	out = append(out, shown...)
	if hidden > 0 {
		out = append(out, s.Faint.Render(fmt.Sprintf("  …%d more — ↑↓ to reach them", hidden)))
	}
	if a, ok := l.Selected(); ok {
		out = append(out, s.Faint.Render(ansi.Truncate("  ↳ "+a.why, w, "…")))
	}
	return strings.Join(out, "\n")
}
