package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
)

// TestBG080SecondCrossingAfterAnsweredGateIsRecorded is BG-080's
// regression test.
//
// Every crossing goes through Store.Transition, which writes the
// transitions row and the card's gate event in one transaction so a
// stage change can never exist without a receipt behind it. The gate
// event's dedupe key is "decision:<id>" whenever the crossing answers an
// open gate decision, and newestOpenGateDecisionTx is what supplies that
// id. It was documented as returning the newest gate decision "that no
// later answer has closed" but consulted an answered set nothing ever
// filled, so it kept handing back a decision id that had already been
// answered. The second crossing then wrote the first crossing's dedupe
// key, ON CONFLICT DO NOTHING discarded it, and the card moved stage
// with no event in its history — permanently, since nothing rewrites it.
//
// The shape is a research card's route: investigate opens a gate
// decision and its crossing answers it, then shape is advanced by hand
// and opens no decision of its own. Feature cards mask the defect
// because every gated stage on their route opens a fresh decision.
func TestBG080SecondCrossingAfterAnsweredGateIsRecorded(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	id, _ := domain.NewID(domain.KindResearch, 1)
	slug, _ := domain.Slugify("snapshot expiry")
	now := time.Now().UTC()
	f := &domain.Feature{
		ID: id, Num: 1, Kind: domain.KindResearch,
		Title: "snapshot expiry", Slug: slug,
		Stage: domain.StageTodo, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, id, domain.StageInvestigate, "auto"); err != nil {
		t.Fatal(err)
	}

	// investigate parks on its gate, so a decision is open when the
	// crossing out of it lands: that crossing correlates to and answers it.
	const gateID = "gate:RS-001:investigate:1"
	if err := s.OpenDecision(ctx, id, domain.StageInvestigate, DecisionPayload{
		ID: gateID, Kind: DecisionKindGate, Question: "investigate is done — move on to shape?",
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Transition(ctx, id, domain.StageShape, "autopilot"); err != nil {
		t.Fatal(err)
	}

	// shape is the interactive stage: the user advances out of it by hand,
	// and nothing opens a gate decision for that crossing to answer.
	if _, err := s.Transition(ctx, id, domain.StageReview, "user"); err != nil {
		t.Fatal(err)
	}

	events, err := s.Events(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var crossings [][2]string
	for _, ev := range events {
		if ev.Kind != EventGate {
			continue
		}
		var p GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		crossings = append(crossings, [2]string{p.From, p.To})
	}

	want := [][2]string{
		{string(domain.StageTodo), string(domain.StageInvestigate)},
		{string(domain.StageInvestigate), string(domain.StageShape)},
		{string(domain.StageShape), string(domain.StageReview)},
	}
	if len(crossings) != len(want) {
		t.Fatalf("the card's history holds %d crossings, want %d — a stage change with no receipt behind it:\n got %v\nwant %v",
			len(crossings), len(want), crossings, want)
	}
	for i := range want {
		if crossings[i] != want[i] {
			t.Errorf("crossing %d = %s → %s, want %s → %s",
				i, crossings[i][0], crossings[i][1], want[i][0], want[i][1])
		}
	}

	// and the crossing that did answer the decision still says so, so the
	// fix does not buy the receipt back by dropping the correlation.
	var answered string
	for _, ev := range events {
		if ev.Kind != EventGate {
			continue
		}
		var p GatePayload
		if err := json.Unmarshal([]byte(ev.Payload), &p); err != nil {
			t.Fatal(err)
		}
		if p.From == string(domain.StageInvestigate) {
			answered = p.ID
		}
	}
	if answered != gateID {
		t.Errorf("the crossing out of investigate correlates to %q, want the decision it answered (%q)", answered, gateID)
	}
}
