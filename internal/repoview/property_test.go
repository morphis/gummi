package repoview

// Property tests of the repositories view, driven by hegel
// (https://hegel.dev): the one rule for which branch may be deleted from
// it, and the push it prints for a person to run — checked in a real sh,
// under every name git allows a branch.

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"hegel.dev/go/hegel"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

const propCases = 2000

// names are branch names git's check-ref-format allows.
var names = []string{"main", "ff/x", "fix#12", "a$(id)", "a;b", "a`b`", "a'b", `a"b`, "a|b", "a&b", "a(b)", "a!b", "a{b}", "a<b>", "é", "x"}

func drawRows(ht *hegel.T) (string, []worktree.BranchInfo, []Owner, []Fork) {
	base := hegel.Draw(ht, hegel.SampledFrom([]string{"", "main"}))
	var branches []worktree.BranchInfo
	var owners []Owner
	var forks []Fork
	seen := map[string]bool{}
	n := hegel.Draw(ht, hegel.Integers(0, 6))
	for i := range n {
		name := hegel.Draw(ht, hegel.SampledFrom(names))
		if seen[name] {
			continue
		}
		seen[name] = true
		b := worktree.BranchInfo{Name: name}
		if hegel.Draw(ht, hegel.WeightedBooleans(0.3)) {
			b.Worktree = "/w/" + name
		}
		b.AheadBase = hegel.Draw(ht, hegel.Integers(0, 2))
		b.Ahead = hegel.Draw(ht, hegel.Integers(0, 2))
		b.Behind = hegel.Draw(ht, hegel.Integers(0, 1))
		if hegel.Draw(ht, hegel.Booleans()) {
			remote := hegel.Draw(ht, hegel.SampledFrom([]string{"origin", "up/stream", "a#b"}))
			theirs := hegel.Draw(ht, hegel.SampledFrom([]string{name, "other#1", "o$(x)"}))
			b.Upstream, b.UpstreamRemote = remote+"/"+theirs, remote
			b.UpstreamGone = hegel.Draw(ht, hegel.WeightedBooleans(0.2))
		}
		branches = append(branches, b)
		if hegel.Draw(ht, hegel.WeightedBooleans(0.4)) {
			goal := hegel.Draw(ht, hegel.WeightedBooleans(0.2))
			adopted := hegel.Draw(ht, hegel.WeightedBooleans(0.2))
			owners = append(owners, Owner{Branch: name, Card: domain.FeatureID(fmt.Sprintf("FF-%d", i)), Goal: goal, Adopted: adopted})
		}
		if hegel.Draw(ht, hegel.WeightedBooleans(0.2)) {
			forks = append(forks, Fork{Branch: name, Card: "FF-99"})
		}
	}
	return base, branches, owners, forks
}

// TestPropertyOnlyAnIdleUnownedBranchMayBeDeleted: the view offers to
// delete a branch only when no card holds it, it is not the base, nothing
// has it checked out and no card forks from it — and every such branch is
// offered, asking to confirm when it holds commits the base lacks.
func TestPropertyOnlyAnIdleUnownedBranchMayBeDeleted(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		base, branches, owners, forks := drawRows(ht)
		owned := map[string]bool{}
		for _, o := range owners {
			owned[o.Branch] = true
		}
		forked := map[string]bool{}
		for _, f := range forks {
			forked[f.Branch] = true
		}
		rows := Rows(base, branches, owners, forks)
		if len(rows) != len(branches) {
			ht.Fatalf("%d rows for %d branches", len(rows), len(branches))
		}
		for _, r := range rows {
			idle := !owned[r.Name] && r.Name != base && r.Worktree == "" && !forked[r.Name]
			want := DeleteNo
			if idle {
				want = DeleteOK
				if r.AheadBase > 0 {
					want = DeleteConfirm
				}
			}
			if r.Delete != want {
				ht.Fatalf("%q (owned %v, base %q, worktree %q, forked %v, ahead %d): delete %q, want %q",
					r.Name, owned[r.Name], base, r.Worktree, forked[r.Name], r.AheadBase, r.Delete, want)
			}
		}
	}, hegel.WithTestCases(propCases), hegel.WithDatabase(""))
}

// TestPropertyAPrintedPushPushesThatBranch: the push the view prints,
// pasted into a shell, runs git with exactly the remote and the branch's
// refspec — whatever characters git allowed in their names.
func TestPropertyAPrintedPushPushesThatBranch(t *testing.T) {
	hegel.Test(t, func(ht *hegel.T) {
		base, branches, owners, forks := drawRows(ht)
		for _, r := range Rows(base, branches, owners, forks) {
			if r.Push == "" {
				continue
			}
			theirs := strings.TrimPrefix(r.Upstream, r.UpstreamRemote+"/")
			spec := r.Name
			if theirs != r.Name {
				spec += ":" + theirs
			}
			want := []string{"push", r.UpstreamRemote, spec}
			var out bytes.Buffer
			cmd := exec.Command("sh", "-c", `git() { printf '%s\n' "$@"; }; `+r.Push)
			cmd.Stdout = &out
			if err := cmd.Run(); err != nil {
				ht.Fatalf("sh -c %q: %v", r.Push, err)
			}
			if got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n"); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				ht.Fatalf("%q runs git %q, want %q", r.Push, got, want)
			}
		}
	}, hegel.WithTestCases(500), hegel.WithDatabase(""))
}
