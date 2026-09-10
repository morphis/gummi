package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
)

// The plan stage hides an automatic plan→critique→replan loop behind a
// single stage pill (reviewloop.go): the feature never leaves Plan, so
// without extra signal the UI reads as "the planner keeps re-running".
// This file names the loop's legs and renders where it currently is —
// a breadcrumb on the dashboard, a leg word on the board card, and the
// verb next to the activity spinner.

// Plan-loop legs, in flow order.
const (
	planLegWrite    = iota // architect writing (or revising) the plan
	planLegCritique        // fresh-context reviewer refuting it
	planLegApprove         // human gate at the end of the loop
)

// planLoopLeg reports which leg a Plan-stage feature is on — escalated
// marks an approve leg the loop gave up on rather than finished clean —
// or ok=false when there is no loop activity to show (nothing live, no
// gate raised).
func (m *Shell) planLoopLeg(id domain.FeatureID) (leg int, escalated, ok bool) {
	// only a scheduled/running session marks an agent leg — a finished one
	// lingers for its transcript while the gate below owns the loop state.
	if sess := m.sessionFor(id); sess != nil &&
		sess.Feature.Stage == domain.StagePlan && !sess.Interactive {
		if st := sess.State(); st == engine.StateRunning || st == engine.StateQueued {
			if sess.Critique {
				return planLegCritique, false, true
			}
			return planLegWrite, false, true
		}
	}
	// no live session: a pending gate means the loop finished (clean or
	// escalated) and the plan is waiting on the human.
	if it, ok := m.inbox.get(id); ok && it.Kind == attnGate {
		return planLegApprove, it.Escalated, true
	}
	return 0, false, false
}

// planLoopLine renders the dashboard breadcrumb for the plan loop: past
// legs checked off quietly, the live one in the stage accent, upcoming
// ones faint — plus the replan round once the critique has bounced.
func (m *Shell) planLoopLine(f domain.Feature) string {
	if f.Stage != domain.StagePlan {
		return ""
	}
	leg, escalated, ok := m.planLoopLeg(f.ID)
	if !ok {
		return ""
	}
	s := m.styles
	names := []string{"plan", "critique", "approve"}
	if m.round(f.ID, domain.RoundKindPlan) > 0 {
		names[planLegWrite] = "replan"
	}
	parts := make([]string, len(names))
	for i, n := range names {
		switch {
		case i < leg:
			parts[i] = s.Subtle.Render(n + " ✓")
		case i == leg && escalated:
			// the loop gave up (round cap or unclear verdict): the gate is
			// a needs-you, not a clean pass
			parts[i] = s.Warning.Render("● " + n)
		case i == leg:
			parts[i] = s.Stage(f.Stage).Render("● " + n)
		default:
			parts[i] = s.Faint.Render(n)
		}
	}
	line := strings.Join(parts, s.Faint.Render(" → "))
	if r := m.round(f.ID, domain.RoundKindPlan); r > 0 {
		line += s.Faint.Render("  ·  round " + itoa(r) + "/" + itoa(maxPlanRounds))
	}
	if escalated {
		line += s.Warning.Render("  ·  escalated")
	}
	return line
}

// cardBusyWord names the work behind a busy board row's spinner, only
// meaningful when cardBusy(r) is true. A running baseline takes
// priority over a scribe pass or a live session — it's a foreground
// blocking action on the card, more specific than either — a scribe
// pass in turn takes priority over a live session, and otherwise it
// reuses runningVerb, the exact word thread.go's own spinner shows for
// the same session, so a card's board-row word and its thread-detail
// word can never disagree. The elapsed clock runningLabel can append is
// deliberately left off here: the compact row has no room for it (that
// is what the thread's own detailed busy line is for), and several
// callers key off this exact word — adding a clock that ticks every
// frame would turn "the row says X" into "the row says X as of whenever
// last compared". A foreign-driven card has no local session to read a
// leg out of — its live file's header carries no plan-loop detail — so
// it always says "running", checked last since a row with a local
// session, a baseline or a scribe pass never also reaches here.
func (m *Shell) cardBusyWord(r featureRow) string {
	if m.baselining[r.F.ID] {
		return "checking"
	}
	if m.reentryRead != nil && m.reentryRead.id == r.F.ID {
		// a re-entry read is a scribe pass and counts as one for busy-ness,
		// but the row can say what it is actually doing: reading the line
		// the reader just typed, not writing anything
		return "reading"
	}
	if m.scribing[r.F.ID] > 0 {
		return "scribing"
	}
	if m.consultSending[r.F.ID] != "" {
		return "asking"
	}
	if sess := m.sessionFor(r.F.ID); sess != nil {
		return m.runningVerb(sess.Snapshot())
	}
	if r.DrivenAbroad && r.Foreign.Busy {
		return "running"
	}
	return ""
}

// queuedLabel names the queued state everywhere the UI speaks it: the
// why the card actions offer on a queued card (cardactions.go's
// runLabelWhy) and the wait line the thread's live stage block shows for
// the same state both draw from here, so a card's board-row vocabulary
// and its thread-detail vocabulary can never disagree — the same
// contract runningLabel carries for the busy word. A queued session is
// never busy (the engine sets busy only around an in-flight turn), but
// every surface still checks queued before busy, so its reading does
// not depend on arm order.
func queuedLabel() string {
	return "queued — waiting for a free slot"
}

// runningLabel is the thread's own busy line: runningVerb's word plus how
// long the session has been at it. cardBusyWord (the board row) calls
// runningVerb directly instead — the compact row has no room for a clock
// and several callers key off its exact word, so the elapsed clause lives
// only in the detail view this function renders for.
//
// since is when the current stage/session began — the caller's best
// cheap answer to that, not a field this function tracks itself. The
// only caller (thread.go's liveStageBlock) passes the feature row's own
// UpdatedAt: a stage transition rewrites it, so in the common case it is
// "when this stage began"; it can also move on an envelope top-up or a
// title/profile edit mid-stage, which restarts the clock a little early
// rather than never restarting it at all. A zero Time means "unknown":
// the label carries no clock rather than a nonsense one.
func (m *Shell) runningLabel(snap engine.Snapshot, since time.Time) string {
	return withElapsed(m.runningVerb(snap), m.now(), since)
}

// runningVerb is runningLabel's word without the elapsed clock —
// Interactive sessions (a raw attach, a consult, the board chat) and any
// stage besides the three that actually run an agent turn fall back to
// the generic "running"; the plan loop's own legs take priority over the
// per-stage verb because they say more than the stage alone can.
func (m *Shell) runningVerb(snap engine.Snapshot) string {
	if !snap.Interactive {
		switch snap.Feature.Stage {
		case domain.StagePlan:
			switch {
			case snap.Critique:
				return "critiquing plan"
			case m.round(snap.Feature.ID, domain.RoundKindPlan) > 0:
				return "replanning"
			default:
				return "writing plan"
			}
		case domain.StageImplement:
			return "implementing"
		case domain.StageVerify:
			return "verifying"
		}
	}
	return "running"
}

// withElapsed appends a compact "· 4m12s" clause to a busy label once
// since is known (non-zero) — see runningLabel. now is m.now() rather
// than time.Now() so a test can fix both ends and assert a stable
// duration instead of racing the wall clock.
func withElapsed(label string, now, since time.Time) string {
	if since.IsZero() || !now.After(since) {
		return label
	}
	return label + " · " + compactDuration(now.Sub(since))
}

// compactDuration renders a duration the way the busy line wants it:
// short enough to sit next to a spinner, but precise enough that "4m12s"
// and "4m58s" read as different ages rather than both rounding to "4m" —
// the whole point of putting a clock next to a spinner that otherwise
// looks the same whether the turn is 4 seconds or 4 minutes old.
func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
