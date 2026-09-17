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
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/spec"
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
	// Done means the card is CLOSED, not that anything merged. It used to
	// mean both, because the only way out of verify was a squash merge;
	// two of the three endings a verified card now has reach done without
	// gummi merging anything (a landing on GitHub, and a hand-off). A
	// caller that wants "is this on the base branch" reads branch_state
	// ("landed"), not this.
	Done bool `json:"done"`
	// HandedOff is true when the card was closed with its branch
	// deliberately left unlanded — the third ending, where the caller owns
	// whatever happens to the branch next. A done card is landed, handed
	// off, or ended through its PR; this is the one of the three the store
	// records directly.
	HandedOff bool `json:"handed_off"`
	// Dropped is true when this card's goal dropped it: it was minted for
	// the goal, never finished, and closed when the goal wrapped up. The
	// store closes such a card the way a hand-off closes one — branch
	// kept, nothing landed — so HandedOff is true for it too, and a caller
	// counting hand-offs used to count a card that never ran as completed
	// work. On the lxd autopilot drive BG-003 reported `done: true,
	// handed_off: true` with 0 credits spent and no branch at all.
	Dropped bool `json:"dropped,omitempty"`
	// Running reports whether something is currently driving this card:
	// either the pid recorded at this card's pid file
	// (.gummi/state/locks/<id>.pid) is still alive — a headless run/resume
	// — or, since status is its own process and cannot see another
	// process's in-memory session, some other gummi process (a headless
	// drive, or an open TUI board — see state.CardLocks) holds the card's
	// exclusive lock right now. That lock is held for the session's whole
	// life, including while it sits paused on an open ask, so a card
	// waiting on a person (see Escalation) still reads running=true rather
	// than contradicting the Waiting line. See cardRunning: an
	// inconclusive lock probe also reads true, so this never asserts "no"
	// about a card it has no way to rule out.
	//
	// Meant for an orchestrating agent whose bash wrapper was killed by the
	// harness: gummi's SIGHUP-ignore makes it survive the hangup, so the
	// wrapper's death is not gummi's death. A caller that sees running=true
	// should wait (or attach to the events.jsonl mirror) instead of
	// retrying, which would hit ErrLocked and look like a fresh failure.
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
	// UnprovenFiles names the files this branch changed that no check
	// that ran exercised — verify's own declaration, read back off the
	// artifact. A cgo tree this container cannot build, a suite only CI
	// runs, a generated file: all legitimate, and all invisible until
	// now, because the admission lived in the artifact's prose while the
	// verdict said only "verified". It qualifies `verified` the same way
	// excused_checks does, and for the same reason: the pass did not
	// cover these. Absent when verify declared none.
	UnprovenFiles []statusUnprovenFile `json:"unproven_files,omitempty"`
	// StageSpend is where the money went, per stage/role/model, largest
	// first. It is what makes a reviewer's bounces answerable: a run total
	// hides a stage that doubled. Note it is per stage, NOT per round —
	// stage_spend is keyed (feature, stage, model, role) and accumulates
	// across rounds, so a bounced implement stage reports both passes as
	// one figure. Rounds above is what tells you how many passes that is.
	StageSpend []statusStageSpend `json:"stage_spend,omitempty"`
	// GoalID is the goal this card belongs to; FoundBy the goal that filed
	// it as found along the way. Both absent on an ordinary card.
	GoalID  string `json:"goal_id,omitempty"`
	FoundBy string `json:"found_by,omitempty"`
	// Goal is a goal's hand-over, as it stands now: its budget tree, its
	// done-when items and their status, its cards, the decisions for
	// review, declined findings and what it found along the way. `ready`
	// inside it is true when the goal is waiting for you. Absent for every
	// other kind.
	Goal *engine.GoalReport `json:"goal,omitempty"`
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

// statusRounds carries each round kind's persisted counter.
//
// Plan and review are LIVE budgets: each is cleared when its loop crosses
// its gate, so on a card past that gate they read 0 no matter how many
// times the loop ran. Corrective is the card's cumulative rework — every
// pass it was sent back to do again — and it is never reset mid-card, so
// it is the only one of the three that answers "how much was redone".
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
		HandedOff:       f.HandedOff(),
		Dropped:         f.GoalDropped(),
		Running:         cardRunning(ws, f.ID),
		PullRequest:     f.PullRequest.StatusPayload(),
		PullRequestLine: f.PullRequest.PlainLine(),
		Escalation:      openEscalation(ctx, store, f),
		Rounds:          roundCounts(ctx, store, f),
		ExcusedChecks:   excusedChecks(ctx, store, f),
		UnprovenFiles:   unprovenFiles(f),
		StageSpend:      stageSpendRows(ctx, store, f),
		GoalID:          string(f.GoalID),
		FoundBy:         string(f.FoundBy),
		Goal:            goalReport(ctx, store, wt, ws, f),
	}
}

// goalReport reads a goal's hand-over through an agent-less engine — the
// goal view is the engine's, and status runs none of its sessions. Nil for
// any other kind, or when the report cannot be read.
func goalReport(ctx context.Context, store *state.Store, wt *worktree.Pool, ws state.Workspace, f *domain.Feature) *engine.GoalReport {
	if !f.IsGoal() {
		return nil
	}
	eng := engine.New(engine.Config{Store: store, Pool: wt, Workspace: ws})
	defer eng.Close()
	r, err := eng.GoalReport(ctx, f.ID)
	if err != nil {
		return nil
	}
	return &r
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

// statusUnprovenFile is one file verify declared no check exercised.
type statusUnprovenFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
}

// unprovenFiles reads verify's UNPROVEN declarations off the card's
// artifact. An unreadable artifact reports none: status must still answer
// for a card whose spec cannot be read, and the honest fallback is to
// claim no gap rather than to invent one.
func unprovenFiles(f *domain.Feature) []statusUnprovenFile {
	raw, err := os.ReadFile(f.ArtifactPath())
	if err != nil {
		return nil
	}
	var out []statusUnprovenFile
	for _, u := range spec.UnprovenFiles(string(raw)) {
		out = append(out, statusUnprovenFile{Path: u.Path, Reason: u.Reason})
	}
	return out
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

// cardRunning reports whether anything is currently driving f: this
// process cannot see another process's in-memory session (status is its
// own separate process, holding no lock of its own — see the package
// doc), so it can only reason from what is durably visible: the pid file
// a headless run/resume records, and the card's own exclusive lock
// (state.CardLockFile), which every drive — headless or the TUI board —
// holds for as long as it drives the card, including while paused on an
// open ask (see state.CardLocks).
//
// The pid file is checked first since it names the exact pid an
// orchestrating agent wants to kill -0 itself. Past that, a clean,
// uncontended trylock on the card's own lock is proof positive that
// nothing else holds it — this process took it and let it go — so "no" is
// as authoritative here as ErrLocked already is everywhere else that
// relies on this lock for mutual exclusion. Any other error from the
// probe (e.g. an unreadable lock dir) leaves the question genuinely open,
// so it reads as running rather than asserting a "no" this check could
// not actually establish.
func cardRunning(ws state.Workspace, id domain.FeatureID) bool {
	if state.ProcessAlive(state.ReadPIDFile(ws.PIDFile(id))) {
		return true
	}
	release, err := state.AcquireLock(ws.CardLockFile(id))
	if err != nil {
		// ErrLocked (another gummi process holds the card) and any other
		// failure to even ask the question both leave "running" as the
		// honest answer.
		return true
	}
	release()
	return false
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
	// Same place, same reason: a pass that never compiled three of the
	// branch's files is a pass about the other files, and the person
	// merging it should read that here rather than find it in the spec.
	if len(v.UnprovenFiles) > 0 {
		paths := make([]string, 0, len(v.UnprovenFiles))
		for _, u := range v.UnprovenFiles {
			paths = append(paths, u.Path)
		}
		fmt.Fprintf(w, "  Unproven: %s (changed, but no check that ran exercised them)\n",
			strings.Join(paths, ", "))
	}
	switch {
	case v.Dropped:
		// A card its goal dropped is closed through the same store path a
		// hand-off uses, so it used to print "handed off — <branch> kept",
		// two lines under a Branch line reading "(none)" — a branch that
		// was never created, described as kept. Say what happened instead.
		if v.BranchState == "none" || v.Branch == "" {
			fmt.Fprintf(w, "  Ending:   dropped by %s — it never started, nothing was kept\n", goalOrItsGoal(v))
		} else {
			fmt.Fprintf(w, "  Ending:   dropped by %s — %s kept, nothing landed\n", goalOrItsGoal(v), v.Branch)
		}
	case v.HandedOff:
		// Under Verified for the same reason Excused is: it qualifies what
		// happened after the pass. Only on a handed-off card — every other
		// card's ending is already legible from Stage and the branch state.
		fmt.Fprintf(w, "  Ending:   handed off — %s kept, nothing landed\n", v.Branch)
	}
	fmt.Fprintf(w, "  Running:  %s\n", yesNo(v.Running))
	fmt.Fprintf(w, "  Spend:    %s / %d credits\n", trimCredits(v.Spend.Credits), v.Spend.Envelope)
	// An envelope bounds what a card may START, not what it may finish:
	// the check fires between sessions, so the session in flight when the
	// cap is reached runs to its end. Every stop on the lxd autopilot
	// drive overran — 70.66 of 60 at one plan gate, a whole critique's
	// worth — and nothing anywhere said the cap was advisory, so a person
	// who set a small envelope to bound a spend had no way to learn by how
	// much it could be missed. Printed only when it actually happened.
	if v.Spend.Envelope > 0 && v.Spend.Credits > float64(v.Spend.Envelope) {
		fmt.Fprintf(w, "            over by %s — the envelope is checked between sessions, "+
			"so the one in flight finishes\n",
			trimCredits(v.Spend.Credits-float64(v.Spend.Envelope)))
	}
	// continuation lines under Spend: the breakdown is the same figure
	// taken apart, not a second one.
	for _, sp := range v.StageSpend {
		fmt.Fprintf(w, "            %-9s %-11s %8s  %s\n",
			sp.Stage, sp.Role, trimCredits(sp.Credits), sp.Model)
	}
	fmt.Fprintf(w, "  Blockers: %d open comment%s · %d open diff comment%s\n",
		v.Blockers.OpenQuestions, cardPlural(v.Blockers.OpenQuestions), v.Blockers.OpenDiff, cardPlural(v.Blockers.OpenDiff))
	if r := v.Rounds; r.Plan > 0 || r.Review > 0 || r.Corrective > 0 {
		// Corrective first, and named for what it is: it is the only one
		// of the three that outlives its loop. Plan and review are live
		// budgets, cleared the moment their loop passes, so on a finished
		// card they read 0 while the card was in fact sent back four
		// times — printing them first made a reworked card look untouched.
		fmt.Fprintf(w, "  Rework:   %d round%s redone in total", r.Corrective, cardPlural(r.Corrective))
		if r.Plan > 0 || r.Review > 0 {
			fmt.Fprintf(w, " (open now: plan %d, review %d of their caps)", r.Plan, r.Review)
		}
		fmt.Fprintln(w)
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
	if v.GoalID != "" {
		fmt.Fprintf(w, "  Goal:     %s\n", v.GoalID)
	}
	if v.FoundBy != "" {
		fmt.Fprintf(w, "  Found by: %s\n", v.FoundBy)
	}
	if g := v.Goal; g != nil {
		met, total := g.Met()
		state := "running"
		switch {
		case g.Ready:
			state = "ready for you"
		case g.WrappingUp:
			state = "wrapping up"
		}
		if g.Partial != "" {
			state += " — partial: " + g.Partial
		}
		fmt.Fprintf(w, "  Goal:     %s · %d of %d done-when met · %d lanes\n", state, met, total, g.Lanes)
		fmt.Fprintf(w, "  Budget:   %d · goal %s · cards %s · reserve %d · left to give %s\n",
			g.Budget.Envelope, trimCredits(g.Budget.Own), trimCredits(g.Budget.CardSpent), g.Budget.Reserve, trimCredits(g.Budget.Available))
		for _, d := range g.DoneWhen {
			fmt.Fprintf(w, "            %s %s — %s\n", d.ID, d.Status, d.Says)
		}
		for _, c := range g.Cards {
			fmt.Fprintf(w, "            %s %-9s %s\n", c.ID, c.State, c.Title)
		}
		if n := len(g.Decisions); n > 0 {
			fmt.Fprintf(w, "  Decisions for review: %d\n", n)
		}
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

// goalOrItsGoal names the goal that dropped a card, falling back to the
// generic when the row no longer carries the id.
func goalOrItsGoal(v statusView) string {
	if v.GoalID != "" {
		return v.GoalID
	}
	return "its goal"
}
