package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/reentry"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/webapi"
)

// Two landings drafting at once: each intent owns a commit dialog waiting
// on its draft, and each draft reaches its own dialog wherever it stands
// on the stack — the first is not lost under the second — so both come
// back with their own draft to be read.
func TestTwoLandingsDraftingAtOnceEachGetTheirDraft(t *testing.T) {
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	f2 := domain.Feature{ID: "FD-002", Num: 2, Title: "Other", Slug: "other", Stage: domain.StagePlan}
	if err := store.CreateFeature(ctx, &f2); err != nil {
		t.Fatal(err)
	}
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.loadRows }); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 2 })

	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	landed := map[string]string{}
	start := func(id domain.FeatureID, release chan struct{}) func(m *Shell, r featureRow) (tea.Cmd, error) {
		return func(m *Shell, r featureRow) (tea.Cmd, error) {
			d := newCommitMsgDialog(r.F, func(msg string) tea.Cmd {
				mu.Lock()
				landed[string(id)] = msg
				mu.Unlock()
				return nil
			}, func(context.Context, domain.Feature, bool) (string, error) {
				<-release
				return "feat: " + string(id), nil
			})
			m.Overlay.Push(d)
			return d.startDraft(false), nil
		}
	}
	var wg sync.WaitGroup
	outs := map[string]webOutcome{}
	run := func(id domain.FeatureID, rel chan struct{}) {
		defer wg.Done()
		out, err := b.intent(ctx, id, webInput{land: true}, 4*time.Second, start(id, rel))
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		outs[string(id)] = out
		mu.Unlock()
	}
	wg.Add(2)
	go run("FD-001", releaseA)
	time.Sleep(200 * time.Millisecond)
	go run("FD-002", releaseB)
	time.Sleep(200 * time.Millisecond)
	close(releaseA) // A's draft arrives while B's dialog is on top
	time.Sleep(200 * time.Millisecond)
	close(releaseB)
	wg.Wait()
	t.Logf("landed=%v", landed)
	t.Logf("FD-001 outcome=%+v", outs["FD-001"])
	t.Logf("FD-002 outcome=%+v", outs["FD-002"])
	for _, id := range []string{"FD-001", "FD-002"} {
		out := outs[id]
		if out.draft == nil || *out.draft != "feat: "+id {
			t.Errorf("%s: came back with draft %v (%+v), want its own \"feat: %s\" to read", id, out.draft, out, id)
		}
	}
	if len(landed) != 0 {
		t.Errorf("a landing went through on a draft nobody read: %v", landed)
	}
}

// A confirm the request's flow reaches for a DIFFERENT card is not the
// request's to answer: its yes was for its own card's question.
func TestAConfirmAnswersOnlyItsOwnCard(t *testing.T) {
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	f2 := domain.Feature{ID: "FD-002", Num: 2, Title: "Other", Slug: "other", Stage: domain.StageImplement}
	if err := store.CreateFeature(ctx, &f2); err != nil {
		t.Fatal(err)
	}
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.loadRows }); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 2 })
	var fired []string
	out, err := b.intent(ctx, "FD-001", webInput{confirm: webConfirmToken("confirm-handoff", "FD-001", "hand off FD-001?")}, webWait, func(m *Shell, r featureRow) (tea.Cmd, error) {
		m.Overlay.Push(&confirmDialog{id: "confirm-handoff", question: "hand off FD-001?", onConfirm: func() tea.Cmd {
			fired = append(fired, "handoff FD-001")
			// the yes starts a follow-up that raises a second question,
			// about another card, that spends credits
			return func() tea.Msg { return rebaseConflictMsg{f: f2, files: []string{"a.go"}} }
		}})
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fired = %v; notices = %q; refused=%q needs=%q", fired, out.notices, out.refused, out.needs)
	for _, n := range out.notices {
		if len(n) >= 6 && n[:6] == "FD-002" {
			t.Errorf("the agent-rebase confirm for FD-002 was answered yes by FD-001's request: %q", n)
		}
	}
}

// The verify gate's "land on main" answer hands the stored scribe draft
// back to be read — as the TUI's dialog arms on an unreviewed draft and
// lands only on a second ctrl+s — and lands nothing until it is sent back
// (TestOnlyALandingRequestAnswersTheLandingDialog lands the sent one).
func TestALandingAnswerHandsBackTheDraftToRead(t *testing.T) {
	b, _, _, f, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	var m0 *Shell
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { m0 = m; return nil }); err != nil {
		t.Fatal(err)
	}
	store := m0.store
	for _, to := range []domain.Stage{domain.StageImplement, domain.StageVerify} {
		if _, err := store.Transition(ctx, f.ID, to, "user"); err != nil {
			t.Fatal(err)
		}
	}
	f, _ = store.GetFeature(ctx, f.ID)
	dir, err := m0.wt.Create(ctx, &f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dark.go"), []byte("package dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "."}, {"commit", "-q", "-m", "work"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	tip, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err := store.SetVerifiedAt(ctx, f.ID, time.Now(), strings.TrimSpace(string(tip))); err != nil {
		t.Fatal(err)
	}
	const draft = "feat: what the scribe wrote"
	if err := store.SetCommitDraft(ctx, f.ID, draft, strings.TrimSpace(string(tip))); err != nil {
		t.Fatal(err)
	}
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.loadRows }); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 && bd.Rows[0].Stage == "verify" })
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { m.raiseAttention(f.ID, attnGate, "verify passed"); return nil }); err != nil {
		t.Fatal(err)
	}
	c, err := b.Card(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if c.Decision == nil {
		t.Fatal("no decision")
	}
	for _, o := range c.Decision.Options {
		t.Logf("option %s: %q — %q", o.ID, o.Label, o.Detail)
	}
	_, err = b.Answer(ctx, "FD-001", webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "advance", Against: c.Decision.Against.Token}, "Simon")
	we, ok := IsWebError(err)
	if !ok || we.Reason != webapi.ConflictNeeds || we.Needs != string(webapi.ActionNeedsMessage) || we.Draft == nil || *we.Draft != draft {
		t.Fatalf("a bare landing answer = %v, want the draft %q handed back to read", err, draft)
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(dir)))
	mainTip := func() string {
		out, _ := exec.Command("git", "-C", root, "log", "-1", "--format=%s", "main").Output()
		return strings.TrimSpace(string(out))
	}
	if mainTip() == draft {
		t.Fatalf("the verify answer landed a message the person never saw (%q)", draft)
	}
}

// A chip (and an in-flight read, and a consult chat) is one card's: a line
// sent to card B leaves card A's pending chip standing, to be answered.
func TestALineOnAnotherCardLeavesThisCardsChip(t *testing.T) {
	b, _, _, f, _ := headlessBoard(t, agent.NewFake("noted"))
	ctx := context.Background()
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	f2 := domain.Feature{ID: "FD-002", Num: 2, Title: "Other", Slug: "other", Stage: domain.StagePlan}
	if err := store.CreateFeature(ctx, &f2); err != nil {
		t.Fatal(err)
	}
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.loadRows }); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 2 })
	// A's line was read and is waiting on its chip, exactly as routeReentry
	// leaves it (syncDecision ran for A's stop first)
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		r, _ := m.rowByID(f.ID)
		leave := m.enterCard(f.ID, true)
		defer leave()
		m.syncDecision(m.openDecision(r))
		m.setChip(&reentryReading{id: f.ID, line: "the flag was never in the spec",
			out: reentry.Outcome{Action: reentry.Rewind, Target: domain.StagePlan, Path: []domain.Stage{domain.StagePlan}, Reason: "requirement_missing", Note: "the flag was never in the spec"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c, _ := b.Card(ctx, "FD-001")
	if c.Decision == nil || c.Decision.Kind != webapi.DecisionConfirm {
		t.Fatalf("fixture: FD-001 decision = %+v", c.Decision)
	}
	chip := c.Decision
	// someone else sends a line on FD-002
	res, err := b.Send(ctx, "FD-002", webapi.SendRequest{Text: "please also cover the settings page"}, "Ana")
	t.Logf("send on FD-002: route=%v err=%v", res.Route, err)
	c, _ = b.Card(ctx, "FD-001")
	if c.Decision == nil || c.Decision.Kind != webapi.DecisionConfirm {
		t.Errorf("FD-001's chip is gone after a line on FD-002: decision now %+v", c.Decision)
	}
	_, err = b.Answer(ctx, "FD-001", webapi.AnswerRequest{Ref: chip.Ref, Option: webOptionKeep, Against: chip.Against.Token}, "Simon")
	t.Logf("answering FD-001's chip 'keep' afterwards: %v", err)
}

// A decision whose kind changed under the page (the stop at plan became a
// gate: the architect finished) is reported "moved", not "answered" after
// an old crossing nobody made against this decision.
func TestAKindChangeReadsAsMovedNotAnswered(t *testing.T) {
	b, _, _, f, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	var store *state.Store
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	// an old crossing, by someone, long ago
	if _, err := store.Transition(ctx, f.ID, domain.StageImplement, state.PersonActor("Ana")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(ctx, f.ID, domain.StagePlan, state.PersonActor("Ana")); err != nil {
		t.Fatal(err)
	}
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.loadRows }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	c, _ := b.Card(ctx, "FD-001")
	stale := c.Decision
	t.Logf("page read %s", stale.Ref)
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { m.raiseAttention(f.ID, attnGate, "plan is ready"); return nil }); err != nil {
		t.Fatal(err)
	}
	c, _ = b.Card(ctx, "FD-001")
	t.Logf("now %s", c.Decision.Ref)
	_, err := b.Answer(ctx, "FD-001", webapi.AnswerRequest{Ref: stale.Ref, Option: "run", Against: stale.Against.Token}, "Simon")
	we, _ := IsWebError(err)
	t.Logf("answer against the idle stop: %+v", we)
	if we != nil && we.Reason == webapi.ConflictAnswered {
		t.Errorf("reported answered (by %q: %q) though nobody answered this stop — it moved", we.By, we.Receipt)
	}
}

// A second line sent on the same card while its chip is up does not lose
// the first line the chip was read from.
func TestASecondLineKeepsTheChipsLine(t *testing.T) {
	b, _, eng, f, _ := headlessBoard(t, agent.NewFake("noted"))
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	const first = "the flag was never in the spec"
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		r, _ := m.rowByID(f.ID)
		leave := m.enterCard(f.ID, true)
		defer leave()
		m.syncDecision(m.openDecision(r))
		m.setChip(&reentryReading{id: f.ID, line: first,
			out: reentry.Outcome{Action: reentry.Rewind, Target: domain.StagePlan, Path: []domain.Stage{domain.StagePlan}, Reason: "requirement_missing", Note: first}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res, err := b.Send(ctx, "FD-001", webapi.SendRequest{Text: "and also the settings page"}, "Simon")
	t.Logf("second send: route=%v err=%v", res.Route, err)
	time.Sleep(2 * time.Second)
	c, _ := b.Card(ctx, "FD-001")
	t.Logf("decision now: %+v", c.Decision)
	delivered := false
	if cs := eng.Consult(f.ID); cs != nil {
		for _, msg := range cs.Snapshot().Transcript {
			t.Logf("consult transcript: %q", msg.Content)
			if strings.Contains(msg.Content, first) {
				delivered = true
			}
		}
	}
	chipStill := c.Decision != nil && c.Decision.Kind == webapi.DecisionConfirm && strings.Contains(c.Decision.Ref, webHash(first))
	if !delivered && !chipStill {
		t.Errorf("the first line %q is neither delivered nor still on its chip", first)
	}
}

// A chip whose act crosses the gate (the router read "looks good, go on"
// as proceed): the TUI's enter refuses it — "that spends credits — press
// y" — and the web's "go" asks the same before the card moves.
func TestAProceedChipAsksOnTheWebAsYDoes(t *testing.T) {
	b, _, _, f, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	pending := func(m *Shell) {
		m.setChip(&reentryReading{id: f.ID, line: "looks good, go on",
			out: reentry.Outcome{Action: reentry.Advance, Target: domain.StageImplement, Note: "looks good, go on", Confirm: true, Reason: "proceed"}})
	}
	// the TUI's enter on the chip
	var tuiStage domain.Stage
	var tuiNotice string
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		pending(m)
		r, _ := m.rowByID(f.ID)
		cmd, handled := m.chipKey(r, tea.KeyPressMsg{Code: tea.KeyEnter})
		tuiNotice = m.notice.text
		_ = handled
		m.chips = nil
		return cmd
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	c, _ := b.Card(ctx, "FD-001")
	tuiStage = domain.Stage(c.Stage)
	t.Logf("TUI enter on the proceed chip: stage=%s notice=%q", tuiStage, tuiNotice)

	if err := b.Do(ctx, func(m *Shell) tea.Cmd { pending(m); return nil }); err != nil {
		t.Fatal(err)
	}
	c, _ = b.Card(ctx, "FD-001")
	t.Logf("web chip: %+v", c.Decision.Options)
	if _, err := b.Answer(ctx, "FD-001", webapi.AnswerRequest{Ref: c.Decision.Ref, Option: webOptionGo, Against: c.Decision.Against.Token}, "Simon"); err != nil {
		t.Logf("answer go: %v", err)
	}
	c, _ = b.Card(ctx, "FD-001")
	t.Logf("web go on the proceed chip: stage=%s", c.Stage)
	if tuiStage == domain.StagePlan && c.Stage != string(domain.StagePlan) {
		t.Errorf("the web's go crossed the gate (now %s) where the TUI's enter asks for y", c.Stage)
	}
}

// A handed-off goal's only answer is "land it after all" (id merge) and
// nothing on it takes words: a sentence typed into its composer is read,
// as the TUI reads it, and never lands the goal.
func TestProseOnAHandedOffGoalNeverLandsIt(t *testing.T) {
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	var m0 *Shell
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { m0 = m; return nil }); err != nil {
		t.Fatal(err)
	}
	store := m0.store
	id, _ := domain.NewID(domain.KindGoal, 2)
	g := domain.Feature{ID: id, Num: 2, Kind: domain.KindGoal, Title: "export works offline", Slug: "export-works-offline",
		Stage: domain.StagePlan, Budget: domain.Budget{Envelope: 4000}}
	if err := store.CreateFeature(ctx, &g); err != nil {
		t.Fatal(err)
	}
	dir, err := m0.wt.Create(ctx, &g)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"add", "."}, {"commit", "-q", "-m", "goal work"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	for _, to := range []domain.Stage{domain.StageImplement, domain.StageVerify, domain.StageDone} {
		if _, err := store.Transition(ctx, g.ID, to, "user"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetVerifiedAt(ctx, g.ID, time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SetHandedOffAt(ctx, g.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.loadRows }); err != nil {
		t.Fatal(err)
	}
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 2 })
	root := filepath.Dir(filepath.Dir(filepath.Dir(dir)))
	before, _ := exec.Command("git", "-C", root, "rev-parse", "main").Output()

	line := "thanks, I pushed it myself"
	comp, err := b.Composer(ctx, string(g.ID), line)
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Card(ctx, string(g.ID))
	if err != nil {
		t.Fatal(err)
	}
	if c.Decision == nil {
		t.Fatalf("no decision on the handed-off goal")
	}
	t.Logf("composer for %q: route=%s says=%q", line, comp.Route, comp.Says)
	for _, o := range c.Decision.Options {
		t.Logf("option %s %q words=%v", o.ID, o.Label, o.Words)
	}
	if comp.Route != webapi.RouteAnswer {
		t.Skip("not classified as an answer")
	}
	// composer.js submit(): route answer -> answer(): options[hi], words only if o.words
	o := c.Decision.Options[0]
	req := webapi.AnswerRequest{Ref: c.Decision.Ref, Option: o.ID, Against: c.Decision.Against.Token}
	if o.Words {
		req.Words = line
	}
	_, aerr := b.Answer(ctx, string(g.ID), req, "Simon")
	t.Logf("answer %s: %v", o.ID, aerr)
	time.Sleep(time.Second)
	after, _ := exec.Command("git", "-C", root, "rev-parse", "main").Output()
	log, _ := exec.Command("git", "-C", root, "log", "--oneline", "-3", "main").Output()
	t.Logf("main log:\n%s", log)
	if string(before) != string(after) {
		t.Errorf("a sentence typed on a handed-off goal landed it on main")
	}
}
