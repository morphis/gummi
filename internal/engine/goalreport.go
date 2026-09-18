package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/morphis/gummi/internal/atomicfile"
	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/experiment"
	"github.com/morphis/gummi/internal/goalpolicy"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// The hand-over: what a goal gives you when it is ready. Every surface —
// the goal page, `gummi status --json`, the done event, and the goal doc's
// own Report section — renders this one structure, so they cannot
// disagree about which done-when items were met.

// Done-when statuses.
const (
	DoneWhenMet     = "met"
	DoneWhenNotMet  = "not met"
	DoneWhenUnknown = "not checked"
	// DoneWhenWaiting: not met YET. Every card serving it is waiting on
	// budget, so the item is unfinished and resumable — a different claim
	// from "not met", which says the goal gave up on it. Conflating the
	// two would report a result as final while a top-up would still
	// finish it.
	DoneWhenWaiting = "waiting on budget"
	// DoneWhenBlocked: not met yet either, and for a reason that is not
	// the work's — a card serving it verified into an environment that
	// could not run its plan, and the goal kept the card. "Not met" here
	// would report the environment's absence as the goal's failure.
	DoneWhenBlocked = "waiting on an environment"
)

// DoneWhenStatus is one done-when item at the hand-over.
type DoneWhenStatus struct {
	ID       string `json:"id"`
	Says     string `json:"says"`
	How      string `json:"how"` // "check: <cmd>" or "judged"
	Status   string `json:"status"`
	Evidence string `json:"evidence,omitempty"`
}

// GoalReportRepo is one repository the goal has a branch in, at the
// hand-over. A goal that spans repositories lands once in each, and git
// has no merge that spans them — so the hand-over says which have the
// goal and which do not, rather than one "landed" for all of them.
type GoalReportRepo struct {
	Name   string `json:"name,omitempty"` // the configured name; empty is the workspace default
	Home   bool   `json:"home,omitempty"` // the goal card's own repository
	Branch string `json:"branch"`
	Landed bool   `json:"landed"` // the goal branch is in this repo's main
}

// GoalReportCard is one card at the hand-over.
type GoalReportCard struct {
	ID       domain.FeatureID `json:"id"`
	Kind     domain.Kind      `json:"kind"`
	Repo     string           `json:"repo,omitempty"` // empty is the workspace default
	Title    string           `json:"title"`
	State    string           `json:"state"`
	Stage    domain.Stage     `json:"stage"`
	Envelope int              `json:"envelope"`
	Spent    float64          `json:"spent"`
	Serves   []string         `json:"serves,omitempty"`
	Commit   string           `json:"commit,omitempty"` // the landed commit on the goal branch
	Subject  string           `json:"subject,omitempty"`
	// Stat is the landed commit's file summary — the card's section of the
	// goal's diff by card. `git show <commit>` is the whole of it.
	Stat     string `json:"stat,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Attached bool   `json:"attached,omitempty"`
}

// GoalLogLine is a log entry as the report lists it.
type GoalLogLine struct {
	Ref         string           `json:"ref,omitempty"` // D-N for decisions
	Card        domain.FeatureID `json:"card,omitempty"`
	Item        string           `json:"item,omitempty"`
	Detail      string           `json:"detail"`
	Alternative string           `json:"alternative,omitempty"`
	Finding     string           `json:"finding,omitempty"`
	By          string           `json:"by,omitempty"`
}

// GoalReportExperiment is one experiment at the hand-over: what it proved
// about the heads the goal has now, and how far the rig that proved it can
// be believed. A pass from a rig that judged nothing four runs in ten
// reads differently from one that never wavered, and honest reporting
// says which this was.
type GoalReportExperiment struct {
	Name      string   `json:"name"`
	Substrate string   `json:"substrate,omitempty"`
	Items     []string `json:"items"`
	Problem   string   `json:"problem,omitempty"`
	// Outcome is the newest conclusive run about the current heads, ""
	// when there is none.
	Outcome  string `json:"outcome,omitempty"`
	Run      string `json:"run,omitempty"`
	Evidence string `json:"evidence,omitempty"` // the run directory
	Passed   int    `json:"assertions_held,omitempty"`
	Total    int    `json:"assertions,omitempty"`
	Running  string `json:"running,omitempty"`
	// Runs, Conclusive, Inconclusive and Flaky count every run the goal
	// made of it; Minutes is the substrate time they held.
	Runs         int     `json:"runs"`
	Conclusive   int     `json:"conclusive"`
	Inconclusive int     `json:"inconclusive"`
	Flaky        int     `json:"flaky"`
	Minutes      float64 `json:"substrate_minutes"`
	// Assertions is the newest conclusive run's, so a reader sees which
	// held without opening the bundle.
	Assertions []experiment.Assertion `json:"assertion_results,omitempty"`
}

func reportExperiment(x GoalExperiment) GoalReportExperiment {
	out := GoalReportExperiment{Name: x.Name, Substrate: x.Substrate, Items: x.Items, Problem: x.Problem, Runs: len(x.Runs)}
	for _, r := range x.Runs {
		out.Minutes += r.Seconds / 60
		switch {
		case r.State == experiment.StateRunning:
		case r.Outcome.Conclusive():
			out.Conclusive++
		default:
			out.Inconclusive++
		}
		if r.Flaky {
			out.Flaky++
		}
	}
	if x.Running != nil {
		out.Running = x.Running.ID
	}
	if ev := x.Evidence; ev != nil {
		out.Outcome, out.Run, out.Evidence, out.Assertions = string(ev.Outcome), ev.ID, ev.Dir, ev.Assertions
		out.Passed, out.Total = ev.Passed()
	}
	return out
}

// GoalReport is the hand-over of one goal.
type GoalReport struct {
	ID      domain.FeatureID `json:"id"`
	Title   string           `json:"title"`
	Stage   domain.Stage     `json:"stage"`
	Ready   bool             `json:"ready"` // verified: waiting for you
	Partial string           `json:"partial,omitempty"`
	// NeedsBudget is set when the goal stopped on a card it cannot fund.
	// Unlike Partial, which says the result is incomplete and final, this
	// says it is unfinished and resumable: raise the envelope and send it
	// back and the card carries on from where it stopped.
	NeedsBudget GoalNeedsBudget `json:"needs_budget,omitempty"`
	// NeedsSubstrate is NeedsBudget for the goal's other ceiling: it cannot
	// afford the run it has to make in order to be judged.
	NeedsSubstrate GoalNeedsSubstrate `json:"needs_substrate,omitempty"`
	// WaitingOn is set when a card of the goal is waiting for an
	// environment that could not run its verification plan: the sentence
	// that says which card and what it lacked. Like NeedsBudget it says
	// unfinished and resumable, never final.
	WaitingOn  string           `json:"waiting_on,omitempty"`
	WrappingUp bool             `json:"wrapping_up,omitempty"`
	Budget     GoalReportBudget `json:"budget"`
	Lanes      int              `json:"lanes"`
	DoneWhen   []DoneWhenStatus `json:"done_when"`
	// Experiments lists the experiments the goal's items are proved by.
	Experiments []GoalReportExperiment `json:"experiments,omitempty"`
	Repos       []GoalReportRepo       `json:"repos,omitempty"`
	Cards       []GoalReportCard       `json:"cards"`
	Decisions   []GoalLogLine          `json:"decisions,omitempty"`
	Declined    []GoalLogLine          `json:"declined_findings,omitempty"`
	Found       []GoalLogLine          `json:"found_along_the_way,omitempty"`
	// Unread lists notes that reached the goal after its last lead turn.
	// A note is delivered to the conductor, which reads it at implement —
	// so one that arrives while the goal is reviewing, verifying or
	// already ready for you has nobody left to read it. It is not lost,
	// but it was not acted on either, and the hand-over is where that has
	// to be said rather than left in the doc for nobody.
	Unread []GoalLogLine `json:"unread_notes,omitempty"`
	TryIt  string        `json:"try_it,omitempty"`
}

// GoalReportBudget is the budget tree at the hand-over.
type GoalReportBudget struct {
	Envelope  int     `json:"envelope"`
	Own       float64 `json:"goal_spend"`
	Given     float64 `json:"held_by_cards"`
	CardSpent float64 `json:"card_spend"`
	Reserve   int     `json:"reserve"`
	Available float64 `json:"left_to_give"`
	Total     float64 `json:"total_spend"`
	// Substrate is the goal's other ledger, absent when none was agreed.
	Substrate *GoalReportSubstrate `json:"substrate,omitempty"`
}

// GoalReportSubstrate is the substrate budget at the hand-over: what was
// agreed, what the goal's runs spent, and what is held back for the runs it
// needs in order to be judged.
type GoalReportSubstrate struct {
	Runs         int     `json:"runs"`
	Minutes      int     `json:"minutes"`
	RunsSpent    int     `json:"runs_spent"`
	MinutesSpent float64 `json:"minutes_spent"`
	ReserveRuns  int     `json:"reserve_runs"`
}

// Met counts the met done-when items.
func (r GoalReport) Met() (met, total int) {
	for _, d := range r.DoneWhen {
		if d.Status == DoneWhenMet {
			met++
		}
	}
	return met, len(r.DoneWhen)
}

var judgedLineRe = regexp.MustCompile(`(?mi)^\s*[-*]?\s*(DW-[0-9]+)\s*[:—-]+\s*(met|not met)\b[\s:—-]*(.*)$`)

// GoalReport builds a goal's hand-over from its view, its log and its doc.
func (e *Engine) GoalReport(ctx context.Context, goalID domain.FeatureID) (GoalReport, error) {
	view, err := e.GoalView(ctx, goalID)
	if err != nil {
		return GoalReport{}, err
	}
	r := buildGoalReport(view)
	// The repositories the goal has a branch in, and which of them already
	// have it: a landing that stopped half-way through a goal that spans
	// repositories is a state the reader has to be able to see.
	trees, terr := e.goalTrees(ctx, view.Goal)
	if terr == nil {
		byRepo := map[string]*worktree.Manager{}
		for _, t := range trees {
			landed, _ := t.Landed(ctx)
			r.Repos = append(r.Repos, GoalReportRepo{Name: t.Repo, Home: t.Home, Branch: t.Branch(), Landed: landed})
			byRepo[t.Repo] = t.Manager()
		}
		// the diff by card: each landed card's commit, summarized, read in
		// the repository the card is in
		for i, c := range r.Cards {
			if c.Commit == "" {
				continue
			}
			mgr := byRepo[c.Repo]
			if mgr == nil {
				continue
			}
			if stat, serr := mgr.CommitStat(ctx, c.Commit); serr == nil {
				_, body, _ := strings.Cut(stat, "\n")
				r.Cards[i].Stat = strings.TrimSpace(body)
			}
		}
	}
	return r, nil
}

func buildGoalReport(v GoalView) GoalReport {
	g := v.Goal
	r := GoalReport{
		ID: g.ID, Title: g.Title, Stage: g.Stage, Partial: g.Goal.Partial,
		Ready:      g.Stage == domain.StageVerify && !g.VerifiedAt.IsZero(),
		WrappingUp: g.Goal.WrappingUp(), Lanes: g.Goal.LaneCount(),
		NeedsBudget: v.NeedsBudget,
	}
	if v.Substrate.Agreed() {
		r.Budget.Substrate = &GoalReportSubstrate{
			Runs: v.Substrate.Runs, Minutes: v.Substrate.Minutes, RunsSpent: v.Substrate.RunsSpent,
			MinutesSpent: v.Substrate.MinutesSpent, ReserveRuns: goalpolicy.ReserveRuns,
		}
	}
	r.NeedsSubstrate = v.NeedsSubstrate
	if g.Stage == domain.StageImplement {
		for _, c := range v.Cards {
			if c.State == goalpolicy.Blocked {
				r.WaitingOn = fmt.Sprintf("%s cannot be verified in this environment: %s", c.Feature.ID, c.Reason)
				break
			}
		}
	}
	r.Budget = GoalReportBudget{
		Envelope: v.Ledger.Envelope, Own: v.Ledger.Own, Given: v.Ledger.Given,
		Reserve: v.Ledger.Reserve, Available: max(0, v.Ledger.Available),
	}

	landedSubject := map[domain.FeatureID]string{}
	var lastChecks []goalCheckResult
	notMet := map[string]string{}
	type seqNote struct {
		seq  int64
		line GoalLogLine
	}
	var notes []seqNote
	var lastLeadSeq int64
	for _, en := range v.Log {
		switch en.Action {
		case state.GoalLeadTurn:
			lastLeadSeq = en.Seq
		case state.GoalNote:
			notes = append(notes, seqNote{en.Seq, GoalLogLine{Detail: en.Detail, By: en.By}})
		case state.GoalLanded:
			landedSubject[en.Card] = en.Detail
		case state.GoalChecks:
			var rs []goalCheckResult
			if json.Unmarshal([]byte(en.Detail), &rs) == nil {
				lastChecks = rs
			}
			// a check that passes after an item was marked not met settles it
			for _, d := range v.DoneWhen {
				for _, c := range rs {
					if c.OK && (d.Check != "" || d.Experiment != "") && c.Name == d.CheckName() {
						delete(notMet, d.ID)
					}
				}
			}
		case state.GoalNotMet:
			notMet[en.Item] = en.Detail
		case state.GoalCheckFixed:
			// the not-met and the last result were about the old command;
			// the repaired one is judged by the next check run
			delete(notMet, en.Item)
			lastChecks = slices.DeleteFunc(slices.Clone(lastChecks), func(c goalCheckResult) bool {
				return c.Name == domain.DoneWhen{ID: en.Item}.CheckName()
			})
		case state.GoalDecision:
			r.Decisions = append(r.Decisions, GoalLogLine{Ref: en.DecisionRef(), Card: en.Card, Item: en.Item, Detail: en.Detail, Alternative: en.Alternative, By: en.By})
		case state.GoalDeclined:
			r.Declined = append(r.Declined, GoalLogLine{Card: en.Card, Finding: en.Ref, Detail: en.Detail, By: en.By})
		case state.GoalFound:
			r.Found = append(r.Found, GoalLogLine{Card: en.Card, Detail: en.Detail, By: en.By})
		}
	}

	servedBy := map[string][]GoalCard{}
	for _, c := range v.Cards {
		rc := GoalReportCard{
			ID: c.Feature.ID, Kind: c.Feature.Kind, Repo: c.Feature.Repo, Title: c.Feature.Title, State: c.State.String(),
			Stage: c.Feature.Stage, Envelope: c.Envelope, Spent: c.Spent, Serves: c.Serves,
			Reason: c.Reason, Attached: c.Feature.GoalAttached,
		}
		if c.State == goalpolicy.Landed {
			rc.Commit = c.Feature.LandedSHA
			if s := landedSubject[c.Feature.ID]; s != "" {
				_, subj, _ := strings.Cut(s, " ")
				rc.Subject = subj
			}
		}
		r.Budget.CardSpent += c.Spent
		r.Cards = append(r.Cards, rc)
		for _, s := range c.Serves {
			servedBy[s] = append(servedBy[s], c)
		}
	}
	r.Budget.Total = r.Budget.Own + r.Budget.CardSpent
	for _, n := range notes {
		if n.seq > lastLeadSeq {
			r.Unread = append(r.Unread, n.line)
		}
	}

	checkByName := map[string]goalCheckResult{}
	for _, c := range lastChecks {
		checkByName[c.Name] = c
	}
	verification, tryIt := "", ""
	if v.DocPath != "" {
		if raw, err := os.ReadFile(v.DocPath); err == nil {
			verification, _ = spec.ViewSection(string(raw), spec.GoalSectionVerify)
			tryIt, _ = spec.ViewSection(string(raw), spec.GoalSectionTryIt)
		}
	}
	liveProof := map[string]goalItemResult{}
	for _, p := range experimentCheckResults(v.DoneWhen, v.Experiments) {
		liveProof[p.Item.ID] = p
	}
	for _, x := range v.Experiments {
		r.Experiments = append(r.Experiments, reportExperiment(x))
	}
	judged := map[string][2]string{}
	for _, m := range judgedLineRe.FindAllStringSubmatch(verification, -1) {
		judged[strings.ToUpper(m[1])] = [2]string{strings.ToLower(m[2]), strings.TrimSpace(m[3])}
	}
	r.TryIt = strings.TrimSpace(stripPrompt(tryIt))

	for _, d := range v.DoneWhen {
		st := DoneWhenStatus{ID: d.ID, Says: d.Says, How: "judged", Status: DoneWhenUnknown}
		if d.Experiment != "" {
			st.How = "experiment: " + d.Experiment
			if len(d.Assertions) > 0 {
				st.How += " [" + strings.Join(d.Assertions, ", ") + "]"
			}
			if res, ok := checkByName[d.CheckName()]; ok {
				// what the goal's verify read — the evidence it was judged on
				if res.OK {
					st.Status = DoneWhenMet
				} else {
					st.Status = DoneWhenNotMet
				}
				st.Evidence = firstNonEmpty(res.Evidence, res.Status)
			} else if live, ok := liveProof[d.ID]; ok && live.Known {
				// not judged yet: what the runs say so far
				st.Status = DoneWhenNotMet
				if live.Held {
					st.Status = DoneWhenMet
				}
				st.Evidence = live.Detail
			} else if ok {
				st.Evidence = live.Detail
			}
		} else if d.Check != "" {
			st.How = "check: " + d.Check
			if res, ok := checkByName[d.CheckName()]; ok {
				if res.OK {
					st.Status, st.Evidence = DoneWhenMet, "check passed"
				} else {
					st.Status, st.Evidence = DoneWhenNotMet, "check "+res.Status
				}
			}
		} else if j, ok := judged[d.ID]; ok {
			st.Status, st.Evidence = j[0], j[1]
		}
		if cards := servedBy[d.ID]; len(cards) > 0 && st.Status != DoneWhenMet {
			allDropped := true
			for _, c := range cards {
				if c.State != goalpolicy.Dropped {
					allDropped = false
				}
			}
			if allDropped {
				st.Status, st.Evidence = DoneWhenNotMet, "every card serving it was dropped"
			}
			// An item whose card is waiting on money is not one the goal
			// gave up on: say so, so a reader tops up rather than reading
			// the result as finished.
			if !allDropped && r.NeedsBudget.Waiting() {
				for _, c := range cards {
					if c.Feature.ID == r.NeedsBudget.Card {
						st.Status = DoneWhenWaiting
						st.Evidence = r.NeedsBudget.Reason
					}
				}
			}
		}
		if st.Status != DoneWhenMet && st.Status != DoneWhenWaiting {
			for _, c := range servedBy[d.ID] {
				if c.State == goalpolicy.Blocked {
					st.Status, st.Evidence = DoneWhenBlocked, string(c.Feature.ID)+": "+c.Reason
					break
				}
			}
		}
		if why, ok := notMet[d.ID]; ok {
			st.Status, st.Evidence = DoneWhenNotMet, why
		}
		r.DoneWhen = append(r.DoneWhen, st)
	}
	return r
}

// stripPrompt drops the template's %% prompt lines from a section body.
func stripPrompt(body string) string {
	var keep []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "%%") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// RenderGoalReport renders a hand-over as the goal doc's Report section.
func RenderGoalReport(r GoalReport) string {
	var b strings.Builder
	met, total := r.Met()
	state := "ready for you"
	switch {
	case r.NeedsBudget.Waiting():
		// Not "ready": work remains and a top-up continues it. Saying
		// ready here would report an unfinished result as a finished one.
		state = "waiting for you — " + r.NeedsBudget.Reason
	case r.NeedsSubstrate.Waiting():
		state = "waiting for you — " + r.NeedsSubstrate.Reason
	case r.WaitingOn != "":
		state = "waiting on an environment — " + r.WaitingOn
	case r.Partial != "":
		state = "ready for you — partial: " + r.Partial
	}
	fmt.Fprintf(&b, "%s. %d of %d done-when items met.\n\n", capitalize(state), met, total)
	if r.NeedsSubstrate.Waiting() && !r.NeedsBudget.Waiting() {
		fmt.Fprintf(&b, "Nothing was dropped. Raise the goal's substrate budget and it makes the run and carries on:\n\n```sh\ngummi resume %s --runs <more> --minutes <more>\n```\n\n", r.ID)
	}
	if r.WaitingOn != "" && !r.NeedsBudget.Waiting() {
		fmt.Fprintf(&b, "Nothing was dropped and nothing was judged. Once the environment can run the card's verification plan, pick the goal back up and it verifies the card again:\n\n```sh\ngummi resume %s --autonomous\n```\n\n", r.ID)
	}
	if r.NeedsBudget.Waiting() {
		fmt.Fprintf(&b, "Raise the goal's envelope and send it back, and %s carries on from where it stopped:\n\n```sh\ngummi resume %s --envelope <more than %d>\n```\n\n",
			r.NeedsBudget.Card, r.ID, r.Budget.Envelope)
	}

	b.WriteString("### Done when\n\n")
	for _, d := range r.DoneWhen {
		mark := "✗"
		switch d.Status {
		case DoneWhenMet:
			mark = "✓"
		case DoneWhenUnknown:
			mark = "?"
		case DoneWhenWaiting, DoneWhenBlocked:
			mark = "…"
		}
		fmt.Fprintf(&b, "- %s %s — %s (%s)", mark, d.ID, d.Says, d.Status)
		if d.Evidence != "" {
			b.WriteString(": " + d.Evidence)
		}
		b.WriteString("\n")
	}

	if len(r.Experiments) > 0 {
		b.WriteString("\n### Experiments\n\n")
		for _, x := range r.Experiments {
			fmt.Fprintf(&b, "- **%s** on %s, proving %s — ", x.Name, x.Substrate, strings.Join(x.Items, ", "))
			switch {
			case x.Problem != "":
				b.WriteString("cannot be run: " + x.Problem)
			case x.Outcome == "" && x.Running != "":
				fmt.Fprintf(&b, "run %s is in flight", x.Running)
			case x.Outcome == "":
				b.WriteString("no conclusive run is about the goal's current heads")
			default:
				fmt.Fprintf(&b, "%s (run %s", x.Outcome, x.Run)
				if x.Total > 0 {
					fmt.Fprintf(&b, ", %d of %d assertions held", x.Passed, x.Total)
				}
				fmt.Fprintf(&b, "); evidence in `%s`", x.Evidence)
			}
			// how far the rig can be believed, in the same breath as what it said
			fmt.Fprintf(&b, "\n  %d run(s): %d conclusive, %d judged nothing", x.Runs, x.Conclusive, x.Inconclusive)
			if x.Flaky > 0 {
				fmt.Fprintf(&b, " (%d of them a failure that did not reproduce)", x.Flaky)
			}
			fmt.Fprintf(&b, "; %.0f substrate minutes\n", x.Minutes)
			for _, a := range x.Assertions {
				if !a.OK {
					fmt.Fprintf(&b, "  - ✗ %s %s\n", a.ID, a.Detail)
				}
			}
		}
	}

	// One repository is the ordinary case and says nothing worth a
	// section; several is the thing a reader has to know, because the
	// goal then lands once in each and can be in some and not others.
	if len(r.Repos) > 1 {
		b.WriteString("\n### Repositories\n\n")
		for _, rp := range r.Repos {
			where := "not landed yet"
			if rp.Landed {
				where = "landed"
			}
			fmt.Fprintf(&b, "- %s — `%s`, %s", repoName(rp.Name), rp.Branch, where)
			if rp.Home {
				b.WriteString(" (the goal's own)")
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n### Cards\n\n")
	for _, c := range r.Cards {
		fmt.Fprintf(&b, "- %s %s — %s", c.ID, c.Title, c.State)
		if len(r.Repos) > 1 && c.Repo != "" {
			fmt.Fprintf(&b, " in %s", c.Repo)
		}
		if c.Commit != "" {
			fmt.Fprintf(&b, " as %s", shortSHA(c.Commit))
		}
		if c.Reason != "" && c.State != "landed" {
			b.WriteString(": " + c.Reason)
		}
		fmt.Fprintf(&b, " · %.0f of %d credits\n", c.Spent, c.Envelope)
	}

	var stats []GoalReportCard
	for _, c := range r.Cards {
		if c.Stat != "" {
			stats = append(stats, c)
		}
	}
	if len(stats) > 0 {
		b.WriteString("\n### Diff by card\n\n")
		for _, c := range stats {
			fmt.Fprintf(&b, "%s %s — `git show %s`\n\n```\n%s\n```\n\n", c.ID, c.Title, shortSHA(c.Commit), c.Stat)
		}
	}

	if len(r.Decisions) > 0 {
		b.WriteString("\n### Decisions for review\n\n")
		for _, d := range r.Decisions {
			fmt.Fprintf(&b, "- %s %s", d.Ref, d.Detail)
			if d.Alternative != "" {
				b.WriteString(" (not: " + d.Alternative + ")")
			}
			if d.Card != "" {
				b.WriteString(" — " + string(d.Card))
			}
			b.WriteString("\n")
		}
	}
	if len(r.Declined) > 0 {
		b.WriteString("\n### Declined findings\n\n")
		for _, d := range r.Declined {
			fmt.Fprintf(&b, "- %s: %s — declined: %s\n", d.Card, d.Finding, d.Detail)
		}
	}
	if len(r.Unread) > 0 {
		b.WriteString("\n### Notes nobody read\n\n")
		b.WriteString("These arrived after the goal's last lead turn, so nothing acted on them:\n\n")
		for _, n := range r.Unread {
			fmt.Fprintf(&b, "- %s\n", n.Detail)
		}
	}
	if len(r.Found) > 0 {
		b.WriteString("\n### Found along the way\n\n")
		for _, d := range r.Found {
			fmt.Fprintf(&b, "- %s %s\n", d.Card, d.Detail)
		}
	}
	fmt.Fprintf(&b, "\n### Spend\n\n- budget %d · goal %.0f · cards %.0f · total %.0f · reserve %d\n",
		r.Budget.Envelope, r.Budget.Own, r.Budget.CardSpent, r.Budget.Total, r.Budget.Reserve)
	if sb := r.Budget.Substrate; sb != nil {
		fmt.Fprintf(&b, "- substrate: %d of %s runs · %.0f of %s minutes · %d run(s) held back for being judged\n",
			sb.RunsSpent, orUnbounded(sb.Runs), sb.MinutesSpent, orUnbounded(sb.Minutes), sb.ReserveRuns)
	}
	return b.String()
}

// orUnbounded spells a ceiling, or says there is none in that dimension.
func orUnbounded(n int) string {
	if n <= 0 {
		return "unbounded"
	}
	return fmt.Sprint(n)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// WriteGoalReport writes a goal's hand-over into its doc's Report section.
// Best-effort: the report is a rendering of facts the store already holds.
func (e *Engine) WriteGoalReport(ctx context.Context, goalID domain.FeatureID) (GoalReport, error) {
	r, err := e.GoalReport(ctx, goalID)
	if err != nil {
		return r, err
	}
	goal, err := e.cfg.Store.GetFeature(ctx, goalID)
	if err != nil {
		return r, err
	}
	path := e.artifactFile(&goal)
	if path == "" {
		return r, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	doc, _, err := spec.ReplaceSection(string(raw), spec.GoalSectionReport, RenderGoalReport(r))
	if err != nil {
		return r, err
	}
	return r, atomicfile.Write(path, []byte(doc), 0o600)
}
