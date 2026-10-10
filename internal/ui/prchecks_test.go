package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/pr"
)

func runPRChecks(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m.sel = 0
	cmd := m.runCardAction(cardAction{id: "prchecks"})
	if cmd == nil {
		t.Fatalf("prchecks produced no command (notice %q)", m.notice.text)
	}
	return pump(t, m, cmd)
}

func stubPRChecks(m *Shell, checks pr.Checks) *int {
	calls := 0
	m.fetchPRChecks = func(context.Context, domain.PullRequestRef) (pr.Checks, error) {
		calls++
		return checks, nil
	}
	return &calls
}

// A verified card whose PR went red has no writer to hand the failures
// to: it goes back over its rerun edge, and the checks ride the next
// implement run's kickoff.
func TestPRChecksSendAVerifyCardBackWithTheFailures(t *testing.T) {
	m := linkFixture(t)
	if _, err := m.store.Transition(context.Background(), "FD-001", domain.StageVerify, "test"); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	stubPRChecks(m, pr.Checks{HeadSHA: strings.Repeat("a", 40), Items: []pr.Check{
		{Name: "test", Workflow: "CI", Bucket: pr.CheckFail, URL: "https://github.com/o/r/actions/runs/1/job/2", Log: "Run tests: --- FAIL: TestThing"},
		{Name: "lint", Bucket: pr.CheckPass},
	}})

	m = runPRChecks(t, m)

	f, err := m.store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Stage != domain.StageImplement {
		t.Errorf("stage = %s, want implement (notice %q)", f.Stage, m.notice.text)
	}
	note := m.bounceNotes["FD-001"]
	for _, want := range []string{"1 failing check on commit aaaaaaa", "- test (CI)", "--- FAIL: TestThing"} {
		if !strings.Contains(note, want) {
			t.Errorf("kickoff note lacks %q:\n%s", want, note)
		}
	}
	anns, err := m.store.ListDiffAnnotations(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if len(anns) != 0 {
		t.Errorf("failing checks were stored as %d diff comment(s); they hold no gate", len(anns))
	}
}

// With nothing failing there is nothing to send: the card stays where it
// is and the notice says what GitHub answered.
func TestPRChecksWithNothingFailingMoveNothing(t *testing.T) {
	m := linkFixture(t)
	stubPRChecks(m, pr.Checks{Items: []pr.Check{
		{Name: "lint", Bucket: pr.CheckPass},
		{Name: "e2e", Bucket: pr.CheckPending},
	}})

	m = runPRChecks(t, m)

	if !strings.Contains(m.notice.text, "no failing checks on o/r#42 (1 still running)") {
		t.Errorf("notice = %q", m.notice.text)
	}
	f, err := m.store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Stage != domain.StageImplement {
		t.Errorf("stage = %s, want implement", f.Stage)
	}
	if note := m.bounceNotes["FD-001"]; note != "" {
		t.Errorf("a kickoff note was left with nothing failing: %q", note)
	}
}

// At the work stage itself there is no edge to take: with no implement
// run in flight a fresh one starts, carrying the failures in its kickoff.
func TestPRChecksRerunAnIdleImplementStage(t *testing.T) {
	m := linkFixture(t)
	eng := engine.New(engine.Config{
		Agents: singleAgent(agent.NewFake("fixed")), Store: m.store, Pool: m.wt,
		Workspace: m.ws,
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)
	m = pump(t, m, m.Init())
	stubPRChecks(m, pr.Checks{Items: []pr.Check{{Name: "test", Bucket: pr.CheckFail}}})

	m = runPRChecks(t, m)

	if !strings.Contains(m.notice.text, "re-running implement with 1 failing check") {
		t.Errorf("notice = %q", m.notice.text)
	}
	settleChat(t, eng)
	if s := eng.Get("FD-001"); s == nil || s.Feature.Stage != domain.StageImplement {
		t.Fatal("the failing checks did not start an implement run")
	}
}

// The action needs a linked PR, and without a PR backend wired it says so
// rather than telling a session nothing is failing.
func TestPRChecksNeedALinkedPRAndABackend(t *testing.T) {
	m, _ := diffWorkspace(t)
	m.sel = 0
	calls := stubPRChecks(m, pr.Checks{})
	if cmd := m.runCardAction(cardAction{id: "prchecks"}); cmd != nil {
		t.Errorf("prchecks ran on an unlinked card")
	}
	if !strings.Contains(m.notice.text, "has no linked PR") || *calls != 0 {
		t.Errorf("notice = %q, fetches = %d", m.notice.text, *calls)
	}

	m = linkFixture(t)
	m.fetchPRChecks = nil
	m = runPRChecks(t, m)
	if !strings.Contains(m.notice.text, "unavailable") || !m.notice.isErr {
		t.Errorf("notice = %q", m.notice.text)
	}
}
