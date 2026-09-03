package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
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

// A research card at verify whose document fails the deterministic
// citation floor (internal/verifydoc) blocks on 'g': the notice names the
// failing checks — not a "→ done" transition notice — and the card stays
// at verify. advanceStage calls engine.Advance directly, with no agent
// session involved, so a transient engine (no configured agent) suffices.
func TestAdvanceBlockedByDocument(t *testing.T) {
	m, _ := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	ctx := context.Background()
	f := &domain.Feature{
		ID: "RS-001", Num: 1, Kind: domain.KindResearch, Title: "research card", Slug: "research-card",
		Stage: domain.StageVerify, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.ws.DraftsDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(f))
	body := "# RS-001: research card\n\n## Findings\n\n" +
		"Broken cite `internal/missing.go:1` here.\n"
	if err := os.WriteFile(draft, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	m = pump(t, m, m.Init()) // load rows
	m = press(t, m, tea.KeyPressMsg{Code: 'g', Text: "g"})
	got, _ := m.store.GetFeature(ctx, f.ID)
	if got.Stage != domain.StageVerify {
		t.Fatalf("failing document did not block the gate (stage=%s)", got.Stage)
	}
	if !strings.Contains(m.notice.text, "1 broken citation") {
		t.Errorf("notice = %q, want it to name the failing citation check", m.notice.text)
	}
	if !m.notice.isErr {
		t.Error("blocked notice should be styled as an error")
	}
}

// TestLoadRowsDerivesAutopilotDrivingFromStore: the board badge snapshot
// derives from the same event log the card thread renders — a card with an
// open took-over row loads AutopilotDriving:true, and once the matching
// handed-back row lands the same card reloads AutopilotDriving:false. The
// flag is recomputed, never persisted.
func TestLoadRowsDerivesAutopilotDrivingFromStore(t *testing.T) {
	m, _ := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "driven", Slug: "driven",
		Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}

	drivingOf := func(msg tea.Msg) *featureRow {
		rm, ok := msg.(rowsMsg)
		if !ok {
			t.Fatalf("loadRows returned %T", msg)
		}
		for i := range rm.rows {
			if rm.rows[i].F.ID == f.ID {
				return &rm.rows[i]
			}
		}
		t.Fatal("row missing from load")
		return nil
	}

	// The board's own switch binds a live file under its own pid the
	// instant it takes a card over (engine.Engine.bindLiveLog); loadRows'
	// liveness check (BG-059) needs that file to tell this genuinely open
	// period from one whose driving process is simply gone.
	w, err := livelog.Create(m.ws.LiveFile(f.ID), livelog.Record{
		Feature: string(f.ID), Stage: string(domain.StageImplement),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })

	if err := m.store.AppendAutopilot(ctx, f.ID, domain.StageImplement,
		state.AutopilotTookOver, "", domain.GateFull, "", fixedTime); err != nil {
		t.Fatal(err)
	}
	if row := drivingOf(m.loadRows()); !row.AutopilotDriving {
		t.Fatal("open period loaded AutopilotDriving=false, want true")
	}

	if err := m.store.AppendAutopilot(ctx, f.ID, domain.StageImplement,
		state.AutopilotHandedBack, "", domain.GateFull, "", fixedTime.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if row := drivingOf(m.loadRows()); row.AutopilotDriving {
		t.Fatal("card still AutopilotDriving after handback")
	}
}
