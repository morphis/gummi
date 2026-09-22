package ui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/worktree"
)

// Hand-off is the third ending a verified card has, beside gummi's own
// squash merge and a landing on GitHub: the card closes and its branch
// stays exactly where it is, to be pushed, PR'd, cherry-picked or simply
// kept. Until it existed the only way to say "I'll take it from here" was
// `D`, which destroys the branch — so the honest answer cost the work,
// and the usual outcome was a finished card parked at verify forever,
// counted by the status bar as work still in review.
//
// The flow mirrors the merge's deliberately: a preflight command off the
// render loop (preconditions plus the one thing the reader cannot see for
// themselves — which other cards this unblocks), then a confirm naming
// what changes, then the act under the card's lock.

// handOffReadyMsg carries a hand-off that passed its preconditions and
// awaits the user's confirmation, or the guard error that stops it.
// dependents are the cards that depend on this one and will become free
// to start coding from a base branch that does not contain this work —
// the consequence the confirm has to say out loud, because nothing else
// on the board will. drift is set when the base no longer carries the
// commit this branch forked from: not a refusal (the branch is leaving
// as it is), but the reader is about to push it somewhere and should
// hear that the base moved before they do.
type handOffReadyMsg struct {
	f          domain.Feature
	dependents []domain.FeatureID
	drift      *worktree.ForkDriftError
	err        error
}

// prepareHandOff checks the hand-off preconditions off the render loop
// and collects the dependents the confirm names. It mutates nothing: the
// checkpoint commit, the stamp and the transition all happen after the
// user confirms (engine.HandOff).
func (m *Shell) prepareHandOff(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// A landed branch has already left by the other door, and a card
		// whose work is on main is not something anyone can take away.
		//
		// Fork drift is not a stop. Landed refuses to guess when the base
		// was rewritten past the recorded fork, which is right for a
		// landing; a hand-off only needs "not already on the base", and a
		// drifted branch is not. The drift rides along to the confirm
		// instead, because the remedy the error would print — rebase, or
		// delete — is the pair of things this verb exists to avoid.
		landed, err := m.wt.Landed(ctx, &f)
		var drift *worktree.ForkDriftError
		if errors.As(err, &drift) {
			landed, err = false, nil
		}
		if err != nil {
			return handOffReadyMsg{err: err}
		} else if landed {
			return handOffReadyMsg{err: errors.New(string(f.ID) + " already landed on " + m.baseBranch(f) + " — " + cleanUpNudge)}
		}
		deps, err := m.store.ListDependents(ctx, f.ID)
		if err != nil {
			return handOffReadyMsg{err: err}
		}
		return handOffReadyMsg{f: f, dependents: deps, drift: drift}
	}
}

// handOffDetail is the confirm's body: what changes, what does not, and
// the consequences that are invisible from the board — the dependents
// this frees, and a base that moved out from under the branch.
//
// The branch and worktree lines are the point of the verb — a reader is
// choosing this precisely because they want those two things kept — so
// they are stated rather than implied by the absence of a warning.
func handOffDetail(f domain.Feature, base string, dependents []domain.FeatureID, drift *worktree.ForkDriftError) string {
	// leading blank line: confirmDialog sets its question and detail on
	// consecutive lines, which is right for the one-liners every other
	// caller passes and runs a three-row table straight into the title.
	var b strings.Builder
	b.WriteString("\nthe card moves to done and nothing lands.\n\n")
	b.WriteString("  " + pad("branch") + f.BranchName() + " — kept\n")
	b.WriteString("  " + pad("worktree") + f.WorktreePath() + " — kept\n")
	b.WriteString("  " + pad(base) + "unchanged")
	if len(dependents) > 0 {
		names := make([]string, 0, len(dependents))
		for _, d := range dependents {
			names = append(names, string(d))
		}
		verb := " depends on this and will start from "
		if len(names) > 1 {
			verb = " depend on this and will start from "
		}
		b.WriteString("\n\n" + strings.Join(names, ", ") + verb + base +
			", which does not have this work.")
	}
	if drift != nil {
		// The branch leaves as it is; nothing here rebases it. What the
		// reader needs is the fact, in one line, before they push a
		// branch that no longer applies cleanly to where it came from.
		b.WriteString("\n\n" + base + " moved since this branch forked (" + short(drift.Recorded) +
			" is no longer in its history) — the branch is kept as is, so rebase it yourself if it should sit on " + base + ".")
	}
	return b.String()
}

// short is the seven-character form of a commit sha, for a sentence.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// pad left-aligns one of the confirm table's labels into a fixed gutter,
// so the three rows read as a table rather than as three sentences. The
// widest fixed label is "worktree"; the base branch's name is the one
// that varies, and a name wider than the gutter simply takes the space
// it needs plus a separating blank.
func pad(label string) string {
	const gutter = 11
	return label + strings.Repeat(" ", max(gutter-len(label), 1))
}

// handOffFeature ends the card without landing it, under the card's lock.
//
// engine.HandOff owns the three steps (final checkpoint commit, the
// hand-off stamp, then the ordinary verify→done crossing) and the result
// goes through advanceOutcome, the same mapping the g key's crossing
// uses: a gate blocker is reported here exactly as it is there, because
// waiving the landing never waived the floor. Only the success sentence
// differs, and it differs because the outcome does — nothing reached the
// base branch, and a reader who is about to go push something needs the
// branch name in front of them.
func (m *Shell) handOffFeature(f domain.Feature) tea.Cmd {
	return m.cardLocked(f.ID, func() tea.Msg {
		return m.withEngine(func(eng *engine.Engine) tea.Msg {
			res, err := eng.HandOff(context.Background(), f.ID, "user")
			if err != nil || res.Status != engine.StatusAdvanced {
				return m.advanceOutcome(f.ID, "user", res, err)
			}
			m.dropSession(f.ID)
			return noticeMsg{
				text:       string(f.ID) + " → done · " + f.BranchName() + " is yours — nothing landed on " + m.baseBranch(f),
				reload:     true,
				clearInbox: f.ID,
			}
		})
	})
}
