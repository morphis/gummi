package ui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestDuplicateFeatureFreshCopy: duplicating a mid-flight feature mints a
// new card in todo that inherits how the work is run (title, one-liner,
// skips, profile, envelope) but none of what happened (stage, spend,
// external ref) — and leaves the original untouched.
func TestDuplicateFeatureFreshCopy(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) }
	m.Attach(store, wt, ws)

	// the original: an external ref, real spend, a stage past todo — none
	// of it may leak into the copy
	now := m.now()
	src := domain.Feature{
		ID: "FD-001", Num: 1, Title: "Add a healthz endpoint",
		OneLiner: "So the load balancer can check liveness.",
		Slug:     "add-a-healthz-endpoint", Stage: domain.StageTodo,
		Skip: domain.SkipFlags{Brainstorm: true}, Profile: "fast",
		Budget:      domain.Budget{Envelope: 500},
		ExternalRef: "https://github.com/o/r/issues/42",
		CreatedAt:   now, UpdatedAt: now,
	}
	if err := store.CreateFeature(ctx, &src); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSpend(ctx, src.ID, 12.5, 0, 1000, 2000); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, src.ID, domain.StageSpec, "user"); err != nil {
		t.Fatal(err)
	}

	if msg := m.duplicateFeature(src.ID)(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("duplicate failed: %s", nm.text)
		}
	}

	dup, err := store.GetFeature(ctx, "FD-002")
	if err != nil {
		t.Fatal(err)
	}
	if dup.Title != src.Title || dup.OneLiner != src.OneLiner || dup.Slug != src.Slug {
		t.Errorf("copy identity = %q/%q/%q, want the original's", dup.Title, dup.OneLiner, dup.Slug)
	}
	if dup.Stage != domain.StageTodo {
		t.Errorf("copy stage = %q, want todo", dup.Stage)
	}
	if !dup.Skip.Brainstorm || dup.Profile != "fast" {
		t.Errorf("copy lost run settings: skip=%+v profile=%q", dup.Skip, dup.Profile)
	}
	if dup.Budget.Envelope != 500 {
		t.Errorf("copy budget = %+v, want the envelope with nothing spent", dup.Budget)
	}
	if !dup.Spend.Zero() {
		t.Errorf("copy inherited spend: %+v", dup.Spend)
	}
	if dup.ExternalRef != "" {
		t.Errorf("copy inherited external ref %q — dedupe lookups must stay unambiguous", dup.ExternalRef)
	}

	orig, err := store.GetFeature(ctx, src.ID)
	if err != nil {
		t.Fatal(err)
	}
	if orig.Stage != domain.StageSpec || orig.Spend.Zero() || orig.ExternalRef == "" {
		t.Errorf("original changed by duplicate: stage=%q spend=%+v ref=%q", orig.Stage, orig.Spend, orig.ExternalRef)
	}
}

// TestDuplicateKeyOpensConfirm: y on a selected card asks before minting
// the copy, like every other card-level action with side effects.
func TestDuplicateKeyOpensConfirm(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := populatedShell(80, 24)
	m.Attach(store, wt, ws) // board keys are gated on an attached store
	m.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !m.Overlay.Contains("confirm-duplicate") {
		t.Fatal("y did not open the duplicate confirm dialog")
	}
}

// TestDuplicateBugStaysABug: a duplicated bug keeps its kind, so the copy
// gets a BG id and re-enters the bug workflow.
func TestDuplicateBugStaysABug(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) }
	m.Attach(store, wt, ws)

	if msg := m.createBug(bugFormResult{Title: "Crash on empty diff", Severity: domain.SeverityHigh})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	if msg := m.duplicateFeature("BG-001")(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("duplicate failed: %s", nm.text)
		}
	}
	dup, err := store.GetFeature(ctx, "BG-002")
	if err != nil {
		t.Fatal(err)
	}
	if dup.Kind != domain.KindBug || dup.Stage != domain.StageTodo {
		t.Errorf("copy = kind %q stage %q, want a bug in todo", dup.Kind, dup.Stage)
	}
}
