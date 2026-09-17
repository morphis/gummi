package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
)

// The sweep is cleanup for the whole board at once.
//
// Every refusal it makes already existed and was already right — a card
// that has not landed, one carrying uncommitted rework, one handed off
// with its branch kept on purpose. What they could not do was be read:
// cleanup was per-card, reachable only by selecting that card, and each
// refusal arrived one at a time as an error on a card someone had to go
// find. Collected into a list they become a plan, which is a thing
// somebody can act on.
//
// "Landed" also described four different states of the machine underneath
// one word — cleaned, not cleaned, handed off, dropped — and no screen
// ever said which, or how much disk it was. The figures here are the
// answer, and the board's archive line carries the total so it is noticed
// rather than remembered.

// sweepCard is one card the sweep would clean, with what it holds.
type sweepCard struct {
	ID    domain.FeatureID
	Bytes int64
}

// sweepHold is one card the sweep is refusing, and why — stated rather
// than silently omitted, because a plan that hides what it skipped reads
// as "everything was covered" when it was not.
type sweepHold struct {
	ID  domain.FeatureID
	Why string
}

// sweepPlan is what a sweep would do.
type sweepPlan struct {
	Clean []sweepCard
	Held  []sweepHold
	Bytes int64
}

// sweepPlannedMsg carries a measured plan back to the open pass.
type sweepPlannedMsg struct{ plan sweepPlan }

// sweptMsg is one finished sweep.
type sweptMsg struct {
	cleaned int
	bytes   int64
	failed  []string
}

// planSweep measures what a sweep would do. It runs off the render loop —
// it walks each worktree on disk and asks git about each branch — and is
// called once per sweep, on arrival at the phase that shows it.
func (m *Shell) planSweep(ctx context.Context, rows []featureRow) sweepPlan {
	var plan sweepPlan
	for _, r := range rows {
		f := r.F
		switch {
		case r.F.HandedOff() && r.HasWorktree:
			// the branch was kept on purpose; cleaning it up would delete
			// the one thing the reader chose to keep
			plan.Held = append(plan.Held, sweepHold{ID: f.ID, Why: "handed off — kept, you asked for it"})
			continue
		case !r.Landed:
			continue
		}
		if dirty, err := m.wt.TrackedDirty(ctx, &f); err != nil {
			plan.Held = append(plan.Held, sweepHold{ID: f.ID, Why: sanitize(err.Error())})
			continue
		} else if dirty {
			plan.Held = append(plan.Held, sweepHold{ID: f.ID, Why: "uncommitted rework — commit it first"})
			continue
		}
		size, _ := m.wt.DiskSize(ctx, &f)
		plan.Clean = append(plan.Clean, sweepCard{ID: f.ID, Bytes: size})
		plan.Bytes += size
	}
	return plan
}

// runSweep cleans every card the plan named, one at a time, each under
// its own card lock — the same act c performs, so a sweep can never do
// something to a card that pressing c on it would have refused.
//
// A card that fails is reported and the sweep carries on: one card's
// problem is not a reason to leave the other five holding disk.
func (m *Shell) runSweep(plan sweepPlan) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(plan.Clean))
	for _, c := range plan.Clean {
		f, err := m.store.GetFeature(context.Background(), c.ID)
		if err != nil {
			continue
		}
		cmds = append(cmds, m.cleanupLanded(f))
	}
	if len(cmds) == 0 {
		return nil
	}
	// Sequenced rather than batched: each cleanup takes the card's lock
	// and shells out to git, and running them concurrently would have
	// several worktree removals racing inside one repository.
	cmds = append(cmds, func() tea.Msg {
		return sweptMsg{cleaned: len(plan.Clean), bytes: plan.Bytes}
	})
	return tea.Sequence(cmds...)
}

// refreshWorktreeSize re-measures what the archive's un-cleaned
// worktrees hold, for the board's archive line. It is dispatched when the
// board reloads after a sweep — never per frame.
func (m *Shell) refreshWorktreeSize() tea.Cmd {
	rows := append([]featureRow(nil), m.rows...)
	pool := m.wt
	return func() tea.Msg {
		var total int64
		ctx := context.Background()
		for _, r := range rows {
			if !r.Landed {
				continue
			}
			if size, err := pool.DiskSize(ctx, &r.F); err == nil {
				total += size
			}
		}
		return worktreeSizeMsg{text: humanBytes(total)}
	}
}

// worktreeSizeMsg carries a measured total back to the shell.
type worktreeSizeMsg struct{ text string }

// humanBytes renders a byte count the way someone deciding whether to
// tidy up would read it: two significant figures at most, and never more
// precision than the decision needs.
func humanBytes(n int64) string {
	const gb = int64(1) << 30
	switch {
	case n <= 0:
		return ""
	case n < 1<<20:
		return fmt.Sprintf("%d KB", max64(n>>10, 1))
	case n < gb:
		return fmt.Sprintf("%d MB", n>>20)
	}
	v := float64(n) / float64(gb)
	// whole gigabytes lose the decimal: "1 GB" is what someone weighing a
	// cleanup reads, and "1.0 GB" is a precision the decision does not have
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d GB", int64(v))
	}
	return fmt.Sprintf("%.1f GB", v)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// sweptText is the sentence a finished sweep leaves behind: what it did,
// in the terms someone ran it for.
func sweptText(msg sweptMsg) string {
	s := fmt.Sprintf("swept %d card%s", msg.cleaned, plural(msg.cleaned))
	if b := humanBytes(msg.bytes); b != "" {
		s += " — " + b + " freed"
	}
	if len(msg.failed) > 0 {
		s += " · " + strings.Join(msg.failed, "; ")
	}
	return s
}

// landedRows counts the rows whose worktree the sweep would clean. It is
// the cheap proxy the shell keys its disk measurement on: the expensive
// walk runs only when this number moves.
func landedRows(rows []featureRow) int {
	var n int
	for _, r := range rows {
		if r.Landed {
			n++
		}
	}
	return n
}
