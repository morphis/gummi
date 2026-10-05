package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/cardrun"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// freeformRow builds a freeform card's row, at the one stage such a card
// ever holds.
func freeformRow(num int, title string, wt bool) featureRow {
	id, _ := domain.NewID(domain.KindFreeform, num)
	slug, _ := domain.Slugify(title)
	f := domain.Feature{
		ID: id, Num: num, Kind: domain.KindFreeform, Title: title, Slug: slug,
		Stage: domain.StageOpen, BranchScheme: domain.BranchSchemeKind,
		Budget:  domain.Budget{Envelope: 400},
		Profile: "thrifty", CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	return featureRow{F: f, HasWorktree: wt}
}

// freeformShell is a detached board whose selected card is a freeform one.
func freeformShell(t *testing.T, w, h int, wt bool) *Shell {
	t.Helper()
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	m.now = func() time.Time { return fixedTime }
	m.rows = []featureRow{
		row(42, "dark mode", domain.StageImplement, "thrifty", true),
		freeformRow(12, "drop the leaked pty fd", wt),
	}
	m.sel = 1
	model, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return model.(*Shell)
}

// TestAFreeformCardsStripNamesItsBranch: the five stages faint with none
// of them lit would say the card is somewhere in the workflow, which is
// the one thing that is not true of it. Its branch is what a reader of
// that row actually wants.
func TestAFreeformCardsStripNamesItsBranch(t *testing.T) {
	m := freeformShell(t, 120, 34, true)
	out := ansi.Strip(m.threadView(120, 34))
	if !strings.Contains(out, "freeform") {
		t.Errorf("the masthead never says the card is freeform:\n%s", out)
	}
	if !strings.Contains(out, "ff/drop-the-leaked-pty-fd") {
		t.Errorf("the masthead never names the card's branch:\n%s", out)
	}
	for _, stage := range []string{"plan", "implement", "verify"} {
		if strings.Contains(out, stage) {
			t.Errorf("the freeform card's page mentions the workflow stage %q:\n%s", stage, out)
		}
	}
}

// TestAFreeformCardSaysHowToStartIt: with no session and nothing on its
// branch, the thread must still say what to do — a blank page under a
// card that claims to be in progress teaches nothing.
func TestAFreeformCardSaysHowToStartIt(t *testing.T) {
	m := freeformShell(t, 120, 34, false)
	out := ansi.Strip(m.threadView(120, 34))
	if !strings.Contains(out, "type below to start") {
		t.Errorf("an unstarted freeform card does not say how to start it:\n%s", out)
	}
}

// TestAStartedFreeformCardPointsAtItsDiff is the other half: a card whose
// work is on its branch but whose conversation is not on record — its row
// dropped, or a gummi that predates the row — must say so and point at the
// branch. Saying nothing there is what would read as "my card is gone".
func TestAStartedFreeformCardPointsAtItsDiff(t *testing.T) {
	m := freeformShell(t, 120, 34, true)
	out := ansi.Strip(m.threadView(120, 34))
	if !strings.Contains(out, "alt+d") {
		t.Errorf("a worked-on freeform card does not point at its diff:\n%s", out)
	}
	if !strings.Contains(out, "no conversation on record here") {
		t.Errorf("nothing explains why the conversation is not here:\n%s", out)
	}
}

// TestAFreeformCardOffersNoWorkflowActions: "not shown" and "not
// available" must not diverge, so every workflow-shaped row is withheld
// from a card that has no workflow — and the endings it does have are
// offered.
func TestAFreeformCardOffersNoWorkflowActions(t *testing.T) {
	in := nextInput{stage: domain.StageOpen, kind: domain.KindFreeform, hasWorktree: true}
	r := freeformRow(12, "drop the leaked pty fd", true)
	acts := cardActionsFor(in, r)

	ids := map[string]bool{}
	for _, a := range acts {
		ids[a.id] = true
	}
	for _, withheld := range []string{"advance", "spec", "verify", "bounce", "gate", "ask"} {
		if ids[withheld] {
			t.Errorf("a freeform card offers %q, which it has no workflow for", withheld)
		}
	}
	for _, want := range []string{"diff", "merge", "handoff"} {
		if !ids[want] {
			t.Errorf("a freeform card does not offer %q", want)
		}
	}
	// write a spec needs an engine to run the handoff brief's turn: a
	// detached board offers no row that only refuses
	if ids["writespec"] {
		t.Error("a detached board offers write a spec")
	}
	in.agentWired = true
	ids = map[string]bool{}
	for _, a := range cardActionsFor(in, r) {
		ids[a.id] = true
	}
	if !ids["writespec"] {
		t.Error("a wired board does not offer write a spec on an open session")
	}
	if a := writespecRowOf(cardActionsFor(in, r)); a == nil {
		t.Error("the writespec row vanished from the wired list")
	} else if a.key != "w" {
		t.Errorf("write a spec wears key %q, want w", a.key)
	}
}

// writespecRowOf finds the write-a-spec row in an action list.
func writespecRowOf(acts []cardAction) *cardAction {
	for i := range acts {
		if acts[i].id == "writespec" {
			return &acts[i]
		}
	}
	return nil
}

// TestAFreeformCardsNextStepsAreItsReviewLoop: it has no gate to teach a
// reader what comes next, so the answer set has to say it — read the
// diff, land it, keep the branch and close, or continue it as a spec.
func TestAFreeformCardsNextStepsAreItsReviewLoop(t *testing.T) {
	in := nextInput{stage: domain.StageOpen, kind: domain.KindFreeform, hasWorktree: true}
	var ids []string
	for _, a := range nextActions(in) {
		ids = append(ids, a.id)
	}
	want := []string{"diff", "merge", "handoff"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("next steps = %v, want %v", ids, want)
	}

	// With an engine to run the handoff brief's turn, the answer set gains
	// the third ending — after the two the branch already has.
	in.agentWired = true
	ids = nil
	for _, a := range nextActions(in) {
		ids = append(ids, a.id)
	}
	want = []string{"diff", "merge", "handoff", "writespec"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("next steps with an engine = %v, want %v", ids, want)
	}

	// Before anything has run there is nothing to read and nothing to
	// land: a row offering either would be a row that refuses.
	if acts := nextActions(nextInput{stage: domain.StageOpen, kind: domain.KindFreeform}); len(acts) != 0 {
		t.Errorf("an unstarted freeform card recommends %v, want nothing", acts)
	}
}

// TestAFreeformCardsKeysDropTheWorkflowOnes: the status bar and the ?
// overlay render from one slice, so a key that refuses on this card must
// not appear in it.
func TestAFreeformCardsKeysDropTheWorkflowOnes(t *testing.T) {
	m := freeformShell(t, 120, 34, true)
	keys := map[string]bool{}
	for _, b := range m.boardBindings() {
		keys[b.key] = true
	}
	for _, withheld := range []string{"g", "s", "b", "v", "A"} {
		if keys[withheld] {
			t.Errorf("the key list offers %q on a freeform card", withheld)
		}
	}
	// What it keeps: the diff, the branch verbs and its endings.
	for _, want := range []string{"d", "m", "h", "r", "c"} {
		if !keys[want] {
			t.Errorf("the key list drops %q, which works on a freeform card", want)
		}
	}
}

// TestTheComposerOnAFreeformCardNamesWhatItCanDo: the placeholder must not
// advertise approve/send-back/gate, which is what this surface is built
// around and exactly what such a card does not have.
func TestTheComposerOnAFreeformCardNamesWhatItCanDo(t *testing.T) {
	got := composerPlaceholder(domain.KindFreeform)
	if got == placeholderText {
		t.Fatal("a freeform card gets the workflow composer's placeholder")
	}
	for _, want := range []string{"branch", "alt+d", "land"} {
		if !strings.Contains(got, want) {
			t.Errorf("the freeform placeholder never mentions %q: %q", want, got)
		}
	}
}

// TestCreatingAFreeformCardMintsItOutsideTheGraph drives the real creation
// dialog and checks what lands in the store: a card at the stage that has
// no edges, on its own branch, with no draft artifact anywhere.
func TestCreatingAFreeformCardMintsItOutsideTheGraph(t *testing.T) {
	m, root := newWorkspace(t)
	m.SetCopilotHint(false)
	m = pump(t, m, m.Init())
	m.Overlay.Push(m.openCardForm(domain.CardType{Kind: domain.KindFreeform}))
	m = typeString(t, m, "Drop the leaked pty fd")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	fs, err := m.store.ListFeatures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 {
		t.Fatalf("minted %d cards, want 1", len(fs))
	}
	f := fs[0]
	if f.Kind != domain.KindFreeform {
		t.Errorf("kind = %q, want freeform", f.Kind)
	}
	if f.Stage != domain.StageOpen {
		t.Errorf("stage = %q, want %q — a freeform card starts outside the graph", f.Stage, domain.StageOpen)
	}
	if got := f.BranchName(); got != "ff/drop-the-leaked-pty-fd" {
		t.Errorf("branch = %q", got)
	}
	// No artifact was seeded: its thread is the record.
	if entries, _ := os.ReadDir(filepath.Join(root, ".gummi", "state", "drafts")); len(entries) != 0 {
		t.Errorf("a freeform card left %d draft(s) behind", len(entries))
	}
}

// TestLandingAFreeformCardFromTheBoard is the ending, driven the way a
// person drives it: m on the card, then the commit message. It has no
// verify stamp and no stage to be at, and it still reaches main and closes
// — through the one store method that may take it out of StageOpen.
func TestLandingAFreeformCardFromTheBoard(t *testing.T) {
	m, root := newWorkspace(t)
	m.SetCopilotHint(false)
	m = pump(t, m, m.Init())
	m.Overlay.Push(m.openCardForm(domain.CardType{Kind: domain.KindFreeform}))
	m = typeString(t, m, "Drop the leaked pty fd")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	ctx := context.Background()
	fs, err := m.store.ListFeatures(ctx)
	if err != nil || len(fs) != 1 {
		t.Fatalf("features = %v, err = %v", fs, err)
	}
	f := fs[0]
	// Cut the tree and leave a commit on it, the way a turn would.
	if _, err := m.wt.Ensure(ctx, &f); err != nil {
		t.Fatal(err)
	}
	commitWork(t, root, string(f.ID))
	m = pump(t, m, m.loadRows)

	before := headSHA(t, root)
	// Creating a freeform card opens its page (work is about to happen
	// there), and on the card page every printable key belongs to the
	// composer — so the board is where m is a verb.
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m.sel = 0
	m = press(t, m, tea.KeyPressMsg{Code: 'm', Text: "m"})
	if _, ok := m.Overlay.Top().(*commitMsgDialog); !ok {
		t.Fatalf("m did not open the commit-message dialog (notice %q)", m.notice.text)
	}
	typeMessage(t, m, "fix(copilot): drop the pty fd leaked on idle timeout")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.notice.isErr || !strings.Contains(m.notice.text, "squash-merged") {
		t.Fatalf("merge notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
	m = pump(t, m, m.loadRows)

	if got := headSHA(t, root); got == before {
		t.Fatalf("main did not move: %s", got)
	}
	got, err := m.store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != domain.StageDone {
		t.Errorf("stage after landing = %q, want done", got.Stage)
	}
}

// headSHA is main's current commit in the managed checkout.
func headSHA(t *testing.T, root string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(), "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// TestTheArtifactChordAnswersOnAFreeformCard: the tab bar does not draw an
// artifact tab for a card with no document, so the chord that names it owes
// an answer rather than mounting a view onto nothing.
func TestTheArtifactChordAnswersOnAFreeformCard(t *testing.T) {
	m := freeformShell(t, 120, 34, true)
	m.cardOpen = true
	if bar := ansi.Strip(m.cardTabBar(cardTabThread, 120)); strings.Contains(bar, "spec") {
		t.Errorf("the tab bar offers a document on a freeform card: %q", bar)
	}
	cmd, handled := m.cardTabKey("alt+s")
	if !handled {
		t.Fatal("alt+s was not answered on a freeform card")
	}
	if cmd != nil {
		t.Error("alt+s mounted something on a card with no document")
	}
	if !strings.Contains(m.notice.text, "no document") {
		t.Errorf("notice = %q, want it to say why", m.notice.text)
	}
}

// TestHandOffIsOfferedOnAFreeformCard: hand-off is stage-gated everywhere
// else because it is an ending and an unfinished card has nothing to end.
// On a freeform card only the person can say when the work is finished, so
// the stage refusal must not fire.
func TestHandOffIsOfferedOnAFreeformCard(t *testing.T) {
	m := freeformShell(t, 120, 34, true)
	m.handleKey(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if strings.Contains(m.notice.text, "hand-off ends a verified card") {
		t.Errorf("hand-off was refused on a freeform card: %q", m.notice.text)
	}
}

// TestTheFreeformCommandIsInTheVocabulary: the kind row of `n` reaches a
// freeform card, but a reader who knows what they want types the word. The
// entry has no accelerator, so the menu and the composer's slash line are
// the only places it can be found.
func TestTheFreeformCommandIsInTheVocabulary(t *testing.T) {
	m := freeformShell(t, 120, 34, true)
	var found *command
	for _, c := range m.globalCommands() {
		if c.name == "freeform" {
			found = &c
			break
		}
	}
	if found == nil {
		t.Fatal("no freeform entry in the command vocabulary")
	}
	if found.key != "" {
		t.Errorf("the freeform command took the accelerator %q", found.key)
	}
	if cmd := m.runCommand(found.id); cmd != nil {
		t.Error("opening the creation dialog should need no command")
	}
	d, ok := m.Overlay.Top().(*cardForm)
	if !ok {
		t.Fatalf("the freeform command did not open the creation dialog")
	}
	if d.Kind() != domain.KindFreeform {
		t.Errorf("the dialog opened preset to %q, want freeform", d.Kind())
	}
}

// TestTheStatsTabReportsAFreeformCardsBill: a freeform card has no passes
// — nothing mirrors a stage enter for it — but it has a real bill, and the
// pty drive caught this surface saying "nothing has run on this card yet"
// in front of a card that had spent nine thousand credits.
func TestTheStatsTabReportsAFreeformCardsBill(t *testing.T) {
	f := freeformRow(12, "drop the leaked pty fd", true).F
	f.Spend = domain.Spend{Credits: 9036}
	run := cardrun.Report(cardrun.Input{
		Feature: f,
		Spend: []state.StageSpend{
			{Stage: domain.StageOpen, Role: "implementer", Model: "stand-in", Credits: 9036},
		},
	})
	out := ansi.Strip(strings.Join(statsLines(theme.New(theme.GummiDark()), run, 90), "\n"))
	if strings.Contains(out, "nothing has run on this card yet") {
		t.Errorf("the stats tab denies a spent card ran:\n%s", out)
	}
	for _, want := range []string{"WHERE IT WENT", "9036", "THE ENVELOPE"} {
		if !strings.Contains(out, want) {
			t.Errorf("the stats tab never reports %q:\n%s", want, out)
		}
	}

	// A card that genuinely has not run still says so.
	bare := cardrun.Report(cardrun.Input{Feature: freeformRow(13, "not started", false).F})
	if !strings.Contains(ansi.Strip(strings.Join(statsLines(theme.New(theme.GummiDark()), bare, 90), "\n")),
		"nothing has run on this card yet") {
		t.Error("an unstarted card no longer says nothing has run")
	}
}

// TestWhileAFreeformTurnRunsTheAnswerIsToStopIt: a turn in flight owns the
// screen, and stopping it is the only answer worth offering — nothing else
// on this board stops an interactive session, and the deps picker (which p
// otherwise opens) must not be what p means while one is running.
func TestWhileAFreeformTurnRunsTheAnswerIsToStopIt(t *testing.T) {
	busy := nextInput{stage: domain.StageOpen, kind: domain.KindFreeform, hasWorktree: true, freeformBusy: true}
	var ids []string
	for _, a := range nextActions(busy) {
		ids = append(ids, a.id)
	}
	if len(ids) != 1 || ids[0] != "pause" {
		t.Errorf("answers while a turn runs = %v, want just the stop", ids)
	}

	r := freeformRow(12, "drop the leaked pty fd", true)
	offered := map[string]bool{}
	for _, a := range cardActionsFor(busy, r) {
		offered[a.id] = true
	}
	if !offered["pause"] {
		t.Error("the inventory offers no way to stop a turn in flight")
	}
	if offered["deps"] {
		t.Error("the dependency picker is what p would open mid-turn")
	}

	// Between turns it is the review loop again: nothing to stop, and no
	// dependency picker either — a session has no stages for a
	// dependency to hold back.
	idle := busy
	idle.freeformBusy = false
	offered = map[string]bool{}
	for _, a := range cardActionsFor(idle, r) {
		offered[a.id] = true
	}
	if offered["pause"] {
		t.Error("between turns there is no turn to stop")
	}
	if offered["deps"] {
		t.Error("a session offers a dependency picker, but nothing holds a session back")
	}
}

// TestDeletingAFreeformCardLeavesTheWorkspaceStanding is the regression
// test for the worst bug this feature had: the delete path removed the
// card's artifact with os.RemoveAll over filepath.Join(root,
// ArtifactPath()), and a freeform card's artifact path is empty — so it
// deleted the repository. A pty drive caught it; nothing in the unit tests
// could have, because none of them deleted a card with no document.
func TestDeletingAFreeformCardLeavesTheWorkspaceStanding(t *testing.T) {
	m, root := newWorkspace(t)
	m.SetCopilotHint(false)
	m = pump(t, m, m.Init())
	m.Overlay.Push(m.openCardForm(domain.CardType{Kind: domain.KindFreeform}))
	m = typeString(t, m, "Drop the leaked pty fd")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})

	ctx := context.Background()
	fs, err := m.store.ListFeatures(ctx)
	if err != nil || len(fs) != 1 {
		t.Fatalf("features = %v, err = %v", fs, err)
	}
	f := fs[0]
	if _, err := m.wt.Ensure(ctx, &f); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.deleteFeature(f.ID))

	// The card is gone…
	if left, err := m.store.ListFeatures(ctx); err != nil || len(left) != 0 {
		t.Errorf("features after the delete = %v (err %v), want none", left, err)
	}
	// …and everything that is not the card is still there.
	for _, keep := range []string{"README.md", ".git", ".gummi"} {
		if _, err := os.Stat(filepath.Join(root, keep)); err != nil {
			t.Fatalf("deleting a freeform card removed %s from the workspace: %v", keep, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".gummi", "worktrees", string(f.ID))); !os.IsNotExist(err) {
		t.Errorf("the card's own worktree survived the delete (stat err = %v)", err)
	}
}

// freeformAgentWorkspace is newWorkspace with a fake-backed engine
// attached, for the surfaces a freeform card's own session drives.
func freeformAgentWorkspace(t *testing.T, ag agent.Agent) (*Shell, *engine.Engine, string) {
	t.Helper()
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
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
	eng := engine.New(engine.Config{
		Agents: singleAgent(ag), Store: store, Pool: worktree.WrapSingle(wt),
		Workspace: ws, Model: "fake-model",
	})
	t.Cleanup(func() { eng.Close() })

	m := NewShell(theme.GummiDark(), "v0-test")
	m.now = func() time.Time { return fixedTime }
	m.Attach(store, worktree.WrapSingle(wt), ws)
	m.AttachEngine(eng)
	m.SetCopilotHint(false)
	m = pump(t, m, m.Init())
	return m, eng, root
}

// TestFreeformWritespec drives the third ending end to end: the dialog
// opens on its drafting state while the fetch — the brief turn — runs, the
// thread says gummi is drafting rather than working, the brief arrives as
// an editable field, and confirming mints the spec with the edited text
// and advances it into plan.
func TestFreeformWritespec(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	once := &sync.Once{}
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "handoff brief") {
			once.Do(func() { close(started) })
			<-release
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "asked\n- drop the leaked pty fd\n\ndecided\n- keep the fix in the adapter\n\ndone\n- sketched the fix on the branch\n\nremaining\n- write the test"},
				{Kind: agent.EventIdle},
			}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "on it"}, {Kind: agent.EventIdle}}
	}}
	ag.Caps = agent.Capabilities{UsageEvents: true, Interrupt: true}
	m, eng, root := freeformAgentWorkspace(t, ag)
	ctx := context.Background()

	m.Overlay.Push(m.openCardForm(domain.CardType{Kind: domain.KindFreeform}))
	m = typeString(t, m, "Drop the leaked pty fd")
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	fs, err := m.store.ListFeatures(ctx)
	if err != nil || len(fs) != 1 {
		t.Fatalf("features = %v, err = %v", fs, err)
	}
	f := fs[0]
	ff, err := eng.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "drop the leaked pty fd"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, eng, f.ID)

	// back to the board, where the card's keys are its own
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})
	m.sel = 0

	// w opens the dialog on its drafting state and starts the fetch; the
	// fetch is driven by hand so the in-flight moment can be looked at
	model, cmd := m.Update(tea.KeyPressMsg{Code: 'w', Text: "w"})
	m = model.(*Shell)
	d, ok := m.Overlay.Top().(*freeformSpecDialog)
	if !ok || !d.drafting {
		t.Fatalf("w did not open the writespec dialog on its drafting state (top %T)", m.Overlay.Top())
	}
	fetchDone := make(chan tea.Msg, 1)
	go func() { fetchDone <- cmd() }()
	<-started

	// while the turn runs, the thread names the drafting, not "working"
	m.cardOpen = true
	out := ansi.Strip(m.threadView(120, 34))
	if !strings.Contains(out, "drafting the handoff brief") {
		t.Errorf("the thread does not name the drafting while the brief turn runs:\n%s", lastLines(out, 6))
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "working…") {
			t.Errorf("the brief turn still reads as the bare working word: %q", l)
		}
	}
	// the tab cycle skips the brief field while it is not rendered: tab
	// from the title lands on the profile, and typing into the dialog
	// cannot reach a textarea that is not on it yet
	model, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = model.(*Shell)
	if d, ok := m.Overlay.Top().(*freeformSpecDialog); !ok || d.focus != specFieldProfile {
		t.Fatalf("tab while drafting landed on field %d, want the profile (the brief is not rendered yet)", d.focus)
	}
	close(release)
	msg := <-fetchDone
	model, cmd = m.Update(msg)
	m = model.(*Shell)
	m = pump(t, m, cmd)

	d, ok = m.Overlay.Top().(*freeformSpecDialog)
	if !ok || d.drafting {
		t.Fatalf("the fetched draft never landed in the dialog (top %T)", m.Overlay.Top())
	}
	if !strings.Contains(d.brief.Value(), "write the test") {
		t.Errorf("the dialog does not hold the session's brief:\n%s", d.brief.Value())
	}

	// with the draft landed, the brief field is back in the cycle: tab
	// back from the profile reaches it
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	d, ok = m.Overlay.Top().(*freeformSpecDialog)
	if !ok || d.focus != specFieldBrief {
		t.Fatalf("shift+tab after the draft landed went to field %d, want the brief", d.focus)
	}

	// the person edits the brief before anything mints
	d.brief.SetValue(strings.Replace(d.brief.Value(), "write the test", "write the test, edited by hand", 1))

	// confirm: ctrl+s from any field starts the spec
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	// the spec exists, at plan, on a branch of its own cut from the
	// session's tip; the session is closed and keeps its branch
	deadline := time.Now().Add(testWaitTimeout)
	var spec domain.Feature
	for {
		rows, err := m.store.ListFeatures(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			if r.ID != f.ID && r.Kind == domain.KindFeature {
				spec = r
			}
		}
		if spec.ID != "" && spec.Stage == domain.StagePlan {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the spec never reached plan: %+v (rows %v)", spec, rows)
		}
		time.Sleep(20 * time.Millisecond)
		m = pump(t, m, m.loadRows)
	}
	if got := spec.BranchName(); got == f.BranchName() {
		t.Errorf("the spec shares the session's branch %q", got)
	}
	if _, err := exec.CommandContext(context.Background(), "git", "-C", root,
		"rev-parse", "--verify", f.BranchName()).CombinedOutput(); err != nil {
		t.Errorf("the session's branch did not survive the handoff: %v", err)
	}
	closed, err := m.store.GetFeature(ctx, f.ID)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Stage != domain.StageDone {
		t.Errorf("the session is at %s, want done", closed.Stage)
	}
	doc, err := os.ReadFile(filepath.Join(root, spec.ArtifactPath()))
	if err != nil {
		doc, err = os.ReadFile(filepath.Join(root, ".gummi", "state", "drafts", filepath.Base(spec.ArtifactPath())))
	}
	if err != nil || !strings.Contains(string(doc), "Continued from the session "+string(f.ID)) ||
		!strings.Contains(string(doc), "edited by hand") {
		t.Errorf("the spec does not carry the edited brief (%v):\n%s", err, doc)
	}
}

// waitFreeformIdle is the engine package's poll, for the ui tests that
// drive a freeform card's own session.
func waitFreeformIdle(t *testing.T, eng *engine.Engine, id domain.FeatureID) {
	t.Helper()
	deadline := time.After(testWaitTimeout)
	for {
		if ff := eng.Freeform(id); ff != nil && !ff.Busy() && !ff.Briefing() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("the freeform session never went idle")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// lastLines is the last n lines of a block of text, for error output.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// The stack row (and the web's stackable list, which is the same list)
// offers a freeform session only while it holds a branch of its own: not
// one in the main checkout, and not one that is closed.
func TestStackCandidatesSkipBranchlessSessions(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0.1.0-test")
	open := freeformRow(1, "open session", true)
	main := freeformRow(2, "main checkout", true)
	main.F.MainCheckout = true
	closed := freeformRow(3, "closed session", true)
	closed.F.Stage = domain.StageDone
	m.rows = []featureRow{row(42, "dark mode", domain.StageImplement, "thrifty", true), open, main, closed}
	var got []domain.FeatureID
	for _, c := range m.stackCands() {
		got = append(got, c.ID)
	}
	if len(got) != 2 || got[0] != "FD-042" || got[1] != open.F.ID {
		t.Errorf("stack candidates = %v, want FD-042 and %s", got, open.F.ID)
	}
}

// TestASessionContinuedAsASpecDoesNotLand: a session continued as a spec
// hands its work to that spec's branch, which lands it verified. Its own
// closing block offers no landing, says where the work went, and every
// landing door refuses it — the same work reaching main a second way,
// past the floor the person just sent it to, is the one thing it must not
// do.
func TestASessionContinuedAsASpecDoesNotLand(t *testing.T) {
	r := freeformRow(31, "drop the leaked pty fd", true)
	r.F.Stage = domain.StageDone
	r.F.HandedOffAt = fixedTime
	r.F.ContinuedAs = "FD-032"

	in := nextInput{
		stage: domain.StageDone, kind: domain.KindFreeform, hasWorktree: true,
		ending: r.F.Ending(false), continuedAs: r.F.ContinuedAs,
	}
	for _, a := range closedActions(in) {
		if a.id == "merge" {
			t.Errorf("the continued session offers %q", a.label)
		}
		if a.id == "newbug" && strings.Contains(a.why, "spec") {
			t.Errorf("a session has no spec to carry, but the bug row says %q", a.why)
		}
	}
	if s := endingSentence(in); !strings.Contains(s, "Continued as the spec FD-032") || strings.Contains(s, "Landing it after all") {
		t.Errorf("the closing sentence = %q, want it to name the spec and offer no landing", s)
	}
	if q := decisionQuestion(decisionClosed, r, in); q != "FF-031 is closed — continued as FD-032." {
		t.Errorf("the closing head = %q, want it to name the spec", q)
	}
	why := (&Shell{}).landingRefusalIn(r.F, r, true, &in)
	if !strings.Contains(why, "continues as FD-032") {
		t.Errorf("landing the continued session was not refused for its spec: %q", why)
	}
	// the inventory reads the same refusal, as nextInputFor hands it over
	in.landRefused = why
	for _, a := range cardActionsFor(in, r) {
		if a.id == "handoff" || a.id == "merge" {
			t.Errorf("the closed session still lists %q", a.id)
		}
	}

	// a plain hand-off still lands after all
	in.continuedAs, r.F.ContinuedAs = "", ""
	if !slices.ContainsFunc(closedActions(in), func(a nextAction) bool { return a.id == "merge" }) {
		t.Error("a plain hand-off no longer offers landing it after all")
	}
}

// TestAQuestionOnAFreeformCardTakesTheLine: a freeform card's agent that
// asks ask_user is blocked inside that call, so a second turn is refused.
// Prose typed while the question is open is its answer, armed or not; with
// no question open the same prose is still a turn, and a project command
// stays a turn whether or not a question is open.
func TestAQuestionOnAFreeformCardTakesTheLine(t *testing.T) {
	m, eng := agentWorkspace(t, &agent.Fake{})
	r := freeformRow(7, "tidy the parser", true)
	if err := m.store.CreateFeature(context.Background(), &r.F); err != nil {
		t.Fatal(err)
	}
	ff, err := eng.OpenFreeform(context.Background(), r.F)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(ff.WorkDir(), ".claude", "commands", "review.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("---\ndescription: review the diff\n---\nReview $ARGUMENTS."), 0o600); err != nil {
		t.Fatal(err)
	}
	m.rows = append(m.rows, r)
	m.sel = len(m.rows) - 1

	ask := &engine.Ask{Question: "Which way?", Options: []engine.AskOption{{Label: "Left"}, {Label: "Right"}}}
	asking := func() *threadDecision {
		return &threadDecision{card: r.F.ID, kind: decisionAsk, question: ask.Question, ask: ask}
	}
	none := func() *threadDecision { return nil }
	route := func(text string, decide func() *threadDecision) lineRoute {
		return m.classifyThreadLine(r, text, decide).route
	}

	if got := route("Go up the middle", asking); got != lineAskAnswer {
		t.Errorf("prose at an open question routes %v, want the answer", got)
	}
	if got := route("Go up the middle", none); got != lineFreeformTurn {
		t.Errorf("prose with no question open routes %v, want a turn", got)
	}
	if got := route("/review the parser", asking); got != lineFreeformTurn {
		t.Errorf("a project command at an open question routes %v, want a turn", got)
	}
	m.threadFreeForm = true
	if got := route("Go up the middle", asking); got != lineAskAnswer {
		t.Errorf("armed prose at an open question routes %v, want the answer", got)
	}
	if got := route("/review the parser", asking); got != lineFreeformTurn {
		t.Errorf("an armed project command at an open question routes %v, want a turn", got)
	}
	m.threadFreeForm = false

	c := m.classifyThreadLine(r, "Go up the middle", asking)
	if got, says := m.webLineRoute(r, "Go up the middle", c); got != webapi.RouteAnswer || says != "answers the question above, in your words" {
		t.Errorf("the web composer says %v %q, want an answer in the person's words", got, says)
	}
}
