package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// TestBG005ParkReasonNamesTheBlocker is BG-005's reproduction.
//
// A bug card handed to autopilot at todo runs its diagnose stage; the
// writer leaves Root cause undrafted, the critique judges what was
// written and passes cleanly, and the crossing autopilot then attempts is
// refused by the undrafted-sections floor. The card parks — but the gate
// decision autopilot opened before the attempt stays open with the
// "critiqued: clean — review & approve" wording, and that open decision
// is what `gummi status --json` reports as the escalation reason. The
// reason a reader (or a driver) sees invites an approval the gate refuses.
//
// The contract asserted: whatever the card is waiting on must name the
// undrafted section, the way the headless driver's blocked record does.
func TestBG005ParkReasonNamesTheBlocker(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	// The diagnose writer never drafts Root cause; the critique judges
	// what was written and passes cleanly.
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		return "diagnosed what I could.\nVERDICT: pass"
	})
	eng := engine.New(engine.Config{
		Agents: singleAgent(ag), Store: store,
		Pool: wt, Workspace: ws, Model: "fake-model",
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)

	f := domain.Feature{
		ID: "BG-001", Num: 1, Kind: domain.KindBug,
		Title: "park reason hides the blocker",
		Slug:  "park-reason-hides-the-blocker",
		Stage: domain.StageTodo,
	}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(wt.Root(), f.ArtifactPath())
	if err := os.WriteFile(report, []byte(spec.BugTemplate(&f)), 0o600); err != nil {
		t.Fatal(err)
	}
	// Load the board row so stageOf (and the decision rows it stamps)
	// see the card the way the running TUI does.
	m = pump(t, m, m.loadRows)

	// Hand the card to autopilot from todo, exactly as the confirmed
	// `A` dialog does.
	plan := m.planAutopilot(f)
	cmd := m.startAutopilot(f, domain.GateAutopilot, plan)
	if msg := cmd(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("startAutopilot failed: %s", nm.text)
		}
		// route the notice (and its reload) the way the runtime does, so
		// the board row reflects the stage the card just entered.
		model, next := m.Update(msg)
		m = pump(t, model.(*Shell), next)
	}

	// Drain the engine loop until the card settles at the gate: the
	// writer finishes, the critique runs and passes, the crossing is
	// attempted and refused.
	drainUntil(t, m, func(m *Shell) bool {
		return len(openGateQuestions(t, ctx, store, f.ID)) > 0
	})

	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StagePlan {
		t.Fatalf("stage = %s, want plan (parked at the design gate)", got.Stage)
	}

	// The gate really is shut: Advance refuses on the undrafted section.
	res, err := eng.Advance(ctx, f.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != engine.StatusBlockedUndrafted {
		t.Fatalf("advance status = %v, want blocked-undrafted", res.Status)
	}
	if len(res.Undrafted) != 1 || res.Undrafted[0] != "Root cause" {
		t.Fatalf("undrafted = %v, want [Root cause]", res.Undrafted)
	}

	// What the card is waiting on must name the blocker, not invite an
	// approval that cannot cross.
	questions := openGateQuestions(t, ctx, store, f.ID)
	if len(questions) == 0 {
		t.Fatal("no open gate decision recorded for the parked card")
	}
	for _, q := range questions {
		if strings.Contains(q, "review & approve") {
			t.Errorf("open gate decision invites an approval the gate refuses: %q", q)
		}
		if !strings.Contains(q, "Root cause") {
			t.Errorf("open gate decision does not name the undrafted section: %q", q)
		}
	}
}

func openGateQuestions(t *testing.T, ctx context.Context, store *state.Store, id domain.FeatureID) []string {
	t.Helper()
	byCard, err := store.OpenDecisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range byCard[id] {
		out = append(out, d.Question)
	}
	return out
}

// TestBG005MisleadingDecisionSurvivesOtherBlockers pins the mechanism the
// undrafted fix (f4dc3e3) only routed around: autopilotCrossGate opens the
// gate decision in the inviting wording BEFORE attempting the crossing,
// and a crossing Advance refuses leaves that row standing. The critique
// loop pre-checks undrafted sections, so on the tip this needs a blocker
// the loop does not pre-check — here an unresolved user %% thread.
func TestBG005MisleadingDecisionSurvivesOtherBlockers(t *testing.T) {
	ctx := context.Background()
	ws, store, wt := uiRepo(t)
	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, wt, ws)

	ag := verdictAgent(func(opts agent.SessionOpts) string {
		return "diagnosed what I could.\nVERDICT: pass"
	})
	eng := engine.New(engine.Config{
		Agents: singleAgent(ag), Store: store,
		Pool: wt, Workspace: ws, Model: "fake-model",
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)

	f := domain.Feature{
		ID: "BG-002", Num: 2, Kind: domain.KindBug,
		Title: "sibling blocker", Slug: "sibling-blocker", Stage: domain.StageTodo,
	}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	// Root cause drafted (so the undrafted pre-check passes) plus one open
	// user thread (so Advance refuses on questions).
	report := filepath.Join(wt.Root(), f.ArtifactPath())
	body := strings.Replace(spec.BugTemplate(&f),
		"## Root cause\n\n%% @gummi:",
		"## Root cause\n\nThe cause is X.\n\n%% @user: which X?\n%% @gummi:", 1)
	if err := os.WriteFile(report, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)

	plan := m.planAutopilot(f)
	cmd := m.startAutopilot(f, domain.GateAutopilot, plan)
	if msg := cmd(); msg != nil {
		if nm, ok := msg.(noticeMsg); ok && nm.isErr {
			t.Fatalf("startAutopilot failed: %s", nm.text)
		}
		model, next := m.Update(msg)
		m = pump(t, model.(*Shell), next)
	}

	drainUntil(t, m, func(m *Shell) bool {
		return len(openGateQuestions(t, ctx, store, f.ID)) > 0
	})

	res, err := eng.Advance(ctx, f.ID, "user")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != engine.StatusBlockedQuestions {
		t.Fatalf("advance status = %v, want blocked-questions", res.Status)
	}
	for _, q := range openGateQuestions(t, ctx, store, f.ID) {
		if strings.Contains(q, "review & approve") {
			t.Errorf("open gate decision invites an approval the gate refuses: %q", q)
		}
		// "blocks"/"block" agrees with the count now (round 3 §5.5), so the
		// assertion asks for the claim rather than one conjugation of it.
		if !strings.Contains(q, "approval") {
			t.Errorf("open gate decision does not name the blocker: %q", q)
		}
	}
}
