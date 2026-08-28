package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	err := runRun([]string{"a feature"})
	if err == nil || !strings.Contains(err.Error(), "envelope is required") {
		t.Fatalf("err = %v, want an envelope-required failure", err)
	}
}

// GUMMI_ENVELOPE supplies the envelope when --envelope is absent.
func TestDriverOptionsEnvelopeFallback(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "250")
	opts, err := driverOptions(0, "", false, driver.GateAuto, time.Minute, false, false, "", "", "", "")
	if err != nil {
		t.Fatalf("driverOptions: %v", err)
	}
	if opts.Envelope != 250 {
		t.Fatalf("envelope = %d, want 250 from GUMMI_ENVELOPE", opts.Envelope)
	}
}

// An unknown --gate-approval value is rejected. --until is no longer
// validated by driverOptions (that moved to runRun, ahead of the kind
// widening — TestRunUntilValidation), so any string threads through as-is.
func TestDriverOptionsGateValidation(t *testing.T) {
	if _, err := driverOptions(100, "", false, "sometimes", time.Minute, false, false, "", "", "", ""); err == nil {
		t.Fatal("bad gate-approval accepted")
	}
	opts, err := driverOptions(100, "", true, driver.GateCaller, 0, true, true, "JIRA-9", "must handle empty input", "plan", "")
	if err != nil {
		t.Fatalf("driverOptions: %v", err)
	}
	if !opts.Full || opts.GateApproval != driver.GateCaller || !opts.Autonomous || opts.Ref != "JIRA-9" {
		t.Fatalf("options not threaded through: %+v", opts)
	}
	if opts.Acceptance != "must handle empty input" || opts.Until != "plan" {
		t.Fatalf("acceptance/until not threaded through: %+v", opts)
	}
}

// --until is validated against the feature route before any workspace work
// begins: an off-route or unknown stage fails as a plain usage error,
// straight out of runRun, before driverOptions or withRunEngine ever run.
// The positive (accepted) cases are covered at the driver level by
// internal/driver/steer_test.go's TestUntilStops family.
func TestRunUntilValidation(t *testing.T) {
	t.Setenv("GUMMI_ENVELOPE", "100")
	// quick route (default, no --full): --until plan is off-route → rejected.
	if err := runRun([]string{"--until", "plan", "a feature"}); err == nil || !strings.Contains(err.Error(), "not a valid stop") {
		t.Fatalf("err = %v, want a --until rejection naming the valid stops", err)
	}
	// an unknown stage is always rejected.
	if err := runRun([]string{"--until", "banana", "a feature"}); err == nil || !strings.Contains(err.Error(), "not a valid stop") {
		t.Fatalf("err = %v, want a --until rejection naming the valid stops", err)
	}
}

// driverExit maps each terminal status to its process exit code, and done
// to a clean (nil) return.
func TestDriverExitMapping(t *testing.T) {
	if err := driverExit(driver.Outcome{Status: driver.StatusDone}, nil); err != nil {
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
// silent re-run.
func TestResumeInputMutuallyExclusive(t *testing.T) {
	if _, err := resumeInput("no", true, "", false, "", true, false, false); err == nil {
		t.Fatal("both --answer and --approve accepted")
	}
	in, err := resumeInput("no", false, "", false, "", true, false, false)
	if err != nil || in.Answer == nil || *in.Answer != "no" {
		t.Fatalf("answer input = %+v, err=%v", in, err)
	}
	in, err = resumeInput("", true, "", false, "", false, false, false)
	if err != nil || !in.Approve {
		t.Fatalf("approve input = %+v, err=%v", in, err)
	}
	// no flags set → an all-zero input (re-run the parked stage).
	in, err = resumeInput("", false, "", false, "", false, false, false)
	if err != nil || in.Answer != nil || in.Approve || in.RequestChanges != nil || in.Bounce != nil {
		t.Fatalf("empty resume input = %+v, err=%v", in, err)
	}
}

// --bounce is a fourth mutually-exclusive decision (the verify/review
// rewind), and --note is only meaningful when carried by --bounce — an
// orphan --note is a usage error, not a silent no-op.
func TestResumeInputBounce(t *testing.T) {
	// --bounce alone → empty-note bounce.
	in, err := resumeInput("", false, "", true, "", false, false, false)
	if err != nil || in.Bounce == nil || *in.Bounce != "" {
		t.Fatalf("bounce input = %+v, err=%v", in, err)
	}
	// --bounce --note "why" → bounce carrying the note.
	in, err = resumeInput("", false, "", true, "flaky mock", false, false, true)
	if err != nil || in.Bounce == nil || *in.Bounce != "flaky mock" {
		t.Fatalf("bounce+note input = %+v, err=%v", in, err)
	}
	// --bounce combined with any other decision is refused.
	if _, err := resumeInput("no", false, "", true, "", true, false, false); err == nil {
		t.Fatal("both --answer and --bounce accepted")
	}
	if _, err := resumeInput("", true, "", true, "", false, false, false); err == nil {
		t.Fatal("both --approve and --bounce accepted")
	}
	if _, err := resumeInput("", false, "changes", true, "", false, true, false); err == nil {
		t.Fatal("both --request-changes and --bounce accepted")
	}
	// --note without --bounce is a usage error, not a silently-dropped flag.
	if _, err := resumeInput("", false, "", false, "orphan", false, false, true); err == nil {
		t.Fatal("--note accepted without --bounce")
	}
}

// resume rejects a malformed work-item id before touching the workspace.
func TestResumeBadID(t *testing.T) {
	if err := runResume([]string{"not-an-id"}); err == nil {
		t.Fatal("malformed id accepted")
	}
}

// isSet distinguishes an explicitly-passed flag from its default.
func TestIsSet(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	a := fs.String("answer", "", "")
	_ = a
	if err := fs.Parse([]string{"--answer", ""}); err != nil {
		t.Fatal(err)
	}
	if !isSet(fs, "answer") {
		t.Fatal("explicitly-set empty flag reported unset")
	}
	if isSet(fs, "approve") {
		t.Fatal("absent flag reported set")
	}
}
