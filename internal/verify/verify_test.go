package verify

import (
	"context"
	"strings"
	"testing"

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
	if len(res) != 0 {
		t.Errorf("cancelled context should run no checks, got %+v", res)
	}
}
