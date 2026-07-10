package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/morphis/gummi/internal/agent"
	"github.com/morphis/gummi/internal/domain"
)

// rebaseFeatureFixture creates a feature with a worktree branched from
// main, and returns the shell, repo root, and worktree path.
func rebaseFeatureFixture(t *testing.T) (*Shell, string, string) {
	t.Helper()
	m, root := newWorkspace(t)
	m.now = func() time.Time { return fixedTime }
	ctx := context.Background()
	f := &domain.Feature{
		ID: "FD-001", Num: 1, Title: "Rebase me", Slug: "rebase-me",
		Stage: domain.StageImplement, CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, f); err != nil {
		t.Fatal(err)
	}
	wt, err := m.wt.Create(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.Init())
	return m, root, wt
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.CommandContext(context.Background(), "git",
		append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestRebaseCleanSucceeds(t *testing.T) {
	m, root, wt := rebaseFeatureFixture(t)
	// advance main with a non-conflicting change
	if err := os.WriteFile(filepath.Join(root, "NEW.md"), []byte("main advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "main advance")
	// a feature commit that doesn't touch NEW.md
	if err := os.WriteFile(filepath.Join(wt, "feat.go"), []byte("package x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature work")

	f, _ := m.store.GetFeature(context.Background(), "FD-001")
	m = pump(t, m, m.rebaseFeature(f))
	if m.notice.isErr || !strings.Contains(m.notice.text, "rebased onto main") {
		t.Fatalf("clean rebase: notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
}

func TestRebaseDirtyRefused(t *testing.T) {
	m, _, wt := rebaseFeatureFixture(t)
	// leave an uncommitted change in the worktree
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, _ := m.store.GetFeature(context.Background(), "FD-001")
	m = pump(t, m, m.rebaseFeature(f))
	if !m.notice.isErr || !strings.Contains(m.notice.text, "uncommitted") {
		t.Fatalf("dirty rebase: notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
}

func TestRebaseConflictNamesFile(t *testing.T) {
	m, root, wt := rebaseFeatureFixture(t)
	// conflicting edits to README.md on both main and the feature branch
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("feature version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature edit")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("main version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "main edit")

	f, _ := m.store.GetFeature(context.Background(), "FD-001")
	m = pump(t, m, m.rebaseFeature(f))
	if !m.notice.isErr || !strings.Contains(m.notice.text, "README.md") {
		t.Fatalf("conflict rebase: notice = %q (err=%v)", m.notice.text, m.notice.isErr)
	}
	// and the worktree is left clean (self-aborted)
	if dirty, err := m.wt.Dirty(context.Background(), &f); dirty || err != nil {
		t.Errorf("worktree dirty after aborted rebase: %v %v", dirty, err)
	}
}

func TestRebaseRecoversStrandedRebase(t *testing.T) {
	// a rebase stranded mid-flight (crash, killed agent) must not wedge
	// the r key: it is aborted and the rebase retried.
	m, root, wt := rebaseFeatureFixture(t)
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("feature version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature edit")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("main version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "main edit")
	// strand a rebase on the conflict
	if out, err := exec.CommandContext(context.Background(), "git", "-C", wt, "rebase", "main").CombinedOutput(); err == nil {
		t.Fatalf("conflicting rebase did not stop:\n%s", out)
	}

	f, _ := m.store.GetFeature(context.Background(), "FD-001")
	m = pump(t, m, m.rebaseFeature(f))
	// without the recovery this reads "rebase of FD-001 did not start";
	// with it, the retry reaches the real conflict report.
	if !strings.Contains(m.notice.text, "README.md") {
		t.Fatalf("stranded rebase not recovered: notice = %q", m.notice.text)
	}
	if in, err := m.wt.RebaseInProgress(context.Background(), &f); in || err != nil {
		t.Errorf("still mid-rebase after r: %v %v", in, err)
	}
}

// conflictOnMain gives FD-001 (created by chatWorkspace) a worktree via
// the stage flow, then commits conflicting README.md edits on both the
// branch and main, returning the refreshed feature.
func conflictOnMain(t *testing.T, m *Shell) (*Shell, domain.Feature) {
	t.Helper()
	m = advanceTo(t, m, domain.StageVerify)
	root := m.wt.Root()
	wt := filepath.Join(root, ".gummi", "worktrees", "FD-001")
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("feature version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, wt, "add", ".")
	git(t, wt, "commit", "-qm", "feature edit")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("main version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "main edit")
	f, err := m.store.GetFeature(context.Background(), "FD-001")
	if err != nil {
		t.Fatal(err)
	}
	return m, f
}

func TestAgentRebaseEscalatesWhenUnresolved(t *testing.T) {
	// the agent claims success but resolves nothing; the git state, not
	// the transcript, decides — and it says escalate.
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "All resolved."},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := chatWorkspace(t, ag)
	m, f := conflictOnMain(t, m)

	m = pump(t, m, m.rebaseFeature(f))
	top := m.Overlay.Top()
	if top == nil || top.ID() != "agent-rebase" {
		t.Fatalf("conflict did not offer the agent hand-off (notice %q)", m.notice.text)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	if !strings.Contains(m.notice.text, "dispatched") {
		t.Fatalf("confirm did not dispatch the agent: notice = %q", m.notice.text)
	}
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if !m.notice.isErr || !strings.Contains(m.notice.text, "agent rebase failed") {
		t.Errorf("notice = %q (err=%v), want agent rebase failed", m.notice.text, m.notice.isErr)
	}
	escalated := false
	for _, it := range m.inbox.list() {
		if it.Feature == "FD-001" && it.Kind == attnGate && it.Escalated {
			escalated = true
		}
	}
	if !escalated {
		t.Error("unresolved agent rebase did not escalate to the inbox")
	}
	if f.Stage != domain.StageVerify {
		t.Errorf("stage moved to %s", f.Stage)
	}
}

func TestAgentRebaseSuccessReVerifies(t *testing.T) {
	// a scripted agent that really performs the rebase in the worktree;
	// on success the feature re-enters verify automatically.
	var rebases atomic.Int32
	shaRe := regexp.MustCompile(`git rebase ([0-9a-f]+)`)
	ag := &agent.Fake{Responder: func(opts agent.SessionOpts, msg string) []agent.Event {
		if strings.Contains(msg, "Proceed with the rebase") {
			rebases.Add(1)
			sha := shaRe.FindStringSubmatch(msg)
			if sha == nil {
				t.Error("rebase kickoff names no target commit")
				return []agent.Event{{Kind: agent.EventIdle}}
			}
			gitIn := func(mustPass bool, args ...string) {
				out, err := exec.CommandContext(context.Background(), "git",
					append([]string{"-C", opts.WorkDir}, args...)...).CombinedOutput()
				if mustPass && err != nil {
					t.Errorf("agent git %v: %v\n%s", args, err, out)
				}
			}
			gitIn(false, "rebase", sha[1]) // stops on the conflict
			if err := os.WriteFile(filepath.Join(opts.WorkDir, "README.md"), []byte("merged version\n"), 0o600); err != nil {
				t.Error(err)
			}
			gitIn(true, "add", "README.md")
			gitIn(true, "-c", "core.editor=true", "rebase", "--continue")
			return []agent.Event{
				{Kind: agent.EventMessage, Text: "Conflicts resolved, rebase complete."},
				{Kind: agent.EventIdle},
			}
		}
		return []agent.Event{
			{Kind: agent.EventMessage, Text: "Checks green.\nVERDICT: pass"},
			{Kind: agent.EventIdle},
		}
	}}
	m, eng := chatWorkspace(t, ag)
	m, f := conflictOnMain(t, m)

	m = pump(t, m, m.rebaseFeature(f))
	if top := m.Overlay.Top(); top == nil || top.ID() != "agent-rebase" {
		t.Fatalf("conflict did not offer the agent hand-off (notice %q)", m.notice.text)
	}
	m = press(t, m, tea.KeyPressMsg{Code: 'y', Text: "y"})
	settleChat(t, eng)
	m = drainEngineLoop(t, m)

	if n := rebases.Load(); n != 1 {
		t.Fatalf("rebase sessions = %d, want 1", n)
	}
	if ok, err := m.wt.RebasedOnMain(context.Background(), &f); !ok || err != nil {
		t.Fatalf("branch not rebased onto main: %v %v", ok, err)
	}
	// the quality floor: verify re-ran (a fresh non-rebase session), and
	// its pass raised the landing gate
	s := eng.Get("FD-001")
	if s == nil || s.Snapshot().Rebase {
		t.Fatal("verify re-run did not replace the rebase session")
	}
	gated := false
	for _, it := range m.inbox.list() {
		if it.Feature == "FD-001" && it.Kind == attnGate && strings.Contains(it.Text, "verify passed") {
			gated = true
		}
	}
	if !gated {
		t.Errorf("re-verify did not raise the landing gate; inbox: %+v", m.inbox.list())
	}
}
