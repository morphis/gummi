package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/reentry"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/workflow"
)

// The chip is what a typed line becomes when the card has read it and
// the reading is an act rather than an answer: it takes the picker's
// place, says what it read, says what it is about to do, and waits.
//
// It is inline, not a dialog, for one reason — the line is still in the
// composer. The reader can look at what they wrote, look at what the
// card made of it, and choose: go, or take the line back as a plain
// message (esc), or change the line, which withdraws the reading
// because it was a reading of something that no longer exists. A dialog
// over the thread would hide exactly the two things the decision is
// between.
//
// The one rule that decides its keys (PROPOSAL-composer-router §9.2): a
// move without a spend — a rewind, a new card — goes on enter; anything
// that spends credits NOW — a run, the landing — goes only on y, and
// enter does nothing. Every other confirm in the package puts the safe
// side under enter for the same reason, and a reader who typed "ok" and
// pressed enter twice out of habit must not have started a 40-credit
// run on the second press.

// reentryReading is the chip's state: the line it was read from, what
// the card would do, and whether enter does it.
type reentryReading struct {
	line      string
	out       reentry.Outcome
	forward   string // the forward row's own label, for an Advance
	goOnEnter bool
}

// goOnEnter is the §9.2 rule as a function of the outcome alone.
func goOnEnter(out reentry.Outcome) bool {
	switch out.Action {
	case reentry.Rewind, reentry.NewCard:
		return true
	}
	return false
}

// stopForward reads off the answer set what "go on" would mean at this
// stop, so the router can only ever produce the act the set offers:
// the advance row's target stage and label, or that the forward act is
// a run of the current stage, and why the gate is blocked when it is.
// It is asked of stageActions rather than re-derived from the stage, so
// the chip and the rows cannot disagree about what going on does.
func stopForward(in nextInput) (fwd domain.Stage, rerun bool, blocked, label string) {
	switch {
	case in.openSpecQs > 0:
		blocked = "open comments in the " + artifactNoun(in.kind)
	case in.openDiffComments > 0:
		blocked = "open diff comments"
	case len(in.undrafted) > 0:
		blocked = "undrafted sections"
	}
	acts := stageActions(in)
	for _, a := range acts {
		if a.id == "advance" {
			if next := workflow.Next(in.stage); len(next) > 0 {
				fwd = next[0]
			}
			return fwd, false, blocked, a.label
		}
	}
	for _, a := range acts {
		if a.id == "run" && !a.sendBack {
			return "", true, blocked, a.label
		}
	}
	return "", false, blocked, ""
}

// chipLines renders the chip. Four parts, in yield order from most to
// least expendable: the "I read that as" line, the detail lines, then —
// never yielded — the arrow line naming the act and the key line saying
// how to take it. The picker's own rule (DESIGN §6.3): an option you can
// see is worth more than air, and the highlighted answer never goes.
func (m *Shell) chipLines(s *theme.Styles, r featureRow, p *reentryReading, width, maxRows int) []string {
	f := r.F
	out := p.out
	readAs := " " + s.Base.Bold(true).Render("gummi") + "  " + s.Base.Render("I read that as "+readingNoun(out)+".")
	// the arrow line wraps rather than truncates: it names the act, and
	// the tail of it — the stage the card ends up at — is the half a
	// narrow terminal would otherwise cut off
	var arrow []string
	for i, l := range strings.Split(wrapText(chipAct(f, p), max(width-4, 8)), "\n") {
		if i == 0 {
			arrow = append(arrow, " "+s.PaneTitleActive.Render("→")+" "+s.Base.Render(l))
		} else {
			arrow = append(arrow, "   "+s.Base.Render(l))
		}
	}
	keys := "   " + chipKeys(s, p.goOnEnter)

	var details []string
	for _, d := range chipDetails(r, p) {
		for _, l := range strings.Split(wrapText(d, max(width-4, 8)), "\n") {
			details = append(details, "   "+s.Subtle.Render(l))
		}
	}
	// the rows that never yield — the act and its keys — then what fits
	// above them: the reading first, the details only with room to spare
	core := append(append([]string{}, arrow...), keys)
	if maxRows <= 0 || maxRows >= len(core)+len(details)+1 {
		return append(append(append([]string{readAs}, arrow...), details...), keys)
	}
	if maxRows >= len(core)+1 {
		return append(append([]string{readAs}, arrow...), keys)
	}
	return core
}

// readingKey answers a key while a line is out being read.
//
// The picker is not on screen while a read is out — the reader answered
// it, and the answer is what is running (decision.go) — so this claims
// its keys the way the chip does, and answers the two that still mean
// something: enter, which has nothing left to commit while the line it
// would send is already out, and esc, which stops the read.
//
// esc here is NOT the chip's esc. The chip has proposed an act, so
// declining it still owes the line a destination and sends it as a
// message. Nothing has been proposed yet, so stopping the read is just
// stopping it: the line stays in the composer, the stop's own answers
// come back, and nothing was sent anywhere.
//
// Anything else falls through — and anything that types withdraws the
// read on the way, for the reason the chip is withdrawn by an edit: it
// is a reading OF the line in the composer, and an edited line is not
// the line that was read. No key here answers with a command, so this
// reports only whether it took the key.
func (m *Shell) readingKey(r featureRow, msg tea.KeyPressMsg) bool {
	if m.reentryRead == nil || m.reentryRead.id != r.F.ID {
		return false
	}
	switch msg.String() {
	case "esc":
		m.withdrawRead()
		m.notice = noticeMsg{text: string(r.F.ID) + ": stopped reading — your line is still here"}
		return true
	case "enter":
		// the line this would send is the line already out being read, so
		// a second enter can only buy the same answer twice. A reader
		// pressing it is asking why nothing has happened; say that.
		m.notice = noticeMsg{text: string(r.F.ID) + ": still reading your line — esc stops it"}
		return true
	case "up", "down", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// the picker's own keys, with no picker on screen: they do nothing
		// rather than moving a highlight nobody can see (chipKey's rule,
		// and the scroll and card-step keys are hoisted above both of us
		// so they keep working regardless)
		return true
	}
	m.withdrawRead()
	return false
}

// readingBindings is the status bar while a read is out: the picker's
// rows are gone with it, so the two keys that still do something are the
// whole table.
func (m *Shell) readingBindings() []binding {
	return m.withCardTabs([]binding{
		{key: "enter", label: "reading…", help: "your line is out being read — what comes back is a proposal you confirm", bar: true, sticky: true},
		{key: "pgup/pgdn", label: "scroll", help: "scroll the thread while the read runs", bar: true},
		{key: "esc", label: "stop reading", help: "drop the read — your line stays in the composer and nothing is sent", bar: true},
	})
}

// readingNoun is the reading in the card's own words — reentry.Describe
// says what the intent means; this says what the card will do about it,
// which is the half a reader is confirming.
func readingNoun(out reentry.Outcome) string {
	switch out.Reason {
	case "requirement_missing":
		return "a missing requirement, not a bad build"
	case "plan_wrong":
		return "a wrong plan, not a bad build"
	case "implementation_wrong", "implementation_wrong-in-place":
		return "an implementation that does not match the plan"
	case "check-missing":
		return "a missing check"
	case "separate-card":
		return "not this card's work"
	case "proceed", "proceed-rerun":
		return "go on"
	case "design-stage-rerun":
		return "something for the architect"
	}
	return strings.ReplaceAll(out.Reason, "-", " ")
}

// chipAct is the arrow line: the act, named by where the card ends up.
func chipAct(f domain.Feature, p *reentryReading) string {
	out := p.out
	art := artifactNoun(f.Kind)
	switch out.Action {
	case reentry.Rewind:
		if !out.Edit.Empty() {
			return "add your line to the " + art + " under " + out.Edit.Section + " as an open comment, then restart from " + string(out.Target) + "."
		}
		return "send it back to " + string(out.Target) + " with your line."
	case reentry.RerunInPlace:
		if !out.Edit.Empty() {
			return "add your line to the " + art + " under " + out.Edit.Section + ", then run " + string(out.Target) + " again."
		}
		if out.Target == domain.StagePlan {
			return "start the design stage with your line as its brief — it writes the " + art + " from there."
		}
		return "run " + string(out.Target) + " again with your line."
	case reentry.Advance:
		if out.Target == domain.StageDone {
			return "land on main — squash-merge the branch and mark " + string(f.ID) + " done."
		}
		label := p.forward
		if label == "" {
			label = "go on"
		}
		return label + " — " + string(f.ID) + " moves to " + string(out.Target) + "."
	case reentry.NewCard:
		return "open a new card seeded with your line — " + string(f.ID) + " stays where it is."
	}
	return ""
}

// chipDetails are the facts a reader cannot see from the arrow line: the
// stages a rewind re-runs on the way, what autopilot will do with the
// gate it lands on, and what the run about to start will cost.
func chipDetails(r featureRow, p *reentryReading) []string {
	f := r.F
	out := p.out
	var d []string
	if out.Action == reentry.Rewind {
		if len(out.Path) > 1 {
			names := make([]string, 0, len(out.Path))
			for _, st := range out.Path {
				names = append(names, string(st))
			}
			d = append(d, "Your code stays on the branch. The card walks back through "+strings.Join(names, " then ")+", and each stage runs again.")
		} else {
			d = append(d, "Your code stays on the branch; "+string(out.Target)+" runs again from there.")
		}
		// THE AUTOPILOT LINE. Without it the chip lies by omission on
		// exactly the cards a reader trusts least: on autopilot the
		// design gate crosses itself, so the "stops for you" a reader
		// expects from a rewind never happens (§9.4).
		//
		// GateMode is the one idiom the package reads the mode through
		// (domain.Feature.GateMode); the explicit `!= "" &&` guard these two
		// arms used to carry said the same thing a longer way, and being the
		// only site spelling it out is how the shorter, wrong spelling
		// survived everywhere else.
		switch {
		case f.GateMode() != domain.GateAttended && !out.Edit.Empty():
			// the open comment the rewind writes holds the gate until the
			// architect resolves it; only then does autopilot cross
			d = append(d, "Autopilot is on: once your comment is resolved the design gate crosses itself and implement runs straight away — /autopilot off first if you want to read the plan.")
		case f.GateMode() != domain.GateAttended:
			d = append(d, "Autopilot is on: the design gate crosses itself and implement runs straight away — /autopilot off first if you want to read the plan.")
		default:
			d = append(d, "The design gate stops for you before implement runs again.")
		}
	}
	if spends := spendStage(out); spends != "" {
		if line := lastRunCost(r, spends); line != "" {
			d = append(d, line)
		}
	}
	return d
}

// spendStage names the stage a chip's act will run now, "" when it runs
// nothing now (a rewind runs later; a new card runs nothing).
func spendStage(out reentry.Outcome) domain.Stage {
	switch out.Action {
	case reentry.RerunInPlace:
		return out.Target
	case reentry.Advance:
		if out.Target != domain.StageDone {
			return out.Target
		}
	}
	return ""
}

// lastRunCost says what the stage about to run cost the last time it ran
// on this card, and what is left — the two numbers a reader weighs
// before pressing y.
func lastRunCost(r featureRow, stage domain.Stage) string {
	spend := stageSpendByStage(r.StageSpend)
	cost := spend[stage]
	var b strings.Builder
	if cost > 0 {
		b.WriteString("The last " + string(stage) + " run on this card cost " + itoa(int(cost+0.5)) + " credits")
	} else {
		b.WriteString(string(stage) + " has not run on this card yet")
	}
	if r.F.Budget.Envelope > 0 {
		left := r.F.Budget.Remaining(r.F.Spend.CreditEquivalent())
		b.WriteString("; " + itoa(int(left+0.5)) + " are left in the envelope")
	}
	b.WriteString(".")
	return b.String()
}

// chipKeys is the key line, which is the §9.2 rule made visible.
func chipKeys(s *theme.Styles, goOnEnter bool) string {
	if goOnEnter {
		return s.KeyHint.Render("enter") + " " + s.Subtle.Render("go") + "  ·  " +
			s.KeyHint.Render("esc") + " " + s.Subtle.Render("keep it here — send the line as a message instead")
	}
	return s.KeyHint.Render("y") + " " + s.Subtle.Render("go") + "  ·  " +
		s.KeyHint.Render("enter") + " " + s.Subtle.Render("nothing") + "  ·  " +
		s.KeyHint.Render("esc") + " " + s.Subtle.Render("keep it here — send the line as a message instead")
}

// chipKey answers a key while a chip is up. handled=false hands the key
// on to the composer — after withdrawing the chip, because a key that
// types is an edit to the line the chip was read from.
func (m *Shell) chipKey(r featureRow, msg tea.KeyPressMsg) (tea.Cmd, bool) {
	p := m.reentryPending
	if p == nil {
		return nil, false
	}
	switch key := msg.String(); key {
	case "enter":
		if p.goOnEnter {
			return m.takeReading(r), true
		}
		m.notice = noticeMsg{text: string(r.F.ID) + ": that spends credits — press y to go, or esc to keep the line here"}
		return nil, true
	case "y":
		return m.takeReading(r), true
	case "esc":
		// the take-it-back gesture, and it never leaves the page: the
		// line goes where a bare composer would have sent it — and that
		// starts a conversation the next line continues (chat.go)
		m.reentryPending = nil
		if s := m.sessionFor(r.F.ID); s == nil || !s.Live() {
			m.startChat(r.F.ID)
		}
		return m.sendThreadMessage(r.F, p.line), true
	case "up", "down", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		// the picker is not on screen, so its keys do nothing rather than
		// selecting a row the reader cannot see
		return nil, true
	}
	m.reentryPending = nil
	return nil, false
}

// takeReading performs the chip's act.
func (m *Shell) takeReading(r featureRow) tea.Cmd {
	p := m.reentryPending
	m.reentryPending = nil
	if p == nil {
		return nil
	}
	m.threadInput.Reset()
	m.clearTransientNotice()
	return m.performReentry(r, p.out)
}

// chipBindings is the status bar while a chip is up: what enter and y
// do, said the same way the chip's own key line says it.
func (m *Shell) chipBindings(p *reentryReading) []binding {
	var bs []binding
	if p.goOnEnter {
		bs = append(bs, binding{key: "enter", label: "go", help: "do what the chip says — the card moves", bar: true, sticky: true})
	} else {
		bs = append(bs,
			binding{key: "y", label: "go", help: "do what the chip says — it spends credits now", bar: true, sticky: true},
			binding{key: "enter", label: "nothing", help: "a spend never goes on enter; y is the key", bar: true})
	}
	return m.withCardTabs(append(bs,
		binding{key: "esc", label: "keep it here", help: "withdraw the reading and send your line as a plain message", bar: true},
	))
}
