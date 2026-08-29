package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
)

func TestParseVerdict(t *testing.T) {
	cases := map[string]reviewVerdict{
		"looks good\nVERDICT: pass":                        verdictPass,
		"issues found\nVERDICT: changes\n":                 verdictChanges,
		"VERDICT: PASS":                                    verdictPass,
		"  verdict:   changes  ":                           verdictChanges,
		"no verdict here":                                  verdictUnclear,
		"VERDICT: pass\nthen later\nVERDICT: changes":      verdictChanges, // last wins
		"the word verdict: pass appears mid-sentence only": verdictUnclear, // not on its own line
		"rock build broken\nVERDICT: fail":                 verdictFail,
		"VERDICT: FAIL":                                    verdictFail,
		"no pip in this sandbox\nVERDICT: blocked":         verdictBlocked,
		"VERDICT: BLOCKED":                                 verdictBlocked,
		// A chatty model glues the verdict to the previous sentence with no
		// intervening newline; anchor the fallback to end-of-text so the
		// tail form still resolves while a mid-text mention doesn't.
		"…making check 16 redundant.VERDICT: changes": verdictChanges,
		"we agreed VERDICT: pass was too generous.":   verdictUnclear,
	}
	for in, want := range cases {
		if got := parseVerdict(in); got != want {
			t.Errorf("parseVerdict(%q) = %d, want %d", in, got, want)
		}
	}
}

// reviewAgent scripts an autonomous agent whose reply depends on the
// feature's stage (passed via SystemHints) — it emits the given verdict
// only for review sessions.
func verdictAgent(reply func(opts agent.SessionOpts) string) *agent.Fake {
	return &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: reply(opts)},
			{Kind: agent.EventIdle},
		}
	}}
}

func isReview(opts agent.SessionOpts) bool { return opts.Role == agent.RoleReviewer }

// isVerify spots the verify session by its stage hint — review and
// verify share the reviewer role, so the role alone no longer
// distinguishes them.
func isVerify(opts agent.SessionOpts) bool {
	return strings.Contains(strings.Join(opts.SystemHints, "\n"), "Stage: Verify")
}

// advanceTo drives g until the feature reaches the target stage.
func advanceTo(t *testing.T, m *Shell, target domain.Stage) *Shell {
	t.Helper()
	for i := 0; i < 8 && m.rows[0].F.Stage != target; i++ {
		m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	}
	if m.rows[0].F.Stage != target {
		t.Fatalf("could not reach %s (at %s)", target, m.rows[0].F.Stage)
	}
	return m
}

func TestReviewPassAdvancesToVerify(t *testing.T) {
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		if isReview(opts) {
			return "No issues.\nVERDICT: pass"
		}
		return "done"
	})
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageReview)

	// run review; a passing verdict should auto-advance to verify
	m = openAndAttach(t, m)
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if m.rows[0].F.Stage != domain.StageVerify {
		t.Fatalf("review pass did not advance to verify (at %s)", m.rows[0].F.Stage)
	}
}

func TestReviewChangesBouncesAndLoops(t *testing.T) {
	// review always requests changes; the loop should bounce to implement,
	// re-review, and after the cap escalate to the inbox.
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		if isReview(opts) {
			return "Found a bug.\nVERDICT: changes"
		}
		return "fixed"
	})
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageReview)

	m = openAndAttach(t, m) // first review
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	// after the cap, the loop stops and escalates
	if m.round("FD-001", domain.RoundKindReview) != 0 {
		t.Errorf("rounds not reset after escalation: %d", m.round("FD-001", domain.RoundKindReview))
	}
	found := false
	for _, it := range m.inbox.list() {
		if it.Feature == "FD-001" && it.Kind == attnGate {
			found = true
		}
	}
	if !found {
		t.Error("changes-loop did not escalate to the inbox after the cap")
	}
}

func TestReviewUnclearVerdictEscalates(t *testing.T) {
	ag := verdictAgent(func(opts agent.SessionOpts) string { return "I reviewed it." })
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageReview)
	m = openAndAttach(t, m)
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if m.rows[0].F.Stage != domain.StageReview {
		t.Errorf("unclear verdict advanced the stage to %s", m.rows[0].F.Stage)
	}
	if m.inbox.len() == 0 {
		t.Error("unclear verdict did not escalate to the inbox")
	}
}

// runVerify drives a feature through review (which passes and
// auto-runs verify) with the given verify-stage reply, and drains the
// loop until the verify gate is raised.
func runVerify(t *testing.T, verifyReply string) *Shell {
	t.Helper()
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		switch {
		case isVerify(opts): // before isReview: verify runs as reviewer too
			return verifyReply
		case isReview(opts):
			return "No issues.\nVERDICT: pass"
		default:
			return "done"
		}
	})
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageReview)
	m = openAndAttach(t, m) // run review → auto verify
	settleChat(t, eng)
	m = drainEngineLoop(t, m)
	if m.rows[0].F.Stage != domain.StageVerify {
		t.Fatalf("flow did not reach verify (at %s)", m.rows[0].F.Stage)
	}
	return m
}

// verifyGate returns the feature's single gate item, failing the test
// when the inbox holds anything else.
func verifyGate(t *testing.T, m *Shell) attnItem {
	t.Helper()
	items := m.inbox.list()
	if len(items) != 1 || items[0].Kind != attnGate {
		t.Fatalf("verify did not raise exactly one gate: %+v", items)
	}
	return items[0]
}

func TestVerifyPassRaisesCleanGate(t *testing.T) {
	m := runVerify(t, "All checks green.\nVERDICT: pass")
	it := verifyGate(t, m)
	if it.Escalated {
		t.Error("a passing verify escalated instead of gating clean")
	}
	if !strings.Contains(it.Text, "passed") {
		t.Errorf("gate text does not say it passed: %q", it.Text)
	}
	acts := nextActions(m.nextInputFor(m.rows[0]))
	if keysOf(acts) != "g d b" {
		t.Fatalf("pass suggestions = %q, want g d b", keysOf(acts))
	}
	if !strings.Contains(acts[0].why, "verify passed") {
		t.Errorf("landing why does not carry the verdict: %q", acts[0].why)
	}
}

func TestVerifyFailEscalates(t *testing.T) {
	m := runVerify(t, "Rock build broken.\nVERDICT: fail")
	it := verifyGate(t, m)
	if !it.Escalated {
		t.Error("a failing verify raised a clean gate instead of escalating")
	}
	if !strings.Contains(it.Text, "FAILED") {
		t.Errorf("gate text does not say it failed: %q", it.Text)
	}
	acts := nextActions(m.nextInputFor(m.rows[0]))
	if keysOf(acts) != "s b g" {
		t.Fatalf("fail suggestions = %q, want s b g (read evidence first)", keysOf(acts))
	}
}

// The FD-004 moment: verify can't execute the plan (missing deps, no
// live service). blocked escalates like fail, but the guidance steers
// at the environment — the bounce is not among the suggestions.
func TestVerifyBlockedEscalatesWithoutBounce(t *testing.T) {
	m := runVerify(t, "No pytest in this workspace.\nVERDICT: blocked")
	it := verifyGate(t, m)
	if !it.Escalated {
		t.Error("a blocked verify raised a clean gate instead of escalating")
	}
	if !strings.Contains(it.Text, "BLOCKED") {
		t.Errorf("gate text does not say it is blocked: %q", it.Text)
	}
	if !strings.Contains(it.Text, "re-implementing won't help") {
		t.Errorf("gate text does not warn off the bounce: %q", it.Text)
	}
	acts := nextActions(m.nextInputFor(m.rows[0]))
	if keysOf(acts) != "s enter g" {
		t.Fatalf("blocked suggestions = %q, want s enter g (no bounce)", keysOf(acts))
	}
	if !strings.Contains(acts[0].why, "environment") {
		t.Errorf("blocked why does not name the environment: %q", acts[0].why)
	}
}

// The FD-003 moment: a verify session that ends on a question instead
// of a verdict escalates as unclear rather than gating as finished.
func TestVerifyUnclearVerdictEscalates(t *testing.T) {
	m := runVerify(t, "Two options remain. Which should be done next?")
	it := verifyGate(t, m)
	if !it.Escalated {
		t.Error("a verdict-less verify raised a clean gate instead of escalating")
	}
	if !strings.Contains(it.Text, "no clear verdict") {
		t.Errorf("gate text does not flag the missing verdict: %q", it.Text)
	}
	acts := nextActions(m.nextInputFor(m.rows[0]))
	if keysOf(acts) != "s b g" {
		t.Fatalf("unclear suggestions = %q, want s b g", keysOf(acts))
	}
	if !strings.Contains(acts[0].why, "no clear verdict") {
		t.Errorf("unclear why does not explain itself: %q", acts[0].why)
	}
}

// verifyBounces counts only verify→work edges — the review loop's own
// bounces and forward moves must not inflate the failure count.
func TestVerifyBounces(t *testing.T) {
	tr := func(from, to domain.Stage) state.TransitionRecord {
		return state.TransitionRecord{From: from, To: to}
	}
	cases := []struct {
		name string
		hist []state.TransitionRecord
		kind domain.Kind
		want int
	}{
		{"no history", nil, domain.KindFeature, 0},
		{"forward only", []state.TransitionRecord{
			tr(domain.StageImplement, domain.StageReview),
			tr(domain.StageReview, domain.StageVerify),
		}, domain.KindFeature, 0},
		{"review bounces don't count", []state.TransitionRecord{
			tr(domain.StageReview, domain.StageImplement),
			tr(domain.StageReview, domain.StageImplement),
		}, domain.KindFeature, 0},
		{"two verify bounces", []state.TransitionRecord{
			tr(domain.StageVerify, domain.StageImplement),
			tr(domain.StageImplement, domain.StageReview),
			tr(domain.StageReview, domain.StageVerify),
			tr(domain.StageVerify, domain.StageImplement),
		}, domain.KindFeature, 2},
		{"bug bounces target fix", []state.TransitionRecord{
			tr(domain.StageVerify, domain.StageFix),
		}, domain.KindBug, 1},
		{"kind mismatch doesn't count", []state.TransitionRecord{
			tr(domain.StageVerify, domain.StageFix),
		}, domain.KindFeature, 0},
	}
	for _, tc := range cases {
		if got := verifyBounces(tc.hist, tc.kind); got != tc.want {
			t.Errorf("%s: verifyBounces = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// After a prior bounce, the fail suggestions drop the bounce to last
// with a warning — pure nextActions table check.
func TestVerifyRepeatedFailDeemphasizesBounce(t *testing.T) {
	in := nextInput{
		stage: domain.StageVerify, kind: domain.KindFeature,
		attn: attnGate, escalated: true,
		verdict: verdictFail, verifyBounces: 2,
	}
	acts := nextActions(in)
	if keysOf(acts) != "s g b" {
		t.Fatalf("repeat-fail suggestions = %q, want s g b (bounce last)", keysOf(acts))
	}
	if !strings.Contains(acts[2].why, "unlikely to help") {
		t.Errorf("bounce why does not warn: %q", acts[2].why)
	}
	if !strings.Contains(acts[0].why, "3 times") {
		t.Errorf("read why does not count the failures: %q", acts[0].why)
	}
}

// The loop-breaker end to end: fail verify, bounce, fail again — the
// second gate warns that re-implementing is unlikely to help.
func TestVerifyLoopBreakerWarnsOnSecondFailure(t *testing.T) {
	m := runVerify(t, "Rock build broken.\nVERDICT: fail")
	it := verifyGate(t, m)
	if strings.Contains(it.Text, "unlikely to help") {
		t.Fatalf("first failure already warns: %q", it.Text)
	}

	m = press(t, m, tea.KeyPressMsg{Code: 'b', Text: "b"}) // bounce to implement
	if m.rows[0].F.Stage != domain.StageImplement {
		t.Fatalf("bounce did not reach implement (at %s)", m.rows[0].F.Stage)
	}
	m = advanceTo(t, m, domain.StageVerify) // implement → review → verify by hand
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	m = drainEngineLoop(t, m)

	it = verifyGate(t, m)
	if !strings.Contains(it.Text, "2nd time") || !strings.Contains(it.Text, "unlikely to help") {
		t.Errorf("second failure gate does not warn: %q", it.Text)
	}
	acts := nextActions(m.nextInputFor(m.rows[0]))
	if keysOf(acts) != "s g b" {
		t.Fatalf("second-failure suggestions = %q, want s g b", keysOf(acts))
	}
}

// planAgent scripts the plan-critique loop: the architect writes the
// plan, and each reviewer (critique) session answers with the next
// verdict from the list (the last one repeats).
func planAgent(counter *atomic.Int32, verdicts ...string) *agent.Fake {
	return verdictAgent(func(opts agent.SessionOpts) string {
		if !isReview(opts) {
			return "plan written"
		}
		n := int(counter.Add(1)) - 1
		if n >= len(verdicts) {
			n = len(verdicts) - 1
		}
		return verdicts[n]
	})
}

// runPlan advances the feature to Plan, runs it, and drains the
// critique loop to completion.
func runPlan(t *testing.T, ag *agent.Fake) *Shell {
	t.Helper()
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StagePlan)
	m = openAndAttach(t, m) // run plan
	settleChat(t, eng)
	return drainEngineLoop(t, m)
}

func TestPlanCritiqueChangesReplansThenPasses(t *testing.T) {
	// the critique requests changes once, then passes: plan → critique →
	// replan → critique → gate, all without leaving the Plan stage.
	var critiques atomic.Int32
	m := runPlan(t, planAgent(&critiques, "Missing authz check.\nVERDICT: changes", "Sound.\nVERDICT: pass"))

	if got := critiques.Load(); got != 2 {
		t.Errorf("critique ran %d times, want 2 (changes → replan → pass)", got)
	}
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Errorf("critique loop moved the stage to %s", m.rows[0].F.Stage)
	}
	if m.round("FD-001", domain.RoundKindPlan) != 0 {
		t.Errorf("rounds not reset after pass: %d", m.round("FD-001", domain.RoundKindPlan))
	}
	if m.inbox.len() != 1 || m.inbox.list()[0].Kind != attnGate {
		t.Fatalf("clean critique did not raise the approval gate: %+v", m.inbox.list())
	}
}

func TestReCritiqueKickoffPointsAtPriorThreads(t *testing.T) {
	// the second critique round burns down the first round's threads
	// instead of re-judging from scratch: its kickoff carries the
	// re-critique note; the first round's does not.
	var mu sync.Mutex
	var kickoffs []string
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		reply := "plan written"
		if isReview(opts) {
			mu.Lock()
			kickoffs = append(kickoffs, msg)
			n := len(kickoffs)
			mu.Unlock()
			if n == 1 {
				reply = "Missing authz check.\nVERDICT: changes"
			} else {
				reply = "Sound.\nVERDICT: pass"
			}
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: reply},
			{Kind: agent.EventIdle},
		}
	}}
	runPlan(t, ag)

	mu.Lock()
	defer mu.Unlock()
	if len(kickoffs) != 2 {
		t.Fatalf("critique ran %d times, want 2", len(kickoffs))
	}
	if strings.Contains(kickoffs[0], "re-critique") {
		t.Errorf("first critique kickoff carries the re-critique note: %q", kickoffs[0])
	}
	if !strings.Contains(kickoffs[1], "re-critique") {
		t.Errorf("re-critique kickoff missing the note: %q", kickoffs[1])
	}
}

func TestRunStageResumesCritique(t *testing.T) {
	// a transient backend error during the plan critique pauses the
	// critique session; re-running the stage must resume the critique
	// leg — the plan is already written — not restart the plan writer.
	var mu sync.Mutex
	var planRuns, critiqueRuns int
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		joined := strings.Join(opts.SystemHints, "\n")
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.Contains(joined, "Stage: Plan critique"):
			critiqueRuns++
			if critiqueRuns == 1 {
				return []agent.Event{{Kind: agent.EventError, Err: errors.New("transient api error")}}
			}
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "Sound.\nVERDICT: pass"},
				{Kind: agent.EventIdle},
			}
		case strings.Contains(joined, "Stage: Plan"):
			planRuns++
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "plan written"},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StagePlan)
	m = openAndAttach(t, m) // run the plan writer
	settleChat(t, eng)
	m = drainEngineLoop(t, m) // writer idles → critique starts → errors

	// the failed critique is paused with its pass flag intact
	s := eng.Get("FD-001")
	if s == nil || s.State() != engine.StatePaused || !s.Snapshot().Critique {
		t.Fatal("want a paused critique session before the re-run")
	}
	mu.Lock()
	if planRuns != 1 || critiqueRuns != 1 {
		mu.Unlock()
		t.Fatalf("plan/critique runs = %d/%d, want 1/1 before the re-run", planRuns, critiqueRuns)
	}
	mu.Unlock()

	// re-run the stage: the critique leg resumes, the plan writer does not
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	settleChat(t, eng)
	drainEngineLoop(t, m)
	mu.Lock()
	defer mu.Unlock()
	if planRuns != 1 {
		t.Errorf("plan writer ran %d times, want 1 (re-run must not restart it)", planRuns)
	}
	if critiqueRuns != 2 {
		t.Errorf("critique ran %d times, want 2 (the resumed leg)", critiqueRuns)
	}
}

func TestPlanCritiqueEscalatesAfterCap(t *testing.T) {
	// a critique that never passes stops looping past the cap and hands
	// the plan to the human.
	var critiques atomic.Int32
	m := runPlan(t, planAgent(&critiques, "Still broken.\nVERDICT: changes"))

	if got := critiques.Load(); got != int32(maxPlanRounds)+1 {
		t.Errorf("critique ran %d times, want %d (initial + %d replans)", got, maxPlanRounds+1, maxPlanRounds)
	}
	if m.round("FD-001", domain.RoundKindPlan) != 0 {
		t.Errorf("rounds not reset after escalation: %d", m.round("FD-001", domain.RoundKindPlan))
	}
	if m.rows[0].F.Stage != domain.StagePlan {
		t.Errorf("escalation moved the stage to %s", m.rows[0].F.Stage)
	}
	if m.inbox.len() != 1 || m.inbox.list()[0].Kind != attnGate {
		t.Fatalf("capped critique did not escalate to the inbox: %+v", m.inbox.list())
	}
}

func TestPlanCritiqueUnclearVerdictEscalates(t *testing.T) {
	var critiques atomic.Int32
	m := runPlan(t, planAgent(&critiques, "I have concerns."))

	if got := critiques.Load(); got != 1 {
		t.Errorf("unclear verdict looped: critique ran %d times", got)
	}
	if m.inbox.len() != 1 || m.inbox.list()[0].Kind != attnGate {
		t.Fatalf("unclear critique verdict did not escalate: %+v", m.inbox.list())
	}
}

// drainEngineLoop pumps engine events AND the review-loop commands they
// produce until the loop settles (no active session, no pending events).
func drainEngineLoop(t *testing.T, m *Shell) *Shell {
	t.Helper()
	for i := 0; i < 60; i++ {
		select {
		case ev := <-m.engine.Events():
			cmd := m.handleEngineEvent(ev)
			m = pump(t, m, cmd) // run any auto-step (transition + run)
		case <-time.After(80 * time.Millisecond):
			return m
		}
	}
	return m
}

// failRoundStore fails reads, writes, or both on the keyed rounds seam —
// the fail-closed proof for the TUI's round persistence, shared by the
// plan and review round tests (mirrors the headless driver's fake).
type failRoundStore struct {
	failLoad  bool
	failWrite bool
}

func (f *failRoundStore) Rounds(context.Context, domain.FeatureID, domain.RoundKind) (int, error) {
	if f.failLoad {
		return 0, errors.New("read failed")
	}
	return 0, nil
}

func (f *failRoundStore) IncrementRounds(context.Context, domain.FeatureID, domain.RoundKind) error {
	if f.failWrite {
		return errors.New("bump failed")
	}
	return nil
}

func (f *failRoundStore) ClearRounds(context.Context, domain.FeatureID, domain.RoundKind) error {
	if f.failWrite {
		return errors.New("clear failed")
	}
	return nil
}

// drainUntil consumes engine events and the review-loop commands they
// produce until pred is true or the loop settles — the plan-loop analog
// of drainEngineLoop that lets a test stop at an intermediate verdict.
func drainUntil(t *testing.T, m *Shell, pred func(*Shell) bool) *Shell {
	t.Helper()
	for i := 0; i < 60; i++ {
		if pred(m) {
			return m
		}
		select {
		case ev := <-m.engine.Events():
			cmd := m.handleEngineEvent(ev)
			m = pump(t, m, cmd)
		case <-time.After(80 * time.Millisecond):
			return m
		}
	}
	return m
}

// A plan resumed through the TUI hydrates its round counter from the
// store (a prior, possibly headless session's count), then bumps it —
// seed(1) + one changes verdict(1) = 2 — instead of restarting at a fresh
// zero budget.
func TestPlanRoundsSeedsFromStoreOnPlanEntry(t *testing.T) {
	var critiques atomic.Int32
	m, eng := chatWorkspace(t, planAgent(&critiques, "Missing authz check.\nVERDICT: changes", "Sound.\nVERDICT: pass"))
	m = advanceTo(t, m, domain.StagePlan)
	// a prior session burned one round; the store is the shared record
	if err := m.store.SetPlanRounds(context.Background(), "FD-001", 1); err != nil {
		t.Fatal(err)
	}
	m = openAndAttach(t, m) // run plan → seed(1)
	settleChat(t, eng)
	m = drainUntil(t, m, func(m *Shell) bool { return m.round("FD-001", domain.RoundKindPlan) == 2 })
	if got := m.round("FD-001", domain.RoundKindPlan); got != 2 {
		t.Errorf("m.round(plan) = %d, want 2 (seed 1 + bump 1)", got)
	}
	if got, err := m.store.Rounds(context.Background(), "FD-001", domain.RoundKindPlan); err != nil || got != 2 {
		t.Errorf("store.Rounds(plan) = %d, %v; want 2", got, err)
	}
}

// Every completion path that resets the in-memory counter also write-
// throughs the clear, and a failed clear never re-grants budget.
func TestPlanRoundsClearOnPassAndExhaustion(t *testing.T) {
	t.Run("pass clears", func(t *testing.T) {
		var critiques atomic.Int32
		m := runPlan(t, planAgent(&critiques, "Sound.\nVERDICT: pass"))
		if got := m.round("FD-001", domain.RoundKindPlan); got != 0 {
			t.Errorf("m.round(plan) after pass = %d, want 0", got)
		}
		if got, err := m.store.Rounds(context.Background(), "FD-001", domain.RoundKindPlan); err != nil || got != 0 {
			t.Errorf("store.Rounds(plan) after pass = %d, %v; want 0", got, err)
		}
	})
	t.Run("exhaustion clears", func(t *testing.T) {
		m, _ := chatWorkspace(t, verdictAgent(func(opts agent.SessionOpts) string { return "done" }))
		m.setRound("FD-001", domain.RoundKindPlan, 1)
		if err := m.store.SetPlanRounds(context.Background(), "FD-001", 1); err != nil {
			t.Fatal(err)
		}
		m = pump(t, m, m.handleEngineEvent(engine.Event{Kind: engine.EventExhausted, Feature: "FD-001", Stage: domain.StagePlan, Committed: false}))
		if got := m.round("FD-001", domain.RoundKindPlan); got != 0 {
			t.Errorf("m.round(plan) after exhaustion = %d, want 0", got)
		}
		if got, err := m.store.Rounds(context.Background(), "FD-001", domain.RoundKindPlan); err != nil || got != 0 {
			t.Errorf("store.Rounds(plan) after exhaustion = %d, %v; want 0", got, err)
		}
	})
	t.Run("exhaustion write-fail keeps count", func(t *testing.T) {
		m, _ := chatWorkspace(t, verdictAgent(func(opts agent.SessionOpts) string { return "done" }))
		m.setRound("FD-001", domain.RoundKindPlan, 1)
		m.roundStore = &failRoundStore{failWrite: true}
		m = pump(t, m, m.handleEngineEvent(engine.Event{Kind: engine.EventExhausted, Feature: "FD-001", Stage: domain.StagePlan, Committed: false}))
		if got := m.round("FD-001", domain.RoundKindPlan); got != 1 {
			t.Errorf("m.round(plan) after failed exhaustion clear = %d, want 1 (count not lost)", got)
		}
		if !m.notice.isErr {
			t.Error("no error notice raised on a failed exhaustion clear")
		}
	})
}

// Store failures are never silently ignored: a failed seed read aborts
// plan dispatch and a failed write-through halts the loop leg.
func TestPlanRoundsWriteThroughFailsClosed(t *testing.T) {
	t.Run("read aborts plan dispatch", func(t *testing.T) {
		m, eng := chatWorkspace(t, verdictAgent(func(opts agent.SessionOpts) string { return "plan written" }))
		m = advanceTo(t, m, domain.StagePlan)
		m.roundStore = &failRoundStore{failLoad: true}
		m = openAndAttach(t, m)
		if !m.notice.isErr {
			t.Error("no error notice on a failing seed read")
		}
		if s := eng.Get("FD-001"); s != nil && (s.State() == engine.StateRunning || s.State() == engine.StateQueued) {
			t.Error("plan session started despite a failing seed read")
		}
	})
	t.Run("write aborts the round", func(t *testing.T) {
		var critiques atomic.Int32
		m, eng := chatWorkspace(t, planAgent(&critiques, "Missing authz check.\nVERDICT: changes"))
		m = advanceTo(t, m, domain.StagePlan)
		m.roundStore = &failRoundStore{failWrite: true}
		m = openAndAttach(t, m)
		settleChat(t, eng)
		m = drainEngineLoop(t, m)
		if got := critiques.Load(); got != 1 {
			t.Errorf("critique ran %d times, want 1 (no replan after a failed write)", got)
		}
		if !hasAttn(m, attnFailure) {
			t.Error("no failure attention raised on a failed write-through")
		}
	})
}

// hasAttn reports whether the inbox holds a needs-attention item of the
// given kind for the test's feature.
func hasAttn(m *Shell, kind attnKind) bool {
	for _, it := range m.inbox.list() {
		if it.Kind == kind {
			return true
		}
	}
	return false
}

// A finished plan-writer session resumed through the TUI routes to the
// critique (via planStep) — the revised plan is already on disk — never
// back to the plan writer. With a prior round burned, the resumed critique
// uses the re-critique kickoff.
func TestRunStageResumesFinishedPlanAsCritique(t *testing.T) {
	var mu sync.Mutex
	var reviewerMsgs []string
	var writerRuns int
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if opts.Role == agent.RoleReviewer {
			mu.Lock()
			reviewerMsgs = append(reviewerMsgs, msg)
			mu.Unlock()
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "Sound plan.\nVERDICT: pass"},
				{Kind: agent.EventIdle},
			}
		}
		if opts.Role == agent.RoleArchitect {
			mu.Lock()
			writerRuns++
			mu.Unlock()
			// the plan writer exhausts, so it parks as a finished (StateDone)
			// writer session without auto-continuing the loop.
			return []agent.Event{{Kind: agent.EventBudgetExhausted}}
		}
		// scribe/interactive sessions respond normally (DiscoverChecks waits
		// on an idle turn, not an exhaustion).
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "done"},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StagePlan)

	// run the plan writer; it exhausts into a finished (StateDone, !Critique)
	// writer session, parked mid-cycle.
	m = openAndAttach(t, m)
	m = drainEngineLoop(t, m) // process the writer's exhaustion event
	s := eng.Get("FD-001")
	if s == nil || s.State() != engine.StateDone || s.Snapshot().Critique {
		t.Fatalf("want a finished plan-writer session, got state=%v critique=%v", s.State(), s.Snapshot().Critique)
	}
	mu.Lock()
	if got := len(reviewerMsgs); got != 0 {
		mu.Unlock()
		t.Fatalf("a critique ran before the resume: %d", got)
	}
	mu.Unlock()

	// a prior round burned on the card: the resumed critique re-critiques.
	if err := m.store.SetPlanRounds(context.Background(), "FD-001", 1); err != nil {
		t.Fatal(err)
	}

	// resume the stage: the finished writer must route to a critique (via
	// planStep), not back to the plan writer.
	f, err := m.store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	pump(t, m, m.runStage(f))
	settleChat(t, eng)
	mu.Lock()
	defer mu.Unlock()
	if writerRuns != 1 {
		t.Errorf("plan writer ran %d times, want 1 (the resume must not restart it)", writerRuns)
	}
	if len(reviewerMsgs) != 1 {
		t.Fatalf("critique ran %d times, want 1 (the resumed leg)", len(reviewerMsgs))
	}
	if !strings.Contains(reviewerMsgs[0], "re-critique") {
		t.Errorf("resumed critique kickoff missing the re-critique note: %q", reviewerMsgs[0])
	}
	if got := eng.Get("FD-001").Snapshot().Critique; !got {
		t.Error("resumed session is not a critique pass")
	}
}

// A review resumed (or relaunched) through the TUI hydrates its round
// counter from the store — a prior, possibly headless session's count —
// then bumps it: seed(1) + one changes verdict(1) = 2, instead of
// restarting at a fresh zero budget.
func TestReviewRoundsSeedsFromStoreOnReviewEntry(t *testing.T) {
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		if isReview(opts) {
			return "Found a bug.\nVERDICT: changes"
		}
		return "done"
	})
	m, eng := chatWorkspace(t, ag)
	m = advanceTo(t, m, domain.StageReview)
	// a prior session burned one round; the store is the shared record
	if err := m.store.SetReviewRounds(context.Background(), "FD-001", 1); err != nil {
		t.Fatal(err)
	}
	m = openAndAttach(t, m) // run review → seed(1)
	settleChat(t, eng)
	m = drainUntil(t, m, func(m *Shell) bool { return m.round("FD-001", domain.RoundKindReview) == 2 })
	if got := m.round("FD-001", domain.RoundKindReview); got != 2 {
		t.Errorf("m.round(review) = %d, want 2 (seed 1 + bump 1)", got)
	}
	if got, err := m.store.Rounds(context.Background(), "FD-001", domain.RoundKindReview); err != nil || got != 2 {
		t.Errorf("store.Rounds(review) = %d, %v; want 2", got, err)
	}
}

// Every completion path that resets the in-memory counter also write-
// throughs the clear, and a failed clear never re-grants budget.
func TestReviewRoundsClearOnPassAndExhaustion(t *testing.T) {
	t.Run("pass clears", func(t *testing.T) {
		ag := verdictAgent(func(opts agent.SessionOpts) string {
			if isReview(opts) {
				return "Clean.\nVERDICT: pass"
			}
			return "done"
		})
		m, _ := chatWorkspace(t, ag)
		m = advanceTo(t, m, domain.StageReview)
		m = openAndAttach(t, m)
		m = drainEngineLoop(t, m)
		if got := m.round("FD-001", domain.RoundKindReview); got != 0 {
			t.Errorf("m.round(review) after pass = %d, want 0", got)
		}
		if got, err := m.store.Rounds(context.Background(), "FD-001", domain.RoundKindReview); err != nil || got != 0 {
			t.Errorf("store.Rounds(review) after pass = %d, %v; want 0", got, err)
		}
	})
	t.Run("exhaustion clears", func(t *testing.T) {
		m, _ := chatWorkspace(t, verdictAgent(func(opts agent.SessionOpts) string { return "done" }))
		m.setRound("FD-001", domain.RoundKindReview, 1)
		if err := m.store.SetReviewRounds(context.Background(), "FD-001", 1); err != nil {
			t.Fatal(err)
		}
		m = pump(t, m, m.handleEngineEvent(engine.Event{Kind: engine.EventExhausted, Feature: "FD-001", Stage: domain.StageReview, Committed: false}))
		if got := m.round("FD-001", domain.RoundKindReview); got != 0 {
			t.Errorf("m.round(review) after exhaustion = %d, want 0", got)
		}
		if got, err := m.store.Rounds(context.Background(), "FD-001", domain.RoundKindReview); err != nil || got != 0 {
			t.Errorf("store.Rounds(review) after exhaustion = %d, %v; want 0", got, err)
		}
	})
	t.Run("exhaustion write-fail keeps count", func(t *testing.T) {
		m, _ := chatWorkspace(t, verdictAgent(func(opts agent.SessionOpts) string { return "done" }))
		m.setRound("FD-001", domain.RoundKindReview, 1)
		m.roundStore = &failRoundStore{failWrite: true}
		m = pump(t, m, m.handleEngineEvent(engine.Event{Kind: engine.EventExhausted, Feature: "FD-001", Stage: domain.StageReview, Committed: false}))
		if got := m.round("FD-001", domain.RoundKindReview); got != 1 {
			t.Errorf("m.round(review) after failed exhaustion clear = %d, want 1 (count not lost)", got)
		}
		if !m.notice.isErr {
			t.Error("no error notice raised on a failed exhaustion clear")
		}
	})
}

// Store failures are never silently ignored: a failed seed read aborts
// review dispatch.
func TestReviewRoundsWriteThroughFailsClosed(t *testing.T) {
	m, eng := chatWorkspace(t, verdictAgent(func(opts agent.SessionOpts) string { return "done" }))
	m = advanceTo(t, m, domain.StageReview)
	m.roundStore = &failRoundStore{failLoad: true}
	m = openAndAttach(t, m)
	if !m.notice.isErr {
		t.Error("no error notice on a failing seed read")
	}
	if s := eng.Get("FD-001"); s != nil && (s.State() == engine.StateRunning || s.State() == engine.StateQueued) {
		t.Error("review session started despite a failing seed read")
	}
}
