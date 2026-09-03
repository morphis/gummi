package ui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The thread's input (thread.go's bottom slot) is a persistent textarea
// on Shell, not one rebuilt per render — so an unsent draft survives
// leaving the tab and coming back (boardSurfacesLive hides, never
// destroys, exactly like the chat pane's m.chat). There is only ever one
// widget, but not one draft: threadDrafts (backlog.go) holds every card's
// line except the one currently loaded into the widget, keyed by
// feature, and openCard/stepCard/closeCard swap the buffer in and out of
// it on every card change — so the persistence is per card, not global.
//
// It is FOCUSED the moment a card page opens, the way a coding agent's
// composer is: the thread is a conversation, and a conversation you have
// to unlock before you can answer it is a worse conversation. Typing
// goes straight in.
//
// It also never gives the keyboard back on esc. That was the original
// trade — esc blurred to the card's single-letter accelerators, esc again
// left the page — and it stopped paying: the accelerator layer it landed
// in has no surface of its own since the action list became an overlay,
// so the first esc appeared to open a mode rather than back out of
// anything, and the status bar swapping wholesale was the only sign it
// had happened. esc leaves the page now, in one press. What the layer
// uniquely offered was J/K; that is alt+j/alt+k here.
//
// The accelerators stay reachable from the line itself, which is where
// the two-way trade actually lives:
//
//   - as words. The closed verb vocabulary (verbs.go) covers what the
//     letters cover — "diff", "approve", "verify" — and the action
//     inventory lists them beside their keys.
//   - as the inventory. Up on an empty line opens it (the placeholder
//     says so), and it carries the keyless actions too — gate, duplicate
//     — which no accelerator ever reached.
//   - as globals. "/" opens the command menu; tab and alt+1/2/3 are
//     answered above every surface.
//
// The blurred accelerator layer still exists (backlog.go's backlogKey),
// but only a card another process drives can be in it: that card
// withholds the composer, so there is nothing for esc to leave from.
//
// While a decision is open the composer and the decision are one control
// (DESIGN §6.3): the composer's emptiness drives the highlight — an empty
// line keeps the highlight where the arrows put it and enter answers it,
// while a typed prose line aims it at the option that consumes words,
// whose label then names what enter will do with them — and enter
// delivers the words to what the screen names. A command never aims: the
// first word being a verb makes the line a command, so the parser (and
// its confirm chip) keeps verb-words while the decision keeps prose; the
// chip's esc hands the line back as a message untouched. With no
// decision, enter is a no-op and up opens the action inventory. None can
// surprise anyone mid-sentence — the moment there is text, they belong to
// the text.
type pendingChip struct {
	feature   domain.FeatureID
	verb      string
	remainder string
	// line is the exact composer text that raised this chip — what esc's
	// "send as a message" promise (threadSkipParse) is scoped to. The chip
	// never edits the buffer (handleChipKey), so this is also, always,
	// what m.threadInput.Value() holds for as long as the chip stands
	// (F2).
	line string
}

// The composer's placeholder is the one place the action inventory can
// be advertised, because it occupies exactly the state that opens it: an
// empty line. It is not decoration — with esc no longer blurring, up is
// how the card's actions are reached by key at all, and a route with no
// visible sign of itself is the thing this whole change is undoing.
//
// There used to be a second, quieter placeholder for while a decision was
// pinned above the line, because up belonged to the decision then and
// promising the inventory too would have been a lie one keystroke from
// being found out. F11 made that untrue: up still moves the highlight
// while the decision is open, but off the top of it the very same key
// escapes into the inventory — one route, reachable everywhere the
// composer is empty — so the two placeholders collapsed back into one.
const placeholderText = "message the agent, a verb (approve, verify, diff…), or ↑ for actions"

// drivenAbroadPlaceholderText is the composer's placeholder on a card
// another gummi process is driving: no verb vocabulary reaches it (this
// is the one row where every line is consult prose, implicitly and
// permanently, per the Chosen approach), so the placeholder says that
// rather than advertising verbs and an inventory that would silently
// do nothing here.
const drivenAbroadPlaceholderText = "ask a question — read-only while another process drives this card"

// threadInputMaxHeight caps how many rows the composer (newThreadInput's
// DynamicHeight) can grow to before it scrolls internally instead of
// taking more of the page (F18). composeThread's priority is foot >
// decision > head > body, so an uncapped composer would not just eat the
// conversation on a short terminal — it would eat it on an ordinary one
// too, and eventually the pinned decision, on nothing more than a long
// paste. The cap has to be a small number of rows in absolute terms, not
// a fraction of the window. Five is enough to read a multi-sentence
// kickoff note or bounce reason without scrolling — the prose these
// decisions actually carry (deliverDecisionWords) — while still leaving
// a normal-height thread most of its room for the conversation and the
// decision above the line; CharLimit stays 4000, so a longer message is
// still typeable, just scrolls inside the five rows like the one-row box
// always scrolled.
const threadInputMaxHeight = 5

// newThreadInput builds the composer, styled from the theme rather than
// left on the widget's own defaults.
//
// Those defaults are raw ANSI palette indices — white on black for the
// focused line, grey 240 for the placeholder — which is the one thing
// §6.2 rules out outright ("no raw colors in components, ever"). Against
// a truecolor charmtone surface they do not read as a quiet input; they
// read as a foreign box someone pasted onto the page, because that is
// literally what a 16-colour fill is next to everything around it.
//
// So: no fill at all. The composer is a line you type on, and the page
// already separates it with a row of its own (thread.go's sep) and names
// what enter does in the bar. What marks it is the ┃ down its left edge,
// which takes the accent while the keyboard is here and goes faint when
// it is not — the same question every other surface answers by colour,
// answered the same way (§6.2: focus is answerable without moving).
//
// No rule above or below it either. In this thread ─── already means a
// boundary in time: the stage rule and the folded receipts both use it
// that way, and spending the same glyph on a fixed edge of the chrome
// would make a spatial divider read as a temporal one.
func newThreadInput(s *theme.Styles) textarea.Model {
	in := textarea.New()
	in.Placeholder = placeholderText
	in.CharLimit = 4000
	in.ShowLineNumbers = false
	// grows with the wrapped line count instead of forcing every
	// multi-sentence note into a permanently scrolling one-row tail
	// (F18); threadInputMaxHeight's comment has the cap and the why.
	// recalculateHeight (the widget's own) reruns on every keystroke and
	// on SetWidth, so a resize rewraps and reflows the height too.
	in.DynamicHeight = true
	in.MinHeight = 1
	in.MaxHeight = threadInputMaxHeight
	in.SetHeight(1)

	plain := lipgloss.NewStyle()
	st := in.Styles()
	st.Focused.Base = plain
	st.Focused.CursorLine = plain // the fill this used to paint is the whole complaint
	st.Focused.Text = s.Base
	st.Focused.Placeholder = s.Faint
	st.Focused.Prompt = s.KeyHint
	st.Blurred.Base = plain
	st.Blurred.CursorLine = plain
	st.Blurred.Text = s.Subtle
	st.Blurred.Placeholder = s.Faint
	st.Blurred.Prompt = s.Faint
	// the cursor is the accent: with no fill behind it, it is what says
	// the keyboard is here and where the next character lands.
	st.Cursor.Color = s.Theme.Accent
	in.SetStyles(st)
	return in
}

// focusThreadInput gives the thread's input the keyboard. A card driven
// by another process gets it too now — its lines go straight to that
// card's consult session (submitThreadLine's own DrivenAbroad branch),
// never through the verb parser or anything that would touch a lock this
// process does not hold — so there is nothing left here to withhold the
// composer for.
func (m *Shell) focusThreadInput() {
	if _, ok := m.selected(); !ok {
		return
	}
	m.threadInput.Focus()
}

// blurThreadInput hands the keyboard back to the card's accelerators
// without discarding the draft — "leaving hides, never discards" applies
// within the page too, not just across a tab switch. Arming ends with
// the blur: refocusing returns to the picker contract (threadinput.go's
// doc comment owns the full story).
//
// esc is no longer a caller — leaving the page keeps the composer focused
// (closeCard). This now runs only for the no-selected-card guard below,
// which is not reachable in practice.
func (m *Shell) blurThreadInput() {
	m.threadInput.Blur()
	m.threadChip = nil
	m.threadFreeForm = false
	m.threadAsk = false
}

// handleThreadInputKey routes a key while the thread input has the
// keyboard (shell.go's handleKey, gated on m.cardOpen &&
// m.threadInput.Focused()).
//
// While the composer is armed as the open ask's free-form channel ('o' —
// the chat pane's channel, inherited with its retirement), the composer
// owns every key the way a plain input does: the decision's picker keys
// stand down so a line that starts with a digit types as prose, and
// enter delivers the line verbatim as the answer. Esc still blurs with
// the draft kept — refocusing disarms.
func (m *Shell) handleThreadInputKey(msg tea.KeyPressMsg) tea.Cmd {
	r, ok := m.selected()
	if !ok {
		// Not reachable in practice — focusThreadInput already refuses —
		// but never leave a focused input feeding no selected card.
		m.blurThreadInput()
		return nil
	}
	// alt+o, alt+j/alt+k and pgup/pgdown all predate the chip and have to
	// keep working over it (F6): they are a modifier chord and page keys,
	// never text, and a pending chip used to route here first — its
	// default case cancels whatever it does not recognise and forwards
	// the key to the textarea, which does nothing with a chord, so the
	// chip died for nothing and the key still did not fire. Hoisted above
	// the chip branch, they leave it standing: a pending chip is exactly
	// when you might want to scroll up and check what you are about to
	// confirm, or step to another card without losing your place in this
	// one's.
	//
	// alt+o, not ctrl+o: zellij binds ctrl+o to session mode, so the
	// toggle never arrived there. The composer owns every printable key,
	// so this needs a modifier — alt is the one no multiplexer claims,
	// and alt+enter already sets that precedent in the forms.
	if msg.String() == "alt+o" {
		m.threadOutputs = !m.threadOutputs
		return nil
	}
	// alt+j/alt+k are the board's J/K, which type here: stepping to the
	// previous or next card is the one thing the retired blur layer
	// offered that nothing else does, so it needs a key a focused
	// composer can't swallow. Alt for the same reason alt+o takes it.
	switch msg.String() {
	case "alt+k":
		return m.stepCard(-1)
	case "alt+j":
		return m.stepCard(1)
	case "pgup", "pgdown":
		// scrolling the conversation is never text, so it works mid-draft
		m.scrollThread(msg.String() == "pgup")
		return nil
	case "alt+s":
		// the artifact the pinned line names, opened from the line that
		// names it. That line has always advertised a key, but the plain
		// s it used to name is a printable one, so the composer swallowed
		// it and the reader who pressed it got the letter in their draft
		// instead of the document — the hint could not fire on the only
		// surface that draws it. Alt for the same reason alt+o and
		// alt+j/k take it, and hoisted here with them so checking what a
		// pending chip is about to confirm works the way scrolling up to
		// re-read does.
		return m.openSpec(r.F)
	}
	// A chip left standing by a step away (F6) belongs to the card that
	// raised it, not to whichever one is selected now — inputBlock
	// already renders it that way (m.threadChip.feature == r.F.ID), and
	// handleChipKey has to be gated the same way: it fires the chip's
	// verb via fireVerb, which acts on the currently selected card, so
	// running it from a different card than the one the chip belongs to
	// would confirm the wrong card's action. Off its own card, a chip is
	// just data waiting to be seen again — every key here is ordinary
	// composer input instead.
	if m.threadChip != nil && m.threadChip.feature == r.F.ID {
		return m.handleChipKey(msg)
	}
	switch msg.String() {
	case "esc":
		if m.threadFreeForm {
			// armed by 'o', and visibly so — the decision names the
			// free-form option and the bar names the key. esc backs out of
			// that, and the decision's own picker keys come back; the draft
			// stays, since disarming is not discarding.
			m.threadFreeForm = false
			return nil
		}
		if m.threadAsk {
			// armed by `ask` — esc drops the consult channel and restores
			// steering, the draft kept (arming is not discarding, same as
			// threadFreeForm's own esc).
			m.threadAsk = false
			return nil
		}
		// nothing pending: esc leaves the page. The composer keeps both
		// its focus and its draft — the card page hides, it is never
		// discarded (backlog.go's closeCard), so coming back finds the
		// line exactly as it was.
		m.closeCard()
		return nil
	case "enter":
		text := strings.TrimSpace(m.threadInput.Value())
		if text == "" {
			if m.threadFreeForm || m.threadAsk {
				// armed, an empty line sends nothing — the pane's own
				// free-form contract, inherited
				return nil
			}
			if d := m.visibleDecision(r); d != nil {
				m.syncDecision(d)
				m.threadScroll = 0 // jump to the newest, where the answer lands
				return m.answerDecision(r, d)
			}
			return nil
		}
		m.threadScroll = 0 // jump to the latest on send, as the pane did
		if m.threadAsk {
			// armed: every key already types unmolested (the branch just
			// below), so enter delivers straight to consult — never the
			// decision/verb machinery submitThreadLine would otherwise run.
			return m.sendConsultMessage(r.F, text)
		}
		return m.submitThreadLine(r, text)
	}
	if m.threadFreeForm || m.threadAsk {
		// armed: the line is the answer (or the next consult question),
		// not a picker — everything else is text
		cmd := m.updateThreadInput(msg)
		m.clearSkipParseIfEmptied()
		return cmd
	}
	switch msg.String() {
	case "up", "down":
		if strings.TrimSpace(m.threadInput.Value()) == "" {
			if d := m.visibleDecision(r); d != nil {
				m.syncDecision(d)
				if msg.String() == "up" && m.decisionCursor == 0 {
					// ↑ off the top of the decision is the one route to
					// the action inventory (F11): the actions sit
					// visually above the decision anyway, so keep
					// pressing up and you eventually reach them — no
					// second key to already know about, and no wrap to
					// make "the top" ambiguous (moveDecision no longer
					// wraps, for the same reason).
					m.openCardActions(r)
					return nil
				}
				delta := 1
				if msg.String() == "up" {
					delta = -1
				}
				m.moveDecision(d, delta)
				return nil
			}
		}
		// No decision pinned: up alone reaches the inventory (down has
		// nothing to move through, so it stays ordinary textarea input).
		if strings.TrimSpace(m.threadInput.Value()) == "" {
			if msg.String() == "up" {
				m.openCardActions(r)
				return nil
			}
		}
	case "space":
		if strings.TrimSpace(m.threadInput.Value()) == "" {
			if d := m.visibleDecision(r); d != nil && d.ask != nil && d.ask.MultiPick && len(d.ask.Options) > 0 {
				m.syncDecision(d)
				// the synthetic "Chat about this" row (index len(d.ask.Options),
				// present iff FreeForm) has no tick box — decisionAnswerText
				// never reads it — so toggling here would record picked state
				// the render doesn't show and the answer ignores.
				if m.decisionCursor < len(d.ask.Options) {
					m.decisionPicked[m.decisionCursor] = !m.decisionPicked[m.decisionCursor]
				}
				return nil
			}
		}
	case "o":
		// the free-form channel: with a question that allows it, 'o' arms
		// the composer as the answer — the picker keys stand down so the
		// words type unmolested, digit-leading included (the pane's own
		// 'o', inherited when the pane retired).
		if strings.TrimSpace(m.threadInput.Value()) == "" {
			if d := m.visibleDecision(r); d != nil && d.ask != nil && d.ask.FreeForm {
				m.syncDecision(d)
				// syncDecision alone won't move the cursor here: wordAim's
				// ask branch only aims once the composer holds prose, and
				// this handler is gated on an empty one — so land on the
				// synthetic row explicitly, the same index digit-select and
				// answerDecision's enter guard reach.
				m.decisionCursor = len(d.ask.Options)
				m.threadFreeForm = true
				return nil
			}
		}
	}
	// A digit SELECTS, never commits (F14): on a live ask it used to fire
	// the answer immediately — one stray keystroke with no confirm and no
	// undo, and option 2 on a gate is routinely "approve — creates the
	// worktree and starts the agent stages". Now it only moves the
	// highlight, the same as ↑↓, for both an ask and a workflow decision
	// alike — enter is what commits. This is also what stops a workflow
	// decision's digit from typing into the composer and letting wordAim
	// yank the highlight onto the word-consuming option instead of the
	// one the digit named (the four-option-gate bug: pressing 2 used to
	// select 1). Multi-pick keeps space as its own toggle, unchanged.
	if strings.TrimSpace(m.threadInput.Value()) == "" {
		if d := m.visibleDecision(r); d != nil {
			if key := msg.String(); len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
				i := int(key[0] - '1')
				if i < d.optionCount() {
					m.syncDecision(d)
					m.decisionCursor = i
					m.decisionAimed = false
					return nil
				}
			}
		}
	}
	cmd := m.updateThreadInput(msg)
	m.clearSkipParseIfEmptied()
	return cmd
}

// updateThreadInput runs msg through the composer textarea and tags the
// result as a subscription (subscription.go) when there is one. The only
// commands textarea.Update produces for a plain key are its own — a
// viewport scroll and, on every cursor move, bubbles' cursor.Blink
// restarting the on-screen caret's blink timer — and Blink is exactly the
// re-arming, indefinite-lived timer subscription() exists to flag: nothing
// in this package ever wants to observe cursor.BlinkMsg, but flow_test.go's
// pump doesn't know that unless told, so left unmarked it ran that 530ms
// timer to completion on every single keystroke a test typed into this
// composer — real work in the live TUI, since Bubble Tea's own runtime
// executes commands off the update loop, but a serial 530ms tax per
// character in a test harness that runs commands to find out what they
// say next.
func (m *Shell) updateThreadInput(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.threadInput, cmd = m.threadInput.Update(msg)
	if cmd != nil {
		cmd = subscription(cmd)
	}
	return cmd
}

// clearSkipParseIfEmptied drops a still-armed chip promise the moment the
// line it was made about is gone (ctrl+u, most commonly) — an emptied
// composer cannot be the line threadSkipParse names, and leaving the
// promise standing would let retyping the very same words later reuse a
// "send as a message" that was never made about that fresh line (F2).
// submitThreadLine's own text comparison would already refuse a
// DIFFERENT line, but two typings of the same words are indistinguishable
// by text alone, so the buffer going empty is the signal to use instead.
func (m *Shell) clearSkipParseIfEmptied() {
	if m.threadInput.Value() == "" {
		m.threadSkipParse = ""
	}
}

func (m *Shell) openCardActions(r featureRow) {
	l := m.cardActions()
	d := newCardActionsDialog(string(r.F.ID), l, func(cursor int, expanded bool) {
		m.actionCursor = cursor
		m.actionsExpanded = expanded
	}, func(a cardAction) tea.Cmd {
		m.clearTransientNotice()
		return m.runCardAction(a)
	})
	m.Overlay.Push(d)
}

// handleThreadPaste routes a bracketed paste into the thread input
// (shell.go's handlePaste). A pending chip is cancelled first, the same
// as any other key reaching handleChipKey's default case.
func (m *Shell) handleThreadPaste(msg tea.PasteMsg) tea.Cmd {
	m.threadChip = nil
	return m.updateThreadInput(msg)
}

// submitThreadLine routes a non-empty line from the composer, deciding
// first whether a decision gets it. With one open, the line and the
// decision are one control (DESIGN §6.3): classified prose is aimed at
// the option that consumes words and enter delivers it there — the ask's
// free-form answer, or the run/bounce whose delivery takes prose. A
// command keeps the parser: a line whose first word is a verb routes
// exactly as the bare composer routes it, chip included, because the
// classification is deterministic and context-free and the mitigation
// belongs at the point of action. Prose nothing consumes, and the chip's
// esc contract, send as a message — always safe prose (§6.3).
//
// One state outranks the classification: threadSkipParse holds the exact
// line a chip's esc promised to send as a message (handleChipKey). It is
// honoured only when the submitted text still matches — a promise made
// about "clean" must not get spent on "spec" typed after a ctrl+u, which
// is the whole bug F2 fixes — and it is spent either way, matched or not:
// a promise is good for one submit of the line it was made about, never
// carried past it.
//
// A card driven by another process outranks all of it: it has no verb
// vocabulary here at all (Chosen approach) — every submitted line is
// consult prose, verbatim, so this never even reaches parseInput for
// such a row. That is what keeps a foreign card's composer from ever
// reaching fireVerb/engine.Send against a lock this process does not
// hold.
func (m *Shell) submitThreadLine(r featureRow, text string) tea.Cmd {
	if r.DrivenAbroad {
		return m.sendConsultMessage(r.F, text)
	}
	skip := m.threadSkipParse != "" && m.threadSkipParse == text
	m.threadSkipParse = ""
	if skip {
		return m.sendThreadMessage(r.F, text)
	}
	if d := m.visibleDecision(r); d != nil {
		m.syncDecision(d)
		if m.threadFreeForm && d.ask != nil && d.ask.FreeForm {
			return m.answerAskWith(r, text)
		}
		if parseInput(text).Kind == verbNone {
			if d.ask != nil && d.ask.FreeForm {
				return m.answerAskWith(r, text)
			}
			if i := d.wordConsumer(); i >= 0 {
				m.decisionCursor = i
				return m.deliverDecisionWords(r, d, i, text)
			}
		}
		// a command keeps the parser; prose nothing consumes sends as a
		// turn — always safe, and exactly what the bare composer did.
	}
	return m.submitThreadInput(r.F)
}

// submitThreadInput parses a line and routes it as the bare composer
// always has (no decision open, or a line the decision does not claim):
//
//   - verbNone -> sent as a message to the agent, exactly as the chat
//     pane's own send path does.
//   - verbMenu whose remainder is ITSELF a recognised verb ("/park",
//     "/verify the csv path") -> routed exactly like the bare word, via
//     routeVerb below, never touching the menu at all. This is the fix
//     for the two-vocabularies bug: the command menu (m.globalCommands)
//     only ever carried board-level and card actions, never the closed
//     verb vocabulary (verbs.go), so "/park" used to search a list that
//     had no "park" in it and report "no commands match" while bare
//     "park" fired straight through fireVerb. Re-parsing the remainder on
//     its own reuses parseInput's exact-first-word rule, so "/appro" (not
//     an exact match) still falls through to the menu below, filtered —
//     only a real, whole verb word short-circuits it.
//   - verbMenu otherwise -> the same command menu overlay boardKey's
//     space key opens, pre-filtered by whatever followed the "/". On a
//     card page that menu now also carries the selected card's own
//     action inventory (globalCommands), so "/envelope", "/duplicate"
//     and the rest of cardactions.go's list — none of them in the verb
//     vocabulary — find something too, instead of needing a second,
//     narrower inventory here.
//   - verbCommand -> routeVerb: fires immediately (a free/navigational
//     verb with no remainder) or raises the inline confirm chip.
//
// The chip's own "esc no, send as a message" promise (threadSkipParse) is
// resolved by submitThreadLine before this ever runs — it either sends
// straight through sendThreadMessage or falls through here with the
// promise already spent, so this function parses every line it sees.
func (m *Shell) submitThreadInput(f domain.Feature) tea.Cmd {
	text := strings.TrimSpace(m.threadInput.Value())
	if text == "" {
		return nil
	}
	parsed := parseInput(text)
	switch parsed.Kind {
	case verbMenu:
		if sub := parseInput(parsed.Remainder); sub.Kind == verbCommand {
			return m.routeVerb(f, sub.Verb, sub.Remainder, text)
		}
		cm := newCommandMenu(m.globalCommands(), m.runCommand)
		if parsed.Remainder != "" {
			cm.filter.SetValue(parsed.Remainder)
			cm.setCursor(0)
		}
		m.Overlay.Push(cm)
		m.threadInput.Reset()
		return nil
	case verbCommand:
		return m.routeVerb(f, parsed.Verb, parsed.Remainder, text)
	default: // verbNone
		return m.sendThreadMessage(f, text)
	}
}

// routeVerb fires or chips a recognised verb — the one decision both a
// bare command line and a "/verb" line reach (submitThreadInput above),
// so they can never land on two different outcomes for the same word.
// State-changing verbs always chip; a free/navigational one (diff, spec,
// park) chips too the moment it carries a remainder it has nowhere to
// spend — raising the chip rather than firing and silently dropping words
// the user typed on purpose. line is the exact composer text (slash
// included, when this came from "/verb") that the chip's esc hands back
// as a message — pendingChip.line's own contract.
func (m *Shell) routeVerb(f domain.Feature, verb, remainder, line string) tea.Cmd {
	if verb == "ask" {
		// ask is never a chip: asking is never destructive, and unlike
		// diff/spec (which chip the moment a remainder has nowhere to
		// spend) ask DOES have somewhere to spend one — arm the composer
		// against the consult session and, when the line carried a
		// question already, deliver it as the first consult turn in the
		// same motion (Chosen approach's own wording).
		m.threadAsk = true
		if remainder == "" {
			m.threadInput.Reset()
			return nil
		}
		return m.sendConsultMessage(f, remainder)
	}
	if !chipVerbs[verb] && remainder == "" {
		m.threadInput.Reset()
		return m.fireVerb(verb, remainder)
	}
	m.threadChip = &pendingChip{feature: f.ID, verb: verb, remainder: remainder, line: line}
	return nil
}

// handleChipKey drives the inline confirm chip.
func (m *Shell) handleChipKey(msg tea.KeyPressMsg) tea.Cmd {
	chip := m.threadChip
	switch msg.String() {
	case "enter":
		m.threadChip = nil
		m.threadInput.Reset()
		return m.fireVerb(chip.verb, chip.remainder)
	case "esc":
		// The chip never touched the input's buffer — it only overlays
		// the rendered line (inputBlock) — so "putting the line back" is
		// just ceasing to render the chip over it. threadSkipParse carries
		// chip.line, the exact text this promise was made about:
		// submitThreadLine only honours it when a submit's text still
		// matches, so it cannot outlive the line that raised it (F2) —
		// ctrl+u and a different word, or the same word retyped fresh
		// after ctrl+u (clearSkipParseIfEmptied), both fall through to the
		// parser instead of silently sending as a message.
		m.threadChip = nil
		m.threadSkipParse = chip.line
		return nil
	}
	// Any other key backs out of the chip and resumes editing in place —
	// the original line is untouched, so this keystroke just continues
	// where the user left off (a correction, more text, a delete).
	m.threadChip = nil
	return m.updateThreadInput(msg)
}

// verbKeys maps a parsed verb to the board accelerator that performs the
// same action (shell.go's boardVerb), for the verbs already wired to one.
// Every entry here has to land on a boardVerb case that means what the
// word means for every state that case can run in — not just the state
// someone happened to test it in — because fireVerb hands the key
// straight to boardVerb with no card-page context of its own.
//
// squash maps to "z", not the "S" its own key would suggest: "S" is
// already bound to the backlog's sort-order toggle (shell.go's boardVerb),
// and "z" is the board's actual squash-in-place accelerator. Mapping to
// "S" here would silently reorder the backlog instead of squashing.
//
// park is deliberately ABSENT, not mapped to "p": boardVerb's "p" case is
// two board-only actions sharing one reused key — pause a live
// autonomous session, or (with nothing to pause) open the dependency
// picker. That fallback is fine for a physical key someone can see is
// dual-purpose from the board itself, but a word typed on purpose never
// means "open something unrelated" — a user who types "park" means pause,
// full stop, the same thing cardevents.go's own comments call "a human
// parked them with p" (ParkPayload, QuitStopped). fireVerb special-cases
// "park" below to reach exactly that action and nothing else (parkVerb,
// shell.go), rather than routing through the ambiguous key.
var verbKeys = map[string]string{
	"approve": "g",
	"diff":    "d",
	"spec":    "s",
	"verify":  "v",
	"land":    "m",
	"rebase":  "r",
	"clean":   "c",
	"squash":  "z",
}

// chipVerbs is the set of state-changing verbs that always raise the
// confirm chip rather than firing straight from the input. verify and
// changes are ordinary English first words — "verify the CSV path is
// right" would otherwise run the checks — so the mitigation lives here,
// at the point of action, rather than in parseInput: the parse stays a
// deterministic, context-free classification, and nothing about it is
// ever guessed.
//
// spec and diff collide the same way and are deliberately absent: they
// only navigate, so the worst a misfire costs is a view you can esc out
// of, which is cheaper than a confirm on every use.
var chipVerbs = map[string]bool{
	"approve":   true,
	"verify":    true,
	"bounce":    true,
	"land":      true,
	"rebase":    true,
	"squash":    true,
	"clean":     true,
	"changes":   true,
	"autopilot": true,
	"park":      true,
}

// fireVerb performs a parsed verb's action: routes to the same key
// boardVerb already answers when one is mapped (against whichever card is
// currently selected — the same card the chip and the input belong to),
// special-cases "park" (verbKeys' doc comment has the why), otherwise
// (changes, bounce, autopilot) reports that it isn't wired to an action
// yet rather than inventing new engine behaviour for it.
func (m *Shell) fireVerb(verb, remainder string) tea.Cmd {
	if verb == "park" {
		return m.parkVerb()
	}
	if key, ok := verbKeys[verb]; ok {
		return m.boardVerb(key)
	}
	return m.notWiredVerb(verb, remainder)
}

// notWiredVerb is changes/bounce/autopilot's landing spot: parsed and
// carried through (including the remainder, so it isn't silently
// dropped), but not yet routed to an action — a later change owns giving
// them one. bounce already has a card-level accelerator (shell.go's "b",
// msgs.go's bounceStage) with matching semantics for the no-remainder
// case, but this verb path stays unwired on purpose so a bounce typed
// with a reason isn't half-implemented ahead of that later change.
func (m *Shell) notWiredVerb(verb, remainder string) tea.Cmd {
	text := verb + " isn't wired to an action yet"
	if remainder != "" {
		text += " — " + remainder
	}
	m.notice = noticeMsg{text: text}
	return nil
}

// sendThreadMessage delivers a line typed into the thread as a turn — to
// the card's own live stage session when one is genuinely steerable
// (Session.Live()), or to its consult session otherwise. This is the fix
// for every non-live row in the Problem table at once: a session that is
// merely absent, not-yet-attached, paused, restored, or done all read
// Live()==false the same way, so all of them fall through to consult
// instead of the old blanket "no live session to message" refusal.
//
// The composer is cleared here, not by the caller, and only once a live
// session is confirmed for the steer path: with no session to hand the
// text to, the line stays exactly where it was typed instead of
// vanishing along with the notice explaining why it wasn't sent (F8) —
// attach (enter) and resend, rather than retype from memory. The consult
// path (sendConsultMessage) has no such precondition to fail against
// synchronously, so it always clears.
func (m *Shell) sendThreadMessage(f domain.Feature, text string) tea.Cmd {
	sess := m.sessionFor(f.ID)
	if !sess.Live() {
		return m.sendConsultMessage(f, text)
	}
	m.threadInput.Reset()
	eng := m.engine
	id := f.ID
	return func() tea.Msg {
		if eng.Get(id) != sess {
			return noticeMsg{text: "session is no longer active", isErr: true}
		}
		if err := eng.Send(context.Background(), id, text); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return nil
	}
}

// sendConsultMessage delivers a line to the card's consult session,
// opening one first if this is the first question asked about this card
// (engine.Engine.OpenConsult is idempotent, so every later call just
// reuses it). Unlike sendThreadMessage's steer path, there is no
// live-session precondition to fail against synchronously — a consult
// session takes no lock and competes for no attention slot — so the
// composer always clears here.
func (m *Shell) sendConsultMessage(f domain.Feature, text string) tea.Cmd {
	if m.engine == nil {
		m.notice = noticeMsg{text: "no agent configured (set a model/provider to enable agents)"}
		return nil
	}
	m.threadInput.Reset()
	eng := m.engine
	return func() tea.Msg {
		c, err := eng.OpenConsult(context.Background(), f)
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		if err := c.Send(context.Background(), text); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return nil
	}
}

// chipQuestions is the confirm chip's per-verb question, e.g. "verify ·
// run the checks?".
var chipQuestions = map[string]string{
	"approve":   "advance the stage?",
	"verify":    "run the checks?",
	"bounce":    "send it back to work?",
	"land":      "merge into main?",
	"rebase":    "rebase onto main?",
	"squash":    "squash onto main?",
	"clean":     "remove the worktree?",
	"changes":   "request changes?",
	"autopilot": "change autopilot mode?",
	"park":      "pause the run and free the slot?",
}

// view renders the inline confirm chip line: "verb · question? enter
// yes · esc no, send as a message".
func (c *pendingChip) view(s *theme.Styles) string {
	q := chipQuestions[c.verb]
	if q == "" {
		q = "run it?"
	}
	return s.Warning.Render(c.verb) + s.Faint.Render(" · "+q+" ") +
		s.KeyHint.Render("enter") + s.Faint.Render(" yes · ") +
		s.KeyHint.Render("esc") + s.Faint.Render(" no, send as a message")
}

// inputBlock is the thread's bottom input slot (thread.go's threadView).
// A card owned by another process gets the composer too now, ask-only —
// every line it accepts goes straight to that card's consult session
// (submitThreadLine's DrivenAbroad branch), never through the verb
// vocabulary, so there is nothing here left to withhold. It renders the
// pending confirm chip in place of the box when one is standing (never
// true for a DrivenAbroad row, since routeVerb is never reached for one),
// or the persistent textarea itself.
func (m *Shell) inputBlock(s *theme.Styles, r featureRow, w int) string {
	if m.threadChip != nil && m.threadChip.feature == r.F.ID {
		return ansi.Truncate(m.threadChip.view(s), w, "…")
	}
	if r.DrivenAbroad {
		m.threadInput.Placeholder = drivenAbroadPlaceholderText
	} else {
		// up reaches the inventory whether or not a decision is pinned
		// above the line (F11), so the placeholder no longer has to pick
		// between two versions of that promise.
		m.threadInput.Placeholder = placeholderText
	}
	// SetWidth reruns the widget's own recalculateHeight (DynamicHeight,
	// newThreadInput), so a resize rewraps the content and reflows the
	// composer's height along with it, not just its rows' width.
	m.threadInput.SetWidth(max(w-2, 10))
	// The chat pane gives the composer three rows because it is the whole
	// surface there. Here it sits under the thread, which owns the
	// height — but it takes one and grows only when someone is actually
	// typing enough to wrap (F18): DynamicHeight does that on its own,
	// up to threadInputMaxHeight, on every keystroke and above.
	return m.threadInput.View()
}

// threadInputBindings is the card page's key table while the thread
// input has the keyboard — cardPageBindings' focused branch (backlog.go),
// the same filtering-split convention bugIngestView.bindings() uses. The
// enter row names what enter will do in THIS state: the highlighted
// option's label on an empty line, the words' destination while one is
// typed (wordAim), and plain "send" when nothing consumes the words —
// the bar may not claim enter for a choice the line is not aimed at.
func (m *Shell) threadInputBindings() []binding {
	// A chip left standing by a step away (F6) is not this card's row
	// any more once it belongs to a different one — the same
	// m.threadChip.feature == r.F.ID gate inputBlock and
	// handleThreadInputKey use, so the bar never promises "confirm" for
	// a key that would now just be typed (handleThreadInputKey's chip
	// gate covers the reverse: why acting on it here would hit the wrong
	// card).
	if r, ok := m.selected(); ok && m.threadChip != nil && m.threadChip.feature == r.F.ID {
		return []binding{
			{key: "enter", label: "confirm", help: "run " + m.threadChip.verb, bar: true},
			{key: "esc", label: "cancel", help: "back out — the line goes back in the input as a message", bar: true},
		}
	}
	if m.threadAsk {
		// armed by `ask`: the composer owns the keyboard the way the
		// free-form answer channel does, but every line goes to the
		// card's consult session instead of a decision — sticky, so it
		// stays armed across a follow-up question without retyping `ask`.
		return []binding{
			{key: "enter", label: "ask", help: "your line is the next consult question — empty sends nothing", bar: true, sticky: true},
			{key: "pgup/pgdn", label: "scroll", help: "scroll the thread without leaving the line", bar: true},
			m.threadOutputsBinding(),
			{key: "alt+j/k", label: "prev/next", help: "next / previous card without leaving the page"},
			{key: "alt+s", label: "open", help: "open the artifact the pinned line names"},
			{key: "esc", label: "steer", help: "drop the consult channel — a plain line steers again (the draft is kept)", bar: true},
		}
	}
	if r, ok := m.selected(); ok {
		if d := m.visibleDecision(r); d != nil {
			aim := m.wordAim(d)
			text := strings.TrimSpace(m.threadInput.Value())
			typed := text != ""
			freeForm := d.ask != nil && d.ask.FreeForm
			if m.threadFreeForm && freeForm {
				// armed: the composer owns the keyboard the way a plain
				// input does; enter delivers the line as the answer
				return []binding{
					// sticky (F15): this enter delivers the line as the
					// decision's answer — on a gate that can mean
					// attaching an agent — so a tight bar sheds pgup/pgdn
					// and outputs before it ever touches this row, not the
					// other way around.
					{key: "enter", label: "answer", help: "your line is the answer — empty sends nothing", bar: true, sticky: true},
					{key: "pgup/pgdn", label: "scroll", help: "scroll the history above the pinned decision", bar: true},
					m.threadOutputsBinding(),
					{key: "alt+j/k", label: "prev/next", help: "next / previous card without leaving the page"},
					{key: "alt+s", label: "open", help: "open the artifact the pinned line names"},
					{key: "esc", label: "picker", help: "drop the free-form channel — the decision's own keys come back (the draft is kept)", bar: true},
				}
			}
			label, help := "answer", "answer the highlighted option"
			switch {
			case d.ask != nil && typed && d.ask.FreeForm:
				help = "your line is the answer"
			case typed && aim < 0:
				// aim is -1 for three reasons now, not two: prose a
				// workflow decision has nowhere to spend (send is right),
				// a verb-leading line the parser owns regardless of what
				// the decision offers, or — F4 — a structured (non-
				// free-form) ask, which wordAim refuses to aim at
				// unconditionally (decision.go) because there is nothing
				// on it that takes prose. submitThreadLine falls all
				// three through to submitThreadInput exactly alike, so
				// the bar names that real destination instead of the
				// picker's "answer" — DESIGN §6.3 keeps a structured
				// ask's terms and routes prose as a turn, and the bar may
				// not claim enter for a choice the line is not aimed at
				// (F7). The parse is deterministic and context-free, so
				// asking it directly is safe here.
				label, help = threadEnterLabel(text)
			case d.ask == nil:
				// the option the highlight sits on — the word-eater while
				// the composer holds prose aimed at it, the cursor's own
				// choice otherwise. The bar carries the option's own name:
				// the relabel ("with your words") is the decision row's
				// statement, and a suffix long enough to spell the words
				// out here is the first hint the bar drops.
				i := aim
				if i < 0 {
					i = m.decisionCursor
				}
				if i >= 0 && i < len(d.actions) {
					label = d.actions[i].label
					if i == aim {
						help = "your line goes with this answer"
					}
				}
			}
			bs := []binding{
				{key: "↑↓", label: "choose", help: "move through the open decision — ↑ off the top opens the action inventory (dependencies, envelope, duplicate, delete, repo, rebase, auto-approve-gates…)", bar: true},
				{key: "1-9", label: "choose", help: "jump the highlight straight to that option — still just selects; enter commits"},
				// sticky (F15): this row names what enter actually commits —
				// an option label that can read "start the architect", an
				// attach that spends credits. A tight bar (a 120-column
				// terminal with the board's usual pills and a long option
				// label falls a few characters short of fitting everything)
				// must shed pgup/pgdn and outputs first; silently collapsing
				// to "↑↓ choose · esc backlog" would drop the one row saying
				// enter does anything consequential at all.
				{key: "enter", label: label, help: help, bar: true, sticky: true},
				{key: "verb", label: "command", help: "type a verb (approve, verify, diff…) instead of choosing — the same vocabulary the empty composer takes"},
				{key: "pgup/pgdn", label: "scroll", help: "scroll the history above the pinned decision", bar: true},
			}
			if freeForm {
				bs = append(bs, binding{key: "o", label: "own answer", help: "type your own answer — the digits stop picking while it's armed", bar: true})
			}
			bs = append(bs, m.threadOutputsBinding(),
				binding{key: "alt+j/k", label: "prev/next", help: "next / previous card without leaving the page"},
				binding{key: "alt+s", label: "open", help: "open the artifact the pinned line names"})
			// esc stays last: the status bar drops hints from the
			// second-to-last backwards precisely so the surface's escape
			// hatch outlives every other row (statusbar.Render).
			return append(bs, binding{key: "esc", label: "backlog", help: "back to the backlog list (the draft is kept)", bar: true})
		}
	}
	return []binding{
		{key: "enter", label: "send", help: "send the line — a message, or route a verb command; does nothing when the line is empty", bar: true},
		{key: "↑", label: "actions", help: "open the action inventory while the line is empty (the placeholder says so too)", bar: true},
		{key: "pgup/pgdn", label: "scroll", help: "scroll the thread without leaving the line", bar: true},
		m.threadOutputsBinding(),
		{key: "alt+j/k", label: "prev/next", help: "next / previous card without leaving the page"},
		{key: "alt+s", label: "open", help: "open the artifact the pinned line names"},
		// esc last, for the reason the decision table gives above: the
		// bar sheds the second-to-last hint first, so the way out is the
		// last thing to go.
		{key: "esc", label: "backlog", help: "back to the backlog list (the draft is kept)", bar: true},
	}
}

// threadEnterLabel names what enter will actually do with a verb-leading
// line while a decision is open and wordAim came back -1 for it (F7): the
// parser owns a command line regardless of what the decision offers, so
// it routes exactly as the bare composer would (submitThreadInput) —
// immediately for a free verb with no remainder, through the confirm
// chip for everything chipVerbs marks, and as a plain message for prose.
// parseInput is deterministic and context-free, so this can ask it
// directly rather than guess from wordAim's -1 alone.
func threadEnterLabel(text string) (label, help string) {
	switch parsed := parseInput(text); parsed.Kind {
	case verbMenu:
		// A remainder that is itself a verb short-circuits the menu in
		// submitThreadInput (routeVerb) — the bar has to preview that
		// same destination, not "menu", or it would promise an overlay
		// enter never opens.
		if sub := parseInput(parsed.Remainder); sub.Kind == verbCommand {
			return verbEnterLabel(sub.Verb, sub.Remainder)
		}
		return "menu", "open the command menu, filtered by what follows the /"
	case verbCommand:
		return verbEnterLabel(parsed.Verb, parsed.Remainder)
	default: // verbNone: nothing here consumes it either, so it sends as a turn
		return "send", "send the line as a message"
	}
}

// verbEnterLabel names what routeVerb will do with a recognised verb —
// shared by threadEnterLabel's two callers (a bare verb line, and a
// "/verb" line whose remainder resolved to the same verb) so the bar's
// preview can never drift from routeVerb's own fire-or-chip decision.
func verbEnterLabel(verb, remainder string) (label, help string) {
	if verb == "ask" {
		// never a chip, and a remainder here is not dropped — it is
		// delivered as the first consult turn in the same motion
		// (routeVerb's own "ask" branch), so the preview never promises a
		// confirm this verb will never raise.
		if remainder == "" {
			return "ask", "arm the composer against the card's consult session"
		}
		return "ask", "arm and ask this question"
	}
	if !chipVerbs[verb] && remainder == "" {
		return verb, "run " + verb
	}
	return "confirm", "run " + verb + " — asks first"
}

// threadOutputsBinding is the alt+o row every card-page table carries:
// the toggle that expands (or folds back) the captured tool outputs in
// the thread, working mid-draft like pgup/pgdn — it is not text. The
// label names what alt+o will do NEXT, the same convention the decision
// row's own label follows above — "fold" while expanded is state you can
// read off the bar itself; before this the label stayed "outputs"
// either way and only the help text (behind alt+/) said which (F19).
func (m *Shell) threadOutputsBinding() binding {
	label, help := "outputs", "expand the captured tool outputs"
	if m.threadOutputs {
		label, help = "fold", "fold the captured tool outputs back"
	}
	return binding{key: "alt+o", label: label, help: help, bar: true}
}
