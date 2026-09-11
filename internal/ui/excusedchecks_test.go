package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
)

// TestVerifyPassNamesTheExcusedChecks: a check already failing when the
// branch was cut is written off at verify and does not floor the verdict,
// which is right — but the sentence reporting the pass said nothing about
// it, so a repo whose `lint` has been red for a month lost that gate on
// every card with nothing on screen admitting it.
func TestVerifyPassNamesTheExcusedChecks(t *testing.T) {
	in := nextInput{
		stage: domain.StageVerify, kind: domain.KindFeature,
		attn: attnGate, verdict: verdictPass,
	}
	clean := verifyStopped(in, "spec")
	if clean != "Verify passed — the work is ready. Decide how it leaves gummi." {
		t.Errorf("a branch born clean changed wording: %q", clean)
	}

	in.excusedChecks = []string{"lint"}
	one := verifyStopped(in, "spec")
	if !strings.Contains(one, "lint") || !strings.Contains(one, "excused") {
		t.Errorf("the excused check is not named: %q", one)
	}
	if !strings.HasPrefix(one, "Verify passed — the work is ready") {
		t.Errorf("the clause replaced the sentence instead of narrowing it: %q", one)
	}

	in.excusedChecks = []string{"lint", "vet"}
	both := verifyStopped(in, "spec")
	if !strings.Contains(both, "lint and vet") {
		t.Errorf("two excused checks are not both named: %q", both)
	}
}

// TestExcusedChecksLoadedPerCard: the names come off the card's stored
// baseline through the same lazy per-card load the event log uses, so the
// render path never reads the store.
func TestExcusedChecksLoadedPerCard(t *testing.T) {
	m, _ := newWorkspace(t)
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "excused", Slug: "excused",
		Stage: domain.StageVerify, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetCheckBaseline(ctx, f.ID, []state.CheckResult{
		{Name: "build", Cmd: "go build ./...", OK: true},
		{Name: "lint", Cmd: "golangci-lint run", OK: false},
	}); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.Init())
	m = pump(t, m, m.loadExcusedChecks(f.ID))

	got := m.excusedChecks[f.ID]
	if len(got) != 1 || got[0] != "lint" {
		t.Fatalf("excused checks = %v, want just the one that was already failing", got)
	}
	in := m.nextInputFor(m.rows[0])
	if len(in.excusedChecks) != 1 || in.excusedChecks[0] != "lint" {
		t.Errorf("nextInput did not carry the excused names: %v", in.excusedChecks)
	}
}

// TestOpenCardLoadsTheExcusedChecks: the load is fired where the event
// log's is — opening a card page — so the page has the names by the time
// it reports the pass.
func TestOpenCardLoadsTheExcusedChecks(t *testing.T) {
	m, _ := newWorkspace(t)
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "excused", Slug: "excused",
		Stage: domain.StageVerify, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetCheckBaseline(ctx, f.ID, []state.CheckResult{
		{Name: "lint", Cmd: "golangci-lint run", OK: false},
	}); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.Init())
	m = pump(t, m, m.openCard())
	if got := m.excusedChecks[f.ID]; len(got) != 1 {
		t.Errorf("opening the card page did not load the excused checks: %v", got)
	}
}
