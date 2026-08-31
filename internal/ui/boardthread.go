package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

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
// gotoTab's own lazy-spawn contract for the pty it is replacing, applied
// here instead. engine.OpenBoard is itself idempotent (a second call
// while one is live just returns the existing session), so the guard
// below exists only to stop this from dispatching a second spawn command
// while the first is still in flight — a quick tab bounce before
// boardOpenedMsg has landed.
//
// Unlike ensureAgent's synchronous pty spawn, OpenBoard can start a real
// backend process and dial its tools, which can take real time — the
// same cost attachChat's own doc comment names for a card's Attach — so
// this runs in a command, never inline (the no-IO-in-Update contract
// attachChat states applies here too).
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

// boardTabPlaceholder is what the agent tab shows before the board
// session has opened, or instead of one that failed to — the board
// thread's counterpart to agenttab.go's agentTabPlaceholder, kept
// separate (rather than reused) because it reads m.board/m.boardErr, its
// own two fields, not m.agent/m.agentErr.
func (m *Shell) boardTabPlaceholder(w, h int) string {
	msg := m.styles.Muted.Render("starting the board session…")
	if m.boardErr != "" {
		msg = m.styles.Muted.Render(m.boardErr)
	}
	return centeredNotice(w, h, msg)
}

// boardThreadView renders the board session's conversation into the
// agent tab's main pane — threadView's counterpart, minus the measure
// split (maxBoardScroll below does that itself, the same way
// maxThreadScroll does for the card thread).
func (m *Shell) boardThreadView(w, h int) string { return m.boardThreadRender(w, h, false) }

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
	head := []string{clip(boardHeader(s, snap))}
	head = append(head, "")
	if sep > 0 {
		// the leading blank separates the masthead from the page's crumb
		// above it, the same reason threadRender adds one.
		head = append([]string{""}, head...)
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
			add("  " + s.Info.Render(m.spinner()+" thinking…"))
		}
	}
	if len(body) == 0 {
		add(boardEmptyLine(s))
	}
	body = trimTrailingBlanks(body)

	// --- foot ---
	foot := make([]string, sep)
	for _, l := range strings.Split(m.boardInputBlock(inner), "\n") {
		foot = append(foot, clip(l))
	}

	// the measure wants every row there is (composeThread's h<=0 branch),
	// having laid the head and the foot out at the real height — see
	// threadRender's own comment on why the two must not disagree.
	composeH := h
	if measure {
		composeH = 0
	}
	return strings.Join(composeThread(s, head, body, nil, foot, composeH, m.boardScroll, inner), "\n")
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
	// refused outright by claude/zz and would hang the others, since no
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
const boardPlaceholderText = "message the board — it can read and act on every card"

// boardInputBlock is the board thread's bottom input slot — inputBlock's
// counterpart, minus everything that does not apply here: no
// DrivenAbroad (a board session belongs to no other process to withhold
// it from), no confirm chip, no verb vocabulary. Just the composer,
// which carries its own styling already (newThreadInput) — unlike
// inputBlock, this needs no *theme.Styles of its own.
func (m *Shell) boardInputBlock(w int) string {
	m.boardInput.Placeholder = boardPlaceholderText
	// SetWidth reruns the widget's own recalculateHeight (DynamicHeight,
	// newThreadInput), so a resize rewraps the content and reflows the
	// composer's height along with it, exactly as inputBlock relies on.
	m.boardInput.SetWidth(max(w-2, 10))
	return m.boardInput.View()
}

// boardThreadSize is threadSize's board-thread counterpart: the main
// pane's dimensions, recomputed fresh (not read off m.layout) so a key
// handler running ahead of the next resize never scrolls against a
// stale height.
func (m *Shell) boardThreadSize() (int, int) {
	main := m.computeLayout().Main
	return main.Dx(), max(main.Dy(), 1)
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
func (m *Shell) handleBoardInputKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "alt+o":
		// mid-draft, like the card thread's own toggle — expanding a
		// captured tool output is never text.
		m.boardOutputs = !m.boardOutputs
		return nil
	case "pgup", "pgdown":
		m.scrollBoardThread(msg.String() == "pgup")
		return nil
	case "esc":
		// Unlike the card thread, where esc leaves the page (its composer
		// keeps the keyboard for good — threadinput.go's doc comment), the
		// agent tab has no separate "page" to leave: the tab IS the board
		// conversation, so esc's one honest job here is interrupting a
		// live turn — the same key a hosted CLI would have taken for
		// itself, before this surface replaced it.
		return m.interruptBoardSession()
	case "enter":
		text := strings.TrimSpace(m.boardInput.Value())
		if text == "" {
			return nil
		}
		m.boardScroll = 0 // jump to the latest on send, as the card thread does
		return m.sendBoardMessage(text)
	}
	var cmd tea.Cmd
	m.boardInput, cmd = m.boardInput.Update(msg)
	return cmd
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

// sendBoardMessage delivers a line typed into the board composer as a
// turn to the board session — sendThreadMessage's board counterpart,
// minus the swapped-out-session guard that name carries (its own
// comment: eng.Get(id) != sess catches a card's session being replaced
// out from under a stale closure). There is nothing analogous to race
// here: engine.OpenBoard only ever replaces e.board on a fresh open, and
// m.board is refreshed by boardOpenedMsg every time that happens, so a
// captured *BoardSession never goes stale behind this closure the way a
// captured *Session could.
func (m *Shell) sendBoardMessage(text string) tea.Cmd {
	b := m.board
	if b == nil {
		m.notice = noticeMsg{text: "board session not open yet", isErr: true}
		return nil
	}
	m.boardInput.Reset()
	return func() tea.Msg {
		if err := b.Send(context.Background(), text); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return nil
	}
}
