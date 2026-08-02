package main

import (
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/driver"
)

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
	opts, err := driverOptions(0, "", false, driver.GateAuto, time.Minute, false, false, "", "", "")
	if err != nil {
		t.Fatalf("driverOptions: %v", err)
	}
	if opts.Envelope != 250 {
		t.Fatalf("envelope = %d, want 250 from GUMMI_ENVELOPE", opts.Envelope)
	}
}

// An unknown --gate-approval value is rejected.
func TestDriverOptionsGateValidation(t *testing.T) {
	if _, err := driverOptions(100, "", false, "sometimes", time.Minute, false, false, "", "", ""); err == nil {
		t.Fatal("bad gate-approval accepted")
	}
	opts, err := driverOptions(100, "", true, driver.GateCaller, 0, true, true, "JIRA-9", "must handle empty input", "plan")
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

// --until is validated against the route: plan is legal on the full route
// but not on the quick route (where plan is skipped); spec is legal on both.
func TestDriverOptionsUntilRouteValidation(t *testing.T) {
	// quick route (full=false): --until plan is off-route → rejected.
	if _, err := driverOptions(100, "", false, driver.GateAuto, time.Minute, false, false, "", "", "plan"); err == nil {
		t.Fatal("--until plan accepted on the quick route (plan is skipped)")
	}
	// quick route: --until spec is valid.
	if _, err := driverOptions(100, "", false, driver.GateAuto, time.Minute, false, false, "", "", "spec"); err != nil {
		t.Fatalf("--until spec rejected on the quick route: %v", err)
	}
	// full route: --until plan is valid.
	if _, err := driverOptions(100, "", true, driver.GateAuto, time.Minute, false, false, "", "", "plan"); err != nil {
		t.Fatalf("--until plan rejected on the full route: %v", err)
	}
	// an unknown stage is always rejected.
	if _, err := driverOptions(100, "", true, driver.GateAuto, time.Minute, false, false, "", "", "banana"); err == nil {
		t.Fatal("--until banana accepted")
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
	if _, err := resumeInput("no", true, "", true, false); err == nil {
		t.Fatal("both --answer and --approve accepted")
	}
	in, err := resumeInput("no", false, "", true, false)
	if err != nil || in.Answer == nil || *in.Answer != "no" {
		t.Fatalf("answer input = %+v, err=%v", in, err)
	}
	in, err = resumeInput("", true, "", false, false)
	if err != nil || !in.Approve {
		t.Fatalf("approve input = %+v, err=%v", in, err)
	}
	// no flags set → an all-zero input (re-run the parked stage).
	in, err = resumeInput("", false, "", false, false)
	if err != nil || in.Answer != nil || in.Approve || in.RequestChanges != nil {
		t.Fatalf("empty resume input = %+v, err=%v", in, err)
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
