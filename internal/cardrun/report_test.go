package cardrun

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

var base = time.Date(2026, 9, 6, 12, 15, 20, 0, time.UTC)

// pass describes one stage session for the fixture builder: who ran it,
// for how many minutes, what it cost, and how it ended.
type pass struct {
	stage           domain.Stage
	role, flavor    string
	minutes         int
	turns           int
	credits         float64
	verdict         string
	ctxPeak, ctxLim int64
}

// build turns a list of passes into the two records a real card leaves
// behind: an event log with a stage_enter/stage_exit around each pass,
// and the session-keyed spend rollup written alongside it.
func build(t *testing.T, passes []pass) ([]state.CardEvent, []state.StageSpend) {
	t.Helper()
	var evs []state.CardEvent
	var spend []state.StageSpend
	at := base
	for i, p := range passes {
		key := time.Duration(i).String() // any stable per-pass key
		enter, _ := json.Marshal(map[string]string{
			"role": p.role, "model": "glm-5.3-flash", "flavor": p.flavor,
		})
		evs = append(evs, state.CardEvent{
			Stage: p.stage, Kind: state.EventStageEnter, At: at, Payload: string(enter),
		})
		for turn := 0; turn < p.turns; turn++ {
			evs = append(evs, state.CardEvent{
				Stage: p.stage, Kind: state.EventMessage, At: at,
				Payload: `{"author":"assistant","content":"…"}`,
			})
		}
		at = at.Add(time.Duration(p.minutes) * time.Minute)
		exit, _ := json.Marshal(map[string]any{
			"verdict": p.verdict, "credits": p.credits,
			"ctx_peak": p.ctxPeak, "ctx_limit": p.ctxLim,
		})
		evs = append(evs, state.CardEvent{
			Stage: p.stage, Kind: state.EventStageExit, At: at, Payload: string(exit),
		})
		spend = append(spend, state.StageSpend{
			Stage: p.stage, Session: key, Role: p.role, Model: "glm-5.3-flash",
			Credits: p.credits, InputTokens: 1000, OutputTokens: 100,
			UpdatedAt: at,
		})
	}
	return evs, spend
}

// bg004 is the real shape of a card from this repo's own board: a bug
// that bounced once in plan, once in implement, then rebased and had to
// prove itself again. Its figures are the ones the proposal was written
// from.
func bg004() []pass {
	return []pass{
		{domain.StagePlan, "architect", "stage", 17, 20, 2.28, "", 0, 0},
		{domain.StagePlan, "reviewer", "critique", 29, 23, 3.27, "changes", 0, 0},
		{domain.StagePlan, "architect", "stage", 18, 14, 1.07, "", 0, 0},
		{domain.StagePlan, "reviewer", "critique", 22, 18, 2.18, "pass", 0, 0},
		{domain.StageImplement, "implementer", "stage", 44, 27, 12.24, "", 148000, 200000},
		{domain.StageImplement, "reviewer", "critique", 28, 19, 3.41, "changes", 0, 0},
		{domain.StageImplement, "implementer", "stage", 32, 44, 12.51, "", 190000, 200000},
		{domain.StageImplement, "reviewer", "critique", 11, 18, 1.90, "pass", 0, 0},
		{domain.StageVerify, "reviewer", "stage", 6, 7, 0.90, "pass", 0, 0},
		{domain.StageVerify, "implementer", "rebase", 8, 24, 1.97, "", 0, 0},
		{domain.StageVerify, "reviewer", "stage", 2, 6, 0.70, "pass", 0, 0},
	}
}

func card(spent float64, envelope int) domain.Feature {
	return domain.Feature{
		ID: "BG-004", Num: 4, Kind: domain.KindBug,
		Title:  "the implement → plan rerun edge is unreachable from every surface",
		Stage:  domain.StageDone,
		Budget: domain.Budget{Envelope: envelope},
		Spend:  domain.Spend{Credits: spent},
	}
}

// The headline the whole proposal turns on: a stage that ran twice keeps
// its first attempt and its redo apart, and the redo can cost more than
// the original. The per-stage rollup cannot say this, because it sums
// exactly the dimension that tells the two passes apart.
func TestReportSplitsFirstPassFromRedo(t *testing.T) {
	evs, spend := build(t, bg004())
	run := Report(Input{Feature: card(42.43, 1350), Events: evs, Spend: spend})

	if len(run.Sessions) != 11 {
		t.Fatalf("sessions = %d, want 11", len(run.Sessions))
	}
	// plan wrote, was sent back, wrote again; implement the same; verify
	// passed, rebased, and proved itself again.
	var redone []int
	for i, s := range run.Sessions {
		if s.Redo {
			redone = append(redone, i)
		}
	}
	wantRedone := []int{2, 3, 6, 7, 10}
	if len(redone) != len(wantRedone) {
		t.Fatalf("redone passes = %v, want %v", redone, wantRedone)
	}
	for i := range wantRedone {
		if redone[i] != wantRedone[i] {
			t.Fatalf("redone passes = %v, want %v", redone, wantRedone)
		}
	}

	// the settled decision: a rebase pass is not itself a redo, but the
	// re-verify it forces is — labelled apart from work a verdict sent back.
	if r := run.Sessions[9]; r.Redo {
		t.Errorf("the rebase pass is marked as a redo; it is the first of its flavour")
	}
	if r := run.Sessions[10]; r.RedoReason != Reproved {
		t.Errorf("post-rebase re-verify reason = %q, want %q", r.RedoReason, Reproved)
	}
	if r := run.Sessions[6]; r.RedoReason != Corrected {
		t.Errorf("implement redo reason = %q, want %q", r.RedoReason, Corrected)
	}

	// and the money that follows from it
	if got := round(run.Money.Rework); got != 18.36 {
		t.Errorf("rework = %v, want 18.36", got)
	}
	if got := round(run.Money.FirstPass); got != 24.07 {
		t.Errorf("first pass = %v, want 24.07", got)
	}
	if got := round(run.Money.Corrected); got != 17.66 {
		t.Errorf("corrected = %v, want 17.66", got)
	}
	if got := round(run.Money.Reproved); got != 0.7 {
		t.Errorf("reproved = %v, want 0.70", got)
	}
	if got := round(run.Money.ReworkShare() * 100); got < 43 || got > 43.4 {
		t.Errorf("rework share = %v%%, want ~43%%", got)
	}

	// the redo cost more than the original — the fact no gummi surface
	// could show before this
	if run.Sessions[6].Credits <= run.Sessions[4].Credits {
		t.Errorf("redo %v should exceed the first pass %v in this fixture",
			run.Sessions[6].Credits, run.Sessions[4].Credits)
	}
}

// Where the money went, and how much of the number can be trusted.
func TestReportMoneySplits(t *testing.T) {
	evs, spend := build(t, bg004())
	run := Report(Input{Feature: card(42.43, 1350), Events: evs, Spend: spend})

	if len(run.Money.ByStage) == 0 || run.Money.ByStage[0].Name != string(domain.StageImplement) {
		t.Fatalf("by stage = %+v, want implement leading", run.Money.ByStage)
	}
	if got := round(run.Money.ByStage[0].Credits); got != 30.06 {
		t.Errorf("implement credits = %v, want 30.06", got)
	}
	if got := round(run.Envelope.Utilization() * 100); got < 3.1 || got > 3.2 {
		t.Errorf("envelope utilization = %v%%, want ~3.1%%", got)
	}
}

// The clock: agent time is the sum of the passes, elapsed is the card's
// own life, and waiting is what is left — which on a card like this is
// most of it.
func TestReportClock(t *testing.T) {
	passes := bg004()
	evs, spend := build(t, passes)
	// push the whole last pass a long way out, the way a card that sat at
	// a gate overnight actually looks: the gap belongs between the passes,
	// not inside one of them.
	last := 0
	for i, ev := range evs {
		if ev.Kind == state.EventStageEnter {
			last = i
		}
	}
	for i := last; i < len(evs); i++ {
		evs[i].At = evs[i].At.Add(13 * time.Hour)
	}
	run := Report(Input{Feature: card(42.43, 1350), Events: evs, Spend: spend})

	if run.Clock.Agent <= 0 || run.Clock.Elapsed <= run.Clock.Agent {
		t.Fatalf("clock = %+v, want elapsed to exceed agent time", run.Clock)
	}
	if run.Clock.Waiting != run.Clock.Elapsed-run.Clock.Agent {
		t.Errorf("waiting = %v, want elapsed − agent", run.Clock.Waiting)
	}
	if share := run.Clock.WaitingShare(); share < 0.5 {
		t.Errorf("waiting share = %v, want most of the card's life", share)
	}
}

// Waiting is floored at zero: two sessions that overlapped did not make
// the card spend negative time waiting for anyone.
func TestReportOverlappingSessionsNeverWaitNegative(t *testing.T) {
	enter, _ := json.Marshal(map[string]string{"role": "implementer", "flavor": "stage"})
	exit, _ := json.Marshal(map[string]any{"verdict": ""})
	evs := []state.CardEvent{
		{Stage: domain.StageImplement, Kind: state.EventStageEnter, At: base, Payload: string(enter)},
		{Stage: domain.StageImplement, Kind: state.EventStageEnter, At: base, Payload: string(enter)},
		{Stage: domain.StageImplement, Kind: state.EventStageExit, At: base.Add(time.Hour), Payload: string(exit)},
		{Stage: domain.StageImplement, Kind: state.EventStageExit, At: base.Add(time.Hour), Payload: string(exit)},
	}
	run := Report(Input{Feature: card(1, 100), Events: evs})
	if run.Clock.Waiting < 0 {
		t.Fatalf("waiting = %v, want never negative", run.Clock.Waiting)
	}
}

// An open pass — the one a live card is running right now, and usually
// the one being asked about — is reported, not dropped.
func TestReportKeepsTheOpenPass(t *testing.T) {
	enter, _ := json.Marshal(map[string]string{"role": "implementer", "flavor": "stage"})
	evs := []state.CardEvent{
		{Stage: domain.StageImplement, Kind: state.EventStageEnter, At: base, Payload: string(enter)},
		{Stage: domain.StageImplement, Kind: state.EventMessage, At: base, Payload: `{"author":"user","content":"go"}`},
	}
	run := Report(Input{Feature: card(0, 100), Events: evs})
	if len(run.Sessions) != 1 {
		t.Fatalf("sessions = %d, want the open one kept", len(run.Sessions))
	}
	if run.Sessions[0].Closed {
		t.Error("the open pass reports as closed")
	}
	if run.Sessions[0].Duration() != 0 {
		t.Error("an open pass has no duration to report yet")
	}
	if run.Sessions[0].Turns != 1 {
		t.Errorf("turns = %d, want 1", run.Sessions[0].Turns)
	}
}

// A card whose backend never reported a tool call says so by reporting
// nothing, not by reporting none. The two are different facts and only
// one of them is about the card.
func TestReportToolsNilWhenTheBackendRecordsNone(t *testing.T) {
	evs, spend := build(t, bg004())
	run := Report(Input{Feature: card(42.43, 1350), Events: evs, Spend: spend})
	if run.Hands.Tools != nil {
		t.Fatalf("tools = %+v, want nil: this log holds no tool call at all", run.Hands.Tools)
	}
}

// With calls recorded, they are counted by name, failures and all, and
// the two kinds of delegation are picked out because their names are the
// only thing that says what was delegated to.
func TestReportCountsToolsSkillsAndChecks(t *testing.T) {
	tool := func(name, detail, status string, ms int64) state.CardEvent {
		p, _ := json.Marshal(state.ToolPayload{
			Label: name + "  " + detail, Tool: name, Detail: detail, Call: name + detail, MS: ms,
		})
		return state.CardEvent{
			Stage: domain.StageImplement, Kind: state.EventTool, Status: status,
			At: base, Payload: string(p),
		}
	}
	check := func(label, status string) state.CardEvent {
		p, _ := json.Marshal(state.ToolPayload{Label: label})
		return state.CardEvent{
			Stage: domain.StageVerify, Kind: state.EventTool, Status: status,
			At: base, Payload: string(p),
		}
	}
	evs := []state.CardEvent{
		tool("Bash", "go test ./...", state.StatusOK, 1200),
		tool("Bash", "go vet ./...", state.StatusFail, 300),
		tool("Read", "internal/engine/engine.go", state.StatusOK, 40),
		tool("Skill", "code-review", state.StatusOK, 9000),
		tool("Task", "find the regression", state.StatusOK, 60000),
		check("check build: pass", state.StatusOK),
		check("check lint: FAIL (pre-existing, exit 2)", state.StatusFail),
	}
	run := Report(Input{
		Feature: card(1, 100), Events: evs,
		Baseline: []state.CheckResult{{Name: "lint", OK: false}},
	})

	if run.Hands.ToolCalls != 5 || run.Hands.ToolFails != 1 {
		t.Errorf("tool calls/fails = %d/%d, want 5/1", run.Hands.ToolCalls, run.Hands.ToolFails)
	}
	if len(run.Hands.Tools) == 0 || run.Hands.Tools[0].Name != "Bash" || run.Hands.Tools[0].Calls != 2 {
		t.Errorf("tools = %+v, want Bash leading with 2 calls", run.Hands.Tools)
	}
	if run.Hands.Tools[0].Fails != 1 {
		t.Errorf("Bash fails = %d, want 1", run.Hands.Tools[0].Fails)
	}
	if got := run.Hands.Tools[0].Total; got != 1500*time.Millisecond {
		t.Errorf("Bash total = %v, want 1.5s", got)
	}

	// the two delegations, named
	if len(run.Hands.Skills) != 1 || run.Hands.Skills[0].Detail != "code-review" {
		t.Errorf("skills = %+v, want the skill named", run.Hands.Skills)
	}
	if len(run.Hands.Subagents) != 1 || run.Hands.Subagents[0].Name != "Task" {
		t.Errorf("subagents = %+v, want the Task call", run.Hands.Subagents)
	}

	// gummi's own checks are a different thing and are counted apart
	if len(run.Hands.Checks) != 2 {
		t.Fatalf("checks = %+v, want build and lint", run.Hands.Checks)
	}
	byName := map[string]CheckRun{}
	for _, c := range run.Hands.Checks {
		byName[c.Name] = c
	}
	if c := byName["lint"]; c.Fails != 1 || !c.Excused {
		t.Errorf("lint = %+v, want one failure written off as pre-existing", c)
	}
	if c := byName["build"]; c.Runs != 1 || c.Fails != 0 {
		t.Errorf("build = %+v, want one clean run", c)
	}
}

// Who decided what: a human crossing is named, and everything else is
// counted as the machine without enumerating the machine's actors.
func TestReportJudgmentSplitsByWhoAnswered(t *testing.T) {
	gate := func(actor string) state.CardEvent {
		p, _ := json.Marshal(state.GatePayload{From: "plan", To: "implement", Actor: actor})
		return state.CardEvent{Kind: state.EventGate, At: base, Payload: string(p)}
	}
	ask := func(by string) state.CardEvent {
		p, _ := json.Marshal(state.AskPayload{Question: "which?", Answer: "this", By: by})
		return state.CardEvent{Kind: state.EventAsk, At: base, Payload: string(p)}
	}
	park := func(reason string) state.CardEvent {
		p, _ := json.Marshal(state.ParkPayload{Reason: reason, Detail: "verify passed"})
		return state.CardEvent{Kind: state.EventPark, At: base, Payload: string(p)}
	}
	evs := []state.CardEvent{
		gate("user"), gate("autopilot"), gate("review"), gate("caller"),
		ask(state.ActorUser), ask(state.ActorAutopilot),
		park(state.ParkReasonNeedsYou), park(state.ParkReasonQuit),
	}
	run := Report(Input{
		Feature: card(1, 100), Events: evs,
		Rounds: map[domain.RoundKind]int{domain.RoundKindCorrective: 2},
	})

	if g := run.Judgment.Gates; g.Total != 4 || g.ByYou != 2 || g.ByMachine != 2 {
		t.Errorf("gates = %+v, want 4 total / 2 you / 2 machine", g)
	}
	if a := run.Judgment.Asks; a.Total != 2 || a.ByYou != 1 || a.ByMachine != 1 {
		t.Errorf("asks = %+v, want 2 total / 1 each", a)
	}
	// a park recorded because the board quit is gummi going away, not the
	// card stopping to wait for anyone
	if len(run.Judgment.Parks) != 1 || run.Judgment.Parks[0].Reason != state.ParkReasonNeedsYou {
		t.Errorf("parks = %+v, want only the one that waited on a person", run.Judgment.Parks)
	}
	if run.Judgment.Rounds[domain.RoundKindCorrective] != 2 {
		t.Errorf("corrective rounds = %d, want 2", run.Judgment.Rounds[domain.RoundKindCorrective])
	}
}

// A card with no record at all reports zeroes rather than dividing by
// them — every share is defined at zero, because a fresh card is a thing
// surfaces have to render.
func TestReportEmptyCard(t *testing.T) {
	run := Report(Input{Feature: card(0, 0)})
	if len(run.Sessions) != 0 {
		t.Fatalf("sessions = %d, want none", len(run.Sessions))
	}
	if run.Money.ReworkShare() != 0 || run.Money.CacheReadRatio() != 0 ||
		run.Clock.WaitingShare() != 0 || run.Envelope.Utilization() != 0 {
		t.Errorf("an empty card reported a nonzero share: %+v", run)
	}
}

// The peak context a pass reached is the one thing here that could not be
// recovered afterwards, so it rides the stage_exit and must survive.
func TestReportKeepsContextPeak(t *testing.T) {
	evs, spend := build(t, bg004())
	run := Report(Input{Feature: card(42.43, 1350), Events: evs, Spend: spend})
	if p := run.Sessions[6]; p.ContextPeak != 190000 || p.ContextLimit != 200000 {
		t.Errorf("context peak = %d/%d, want 190000/200000", p.ContextPeak, p.ContextLimit)
	}
}

func round(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// A gap nobody was asked about is not time waiting on a person. This is
// the shape of the goal whose stats read "waiting on you 4h52m (91%)"
// while nobody was attending it: the backend stopped serving between two
// passes, no decision was ever opened, and the residual was charged to
// the reader anyway.
func TestReportDoesNotChargeAnUnaskedGapToYou(t *testing.T) {
	enter, _ := json.Marshal(map[string]string{"role": "implementer", "flavor": "stage"})
	exit, _ := json.Marshal(map[string]any{"verdict": ""})
	evs := []state.CardEvent{
		{Stage: domain.StageImplement, Kind: state.EventStageEnter, At: base, Payload: string(enter)},
		{Stage: domain.StageImplement, Kind: state.EventStageExit, At: base.Add(10 * time.Minute), Payload: string(exit)},
		// five hours of nothing, then the card picks up where it left off
		{Stage: domain.StageImplement, Kind: state.EventStageEnter, At: base.Add(5 * time.Hour), Payload: string(enter)},
		{Stage: domain.StageImplement, Kind: state.EventStageExit, At: base.Add(5*time.Hour + 10*time.Minute), Payload: string(exit)},
	}
	run := Report(Input{Feature: card(10, 100), Events: evs})

	if run.Clock.Waiting < 4*time.Hour {
		t.Fatalf("waiting = %v, want the gap to show up as waiting at all", run.Clock.Waiting)
	}
	if run.Clock.OnYou != 0 {
		t.Errorf("on you = %v, want nothing: no decision was ever opened", run.Clock.OnYou)
	}
	if run.Clock.Idle != run.Clock.Waiting {
		t.Errorf("idle = %v, want the whole %v residual", run.Clock.Idle, run.Clock.Waiting)
	}
}

// The other half of the same claim: a card that really did stand at a
// gate overnight is reported as having stood at a gate overnight, and
// the wait ends when the gate is crossed rather than running on.
func TestReportChargesAnOpenDecisionToYou(t *testing.T) {
	enter, _ := json.Marshal(map[string]string{"role": "architect", "flavor": "stage"})
	exit, _ := json.Marshal(map[string]any{"verdict": "approve"})
	open1, _ := json.Marshal(state.DecisionPayload{ID: "d1", Kind: state.DecisionKindGate, Question: "land it?"})
	gate, _ := json.Marshal(state.GatePayload{From: "plan", To: "implement", Actor: "user", ID: "d1"})
	evs := []state.CardEvent{
		{Stage: domain.StagePlan, Kind: state.EventStageEnter, At: base, Payload: string(enter)},
		{Stage: domain.StagePlan, Kind: state.EventStageExit, At: base.Add(10 * time.Minute), Payload: string(exit)},
		{Stage: domain.StagePlan, Kind: state.EventDecisionOpen, At: base.Add(10 * time.Minute), Payload: string(open1)},
		{Stage: domain.StagePlan, Kind: state.EventGate, At: base.Add(8 * time.Hour), Payload: string(gate)},
		{Stage: domain.StageImplement, Kind: state.EventStageEnter, At: base.Add(9 * time.Hour), Payload: string(enter)},
		{Stage: domain.StageImplement, Kind: state.EventStageExit, At: base.Add(9*time.Hour + 10*time.Minute), Payload: string(exit)},
	}
	run := Report(Input{Feature: card(10, 100), Events: evs})

	if want := 7*time.Hour + 50*time.Minute; run.Clock.OnYou != want {
		t.Errorf("on you = %v, want %v — question to crossing", run.Clock.OnYou, want)
	}
	if want := time.Hour; run.Clock.Idle != want {
		t.Errorf("idle = %v, want %v — the hour after the gate, which nobody was asked about", run.Clock.Idle, want)
	}
	if run.Clock.OnYou+run.Clock.Idle != run.Clock.Waiting {
		t.Errorf("on you %v + idle %v != waiting %v", run.Clock.OnYou, run.Clock.Idle, run.Clock.Waiting)
	}
}

// Two decisions standing open together are one wait, not two: a card
// cannot wait on two people for twice the time.
func TestReportUnionsOverlappingDecisions(t *testing.T) {
	enter, _ := json.Marshal(map[string]string{"role": "architect", "flavor": "stage"})
	exit, _ := json.Marshal(map[string]any{"verdict": ""})
	d1, _ := json.Marshal(state.DecisionPayload{ID: "d1", Kind: state.DecisionKindAsk})
	d2, _ := json.Marshal(state.DecisionPayload{ID: "d2", Kind: state.DecisionKindAsk})
	a1, _ := json.Marshal(state.AskPayload{ID: "d1", Answer: "yes"})
	a2, _ := json.Marshal(state.AskPayload{ID: "d2", Answer: "yes"})
	evs := []state.CardEvent{
		{Stage: domain.StagePlan, Kind: state.EventStageEnter, At: base, Payload: string(enter)},
		{Stage: domain.StagePlan, Kind: state.EventStageExit, At: base.Add(time.Minute), Payload: string(exit)},
		{Stage: domain.StagePlan, Kind: state.EventDecisionOpen, At: base.Add(time.Minute), Payload: string(d1)},
		{Stage: domain.StagePlan, Kind: state.EventDecisionOpen, At: base.Add(2 * time.Minute), Payload: string(d2)},
		{Stage: domain.StagePlan, Kind: state.EventAsk, At: base.Add(3 * time.Minute), Payload: string(a1)},
		{Stage: domain.StagePlan, Kind: state.EventAsk, At: base.Add(4 * time.Minute), Payload: string(a2)},
		{Stage: domain.StagePlan, Kind: state.EventStageEnter, At: base.Add(4 * time.Minute), Payload: string(enter)},
		{Stage: domain.StagePlan, Kind: state.EventStageExit, At: base.Add(5 * time.Minute), Payload: string(exit)},
	}
	run := Report(Input{Feature: card(10, 100), Events: evs})

	if want := 3 * time.Minute; run.Clock.OnYou != want {
		t.Errorf("on you = %v, want %v — one union, not the 4m the two spans sum to", run.Clock.OnYou, want)
	}
}

// Not every turn a card pays for is a pass. A goal's lead turns and the
// one-shot scribe passes leave no stage_enter to bracket them, so the
// pass list cannot hold them — and a report that only listed passes said
// a one-card goal cost 80 credits while its own stage table said 149.
func TestReportNamesSpendThatBelongsToNoPass(t *testing.T) {
	passes := []pass{{stage: domain.StagePlan, role: "architect", flavor: "stage", minutes: 2, turns: 4, credits: 19.2}}
	evs, spend := build(t, passes)
	// the lead's turns and a one-shot scribe pass: rollup rows under
	// session keys no stage session ever opened
	spend = append(spend,
		state.StageSpend{Stage: domain.StageImplement, Session: "lead-1", Role: "lead",
			Model: "m", Credits: 50.38, UpdatedAt: base},
		state.StageSpend{Stage: domain.StageImplement, Session: "oneshot-1", Role: "scribe",
			Model: "m", Credits: 18.29, UpdatedAt: base},
	)
	run := Report(Input{Feature: card(87.87, 2000), Events: evs, Spend: spend})

	if want := 68.67; run.Money.Elsewhere < want-0.01 || run.Money.Elsewhere > want+0.01 {
		t.Errorf("elsewhere = %.2f, want %.2f", run.Money.Elsewhere, want)
	}
	if got := run.Money.FirstPass + run.Money.Rework + run.Money.Elsewhere; got < 87.86 || got > 87.88 {
		t.Errorf("first pass + rework + elsewhere = %.2f, want the card's %.2f", got, 87.87)
	}
	// and it says which roles, because "the lead" and "a scribe one-shot"
	// are different facts about where a goal's money went
	if len(run.Money.ElsewhereBy) != 2 || run.Money.ElsewhereBy[0].Name != "lead" {
		t.Errorf("elsewhere by role = %+v, want the lead's share named and largest", run.Money.ElsewhereBy)
	}
}

// A card whose every credit belongs to a pass reports none elsewhere,
// rather than a rounding crumb that would make a reader look for a turn
// that never happened.
func TestReportNamesNothingElsewhereWhenEveryPassIsAccountedFor(t *testing.T) {
	evs, spend := build(t, bg004())
	run := Report(Input{Feature: card(42.43, 1350), Events: evs, Spend: spend})
	if run.Money.Elsewhere != 0 {
		t.Errorf("elsewhere = %v, want nothing", run.Money.Elsewhere)
	}
}

// A card recorded before the rollup carried session keys has passes that
// match nothing, and that is not evidence of a turn outside them. Read
// the other way it reported every credit of such a card as spent on
// turns that are not passes — while the pass list held the same credits,
// which is the double count this figure exists to prevent.
func TestReportNamesNothingElsewhereOnACardWithNoSessionKeys(t *testing.T) {
	enter, _ := json.Marshal(map[string]string{"role": "architect", "flavor": "stage"})
	exit, _ := json.Marshal(map[string]any{"verdict": "pass", "credits": 40.0})
	evs := []state.CardEvent{
		{Stage: domain.StagePlan, Kind: state.EventStageEnter, At: base, Payload: string(enter)},
		{Stage: domain.StagePlan, Kind: state.EventStageExit, At: base.Add(time.Minute), Payload: string(exit)},
	}
	// the rollup as it was written before session keys: no Session
	spend := []state.StageSpend{{Stage: domain.StagePlan, Role: "architect", Model: "m", Credits: 40, UpdatedAt: base}}
	run := Report(Input{Feature: card(40, 500), Events: evs, Spend: spend})

	if run.Money.Elsewhere != 0 {
		t.Errorf("elsewhere = %v, want nothing — an unkeyed row is not a turn outside the passes", run.Money.Elsewhere)
	}
	if got := run.Money.FirstPass + run.Money.Rework + run.Money.Elsewhere; got > run.Money.Credits+0.01 {
		t.Errorf("first pass + rework + elsewhere = %.2f, more than the card's %.2f", got, run.Money.Credits)
	}
}
