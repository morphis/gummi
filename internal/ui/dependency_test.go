package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

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
