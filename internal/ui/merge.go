package ui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/worktree"
)

// landConflictMsg reports a landing git refused on conflicts with the
// base. Update records the files (Shell.landConflicts) so the card's
// decision leads with the rebase that resolves them instead of the
// landing that just failed, and shows the notice.
type landConflictMsg struct {
	id     domain.FeatureID
	files  []string
	notice noticeMsg
}

// mergeReadyMsg carries a squash merge that passed its preconditions and
// awaits the user's commit message, or the guard error that stops it.
// thenDone marks a merge of a card that is AT verify: landing it also
// moves the feature to Done.
//
// It used to mean "launched from the verify→done gate", which is how the
// board's `m` key came to pass false unconditionally and land a branch on
// main while leaving its card at verify forever — `gummi status` reporting
// `Stage: verify`, the board filing it under REVIEW, and the status bar
// counting work that is already on main as "in review". The two landing
// keys reach the same place, so what decides the transition is the card's
// own stage, not which key was pressed. It stays false for a card that is
// not at verify: landing an earlier stage's branch by hand is not a
// judgment that verify happened, and jumping such a card to done would
// skip the quality floor outright.
type mergeReadyMsg struct {
	f        domain.Feature
	thenDone bool
	// warn carries a non-blocking pre-land caution (agent attribution
	// found in branch commit messages); the merge still proceeds.
	warn string
	err  error
	// webErr is err as the web face says it, when err names a command the
	// page has a control for instead. Empty means err reads the same.
	webErr string
}

// landingRefusal is why f may not land right now, or "" when it may. The
// floors are the workflow's (AGENTS.md; domain.Feature.MayLand): a card
// lands from verify — verified, or overruled there by a person, which is
// an answer that stop offers — and never from before it, whatever key or
// request asks. A freeform card lands on a person's read of its diff, so
// not while its agent is still writing it, nor over the person's own open
// comments on it. A handed-off card may still be landed after all.
//
// Every landing path reads this one floor — the verify decision's own
// "land on main" and "land anyway", the menu's land and squash-to-main,
// the TUI's m and the web's actions alike — so none of them lands on less
// than the others. The overrule is the verify stop's: "land anyway" is on
// offer once a verify pass has finished and failed (or could not run), and
// a landing that reaches here on such a card is that overrule, whichever
// way it was asked for. A card that has not finished a verify pass at all
// has nothing to overrule, and does not land.
func (m *Shell) landingRefusal(f domain.Feature) string {
	r, ok := m.rowByID(f.ID)
	return m.landingRefusalIn(f, r, ok, nil)
}

// landingRefusalIn is landingRefusal for a caller that already holds the
// card's board row (ok false when the board has none) and, optionally,
// the answer set's input built from it: nextInputFor reads the floor
// this way, so the menu offers a landing exactly when this lets one
// through (nextInput.landRefused). in nil builds it from r on demand.
func (m *Shell) landingRefusalIn(f domain.Feature, r featureRow, ok bool, in *nextInput) string {
	if f.HandedOff() {
		if err := f.MayLandAfterAll(); err != nil {
			return string(f.ID) + " is " + err.Error()
		}
	}
	if f.IsFreeform() {
		if !ok {
			return ""
		}
		if f.MainCheckout {
			return string(f.ID) + " runs in the main checkout — there is no branch to land; commit your work there yourself, then hand the card off"
		}
		if m.freeformTurnBusy(r) {
			return string(f.ID) + ": a turn is in flight — stop it, or let it finish, before landing"
		}
		if n := r.OpenDiffComments; n > 0 {
			return fmt.Sprintf("%s: %d open diff comment%s — resolve %s or send %s back before landing", f.ID, n, plural(n), them(n), them(n))
		}
		return ""
	}
	switch {
	case f.HandedOff():
		return ""
	case f.Stage != domain.StageVerify:
		return string(f.ID) + " is at " + string(f.Stage) + " — it lands from verify, once the branch has been verified"
	case f.MayLand() == nil:
		if ok {
			return staleRefusal(r)
		}
		return ""
	}
	if ok {
		if !r.F.VerifiedAt.IsZero() {
			// the row read the stamp the copy passed in predates
			return staleRefusal(r)
		}
		if in == nil {
			built := m.nextInputFor(r)
			in = &built
		}
		if slices.ContainsFunc(stageActions(*in), func(a nextAction) bool { return a.id == "advance" }) {
			// the verify stop offers the landing: a pass the stamp has
			// not reached yet, or a failure a person may overrule
			return ""
		}
	}
	return string(f.ID) + " has not finished a verify pass — run verify first; a failed verify is overruled from its own answer (land anyway)"
}

// staleRefusal is the landing floor's answer for a verified card whose
// branch moved past the revision its verify passed on, read off the row
// (featureRow.Head), or "" when the row says it has not. The landing's
// own run checks the live tip again (verifiedTipRefusal), so a row a beat
// stale costs a refusal one step later, never a landing.
func staleRefusal(r featureRow) string {
	if !r.F.VerifyStale(r.Head) {
		return ""
	}
	return staleSentence(r.F, r.Head)
}

// staleSentence says why a verified card does not land on head.
func staleSentence(f domain.Feature, head string) string {
	if f.VerifiedRev == "" {
		return string(f.ID) + ": verified before gummi recorded what it verified — re-verify before landing"
	}
	return fmt.Sprintf("%s: the branch moved since verify passed (verified %s, now %s) — re-verify before landing",
		f.ID, domain.ShortRev(f.VerifiedRev), domain.ShortRev(head))
}

// verifiedTipRefusal is the verified floor read live, right before a
// landing: the card as the store has it now and the tip the squash would
// take. "" when it may land. A handed-off card has already ended and a
// freeform one has no verify; their floors are the ones landingRefusal
// reads.
func (m *Shell) verifiedTipRefusal(ctx context.Context, f domain.Feature) string {
	if f.IsFreeform() || f.HandedOff() || f.IsGoal() || m.store == nil || m.wt == nil {
		return ""
	}
	cur, err := m.store.GetFeature(ctx, f.ID)
	if err != nil {
		return sanitize(err.Error())
	}
	if cur.VerifiedAt.IsZero() {
		// the overrule: a failed verify a person chose to land anyway
		// (landingRefusal let it through); there is no pass to be stale
		return ""
	}
	head, err := m.wt.Head(ctx, &cur)
	if err != nil {
		return sanitize(err.Error())
	}
	if err := cur.MayLandAt(head); err != nil {
		return staleSentence(cur, head)
	}
	return ""
}

// them is "it" or "them" for a count.
func them(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// prepareMerge checks the merge preconditions off the render loop. On
// success it opens the commit-message dialog, which drafts a suggested
// landing message from the spec and the branch — the user still approves
// and can edit it; nothing lands without an explicit ctrl+s. Anything
// still uncommitted in the worktree (including untracked files — new
// source files are agent work like any other) is committed as a final
// checkpoint first: gummi owns the branch's commits, and only committed
// work merges.
func (m *Shell) prepareMerge(f domain.Feature, thenDone bool) tea.Cmd {
	if why := m.landingRefusal(f); why != "" {
		return func() tea.Msg { return mergeReadyMsg{f: f, err: errors.New(why)} }
	}
	return func() tea.Msg {
		ctx := context.Background()
		// A card lands either via its linked PR or locally, never both.
		//
		// This refusal is the whole of what `g` could do on a linked card
		// until hand-off existed: the row said "merge the PR" and the key
		// said no, with an unlink as the only move it named. Both real
		// moves are named now — finish the landing on GitHub and pull, or
		// end the card here and let the PR carry it — because a reader who
		// opened a PR is usually done with gummi, not stuck.
		if !f.PullRequest.Empty() {
			return mergeReadyMsg{
				f: f,
				err: fmt.Errorf("%s is linked to %s#%d (%s) — merge it there and pull %s, or hand the card off to close it and let the PR carry it (`gummi pr unlink %s` to land it locally instead)",
					f.ID, f.PullRequest.Repo, f.PullRequest.Number, f.PullRequest.URL, m.baseBranch(f), f.ID),
				webErr: fmt.Sprintf("%s is linked to %s#%d (%s) — merge it there and pull %s, or hand the card off to close it and let the PR carry it (“unlink PR” in the card's menu lands it locally instead)",
					f.ID, f.PullRequest.Repo, f.PullRequest.Number, f.PullRequest.URL, m.baseBranch(f)),
			}
		}
		// A stacked card's branch contains the commits of every card
		// below it, so landing it early would land their work too —
		// under this card's message and without their review. This is
		// the ONE ordering a stack imposes, and git imposes it, not
		// gummi: working on the cards above is never held up.
		if eng, release := m.stackEngine(); eng != nil {
			defer release()
			if blocker, blocked := eng.StackLandBlocker(ctx, &f); blocked {
				return mergeReadyMsg{f: f, err: fmt.Errorf("%s sits on %s in its stack — %s has to land first, or its commits would ride in under this card",
					f.ID, blocker, blocker)}
			}
		}
		if f.IsFreeform() {
			// A freeform card may land as a merge commit, which keeps every
			// commit on the branch in main's history: a canned checkpoint
			// subject would be there for good. Its commits are the person's
			// own, so loose work is theirs to commit with a message.
			if dirty, err := m.wt.Dirty(ctx, &f); err != nil {
				return mergeReadyMsg{f: f, err: err}
			} else if dirty {
				return mergeReadyMsg{f: f, err: errors.New(looseWorkRefusal(f))}
			}
		} else if _, err := m.wt.CommitAll(ctx, &f, string(f.ID)+": final checkpoint"); err != nil {
			return mergeReadyMsg{f: f, err: err}
		}
		// what lands is the tip after that checkpoint, which is itself new
		// work when it committed anything: the verified floor reads it
		// (domain.Feature.MayLandAt) before the message is ever drafted
		if why := m.verifiedTipRefusal(ctx, f); why != "" {
			return mergeReadyMsg{f: f, err: errors.New(why)}
		}
		if dirty, err := m.wt.MainTrackedDirty(ctx, &f); err != nil {
			return mergeReadyMsg{f: f, err: err}
		} else if dirty {
			return mergeReadyMsg{f: f, err: errors.New(m.baseBranch(f) + " checkout has uncommitted changes — commit or stash them before merging")}
		}
		// stale-row safety: the board flag may predate an outside merge
		if landed, err := m.wt.Landed(ctx, &f); err != nil {
			return mergeReadyMsg{f: f, err: err}
		} else if landed {
			return mergeReadyMsg{f: f, err: errors.New(string(f.ID) + " already landed on " + m.baseBranch(f) + " — " + cleanUpNudge)}
		}
		// pre-land provenance scan: warn (never block) when branch commits
		// carry agent attribution — the squash discards their messages, but
		// the user should know before landing, not after
		var warn string
		if leaks, err := m.wt.ProvenanceWarnings(ctx, &f); err == nil && len(leaks) > 0 {
			warn = string(f.ID) + ": branch commits carry agent attribution — " + strings.Join(leaks, ", ")
			if f.Offers(domain.LandMerge) {
				// the method is chosen in the dialog this warning sits beside
				warn += " (a squash discards those messages; a merge commit keeps them)"
			} else {
				warn += " (a squash discards those messages)"
			}
		}
		return mergeReadyMsg{f: f, thenDone: thenDone, warn: warn}
	}
}

// landFeature lands the branch on main by method — one squash commit, or a
// merge commit that keeps the branch's commits — carrying the user-approved
// message. Landed is re-checked at run time so a stale
// board row (or a dialog left open across an outside merge) can't land
// the work twice. With thenDone set (the card was at verify) a landed
// merge also moves the feature to Done — the user's "this is done"
// decision and the landing are one action.
//
// Every success return clears the card's needs-attention entry
// (noticeMsg.clearInbox). That removal used to sit in the gate path's own
// key handler, which is why the `m` key never did it: the branch went to
// main and the inbox went on asking the reader to "review & land on main"
// work that was already landed. Clearing it here, on the outcome rather
// than on the keypress, is also what keeps the decision open when the
// merge is refused or conflicts — the thing still has not been attended
// to — and leaves no second place for a future caller to forget.
func (m *Shell) landFeature(f domain.Feature, message string, method domain.LandMethod, thenDone bool) tea.Cmd {
	actor := m.humanActor()
	return m.cardLocked(f.ID, func() tea.Msg {
		ctx := context.Background()
		if landed, err := m.wt.Landed(ctx, &f); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		} else if landed {
			return noticeMsg{text: string(f.ID) + " already landed on " + m.baseBranch(f) + " — " + cleanUpNudge, isErr: true}
		}
		// read again here, not only when the dialog opened: a commit made
		// while the message was being written is work no verify has seen
		if why := m.verifiedTipRefusal(ctx, f); why != "" {
			return noticeMsg{text: why, isErr: true, id: f.ID}
		}
		if _, err := m.wt.Land(ctx, &f, message, method); err != nil {
			var ce *worktree.MergeConflictError
			if errors.As(err, &ce) {
				// ce carries git-derived file names; sanitize like every
				// other notice before it reaches the terminal.
				//
				// The decision then leads with the rebase that resolves
				// them (landConflictMsg), and the notice names the act, not
				// a key: the web face shows this sentence too.
				files := make([]string, 0, len(ce.Files))
				for _, f := range ce.Files {
					files = append(files, sanitize(f))
				}
				return landConflictMsg{id: f.ID, files: files, notice: noticeMsg{
					text:  sanitize(string(f.ID) + ": " + ce.Error() + " — rebase it onto " + m.baseBranch(f) + " to resolve them, then land again"),
					isErr: true, id: f.ID,
				}}
			}
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		// The branch name these three sentences report is the card's own
		// base, not the literal "main" they used to carry: on a `master`
		// repo the merge dialog said "… → master" and the notice it
		// produced one keypress later said "squash-merged into main"
		// (round 3 §5.2).
		base := m.baseBranch(f)
		// Landing after a hand-off retracts it. The two endings are
		// mutually exclusive — the branch is on the base now — and the
		// stamp is what every surface reads to say how the card ended, so
		// leaving it would badge a landed card as handed off forever.
		if f.HandedOff() {
			if err := m.store.ClearHandedOffAt(ctx, f.ID); err != nil {
				return noticeMsg{text: sanitize(string(f.ID) + " " + landedAs(method, base) + ", but clearing its hand-off mark failed: " + err.Error()), isErr: true, reload: true, clearInbox: f.ID}
			}
		}
		// a handed-off card is already closed (done): landing it after all
		// retracts the hand-off above and leaves it where it stands
		if thenDone && f.Stage != domain.StageDone {
			if err := m.closeLandedCard(ctx, f, actor); err != nil {
				// the branch IS on the base; the card just did not move. That
				// is still the state the inbox item was asking about, so it is
				// cleared here too — leaving it up would keep inviting a
				// second landing of work already landed.
				return noticeMsg{text: sanitize(string(f.ID) + " " + landedAs(method, base) + ", but moving to done failed: " + err.Error()), isErr: true, reload: true, clearInbox: f.ID}
			}
			m.dropSession(f.ID)
			return noticeMsg{text: string(f.ID) + " " + landedAs(method, base) + " → done — " + cleanUpNudge, reload: true, clearInbox: f.ID}
		}
		return noticeMsg{text: string(f.ID) + " " + landedAs(method, base) + " — " + cleanUpNudge, reload: true, clearInbox: f.ID}
	})
}

// landedAs names how a landing reached base, for a notice: a squash is one
// commit, a merge keeps the branch's commits and says so.
func landedAs(method domain.LandMethod, base string) string {
	if method == domain.LandMerge {
		return "merged into " + base + " (keeping its commits)"
	}
	return "squash-merged into " + base
}

// closeLandedCard moves a just-landed card to done. A card in the workflow
// crosses its verify→done edge; a freeform card has no edge to cross (its
// stage has none at all, DESIGN §19), so it closes through the store method
// that exists for exactly that and refuses every other kind.
func (m *Shell) closeLandedCard(ctx context.Context, f domain.Feature, actor string) error {
	if f.IsFreeform() {
		_, err := m.store.CloseFreeform(ctx, f.ID, actor)
		return err
	}
	_, err := m.store.Transition(ctx, f.ID, domain.StageDone, actor)
	return err
}

// recordCommitDraftFail persists a squash-merge scribe pass's outcome on
// the feature (the failure reason, or "" cleared on a successful draft)
// and, once written, reflects it on the row's dashboard in place. It runs
// in a command because the store write can block on the single sqlite
// connection (SetMaxOpenConns(1)); the row reflection needs no board
// reload — CommitDraftFail is metadata for the feature's own already-open
// dashboard, with no git-state change for the board list to re-walk.
func (m *Shell) recordCommitDraftFail(id domain.FeatureID, reason string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.store.SetCommitDraftFail(ctx, id, reason); err != nil {
			return noticeMsg{text: sanitize("recording commit-draft outcome: " + err.Error()), isErr: true}
		}
		return commitDraftPersistedMsg{id: id, reason: reason}
	}
}

// predraftLandingMessage composes the card's landing commit message at
// the verify gate, before anyone asks for it, and stores it against the
// branch tip it describes (engine.PredraftCommitMessage). The merge
// dialog opens on it later instead of on a live pass.
//
// It is a command because it runs a model pass — the longest-running one
// on this path, bounded at two minutes — and nothing on screen waits for
// it: the gate is already raised, the inbox already says the card is
// ready to land, and the reader is free to go do something else. A card
// whose pre-draft fails is exactly a card that drafts at the keypress,
// which is where every landing started before this existed, so the
// failure is recorded durably (the engine writes CommitDraftFail) and
// reflected on the row rather than raised as a notice about nothing.
func (m *Shell) predraftLandingMessage(id domain.FeatureID, verifyNote string) tea.Cmd {
	if m.engine == nil || m.store == nil {
		return nil
	}
	return func() tea.Msg {
		ctx := context.Background()
		f, err := m.store.GetFeature(ctx, id)
		if err != nil {
			return nil
		}
		// an excluded card (a goal's own, a goal's card, a linked one)
		// leaves even the durable draft-failure note alone: nothing was
		// attempted, so there is nothing to report about it.
		if !engine.PredraftEligible(f) {
			return nil
		}
		if _, err := m.engine.PredraftCommitMessage(ctx, f, verifyNote); err != nil {
			return commitDraftPersistedMsg{id: id, reason: err.Error()}
		}
		return commitDraftPersistedMsg{id: id, reason: ""}
	}
}

// commitDraftMsg carries a best-effort draft landing message for the open
// commit-message dialog. gen tags the pass that produced it: only the
// latest generation is applied, so a late reply from a re-draft (ctrl+r)
// or a dialog closed by esc is dropped rather than clobbering.
type commitDraftMsg struct {
	f     domain.FeatureID
	gen   int
	draft string
	// cancelled marks a pass stopped on purpose (esc, a merge method, a
	// re-draft): it says nothing about the draft backend, so it is not
	// recorded as a failure.
	cancelled bool
	// reason is a non-empty explanation when the draft pass failed (empty
	// on success); guard marks a deliberate guard rejection so the dialog
	// renders it as a warning rather than a config fault.
	reason string
	guard  bool
}

// commitDraftPersistedMsg reports that a scribe pass's outcome was
// durably recorded on the feature (the failure reason, or "" cleared on a
// successful draft). The shell reflects it on the feature's own dashboard
// row in place — it is row metadata only, so no board reload is needed.
type commitDraftPersistedMsg struct {
	id     domain.FeatureID
	reason string
}

// mergeThenDoneMsg asks the shell to run the merge flow as the
// verify→done gate: collect the commit message from the user,
// squash-merge, and only then move the feature to Done.
type mergeThenDoneMsg struct {
	f domain.Feature
}

// looseWorkRefusal is why a freeform card with uncommitted work does not
// land yet, and the two ways to make it.
func looseWorkRefusal(f domain.Feature) string {
	return string(f.ID) + " has uncommitted work in its worktree — commit it with a message of your own first (the commit action, or `gummi commit " + string(f.ID) + " -m <message>`), or discard it"
}
