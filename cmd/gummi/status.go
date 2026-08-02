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
	jsonOut := fs.Bool("json", false, "emit machine-readable JSON instead of the text summary")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: gummi status <id|ref> [--json]")
		fs.PrintDefaults()
	}
	idArg, err := idFirstArg(fs, args)
	if err != nil {
		return err
	}
	return withReadWorkspace(func(ctx context.Context, store *state.Store, wt *worktree.Manager, ws state.Workspace) error {
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
	Done        bool           `json:"done"`
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
func buildStatus(ctx context.Context, store *state.Store, wt *worktree.Manager, ws state.Workspace, f *domain.Feature) statusView {
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
		ID:          string(f.ID),
		Ref:         f.ExternalRef,
		Kind:        string(kind),
		Title:       f.Title,
		Stage:       string(f.Stage),
		Route:       route,
		Blockers:    statusBlockers{OpenQuestions: sq, OpenDiff: dq},
		Spend:       statusSpend{Credits: f.Spend.Credits, Envelope: f.Budget.Envelope},
		Branch:      f.BranchName(),
		BranchState: branchState(ctx, wt, f),
		Done:        f.Stage == domain.StageDone,
	}
}

// branchState collapses the worktree manager's branch queries into one
// word: none (no branch yet), created (branch exists, no commits of its
// own), ahead (has commits not on main — the verified-branch state), or
// landed (already merged). Any query error degrades to the safe "none".
func branchState(ctx context.Context, wt *worktree.Manager, f *domain.Feature) string {
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
	fmt.Fprintf(w, "  Spend:    %s / %d credits\n", trimCredits(v.Spend.Credits), v.Spend.Envelope)
	fmt.Fprintf(w, "  Blockers: %d open question(s) · %d open diff comment(s)\n", v.Blockers.OpenQuestions, v.Blockers.OpenDiff)
	if v.Ref != "" {
		fmt.Fprintf(w, "  Ref:      %s\n", v.Ref)
	}
}

// trimCredits formats a credit figure without a trailing ".0" for whole
// numbers, so the common integer case reads cleanly.
func trimCredits(c float64) string {
	if c == float64(int64(c)) {
		return fmt.Sprintf("%d", int64(c))
	}
	return fmt.Sprintf("%.2f", c)
}
