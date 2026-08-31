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

// eventsFileMode is the permission the .gummi/state/events.jsonl mirror is
// created with — same as the SQLite state DB (state may carry transcripts,
// so it stays 0600).
const eventsFileMode = 0o600

// defaultStageTimeout bounds how long a single stage may go without any
// activity before the driver treats it as a hang and escalates. Generous
// by default (a frontier reviewer on a dense plan spec can genuinely take
// well past ten minutes to complete one critique turn); --stage-timeout 0
// disables it. Callers who know their profile is faster can shrink it;
// callers on especially large specs can push it higher.
const defaultStageTimeout = 20 * time.Minute

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
	// validate --until against the route --full selects, before any work
	// begins (so a bad target fails as a plain usage error, not mid-run).
	skip := domain.QuickRoute()
	if *rv.full {
		skip = domain.SkipFlags{}
	}
	if err := driver.ValidateUntil(domain.Stage(*rv.until), domain.KindFeature, skip); err != nil {
		return err
	}
	opts, err := driverOptions(*rv.envelope, *rv.profile, *rv.full, *rv.gate, *rv.timeout, *rv.autonomous, *rv.verbose, *rv.ref, acceptanceText, *rv.until, *rv.repo)
	if err != nil {
		return err
	}

	return withRunEngine(func(ctx context.Context, d *driver.Driver, _ *state.Store, ws state.Workspace) (driver.Outcome, error) {
		// mint the card first, then take its per-card lock for the drive so
		// this run is the sole governor of the card it just created (two
		// runs mint disjoint cards and so never contend on each other's lock).
		f, err := d.Create(ctx, domain.KindFeature, desc)
		if err != nil {
			return driver.Outcome{}, err
		}
		release, err := state.AcquireLock(ws.CardLockFile(f.ID))
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
		return d.Drive(ctx, f)
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
	repo, until                    *string
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
		gate:       fs.String("gate-approval", driver.GateGates, "who approves design gates: off|gates|full (aliases: auto=gates, caller=off; persisted on the card; resume keeps it)"),
		timeout:    fs.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)"),
		autonomous: fs.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions"),
		verbose:    fs.Bool("verbose", false, "add per-tool-call activity lines to the stream"),
		ref:        fs.String("ref", "", "external correlation id, echoed in the stream and persisted for `status`/`resume` lookup"),
		repo:       fs.String("repo", "", "managed repository to create the card in (a configured `repos:` name; required when `repos:` is configured)"),
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
func driverOptions(envelope int, profile string, full bool, gate string, timeout time.Duration, autonomous, verbose bool, ref, acceptance, until, repo string) (driver.Options, error) {
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
	norm, ok := domain.NormalizeGateApproval(gate)
	if !ok {
		return driver.Options{}, fmt.Errorf(
			"--gate-approval must be %q, %q, or %q (aliases %q, %q accepted), got %q",
			domain.GateOff, domain.GateGates, domain.GateFull, "auto", "caller", gate)
	}
	return driver.Options{
		Envelope: envelope, Profile: profile, Full: full, GateApproval: norm,
		StageTimeout: timeout, Autonomous: autonomous, Verbose: verbose, Ref: ref,
		Acceptance: acceptance, Until: domain.Stage(until), Repo: repo,
	}, nil
}

// trackPID records this process's pid at ws.PIDFile(id) and returns a
// cleanup that clears it on clean exit. Call it once a card's per-card lock
// is held, right where the id first comes into scope — for `run`/`research`
// that's after Create mints the card, for `resume`/`verify` it's after the
// existing card resolves. A caller whose bash wrapper is killed by the
// harness (SIGHUP-ignored gummi keeps running) has no way to tell whether
// gummi died with the wrapper or is still churning: recording our pid lets
// an external check use `kill -0` on that specific card to answer the
// question without touching the flock (which would fight the live run).
// Scoping the file per card (BG-006) means concurrent drives of independent
// cards never clobber or clear each other's entry. Clear on clean exit so
// the absence is authoritative; a crash leaves the file behind but the pid
// it names no longer signals, so the same check still reads dead.
func trackPID(ws state.Workspace, id domain.FeatureID) (func(), error) {
	path := ws.PIDFile(id)
	pid := os.Getpid()
	if err := state.WritePIDFile(path, pid); err != nil {
		return nil, fmt.Errorf("recording pid: %w", err)
	}
	return func() { _ = state.ClearPIDFile(path, pid) }, nil
}

// withRunEngine wires the workspace, store, worktree manager, and agent
// engine (mirroring cmd/gummi/ingest.go), builds a driver over os.Stdout,
// hands it to fn, and maps the terminal Outcome to a process exit. It takes
// no whole-workspace lock: each command's fn resolves its card and holds
// that card's per-card lock for the drive, so independent cards run
// concurrently while a single card is still guarded against double-drive.
func withRunEngine(fn func(context.Context, *driver.Driver, *state.Store, state.Workspace) (driver.Outcome, error), opts driver.Options) error {
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
	wsRoot, defaultRoot, named, err := resolveAllRoots(cwd)
	if err != nil {
		return err
	}
	ws, err := ensureWorkspace(wsRoot, defaultRoot)
	if err != nil {
		return err
	}

	// Mirror the NDJSON stream to .gummi/state/events.jsonl in addition to
	// stdout so a wrapper-death survivor can tail progress off disk. Append
	// mode preserves cross-invocation history; the driver's own emitter
	// serializes writes so no cross-line interleave slips in.
	events, err := os.OpenFile(ws.EventsFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, eventsFileMode)
	if err != nil {
		return fmt.Errorf("opening events log: %w", err)
	}
	defer events.Close()

	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		return err
	}
	defer store.Close()
	pool, err := newPool(context.Background(), wsRoot, defaultRoot, named, store, true)
	if err != nil {
		return err
	}
	eng, agents, _, err := newEngineFromEnv(store, pool, ws)
	if eng == nil {
		if err != nil {
			return err
		}
		return fmt.Errorf("no coding agent is configured; a run needs one (GitHub Copilot, or set GUMMI_AGENT/GUMMI_AGENT_CMD)")
	}
	defer func() { _ = eng.Close(); closeAgents(agents) }()

	d := driver.New(eng, store, ws, io.MultiWriter(os.Stdout, events), opts)
	out, derr := fn(context.Background(), d, store, ws)
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
