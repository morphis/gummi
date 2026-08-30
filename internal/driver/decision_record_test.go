package driver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestCallerGateRecordsItsDecision: the GateOff caller checkpoint raises
// a durable decision (§10.18 — the card is blocked on a person), the
// stream's checkpoint carries its id, and the eventual crossing answers
// it with the same id — after --approve, nothing reads as waiting.
func TestCallerGateRecordsItsDecision(t *testing.T) {
	h := newHarness(t, true, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageImplement: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return msgIdle(o.Model, "Implemented.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return toolVerdict(o.Model, "pass")
		},
	})

	out, err := h.driver(Options{GateApproval: GateOff}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v; stream=%v", err, h.eventKinds())
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question (a caller design gate); stream=%v", out.Status, h.eventKinds())
	}

	// the checkpoint raised its decision
	opens, err := h.store.OpenDecisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	list := opens[h.only()]
	if len(list) != 1 || list[0].Kind != state.DecisionKindGate {
		t.Fatalf("open decisions at the caller gate = %+v, want one gate row", opens)
	}
	decisionID := list[0].ID

	// the stream's checkpoint names the same id, so a driving script can
	// correlate the stop with the row.
	q := lastEvent(h, "gate")
	if q == nil || q["decision"] != decisionID {
		t.Fatalf("gate event = %v, want a decision id %q", q, decisionID)
	}

	h.buf.Reset()
	out2, err := h.driver(Options{}).Resume(context.Background(), h.only(), ResumeInput{Approve: true})
	if err != nil {
		t.Fatalf("Resume --approve: %v; stream=%v", err, h.eventKinds())
	}
	if out2.Status != StatusDone {
		t.Fatalf("approve status = %q, want done; stream=%v", out2.Status, h.eventKinds())
	}

	// the crossing is the answer: the gate event's payload carries the
	// decision id, and nothing reads open afterwards.
	evs, err := h.store.Events(context.Background(), h.only())
	if err != nil {
		t.Fatal(err)
	}
	var gate *state.GatePayload
	for _, ev := range evs {
		if ev.Kind != state.EventGate {
			continue
		}
		var p state.GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		gate = &p
	}
	if gate == nil || gate.ID != decisionID {
		t.Fatalf("gate event = %+v, want payload id == the decision id", gate)
	}
	opens, err = h.store.OpenDecisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(opens) != 0 {
		t.Fatalf("the approved gate's decision still reads open: %+v", opens)
	}
}

// The §2.2 bug, headless end to end: an ask answered by an unattended run
// lands in the morning receipt. The receipt reads the ask events whose
// `by` says autopilot — the answerer's declared word, not the card's
// stored mode.
func TestSpecQuestionThenResumeCorrelatesItsDecision(t *testing.T) {
	h := newHarness(t, false, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, n int, o agent.SessionOpts, _ string) []agent.Event {
			if n == 0 {
				return convAsk(o.Model, "Include a schema header?", "no (recommended)", "yes")
			}
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
	})

	out, err := h.driver(Options{}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question; stream=%v", out.Status, h.eventKinds())
	}
	// the stream's checkpoint names the decision row it opened
	q := lastEvent(h, "question")
	if q == nil || q["decision"] == "" {
		t.Fatalf("question event carries no decision id: %v; stream=%v", q, h.eventKinds())
	}
	decisionID := q["decision"].(string)

	opens, err := h.store.OpenDecisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(opens[domain.FeatureID(out.ID)]) != 1 {
		t.Fatalf("no open ask decision after the question checkpoint: %+v", opens)
	}

	ans := "no"
	h.buf.Reset()
	if _, err := h.driver(Options{}).Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{Answer: &ans}); err != nil {
		t.Fatalf("Resume: %v; stream=%v", err, h.eventKinds())
	}

	evs, err := h.store.Events(context.Background(), h.only())
	if err != nil {
		t.Fatal(err)
	}
	var answer *state.AskPayload
	for _, ev := range evs {
		if ev.Kind != state.EventAsk {
			continue
		}
		var p state.AskPayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		answer = &p
	}
	if answer == nil {
		t.Fatal("resume --answer recorded no ask event")
	}
	if answer.By != state.ActorUser || answer.ID != decisionID {
		t.Errorf("answer = by %q id %q, want by %q and the decision id %q",
			answer.By, answer.ID, state.ActorUser, decisionID)
	}

	opens, err = h.store.OpenDecisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(opens[domain.FeatureID(out.ID)]) != 0 {
		t.Fatalf("resume --answer left a decision open forever: %+v", opens)
	}
}

// --answer on a card parked anywhere but an ask does not silently deliver
// the text as a plain turn: the durable decision is the record of what
// the card is waiting on, and it names which verb this stop takes.
func TestResumeAnswerWithoutOpenQuestionIsATypedUsageError(t *testing.T) {
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

	out, err := h.driver(Options{GateApproval: GateOff}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question (the caller gate); stream=%v", out.Status, h.eventKinds())
	}

	ans := "no"
	h.buf.Reset()
	_, rerr := h.driver(Options{}).Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{Answer: &ans})
	if rerr == nil {
		t.Fatalf("resume --answer at a caller gate succeeded; stream=%v", h.eventKinds())
	}
	if !strings.Contains(rerr.Error(), "--approve") {
		t.Errorf("usage error = %q, want it naming the verb this stop actually takes", rerr.Error())
	}
}

// A bare resume onto a card blocked on an answer re-presents the question
// checkpoint — the record of an open ask survives the process, so a bare
// resume no longer reads as the design gate its transcript used to proxy
// for. The next --answer still lands through engine.Answer.
func TestBareResumeRePresentsAnOpenAsk(t *testing.T) {
	h := newHarness(t, false, map[domain.Stage]stageFn{
		domain.StageSpec: func(_ *harness, n int, o agent.SessionOpts, _ string) []agent.Event {
			if n == 0 {
				return convAsk(o.Model, "Include a schema header?", "no (recommended)", "yes")
			}
			return msgIdle(o.Model, "Spec drafted.")
		},
		domain.StageReview: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
		domain.StageVerify: func(_ *harness, _ int, o agent.SessionOpts, _ string) []agent.Event {
			return prosePass(o.Model)
		},
	})

	out, err := h.driver(Options{}).Run(context.Background(), "add export")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Status != StatusQuestion {
		t.Fatalf("status = %q, want question; stream=%v", out.Status, h.eventKinds())
	}

	// a bare resume re-presents the question the record says is open —
	// it must not present the design gate the transcript-emptiness proxy
	// used to read the stop as.
	h.buf.Reset()
	out2, err := h.driver(Options{}).Resume(context.Background(), domain.FeatureID(out.ID), ResumeInput{})
	if err != nil {
		t.Fatalf("bare Resume: %v; stream=%v", err, h.eventKinds())
	}
	if out2.Status != StatusQuestion {
		t.Fatalf("bare resume onto an open ask = %q, want the question checkpoint re-presented; stream=%v",
			out2.Status, h.eventKinds())
	}
	q := lastEvent(h, "question")
	if q == nil || q["decision"] == "" {
		t.Fatalf("re-presented checkpoint carries no decision id: %v; stream=%v", q, h.eventKinds())
	}
}
