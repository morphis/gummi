package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/worktree"
)

// runStatus implements `gummi status <id|ref> [--json]` (DESIGN §3): a
// read-only snapshot of a feature's stage, gate blockers, spend/envelope,
// and branch state. --json is the skill's machine-readable path. It drives
// nothing and holds no lock, so it is safe to poll a running feature.
func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	jsonOut := registerStatusFlags(fs)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi status <id|ref> [--json]")
		fs.PrintDefaults()
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	return withReadWorkspace(func(ctx context.Context, store *state.Store, wt *worktree.Pool, ws state.Workspace) error {
		f, err := resolveFeatureID(ctx, store, idArg)
		if err != nil {
			return err
		}
		view := buildStatus(ctx, store, wt, ws, &f)
		if *jsonOut {
			b, err := json.MarshalIndent(view, "", "  ")
			if err != nil {
				return err
			}
			fmt.Println(string(b))
			return nil
		}
		renderStatus(os.Stdout, view)
		return nil
	})
}

// registerStatusFlags binds `gummi status`'s only flag onto fs and returns
// its pointer, so the skill's grammar generator can enumerate it alongside
// the run/resume flag sets (see runFlagValues).
func registerStatusFlags(fs *flag.FlagSet) *bool {
	return fs.Bool("json", false, "emit machine-readable JSON instead of the text summary")
}

// statusView is the status command's payload — the JSON schema the skill
// parses, and the source of the text summary.
type statusView struct {
	ID    string `json:"id"`
	Ref   string `json:"ref,omitempty"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Stage string `json:"stage"`
	// Route is wire-only: a constant since the stage merge, kept for a
	// consumer still parsing it and never rendered by renderStatus. See
	// buildStatus.
	Route       string         `json:"route,omitempty"`
	Blockers    statusBlockers `json:"blockers"`
	Spend       statusSpend    `json:"spend"`
	Branch      string         `json:"branch"`
	BranchState string         `json:"branch_state"`
	// Verified is true once the verify gate has passed and the branch is
	// ready to land — the headless driver's stop-at-verified terminal state.
	// Distinct from Done (== merged): a CI caller polls `verified` to know a
	// run reached its verified branch, since the headless driver never merges.
	Verified bool `json:"verified"`
	Done     bool `json:"done"`
	// Running reports whether the pid recorded at this card's pid file
	// (.gummi/state/locks/<id>.pid) is still alive — a live run or resume
	// driving this specific card. Meant for an orchestrating agent whose
	// bash wrapper was killed by the harness:
	// gummi's SIGHUP-ignore makes it survive the hangup, so the wrapper's
	// death is not gummi's death. A caller that sees running=true should wait
	// (or attach to the events.jsonl mirror) instead of retrying, which would
	// hit ErrLocked and look like a fresh failure.
	Running bool `json:"running"`
	// PullRequest mirrors the linked PullRequestRef verbatim (repo, number,
	// url, head_sha) when the card is linked; absent otherwise. Never a live
	// gh call — the stored ref only.
	PullRequest any `json:"pull_request,omitempty"`
	// PullRequestLine is the plain-text `pr: owner/repo#N` render, never
	// marshaled into the JSON view.
	PullRequestLine string `json:"-"`
	// Escalation names what the card is waiting on a person for, absent
	// when nothing is. Without it a caller that finds verified:false and
	// running:false has to parse the event stream to learn why the card
	// stopped — which is the question a JSON status endpoint exists to
	// answer. When several decisions are open it is the newest: the one
	// that stopped the card most recently.
	Escalation *statusEscalation `json:"escalation,omitempty"`
	// Rounds is the automatic round counters, per loop. A review bounce
	// costs an entire second implement pass and is the most expensive
	// single event in the workflow, so "how many times did this bounce"
	// belongs beside the spend rather than in the event log.
	Rounds statusRounds `json:"rounds"`
	// ExcusedChecks names the repo checks that were already failing on
	// this card's fresh branch. Verify writes those off as pre-existing
	// and does not floor the verdict for them — only regressions count —
	// so a check listed here gated nothing on this card, and "verified"
	// above is a pass that did not cover it. Absent when the branch was
	// born clean, which is the ordinary case.
	ExcusedChecks []string `json:"excused_checks,omitempty"`
	// StageSpend is where the money went, per stage/role/model, largest
	// first. It is what makes a reviewer's bounces answerable: a run total
	// hides a stage that doubled. Note it is per stage, NOT per round —
	// stage_spend is keyed (feature, stage, model, role) and accumulates
	// across rounds, so a bounced implement stage reports both passes as
	// one figure. Rounds above is what tells you how many passes that is.
	StageSpend []statusStageSpend `json:"stage_spend,omitempty"`
}

// statusEscalation is the open decision that parked the card, in the
// words it was raised with.
type statusEscalation struct {
	// Kind is the decision vocabulary: gate, ask, verify, conflict,
	// budget, idle.
	Kind string `json:"kind"`
	// Reason is the question verbatim — what the card is waiting for.
	Reason string `json:"reason"`
	// Stage is the stage the card was waiting in when it was raised.
	Stage string `json:"stage"`
	At    string `json:"at"`
}

// statusRounds carries each round kind's persisted counter. Plan and
// review are the two loops with their own caps; corrective is the unified
// budget across everything that bounces work back.
type statusRounds struct {
	Plan       int `json:"plan"`
	Review     int `json:"review"`
	Corrective int `json:"corrective"`
}

// statusStageSpend is one row of the per-stage cost breakdown.
type statusStageSpend struct {
	Stage        string  `json:"stage"`
	Role         string  `json:"role"`
	Model        string  `json:"model"`
	Credits      float64 `json:"credits"`
	InputTokens  int64   `json:"input_tok"`
	CachedTokens int64   `json:"cached_tok"`
	OutputTokens int64   `json:"output_tok"`
}

type statusBlockers struct {
	OpenQuestions int `json:"open_questions"`
	OpenDiff      int `json:"open_diff"`
}

type statusSpend struct {
	Credits  float64 `json:"credits"`
	Envelope int     `json:"envelope"`
}

// buildStatus assembles the view from the store, the artifact, and the
// worktree manager. Blocker counts mirror the gate floor; branch state is a
// best-effort read (each git query is guarded, so a not-yet-created branch
// or worktree simply reads as "none").
func buildStatus(ctx context.Context, store *state.Store, wt *worktree.Pool, ws state.Workspace, f *domain.Feature) statusView {
	kind := f.Kind
	if kind == "" {
		kind = domain.KindFeature
	}
	// One workflow, so one route: this is a constant, "full" for a
	// feature and "" for everything else. It is deliberately wire-only —
	// still marshaled for a consumer that reads the JSON schema, never
	// printed in the human summary, where a line that cannot vary would
	// spend one of twelve saying nothing, contradict the README's "no
	// routes", and give features and bugs differently shaped output for
	// a reason a user cannot see.
	route := ""
	if kind == domain.KindFeature {
		route = "full"
	}
	sq, dq := gateBlockers(ctx, store, wt, ws, f)
	return statusView{
		ID:              string(f.ID),
		Ref:             f.ExternalRef,
		Kind:            string(kind),
		Title:           f.Title,
		Stage:           string(f.Stage),
		Route:           route,
		Blockers:        statusBlockers{OpenQuestions: sq, OpenDiff: dq},
		Spend:           statusSpend{Credits: f.Spend.Credits, Envelope: f.Budget.Envelope},
		Branch:          f.BranchName(),
		BranchState:     branchState(ctx, wt, f),
		Verified:        !f.VerifiedAt.IsZero(),
		Done:            f.Stage == domain.StageDone,
		Running:         state.ProcessAlive(state.ReadPIDFile(ws.PIDFile(f.ID))),
		PullRequest:     f.PullRequest.StatusPayload(),
		PullRequestLine: f.PullRequest.PlainLine(),
		Escalation:      openEscalation(ctx, store, f),
		Rounds:          roundCounts(ctx, store, f),
		ExcusedChecks:   excusedChecks(ctx, store, f),
		StageSpend:      stageSpendRows(ctx, store, f),
	}
}

// openEscalation reads the newest still-open decision on the card — the
// reason it is waiting on a person — or nil when nothing is. Every read
// here degrades to nil rather than failing the status: a caller polling a
// running card must still get its stage and spend when the decision scan
// is unreadable.
func openEscalation(ctx context.Context, store *state.Store, f *domain.Feature) *statusEscalation {
	byCard, err := store.OpenDecisions(ctx)
	if err != nil {
		return nil
	}
	open := byCard[f.ID]
	if len(open) == 0 {
		return nil
	}
	// OpenDecisions reports oldest first; the newest is the one that
	// stopped the card most recently, which is what a driver asking "why
	// is it not running" wants named.
	d := open[len(open)-1]
	return &statusEscalation{
		Kind:   d.Kind,
		Reason: d.Question,
		Stage:  string(d.Stage),
		At:     d.At.UTC().Format(time.RFC3339),
	}
}

// roundCounts reads the three persisted round counters. A store error on
// any one of them reads as 0 — the same degradation the rest of the view
// takes, and the honest value for a counter that was never written.
func roundCounts(ctx context.Context, store *state.Store, f *domain.Feature) statusRounds {
	n := func(k domain.RoundKind) int {
		c, err := store.Rounds(ctx, f.ID, k)
		if err != nil {
			return 0
		}
		return c
	}
	return statusRounds{
		Plan:       n(domain.RoundKindPlan),
		Review:     n(domain.RoundKindReview),
		Corrective: n(domain.RoundKindCorrective),
	}
}

// excusedChecks names the repo checks verify will write off as
// pre-existing for this card, read straight off the stored baseline. A
// store error reads as none: status must still answer for a card whose
// baseline is unreadable, and the honest fallback is to claim no
// carve-out rather than to invent one.
func excusedChecks(ctx context.Context, store *state.Store, f *domain.Feature) []string {
	baseline, err := store.CheckBaseline(ctx, f.ID)
	if err != nil {
		return nil
	}
	return state.ExcusedChecks(baseline)
}

// stageSpendRows projects the store's per-stage breakdown into the view,
// largest first so the stage that dominates a run reads at the top. Ties
// break on stage then role, so the order is stable across calls.
func stageSpendRows(ctx context.Context, store *state.Store, f *domain.Feature) []statusStageSpend {
	rows, err := store.StageBreakdown(ctx, f.ID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := make([]statusStageSpend, 0, len(rows))
	for _, r := range rows {
		out = append(out, statusStageSpend{
			Stage: string(r.Stage), Role: r.Role, Model: r.Model,
			Credits:     r.Credits,
			InputTokens: r.InputTokens, CachedTokens: r.CachedTokens, OutputTokens: r.OutputTokens,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Credits != out[j].Credits {
			return out[i].Credits > out[j].Credits
		}
		if out[i].Stage != out[j].Stage {
			return out[i].Stage < out[j].Stage
		}
		return out[i].Role < out[j].Role
	})
	return out
}

// branchState collapses the worktree manager's branch queries into one
// word: none (no branch yet), created (branch exists, no commits of its
// own), ahead (has commits not on main — the verified-branch state), or
// landed (already merged). Any query error degrades to the safe "none".
func branchState(ctx context.Context, wt *worktree.Pool, f *domain.Feature) string {
	exists, err := wt.BranchExists(ctx, f)
	if err != nil || !exists {
		return "none"
	}
	if landed, err := wt.Landed(ctx, f); err == nil && landed {
		return "landed"
	}
	if ahead, err := wt.BranchAhead(ctx, f); err == nil && ahead {
		return "ahead"
	}
	return "created"
}

// renderStatus prints the human-readable summary.
func renderStatus(w io.Writer, v statusView) {
	fmt.Fprintf(w, "%s  %s\n", v.ID, v.Title)
	fmt.Fprintf(w, "  Kind:     %s\n", v.Kind)
	fmt.Fprintf(w, "  Stage:    %s\n", v.Stage)
	fmt.Fprintf(w, "  Branch:   %s  (%s)\n", v.Branch, v.BranchState)
	fmt.Fprintf(w, "  Verified: %s\n", yesNo(v.Verified))
	// Directly under Verified because it qualifies it: these checks were
	// red before the card touched anything, so verify excused them and
	// the pass above says nothing about them. Printed only when there
	// are any — the ordinary clean-baseline card should not carry a line
	// about a carve-out that did not apply to it.
	if len(v.ExcusedChecks) > 0 {
		fmt.Fprintf(w, "  Excused:  %s (already failing on the fresh branch; not gated at verify)\n",
			strings.Join(v.ExcusedChecks, ", "))
	}
	fmt.Fprintf(w, "  Running:  %s\n", yesNo(v.Running))
	fmt.Fprintf(w, "  Spend:    %s / %d credits\n", trimCredits(v.Spend.Credits), v.Spend.Envelope)
	// continuation lines under Spend: the breakdown is the same figure
	// taken apart, not a second one.
	for _, sp := range v.StageSpend {
		fmt.Fprintf(w, "            %-9s %-11s %8s  %s\n",
			sp.Stage, sp.Role, trimCredits(sp.Credits), sp.Model)
	}
	fmt.Fprintf(w, "  Blockers: %d open question(s) · %d open diff comment(s)\n", v.Blockers.OpenQuestions, v.Blockers.OpenDiff)
	if r := v.Rounds; r.Plan > 0 || r.Review > 0 || r.Corrective > 0 {
		fmt.Fprintf(w, "  Rounds:   plan %d · review %d · corrective %d\n", r.Plan, r.Review, r.Corrective)
	}
	if e := v.Escalation; e != nil {
		fmt.Fprintf(w, "  Waiting:  [%s at %s] %s\n", e.Kind, e.Stage, firstLine(e.Reason))
	}
	if v.Ref != "" {
		fmt.Fprintf(w, "  Ref:      %s\n", v.Ref)
	}
	if v.PullRequestLine != "" {
		fmt.Fprintf(w, "  pr: %s\n", v.PullRequestLine)
	}
}

// yesNo renders a boolean status line as the human-friendly yes/no the
// text summary uses.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// trimCredits formats a credit figure without a trailing ".0" for whole
// numbers, so the common integer case reads cleanly.
func trimCredits(c float64) string {
	if c == float64(int64(c)) {
		return fmt.Sprintf("%d", int64(c))
	}
	return fmt.Sprintf("%.2f", c)
}
