package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/ui/theme"
)

// squashFixture is mergeFixture with a remote-tracking origin/main set up,
// so tests model a repo with a remote even though ResolveCollapseBase uses
// the branch's fork point with local main.
func squashFixture(t *testing.T) (*Shell, string, string) {
	t.Helper()
	m, root, wt := mergeFixture(t)
	mainHead := gitOut(t, root, "rev-parse", "HEAD")
	git(t, root, "update-ref", "refs/remotes/origin/main", mainHead)
	return m, root, wt
}

func pressSquash(t *testing.T, m *Shell) *Shell {
	t.Helper()
	m.sel = 0
	return press(t, m, tea.KeyPressMsg{Code: 'z', Text: "z"})
}

func TestSquashOpensCommitDialog(t *testing.T) {
	m, _, _ := squashFixture(t)
	m = pressSquash(t, m)
	if _, ok := m.Overlay.Top().(*commitMsgDialog); !ok {
		t.Fatalf("z did not open the commit-message dialog (notice %q)", m.notice.text)
	}
	if m.squashPrep {
		t.Error("squashPrep flag still set with the dialog open")
	}
}

func TestSquashRewritesBranchInPlace(t *testing.T) {
	m, root, wt := squashFixture(t)
	mainHead := gitOut(t, root, "rev-parse", "HEAD")
	branchHead := gitOut(t, wt, "rev-parse", "HEAD")
	message := "FD-001: collapsed\n\nSingle commit message."

	m = pressSquash(t, m)
	typeMessage(t, m, message)
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if m.notice.isErr {
		t.Fatalf("squash notice is an error: %q", m.notice.text)
	}
	if !strings.Contains(m.notice.text, "squashed to") {
		t.Fatalf("notice missing 'squashed to': %q", m.notice.text)
	}
	if !strings.Contains(m.notice.text, "git push --force-with-lease origin gummi/FD-001-rebase-me") {
		t.Fatalf("notice missing push hint: %q", m.notice.text)
	}
	if got := gitOut(t, root, "rev-parse", "HEAD"); got != mainHead {
		t.Errorf("main moved: %s -> %s", mainHead, got)
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got == branchHead {
		t.Error("branch head did not change")
	}
	if got := gitOut(t, wt, "log", "-1", "--format=%B"); got != message {
		t.Errorf("branch tip message = %q, want %q", got, message)
	}
}

func TestSquashNoOpAlreadyCollapsed(t *testing.T) {
	m, _, wt := squashFixture(t)
	branchHead := gitOut(t, wt, "rev-parse", "HEAD")

	m = pressSquash(t, m)
	// The fixture's single commit has subject "feature work"; matching it
	// triggers Collapse's no-op path.
	typeMessage(t, m, "feature work")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if m.notice.isErr {
		t.Fatalf("no-op squash surfaced as error: %q", m.notice.text)
	}
	want := "FD-001 already collapsed, nothing to do"
	if m.notice.text != want {
		t.Fatalf("notice = %q, want %q", m.notice.text, want)
	}
	if m.notice.reload {
		t.Error("no-op notice should not reload")
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != branchHead {
		t.Errorf("branch head changed on no-op: %s -> %s", branchHead, got)
	}
}

func TestSquashRefusedWhenLanded(t *testing.T) {
	m, root, wt := rebaseFeatureFixture(t)
	landFeature(t, m, root, wt)
	branchHead := gitOut(t, wt, "rev-parse", "HEAD")
	m = pump(t, m, m.loadRows)

	m = pressSquash(t, m)
	if !m.notice.isErr || m.notice.text != "FD-001 already landed on main — press c to clean up" {
		t.Fatalf("notice = %q (err=%v), want landed guard", m.notice.text, m.notice.isErr)
	}
	if m.Overlay.Top() != nil {
		t.Fatal("dialog opened for a landed card")
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != branchHead {
		t.Errorf("branch head changed on landed guard: %s -> %s", branchHead, got)
	}
}

func TestSquashWarnsOnOpenReviewThreads(t *testing.T) {
	m, _, wt := squashFixture(t)
	branchHead := gitOut(t, wt, "rev-parse", "HEAD")
	prURL := "https://github.com/o/r/pull/42"
	m.openReviewThreads = func(context.Context, domain.Feature) (int, string, error) {
		return 3, prURL, nil
	}

	m = pressSquash(t, m)
	d, ok := m.Overlay.Top().(*confirmDialog)
	if !ok {
		t.Fatalf("expected confirm dialog (notice %q)", m.notice.text)
	}
	if !strings.Contains(d.detail, strconv.Itoa(3)) {
		t.Errorf("confirm detail missing thread count: %q", d.detail)
	}
	if !strings.Contains(d.detail, prURL) {
		t.Errorf("confirm detail missing PR URL: %q", d.detail)
	}

	// cancel leaves the branch untouched
	m = press(t, m, tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.Overlay.Top() != nil {
		t.Fatal("cancel left a dialog open")
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != branchHead {
		t.Errorf("branch head changed after cancel: %s -> %s", branchHead, got)
	}

	// re-open and confirm proceeds to the commit-message dialog
	m = pressSquash(t, m)
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if _, ok := m.Overlay.Top().(*commitMsgDialog); !ok {
		t.Fatalf("confirm did not open commit-message dialog (notice %q)", m.notice.text)
	}
}

func TestSquashRefusedWithoutWorktree(t *testing.T) {
	m, _, _ := squashFixture(t)
	if err := m.wt.Remove(context.Background(), &m.rows[0].F, true); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	m = pressSquash(t, m)
	if !m.notice.isErr || !strings.Contains(m.notice.text, "no worktree") {
		t.Fatalf("notice = %q (err=%v), want a no-worktree refusal", m.notice.text, m.notice.isErr)
	}
	if m.Overlay.Top() != nil {
		t.Fatal("dialog opened without a worktree")
	}
}

func TestSquashBindingOmittedForResearchCards(t *testing.T) {
	m := NewShell(theme.GummiDark(), "v0-test")
	rid, _ := domain.NewID(domain.KindResearch, 1)
	fid, _ := domain.NewFeatureID(2)
	m.rows = []featureRow{
		{F: domain.Feature{ID: rid, Num: 1, Kind: domain.KindResearch, Title: "research", Stage: domain.StageInvestigate, CreatedAt: fixedTime, UpdatedAt: fixedTime}},
		{F: domain.Feature{ID: fid, Num: 2, Title: "feature", Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime}},
	}

	m.sel = 0
	for _, b := range m.boardBindings() {
		if b.key == "z" {
			t.Errorf("RS-selected board bindings still include %q", b.key)
		}
	}

	m.sel = 1
	found := false
	for _, b := range m.boardBindings() {
		if b.key == "z" {
			found = true
			break
		}
	}
	if !found {
		t.Error("feature-selected board bindings missing z")
	}
}

func TestSquashFailsOnDirtyWorktree(t *testing.T) {
	m, _, wt := squashFixture(t)
	branchHead := gitOut(t, wt, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(wt, "dirty.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m = pressSquash(t, m)
	typeMessage(t, m, "FD-001: collapsed")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	if !m.notice.isErr || !strings.Contains(m.notice.text, "squash failed:") {
		t.Fatalf("notice = %q (err=%v), want a squash failed error", m.notice.text, m.notice.isErr)
	}
	if m.notice.reload {
		t.Error("error notice should not reload")
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != branchHead {
		t.Errorf("branch head changed on error: %s -> %s", branchHead, got)
	}
}

func TestSquashPillShowsDuringPrepare(t *testing.T) {
	m, _, _ := mergeFixture(t)
	m.squashPrep = true
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = model.(*Shell)
	rendered := m.View().Content
	if !strings.Contains(rendered, "squashing") {
		t.Errorf("status bar does not show squashing pill:\n%s", rendered)
	}
}

func TestSquashProbeFailureSurfaced(t *testing.T) {
	m, _, wt := squashFixture(t)
	branchHead := gitOut(t, wt, "rev-parse", "HEAD")
	m.openReviewThreads = func(context.Context, domain.Feature) (int, string, error) {
		return 0, "", errors.New("probe failure")
	}

	m = pressSquash(t, m)
	want := "FD-001 squash failed: probe failure"
	if !m.notice.isErr || m.notice.text != want {
		t.Fatalf("notice = %q (err=%v), want %q", m.notice.text, m.notice.isErr, want)
	}
	if m.Overlay.Top() != nil {
		t.Fatal("dialog opened after probe error")
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD"); got != branchHead {
		t.Errorf("branch head changed after probe error: %s -> %s", branchHead, got)
	}
}
