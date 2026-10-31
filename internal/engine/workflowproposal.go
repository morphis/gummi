package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/workflowrecipe"
)

// What a person reads to allow a run (DESIGN §25.5), as data: the faces
// draw the proposal's card from it, and the question's text
// (proposalQuestion) says the same in words.

// WorkflowProposal is a run's open proposal, or a planner's revised plan,
// as the faces draw it.
type WorkflowProposal struct {
	Run  int
	Kind domain.RunDecisionKind
	// Recipe is the recipe's name ("deliver"), or how an inline one is
	// called; Description is its own line about itself.
	Recipe      string
	Description string
	// Spec is the spec the run is judged against, SpecTitle its title and
	// SpecRev the revision it would freeze were it to start now.
	Spec      int
	SpecTitle string
	SpecRev   int
	// Steps is the recipe's step tree, depth first: for a revised plan,
	// the steps that would replace the run's remaining ones, and Replaces
	// those remaining ones, which would not run.
	Steps    []ProposalStep
	Replaces []ProposalStep
	Warnings []string
	// Checks are the done-when check commands the run will execute in the
	// tree when it ends.
	Checks []ProposalCheck
	// PoolLeft is what the session's envelope has left, when Bounded (a
	// session with no envelope has no pool to say). A run estimates
	// nothing: it spends from that pool until it is spent.
	PoolLeft float64
	Bounded  bool
	// Dirty marks a tree holding uncommitted work, which the run's diff
	// and a Revert run take in too.
	Dirty bool
	// Branch is the session's branch: the tree the run works in.
	Branch string
	// Baseline is the proposal's done-when checks run in the tree as it is
	// (§25.5), nil for a revised plan or a spec with no checks.
	Baseline *ProposalBaseline
}

// ProposalBaseline is a proposal's baseline: still running, or each
// checked item's result before the run. Evidence for the person, never a
// gate.
type ProposalBaseline struct {
	Running bool
	// Err says why the checks could not be run.
	Err   string
	Items []BaselineItem
}

// BaselineItem is one checked item's baseline: its verdict, the words the
// faces say it in (BaselineLine), and whether it proves nothing about the
// run (BaselineProblem).
type BaselineItem struct {
	Item    string
	Verdict domain.ItemVerdict
	Line    string
	Problem bool
}

// ProposalBaselineOf is a run's baseline as its proposal shows it, nil
// when it has none, or one that ran no check.
func ProposalBaselineOf(b *domain.RunBaseline) *ProposalBaseline {
	if b == nil || !b.Running && b.Err == "" && len(b.Results) == 0 {
		return nil
	}
	out := &ProposalBaseline{Running: b.Running, Err: b.Err}
	if b.Running {
		return out
	}
	for _, r := range b.Results {
		out.Items = append(out.Items, BaselineItem{Item: r.Item, Verdict: r.Verdict, Line: BaselineLine(r), Problem: BaselineProblem(r)})
	}
	return out
}

// ProposalStep is one entry of a proposal's step tree.
type ProposalStep struct {
	// ID is the entry's id; Depth how deep it sits (a repeat's body, a
	// parallel's branches, one deeper).
	ID    string
	Depth int
	// Kind is "step", "parallel" or "repeat".
	Kind string
	// A step's role, the profile slot it named (empty: its role's), the
	// model it runs on under the session's profile in words, and the first
	// paragraph of its prompt.
	Role   domain.WorkflowRole
	Slot   string
	Model  string
	Prompt string
	// A repeat's bound, its stop condition in words and what it does at
	// its max; a parallel's join.
	Max   int
	Until string
	OnMax domain.OnMax
	Join  domain.Join
}

// ProposalCheck is one done-when item's check command.
type ProposalCheck struct {
	Item string
	Says string
	Cmd  string
}

// WorkflowProposal reads run n's open proposal or revised plan. It fails
// when the run has neither waiting on the person.
func (e *Engine) WorkflowProposal(ctx context.Context, session domain.FeatureID, n int) (WorkflowProposal, error) {
	st := e.cfg.Store
	if st == nil {
		return WorkflowProposal{}, errors.New("workflows need the store")
	}
	run, err := st.Run(ctx, session, n)
	if err != nil {
		return WorkflowProposal{}, err
	}
	if run == nil || run.Pending == nil ||
		(run.Pending.Kind != domain.RunDecideProposal && run.Pending.Kind != domain.RunDecidePlan) {
		return WorkflowProposal{}, fmt.Errorf("run %d has no proposal waiting", n)
	}
	// a proposal can wait for hours while every workflow event asks for it:
	// the one made for this revision of the run stands for a few seconds,
	// then the tree is read again (the pool and a dirty tree move)
	cacheKey := fmt.Sprintf("%s/%d", session, n)
	rt := e.wfRT()
	rt.mu.Lock()
	hit, cached := rt.proposals[cacheKey]
	rt.mu.Unlock()
	if cached && hit.rev == run.Revision && time.Since(hit.at) < proposalTTL {
		return hit.p, nil
	}
	sess, err := st.GetFeature(ctx, session)
	if err != nil {
		return WorkflowProposal{}, err
	}
	p, err := e.buildProposal(ctx, sess, run)
	if err != nil {
		return p, err
	}
	rt.mu.Lock()
	rt.proposals[cacheKey] = cachedProposal{rev: run.Revision, p: p, at: time.Now()}
	rt.mu.Unlock()
	return p, nil
}

// proposalTTL is how long WorkflowProposal stands on a proposal it made.
const proposalTTL = 5 * time.Second

type cachedProposal struct {
	rev int
	p   WorkflowProposal
	at  time.Time
}

func (e *Engine) buildProposal(ctx context.Context, sess domain.Feature, run *domain.Run) (WorkflowProposal, error) {
	st := e.cfg.Store
	session, n := run.Session, run.N
	var err error
	d := run.Pending
	p := WorkflowProposal{
		Run: run.N, Kind: d.Kind, Recipe: recipeLabel(run), Description: run.Recipe.Description,
		Spec: run.Spec, Warnings: d.Warnings, Branch: sess.BranchName(),
	}
	entries := run.Recipe.Steps
	if d.Kind == domain.RunDecidePlan {
		if entries, err = workflowrecipe.ParseEntries([]byte(d.RecipeYAML)); err != nil {
			return WorkflowProposal{}, err
		}
		if steps, err := st.RunSteps(ctx, session, n); err == nil {
			if started := startedEntries(run.Recipe, steps); started < len(run.Recipe.Steps) {
				p.Replaces = e.proposalSteps(sess.Profile, run.Recipe.Steps[started:], 0)
			}
		}
	}
	p.Steps = e.proposalSteps(sess.Profile, entries, 0)
	if sp, ok, err := st.SessionSpec(ctx, session, run.Spec); err == nil && ok {
		p.SpecTitle = sp.Title
	}
	if revs, _ := st.SpecRevisions(ctx, session, run.Spec); len(revs) > 0 {
		p.SpecRev = revs[0].Rev
	}
	if run.SpecRev > 0 {
		p.SpecRev = run.SpecRev
	}
	// the checks of the revision the run is judged against: frozen when it
	// was proposed, and the spec as it is now only for a run from before
	items, _ := e.frozenSpecItems(ctx, sess, run)
	for _, it := range items {
		if strings.TrimSpace(it.Check) != "" {
			p.Checks = append(p.Checks, ProposalCheck{Item: it.ID, Says: it.Says, Cmd: it.Check})
		}
	}
	if d.Kind == domain.RunDecideProposal {
		p.Baseline = ProposalBaselineOf(run.Baseline)
	}
	p.PoolLeft, p.Bounded = e.sessionPoolLeft(ctx, sess)
	if mgr, err := e.mgr(ctx, &sess); err == nil {
		p.Dirty, _ = mgr.Dirty(ctx, &sess)
	}
	return p, nil
}

// frozenSpecItems is the done-when items a run is judged against: its
// frozen revision's and the checks its steps added, or for a run from
// before that froze none, the spec's as it is now.
func (e *Engine) frozenSpecItems(ctx context.Context, sess domain.Feature, run *domain.Run) ([]domain.DoneWhen, error) {
	if run.SpecRev > 0 {
		_, items := e.runItems(ctx, run)
		return items, nil
	}
	text, ok := e.SessionSpec(sess, run.Spec)
	if !ok {
		return nil, fmt.Errorf("spec %d is gone", run.Spec)
	}
	items, _, err := spec.ParseDoneWhen(text)
	return items, err
}

// proposalSteps flattens entries into the proposal's tree, depth first.
func (e *Engine) proposalSteps(profile string, es []domain.Entry, depth int) []ProposalStep {
	step := func(s domain.Step, depth int) ProposalStep {
		return ProposalStep{
			ID: s.ID, Depth: depth, Kind: "step", Role: s.Role, Slot: s.RunsOnSlot(),
			Model: e.stepModelWords(profile, s), Prompt: promptGist(s.Prompt),
		}
	}
	var out []ProposalStep
	for _, en := range es {
		switch {
		case en.Step != nil:
			out = append(out, step(*en.Step, depth))
		case en.Parallel != nil:
			out = append(out, ProposalStep{
				ID: en.Parallel.ID, Depth: depth, Kind: "parallel", Join: en.Parallel.Join,
			})
			for _, s := range en.Parallel.Steps {
				out = append(out, step(s, depth+1))
			}
		case en.Repeat != nil:
			r := en.Repeat
			out = append(out, ProposalStep{
				ID: r.ID, Depth: depth, Kind: "repeat", Max: r.Max, Until: untilText(r.Until), OnMax: r.OnMax.Or(),
			})
			out = append(out, e.proposalSteps(profile, r.Steps, depth+1)...)
		}
	}
	return out
}

// promptGist is a step's prompt as its proposal row says it: its first
// paragraph on one line, cut at a sentence or 200 characters, so a prompt
// wrapped in its YAML reads whole rather than cut mid-sentence.
func promptGist(prompt string) string {
	para, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n\n")
	s := strings.Join(strings.Fields(para), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:199]) + "…"
	}
	return s
}
