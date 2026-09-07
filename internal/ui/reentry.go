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
	if r.F.Stage == domain.StagePlan {
		return m.sendThreadMessage(r.F, note)
	}
	eng := m.engine
	if eng == nil {
		return m.fixedSendBack(r, fallback, note)
	}
	m.threadInput.Reset()
	m.notice = noticeMsg{text: string(r.F.ID) + ": reading the card to place your line…"}
	f := r.F
	return func() tea.Msg {
		intent, err := eng.ClassifyReentry(context.Background(), f, note)
		return reentryClassifiedMsg{f: f, note: note, fallback: fallback, intent: intent, err: err}
	}
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
	out := reentry.Decide(reentry.Input{
		Stage: r.F.Stage, Kind: r.F.Kind, Intent: msg.intent, Note: msg.note,
	})
	return m.performReentry(r, out)
}

// performReentry carries out one routed outcome.
func (m *Shell) performReentry(r featureRow, out reentry.Outcome) tea.Cmd {
	switch out.Action {
	case reentry.RerunInPlace:
		if err := m.writeReentryEdit(r.F, out); err != nil {
			return err
		}
		m.reentryNotice(r.F, out)
		return m.runStageWithNote(r.F, out.Note)

	case reentry.Rewind:
		// A REWIND CONFIRMS. It moves the card off the stage the reader
		// is looking at, sometimes two stages, and every stage it passes
		// gets re-run — that is a bigger thing than the row said it was
		// doing, so it is said out loud before it happens. An in-place
		// re-run above just goes: it is exactly what "send it back" has
		// always meant at that stage.
		m.Overlay.Push(&confirmDialog{
			id:       "reentry:" + string(r.F.ID),
			question: rewindQuestion(r.F, out),
			// Wrapped here rather than left to the dialog: confirmDialog
			// renders detail as one line and every other caller's is
			// short, so an unwrapped sentence loses its tail off the
			// right edge — and the tail is the half naming the stages
			// the card walks through.
			detail:       wrapText(rewindDetail(out), max(m.width-12, 40)),
			confirmLabel: "Send it back",
			onConfirm:    func() tea.Cmd { return m.commitRewind(r.F, out) },
		})
		return nil

	case reentry.NewCard:
		// Not this card's work. The card stays exactly where it is and
		// the sentence becomes the seed of a new one — the honest answer
		// the fixed rule had no way to give, since every route it knew
		// moved THIS card.
		form := newFeatureForm(m.profileNames, m.repoNames, m.repoHasDefault(), m.envelopePrefill(), m.createFeature)
		form.desc.SetValue(out.Note)
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

// rewindQuestion is the confirm's headline: what will happen, named by
// the stage it lands on rather than by the intent that got it there. A
// reader confirming a move needs to know where the card ends up.
func rewindQuestion(f domain.Feature, out reentry.Outcome) string {
	return "Send " + string(f.ID) + " back to " + string(out.Target) + "?"
}

// rewindDetail says the two things a reader cannot see from the
// headline: that the miss is written into the artifact (and so will hold
// the gate shut until it is answered), and every stage that re-runs on
// the way.
func rewindDetail(out reentry.Outcome) string {
	var b strings.Builder
	if !out.Edit.Empty() {
		b.WriteString("Your line is added to \"" + out.Edit.Section + "\" as an open comment first, so the gate stays shut until it is answered. ")
	}
	if len(out.Path) > 1 {
		names := make([]string, 0, len(out.Path))
		for _, s := range out.Path {
			names = append(names, string(s))
		}
		b.WriteString("The card walks back through " + strings.Join(names, " then ") + ", and each stage runs again.")
	} else {
		b.WriteString(strings.ToUpper(string(out.Target)[:1]) + string(out.Target)[1:] + " runs again from there.")
	}
	return b.String()
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
