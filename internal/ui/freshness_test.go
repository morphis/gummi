package ui

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/webapi"
)

// waitCard polls a card's head through the bridge until ok accepts it.
func waitCard(t *testing.T, b *Bridge, id string, what string, ok func(webapi.Card) bool) webapi.Card {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		c, err := b.Card(context.Background(), id)
		if err == nil && ok(c) {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: card never settled: %+v (err %v)", what, c, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// withWorktree cuts card f's worktree, as its first stage would have.
func withWorktree(t *testing.T, b *Bridge, f domain.Feature) string {
	t.Helper()
	ctx := context.Background()
	var dir string
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		ff := f
		if _, err := m.wt.Create(ctx, &ff); err != nil {
			t.Error(err)
		}
		dir = filepath.Join(m.ws.Root, ff.WorktreePath())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A verify pass the board itself drives is headed "verify passed" on the
// page — without a restart. The Shell stamps VerifiedAt in a command it
// batches with a row load of its own, and the row the page heads the
// decision from used to be read before the stamp landed: every
// board-driven pass read "VERIFY FAILED" until the server restarted,
// while a CLI-driven one (stamped by engine.Advance, then read) read
// right. The parity fixture set VerifiedAt by hand and never saw it.
func TestABoardDrivenVerifyPassReadsPassedWithoutARestart(t *testing.T) {
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		if isVerify(opts) {
			return "All checks green.\nVERDICT: pass"
		}
		return "done"
	})
	b, _, _, f, _ := headlessBoardFor(t, ag, domain.Feature{ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageVerify})
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	withWorktree(t, b, f)
	ctx := context.Background()
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.runStage(f) }); err != nil {
		t.Fatal(err)
	}
	c := waitCard(t, b, "FD-001", "verify gate", func(c webapi.Card) bool {
		return c.Decision != nil && c.Decision.Kind == webapi.DecisionVerify
	})
	// the store is stamped by now or never: the gate is raised in the
	// same handler that dispatches the stamp
	deadline := time.Now().Add(10 * time.Second)
	for c.Decision.Word != "verify passed" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		c = waitCard(t, b, "FD-001", "verify gate", func(c webapi.Card) bool { return c.Decision != nil })
	}
	if c.Decision.Word != "verify passed" || c.Decision.Tone != "ok" {
		t.Fatalf("a board-driven verify pass is headed %q (%s); want \"verify passed\"", c.Decision.Word, c.Decision.Tone)
	}
}

// Board and card spend is the store's, however it was booked. A scribe
// pass, a consult, a stage session other than the live one — each books
// straight to the store, and neither the row snapshot nor the live
// session's own running total heard of it: the page read 60 where
// `gummi status` read 62, and 13.27 where it read 46.22.
func TestBoardSpendFollowsTheStore(t *testing.T) {
	b, log, _, f, _ := headlessBoard(t, agent.NewFake("ok"))
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	var store interface {
		AddSpend(context.Context, domain.FeatureID, float64, float64, int64, int64) error
	}
	if err := b.Do(context.Background(), func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	// booked the way a one-shot scribe pass books: straight to the row,
	// no engine event, no session
	if err := store.AddSpend(context.Background(), f.ID, 2.5, 0, 10, 20); err != nil {
		t.Fatal(err)
	}
	bd := waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 && bd.Rows[0].Spend == 2.5 })
	if bd.Rows[0].Spend != 2.5 {
		t.Fatalf("row spend = %v", bd.Rows[0].Spend)
	}
	c, err := b.Card(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if c.Spend != 2.5 {
		t.Errorf("card spend = %v, want the store's 2.5", c.Spend)
	}
	log.waitFor(t, "card", func(c webapi.Change) bool { return c.Kind == webapi.ChangeCard && c.ID == "FD-001" })
}

// The live session's own running total is a mirror seeded when it
// spawned: it never hears of spend another pass booked on the card. The
// row's store figure, once it is fresher, wins — and the mirror is never
// added to it, so nothing is counted twice.
func TestLiveSpendNeverUndercutsTheStore(t *testing.T) {
	b, _, eng, f, _ := headlessBoard(t, agent.NewFake("ok"))
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	ctx := context.Background()
	if _, err := eng.Attach(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := eng.Send(ctx, f.ID, "hello"); err != nil {
		t.Fatal(err)
	}
	// the fake books a credit a turn, on the session and on the row; wait
	// for the turns to settle and take what the board says
	var before float64
	for i := 0; ; i++ {
		bd := waitBoard(t, b, func(bd webapi.Board) bool { return bd.Rows[0].Spend > 0 && bd.Rows[0].Status != webapi.StatusRunning })
		if bd.Rows[0].Spend == before {
			break
		}
		before = bd.Rows[0].Spend
		time.Sleep(300 * time.Millisecond)
	}
	var store interface {
		AddSpend(context.Context, domain.FeatureID, float64, float64, int64, int64) error
	}
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { store = m.store; return nil }); err != nil {
		t.Fatal(err)
	}
	// a pass beside the session books ten more
	if err := store.AddSpend(ctx, f.ID, 10, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	bd := waitBoard(t, b, func(bd webapi.Board) bool { return bd.Rows[0].Spend != before })
	if bd.Rows[0].Spend != before+10 {
		t.Fatalf("row spend = %v, want the store's %v (not the session's %v, not both added)", bd.Rows[0].Spend, before+10, before)
	}
}

// A load that started before a write and returned after a load that
// started after it is dropped: whichever returned last used to win.
func TestAnOlderRowLoadNeverReplacesANewerOne(t *testing.T) {
	m := populatedShell(160, 50)
	newer := append([]featureRow(nil), m.rows...)
	newer[0].F.Title = "newer"
	older := append([]featureRow(nil), m.rows...)
	older[0].F.Title = "older"
	m.update(rowsMsg{rows: newer, seq: 2})
	m.update(rowsMsg{rows: older, seq: 1})
	if got := m.rows[0].F.Title; got != "newer" {
		t.Fatalf("title = %q: an older load replaced a newer one", got)
	}
}

// A card that left the board (deleted) is reported gone, not moved: a
// page that has it open has nothing to refetch, and every refetch of it
// would be a 404 the browser logs as a failed load.
func TestADeletedCardIsReportedGone(t *testing.T) {
	m := populatedShell(160, 50)
	var got []webapi.Change
	m.SetChangeHook(func(c webapi.Change) { got = append(got, c) })
	all := append([]featureRow(nil), m.rows...)
	m.Update(rowsMsg{rows: all})
	left := all[0].F.ID
	got = nil
	m.Update(rowsMsg{rows: append([]featureRow(nil), all[1:]...)})
	var gone, moved bool
	for _, c := range got {
		if c.Kind == webapi.ChangeCard && c.ID == string(left) {
			if c.Gone {
				gone = true
			} else {
				moved = true
			}
		}
	}
	if !gone || moved {
		t.Fatalf("changes for the deleted %s = %+v, want one gone card change and no refetch", left, got)
	}
}

// A re-entry chip raised after its request stopped waiting reaches every
// open page. With a real model the read takes longer than the request
// follows it; the chip was raised on a detached message nothing mapped
// to a change, and the page that sent the line saw nothing until a
// reload.
func TestAChipRaisedAfterTheRequestReturnedIsPushed(t *testing.T) {
	var release sync.Once
	gate := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		reply := "ok"
		if strings.Contains(msg, "INTENT: <one of the words above>") {
			<-gate // the scribe answers only after the request returned
			reply = "INTENT: requirement_missing"
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: reply}, {Kind: agent.EventIdle}}
	}}
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	b, log, _, f, _ := headlessBoardFor(t, ag, domain.Feature{ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageVerify})
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	withWorktree(t, b, f)
	ctx := context.Background()
	_, err := b.intent(ctx, f.ID, webInput{}, 20*time.Millisecond, func(m *Shell, r featureRow) (tea.Cmd, error) {
		return m.routeReentry(r, "bounce", "the persistence step was never in the spec"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := b.Card(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if c.Decision != nil && c.Decision.Kind == webapi.DecisionConfirm {
		t.Fatal("precondition: the chip is up before the scribe answered")
	}
	log.mu.Lock()
	mark := len(log.all)
	log.mu.Unlock()
	release.Do(func() { close(gate) })
	waitCard(t, b, "FD-001", "chip", func(c webapi.Card) bool {
		return c.Decision != nil && c.Decision.Kind == webapi.DecisionConfirm
	})
	// a card change reported after the chip went up: the page refetches
	// the head on it and draws the chip
	deadline := time.Now().Add(5 * time.Second)
	for {
		log.mu.Lock()
		var seen bool
		for _, c := range log.all[mark:] {
			seen = seen || (c.Kind == webapi.ChangeCard && c.ID == "FD-001")
		}
		log.mu.Unlock()
		if seen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the chip went up and no page was told the card changed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A commit made in a card's worktree by something else — another
// process, a person at a shell — reaches the open page: the store never
// hears of it, so only a watch of the branch head can.
func TestAWorktreeCommitElsewhereReachesTheOpenCard(t *testing.T) {
	b, log, _, f, _ := headlessBoard(t, agent.NewFake("ok"))
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	ctx := context.Background()
	dir := withWorktree(t, b, f)
	if _, err := b.Card(ctx, "FD-001"); err != nil { // the page opens it
		t.Fatal(err)
	}
	// the first probe records where it stands
	time.Sleep(2*foreignInterval + 200*time.Millisecond)
	log.mu.Lock()
	mark := len(log.all)
	log.mu.Unlock()
	if err := os.WriteFile(filepath.Join(dir, "elsewhere.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "elsewhere"}} {
		if out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	waitChangeAfter(t, log, mark, webapi.ChangeCard, "FD-001", "a commit in the open card's worktree")
}

// A spec gummi itself rewrites — check discovery writing the gummi-checks
// block, a scribe's estimate — reaches the open page, so its spec tab
// reloads onto the new revision instead of keeping the one without the
// checks until a reload. The same watch is what moves an open goal's
// "N of M done-when met" once its verify writes the results.
func TestASpecRewrittenUnderAnOpenCardReachesItsPage(t *testing.T) {
	b, log, _, f, _ := headlessBoard(t, agent.NewFake("ok"))
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	ctx := context.Background()
	var path string
	if err := b.Do(ctx, func(m *Shell) tea.Cmd {
		ff := f
		p, _ := ff.ArtifactFile(m.wt.Root())
		path = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("# Dark mode\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Card(ctx, "FD-001"); err != nil { // the page opens it
		t.Fatal(err)
	}
	time.Sleep(2*foreignInterval + 200*time.Millisecond)
	log.mu.Lock()
	mark := len(log.all)
	log.mu.Unlock()
	if err := os.WriteFile(path, []byte("# Dark mode\n\n```gummi-checks\nbuild: go build ./...\n```\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitChangeAfter(t, log, mark, webapi.ChangeCard, "FD-001", "a spec rewritten under the open card")
}

// waitChangeAfter waits for a change of kind about id reported after the
// mark'th one.
func waitChangeAfter(t *testing.T, log *changeLog, mark int, kind webapi.ChangeKind, id, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		log.mu.Lock()
		var seen bool
		for _, c := range log.all[mark:] {
			seen = seen || (c.Kind == kind && c.ID == id)
		}
		log.mu.Unlock()
		if seen {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s never reached its page (no %s change for %s)", what, kind, id)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Two autopilot cards started together both run: nothing holds the second
// back, and the header counts both as running.
func TestTwoAutopilotCardsBothRun(t *testing.T) {
	release := make(chan struct{})
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		<-release
		return []agent.Event{{Kind: agent.EventIdle}}
	}}
	card := func(n int, slug string) domain.Feature {
		return domain.Feature{ID: domain.FeatureID(fmt.Sprintf("FD-%03d", n)), Num: n, Title: slug, Slug: slug,
			Stage: domain.StageImplement, GateApproval: domain.GateAutopilot}
	}
	feats := []domain.Feature{card(1, "one"), card(2, "two")}
	b, _, eng, _ := headlessBoardWith(t, ag, feats, func(*engine.Config) {})
	t.Cleanup(func() { close(release) })
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 2 })
	for _, f := range feats {
		withWorktree(t, b, f)
		if err := eng.Run(f); err != nil {
			t.Fatal(err)
		}
	}
	bd := waitBoard(t, b, func(bd webapi.Board) bool { return bd.Counts.Running == 2 })
	for _, r := range bd.Rows {
		if r.Running == nil || r.Running.Verb == "queued" {
			t.Errorf("%s = %+v, want a running card", r.ID, r.Running)
		}
	}
}

// What the status bar says once a slow read comes back — after the request
// that sent the line has answered — reaches the open pages as a toast: the
// request's own outcome can no longer carry it.
func TestANoticeFromADetachedFlowIsToasted(t *testing.T) {
	gate := make(chan struct{})
	var release sync.Once
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "INTENT: <one of the words above>") {
			<-gate
			return []agent.Event{{Kind: agent.EventError, Err: errors.New("model unavailable")}}
		}
		return []agent.Event{{Kind: agent.EventMessage, Text: "ok"}, {Kind: agent.EventIdle}}
	}}
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	b, log, _, f, _ := headlessBoardFor(t, ag, domain.Feature{ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageVerify})
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	withWorktree(t, b, f)
	ctx := context.Background()
	if _, err := b.intent(ctx, f.ID, webInput{}, 20*time.Millisecond, func(m *Shell, r featureRow) (tea.Cmd, error) {
		return m.routeReentry(r, "bounce", "the persistence step was never in the spec"), nil
	}); err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	mark := len(log.all)
	log.mu.Unlock()
	release.Do(func() { close(gate) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		log.mu.Lock()
		var said string
		for _, c := range log.all[mark:] {
			if c.Kind == webapi.ChangeToast && strings.Contains(c.Text, "could not read the card") {
				said = c.Text
			}
		}
		log.mu.Unlock()
		if said != "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the read failed after the request returned, and no page was told: %+v", log.all[mark:])
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A passed verify the person stops at ("stop here", a park) is still a
// passed verify: the pause took the session's done state, which the
// verdict was read from, and the page headed the card "verify failed"
// over "land on main" while `gummi status` said verified.
func TestAPassedVerifyStoppedByHandStillReadsPassed(t *testing.T) {
	ag := verdictAgent(func(opts agent.SessionOpts) string {
		if isVerify(opts) {
			return "All checks green.\nVERDICT: pass"
		}
		return "done"
	})
	b, _, _, f, _ := headlessBoardFor(t, ag, domain.Feature{ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode", Stage: domain.StageVerify})
	waitBoard(t, b, func(bd webapi.Board) bool { return len(bd.Rows) == 1 })
	withWorktree(t, b, f)
	ctx := context.Background()
	if err := b.Do(ctx, func(m *Shell) tea.Cmd { return m.runStage(f) }); err != nil {
		t.Fatal(err)
	}
	c := waitCard(t, b, "FD-001", "verify passed", func(c webapi.Card) bool {
		return c.Decision != nil && c.Decision.Word == "verify passed"
	})
	// the gate settles a beat after it is raised (the stamp, the diff):
	// an answer against a token that moved is read again and resent
	for try := 0; ; try++ {
		_, err := b.Answer(ctx, "FD-001", webapi.AnswerRequest{Ref: c.Decision.Ref, Option: "pause", Against: c.Decision.Against.Token}, "Simon")
		if err == nil {
			break
		}
		if try == 50 || !strings.Contains(err.Error(), "moved") {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
		if c, err = b.Card(ctx, "FD-001"); err != nil || c.Decision == nil {
			t.Fatalf("re-reading the gate: %v", err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for paused := false; !paused; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the stop never paused the session")
		}
		_ = b.Do(ctx, func(m *Shell) tea.Cmd {
			s := m.sessionFor(f.ID)
			paused = s != nil && s.State() == engine.StatePaused
			return nil
		})
	}
	c, err := b.Card(ctx, "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	if c.Decision == nil || c.Decision.Word != "verify passed" || c.Decision.Tone != "ok" {
		t.Fatalf("a passed verify stopped by hand reads %+v; want \"verify passed\"", c.Decision)
	}
	if !strings.HasPrefix(c.Decision.Question, "verification passed") {
		t.Errorf("its question reads %q", c.Decision.Question)
	}
}
