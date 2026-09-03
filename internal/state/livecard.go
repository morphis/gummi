package state

import (
	"os"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/livelog"
)

// ForeignDrive describes a live session on a card owned by a *different*
// gummi process — a headless run/resume while the board is open, or a
// board while a run drives another card. It is what a surface needs to
// say "someone else is driving this" honestly: who, since when, and at
// which stage.
type ForeignDrive struct {
	// PID owns the session and was alive at the moment it was read.
	PID int
	// Stage/Role/Agent/Model come from the live file's header — the
	// driving process's view, which is fresher than the store's.
	Stage string
	Role  string
	Agent string
	Model string
	// Since is when that session started.
	Since time.Time
	// Updated is the live file's last write: a run that has gone quiet
	// shows here before it shows anywhere else.
	Updated time.Time
	// Busy mirrors the live file's own busy signal — true iff the last
	// busy record seen in the tail scan was true and no stopped record
	// followed it. Only meaningful here at all, since a stopped drive
	// never reaches this struct (ForeignDriver reports ok=false for it).
	Busy bool
}

// stopGrace is how long a file's terminal record still counts as "mid
// advance" rather than "done". A stage advance stops the old session —
// writing that terminal record — and only afterward creates the
// successor's file (Engine.startAutonomous's replace path: old.stop()
// then bindLiveLog), so the file reads Stopped for that gap even though
// the owning process is about to keep driving the card. Past this
// window, a still-alive pid on a stopped file just means that pid is
// doing something else now (or was recycled) — Updated stops advancing
// the moment nothing more replaces the file, so recency is what tells
// the two cases apart.
const stopGrace = 5 * time.Second

// ForeignDriver reports the live session another process is running on
// card id, if there is one. It is a cheap read of the card's live-file
// header plus a kill -0 on the pid it names, so a board can call it per
// row per refresh.
//
// It reports nothing for three cases that all mean "not being driven from
// outside": no live file, a session this very process owns (a board
// driving its own card), and a file whose owner has exited or whose
// session ended more than stopGrace ago. A stale file left by a crash
// therefore reads as not-driven soon after anyone checks — the same "the
// next read reads it as dead" property the pid files rely on.
func ForeignDriver(ws Workspace, id domain.FeatureID) (ForeignDrive, bool) {
	st, ok := liveSession(ws, id)
	if !ok || st.PID == os.Getpid() {
		return ForeignDrive{}, false
	}
	return ForeignDrive{
		PID: st.PID, Stage: st.Stage, Role: st.Role,
		Agent: st.Agent, Model: st.Model,
		Since: st.Started, Updated: st.Updated,
		Busy: st.Busy,
	}, true
}

// CardIsLive reports whether any process — this one or another's — is
// driving card id right now. It is ForeignDriver's liveness check without
// the self-exclusion: a board running its own in-process autopilot on a
// card is trivially "being driven right now" for that check's purposes,
// where ForeignDriver correctly says "not from outside".
//
// This is what a stretch (stretch.go) still open per the event log needs
// to tell an autopilot period genuinely in progress from one whose
// driving process died without writing anything on its way out — a
// question the log alone can never answer, since a killed process leaves
// no row behind.
func CardIsLive(ws Workspace, id domain.FeatureID) bool {
	_, ok := liveSession(ws, id)
	return ok
}

// liveSession reads card id's live-file header and reports it plus
// ok=true iff the session it names is still alive right now: a live file
// exists, its pid answers a kill -0, and it has not been stopped for
// longer than stopGrace. ForeignDriver and CardIsLive share this and
// differ only in whether a session this process itself owns counts.
func liveSession(ws Workspace, id domain.FeatureID) (livelog.Status, bool) {
	st, err := livelog.Stat(ws.LiveFile(id))
	if err != nil {
		return livelog.Status{}, false
	}
	if st.PID == 0 || !ProcessAlive(st.PID) {
		return livelog.Status{}, false
	}
	if st.Stopped && time.Since(st.Updated) > stopGrace {
		return livelog.Status{}, false
	}
	return st, true
}
