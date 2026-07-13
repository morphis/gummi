package ui

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
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
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
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

	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // first review
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	// after the cap, the loop stops and escalates
	if m.reviewRounds["FD-001"] != 0 {
		t.Errorf("rounds not reset after escalation: %d", m.reviewRounds["FD-001"])
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
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
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
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // run review → auto verify
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
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}) // run plan
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
	if m.planRounds["FD-001"] != 0 {
		t.Errorf("rounds not reset after pass: %d", m.planRounds["FD-001"])
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

func TestPlanCritiqueEscalatesAfterCap(t *testing.T) {
	// a critique that never passes stops looping past the cap and hands
	// the plan to the human.
	var critiques atomic.Int32
	m := runPlan(t, planAgent(&critiques, "Still broken.\nVERDICT: changes"))

	if got := critiques.Load(); got != maxPlanRounds+1 {
		t.Errorf("critique ran %d times, want %d (initial + %d replans)", got, maxPlanRounds+1, maxPlanRounds)
	}
	if m.planRounds["FD-001"] != 0 {
		t.Errorf("rounds not reset after escalation: %d", m.planRounds["FD-001"])
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
