package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/ui/theme"
)

// offered reports which of ids the action list carries.
func offered(acts []cardAction, ids ...string) []string {
	var out []string
	for _, a := range acts {
		if slices.Contains(ids, a.id) {
			out = append(out, a.id)
		}
	}
	return out
}

// The menu offers only what can work on the card as it stands: a closed
// card has nothing left to verify, rebase, wait on, spend or gate; a plan
// has built nothing to verify; and a stage whose agent is still on it is
// not moved on from under it.
func TestCardMenuOffersOnlyWhatCanWork(t *testing.T) {
	steering := []string{"verify", "rebase", "deps", "envelope", "gate"}

	landed := cardRow(domain.KindFeature, domain.StageVerify, true, true)
	landed.F.ID = "FD-003"
	in := nextInput{stage: domain.StageVerify, kind: domain.KindFeature, landed: true, sess: engine.StateDone}
	if got := offered(cardActionsFor(in, landed), steering...); len(got) > 0 {
		t.Errorf("a landed card offers %v", got)
	}

	done := cardRow(domain.KindFeature, domain.StageDone, false, true)
	if got := offered(cardActionsFor(nextInput{stage: domain.StageDone, kind: domain.KindFeature}, done), steering...); len(got) > 0 {
		t.Errorf("a done card offers %v", got)
	}

	// a done research card still re-runs decompose, which spends
	rs := cardRow(domain.KindResearch, domain.StageDone, false, false)
	if got := offered(cardActionsFor(nextInput{stage: domain.StageDone, kind: domain.KindResearch}, rs), "advance", "envelope"); len(got) != 2 {
		t.Errorf("a done research card offers %v, want its decompose re-run and the budget that pays for it", got)
	}

	// a handed-off card can still be landed after all, so a rebase stays
	handed := cardRow(domain.KindFeature, domain.StageDone, false, true)
	handed.F.HandedOffAt = handed.F.CreatedAt.AddDate(2026, 0, 0)
	if got := offered(cardActionsFor(nextInput{stage: domain.StageDone, kind: domain.KindFeature}, handed), "rebase"); len(got) != 1 {
		t.Error("a handed-off card lost the rebase its later landing may need")
	}

	plan := cardRow(domain.KindFeature, domain.StagePlan, false, true)
	if got := offered(cardActionsFor(nextInput{stage: domain.StagePlan, kind: domain.KindFeature, attn: attnGate}, plan), "verify"); len(got) > 0 {
		t.Error("a plan with nothing built offers verify")
	}

	impl := cardRow(domain.KindFeature, domain.StageImplement, false, true)
	for _, in := range []nextInput{
		{stage: domain.StageImplement, kind: domain.KindFeature, sess: engine.StateRunning},
		{stage: domain.StagePlan, kind: domain.KindFeature, sess: engine.StateInteractive, live: true, busy: true},
	} {
		if got := offered(cardActionsFor(in, impl), "advance"); len(got) > 0 {
			t.Errorf("next stage offered while the agent is at work (%s, busy %v)", in.sess, in.busy)
		}
	}
	if got := offered(cardActionsFor(nextInput{stage: domain.StageImplement, kind: domain.KindFeature, sess: engine.StateRunning}, impl), "verify"); len(got) != 1 {
		t.Error("an implement card lost its verify")
	}
}

// Each withheld row's key says why instead of acting: the list and the
// board's keys stay in lockstep.
func TestWithheldVerbsRefuseFromTheirKeys(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	m.rows = []featureRow{
		{F: domain.Feature{ID: "FD-003", Title: "landed", Stage: domain.StageVerify}, Landed: true, HasWorktree: true},
		{F: domain.Feature{ID: "FD-004", Title: "plan", Stage: domain.StagePlan}, HasWorktree: true},
	}
	for _, c := range []struct {
		sel  int
		key  string
		want string
	}{
		{0, "v", "nothing left to verify"},
		{0, "r", "nothing left to rebase"},
		{0, "u", "nothing is left for a budget"},
		{0, "p", "nothing left for it to wait on"},
		{1, "v", "nothing has been built to verify yet"},
	} {
		m.sel = c.sel
		m.notice = noticeMsg{}
		if cmd := m.boardVerb(c.key); cmd != nil {
			t.Errorf("%s on %s started something", c.key, m.rows[c.sel].F.ID)
		}
		if !strings.Contains(m.notice.text, c.want) || !m.notice.isErr {
			t.Errorf("%s on %s: notice %q, want %q", c.key, m.rows[c.sel].F.ID, m.notice.text, c.want)
		}
		if m.Overlay.HasDialogs() {
			t.Errorf("%s on %s opened a dialog", c.key, m.rows[c.sel].F.ID)
			m.Overlay.Pop()
		}
	}
}
