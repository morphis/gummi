package engine

import (
	"context"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
)

// DocumentFloorCheck is the name the research document floor's result
// row carries in the thread ("check document floor: …"), so both faces
// draw it where they draw every other check gummi ran.
const DocumentFloorCheck = "document floor"

// A research card's verify is judged against its document, and the only
// part of that judgement gummi can make deterministically is the floor
// (internal/verifydoc) — the same floor that decides, at verify→done,
// whether the card may be marked done at all.
//
// It used to be checked only there. So a verify could pass on a document
// the floor then refused, and the person was offered "mark done" first,
// answered it, and was refused with three counts nobody had explained.
// Now the floor runs at both ends of the verify stage: its report opens
// the kickoff, so the verifier can repair what it names, and it runs
// again when the stage ends, where a failing floor overrules a pass.

// documentFloorPreamble is the research verify kickoff's opening block:
// the floor's report as the document stands, and what to do about it.
// Empty for any other card, and for a document that cannot be read (the
// finishing check still runs and says so).
func (e *Engine) documentFloorPreamble(s *Session) string {
	if s.Feature.Kind != domain.KindResearch || s.Feature.Stage != domain.StageVerify {
		return ""
	}
	f := s.Feature
	rep, err := e.documentReport(context.Background(), &f)
	if err != nil {
		return ""
	}
	l := spec.LayoutOf(&f)
	if rep.Pass() {
		return "gummi ran the research document's deterministic floor: it passes (" + rep.Summary(l) +
			"). Keep it passing — it runs again when this stage ends."
	}
	return "gummi ran the research document's deterministic floor and it FAILS (" + rep.Summary(l) +
		"). It runs again when this stage ends, and a failing floor fails verify whatever your verdict says.\n" +
		rep.Explain(l)
}

// gateDocumentVerdict re-runs the floor as a research verify finishes and
// records the result as a check row. A failing floor stamps a "fail"
// verdict floor — the document is what failed, not the environment — so
// the decision that follows is a failed verify carrying the report,
// rather than a pass whose default answer is then refused.
func (e *Engine) gateDocumentVerdict(s *Session) {
	if s == nil || s.Feature.Kind != domain.KindResearch || s.Feature.Stage != domain.StageVerify || s.Critique {
		return
	}
	f := s.Feature
	rep, err := e.documentReport(context.Background(), &f)
	if err != nil {
		s.appendToolDone("check "+DocumentFloorCheck+": NOT RUN ("+err.Error()+")", false, "")
		s.setVerdictFloor(FloorDocument, "blocked", "the document floor could not run: "+err.Error())
		return
	}
	l := spec.LayoutOf(&f)
	if rep.Pass() {
		s.appendToolDone("check "+DocumentFloorCheck+": pass", true, "")
		return
	}
	s.appendToolDone("check "+DocumentFloorCheck+": FAIL ("+rep.Summary(l)+")", false, rep.Explain(l))
	s.setVerdictFloor(FloorDocument, "fail", documentFloorReason+rep.Summary(l))
}

// documentFloorReason opens the verdict-floor reason a failing document
// floor stamps, so a reader of the snapshot can tell it from the others.
const documentFloorReason = "the document floor failed — "

// DocumentFloorFailed reports whether a finished research verify was
// failed by the document floor rather than by its verifier: the case a
// caller answers with the floor's own report (the done edge's
// StatusBlockedDocument) rather than as a verify that found the work
// wanting.
func DocumentFloorFailed(snap Snapshot) bool {
	return snap.Feature.Kind == domain.KindResearch && snap.VerdictFloor == "fail" &&
		strings.HasPrefix(snap.VerdictFloorReason, documentFloorReason)
}
