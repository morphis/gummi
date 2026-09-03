package ui

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// waitCardLive polls until id's session reports live — an attach's
// kickoff turn settles asynchronously, off the goroutine that started it.
func waitCardLive(t *testing.T, eng *engine.Engine, id domain.FeatureID) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if s := eng.Get(id); s.Live() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("%s never went live", id)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// cardProfilePickerFixture declares two profiles whose implementer and
// reviewer roles diverge from each other — so a picker that mislabeled a
// choice with the board/architect resolution (the pitfall BoardProfile's
// own doc comment warns about) is caught rather than accidentally
// agreeing.
func cardProfilePickerFixture() config.Profiles {
	return config.Profiles{
		Default: "alpha",
		Profiles: map[string]config.Profile{
			"alpha": {
				"implementer": {Backend: "one", Model: "alpha-impl"},
				"reviewer":    {Backend: "one", Model: "alpha-review"},
			},
			"beta": {
				"implementer": {Backend: "two", Model: "beta-impl"},
				"reviewer":    {Backend: "two", Model: "beta-review"},
			},
		},
	}
}

// selectSyntheticCard puts f in front of m.selected() without a store or
// board reload — openCardProfilePicker only reads r.F.Stage/r.F.Profile,
// so this is enough for the picker tests below.
func selectSyntheticCard(m *Shell, f domain.Feature) {
	m.rows = []featureRow{{F: f}}
	m.sel = 0
}

func valueRowIDs(m *Shell) []string {
	menu, ok := m.Overlay.Top().(*commandMenu)
	if !ok {
		return nil
	}
	ids := make([]string, len(menu.cmds))
	for i, c := range menu.cmds {
		ids[i] = c.id
	}
	sort.Strings(ids)
	return ids
}

// TestOpenCardProfilePickerListsProfiles: the value tier offers every
// declared profile, as a commandMenu overlay (not the board's inline
// popup — this thread has none).
func TestOpenCardProfilePickerListsProfiles(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.engine = testProfileEngine(t, cardProfilePickerFixture())
	selectSyntheticCard(m, domain.Feature{ID: "FD-001", Stage: domain.StageImplement, Profile: "alpha"})

	m.openCardProfilePicker()

	if got := strings.Join(valueRowIDs(m), ","); got != "profile-value:alpha,profile-value:beta" {
		t.Fatalf("value tier ids = %q, want both declared profiles", got)
	}
}

// TestOpenCardProfilePickerMarksCardsCurrentProfile: "current" tracks the
// selected card's own Feature.Profile, never the board's own profile.
func TestOpenCardProfilePickerMarksCardsCurrentProfile(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.engine = testProfileEngine(t, cardProfilePickerFixture())
	selectSyntheticCard(m, domain.Feature{ID: "FD-001", Stage: domain.StageImplement, Profile: "beta"})

	m.openCardProfilePicker()

	menu, ok := m.Overlay.Top().(*commandMenu)
	if !ok {
		t.Fatalf("openCardProfilePicker did not open a commandMenu, got %T", m.Overlay.Top())
	}
	labels := map[string]string{}
	for _, c := range menu.cmds {
		labels[c.id] = c.label
	}
	if !strings.Contains(labels["profile-value:beta"], "current") {
		t.Errorf("beta label = %q, want it marked current (the card's own profile)", labels["profile-value:beta"])
	}
	if strings.Contains(labels["profile-value:alpha"], "current") {
		t.Errorf("alpha label = %q, wrongly marked current", labels["profile-value:alpha"])
	}
}

// TestOpenCardProfilePickerLabelsPerCardRole: an implement-stage card
// labels its choices with the implementer backend/model, not the
// board/architect one BoardProfiles would report for the same profile —
// the exact divergence CardProfiles exists to fix.
func TestOpenCardProfilePickerLabelsPerCardRole(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.engine = testProfileEngine(t, cardProfilePickerFixture())
	selectSyntheticCard(m, domain.Feature{ID: "FD-001", Stage: domain.StageImplement, Profile: "alpha"})

	m.openCardProfilePicker()

	menu, ok := m.Overlay.Top().(*commandMenu)
	if !ok {
		t.Fatalf("openCardProfilePicker did not open a commandMenu, got %T", m.Overlay.Top())
	}
	labels := map[string]string{}
	for _, c := range menu.cmds {
		labels[c.id] = c.label
	}
	if !strings.Contains(labels["profile-value:alpha"], "alpha-impl") {
		t.Errorf("alpha label = %q, want the implementer model alpha-impl", labels["profile-value:alpha"])
	}
	if !strings.Contains(labels["profile-value:beta"], "beta-impl") {
		t.Errorf("beta label = %q, want the implementer model beta-impl", labels["profile-value:beta"])
	}
}

// TestRunCommandProfileValueConfirms: choosing a value-tier row (the
// "profile-value:" id the picker built) reaches confirmCardProfileChange
// for the selected card — mirroring runBoardCompletion's own
// "agent-cli:"-prefix split, no new parsing convention.
func TestRunCommandProfileValueConfirms(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ag := agent.NewFake("ok")
	eng := engine.New(engine.Config{
		Agents: singleAgent(ag), Store: store, Pool: wt, Workspace: ws,
		Model: "fallback", Profiles: cardProfilePickerFixture(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx := context.Background()
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "x", Slug: "x", Stage: domain.StageImplement, Profile: "alpha"}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.engine = eng
	selectSyntheticCard(m, f)

	// no live session for FD-001: this must apply at once rather than confirm.
	cmd := m.runCommand("profile-value:beta")
	if cmd == nil {
		t.Fatal("runCommand(profile-value:beta) returned no command")
	}
	cmd()

	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "beta" {
		t.Errorf("persisted profile = %q, want beta", got.Profile)
	}
}

// TestConfirmCardProfileChangeAppliesWhenIdle: nothing live to lose, so
// the change lands without asking — confirmBoardReopen's own idle
// gating, reused for the card-scoped picker.
func TestConfirmCardProfileChangeAppliesWhenIdle(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ag := agent.NewFake("ok")
	eng := engine.New(engine.Config{
		Agents: singleAgent(ag), Store: store, Pool: wt, Workspace: ws,
		Model: "fallback", Profiles: cardProfilePickerFixture(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx := context.Background()
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "x", Slug: "x", Stage: domain.StageImplement, Profile: "alpha"}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.engine = eng

	cmd := m.confirmCardProfileChange(f.ID, "beta")
	if m.Overlay.HasDialogs() {
		t.Fatal("an idle card should apply at once, not confirm first")
	}
	if cmd == nil {
		t.Fatal("confirmCardProfileChange on an idle card returned no command")
	}
	cmd()

	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "beta" {
		t.Errorf("persisted profile = %q, want beta", got.Profile)
	}
}

// TestConfirmCardProfileChangeAsksWhenLive: a live session has an
// in-flight turn to lose, so the switch confirms first rather than
// applying silently.
func TestConfirmCardProfileChangeAsksWhenLive(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ag := agent.NewFake("ok")
	eng := engine.New(engine.Config{
		Agents: singleAgent(ag), Store: store, Pool: wt, Workspace: ws,
		Model: "fallback", Profiles: cardProfilePickerFixture(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx := context.Background()
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "x", Slug: "x", Stage: domain.StageBrainstorm, Profile: "alpha"}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	waitCardLive(t, eng, f.ID)

	m := NewShell(theme.GummiDark(), "v0-test")
	m.engine = eng

	if cmd := m.confirmCardProfileChange(f.ID, "beta"); cmd != nil {
		t.Error("a live card's confirm should return nil until the dialog answers")
	}
	d, ok := m.Overlay.Top().(*confirmDialog)
	if !ok {
		t.Fatalf("confirmCardProfileChange on a live card did not confirm first, got %T", m.Overlay.Top())
	}

	applyCmd := d.onConfirm()
	if applyCmd == nil {
		t.Fatal("confirming returned no command")
	}
	applyCmd()

	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "beta" {
		t.Errorf("persisted profile = %q, want beta", got.Profile)
	}
}

// TestApplyCardProfileChangeUpdatesStoreAndNoticesOnError: success runs
// the engine mutation silently (the engine's own events carry the
// visible change through); an unknown profile name surfaces as an error
// notice instead.
func TestApplyCardProfileChangeUpdatesStoreAndNoticesOnError(t *testing.T) {
	ws, store, wt := uiRepo(t)
	ag := agent.NewFake("ok")
	eng := engine.New(engine.Config{
		Agents: singleAgent(ag), Store: store, Pool: wt, Workspace: ws,
		Model: "fallback", Profiles: cardProfilePickerFixture(),
	})
	t.Cleanup(func() { eng.Close() })

	ctx := context.Background()
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "x", Slug: "x", Stage: domain.StageImplement, Profile: "alpha"}
	if err := store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.engine = eng

	if msg := m.applyCardProfileChange(f.ID, "beta")(); msg != nil {
		t.Errorf("success returned %#v, want nil", msg)
	}
	got, err := store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Profile != "beta" {
		t.Errorf("persisted profile = %q, want beta", got.Profile)
	}

	msg := m.applyCardProfileChange(f.ID, "does-not-exist")()
	nm, ok := msg.(noticeMsg)
	if !ok || !nm.isErr {
		t.Fatalf("unknown-profile change = %#v, want an error notice", msg)
	}
}
