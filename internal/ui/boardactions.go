package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// This file joins the two action surfaces to the board's key handler.
// Both a card action and a command carry the board key they stand for,
// so invoking one is boardKey(key) — the guards each case already
// carries (a research card refusing a merge, a card with no worktree
// refusing a diff) run exactly once, in one place, whichever surface
// the user came through.

// cardActions builds the selected card's action list, positioned at the
// stored cursor. It is rebuilt on every use rather than cached: the
// board reloads rows often, and a cached list would keep offering
// actions for a stage the card has already left.
func (m *Shell) cardActions() *cardActionList {
	r, ok := m.selected()
	if !ok {
		return newCardActionList(nil)
	}
	actions := cardActionsFor(m.nextInputFor(r), r)
	if !r.F.IsFreeform() {
		// a session's menu switches the model instead of the profile
		// (DESIGN §19.8) — the web face's inventory already withholds
		// the profile row from one, so the terminal cannot offer what
		// the page will not
		actions = append(actions, m.cardProfileActions(r.F.Stage)...)
	}
	l := newCardActionList(actions)
	l.expanded = m.actionsExpanded
	if n := l.Len(); n > 0 {
		l.cursor = clamp(m.actionCursor, 0, n-1)
	}
	return l
}

// cardProfileActions returns the profile-switch entry for the selected
// card — one keyless cardAction when there is a profile picker to open,
// nil otherwise. Kept at the Shell layer, not inside the pure
// cardActionsFor, because it needs m.engine to know whether there is
// anything to pick from and cardActionsFor takes no engine at all (see
// TestCardActionsDialogWideGolden/NarrowGolden, which call it directly
// and would need no regeneration only if this row is appended here
// instead). It applies uniformly to whichever card is selected — the
// stage only feeds the availability check, never the row itself — so it
// takes a stage rather than a featureRow.
func (m *Shell) cardProfileActions(stage domain.Stage) []cardAction {
	if m.engine == nil || len(m.engine.CardProfiles(stage)) == 0 {
		return nil
	}
	return []cardAction{{
		id:    "profile",
		label: "profile",
		why:   "switch this card's profile — a live session restarts under it",
	}}
}

// blurActions hands the arrow keys back to the cards and refolds the
// list. The fold exists so the pane stays short while you are reading
// it, so an expansion is scoped to the visit that asked for it.
func (m *Shell) blurActions() {
	m.actionFocused = false
	m.actionsExpanded = false
}

// moveAction steps the action cursor, clamping through the live list so
// a shrunk list (an action that stopped applying) can't strand it past
// the end.
func (m *Shell) moveAction(delta int) {
	l := m.cardActions()
	l.Move(delta)
	m.actionCursor = l.cursor
}

// syncActionFocus returns focus to the cards and rewinds the action
// cursor whenever the selected card changed. It keys off the card's
// identity rather than the selection index, because a board reload can
// move the selection onto a different card with no keypress at all —
// and a cursor left on "delete" then belongs to the wrong card.
func (m *Shell) syncActionFocus() {
	var id domain.FeatureID
	if r, ok := m.selected(); ok {
		id = r.F.ID
	}
	if id == m.actionCard {
		return
	}
	m.actionCard = id
	m.actionCursor = 0
	m.blurActions()
}

// globalCommands is the space menu's contents: the board-level actions
// that belong to no particular card, plus — when a card page is open —
// that same card's own action inventory (cardactions.go), via
// cardCommands below.
//
// On the plain board dashboard the card actions stay left out: they are
// already on screen in the dashboard's list there, so repeating them
// would only make the menu longer without making anything more
// reachable. The card's thread page is a different surface — nothing of
// the inventory is visible there until ↑ opens it as a modal
// (openCardActions), which "/" cannot reach — so that is where this menu
// and the inventory actually diverge, and merging them is what makes
// "/park", "/diff" and "/envelope" alike find something instead of "no
// commands match" (verbs already short-circuit the menu entirely before
// this ever runs — see submitThreadInput's routeVerb — so this merge is
// only what carries the words verbs.go never covered: envelope,
// duplicate, delete, dependencies, gate, and the rest of the inventory).
//
// A command's id is the board key it stands for, so onRun is boardKey
// itself and there is no second mapping to drift — except the handful of
// card actions with no key of their own (cardCommands' doc has why).
func (m *Shell) globalCommands() []command {
	attached := m.attached()
	cmds := []command{
		{id: "n", name: "new", label: "New card", key: "n", available: attached},
		{id: "B", name: "bug", label: "New bug", key: "B", available: attached},
		{id: "R", name: "research", label: "New research card", key: "R", available: attached},
		// No accelerator: the board's lowercase letters are spent, and the
		// kind row of `n` already reaches this. The entry exists so the word
		// itself is in the vocabulary — a reader who knows what they want
		// types "/freeform" instead of opening a dialog and cycling a row.
		{id: "new-freeform", name: "freeform", label: "New freeform card — no workflow, its own branch", key: "", available: attached},
		{id: "I", name: "ingest", label: "Split a document into cards", key: "I", available: attached && m.engine != nil},
		{id: "G", name: "import", label: "Import a GitHub issue as a bug", key: "G", available: attached && m.engine != nil},
		{id: "i", name: "inbox", label: "Open the needs-you inbox", key: "i", available: attached},
		{id: "L", name: "schedules", label: "Schedules and heartbeats", key: "L", available: attached},
		{id: "S", name: "sort", label: "Sort todo by severity", key: "S", available: attached},
		{id: "settings", name: "settings", label: "Settings — name this gummi instance", key: "", available: true},
		{id: "?", name: "keys", label: "Show the keys for this surface", key: helpKeyFor(m.cardOpen), available: true},
		{id: "q", name: "quit", label: "Quit gummi", key: "q", available: true},
	}
	if m.cardOpen {
		cmds = append(cmds, m.cardCommands(cmds)...)
	}
	return cmds
}

// cardCommands converts the selected card's action inventory — the exact
// list ↑ opens as a modal (cardActionsFor) — into command-menu entries.
// cardActionsFor already filters to what is valid for this card right
// now, so every entry is available.
//
// A keyed action reuses its own key as the command id, the same "id is
// the board key" contract globalCommands' entries carry, so runCommand
// needs no new dispatch for it — it lands on the identical boardVerb case
// its accelerator does. An entry whose key already belongs to a global
// command (only "i", the inbox, both ways) is skipped rather than shown
// twice for one keystroke. The few genuinely keyless actions (envelope's
// gate toggle, duplicate) keep their own id, since there is no key to
// borrow — runCommand answers those two by id, the same functions
// runCardAction already calls for them, so there is still exactly one
// place each performs its action.
// cardCommandNames gives a card action the words a reader types for it
// after a "/" on the CARD page. It is the inverse of threadinput.go's
// verbActionIDs, and it exists for the same reason: a verb the answer
// set is not currently offering degrades to the "/" menu pre-filtered by
// the word, so the word has to find the row. A label alone cannot be
// trusted to — several of them adapt to card state ("hand to autopilot"
// / "stop autopilot"), and one of the two wordings would always miss.
//
// These land in command.alias, not command.name: name is the word a
// command answers to in the command menu's own vocabulary, and a card's
// actions must not shadow one of the board-root globals by word.
var cardCommandNames = map[string]string{
	"advance":   "approve land",
	"bounce":    "bounce",
	"changes":   "changes",
	"pause":     "park pause",
	"verify":    "verify",
	"rebase":    "rebase",
	"handoff":   "handoff close keep hand off",
	"writespec": "writespec write a spec",
	"merge":     "land merge",
	"squash":    "squash",
	"clean":     "clean",
	"newbug":    "bug followup",
	"adopt":     "adopt take back",
	"gate":      "autopilot",
	"spec":      "spec",
	"diff":      "diff",
	"ask":       "ask",
}

func (m *Shell) cardCommands(existing []command) []command {
	r, ok := m.selected()
	if !ok {
		return nil
	}
	taken := make(map[string]bool, len(existing))
	for _, c := range existing {
		if c.key != "" {
			taken[c.key] = true
		}
	}
	var all []cardAction
	if !r.F.IsFreeform() {
		all = append(all, m.cardProfileActions(r.F.Stage)...)
	}
	all = append(all, cardActionsFor(m.nextInputFor(r), r)...)
	var out []command
	for _, a := range all {
		id := a.id
		if a.key != "" {
			if taken[a.key] {
				continue
			}
			id = a.key
		}
		out = append(out, command{id: id, alias: cardCommandNames[a.id], label: a.label, key: a.key, available: true})
	}
	// a freeform card also offers the repository's own command files;
	// choosing one starts the line, since most of them take arguments
	if r.F.IsFreeform() && m.engine != nil {
		if ff := m.engine.Freeform(r.F.ID); ff != nil {
			for _, c := range ff.Commands() {
				label := "/" + c.Name
				if c.Description != "" {
					label += " — " + c.Description
				}
				out = append(out, command{id: "project-command:" + c.Name, alias: c.Name, label: label, available: true})
			}
		}
	}
	return out
}

// helpKeyFor names the key that actually opens the help table on the
// surface the reader is looking at.
//
// The card page's composer owns every printable key, so ? types a "?"
// there and the chord is alt+/ — which the card table's own last row says
// ("alt+/  this table — ? types here rather than opening help"). The menu
// row advertised ? regardless, so on the one surface where help is hardest
// to find, the only thing pointing at it named a key that does nothing,
// and the right key was documented exclusively inside the table you needed
// it to reach (round 3 §2.3).
func helpKeyFor(cardOpen bool) string {
	if cardOpen {
		return "alt+/"
	}
	return "?"
}

// runCommand is the space menu's invoke path. q and ? are answered by
// handleKey above the attached check, so they never reach boardKey and
// have to be routed here explicitly. duplicate and gate are cardCommands'
// two keyless entries (cardCommands' doc has why) and route to the same
// functions runCardAction calls for them, rather than a second copy of
// that dispatch. Everything else is a board key.
func (m *Shell) runCommand(id string) tea.Cmd {
	// the card profile picker's value tier, mirroring runBoardCompletion's
	// own "agent-cli:"-prefix split (boardcomplete.go) — no new parsing
	// convention introduced for it.
	if name, ok := strings.CutPrefix(id, "profile-value:"); ok {
		r, ok := m.selected()
		if !ok {
			return nil
		}
		return m.confirmCardProfileChange(r.F.ID, name)
	}
	if name, ok := strings.CutPrefix(id, "project-command:"); ok {
		m.threadInput.SetValue("/" + name + " ")
		m.threadInput.CursorEnd()
		return nil
	}
	switch id {
	case "q":
		return m.quitCmd()
	case "?":
		m.Overlay.Push(m.helpOverlay())
		return nil
	case "settings":
		m.openSettings()
		return nil
	case "run":
		// The keyless run is the implement stage's "send it back": it
		// re-runs the stage in place with the composer's line riding as
		// the note (decision.go's deliverDecisionWords, which is what
		// handles it when there IS a line). The ordinary run action wears
		// enter and never reaches this switch at all.
		//
		// Reached with nothing typed, there is nothing to send back — so
		// it says what it wants rather than answering with silence. The
		// bar named the row, so enter owes a response; this is the same
		// answer "changes" gives one row away, for the same reason.
		m.notice = noticeMsg{text: "type what should change — your line rides the re-run"}
		return nil
	case "topup":
		// the same act the inbox's u performs, reached from the stop
		// itself: raise the budget and let the stage pick up where it
		// stopped (shell.go's topUpBudget).
		if r, ok := m.selected(); ok {
			return m.topUpBudget(r.F.ID)
		}
	case "duplicate":
		return m.confirmDuplicate()
	case "deps":
		// the keyless dependency entry (cardactions.go): p pauses while a
		// stage session exists, so the menu is how the picker is reached
		if r, ok := m.selected(); ok {
			m.clearTransientNotice()
			return m.openDeps(r.F)
		}
		return nil
	case "newbug":
		if r, ok := m.selected(); ok {
			return m.openBugFromCard(r)
		}
		return nil
	case "adopt":
		if r, ok := m.selected(); ok {
			return m.openAdopt(r)
		}
		return nil
	case "gate":
		if r, ok := m.selected(); ok {
			return m.openAutopilot(r.F)
		}
		return nil
	case "profile":
		return m.openCardProfilePicker()
	case "model":
		return m.openCardModelPicker()
	case "new-freeform":
		m.Overlay.Push(m.openCardForm(domain.CardType{Kind: domain.KindFreeform}))
		return nil
	}
	return m.boardVerb(id)
}

// confirmDuplicate raises the duplicate confirm. Duplicating used to sit
// on the board's `y`, which is also "yes" in the confirm dialog `y`
// itself raises — one letter, two meanings, one keystroke apart. It has
// no accelerator now: it is a rare action, the action list and the
// command menu both reach it, and `y` gets to mean exactly one thing.
func (m *Shell) confirmDuplicate() tea.Cmd {
	r, ok := m.selected()
	if !ok {
		return nil
	}
	f := r.F
	m.Overlay.Push(&confirmDialog{
		card:         f.ID,
		id:           "confirm-duplicate",
		cancelLabel:  "Cancel",
		confirmLabel: "Duplicate",
		question:     "duplicate " + string(f.ID) + "?",
		detail:       f.Title + " — fresh copy in todo (same skips, profile, budget); this card stays",
		onConfirm:    func() tea.Cmd { return m.duplicateFeature(f.ID) },
	})
	return nil
}

// runCardAction performs one entry from the card's action list. A keyed
// action goes through boardVerb so it hits the same guarded case body as
// its accelerator; a keyless one (duplicate, gate, and the fold row) is
// handled here, since there is no key to route it by.
func (m *Shell) runCardAction(a cardAction) tea.Cmd {
	if a.key != "" {
		return m.boardVerb(a.key)
	}
	switch a.id {
	case expandID:
		// the cursor stays on the fold row, so enter toggles in place and
		// the newly revealed actions start one ↓ away.
		m.actionsExpanded = !m.actionsExpanded
		return nil
	case "topup":
		// A budget stop offers exactly two rows — this one and "stop
		// here" — so an id this switch does not know is not a missing
		// convenience, it is the card page having no way forward at all
		// (round 3 §1.1: enter on the only forward row did nothing, twice,
		// with no notice). The row is keyless by construction
		// (nextsteps.go's attnBudget arm), so the a.key shortcut above
		// cannot catch it either; runCommand carries the same case for the
		// space menu, and both route to the one act (shell.go's
		// topUpBudget) the inbox's u performs.
		if r, ok := m.selected(); ok {
			return m.topUpBudget(r.F.ID)
		}
	case "duplicate":
		return m.confirmDuplicate()
	case "reverify":
		// the verify stop on a branch that moved since its pass
		// (nextsteps.go's verifyStale arm)
		if r, ok := m.selected(); ok {
			return m.reverify(r.F.ID)
		}
		return nil
	case "wait":
		// the row a held gate leads with (nextsteps.go's waitOnDeps, the
		// stack's "lands after"): nothing to do but wait, and it says what
		// for rather than answering with silence. A message rather than a
		// write to m.notice: the web face hears a notice only as one
		// (bridge.go's emitChanges), and from the page this row is an
		// answer that would otherwise read as having done something.
		if r, ok := m.selected(); ok {
			n := noticeMsg{text: string(r.F.ID) + ": " + a.label + " — " + a.why, id: r.F.ID}
			return func() tea.Msg { return n }
		}
		return nil
	case "settle":
		// "stop here" on a failed stage (nextsteps.go's failure arm): the
		// run already stopped, so what stops is the card asking for you.
		// The failure stays on record; the next run of the stage closes it.
		if r, ok := m.selected(); ok {
			m.inbox.remove(r.F.ID)
			m.notice = noticeMsg{text: string(r.F.ID) + ": left at " + string(r.F.Stage) + " — nothing runs until you start it again", id: r.F.ID}
		}
		return nil
	case "deps":
		// the dependency picker, reached from the list while a stage
		// session exists — where p pauses instead (cardactions.go)
		if r, ok := m.selected(); ok {
			m.clearTransientNotice()
			return m.openDeps(r.F)
		}
	case "newbug":
		// keyless by construction (closedActions), so the a.key shortcut
		// above cannot catch it — the same shape topup has.
		if r, ok := m.selected(); ok {
			return m.openBugFromCard(r)
		}
	case "adopt":
		if r, ok := m.selected(); ok {
			return m.openAdopt(r)
		}
	case "goaltopup":
		if r, ok := m.selected(); ok && r.Goal != nil {
			return m.topUpGoalAndContinue(r.F, r.Goal.NeedsBudget)
		}
		return nil
	case "goalstop":
		if r, ok := m.selected(); ok {
			return m.confirmStopGoal(r.F)
		}
	case "goalreverse":
		if r, ok := m.selected(); ok {
			return m.openReverseDecision(r.F)
		}
	case "goalpage":
		if r, ok := m.selected(); ok {
			return m.openGoalPage(r.F)
		}
	case "profile":
		return m.openCardProfilePicker()
	case "model":
		// the freeform card's model row (cardactions.go): the two-tier
		// picker, the same offer the web face's picker makes
		return m.openCardModelPicker()
	case "writespec":
		// the freeform card's third ending (cardactions.go): the writespec
		// dialog, the same offer the web face's menu row makes. Keyless
		// here only because this switch is the keyless path; the row
		// itself wears `w` and routes through boardVerb.
		if r, ok := m.selected(); ok {
			return m.openWritespec(r.F)
		}
		return nil
	case "objective":
		// the composer is where an objective is written: it is the
		// session's own /objective command, with its completions
		m.threadInput.SetValue("/objective ")
		m.threadInput.CursorEnd()
		m.focusThreadInput()
		return nil
	case "heartbeat":
		// the freeform card's recurring turn (cardactions.go): the
		// schedule dialog, already a heartbeat aimed at this card
		if r, ok := m.selected(); ok {
			return m.openHeartbeatForm(r.F)
		}
		return nil
	case "ask":
		// arms the same channel typing `ask` on the composer does
		// (threadinput.go's routeVerb) — this is just the inventory's own
		// route to it for someone who reaches for the list instead of the
		// word.
		m.threadAsk = true
		m.focusThreadInput()
		return nil
	case "changes":
		// this option IS the composer's words: typing aims at it and enter
		// delivers the line as the turn asking for the changes
		// (decision.go's deliverDecisionWords). Reached with nothing typed
		// there is nothing to send, so it says what it wants rather than
		// answering with silence — the bar named it, so enter owes a
		// response.
		m.notice = noticeMsg{text: "type what should change — your line goes back with it"}
		return nil
	case "gate":
		// the two-state toggle (tighten applies immediately, loosen
		// confirms first) is superseded by the autopilot overlay
		// (autopilot.go): its own confirm button is the deliberate act
		// that protects a loosening move now, so there is no second
		// confirm layered on top of it here.
		if r, ok := m.selected(); ok {
			return m.openAutopilot(r.F)
		}
	case "prlink":
		if r, ok := m.selected(); ok {
			return m.openPRLinkDialog(r.F)
		}
	case "prcreate", "push", "prready", "prdraft":
		if r, ok := m.selected(); ok {
			return m.openPublish(r, publishActs[a.id], "")
		}
	case "prunlink":
		if r, ok := m.selected(); ok {
			return m.confirmPRUnlink(r.F)
		}
	case "prpull":
		if r, ok := m.selected(); ok {
			if r.F.PullRequest.Empty() {
				m.notice = noticeMsg{text: string(r.F.ID) + " has no linked PR", isErr: true}
				return nil
			}
			return m.pullPRReview(r.F)
		}
	case "prchecks":
		if r, ok := m.selected(); ok {
			if r.F.PullRequest.Empty() {
				m.notice = noticeMsg{text: string(r.F.ID) + " has no linked PR", isErr: true}
				return nil
			}
			return m.sendPRChecks(r.F)
		}
	}
	return nil
}
