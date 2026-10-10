package ui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/pr"
)

// prChecksReadyMsg carries what a send-failing-checks read found: the
// message for the session, and how many checks it names. Delivery is the
// Update goroutine's, because a card at verify goes back over its rerun
// edge (bounceStage), which writes the board's own fields.
type prChecksReadyMsg struct {
	f    domain.Feature
	turn string
	n    int
}

// sendPRChecks reads f's linked PR's checks from GitHub, with the end of
// each failed job's log, and hands the failing ones to the card's session.
// It is a read and a message: nothing is stored and no gate is held by it,
// since a check's answer belongs to a commit and is stale the moment the
// branch is pushed again. Only a person starts it.
func (m *Shell) sendPRChecks(f domain.Feature) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		// stale-row safety, as pullPRReview: the row may predate an unlink
		cur, err := m.store.GetFeature(ctx, f.ID)
		if err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": " + err.Error()), isErr: true}
		}
		f = cur
		if f.PullRequest.Empty() {
			return noticeMsg{text: string(f.ID) + " has no linked PR", isErr: true}
		}
		if m.fetchPRChecks == nil {
			return noticeMsg{text: "sending failing checks is unavailable — no PR backend wired", isErr: true}
		}
		checks, err := m.fetchPRChecks(ctx, f.PullRequest)
		if err != nil {
			return noticeMsg{text: sanitize(string(f.ID) + ": reading the PR's checks: " + err.Error()), isErr: true}
		}
		n := len(checks.Failing())
		if n == 0 {
			text := fmt.Sprintf("%s: no failing checks on %s#%d", f.ID, f.PullRequest.Repo, f.PullRequest.Number)
			if p := checks.Count(pr.CheckPending); p > 0 {
				text += fmt.Sprintf(" (%d still running)", p)
			}
			return noticeMsg{text: text}
		}
		tip, _ := m.wt.Head(ctx, &f) // "" leaves the stale-tip line out
		return prChecksReadyMsg{f: f, turn: pr.FailingTurn(f.PullRequest, checks, tip), n: n}
	}
}

// deliverPRChecks routes the failing checks to whoever writes the card's
// code. A freeform card has one session and it is the writer. A workflow
// card's writer is its implement stage: live when that is running, a fresh
// run carrying the checks in its kickoff when it is not, and from verify
// the card goes back over its rerun edge first — the same route a person's
// "request changes" takes. A critique, a rebase or a verify in flight is
// not interrupted: the checks are not stored, so the person sends them
// again once it has finished.
func (m *Shell) deliverPRChecks(msg prChecksReadyMsg) tea.Cmd {
	f, turn := msg.f, msg.turn
	what := fmt.Sprintf("%d failing check%s", msg.n, plural(msg.n))
	if m.engine == nil && f.Stage != domain.StageVerify {
		text := string(f.ID) + ": no agent engine to hand the failing checks to"
		return func() tea.Msg { return noticeMsg{text: text, isErr: true} }
	}
	if f.IsFreeform() {
		return func() tea.Msg {
			ctx := context.Background()
			ff, err := m.engine.OpenFreeform(ctx, f)
			if err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
			if err := ff.Send(ctx, turn); err != nil {
				return noticeMsg{text: sanitize(err.Error()), isErr: true}
			}
			return noticeMsg{text: fmt.Sprintf("%s: sent %s to its session", f.ID, what), reload: true}
		}
	}
	running := false
	if s := m.session(f.ID); s != nil && s.State() == engine.StateRunning {
		running = true
		snap := s.Snapshot()
		if snap.Critique || snap.Rebase || f.Stage != domain.StageImplement {
			pass := string(f.Stage)
			switch {
			case snap.Critique:
				pass += " critique"
			case snap.Rebase:
				pass = "a rebase"
			}
			text := fmt.Sprintf("%s: %s is running — send the failing checks again when it has finished", f.ID, pass)
			return func() tea.Msg { return noticeMsg{text: text, isErr: true} }
		}
	}
	switch f.Stage {
	case domain.StageImplement:
		if running {
			return func() tea.Msg {
				if err := m.engine.Send(context.Background(), f.ID, turn); err != nil {
					return noticeMsg{text: sanitize(err.Error()), isErr: true}
				}
				return noticeMsg{text: fmt.Sprintf("%s: sent %s to the running implement agent", f.ID, what), reload: true}
			}
		}
		m.dropSession(f.ID)
		return func() tea.Msg {
			if err := m.engine.RunWith(f, turn); err != nil {
				return noticeMsg{text: err.Error(), isErr: true}
			}
			return noticeMsg{text: fmt.Sprintf("%s: re-running implement with %s", f.ID, what), reload: true}
		}
	case domain.StageVerify:
		return m.bounceStage(f.ID, turn)
	}
	text := fmt.Sprintf("%s is in %s — failing checks go to a session, or to a card in implement or verify", f.ID, f.Stage)
	return func() tea.Msg { return noticeMsg{text: text, isErr: true} }
}

// prChecksDetail says what sending the failing checks does from stage: a
// card at verify has no writer to hand them to until it goes back.
func prChecksDetail(stage domain.Stage) string {
	if stage == domain.StageVerify {
		return "read the PR's failing checks and their logs, and send the card back to implement to fix them"
	}
	return "read the PR's failing checks and their logs, and hand them to the session to fix"
}

// session is the card's stage session, nil when it has none or the board
// runs without an engine.
func (m *Shell) session(id domain.FeatureID) *engine.Session {
	if m.engine == nil {
		return nil
	}
	return m.engine.Get(id)
}
