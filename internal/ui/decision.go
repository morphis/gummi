package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/workflow"
)

type decisionKind string

const (
	decisionAsk    decisionKind = "ask"
	decisionGate   decisionKind = "gate"
	decisionVerify decisionKind = "verify"
	decisionBudget decisionKind = "budget"
	decisionIdle   decisionKind = "idle"
)

// threadDecision is a render-time projection, not a second stored model.
// Step 4 makes open decisions durable; until then asks come from the live
// session and workflow options are regenerated from nextActions.
type threadDecision struct {
	key      string
	kind     decisionKind
	question string
	actions  []nextAction
	ask      *engine.Ask
}

func (m *Shell) openDecision(r featureRow) *threadDecision {
	if r.DrivenAbroad {
		// a card another process drives withholds the composer entirely,
		// so nothing here could answer — and nextActions' suggestions for
		// it are about driving it locally, which would be a lie on screen.
		// Its read-only rendering is later work (PLAN R5).
		return nil
	}
	if sess := m.sessionFor(r.F.ID); sess != nil {
		if ask := sess.Snapshot().PendingAsk; ask != nil {
			key := "ask|" + string(r.F.ID) + "|" + ask.CallID + "|" + ask.Question
			return &threadDecision{key: key, kind: decisionAsk, question: ask.Question, ask: ask}
		}
		if sess.Interactive {
			// the conversation is live in this thread — the composer below
			// is its input, so there is nothing to decide: enter sends the
			// line, and ↑ opens the action inventory (DESIGN §10.19: a bare
			// composer means an agent is working)
			return nil
		}
	}

	in := m.nextInputFor(r)
	actions := nextActions(in)
	if len(actions) == 0 || in.landed || in.stage == domain.StageDone {
		return nil
	}
	kind := decisionIdle
	switch {
	case in.attn == attnBudget:
		kind = decisionBudget
	case in.stage == domain.StageVerify && (in.attn == attnGate || in.sess == engine.StateDone):
		kind = decisionVerify
	case in.attn == attnGate:
		kind = decisionGate
	}
	question := decisionQuestion(kind, r, in)
	ids := make([]string, 0, len(actions))
	for _, action := range actions {
		ids = append(ids, action.id)
	}
	key := strings.Join([]string{string(kind), string(r.F.ID), string(r.F.Stage), strings.Join(ids, ",")}, "|")
	return &threadDecision{key: key, kind: kind, question: question, actions: actions}
}

// wordConsumer is the index of the option that consumes the composer's
// line, or -1 when none does: the first action in the decision's order
// whose delivery takes prose — the run that opens (or re-runs) a stage,
// which rides the line as its kickoff note or first turn, and the bounce
// that sends findings back with it. Everything else — advance, read the
// spec, open the inbox — has nowhere to spend words, so a typed line sends
// as a turn instead (DESIGN §6.3: prose is always accepted and always
// safe; it becomes a turn, never an action nobody offered).
func (d *threadDecision) wordConsumer() int {
	if d == nil || d.ask != nil {
		return -1
	}
	for i, action := range d.actions {
		if action.id == "run" || action.id == "bounce" {
			return i
		}
	}
	return -1
}

// pickerOption is one row in the decision's picker: what the choice is,
// and the detail that says what it does.
type pickerOption struct {
	label  string
	detail string
	key    string
	danger bool
}

// askPickerOptions shapes a live ask_user question for the picker.
func askPickerOptions(ask *engine.Ask) []pickerOption {
	options := make([]pickerOption, 0, len(ask.Options))
	for _, option := range ask.Options {
		options = append(options, pickerOption{label: option.Label, detail: option.Detail})
	}
	return options
}

// pickerView is the shared inline decision picker. The card thread feeds
// it a live ask_user question; it also renders regenerated workflow
// actions. Selection state is explicit so neither caller has to fake the
// other's model merely to reuse its renderer.
func pickerView(s *theme.Styles, title, question string, options []pickerOption, selected int, picked map[int]bool, multi bool, w int) string {
	width := max(w-2, 10)
	var b strings.Builder
	b.WriteString(s.Muted.Render(sanitize(title)) + "  " +
		s.Base.Render(ansi.Truncate(sanitize(question), max(width-ansi.StringWidth(title)-2, 8), "…")) + "\n")
	for i, option := range options {
		marker := "  "
		label := s.Base
		if i == selected {
			marker = s.KeyHint.Render("▸ ")
			label = s.Title
		}
		tick := ""
		if multi {
			box := "○ "
			if picked[i] {
				box = "● "
			}
			tick = s.Faint.Render(box)
		}
		line := fmt.Sprintf("%s%s%d. %s", marker, tick, i+1, sanitize(option.label))
		if option.detail != "" {
			line += s.Faint.Render(" — " + sanitize(option.detail))
		}
		if option.key != "" {
			line += "  " + s.KeyHint.Render(option.key)
		}
		if option.danger && i != selected {
			label = s.Error
		}
		b.WriteString(ansi.Truncate(label.Render(line), width, "…") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func decisionQuestion(kind decisionKind, r featureRow, in nextInput) string {
	switch kind {
	case decisionBudget:
		return string(r.F.Stage) + " reached its envelope."
	case decisionVerify:
		if in.verdict == verdictPass {
			return "verification passed — decide whether this work is ready to land."
		}
		return "verification stopped here — choose what happens next."
	case decisionGate:
		return string(r.F.Stage) + " is ready for your decision."
	default:
		return "nothing is running — choose what happens next."
	}
}

// wordAim is the composer's emptiness driving the highlight (DESIGN
// §6.3): while a decision is open, a typed prose line aims the cursor at
// the option that consumes words, so the screen always states what enter
// is about to do with them before it does. A command never aims — the
// first word being a verb makes the line a command the parser owns (the
// chip is its confirmation), so verb-words leave the highlight where the
// user put it, and while a chip is pending the line belongs to the chip
// anyway.
func (m *Shell) wordAim(d *threadDecision) int {
	if d == nil || d.ask != nil || m.threadChip != nil {
		return -1
	}
	text := strings.TrimSpace(m.threadInput.Value())
	if text == "" || parseInput(text).Kind != verbNone {
		return -1
	}
	return d.wordConsumer()
}

func (m *Shell) syncDecision(d *threadDecision) {
	if d == nil {
		m.decisionKey, m.decisionCursor, m.decisionPicked = "", 0, nil
		m.threadFreeForm = false
		return
	}
	if d.key != m.decisionKey {
		m.decisionKey = d.key
		m.decisionCursor = 0
		m.decisionPicked = map[int]bool{}
		// a different question invalidates the armed free-form channel —
		// it belonged to the answer that is gone
		m.threadFreeForm = false
	}
	n := len(d.actions)
	if d.ask != nil {
		n = len(d.ask.Options)
	}
	if n == 0 {
		m.decisionCursor = 0
	} else {
		m.decisionCursor = clamp(m.decisionCursor, 0, n-1)
	}
	if i := m.wordAim(d); i >= 0 {
		m.decisionCursor = i
	}
}

func (m *Shell) openDecisionBlock(s *theme.Styles, r featureRow, w, maxRows int) []string {
	d := m.openDecision(r)
	m.syncDecision(d)
	if d == nil {
		return nil
	}
	title := "gummi"
	options := make([]pickerOption, 0, len(d.actions))
	multi := false
	// A decision autopilot has taken renders open, with its options,
	// saying whose it is — never a countdown. The answer runs in a
	// command, so there is a real interval where the question is on
	// screen and already spoken for, and the honest thing is to name the
	// answerer rather than let it read as waiting for you. It collapses
	// when the answer event lands, not on a timer: a timer would make the
	// same decision behave differently depending on whether a human
	// happened to be looking, and would diverge the TUI from the driver,
	// which answers immediately. esc keeps its two meanings (blur the
	// composer, close the card) — the take-it-back gesture belonged to
	// the countdown that was cut.
	//
	// The flag, not autopilotAnswers: the rule table says which decisions
	// this card's mode MAY take, and a card sitting idle on gates is one
	// nothing is going to move — marking it from the table would put a
	// standing claim on screen that no answer was ever coming to honour.
	autopilots := m.autopilotAnswering[r.F.ID]
	if d.ask != nil {
		title = string(r.F.ID) + " asks"
		if m.threadFreeForm {
			// armed: the composer below is the answer channel — say so on
			// the pinned control, the way the pane's free-form mode put
			// the textarea where the picker stood
			title += " · your line is the answer"
		}
		options = askPickerOptions(d.ask)
		multi = d.ask.MultiPick
	} else {
		aim := m.wordAim(d)
		for i, action := range d.actions {
			// the aimed row's label names what enter will do with the
			// words before it does it (DESIGN §6.3) — the only render that
			// follows the composer's text rather than the card's state
			label := action.label
			if i == aim {
				label += " with your words"
			}
			options = append(options, pickerOption{
				label: label, detail: action.detail, key: action.key, danger: action.danger,
			})
		}
	}
	if autopilots {
		// on the title, not a row of its own: the pinned region's height
		// is load-bearing at 36×9, where a spent row costs an option, and
		// the free-form arming above already set the precedent that who
		// owns the answer is said on the title.
		title += " · autopilot is taking this one"
	}
	lines := strings.Split(pickerView(s, title, d.question, options,
		m.decisionCursor, m.decisionPicked, multi, w), "\n")
	return windowDecisionBlock(s, lines, len(options), m.decisionCursor, maxRows)
}

// windowDecisionBlock keeps a decision taller than its row budget usable:
// the question keeps its row, the option rows window around the cursor so
// the highlighted answer is always on screen, and whatever is hidden is
// stated rather than silently dropped — the same contract the action
// list's fold honours (cardactions.go), and the shape the design's 36×9
// frame shows. composeThread would otherwise trim from the bottom, which
// can leave the cursor on an option that is no longer visible.
func windowDecisionBlock(s *theme.Styles, lines []string, nOptions, cursor, maxRows int) []string {
	if maxRows <= 0 || len(lines) <= maxRows {
		return lines
	}
	// lines[0] is the question; lines[1:] are one row per option.
	rows := maxRows - 1
	if rows <= 0 {
		return lines[:1]
	}
	marker := nOptions > rows
	if marker {
		rows-- // one row for the "…N more" count
		if rows <= 0 {
			// no room for both a count and an option: the highlighted
			// answer is worth more than knowing how many options exist
			rows, marker = 1, false
		}
	}
	out := make([]string, 0, rows+2)
	out = append(out, lines[0])
	out = append(out, windowLines(lines[1:], cursor, rows)...)
	if marker {
		out = append(out, s.Faint.Render(fmt.Sprintf("  …%d more — ↑↓ to reach them", nOptions-rows)))
	}
	return out
}

func (m *Shell) moveDecision(d *threadDecision, delta int) {
	n := len(d.actions)
	if d.ask != nil {
		n = len(d.ask.Options)
	}
	if n == 0 {
		return
	}
	m.decisionCursor = (m.decisionCursor + delta + n) % n
}

// answerAskWith delivers free-form prose as the answer to the open ask —
// the chat pane's 'o' channel, which the composer makes always-on: the
// question declared allow_free_form, so the line is the answer the ask
// invited (DESIGN §6.3; a structured ask keeps its terms and prose
// routes as a turn instead). Same live-session guard as the picker path.
func (m *Shell) answerAskWith(r featureRow, text string) tea.Cmd {
	sess := m.sessionFor(r.F.ID)
	eng := m.engine
	return func() tea.Msg {
		if sess == nil || eng.Get(r.F.ID) != sess {
			return noticeMsg{text: "session is no longer active", isErr: true}
		}
		if err := eng.Answer(context.Background(), r.F.ID, text); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return nil
	}
}

// deliverDecisionWords sends the composer's line through the highlighted
// word-eating option. run opens (or re-runs) the stage with the line as
// its kickoff note — an interactive stage attaches and the line is the
// conversation's first turn — and bounce rewinds the card with the line
// riding the reborn work stage's kickoff, the same delivery the headless
// --bounce note takes. Both are actions the screen is already offering,
// highlighted and named, which is what makes answering unambiguous
// (DESIGN §6.3) rather than a guess.
func (m *Shell) deliverDecisionWords(r featureRow, d *threadDecision, i int, text string) tea.Cmd {
	switch d.actions[i].id {
	case "run":
		if workflow.Interactive(r.F.Stage) {
			return m.attachChatWith(r.F, text)
		}
		return m.runStageWithNote(r.F, text)
	case "bounce":
		return m.bounceStage(r.F.ID, text)
	}
	return nil
}

func (m *Shell) answerDecision(r featureRow, d *threadDecision) tea.Cmd {
	if d.ask != nil {
		answer := decisionAnswerText(d.ask, m.decisionCursor, m.decisionPicked)
		if answer == "" {
			return nil
		}
		sess := m.sessionFor(r.F.ID)
		eng := m.engine
		return func() tea.Msg {
			if sess == nil || eng.Get(r.F.ID) != sess {
				return noticeMsg{text: "session is no longer active", isErr: true}
			}
			if err := eng.Answer(context.Background(), r.F.ID, answer); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
			return nil
		}
	}
	if m.decisionCursor < 0 || m.decisionCursor >= len(d.actions) {
		return nil
	}
	action := d.actions[m.decisionCursor]
	m.clearTransientNotice()
	return m.runCardAction(cardAction{
		id: action.id, key: action.key, label: action.label,
		why: action.detail, danger: action.danger,
	})
}

func decisionAnswerText(ask *engine.Ask, cursor int, picked map[int]bool) string {
	if ask.MultiPick {
		var labels []string
		for i, option := range ask.Options {
			if picked[i] {
				labels = append(labels, option.Label)
			}
		}
		if len(labels) > 0 {
			return strings.Join(labels, ", ")
		}
	}
	if cursor >= 0 && cursor < len(ask.Options) {
		return ask.Options[cursor].Label
	}
	return ""
}
