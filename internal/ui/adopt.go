package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// Adopting is the answer a dropped card never had.
//
// A goal drops a card for reasons of its own — it got stuck twice, the
// goal is wrapping up, something it depended on was dropped — and until
// now that decision was final in one direction only. A card the goal had
// been HANDED went back to the board automatically. A card the goal had
// MADE was closed inside it, branch and commits intact and unreachable,
// and the only way to pick the work back up was to start a fresh card and
// find the branch by hand.
//
// The flow mirrors hand-off's, deliberately: a confirm that names what
// changes, then the act under the card's lock.

// adoptDetail is the confirm's body. A reader taking a card back wants to
// know exactly two things — where it lands and what it still has — so
// both are stated rather than implied.
func adoptDetail(f domain.Feature, back domain.Stage) string {
	return "\nthe card comes back to the board, with its work kept.\n\n" +
		"  " + pad("stage") + string(back) + " — where the drop closed it\n" +
		"  " + pad("branch") + f.BranchName() + " — kept, and moved onto main\n" +
		"  " + pad("goal") + string(f.GoalID) + " — it stops counting toward it"
}

// adoptBackStage is the stage the card will return to, as the confirm
// names it before anything is written. It reads the same closing
// transition the store reads, so the sentence and the act cannot
// disagree; with no closing edge on record both fall back to verify.
func adoptBackStage(hist []stageEdge) domain.Stage {
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].to == domain.StageDone && hist[i].from != domain.StageDone {
			return hist[i].from
		}
	}
	return domain.StageVerify
}

// stageEdge is one transition reduced to what adoptBackStage reads.
type stageEdge struct{ from, to domain.Stage }

// openAdopt raises the confirm for taking a dropped card back.
func (m *Shell) openAdopt(r featureRow) tea.Cmd {
	if !r.F.GoalDropped() {
		m.notice = noticeMsg{text: string(r.F.ID) + " was not dropped — there is nothing to take back", isErr: true}
		return nil
	}
	edges := make([]stageEdge, 0, len(r.History))
	for _, t := range r.History {
		edges = append(edges, stageEdge{from: t.From, to: t.To})
	}
	f, back := r.F, adoptBackStage(edges)
	m.Overlay.Push(&confirmDialog{
		id:           "confirm-adopt",
		cancelLabel:  "Leave it",
		confirmLabel: "Adopt",
		question:     "adopt " + string(f.ID) + "?",
		detail:       adoptDetail(f, back),
		onConfirm:    func() tea.Cmd { return m.adoptCard(f) },
	})
	return nil
}

// adoptCard performs the adoption under the card's lock, the same way
// every other verb that touches a branch does.
func (m *Shell) adoptCard(f domain.Feature) tea.Cmd {
	return m.cardLocked(f.ID, func() tea.Msg {
		got, err := m.engine.Adopt(context.Background(), f.ID, "user")
		if err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		return noticeMsg{
			text:   string(f.ID) + " adopted — back on the board at " + string(got.Stage) + ", its work kept",
			reload: true,
		}
	})
}
