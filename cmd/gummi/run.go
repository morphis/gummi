package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"time"

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
	envelope := fs.Int("envelope", 0, "credit envelope for the feature (required; falls back to GUMMI_ENVELOPE)")
	profile := fs.String("profile", "", "profile mapping roles to models (default: first configured)")
	full := fs.Bool("full", false, "run the full route (brainstorm + plan), not the quick route")
	gate := fs.String("gate-approval", driver.GateAuto, "who approves design gates: auto|caller")
	timeout := fs.Duration("stage-timeout", defaultStageTimeout, "per-stage inactivity timeout (0 disables)")
	autonomous := fs.Bool("autonomous", false, "auto-take the recommended answer instead of checkpointing questions")
	verbose := fs.Bool("verbose", false, "add per-tool-call activity lines to the stream")
	ref := fs.String("ref", "", "external correlation id, echoed in the stream")
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

	opts, err := driverOptions(*envelope, *profile, *full, *gate, *timeout, *autonomous, *verbose, *ref)
	if err != nil {
		return err
	}

	return withRunEngine(func(ctx context.Context, d *driver.Driver) (driver.Outcome, error) {
		return d.Run(ctx, desc)
	}, opts)
}

// driverOptions validates and assembles the shared driving options. The
// envelope is required: it falls back to GUMMI_ENVELOPE, then refuses.
func driverOptions(envelope int, profile string, full bool, gate string, timeout time.Duration, autonomous, verbose bool, ref string) (driver.Options, error) {
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
	return driver.Options{
		Envelope: envelope, Profile: profile, Full: full, GateApproval: gate,
		StageTimeout: timeout, Autonomous: autonomous, Verbose: verbose, Ref: ref,
	}, nil
}

// withRunEngine wires the workspace, lock, store, worktree manager, and
// agent engine (mirroring cmd/gummi/ingest.go), builds a driver over
// os.Stdout, hands it to fn, and maps the terminal Outcome to a process
// exit. The exclusive lock refuses to start while the TUI or another run
// holds the workspace (D13).
func withRunEngine(fn func(context.Context, *driver.Driver) (driver.Outcome, error), opts driver.Options) error {
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
	out, derr := fn(context.Background(), d)
	return driverExit(out, derr)
}

// driverExit maps a driver Outcome to a process exit. done exits 0
// (return nil); every other terminal status exits with its code via
// exitError — the NDJSON stream already carried the detail, so no stderr
// line is added (a setup error before the driver ran is returned plainly
// and handled by main).
func driverExit(out driver.Outcome, _ error) error {
	if out.Status == driver.StatusDone {
		return nil
	}
	return &exitError{code: out.Status.ExitCode()}
}
