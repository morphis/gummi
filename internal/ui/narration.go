package ui

import (
	"strconv"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
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
// CITATIONS. Every claim may carry one anchor naming something openable
// — check:<name>, event:<seq>, diff:<path>:<line>, spec:<section> — and
// a rendered narration may not contain an anchor that resolves to
// nothing (citations.go, invariant 3). That is what makes the one
// generated thing on the page the one thing with a machine-checkable
// contract: hallucinated evidence is discarded by the code that admits
// evidence, not by asking a model nicely.
//
// The mark is a lettered alt chord — [alt+a], [alt+b] — and each of
// those three words is load-bearing. It is an ALT chord because the card
// page's composer owns every printable key, which is why alt+s, alt+d
// and alt+j/k are chords too. It is not a bare DIGIT because digits
// select the picker's options, deliberately: a digit used to fire an
// answer outright with no confirm and no undo, and digit-selects-option
// was the fix (F14, threadinput.go). And it is not an alt+digit either,
// because alt+1/2/3 are the shell's own board/inbox/agent tabs and are
// answered above the card's tier — a numbered chord would have switched
// tab instead of opening the citation. Letters are the footnote
// convention anyway.
//
// The mark carries its key rather than a bare number because the chord
// is a status-bar hint, and a board-width bar has room for about three
// of those.
//
// A claim without an anchor is still a claim. The deterministic
// sentences name their evidence in words as well — the check by its own
// name, the blocker verbatim — so they read correctly with the numbers
// stripped, on the board line and on a page too short for the marks.

// anchor names something a claim cites, in the design's four kinds. It
// is a value rather than a string so a malformed one cannot be built by
// accident — parseAnchor is the only way in from a model's reply.
type anchor struct {
	kind string // "check", "event", "diff", "spec"
	ref  string // the check name, event seq, "<path>:<line>", or section
}

// anchorKinds is the closed set. A kind outside it is not an anchor
// gummi can resolve, so it is not an anchor at all.
var anchorKinds = map[string]bool{"check": true, "event": true, "diff": true, "spec": true}

func (a anchor) empty() bool { return a.kind == "" }

// String round-trips an anchor back to its wire spelling, for notices
// and test failure output.
func (a anchor) String() string {
	if a.empty() {
		return ""
	}
	return a.kind + ":" + a.ref
}

// parseAnchor reads a model's anchor string. Everything about it is
// fail-closed: an unknown kind, a missing reference, or no colon at all
// is (anchor{}, false), and a claim whose anchor did not parse is
// discarded exactly like one whose anchor did not resolve.
func parseAnchor(s string) (anchor, bool) {
	s = strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "`\"'"))
	kind, ref, ok := strings.Cut(s, ":")
	if !ok {
		return anchor{}, false
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	ref = strings.TrimSpace(ref)
	if !anchorKinds[kind] || ref == "" {
		return anchor{}, false
	}
	return anchor{kind: kind, ref: ref}, true
}

// claim is one sentence of the narration, with the evidence that backs
// it. Most claims are free and carry the anchor their own source
// already knows; the code-vs-plan claim is model-written and carries
// whichever anchor it cited, checked before it is admitted.
type claim struct {
	text string
	a    anchor
}

// sentence builds a claim citing nothing — the ordinary case for a
// sentence whose evidence is the card's own state.
func sentence(text string) claim { return claim{text: text} }

// cite builds a claim carrying an anchor.
func cite(text, kind, ref string) claim {
	if text == "" {
		return claim{}
	}
	return claim{text: text, a: anchor{kind: kind, ref: ref}}
}

// narrationStop reports whether the card is genuinely parked for a
// human — the only state a narration is generated in.
//
// This is invariant 4 ("free at rest") stated as a predicate: a running,
// queued or done card has nothing to narrate and asks for nothing. It
// gates BOTH halves of the invariant — the free sentences below are not
// built for such a card, and citations.go refuses to spend a model turn
// on one, since ensureNarration asks this first.
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
func (m *Shell) cardNarration(in nextInput, r featureRow) []claim {
	if !narrationStop(in) {
		return nil
	}
	// The log is read from m.cardEvents rather than from r.Events, and
	// the difference matters: featureRow.Events is filled at RENDER time
	// (thread.go) and is nil on a row handed straight out of m.selected,
	// so a caller outside the render path — openCitation counting the
	// marks, ensureNarration keying the cache — would see a different
	// paragraph from the one on screen and number it differently. One
	// source, and it is the durable one.
	events := m.cardEvents[r.F.ID]
	var out []claim
	if s := whyItStopped(in); s != "" {
		// The failing check is the claim's own evidence and the anchor is
		// free: in.failedCheck comes from m.checksFor, so a check named
		// here is by construction a check that ran.
		if in.failedCheck != "" && in.stage == domain.StageVerify {
			out = append(out, cite(s, "check", in.failedCheck))
		} else {
			out = append(out, sentence(s))
		}
	}
	// liveStretches, not autopilotStretches: a period the driving process
	// abandoned, or one autopilot ended by carrying the card into a stage
	// it may not drive, are both closed by query-time judgement rather
	// than by any row in the log (stretch.go). Reading the raw stretches
	// here would report a period as still running while the card sits in
	// front of you waiting.
	if c := unattendedClaim(liveStretches(r.F, events, m.ws), events); c.text != "" {
		out = append(out, c)
	}
	// The third question, and the only one that costs anything. It is
	// read from the cache and never generated here: rendering must stay
	// free and side-effect-free, so the turn that fills the cache is
	// dispatched from the Update loop (citations.go's ensureNarration).
	if c, ok := m.cachedCodeVsPlan(in, r); ok {
		out = append(out, c)
	}
	return out
}

// approveShaped reports whether this stop is one where "what does the
// code do against what the plan promised" is a question worth paying
// for: implement finished, the verify gate, the landing gate.
//
// Everywhere else the sentence would be noise or worse. A failure, an
// exhausted envelope and a blocked ask are stops about themselves, and
// a reader answering one is not weighing a diff against a plan. Nothing
// has been built at the design stage to compare. And a RESEARCH card
// has no diff at all — it never gets a branch — so the claim is
// undefined for it; its verify is the deterministic document floor
// (DESIGN §13.4), which already reports shape and citations without a
// model.
func approveShaped(in nextInput) bool {
	if !narrationStop(in) || in.kind == domain.KindResearch {
		return false
	}
	if in.hasAsk || in.attn == attnQuestion || in.attn == attnFailure || in.attn == attnBudget {
		return false
	}
	if in.sess == engine.StatePaused {
		return false
	}
	if in.stage != domain.StageImplement && in.stage != domain.StageVerify {
		return false
	}
	return in.finished()
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

	finished := in.finished()
	if !finished {
		return ""
	}

	if in.stage == domain.StageVerify {
		return verifyStopped(in, art)
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
		return "Verify passed — the branch is ready to land" + excusedClause(in.excusedChecks) + "."
	}
	return ""
}

// excusedClause names the checks a clean verify did not actually hold to,
// as a clause on the pass rather than a warning of its own.
//
// A check already failing on the fresh branch is written off as "FAIL
// (pre-existing)" and does not floor the verdict (internal/engine's
// checkReport), which is right — only regressions are this card's fault.
// But the sentence reporting the pass said nothing about it, so a repo
// whose `lint` has been red for a month lost that gate on every card with
// nothing on screen admitting it. It is a clause and not a banner because
// the pass is still a pass: this narrows what it claims, it does not
// contradict it.
//
// Empty for the ordinary case — a branch born clean — so the sentence is
// unchanged wherever there is nothing to disclose.
func excusedClause(names []string) string {
	if len(names) == 0 {
		return ""
	}
	safe := make([]string, 0, len(names))
	for _, n := range names {
		// check names come out of the artifact's own gummi-checks block,
		// which is agent-written text like any other on this page
		safe = append(safe, sanitize(n))
	}
	return ", with " + strings.Join(safe, " and ") + " excused (already failing before this card)"
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
func unattendedClaim(stretches []autopilotStretch, events []state.CardEvent) claim {
	if len(stretches) == 0 {
		return claim{}
	}
	st := stretches[len(stretches)-1]
	if st.decidedNothing() {
		// "it ran a stage while you were out" is worth a rule in the
		// history, but it is not worth a sentence at the stop: the folded
		// receipt above already says which stage ran, and a sentence that
		// reports two zeroes is noise where the reader is deciding.
		return claim{}
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
	text := lead + strings.Join(parts, " and ") + " without you."
	// The period's OPENING event is the citation: it is the row in the
	// thread where the stretch begins, and from there the crossings the
	// sentence counts are drawn in order below it. st.from indexes the
	// same slice autopilotStretches walked, so the seq is always a real
	// event on this card.
	if st.from >= 0 && st.from < len(events) {
		return cite(text, "event", itoa64(events[st.from].Seq))
	}
	return sentence(text)
}

// isAre agrees a verb with a count, so the sentences above read as
// English at one and at many.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// itoa64 formats an event sequence for an anchor.
func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
