package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/fleetrun"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
)

func wantWebErr(t *testing.T, what string, err error, code WebErrorCode) {
	t.Helper()
	var we *WebError
	if !errors.As(err, &we) || we.Code != code {
		t.Fatalf("%s: err = %v, want a WebError of code %d", what, err, code)
	}
}

func TestWebCardStatsProjectsTheRun(t *testing.T) {
	start := fixedTime
	run := cardrun.Run{
		ID: "FD-001", Title: "Dark mode", Kind: domain.KindFeature, Stage: domain.StageVerify,
		Sessions: []cardrun.Session{
			{
				Stage: domain.StageImplement, Role: "implementer", Model: "m",
				Started: start, Ended: start.Add(time.Minute), Closed: true,
				Turns: 3, Credits: 4, InputTokens: 1200, CachedTokens: 800, OutputTokens: 300,
				ContextPeak: 41000, ContextLimit: 200000,
			},
			{Stage: domain.StageImplement, Role: "implementer", Model: "m", Started: start.Add(time.Hour), Redo: true, RedoReason: "corrected"},
		},
		Money: cardrun.Money{
			Credits: 5, Rework: 1, FirstPass: 4,
			ByStage: []cardrun.Bucket{{Name: "implement", Credits: 5}},
		},
		Clock: cardrun.Clock{Agent: 90 * time.Second, Elapsed: time.Hour, ToFirstGate: 30 * time.Minute, ToVerified: time.Hour},
		Hands: cardrun.Hands{
			Turns: 7, ToolCalls: 9, ToolFails: 1,
			Tools: []cardrun.ToolUse{
				{Name: "read", Calls: 6, Fails: 1, Total: 40 * time.Millisecond},
				{Name: "run", Calls: 3, Detail: "go test ./...", Total: 2 * time.Second},
			},
			Skills:    []cardrun.ToolUse{{Name: "skill", Calls: 2, Detail: "gummi-go-verify"}},
			Subagents: []cardrun.ToolUse{{Name: "task", Calls: 1, Detail: "find the fold"}},
			Checks:    []cardrun.CheckRun{{Name: "build", Runs: 3, Fails: 1, Excused: true}},
		},
		Judgment: cardrun.Judgment{
			Gates: cardrun.Answered{Total: 2, ByYou: 1, ByMachine: 1},
			Asks:  cardrun.Answered{Total: 1, ByYou: 1},
			// A quit park rides through as-is: the fold already leaves
			// board-quit parks off, and the projection must not grow a
			// second copy of that exclusion.
			Parks: []cardrun.Park{{Reason: "quit", Detail: "board quit", At: start}},
		},
		Envelope: cardrun.Envelope{Granted: 100, Spent: 5},
	}
	got := WebCardStats(run)
	if got.ID != "FD-001" || got.Kind != "feature" || got.Stage != "verify" {
		t.Fatalf("head = %+v", got)
	}
	if len(got.Sessions) != 2 || got.Sessions[0].Ended.IsZero() || !got.Sessions[1].Ended.IsZero() {
		t.Fatalf("an open session must carry no end: %+v", got.Sessions)
	}
	if !got.Sessions[1].Redo || got.Sessions[1].RedoReason != "corrected" {
		t.Errorf("redo lost: %+v", got.Sessions[1])
	}
	if got.Money.Credits != 5 || got.Money.Rework != 1 || len(got.Money.ByStage) != 1 || got.Money.ByRole == nil || got.Money.ByModel == nil {
		t.Errorf("money = %+v, want totals, and non-nil empty buckets for the rest", got.Money)
	}
	if got.Clock.AgentMs != 90000 || got.Clock.ElapsedMs != 3600000 {
		t.Errorf("clock = %+v", got.Clock)
	}
	if got.Clock.ToFirstGateMs != 1800000 || got.Clock.ToVerifiedMs != 3600000 {
		t.Errorf("clock extras lost: %+v", got.Clock)
	}
	if got.Envelope.Credits != 100 || got.Envelope.Left != 95 {
		t.Errorf("envelope = %+v, want 100 granted and 95 left", got.Envelope)
	}

	// hands project one-to-one, with the tool's summed duration in ms.
	h := got.Hands
	if h.Turns != 7 || h.ToolCalls != 9 || h.ToolFails != 1 {
		t.Errorf("hands tallies = %+v", h)
	}
	if len(h.Tools) != 2 || h.Tools[0].Name != "read" || h.Tools[0].Calls != 6 || h.Tools[0].Fails != 1 || h.Tools[0].TotalMs != 40 ||
		h.Tools[1].Name != "run" || h.Tools[1].Detail != "go test ./..." || h.Tools[1].TotalMs != 2000 {
		t.Errorf("tools = %+v", h.Tools)
	}
	if len(h.Skills) != 1 || h.Skills[0].Detail != "gummi-go-verify" || len(h.Subagents) != 1 || h.Subagents[0].Calls != 1 {
		t.Errorf("delegation = %+v / %+v", h.Skills, h.Subagents)
	}
	if len(h.Checks) != 1 || h.Checks[0].Name != "build" || h.Checks[0].Runs != 3 || h.Checks[0].Fails != 1 || !h.Checks[0].Excused {
		t.Errorf("checks = %+v", h.Checks)
	}

	// judgment: the counts and the park, copied as the fold wrote them.
	j := got.Judgment
	if j.Gates.Total != 2 || j.Gates.ByYou != 1 || j.Gates.ByMachine != 1 || j.Asks.Total != 1 || j.Asks.ByYou != 1 {
		t.Errorf("judgment tallies = %+v", j)
	}
	if len(j.Parks) != 1 || j.Parks[0].Reason != "quit" || j.Parks[0].Detail != "board quit" || !j.Parks[0].At.Equal(start) {
		t.Errorf("parks = %+v, want the fold's park passed through unchanged", j.Parks)
	}

	// per-pass tokens and context occupancy, projected on the closed
	// session and left zero on the open one.
	if got.Sessions[0].Tokens != (webapi.Tokens{Input: 1200, Cached: 800, Output: 300}) ||
		got.Sessions[0].ContextPeak != 41000 || got.Sessions[0].ContextLimit != 200000 {
		t.Errorf("tokens = %+v / ctx = %d/%d", got.Sessions[0].Tokens, got.Sessions[0].ContextPeak, got.Sessions[0].ContextLimit)
	}
	if got.Sessions[1].Tokens != (webapi.Tokens{}) || got.Sessions[1].ContextPeak != 0 || got.Sessions[1].ContextLimit != 0 {
		t.Errorf("a pass with no token record must stay zero: %+v", got.Sessions[1])
	}

	// The nil-vs-empty tools distinction survives the projection: a
	// backend that records no tool calls keeps its nil, which the wire
	// turns into an absent array.
	bare := WebCardStats(cardrun.Run{Hands: cardrun.Hands{Turns: 2}})
	if bare.Hands.Tools != nil {
		t.Errorf("nil tools must stay nil, got %+v", bare.Hands.Tools)
	}
	if empty := WebCardStats(cardrun.Run{Hands: cardrun.Hands{Tools: []cardrun.ToolUse{}}}); empty.Hands.Tools == nil || len(empty.Hands.Tools) != 0 {
		t.Errorf("empty non-nil tools must stay empty, got %+v", empty.Hands.Tools)
	}
}

func TestWebFleetReportProjectsLanesAndWindow(t *testing.T) {
	end := fixedTime
	rep := fleetrun.Report{
		Window:   fleetrun.Window{To: end},
		RateSpan: 48 * time.Hour,
		Credits:  12, Running: 1, PeakLanes: 2,
		Agent: time.Minute, Tokens: fleetrun.Tokens{Input: 10, Cached: 4, Output: 2},
		BusiestAgent: time.Minute, Busiest: end.Add(-time.Hour), BusiestLen: 10 * time.Minute,
		Lanes: []fleetrun.Lane{{
			ID: "FD-001", Title: "Dark mode", Kind: domain.KindFeature, Credits: 12,
			Blocks: []fleetrun.Block{
				{From: end.Add(-2 * time.Hour), To: end.Add(-time.Hour), Stage: domain.StagePlan},
				{From: end.Add(-time.Hour), Stage: domain.StageImplement, Open: true},
			},
			Waits: []fleetrun.Span{{From: end.Add(-time.Hour), To: end}},
		}},
	}
	got := WebFleetReport(rep)
	if want := end.Add(-48 * time.Hour); !got.From.Equal(want) {
		t.Errorf("an all-history window starts at %v, want first activity %v", got.From, want)
	}
	if got.Busiest == nil || got.Busiest.LenMs != 600000 {
		t.Errorf("busiest = %+v", got.Busiest)
	}
	if got.Tokens.Cached != 4 || got.Running != 1 || got.PeakLanes != 2 {
		t.Errorf("report = %+v", got)
	}
	if len(got.Lanes) != 1 || len(got.Lanes[0].Blocks) != 2 || len(got.Lanes[0].Waits) != 1 {
		t.Fatalf("lanes = %+v", got.Lanes)
	}
	if !got.Lanes[0].Blocks[1].To.IsZero() || got.Lanes[0].Blocks[0].To.IsZero() {
		t.Errorf("only the open block has no end: %+v", got.Lanes[0].Blocks)
	}

	// nothing ever ran: an empty window at its own end, never year 1
	empty := WebFleetReport(fleetrun.Report{Window: fleetrun.Window{To: end}})
	if !empty.From.Equal(end) || empty.Busiest != nil || empty.Lanes == nil {
		t.Errorf("empty = %+v", empty)
	}
}

func TestWebFleetAndCardStatsReadTheStore(t *testing.T) {
	d, m, _ := docsWorkspace(t)
	ctx := context.Background()

	st, err := d.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.ID != "FD-001" || st.Sessions == nil {
		t.Fatalf("stats = %+v", st)
	}

	fl, err := m.WebFleet()
	if err != nil {
		t.Fatal(err)
	}
	now := m.now()
	if _, err := fl.Report(ctx, now, now.Add(-time.Hour)); err == nil {
		t.Error("a window that ends before it starts was accepted")
	} else if ie := (*InvalidError)(nil); !errors.As(err, &ie) {
		t.Errorf("backwards window: err = %T, want *InvalidError", err)
	}
	rep, err := fl.Report(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Lanes == nil || rep.To.IsZero() {
		t.Errorf("all-history report = %+v", rep)
	}
	if FleetDefaultWindow() <= 0 {
		t.Error("the default window must be positive")
	}

	detached := NewShell(theme.GummiDark(), "v0")
	if _, err := detached.WebFleet(); !errors.Is(err, ErrDetached) {
		t.Errorf("fleet on a detached board: err = %v", err)
	}
}

func TestWebRequestChangesRefusalsAndPullPR(t *testing.T) {
	d, m, _ := docsWorkspace(t)
	_, err := m.WebPullPR("FD-404")
	if !errors.Is(err, ErrNoCard) {
		t.Errorf("pull on an unknown card: err = %v", err)
	}
	_, err = m.WebRequestSpecChanges("FD-404", "sam", "")
	wantWebErr(t, "spec changes, unknown card", err, WebNotFound)
	_, err = m.WebRequestDiffChanges("FD-404", "sam", "")
	wantWebErr(t, "diff changes, unknown card", err, WebNotFound)
	_, err = m.WebRequestSpecChanges(string(d.f.ID), "sam", "")
	wantWebErr(t, "spec changes with no document", err, WebConflict)
	// no comments to send: the refusal is the terminal's sentence
	if _, err = m.WebRequestDiffChanges(string(d.f.ID), "sam", ""); err == nil {
		t.Fatal("sending no comments was accepted")
	}
}
