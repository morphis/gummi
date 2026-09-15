package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A claude session that continues a conversation the CLI still holds, and
// the guard that keeps a conversation it does not hold from failing a run.

// A session that continues a conversation hands the CLI that
// conversation's id, so a restored ask lands in a session that still has
// the repository open.
func TestClaudeCodeResumesAKnownConversation(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	wd := t.TempDir()
	wd, _ = filepath.EvalSymlinks(wd)
	dir := filepath.Join(cfg, "projects", claudeProjectSlug(wd))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	const id = "11111111-2222-3333-4444-555555555555"
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	msg := claudeRosterArgv(t, claudeArgvEchoScript, SessionOpts{WorkDir: wd, ResumeID: id})
	if !strings.Contains(msg, "--resume "+id) {
		t.Errorf("argv missing --resume %s: %s", id, msg)
	}
}

// An id the CLI no longer holds is dropped rather than passed: `--resume
// <unknown>` exits 1 with an error result, which would turn a saved
// re-read into a failed stage.
func TestClaudeCodeSkipsAnUnknownConversation(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	msg := claudeRosterArgv(t, claudeArgvEchoScript, SessionOpts{ResumeID: "no-such-session"})
	if strings.Contains(msg, "--resume") {
		t.Errorf("argv resumes a conversation the CLI does not hold: %s", msg)
	}
}

// The slug is the CLI's own transcript-directory mangling: anything that
// is not a letter, digit or hyphen becomes a hyphen.
func TestClaudeProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/repo/.gummi/worktrees/FD-001": "-repo--gummi-worktrees-FD-001",
		"/tmp/a.b_c-d":                  "-tmp-a-b-c-d",
		"/project":                      "-project",
	}
	for in, want := range cases {
		if got := claudeProjectSlug(in); got != want {
			t.Errorf("claudeProjectSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

// Resume is an optimization, never a way for a session to fail to start:
// with no discoverable config dir there is nothing to confirm against, so
// the flag is left off.
func TestClaudeResumableFailsClosed(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "absent"))
	if claudeResumable(t.TempDir(), "some-id") {
		t.Error("claudeResumable confirmed a transcript that does not exist")
	}
	if claudeResumable("", "some-id") || claudeResumable(t.TempDir(), "") {
		t.Error("claudeResumable accepted an empty workdir or id")
	}
}

// The adapter publishes the CLI's conversation id: without it the engine
// has nothing to hand back as ResumeID, and every reattach opens a blank
// conversation.
func TestClaudeCodeReportsItsSessionID(t *testing.T) {
	ag, err := NewClaudeCode(writeFakeClaude(t, fakeClaudeScript))
	if err != nil {
		t.Fatal(err)
	}
	defer ag.Close()
	sess, err := ag.NewSession(context.Background(), SessionOpts{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	id, ok := sess.(Identified)
	if !ok {
		t.Fatal("claude session does not implement Identified")
	}
	if err := sess.Send(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	collect(t, sess)
	if id.SessionID() == "" {
		t.Error("no session id after a turn; the engine has nothing to resume from")
	}
}
