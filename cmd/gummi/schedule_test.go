package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

// scheduleFixture builds a temp repo (gummi initialized), chdirs the test
// into it, and opens the store for direct assertions.
func scheduleFixture(t *testing.T) *state.Store {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	git := func(args ...string) {
		t.Helper()
		out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")
	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestScheduleCLIRoundTrip: add stores the definition off (a mint always
// with an envelope, the preset compiled to cron), enable arms the first
// fire, run-now refuses a disabled row and marks an enabled one, disable
// turns it off, rm removes it, and list reads them back as JSON.
func TestScheduleCLIRoundTrip(t *testing.T) {
	store := scheduleFixture(t)
	ctx := context.Background()

	// A mint with a preset cadence: stored off, cron canonical.
	if err := runCLI("schedule", "add", "--name", "nightly triage", "--every", "1h",
		"--prompt", "triage new issues", "--envelope", "0.50"); err != nil {
		t.Fatalf("add: %v", err)
	}
	sc, err := store.Schedule(ctx, "nightly-triage")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Enabled || sc.Cron != "0 * * * *" || sc.Envelope != 50 || sc.Kind != domain.ScheduleMint {
		t.Errorf("added row = enabled=%v cron=%q envelope=%d kind=%s; want off, compiled, 50, mint",
			sc.Enabled, sc.Cron, sc.Envelope, sc.Kind)
	}

	// --json marshals the web page's shape.
	raw := captureStdout(t, func() {
		if err := runCLI("schedule", "list", "--json"); err != nil {
			t.Fatal(err)
		}
	})
	var list webapi.Schedules
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("list --json is not a schedules shape: %v\n%s", err, raw)
	}
	if len(list.Schedules) != 1 || list.Schedules[0].Enabled || list.Schedules[0].Cron != "0 * * * *" {
		t.Fatalf("list = %+v", list.Schedules)
	}

	// run-now on a disabled row: refused, naming the enable.
	err = runCLI("schedule", "run-now", "nightly triage")
	if err == nil || !strings.Contains(err.Error(), "enable it first") {
		t.Fatalf("run-now on a disabled row = %v, want an enable-it-first refusal", err)
	}

	// Enable: the first fire is computed from now.
	if err := runCLI("schedule", "enable", "nightly-triage"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	sc, _ = store.Schedule(ctx, "nightly-triage")
	if !sc.Enabled || sc.NextRun.IsZero() || sc.RunRequested {
		t.Errorf("enabled row = enabled=%v next=%v requested=%v", sc.Enabled, sc.NextRun, sc.RunRequested)
	}
	// The first fire is the cadence's next mark, inside an hour for an
	// hourly schedule.
	if d := time.Until(sc.NextRun); d <= 0 || d > time.Hour+time.Minute {
		t.Errorf("next run = %s (%s out), want the next hourly mark", sc.NextRun, d)
	}

	// run-now on an enabled row: the request lands for the board's tick.
	if err := runCLI("schedule", "run-now", "nightly-triage"); err != nil {
		t.Fatalf("run-now: %v", err)
	}
	sc, _ = store.Schedule(ctx, "nightly-triage")
	if !sc.RunRequested {
		t.Error("run-now did not set the request the board's tick serves")
	}

	// Disable clears it.
	if err := runCLI("schedule", "disable", "nightly-triage"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	sc, _ = store.Schedule(ctx, "nightly-triage")
	if sc.Enabled || sc.RunRequested || !sc.NextRun.IsZero() {
		t.Errorf("disabled row = enabled=%v requested=%v next=%v", sc.Enabled, sc.RunRequested, sc.NextRun)
	}

	// A heartbeat against a freeform card.
	id, _ := domain.NewID(domain.KindFreeform, 1)
	slug, _ := domain.Slugify("tidy the readme")
	now := time.Now()
	f := domain.Feature{
		ID: id, Num: 1, Kind: domain.KindFreeform, Title: "Tidy the README",
		Slug: slug, Stage: domain.StageOpen, Budget: domain.Budget{Envelope: 400},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if err := runCLI("schedule", "add", "--name", "keep tidy", "--every", "15m",
		"--prompt", "check the README", "--heartbeat", "FF-001"); err != nil {
		t.Fatalf("heartbeat add: %v", err)
	}
	sc, err = store.Schedule(ctx, "keep-tidy")
	if err != nil {
		t.Fatal(err)
	}
	if sc.Kind != domain.ScheduleHeartbeat || sc.Target != "FF-001" || sc.Envelope != 0 || sc.Repo != "" {
		t.Errorf("heartbeat row = %+v", sc)
	}

	// rm.
	if err := runCLI("schedule", "rm", "keep-tidy"); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if _, err := store.Schedule(ctx, "keep-tidy"); err == nil {
		t.Error("the row survived rm")
	}
}

// TestScheduleCLIRefusals: a cadence the cron package refuses, a
// non-freeform heartbeat target, and a mint without an envelope are all
// refused with an exit non-zero.
func TestScheduleCLIRefusals(t *testing.T) {
	store := scheduleFixture(t)
	ctx := context.Background()

	// A cron no month can match.
	err := runCLI("schedule", "add", "--name", "x", "--cron", "0 0 30 2 *", "--prompt", "p", "--envelope", "5")
	if err == nil || !strings.Contains(err.Error(), "0 0 30 2 *") {
		t.Fatalf("bad cron = %v, want a refusal naming the expression", err)
	}
	// An unknown zone.
	if err := runCLI("schedule", "add", "--name", "x", "--cron", "0 0 * * *", "--tz", "Nowhere/Nothing", "--prompt", "p", "--envelope", "5"); err == nil {
		t.Fatal("an unknown zone was accepted")
	}
	// A mint without an envelope.
	if err := runCLI("schedule", "add", "--name", "x", "--cron", "0 0 * * *", "--prompt", "p"); err == nil {
		t.Fatal("a mint without an envelope was accepted")
	}
	// A heartbeat against a non-freeform card.
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "x", Slug: "x", Stage: domain.StageTodo, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	err = runCLI("schedule", "add", "--name", "hb", "--every", "5m", "--prompt", "p", "--heartbeat", "FD-001")
	if err == nil || !strings.Contains(err.Error(), "freeform") {
		t.Fatalf("heartbeat against FD-001 = %v, want a freeform refusal", err)
	}
	// A heartbeat with an envelope.
	err = runCLI("schedule", "add", "--name", "hb", "--every", "5m", "--prompt", "p", "--heartbeat", "FF-001", "--envelope", "5")
	if err == nil {
		t.Fatal("a heartbeat with an envelope was accepted")
	}
}

// TestScheduleCLINameResolution: a schedule is named by id or by the
// name it was added under.
func TestScheduleCLINameResolution(t *testing.T) {
	store := scheduleFixture(t)

	if err := runCLI("schedule", "add", "--name", "Nightly Triage", "--every", "1h", "--prompt", "p", "--envelope", "5"); err != nil {
		t.Fatal(err)
	}
	// The display name with case and spaces resolves to its slug.
	if err := runCLI("schedule", "enable", "Nightly Triage"); err != nil {
		t.Fatalf("enable by name: %v", err)
	}
	if sc, err := store.Schedule(context.Background(), "nightly-triage"); err != nil || !sc.Enabled {
		t.Errorf("the enabled row = %v, %v", sc, err)
	}
	// An arg that names nothing says so.
	if err := runCLI("schedule", "rm", "ghost"); err == nil {
		t.Error("rm resolved a schedule that does not exist")
	}
}
