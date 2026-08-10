package driver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/worktree"
)

// rewindMain rewinds the main checkout to an unrelated lineage that does
// not descend from a feature's recorded fork — the FD-002 drift shape.
func rewindMain(t *testing.T, root string) {
	t.Helper()
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.CommandContext(context.Background(), "git",
			append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "rewound.ts"), []byte("rewound\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("checkout", "-q", "--orphan", "tmp-rewound")
	git("commit", "-q", "-m", "rewound main")
	git("branch", "-M", "tmp-rewound", "main")
}

// The operator-facing resume path surfaces the drift guard end-to-end:
// resuming a feature whose worktree's main was rewound returns a
// ForkDriftError before any engine work runs, and the stored stage never
// moves off the entry stage.
func TestResumeRefusesOnDrift(t *testing.T) {
	h := newHarness(t, true, nil)
	f := feature(1, domain.StageImplement)
	if err := h.store.CreateFeature(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	if _, err := h.wt.Create(context.Background(), &f); err != nil {
		t.Fatal(err)
	}
	rewindMain(t, h.root)

	_, err := h.driver(Options{}).Resume(context.Background(), f.ID, ResumeInput{})
	if err == nil {
		t.Fatal("Resume succeeded despite drift")
	}
	var fe *worktree.ForkDriftError
	if !errors.As(err, &fe) {
		t.Fatalf("want *worktree.ForkDriftError, got %T: %v", err, err)
	}
	if st := h.stageOf(f.ID); st != domain.StageImplement {
		t.Fatalf("stage moved to %s despite refusal, want StageImplement", st)
	}
}
