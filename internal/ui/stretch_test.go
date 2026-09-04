package ui

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/state"
)

// A card's whole history is a slice of events, so these fixtures build
// one row at a time and hand the slice to autopilotStretches. Nothing
// here renders: the derivation is the part most likely to be wrong, and
// it is worth being able to see it is right without reading a screen.

var stretchT0 = time.Date(2026, 8, 1, 23, 40, 0, 0, time.UTC)

func at(mins int) time.Time { return stretchT0.Add(time.Duration(mins) * time.Minute) }

func evTookOver(mode string, t time.Time) state.CardEvent {
	p, _ := json.Marshal(state.AutopilotPayload{Event: state.AutopilotTookOver, Mode: mode})
	return state.CardEvent{Kind: state.EventAutopilot, At: t, Payload: string(p)}
}

func evHandedBack(reason string, t time.Time) state.CardEvent {
	p, _ := json.Marshal(state.AutopilotPayload{Event: state.AutopilotHandedBack, Reason: reason})
	return state.CardEvent{Kind: state.EventAutopilot, At: t, Payload: string(p)}
}

func evModeChange(mode string, t time.Time) state.CardEvent {
	p, _ := json.Marshal(state.AutopilotPayload{Mode: mode})
	return state.CardEvent{Kind: state.EventAutopilot, At: t, Payload: string(p)}
}

func evPark(stage domain.Stage, detail string, t time.Time) state.CardEvent {
	p, _ := json.Marshal(state.ParkPayload{Reason: state.ParkReasonNeedsYou, Detail: detail})
	return state.CardEvent{Kind: state.EventPark, Stage: stage, At: t, Payload: string(p)}
}

func evGate(from, to domain.Stage, actor string, t time.Time) state.CardEvent {
	p, _ := json.Marshal(state.GatePayload{From: string(from), To: string(to), Actor: actor})
	return state.CardEvent{Kind: state.EventGate, Stage: from, At: t, Payload: string(p)}
}

func evAsk(answer, by string, t time.Time) state.CardEvent {
	p, _ := json.Marshal(state.AskPayload{Question: "q?", Answer: answer, By: by})
	return state.CardEvent{Kind: state.EventAsk, At: t, Payload: string(p)}
}

func evMessage(author, content string, t time.Time) state.CardEvent {
	p, _ := json.Marshal(messagePayload{Author: author, Content: content})
	return state.CardEvent{Kind: state.EventMessage, At: t, Payload: string(p)}
}

func evExit(stage domain.Stage, verdict string, t time.Time) state.CardEvent {
	p, _ := json.Marshal(stageExitPayload{Verdict: verdict})
	return state.CardEvent{Kind: state.EventStageExit, Stage: stage, At: t, Payload: string(p)}
}

func aFeature() domain.Feature {
	return domain.Feature{ID: "FD-001", Kind: domain.KindFeature, Stage: domain.StageImplement}
}

func onlyStretch(t *testing.T, sts []autopilotStretch) autopilotStretch {
	t.Helper()
	if len(sts) != 1 {
		t.Fatalf("stretches = %d, want exactly 1: %+v", len(sts), sts)
	}
	return sts[0]
}

// TestStretchOpensOnlyOnAnExplicitRow is the rule the whole design rests
// on: nothing but a took-over row starts a period. A card whose gates
// were crossed unattended by the review→fix loop — which happens with
// autopilot switched off entirely — has no period, and must not grow one
// from its crossings alone.
func TestStretchOpensOnlyOnAnExplicitRow(t *testing.T) {
	events := []state.CardEvent{
		evGate(domain.StageReview, domain.StageFix, "review", at(0)),
		evGate(domain.StageFix, domain.StageReview, "review", at(10)),
		evPark(domain.StageReview, "review needs you", at(20)),
	}
	if got := autopilotStretches(aFeature(), events); len(got) != 0 {
		t.Fatalf("stretches = %+v, want none — nobody handed this card over", got)
	}
}

// TestModeChangeIsNotABoundary: EventAutopilot rows predate this feature
// and are still written on every gate-approval change. They share the
// kind and mean something else, so a reader that did not check Event
// would open a period every time the switch moved.
func TestModeChangeIsNotABoundary(t *testing.T) {
	events := []state.CardEvent{
		evModeChange(domain.GateFull, at(0)),
		evGate(domain.StageSpec, domain.StagePlan, state.ActorAutopilot, at(5)),
	}
	if got := autopilotStretches(aFeature(), events); len(got) != 0 {
		t.Fatalf("stretches = %+v, want none — a mode change is a preference, not a period", got)
	}
}

// TestStretchCollectsWhatAutopilotDecided: the tally is the crossings
// and answers made inside the period, and nothing a person did.
func TestStretchCollectsWhatAutopilotDecided(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateFull, at(0)),
		evGate(domain.StageSpec, domain.StagePlan, state.ActorAutopilot, at(6)),
		evAsk("stream rows", state.ActorAutopilot, at(20)),
		evAsk("8080", state.ActorUser, at(21)), // a person: closes the period
		evGate(domain.StagePlan, domain.StageImplement, state.ActorAutopilot, at(24)),
	}
	st := onlyStretch(t, autopilotStretches(aFeature(), events))
	if len(st.gates) != 1 || st.gates[0].from != domain.StageSpec {
		t.Fatalf("gates = %+v, want only the spec crossing (the plan one is after the close)", st.gates)
	}
	if len(st.answers) != 1 || st.answers[0].answer != "stream rows" {
		t.Fatalf("answers = %+v, want only autopilot's own", st.answers)
	}
	if st.closed != stretchTakenBack {
		t.Fatalf("closed = %q, want %q — a person answered", st.closed, stretchTakenBack)
	}
}

// TestStretchClosers walks every way a period can end, including the two
// that decide between "parked" and "finished".
func TestStretchClosers(t *testing.T) {
	// A normal feature's landing gate is verify: the last stage on its
	// own sequence that is not done.
	cases := []struct {
		name   string
		tail   []state.CardEvent
		want   stretchClose
		reason string
	}{
		{
			name: "an explicit handback",
			tail: []state.CardEvent{evHandedBack("you turned autopilot off", at(30))},
			want: stretchTakenBack, reason: "you turned autopilot off",
		},
		{
			name: "a park short of the end",
			tail: []state.CardEvent{evPark(domain.StageImplement, "implement finished, review it", at(30))},
			want: stretchParked, reason: "implement finished, review it",
		},
		{
			name: "a park at the landing gate after a pass",
			tail: []state.CardEvent{
				evExit(domain.StageVerify, state.StatusOK, at(29)),
				evPark(domain.StageVerify, "verify passed — ready to land", at(30)),
			},
			want: stretchFinished, reason: "verify passed — ready to land",
		},
		{
			name: "a park at the landing gate after a failure",
			tail: []state.CardEvent{
				evExit(domain.StageVerify, state.StatusFail, at(29)),
				evPark(domain.StageVerify, "verify failed", at(30)),
			},
			want: stretchParked, reason: "verify failed",
		},
		{
			name: "a gate a person crossed",
			tail: []state.CardEvent{evGate(domain.StageImplement, domain.StageReview, "user", at(30))},
			want: stretchTakenBack,
		},
		{
			name: "a turn a person typed",
			tail: []state.CardEvent{evMessage(string(engine.AuthorUser), "hold on", at(30))},
			want: stretchTakenBack,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := append([]state.CardEvent{evTookOver(domain.GateFull, at(0))}, tc.tail...)
			st := onlyStretch(t, autopilotStretches(aFeature(), events))
			if st.closed != tc.want {
				t.Fatalf("closed = %q, want %q", st.closed, tc.want)
			}
			if st.reason != tc.reason {
				t.Fatalf("reason = %q, want %q", st.reason, tc.reason)
			}
			// the closer is the last row of the tail, and the took-over
			// occupies index 0 — so the period ends at the tail's length,
			// bounded by the closing event rather than containing it.
			if st.to != len(tc.tail) {
				t.Fatalf("to = %d, want %d — the closing event bounds the period, it is not inside it",
					st.to, len(tc.tail))
			}
		})
	}
}

// TestAgentTurnDoesNotClose: only a turn a PERSON typed ends a period.
// The agent talking is the period doing its job, and closing on it would
// end every stretch at its first line of output.
func TestAgentTurnDoesNotClose(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateFull, at(0)),
		evMessage("implementer", "wired the theme layer", at(5)),
		evMessage(string(engine.AuthorSystem), "kickoff", at(6)),
	}
	st := onlyStretch(t, autopilotStretches(aFeature(), events))
	if !st.running() {
		t.Fatalf("closed = %q, want still running — only a person ends a period", st.closed)
	}
}

// TestSecondTookOverInsideAPeriodIsIgnored: the headless driver writes a
// took-over per process and deliberately does not dedupe, on the grounds
// that a duplicate is collapsible and a missing row is not. This is the
// collapse — one uninterrupted period is one period however many times a
// crashed run restarted inside it.
func TestSecondTookOverInsideAPeriodIsIgnored(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateFull, at(0)),
		evTookOver(domain.GateFull, at(5)),
		evPark(domain.StageImplement, "needs you", at(10)),
	}
	st := onlyStretch(t, autopilotStretches(aFeature(), events))
	if st.from != 0 || !st.openedAt.Equal(at(0)) {
		t.Fatalf("period opened at index %d / %s, want the first row", st.from, st.openedAt)
	}
}

// TestStretchStaysOpen: a card autopilot is driving right now has an
// open period, which the thread draws with an opening rule and no
// closing one. to spans to the end so everything since the takeover
// counts as inside it.
func TestStretchStaysOpen(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateGates, at(0)),
		evGate(domain.StageSpec, domain.StagePlan, state.ActorAutopilot, at(6)),
	}
	st := onlyStretch(t, autopilotStretches(aFeature(), events))
	if !st.running() {
		t.Fatalf("closed = %q, want running", st.closed)
	}
	if st.to != len(events) {
		t.Fatalf("to = %d, want %d", st.to, len(events))
	}
	if st.mode != domain.GateGates {
		t.Fatalf("mode = %q, want the mode it was handed over under", st.mode)
	}
}

// TestCloseOrphanedDowngradesAnOpenPeriod: a period the log still calls
// running() renders as orphaned once nothing is actually driving the
// card any more — the render-time judgement closeOrphaned adds on top of
// the log-derived stretch.
func TestCloseOrphanedDowngradesAnOpenPeriod(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateGates, at(0)),
		evGate(domain.StageSpec, domain.StagePlan, state.ActorAutopilot, at(6)),
	}
	st := onlyStretch(t, closeOrphaned(autopilotStretches(aFeature(), events), events, false))
	if st.running() {
		t.Fatalf("closed = %q, want orphaned — nothing is driving this card", st.closed)
	}
	if st.closed != stretchOrphaned {
		t.Fatalf("closed = %q, want %q", st.closed, stretchOrphaned)
	}
}

// TestCloseOrphanedLeavesALiveOneRunning: a period that is still running
// per the log stays running when a session is actually alive — the
// board's own in-process autopilot switch, or a foreign process the
// liveness check found alive.
func TestCloseOrphanedLeavesALiveOneRunning(t *testing.T) {
	events := []state.CardEvent{evTookOver(domain.GateGates, at(0))}
	st := onlyStretch(t, closeOrphaned(autopilotStretches(aFeature(), events), events, true))
	if !st.running() {
		t.Fatal("closeOrphaned closed a period a live session is still driving")
	}
}

// TestCloseOrphanedLeavesAnAlreadyClosedPeriodAlone: closeOrphaned only
// ever touches the newest, still-open period — a period that closed on
// its own is left exactly as the log says, whether or not anything is
// live right now.
func TestCloseOrphanedLeavesAnAlreadyClosedPeriodAlone(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateGates, at(0)),
		evPark(domain.StageImplement, "needs you", at(10)),
	}
	st := onlyStretch(t, closeOrphaned(autopilotStretches(aFeature(), events), events, false))
	if st.closed != stretchParked {
		t.Fatalf("closed = %q, want %q — closeOrphaned must not touch a period the log already closed", st.closed, stretchParked)
	}
}

// TestLiveStretchesClosesAKilledDriversOpenPeriod pins BG-059 end to end,
// through liveStretches — the function every render call site (thread.go,
// msgs.go, shell.go) now calls instead of autopilotStretches directly.
//
// The event log is exactly TestStretchStaysOpen's shape: a took-over row
// and an in-period gate crossing, nothing after it — no handback, no
// park, no human gate, no user message. That is what a driver killed
// mid-run (crash, OOM, SIGKILL, container restart) leaves behind. Before
// BG-059's fix this stayed open forever; with a real, dead pid behind the
// card's live file, it must read as closed.
func TestLiveStretchesClosesAKilledDriversOpenPeriod(t *testing.T) {
	dir := t.TempDir()
	ws := state.Workspace{Root: dir, RepoRoot: dir}
	f := aFeature()

	owner := exec.CommandContext(context.Background(), "true")
	if err := owner.Start(); err != nil {
		t.Fatalf("start stand-in owner: %v", err)
	}
	// Fully reap it: a killed process a test still parents sits as a
	// zombie, which a bare kill(pid, 0) probe reads as alive, until Wait.
	if err := owner.Wait(); err != nil {
		t.Fatalf("stand-in owner: %v", err)
	}

	w, err := livelog.Create(ws.LiveFile(f.ID), livelog.Record{
		Feature: string(f.ID), Stage: string(f.Stage), PID: owner.Process.Pid,
	})
	if err != nil {
		t.Fatalf("create live file: %v", err)
	}
	w.Close()

	events := []state.CardEvent{
		evTookOver(domain.GateGates, at(0)),
		evGate(domain.StageSpec, domain.StagePlan, state.ActorAutopilot, at(6)),
	}
	st := onlyStretch(t, liveStretches(f, events, ws))
	if st.running() {
		t.Fatalf("closed = %q, want orphaned — the driving pid %d is dead", st.closed, owner.Process.Pid)
	}
	if st.closed != stretchOrphaned {
		t.Fatalf("closed = %q, want %q", st.closed, stretchOrphaned)
	}
}

// TestTwoStretchesAlternate is the shape a real card takes: it runs
// itself, you take it back, you hand it over again. Two periods, each
// bounded, with your own work in between belonging to neither.
func TestTwoStretchesAlternate(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateFull, at(0)),
		evGate(domain.StageSpec, domain.StagePlan, state.ActorAutopilot, at(6)),
		evPark(domain.StageImplement, "needs you", at(10)),
		evMessage(string(engine.AuthorUser), "let me look", at(20)),
		evTookOver(domain.GateFull, at(30)),
		evGate(domain.StageImplement, domain.StageReview, state.ActorAutopilot, at(36)),
	}
	got := autopilotStretches(aFeature(), events)
	if len(got) != 2 {
		t.Fatalf("stretches = %d, want 2: %+v", len(got), got)
	}
	if got[0].closed != stretchParked || !got[1].running() {
		t.Fatalf("want a closed period then an open one, got %q and %q", got[0].closed, got[1].closed)
	}
	if _, in := stretchAt(got, 3); in {
		t.Fatal("the turn you typed between the two periods belongs to neither")
	}
	if _, in := stretchAt(got, 5); !in {
		t.Fatal("the second period's own crossing is inside it")
	}
}

// TestStretchDecidedNothingStillOpens is D9: pressing the switch, having
// it run a stage and park with no gate crossed and no question answered
// is a real period worth drawing. Only the tally row is withheld.
func TestStretchDecidedNothingStillOpens(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateFull, at(0)),
		evMessage("implementer", "did the work", at(5)),
		evPark(domain.StageImplement, "implement finished, review it", at(10)),
	}
	st := onlyStretch(t, autopilotStretches(aFeature(), events))
	if !st.decidedNothing() {
		t.Fatalf("tally = %+v / %+v, want empty", st.gates, st.answers)
	}
	if st.closed != stretchParked {
		t.Fatalf("closed = %q, want the period still bounded", st.closed)
	}
}

// TestLandingGateIsPerCard: "finished" means the card got as far as it
// is allowed to go, and how far that is depends on the card's own
// workflow rather than on the word "verify".
func TestLandingGateIsPerCard(t *testing.T) {
	feature := domain.Feature{ID: "FD-001", Kind: domain.KindFeature}
	if !landingGate(feature, domain.StageVerify) {
		t.Fatal("a feature's last decision is verify")
	}
	if landingGate(feature, domain.StageImplement) {
		t.Fatal("implement is not a landing gate")
	}
	if landingGate(feature, domain.StageDone) {
		t.Fatal("done is not a gate — it is the far side of one")
	}
}

// withSeqs stamps a fixture's events with the seq numbers the store
// would have given them, since the unread mark is a seq and a fixture
// built in memory has none.
func withSeqs(events []state.CardEvent) []state.CardEvent {
	for i := range events {
		events[i].Seq = int64(i + 1)
	}
	return events
}

// TestUnseenStretchIsTheNewestClosedOne: the thread opens on a period
// that both ended and ended since the reader last looked. One still
// running is never it — the card is moving, and the end of the
// conversation is where a reader should land.
func TestUnseenStretchIsTheNewestClosedOne(t *testing.T) {
	events := withSeqs([]state.CardEvent{
		evTookOver(domain.GateFull, at(0)),                     // 1
		evPark(domain.StageImplement, "first", at(10)),         // 2
		evMessage(string(engine.AuthorUser), "looked", at(20)), // 3
		evTookOver(domain.GateFull, at(30)),                    // 4
		evPark(domain.StageImplement, "second", at(40)),        // 5
		evTookOver(domain.GateFull, at(50)),                    // 6
	})
	sts := autopilotStretches(aFeature(), events)
	if len(sts) != 3 {
		t.Fatalf("stretches = %d, want 3: %+v", len(sts), sts)
	}

	got, ok := unseenStretch(sts, events, 0)
	if !ok || got.reason != "second" {
		t.Fatalf("unseen = %+v (ok=%v), want the newest CLOSED period, not the open one", got, ok)
	}

	// Having read to the end, nothing is unread — including the period
	// still running, which never counts.
	if _, ok := unseenStretch(sts, events, newestSeq(events)); ok {
		t.Fatal("everything read, but a period still reported unread")
	}

	// Read only as far as the first park: the second period is unread.
	got, ok = unseenStretch(sts, events, 2)
	if !ok || got.reason != "second" {
		t.Fatalf("unseen = %+v (ok=%v), want the second period", got, ok)
	}
}

// TestNoUnseenStretchWithoutAPeriod: a card nobody handed over has
// nothing to jump to, however much history it carries.
func TestNoUnseenStretchWithoutAPeriod(t *testing.T) {
	events := withSeqs([]state.CardEvent{
		evGate(domain.StageReview, domain.StageFix, "review", at(0)),
		evPark(domain.StageFix, "needs you", at(10)),
	})
	sts := autopilotStretches(aFeature(), events)
	if _, ok := unseenStretch(sts, events, 0); ok {
		t.Fatal("a card with no period reported one to jump to")
	}
}

// TestInterruptedLandingGateIsNotFinished: quitting the board stops a
// running session where it stands and parks it without ever writing a
// stage_exit. Asking for "the newest exit anywhere in the log" then
// borrows some earlier stage's pass and closes the period as though the
// card had got all the way there — the closing rule congratulating
// itself over a verify that never produced a verdict at all.
func TestInterruptedLandingGateIsNotFinished(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateFull, at(0)),
		evExit(domain.StageReview, state.StatusOK, at(10)), // an earlier stage passed
		{Kind: state.EventStageEnter, Stage: domain.StageVerify, At: at(11)},
		// verify itself never exits: the board quit mid-run
		evPark(domain.StageVerify, "stopped when the board quit", at(20)),
	}
	st := onlyStretch(t, autopilotStretches(aFeature(), events))
	if st.closed != stretchParked {
		t.Fatalf("closed = %q, want %q — verify never finished, it was interrupted",
			st.closed, stretchParked)
	}
}

// TestLandingGateFinishesOnItsOwnVerdict is the other side: the stage
// that parked must be the stage whose exit is read.
func TestLandingGateFinishesOnItsOwnVerdict(t *testing.T) {
	events := []state.CardEvent{
		evTookOver(domain.GateFull, at(0)),
		evExit(domain.StageReview, state.StatusFail, at(10)), // an earlier failure
		evExit(domain.StageVerify, state.StatusOK, at(19)),   // verify's own pass
		evPark(domain.StageVerify, "verify passed — ready to land", at(20)),
	}
	st := onlyStretch(t, autopilotStretches(aFeature(), events))
	if st.closed != stretchFinished {
		t.Fatalf("closed = %q, want %q — verify passed on its own exit", st.closed, stretchFinished)
	}
}

// TestAutopilotDriving pins the board's version of running(): only the
// last stretch matters, and only while it is still open.
func TestAutopilotDriving(t *testing.T) {
	cases := []struct {
		name   string
		events []state.CardEvent
		want   bool
	}{
		{
			name: "no periods",
			want: false,
		},
		{
			name:   "one still-open period",
			events: []state.CardEvent{evTookOver(domain.GateFull, at(0))},
			want:   true,
		},
		{
			name: "one period opened then closed",
			events: []state.CardEvent{
				evTookOver(domain.GateFull, at(0)),
				evHandedBack("", at(10)),
			},
			want: false,
		},
		{
			name: "a closed period followed by a second still-open one",
			events: []state.CardEvent{
				evTookOver(domain.GateFull, at(0)),
				evHandedBack("", at(10)),
				evTookOver(domain.GateFull, at(20)),
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := autopilotDriving(autopilotStretches(aFeature(), tc.events))
			if got != tc.want {
				t.Fatalf("autopilotDriving = %v, want %v", got, tc.want)
			}
		})
	}
}
