package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearDoctorEnv neutralizes every environment variable buildDoctorReport
// reads, so a test sees exactly what it sets (the suite runs inside other
// agents whose env would otherwise leak in). t.Setenv also restores them.
func clearDoctorEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GUMMI_AGENT", "GUMMI_AGENT_CMD", "GUMMI_CLAUDE_BIN", "GUMMI_OPENCODE_BIN",
		"GUMMI_PROVIDER_BASE_URL", "GUMMI_PROVIDER_KEY_ENV", "GUMMI_ENVELOPE",
	} {
		t.Setenv(k, "")
	}
}

// gitRepo makes a temp dir look like a git repo root (buildDoctorReport only
// stats .git; it never shells out).
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fakeAgentOnPath drops an executable named bin into a fresh dir and puts
// that dir on PATH, so a headless backend check finds it hermetically.
func fakeAgentOnPath(t *testing.T, bin string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, bin)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func checkByName(r doctorReport, name string) doctorCheck {
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	return doctorCheck{}
}

// A repo with a present backend binary, a set BYOK key, and a healthy
// envelope reports ready (workspace/profile warns don't block).
func TestDoctorReadyWithByokKey(t *testing.T) {
	clearDoctorEnv(t)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent --serve")
	t.Setenv("GUMMI_PROVIDER_BASE_URL", "http://127.0.0.1:8080/v1")
	t.Setenv("GUMMI_PROVIDER_KEY_ENV", "GUMMI_TEST_KEY")
	t.Setenv("GUMMI_TEST_KEY", "sekret")
	t.Setenv("GUMMI_ENVELOPE", "500")

	r := buildDoctorReport(gitRepo(t))
	if !r.Ready {
		t.Fatalf("expected ready, got not ready: %+v", r.Checks)
	}
	if c := checkByName(r, "backend"); c.Status != statusOK {
		t.Errorf("backend = %+v, want ok", c)
	}
	if c := checkByName(r, "auth"); c.Status != statusOK {
		t.Errorf("auth = %+v, want ok", c)
	}
	if c := checkByName(r, "envelope"); c.Status != statusOK {
		t.Errorf("envelope = %+v, want ok", c)
	}
}

// The BYOK key check reports the env var by NAME and never its value, and an
// unset key fails readiness.
func TestDoctorByokKeyMissingFailsAndHidesValue(t *testing.T) {
	clearDoctorEnv(t)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent")
	t.Setenv("GUMMI_PROVIDER_BASE_URL", "http://127.0.0.1:8080/v1")
	t.Setenv("GUMMI_PROVIDER_KEY_ENV", "GUMMI_TEST_KEY")
	// GUMMI_TEST_KEY intentionally unset.
	t.Setenv("GUMMI_ENVELOPE", "500")

	r := buildDoctorReport(gitRepo(t))
	c := checkByName(r, "auth")
	if c.Status != statusFail {
		t.Fatalf("auth = %+v, want fail", c)
	}
	if r.Ready {
		t.Error("report is ready despite a missing BYOK key")
	}
	if !strings.Contains(c.Detail, "GUMMI_TEST_KEY") {
		t.Errorf("auth detail should name the env var: %q", c.Detail)
	}
}

// A selected backend whose binary is absent fails readiness.
func TestDoctorBackendMissingBinary(t *testing.T) {
	clearDoctorEnv(t)
	t.Setenv("GUMMI_AGENT", "claude")
	t.Setenv("GUMMI_CLAUDE_BIN", "gummi-no-such-binary-xyz")

	r := buildDoctorReport(gitRepo(t))
	if c := checkByName(r, "backend"); c.Status != statusFail {
		t.Errorf("backend = %+v, want fail", c)
	}
	if r.Ready {
		t.Error("report is ready with a missing backend binary")
	}
}

// An unset envelope warns but does not block readiness (a run can still pass
// --envelope); a sub-turn envelope also warns.
func TestDoctorEnvelopeWarnDoesNotBlock(t *testing.T) {
	clearDoctorEnv(t)
	fakeAgentOnPath(t, "fakeagent")
	t.Setenv("GUMMI_AGENT", "headless")
	t.Setenv("GUMMI_AGENT_CMD", "fakeagent")
	// no envelope, no BYOK (auth becomes n/a for headless).

	r := buildDoctorReport(gitRepo(t))
	if c := checkByName(r, "envelope"); c.Status != statusWarn {
		t.Errorf("envelope = %+v, want warn", c)
	}
	if !r.Ready {
		t.Errorf("an unset envelope should not block readiness: %+v", r.Checks)
	}

	t.Setenv("GUMMI_ENVELOPE", "5") // below one turn
	r = buildDoctorReport(gitRepo(t))
	if c := checkByName(r, "envelope"); c.Status != statusWarn {
		t.Errorf("sub-turn envelope = %+v, want warn", c)
	}
}

// A non-repo directory fails the repo check and blocks readiness.
func TestDoctorNoRepoFails(t *testing.T) {
	clearDoctorEnv(t)
	r := buildDoctorReport(t.TempDir())
	if c := checkByName(r, "repo"); c.Status != statusFail {
		t.Errorf("repo = %+v, want fail", c)
	}
	if r.Ready {
		t.Error("report is ready outside a git repo")
	}
}
