package state

import (
	"context"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

func scheduleFixture(id, name string, kind domain.ScheduleKind) *domain.Schedule {
	sc := &domain.Schedule{
		ID: domain.ScheduleID(id), Name: name, Kind: kind,
		Cron: "0 * * * *", Prompt: "check CI, keep going",
		CreatedAt: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC),
	}
	if kind == domain.ScheduleHeartbeat {
		sc.Target = "FF-001"
	} else {
		sc.Envelope = 50
	}
	return sc
}

// TestScheduleCreateReadsDisabled: off by default is the feature's own
// rule — a definition is inserted disabled whatever the caller's struct
// said, with nothing armed and nothing requested.
func TestScheduleCreateReadsDisabled(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sc := scheduleFixture("nightly", "nightly triage", domain.ScheduleMint)
	sc.Enabled = true
	sc.RunRequested = true
	sc.NextRun = time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	got, err := s.Schedule(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.RunRequested || !got.NextRun.IsZero() {
		t.Errorf("a new schedule reads enabled=%v requested=%v next=%v; want all off", got.Enabled, got.RunRequested, got.NextRun)
	}
	if got.Cron != "0 * * * *" || got.Prompt != sc.Prompt || got.Envelope != 50 {
		t.Errorf("the definition did not survive the write: %+v", got)
	}
}

// TestScheduleWriteRefusesBadCadence: the store is the enforcement point,
// so no face can write around the cron package. A never-matching
// expression and an unknown zone are both refused, on create and on edit.
func TestScheduleWriteRefusesBadCadence(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sc := scheduleFixture("nightly", "nightly triage", domain.ScheduleMint)
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cron, tz string
	}{
		{"0 0 30 2 *", ""},
		{"* * * * *", "Nowhere/Nothing"},
		{"not a cron", ""},
	} {
		bad := scheduleFixture("nightly", "nightly triage", domain.ScheduleMint)
		bad.Cron, bad.Timezone = tc.cron, tc.tz
		if err := s.CreateSchedule(ctx, bad); err == nil {
			t.Errorf("CreateSchedule(%q, %q) = ok, want refused", tc.cron, tc.tz)
		}
		if err := s.UpdateScheduleDefinition(ctx, bad); err == nil {
			t.Errorf("UpdateScheduleDefinition(%q, %q) = ok, want refused", tc.cron, tc.tz)
		}
	}
	// The row is untouched by the refused edits.
	got, err := s.Schedule(ctx, sc.ID)
	if err != nil || got.Cron != "0 * * * *" {
		t.Errorf("a refused edit changed the row: %v, %q", err, got.Cron)
	}
}

func TestScheduleValidateRefusalsPersist(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sc := scheduleFixture("mint", "mint", domain.ScheduleMint)
	sc.Envelope = 0 // a mint always carries a brake
	if err := s.CreateSchedule(ctx, sc); err == nil {
		t.Error("CreateSchedule accepted a mint with no envelope")
	}
}

// TestScheduleClaimCAS: one due instant produces at most one fire. The
// second claim with the same expectNext gets false; a non-forced claim
// on a disabled row gets false; a claim advances next_run and clears a
// pending run request in the same write.
func TestScheduleClaimCAS(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sc := scheduleFixture("hourly", "hourly", domain.ScheduleHeartbeat)
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	if err := s.SetScheduleEnabled(ctx, sc.ID, true, first); err != nil {
		t.Fatal(err)
	}
	next := time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC)
	ok, err := s.ClaimScheduleFire(ctx, sc.ID, first, next, false)
	if err != nil || !ok {
		t.Fatalf("first claim = %v, %v; want claimed", ok, err)
	}
	got, _ := s.Schedule(ctx, sc.ID)
	if !got.NextRun.Equal(next) || got.RunRequested {
		t.Errorf("after claim: next=%v requested=%v", got.NextRun, got.RunRequested)
	}
	ok, err = s.ClaimScheduleFire(ctx, sc.ID, first, next, false)
	if err != nil || ok {
		t.Errorf("second claim on the same instant = %v, %v; want false", ok, err)
	}
	// A non-forced claim on a disabled row does nothing.
	if err := s.SetScheduleEnabled(ctx, sc.ID, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	ok, err = s.ClaimScheduleFire(ctx, sc.ID, next, next.Add(time.Hour), false)
	if err != nil || ok {
		t.Errorf("claim on a disabled row = %v, %v; want false", ok, err)
	}
	// An enabled row refuses a zero next run.
	if err := s.SetScheduleEnabled(ctx, sc.ID, true, next); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimScheduleFire(ctx, sc.ID, next, time.Time{}, false); err == nil {
		t.Error("an enabled claim accepted a zero next run")
	}
	// A run request survives a read but is cleared by a claim — the
	// run-now that lands while a cadence fire is due is served by it.
	if err := s.RequestScheduleRun(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	later := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	ok, err = s.ClaimScheduleFire(ctx, sc.ID, next, later, false)
	if err != nil || !ok {
		t.Fatalf("claim with a pending request = %v, %v; want claimed", ok, err)
	}
	if got, _ = s.Schedule(ctx, sc.ID); got.RunRequested {
		t.Error("the claim left run_requested set")
	}
}

// TestScheduleDisableClearsRunRequested: no disable path may leave a
// pending run request behind — a request that survived a disable would
// make the next enable fire at once, off-cadence, with the envelope.
func TestScheduleDisableClearsRunRequested(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sc := scheduleFixture("hourly", "hourly", domain.ScheduleHeartbeat)
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	if err := s.SetScheduleEnabled(ctx, sc.ID, true, first); err != nil {
		t.Fatal(err)
	}
	// An explicit enable clears a stale request.
	if _, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET run_requested = 1 WHERE id = ?`, string(sc.ID)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetScheduleEnabled(ctx, sc.ID, true, first); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Schedule(ctx, sc.ID); got.RunRequested {
		t.Error("enabling left a stale run request in place")
	}
	// Disabling clears it too.
	if err := s.RequestScheduleRun(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetScheduleEnabled(ctx, sc.ID, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Schedule(ctx, sc.ID); got.Enabled || got.RunRequested || !got.NextRun.IsZero() {
		t.Errorf("a disabled row reads enabled=%v requested=%v next=%v", got.Enabled, got.RunRequested, got.NextRun)
	}
	// An edit (the web form) forces the schedule off and clears it.
	if err := s.RequestScheduleRun(ctx, sc.ID); err == nil {
		t.Fatal("RequestScheduleRun on a disabled row must be refused")
	}
	if err := s.SetScheduleEnabled(ctx, sc.ID, true, first); err != nil {
		t.Fatal(err)
	}
	if err := s.RequestScheduleRun(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	edited := scheduleFixture("hourly", "hourly", domain.ScheduleHeartbeat)
	edited.Cron = "*/5 * * * *"
	if err := s.UpdateScheduleDefinition(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Schedule(ctx, sc.ID); got.Enabled || got.RunRequested || !got.NextRun.IsZero() {
		t.Errorf("an edit left the row enabled=%v requested=%v next=%v", got.Enabled, got.RunRequested, got.NextRun)
	}
}

// TestScheduleRequestRunRefusesDisabled: the CLI's run-now is refused on
// a disabled row, at the write, with the instruction to enable it first.
func TestScheduleRequestRunRefusesDisabled(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sc := scheduleFixture("nightly", "nightly triage", domain.ScheduleMint)
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	err := s.RequestScheduleRun(ctx, sc.ID)
	if err == nil {
		t.Fatal("RequestScheduleRun on a disabled row = ok")
	}
	if got, gerr := s.Schedule(ctx, sc.ID); gerr != nil || got.Enabled {
		t.Error("the refused request enabled the row")
	}
	if _, serr := s.Schedule(ctx, "nope"); serr == nil {
		t.Error("Schedule on a missing id = ok")
	}
}

// TestScheduleDisablingOutcomeClearsRunRequested: a CLI run-now landing
// between the claim (which cleared the flag) and a disabling outcome
// leaves the row disabled with the request cleared — so re-enabling it
// fires on the cadence, never at once.
func TestScheduleDisablingOutcomeClearsRunRequested(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sc := scheduleFixture("hourly", "hourly", domain.ScheduleHeartbeat)
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	if err := s.SetScheduleEnabled(ctx, sc.ID, true, first); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ClaimScheduleFire(ctx, sc.ID, first, first.Add(time.Hour), false)
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	// The run-now lands here: after the claim, before the outcome.
	if err := s.RequestScheduleRun(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	// The fire comes back with the target's envelope exhausted.
	if err := s.RecordScheduleOutcome(ctx, sc.ID, domain.ScheduleOutcome{
		At:     time.Date(2026, 10, 3, 10, 0, 30, 0, time.UTC),
		Status: domain.SchedulePausedExhausted,
		Detail: "FF-001 has spent its envelope",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Schedule(ctx, sc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.RunRequested || !got.NextRun.IsZero() {
		t.Errorf("after a disabling outcome: enabled=%v requested=%v next=%v; want all off",
			got.Enabled, got.RunRequested, got.NextRun)
	}
	// Re-enabling fires on the cadence: the enable writes the fresh next
	// run and the stale request is still gone.
	if err := s.SetScheduleEnabled(ctx, sc.ID, true, first.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Schedule(ctx, sc.ID); got.RunRequested {
		t.Error("the enable inherited the cleared request")
	}
}

// TestScheduleOutcomeOverwritesOrphan: orphan_card is written by every
// outcome — a failed outcome with no orphan clears it, and last_card
// keeps its display value across a fire that minted nothing.
func TestScheduleOutcomeOverwritesOrphan(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sc := scheduleFixture("mint", "mint", domain.ScheduleMint)
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	if err := s.SetScheduleEnabled(ctx, sc.ID, true, first); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ClaimScheduleFire(ctx, sc.ID, first, first.Add(time.Hour), false)
	if err != nil || !ok {
		t.Fatalf("claim = %v, %v", ok, err)
	}
	at := time.Date(2026, 10, 3, 10, 0, 5, 0, time.UTC)
	if err := s.RecordScheduleOutcome(ctx, sc.ID, domain.ScheduleOutcome{
		At: at, Status: domain.ScheduleFailed, Detail: "kickoff refused",
		Card: "FF-002", Orphan: "FF-002",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Schedule(ctx, sc.ID)
	if got.LastCard != "FF-002" || got.OrphanCard != "FF-002" || got.LastStatus != domain.ScheduleFailed {
		t.Errorf("the failing outcome: card=%q orphan=%q status=%q", got.LastCard, got.OrphanCard, got.LastStatus)
	}
	if !got.LastRun.Equal(at) {
		t.Errorf("last run = %s, want %s", got.LastRun, at)
	}
	// The next outcome is a failure before the mint: no card, and the
	// orphan pointer goes with it. last_card keeps FF-002 for display.
	ok, err = s.ClaimScheduleFire(ctx, sc.ID, first.Add(time.Hour), first.Add(2*time.Hour), false)
	if err != nil || !ok {
		t.Fatalf("second claim = %v, %v", ok, err)
	}
	if err := s.RecordScheduleOutcome(ctx, sc.ID, domain.ScheduleOutcome{
		At:     at.Add(time.Hour),
		Status: domain.ScheduleFailed,
		Detail: "repository \"x\" is not configured",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Schedule(ctx, sc.ID)
	if got.OrphanCard != "" {
		t.Errorf("a pre-mint failure left orphan_card %q; want empty", got.OrphanCard)
	}
	if got.LastCard != "FF-002" {
		t.Errorf("last_card lost its display value: %q", got.LastCard)
	}
	// A successful outcome names the new card and leaves the pointer
	// empty.
	ok, err = s.ClaimScheduleFire(ctx, sc.ID, first.Add(2*time.Hour), first.Add(3*time.Hour), false)
	if err != nil || !ok {
		t.Fatalf("third claim = %v, %v", ok, err)
	}
	if err := s.RecordScheduleOutcome(ctx, sc.ID, domain.ScheduleOutcome{
		At: at.Add(2 * time.Hour), Status: domain.ScheduleOK, Card: "FF-003",
	}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Schedule(ctx, sc.ID)
	if got.LastCard != "FF-003" || got.OrphanCard != "" || got.LastStatus != domain.ScheduleOK {
		t.Errorf("after a good fire: card=%q orphan=%q status=%q", got.LastCard, got.OrphanCard, got.LastStatus)
	}
}

// TestScheduleForcedClaimOnDisabledRow: a person's run-now from the
// board (RunSchedule) claims with force; the row stays disabled with its
// next run empty, and the outcome leaves it that way.
func TestScheduleForcedClaimOnDisabledRow(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	sc := scheduleFixture("nightly", "nightly triage", domain.ScheduleMint)
	if err := s.CreateSchedule(ctx, sc); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ClaimScheduleFire(ctx, sc.ID, time.Time{}, time.Time{}, true)
	if err != nil || !ok {
		t.Fatalf("forced claim on a disabled row = %v, %v; want claimed", ok, err)
	}
	got, _ := s.Schedule(ctx, sc.ID)
	if got.Enabled || !got.NextRun.IsZero() {
		t.Errorf("a forced fire armed the row: enabled=%v next=%v", got.Enabled, got.NextRun)
	}
	if err := s.DeleteSchedule(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Schedule(ctx, sc.ID); err == nil {
		t.Error("the row survived its delete")
	}
}

// TestScheduleListOrder: rows come back oldest first, both kinds and
// every state.
func TestScheduleListOrder(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a := scheduleFixture("a-older", "a", domain.ScheduleMint)
	b := scheduleFixture("b-newer", "b", domain.ScheduleHeartbeat)
	b.CreatedAt = a.CreatedAt.Add(time.Minute)
	if err := s.CreateSchedule(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSchedule(ctx, b); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("list = %v, want [%s %s]", scheduleIDs(list), a.ID, b.ID)
	}
}

func scheduleIDs(list []domain.Schedule) []string {
	out := make([]string, len(list))
	for i, sc := range list {
		out[i] = string(sc.ID)
	}
	return out
}
