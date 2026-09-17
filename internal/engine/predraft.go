package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
)

// The landing message is drafted before the landing, not during it.
//
// The pass itself (commitmsg.go) never moved: same zero-tool scribe turn,
// same guards, same human at the form. What moved is WHEN it runs. It
// used to run on the keypress — the reader pressed the landing key, the
// dialog opened on an empty box, and a measured ~60s later a message
// appeared in it. That is a minute of a person's attention spent on a
// pass that needed none of it, and the close-out ritual (`C`), which
// walks every verified card one confirm at a time, spends it once per
// card, serially, at the end of a session.
//
// The moment the same pass costs nobody anything is the one where the
// branch has just become final and the card has parked at its landing
// gate: verify passed, the inbox says "review & land on main", and the
// reader is somewhere else entirely. Everything the pass reads exists by
// then — the spec's digest is what plan wrote, the branch's commits and
// diffstat are what implement left, and the verify report is the thing
// that just happened — so a draft made there is made from strictly more
// than one made at the keypress.
//
// Two properties keep the earlier draft honest:
//
//   - It is stamped with the branch tip it was composed against
//     (CommitDraftSHA). A branch that moved since — a rebase, a
//     post-verify fix, the merge flow's own final checkpoint — describes
//     work the draft never saw, so the stored draft is ignored and the
//     live pass runs exactly as before. Staleness is a question about
//     the tree, and the tree answers it.
//   - It is still a draft. The dialog opens on it, the reader edits it,
//     and the arm-then-land guard still demands a second keypress for
//     text they never touched. Nothing lands that a human did not
//     approve, which is the same sentence as before this file existed.

// PredraftEligible reports whether a card is one whose landing message is
// worth composing ahead of time — which is to say: one a PERSON will land,
// later, at a moment nobody can predict.
//
// Everything excluded here is excluded because something else already
// answers the landing question sooner or differently:
//
//   - a goal lands as a merge commit gummi writes from its cards
//     (GoalMergeMessage), never as a scribe's prose;
//   - a goal's CARD is landed onto the goal branch by the goal itself,
//     seconds after this gate, with its own draft in-line — pre-drafting
//     it would buy nothing and risk paying for the same pass twice, in
//     parallel;
//   - a research card carries no branch to land;
//   - a card linked to a PR refuses a local squash merge outright, so a
//     landing message for it would never be read;
//   - a handed-off or goal-dropped card has already ended, without a
//     landing.
//
// It is exported because a caller that NARRATES the pass has to know
// before it starts: a stream line announcing a draft that was never going
// to happen is worse than no line, and the answer is a field read, not a
// pass.
func PredraftEligible(f domain.Feature) bool {
	switch {
	case f.Kind == domain.KindGoal, f.Kind == domain.KindResearch:
		return false
	case f.GoalID != "":
		return false
	case !f.PullRequest.Empty():
		return false
	case f.HandedOff(), f.GoalDropped():
		return false
	}
	return true
}

// PredraftCommitMessage composes the card's landing commit message ahead
// of the landing and stores it against the branch tip it describes. It is
// the same pass DraftCommitMessage runs, fired from the verify gate
// instead of from the merge dialog, plus the verify report the dialog
// could never have had.
//
// It is best-effort in the strongest sense: it is called from a place
// where no one is waiting for it, so every failure returns quietly and
// changes nothing except the durable reason (CommitDraftFail) the card
// already carries for the dialog's own failures. A card that gets no pre-draft is a card
// whose dialog drafts at the keypress — which is to say, the behaviour
// everything had before.
//
// A card that already holds a draft for the current tip is not re-drafted:
// re-reaching this gate (a restart replaying the completion, a re-verify
// that changed nothing) must not buy a second pass for an answer already
// on disk.
func (e *Engine) PredraftCommitMessage(ctx context.Context, f domain.Feature, verifyNote string) (string, error) {
	if !PredraftEligible(f) {
		return "", nil
	}
	if e.cfg.Store == nil {
		return "", errors.New("no store to record a pre-drafted landing message on")
	}
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return "", fmt.Errorf("pre-draft could not resolve the card's repository: %w", err)
	}
	// Nothing to describe: a branch with no commits of its own has no
	// landing to compose a message for, and asking the scribe about it
	// would spend a turn to be told so.
	if ahead, err := wt.BranchAhead(ctx, &f); err != nil || !ahead {
		return "", err
	}
	tip, err := wt.Head(ctx, &f)
	if err != nil {
		return "", fmt.Errorf("pre-draft could not resolve the branch tip: %w", err)
	}
	if cur, err := e.cfg.Store.GetFeature(ctx, f.ID); err == nil &&
		cur.CommitDraft != "" && cur.CommitDraftSHA == tip {
		return cur.CommitDraft, nil
	}
	draft, err := e.draftCommitMessage(ctx, f, verifyNote)
	if err != nil {
		// Recorded on the same field the dialog's own failures use, and
		// for the same reason: a pass that produced nothing must leave
		// behind why, so the reader who later lands to a live pass can
		// find out this was not the first attempt.
		_ = e.cfg.Store.SetCommitDraftFail(ctx, f.ID, err.Error())
		return "", err
	}
	if err := e.cfg.Store.SetCommitDraft(ctx, f.ID, draft, tip); err != nil {
		return draft, err
	}
	_ = e.cfg.Store.SetCommitDraftFail(ctx, f.ID, "")
	return draft, nil
}

// PendingCommitDraft returns the card's pre-drafted landing message when
// it still describes the branch as it stands, and "" otherwise — no
// backend, no pass, no error: a missing or stale draft is not a fault,
// it is a caller that has to draft for itself.
//
// The card is re-read from the store rather than taken from f. The
// caller's copy is usually a board row, and a board row is a snapshot
// that can predate the very pass that wrote the draft — which would make
// the pre-draft invisible on exactly the cards that have one.
func (e *Engine) PendingCommitDraft(ctx context.Context, f domain.Feature) string {
	if e.cfg.Store == nil || !PredraftEligible(f) {
		return ""
	}
	cur, err := e.cfg.Store.GetFeature(ctx, f.ID)
	if err != nil || cur.CommitDraft == "" || cur.CommitDraftSHA == "" {
		return ""
	}
	wt, err := e.mgr(ctx, &f)
	if err != nil {
		return ""
	}
	tip, err := wt.Head(ctx, &f)
	if err != nil || tip != cur.CommitDraftSHA {
		return ""
	}
	return cur.CommitDraft
}

// LandingMessage is the merge dialog's one seam for getting a message
// into its box: the pre-drafted one when the branch has not moved under
// it, the live pass otherwise.
//
// fresh is the Redraft button (and ctrl+r). It means what the reader
// means by pressing it — compose this again, now — so it never answers
// from the store: a reader who asks for another draft and is handed back
// the same stored one, instantly, has been told the button is broken.
func (e *Engine) LandingMessage(ctx context.Context, f domain.Feature, fresh bool) (string, error) {
	if !fresh {
		if draft := e.PendingCommitDraft(ctx, f); draft != "" {
			return draft, nil
		}
	}
	return e.DraftCommitMessage(ctx, f)
}
