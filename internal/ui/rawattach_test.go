package ui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

func TestResolveAttachSuccess(t *testing.T) {
	m, root := diffWorkspace(t) // FD-001 at review with a worktree
	t.Setenv("GUMMI_ATTACH_CMD", "sh -c true")
	f := m.rows[0].F
	argv, dir, problem := m.resolveAttach(f)
	if problem != "" {
		t.Fatalf("unexpected problem: %s", problem)
	}
	if len(argv) != 3 || argv[0] != "sh" {
		t.Errorf("argv = %v, want [sh -c true]", argv)
	}
	want := filepath.Join(root, f.WorktreePath())
	if dir != want {
		t.Errorf("dir = %s, want %s (the worktree)", dir, want)
	}
}

func TestResolveAttachNoWorktree(t *testing.T) {
	m, _ := newWorkspace(t)
	f := domain.Feature{ID: "FD-001", Num: 1, Title: "x", Slug: "x", Stage: domain.StageTodo}
	_, _, problem := m.resolveAttach(f)
	if !strings.Contains(problem, "no worktree") {
		t.Errorf("problem = %q, want a no-worktree message", problem)
	}
}

func TestResolveAttachCommandNotFound(t *testing.T) {
	m, _ := diffWorkspace(t)
	t.Setenv("GUMMI_ATTACH_CMD", "definitely-not-a-real-binary-xyz")
	_, _, problem := m.resolveAttach(m.rows[0].F)
	if !strings.Contains(problem, "not found") {
		t.Errorf("problem = %q, want a not-found message", problem)
	}
}

func TestDefaultAttachUsesSelectedCodexBinary(t *testing.T) {
	t.Setenv("GUMMI_ATTACH_CMD", "")
	t.Setenv("GUMMI_AGENT", "codex")
	t.Setenv("GUMMI_CODEX_BIN", "/opt/codex-custom")
	if got := defaultAttachCommand(); got != "/opt/codex-custom" {
		t.Fatalf("default attach = %q", got)
	}
}
