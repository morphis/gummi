package ui

import (
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// The narration is the short paragraph above the answer set: what a
// reader needs to hold in their head at a stop, said once, so they do
// not have to reconstruct it by sweeping the options.
//
// It answers three questions in order (PROPOSAL-card-surface §1):
//
//  1. why it stopped — the verdict, the failing check, the blocker
//  2. what it did unattended — gates crossed and answers taken without you
//  3. code vs plan — what the diff does against what the plan promised
//
// The first two are in this file and cost nothing: every fact they state
// is already on nextInput or already folded out of the event log by
// stretch.go. The third is the only claim in the paragraph that is not
// derivable from the log, because nothing compares a diff to a plan's
// steps — it is model-written, and it is a later phase's to add.
//
// THE SPLIT IS THE POINT. The model narrates; the workflow enumerates.
// Nothing here may add an option, drop one, or change their order —
// stageActions is a pure function of nextInput and stays one (DESIGN
// §6.3). A sentence that disagrees with the rows below it is a bug in
// this file, never a reason to re-rank them.
//
// Citations: the design's contract is that every claim carries a
// resolvable anchor (check:<name>, event:<id>, diff:<file>:<line>,
// spec:<anchor>) and that the bracketed number opens it. That contract
// arrives with the model-written sentence, and it needs a key that is
// not a digit — the digits already select the picker's options, and
// deliberately so (a digit used to fire an answer outright, with no
// confirm and no undo). The sentences here name their evidence in words
// instead: the check by its own name, the count of crossings, the
// blocker verbatim. Every one of them is one keystroke from the tab that
// shows it.

// narrationStop reports whether the card is genuinely parked for a
// human — the only state a narration is generated in.
//
// This is invariant 4 ("free at rest") stated as a predicate rather than
// as a cache: a running, queued or done card has nothing to narrate and
// asks for nothing, so there is no work to skip and no staleness to
// guard against. When the model-written sentence lands it will need the
// real cache key (the newest event id, plus the gate's blocking counts);
// until then the honest implementation of "never regenerated" is "never
// generated".
func narrationStop(in nextInput) bool {
	if in.landed || in.stage == domain.StageDone {
		return false
	}
	switch in.sess {
	case engine.StateQueued:
		return false
	case engine.StateRunning:
		// mid-turn the agent owns the screen; a blocking question is the
		// one thing that stops it and needs a person.
		return in.hasAsk
	}
	return true
}

// cardNarration builds the paragraph for a stop. Empty is the ordinary
// answer for a card with nothing to say — an idle card in todo has not
// stopped for a reason, and has done nothing unattended.
func (m *Shell) cardNarration(in nextInput, r featureRow) []string {
	if !narrationStop(in) {
		return nil
	}
	var out []string
	if s := whyItStopped(in); s != "" {
		out = append(out, s)
	}
	// liveStretches, not autopilotStretches: a period the driving process
	// abandoned, or one autopilot ended by carrying the card into a stage
	// it may not drive, are both closed by query-time judgement rather
	// than by any row in the log (stretch.go). Reading the raw stretches
	// here would report a period as still running while the card sits in
	// front of you waiting.
	if s := unattendedSentence(liveStretches(r.F, r.Events, m.ws)); s != "" {
		out = append(out, s)
	}
	return out
}

// whyItStopped is the first sentence. Its order of precedence is the
// same one decisionQuestion and stageActions read the state in, so the
// three can never describe different stops.
func whyItStopped(in nextInput) string {
	art := artifactNoun(in.kind)
	stage := string(in.stage)

	switch {
	case in.hasAsk || in.attn == attnQuestion:
		return "The agent asked a question and is blocked on your reply."
	case in.attn == attnFailure:
		if in.verdictFloorReason != "" {
			return "The " + stage + " session stopped: " + sanitize(in.verdictFloorReason) + "."
		}
		return "The " + stage + " session errored before it finished."
	case in.attn == attnBudget:
		return "The " + stage + " stage reached its envelope and stopped."
	case in.sess == engine.StatePaused:
		return "The " + stage + " run is paused — you stopped it."
	}

	// A blocked gate outranks any verdict: the crossing is held shut
	// whatever verify thought, and saying "verify passed" above a row
	// that reads "you cannot cross yet" would be two sentences about
	// different cards.
	if in.openSpecQs > 0 {
		return itoa(in.openSpecQs) + " open comment" + plural(in.openSpecQs) + " in the " + art + " " +
			isAre(in.openSpecQs) + " holding the gate shut."
	}
	if in.openDiffComments > 0 {
		return itoa(in.openDiffComments) + " unresolved diff comment" + plural(in.openDiffComments) + " " +
			isAre(in.openDiffComments) + " holding the gate shut."
	}
	if len(in.undrafted) > 0 {
		return "The " + art + " still has " + strings.Join(in.undrafted, " and ") + " blank, and " +
			isAre(len(in.undrafted)) + " required before the gate opens."
	}

	finished := in.attn == attnGate || in.sess == engine.StateDone
	if !finished {
		return ""
	}

	if in.stage == domain.StageVerify {
		return verifyStopped(in, art)
	}
	if in.attn != attnGate && in.sess != engine.StateDone {
		return ""
	}
	switch in.stage {
	case domain.StagePlan:
		return "The design stage finished and wrote the " + art + " — the gate is waiting on you."
	case domain.StageImplement:
		return "Implement finished and its critique passed — the diff has not been read yet."
	}
	return ""
}

// verifyStopped is the first sentence at a finished verify, where the
// state actually has something to say: a verdict, a failing check, or an
// environment gummi's own floor refused the run on.
func verifyStopped(in nextInput, art string) string {
	if in.failedCheck != "" {
		return "Verify stopped on the '" + sanitize(in.failedCheck) + "' check." + loopBreaker(in)
	}
	if in.verdict == verdictBlocked {
		if in.verdictFloorReason != "" {
			return "Verify could not run: " + sanitize(in.verdictFloorReason) + "."
		}
		return "Verify is blocked on the environment — the missing prerequisites are in the " + art + "."
	}
	if in.verdict == verdictFail || in.verdict == verdictChanges {
		return "Verify reported failure — the evidence is in the " + art + "." + loopBreaker(in)
	}
	if in.verdict == verdictUnclear && in.escalated {
		return "Verify gave no clear verdict, and the loop gave up rather than passing it." + loopBreaker(in)
	}
	if in.verdict == verdictPass {
		return "Verify passed — the branch is ready to land."
	}
	return ""
}

// loopBreaker is the FD-004 guard, moved from the ordering into the
// narration.
//
// It used to de-rank the bounce row after a repeated failed verify, with
// its warning in the row's own why. That no longer works: "send it back"
// carries the in-place re-run as well as the rewind, so demoting the row
// would demote an answer the guard was never about. Said here it warns
// without touching the answer set at all, which is the split this whole
// change rests on — the narration may say anything, and may change
// nothing.
//
// It leads with a space because it is appended to a finished sentence.
func loopBreaker(in nextInput) string {
	if in.verifyBounces < 1 {
		return ""
	}
	n := itoa(in.verifyBounces + 1)
	return " It has now failed " + n + " times, so sending it back has already bought a full rework round that changed nothing — check the environment and the verification plan before spending another."
}

// unattendedSentence is the second sentence: what the card decided while
// nobody was watching.
//
// It reports the newest period only — the one that led to this stop.
// Summing every period a card ever ran would make the sentence grow
// without bound and stop being about the stop you are looking at, and
// the older ones are already drawn where they happened, bracketed among
// the history (stretch.go).
func unattendedSentence(stretches []autopilotStretch) string {
	if len(stretches) == 0 {
		return ""
	}
	st := stretches[len(stretches)-1]
	if st.decidedNothing() {
		// "it ran a stage while you were out" is worth a rule in the
		// history, but it is not worth a sentence at the stop: the folded
		// receipt above already says which stage ran, and a sentence that
		// reports two zeroes is noise where the reader is deciding.
		return ""
	}
	var parts []string
	if n := len(st.gates); n > 0 {
		parts = append(parts, "crossed "+itoa(n)+" gate"+plural(n))
	}
	if n := len(st.answers); n > 0 {
		parts = append(parts, "took "+itoa(n)+" answer"+plural(n))
	}
	lead := "Before that, autopilot "
	if st.running() {
		lead = "Autopilot has the card and has "
	}
	return lead + strings.Join(parts, " and ") + " without you."
}

// isAre agrees a verb with a count, so the sentences above read as
// English at one and at many.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
