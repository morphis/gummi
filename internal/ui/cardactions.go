package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/publish"
	"github.com/morphis/gummi/internal/ui/theme"
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
// stage. The action pop-over has no permanent promotions: cardActionsFor
// promotes the visible tier only from its rank map, so whatever
// nextActions recommends for this exact state is what reads as "what to
// do now" and the fold holds what is merely legal.
//
// This is also how an action that is valid but never the advice stays
// out of the way without being gated out: merge and rebase on a
// mid-implement card, the envelope, attach, duplicate. Gating them out
// would put the table back at odds with the key handler (see below);
// folding leaves them in the list, on their key, one row away.
var promotedActions = map[string]bool{}

// foreignSafeActions are the actions that survive on a card this board
// may watch but not steer (featureRow.watchOnly): reading it, and
// watching its live stream. Everything else would write to a card this
// board does not own — another gummi process would either fight the
// change or never see it, and a goal's lead, which starts, answers and
// lands its own cards, would fight it from inside this very process — so
// they are withheld while the drive lasts, not disabled forever.
//
// handOffHelp is the one sentence the hand-off verb wears everywhere —
// the action inventory, the board's key table, the ? overlay. It names
// what is kept before what is lost, because keeping the branch is the
// whole reason anyone reaches for it.
const handOffHelp = "close it without landing — nothing reaches the base"

// foreignBlockedKeys is the same rule stated as accelerators, for the
// board's key handler (shell.go's boardVerb). The two must stay in
// lockstep: an action absent from the list but answered by its key is
// exactly the divergence this table exists to prevent. Board-level keys
// (new card, ingest, inbox, navigation) are not card verbs and are not
// listed — they never touch the driven card.
var foreignSafeActions = map[string]bool{
	"run": true, "spec": true, "diff": true,
	"inbox": true, "duplicate": true, "ask": true,
}

var foreignBlockedKeys = map[string]bool{
	"p": true, // pause / dependencies
	"v": true, // verify
	"a": true, // raw attach
	"g": true, // advance / decompose
	"b": true, // bounce
	"u": true, // envelope
	"o": true, // repo picker
	"r": true, // rebase
	"h": true, // hand off
	"m": true, // merge
	"z": true, // squash
	"c": true, // clean
	"D": true, // delete
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
// diff/rebase/merge/clean — branch-gated — never appear for one, the
// same way keymap.go filters them from the
// status bar and help overlay. Here that filtering doubles as the
// availability check itself, so "not shown" and "not available" can't
// diverge the way they used to when the handler answered a key the
// table didn't advertise.
func cardActionsFor(in nextInput, r featureRow) []cardAction {
	research := in.kind == domain.KindResearch
	// A retired workflow card (anything but a session) is read-only: it
	// keeps the verbs that read it and put it away, and none that would
	// run it — "not shown" and "not available" must not diverge.
	freeform := in.kind == domain.KindFreeform
	doneStage := in.stage == domain.StageDone
	// carries a branch: a session from its mint, a retired card only if
	// its worktree is still here. A research card never had one.
	needsWT := !research && (freeform || r.HasWorktree)

	// A landing is offered only where one would go through: the floor
	// every landing door reads (merge.go's landingRefusal, carried in as
	// in.landRefused) and the one order a stack imposes (the card below
	// lands first). Each withheld entry still answers its key with the
	// refusal's own sentence (boardVerb), and the decision says why
	// nothing lands yet ("lands after …").
	landable := in.landRefused == "" && in.stackBlocker == ""
	// A closed card — done, or its branch already on its base — has no
	// work left for the verbs that steer work. Each one's key refuses the
	// same way (closedRefusal).
	closed := in.closed()

	// A session has no noun of its own (artifactNoun(KindFreeform) is
	// empty), but it may still have a document to open: its own spec,
	// the same predicate the tab bar and alt+s answer from
	// (cardHasSessionSpec), so the three cannot disagree about whether
	// this card has one.
	sessionSpec := cardHasSessionSpec(r)
	specLabel := artifactNoun(in.kind)
	if sessionSpec {
		specLabel = "spec"
	}

	specs := []actionSpec{
		// on a card something else drives, enter can only watch
		{
			"run", "enter", "watch", "follow the live agent stream — " + r.watchDriver() + " owns this run", false,
			r.watchOnly(),
		},
		// a turn in flight is what p stops
		{
			"pause", "p", "stop this turn", "stop it mid-turn — whatever it has written stays in the worktree, uncommitted", false,
			in.freeformBusy,
		},
		{
			// the label is the interface, so it takes the card's own noun
			// for its document — "spec" for a session that has one. The id
			// stays "spec": that is what the Shell switches on, not what a
			// reader sees.
			"spec", "s", specLabel, "read the " + specLabel, false,
			!freeform || sessionSpec,
		},
		{
			"diff", "d", "diff", "read or comment on the diff (tab toggles commenting)", false,
			needsWT,
		},
		{
			// a session runs on the agent and model its person picked
			// (DESIGN §19.8). Keyless like the other setting changes; the
			// picker asks the agent what models it provides, which is IO,
			// so it is fetched off the loop (sessionmodel.go).
			"model", "", "model", "switch the agent and model this session runs on — from its next turn, with the conversation so far", false,
			freeform && in.stage == domain.StageOpen && in.agentWired && !r.watchOnly(),
		},
		{
			// the id and the dialog it opens (envelope.go) both keep the old
			// name internally; the label and why are the only parts a reader
			// sees.
			"envelope", "u", "budget", "set the card's budget (dollars; 0 = uncapped)", false,
			freeform && envelopeRefusal(r) == nil,
		},
		// no accelerator: `ask` is a word you type on the composer. On a
		// session every line already reaches the agent that can answer
		// AND act, so a second, read-only channel beside it would be a
		// worse version of what the composer already does.
		{
			"ask", "", "ask", "ask the card's agent a question — read-only, never steers", false,
			!freeform,
		},
		{
			"inbox", "i", "inbox", "open the needs-you inbox", false,
			in.attn != "",
		},
		// no accelerator: u is the budget DIALOG on the board, and this
		// is the one-keystroke top-up the inbox reaches with its own u,
		// offered at the stop instead of behind a pointer to another tab.
		{
			"topup", "", "top up", "raise the budget so the session can carry on", false,
			in.attn == attnBudget,
		},
		{
			"attach", "a", "attach", "open a terminal agent in this card's worktree", false,
			r.HasWorktree,
		},
		{
			"rebase", "r", "rebase", "rebase branch onto " + r.baseBranch() + " (conflicts go to an agent)", false,
			needsWT && !r.F.MainCheckout && rebaseRefusal(r) == nil,
		},
		{
			// Only the person can say when a card's work is finished, and
			// saying so IS the ending (DESIGN §19). Never while a turn, or
			// a workflow run, is writing the branch it keeps
			// (Engine.RunHoldsClose). A main-checkout session has no
			// worktree row to require: the work it keeps is loose in the
			// checkout, and that is what the confirm names.
			"handoff", "h", "hand off", handOffHelp, false,
			((needsWT && r.HasWorktree) || in.mainCheckout) &&
				!r.Landed && !in.freeformBusy && r.OpenRun == nil && !closed,
		},
		{
			// never on a card linked to a pull request, which lands there
			// (prepareMerge refuses it with the two ways that do)
			"merge", "m", "merge", mergeHelp(r.F.Kind, r.baseBranch(), r.F.Offers(domain.LandMerge)), false,
			needsWT && r.HasWorktree && !r.Landed && !in.freeformBusy && (!doneStage || r.F.HandedOff()) &&
				landable && r.F.PullRequest.Empty(),
		},
		{
			"squash", "z", "squash", "collapse the branch to one commit in place (review & approve the drafted message)", false,
			needsWT && r.HasWorktree && !r.Landed && !in.freeformBusy,
		},
		{
			// keyless like the other setting changes: it defines a
			// heartbeat (DESIGN §19.9) aimed at this card and turns it on,
			// the same schedule the Schedules view defines by hand
			"heartbeat", "", "heartbeat", "come back to this session on a cadence — a recurring turn, spent from its own budget", false,
			freeform && in.stage == domain.StageOpen && in.agentWired && !r.watchOnly(),
		},
		{
			// keyless, like objective: the TUI hands it to the composer as
			// /rename and the web page asks for the title (sessionname.go)
			"rename", "", "rename…", "retitle the session — its branch takes the title's words too, unless it has been pushed", false,
			freeform && in.stage == domain.StageOpen && in.agentWired && !r.watchOnly() && !r.F.IsChild(),
		},
		{
			// keyless, like heartbeat: the session keeps going on its own
			// until an auditor finds the objective met (DESIGN §19.11). The
			// TUI hands it to the composer as /objective; the web page asks
			// for the text, or a verb its strip sends
			"objective", "", "objective…", "keep the session going until something is done — an auditor judges each turn, and gummi sends the next", false,
			freeform && in.stage == domain.StageOpen && in.agentWired && !r.watchOnly(),
		},
		{
			"clean", "c", "clean up", r.cleanHelp(), true,
			needsWT && r.cleanable(),
		},
		// a session follows a pull request that already exists for its
		// branch — one opened on GitHub, or by hand — once it is linked
		{
			"prlink", "", "link PR…", "link this session to a GitHub pull request that already exists, by its URL or number, to follow its review and checks", false,
			freeform && !r.F.Home && !r.F.MainCheckout && !r.Landed && r.F.PullRequest.Empty() && !r.watchOnly(),
		},
		// unlinking destroys one row (PullRequestRef), not any diff
		// annotations already pulled from it — so it is not danger:true;
		// the confirm names what changes instead.
		{
			"prunlink", "", "unlink PR", "clear the linked PR — the card becomes locally landable again", false,
			!r.F.PullRequest.Empty(),
		},
		// publishing (DESIGN §22): keyless, offered only where gh is
		// signed in and the card is one publish.Refusal lets through on
		// what the row already knows; the confirm reads the rest
		{
			"prcreate", "", "open pull request…", "push the branch and open its pull request on GitHub — you confirm where it goes first", false,
			publishable(in, r) && r.F.PullRequest.Empty(),
		},
		{
			// a session spanning repositories (DESIGN §27.7): one PR in
			// each, the descriptions naming one another
			publishSpanID, "", "open pull requests in every repository…", "push each repository's branch and open a pull request in each, every description naming the others — you confirm each first", false,
			publishable(in, r) && r.F.PullRequest.Empty() && spansUnlanded(r),
		},
		{
			// a person's standing allowance (DESIGN §22.14): the session's
			// own pr_* tools then publish with no confirm of their own
			agentPublishID, "", agentPublishLabel(in.agentPublish), agentPublishDetail(in.agentPublish), false,
			agentPublishOffered(in, r),
		},
		{
			// a person's switch for specs and workflow runs, this session
			// only; it wins over the workspace setting
			sessionWorkflowsID, "", sessionWorkflowsLabel(in.workflows), sessionWorkflowsDetail(in.workflows), false,
			sessionWorkflowsOffered(in, r),
		},
		{
			sessionWorkflowsResetID, "", "follow the workspace setting for workflows", "drop this session's own switch; the workspace setting decides again", false,
			sessionWorkflowsOffered(in, r) && in.workflowsSet,
		},
		{
			"push", "", "push to GitHub…", "push the branch's new commits to its remote", false,
			publishable(in, r),
		},
		{
			"prready", "", "mark PR ready…", "mark the linked draft PR ready for review", false,
			publishable(in, r) && !r.F.PullRequest.Empty(),
		},
		{
			"prdraft", "", "PR back to draft…", "turn the linked PR back into a draft", false,
			publishable(in, r) && !r.F.PullRequest.Empty(),
		},
		// prpull rises out of the fold via nextActions' ranking (see
		// nextsteps.go) in the states where pulling the review is the
		// move, without spending a letter that has to be right forever.
		{
			"prpull", "", "pull PR review", "read the PR's review comments back onto the diff", false,
			!r.F.PullRequest.Empty() && needsWT,
		},
		// read from GitHub by gummi, with the person's own gh: a session is
		// never lent the credential to look for itself
		{
			"prchecks", "", "send failing checks", prChecksDetail(), false,
			!r.F.PullRequest.Empty() && needsWT && freeform,
		},
		// no accelerator: y is "yes" in the confirm this very action raises,
		// so binding it here made one letter mean two things one keystroke
		// apart. The list and the command menu are how you reach it now.
		{
			"duplicate", "", "duplicate", "duplicate as a fresh session (this card stays)", false,
			freeform,
		},
		// Offered only once the card has ended, which is the whole point
		// of it: before then, work that is not this card's opens a new
		// card through the composer. Afterwards this is the only door
		// left. Keyless for the same reason duplicate is.
		{
			"newbug", "", "start a follow-up session",
			"a fresh session carrying this card's spec, branch and thread", false,
			(doneStage || r.Landed) && in.kind != domain.KindGoal,
		},
		{
			"delete", "D", "delete", "remove the worktree, branch, and record — irrecoverable", true,
			true,
		},
	}

	specs = append(specs, repoActions(in, r)...)

	// seed ordering and why text from the ranked suggestions: first
	// occurrence of a key wins, so the recommendation (steps[0]) always
	// out-ranks a same-keyed entry appended later in the same call.
	// a card someone else drives has no recommendation to make here:
	// every step nextActions ranks is about driving it from this board.
	// Watching is the only move, and the run action already says so.
	var steps []nextAction
	if !r.watchOnly() {
		steps = nextActions(in)
	}
	rank := make(map[string]int, len(steps))
	detail := make(map[string]string, len(steps))
	for i, st := range steps {
		if _, ok := rank[st.id]; !ok {
			rank[st.id] = i
			detail[st.id] = st.detail
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
		if r.watchOnly() && !foreignSafeActions[sp.id] {
			continue
		}
		w := sp.why
		order := len(steps) + i // canonical fallback order, after every recommendation
		folded := !promotedActions[sp.id]
		if r, ok := rank[sp.id]; ok {
			w = detail[sp.id]
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

// closedRefusal is the answer a verb that steers work gets on a card whose
// work is over — at done, or with its branch already on its base — or nil
// on any other card. what says what that leaves for the verb to do. The
// menu withholds the same verbs on the same test (cardActionsFor's
// closed), so a row that is not listed is a key that says why.
func closedRefusal(r featureRow, what string) *noticeMsg {
	if r.F.Stage != domain.StageDone && !r.Landed {
		return nil
	}
	how := "is done"
	if r.Landed {
		how = "has landed on " + r.baseBranch()
	}
	return &noticeMsg{text: r.F.Name() + " " + how + " — " + what, isErr: true, id: r.F.ID}
}

// envelopeRefusal is closedRefusal for the budget: a closed card spends
// nothing more, except a done research card, whose g re-runs decompose.
func envelopeRefusal(r featureRow) *noticeMsg {
	if r.F.Kind == domain.KindResearch && r.F.Stage == domain.StageDone {
		return nil
	}
	return closedRefusal(r, "nothing is left for a budget to pay for")
}

// rebaseRefusal is closedRefusal for the rebase, which a handed-off card
// still has a use for: its branch can be landed after all, and landing it
// may want it on the base's tip first.
func rebaseRefusal(r featureRow) *noticeMsg {
	if r.F.HandedOff() && !r.Landed {
		return nil
	}
	return closedRefusal(r, "there is nothing left to rebase")
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
	// the explainer belongs to the row it describes, not to whichever row
	// happens to render last — insert it right after the highlighted row's
	// own line so it travels with that row through folding and windowing
	// alike (BG-053). It carries sepPrefix for the same reason the danger
	// rule does: so the counting below can tell it apart from an action.
	if a, ok := l.Selected(); ok {
		why := sepPrefix + s.Faint.Render(ansi.Truncate("  ↳ "+a.why, w, "…"))
		insertAt := cursorLine + 1
		lines = append(lines[:insertAt:insertAt], append([]string{why}, lines[insertAt:]...)...)
	}
	// the "…N more" line is the only thing left outside the windowed
	// block now that the why line moved inside it, so the reserve the
	// caller granted only has to hold room for that one line.
	rowBudget := maxRows
	if rowBudget > 0 {
		rowBudget = max(rowBudget-1, 1)
	}
	shown := lines
	hidden := 0
	if rowBudget > 0 && len(lines) > rowBudget {
		shown = windowLines(lines, cursorLine, rowBudget)
		// count hidden *actions*, not hidden lines: the danger separator
		// and the why line are lines but not something ↑↓ can reach, so
		// counting them made the tally too high whenever they fell
		// outside the window.
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
	out := make([]string, 0, len(shown)+1)
	out = append(out, shown...)
	if hidden > 0 {
		out = append(out, s.Faint.Render(fmt.Sprintf("  …%d more — ↑↓ to reach them", hidden)))
	}
	return strings.Join(out, "\n")
}

// cardActionsDialog gives the card's complete legal action inventory a
// visible keyboard surface. The list itself remains the source of cursor,
// folding, filtering, and rendering behaviour; the dialog only provides a
// frame and routes modal keys to it.
type cardActionsDialog struct {
	feature string
	list    *cardActionList
	onMove  func(cursor int, expanded bool)
	onRun   func(cardAction) tea.Cmd
}

func newCardActionsDialog(feature string, list *cardActionList, onMove func(int, bool), onRun func(cardAction) tea.Cmd) *cardActionsDialog {
	// Folding protects an inline list's scarce rows. A modal is the explicit
	// request for the whole inventory, so expose every action and let View's
	// height window state exactly how many remain off-screen.
	for i := range list.actions {
		list.actions[i].folded = false
	}
	list.expanded = false
	list.cursor = clamp(list.cursor, 0, list.Len()-1)
	return &cardActionsDialog{feature: feature, list: list, onMove: onMove, onRun: onRun}
}

// ID implements overlay.Dialog.
func (d *cardActionsDialog) ID() string { return "card-actions" }

// HandleKey implements overlay.Dialog.
func (d *cardActionsDialog) HandleKey(key tea.KeyPressMsg) (bool, tea.Cmd) {
	switch key.String() {
	case "esc":
		return true, nil
	case "up", "k":
		d.list.Move(-1)
		d.onMove(d.list.cursor, d.list.expanded)
	case "down", "j":
		d.list.Move(1)
		d.onMove(d.list.cursor, d.list.expanded)
	case "enter":
		a, ok := d.list.Selected()
		if !ok {
			return true, nil
		}
		if a.id == expandID {
			d.list.expanded = !d.list.expanded
			d.list.cursor = clamp(d.list.cursor, 0, d.list.Len()-1)
			d.onMove(d.list.cursor, d.list.expanded)
			return false, nil
		}
		return true, d.onRun(a)
	}
	return false, nil
}

// View implements overlay.Dialog.
func (d *cardActionsDialog) View(s *theme.Styles, w, h int) string {
	frameW := min(max(w-4, 8), 76)
	innerW := max(frameW-6, 1) // border plus two-cell padding on each side
	listRows := max(h-8, 3)
	footer := "↑↓ choose · enter run · esc back to the line"
	if ansi.StringWidth(footer) > innerW {
		footer = "↑↓ · enter run · esc back"
	}
	body := s.DialogTitle.Render("actions · "+d.feature) + "\n\n" +
		d.list.View(s, innerW, listRows, true) + "\n\n" +
		s.Faint.Render(footer)
	return s.DialogFrame.Width(frameW).Render(body)
}

// mergeHelp describes the merge action: a goal lands as one merge commit
// over its cards' commits; every other card lands as one squash commit, or
// as a merge commit keeping its own commits when choice is set (the person
// picks which in the message dialog).
func mergeHelp(kind domain.Kind, base string, choice bool) string {
	if kind == domain.KindGoal {
		return "merge the goal branch into " + base + " over its cards' commits (review & approve the drafted message)"
	}
	what := landWhy(choice, "squash-merge branch into "+base, "land branch into "+base)
	if kind == domain.KindFreeform {
		// prepareMerge refuses loose work on a session: its commits are
		// the person's own, and the reader should know before pressing
		return what + " — uncommitted work has to be committed first, with your own message (" + landMessageNote(choice) + ")"
	}
	return what + " (" + landMessageNote(choice) + ")"
}

// landMessageNote says who writes a landing's message. A squash's is the
// drafted one the reader approves; a merge commit takes git's own, so where
// the card may choose, the note names both.
func landMessageNote(choice bool) string {
	if choice {
		return "a squash takes the drafted message, reviewed and approved; a merge commit takes git's own"
	}
	return "review & approve the drafted message"
}

// landWhy picks the wording of a landing. A card that may keep its commits
// lands as one squash commit or a merge commit, which the message dialog's
// toggle chooses, so its wording names both; any other card keeps squash.
func landWhy(choice bool, squash, either string) string {
	if !choice {
		return squash
	}
	return either + " — one squash commit, or a merge commit keeping its commits (chosen with the message)"
}

// publishable is the row-level half of publish.Refusal: the board offers
// the publish rows only where the card could be published at all.
func publishable(in nextInput, r featureRow) bool {
	if !in.publish || !r.HasWorktree || r.Landed || r.watchOnly() {
		return false
	}
	return publish.Refusal(publish.Card{F: &r.F, Busy: in.freeformBusy}) == nil
}
