package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/driver"
	"github.com/morphis/gummi/internal/state"
)

// runResume implements `gummi resume <FD-id> [--answer <text> | --approve
// | --request-changes <note>]` (DESIGN §8.2): it rehydrates the parked
// feature, applies the caller's decision, and drives on — streaming the
// same NDJSON and exiting with a typed status. With no decision flag it
// simply re-runs the parked stage (after a timeout, an escalation, or an
// envelope top-up). The same two flags also resolve a done research
// card's decompose checkpoint (FD-081): --approve mints its pending
// proposals into FDs, --request-changes re-runs the decompose pass with
// the note attached — no new plumbing, the driver dispatches on the
// card's kind and stage.
func runResume(fl cliFlags, args []string) error {
	// resume is id-first (`resume FD-042 --answer no`) and accepts an
	// external ref in the id slot (resolved against the store below, D11).
	idArg, err := oneID("resume", args)
	if err != nil {
		return err
	}

	in, err := resumeInput(fl)
	if err != nil {
		return err
	}
	if in, err = goalResumeInput(fl, in); err != nil {
		return err
	}
	gate, err := gateApproval(fl.String("gate-approval"))
	if err != nil {
		return err
	}
	envelope, runs, minutes := fl.Budget("envelope"), fl.Int("runs"), fl.Int("minutes")
	if envelope < 0 {
		return fmt.Errorf("--envelope must be a positive dollar amount, got %s", domain.FormatDollars(float64(envelope)))
	}
	if runs < 0 || minutes < 0 {
		return fmt.Errorf("--runs and --minutes must be positive, got %d and %d", runs, minutes)
	}
	if err := driver.ValidateUntil(domain.Stage(fl.String("until"))); err != nil {
		return err
	}
	// resume mostly reuses the feature's existing envelope; --envelope raises
	// it (the only way to clear an exhausted stage headlessly — driver.Resume
	// treats it as a floor and never lowers). The rest of the driving options
	// mirror run so the continued tail behaves the same.
	opts := driver.Options{
		Envelope:      envelope,
		SubstrateRuns: runs, SubstrateMinutes: minutes,
		Retake:       fl.String("retake"),
		GateApproval: gate, GateApprovalSet: fl.Changed("gate-approval"),
		StageTimeout: fl.Duration("stage-timeout"),
		Autonomous:   fl.Bool("autonomous"), Verbose: fl.Bool("verbose"), Ref: fl.String("ref"),
		Until: domain.Stage(fl.String("until")),
	}

	// resolve the id/ref inside the closure, once the store is open; --until
	// is validated against the resolved feature's route in driver.Resume. The
	// resolved card's per-card lock guards a single card against double-drive
	// while independent cards resume concurrently.
	return withRunEngine(func(ctx context.Context, d *driver.Driver, store *state.Store, ws state.Workspace) (driver.Outcome, error) {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return driver.Outcome{}, err
		}
		// --say reads a line and reports what the card page would do with
		// it. It advances nothing, writes nothing and spawns no stage, so
		// it takes no card lock: taking one made the surface for
		// rehearsing an intervention unavailable on exactly the cards it
		// is for — an autopilot card mid-run answered "another gummi
		// process is already driving this card" for the hour it worked.
		// It also skips the pid/orphan bookkeeping, which belongs to a
		// process that is driving.
		if in.Say != nil {
			return d.Resume(ctx, f.ID, in)
		}
		release, err := state.AcquireLock(ws.CardLockFile(f.ID))
		if errors.Is(err, state.ErrLocked) && in.GoalDecision() {
			// --goal-note, --wrap-up and --reverse exist to reach a goal
			// WHILE it runs, and a running goal is exactly the card whose
			// lock is held. Each writes one row the conductor reads on its
			// next tick and drives nothing itself, so a busy lock is the
			// case they are for: hand the decision over and report it,
			// instead of refusing on the only card they apply to.
			in.Deliver = true
			return d.Resume(ctx, f.ID, in)
		}
		if err != nil {
			return driver.Outcome{}, err
		}
		defer release()
		state.ReapOrphanAgent(ws, f.ID)
		clearPID, err := trackPID(ws, f.ID)
		if err != nil {
			return driver.Outcome{}, err
		}
		defer clearPID()
		return d.Resume(ctx, f.ID, in)
	}, opts)
}

// goalResumeInput folds the goal-only resume flags into in. --goal-note and
// --wrap-up stand alone; --reverse may carry a --request-changes reason.
func goalResumeInput(fl cliFlags, in driver.ResumeInput) (driver.ResumeInput, error) {
	noteSet, reverseSet := fl.Changed("goal-note"), fl.Changed("reverse")
	wrapUp := fl.Bool("wrap-up")
	n := 0
	if noteSet {
		n++
	}
	if reverseSet {
		n++
	}
	if wrapUp {
		n++
	}
	if n == 0 {
		return in, nil
	}
	if n > 1 {
		return in, fmt.Errorf("give at most one of --goal-note, --reverse, --wrap-up")
	}
	others := in.Answer != nil || in.Approve || in.Bounce != nil || in.Say != nil || (in.RequestChanges != nil && !reverseSet)
	if others {
		return in, fmt.Errorf("--goal-note, --reverse and --wrap-up do not combine with another decision flag")
	}
	switch {
	case noteSet:
		note := fl.String("goal-note")
		in.Note = &note
	case reverseSet:
		ref := fl.String("reverse")
		in.Reverse = &ref
	case wrapUp:
		in.WrapUp = true
	}
	return in, nil
}

// resumeInput builds the ResumeInput from the mutually exclusive decision
// flags, refusing more than one. All unset means "re-run the parked
// stage". answerSet/changesSet/noteSet distinguish an explicitly-empty flag
// from an unset one, so `--answer ""` is still an (empty-answer) decision
// the driver can reject cleanly rather than silently re-running. --note
// only composes with --bounce; on its own it is a usage error, not a silent
// no-op.
func resumeInput(fl cliFlags) (driver.ResumeInput, error) {
	bounce, noteSet := fl.Bool("bounce"), fl.Changed("note")
	n := 0
	var in driver.ResumeInput
	if fl.Changed("say") {
		n++
		sy := fl.String("say")
		in = driver.ResumeInput{Say: &sy}
	}
	if fl.Changed("answer") {
		n++
		a := fl.String("answer")
		in = driver.ResumeInput{Answer: &a}
	}
	if fl.Bool("approve") {
		n++
		in = driver.ResumeInput{Approve: true}
	}
	if fl.Changed("request-changes") {
		n++
		c := fl.String("request-changes")
		in = driver.ResumeInput{RequestChanges: &c}
	}
	if bounce {
		n++
		nt := fl.String("note")
		in = driver.ResumeInput{Bounce: &nt}
	}
	if n > 1 {
		return driver.ResumeInput{}, fmt.Errorf("give at most one of --answer, --approve, --request-changes, --bounce, --say")
	}
	if noteSet && !bounce {
		return driver.ResumeInput{}, fmt.Errorf("--note only applies with --bounce")
	}
	return in, nil
}
