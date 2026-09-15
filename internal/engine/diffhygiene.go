package engine

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/worktree"
)

// The diff hygiene check: a deterministic read of WHAT a branch is
// shipping, run at verify kickoff alongside the repo's own checks.
//
// It exists because the reviewer tier is the one part of the quality floor
// that varies with the model behind it. A drive on a cheap tier produced a
// branch that passed build, vet, test, its own live checks, the implement
// critique — which reported "no scope creep detected" — and the verify
// verdict, while committing a 3 MB compiled binary, two scratch shell
// scripts and three test fixtures alongside the feature. Every one of
// those is a fact about the tree rather than a judgement about the code,
// and a fact is worth checking with code.
//
// The split matters. Committing a build artifact is never right, so it
// FAILS the stage. Touching a file the plan did not list is often right —
// the implement contract says the manifest is a starting point and not a
// boundary — so it is reported and left to the reviewer.
const (
	// maxCommittedFileBytes is the size above which a text file added by a
	// branch is called out. Generous on purpose: a large generated fixture
	// or a vendored source file can be legitimate, and the check is meant
	// to catch what nobody would defend, not to litigate file sizes.
	maxCommittedFileBytes = 1 << 20 // 1 MiB
)

// hygieneFinding is one thing the check noticed about the branch's files.
type hygieneFinding struct {
	Path string
	Why  string
	// Blocking marks a finding that floors the verdict. Only the two
	// unarguable kinds are: a binary file, and a file past the size cap.
	Blocking bool
}

// checkDiffHygiene reports what the branch's changed files look like
// against the plan's manifest. A nil manager, a card with no worktree, or
// a git failure yields no findings: this check adds a floor where it can
// see the tree and never invents one where it cannot.
func checkDiffHygiene(files []worktree.ChangedFile, planned []domain.PlannedFile) []hygieneFinding {
	inPlan := make(map[string]bool, len(planned))
	for _, p := range planned {
		inPlan[p.Path] = true
	}
	var out []hygieneFinding
	for _, f := range files {
		if f.Deleted {
			// a removed file ships nothing; its size and binary flag
			// describe a file that is no longer there
			continue
		}
		switch {
		case f.Binary:
			out = append(out, hygieneFinding{
				Path:     f.Path,
				Why:      "binary file committed to the branch",
				Blocking: true,
			})
		case f.Size > maxCommittedFileBytes:
			out = append(out, hygieneFinding{
				Path:     f.Path,
				Why:      fmt.Sprintf("%s committed to the branch, past the %s cap", humanBytes(f.Size), humanBytes(maxCommittedFileBytes)),
				Blocking: true,
			})
		case len(planned) > 0 && !inPlan[f.Path]:
			out = append(out, hygieneFinding{
				Path: f.Path,
				Why:  "changed but not in the plan's file manifest",
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Blocking != out[j].Blocking {
			return out[i].Blocking
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// hygieneBlock renders the findings for the verify kickoff, or "" when
// there are none. The blocking ones are stated as the stage's outcome
// rather than as advice, because that is what they are: the floor is
// already stamped by the time the agent reads this, and telling it
// otherwise would invite a pass it cannot give.
func hygieneBlock(findings []hygieneFinding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nFiles this branch ships (gummi's own read of the tree, not a judgement):\n")
	blocking := false
	for _, f := range findings {
		mark := "note"
		if f.Blocking {
			mark = "BLOCKING"
			blocking = true
		}
		fmt.Fprintf(&b, "- %s: %s — %s\n", mark, f.Path, f.Why)
	}
	if blocking {
		b.WriteString("\nA BLOCKING entry is not yours to weigh: the verdict is already " +
			"floored to fail, and the branch cannot land carrying these files. Say so in " +
			"the Verification section, naming each file and what should happen to it " +
			"(removed from the branch, or added to .gitignore and removed), so the next " +
			"implement round has the instruction rather than the symptom.\n")
	}
	b.WriteString("\nA \"note\" entry is not a defect by itself — the plan's manifest is a " +
		"starting point, and work legitimately goes beyond it. Judge each one: a file " +
		"that belongs to this feature needs no action beyond a line in Progress, and a " +
		"file that does not belong on this branch is a finding.\n")
	return b.String()
}

// blockingHygiene reports whether any finding floors the verdict, and
// names them for the floor's reason.
func blockingHygiene(findings []hygieneFinding) (names []string, ok bool) {
	for _, f := range findings {
		if f.Blocking {
			names = append(names, f.Path)
		}
	}
	return names, len(names) > 0
}

// humanBytes renders a size the way a person would say it.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// diffHygiene runs the check for a session's card, resolving the branch's
// changed files and the plan's manifest. Anything it cannot read yields no
// findings — a check that guesses is worse than one that stays quiet.
func (e *Engine) diffHygiene(s *Session) []hygieneFinding {
	// The session's own context, so a cancelled stage does not leave a git
	// process behind; a session constructed without one (tests) falls back
	// to a background context bounded by the same timeout the repo card
	// uses, because neither call should be able to hang a stage.
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, repoCardTimeout)
	defer cancel()

	mgr, err := e.mgr(ctx, &s.Feature)
	if err != nil || mgr == nil {
		return nil
	}
	files, err := mgr.ChangedFiles(ctx, &s.Feature)
	if err != nil || len(files) == 0 {
		return nil
	}
	var planned []domain.PlannedFile
	if path := s.SpecPath(); path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			planned, _, _ = spec.ParseFiles(string(raw))
		}
	}
	return checkDiffHygiene(files, planned)
}
