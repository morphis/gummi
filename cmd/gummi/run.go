package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/driver"
	"github.com/morphis/gummi/internal/state"
)

// defaultStageTimeout bounds how long a single stage may go without any
// activity before the driver treats it as a hang and escalates. Generous
// by default (agents legitimately take minutes); --stage-timeout 0
// disables it.
const defaultStageTimeout = 10 * time.Minute

// runRun implements `gummi run [flags] "<description>"` (DESIGN §8.2): it
// creates one feature and drives it headlessly through the quality floor
// to a verified branch, streaming milestone + decision NDJSON and exiting
// with a typed status. Quick route by default; --full opts into
// brainstorm+plan. An envelope is required (D6) and an agent must be
// configured — both fail loud before any work begins.
func runRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	rv := registerRunFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `usage: gummi run [flags] "<description>"`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("run needs exactly one description argument")
	}
	desc := fs.Arg(0)

	acceptanceText, err := readAcceptance(*rv.acceptance)
	if err != nil {
		return err
	}
	opts, err := driverOptions(*rv.envelope, *rv.profile, *rv.full, *rv.gate, *rv.timeout, *rv.autonomous, *rv.verbose, *rv.ref, acceptanceText, *rv.until)
	if err != nil {
		return err
	}

	return withRunEngine(func(ctx context.Context, d *driver.Driver, _ *state.Store) (driver.Outcome, error) {
		return d.Run(ctx, desc)
	}, opts)
}

// runFlagValues holds the flag pointers `gummi run` binds. registerRunFlags
// is the single registration site: runRun reads these pointers, and the
// skill's command-grammar generator (cmd/gummi/skill.go) enumerates the
// same flag set — so the documented grammar can never drift from the
// shipped flags (a golden test asserts every one appears in SKILL.md).
type runFlagValues struct {
	envelope                       *int
	profile, gate, ref, acceptance *string
	until                          *string
	full, autonomous, verbose      *bool
	timeout                        *time.Duration
}

// registerRunFlags binds `gummi run`'s flags onto fs and returns their
// pointers. It defines the flags only — parsing and validation stay in
// runRun — so a throwaway FlagSet can be handed here purely to enumerate
// the grammar.
func registerRunFlags(fs *flag.FlagSet) *runFlagValues {
	return &runFlagValues{
		envelope:   fs.Int("envelope", 0, "credit envelope for the feature (required; falls back to GUMMI_ENVELOPE)"),
		profile:    fs.String("profile", "", "profile mapping roles to models (default: first configured)"),
		full:       fs.Bool("full", false, "run the full route (brainstorm + plan), not the quick route"),
		gate:       fs.String("gate-approval", driver.GateAuto, "who approves design gates: auto|caller (persisted on the card; resume keeps it)"),
		timeout:    fs.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)"),
		autonomous: fs.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions"),
		verbose:    fs.Bool("verbose", false, "add per-tool-call activity lines to the stream"),
		ref:        fs.String("ref", "", "external correlation id, echoed in the stream and persisted for `status`/`resume` lookup"),
		acceptance: fs.String("acceptance", "", "acceptance criteria to seed the spec draft's Verification plan (a file path, or - for stdin)"),
		until:      fs.String("until", "", "stop cleanly before crossing the gate that leaves this design stage (default: run to a verified branch)"),
	}
}

// readAcceptance loads the --acceptance criteria: a file path, or "-" for
// stdin. An empty flag (the default) yields empty text and no read. File IO
// lives here in the CLI so the driver's Options carries the criteria text,
// not a path.
func readAcceptance(pathOrDash string) (string, error) {
	switch pathOrDash {
	case "":
		return "", nil
	case "-":
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading --acceptance from stdin: %w", err)
		}
		return string(b), nil
	default:
		b, err := os.ReadFile(pathOrDash)
		if err != nil {
			return "", fmt.Errorf("reading --acceptance %s: %w", pathOrDash, err)
		}
		return string(b), nil
	}
}

// driverOptions validates and assembles the shared driving options. The
// envelope is required: it falls back to GUMMI_ENVELOPE, then refuses.
func driverOptions(envelope int, profile string, full bool, gate string, timeout time.Duration, autonomous, verbose bool, ref, acceptance, until string) (driver.Options, error) {
	if envelope == 0 {
		if v := os.Getenv("GUMMI_ENVELOPE"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				envelope = n
			}
		}
	}
	if envelope <= 0 {
		return driver.Options{}, fmt.Errorf("an envelope is required: pass --envelope N (or set GUMMI_ENVELOPE); runs refuse to start without one")
	}
	if gate != driver.GateAuto && gate != driver.GateCaller {
		return driver.Options{}, fmt.Errorf("--gate-approval must be %q or %q, got %q", driver.GateAuto, driver.GateCaller, gate)
	}
	// validate --until against the route --full selects, before any work
	// begins (so a bad target fails as a plain usage error, not mid-run).
	skip := domain.QuickRoute()
	if full {
		skip = domain.SkipFlags{}
	}
	if err := driver.ValidateUntil(domain.Stage(until), domain.KindFeature, skip); err != nil {
		return driver.Options{}, err
	}
	return driver.Options{
		Envelope: envelope, Profile: profile, Full: full, GateApproval: gate,
		StageTimeout: timeout, Autonomous: autonomous, Verbose: verbose, Ref: ref,
		Acceptance: acceptance, Until: domain.Stage(until),
	}, nil
}

// withRunEngine wires the workspace, lock, store, worktree manager, and
// agent engine (mirroring cmd/gummi/ingest.go), builds a driver over
// os.Stdout, hands it to fn, and maps the terminal Outcome to a process
// exit. The exclusive lock refuses to start while the TUI or another run
// holds the workspace (D13).
func withRunEngine(fn func(context.Context, *driver.Driver, *state.Store) (driver.Outcome, error), opts driver.Options) error {
	// A headless run/resume is routinely launched detached (backgrounded,
	// nohup, a supervisor). Without this, gummi keeps the default SIGHUP
	// disposition — terminate — so a controlling terminal that hangs up on
	// detach kills gummi, and the death of the parent tears down the stdio
	// pipes tethering the backend agent child, killing the model turn too
	// (near-zero progress, no commits). Ignoring SIGHUP makes gummi survive
	// the hangup and keep draining the child, nohup-style. The interactive
	// board does not go through here, so its terminal handling is unchanged.
	signal.Ignore(syscall.SIGHUP)

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	ws, err := ensureWorkspace(cwd)
	if err != nil {
		return err
	}
	release, err := state.AcquireLock(ws.LockFile())
	if err != nil {
		return err
	}
	defer release()
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return err
	}
	defer store.Close()
	wt, err := newManager(context.Background(), cwd)
	if err != nil {
		return err
	}
	eng, ag, _ := newEngineFromEnv(store, wt, ws)
	if eng == nil {
		return fmt.Errorf("no coding agent is configured; a run needs one (GitHub Copilot, or set GUMMI_AGENT/GUMMI_AGENT_CMD)")
	}
	defer func() { _ = eng.Close(); _ = ag.Close() }()

	d := driver.New(eng, store, ws, os.Stdout, opts)
	out, derr := fn(context.Background(), d, store)
	if out.Status == "" && derr != nil {
		// the closure failed before the driver produced any outcome (e.g. an
		// unknown id/ref in `resume`) — a plain setup/usage error to stderr,
		// exit 1; the driver's own failures instead carry a StatusError
		// Outcome (already reported on the NDJSON stream) and stay quiet.
		return derr
	}
	return driverExit(out, derr)
}

// driverExit maps a driver Outcome to a process exit. done exits 0
// (return nil); every other terminal status exits with its code via
// exitError — the NDJSON stream already carried the detail, so no stderr
// line is added (a setup error before the driver ran is returned plainly
// and handled by main).
func driverExit(out driver.Outcome, _ error) error {
	// done and --until's clean stop both exit 0; every decision boundary
	// takes its typed non-zero code (the NDJSON stream carried the detail).
	if out.Status.ExitCode() == 0 {
		return nil
	}
	return &exitError{code: out.Status.ExitCode()}
}
