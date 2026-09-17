package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
)

// bouncedStats is a card that did the same work twice — the shape the run
// report exists to make legible.
func bouncedStats() cardrun.Run {
	start := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	pass := func(role string, mins int, credits float64, redo bool, reason string) cardrun.Session {
		return cardrun.Session{
			Stage: domain.StageImplement, Role: role, Flavor: "stage", Turns: 20,
			Started: start, Ended: start.Add(time.Duration(mins) * time.Minute), Closed: true,
			Credits: credits, Redo: redo, RedoReason: reason,
		}
	}
	return cardrun.Run{
		ID: "BG-004", Title: "the rerun edge is unreachable", Stage: domain.StageDone,
		Sessions: []cardrun.Session{
			pass("implementer", 44, 12.24, false, ""),
			pass("implementer", 32, 12.51, true, cardrun.Corrected),
		},
		Money: cardrun.Money{
			Credits: 24.75, FirstPass: 12.24, Rework: 12.51, Corrected: 12.51,
			ByStage:      []cardrun.Bucket{{Name: "implement", Credits: 24.75}},
			InputTokens:  1000,
			CachedTokens: 3000,
		},
		Clock:    cardrun.Clock{Agent: 76 * time.Minute, Elapsed: 300 * time.Minute, Waiting: 224 * time.Minute},
		Envelope: cardrun.Envelope{Granted: 1350, Spent: 24.75},
		Judgment: cardrun.Judgment{
			Gates: cardrun.Answered{Total: 3, ByYou: 1, ByMachine: 2},
			Rounds: map[domain.RoundKind]int{
				domain.RoundKindCorrective: 1,
			},
		},
	}
}

// The wire shape carries the figure the stage-grained rollup cannot
// produce: what share of this card's spend went on work it had already
// done, and which passes those were.
func TestStatusRunPayloadCarriesTheReworkSplit(t *testing.T) {
	r := statsPayload(bouncedStats())

	if r.Sessions != 2 {
		t.Fatalf("sessions = %d, want 2", r.Sessions)
	}
	if r.Money.Rework != 12.51 || r.Money.FirstPass != 12.24 {
		t.Errorf("money = %+v, want 12.24 first / 12.51 redone", r.Money)
	}
	if got := r.Money.ReworkShare; got < 0.505 || got > 0.506 {
		t.Errorf("rework share = %v, want ~0.5055", got)
	}
	if r.Money.Utilization != 0.0183 {
		t.Errorf("utilization = %v, want 0.0183 (24.75 of 1350)", r.Money.Utilization)
	}
	if r.Money.Tokens.CacheReadRatio != 0.75 {
		t.Errorf("cache read ratio = %v, want 0.75", r.Money.Tokens.CacheReadRatio)
	}
	if len(r.Passes) != 2 || !r.Passes[1].Redo || r.Passes[1].RedoReason != cardrun.Corrected {
		t.Errorf("passes = %+v, want the second marked as a correction", r.Passes)
	}
	if r.Clock.WaitingShare < 0.74 || r.Clock.WaitingShare > 0.75 {
		t.Errorf("waiting share = %v, want ~0.747", r.Clock.WaitingShare)
	}
}

// null, not {}. A caller must be able to tell "this backend reports no
// tool calls" from "this card made none" — they are different facts and
// only one of them is about the card.
func TestStatusRunToolsNullWhenUnrecorded(t *testing.T) {
	b, err := json.Marshal(statsPayload(bouncedStats()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"tools":null`) {
		t.Fatalf("tools did not marshal as null:\n%s", b)
	}

	r := bouncedStats()
	r.Hands.Tools = []cardrun.ToolUse{{Name: "Bash", Calls: 2, Fails: 1, Detail: "go vet ./..."}}
	r.Hands.ToolCalls, r.Hands.ToolFails = 2, 1
	out := statsPayload(r)
	if out.Hands.Tools["Bash"].Calls != 2 || out.Hands.Tools["Bash"].Fails != 1 {
		t.Errorf("tools = %+v, want Bash 2/1", out.Hands.Tools)
	}
	if out.Hands.Tools["Bash"].Detail != "go vet ./..." {
		t.Errorf("a failed call lost the argument the prune keeps: %+v", out.Hands.Tools["Bash"])
	}
}

// The text render leads with where the money went and names the redo,
// because that is the answer someone opened the report for.
func TestRenderRunNamesTheRedo(t *testing.T) {
	var b bytes.Buffer
	view := statusView{ID: "BG-004", Title: "the rerun edge is unreachable", Ending: domain.EndingLanded}
	renderStats(&b, view, statsPayload(bouncedStats()))
	out := b.String()

	for _, want := range []string{
		"BG-004", "landed · 2 sessions", "where it went", "the redo",
		"implement · implementer · corrected", "12.51 of 24.75 credits was work already done (51%)",
		"waiting on you", "granted 1350",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

// A card that never did anything twice gets no redo block: the block is
// the exception, never a heading over an empty list.
func TestRenderRunOmitsTheRedoWhenThereIsNone(t *testing.T) {
	r := bouncedStats()
	r.Sessions[1].Redo, r.Sessions[1].RedoReason = false, ""
	r.Money.Rework, r.Money.Corrected = 0, 0
	r.Money.FirstPass = r.Money.Credits

	var b bytes.Buffer
	renderStats(&b, statusView{ID: "BG-004", Stage: "done"}, statsPayload(r))
	if strings.Contains(b.String(), "the redo") {
		t.Errorf("a clean card was given a redo block:\n%s", b.String())
	}
}

// Under a second says so rather than rounding to "0s".
func TestStatusRunDurations(t *testing.T) {
	for _, c := range []struct {
		seconds float64
		want    string
	}{
		{0, "—"}, {0.3, "<1s"}, {12, "12s"}, {150, "2.5m"}, {5400, "1h30m"},
	} {
		if got := dur(c.seconds); got != c.want {
			t.Errorf("dur(%v) = %q, want %q", c.seconds, got, c.want)
		}
	}
}
