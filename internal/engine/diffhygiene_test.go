package engine

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// TestDiffHygieneBlocksCommittedArtifacts: a cheap-tier drive shipped a
// 3 MB compiled binary, two scratch scripts and three fixtures past the
// implement critique ("no scope creep detected"), verify, and exit 0.
// Whether a branch carries a build artifact is a fact about the tree, so
// it is checked with code rather than left to the reviewer's model.
func TestDiffHygieneBlocksCommittedArtifacts(t *testing.T) {
	files := []worktree.ChangedFile{
		{Path: "cmd/tasks/main.go", Size: 4000},
		{Path: "tasks", Size: 3023908, Binary: true},
		{Path: "huge.json", Size: maxCommittedFileBytes + 1},
	}
	planned := []domain.PlannedFile{{Path: "cmd/tasks/main.go"}}

	got := checkDiffHygiene(files, planned)
	names, blocking := blockingHygiene(got)
	if !blocking {
		t.Fatalf("nothing blocked on a branch shipping a binary: %+v", got)
	}
	if len(names) != 2 {
		t.Errorf("blocking files = %v, want the binary and the oversized file", names)
	}
	// the planned source file is not a finding at all
	for _, f := range got {
		if f.Path == "cmd/tasks/main.go" {
			t.Errorf("a planned source file was flagged: %+v", f)
		}
	}
}

// A file outside the manifest is reported, never blocking: the implement
// contract says the manifest is a starting point and not a boundary, so
// work legitimately goes beyond it.
func TestDiffHygieneReportsButDoesNotBlockUnplannedFiles(t *testing.T) {
	got := checkDiffHygiene(
		[]worktree.ChangedFile{{Path: "README.md", Size: 200}},
		[]domain.PlannedFile{{Path: "cmd/tasks/main.go"}},
	)
	if len(got) != 1 {
		t.Fatalf("findings = %+v, want one note", got)
	}
	if got[0].Blocking {
		t.Error("an unplanned file blocked the stage; the manifest is not a boundary")
	}
	if _, blocking := blockingHygiene(got); blocking {
		t.Error("blockingHygiene claimed a note was blocking")
	}
}

// With no manifest at all there is nothing to compare against, so only the
// unarguable findings stand.
func TestDiffHygieneWithoutAManifest(t *testing.T) {
	got := checkDiffHygiene([]worktree.ChangedFile{
		{Path: "whatever.go", Size: 10},
		{Path: "a.bin", Binary: true},
	}, nil)
	if len(got) != 1 || got[0].Path != "a.bin" {
		t.Fatalf("findings = %+v, want only the binary", got)
	}
}

// A deleted path ships nothing — its size and binary flag describe a file
// that is no longer there.
func TestDiffHygieneIgnoresDeletions(t *testing.T) {
	got := checkDiffHygiene([]worktree.ChangedFile{
		{Path: "old.bin", Binary: true, Deleted: true},
	}, nil)
	if len(got) != 0 {
		t.Errorf("a deleted file was flagged: %+v", got)
	}
}

// The kickoff block has to tell the agent which findings are already
// decided, or it will weigh them and report a pass it cannot give.
func TestHygieneBlockStatesTheFloor(t *testing.T) {
	block := hygieneBlock(checkDiffHygiene(
		[]worktree.ChangedFile{{Path: "tasks", Binary: true}}, nil))
	if !strings.Contains(block, "BLOCKING") {
		t.Errorf("block does not mark the blocking finding:\n%s", block)
	}
	if !strings.Contains(block, "floored to fail") {
		t.Errorf("block does not say the verdict is already decided:\n%s", block)
	}
	if hygieneBlock(nil) != "" {
		t.Error("an empty finding list still produced a block")
	}
}
