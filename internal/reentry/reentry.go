// Package reentry is the single decision seam for what a typed sentence
// at a stop means for the card: send it as a turn, re-run the stage in
// place, rewind to an earlier stage with the artifact edited on the way,
// or split the work onto a card of its own.
//
// It is the routing half of a two-part answer. Classification — reading
// a human sentence and naming which of the six things it is — is a
// model's job and lives in the engine (Engine.ClassifyReentry). Routing
// is not: given the intent, where the card goes is a compiled-in rule,
// the same one for the TUI, the web surface and the headless driver.
// Decide is pure — no I/O, no store, no session, no agent — so the rule
// table is stated once and table-tested without any of them, the same
// shape internal/gatepolicy has and for the same reason.
//
// TWO PROPERTIES MAKE IT SAFE TO PUT A MODEL IN FRONT OF THIS.
//
//  1. Every outcome is an act the workflow already declares. The
//     rewinds walk internal/workflow's own rerun edges and nothing else,
//     so no classification — however wrong, however adversarial — can
//     produce a transition the graph does not have. An intent that maps
//     to no legal edge degrades to a turn, which is DESIGN §6.3's
//     standing safety property: prose is always accepted and always
//     safe, becoming a turn or a routed re-entry, never an action
//     nobody offered.
//
//  2. A rewind never travels without its artifact edit. A note carried
//     into a kickoff evaporates when the stage ends: the spec stays
//     wrong, and verify still has no check for the thing that was
//     missed — which is the bug this package exists to fix, not a
//     detail of how it is delivered. So Decide refuses to rewind at all
//     when it cannot name the section the edit belongs in, and hands
//     back a turn instead. Edit.Section is the caller's instruction,
//     not a suggestion.
//
// The edit lands as a `%%` user marker (the seam the ask protocol
// already uses, spec.AddComment), which means it also holds the gate
// shut until somebody resolves it. That is deliberate and it is the
// whole point: a requirement the plan missed cannot be crossed past
// again without being read.
//
// reentry sits below every caller: it imports internal/domain and
// internal/workflow, both pure, and nothing else. It deliberately does
// NOT carry the finished session's verdict, which the design's original
// signature named — internal/verdict imports internal/engine, so a
// verdict field here would make the package unusable from the engine
// (which needs the vocabulary below to write the classifier's prompt),
// and no rule in the table consults one.
package reentry

import (
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/workflow"
)

// Intent is the closed vocabulary a sentence is classified into. It is
// closed on purpose: the classifier picks one of these or nothing, and
// nothing routes to a turn. Adding a word here is adding a route, and
// the route has to be in the table below before the word is worth
// asking for.
type Intent string

const (
	// RequirementMissing: the artifact never asked for the thing. The
	// build is not wrong about the plan; the plan is missing a line.
	RequirementMissing Intent = "requirement_missing"
	// PlanWrong: the artifact asked for the wrong thing. The approach
	// itself is what needs to change.
	PlanWrong Intent = "plan_wrong"
	// ImplementationWrong: the artifact is right and the code does not
	// match it.
	ImplementationWrong Intent = "implementation_wrong"
	// CheckMissing: nothing proves the thing works. The verification
	// plan, not the code and not the design.
	CheckMissing Intent = "check_missing"
	// SeparateCard: real, but not this card's work.
	SeparateCard Intent = "separate_card"
	// Question: a question about the card rather than a complaint about
	// it. The honest answer is an answer, not a rewind.
	Question Intent = "question"
	// Proceed: go on. Not a complaint at all — the forward answer the
	// stop already offers, asked for in words: approve the artifact,
	// advance, run the stage that is waiting. It never means more than
	// that row means, and it never lands anywhere a row does not.
	Proceed Intent = "proceed"
)

// intentKnown reports whether i is one of the vocabulary's words.
func intentKnown(i Intent) (Intent, bool) {
	for _, v := range Vocabulary() {
		if v == i {
			return v, true
		}
	}
	return "", false
}

// Vocabulary lists the intents a classifier may return, in the order a
// prompt should present them. It exists so the classifier's prompt is
// written from the same list the router switches on, rather than from a
// second copy of the six words free to drift from it.
func Vocabulary() []Intent {
	return []Intent{RequirementMissing, PlanWrong, ImplementationWrong, CheckMissing, SeparateCard, Question, Proceed}
}

// Describe is the one-line gloss of an intent, for the classifier's
// prompt and for a notice explaining what the router read. Same reason
// as Vocabulary: one copy of the meaning, beside the constant.
func Describe(i Intent) string {
	switch i {
	case RequirementMissing:
		return "the artifact never asked for this — a requirement is missing from it"
	case PlanWrong:
		return "the artifact asked for the wrong thing — the approach itself is wrong"
	case ImplementationWrong:
		return "the artifact is right and the code does not match it"
	case CheckMissing:
		return "nothing verifies this — the verification plan is missing a check"
	case SeparateCard:
		return "real, but not this card's work"
	case Question:
		return "a question about the card, not a complaint about it"
	case Proceed:
		return "go on — approve, advance, or run what this stop is offering; not a complaint"
	}
	return ""
}

// ParseIntent reads a classifier's reply word into the vocabulary. It
// accepts the bare word in any case with surrounding punctuation, and
// nothing else: an unrecognised reply is ("", false), which callers
// route as a turn rather than guessing at the nearest match.
func ParseIntent(s string) (Intent, bool) {
	got := strings.ToLower(strings.TrimSpace(s))
	got = strings.Trim(got, ".,:;\"'`*")
	got = strings.ReplaceAll(got, "-", "_")
	got = strings.ReplaceAll(got, " ", "_")
	for _, i := range Vocabulary() {
		if got == string(i) {
			return i, true
		}
	}
	return "", false
}

// Action is what a caller should do with the sentence.
type Action int

const (
	// Turn sends the line to the card as an ordinary message. It is the
	// floor: everything the table cannot route lands here, and nothing
	// about it can surprise a reader, because it is what a bare composer
	// has always done with prose.
	Turn Action = iota
	// RerunInPlace re-runs Outcome.Target — always the card's current
	// stage — with the line riding its kickoff. No transition, so
	// nothing to confirm.
	RerunInPlace
	// Rewind walks Outcome.Path backward along the graph's rerun edges
	// to Outcome.Target, after writing Outcome.Edit into the artifact.
	// Always confirms.
	Rewind
	// NewCard splits the sentence onto a card of its own and leaves this
	// card exactly where it is. Confirms, because it mints something.
	NewCard
	// Advance crosses the stop's forward gate as the user — approve the
	// artifact, advance to verify, land on main — the act the stop's own
	// "go on" row performs, reached in words. Always confirms: it moves
	// the card, and past verify it leaves it.
	Advance
)

// String names an Action for log lines and test failure output.
func (a Action) String() string {
	switch a {
	case Turn:
		return "turn"
	case RerunInPlace:
		return "rerun-in-place"
	case Rewind:
		return "rewind"
	case NewCard:
		return "new-card"
	case Advance:
		return "advance"
	default:
		return "unknown"
	}
}

// Edit is the artifact write a re-entry performs before it moves the
// card: Text, added as an open `%%` user marker under the Section
// heading. Empty for a route that has nothing to record.
type Edit struct {
	Section string
	Text    string
}

// Empty reports whether there is no edit to perform.
func (e Edit) Empty() bool { return strings.TrimSpace(e.Section) == "" }

// Input is everything Decide needs. It carries no session, store or
// agent handle: a caller classifies the sentence first and passes the
// answer in.
type Input struct {
	// Stage is where the card is right now — the stage the reader is
	// looking at when they type.
	Stage domain.Stage
	// Kind selects which artifact section an edit lands in; the three
	// kinds spell the same three sections differently (spec.Template,
	// BugTemplate, research.go).
	Kind domain.Kind
	// Intent is the classifier's answer, or "" when it had none.
	Intent Intent
	// Note is the sentence itself, verbatim. An empty one routes as a
	// turn: there is nothing to carry and nothing to write.
	Note string

	// The stop's own forward answer, which is all Proceed may ever mean.
	// The caller reads these off the answer set it is rendering, so a
	// "go on" can only produce the act that set already offers — the
	// confinement is by construction, not by a second table here.
	//
	// Forward is the stage the offered advance row leads to (StageDone
	// for the landing), "" when the stop offers no advance. Rerun
	// reports that the stop's forward act is a run of the current stage
	// instead. Blocked names why the gate is held shut ("open comments",
	// "undrafted sections", …) and "" when it is clear; a blocked gate
	// answers Proceed with the blocker, never with a crossing.
	Forward domain.Stage
	Rerun   bool
	Blocked string
}

// Outcome is Decide's answer. Reason is machine-readable and never
// user-facing prose — callers build their own sentence from it, the
// same contract gatepolicy.Outcome has.
type Outcome struct {
	Action Action
	// Target is the stage the card ends at: itself for RerunInPlace, the
	// end of Path for Rewind, unset otherwise.
	Target domain.Stage
	// Path is the rewind's stages in the order they are taken, each one
	// a rerun edge the graph declares. A verify→plan re-entry is
	// [implement, plan]: two real transitions, both recorded, rather
	// than one long edge the graph does not have.
	Path []domain.Stage
	// Edit is written into the artifact BEFORE the first transition.
	Edit Edit
	// Note is the line to carry into whatever runs next.
	Note string
	// Confirm reports whether the caller must ask before performing
	// this. Everything that moves the card or spends its credits
	// confirms — a rewind, a re-run, an advance, a new card; only a
	// turn just goes.
	Confirm bool
	Reason  string
}

// maxWalk bounds the rerun walk. The graph is four stages deep, so any
// legal rewind is at most two edges; the bound exists so a future graph
// with a cycle in its rerun edges cannot hang a render.
const maxWalk = 4

// Decide routes one classified sentence.
//
// The design stage is the whole first rule and it costs nothing: at
// plan the architect is live in the very thread the line was typed
// into, the artifact is what it is writing right now, and there is no
// earlier stage to rewind to. Every sentence there is already delivered
// correctly by being a turn, so the router says so — and a caller that
// checks this first never spends a classification call on the stage
// that needs one least.
func Decide(in Input) Outcome {
	note := strings.TrimSpace(in.Note)
	if note == "" {
		return Outcome{Reason: "no-note"}
	}
	// The design stage. Decide is only ever reached here with nobody
	// live in the thread — a line typed at a live architect is the next
	// thing said to it and is never read (the caller's rule) — so a
	// complaint has no one to be a turn to. It starts the design stage
	// with the line as its kickoff instead: confirmed, since that spends.
	// A question is still a question, and "go on" is the approval the
	// gate is waiting for; both go through the table like anywhere else.
	if in.Stage == domain.StagePlan && in.Intent != Proceed && in.Intent != Question {
		if _, known := intentKnown(in.Intent); !known {
			return Outcome{Note: note, Reason: "unclassified"}
		}
		return Outcome{Action: RerunInPlace, Target: in.Stage, Note: note, Confirm: true, Reason: "design-stage-rerun"}
	}

	switch in.Intent {
	case Proceed:
		return proceed(in, note)
	case Question:
		return Outcome{Note: note, Reason: "question"}
	case SeparateCard:
		return Outcome{Action: NewCard, Note: note, Confirm: true, Reason: "separate-card"}
	case CheckMissing:
		// The verification plan is the artifact's own section, so this is
		// an edit plus a re-run of wherever the card is — never a rewind.
		// At verify that is exactly the tight loop it should be: write
		// the check, run the checks again.
		return withEdit(in, note, Outcome{
			Action: RerunInPlace, Target: in.Stage, Note: note, Confirm: true, Reason: "check-missing",
		})
	case RequirementMissing, PlanWrong:
		return rewindTo(in, note, domain.StagePlan, string(in.Intent))
	case ImplementationWrong:
		return rewindTo(in, note, domain.StageImplement, string(in.Intent))
	}
	// Nothing recognised. The floor, and the only honest one: a sentence
	// gummi could not read is still a sentence someone meant, and a turn
	// delivers it without inventing a move for it.
	return Outcome{Note: note, Reason: "unclassified"}
}

// proceed routes "go on" to the stop's own forward answer, and nowhere
// else. It has no table of its own: Forward, Rerun and Blocked are what
// the caller read off the answer set, so the outcome is the row that
// set already shows, chosen in words. A blocked gate is answered with
// the blocker — a turn, said back — because the crossing the reader
// asked for is the one thing the stop cannot offer.
func proceed(in Input, note string) Outcome {
	switch {
	case in.Blocked != "":
		return Outcome{Note: note, Reason: "proceed-blocked"}
	case in.Forward != "":
		return Outcome{Action: Advance, Target: in.Forward, Note: note, Confirm: true, Reason: "proceed"}
	case in.Rerun:
		return Outcome{Action: RerunInPlace, Target: in.Stage, Note: note, Confirm: true, Reason: "proceed-rerun"}
	}
	return Outcome{Note: note, Reason: "proceed-nowhere"}
}

// rewindTo builds the outcome for an intent that names a target stage.
// Landing on the stage the card is already in is an in-place re-run, not
// a zero-length rewind: there is no transition to record. It still
// confirms — a re-run starts a session that spends credits now, and
// every spend asks (PROPOSAL-composer-router §9.2) — so implement's own
// "send it back" now says what it is about to run before it runs it.
func rewindTo(in Input, note string, target domain.Stage, reason string) Outcome {
	if target == in.Stage {
		return withEdit(in, note, Outcome{
			Action: RerunInPlace, Target: target, Note: note, Confirm: true, Reason: reason + "-in-place",
		})
	}
	path, ok := rewindPath(in.Stage, target)
	if !ok {
		// No backward route: the target is forward of here, or the graph
		// has no rerun edge to walk. Never invent one.
		return Outcome{Note: note, Reason: reason + "-unreachable"}
	}
	return withEdit(in, note, Outcome{
		Action: Rewind, Target: target, Path: path, Note: note, Confirm: true, Reason: reason,
	})
}

// withEdit attaches the artifact edit an outcome owes, or replaces the
// outcome with a turn when this kind has no section to write it into.
//
// The refusal is the point (see the package doc): a research card has no
// verification section, so "the checks miss this" has nowhere to land on
// one, and a re-entry that moved the card anyway would carry the note
// into a kickoff that forgets it. A turn at least reaches a person.
func withEdit(in Input, note string, out Outcome) Outcome {
	section, ok := editSection(in.Kind, in.Intent)
	if !ok {
		return Outcome{Note: note, Reason: out.Reason + "-no-section"}
	}
	if section != "" {
		out.Edit = Edit{Section: section, Text: note}
	}
	return out
}

// editSection names the artifact section an intent's edit belongs under,
// per kind. The three kinds spell the same three jobs differently and
// each spelling is the heading its own template actually renders
// (spec.Template, spec.BugTemplate, spec research.go) — a heading that
// does not exist is a write that silently lands at the top of the
// document, so these are matched against the templates rather than
// guessed.
//
// Research has no verification section at all: its verify is the
// deterministic document floor (DESIGN §13.4), which no artifact prose
// can add a check to. That row is ("", false) rather than a nearby
// heading, and withEdit turns it into a turn.
func editSection(kind domain.Kind, intent Intent) (string, bool) {
	switch intent {
	case RequirementMissing:
		switch kind {
		case domain.KindBug:
			return "Summary", true
		case domain.KindResearch:
			return "Brief", true
		default:
			return "Problem", true
		}
	case PlanWrong:
		switch kind {
		case domain.KindBug:
			return "Root cause", true
		case domain.KindResearch:
			return "Direction", true
		default:
			return "Chosen approach", true
		}
	case CheckMissing:
		switch kind {
		case domain.KindBug:
			return "Verification", true
		case domain.KindResearch:
			return "", false
		default:
			return "Verification plan", true
		}
	case ImplementationWrong:
		// The artifact is right; only the code is wrong. There is
		// nothing to record in the document, and a marker saying "the
		// code did not match this" would hold the gate shut over a
		// section that is already correct.
		return "", true
	}
	return "", false
}

// rewindPath walks the graph's rerun edges from `from` back to `to`,
// returning the stages in the order they are entered.
//
// It walks rather than asking for one edge because the graph declares
// only one-hop rerun edges and RerunTarget returns a single target: a
// verify-stage miss that belongs in the plan is a compound walk of
// edges that already exist, not a new long edge. No schema change, and
// history records both transitions honestly — which is the difference
// between "this card went back to plan" and "this card went back to
// plan, through implement, because verify found the plan was wrong".
func rewindPath(from, to domain.Stage) ([]domain.Stage, bool) {
	var path []domain.Stage
	cur := from
	for range maxWalk {
		next, ok := workflow.RerunTarget(cur)
		if !ok {
			return nil, false
		}
		path = append(path, next)
		if next == to {
			return path, true
		}
		cur = next
	}
	return nil, false
}
