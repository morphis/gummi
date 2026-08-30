package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
)

// foreignRow is a card another process is driving, with the pid of a
// process that is certainly alive (this test binary's own).
func foreignRow(id domain.FeatureID, stage domain.Stage) featureRow {
	return featureRow{
		F:            domain.Feature{ID: id, Kind: domain.KindFeature, Stage: stage},
		DrivenAbroad: true,
		Foreign:      state.ForeignDrive{PID: 4242, Stage: string(stage), Agent: "copilot"},
	}
}

// A card driven by another process offers only what a watcher can do:
// reading its artifacts and following its stream. Every action that would
// write to the card is withheld while the drive lasts.
func TestCardActionsWithholdWritesOnForeignDrive(t *testing.T) {
	in := nextInput{stage: domain.StageImplement, kind: domain.KindFeature}
	r := foreignRow("FD-200", domain.StageImplement)
	r.HasWorktree = true

	for _, a := range cardActionsFor(in, r) {
		if a.id == expandID {
			continue
		}
		if !foreignSafeActions[a.id] {
			t.Errorf("action %q is offered on a card driven by another process", a.id)
		}
	}
}

// enter on such a card is relabeled: it watches the other process's run
// rather than starting one here, and says who owns it.
func TestCardActionsWatchLabel(t *testing.T) {
	in := nextInput{stage: domain.StageImplement, kind: domain.KindFeature}
	acts := cardActionsFor(in, foreignRow("FD-201", domain.StageImplement))

	var run *cardAction
	for i := range acts {
		if acts[i].id == "run" {
			run = &acts[i]
		}
	}
	if run == nil {
		t.Fatal("no run action on a card driven elsewhere; watching must stay reachable")
	}
	if run.label != "watch" {
		t.Errorf("run label = %q, want %q", run.label, "watch")
	}
	if !strings.Contains(run.why, "4242") {
		t.Errorf("why = %q, want it to name the owning pid", run.why)
	}
}

// A stage that could never be run locally (done) can still be watched
// while another process drives it — what enter opens is that run's
// stream, not this board's session.
func TestCardActionsWatchAtAnyStage(t *testing.T) {
	in := nextInput{stage: domain.StageDone, kind: domain.KindFeature}
	acts := cardActionsFor(in, foreignRow("FD-202", domain.StageDone))
	found := false
	for _, a := range acts {
		if a.id == "run" {
			found = true
		}
	}
	if !found {
		t.Error("a done card driven elsewhere offers no way to watch it")
	}
}

// The action list and the key handler must agree: every action the list
// withholds on a foreign drive has its accelerator refused too, or the
// board would answer a key it does not advertise.
func TestForeignBlockedKeysMatchWithheldActions(t *testing.T) {
	in := nextInput{stage: domain.StageImplement, kind: domain.KindFeature}
	local := featureRow{F: domain.Feature{ID: "FD-203", Kind: domain.KindFeature, Stage: domain.StageImplement}, HasWorktree: true}

	for _, a := range cardActionsFor(in, local) {
		if a.id == expandID || a.key == "" {
			continue
		}
		withheld := !foreignSafeActions[a.id]
		if withheld && !foreignBlockedKeys[a.key] {
			t.Errorf("action %q (key %q) is withheld on a foreign drive but its key is still answered", a.id, a.key)
		}
		if !withheld && foreignBlockedKeys[a.key] {
			t.Errorf("action %q (key %q) is offered on a foreign drive but its key is refused", a.id, a.key)
		}
	}
}

// The board badges a card driven elsewhere, so an actively-changing card
// never renders as an idle one.
func TestBoardRowBadgesForeignDrive(t *testing.T) {
	th, _ := theme.ByName("dark")
	m := NewShell(th, "test")
	row := foreignRow("FD-204", domain.StageImplement)
	row.F.Title = "a card someone else is driving"

	line := ansi.Strip(m.cardLine(row, 0, false, false, 100))
	if !strings.Contains(line, "elsewhere") {
		t.Errorf("row = %q, want a badge naming the foreign drive", line)
	}
}

// A watched card renders the followed transcript in its thread and stays
// read-only: the input is withheld, so nothing on the page can send a
// turn or answer the question the run's owner will answer.
func TestWatchRendersInThread(t *testing.T) {
	dir := t.TempDir()
	ws := state.Workspace{Root: dir, RepoRoot: dir}
	f := domain.Feature{ID: "FD-205", Stage: domain.StageImplement, Title: "watched"}

	w, err := livelog.Create(ws.LiveFile(f.ID), livelog.Record{
		Feature: string(f.ID), Stage: string(f.Stage), Role: "implementer", PID: 4242,
	})
	if err != nil {
		t.Fatalf("create live file: %v", err)
	}
	w.Emit(livelog.Record{Kind: livelog.KindMessage, Text: "working on it"})
	w.Close()

	th, _ := theme.ByName("dark")
	m := NewShell(th, "test")
	m.ws = ws
	m.rows = []featureRow{foreignRow("FD-205", domain.StageImplement)}
	m.sel = 0
	cmd := m.startFollow(f)
	if cmd == nil {
		t.Fatal("startFollow returned no pump command")
	}
	defer m.stopFollow()
	if m.follow == nil || m.follow.feature != f.ID {
		t.Fatal("startFollow did not install a follow source")
	}

	// feed the records the tail would deliver.
	m.follow.fl.Apply(livelog.Record{Kind: livelog.KindSession, Feature: string(f.ID), Stage: string(f.Stage), PID: 4242})
	m.follow.pid = 4242
	m.follow.fl.Apply(livelog.Record{Kind: livelog.KindMessage, Text: "working on it"})

	view := ansi.Strip(m.threadView(80, 24))
	if !strings.Contains(view, "working on it") {
		t.Errorf("thread = %q, want the followed transcript", view)
	}
	if !strings.Contains(view, "watching") {
		t.Errorf("thread = %q, want the watching marker", view)
	}
	if !strings.Contains(view, "read-only") {
		t.Errorf("thread = %q, want the read-only footer", view)
	}
	if strings.Contains(view, "message the agent") {
		t.Errorf("thread = %q, must not render the live composer on a watched card", view)
	}
}

// Closing the watched card's page cancels its tail, so a session that
// opens many watches does not leak a poller per card.
func TestStopFollowCancelsTail(t *testing.T) {
	dir := t.TempDir()
	ws := state.Workspace{Root: dir, RepoRoot: dir}
	f := domain.Feature{ID: "FD-206", Stage: domain.StageImplement}
	w, err := livelog.Create(ws.LiveFile(f.ID), livelog.Record{Feature: string(f.ID)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer w.Close()

	th, _ := theme.ByName("dark")
	m := NewShell(th, "test")
	m.ws = ws
	m.startFollow(f)
	src := m.follow
	m.stopFollow()
	if m.follow != nil {
		t.Fatal("stopFollow left a follow installed")
	}
	// the follower's context is canceled: its tail goroutine returns and
	// the channel closes. Re-canceling is a no-op, so the only observable
	// is that a second close does not panic.
	src.cancel()
}

// applyForeign reports a change when a drive appears or ends, which is
// what triggers the board's row reload.
func TestApplyForeignReportsChange(t *testing.T) {
	m := &Shell{rows: []featureRow{
		{F: domain.Feature{ID: "FD-207"}},
		{F: domain.Feature{ID: "FD-208"}},
	}}
	drives := map[domain.FeatureID]state.ForeignDrive{"FD-207": {PID: 99}}
	if !m.applyForeign(drives) {
		t.Fatal("a newly appeared drive did not report a change")
	}
	if !m.rows[0].DrivenAbroad || m.rows[1].DrivenAbroad {
		t.Fatalf("drives applied to the wrong rows: %v / %v", m.rows[0].DrivenAbroad, m.rows[1].DrivenAbroad)
	}
	if m.applyForeign(drives) {
		t.Error("an unchanged probe reported a change; the board would reload every tick")
	}
	if !m.applyForeign(map[domain.FeatureID]state.ForeignDrive{}) {
		t.Error("a drive that ended did not report a change")
	}
}

// ForeignDriver ignores a live file this very process owns: a board
// driving its own card is not "driven elsewhere".
func TestForeignDriverIgnoresOwnPID(t *testing.T) {
	dir := t.TempDir()
	ws := state.Workspace{Root: dir, RepoRoot: dir}
	w, err := livelog.Create(ws.LiveFile("FD-209"), livelog.Record{Feature: "FD-209"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer w.Close()
	if _, ok := state.ForeignDriver(ws, "FD-209"); ok {
		t.Fatal("this process's own live session reads as a foreign drive")
	}
}

// A follower over a live file that never appeared renders an empty
// transcript rather than failing: the run may not have started yet.
func TestFollowerEmptyBeforeRecords(t *testing.T) {
	fl := engine.NewFollower(domain.Feature{ID: "FD-210", Title: "not started"})
	snap := fl.Snapshot()
	if len(snap.Transcript) != 0 {
		t.Fatalf("transcript = %d messages, want none", len(snap.Transcript))
	}
	if snap.Feature.Title != "not started" {
		t.Errorf("title = %q, want the seeded card's", snap.Feature.Title)
	}
}

// A board git verb on a card another gummi process holds is refused
// before it touches the worktree, and says what the board can offer
// instead.
func TestCardLockedRefusesForeignHeldCard(t *testing.T) {
	dir := t.TempDir()
	ws := state.Workspace{Root: dir, RepoRoot: dir}
	m := &Shell{locks: state.NewCardLocks(ws)}

	foreign, err := state.AcquireLock(ws.CardLockFile("FD-300"))
	if err != nil {
		t.Fatal(err)
	}
	defer foreign()

	ran := false
	msg := m.cardLocked("FD-300", func() tea.Msg {
		ran = true
		return noticeMsg{text: "did the work"}
	})()
	if ran {
		t.Fatal("the verb ran against a card another process is driving")
	}
	n, ok := msg.(noticeMsg)
	if !ok || !n.isErr {
		t.Fatalf("msg = %#v, want an error notice", msg)
	}
	if !strings.Contains(n.text, "FD-300") || !strings.Contains(n.text, "watch") {
		t.Errorf("notice = %q, want it to name the card and offer watching", n.text)
	}
}

// The board's verbs and its engine share one registry, so a merge on a
// card the board is already driving joins that hold instead of refusing
// itself — the deadlock a naive per-verb flock would cause.
func TestCardLockedJoinsThisProcessHold(t *testing.T) {
	dir := t.TempDir()
	ws := state.Workspace{Root: dir, RepoRoot: dir}
	locks := state.NewCardLocks(ws)
	m := &Shell{locks: locks}

	// stand in for the engine's session hold on the same card.
	held, err := locks.Acquire("FD-301")
	if err != nil {
		t.Fatal(err)
	}
	defer held()

	ran := false
	m.cardLocked("FD-301", func() tea.Msg {
		ran = true
		return nil
	})()
	if !ran {
		t.Fatal("a verb was refused on a card this very process holds")
	}
	if !locks.Holds("FD-301") {
		t.Error("the verb's release dropped the session's hold")
	}
}

// With no registry wired (a static test scaffold) the verbs run
// unlocked, exactly as they did before locking existed.
func TestCardLockedWithoutRegistry(t *testing.T) {
	m := &Shell{}
	ran := false
	m.cardLocked("FD-302", func() tea.Msg {
		ran = true
		return nil
	})()
	if !ran {
		t.Fatal("a verb was refused with no lock registry wired")
	}
}
