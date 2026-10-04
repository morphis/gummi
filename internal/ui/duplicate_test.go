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
		Slug:     "add-a-healthz-endpoint", Stage: domain.StageTodo, Profile: "fast",
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
	if _, err := store.Transition(ctx, src.ID, domain.StagePlan, "user"); err != nil {
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
	if dup.Profile != "fast" {
		t.Errorf("copy lost run settings: profile=%q", dup.Profile)
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
	if orig.Stage != domain.StagePlan || orig.Spend.Zero() || orig.ExternalRef == "" {
		t.Errorf("original changed by duplicate: stage=%q spend=%+v ref=%q", orig.Stage, orig.Spend, orig.ExternalRef)
	}
}

// TestDuplicateFromActionListOpensConfirm: duplicating asks before
// minting the copy, like every other card-level action with side
// effects. It has no accelerator any more — y is "yes" in the very
// confirm this action raises — so the action list is the way in.
func TestDuplicateFromActionListOpensConfirm(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := populatedShell(80, 24)
	m.Attach(store, wt, ws) // board keys are gated on an attached store
	m.runCardAction(cardAction{id: "duplicate"})
	if !m.Overlay.Contains("confirm-duplicate") {
		t.Fatal("the duplicate action did not open its confirm dialog")
	}
}

// TestDuplicateNoLongerOnY guards the collision that motivated moving
// it: y raised the duplicate confirm and was also "yes" inside it, so
// one letter meant two things a single keystroke apart.
func TestDuplicateNoLongerOnY(t *testing.T) {
	ws, store, wt := uiRepo(t)
	m := populatedShell(80, 24)
	m.Attach(store, wt, ws)
	m.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.Overlay.HasDialogs() {
		t.Fatal("y still opens a dialog on the board")
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

	if msg := m.createCard(formResult{Kind: domain.KindBug, Desc: "Crash on empty diff", Severity: domain.SeverityHigh})(); msg != nil {
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

// TestDuplicateCarriesIdentity: the copy carries the source's identity
// set — the same repo (an empty repo is a card mint refuses in a
// repos:-only workspace, so dropping it here would mint one mint itself
// would have refused), the same base, and — for a bug — the same
// severity. Its branch scheme is stamped to the current default, never
// the source's stored spelling, and a source that was adopted does not
// make the copy one. Everything deliberately not carried stays unset:
// the external ref, the adopted branch, the stack position, the goal
// membership, the spend, the session backend/model, the main-checkout
// flag, and the gate approval.
func TestDuplicateCarriesIdentity(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ctx := context.Background()
	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC) }
	m.Attach(store, wt, ws)

	// the source carries one of everything, so each negative assertion
	// below has a value it could have wrongly inherited.
	now := m.now()
	src := domain.Feature{
		ID: "BG-001", Num: 1, Kind: domain.KindBug,
		Title: "Crash on empty diff", OneLiner: "An empty diff panics the renderer.",
		Slug: "crash-on-empty-diff", Stage: domain.StageTodo, Profile: "fast",
		Budget:       domain.Budget{Envelope: 500},
		Repo:         "a",
		Base:         "feat/parent",
		Severity:     domain.SeverityHigh,
		BranchScheme: domain.BranchSchemeAdopted,
		Branch:       "somebody-elses/branch",
		StackID:      "carried-stack",
		StackPos:     1,
		GoalID:       "GL-001",
		GateApproval: domain.GateAutopilot,
		ExternalRef:  "https://github.com/o/r/issues/7",
		CreatedAt:    now, UpdatedAt: now,
	}
	if err := store.CreateFeature(ctx, &src); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSpend(ctx, src.ID, 12.5, 0, 1000, 2000); err != nil {
		t.Fatal(err)
	}

	if msg := m.duplicateFeature(src.ID)(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("duplicate failed: %s", nm.text)
		}
	}

	dup, err := store.GetFeature(ctx, "BG-002")
	if err != nil {
		t.Fatal(err)
	}
	if dup.Repo != src.Repo {
		t.Errorf("copy repo = %q, want the source's %q", dup.Repo, src.Repo)
	}
	if dup.Base != src.Base {
		t.Errorf("copy base = %q, want the source's %q", dup.Base, src.Base)
	}
	if dup.Severity != src.Severity {
		t.Errorf("copy severity = %q, want the source's %q", dup.Severity, src.Severity)
	}
	if dup.BranchScheme != domain.DefaultBranchScheme {
		t.Errorf("copy branch scheme = %q, want the current default %q, never the source's spelling", dup.BranchScheme, domain.DefaultBranchScheme)
	}
	if dup.Branch != "" {
		t.Errorf("copy inherited adopted branch %q — a copy is never adopted", dup.Branch)
	}
	if dup.ExternalRef != "" {
		t.Errorf("copy inherited external ref %q — dedupe lookups must stay unambiguous", dup.ExternalRef)
	}
	if dup.StackID != "" || dup.StackPos != 0 {
		t.Errorf("copy inherited stack position %q/%d", dup.StackID, dup.StackPos)
	}
	if dup.GoalID != "" {
		t.Errorf("copy inherited goal membership %q", dup.GoalID)
	}
	if !dup.Spend.Zero() {
		t.Errorf("copy inherited spend: %+v", dup.Spend)
	}
	if dup.SessionBackend != "" || dup.SessionModel != "" {
		t.Errorf("copy inherited session backend/model %q/%q", dup.SessionBackend, dup.SessionModel)
	}
	if dup.MainCheckout {
		t.Error("copy inherited main-checkout")
	}
	if dup.GateApproval != "" {
		t.Errorf("copy inherited gate approval %q — an unspent copy attends its gates", dup.GateApproval)
	}
}
