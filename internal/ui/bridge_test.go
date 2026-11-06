package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
	"github.com/morphis/gummi/internal/worktree"
)

// changeLog collects what a Shell reports through its change hook.
type changeLog struct {
	mu  sync.Mutex
	all []webapi.Change
	ch  chan struct{}
}

func newChangeLog() *changeLog { return &changeLog{ch: make(chan struct{}, 1)} }

func (l *changeLog) add(c webapi.Change) {
	l.mu.Lock()
	l.all = append(l.all, c)
	l.mu.Unlock()
	select {
	case l.ch <- struct{}{}:
	default:
	}
}

// waitFor blocks until a change matching want has been reported.
func (l *changeLog) waitFor(t *testing.T, what string, want func(webapi.Change) bool) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		l.mu.Lock()
		for _, c := range l.all {
			if want(c) {
				l.mu.Unlock()
				return
			}
		}
		l.mu.Unlock()
		select {
		case <-l.ch:
		case <-deadline:
			t.Fatalf("no %s change reported; saw %+v", what, l.all)
		}
	}
}

// headlessBoard builds an attached Shell with a fake-agent engine and one
// card at plan, and runs it under a Bridge. out receives anything the
// program writes to its output.
func headlessBoard(t *testing.T, ag agent.Agent) (*Bridge, *changeLog, *engine.Engine, domain.Feature, *bytes.Buffer) {
	t.Helper()
	return headlessBoardFor(t, ag, domain.Feature{ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StagePlan})
}

// headlessBoardFor is headlessBoard with the one card given.
func headlessBoardFor(t *testing.T, ag agent.Agent, f domain.Feature) (*Bridge, *changeLog, *engine.Engine, domain.Feature, *bytes.Buffer) {
	t.Helper()
	b, log, eng, out := headlessBoardWith(t, ag, []domain.Feature{f}, nil)
	return b, log, eng, f, out
}

// headlessBoardWith is headlessBoard with the cards given, and the
// engine's config tuned by tune (nil leaves it).
func headlessBoardWith(t *testing.T, ag agent.Agent, feats []domain.Feature, tune func(*engine.Config)) (*Bridge, *changeLog, *engine.Engine, *bytes.Buffer) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(a ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git", append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
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
	cfg := engine.Config{Agents: singleAgent(ag), Store: store, Pool: pool, Workspace: ws, Model: "fake-model", Persist: true, Workflows: true}
	if tune != nil {
		tune(&cfg)
	}
	eng := engine.New(cfg)
	t.Cleanup(func() { eng.Close() })

	for i := range feats {
		if err := store.CreateFeature(context.Background(), &feats[i]); err != nil {
			t.Fatal(err)
		}
	}

	m := NewShell(theme.GummiDark(), "v0-test")
	m.Attach(store, pool, ws)
	m.AttachEngine(eng)
	m.SetCopilotHint(false)
	m.SetMotion(false)
	log := newChangeLog()
	m.SetChangeHook(log.add)

	var out bytes.Buffer
	b := NewHeadless(m, tea.WithOutput(&out))
	go func() { _ = b.Run() }()
	t.Cleanup(b.Stop)
	return b, log, eng, &out
}

// waitBoard polls the board through the bridge until ok accepts it.
func waitBoard(t *testing.T, b *Bridge, ok func(webapi.Board) bool) webapi.Board {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var board webapi.Board
		if err := b.Do(context.Background(), func(m *Shell) tea.Cmd { board = m.WebBoard(); return nil }); err != nil {
			t.Fatal(err)
		}
		if ok(board) {
			return board
		}
		if time.Now().After(deadline) {
			t.Fatalf("board never settled: %+v", board)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A Shell under the bridge runs its Init — the row load and the engine
// listener — and reports what an engine event changed, all without
// writing a byte of output.
func TestHeadlessShellReactsToEngineEvents(t *testing.T) {
	sess := domain.Feature{
		ID: "FF-001", Num: 1, Kind: domain.KindFreeform, Title: "Dark mode", Slug: "dark-mode",
		Stage: domain.StageOpen, BranchScheme: domain.BranchSchemeKind,
	}
	b, log, eng, f, out := headlessBoardFor(t, agent.NewFake("on it"), sess)

	// Init's row load landed: a board change was reported, and the row is
	// readable through the bridge.
	log.waitFor(t, "board", func(c webapi.Change) bool { return c.Kind == webapi.ChangeBoard })
	board := waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	if board.Rows[0].ID != "FF-001" || board.Rows[0].Status != webapi.StatusIdle {
		t.Fatalf("board rows = %+v, want FF-001 idle", board.Rows)
	}

	// The engine listener is live: a turn on the card reaches the hook.
	ctx := context.Background()
	ff, err := eng.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	log.waitFor(t, "card", func(c webapi.Change) bool { return c.Kind == webapi.ChangeCard && c.ID == "FF-001" })
	log.waitFor(t, "live", func(c webapi.Change) bool { return c.Kind == webapi.ChangeLive && c.ID == "FF-001" })

	var card webapi.Card
	var ok bool
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { card, ok = m.WebCard("FF-001"); return nil }); err != nil {
		t.Fatal(err)
	}
	if !ok || card.Title != "Dark mode" || card.Branch == "" {
		t.Errorf("card = %+v (ok %v), want the head of FF-001", card, ok)
	}
	b.Stop()
	if out.Len() != 0 {
		t.Errorf("a headless board wrote %d bytes of output: %q", out.Len(), out.String())
	}
}

// Do runs inside Update, one call at a time, and returns only after its
// function ran.
func TestBridgeDoSerializesWithUpdate(t *testing.T) {
	b, _, _, _, _ := headlessBoard(t, agent.NewFake("ok"))
	ctx := context.Background()

	counter := 0 // deliberately unsynchronized: only the loop may touch it
	var wg sync.WaitGroup
	for range 200 {
		wg.Go(func() {
			if err := b.Do(ctx, func(*Shell) tea.Cmd { counter++; return nil }); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var seen int
	if err := b.Do(ctx, func(*Shell) tea.Cmd { seen = counter; return nil }); err != nil {
		t.Fatal(err)
	}
	if seen != 200 {
		t.Errorf("counter = %d after 200 Do calls, want 200", seen)
	}

	// A command returned from Do is dispatched like Update's own: its
	// message comes back through Update.
	if err := b.Do(ctx, func(*Shell) tea.Cmd {
		return func() tea.Msg { return noticeMsg{text: "from a web intent"} }
	}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var notice string
		_ = b.Do(ctx, func(m *Shell) tea.Cmd { notice = m.notice.text; return nil })
		if notice == "from a web intent" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the returned command's message never reached Update (notice %q)", notice)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A panic in fn is an error for the caller, not a dead board.
	if err := b.Do(ctx, func(*Shell) tea.Cmd { panic("boom") }); err == nil {
		t.Error("a panicking Do reported no error")
	}
	if err := b.Do(ctx, func(*Shell) tea.Cmd { return nil }); err != nil {
		t.Errorf("the board did not survive a panicking Do: %v", err)
	}

	// A caller that has given up before the loop took the call gets its
	// context's error, and then the call never runs.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	ran := false
	err := b.Do(cancelled, func(*Shell) tea.Cmd { ran = true; return nil })
	var after bool
	_ = b.Do(ctx, func(*Shell) tea.Cmd { after = ran; return nil })
	switch {
	case errors.Is(err, context.Canceled) && after:
		t.Error("a Do that reported cancellation ran anyway")
	case err == nil && !after:
		t.Error("a Do that reported success never ran")
	case err != nil && !errors.Is(err, context.Canceled):
		t.Errorf("Do on a cancelled context = %v", err)
	}

	b.Stop()
	if err := b.Do(ctx, func(*Shell) tea.Cmd { return nil }); !errors.Is(err, ErrBridgeStopped) {
		t.Errorf("Do after Stop = %v, want ErrBridgeStopped", err)
	}
}

// A needs-you item reaches the hook when it is raised and cleared, from
// whichever goroutine raised it, and the board row groups under needs.
func TestInboxChangesReachTheHookAndTheRow(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{
		{F: domain.Feature{ID: "FD-001", Title: "a", Stage: domain.StagePlan}},
		{F: domain.Feature{ID: "FD-002", Title: "b", Stage: domain.StageTodo}},
		{F: domain.Feature{ID: "FD-003", Title: "c", Stage: domain.StageDone}},
	}
	log := newChangeLog()
	m.SetChangeHook(log.add)

	m.inbox.add("FD-001", attnQuestion, "Persist where?")
	log.waitFor(t, "card", func(c webapi.Change) bool { return c.Kind == webapi.ChangeCard && c.ID == "FD-001" })

	b := m.WebBoard()
	if b.Counts.Needs != 1 || b.Rows[0].Status != webapi.StatusNeeds ||
		b.Rows[0].Needs == nil || b.Rows[0].Needs.Question != "Persist where?" || b.Rows[0].Needs.Color != "info" {
		t.Errorf("row = %+v needs %+v, want an open question", b.Rows[0], b.Rows[0].Needs)
	}
	if b.Rows[1].Status != webapi.StatusTodo || b.Rows[2].Status != webapi.StatusDone {
		t.Errorf("statuses = %s, %s; want todo, done", b.Rows[1].Status, b.Rows[2].Status)
	}

	n := len(log.all)
	m.inbox.remove("FD-001")
	if len(log.all) == n {
		t.Error("clearing a needs-you item reported nothing")
	}
	if got := m.WebBoard().Rows[0].Status; got != webapi.StatusIdle {
		t.Errorf("status after clearing = %s, want idle", got)
	}
}

// Messages that change nothing a page shows report nothing: a hook fired
// on every tick would have each open page refetching the board.
func TestQuietMessagesReportNoChange(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	log := newChangeLog()
	m.SetChangeHook(log.add)
	for _, msg := range []tea.Msg{spinnerTickMsg{}, tea.WindowSizeMsg{Width: 80, Height: 24}, foreignMsg{}} {
		m.Update(msg)
	}
	m.Update(noticeMsg{text: "hello", id: "FD-9"})
	if len(log.all) != 1 || log.all[0].Kind != webapi.ChangeToast || log.all[0].Text != "hello" || log.all[0].ID != "FD-9" {
		t.Errorf("changes = %+v, want exactly the toast", log.all)
	}
}
