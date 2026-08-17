package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// TestLoadRowsDerivesBlockedFromStore: the board badge snapshot derives
// from the live dependency store at load — a card at Plan with an unmet dep
// loads DepBlocked:true, and once the dep reaches Done the same card
// reloads DepBlocked:false. The flag is recomputed, never persisted.
func TestLoadRowsDerivesBlockedFromStore(t *testing.T) {
	m, _ := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "dependent", Slug: "dependent",
		Stage: domain.StagePlan, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	dep := &domain.Feature{
		ID: "FD-002", Num: 2, Title: "dep", Slug: "dep",
		Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, dep); err != nil {
		t.Fatal(err)
	}
	if err := m.store.AddDependency(ctx, f.ID, dep.ID); err != nil {
		t.Fatal(err)
	}

	blockedOf := func(msg tea.Msg) *featureRow {
		rm, ok := msg.(rowsMsg)
		if !ok {
			t.Fatalf("loadRows returned %T", msg)
		}
		for i := range rm.rows {
			if rm.rows[i].F.ID == f.ID {
				return &rm.rows[i]
			}
		}
		t.Fatal("dependent row missing from load")
		return nil
	}

	if row := blockedOf(m.loadRows()); !row.DepBlocked {
		t.Fatal("blocked card loaded DepBlocked=false, want true")
	}

	// land the dep → reloading yields DepBlocked:false
	for _, st := range []domain.Stage{domain.StageReview, domain.StageVerify, domain.StageDone} {
		if _, err := m.store.Transition(ctx, dep.ID, st, "test"); err != nil {
			t.Fatalf("transitioning dep to %s: %v", st, err)
		}
	}
	if row := blockedOf(m.loadRows()); row.DepBlocked {
		t.Fatal("card still DepBlocked after dep landed")
	}
}

// A feature parked at its coding gate with an unmet dependency blocks on
// 'g': the notice names the outstanding dep and the stage stays put.
func TestAdvanceBlockedByDependency(t *testing.T) {
	m, _ := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode",
		Stage: domain.StagePlan, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err := m.wt.Create(ctx, f); err != nil {
		t.Fatal(err)
	}
	dep := &domain.Feature{
		ID: "FD-002", Num: 2, Title: "Dep", Slug: "dep",
		Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, dep); err != nil {
		t.Fatal(err)
	}
	if err := m.store.AddDependency(ctx, f.ID, dep.ID); err != nil {
		t.Fatal(err)
	}

	m = pump(t, m, m.Init()) // load rows
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	got, _ := m.store.GetFeature(ctx, f.ID)
	if got.Stage != domain.StagePlan {
		t.Fatalf("unmet dependency did not block the gate (stage=%s)", got.Stage)
	}
	if !strings.Contains(m.notice.text, "FD-002@implement") {
		t.Errorf("notice = %q, want it to name the unmet dependency", m.notice.text)
	}
}
