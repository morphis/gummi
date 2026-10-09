package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/driver"
	"github.com/morphis/gummi/internal/state"
)

// TestWithRunEngineSurfacesGuardedMismatch pins the call site the review
// flagged: a guarded config with a role on claude must reach the caller as
// the specific guarded-mismatch diagnosis, not the generic "no coding agent
// is configured" text that would misdiagnose an agent that's actually on
// PATH. GUMMI_CLAUDE_BIN points at this test binary so claude is
// independently startable, proving the block comes from the gate.
func TestWithRunEngineSurfacesGuardedMismatch(t *testing.T) {
	clearDoctorEnv(t)
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GUMMI_CLAUDE_BIN", bin)

	root := t.TempDir()
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	cliGit(t, root, "init", "-q", "-b", "main")
	cliGit(t, root, "config", "user.name", "t")
	cliGit(t, root, "config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cliGit(t, root, "add", ".")
	cliGit(t, root, "commit", "-q", "-m", "init")

	writeConfig(t, root, "permissions: guarded\n")
	writeProfiles(t, root, `
default: premium
profiles:
  premium:
    architect: { backend: claude, model: m }
`)

	runErr := withRunEngine(func(ctx context.Context, d *driver.Driver, store *state.Store, ws state.Workspace) (driver.Outcome, error) {
		t.Fatal("fn should not run when the guarded/backend gate blocks the engine")
		return driver.Outcome{}, nil
	}, driver.Options{})
	if runErr == nil {
		t.Fatal("err = nil, want a guarded-mismatch error")
	}
	if strings.Contains(runErr.Error(), "no coding agent is configured") {
		t.Fatalf("err = %v, want the guarded-mismatch diagnosis, not the generic no-agent message", runErr)
	}
	for _, want := range []string{"premium", "architect", "claude"} {
		if !strings.Contains(runErr.Error(), want) {
			t.Errorf("error %q should name %q", runErr.Error(), want)
		}
	}
}

// An envelope is required (D6): missing --envelope with no GUMMI_ENVELOPE
// fails loud before any workspace is touched.
func TestRunRequiresEnvelope(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "")
	err := runCLI("run", "a feature")
	if err == nil || !strings.Contains(err.Error(), "envelope is required") {
		t.Fatalf("err = %v, want an envelope-required failure", err)
	}
}

// GUMMI_ENVELOPE supplies the envelope when --envelope is absent.
func TestDriverOptionsEnvelopeFallback(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "2.50")
	opts, err := driverOptions(parsedFlags(t, "run"), "")
	if err != nil {
		t.Fatalf("driverOptions: %v", err)
	}
	if opts.Envelope != 250 {
		t.Fatalf("envelope = %d, want 250 credits from GUMMI_ENVELOPE=2.50", opts.Envelope)
	}
}

// An unknown --gate-approval value is rejected, and the rest of the shared
// driving surface threads through to driver.Options unchanged.
func TestDriverOptionsGateValidation(t *testing.T) {
	bad := parsedFlags(t, "run", "--envelope", "100", "--gate-approval", "sometimes")
	if _, err := driverOptions(bad, ""); err == nil {
		t.Fatal("bad gate-approval accepted")
	}
	fl := parsedFlags(t, "run",
		"--envelope", "100", "--gate-approval", driver.GateAttended,
		"--stage-timeout", "0", "--autonomous", "--verbose",
		"--ref", "JIRA-9", "--until", "plan", "--base", "release-2.1")
	opts, err := driverOptions(fl, "must handle empty input")
	if err != nil {
		t.Fatalf("driverOptions: %v", err)
	}
	if opts.GateApproval != driver.GateAttended || !opts.Autonomous || opts.Ref != "JIRA-9" {
		t.Fatalf("options not threaded through: %+v", opts)
	}
	if opts.Acceptance != "must handle empty input" || opts.Until != "plan" || opts.Base != "release-2.1" {
		t.Fatalf("acceptance/until/base not threaded through: %+v", opts)
	}
}

// A verb that does not offer one of the shared driving flags still
// assembles its options: goal takes no --repo, and resume changes neither
// repo nor base. Reading an absent flag must be "" rather than a panic.
func TestDriverOptionsToleratesAbsentSharedFlags(t *testing.T) {
	for _, verb := range []string{"goal", "resume"} {
		opts, err := driverOptions(parsedFlags(t, verb, "--envelope", "100"), "")
		if err != nil {
			t.Fatalf("%s: driverOptions: %v", verb, err)
		}
		if opts.Repo != "" {
			t.Errorf("%s: Repo = %q, want empty", verb, opts.Repo)
		}
	}
}

// --until is validated against the feature route before any workspace work
// begins: an off-route or unknown stage fails as a plain usage error,
// straight out of runRun, before driverOptions or withRunEngine ever run.
// The positive (accepted) cases are covered at the driver level by
// internal/driver/steer_test.go's TestUntilStops family.
func TestRunUntilValidation(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "100")
	// a real stage that is not a stop on the route → rejected. The design
	// gate is the one stop, so every other stage lands here.
	if err := runCLI("run", "--until", "implement", "a feature"); err == nil || !strings.Contains(err.Error(), "not a valid stop") {
		t.Fatalf("err = %v, want a --until rejection naming the valid stops", err)
	}
	// an unknown stage is always rejected.
	if err := runCLI("run", "--until", "banana", "a feature"); err == nil || !strings.Contains(err.Error(), "not a valid stop") {
		t.Fatalf("err = %v, want a --until rejection naming the valid stops", err)
	}
}

// driverExit maps each terminal status to its process exit code, and done
// to a clean (nil) return.
func TestDriverExitMapping(t *testing.T) {
	if err := driverExit(driver.Outcome{Status: driver.StatusVerified}, nil); err != nil {
		t.Fatalf("done → %v, want nil", err)
	}
	// --until's clean stop also exits 0 (nil return).
	if err := driverExit(driver.Outcome{Status: driver.StatusStopped}, nil); err != nil {
		t.Fatalf("stopped → %v, want nil (exit 0)", err)
	}
	cases := map[driver.Status]int{
		driver.StatusQuestion:   2,
		driver.StatusBlocked:    3,
		driver.StatusEscalation: 4,
		driver.StatusExhausted:  5,
		driver.StatusTimeout:    6,
		driver.StatusError:      1,
	}
	for st, code := range cases {
		err := driverExit(driver.Outcome{Status: st}, nil)
		var ec *exitError
		if !errors.As(err, &ec) || ec.code != code {
			t.Fatalf("%s → %v, want exit code %d", st, err, code)
		}
	}
}

// resumeInput enforces at most one decision flag and preserves an
// explicitly-empty answer as a (rejectable) decision rather than a
// silent re-run. It reads the flags `gummi resume` really declares, so a
// case here cannot describe a flag surface the binary does not have.
func TestResumeInputMutuallyExclusive(t *testing.T) {
	rf := func(argv ...string) cliFlags { return parsedFlags(t, "resume", argv...) }

	if _, err := resumeInput(rf("--answer", "no", "--approve")); err == nil {
		t.Fatal("both --answer and --approve accepted")
	}
	in, err := resumeInput(rf("--answer", "no"))
	if err != nil || in.Answer == nil || *in.Answer != "no" {
		t.Fatalf("answer input = %+v, err=%v", in, err)
	}
	in, err = resumeInput(rf("--approve"))
	if err != nil || !in.Approve {
		t.Fatalf("approve input = %+v, err=%v", in, err)
	}
	// no flags set → an all-zero input (re-run the parked stage).
	in, err = resumeInput(rf())
	if err != nil || in.Answer != nil || in.Approve || in.RequestChanges != nil || in.Bounce != nil {
		t.Fatalf("empty resume input = %+v, err=%v", in, err)
	}
}

// --bounce is a fourth mutually-exclusive decision (the verify/review
// rewind), and --note is only meaningful when carried by --bounce — an
// orphan --note is a usage error, not a silent no-op.
func TestResumeInputBounce(t *testing.T) {
	rf := func(argv ...string) cliFlags { return parsedFlags(t, "resume", argv...) }

	// --bounce alone → empty-note bounce.
	in, err := resumeInput(rf("--bounce"))
	if err != nil || in.Bounce == nil || *in.Bounce != "" {
		t.Fatalf("bounce input = %+v, err=%v", in, err)
	}
	// --bounce --note "why" → bounce carrying the note.
	in, err = resumeInput(rf("--bounce", "--note", "flaky mock"))
	if err != nil || in.Bounce == nil || *in.Bounce != "flaky mock" {
		t.Fatalf("bounce+note input = %+v, err=%v", in, err)
	}
	// --bounce combined with any other decision is refused.
	if _, err := resumeInput(rf("--answer", "no", "--bounce")); err == nil {
		t.Fatal("both --answer and --bounce accepted")
	}
	if _, err := resumeInput(rf("--approve", "--bounce")); err == nil {
		t.Fatal("both --approve and --bounce accepted")
	}
	if _, err := resumeInput(rf("--request-changes", "changes", "--bounce")); err == nil {
		t.Fatal("both --request-changes and --bounce accepted")
	}
	// --note without --bounce is a usage error, not a silently-dropped flag.
	if _, err := resumeInput(rf("--note", "orphan")); err == nil {
		t.Fatal("--note accepted without --bounce")
	}
}

// resume rejects a malformed work-item id before touching the workspace.
func TestResumeBadID(t *testing.T) {
	if err := runCLI("resume", "not-an-id"); err == nil {
		t.Fatal("malformed id accepted")
	}
}

// GUMMI_ENVELOPE is dollars wherever it is read — run, ingest and bugs
// alike — so the same export never gives one command's cards a hundredth
// of another's budget.
func TestEnvEnvelopeIsDollars(t *testing.T) {
	for v, want := range map[string]int{"20": 2000, "$12.50": 1250, "1,200": 120000, "": 0, "0": 0, "-5": 0, "lots": 0} {
		t.Setenv("GUMMI_ENVELOPE", v)
		if got := envEnvelope(); got != want {
			t.Errorf("GUMMI_ENVELOPE=%q = %d credits, want %d", v, got, want)
		}
	}
}
