package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

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
	ID          string         `json:"id"`
	Ref         string         `json:"ref,omitempty"`
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	Stage       string         `json:"stage"`
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
	route := ""
	if kind == domain.KindFeature {
		if f.Skip.Quick {
			route = "quick"
		} else {
			route = "full"
		}
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
	}
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
	if v.Route != "" {
		fmt.Fprintf(w, "  Route:    %s\n", v.Route)
	}
	fmt.Fprintf(w, "  Branch:   %s  (%s)\n", v.Branch, v.BranchState)
	fmt.Fprintf(w, "  Verified: %s\n", yesNo(v.Verified))
	fmt.Fprintf(w, "  Running:  %s\n", yesNo(v.Running))
	fmt.Fprintf(w, "  Spend:    %s / %d credits\n", trimCredits(v.Spend.Credits), v.Spend.Envelope)
	fmt.Fprintf(w, "  Blockers: %d open question(s) · %d open diff comment(s)\n", v.Blockers.OpenQuestions, v.Blockers.OpenDiff)
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
