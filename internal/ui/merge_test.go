package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/engine"
)

// mergeFixture is rebaseFeatureFixture plus one committed change on the
// feature branch, so there is something to squash-merge.
func mergeFixture(t *testing.T) (*Shell, string, string) {
	t.Helper()
	m, root, wt := rebaseFeatureFixture(t)
	if err := os.WriteFile(filepath.Join(wt, "feat.go"), []byte("package x // feature work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature work")
	return m, root, wt
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(context.Background(),
		"git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// pressMerge presses m on the selected row and returns the shell with
// the draft pumped through (dialog open on success).
func pressMerge(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m.sel = 0
	return press(t, m, tea.KeyPressMsg{Code: 'm', Text: "m"})
}

func TestSquashMergeFlowWithScribeDraft(t *testing.T) {
	m, root, _ := mergeFixture(t)
	draft := "FD-001: rebase me\n\nAdds feat.go with the feature work."
	eng := engine.New(engine.Config{
		Agent: &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
			if !strings.Contains(msg, "feature work") {
				t.Errorf("draft prompt does not carry the branch diff:\n%s", msg)
			}
			return []agent.Event{{Kind: agent.EventMessage, Text: draft}, {Kind: agent.EventIdle}}
		}},
		Store: m.store, Worktrees: m.wt, Workspace: m.ws, MaxActive: 1,
	})
	t.Cleanup(func() { eng.Close() })
	m.AttachEngine(eng)

	m = pressMerge(t, m)
	d, ok := m.Overlay.Top().(*commitMsgDialog)
	if !ok {
		t.Fatalf("m did not open the commit-message dialog (notice %q)", m.notice.text)
	}
	if d.input.Value() != draft {
		t.Fatalf("dialog prefill = %q, want the scribe draft %q", d.input.Value(), draft)
	}
	if m.drafting {
		t.Error("drafting flag still set with the dialog open")
	}

	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.notice.isErr || !strings.Contains(m.notice.text, "squash-merged") {
		t.Fatalf("merge notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
	ctx := context.Background()
	f, _ := m.store.GetFeature(ctx, "FD-001")
	if landed, err := m.wt.Landed(ctx, &f); !landed || err != nil {
		t.Errorf("Landed after merge = %v, %v; want true", landed, err)
	}
	if got := gitOut(t, root, "log", "-1", "--format=%B"); got != draft {
		t.Errorf("main HEAD message = %q, want %q", got, draft)
	}

	// the landed row now cleans up via c — the squash-merged branch is not
	// an ancestor of main, so this exercises the content-checked delete
	m = pump(t, m, m.loadRows)
	m.sel = 0
	m = press(t, m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	if m.Overlay.Top() == nil {
		t.Fatalf("c did not open the cleanup dialog (notice %q)", m.notice.text)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if m.notice.isErr || !strings.Contains(m.notice.text, "cleaned up") {
		t.Fatalf("cleanup notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
	if ok, _ := m.wt.BranchExists(ctx, &f); ok {
		t.Error("squash-merged branch survived cleanup")
	}
}

func TestSquashMergeNoEngineFallsBackToTemplate(t *testing.T) {
	m, root, _ := mergeFixture(t)

	m = pressMerge(t, m)
	d, ok := m.Overlay.Top().(*commitMsgDialog)
	if !ok {
		t.Fatalf("m did not open the commit-message dialog (notice %q)", m.notice.text)
	}
	if d.input.Value() != "FD-001: Rebase me" {
		t.Fatalf("dialog prefill = %q, want the ID+title template", d.input.Value())
	}
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.notice.isErr || !strings.Contains(m.notice.text, "squash-merged") {
		t.Fatalf("merge notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
	if got := gitOut(t, root, "log", "-1", "--format=%s"); got != "FD-001: Rebase me" {
		t.Errorf("main HEAD subject = %q", got)
	}
}

func TestSquashMergeEscCancels(t *testing.T) {
	m, root, _ := mergeFixture(t)
	head := gitOut(t, root, "rev-parse", "HEAD")

	m = pressMerge(t, m)
	if m.Overlay.Top() == nil {
		t.Fatal("m did not open the commit-message dialog")
	}
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.Overlay.Top() != nil {
		t.Fatal("esc did not close the dialog")
	}
	if got := gitOut(t, root, "rev-parse", "HEAD"); got != head {
		t.Error("esc still merged")
	}
}

func TestSquashMergeEmptyMessageStaysOpen(t *testing.T) {
	m, _, _ := mergeFixture(t)
	m = pressMerge(t, m)
	d, ok := m.Overlay.Top().(*commitMsgDialog)
	if !ok {
		t.Fatal("m did not open the commit-message dialog")
	}
	d.input.SetValue("   \n ")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.Overlay.Top() == nil {
		t.Fatal("ctrl+s with an empty message closed the dialog instead of keeping it open")
	}
}

func TestSquashMergeRefusedWithoutWorktree(t *testing.T) {
	m, _, _ := mergeFixture(t)
	if err := m.wt.Remove(context.Background(), &m.rows[0].F, true); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	m = pressMerge(t, m)
	if !m.notice.isErr || !strings.Contains(m.notice.text, "no worktree") {
		t.Fatalf("notice = %q (err=%v), want a no-worktree refusal", m.notice.text, m.notice.isErr)
	}
	if m.Overlay.Top() != nil {
		t.Fatal("dialog opened without a worktree")
	}
}

func TestSquashMergeRefusedWhenLanded(t *testing.T) {
	m, root, wt := rebaseFeatureFixture(t)
	landFeature(t, root, wt) // commits + merges --no-ff into main
	m = pump(t, m, m.loadRows)
	m = pressMerge(t, m)
	if !m.notice.isErr || !strings.Contains(m.notice.text, "already landed") {
		t.Fatalf("notice = %q (err=%v), want an already-landed refusal", m.notice.text, m.notice.isErr)
	}
}

func TestSquashMergeRefusedDirtyBranch(t *testing.T) {
	m, _, wt := mergeFixture(t)
	if err := os.WriteFile(filepath.Join(wt, "feat.go"), []byte("package x // rework\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = pressMerge(t, m)
	if !m.notice.isErr || !strings.Contains(m.notice.text, "uncommitted changes on its branch") {
		t.Fatalf("notice = %q (err=%v), want a dirty-branch refusal", m.notice.text, m.notice.isErr)
	}
	if m.Overlay.Top() != nil {
		t.Fatal("dialog opened despite dirty branch")
	}
}

func TestSquashMergeRefusedDirtyMain(t *testing.T) {
	m, root, _ := mergeFixture(t)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("local edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m = pressMerge(t, m)
	if !m.notice.isErr || !strings.Contains(m.notice.text, "main checkout has uncommitted changes") {
		t.Fatalf("notice = %q (err=%v), want a dirty-main refusal", m.notice.text, m.notice.isErr)
	}
}

func TestSquashMergeReentryRefused(t *testing.T) {
	m, _, _ := mergeFixture(t)
	m.drafting = true
	m = pressMerge(t, m)
	if !m.notice.isErr || !strings.Contains(m.notice.text, "already drafting") {
		t.Fatalf("notice = %q (err=%v), want a re-entry refusal", m.notice.text, m.notice.isErr)
	}
}

func TestSquashMergeConflictNoticeNamesFile(t *testing.T) {
	m, root, wt := mergeFixture(t)
	// conflicting README.md edits on both sides
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("feature version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature readme")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("main version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "main readme")

	m = pressMerge(t, m)
	if m.Overlay.Top() == nil {
		t.Fatalf("m did not open the commit-message dialog (notice %q)", m.notice.text)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if !m.notice.isErr || !strings.Contains(m.notice.text, "README.md") || !strings.Contains(m.notice.text, "rebase (r)") {
		t.Fatalf("conflict notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
	// main is unwound and clean
	if out := gitOut(t, root, "status", "--porcelain", "--untracked-files=no"); out != "" {
		t.Errorf("main checkout dirty after undone merge:\n%s", out)
	}
}
