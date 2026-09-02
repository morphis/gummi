package driver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// tookOverEvents filters id's event log down to the took-over boundary
// rows (state.EventAutopilot carrying AutopilotPayload.Event ==
// AutopilotTookOver) — appendAutopilotEvent's plain mode-change rows and
// any future handed-back rows share the same event kind, so a test that
// cares about the boundary specifically must not just count EventAutopilot.
func tookOverEvents(t *testing.T, h *harness, id domain.FeatureID) []state.AutopilotPayload {
	t.Helper()
	evs, err := h.store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []state.AutopilotPayload
	for _, ev := range evs {
		if ev.Kind != state.EventAutopilot {
			continue
		}
		var p state.AutopilotPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatalf("undecodable autopilot payload %q: %v", ev.Payload, err)
		}
		if p.Event == state.AutopilotTookOver {
			out = append(out, p)
		}
	}
	return out
}

// parkRows filters id's event log down to state.EventPark rows, decoded.
func parkRows(t *testing.T, h *harness, id domain.FeatureID) []state.ParkPayload {
	t.Helper()
	evs, err := h.store.Events(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []state.ParkPayload
	for _, ev := range evs {
		if ev.Kind != state.EventPark {
			continue
		}
		var p state.ParkPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatalf("undecodable park payload %q: %v", ev.Payload, err)
		}
		out = append(out, p)
	}
	return out
}

// An unattended run (the default --gate-approval=gates, d.actor == "auto")
// writes exactly one took-over row, naming the card's stored gate-approval
// mode and the stage the loop actually started driving (its first real
// stage past todo's pure kickoff hop).
func TestUnattendedRunLogsTookOver(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})

	out, err := h.driver(Options{}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v; stream=%v", err, h.eventKinds())
	}
	if out.Status != StatusDone {
		t.Fatalf("status = %q, want done; stream=%v", out.Status, h.eventKinds())
	}

	rows := tookOverEvents(t, h, domain.FeatureID(out.ID))
	if len(rows) != 1 {
		t.Fatalf("took-over rows = %d, want 1: %+v", len(rows), rows)
	}
	if rows[0].Mode != domain.GateGates {
		t.Fatalf("took-over mode = %q, want %q", rows[0].Mode, domain.GateGates)
	}
	if rows[0].Reason == "" {
		t.Fatal("took-over row carries no reason")
	}

	// The run reached done the ordinary way — no escalation — but it is
	// still the driver stepping back and handing the card to a person to
	// merge. Without a matching park row here, the took-over row above
	// opens a period that nothing ever closes (BG-044): the thread reads
	// as still driving forever, and every gate/answer autopilot recorded
	// this run never renders.
	parks := parkRows(t, h, domain.FeatureID(out.ID))
	if len(parks) != 1 {
		t.Fatalf("park rows = %d, want 1 — the took-over period must close on an ordinary done: %+v", len(parks), parks)
	}
	if parks[0].Reason != state.ParkReasonNeedsYou {
		t.Fatalf("park reason = %q, want %q", parks[0].Reason, state.ParkReasonNeedsYou)
	}
	if parks[0].Detail == "" {
		t.Fatal("park row carries no detail")
	}

	evs, err := h.store.Events(context.Background(), domain.FeatureID(out.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if ev.Kind != state.EventAutopilot {
			continue
		}
		var p state.AutopilotPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		if p.Event != state.AutopilotTookOver {
			continue
		}
		if ev.Stage != domain.StageSpec {
			t.Fatalf("took-over stage = %q, want %q (the flow's first real stage, not todo)", ev.Stage, domain.StageSpec)
		}
	}
}

// An attended run (--gate-approval=off, d.actor == "caller") never writes a
// took-over row: a human or script is waiting on every gate, and this
// history exists specifically so it never claims otherwise.
func TestAttendedRunLogsNoTookOver(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
	})

	out, err := h.driver(Options{GateApproval: GateOff}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v; stream=%v", err, h.eventKinds())
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question (a caller design gate); stream=%v", out.Status, h.eventKinds())
	}

	if rows := tookOverEvents(t, h, domain.FeatureID(out.ID)); len(rows) != 0 {
		t.Fatalf("attended run logged %d took-over rows, want 0: %+v", len(rows), rows)
	}
}

// Re-entering the same card while it is still stuck at the stage the
// unattended loop was already driving — a resume after a crash (a
// process killed mid-run parks nothing; a stage-timeout or an exhausted
// envelope now do, via logPark) — must not accumulate a second
// took-over row for what is, from the thread's point of view, one
// continuous unattended stretch that merely survived a process restart.
//
// This drives logTookOver directly, the way TestDriverEscalationRecordsAPark
// drives escalation directly, rather than through a full stage-timeout +
// resume cycle: reproducing a stalled-then-resumed session deterministically
// depends on the engine's own session-restore behavior (which, on a resume,
// may fast-forward a stalled interactive stage rather than re-presenting it
// stalled), not on anything this package controls. The dedupe key itself —
// (card, stage) — is exactly what a real resume observes: GetFeature
// returns the same persisted stage on both calls, since nothing advanced it
// in between.
// TestTookOverWritesEveryCall pins the deliberate absence of a dedupe
// key. The driver's own once-per-call flag is what holds a single run to
// one row; across runs this writes unconditionally, because collapsing
// here cannot tell a crash-and-resume on one stage apart from two
// separate runs that legitimately begin at that same stage — the
// ordinary shape of a card bounced back to implement twice.
//
// Writing both is safe in a way that dropping one is not: the reader
// ignores a took-over that arrives while a stretch is already open, so a
// duplicate collapses at render time, whereas a row never written is a
// stretch that can never open and a period of unattended work that
// renders as though nobody had handed the card over at all.
func TestTookOverWritesEveryCall(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{})
	f := domain.Feature{
		ID: "FD-001", Num: 1, Title: "t", Slug: "t",
		Stage: domain.StageImplement, Kind: domain.KindFeature,
	}
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}

	d := h.driver(Options{})
	d.logTookOver(f)
	d.logTookOver(f) // a second run, beginning at the same stage

	if rows := tookOverEvents(t, h, f.ID); len(rows) != 2 {
		t.Fatalf("two runs at one stage produced %d took-over rows, want 2 — a dedupe key here would swallow the second run's whole stretch: %+v", len(rows), rows)
	}

	f.Stage = domain.StageReview
	d.logTookOver(f)
	if rows := tookOverEvents(t, h, f.ID); len(rows) != 3 {
		t.Fatalf("a run beginning at a new stage produced %d took-over rows, want 3: %+v", len(rows), rows)
	}
}
