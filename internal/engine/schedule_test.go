package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// turnRecorder turns with a fixed reply and records every turn text it
// was sent, so a test can assert what a schedule's fire delivered.
type turnRecorder struct {
	*agent.Fake
	mu   sync.Mutex
	sent []string
}

func newTurnRecorder(reply string) *turnRecorder {
	ag := &turnRecorder{Fake: agent.NewFake(reply)}
	ag.Responder = func(_ agent.SessionOpts, msg string) []agent.Event {
		ag.mu.Lock()
		ag.sent = append(ag.sent, msg)
		ag.mu.Unlock()
		return []agent.Event{{Kind: agent.EventMessage, Text: reply}, {Kind: agent.EventIdle}}
	}
	return ag
}

func (ag *turnRecorder) sentTurns() []string {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return append([]string(nil), ag.sent...)
}

// scheduleEngine is twoRepoEngine over an agent the test chooses, so a
// mint test can record what its fire delivered and can set the fake's
// refusals between ticks.
func scheduleEngine(t *testing.T, ag agent.Agent) *Engine {
	t.Helper()
	e := twoRepoEngine(t)
	e.cfg.Agents = singleAgent(ag)
	return e
}

func scheduleFixture(t *testing.T, store *state.Store, sc domain.Schedule, at time.Time) {
	t.Helper()
	if err := store.CreateSchedule(context.Background(), &sc); err != nil {
		t.Fatal(err)
	}
	if err := store.SetScheduleEnabled(context.Background(), sc.ID, true, at); err != nil {
		t.Fatal(err)
	}
}

func userTurnCount(t *testing.T, e *Engine, id domain.FeatureID) int {
	t.Helper()
	ff := e.Freeform(id)
	if ff == nil {
		return 0
	}
	n := 0
	for _, m := range ff.Snapshot().Transcript {
		if m.Author == AuthorUser {
			n++
		}
	}
	return n
}

func lastUserTurn(t *testing.T, e *Engine, id domain.FeatureID) (string, string) {
	t.Helper()
	ff := e.Freeform(id)
	if ff == nil {
		t.Fatalf("%s has no freeform session", id)
	}
	snap := ff.Snapshot()
	for i := len(snap.Transcript) - 1; i >= 0; i-- {
		m := snap.Transcript[i]
		if m.Author == AuthorUser {
			return m.Content, m.By
		}
	}
	t.Fatalf("%s's transcript has no user turn:\n%v", id, snap.Transcript)
	return "", ""
}

func mintSchedule(repo string) domain.Schedule {
	return domain.Schedule{
		ID: "nightly", Name: "nightly triage", Kind: domain.ScheduleMint,
		Repo: repo, Cron: "0 * * * *", Prompt: "triage new issues", Envelope: 50,
	}
}

func hourlyHeartbeat(target domain.FeatureID) domain.Schedule {
	return domain.Schedule{
		ID: "hourly", Name: "hourly", Kind: domain.ScheduleHeartbeat,
		Target: target, Cron: "0 * * * *", Prompt: "check CI, keep going",
	}
}

// tenPast returns the instant one cadence mark past at, which is when a
// schedule armed at at becomes due again in a test's hands.
func tenPast(at time.Time) time.Time { return at.Add(10 * time.Minute) }

// nextDue returns the instant just past the row's current next run, so a
// second tick in a test is due whatever cadence the first fire armed.
func nextDue(t *testing.T, store *state.Store, id domain.ScheduleID) time.Time {
	t.Helper()
	row, err := store.Schedule(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return row.NextRun.Add(time.Minute)
}

// deleteCard takes a card off the board the way the board itself does:
// its worktree and branch go with it, then the record. A missing-orphan
// test needs the branch gone too, or the next mint refuses to cut the
// same name.
func deleteCard(t *testing.T, e *Engine, id domain.FeatureID) error {
	t.Helper()
	ctx := context.Background()
	f, err := e.cfg.Store.GetFeature(ctx, id)
	if err != nil {
		return err
	}
	wt, err := e.pool.ManagerFor(ctx, &f)
	if err != nil {
		return err
	}
	if ok, err := wt.Exists(ctx, &f); err == nil && ok {
		if err := wt.Remove(ctx, &f, true); err != nil {
			return err
		}
	}
	if ok, err := wt.BranchExists(ctx, &f); err == nil && ok {
		if err := wt.DeleteBranch(ctx, &f, true); err != nil {
			return err
		}
	}
	return e.cfg.Store.DeleteFeature(ctx, id)
}

func TestScheduleTickHeartbeatDeliversOnce(t *testing.T) {
	ctx := context.Background()
	ag := newTurnRecorder("looking")
	e := newEngine(t, ag)
	f := freeformCard(1, "watch the deploy")
	createFeature(t, e.cfg.Store, f)

	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, hourlyHeartbeat(f.ID), next)

	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("fires = %v, want one ok fire", fires)
	}
	waitFreeformIdle(t, e.Freeform(f.ID))
	got, by := lastUserTurn(t, e, f.ID)
	if got != "check CI, keep going" {
		t.Errorf("the delivered turn = %q, want the schedule's prompt", got)
	}
	if by != "schedule:hourly" {
		t.Errorf("the turn's actor = %q, want schedule:hourly", by)
	}
	if turns := ag.sentTurns(); len(turns) != 1 || turns[0] != "check CI, keep going" {
		t.Errorf("the backend saw %v, want exactly the prompt once", turns)
	}
	// The cadence advanced past now, so the same tick again is quiet.
	row, err := e.cfg.Store.Schedule(ctx, "hourly")
	if err != nil {
		t.Fatal(err)
	}
	if !row.NextRun.Equal(time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("next run = %s, want 11:00", row.NextRun)
	}
	if again, err := e.ScheduleTick(ctx, next.Add(time.Minute)); err != nil || len(again) != 0 {
		t.Errorf("a second tick at the same instant fired %v (%v)", again, err)
	}
}

// TestScheduleTickFallBackDayDoesNotRefire pins the clock's own shape
// against the fire loop: on fall-back day, a now inside the second pass
// of a repeated hour (01:00 EST, the second of the two) must arm a
// next_run strictly after itself. When Next answered on the wall clock
// alone, Advance armed the first pass of the repeated hour — an instant
// before now — and Due then refired the row on every poll for the whole
// repeated hour: ~240 turn attempts for a minute-cadence heartbeat.
func TestScheduleTickFallBackDayDoesNotRefire(t *testing.T) {
	if _, err := time.LoadLocation("America/New_York"); err != nil {
		t.Skip("no tzdata on this host:", err)
	}
	ctx := context.Background()
	ag := newTurnRecorder("looking")
	e := newEngine(t, ag)
	f := freeformCard(1, "watch the deploy")
	createFeature(t, e.cfg.Store, f)

	// 2026-11-01: 06:00Z is 01:00 EST — the second pass of the wall
	// minute 01:00, an hour after its first (EDT) occurrence.
	secondPass := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	s := hourlyHeartbeat(f.ID)
	s.ID = "minute"
	s.Cron = "*/1 * * * *"
	s.Timezone = "America/New_York"
	scheduleFixture(t, e.cfg.Store, s, secondPass)

	fires, err := e.ScheduleTick(ctx, secondPass)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("fires = %v, want one ok fire", fires)
	}
	waitFreeformIdle(t, e.Freeform(f.ID))
	row, err := e.cfg.Store.Schedule(ctx, "minute")
	if err != nil {
		t.Fatal(err)
	}
	if want := secondPass.Add(time.Minute); !row.NextRun.Equal(want) {
		t.Errorf("next run = %s, want %s (01:01 EST, the second pass)", row.NextRun, want)
	}
	if !row.NextRun.After(secondPass) {
		t.Errorf("next run = %s, which is not after now (%s): Due would refire the row on the next poll", row.NextRun, secondPass)
	}
	// Halfway to the next cadence mark the row is quiet: the answer the
	// bug armed was in the past, and this is the tick that re-fired.
	if again, err := e.ScheduleTick(ctx, secondPass.Add(30*time.Second)); err != nil || len(again) != 0 {
		t.Fatalf("a tick before the armed next run fired %v (%v)", again, err)
	}
	// At the armed mark it fires again, exactly once.
	fires, err = e.ScheduleTick(ctx, row.NextRun)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("fires = %v, want one ok fire at the armed mark", fires)
	}
	waitFreeformIdle(t, e.Freeform(f.ID))
	if n := userTurnCount(t, e, f.ID); n != 2 {
		t.Errorf("the transcript holds %d user turns after two cadence marks, want 2", n)
	}
	if turns := ag.sentTurns(); len(turns) != 2 {
		t.Errorf("the backend saw %d turns, want 2 (one per cadence mark, none per poll)", len(turns))
	}
}

// TestScheduleTickHeartbeatBusySkips: a fire against a target whose
// session is working right now is skipped, not queued — nothing is
// delivered, and the row records skipped-busy without a notification.
func TestScheduleTickHeartbeatBusySkips(t *testing.T) {
	ctx := context.Background()
	hold := make(chan struct{})
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
		<-hold
		return []agent.Event{{Kind: agent.EventMessage, Text: "still here"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{Resume: true, UsageEvents: true, Interrupt: true}
	e := newEngine(t, ag)
	f := freeformCard(1, "long migration")
	createFeature(t, e.cfg.Store, f)

	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, hourlyHeartbeat(f.ID), next)

	// Someone is using the session: its one turn is still in flight.
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "start the migration"); err != nil {
		t.Fatal(err)
	}

	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleSkippedBusy {
		t.Fatalf("fires = %v, want one skipped-busy fire", fires)
	}
	// Nothing was delivered beyond the person's own turn: the transcript
	// holds exactly one user line, the one the person typed.
	if got, _ := lastUserTurn(t, e, f.ID); got != "start the migration" {
		t.Errorf("the last user turn = %q; the fire delivered nothing", got)
	}
	if n := userTurnCount(t, e, f.ID); n != 1 {
		t.Errorf("the transcript holds %d user turns; the fire delivered nothing", n)
	}
	row, _ := e.cfg.Store.Schedule(ctx, "hourly")
	if !row.Enabled {
		t.Error("a skipped fire turned the schedule off")
	}
	close(hold)
	waitFreeformIdle(t, ff)
}

// TestScheduleTickHeartbeatExhaustedPauses: an exhausted target pauses
// the schedule — it disables the row and leaves the card's envelope
// untouched.
func TestScheduleTickHeartbeatExhaustedPauses(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t, agent.NewFake("ack"))
	f := freeformCard(1, "done spending")
	f.Budget = domain.Budget{Envelope: 10}
	createFeature(t, e.cfg.Store, f)
	// Every credit of the envelope is spent, as the store holds it.
	if err := e.cfg.Store.AddSpend(ctx, f.ID, 10, 0, 0, 0); err != nil {
		t.Fatal(err)
	}

	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, hourlyHeartbeat(f.ID), next)

	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.SchedulePausedExhausted {
		t.Fatalf("fires = %v, want one paused-exhausted fire", fires)
	}
	row, _ := e.cfg.Store.Schedule(ctx, "hourly")
	if row.Enabled {
		t.Error("an exhausted target must pause the schedule")
	}
	cur, err := e.cfg.Store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Budget.Envelope != 10 || cur.Spend.Credits != 10 {
		t.Errorf("the card's envelope changed: %v spend %v", cur.Budget, cur.Spend.Credits)
	}
	if ff := e.Freeform(f.ID); ff != nil {
		t.Error("an exhausted fire opened a session it must not")
	}
}

// TestScheduleTickHeartbeatClosedTargetDisables: a heartbeat whose target
// is done has nothing to send to; the schedule turns itself off.
func TestScheduleTickHeartbeatClosedTargetDisables(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t, agent.NewFake("ack"))
	f := freeformCard(1, "landed and closed")
	f.Stage = domain.StageDone
	createFeature(t, e.cfg.Store, f)

	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, hourlyHeartbeat(f.ID), next)

	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleDisabledTargetClosed {
		t.Fatalf("fires = %v, want one disabled-target-closed fire", fires)
	}
	if row, _ := e.cfg.Store.Schedule(ctx, "hourly"); row.Enabled {
		t.Error("a closed target must disable the schedule")
	}
}

// TestScheduleTickDisabledFiresNothing: a disabled row is never due, and
// the poll fires nothing on it — run-now on a disabled row is refused at
// the store, and the only forced fire is RunSchedule (a person at the
// board), which the pure policy's TestDueRequiresEnabled already fences.
func TestScheduleTickDisabledFiresNothing(t *testing.T) {
	ctx := context.Background()
	ag := newTurnRecorder("ack")
	e := newEngine(t, ag)
	f := freeformCard(1, "watch the deploy")
	createFeature(t, e.cfg.Store, f)

	if err := e.cfg.Store.CreateSchedule(ctx, func() *domain.Schedule {
		s := hourlyHeartbeat(f.ID)
		return &s
	}()); err != nil {
		t.Fatal(err)
	}
	fires, err := e.ScheduleTick(ctx, time.Date(2027, 10, 3, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 0 {
		t.Errorf("a disabled row fired %v", fires)
	}
	if ag.sentTurns() != nil {
		t.Errorf("the backend saw %v; a disabled row must not speak", ag.sentTurns())
	}
}

// TestScheduleTickHeartbeatReopensClosedSession: a board restart leaves
// the conversation on the row; the next fire's open brings it back and
// the turn lands in the same conversation, not a fresh one.
func TestScheduleTickHeartbeatReopensClosedSession(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := newRepo(t)
	ag1 := newTurnRecorder("before the restart")
	e1 := New(Config{Agents: singleAgent(ag1), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	f := freeformCard(1, "long migration")
	createFeature(t, store, f)
	ff, err := e1.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "start the migration"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	e1.Close()

	ag2 := newTurnRecorder("after the restart")
	e2 := New(Config{Agents: singleAgent(ag2), Store: store, Worktrees: wt, Workspace: ws, Model: "m", Persist: true})
	t.Cleanup(func() { e2.Close() })
	if err := e2.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	if e2.Freeform(f.ID) == nil {
		t.Fatal("the restored board has no session for the card")
	}

	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, store, hourlyHeartbeat(f.ID), next)

	fires, err := e2.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("fires = %v, want one ok fire", fires)
	}
	waitFreeformIdle(t, e2.Freeform(f.ID))
	if turns := ag2.sentTurns(); len(turns) != 1 || turns[0] != "check CI, keep going" {
		t.Errorf("the reopened backend saw %v, want exactly the heartbeat turn", turns)
	}
}

func TestScheduleTickMint(t *testing.T) {
	ctx := context.Background()
	ag := newTurnRecorder("on it")
	e := scheduleEngine(t, ag)
	// twoRepoEngine wires no default repo: the schedule names "a".
	sc := mintSchedule("a")
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, sc, next)

	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("fires = %v, want one ok fire", fires)
	}
	minted := fires[0].Outcome.Card
	if minted == "" || minted.Kind() != domain.KindFreeform {
		t.Fatalf("the fire minted %q, want an FF card", minted)
	}
	f, err := e.cfg.Store.GetFeature(ctx, minted)
	if err != nil {
		t.Fatal(err)
	}
	if f.Budget.Envelope != 50 {
		t.Errorf("the minted card's envelope = %d, want the schedule's 50", f.Budget.Envelope)
	}
	if f.Repo != "a" {
		t.Errorf("the minted card's repo = %q, want a", f.Repo)
	}
	if f.MainCheckout {
		t.Error("a scheduled card must get its own worktree, not the main checkout")
	}
	row, _ := e.cfg.Store.Schedule(ctx, "nightly")
	if row.LastCard != minted || row.OrphanCard != "" {
		t.Errorf("row after a good fire: card=%q orphan=%q", row.LastCard, row.OrphanCard)
	}
	waitFreeformIdle(t, e.Freeform(minted))
	if turns := ag.sentTurns(); len(turns) != 1 || turns[0] != "triage new issues" {
		t.Errorf("the minted card saw %v, want the prompt as its opening turn", turns)
	}
}

// TestScheduleTickMintBusyLastCardSkips: an older card whose session is
// working right now makes the fire skip, not mint a second one.
func TestScheduleTickMintBusyLastCardSkips(t *testing.T) {
	ctx := context.Background()
	hold := make(chan struct{})
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, _ string) []agent.Event {
		<-hold
		return []agent.Event{{Kind: agent.EventMessage, Text: "working"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{Resume: true, UsageEvents: true, Interrupt: true}
	e := scheduleEngine(t, ag)
	sc := mintSchedule("a")
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, sc, next)

	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	first := fires[0].Outcome.Card
	ff := e.Freeform(first)
	if err := ff.Send(ctx, "keep working"); err != nil {
		t.Fatal(err)
	}
	// The next cadence mark is due at 11:00; the person's turn is still
	// in flight.
	fires, err = e.ScheduleTick(ctx, nextDue(t, e.cfg.Store, sc.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleSkippedBusy {
		t.Fatalf("second fire = %v, want one skipped-busy fire", fires)
	}
	close(hold)
	waitFreeformIdle(t, ff)
	// The skip left the row pointing at the first card, with nothing new.
	row, _ := e.cfg.Store.Schedule(ctx, "nightly")
	if row.LastCard != first || row.OrphanCard != "" {
		t.Errorf("after the skip: card=%q orphan=%q", row.LastCard, row.OrphanCard)
	}
}

// TestScheduleTickMintIdleLastCardMintsAgain: a cadence mints a card per
// fire — the same description every time, with nobody present to retitle
// a collision, so the second card takes the next free variant instead of
// refusing to cut its branch.
func TestScheduleTickMintIdleLastCardMintsAgain(t *testing.T) {
	ctx := context.Background()
	e := scheduleEngine(t, newTurnRecorder("on it"))
	sc := mintSchedule("a")
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, sc, next)

	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	first := fires[0].Outcome.Card
	waitFreeformIdle(t, e.Freeform(first))
	fires, err = e.ScheduleTick(ctx, nextDue(t, e.cfg.Store, sc.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("second fire = %v, want one ok fire", fires)
	}
	second := fires[0].Outcome.Card
	if second == "" || second == first {
		t.Fatalf("the second fire minted %q, want a new card", second)
	}
	a, _ := e.cfg.Store.GetFeature(ctx, first)
	b, _ := e.cfg.Store.GetFeature(ctx, second)
	if a.Slug == b.Slug {
		t.Errorf("both cards take slug %q; the second must have a variant", a.Slug)
	}
}

// TestScheduleTickMintUnconfiguredRepoFailed: a mint against a
// repository the workspace does not configure records failed and mints
// nothing — the schedule stays enabled for the next cadence.
func TestScheduleTickMintUnconfiguredRepoFailed(t *testing.T) {
	ctx := context.Background()
	e := twoRepoEngine(t)
	sc := mintSchedule("nowhere")
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, sc, next)

	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleFailed {
		t.Fatalf("fires = %v, want one failed fire", fires)
	}
	if fires[0].Outcome.Card != "" || fires[0].Outcome.Orphan != "" {
		t.Errorf("a refused mint left card=%q orphan=%q; want both empty", fires[0].Outcome.Card, fires[0].Outcome.Orphan)
	}
	rows, err := e.cfg.Store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range rows {
		if f.IsFreeform() {
			t.Errorf("the refused mint left %s behind", f.ID)
		}
	}
	row, _ := e.cfg.Store.Schedule(ctx, "nightly")
	if !row.Enabled {
		t.Error("a failed fire turned the schedule off; it fires again on its next cadence")
	}
}

// TestScheduleMintOrphanIsRetriedNotDuplicated: a mint whose kickoff
// fails leaves the card as the schedule's orphan; the next fire retries
// that card's kickoff instead of minting another beside it.
func TestScheduleMintOrphanIsRetriedNotDuplicated(t *testing.T) {
	ctx := context.Background()
	ag := newTurnRecorder("on it")
	e := scheduleEngine(t, ag)
	sc := mintSchedule("a")
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, sc, next)

	// The kickoff is refused on the first fire — at the open, the way a
	// sandbox refusal refuses.
	refuse := &refusingAgent{Agent: ag, err: errors.New("sandbox: the session is confined")}
	e.cfg.Agents = singleAgent(refuse)
	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleFailed {
		t.Fatalf("first fire = %v, want one failed fire", fires)
	}
	orphan := fires[0].Outcome.Orphan
	if orphan == "" || orphan != fires[0].Outcome.Card {
		t.Fatalf("the failed fire named card=%q orphan=%q; want both the minted card", fires[0].Outcome.Card, fires[0].Outcome.Orphan)
	}
	row, _ := e.cfg.Store.Schedule(ctx, "nightly")
	if row.OrphanCard != orphan {
		t.Fatalf("the row does not name the orphan: %q", row.OrphanCard)
	}

	// The refusal clears: the next fire retries the same card.
	refuse.release()
	fires, err = e.ScheduleTick(ctx, nextDue(t, e.cfg.Store, sc.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("second fire = %v, want one ok fire", fires)
	}
	if fires[0].Outcome.Card != orphan || fires[0].Outcome.Orphan != "" {
		t.Errorf("the retry = card=%q orphan=%q; want the orphan kicked off and the pointer cleared",
			fires[0].Outcome.Card, fires[0].Outcome.Orphan)
	}
	ff := e.Freeform(orphan)
	waitFreeformIdle(t, ff)
	if turns := ag.sentTurns(); len(turns) != 1 || turns[0] != "triage new issues" {
		t.Errorf("the orphan saw %v, want exactly one opening turn", turns)
	}
}

// refusingAgent refuses every session open with a fixed error — the
// shape of a sandbox refusal at the open — until released.
type refusingAgent struct {
	agent.Agent
	mu  sync.Mutex
	err error
}

func (a *refusingAgent) NewSession(ctx context.Context, opts agent.SessionOpts) (agent.Session, error) {
	a.mu.Lock()
	err := a.err
	a.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return a.Agent.NewSession(ctx, opts)
}

func (a *refusingAgent) release() {
	a.mu.Lock()
	a.err = nil
	a.mu.Unlock()
}

// TestScheduleMintOrphanRefusedTwiceStaysOneCard: a persistent refusal
// never piles up cards — the orphan stays the orphan and each cadence
// retries it.
func TestScheduleMintOrphanRefusedTwiceStaysOneCard(t *testing.T) {
	ctx := context.Background()
	refuse := &refusingAgent{Agent: newTurnRecorder("on it"), err: errors.New("sandbox: the session is confined")}
	e := scheduleEngine(t, refuse)
	sc := mintSchedule("a")
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, sc, next)

	if _, err := e.ScheduleTick(ctx, next.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ScheduleTick(ctx, nextDue(t, e.cfg.Store, sc.ID)); err != nil {
		t.Fatal(err)
	}
	rows, err := e.cfg.Store.ListFeatures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ffCards []domain.FeatureID
	for _, f := range rows {
		if f.IsFreeform() {
			ffCards = append(ffCards, f.ID)
		}
	}
	if len(ffCards) != 1 {
		t.Fatalf("two refusals left %v behind; the orphan must never be duplicated", ffCards)
	}
	if row, _ := e.cfg.Store.Schedule(ctx, "nightly"); row.OrphanCard != ffCards[0] {
		t.Errorf("the orphan pointer = %q, want %q", row.OrphanCard, ffCards[0])
	}
}

// TestScheduleMintFailureBeforeMintDoesNotReuseLastCard: a failure
// before the mint leaves no orphan behind, so a later fire cannot retry
// the last good fire's card and deliver its prompt a second time.
func TestScheduleMintFailureBeforeMintDoesNotReuseLastCard(t *testing.T) {
	ctx := context.Background()
	ag := newTurnRecorder("on it")
	e := scheduleEngine(t, ag)
	sc := mintSchedule("a")
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, sc, next)

	// Fire 1: a good mint of card A.
	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	a := fires[0].Outcome.Card
	waitFreeformIdle(t, e.Freeform(a))

	// Fire 2: the definition is edited to name a repo the workspace does
	// not configure — the real-world path is edit, re-enable, and the
	// next fire fails before any mint.
	sc.Repo = "nowhere"
	if err := e.cfg.Store.UpdateScheduleDefinition(ctx, &sc); err != nil {
		t.Fatal(err)
	}
	if err := e.cfg.Store.SetScheduleEnabled(ctx, sc.ID, true, tenPast(next)); err != nil {
		t.Fatal(err)
	}
	fires, err = e.ScheduleTick(ctx, tenPast(next).Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleFailed {
		t.Fatalf("second fire = %v, want one failed fire", fires)
	}
	if fires[0].Outcome.Orphan != "" {
		t.Fatalf("a pre-mint failure named orphan %q", fires[0].Outcome.Orphan)
	}

	// Fire 3: the repo is back; a fresh card is minted, and card A's
	// conversation holds exactly its one opening turn.
	sc.Repo = "a"
	if err := e.cfg.Store.UpdateScheduleDefinition(ctx, &sc); err != nil {
		t.Fatal(err)
	}
	if err := e.cfg.Store.SetScheduleEnabled(ctx, sc.ID, true, tenPast(next).Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	fires, err = e.ScheduleTick(ctx, tenPast(next).Add(time.Hour).Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("third fire = %v, want one ok fire", fires)
	}
	if fires[0].Outcome.Card == a {
		t.Fatal("the third fire reused the first card; a good fire's card is never retried")
	}
	waitFreeformIdle(t, e.Freeform(a))
	// Card A's conversation holds exactly its one opening turn, whatever
	// the later fires minted beside it.
	if n := userTurnCount(t, e, a); n != 1 {
		t.Errorf("card A holds %d user turns, want exactly one across three fires", n)
	}
	if got, _ := lastUserTurn(t, e, a); got != "triage new issues" {
		t.Errorf("card A's turn = %q, want the prompt", got)
	}
}

// TestScheduleMintMissingOrphanMintsFresh: an orphan whose card was
// deleted counts as closed — the next fire mints a fresh card and the
// outcome's empty orphan clears the pointer, so the schedule is never
// stuck retrying a card that is not there.
func TestScheduleMintMissingOrphanMintsFresh(t *testing.T) {
	ctx := context.Background()
	ag := newTurnRecorder("on it")
	e := scheduleEngine(t, ag)
	sc := mintSchedule("a")
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, sc, next)

	// The kickoff is refused at the open, the way a sandbox refusal
	// refuses.
	refuse := &refusingAgent{Agent: ag, err: errors.New("sandbox: the session is confined")}
	e.cfg.Agents = singleAgent(refuse)
	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	orphan := fires[0].Outcome.Orphan
	if orphan == "" {
		t.Fatal("the failed fire left no orphan behind")
	}
	refuse.release()
	// The person deletes the orphaned card like any other: its worktree
	// and branch go with it, then the record.
	if err := deleteCard(t, e, orphan); err != nil {
		t.Fatal(err)
	}
	fires, err = e.ScheduleTick(ctx, nextDue(t, e.cfg.Store, sc.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("the fire after the deletion = %v, want one ok fire", fires)
	}
	if fires[0].Outcome.Card == orphan {
		t.Fatal("the fire retried a deleted card")
	}
	row, _ := e.cfg.Store.Schedule(ctx, "nightly")
	if row.OrphanCard != "" {
		t.Errorf("orphan_card = %q after the fresh mint, want empty", row.OrphanCard)
	}
}

// TestScheduleMintClosedOrphanMintsFresh: a closed orphan is not reused;
// the next fire mints fresh and the pointer is cleared.
func TestScheduleMintClosedOrphanMintsFresh(t *testing.T) {
	ctx := context.Background()
	refuse := &refusingAgent{Agent: newTurnRecorder("on it"), err: errors.New("sandbox: the session is confined")}
	e := scheduleEngine(t, refuse)
	sc := mintSchedule("a")
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, sc, next)

	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	orphan := fires[0].Outcome.Orphan
	refuse.release()
	// The person closes the orphaned card like any other.
	if _, err := e.cfg.Store.CloseFreeform(ctx, orphan, "user"); err != nil {
		t.Fatal(err)
	}
	fires, err = e.ScheduleTick(ctx, nextDue(t, e.cfg.Store, sc.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("the fire after the close = %v, want one ok fire", fires)
	}
	if fires[0].Outcome.Card == orphan {
		t.Fatal("the fire retried a closed card")
	}
	if row, _ := e.cfg.Store.Schedule(ctx, "nightly"); row.OrphanCard != "" {
		t.Errorf("orphan_card = %q after the fresh mint, want empty", row.OrphanCard)
	}
}

// TestScheduleTickFailureKeepsSchedule: an error mid-fire never disables
// — the schedule fires again on its next cadence.
func TestScheduleTickFailureKeepsSchedule(t *testing.T) {
	ctx := context.Background()
	ag := newTurnRecorder("ack")
	e := newEngine(t, ag)
	f := freeformCard(1, "watch the deploy")
	createFeature(t, e.cfg.Store, f)
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, hourlyHeartbeat(f.ID), next)

	// The send is refused this once.
	ag.SendErr = errors.New("backend refused the turn")
	fires, err := e.ScheduleTick(ctx, next.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleFailed {
		t.Fatalf("fires = %v, want one failed fire", fires)
	}
	row, _ := e.cfg.Store.Schedule(ctx, "hourly")
	if !row.Enabled {
		t.Error("a failed fire turned the schedule off")
	}
	if !row.NextRun.After(next) {
		t.Errorf("next run = %s, want advanced past the failed fire", row.NextRun)
	}
	// And the next cadence delivers normally.
	ag.SendErr = nil
	fires, err = e.ScheduleTick(ctx, row.NextRun.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].Outcome.Status != domain.ScheduleOK {
		t.Fatalf("retry = %v, want one ok fire", fires)
	}
}

// TestRunScheduleForcesDisabled: a person's run-now from the board fires
// a disabled schedule once and leaves it disabled with no cadence.
func TestRunScheduleForcesDisabled(t *testing.T) {
	ctx := context.Background()
	ag := newTurnRecorder("ack")
	e := newEngine(t, ag)
	f := freeformCard(1, "watch the deploy")
	createFeature(t, e.cfg.Store, f)
	if err := e.cfg.Store.CreateSchedule(ctx, func() *domain.Schedule {
		s := hourlyHeartbeat(f.ID)
		s.Prompt = "check CI once"
		return &s
	}()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	fire, err := e.RunSchedule(ctx, "hourly", now)
	if err != nil {
		t.Fatal(err)
	}
	if !fire.Forced || fire.Outcome.Status != domain.ScheduleOK {
		t.Fatalf("the forced fire = %+v, want a forced ok fire", fire)
	}
	waitFreeformIdle(t, e.Freeform(f.ID))
	if turns := ag.sentTurns(); len(turns) != 1 || turns[0] != "check CI once" {
		t.Errorf("the backend saw %v, want exactly the prompt", turns)
	}
	row, _ := e.cfg.Store.Schedule(ctx, "hourly")
	if row.Enabled || !row.NextRun.IsZero() {
		t.Errorf("after a forced fire the row reads enabled=%v next=%v; want disabled with no cadence", row.Enabled, row.NextRun)
	}
	// The board's poll never picks it up from here.
	fires, err := e.ScheduleTick(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 0 {
		t.Errorf("the poll fired %v on a disabled row", fires)
	}
}

// TestRunScheduleForcesEnabled: a forced fire of an enabled row advances
// its cadence from now, the way any fire does.
func TestRunScheduleForcesEnabled(t *testing.T) {
	ctx := context.Background()
	e := newEngine(t, agent.NewFake("ack"))
	f := freeformCard(1, "watch the deploy")
	createFeature(t, e.cfg.Store, f)
	next := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	scheduleFixture(t, e.cfg.Store, hourlyHeartbeat(f.ID), next)
	now := time.Date(2026, 10, 3, 10, 42, 0, 0, time.UTC)
	if _, err := e.RunSchedule(ctx, "hourly", now); err != nil {
		t.Fatal(err)
	}
	row, _ := e.cfg.Store.Schedule(ctx, "hourly")
	if !row.Enabled {
		t.Error("a forced fire of an enabled row turned it off")
	}
	if !row.NextRun.Equal(time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("next run = %s, want 11:00 (advanced from now)", row.NextRun)
	}
}
