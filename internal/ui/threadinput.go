package ui

import (
	"context"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// The thread's input (thread.go's bottom slot) is a persistent textarea
// on Shell, not one rebuilt per render — so an unsent draft survives
// leaving the tab and coming back (boardSurfacesLive hides, never
// destroys, exactly like the chat pane's m.chat).
//
// It is FOCUSED the moment a card page opens, the way a coding agent's
// composer is: the thread is a conversation, and a conversation you have
// to unlock before you can answer it is a worse conversation. Typing
// goes straight in.
//
// That leaves the page's single-letter accelerators reachable two ways
// rather than one, which is the trade this makes deliberately:
//
//   - as words. The closed verb vocabulary (verbs.go) covers what the
//     letters cover — "diff", "approve", "verify" — and the next card
//     lists them beside their keys.
//   - as keys, after esc. Blurring hands the keyboard back to the
//     accelerator table with the draft intact; esc again leaves the page.
//
// Two keys keep their old meaning while the composer is empty, because
// an empty composer has nothing for them to do: enter runs the next
// card's highlighted action, and up/down move its cursor. Neither can
// surprise anyone mid-sentence — the moment there is text, both belong
// to the text.
type pendingChip struct {
	feature   domain.FeatureID
	verb      string
	remainder string
}

// newThreadInput builds the thread's persistent input, mirroring
// chat.go's newChatInput for visual consistency with the pane it stands
// in for.
func newThreadInput() textarea.Model {
	in := textarea.New()
	in.Placeholder = "message the agent, or a verb (approve, verify, diff…)"
	in.CharLimit = 4000
	in.ShowLineNumbers = false
	in.SetHeight(1)
	return in
}

// focusThreadInput gives the thread's input the keyboard. A card driven
// by another process withholds it entirely — the same guard inputBlock
// applies when rendering a read-only line for it (thread.go) — which is
// why opening a card calls this rather than focusing unconditionally.
func (m *Shell) focusThreadInput() {
	r, ok := m.selected()
	if !ok || r.DrivenAbroad {
		return
	}
	m.threadInput.Focus()
}

// blurThreadInput hands the keyboard back to the card's accelerators
// without discarding the draft — "leaving hides, never discards" applies
// within the page too, not just across a tab switch.
func (m *Shell) blurThreadInput() {
	m.threadInput.Blur()
	m.threadChip = nil
}

// handleThreadInputKey routes a key while the thread input has the
// keyboard (shell.go's handleKey, gated on m.cardOpen &&
// m.threadInput.Focused()).
func (m *Shell) handleThreadInputKey(msg tea.KeyPressMsg) tea.Cmd {
	r, ok := m.selected()
	if !ok || r.DrivenAbroad {
		// Not reachable in practice — focusThreadInput already refuses
		// both — but never leave a focused input feeding a card that
		// should be withholding it.
		m.blurThreadInput()
		return nil
	}
	if m.threadChip != nil {
		return m.handleChipKey(msg)
	}
	switch msg.String() {
	case "esc":
		m.threadInput.Blur()
		return nil
	case "enter":
		// Nothing typed means nothing to send, so enter keeps meaning what
		// it meant before the input took the keyboard: run whatever the
		// next card has highlighted. That is what lets the composer stay
		// focused by default without swallowing the page's primary key.
		if strings.TrimSpace(m.threadInput.Value()) == "" {
			if a, ok := m.cardActions().Selected(); ok {
				m.clearTransientNotice()
				return m.runCardAction(a)
			}
			return nil
		}
		return m.submitThreadInput(r.F)
	case "pgup", "pgdown":
		// scrolling the conversation is never text, so it works mid-draft
		m.scrollThread(msg.String() == "pgup")
		return nil
	case "up", "down":
		// an empty composer has no line to move within, so the arrows
		// still drive the action list underneath it
		if strings.TrimSpace(m.threadInput.Value()) == "" {
			m.moveAction(map[string]int{"up": -1, "down": 1}[msg.String()])
			return nil
		}
	}
	var cmd tea.Cmd
	m.threadInput, cmd = m.threadInput.Update(msg)
	return cmd
}

// handleThreadPaste routes a bracketed paste into the thread input
// (shell.go's handlePaste). A pending chip is cancelled first, the same
// as any other key reaching handleChipKey's default case.
func (m *Shell) handleThreadPaste(msg tea.PasteMsg) tea.Cmd {
	m.threadChip = nil
	var cmd tea.Cmd
	m.threadInput, cmd = m.threadInput.Update(msg)
	return cmd
}

// submitThreadInput parses the input's current line and routes it:
//
//   - verbNone -> sent as a message to the agent, exactly as the chat
//     pane's own send path does.
//   - verbMenu -> the same command menu overlay boardKey's space key
//     opens, pre-filtered by whatever followed the "/".
//   - verbCommand -> either fires immediately (a free/navigational verb
//     with no remainder) or raises the inline confirm chip.
//
// threadSkipParse (set by esc on a chip — handleChipKey) makes exactly
// this one call skip parseInput and send the line as a message
// unconditionally: that is the "esc no, send as a message" half of the
// chip contract, and the only way to satisfy it without an infinite
// loop — the line still starts with the same verb word, so parsing it
// again would just raise the same chip again.
func (m *Shell) submitThreadInput(f domain.Feature) tea.Cmd {
	text := strings.TrimSpace(m.threadInput.Value())
	if text == "" {
		return nil
	}
	if m.threadSkipParse {
		m.threadSkipParse = false
		m.threadInput.Reset()
		return m.sendThreadMessage(f.ID, text)
	}
	parsed := parseInput(text)
	switch parsed.Kind {
	case verbMenu:
		cm := newCommandMenu(m.globalCommands(), m.runCommand)
		if parsed.Remainder != "" {
			cm.filter.SetValue(parsed.Remainder)
			cm.setCursor(0)
		}
		m.Overlay.Push(cm)
		m.threadInput.Reset()
		return nil
	case verbCommand:
		// State-changing verbs always chip; a free/navigational one
		// (diff, spec, park) chips too the moment it carries a remainder
		// it has nowhere to spend — raising the chip rather than firing
		// and silently dropping words the user typed on purpose.
		if !chipVerbs[parsed.Verb] && parsed.Remainder == "" {
			m.threadInput.Reset()
			return m.fireVerb(parsed.Verb, parsed.Remainder)
		}
		m.threadChip = &pendingChip{feature: f.ID, verb: parsed.Verb, remainder: parsed.Remainder}
		return nil
	default: // verbNone
		m.threadInput.Reset()
		return m.sendThreadMessage(f.ID, text)
	}
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
		// just ceasing to render the chip over it. threadSkipParse is
		// what makes the NEXT submit actually send it rather than
		// re-parsing into the same chip.
		m.threadChip = nil
		m.threadSkipParse = true
		return nil
	}
	// Any other key backs out of the chip and resumes editing in place —
	// the original line is untouched, so this keystroke just continues
	// where the user left off (a correction, more text, a delete).
	m.threadChip = nil
	var cmd tea.Cmd
	m.threadInput, cmd = m.threadInput.Update(msg)
	return cmd
}

// verbKeys maps a parsed verb to the board accelerator that performs the
// same action (shell.go's boardVerb), for the verbs already wired to one.
//
// squash maps to "z", not the "S" its own key would suggest: "S" is
// already bound to the backlog's sort-order toggle (shell.go's boardVerb),
// and "z" is the board's actual squash-in-place accelerator. Mapping to
// "S" here would silently reorder the backlog instead of squashing.
var verbKeys = map[string]string{
	"approve": "g",
	"diff":    "d",
	"spec":    "s",
	"verify":  "v",
	"park":    "p",
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
}

// fireVerb performs a parsed verb's action: routes to the same key
// boardVerb already answers when one is mapped (against whichever card is
// currently selected — the same card the chip and the input belong to),
// otherwise (changes, bounce, autopilot) reports that it isn't wired to
// an action yet rather than inventing new engine behaviour for it.
func (m *Shell) fireVerb(verb, remainder string) tea.Cmd {
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

// sendThreadMessage delivers a line typed into the thread as a turn to
// the card's own live session — the thread's counterpart to
// shell.go's sendChat, keyed off the selected card rather than an open
// chat pane. Mirrors sendChat's capture-then-check-then-send shape so a
// swapped-out session can't receive a stray turn.
func (m *Shell) sendThreadMessage(id domain.FeatureID, text string) tea.Cmd {
	sess := m.sessionFor(id)
	if sess == nil {
		m.notice = noticeMsg{text: string(id) + ": no live session to message — attach first (enter)"}
		return nil
	}
	eng := m.engine
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
// A card owned by another process withholds it — featureRow.DrivenAbroad
// — rather than rendering a box that would fail at send time. Otherwise
// it renders the pending confirm chip in place of the box, or the
// persistent textarea itself.
func (m *Shell) inputBlock(s *theme.Styles, r featureRow, w int) string {
	if r.DrivenAbroad {
		return s.Faint.Render(ansi.Truncate("read-only — driven by "+foreignSummary(r.Foreign), w, "…"))
	}
	if m.threadChip != nil && m.threadChip.feature == r.F.ID {
		return ansi.Truncate(m.threadChip.view(s), w, "…")
	}
	m.threadInput.SetWidth(max(w-2, 10))
	// The chat pane gives the composer three rows because it is the whole
	// surface there. Here it sits under the thread, which owns the height,
	// so it takes one and grows only when someone is actually typing.
	m.threadInput.SetHeight(1)
	return m.threadInput.View()
}

// threadInputBindings is the card page's key table while the thread
// input has the keyboard — cardPageBindings' focused branch (backlog.go),
// the same filtering-split convention bugIngestView.bindings() uses.
func (m *Shell) threadInputBindings() []binding {
	if m.threadChip != nil {
		return []binding{
			{key: "enter", label: "confirm", help: "run " + m.threadChip.verb, bar: true},
			{key: "esc", label: "cancel", help: "back out — the line goes back in the input as a message", bar: true},
		}
	}
	return []binding{
		{key: "enter", label: "send", help: "send the line — a message, or route a verb command; runs the highlighted action when the line is empty", bar: true},
		{key: "esc", label: "keys", help: "hand the keyboard back to the card's single-letter accelerators (the draft is kept)", bar: true},
		{key: "pgup/pgdn", label: "scroll", help: "scroll the thread without leaving the line", bar: true},
		{key: "↑↓", label: "actions", help: "move the next card's cursor while the line is empty"},
	}
}
