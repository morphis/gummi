package threadfold

// The thread's own sentences: what a logged event says, in the words
// every face shows it in. Each helper returns plain text — the TUI adds
// its marks and styles around it, the web face its markup — so the two
// cannot drift apart on what a gate crossing or a park reads as.

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

// Sanitize strips terminal escape sequences and control characters from
// untrusted text (model output, provider error strings) before it is
// shown. Neither lipgloss nor ultraviolet neutralizes embedded escapes,
// so raw model bytes could otherwise smuggle OSC 52 (clipboard write),
// title-spoofing, or cursor/screen sequences to the terminal — and a
// browser has no use for them either. Only newline and tab survive;
// gummi's own styling is applied after this, so nothing legitimate is
// lost.
// StatusWatching marks a tool call that is a backend's own persistent
// background watch (WatchTool) while it is still outstanding: neither
// TUI nor web ever sees an EventToolResult for one (no backend reports
// it), so without this it would read exactly like any other call nobody
// ever heard back from — the neutral "outcome unknown" dot — instead of
// what it is, still active and the reason a session that looks idle may
// still interject on its own.
const StatusWatching = "watching"

// WatchTool is agent.WatchTool, re-exported for the faces that read it
// from the fold.
func WatchTool(tool string) bool { return agent.WatchTool(tool) }

func Sanitize(s string) string {
	s = ansi.Strip(s) // remove recognized escape sequences (CSI/OSC/…)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return -1 // drop remaining C0/C1 control runes
		}
		return r
	}, s)
}

// AuthorLabel names a logged turn's author the way the live pane labels
// the same turn: the user by name, gummi's own kickoffs as gummi, and the
// agent by whichever role was speaking.
func AuthorLabel(author, role string) string {
	switch author {
	case string(engine.AuthorUser):
		return "you"
	case string(engine.AuthorSystem):
		return "gummi"
	case string(engine.AuthorThinking):
		return "thinking"
	default:
		if role != "" {
			return role
		}
		if author == "" {
			return "agent"
		}
		return author
	}
}

// AskAnswerer is who an answered ask names as its answerer on the
// thread: "autopilot" for an answer the card took itself, "you"
// otherwise. It reads the older Actor field, as the thread always has.
func AskAnswerer(p state.AskPayload) string {
	if p.Actor == state.ActorAutopilot {
		return "autopilot"
	}
	if name := state.PersonName(AskedBy(p)); name != "" {
		return name
	}
	return "you"
}

// AskLine is an answered ask's receipt: who answered, the question, and
// the answer — "you answered “Persist where?” — per-device".
func AskLine(p state.AskPayload) string {
	line := AskAnswerer(p) + " answered"
	if p.Question != "" {
		line += " “" + Sanitize(p.Question) + "”"
	}
	if p.Answer != "" {
		line += " — " + Sanitize(p.Answer)
	}
	return line
}

// GateCrosser is who a gate crossing names as its crosser on the thread:
// "you" for a crossing by hand, otherwise the actor's own name.
func GateCrosser(p state.GatePayload) string {
	if name := state.PersonName(p.Actor); name != "" {
		return name
	}
	if p.Actor != "" && p.Actor != state.ActorUser {
		return p.Actor
	}
	return "you"
}

// GateLine is a gate crossing's receipt: "you advanced plan → implement",
// and "you sent it back verify → implement" for a move down the graph —
// a send-back is not an advance, and a receipt saying it was reads as the
// opposite of what the person chose.
func GateLine(p state.GatePayload) string {
	verb := " advanced"
	if movesBack(domain.Stage(p.From), domain.Stage(p.To)) {
		verb = " sent it back"
	}
	line := GateCrosser(p) + verb
	if p.From != "" || p.To != "" {
		line += " " + p.From + " → " + p.To
	}
	return line
}

// movesBack reports whether a crossing from → to walks the card down its
// own stage sequence: a rerun edge taken, not a gate crossed forward.
func movesBack(from, to domain.Stage) bool {
	fi, ti := -1, -1
	for i, st := range StageSequence() {
		switch st {
		case from:
			fi = i
		case to:
			ti = i
		}
	}
	return fi >= 0 && ti >= 0 && ti < fi
}

// ParkLine is a park's receipt: "parked — " and the sentence the person
// was shown when the card stopped.
//
// Detail is kept verbatim (ParkPayload's own doc comment,
// state/cardevents.go) precisely so history explains itself without the
// reader reconstructing it from the reason code, so it wins whenever a
// row has one. Only an old row, written before ParkPayload carried
// Detail at all, falls back to a sentence derived from Reason — the same
// three-way split QuitStopped already treats ParkReasonQuit as
// load-bearing and everything else as "a human should look at this,"
// here spelled out as prose instead of a boolean.
func ParkLine(p state.ParkPayload) string {
	sentence := p.Detail
	if sentence == "" {
		switch p.Reason {
		case state.ParkReasonQuit:
			sentence = "the board quit"
		case state.ParkReasonGaveUp:
			sentence = "it gave up"
		default: // ParkReasonNeedsYou, and any reason not yet named
			sentence = "it needs you"
		}
	}
	return "parked — " + Sanitize(sentence)
}

// PauseLine is the receipt for a run a person stopped by hand: "you
// parked it", "Simon parked it" — who pressed "stop here" or park, from
// whichever face or device.
func PauseLine(p state.PausePayload) string {
	return Sanitize(PersonWord(p.By)) + " parked it"
}

// RebaseLine is a rebase's receipt: "you rebased it onto main", or "the
// agent rebased it onto main" for a conflicted rebase handed to the agent
// and judged clean afterwards.
func RebaseLine(p state.RebasePayload) string {
	who := PersonWord(p.By)
	if p.Agent {
		who = "the agent"
	}
	line := who + " rebased it"
	if p.Onto != "" {
		line += " onto " + p.Onto
	}
	return Sanitize(line)
}

// PersonWord is ActorWord for a record a person made: a missing actor is
// the terminal's own "you", as on every row written before names were.
func PersonWord(actor string) string {
	if w := ActorWord(actor); w != "" {
		return w
	}
	return "you"
}

// SupersededLine is the trace a decision leaves when it was opened and
// then superseded before anyone answered it (DESIGN §10.18: nothing may
// block a card without leaving a row).
func SupersededLine(p state.DecisionPayload) string {
	return Sanitize(p.Question) + " — unanswered, superseded"
}

// AutopilotLabel names the card's gate-approval mode for a field already
// labelled "autopilot:", so it answers that field's own question rather
// than repeating the mode's name back ("autopilot: autopilot"). Two modes
// means the answer is a state: on for domain.GateAutopilot, off for
// attended and for the empty value that reads as it.
func AutopilotLabel(mode string) string {
	if mode == domain.GateAutopilot {
		return "on"
	}
	return "off"
}

// ModeLine is the receipt for a stored gate-approval mode change (an
// AutopilotPayload with no Event): "autopilot set to on".
//
// Normalized, then labelled. The event log is history: a row written
// before the three modes collapsed carries a retired spelling ("full",
// "gates", "caller"), and labelling that directly would render an old
// handover as "off" — the exact opposite of what happened.
// NormalizeGateApproval is the one place those spellings resolve, so the
// past reads correctly for the same reason a stored row does.
//
// Labelling at all, rather than printing p.Mode raw: the empty string is
// a legal stored mode (domain.ValidGateApproval accepts it), and printing
// it would leave the row trailing off after "set to" as though the value
// had gone missing.
func ModeLine(p state.AutopilotPayload) string {
	mode := p.Mode
	if canonical, ok := domain.NormalizeGateApproval(mode); ok {
		mode = canonical
	}
	return "autopilot set to " + Sanitize(AutopilotLabel(mode))
}

// DecisionLine is one crossing or answer autopilot made, pulled out of a
// folded stage so it keeps the position it happened at: "autopilot
// crossed plan → implement", "autopilot answered “q” — a". Empty for any
// event that is not such a decision — a person's crossing or answer, or
// any other kind.
//
// who comes from whether the event fell inside a period, never from the
// actor alone. Inside one it is autopilot's, whatever name the crossing
// was filed under — the review loop's own actor reaches this path for
// cards started by the switch before that was corrected. Outside one it
// keeps the actor's own name, because an unattended crossing on a card
// nobody handed over is the review loop's work and saying otherwise
// would claim a handover that never happened.
func DecisionLine(ev state.CardEvent, inStretch bool) string {
	switch ev.Kind {
	case state.EventGate:
		var p state.GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil || HumanGateActor(p.Actor) {
			return ""
		}
		who := p.Actor
		if inStretch {
			who = "autopilot"
		}
		return who + " crossed " + p.From + " → " + p.To
	case state.EventAsk:
		var p state.AskPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil || AskedBy(p) != state.ActorAutopilot {
			return ""
		}
		line := "autopilot answered “" + Sanitize(p.Question) + "”"
		if p.Answer != "" {
			line += " — " + Sanitize(p.Answer)
		}
		return line
	}
	return ""
}

// GoalSentence says one goal log entry in a line, in the vocabulary the
// goal page's own log already prints: the action, the card it is about,
// the decision number when it has one, and the detail that explains it.
// Two surfaces naming the same entry two ways would be two things to
// learn, so the wording is shared rather than re-invented for the thread.
//
// The checks entry is the one exception it makes: its Detail is the
// verify stage's check results as JSON, which is for the goal page to
// unpack, not for a line of prose.
func GoalSentence(p state.GoalPayload) string {
	line := p.Action
	if p.Card != "" {
		line += " " + string(p.Card)
	}
	if p.Item != "" {
		line += " " + p.Item
	}
	if p.N > 0 {
		line += " " + (state.GoalEntry{GoalPayload: p}).DecisionRef()
	}
	// An amount pair is the whole fact of a raise or a re-estimated
	// reserve, and the Detail beside it carries the reason rather than
	// the numbers. Both ends have to be there to say "from x to y": an
	// entry with only To (a goal stopped on a card it cannot fund) is
	// naming what is needed, not a move, and "0 → 1500" would read as a
	// card that had been given nothing.
	if p.From > 0 && p.To > 0 {
		line += " " + strconv.Itoa(p.From) + " → " + strconv.Itoa(p.To)
	}
	if p.Detail != "" && p.Action != state.GoalChecks {
		first, _, _ := strings.Cut(p.Detail, "\n")
		line += " — " + first
	}
	return Sanitize(line)
}

// ActorWord is how any recorded actor is named to a reader: a person by
// their name, the terminal's bare "user" as "you", and a loop ("goal",
// "autopilot", "lead") by its own word — the rule GateCrosser applies,
// for records that are not a crossing (a goal's log). No actor is no word.
func ActorWord(actor string) string {
	if name := state.PersonName(actor); name != "" {
		return name
	}
	if actor == state.ActorUser {
		return "you"
	}
	return actor
}

// TurnBy names who sent a user turn in a thread: a person by name, "" for
// the terminal's own (drawn as "you"), and "gummi" for a turn gummi sent
// for a session's objective (DESIGN §19.11) — never drawn as the person's.
func TurnBy(by string) string {
	if by == state.ActorObjective {
		return "gummi"
	}
	return state.PersonName(by)
}
