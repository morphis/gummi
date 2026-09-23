package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The board thread: the agent tab's own conversation surface, built from
// exactly the same pieces the card thread uses (transcriptLines,
// composeThread) but bound to engine.BoardSession — a workspace-scoped
// conversation with no card, no stage, no decision and no verb
// vocabulary behind it. Where the card thread is "one card's whole
// history, scrollable, with a pinned decision", the board thread is
// simply "one long-lived conversation with the board itself": a head
// naming the session, the conversation, and the composer.
//
// It is deliberately not a second copy of thread.go's machinery. Every
// card-specific concept the card thread carries — the stage strip, the
// pinned spec line, folded per-stage receipts, the confirm chip and verb
// parser, the open decision — has no board-session analogue: a board
// conversation has no stage to be at and no verb that only makes sense
// against a single selected card. What's left after removing all of
// that is a head, a body and a foot, which is exactly what composeThread
// already lays out for the card thread; reusing it here is what keeps
// the two surfaces' layout behavior (foot wins on a short terminal,
// scroll markers, the rest) from drifting apart by accident.

// ensureBoardSession opens the board's own conversation the first time
// the agent tab is visited, and is a no-op on every later visit —
// gotoTab's own lazy-spawn contract. engine.OpenBoard is itself
// idempotent (a second call while one is live just returns the existing
// session), so the guard below exists only to stop this from dispatching
// a second spawn command while the first is still in flight — a quick
// tab bounce before boardOpenedMsg has landed.
//
// OpenBoard can start a real backend process and dial its tools, which
// can take real time — the same cost attachChat's own doc comment names
// for a card's Attach — so this runs in a command, never inline (the
// no-IO-in-Update contract attachChat states applies here too).
func (m *Shell) ensureBoardSession() tea.Cmd {
	if m.board != nil || m.boardErr != "" || m.boardOpening {
		return nil
	}
	if m.engine == nil {
		// mirrors attachOrRun's own wording for the identical precondition
		// on a card's chat/run — a static board with no coding agent wired
		// has nothing to open here either.
		m.boardErr = "no agent configured (set a model/provider to enable agents)"
		return nil
	}
	m.boardOpening = true
	eng := m.engine
	return func() tea.Msg {
		b, err := eng.OpenBoard(context.Background(), engine.BoardOpts{})
		return boardOpenedMsg{session: b, err: err}
	}
}

// reopenBoard ends the live board session (if any) and starts a fresh
// one under opts — the board's /profile and /model commands
// (boardcomplete.go) reaching for engine.ReopenBoard rather than
// OpenBoard, whose whole point is the opposite: OpenBoard reuses a live
// session (its own doc comment), and a picker exists precisely to
// override that reuse, not race it.
//
// eng is read off m.engine before the returned command runs, on the
// Update goroutine — never inside the closure below — the discipline
// every command in this package that needs a Shell field follows: once a
// command is running on its own goroutine, a Shell field it touches can
// be read by Update on the very next frame, and nothing serializes the
// two.
//
// The four fields reset here are every piece of state a stale board
// session left behind that a fresh one must not inherit: boardOpening so
// ensureBoardSession doesn't also fire a second spawn while this one is
// in flight, boardErr so a prior failure doesn't linger once the retry
// that just started succeeds (the boardOpenedMsg handler in shell.go
// clears it again on that success, but this clears it up front so the
// placeholder reads "starting…" rather than the old error while the
// respawn is still running), and boardScroll/boardComplete because both
// describe a conversation and a composer line that are about to stop
// existing.
func (m *Shell) reopenBoard(opts engine.BoardOpts) tea.Cmd {
	eng := m.engine
	if eng == nil {
		return nil
	}
	if m.boardOpening {
		// ensureBoardSession refuses to dispatch a second spawn while one
		// is in flight, and this has to refuse for the same reason: the
		// engine serializes the two on boardMu, so nothing races, but the
		// loser spawns a whole backend process only to have it torn down
		// by the winner a moment later. It is also the window where
		// m.board is still nil, which is what let a /profile issued here
		// skip the confirm as "nothing to lose" while an open was already
		// on its way.
		m.notice = noticeMsg{text: "the board session is still opening — try again in a moment"}
		return nil
	}
	m.boardOpening = true
	m.boardErr = ""
	m.boardScroll = 0
	m.boardComplete = nil
	return func() tea.Msg {
		b, err := eng.ReopenBoard(context.Background(), opts)
		return boardOpenedMsg{session: b, err: err}
	}
}

// boardTabPlaceholder is what the agent tab shows before the board
// session has opened, or instead of one that failed to — reading
// m.board/m.boardErr, the two fields that carry the board session's own
// two-state contract (nil/non-nil, plus the error string).
func (m *Shell) boardTabPlaceholder(w, h int) string {
	msg := m.styles.Muted.Render("starting the board session…")
	if m.boardErr != "" {
		msg = m.styles.Muted.Render(m.boardErr)
	}
	return centeredNotice(w, h, msg)
}

// boardPageBlank is the board thread's share of cardPageChrome: the
// blank row between the composer and the status bar, which stops two
// chrome-coloured rows stacked with nothing between them from reading
// as one control (cardPageChrome's own reason for it).
//
// The card thread gets that row from its page wrapper — cardPageView
// spends it around threadView. The agent tab has no wrapper: mainView
// hands boardThreadView the whole pane, so without this the board
// composer sat flush against the status bar while the card composer,
// the same widget, kept its row of air. It is the same budget
// (composerBlankRows) minus the crumb the board thread has no analogue
// for, so on any terminal tall enough to afford it the two composers
// sit at the same distance from the bottom, and on one that is not,
// both give the row up together.
func boardPageBlank(h int) int {
	if h >= composerBlankRows {
		return 1
	}
	return 0
}

// boardThreadView renders the board session's conversation into the
// agent tab's main pane — threadView's counterpart, minus the measure
// split (maxBoardScroll below does that itself, the same way
// maxThreadScroll does for the card thread), plus the composer's blank
// row, which cardPageView spends on the card thread's behalf.
func (m *Shell) boardThreadView(w, h int) string {
	blank := boardPageBlank(h)
	out := m.boardThreadRender(w, max(h-blank, 1), false)
	if blank > 0 {
		out += "\n"
	}
	return out
}

// boardThreadRender is boardThreadView with the measure pass split out,
// mirroring threadRender's own doc comment: measuring lays the head and
// the foot out at the real height (so a decoration that only appears
// past a certain height is counted exactly when the render will draw
// it) while leaving the body unwindowed, so maxBoardScroll can count
// every row there is to reach.
//
// It is only ever called with a live m.board from boardThreadView/
// mainView (mainView shows boardTabPlaceholder otherwise) — but
// maxBoardScroll can reach it through a pgup/pgdn pressed before the
// session finished opening, so the nil case is handled rather than
// assumed away.
func (m *Shell) boardThreadRender(w, h int, measure bool) string {
	s := m.styles
	inner := max(w-threadGutter, 8)
	clip := func(str string) string {
		if strings.TrimSpace(str) == "" {
			return "" // a blank row is blank, not a run of spaces
		}
		return ansi.Truncate(str, inner, "…")
	}
	sep := 0
	if h >= composerBlankRows {
		sep = 1
	}

	var snap engine.Snapshot
	if m.board != nil {
		snap = m.board.Snapshot()
	}

	// --- head ---
	buildHead := func(sep int) []string {
		head := []string{clip(boardHeader(s, snap))}
		head = append(head, "")
		if sep > 0 {
			// the leading blank separates the masthead from the page's crumb
			// above it, the same reason threadRender adds one.
			head = append([]string{""}, head...)
		}
		return head
	}
	head := buildHead(sep)
	// headMin is head with its sep-gated leading blank given up before any
	// row of content — threadRender's headMin, for the same reason
	// (BG-050).
	headMin := head
	if sep > 0 {
		headMin = buildHead(0)
	}

	// --- body ---
	var body []string
	add := func(str string) { body = append(body, clip(str)) }
	if m.board != nil {
		for _, l := range transcriptLines(s, snap, inner, m.boardOutputs) {
			add(l)
		}
		if snap.Err != nil {
			// wrap the whole message rather than truncating it away,
			// capped to errLines — the same treatment a card's live-stage
			// block gives a session-start failure (thread.go).
			for _, l := range strings.Split(wrapError(snap.Err.Error(), max(inner-2, 4)), "\n") {
				add("  " + s.Error.Render(l))
			}
		}
		if snap.Busy {
			// The interrupt hint rides the spinner rather than living in the
			// key bar alone: this line is where the eye already is while a
			// turn runs, and a turn running is the only moment the key is
			// wanted. Faint, because it is an aside to the state, not part
			// of it.
			add("  " + s.Info.Render(m.spinner()+" thinking…") + " " + s.Faint.Render("(esc to interrupt)"))
		}
	}
	if len(body) == 0 {
		add(boardEmptyLine(s))
	}
	body = trimTrailingBlanks(body)

	// --- foot ---
	// footMin is foot without its own sep-gated lead-in blank —
	// threadRender's footMin, for the same reason (BG-050).
	var footMin []string
	for _, l := range strings.Split(m.boardInputBlock(inner), "\n") {
		footMin = append(footMin, clip(l))
	}
	foot := footMin
	if sep > 0 {
		foot = append(make([]string, sep), footMin...)
	}

	// the measure wants every row there is (composeThread's h<=0 branch),
	// having laid the head and the foot out at the real height — see
	// threadRender's own comment on why the two must not disagree.
	composeH := h
	if measure {
		composeH = 0
	}
	return strings.Join(composeThread(s, head, headMin, body, nil, nil, foot, footMin, composeH, m.boardScroll, inner), "\n")
}

// boardHeader is the board thread's masthead: what threadHeader is to a
// card, except naming the session rather than a card — a board session
// belongs to no single one (engine/boardsession.go's own doc comment).
// It carries backend, model, permission mode and, once known, context
// occupancy and running spend, reusing the same formatting helpers
// (spendformat.go) sessionMeta composes for a card's live-stage line
// rather than re-deriving them.
func boardHeader(s *theme.Styles, snap engine.Snapshot) string {
	head := s.Title.Render("board session")
	var facts []string
	if snap.AgentName != "" {
		facts = append(facts, snap.AgentName)
	}
	if mdl := runModel(snap); mdl != "" {
		facts = append(facts, mdl)
	}
	// boardPermission (engine/boardsession.go) is fixed at allow-all for
	// every backend a board session can spawn — PermissionGuarded is
	// refused outright by claude and would hang the others, since no
	// adapter in this codebase ever emits agent.EventPermission to answer
	// it (that file's own comment has the full case). This is not a live
	// read of anything the session reports; it is what the engine
	// actually spawned it with, named here rather than left entirely
	// unstated the way a card's own header can (a card's permission mode
	// varies by backend/config; a board session's cannot, yet).
	facts = append(facts, "allow-all")
	if c := snap.Context; c.Tokens > 0 {
		ctx := humanTokens(c.Tokens) + " ctx"
		if c.Limit > 0 {
			ctx = fmt.Sprintf("%s/%s ctx (%d%%)", humanTokens(c.Tokens), humanTokens(c.Limit), c.Tokens*100/c.Limit)
		}
		facts = append(facts, ctx)
	}
	if sp := spendSummary(snap); sp != "" {
		facts = append(facts, sp)
	}
	if len(facts) > 0 {
		head += headerGap + s.Faint.Render(strings.Join(facts, " · "))
	}
	return head
}

// boardEmptyLine is the board thread's one line before any conversation
// exists — threadEmptyLine's board counterpart, with no card one-liner
// to fall back to (a board session belongs to no card at all).
func boardEmptyLine(s *theme.Styles) string {
	return s.Faint.Render("say something to get started — it can read and act on every card")
}

// boardPlaceholderText is the board composer's placeholder text.
// threadinput.go's placeholderText advertises the card thread's verb
// vocabulary and action inventory, neither of which the board thread
// has (handleBoardInputKey's own doc comment on why); this says what a
// message here can actually do instead.
const boardPlaceholderText = "message the board — it can read and act on every card, or / for commands"

// boardInputBlock is the board thread's bottom input slot — inputBlock's
// counterpart, minus everything that does not apply here: no
// DrivenAbroad (a board session belongs to no other process to withhold
// it from), no confirm chip, no verb vocabulary. The composer carries
// its own styling already (newThreadInput); what this adds on top of it
// is the completion popup.
//
// The popup goes ABOVE the line, growing upward into the conversation,
// because the composer is already the bottom of the pane and a list
// hung below it would be off-screen. It is part of the foot rather than
// a layer over the body on purpose: the foot is what composeThread
// protects on a short terminal, so the rows the user is choosing between
// can never be the thing that gets scrolled away, and the transcript
// gives up height for them instead.
func (m *Shell) boardInputBlock(w int) string {
	m.boardInput.Placeholder = boardPlaceholderText
	// SetWidth reruns the widget's own recalculateHeight (DynamicHeight,
	// newThreadInput), so a resize rewraps the content and reflows the
	// composer's height along with it, exactly as inputBlock relies on.
	m.boardInput.SetWidth(max(w-2, 10))
	out := m.boardComplete.view(m.styles, w)
	return strings.Join(append(out, m.boardInput.View()), "\n")
}

// boardThreadSize is threadSize's board-thread counterpart: the main
// pane's dimensions, recomputed fresh (not read off m.layout) so a key
// handler running ahead of the next resize never scrolls against a
// stale height.
func (m *Shell) boardThreadSize() (int, int) {
	main := m.computeLayout().Main
	// less boardPageBlank's row, so the height the thread is rendered at
	// and the height its scroll clamp is measured against cannot
	// disagree — threadSize resolves cardPageChrome for the same reason.
	return main.Dx(), max(main.Dy()-boardPageBlank(main.Dy()), 1)
}

// maxBoardScroll is maxThreadScroll's board-thread counterpart: how far
// back the body can be scrolled for a given window, measured by actually
// rendering it (wrapping and fold state make anything else a guess) and
// padded by the two rows composeThread spends on scroll markers once
// anything scrolls (see maxThreadScroll's own comment on why the pad has
// to be added back here rather than assumed away).
func (m *Shell) maxBoardScroll(w, h int) int {
	full := len(strings.Split(m.boardThreadRender(w, h, true), "\n"))
	if full <= h {
		return 0
	}
	return full - h + 2
}

// scrollBoardThread pages the board thread's body — scrollThread's
// board-thread counterpart, same step-is-the-visible-height and
// clamped-both-ends behavior.
func (m *Shell) scrollBoardThread(up bool) {
	w, h := m.boardThreadSize()
	step := max(h-1, 1)
	if up {
		m.boardScroll = min(m.boardScroll+step, m.maxBoardScroll(w, h))
		return
	}
	m.boardScroll = max(m.boardScroll-step, 0)
}

// boardOutputsBinding is the board composer's alt+o row —
// threadOutputsBinding's board-thread counterpart, reading
// m.boardOutputs instead of m.threadOutputs (the Shell field's own doc
// comment has why they are two separate toggles rather than one shared
// one).
func (m *Shell) boardOutputsBinding() binding {
	label, help := "outputs", "expand the captured tool outputs"
	if m.boardOutputs {
		label, help = "fold", "fold the captured tool outputs back"
	}
	return binding{key: "alt+o", label: label, help: help, bar: true}
}

// boardEscBinding is the board composer's esc row. esc has exactly one
// job on this surface — interrupt a live turn — so the row takes a bar
// slot only while there is one to interrupt: a bar reading "esc
// interrupt" over an idle conversation names a key that will do nothing,
// which is the same promise-a-key-cannot-keep the tab's own chrome is
// meant to avoid. It stays in the help table in either state, because
// "esc does not leave this tab" is worth being able to look up.
//
// esc deliberately does not clear the draft, though some of the CLIs this
// surface is modelled on let it: in gummi leaving never discards — the
// card thread's esc keeps its draft, and so does every tab switch — so an
// esc that silently emptied this composer would be the one key in the
// program that throws typing away. ctrl+c is the clear key, and the bar
// names it as such the moment there is something to clear.
func (m *Shell) boardEscBinding() binding {
	return binding{
		key:   "esc",
		label: "interrupt",
		help:  "interrupt the board's in-flight turn — it never leaves the tab and never clears the line (ctrl+c does that)",
		bar:   m.board != nil && m.board.Snapshot().Busy,
	}
}

// boardNewlineBinding is the composer's newline row, in the bar only
// while there is a draft for a second line to join — the same
// state-dependent rule boardCancelBinding follows, and for the same
// reason: on an empty composer the key is trivia, and mid-paragraph it is
// the thing the user is looking for.
func (m *Shell) boardNewlineBinding() binding {
	return binding{
		key:   "alt+enter",
		label: "newline",
		help:  "a line break without sending — ctrl+j and shift+enter do the same, for terminals that report them",
		bar:   m.boardInput.Value() != "",
	}
}

// boardCancelBinding is the board composer's ctrl+c row, naming what the
// NEXT press of it will do rather than the key's general rule — the same
// thing boardOutputsBinding does for the outputs toggle, and for the same
// reason the keyboard-lock badge states what is true now (DESIGN §6): a
// bar naming a key's other meaning is a bar telling you to press the
// wrong thing.
//
// Only the clear state takes a bar slot. The other two would be naming a
// key the row above already names (esc, while a turn is in flight) or the
// one thing every terminal program does anyway (quit), and the row fits
// about six hints — so those two live in the help table alone, where the
// full contract is written out once.
func (m *Shell) boardCancelBinding() binding {
	switch {
	case m.boardInput.Value() != "":
		return binding{
			key:   "ctrl+c",
			label: "clear",
			help:  "empty the composer — the draft is dropped, nothing is sent, and with the line empty the same key interrupts a live turn (or quits when there is none)",
			bar:   true,
		}
	case m.board != nil && m.board.Snapshot().Busy:
		return binding{
			key:  "ctrl+c",
			help: "interrupt the in-flight turn, the same as esc — with nothing typed to clear, the turn is what there is to cancel",
		}
	default:
		return binding{
			key:  "ctrl+c",
			help: "quit gummi — with an empty composer and no turn in flight there is nothing on this surface left to cancel",
		}
	}
}

// boardCancelKey answers ctrl+c while the agent tab's composer has the
// keyboard, and reports whether it claimed the key. It is reached from
// update's hoisted ctrl+c branch, above the overlay stack, because that
// is the only place the key is ever seen — handleKey never gets it.
//
// The contract is the one every modern coding CLI converged on, read as
// "cancel what is in front of you", narrowing by what there is to cancel:
//
//	a draft in the composer → empty it, and nothing else
//	nothing typed, a turn in flight → interrupt it (esc's job, same key)
//	nothing typed, nothing running → quit (the caller's fall-through)
//
// The draft wins over the turn deliberately: the line is the thing the
// user just typed and the only one of the two that is unrecoverable —
// an interrupted turn can be asked again, a cleared line that was never
// stored anywhere cannot be got back. Before this, ctrl+c on this surface
// was quit and only quit, so a composer holding a half-written paragraph
// had no clear key at all: ctrl+u (the widget's own) deletes to the
// cursor rather than emptying the box, and the one key a person reaches
// for out of habit exited the program mid-sentence instead.
//
// An open dialog keeps the key: the hoist exists so a modal can never
// trap the one key every terminal program is expected to answer, and a
// picker over this tab (/profile, /model) is exactly such a modal.
func (m *Shell) boardCancelKey() (tea.Cmd, bool) {
	if m.tab != TabAgent || !m.boardInput.Focused() || m.Overlay.HasDialogs() {
		return nil, false
	}
	// Value(), not TrimSpace(Value()): a box holding only spaces still has
	// something in it, and a clear key that quits gummi over whitespace
	// the user cannot see is the same trap this removes.
	if m.boardInput.Value() != "" {
		m.boardInput.Reset()
		// the popup is derived from the line, and the line is now empty —
		// the same resync every other path that rewrites it runs.
		m.syncBoardCompletion()
		return nil, true
	}
	if m.board != nil && m.board.Snapshot().Busy {
		return m.interruptBoardSession(), true
	}
	return nil, false
}

// handleBoardInputKey routes a key while the board thread's composer has
// the keyboard (shell.go's handleKey, gated on m.tab == TabAgent &&
// m.boardInput.Focused()).
//
// Its keyset is much smaller than the card thread's own composer
// (threadinput.go's handleThreadInputKey): a board conversation carries
// no open decision, no confirm chip and no closed verb vocabulary —
// those all exist to act on a SELECTED CARD (approve, verify, land…),
// and a board composer has none. Routing "verify" typed here through the
// card thread's parser would either silently fire it against whatever
// card happens to be selected on the hidden board tab, or have nothing
// to act on at all; neither is what a line typed here could honestly
// mean. Every turn is a message, full stop — the board's own seven tools
// (workspaceTools, engine/boardsession.go) are how it acts on a card,
// reached by the model deciding to call them, never by gummi parsing the
// user's words for a verb the way the card thread does.
//
// The one command it answers that belongs to no card and no workspace
// is "/clear", and that is not the verb vocabulary coming back through
// a side door: a verb acts on a card, while /clear acts on the
// conversation the composer is standing in — the same category as esc's
// interrupt above, which this surface already takes for itself. Enter
// dispatches the trimmed line through runTypedBoardCommand
// (boardcomplete.go), whose vocabulary is the popup's own: the space
// menu's named entries plus this conversation's /clear, matched
// whole-word and case-insensitively. A message can honestly open with a
// path ("/etc/hosts is stale, fix it"), and a board conversation is
// exactly where such a line gets typed, so an unrecognised slash line
// is sent rather than refused as a mistyped command.
func (m *Shell) handleBoardInputKey(msg tea.KeyPressMsg) tea.Cmd {
	// The completion popup is answered first, and only for the keys it
	// actually claims (boardcomplete.go). It sits above this switch
	// rather than inside it because two of those keys — esc and enter —
	// already mean something here, and the popup has to be able to take
	// them back for as long as it is open without either meaning being
	// rewritten for the case where it is not.
	if cmd, took := m.handleBoardCompletionKey(msg); took {
		return cmd
	}
	switch msg.String() {
	case "alt+o":
		// mid-draft, like the card thread's own toggle — expanding a
		// captured tool output is never text.
		m.boardOutputs = !m.boardOutputs
		return nil
	case "pgup", "pgdown":
		m.scrollBoardThread(msg.String() == "pgup")
		return nil
	case "alt+enter", "ctrl+j", "shift+enter":
		// A line break, because enter sends. The composer is DynamicHeight
		// up to threadInputMaxHeight — built to grow with a paragraph — and
		// before this the only way to put a second line in it was to paste
		// one: the widget's own InsertNewline is bound to enter, which this
		// handler claims for the send, and nothing else offered a newline.
		//
		// Three spellings for one job, because a terminal decides which of
		// them gummi ever sees: shift+enter needs a terminal that reports
		// modified enter at all, alt+enter is what the card form already
		// binds (cardform.go), and ctrl+j is the one every terminal can
		// send. Binding all three costs nothing and means the key a person
		// tries first is the one that works.
		m.boardInput.InsertString("\n")
		// completeSlash refuses anything holding a newline, so the line
		// that just became two closes the popup — the same resync every
		// path that rewrites the text runs.
		m.syncBoardCompletion()
		return nil
	case "up", "down":
		// Recall, and only when there is somewhere to recall from or back
		// to: an unclaimed key falls through to the widget below, where ↑/↓
		// move between the rows of a multi-line draft as they always have.
		if m.boardHistoryRecall(msg.String() == "up") {
			return nil
		}
	case "esc":
		// Unlike the card thread, where esc leaves the page (its composer
		// keeps the keyboard for good — threadinput.go's doc comment), the
		// agent tab has no separate "page" to leave: the tab IS the board
		// conversation, so esc's one honest job here is interrupting a
		// live turn — the same key a hosted CLI would have taken for
		// itself, before this surface replaced it.
		return m.interruptBoardSession()
	case "enter":
		// Trimmed once, up front, and every branch below takes that same
		// string: the dispatcher matches the line it is given, and
		// completeSlash only recognizes a "/" in column one, so an
		// untrimmed " /clear" would fall through every command check and
		// reach the agent as a message — the exact line the literal
		// matcher used to clear before this became a plain vocabulary
		// word. The send below taking the same string keeps unclaimed
		// prose byte-identical; trimming before matching is the idiom the
		// card thread's parser already sets (verbs.go parseInput trims
		// first).
		text := strings.TrimSpace(m.boardInput.Value())
		if text == "" {
			return nil
		}
		// Recorded before the dispatch and for either kind of line: a
		// command is something the user typed too, and ↑ after a mistyped
		// "/profle" is exactly when recall earns itself.
		m.rememberBoardLine(text)
		// The popup is already closed here (handleBoardCompletionKey above
		// claims enter for as long as one is open), which is exactly the
		// hole a completed-but-dismissed command line falls into: "/inbox "
		// or a tab-completed "/inbox " has no popup left to run it, so
		// without this check enter would send the literal text as a
		// sentence instead of opening the inbox. runTypedBoardCommand
		// answers that by the command word alone — matching prose that
		// starts with "/" is still routed as a message below, unchanged.
		if cmd, ok := m.runTypedBoardCommand(text); ok {
			return cmd
		}
		m.boardScroll = 0 // jump to the latest on send, as the card thread does
		return m.sendBoardMessage(text)
	}
	var cmd tea.Cmd
	m.boardInput, cmd = m.boardInput.Update(msg)
	// Every key that reaches the widget can have changed the line, so the
	// popup is re-derived from the new text rather than from the key that
	// produced it — including the backspace that unwrites the "/" and the
	// ctrl+u that clears the line, both of which have to close it.
	m.syncBoardCompletion()
	return cmd
}

// boardHistoryMax caps the composer's recall ring. A board conversation
// is typed in sentences, not keystrokes, so this is generous by orders of
// magnitude for one session — it exists so a process left open for days
// has a bound at all, not because anyone will reach it.
const boardHistoryMax = 200

// rememberBoardLine records a submitted line and puts ↑/↓ back on the
// live draft. A line identical to the one before it is not recorded
// twice: a repeated "go on" should be one entry to walk past, not five.
func (m *Shell) rememberBoardLine(text string) {
	if n := len(m.boardHistory); n == 0 || m.boardHistory[n-1] != text {
		m.boardHistory = append(m.boardHistory, text)
		if len(m.boardHistory) > boardHistoryMax {
			m.boardHistory = m.boardHistory[len(m.boardHistory)-boardHistoryMax:]
		}
	}
	m.boardHistoryAt = len(m.boardHistory)
	m.boardHistoryDraft = ""
}

// boardHistoryRecall answers ↑/↓ in the board composer and reports
// whether it claimed the key — the recall every coding CLI (and every
// shell before them) puts on those two keys, which this surface simply
// did not have: a sent line existed only in the transcript, where it
// could be read and not re-sent, so the way to fix a typo in a long
// message was to type the whole thing again.
//
// ↑ walks back only from the composer's first row, so the rows of a
// multi-line draft stay reachable with the same key — the test every
// shell-style recall uses. ↓ only ever answers while browsing; off the
// newest entry it hands back the draft browsing interrupted, and from
// there it is ordinary cursor movement again.
//
// Editing a recalled line does not end browsing: ↑ from it steps further
// back, as readline does. The position resets on submit
// (rememberBoardLine) and on /clear, never on a keystroke, so there is
// one rule for where ↑ goes next and it does not depend on what the user
// did to the line in between.
func (m *Shell) boardHistoryRecall(up bool) bool {
	if up {
		if m.boardInput.Line() > 0 || m.boardHistoryAt == 0 {
			return false
		}
		if m.boardHistoryAt == len(m.boardHistory) {
			m.boardHistoryDraft = m.boardInput.Value()
		}
		m.boardHistoryAt--
		m.recallBoardLine(m.boardHistory[m.boardHistoryAt])
		return true
	}
	if m.boardHistoryAt >= len(m.boardHistory) {
		return false
	}
	m.boardHistoryAt++
	if m.boardHistoryAt == len(m.boardHistory) {
		m.recallBoardLine(m.boardHistoryDraft)
		m.boardHistoryDraft = ""
		return true
	}
	m.recallBoardLine(m.boardHistory[m.boardHistoryAt])
	return true
}

// recallBoardLine installs a line from the history ring. It is
// setBoardLine (boardcomplete.go) minus the popup, and the difference is
// the point: that helper exists for a line being TYPED, where re-deriving
// the popup is what opens the next tier — but a recalled line is already
// complete, and a popup over it would take ↑/↓ for its own list
// (handleBoardCompletionKey claims them first) and strand the reader one
// step into their own history, unable to walk past a "/clear" they once
// sent. Typing after the recall syncs the popup as usual.
func (m *Shell) recallBoardLine(text string) {
	m.boardInput.SetValue(text)
	m.boardInput.CursorEnd()
	m.boardComplete = nil
}

// interruptBoardSession aborts the board session's in-flight turn. A nil
// session (esc pressed before the first open landed, or after one
// failed) is simply nothing to interrupt.
func (m *Shell) interruptBoardSession() tea.Cmd {
	b := m.board
	if b == nil {
		return nil
	}
	return func() tea.Msg {
		if err := b.Interrupt(context.Background()); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return nil
	}
}

// boardClearCommand is the board conversation's command word: typed
// rather than bound to a key, because it is the line a person arriving
// from a coding CLI already has in their fingers — /clear is a familiar
// command from that world, and the board conversation answers to it too.
// It is spelled here for the keybar row that names it (keymap.go); the
// running of it lives with the rest of the vocabulary, in the board
// command dispatcher (boardcomplete.go), which claims the word
// whole-line, case-insensitively, and never with an argument.
const boardClearCommand = "/clear"

// clearBoardConversation drops the board conversation and starts a fresh
// one: the transcript, the context window it accumulated and the spend
// it ran up all belong to the session, so the honest way to clear them
// is to close it and open another — engine.OpenBoard reuses a live
// session (its own doc comment), and only ever starts a new backend once
// the old one is gone. It is the same close-and-reopen reopenBoard does
// for /profile and /model, asked for directly instead of as a side
// effect of switching.
//
// A turn in flight is ended by it. That is the point rather than a
// wrinkle — a person clearing a conversation is saying they are done
// with what is in it, including whatever it is still saying — and
// Close's own teardown is what stops the agent, so nothing is left
// talking to a session the UI has dropped.
func (m *Shell) clearBoardConversation() tea.Cmd {
	if m.board == nil && m.boardOpening {
		// ensureBoardSession's open is still in flight; its result lands
		// in boardOpenedMsg and would install the very session this just
		// closed. There is also nothing yet to clear — the tab is still
		// showing the placeholder.
		m.notice = noticeMsg{text: "the board session is still opening"}
		return nil
	}
	if m.board != nil {
		_ = m.board.Close()
		m.board = nil
	}
	// A failed open is cleared along with a live session: ensureBoardSession
	// refuses to retry past boardErr, so leaving it set here would turn
	// "start over" into "stay broken" for the one user who most wants a
	// retry. reopenBoard clears it for the same reason; between them they
	// are the two ways back from a failed open.
	m.boardErr = ""
	m.boardScroll = 0
	m.boardInput.Reset()
	// Reset emptied the box, so browsing has nothing left to be standing
	// in. The ring itself survives: it is what the user typed, not what
	// the conversation held (see Shell.boardHistory).
	m.boardHistoryAt = len(m.boardHistory)
	m.boardHistoryDraft = ""
	m.notice = noticeMsg{text: "board conversation cleared"}
	return m.ensureBoardSession()
}

// sendBoardMessage delivers a line typed into the board composer as a
// turn to the board session — sendThreadMessage's board counterpart,
// including the swapped-out-session guard that one carries.
//
// This file used to argue that guard was unnecessary here, on the
// grounds that OpenBoard only ever installs a board when there isn't one,
// so a captured *BoardSession could never go stale behind the closure the
// way a captured *Session can. ReopenBoard ended that: /profile and
// /model stop the live session and install a new one, and commands run on
// their own goroutines, so the window is real. Press enter and confirm a
// switch close enough together and the closure below would reach a
// stopped session — the composer already cleared, the turn landing
// nowhere, and nothing said about it. eng.Board() != b is the same
// question eng.Get(id) != sess asks for a card, answered the same way.
func (m *Shell) sendBoardMessage(text string) tea.Cmd {
	b := m.board
	if b == nil {
		m.notice = noticeMsg{text: "board session not open yet", isErr: true}
		return nil
	}
	m.boardInput.Reset()
	// Read on the Update goroutine, not inside the closure — this
	// package's rule for every command that needs a Shell field.
	eng := m.engine
	return func() tea.Msg {
		if eng != nil && eng.Board() != b {
			return noticeMsg{text: "the board session was replaced — that line was not sent", isErr: true}
		}
		if err := b.Send(context.Background(), text); err != nil {
			if errors.Is(err, agent.ErrBusy) {
				// The backend refused a second turn. The board session has
				// already undone its own half (the echo is back out of the
				// transcript — engine/boardsession.go's ErrBusy branch);
				// this is the other half, and the same answer the card
				// thread gives (sendThreadMessage): the line comes back to
				// the composer it was typed in rather than being lost to a
				// clear that happened before the send was known to fail.
				return noticeMsg{
					text:         "the board agent is still mid-turn — your line is back in the composer, send it when the turn ends",
					restoreBoard: text,
				}
			}
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return nil
	}
}
