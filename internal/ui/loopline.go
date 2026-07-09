package ui

import (
	"strings"

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
	if m.planRounds[f.ID] > 0 {
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
	if r := m.planRounds[f.ID]; r > 0 {
		line += s.Faint.Render("  ·  round " + itoa(r) + "/" + itoa(maxPlanRounds))
	}
	if escalated {
		line += s.Warning.Render("  ·  escalated")
	}
	return line
}

// planLoopWord is the board-card hint while a plan-loop session runs:
// the stage glyph can't distinguish the legs, so the card says which.
func (m *Shell) planLoopWord(sess *engine.Session) string {
	if sess.Feature.Stage != domain.StagePlan || sess.Interactive {
		return ""
	}
	switch {
	case sess.Critique:
		return "critiquing"
	case m.planRounds[sess.Feature.ID] > 0:
		return "replanning"
	default:
		return "planning"
	}
}

// runningLabel names what a busy session is doing next to the activity
// spinner. The plan loop's legs are invisible to the stage machine, so
// the label carries them; everything else just says "running".
func (m *Shell) runningLabel(snap engine.Snapshot) string {
	if snap.Feature.Stage != domain.StagePlan || snap.Interactive {
		return "running"
	}
	switch {
	case snap.Critique:
		return "critiquing plan"
	case m.planRounds[snap.Feature.ID] > 0:
		return "replanning"
	default:
		return "writing plan"
	}
}
