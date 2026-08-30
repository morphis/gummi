package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/livelog"
	"github.com/morphis/gummi/internal/state"
)

// This file is the board's read side of a run it does not own. A card
// driven by another gummi process (a headless run/resume, another board)
// streams its transcript to a live file; here that file is tailed, folded
// into an engine.Follower, and rendered by the card thread's live stage
// block (thread.go) the same way a local session is. Nothing here can
// write: no turn, no answer, no gate.

// followSource is a live tail of another process's session, bound to the
// card page that renders it.
type followSource struct {
	feature domain.FeatureID
	fl      *engine.Follower
	cancel  context.CancelFunc
	// pid owns the run, from the live file's header. Zero until the
	// header arrives.
	pid int
}

// Snapshot renders the followed session.
func (f *followSource) Snapshot() engine.Snapshot { return f.fl.Snapshot() }

// marker is the tag that keeps the thread honest about what it is:
// someone else's run, watched.
func (f *followSource) marker() string {
	if f.pid == 0 {
		return "watching"
	}
	return fmt.Sprintf("watching · pid %d", f.pid)
}

// footer says why there is nothing to send a turn to and what the run is
// waiting on.
func (f *followSource) footer(snap engine.Snapshot) string {
	var line string
	switch {
	case f.fl.Stopped():
		line = "the session ended — this is the final transcript"
	case snap.PendingAsk != nil:
		line = fmt.Sprintf("the agent is asking a question; answer it where the run is owned (pid %d)", f.pid)
	default:
		line = "read-only: another gummi process owns this run"
	}
	// the writer drops records rather than stalling the run it mirrors;
	// say so, so a gap in the transcript is never read as the whole story.
	if n := f.fl.Dropped(); n > 0 {
		line += fmt.Sprintf("  ·  %d record(s) dropped by a busy writer", n)
	}
	return line
}

// followRecordMsg delivers one record from a followed card's live file.
// It carries the feature so a record from a pane the user has since
// closed is discarded rather than folded into the wrong session.
type followRecordMsg struct {
	feature domain.FeatureID
	rec     livelog.Record
	ch      <-chan livelog.Record
}

// followClosedMsg reports that a followed stream ended (its context was
// canceled, or the pane closed).
type followClosedMsg struct{ feature domain.FeatureID }

// startFollow tails the live file of a card another process is driving
// into Shell.follow; the card thread's live stage block renders it
// read-only. The command it returns pumps the records in, and it stops
// any tail a previous card's watch left behind.
func (m *Shell) startFollow(f domain.Feature) tea.Cmd {
	m.stopFollow()
	ctx, cancel := context.WithCancel(context.Background())
	m.follow = &followSource{feature: f.ID, fl: engine.NewFollower(f), cancel: cancel}
	ch := livelog.Follow(ctx, m.ws.LiveFile(f.ID), 0)
	return waitFollow(f.ID, ch)
}

// stopFollow cancels an open tail. Every path that closes or moves off
// the watched card goes through here: an uncanceled follow would keep
// polling a file nobody is reading, for the rest of the session.
func (m *Shell) stopFollow() {
	if m.follow != nil {
		m.follow.cancel()
		m.follow = nil
	}
}

// waitFollow blocks on the tail for the next record. Like the engine
// listener it re-arms itself: Update hands the channel back so the pump
// continues without the page holding a goroutine of its own.
func waitFollow(id domain.FeatureID, ch <-chan livelog.Record) tea.Cmd {
	return subscription(func() tea.Msg {
		rec, ok := <-ch
		if !ok {
			return followClosedMsg{feature: id}
		}
		return followRecordMsg{feature: id, rec: rec, ch: ch}
	})
}

// applyFollow folds one streamed record into the open follow tail and
// re-arms it. A record for a card the page no longer shows is dropped,
// and its stream is left to close on its own canceled context.
func (m *Shell) applyFollow(msg followRecordMsg) tea.Cmd {
	if m.follow == nil || m.follow.feature != msg.feature {
		return nil
	}
	m.follow.fl.Apply(msg.rec)
	if msg.rec.Kind == livelog.KindSession {
		m.follow.pid = msg.rec.PID
	}
	// a record that arrives while the user is scrolled back must not yank
	// the view; the thread's own tail-anchoring handles the rest.
	return waitFollow(msg.feature, msg.ch)
}

// watchForeign opens the read-only view of a card another process is
// driving: the card's page, with its live stream tailed into the
// thread's live stage block. It is what enter and the transcript key
// both land on for such a card — this board cannot drive it, and
// pretending otherwise would either fight the other process or fail at
// the last moment.
func (m *Shell) watchForeign(f domain.Feature) tea.Cmd {
	var open tea.Cmd
	if !m.cardOpen {
		// the page is the view; openCard's event-log load is what puts
		// the followed stream on top of the card's own history rather
		// than on a blank body.
		open = m.openCard()
	}
	m.noteWatching(f.ID)
	return tea.Batch(open, m.startFollow(f))
}

// noteWatching says who owns the run being watched, so the read-only
// pane never reads as a broken chat.
func (m *Shell) noteWatching(id domain.FeatureID) {
	if d, ok := m.foreignFor(id); ok {
		m.notice = noticeMsg{text: fmt.Sprintf("watching %s — driven by pid %d (read-only)", id, d.PID)}
		return
	}
	m.notice = noticeMsg{text: fmt.Sprintf("watching %s (read-only)", id)}
}

// foreignSummary is the dashboard's one-line account of a card another
// process is driving: who owns it, at which stage, and how long since it
// last wrote anything.
func foreignSummary(d state.ForeignDrive) string {
	out := fmt.Sprintf("pid %d", d.PID)
	if d.Stage != "" {
		out += " · " + d.Stage
	}
	if d.Agent != "" {
		out += " · " + d.Agent
	}
	if !d.Updated.IsZero() {
		out += fmt.Sprintf(" · last spoke %s ago", compactSince(time.Since(d.Updated)))
	}
	return out + " · enter to watch"
}

// compactSince renders an age in the shortest honest unit.
func compactSince(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// foreignInterval paces the board's probe for cards driven by other
// processes. The probe is a stat plus a kill -0 per row, so it is cheap
// enough to run this often; the full row reload it can trigger is not,
// and runs at foreignReloadEvery times this interval.
const foreignInterval = 2 * time.Second

// foreignReloadEvery is how many probe ticks pass between full row
// reloads while some card is driven elsewhere. The probe keeps the badge
// live; the reload is what picks up the stage, spend, and worktree state
// the other process wrote to the store.
const foreignReloadEvery = 5

// foreignTickMsg fires the next probe.
type foreignTickMsg struct{}

// foreignMsg carries a probe's result: which cards are currently driven
// by another process.
type foreignMsg struct {
	drives map[domain.FeatureID]state.ForeignDrive
	// reload asks for a full row reload alongside applying the probe —
	// the other process has been writing to the store, and the board's
	// derived state (stage, spend, worktree) is behind.
	reload bool
}

func foreignTick() tea.Cmd {
	return subscription(tea.Tick(foreignInterval, func(time.Time) tea.Msg { return foreignTickMsg{} }))
}

// probeForeign re-reads which cards another process is driving. It runs
// as a command (off the update loop) because it touches the filesystem,
// though only with a stat and a signal probe per row.
func (m *Shell) probeForeign() tea.Msg {
	drives := map[domain.FeatureID]state.ForeignDrive{}
	for _, r := range m.rows {
		if d, ok := state.ForeignDriver(m.ws, r.F.ID); ok {
			drives[r.F.ID] = d
		}
	}
	m.foreignTicks++
	reload := len(drives) > 0 && m.foreignTicks%foreignReloadEvery == 0
	return foreignMsg{drives: drives, reload: reload}
}

// applyForeign updates the rows' driven-elsewhere state in place. It
// reports whether the set changed, which is worth a row reload on its own
// — a run that just started or just ended moved the store too.
func (m *Shell) applyForeign(drives map[domain.FeatureID]state.ForeignDrive) bool {
	changed := false
	for i := range m.rows {
		d, ok := drives[m.rows[i].F.ID]
		if ok != m.rows[i].DrivenAbroad {
			changed = true
		}
		m.rows[i].Foreign, m.rows[i].DrivenAbroad = d, ok
	}
	return changed
}

// foreignFor returns the live foreign drive on a card, if any.
func (m *Shell) foreignFor(id domain.FeatureID) (state.ForeignDrive, bool) {
	for _, r := range m.rows {
		if r.F.ID == id {
			return r.Foreign, r.DrivenAbroad
		}
	}
	return state.ForeignDrive{}, false
}
