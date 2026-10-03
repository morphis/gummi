package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// namedBackendAgent is a fake under a real backend's name, so a session
// can name it the way a person names claude in the picker.
type namedBackendAgent struct {
	*agent.Fake
	name string
}

func (n namedBackendAgent) Name() string { return n.name }

// sessionPickerBoard is a board whose claude agent stages its own model
// catalog, whose profiles run one claude model, and whose selected card
// is a freeform session already running on that pair. The opencode probe
// is pointed at a binary no test ships, so no test reaches the real CLI.
func sessionPickerBoard(t *testing.T) (*Shell, featureRow) {
	t.Helper()
	t.Setenv("GUMMI_OPENCODE_BIN", filepath.Join(t.TempDir(), "no-opencode-here"))
	// the agent tier's availability must not depend on what this host
	// happens to have on PATH: only the agent the engine holds is in.
	oldInstalled := agentInstalled
	agentInstalled = func(name string) bool { return name == "claude" }
	t.Cleanup(func() { agentInstalled = oldInstalled })
	named := namedBackendAgent{Fake: agent.NewFake("ok"), name: "claude"}
	named.Models = []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-4-5"}
	profiles := config.Profiles{Default: "alpha", Profiles: map[string]config.Profile{
		"alpha": {"implementer": {Backend: "claude", Model: "claude-sonnet-5-5"}},
	}}
	m, _ := agentWorkspaceProfiles(t, named, profiles)
	r := freeformRow(9, "drop the leaked pty fd", true)
	r.F.SessionBackend, r.F.SessionModel = "claude", "claude-sonnet-5-5"
	if err := m.store.CreateFeature(context.Background(), &r.F); err != nil {
		t.Fatal(err)
	}
	m.rows = []featureRow{r}
	m.sel = 0
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return model.(*Shell), r
}

// TestAFreeformCardOffersTheModelSwitch: the row exists exactly for an
// open session on a board with an agent, never for a card in the
// workflow and never on a board with no engine — the same conditions the
// web face's picker lives under.
func TestAFreeformCardOffersTheModelSwitch(t *testing.T) {
	m, r := sessionPickerBoard(t)
	acts := cardActionsFor(m.nextInputFor(r), r)
	if !containsAction(acts, "model") {
		t.Errorf("the open session's menu has no model row: %+v", acts)
	}

	// a card in the workflow never gets one
	detached := freeformShell(t, 120, 34, true)
	if containsAction(cardActionsFor(detached.nextInputFor(detached.rows[1]), detached.rows[1]), "model") {
		t.Errorf("a freeform card on an engine-less board offered the model switch")
	}
	work := row(42, "dark mode", domain.StageImplement, "thrifty", true)
	if containsAction(cardActionsFor(m.nextInputFor(work), work), "model") {
		t.Errorf("a card in the workflow offered the model switch")
	}

	// and the profile row is withheld from a session at the Shell layer
	// too — the model row replaces it (DESIGN §19.8) — while a card in
	// the workflow keeps it: the two faces' inventories agree either way
	l := m.cardActions()
	if containsAction(l.actions, "profile") {
		t.Errorf("a session's inventory still offers the profile switch: %+v", l.actions)
	}
	if !containsAction(l.actions, "model") {
		t.Errorf("a session's inventory has no model row: %+v", l.actions)
	}
	m.cardOpen = true
	for _, c := range m.globalCommands() {
		if c.id == "profile" {
			t.Errorf("a session's command menu still offers the card's profile row")
		}
	}
	m.rows = append(m.rows, work)
	m.sel = len(m.rows) - 1
	if !containsAction(m.cardActions().actions, "profile") {
		t.Errorf("a card in the workflow lost its profile row: %+v", m.cardActions().actions)
	}
	if containsAction(m.cardActions().actions, "model") {
		t.Errorf("a card in the workflow grew a model row")
	}
}

// TestTheSessionModelPickerOffersTheAgentsCatalog: the agent tier lists
// every session backend with the current one marked, and the model tier
// offers what the agent itself provides merged with the workspace's
// profile ids, the running pair marked.
func TestTheSessionModelPickerOffersTheAgentsCatalog(t *testing.T) {
	m, _ := sessionPickerBoard(t)

	m.openCardModelPicker()
	menu, ok := m.Overlay.Top().(*commandMenu)
	if !ok {
		t.Fatalf("the model action did not open the agent tier: %T", m.Overlay.Top())
	}
	var backends []string
	for _, c := range menu.visible() {
		backends = append(backends, menu.cmds[c].id)
	}
	want := make([]string, 0, len(engine.SessionBackends))
	for _, name := range engine.SessionBackends {
		want = append(want, "session-backend:"+name)
	}
	if !equalStrings(backends, want) {
		t.Errorf("the agent tier offered %v, want %v", backends, want)
	}
	for _, c := range menu.cmds {
		if c.id == "session-backend:claude" && !strings.Contains(c.label, "current") {
			t.Errorf("the running agent is not marked: %q", c.label)
		}
		if c.id == "session-backend:pi" && c.available {
			t.Errorf("pi, absent from this host, is offered as available")
		}
	}

	// picking the running agent probes it and opens the model tier
	m = pump(t, m, menu.onRun("session-backend:claude"))
	tier2, ok := m.Overlay.Top().(*commandMenu)
	if !ok {
		t.Fatalf("the probe did not open the model tier: %T", m.Overlay.Top())
	}
	var ids []string
	for _, c := range tier2.visible() {
		ids = append(ids, tier2.cmds[c].id)
	}
	wantModels := []string{
		"session-model:", // claude takes no model id: the default is offered
		"session-model:claude-haiku-4-5",
		"session-model:claude-opus-5-5",
		"session-model:claude-sonnet-5-5",
	}
	if !equalStrings(ids, wantModels) {
		t.Errorf("the model tier offered %v, want %v", ids, wantModels)
	}
	for _, c := range tier2.cmds {
		if c.id == "session-model:claude-sonnet-5-5" && !strings.Contains(c.label, "current") {
			t.Errorf("the running model is not marked: %q", c.label)
		}
		if c.id == "session-model:claude-opus-5-5" && strings.Contains(c.label, "current") {
			t.Errorf("a model the session does not run is marked current")
		}
	}
}

// TestTheSessionPickerOpensFromBothMenuPaths: the card menu's invoke
// paths — the space menu's "model" row and the action list's — reach the
// same agent tier, and on a board with no engine the dispatch says so
// rather than opening a picker nothing can answer.
func TestTheSessionPickerOpensFromBothMenuPaths(t *testing.T) {
	m, r := sessionPickerBoard(t)

	m.runCommand("model")
	if _, ok := m.Overlay.Top().(*commandMenu); !ok {
		t.Fatalf("the space menu's model row did not open the agent tier: %T", m.Overlay.Top())
	}
	m.Overlay.Pop()

	m.runCardAction(cardAction{id: "model", label: "model"})
	if _, ok := m.Overlay.Top().(*commandMenu); !ok {
		t.Fatalf("the action list's model row did not open the agent tier: %T", m.Overlay.Top())
	}

	// a board with no engine has no agent to start: the row is withheld
	// from the list, and a dispatch that reaches it anyway says so
	detached := freeformShell(t, 120, 34, true)
	detached.sel = 1
	detached.runCardAction(cardAction{id: "model", label: "model"})
	if detached.Overlay.Len() != 0 {
		t.Errorf("an engine-less board opened a picker (%d open)", detached.Overlay.Len())
	}
	if !detached.notice.isErr || !strings.Contains(detached.notice.text, "no agent") {
		t.Errorf("the engine-less dispatch is silent: %q", detached.notice.text)
	}
	_ = r
}

// TestTheSessionModelPickerRefusesAnAbsentAgent: a backend this host
// cannot start is visible but not offered — enter says why and leaves
// the menu up, the same visible-but-not-offered rule every other command
// row follows.
func TestTheSessionModelPickerRefusesAnAbsentAgent(t *testing.T) {
	m, _ := sessionPickerBoard(t)

	m.openCardModelPicker()
	menu := m.Overlay.Top().(*commandMenu)
	for i, c := range menu.cmds {
		if c.id != "session-backend:pi" {
			continue
		}
		menu.setCursor(i)
	}
	depth := m.Overlay.Len()
	done, _ := menu.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if done || m.Overlay.Len() != depth {
		t.Errorf("the absent agent's row was not refused in place (done=%v, %d open)", done, m.Overlay.Len())
	}
	if !strings.Contains(menu.hint, "not available") {
		t.Errorf("the refusal says nothing: %q", menu.hint)
	}
}

// TestTheModelWordDegradesToTheMenuOnASession: "/model" typed on a
// freeform card's composer is a setting, not an answer — it opens the
// menu pre-filtered to the card's own model row, one enter from the
// picker. The same word on the board's composer is the agent tab's own
// switch; the pre-filter cannot mix them up, because the card's row only
// exists on a card page.
func TestTheModelWordDegradesToTheMenuOnASession(t *testing.T) {
	m, r := sessionPickerBoard(t)
	m.cardOpen = true

	if m.verbOnScreen(r, "model") {
		t.Fatal("the model switch is in the answer set — it is a card setting, not an answer")
	}
	if !m.verbDegrades(r, "model") {
		t.Fatal("/model does not degrade to the menu")
	}
	m.threadInput.SetValue("/model")
	m.submitThreadInput(r)
	cm, ok := m.Overlay.Top().(*commandMenu)
	if !ok {
		t.Fatalf("/model did not open the command menu: %T", m.Overlay.Top())
	}
	if cm.filter.Value() != "model" {
		t.Fatalf("menu filter = %q, want the verb", cm.filter.Value())
	}
	// the card's own row answers to the word, and choosing it opens the
	// agent tier — the same two-tier picker the action list's row opens
	pos := -1
	for i, idx := range cm.visible() {
		if cm.cmds[idx].id == "model" {
			pos = i
		}
	}
	if pos < 0 {
		t.Fatalf("the menu has no model row for the card")
	}
	cm.setCursor(pos)
	_, cmd := cm.HandleKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = pump(t, m, cmd)
	if _, ok := m.Overlay.Top().(*commandMenu); !ok {
		t.Fatalf("choosing the card's model row did not open the agent tier: %T", m.Overlay.Top())
	}
}

// TestTheSessionModelPickerSwitches: a pick stores the pair on the card —
// the engine's own refusal (mid-turn) is the only thing left between the
// pick and the next turn running on it.
func TestTheSessionModelPickerSwitches(t *testing.T) {
	m, r := sessionPickerBoard(t)

	m.openCardModelPicker()
	menu := m.Overlay.Top().(*commandMenu)
	m = pump(t, m, menu.onRun("session-backend:claude"))
	tier2 := m.Overlay.Top().(*commandMenu)
	m = pump(t, m, tier2.onRun("session-model:claude-haiku-4-5"))
	if _, model := m.sessionModelOf(r.F); model != "claude-haiku-4-5" {
		t.Errorf("the session runs on %q, want the pick", model)
	}
}

// TestTheSessionModelPickerTakesATypedId: an id nobody offered is still a
// model — the filter's own row offers it — and one the backend would
// refuse is refused in the backend's own words, in the notice where the
// picker was.
func TestTheSessionModelPickerTakesATypedId(t *testing.T) {
	m, r := sessionPickerBoard(t)

	// openModelTier re-opens both tiers: a pick (including a refused
	// one) closes the picker, the way the web face's popover closes
	// under its toast
	openTier2 := func(m *Shell) *commandMenu {
		m.openCardModelPicker()
		menu := m.Overlay.Top().(*commandMenu)
		m = pump(t, m, menu.onRun("session-backend:claude"))
		return m.Overlay.Top().(*commandMenu)
	}

	tier2 := openTier2(m)
	m = typeString(t, m, "gpt-5-1")
	rows := tier2.visible()
	if len(rows) != 1 || !strings.HasPrefix(tier2.cmds[rows[0]].id, "session-model-typed:") {
		t.Fatalf("the typed id is not the one row offered: %+v", tier2.cmds)
	}
	m = pump(t, m, tier2.onRun(tier2.cmds[rows[0]].id))
	if !m.notice.isErr || !strings.Contains(m.notice.text, "only runs Anthropic models") {
		t.Errorf("the refusal is not the CLI's own words: %q (err %v)", m.notice.text, m.notice.isErr)
	}
	if _, model := m.sessionModelOf(r.F); model != "claude-sonnet-5-5" {
		t.Errorf("a refused pick switched the session to %q", model)
	}

	// an id the CLI would take goes through
	tier2 = openTier2(m)
	m = typeString(t, m, "claude-fable-9")
	rows = tier2.visible()
	if len(rows) != 1 {
		t.Fatalf("the second typed id is not the one row offered")
	}
	m = pump(t, m, tier2.onRun(tier2.cmds[rows[0]].id))
	if _, model := m.sessionModelOf(r.F); model != "claude-fable-9" {
		t.Errorf("the session runs on %q, want the typed id", model)
	}
}

// TestTheSessionModelTierOfAnAgentThatNeedsAModel: an agent that refuses
// to start without a model id offers no default row, and — this one
// staged without a catalog and no profile naming it — nothing but the
// typed id: its tier is one row, and it is the filter's own. The pick
// rides the same install check the listed ids do, so an agent this host
// cannot start is refused before anything is stored.
func TestTheSessionModelTierOfAnAgentThatNeedsAModel(t *testing.T) {
	m, r := sessionPickerBoard(t)

	m.openCardModelPicker()
	menu := m.Overlay.Top().(*commandMenu)
	m = pump(t, m, menu.onRun("session-backend:opencode"))
	tier2 := m.Overlay.Top().(*commandMenu)
	if len(tier2.cmds) != 0 {
		t.Fatalf("opencode's tier offers %v, want nothing but the typed row", tier2.cmds)
	}
	m = typeString(t, m, "anthropic/claude-sonnet-5-5")
	rows := tier2.visible()
	if len(rows) != 1 || !strings.HasPrefix(tier2.cmds[rows[0]].id, "session-model-typed:") {
		t.Fatalf("the typed id is not the one row offered: %+v", tier2.cmds)
	}
	m = pump(t, m, tier2.onRun(tier2.cmds[rows[0]].id))
	// the fixture's stub keeps every agent but claude off this host, so
	// the pick is refused before anything is stored — the install check
	// rides the typed path like it rides the listed ones
	if !m.notice.isErr || !strings.Contains(m.notice.text, "not installed") {
		t.Errorf("a pick on an absent agent is not refused: %q", m.notice.text)
	}
	if b, model := m.sessionModelOf(r.F); b != "claude" || model != "claude-sonnet-5-5" {
		t.Errorf("a refused pick switched the session to %s/%q", b, model)
	}
}

// TestTheSessionPickerSurvivesACardGone: the probe lands after the card
// ended — the model tier opens on nothing rather than on a menu that
// cannot change the card it named.
func TestTheSessionPickerSurvivesACardGone(t *testing.T) {
	m, r := sessionPickerBoard(t)

	m.openCardModelPicker()
	menu := m.Overlay.Top().(*commandMenu)
	m = pump(t, m, menu.onRun("session-backend:claude"))
	tier2, ok := m.Overlay.Top().(*commandMenu)
	if !ok {
		t.Fatalf("the model tier never opened")
	}

	// the row ends up gone behind the probe's back (a delete, a
	// multi-repo reload), then its message lands anyway: no second tier
	// is pushed on top of the one already open
	depth := m.Overlay.Len()
	m.rows = nil
	m.handleSessionModelsMsg(sessionModelsMsg{id: r.F.ID, backend: "claude", models: []string{"claude-haiku-4-5"}})
	if m.Overlay.Top().(*commandMenu) != tier2 || m.Overlay.Len() != depth {
		t.Errorf("a gone card's probe pushed another picker (%d open, was %d)", m.Overlay.Len(), depth)
	}
}

// TestTheWebSessionPickerOffersTheSameList: the web face's picker rows
// come from the same merged view — the agent's own catalog plus the
// workspace's profile ids — so the two faces cannot disagree about what
// a session can run on. The row appears once: the inventory is one list.
func TestTheWebSessionPickerOffersTheSameList(t *testing.T) {
	m, r := sessionPickerBoard(t)
	acts := m.webActions(r)
	n := 0
	for _, a := range acts {
		if a.ID == "model" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the web menu offers the model switch %d times", n)
	}
	models := m.webSessionModels()
	for _, a := range models.Agents {
		if a.Name != "claude" {
			continue
		}
		// the loop-built rows are the profile ids only; the merged view
		// arrives off the loop (Bridge.Form)
		if len(a.Models) != 1 || a.Models[0] != "claude-sonnet-5-5" {
			t.Errorf("the inline rows = %v, want the profile id", a.Models)
		}
	}
	merged := m.engine.SessionModelChoices(context.Background(), "claude")
	want := []string{"claude-haiku-4-5", "claude-opus-5-5", "claude-sonnet-5-5"}
	if !equalStrings(merged, want) {
		t.Errorf("the merged view = %v, want %v", merged, want)
	}
}

// sessionModelOf is the pair card id's session runs on, read from the
// store's row — the switch writes the card, not the test's stale copy.
func (m *Shell) sessionModelOf(f domain.Feature) (backend, model string) {
	if fresh, err := m.store.GetFeature(context.Background(), f.ID); err == nil {
		f = fresh
	}
	return m.engine.SessionModel(f)
}

// containsAction reports whether the action list carries id.
func containsAction(acts []cardAction, id string) bool {
	for _, a := range acts {
		if a.id == id {
			return true
		}
	}
	return false
}

// equalStrings compares two id lists, order and content both.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
