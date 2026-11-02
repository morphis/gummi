package ui

// The workflow loop (DESIGN §25.7): the board ticks every session's live
// workflow run whenever the engine says something a run's conductor reads
// changed (EventWorkflow), and on a slow poll, so a run whose last event
// was lost still moves. Ticks never overlap: one asked for while another
// runs is coalesced into one more after it.
//
// A run's open decision — a proposal, a revised plan, a repeat at its
// max or one that cannot judge its condition, or the budget — is its
// session's open decision,
// asked the way an ask_user question is and answered through
// Engine.WorkflowAnswer.

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/threadfold"
	"github.com/morphis/gummi/internal/ui/theme"
)

const workflowPollInterval = 10 * time.Second

type workflowTickedMsg struct{ err error }

type workflowPollMsg struct{}

func workflowPollTick() tea.Cmd {
	return subscription(tea.Tick(workflowPollInterval, func(time.Time) tea.Msg { return workflowPollMsg{} }))
}

// workflowTickCmd runs one tick of the workspace's runs off the update
// loop.
func (m *Shell) workflowTickCmd() tea.Cmd {
	if m.engine == nil {
		return nil
	}
	if m.workflowTicking {
		m.workflowTickAgain = true
		return nil
	}
	m.workflowTicking = true
	eng := m.engine
	return func() tea.Msg {
		_, err := eng.WorkflowTick(context.Background())
		return workflowTickedMsg{err: err}
	}
}

// updateWorkflow handles the workflow loop's own messages.
func (m *Shell) updateWorkflow(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case workflowPollMsg:
		return tea.Batch(workflowPollTick(), m.workflowTickCmd()), true
	case workflowTickedMsg:
		m.workflowTicking = false
		var cmds []tea.Cmd
		if msg.err != nil {
			text := "workflow: " + sanitize(msg.err.Error())
			cmds = append(cmds, func() tea.Msg { return noticeMsg{text: text, isErr: true} })
		}
		if m.workflowTickAgain {
			m.workflowTickAgain = false
			cmds = append(cmds, m.workflowTickCmd())
		}
		return tea.Batch(cmds...), true
	}
	return nil, false
}

// workflowDecision is a session's run decision as the card's open
// decision: an ask-shaped stop whose options are the run's.
func workflowDecision(r featureRow) *threadDecision {
	run := r.DecidingRun
	if run == nil || run.Pending == nil {
		return nil
	}
	d := run.Pending
	ask := &engine.Ask{Question: d.Question, DecisionID: d.ID}
	for _, o := range d.Options {
		ask.Options = append(ask.Options, engine.AskOption{Label: o, Detail: workflowOptionDetail(o)})
	}
	return &threadDecision{card: r.F.ID, key: "workflow|" + d.ID, kind: decisionWorkflow, question: d.Question, ask: ask, run: run}
}

func workflowOptionDetail(o string) string {
	switch o {
	case engine.WorkflowAlways:
		return "allows this run and every later one of this session without asking; its budget is the only cap"
	case engine.WorkflowAllow:
		return "the run starts; no agent allows its own run"
	case engine.WorkflowDeny:
		return "no run; the session carries on by itself"
	case engine.WorkflowAllowPlan:
		return "the revised steps replace the rest of the run"
	case engine.WorkflowKeepPlan:
		return "the run carries on with the plan it had"
	case engine.WorkflowOneMore:
		return "the loop runs once more"
	case engine.WorkflowContinue:
		return "the run goes on to its next step"
	case engine.WorkflowStopRun:
		return "the run stops; its changes stay in the tree"
	case engine.WorkflowTopUp:
		return "raises the session's envelope; a paused run carries on"
	case engine.WorkflowResumeRun:
		return "the run carries on where it was; its steps pick their work up"
	}
	return ""
}

// workflowDecisionWord is the word a run's decision is badged with.
func workflowDecisionWord(d *domain.RunDecision) string {
	switch d.Kind {
	case domain.RunDecideProposal:
		return "allow run"
	case domain.RunDecidePlan:
		return "revised plan"
	case domain.RunDecideMax:
		return "loop at max"
	case domain.RunDecideUnjudged:
		return "loop can't judge"
	case domain.RunDecideBudget:
		return "run budget"
	case domain.RunDecideResume:
		return "run parked"
	case domain.RunDecideWatch:
		return "PR asks"
	}
	return "run asks"
}

// answerWorkflow answers a run's decision.
func (m *Shell) answerWorkflow(r featureRow, d *threadDecision, answer string) tea.Cmd {
	eng := m.engine
	n := d.run.N
	// the answer is to the decision the person read: the engine refuses it
	// if the run has raised another since
	decision := d.ask.DecisionID
	return func() tea.Msg {
		if err := eng.WorkflowAnswer(context.Background(), r.F.ID, n, decision, answer); err != nil {
			return noticeMsg{text: sanitize(err.Error()), isErr: true}
		}
		// the row carries the run's decision: it is read again before the
		// answer is done, so whoever gave it (a web answer waits on this)
		// never sees the decision it just answered still standing
		return tea.BatchMsg{
			func() tea.Msg { return noticeMsg{clearInbox: r.F.ID, reseedInbox: true} },
			m.loadRows,
		}
	}
}

// runProposal is the proposal (or revised plan) d asks a person to allow,
// as data, when the row read one for d's run.
func runProposal(r featureRow, d *threadDecision) *engine.WorkflowProposal {
	if d == nil || d.run == nil || r.Proposal == nil || r.Proposal.Run != d.run.N {
		return nil
	}
	return r.Proposal
}

// firstParagraph is text up to its first blank line.
func firstParagraph(text string) string {
	if i := strings.Index(text, "\n\n"); i >= 0 {
		return strings.TrimSpace(text[:i])
	}
	return strings.TrimSpace(text)
}

// proposalTreeRow is one entry of a proposal's step tree: a repeat with
// its bound and condition, a parallel with its join, or a step with its
// role, tier, the model it runs on and its prompt's gist.
func proposalTreeRow(s *theme.Styles, st engine.ProposalStep, fit func(string) string) string {
	indent := strings.Repeat("  ", st.Depth)
	switch st.Kind {
	case "repeat":
		l := indent + st.ID + fmt.Sprintf(" ↺ ≤%d", st.Max)
		if st.Until != "" {
			l += s.Faint.Render(" until " + sanitize(st.Until))
		}
		if st.OnMax != "" {
			l += s.Faint.Render(", else " + string(st.OnMax))
		}
		return fit("  " + s.Base.Render(l))
	case "parallel":
		l := indent + st.ID + " ∥"
		if st.Join != "" {
			l += s.Faint.Render(" join " + string(st.Join))
		}
		return fit("  " + s.Base.Render(l))
	}
	row := "  " + s.Base.Render(padTo(indent+st.ID, 16)) + " " +
		roleStyle(s, st.Role).Render(padTo(string(st.Role), 12)) + " " +
		s.Faint.Render(padTo(sanitize(st.Model), 28))
	if st.Prompt != "" {
		row += " " + s.Faint.Render(sanitize(st.Prompt))
	}
	return fit(row)
}

// proposalLines is a run's proposal as the picker draws it above its
// answers (DESIGN §25.5): the recipe and its spec, what the session's pool
// has left and whether the tree holds uncommitted work, Check's warnings,
// the step tree with each step's role and tier, and
// every done-when check it will run. It is the narration, so it sheds
// from the end on a short page: the order is what matters most first.
func proposalLines(s *theme.Styles, p engine.WorkflowProposal, w int) []string {
	fit := func(l string) string { return " " + ansi.Truncate(l, max(w-1, 8), "…") }
	var out []string
	head := s.Title.Render(sanitize(p.Recipe))
	if p.Kind == domain.RunDecidePlan {
		head = s.Title.Render("revised plan") + s.Faint.Render(" for "+sanitize(p.Recipe)+" · run "+strconv.Itoa(p.Run))
	}
	if p.Description != "" {
		head += s.Faint.Render(" — " + sanitize(p.Description))
	}
	out = append(out, fit(head))
	spec := fmt.Sprintf("spec S%d", p.Spec)
	if p.SpecTitle != "" {
		spec += " " + strconv.Quote(sanitize(p.SpecTitle))
	}
	out = append(out, fit(s.Subtle.Render(spec)+s.Faint.Render(fmt.Sprintf(" · revision %d, frozen now", p.SpecRev))))
	money := "the session has no envelope; the run spends from it"
	if p.Bounded {
		money = domain.FormatDollars(math.Max(0, p.PoolLeft)) + " left in the session; the run spends from it"
	}
	tree := "nothing uncommitted in " + p.Branch
	treeStyle := s.Faint
	if p.Dirty {
		tree, treeStyle = "the tree holds uncommitted work: the run's diff and a revert take it in too", s.Warning
	}
	out = append(out, fit(s.Muted.Render(money)+s.Faint.Render(" · ")+treeStyle.Render(tree)))
	for _, warn := range p.Warnings {
		out = append(out, fit(s.Warning.Render("⚠ "+sanitize(warn))))
	}
	if len(p.Warnings) == 0 && p.Kind == domain.RunDecideProposal {
		out = append(out, fit(s.Success.Render("✓ the recipe passes its checks")))
	}
	drawTree := func(steps []engine.ProposalStep) {
		for _, st := range steps {
			out = append(out, proposalTreeRow(s, st, fit))
		}
	}
	drawTree(p.Steps)
	if len(p.Replaces) > 0 {
		out = append(out, fit(s.Subtle.Render("what it replaces, which would not run")))
		drawTree(p.Replaces)
	}
	if len(p.Checks) > 0 {
		out = append(out, fit(s.Subtle.Render("checks it will run in the tree")))
		for _, c := range p.Checks {
			out = append(out, fit("  "+s.Muted.Render(c.Item)+" "+s.Base.Render(sanitize(c.Cmd))))
		}
	}
	if b := p.Baseline; b != nil {
		switch {
		case b.Running:
			out = append(out, fit(s.Subtle.Render("baseline ")+s.Faint.Render(threadfold.BaselineWords)))
		case b.Err != "":
			out = append(out, fit(s.Subtle.Render("baseline ")+s.Faint.Render("not run: "+sanitize(firstLine(b.Err)))))
		default:
			out = append(out, fit(s.Subtle.Render("baseline, before the run")))
			for _, it := range b.Items {
				if it.Problem {
					out = append(out, fit("  "+s.Warning.Render("⚠ "+it.Line+" — it proves nothing about the run")))
					continue
				}
				out = append(out, fit("  "+s.Faint.Render(it.Line)))
			}
		}
	}
	if p.Preflight != "" {
		out = append(out, fit(s.Subtle.Render("preflight ")+s.Base.Render(sanitize(p.Preflight))+s.Success.Render(" ✓ passed")))
		lines := strings.Split(p.PreflightOutput, "\n")
		if len(lines) > 4 {
			lines = append([]string{"…"}, lines[len(lines)-4:]...)
		}
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				out = append(out, fit("  "+s.Faint.Render(sanitize(l))))
			}
		}
	}
	return out
}
