package ui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/reentry"
)

// This file is the card page's half of the re-entry: what happens when a
// reader types what is wrong and aims it at "send it back".
//
// The row itself does not change and neither does anything around it —
// the label, the word-aim, the status bar and wordConsumer are all
// exactly what Phase 0 left. What changes is where the line GOES. Until
// now the answer took a fixed rule: at verify it bounced to implement,
// at implement it re-ran in place, at the design stage it was a turn.
// That rule cannot reach the case it most needs to. "This was never in
// the spec" is not a bad build, and bouncing it to implement asks the
// same stage to guess at a requirement its contract still does not
// carry — while the note itself dies with the kickoff that read it.
//
// So the line is classified once (engine.ClassifyReentry) and routed by
// a compiled-in table (reentry.Decide). The table can only produce acts
// the workflow already declares, an unreadable sentence becomes a turn,
// and a rewind never moves without first writing what was missed into
// the artifact. DESIGN §6.3's safety property is unchanged and is what
// all of that is protecting: prose is always accepted and always safe,
// becoming a turn or a routed re-entry, never an action nobody offered.

// reentryClassifiedMsg carries the classification turn's answer back to
// the Update loop. The feature is carried whole rather than by id
// because the routing reads its stage and kind, and the card may have
// been scrolled past by the time the turn returns.
type reentryClassifiedMsg struct {
	f    domain.Feature
	note string
	// fallback is the action id of the row the line was aimed at — the
	// route that answer declares for itself, taken when no classifier
	// could run. It is captured when the line is sent rather than
	// re-derived when the answer comes back: the classification is a
	// model call, and the answer set can have changed under it (a run
	// finished, a gate opened), so re-reading it would fall back to a
	// route the reader was never offered.
	fallback string
	intent   reentry.Intent
	err      error
	// read is the in-flight pass this answer belongs to, nil for an
	// answer no pass produced (chatExit's leading word is itself the
	// reading, so nothing was sent anywhere). Update drops an answer
	// whose pass the shell is no longer waiting on — esc took it back,
	// or the line it was a reading of was edited — so a model call that
	// lands late cannot raise a chip over a line that has moved on.
	read *reentryRead
}

// reentryRead is a classification in flight: whose line went out to be
// read, the line itself, and the cancel that takes it back.
//
// It exists because the read is a model call and costs seconds, not
// milliseconds. Without it the screen between enter and the chip is
// exactly the screen before enter — the same picker, the same line, no
// motion anywhere — and a reader who has just been told nothing reads
// that as a UI that has stopped. So the pass is state: it stands in the
// chip's own slot (chip.go's readingLines), counts as a scribe pass
// against the card (Shell.scribing) so every busy marker in the package
// animates for it, and owns esc, which is the same escape hatch the chip
// offers — the line comes back as a plain message and nothing is spent
// waiting for a reading nobody wants any more.
type reentryRead struct {
	id     domain.FeatureID
	line   string
	cancel context.CancelFunc
}

// routeReentry is the single entry point for a "send it back" carrying a
// line — from the picker's word-eating row (deliverDecisionWords) and
// from "/bounce <reason>" and "/changes <reason>" (fireVerb) alike, so
// the two cannot diverge on what the same sentence does.
//
// The design stage short-circuits before any model call. There the
// architect is live in this very thread, the artifact is what it is
// writing right now, and there is no earlier stage to rewind to — every
// sentence is already delivered correctly by being a turn, so spending
// a classification on it would buy nothing. reentry.Decide states the
// same rule for every other caller; this checks it here as well so the
// TUI never pays for a turn whose answer is already known.
func (m *Shell) routeReentry(r featureRow, fallback, note string) tea.Cmd {
	note = strings.TrimSpace(note)
	if note == "" {
		// Nothing to classify and nothing to carry: the bare answer, the
		// same act pressing the row with an empty composer performs.
		return m.fixedSendBack(r, fallback, "")
	}
	// A live session is a conversation, and a line typed into one is
	// the next thing said in it — never something to read first. This
	// covers the design chat (an attached architect sitting idle at its
	// own gate) as well as a running stage: both already have someone
	// on the other end of the composer.
	if sess := m.sessionFor(r.F.ID); sess != nil && sess.Live() {
		return m.sendThreadMessage(r.F, note)
	}
	eng := m.engine
	if eng == nil {
		return m.fixedSendBack(r, fallback, note)
	}
	if m.reentryRead != nil && m.reentryRead.id == r.F.ID {
		// A second line sent while the first is still out buys nothing and
		// spends another scribe turn. The slot says the read is running;
		// this is the guard for the paths that reach here without it —
		// "/bounce <reason>" typed at a card already reading, say — and it
		// says so, because a verb that silently does nothing is the same
		// complaint one stop further along.
		m.notice = noticeMsg{text: string(r.F.ID) + ": still reading your line — esc sends it as a plain message instead"}
		return nil
	}
	// The composer keeps the line. The chip that may follow is a reading
	// OF that line, shown beside it; resetting here would show a reading
	// of nothing.
	m.notice = noticeMsg{text: string(r.F.ID) + ": reading the card to place your line…"}
	f := r.F
	_, _, _, forward := stopForward(m.nextInputFor(r))
	// The pass is visible for as long as it runs: in the chip's slot on
	// this page, in the card's busy marker everywhere else. Both hang off
	// the same two writes, so neither can show a read the other has
	// already settled.
	ctx, cancel := context.WithCancel(context.Background())
	read := &reentryRead{id: f.ID, line: note, cancel: cancel}
	m.reentryRead = read
	m.scribing[f.ID]++
	return func() tea.Msg {
		intent, err := eng.ClassifyReentry(ctx, f, note, forward)
		return reentryClassifiedMsg{f: f, note: note, fallback: fallback, intent: intent, err: err, read: read}
	}
}

// withdrawRead takes back an in-flight read: the model call is cancelled,
// the card's scribe count settles, and the answer that may already be on
// its way is dropped by applyReentry's own identity check. It returns the
// withdrawn pass — nil when there was none — so the caller can do
// something with the line it was holding.
func (m *Shell) withdrawRead() *reentryRead {
	p := m.reentryRead
	if p == nil {
		return nil
	}
	m.reentryRead = nil
	p.cancel()
	m.scribeSettled(p.id)
	return p
}

// applyReentry routes a classified sentence. Update calls it on
// reentryClassifiedMsg.
//
// A classifier that could not run is NOT a statement about the sentence,
// and the two must not collapse into one path: an unreadable sentence
// becomes a turn (the safety property), but an unreachable model falls
// back to the fixed route the row itself declares, because the screen
// already offered "send it back" and quietly turning that into a chat
// message would drop an answer the reader had been given.
func (m *Shell) applyReentry(msg reentryClassifiedMsg) tea.Cmd {
	if msg.read != nil {
		if m.reentryRead != msg.read {
			// The pass was withdrawn while it was out — esc, or an edit to
			// the line it was a reading of. Whoever withdrew it settled it;
			// this answer is a reading of something that is no longer on
			// screen, and raising a chip from it would confirm an act
			// against a line the reader has already taken back.
			return nil
		}
		// Settled before anything below can return early: a pass that
		// leaks its count leaves the card busy forever.
		m.reentryRead = nil
		m.scribeSettled(msg.f.ID)
	}
	r, ok := m.rowByID(msg.f.ID)
	if !ok {
		return nil
	}
	if msg.err != nil {
		why := "could not read the card"
		if errors.Is(msg.err, engine.ErrNoScribe) {
			why = "no agent to read the card with"
		}
		m.notice = noticeMsg{text: string(msg.f.ID) + ": " + why + " — sending it back the usual way"}
		return m.fixedSendBack(r, msg.fallback, msg.note)
	}
	in := m.nextInputFor(r)
	fwd, rerun, blocked, label := stopForward(in)
	out := reentry.Decide(reentry.Input{
		Stage: r.F.Stage, Kind: r.F.Kind, Intent: msg.intent, Note: msg.note,
		Forward: fwd, Rerun: rerun, Blocked: blocked,
	})
	if out.Reason == "proceed-blocked" {
		// "go on" at a gate that is held shut: the answer is the blocker,
		// said at the moment it was asked for, and nothing is sent.
		why := blocked
		if b := blockedGate(in); b != nil {
			why = b.detail
		}
		m.notice = noticeMsg{text: string(r.F.ID) + ": can't go on yet — " + why}
		return nil
	}
	if out.Confirm {
		// An act, not an answer: it waits. The chip takes the picker's
		// place and the line stays in the composer (chip.go).
		m.reentryPending = &reentryReading{line: msg.note, out: out, forward: label, goOnEnter: goOnEnter(out)}
		m.clearTransientNotice()
		return nil
	}
	m.threadInput.Reset()
	// the "reading…" notice was about a read that is over; a turn has
	// nothing to say in the bar beyond what the thread is about to show
	if out.Reason != "unclassified" {
		m.clearTransientNotice()
	}
	// a turn with nobody live goes to the consult conversation, and the
	// next line continues it rather than being read again (chat.go)
	if s := m.sessionFor(r.F.ID); s == nil || !s.Live() {
		m.startChat(r.F.ID)
	}
	return m.performReentry(r, out)
}

// performReentry carries out one routed outcome — a turn straight away,
// an act once the chip has been taken (chip.go's takeReading).
func (m *Shell) performReentry(r featureRow, out reentry.Outcome) tea.Cmd {
	switch out.Action {
	case reentry.RerunInPlace:
		if err := m.writeReentryEdit(r.F, out); err != nil {
			return err
		}
		m.reentryNotice(r.F, out)
		return m.runStageWithNote(r.F, out.Note)

	case reentry.Rewind:
		return m.commitRewind(r.F, out)

	case reentry.Advance:
		// The forward row's own act, reached in words: the same
		// runCardAction the row's enter takes, so "go on" can only ever
		// do what pressing 1 would have done — including the landing
		// dialog that opens at the verify gate.
		return m.runCardAction(cardAction{id: "advance", key: "g", label: "advance"})

	case reentry.NewCard:
		// Not this card's work. The card stays exactly where it is and
		// the sentence becomes the seed of a new one — the honest answer
		// the fixed rule had no way to give, since every route it knew
		// moved THIS card.
		form := m.openCardForm(domain.KindFeature)
		form.SetText(out.Note)
		// Backing out of the form is backing out of the whole route, so
		// it lands the reader where the route started: the line back in
		// the composer it was typed in, the stop's own options under it,
		// nothing created and nothing moved. Without this the line is
		// gone — takeReading cleared the composer on the way in — and
		// declining the new card silently costs the reader what they
		// wrote.
		line := out.Note
		id := r.F.ID
		form.onCancel = func() tea.Cmd {
			if cur, ok := m.selected(); !ok || cur.F.ID != id {
				// the reader moved on while the form was up; putting the
				// line into another card's composer would be worse than
				// dropping it, so the notice carries it instead
				m.notice = noticeMsg{text: string(id) + ": nothing created — your line was \"" + oneLineText(line) + "\""}
				return nil
			}
			m.threadInput.SetValue(line)
			m.notice = noticeMsg{text: string(id) + ": nothing created — your line is back in the composer"}
			return nil
		}
		m.Overlay.Push(form)
		m.notice = noticeMsg{text: string(r.F.ID) + ": that reads as separate work — " + string(r.F.ID) + " stays where it is"}
		return nil

	default:
		// Turn: the floor, and the only safe one. Everything the table
		// could not route reaches the card as prose, which is what a
		// bare composer has always done with it.
		//
		// It says so when the reader had asked for something else. A
		// line aimed at "send it back" that arrives as a message is a
		// smaller act than the row promised, and a reader who is not
		// told will read the card's silence as the bounce having
		// happened.
		if out.Reason == "unclassified" {
			m.notice = noticeMsg{text: string(r.F.ID) + ": could not tell what kind of change that is — sent as a message instead, nothing moved"}
		}
		return m.sendThreadMessage(r.F, out.Note)
	}
}

// writeReentryEdit performs the outcome's artifact edit, returning a
// command that reports the failure when it cannot — and, critically,
// NOT performing the move.
//
// Failing closed is the rule the whole re-entry rests on. A move whose
// edit did not land is the exact failure this replaces: the note rides
// a kickoff, the stage ends, and the artifact still asks for the wrong
// thing with no check for the miss. Better to refuse and say why.
func (m *Shell) writeReentryEdit(f domain.Feature, out reentry.Outcome) tea.Cmd {
	if out.Edit.Empty() {
		return nil
	}
	if m.engine == nil {
		text := string(f.ID) + ": no engine to record this in the " + artifactNoun(f.Kind) + " — nothing sent back"
		return func() tea.Msg { return noticeMsg{text: text, isErr: true, id: f.ID} }
	}
	if err := m.engine.ApplyReentryEdit(f, out.Edit); err != nil {
		text := string(f.ID) + ": " + sanitize(err.Error()) + " — nothing sent back"
		return func() tea.Msg { return noticeMsg{text: text, isErr: true, id: f.ID} }
	}
	return nil
}

// commitRewind performs a confirmed rewind: the artifact edit first,
// then the walk.
//
// The edit is written HERE, synchronously, before anything else moves —
// the same discipline bounceStage follows for the reads it must make on
// the Update goroutine. A failed edit returns before the session is
// dropped or a note stashed, so a refusal leaves the card untouched
// rather than half-moved.
//
// The walk itself is one store transition per edge, in order, because
// the graph declares one-hop rerun edges and a verify→plan re-entry is
// two of them. History then records both, which is the honest account:
// the card went back through implement, to plan.
func (m *Shell) commitRewind(f domain.Feature, out reentry.Outcome) tea.Cmd {
	if cmd := m.writeReentryEdit(f, out); cmd != nil {
		return cmd
	}
	if out.Note != "" {
		if m.bounceNotes == nil {
			m.bounceNotes = map[domain.FeatureID]string{}
		}
		m.bounceNotes[f.ID] = out.Note
	}
	m.dropSession(f.ID)
	store, path, id := m.store, out.Path, f.ID
	target, edited := out.Target, !out.Edit.Empty()
	return func() tea.Msg {
		ctx := context.Background()
		for _, to := range path {
			if _, err := store.Transition(ctx, id, to, "user"); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true, id: id}
			}
		}
		text := string(id) + " sent back to " + string(target)
		if edited {
			text += " — the miss is now an open comment in the " + artifactNoun(f.Kind)
		} else {
			text += " — your line rides the next run's kickoff"
		}
		return noticeMsg{text: text, reload: true, clearInbox: id}
	}
}

// fixedSendBack is the route the pressed row declares for itself, with
// no classification behind it: the rule the surface followed before this
// file existed, kept for a bare answer and for a classifier that could
// not run.
//
// It switches on the row's own id rather than on the stage, because the
// id IS the delivery (nextsteps.go's sendBack comment) and these three
// are exactly the three deliveries deliverDecisionWords has always
// known. An id that is none of them routes nowhere rather than picking
// one — a fallback that guessed would be the invented action §6.3
// forbids, only harder to notice.
func (m *Shell) fixedSendBack(r featureRow, id, note string) tea.Cmd {
	switch id {
	case "bounce":
		return m.bounceStage(r.F.ID, note)
	case "run":
		return m.runStageWithNote(r.F, note)
	case "changes":
		if note == "" {
			return nil
		}
		return m.sendThreadMessage(r.F, note)
	}
	return nil
}

// reentryNotice says what the router read, in the router's own words,
// so an in-place re-run that quietly did something different from the
// last one is legible. A rewind says it in the confirm instead.
func (m *Shell) reentryNotice(f domain.Feature, out reentry.Outcome) {
	if out.Edit.Empty() {
		return
	}
	m.notice = noticeMsg{text: string(f.ID) + ": recorded under \"" + out.Edit.Section +
		"\" in the " + artifactNoun(f.Kind) + " — re-running " + string(out.Target)}
}

// rowByID finds a card's current board row by id.
//
// The classification turn is a model call, so the board can have moved
// under it — a different card selected, or this one advanced by an
// autonomous run while the turn was in flight. Routing reads the row's
// stage and kind, so it reads them from the board as it is NOW rather
// than from the feature the turn was launched with; a card that is gone
// routes nowhere at all.
func (m *Shell) rowByID(id domain.FeatureID) (featureRow, bool) {
	for i := range m.rows {
		if m.rows[i].F.ID == id {
			return m.rows[i], true
		}
	}
	return featureRow{}, false
}
