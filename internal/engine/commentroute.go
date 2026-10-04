package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/workflow"
)

// A review comment belongs to the stage that writes what it is about.
//
// "Request changes" used to hand a card's open comments to whichever stage
// the card happened to be in. At verify that meant a note on the Chosen
// approach went to the verify agent, which has no business rewriting the
// design — and, the design being the thing every later stage builds on,
// could not have fixed the build that follows from it either. The same
// comment also held every gate on the way to done, so the only stage that
// could answer it was one the card had already left.
//
// So a comment is owned: a spec comment by the stage whose writer writes
// the section it sits in (SectionOwner), a diff comment by implement,
// which writes the code. Ownership decides three things, and all three
// read the rules below so they cannot disagree:
//
//   - where "request changes" sends the card (RouteComments): to the
//     earliest stage that owns an open comment, walking the graph's own
//     rerun edges back to it — a verify-time design note is two real
//     transitions, verify → implement → plan, not an edge the graph lacks;
//   - which comments a stage's writer is given (SpecCommentsAnswered):
//     the ones it owns, and the ones nobody owns;
//   - which comments hold a stage's gate (SpecCommentsHolding,
//     DiffCommentsHold): every one not owned by a LATER stage. A card
//     rewound to plan for a design note is not held at the plan gate by a
//     note on Progress the architect cannot answer; that note holds
//     implement's gate instead, and nothing reaches done with a comment
//     open, which is the floor DESIGN §6.1 sets.
//
// A comment in a section no stage owns — the preamble, the Review
// section, the Verification plan the verify stage re-authors, a section a
// template does not have — is the current stage's, which is what every
// comment was before ownership existed.

// sectionOwners maps a card type's sections to the stage whose writer
// writes them, by the headings its template renders (spec.Template,
// spec.BugTemplate, research.go) and the stage contracts in hints.go.
// Keys are lower-cased. A section absent here has no owner.
//
// Only the two writing stages own anything. Verify judges; the sections
// it writes (the verification results) are ones the human's comment on is
// best answered by re-running it in place, which is what an unowned
// comment at verify gets.
var sectionOwners = map[domain.CardType]map[string]domain.Stage{
	{Kind: domain.KindFeature}: {
		"problem":               domain.StagePlan,
		"out of scope":          domain.StagePlan,
		"considered approaches": domain.StagePlan,
		"chosen approach":       domain.StagePlan,
		"implementation notes":  domain.StagePlan,
		"progress":              domain.StageImplement,
	},
	{Kind: domain.KindBug}: {
		"summary":            domain.StagePlan,
		"reproduction":       domain.StagePlan,
		"expected vs actual": domain.StagePlan,
		"environment":        domain.StagePlan,
		"discussion":         domain.StagePlan,
		"root cause":         domain.StagePlan,
		"fix":                domain.StageImplement,
	},
	// the survey: shapeHint settles the question, its constraints and the
	// direction; investigateHint writes the Findings
	{Kind: domain.KindResearch}: {
		"brief":       domain.StagePlan,
		"questions":   domain.StagePlan,
		"constraints": domain.StagePlan,
		"direction":   domain.StagePlan,
		"findings":    domain.StageImplement,
	},
	// the diagnosis: the design pass sets the reproduction and the bounds,
	// diagnoseInvestigateHint writes the rest
	{Kind: domain.KindResearch, Mode: domain.ModeDiagnosis}: {
		strings.ToLower(spec.DiagSectionSymptom):  domain.StagePlan,
		strings.ToLower(spec.DiagSectionRepro):    domain.StagePlan,
		strings.ToLower(spec.DiagSectionCons):     domain.StagePlan,
		strings.ToLower(spec.DiagSectionEvidence): domain.StageImplement,
		strings.ToLower(spec.DiagSectionRuledOut): domain.StageImplement,
		strings.ToLower(spec.DiagSectionCauses):   domain.StageImplement,
	},
}

// SectionOwner names the stage whose writer writes section on a card of
// type ct. ok is false for a section no stage owns.
func SectionOwner(ct domain.CardType, section string) (domain.Stage, bool) {
	st, ok := sectionOwners[ct][strings.ToLower(strings.TrimSpace(section))]
	return st, ok
}

// SpecCommentOwner names the stage that owns thread t's open comment: the
// owner of the section the human's marker sits in. The marker's own line
// is read rather than its anchor, since a doc-level marker has no anchor
// but still sits somewhere, and a comment on a heading sits in the section
// the heading opens.
func SpecCommentOwner(ct domain.CardType, doc spec.Doc, t spec.Thread) (domain.Stage, bool) {
	line := t.Anchor
	if mk := spec.UnresolvedUserMarker(t); mk != nil {
		line = mk.Line
	}
	return SectionOwner(ct, doc.SectionAt(line))
}

// stageAfter reports whether a comes after b in workflow order. A stage
// off the graph (a freeform card's StageOpen) is after nothing and nothing
// is after it, so ownership never changes anything for it.
func stageAfter(a, b domain.Stage) bool {
	ia, ib := slices.Index(domain.Stages, a), slices.Index(domain.Stages, b)
	return ia >= 0 && ib >= 0 && ia > ib
}

// SpecCommentsHolding returns the open human comments that hold stage's
// gate: every one except those a later stage owns.
func SpecCommentsHolding(ct domain.CardType, stage domain.Stage, doc spec.Doc) []spec.Thread {
	var out []spec.Thread
	for _, t := range doc.UserOpenThreads() {
		if owner, ok := SpecCommentOwner(ct, doc, t); ok && stageAfter(owner, stage) {
			continue
		}
		out = append(out, t)
	}
	return out
}

// SpecCommentsAnswered returns the open human comments stage's writer is
// given to answer: the ones it owns and the ones nobody owns. A comment an
// earlier stage owns is not among them — it holds this gate until the card
// is sent back to that stage (RouteComments), and handing it to this
// writer meanwhile is how a verify agent came to rewrite a design.
func SpecCommentsAnswered(ct domain.CardType, stage domain.Stage, doc spec.Doc) []spec.Thread {
	var out []spec.Thread
	for _, t := range doc.UserOpenThreads() {
		if owner, ok := SpecCommentOwner(ct, doc, t); ok && owner != stage {
			continue
		}
		out = append(out, t)
	}
	return out
}

// DiffCommentsHold reports whether open diff comments hold stage's gate.
// Implement owns them, so they hold its gate and every one after it, and
// nothing before: a card sent back to plan with diff comments open is not
// held at the design gate by lines of code the architect does not write.
func DiffCommentsHold(stage domain.Stage) bool {
	return !stageAfter(domain.StageImplement, stage)
}

// CommentRoute is where "request changes" sends a card's open comments.
type CommentRoute struct {
	// From is the stage the card is in.
	From domain.Stage
	// Target is the stage whose writer takes the comments: From itself
	// when they are delivered in place.
	Target domain.Stage
	// Path is the rerun edges walked to reach Target, in the order they
	// are taken; nil in place.
	Path []domain.Stage
	// Sections are the spec sections whose comments pulled the card back,
	// in document order, and Diff whether the diff's did. Both are empty
	// in place.
	Sections []string
	Diff     bool
}

// Rewinds reports whether the route moves the card back.
func (r CommentRoute) Rewinds() bool { return len(r.Path) > 0 }

// Why says, for a confirmation, what pulled the card back — "the comments
// on Chosen approach", "the diff comments". Empty in place.
func (r CommentRoute) Why() string {
	var parts []string
	if len(r.Sections) > 0 {
		parts = append(parts, "the comments on "+strings.Join(r.Sections, ", "))
	}
	if r.Diff {
		parts = append(parts, "the diff comments")
	}
	return strings.Join(parts, " and ")
}

// Question is the confirmation a rewinding route asks before it moves the
// card, the same words on every face. Empty in place.
func (r CommentRoute) Question(id domain.FeatureID) string {
	if !r.Rewinds() {
		return ""
	}
	why := r.Why()
	// the reason starts a sentence of its own, after the question
	if why != "" {
		why = strings.ToUpper(why[:1]) + why[1:]
	}
	q := fmt.Sprintf("send %s back to %s? %s are %s's to answer", id, r.Target, why, r.Target)
	var reruns []string
	for _, s := range domain.Stages {
		if stageAfter(s, r.Target) && !stageAfter(s, r.From) {
			reruns = append(reruns, string(s))
		}
	}
	if len(reruns) > 0 {
		q += " — " + strings.Join(reruns, " and ") + " run again after it"
	}
	return q
}

// RouteComments routes a card's open comments: to the earliest stage
// before stage that owns one of them, reached over the graph's rerun
// edges, or in place when none is owned earlier (or no edge leads back).
// diffOpen is the number of unresolved diff comments.
func RouteComments(ct domain.CardType, stage domain.Stage, doc spec.Doc, diffOpen int) CommentRoute {
	here := CommentRoute{From: stage, Target: stage}
	target := stage
	owners := map[string]domain.Stage{}
	var sections []string
	for _, t := range doc.UserOpenThreads() {
		owner, ok := SpecCommentOwner(ct, doc, t)
		if !ok || !stageAfter(stage, owner) {
			continue
		}
		sec := doc.SectionAt(spec.UnresolvedUserMarker(t).Line)
		if _, seen := owners[sec]; !seen {
			sections = append(sections, sec)
		}
		owners[sec] = owner
		if stageAfter(target, owner) {
			target = owner
		}
	}
	if diffOpen > 0 && stageAfter(target, domain.StageImplement) {
		target = domain.StageImplement
	}
	if target == stage {
		return here
	}
	path, ok := rerunPath(stage, target)
	if !ok {
		return here
	}
	// the diff comments pulled the card back only if they are what it went
	// back for; a card sent to plan meets them again at implement
	out := CommentRoute{From: stage, Target: target, Path: path, Diff: diffOpen > 0 && target == domain.StageImplement}
	for _, sec := range sections {
		if owners[sec] == target {
			out.Sections = append(out.Sections, sec)
		}
	}
	return out
}

// rerunPath walks the graph's rerun edges from `from` back to `to`,
// returning the stages in the order they are entered — the same walk
// reentry's rewinds take, for the same reason: the graph declares one-hop
// edges, and history should record every one of them.
func rerunPath(from, to domain.Stage) ([]domain.Stage, bool) {
	var path []domain.Stage
	for cur := from; len(path) < len(domain.Stages); {
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
