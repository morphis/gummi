package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/verify"
	"github.com/morphis/gummi/internal/webapi"
)

// The "waits on" row answers nothing, and says so: what the TUI puts in
// its status bar reaches a page as a toast about the card, because the
// page's own reading of the answer ("Answered") would claim the card
// moved.
func TestWaitOnDepsReachesThePageAsANotice(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{{F: domain.Feature{ID: "FD-007", Title: "a", Stage: domain.StagePlan}}}
	m.sel = 0
	log := newChangeLog()
	m.SetChangeHook(log.add)

	in := nextInput{stage: domain.StagePlan, depBlockers: []domain.FeatureID{"FD-006"}}
	row := waitOnDeps(in)
	if !strings.Contains(row.why, "until it does;") {
		t.Errorf("one dependency reads %q, want \"until it does\"", row.why)
	}
	if two := waitOnDeps(nextInput{stage: domain.StagePlan, depBlockers: []domain.FeatureID{"FD-005", "FD-006"}}); !strings.Contains(two.why, "until they do;") {
		t.Errorf("two dependencies read %q, want \"until they do\"", two.why)
	}

	cmd := m.runCardAction(cardAction{id: row.id, label: row.label, why: row.why})
	if cmd == nil {
		t.Fatal("the wait row produced no message for the page to hear")
	}
	m.Update(cmd())
	log.waitFor(t, "toast", func(c webapi.Change) bool {
		return c.Kind == webapi.ChangeToast && c.ID == "FD-007" && strings.Contains(c.Text, "waits on FD-006") && !c.Err
	})
	if !strings.Contains(m.notice.text, "waits on FD-006") {
		t.Errorf("the status bar says %q, want the wait", m.notice.text)
	}
}

// A verify run from the card menu reports its result to a page — a toast
// about the card, and a card change so the spec's checks are read again —
// and a failing run is passed on rather than read as the verify refused.
func TestMenuVerifyResultReachesThePage(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{{F: domain.Feature{ID: "FD-001", Title: "a", Stage: domain.StageVerify}}}
	log := newChangeLog()
	m.SetChangeHook(log.add)

	m.Update(verifyResultMsg{feature: "FD-001", stage: domain.StageVerify, results: []verify.Result{
		{Name: "build", Cmd: "go build ./...", OK: true},
		{Name: "test", Cmd: "go test ./...", OK: false},
	}})
	log.waitFor(t, "toast", func(c webapi.Change) bool {
		return c.Kind == webapi.ChangeToast && c.ID == "FD-001" && c.Text == "FD-001 verify: 1/2 passed" && c.Err
	})
	log.waitFor(t, "card", func(c webapi.Change) bool { return c.Kind == webapi.ChangeCard && c.ID == "FD-001" })
	if !m.notice.aside {
		t.Error("a finished verify run is an aside, not a refusal of the act that ran it")
	}
	if got := m.checksFor(m.rows[0].F); len(got) != 2 || m.checks["FD-001"].at.IsZero() {
		t.Errorf("checks = %+v, want both results stamped with when they ran", m.checks["FD-001"])
	}
}

// The spec's checks table shows the menu's verify run when it is newer
// than the log's verify rows: it writes nothing to the log, and "not run
// yet" under a run that just finished is the page contradicting itself.
func TestSpecChecksReadTheMenuVerifyRun(t *testing.T) {
	store, err := state.OpenStore(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "a", Slug: "a", Stage: domain.StageVerify}
	if err := store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	d := &WebDocs{f: f, store: store, manual: stagedChecks{
		stage: domain.StageVerify, at: at,
		results: []verify.Result{{Name: "build", OK: true}, {Name: "test", OK: false}},
	}}
	got := d.lastChecks(context.Background())
	if o, ok := got["build"]; !ok || !o.OK || !o.At.Equal(at) {
		t.Errorf("build = %+v (%v), want the menu run's pass", o, ok)
	}
	if o, ok := got["test"]; !ok || o.OK {
		t.Errorf("test = %+v (%v), want the menu run's failure", o, ok)
	}
}
