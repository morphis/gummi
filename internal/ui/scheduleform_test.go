package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/config"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// scheduleFormBoard is a board with a store and a claude agent that
// stages its own model catalog and a priced credit rate, so the dialog's
// pickers, probe and envelope hint all answer without a PATH lookup.
// Only claude answers installed, whatever this host happens to have.
func scheduleFormBoard(t *testing.T) *Shell {
	t.Helper()
	oldInstalled := agentInstalled
	agentInstalled = func(name string) bool { return name == "claude" }
	t.Cleanup(func() { agentInstalled = oldInstalled })

	named := namedBackendAgent{Fake: agent.NewFake("ok"), name: "claude"}
	named.Models = []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-4-5"}
	named.Rate = 2
	profiles := config.Profiles{Default: "alpha", Profiles: map[string]config.Profile{
		"alpha": {"implementer": {Backend: "claude", Model: "claude-sonnet-5-5"}},
	}}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(a ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", a, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "t")
	git("config", "user.email", "t@e.invalid")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-q", "-m", "init")

	ws, err := state.Init(root, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.OpenStore(ws.DBFile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	wt, err := worktree.NewManager(context.Background(), root, root, store)
	if err != nil {
		t.Fatal(err)
	}
	pool := worktree.WrapSingle(wt)
	eng := engine.New(engine.Config{
		Agents: singleAgent(named), Store: store, Pool: pool,
		Workspace: ws, Model: "claude-sonnet-5-5", Profiles: profiles,
	})
	t.Cleanup(func() { eng.Close() })

	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, pool, ws)
	m.AttachEngine(eng)
	m.SetProfileNames([]string{"alpha"})
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	m = model.(*Shell)
	// the open sessions a heartbeat may target; the dialog refuses one
	// that is not on the board, or closed
	for n := 1; n <= 4; n++ {
		f := freeformRow(n, fmt.Sprintf("session %d", n), true).F
		if err := store.CreateFeature(context.Background(), &f); err != nil {
			t.Fatal(err)
		}
		m.rows = append(m.rows, featureRow{F: f, HasWorktree: true})
	}
	return m
}

// TestTheScheduleDialogRefusesAHeartbeatOnAGoneOrClosedTarget: the fire
// turns a heartbeat off when its target is gone or closed, so the door
// refuses both before anything is stored — the same as a target that is
// not a freeform card.
func TestTheScheduleDialogRefusesAHeartbeatOnAGoneOrClosedTarget(t *testing.T) {
	m := scheduleFormBoard(t)
	closed := freeformRow(5, "closed session", true)
	closed.F.Stage = domain.StageDone
	m.rows = append(m.rows, closed)
	for target, want := range map[string]string{
		"FF-999": "not a card on this board",
		"FF-005": "is closed",
		"FD-001": "not a freeform card",
	} {
		_, err := m.scheduleFromForm(webapi.ScheduleRequest{
			Name: "beat " + target, Kind: string(domain.ScheduleHeartbeat), Target: target,
			Every: "1h", Prompt: "p",
		})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a heartbeat on %s = %v, want a refusal naming %q", target, err, want)
		}
	}
	if _, err := m.scheduleFromForm(webapi.ScheduleRequest{
		Name: "beat", Kind: string(domain.ScheduleHeartbeat), Target: "ff-002", Every: "1h", Prompt: "p",
	}); err != nil {
		t.Errorf("a heartbeat on an open session was refused: %v", err)
	}
}

// openScheduleFormOn pushes the dialog the way the schedules view does
// and runs the probe it returns, so the model rows are the merged
// catalog by the time the test looks.
func openScheduleFormOn(t *testing.T, m *Shell, edit *domain.Schedule) (*Shell, *scheduleForm) {
	t.Helper()
	m = pump(t, m, m.openScheduleForm(edit))
	d, ok := m.Overlay.Top().(*scheduleForm)
	if !ok {
		t.Fatalf("the schedule dialog is not on top: %T", m.Overlay.Top())
	}
	return m, d
}

// scheduleRowReads reads a row straight from the store, the way a test
// asserts what the dialog did.
func scheduleRowReads(t *testing.T, m *Shell, id string) domain.Schedule {
	t.Helper()
	sc, err := m.store.Schedule(context.Background(), domain.ScheduleID(id))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

// TestTheScheduleDialogSavesACompiledCron: a preset compiles at the
// dialog, the same way the web route compiles it, and the row is stored
// off — off by default is the rule, whatever face defines it.
func TestTheScheduleDialogSavesACompiledCron(t *testing.T) {
	m := scheduleFormBoard(t)
	_, d := openScheduleFormOn(t, m, nil)

	d.name.SetValue("nightly triage")
	d.every.SetValue("1h")
	d.prompt.SetValue("triage new issues")
	d.env.SetValue("50")
	d.refresh()
	if d.preview.Err != nil || d.preview.Cron != "0 * * * *" {
		t.Fatalf("the preview read %+v, want the compiled hourly cron", d.preview)
	}
	done, cmd := d.submit()
	if !done {
		t.Fatalf("save refused: %s", d.errText)
	}
	m = pump(t, m, cmd)

	sc := scheduleRowReads(t, m, "nightly-triage")
	if sc.Cron != "0 * * * *" || sc.Enabled || sc.Envelope != 50 {
		t.Errorf("stored row = cron %q enabled %v envelope %d", sc.Cron, sc.Enabled, sc.Envelope)
	}
	if sc.Kind != domain.ScheduleMint || sc.Timezone != "" {
		t.Errorf("stored row = kind %s tz %q; the default zone is the stored empty one", sc.Kind, sc.Timezone)
	}
}

// TestTheScheduleDialogRefusesAPairNoSessionCouldRun: the session
// picker's pre-save refusal is the dialog's too — a pair the engine
// would refuse is blocked before save, and nothing is stored.
func TestTheScheduleDialogRefusesAPairNoSessionCouldRun(t *testing.T) {
	m := scheduleFormBoard(t)
	_, d := openScheduleFormOn(t, m, nil)

	d.name.SetValue("bad pair")
	d.every.SetValue("1h")
	d.prompt.SetValue("p")
	d.env.SetValue("10")
	for i, b := range d.backends {
		if b.name == "claude" {
			d.backend = i
		}
	}
	d.model.SetValue("gpt-5")
	done, _ := d.submit()
	if done {
		t.Fatal("a pair the engine refuses was saved")
	}
	if !strings.Contains(d.errText, "Anthropic") {
		t.Errorf("the refusal = %q, want the engine's own words", d.errText)
	}
	if rows, err := m.store.ListSchedules(context.Background()); err != nil || len(rows) != 0 {
		t.Fatalf("the refused save stored %v", rows)
	}

	// a listed model saves: the probe's rows are the ones the engine
	// would run
	m2, d2 := openScheduleFormOn(t, m, nil)
	d2.name.SetValue("good pair")
	d2.every.SetValue("1h")
	d2.prompt.SetValue("p")
	d2.env.SetValue("10")
	for i, b := range d2.backends {
		if b.name == "claude" {
			d2.backend = i
		}
	}
	m2 = pump(t, m2, d2.probeModels())
	d2.cycleModel(1)
	done, cmd := d2.submit()
	if !done {
		t.Fatalf("a listed pair was refused: %s", d2.errText)
	}
	m2 = pump(t, m2, cmd)
	sc := scheduleRowReads(t, m2, "good-pair")
	if sc.Backend != "claude" || sc.Model != "claude-haiku-4-5" {
		t.Errorf("stored pair = %s/%q", sc.Backend, sc.Model)
	}
}

// TestTheScheduleDialogGatesKindRows: a heartbeat has a target and no
// repository, pair or brake of its own — the rows the kind does not mean
// are not there to fill, and the model probe follows the picked backend.
func TestTheScheduleDialogGatesKindRows(t *testing.T) {
	m := scheduleFormBoard(t)
	_, d := openScheduleFormOn(t, m, nil)

	for i, b := range d.backends {
		if b.name == "claude" {
			d.backend = i
		}
	}
	m = pump(t, m, d.probeModels())
	if len(d.models) != 3 {
		t.Fatalf("the probe answered %v, want the staged catalog", d.models)
	}

	// cycle the kind row to heartbeat: the mint rows leave the stop list
	// and the view
	d.setFocus(schedStopKind)
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if !d.kindIsHeartbeat() {
		t.Fatal("right did not reach the heartbeat")
	}
	for _, stop := range []int{schedStopRepo, schedStopBackend, schedStopModel, schedStopEnvelope} {
		for _, live := range d.stops() {
			if live == stop {
				t.Errorf("a heartbeat's stops include %d", stop)
			}
		}
	}
	view := d.View(m.styles, 120, 34)
	if strings.Contains(view, "budget") || strings.Contains(view, "model") {
		t.Error("the heartbeat dialog renders the mint rows")
	}
	if !strings.Contains(view, "target") {
		t.Error("the heartbeat dialog has no target row")
	}

	// and back: the mint rows return, the probe's rows still there
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	if d.kindIsHeartbeat() || len(d.models) != 3 {
		t.Fatalf("cycling back lost the kind or the probe's rows: %v", d.models)
	}
}

// TestTheScheduleDialogPrefillsAndEdits: the dialog opens on a stored row
// with its definition in the rows — the stored cron in the cron field,
// the empty timezone read back as "local" — and the save keeps the row
// off, every time.
func TestTheScheduleDialogPrefillsAndEdits(t *testing.T) {
	m := scheduleFormBoard(t)
	seed := &domain.Schedule{
		ID: "nightly", Name: "nightly triage", Kind: domain.ScheduleMint,
		Cron: "0 5 * * *", Prompt: "triage new issues", Envelope: 50,
	}
	if err := m.store.CreateSchedule(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	if err := m.store.SetScheduleEnabled(context.Background(), seed.ID, true, fixedTime); err != nil {
		t.Fatal(err)
	}

	stored, err := m.store.Schedule(context.Background(), seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, d := openScheduleFormOn(t, m, &stored)
	if d.name.Value() != "nightly triage" || d.cron.Value() != "0 5 * * *" {
		t.Fatalf("the rows read name=%q cron=%q", d.name.Value(), d.cron.Value())
	}
	if d.zone.Value() != "local" {
		t.Errorf("the empty stored timezone reads back %q, want local", d.zone.Value())
	}
	if d.env.Value() != "50" {
		t.Errorf("the budget reads back %q", d.env.Value())
	}

	d.cron.SetValue("30 5 * * *")
	d.refresh()
	done, cmd := d.submit()
	if !done {
		t.Fatalf("the edit was refused: %s", d.errText)
	}
	m = pump(t, m, cmd)

	fresh := scheduleRowReads(t, m, "nightly")
	if fresh.Cron != "30 5 * * *" {
		t.Errorf("the edited cron = %q", fresh.Cron)
	}
	if fresh.Enabled || !fresh.NextRun.IsZero() {
		t.Errorf("the edited row reads enabled=%v next=%v; an edit forces it off", fresh.Enabled, fresh.NextRun)
	}
	if fresh.CreatedAt != stored.CreatedAt {
		t.Error("the edit moved the row's created-at")
	}
}

// TestTheScheduleDialogRefusesWhatTheStoreWouldRefuse: the store's
// structural rules run at the front door, so a mint without a brake and
// a heartbeat without a target are refused with the dialog still open
// and its rows intact — not with a notice after it has closed, storing
// nothing. A filled heartbeat still saves, carrying no pair or brake of
// its own even when the rows it replaced held them.
func TestTheScheduleDialogRefusesWhatTheStoreWouldRefuse(t *testing.T) {
	m := scheduleFormBoard(t)
	_, d := openScheduleFormOn(t, m, nil)

	d.name.SetValue("no brake")
	d.every.SetValue("1h")
	d.prompt.SetValue("p")
	d.env.SetValue("")
	d.refresh()
	done, _ := d.submit()
	if done {
		t.Fatal("a mint without a brake was saved")
	}
	if !strings.Contains(d.errText, "brake") {
		t.Errorf("the refusal = %q, want the store's brake words", d.errText)
	}

	// a non-numeric budget reads as no brake at all
	d.env.SetValue("soon")
	d.refresh()
	if done, _ := d.submit(); done {
		t.Fatal("a non-numeric budget was saved")
	}

	// a heartbeat without a target
	d.name.SetValue("no target")
	d.kind = 1
	d.refresh()
	done, _ = d.submit()
	if done {
		t.Fatal("a targetless heartbeat was saved")
	}
	if !strings.Contains(d.errText, "names the freeform card") {
		t.Errorf("the refusal = %q, want the store's target words", d.errText)
	}
	if _, ok := m.Overlay.Top().(*scheduleForm); !ok {
		t.Fatal("the dialog closed on a refusal")
	}

	// the same heartbeat with a target saves, pair and brake cleared
	d.target.SetValue("ff-001")
	d.refresh()
	done, cmd := d.submit()
	if !done {
		t.Fatalf("a heartbeat with a target was refused: %s", d.errText)
	}
	m = pump(t, m, cmd)
	sc := scheduleRowReads(t, m, "no-target")
	if sc.Kind != domain.ScheduleHeartbeat || sc.Target != "FF-001" {
		t.Errorf("the stored heartbeat = %s target %s", sc.Kind, sc.Target)
	}
	if sc.Backend != "" || sc.Envelope != 0 {
		t.Errorf("the stored heartbeat carries backend %q envelope %d", sc.Backend, sc.Envelope)
	}
	if rows, err := m.store.ListSchedules(context.Background()); err != nil || len(rows) != 1 {
		t.Fatalf("the refused saves stored %v", rows)
	}
}

// TestTheScheduleDialogKeepsTheKindOnEdit: an edit's kind row renders
// frozen — not a stop, no cycle — so a mint edit stays a mint and a
// heartbeat edit stays a heartbeat, whatever the keys say, and neither
// edit's save is refused after the dialog has closed.
func TestTheScheduleDialogKeepsTheKindOnEdit(t *testing.T) {
	m := scheduleFormBoard(t)
	seed := &domain.Schedule{
		ID: "nightly", Name: "nightly triage", Kind: domain.ScheduleMint,
		Cron: "0 5 * * *", Prompt: "triage new issues", Envelope: 50,
	}
	if err := m.store.CreateSchedule(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	stored, err := m.store.Schedule(context.Background(), seed.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, d := openScheduleFormOn(t, m, &stored)

	for _, live := range d.stops() {
		if live == schedStopKind {
			t.Fatal("an edit's stops include the kind row")
		}
	}
	d.focus = schedStopKind
	d.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if d.kindIsHeartbeat() {
		t.Error("a mint edit's kind row cycled to heartbeat")
	}
	if view := d.View(m.styles, 120, 34); !strings.Contains(view, "an edit keeps the kind") {
		t.Error("the edit dialog does not say the kind is kept")
	}
	d.env.SetValue("60")
	done, cmd := d.submit()
	if !done {
		t.Fatalf("the mint edit was refused: %s", d.errText)
	}
	m = pump(t, m, cmd)
	fresh := scheduleRowReads(t, m, "nightly")
	if fresh.Kind != domain.ScheduleMint || fresh.Envelope != 60 || fresh.Target != "" {
		t.Errorf("the edited row = kind %s target %q envelope %d", fresh.Kind, fresh.Target, fresh.Envelope)
	}

	// the same freeze on a heartbeat: the kind stays, the target row is
	// the one to fill, and clearing it is refused in the open dialog
	hb := &domain.Schedule{
		ID: "watch", Name: "watch the deploy", Kind: domain.ScheduleHeartbeat,
		Target: "FF-003", Cron: "0 * * * *", Prompt: "keep going",
	}
	if err := m.store.CreateSchedule(context.Background(), hb); err != nil {
		t.Fatal(err)
	}
	stored, err = m.store.Schedule(context.Background(), hb.ID)
	if err != nil {
		t.Fatal(err)
	}
	m2, d2 := openScheduleFormOn(t, m, &stored)
	if !d2.kindIsHeartbeat() {
		t.Fatal("the heartbeat edit did not prefill its kind")
	}
	d2.focus = schedStopKind
	d2.HandleKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if !d2.kindIsHeartbeat() {
		t.Error("a heartbeat edit's kind row cycled to mint")
	}
	d2.target.SetValue("")
	done, _ = d2.submit()
	if done || !strings.Contains(d2.errText, "names the freeform card") {
		t.Fatalf("a targetless heartbeat edit = done %v refusal %q", done, d2.errText)
	}
	d2.target.SetValue("FF-004")
	done, cmd = d2.submit()
	if !done {
		t.Fatalf("the heartbeat edit was refused: %s", d2.errText)
	}
	m2 = pump(t, m2, cmd)
	fresh = scheduleRowReads(t, m2, "watch")
	if fresh.Kind != domain.ScheduleHeartbeat || fresh.Target != "FF-004" {
		t.Errorf("the edited heartbeat = kind %s target %s", fresh.Kind, fresh.Target)
	}
	if fresh.Backend != "" || fresh.Envelope != 0 {
		t.Errorf("the edited heartbeat carries backend %q envelope %d", fresh.Backend, fresh.Envelope)
	}
}

// TestTheScheduleDialogHintsTheEnvelope: the budget row carries the
// pair's guidance — what a credit buys where the workspace can price the
// pair, what the brake is for where it cannot — the way the web form's
// preview carries the same hint, so the terminal dialog never shows a
// bare budget row.
func TestTheScheduleDialogHintsTheEnvelope(t *testing.T) {
	m := scheduleFormBoard(t)
	_, d := openScheduleFormOn(t, m, nil)

	// the profile default is not priced here: the brake, in words
	d.refresh()
	if !strings.Contains(d.envHint, "caps what one minted card may spend") {
		t.Errorf("the unpriced hint = %q, want the brake words", d.envHint)
	}

	// a priced backend: what a credit buys on it, under the budget row
	for i, b := range d.backends {
		if b.name == "claude" {
			d.backend = i
		}
	}
	m = pump(t, m, d.probeModels())
	if !strings.Contains(d.envHint, "1 credit ≈ 500 tokens") {
		t.Errorf("the priced hint = %q, want the rate estimate", d.envHint)
	}
	if view := d.View(m.styles, 120, 34); !strings.Contains(view, "1 credit ≈ 500 tokens") {
		t.Error("the dialog does not render the hint")
	}
}

// TestTheScheduleDialogPreviewsBeforeSave: the live preview answers as
// the rows are typed — the canonical cron for a valid cadence, the
// package's refusal for one that can never fire — and a cadence the
// package refuses cannot reach the store from the dialog either.
func TestTheScheduleDialogPreviewsBeforeSave(t *testing.T) {
	m := scheduleFormBoard(t)
	_, d := openScheduleFormOn(t, m, nil)

	d.name.SetValue("never fires")
	d.cron.SetValue("0 0 30 2 *")
	d.prompt.SetValue("p")
	d.env.SetValue("10")
	d.refresh()
	if d.preview.Err == nil || !strings.Contains(d.preview.Err.Error(), "never") {
		t.Fatalf("the preview read %+v, want the never-fires refusal", d.preview)
	}
	if !strings.Contains(d.previewLine(m.styles, 100), "never") {
		t.Error("the preview line does not show the refusal")
	}
	done, _ := d.submit()
	if done {
		t.Fatal("a cadence the package refuses was saved")
	}
	if !strings.Contains(d.errText, "never") {
		t.Errorf("the save refusal = %q", d.errText)
	}

	// an unknown zone refuses in the preview, before save
	d2 := newScheduleForm(m, nil)
	d2.zone.SetValue("Mars/Olympus")
	d2.every.SetValue("1h")
	d2.prompt.SetValue("p")
	d2.env.SetValue("10")
	d2.refresh()
	if d2.preview.Err == nil || !strings.Contains(d2.preview.Err.Error(), "unknown timezone") {
		t.Fatalf("the preview read %+v, want the unknown-zone refusal", d2.preview)
	}
}

// TestTheCardMenuOpensAHeartbeatAimedAtTheCard: the card's own heartbeat
// row opens the dialog as a heartbeat on that card, and saving it stores
// the row and turns it on.
func TestTheCardMenuOpensAHeartbeatAimedAtTheCard(t *testing.T) {
	m := scheduleFormBoard(t)
	card := m.rows[len(m.rows)-1].F
	m = pump(t, m, m.openHeartbeatForm(card))
	d, ok := m.Overlay.Top().(*scheduleForm)
	if !ok {
		t.Fatalf("the schedule dialog is not on top: %T", m.Overlay.Top())
	}
	if !d.kindIsHeartbeat() || d.target.Value() != string(card.ID) {
		t.Fatalf("the dialog opened as kind %v target %q, want a heartbeat on the card", d.kind, d.target.Value())
	}
	d.prompt.SetValue("check CI, keep going")
	done, cmd := d.submit()
	if !done {
		t.Fatalf("save refused: %s", d.errText)
	}
	m = pump(t, m, cmd)
	sc := scheduleRowReads(t, m, strings.ToLower(string(card.ID))+"-heartbeat")
	if !sc.Enabled || sc.NextRun.IsZero() || sc.Target != card.ID {
		t.Errorf("stored row = enabled %v next %v target %s, want it on and aimed at %s", sc.Enabled, sc.NextRun, sc.Target, card.ID)
	}
}
