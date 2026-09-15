package main

import (
	"errors"
	"os"
	"testing"
)

// doctorExit runs `gummi doctor` with args in repo and returns its exit code.
func doctorExit(t *testing.T, repo string, args ...string) int {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	err = runDoctor(args)
	if err == nil {
		return 0
	}
	var ec *exitError
	if errors.As(err, &ec) {
		return ec.code
	}
	t.Fatalf("doctor returned a non-exit error: %v", err)
	return -1
}

// TestDoctorExitsNonZeroWhenNotReady: doctor printed "not ready" beside a ✗
// and exited 0, so `gummi doctor && gummi run …` ran anyway and a person
// reading the shell's verdict rather than the checklist was told a broken
// workspace was fine.
func TestDoctorExitsNonZeroWhenNotReady(t *testing.T) {
	clearDoctorEnv(t)
	// no backend reachable and no profile it can drive: a failed check
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "definitely-not-on-path --serve")
	repo := gitRepo(t)

	r := buildDoctorReport(repo, doctorOpts{})
	if r.Ready {
		t.Fatal("this workspace was supposed to be unready; the test proves nothing")
	}
	if got := doctorExit(t, repo); got != doctorNotReadyExit {
		t.Errorf("doctor exit = %d, want %d", got, doctorNotReadyExit)
	}
	// the JSON shape is the documented path for a calling agent, and it
	// must carry the same verdict in its exit status
	if got := doctorExit(t, repo, "--json"); got != doctorNotReadyExit {
		t.Errorf("doctor --json exit = %d, want %d", got, doctorNotReadyExit)
	}
}

// A ready workspace still exits 0, in both output shapes.
func TestDoctorExitsZeroWhenReady(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent --serve")
	t.Setenv("GUMMI_ENVELOPE", "500")
	stubBackendBins(t, "fakeagent")
	repo := gitRepo(t)
	writeProfiles(t, repo, `
default: p
profiles:
  p:
    architect: { backend: headless, model: m }
    implementer: { backend: headless, model: m }
    reviewer: { backend: headless, model: m }
    scribe: { backend: headless, model: m }
`)
	if !buildDoctorReport(repo, doctorOpts{}).Ready {
		t.Fatal("workspace was supposed to be ready")
	}
	if got := doctorExit(t, repo); got != 0 {
		t.Errorf("ready doctor exit = %d, want 0", got)
	}
	if got := doctorExit(t, repo, "--json"); got != 0 {
		t.Errorf("ready doctor --json exit = %d, want 0", got)
	}
}

// The code must not collide with a driver status code, or a caller
// branching on the exit table reads "not ready" as "timeout".
func TestDoctorExitCodeIsOutsideTheDriverTable(t *testing.T) {
	if doctorNotReadyExit <= 6 {
		t.Errorf("doctorNotReadyExit = %d collides with the driver's 1-6 status codes", doctorNotReadyExit)
	}
}
