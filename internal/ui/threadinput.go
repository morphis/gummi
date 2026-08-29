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
// destroys, exactly like the chat pane's m.chat). It has two states,
// which is the SAME convention chat.go and bugIngestView already use for
// "typing vs. the surface's own keys" (chat.go: an always-focused
// textarea with a short reserved-key list intercepted first; bugIngestView:
// a filtering bool, toggled by "/", that the surface's own key handler
// checks before falling through to its input):
//
//   - unfocused (the default, and the state a freshly opened card page is
//     in): every one of the card page's existing single-letter
//     accelerators fires exactly as before — this file changes nothing
//     about that path, which is what "must keep working" means literally
//     here: zero new code runs on that route.
//   - focused: entered by pressing "/" (focusThreadInput, boardKey) —
//     the same trigger and the same "consumed, not inserted" behaviour
//     bugIngestView's own filter uses. Once focused, every key types into
//     the textarea except enter (submit) and esc (blur back to
//     accelerators, keeping the draft).
//
// "/" was chosen over any letter because the accelerator alphabet is
// almost fully claimed (see cardPageBindings/boardVerb) — no single
// letter was free to double as "start typing" without shadowing an
// existing key on its very first press. "/" is free, and it already
// carries the right connotation: parseInput's own bare-"/"/"/foo" rule
// means typing "/" once focused, then enter, reaches the exact same
// command menu this key would have opened directly.
type pendingChip struct {
	feature   domain.FeatureID
	verb      string
	remainder string
}

// newThreadInput builds the thread's persistent input. Mirrors chat.go's
// newChatInput for visual consistency, but starts blurred: unlike the
// chat pane (the only widget on its screen), the card page's input shares
// the keyboard with a full accelerator table, so it opts in rather than
// grabbing focus on arrival.
func newThreadInput() textarea.Model {
	in := textarea.New()
	in.Placeholder = "message the agent, or a verb (approve, verify, diff…) — / to compose"
	in.CharLimit = 4000
	in.ShowLineNumbers = false
	in.SetHeight(1)
	return in
}

// focusThreadInput switches the card page's keyboard from accelerators to
// the thread's input, per this file's doc comment. A card driven by
// another process withholds it entirely — the same guard inputBlock
// applies when rendering a read-only line for it (thread.go).
func (m *Shell) focusThreadInput() bool {
	r, ok := m.selected()
	if !ok || r.DrivenAbroad {
		return false
	}
	m.threadInput.Focus()
	return true
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
		return m.submitThreadInput(r.F)
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

// submitThreadInput parses the input's current line and routes it
// (PART 3 of the leading-verbs work):
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
		{key: "type", label: "type", help: "everything types into the line — a message, or a leading verb", bar: true},
		{key: "enter", label: "send", help: "send the line — a message, or route a verb command", bar: true},
		{key: "esc", label: "accelerators", help: "blur the input; the card's single-letter keys take over again", bar: true},
	}
}
