package verify

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func TestRunPassAndFail(t *testing.T) {
	dir := t.TempDir()
	checks := []domain.Check{
		{Name: "ok", Cmd: "echo hello"},
		{Name: "fail", Cmd: "echo boom >&2; exit 3"},
		{Name: "pwd", Cmd: "pwd"},
	}
	res := Run(context.Background(), dir, checks)
	if len(res) != 3 {
		t.Fatalf("got %d results", len(res))
	}
	if !res[0].OK || !strings.Contains(res[0].Output, "hello") {
		t.Errorf("check 0: %+v", res[0])
	}
	if res[1].OK || res[1].ExitCode != 3 || !strings.Contains(res[1].Output, "boom") {
		t.Errorf("check 1 should fail with exit 3: %+v", res[1])
	}
	// commands run in the given workdir
	if !strings.Contains(res[2].Output, dir) {
		t.Errorf("check ran in wrong dir: %q", res[2].Output)
	}
	if AllOK(res) {
		t.Error("AllOK should be false with a failure present")
	}
}

func TestRunAllOKVacuous(t *testing.T) {
	if !AllOK(Run(context.Background(), t.TempDir(), nil)) {
		t.Error("no checks should be vacuously OK")
	}
}

func TestRunTruncatesOutput(t *testing.T) {
	res := Run(context.Background(), t.TempDir(), []domain.Check{
		{Name: "loud", Cmd: "yes x | head -c 20000"},
	})
	if len(res[0].Output) > maxOutput+32 {
		t.Errorf("output not truncated: %d bytes", len(res[0].Output))
	}
	if !strings.Contains(res[0].Output, "truncated") {
		t.Error("truncation marker missing")
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := Run(ctx, t.TempDir(), []domain.Check{{Name: "x", Cmd: "echo hi"}})
	if len(res) != 1 {
		t.Fatalf("cancelled context should still report every check, got %d", len(res))
	}
	if res[0].Status != StatusNotRun || res[0].OK {
		t.Errorf("unstarted check should be StatusNotRun, got %+v", res[0])
	}
}

// A check that exhausts the shared budget must not swallow the checks
// behind it: every configured check appears in the results, and one that
// never started is explicitly not-run rather than absent.
func TestRunEmitsNotRunWhenBudgetExhausted(t *testing.T) {
	dir := t.TempDir()
	checks := []domain.Check{
		{Name: "slow", Cmd: "sleep 5"},
		{Name: "mustfail", Cmd: "exit 7"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res := Run(ctx, dir, checks)
	if len(res) != len(checks) {
		t.Fatalf("got %d results for %d checks: checks behind a slow one must not vanish", len(res), len(checks))
	}
	if res[0].Status != StatusTimeout {
		t.Errorf("deadline-killed check should be StatusTimeout, got %+v", res[0])
	}
	if res[1].Status != StatusNotRun {
		t.Errorf("check behind the slow one should be StatusNotRun (present, not absent), got %+v", res[1])
	}
}

// A check killed by the deadline must be distinguishable from one that
// failed to start (both surface as ExitCode -1): only the former is a
// timeout.
func TestRunDistinguishesTimeoutFromSpawnFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	// "exit 7" is fast; it runs and fails before the deadline. A timed-out
	// sibling is reported as StatusTimeout, not as a spawn failure.
	res := RunBounded(ctx, t.TempDir(), []domain.Check{
		{Name: "fail", Cmd: "exit 7"},
		{Name: "slow", Cmd: "sleep 5"},
	}, 100*time.Millisecond)
	if res[0].Status != StatusFail || res[0].ExitCode != 7 {
		t.Errorf("fast failing check: %+v", res[0])
	}
	if res[1].Status != StatusTimeout {
		t.Errorf("killed check should be StatusTimeout, got %+v", res[1])
	}
}
