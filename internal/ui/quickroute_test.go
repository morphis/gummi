package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestFeatureFormQuickToggle: q on the options row is a route preset —
// one keystroke in (both skips + the marker), one keystroke back out,
// and an individual b/p toggle demotes the route to plain skips.
func TestFeatureFormQuickToggle(t *testing.T) {
	form := newFeatureForm(nil, nil, 0, func(formResult) tea.Cmd { return nil })
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}) // focus the envelope field
	form.HandleKey(tea.KeyPressMsg{Code: tea.KeyTab}) // focus the options row

	form.HandleKey(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if form.skip != domain.QuickRoute() {
		t.Fatalf("q set %+v, want the quick route", form.skip)
	}
	if !strings.Contains(form.skipLabel(), "quick") {
		t.Errorf("label = %q, want it to name the quick route", form.skipLabel())
	}

	// p while quick: back to an explicit route, no orphaned marker
	form.HandleKey(tea.KeyPressMsg{Code: 'p', Text: "p"})
	if form.skip.Quick || form.skip.Plan || !form.skip.Brainstorm {
		t.Fatalf("p under quick = %+v, want plan+quick dropped, brainstorm kept", form.skip)
	}

	// q toggles the whole preset back off
	form.HandleKey(tea.KeyPressMsg{Code: 'q', Text: "q"})
	form.HandleKey(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if form.skip != (domain.SkipFlags{}) {
		t.Fatalf("double q = %+v, want the full workflow", form.skip)
	}
}

// TestRouteViaPlan: P restores the Plan stage on a quick card still in
// the design phase — and refuses once the plan stage is behind it, on a
// card already routing through plan, and on bugs.
func TestRouteViaPlan(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) }
	m.Attach(store, wt, ws)

	if msg := m.createFeature(formResult{Desc: "Add a healthz endpoint", Skip: domain.QuickRoute()})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	if _, err := store.Transition(ctx, "FD-001", domain.StageSpec, "user"); err != nil {
		t.Fatal(err)
	}

	if nm := m.routeViaPlan("FD-001")().(noticeMsg); nm.isErr {
		t.Fatalf("escalation refused: %s", nm.text)
	}
	f, err := store.GetFeature(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if f.Skip.Plan || f.Skip.Quick || !f.Skip.Brainstorm {
		t.Fatalf("escalated flags = %+v, want plan+quick cleared, brainstorm kept", f.Skip)
	}

	// second press: nothing left to restore
	if nm := m.routeViaPlan("FD-001")().(noticeMsg); !nm.isErr {
		t.Error("P on a card already routing through plan should refuse")
	}

	// past the design phase the plan stage is behind the card
	if msg := m.createFeature(formResult{Desc: "Another one", Skip: domain.QuickRoute()})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create failed: %s", nm.text)
		}
	}
	for _, st := range []domain.Stage{domain.StageSpec, domain.StageImplement} {
		if _, err := store.Transition(ctx, "FD-002", st, "user"); err != nil {
			t.Fatal(err)
		}
	}
	if nm := m.routeViaPlan("FD-002")().(noticeMsg); !nm.isErr {
		t.Error("P past the design phase should refuse")
	}

	// bugs have no plan stage
	if msg := m.createBug(bugFormResult{Title: "Crash on empty diff", Severity: domain.SeverityLow})(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("create bug failed: %s", nm.text)
		}
	}
	if nm := m.routeViaPlan("BG-003")().(noticeMsg); !nm.isErr {
		t.Error("P on a bug should refuse")
	}
}

// TestNextActionsQuickSpec: a quick card at Spec keeps the enter/g key
// sequence but the guidance names the route — the chat drafts in one
// pass, and the gate mentions P as the way out of quick.
func TestNextActionsQuickSpec(t *testing.T) {
	acts := nextActions(nextInput{stage: domain.StageSpec, kind: domain.KindFeature, quick: true})
	if got := keysOf(acts); got != "enter g" {
		t.Fatalf("quick spec keys = %q, want \"enter g\"", got)
	}
	if !strings.Contains(acts[0].why, "one pass") {
		t.Errorf("chat guidance = %q, want it to name the one-pass draft", acts[0].why)
	}
	if !strings.Contains(acts[1].why, "P") {
		t.Errorf("gate guidance = %q, want it to mention the P escalation", acts[1].why)
	}
}
