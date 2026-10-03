package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
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

func TestWebBoardItemsFoldsTheConversation(t *testing.T) {
	at := fixedTime
	tr := []engine.Message{
		{Author: engine.AuthorUser, Content: "start FD-001", At: at, AnsweredBy: "sam"},
		{Author: engine.AuthorTool, Tool: "board", Content: "list", ToolStatus: engine.ToolOK, At: at, DoneAt: at.Add(250 * time.Millisecond)},
		{Author: engine.AuthorTool, Tool: "board", Content: "start", ToolStatus: engine.ToolFail, At: at},
		{Author: engine.AuthorAssistant, Content: "started", At: at},
		{Author: engine.AuthorSystem, Content: "note to self", At: at},
		{Author: engine.AuthorTool, Tool: "board", Content: "watch", At: at},
		{Author: engine.AuthorAssistant, Content: "partial", Streaming: true, At: at},
	}
	items, streaming, tool := webBoardItems(tr)
	if streaming != "partial" {
		t.Errorf("streaming = %q, want the unfinished last message", streaming)
	}
	if tool != nil {
		t.Errorf("tool = %+v: a running call that is not last is not the live one", tool)
	}
	want := []webapi.ItemType{webapi.ItemYou, webapi.ItemActivity, webapi.ItemMessage, webapi.ItemNote, webapi.ItemActivity}
	if len(items) != len(want) {
		t.Fatalf("items = %d, want %d: %+v", len(items), len(want), items)
	}
	for i, w := range want {
		if items[i].T != w {
			t.Errorf("item %d is %s, want %s", i, items[i].T, w)
		}
	}
	if items[0].By != "sam" || items[0].Key != "b0" {
		t.Errorf("you item = %+v", items[0])
	}
	if got := items[1].Items; len(got) != 1 || len(got[0].Tools) != 2 || got[0].Tools[0].Status != "ok" || got[0].Tools[0].Ms != 250 || got[0].Tools[1].Status != "fail" {
		t.Errorf("a run of calls should be one activity with each call's outcome: %+v", got)
	}
	if items[1].Seq != 3 {
		t.Errorf("a grown activity's Seq = %d, want the last call's position (3)", items[1].Seq)
	}

	// a call still running as the last message is the live block's tool
	_, _, tool = webBoardItems([]engine.Message{{Author: engine.AuthorTool, Tool: "board", Content: "watch", At: at}})
	if tool == nil || tool.Status != "running" {
		t.Errorf("live tool = %+v", tool)
	}
	// a streaming message that is not last was finished
	items, streaming, _ = webBoardItems([]engine.Message{
		{Author: engine.AuthorAssistant, Content: "a", Streaming: true},
		{Author: engine.AuthorUser, Content: "b"},
	})
	if streaming != "" || len(items) != 2 {
		t.Errorf("a superseded streaming message: streaming=%q items=%d", streaming, len(items))
	}
}

func TestWebAgentWithNothingOpen(t *testing.T) {
	m, _ := newWorkspace(t)
	a := m.WebAgent()
	if a.Open || a.Live != nil || a.Items == nil || a.Profiles == nil || a.Models == nil {
		t.Fatalf("a board with no agent: %+v, want closed with empty non-nil lists", a)
	}
	_, err := m.WebAgentSend("   ")
	wantWebErr(t, "blank send", err, WebBadRequest)
	_, err = m.WebAgentSend("hello")
	wantWebErr(t, "send with no session", err, WebConflict)
	_, err = m.WebAgentInterrupt()
	wantWebErr(t, "interrupt with no session", err, WebConflict)
	_, err = m.WebAgentOpen(webapi.AgentOpenRequest{})
	wantWebErr(t, "open with no engine", err, WebUnavailable)
	_, err = m.WebAgentProfile(webapi.AgentProfileRequest{Profile: "thrifty"})
	wantWebErr(t, "switch with no engine", err, WebUnavailable)
}

func TestWebAgentProfileNeedsANameAndAKnownProfile(t *testing.T) {
	m := repoWorkspace(t) // this one has an engine
	_, err := m.WebAgentProfile(webapi.AgentProfileRequest{})
	wantWebErr(t, "switch naming nothing", err, WebBadRequest)
	_, err = m.WebAgentProfile(webapi.AgentProfileRequest{Profile: "no-such-profile"})
	wantWebErr(t, "switch to an unknown profile", err, WebBadRequest)
	_, err = m.WebAgentOpen(webapi.AgentOpenRequest{Profile: "no-such-profile"})
	wantWebErr(t, "open on an unknown profile", err, WebBadRequest)
	m.boardOpening = true
	_, err = m.WebAgentOpen(webapi.AgentOpenRequest{})
	wantWebErr(t, "open while opening", err, WebConflict)
	_, err = m.WebAgentProfile(webapi.AgentProfileRequest{Model: "x"})
	wantWebErr(t, "switch while opening", err, WebConflict)
}

func TestWebCardStatsProjectsTheRun(t *testing.T) {
	start := fixedTime
	run := cardrun.Run{
		ID: "FD-001", Title: "Dark mode", Kind: domain.KindFeature, Stage: domain.StageVerify,
		Sessions: []cardrun.Session{
			{Stage: domain.StageImplement, Role: "implementer", Model: "m", Started: start, Ended: start.Add(time.Minute), Closed: true, Turns: 3, Credits: 4},
			{Stage: domain.StageImplement, Role: "implementer", Model: "m", Started: start.Add(time.Hour), Redo: true, RedoReason: "corrected"},
		},
		Money: cardrun.Money{
			Credits: 5, Rework: 1, FirstPass: 4,
			ByStage: []cardrun.Bucket{{Name: "implement", Credits: 5}},
		},
		Clock:    cardrun.Clock{Agent: 90 * time.Second, Elapsed: time.Hour},
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
	if got.Envelope.Credits != 100 || got.Envelope.Left != 95 {
		t.Errorf("envelope = %+v, want 100 granted and 95 left", got.Envelope)
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
