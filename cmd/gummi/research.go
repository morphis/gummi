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

// runResearch implements `gummi research [flags] "<brief>"`: it mints one
// RS card from a free-form brief and drives it headlessly through the
// decompose gate, streaming the same milestone + decision NDJSON as `run`.
// RS has no brainstorm/plan and no acceptance-seeded Verification plan, so
// --full and --acceptance are not on its flag surface; --until only ever
// accepts "shape", the sole pre-decompose stop on RS's route.
func runResearch(args []string) error {
	fs := flag.NewFlagSet("research", flag.ContinueOnError)
	rv := registerResearchFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `usage: gummi research [flags] "<brief>"`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("research needs exactly one brief argument")
	}
	brief := fs.Arg(0)

	// validate --until against RS's route before any work begins, so a bad
	// stop target fails as a plain usage error, not mid-run.
	if err := driver.ValidateUntil(domain.Stage(*rv.until), domain.KindResearch, domain.SkipFlags{}); err != nil {
		return err
	}
	opts, err := driverOptions(*rv.envelope, *rv.profile, false, *rv.gate, *rv.timeout, *rv.autonomous, *rv.verbose, *rv.ref, "", "", *rv.repo)
	if err != nil {
		return err
	}
	opts.Until = domain.Stage(*rv.until)

	return withRunEngine(func(ctx context.Context, d *driver.Driver, _ *state.Store, ws state.Workspace) (driver.Outcome, error) {
		// mint the card first, then take its per-card lock for the drive so
		// this run is the sole governor of the card it just created (two
		// runs mint disjoint cards and so never contend on each other's lock).
		f, err := d.Create(ctx, domain.KindResearch, brief)
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

// researchFlagValues holds the flag pointers `gummi research` binds.
// registerResearchFlags is the single registration site: runResearch reads
// these pointers, and the skill's command-grammar generator
// (cmd/gummi/skill.go) enumerates the same flag set — so the documented
// grammar can never drift from the shipped flags.
type researchFlagValues struct {
	envelope            *int
	profile, gate, ref  *string
	repo, until         *string
	autonomous, verbose *bool
	timeout             *time.Duration
}

// registerResearchFlags binds `gummi research`'s flags onto fs and returns
// their pointers. It defines the flags only — parsing and validation stay
// in runResearch — so a throwaway FlagSet can be handed here purely to
// enumerate the grammar. Deliberately no --full, --acceptance, or
// --skip-investigate: RS has no brainstorm/plan and no Verification-plan
// section to seed.
func registerResearchFlags(fs *flag.FlagSet) *researchFlagValues {
	return &researchFlagValues{
		envelope:   fs.Int("envelope", 0, "credit envelope for the research card (required; falls back to GUMMI_ENVELOPE)"),
		profile:    fs.String("profile", "", "profile mapping roles to models (default: first configured)"),
		gate:       fs.String("gate-approval", driver.GateGates, "who approves design gates: off|gates|full (aliases: auto=gates, caller=off; persisted on the card; resume keeps it)"),
		timeout:    fs.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)"),
		autonomous: fs.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions"),
		verbose:    fs.Bool("verbose", false, "add per-tool-call activity lines to the stream"),
		ref:        fs.String("ref", "", "external correlation id, echoed in the stream and persisted for `status`/`resume` lookup"),
		repo:       fs.String("repo", "", "managed repository to create the card in (a configured `repos:` name; required when `repos:` is configured)"),
		until:      fs.String("until", "", "stop cleanly before crossing the gate that leaves this stage (only \"shape\" is a valid stop on RS's route)"),
	}
}
