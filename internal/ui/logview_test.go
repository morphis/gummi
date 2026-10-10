package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// logFixture is a verified card whose branch carries three commits of its
// own, with its card page open and the log tab mounted through its chord.
func logFixture(t *testing.T) (*Shell, string) {
	t.Helper()
	m, _, wt := mergeFixture(t)
	for _, n := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(wt, n+".txt"), []byte(n+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		git(t, wt, "add", "-A")
		git(t, wt, "commit", "-q", "-m", "wip: "+n)
	}
	m.sel = 0
	m.cardOpen = true
	m.focusThreadInput()
	m = press(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModAlt})
	if m.logv == nil {
		t.Fatalf("alt+l did not mount the log tab (notice %q)", m.notice.text)
	}
	return m, wt
}

func TestLogTabListsTheBranchOldestFirst(t *testing.T) {
	m, _ := logFixture(t)
	if m.activeCardTab() != cardTabLog {
		t.Fatalf("active tab = %s", m.activeCardTab())
	}
	rows := m.logv.log.Rows
	if len(rows) != 3 || rows[0].Subject != "feature work" || rows[2].Subject != "wip: two" {
		t.Fatalf("rows = %+v", rows)
	}
	screen := ansi.Strip(m.mainView(100, 30))
	for _, want := range []string{"log", "3 commits", "wip: one", "wip: two"} {
		if !strings.Contains(screen, want) {
			t.Errorf("screen lacks %q:\n%s", want, screen)
		}
	}
	// enter opens what the commit under the cursor changed
	m = press(t, m, key('G'))
	m = press(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if screen := ansi.Strip(m.mainView(100, 40)); !strings.Contains(screen, "+two") {
		t.Errorf("the commit's changes are not drawn:\n%s", screen)
	}
	// and alt+t goes back to the thread
	m = press(t, m, tea.KeyPressMsg{Code: 't', Mod: tea.ModAlt})
	if m.logv != nil {
		t.Error("alt+t left the log mounted")
	}
}

func TestLogTabSquashesAndRewordsWithoutChangingContent(t *testing.T) {
	m, wt := logFixture(t)
	tree := gitOut(t, wt, "rev-parse", "HEAD^{tree}")
	// fold the last commit into the one before, and reword that group
	m = press(t, m, key('G'))
	m = press(t, m, key('s'))
	m = press(t, m, key('k'))
	m = press(t, m, key('e'))
	d, ok := m.Overlay.Top().(*rewordDialog)
	if !ok {
		t.Fatalf("e did not open the reword dialog (notice %q)", m.notice.text)
	}
	if d.group != 2 {
		t.Errorf("the dialog words a group of %d, want 2", d.group)
	}
	d.input.SetValue("feat: add one and two")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if m.Overlay.HasDialogs() {
		t.Fatal("ctrl+s did not keep the message")
	}
	if got := gitOut(t, wt, "log", "-1", "--format=%s"); got != "wip: two" {
		t.Fatalf("a draft moved the branch: tip is %q", got)
	}

	m = press(t, m, key('a'))
	c, ok := m.Overlay.Top().(*confirmDialog)
	if !ok || c.id != "confirm-rewrite" {
		t.Fatalf("a did not ask first (notice %q)", m.notice.text)
	}
	if !strings.Contains(c.detail, "3 commits → 2") {
		t.Errorf("confirm detail = %q", c.detail)
	}
	m.Overlay.Pop()
	m = pump(t, m, c.onConfirm())

	if m.notice.isErr || !strings.Contains(m.notice.text, "history rewritten") {
		t.Fatalf("notice = %q", m.notice.text)
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD^{tree}"); got != tree {
		t.Errorf("content changed: %s, want %s", got, tree)
	}
	if got := gitOut(t, wt, "log", "--format=%s", "-2"); got != "feat: add one and two\nfeature work" {
		t.Errorf("history = %q", got)
	}
	if m.logv == nil || len(m.logv.log.Rows) != 2 || m.logv.dirty() {
		t.Errorf("the tab did not reload onto the new history: %+v", m.logv)
	}
}

func TestLogTabRefusesWhileTheWorktreeIsDirty(t *testing.T) {
	m, wt := logFixture(t)
	if err := os.WriteFile(filepath.Join(wt, "stray.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := gitOut(t, wt, "rev-parse", "HEAD")
	m = press(t, m, key('G'))
	m = press(t, m, key('s'))
	m = press(t, m, key('a'))
	if _, ok := m.Overlay.Top().(*confirmDialog); ok {
		t.Fatal("a rewrite over uncommitted changes was offered")
	}
	if !m.notice.isErr || !strings.Contains(m.notice.text, "uncommitted") {
		t.Errorf("notice = %q", m.notice.text)
	}
	if gitOut(t, wt, "rev-parse", "HEAD") != before {
		t.Error("a refused rewrite moved the branch")
	}
}

func TestLogRewordRefusesAttribution(t *testing.T) {
	m, _ := logFixture(t)
	m = press(t, m, key('e'))
	d := m.Overlay.Top().(*rewordDialog)
	d.input.SetValue("feat: x\n\nCo-Authored-By: Claude <noreply@anthropic.com>")
	m = press(t, m, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	if _, ok := m.Overlay.Top().(*rewordDialog); !ok || d.errText == "" {
		t.Fatal("a message carrying attribution was kept")
	}
}

// S is offered only where commits are signed and some are not; it drafts
// every unsigned commit to be made again, and applying leaves them all
// signed with the content as it was.
func TestLogTabSignsTheUnsignedCommits(t *testing.T) {
	m, wt := logFixture(t)
	m = press(t, m, key('S'))
	if m.logv.sign || !strings.Contains(m.notice.text, "not signed here") {
		t.Fatalf("S where nothing signs: sign=%v notice=%q", m.logv.sign, m.notice.text)
	}

	stub := filepath.Join(t.TempDir(), "sign")
	script := "#!/bin/sh\ncat >/dev/null\necho '[GNUPG:] SIG_CREATED ' >&2\nprintf -- '-----BEGIN PGP SIGNATURE-----\\n\\nstub\\n-----END PGP SIGNATURE-----\\n'\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "config", "gpg.program", stub)
	git(t, wt, "config", "commit.gpgsign", "true")
	tree := gitOut(t, wt, "rev-parse", "HEAD^{tree}")

	m = press(t, m, key('r'))
	if !m.logv.log.Signable() {
		t.Fatalf("log = %+v, want signable", m.logv.log)
	}
	m = press(t, m, key('S'))
	if screen := ansi.Strip(m.mainView(100, 30)); strings.Count(screen, "to sign") != 3 {
		t.Fatalf("the draft does not mark three commits to sign:\n%s", screen)
	}
	m = press(t, m, key('a'))
	c, ok := m.Overlay.Top().(*confirmDialog)
	if !ok || !strings.Contains(c.detail, "3 commits made again, signed") {
		t.Fatalf("a did not ask first, or not about signing (notice %q)", m.notice.text)
	}
	m.Overlay.Pop()
	m = pump(t, m, c.onConfirm())
	if m.notice.isErr || !strings.Contains(m.notice.text, "history rewritten") {
		t.Fatalf("notice = %q", m.notice.text)
	}
	if got := gitOut(t, wt, "rev-parse", "HEAD^{tree}"); got != tree {
		t.Error("content changed")
	}
	for _, r := range m.logv.log.Rows {
		if !r.Signed {
			t.Errorf("%s %q is not signed", r.Short, r.Subject)
		}
	}
	if m.logv.sign || m.logv.log.Signable() {
		t.Error("a signed branch still offers signing")
	}
}
