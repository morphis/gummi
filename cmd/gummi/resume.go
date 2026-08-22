package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

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
func runResume(args []string) error {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	rv := registerResumeFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi resume <id|ref> [--answer <text> | --approve | --request-changes <note> | --bounce [--note <text>]]")
		fs.PrintDefaults()
	}
	// resume is id-first (`resume FD-042 --answer no`) and accepts an
	// external ref in the id slot (resolved against the store below, D11).
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}

	in, err := resumeInput(*rv.answer, *rv.approve, *rv.requestChanges, *rv.bounce, *rv.note,
		isSet(fs, "answer"), isSet(fs, "request-changes"), isSet(fs, "note"))
	if err != nil {
		return err
	}
	if *rv.gate != driver.GateAuto && *rv.gate != driver.GateCaller {
		return fmt.Errorf("--gate-approval must be %q or %q, got %q", driver.GateAuto, driver.GateCaller, *rv.gate)
	}
	if *rv.envelope < 0 {
		return fmt.Errorf("--envelope must be a positive credit count, got %d", *rv.envelope)
	}
	// resume mostly reuses the feature's existing envelope; --envelope raises
	// it (the only way to clear an exhausted stage headlessly — driver.Resume
	// treats it as a floor and never lowers). The rest of the driving options
	// mirror run so the continued tail behaves the same.
	opts := driver.Options{
		Envelope:     *rv.envelope,
		GateApproval: *rv.gate, GateApprovalSet: isSet(fs, "gate-approval"),
		StageTimeout: *rv.timeout,
		Autonomous:   *rv.autonomous, Verbose: *rv.verbose, Ref: *rv.ref,
		Until: domain.Stage(*rv.until),
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
		release, err := state.AcquireLock(ws.CardLockFile(f.ID))
		if err != nil {
			return driver.Outcome{}, err
		}
		defer release()
		return d.Resume(ctx, f.ID, in)
	}, opts)
}

// resumeFlagValues holds the flag pointers `gummi resume` binds.
// registerResumeFlags is the single registration site, so the skill's
// grammar generator can enumerate the same set (see runFlagValues).
type resumeFlagValues struct {
	answer, requestChanges, note *string
	gate, ref, until             *string
	approve, autonomous, bounce  *bool
	verbose                      *bool
	envelope                     *int
	timeout                      *time.Duration
}

// registerResumeFlags binds `gummi resume`'s flags onto fs and returns
// their pointers (definition only; parsing stays in runResume).
func registerResumeFlags(fs *flag.FlagSet) *resumeFlagValues {
	return &resumeFlagValues{
		answer:         fs.String("answer", "", "answer a delegated ask_user question"),
		envelope:       fs.Int("envelope", 0, "raise the credit envelope before resuming (required to clear an exhausted stage; never lowers it)"),
		approve:        fs.Bool("approve", false, "approve a caller design gate"),
		requestChanges: fs.String("request-changes", "", "send a caller design gate back with a note"),
		bounce:         fs.Bool("bounce", false, "rewind a verify/review-fail escalation to the work stage and continue (the TUI's `b` key)"),
		note:           fs.String("note", "", "addendum to the reborn implement/fix kickoff (used with --bounce)"),
		gate:           fs.String("gate-approval", driver.GateAuto, "who approves later design gates: auto|caller (inherits the run's mode when omitted; pass to change it)"),
		timeout:        fs.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)"),
		autonomous:     fs.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions"),
		verbose:        fs.Bool("verbose", false, "add per-tool-call activity lines to the stream"),
		ref:            fs.String("ref", "", "external correlation id, echoed in the stream"),
		until:          fs.String("until", "", "stop cleanly before crossing the gate that leaves this design stage (default: run to a verified branch)"),
	}
}

// resumeInput builds the ResumeInput from the mutually exclusive decision
// flags, refusing more than one. All unset means "re-run the parked
// stage". answerSet/changesSet/noteSet distinguish an explicitly-empty flag
// from an unset one, so `--answer ""` is still an (empty-answer) decision
// the driver can reject cleanly rather than silently re-running. --note
// only composes with --bounce; on its own it is a usage error, not a silent
// no-op.
func resumeInput(answer string, approve bool, requestChanges string, bounce bool, note string,
	answerSet, changesSet, noteSet bool,
) (driver.ResumeInput, error) {
	n := 0
	var in driver.ResumeInput
	if answerSet {
		n++
		a := answer
		in = driver.ResumeInput{Answer: &a}
	}
	if approve {
		n++
		in = driver.ResumeInput{Approve: true}
	}
	if changesSet {
		n++
		c := requestChanges
		in = driver.ResumeInput{RequestChanges: &c}
	}
	if bounce {
		n++
		nt := note
		in = driver.ResumeInput{Bounce: &nt}
	}
	if n > 1 {
		return driver.ResumeInput{}, fmt.Errorf("give at most one of --answer, --approve, --request-changes, --bounce")
	}
	if noteSet && !bounce {
		return driver.ResumeInput{}, fmt.Errorf("--note only applies with --bounce")
	}
	return in, nil
}

// isSet reports whether a flag was present on the command line (vs left at
// its zero default), so an explicit empty value is distinguishable.
func isSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
