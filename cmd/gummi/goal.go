package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/driver"
	"github.com/morphis/gummi/internal/state"
)

// runGoal implements `gummi goal [flags] "<objective>"`: it mints one goal
// and drives it headlessly — the plan conversation first (a question per
// turn unless --autonomous), then its cards on the goal branch, its review
// and its verify — to the point it is ready for you. Land it with `gummi
// merge`, send it back with `gummi resume --request-changes`, or hand it
// off. The envelope is the goal's whole budget: its cards, its lead and
// its own review and verify all spend inside it.
func runGoal(args []string) error {
	fs := flag.NewFlagSet("goal", flag.ContinueOnError)
	gv := registerGoalFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `usage: gummi goal [flags] "<objective>"`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("goal needs exactly one objective argument")
	}
	objective := fs.Arg(0)
	if err := driver.ValidateUntil(domain.Stage(*gv.until)); err != nil {
		return err
	}
	doc, err := readAcceptance(*gv.planFile)
	if err != nil {
		return fmt.Errorf("%s", strings.NewReplacer("--acceptance", "--plan-file").Replace(err.Error()))
	}
	opts, err := driverOptions(*gv.envelope, *gv.profile, *gv.gate, *gv.timeout, *gv.autonomous, *gv.verbose, *gv.ref, "", *gv.until, *gv.repo, *gv.base)
	if err != nil {
		return err
	}
	opts.GoalDoc = doc

	return withRunEngine(func(ctx context.Context, d *driver.Driver, _ *state.Store, ws state.Workspace) (driver.Outcome, error) {
		f, err := d.Create(ctx, domain.CardType{Kind: domain.KindGoal}, objective)
		if err != nil {
			return driver.Outcome{}, err
		}
		release, err := state.AcquireLock(ws.CardLockFile(f.ID))
		if err != nil {
			return driver.Outcome{}, err
		}
		defer release()
		clearPID, err := trackPID(ws, f.ID)
		if err != nil {
			return driver.Outcome{}, err
		}
		defer clearPID()
		return d.Drive(ctx, f)
	}, opts)
}

// goalFlagValues holds the flag pointers `gummi goal` binds.
// registerGoalFlags is the single registration site, so the skill's
// grammar generator enumerates the same set (see runFlagValues).
type goalFlagValues struct {
	envelope            *int
	profile, gate, ref  *string
	repo, until, base   *string
	planFile            *string
	autonomous, verbose *bool
	timeout             *time.Duration
}

// registerGoalFlags binds `gummi goal`'s flags onto fs and returns their
// pointers (definition only; parsing stays in runGoal).
func registerGoalFlags(fs *flag.FlagSet) *goalFlagValues {
	return &goalFlagValues{
		envelope:   fs.Int("envelope", 0, "the goal's whole budget in credits — its cards, its lead and its own review all spend inside it (required; falls back to GUMMI_ENVELOPE)"),
		profile:    fs.String("profile", "", "profile mapping roles to models, the lead included (default: first configured)"),
		gate:       fs.String("gate-approval", driver.GateAttended, "who approves the goal's plan: attended|autopilot (past its plan a goal always runs itself)"),
		timeout:    fs.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout for the goal and each of its cards (0 disables)"),
		autonomous: fs.Bool("autonomous", false, "let the architect take its recommended answer instead of asking during the plan conversation"),
		verbose:    fs.Bool("verbose", false, "add per-tool-call activity lines to the stream"),
		ref:        fs.String("ref", "", "external correlation id, echoed in the stream and persisted for `status`/`resume` lookup"),
		repo:       fs.String("repo", "", "managed repository for the goal and all its cards (a configured `repos:` name; required when `repos:` is configured)"),
		base:       fs.String("base", "", "branch the goal branch forks from and lands on (default: whatever the repository has checked out)"),
		planFile:   fs.String("plan-file", "", "a complete goal doc to start the plan conversation from (a file path, or - for stdin)"),
		until:      fs.String("until", "", "stop cleanly before the goal's plan is approved (only \"plan\" is a valid stop)"),
	}
}
