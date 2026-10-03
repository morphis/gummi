package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/agent"
)

func writeCommandFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestProjectCommandsAreReadFromEveryBackendsDirectory: each backend's
// own command directory is read, a subdirectory of Claude Code's
// namespaces a name, and the first directory to define a name owns it.
func TestProjectCommandsAreReadFromEveryBackendsDirectory(t *testing.T) {
	dir := t.TempDir()
	writeCommandFile(t, dir, ".claude/commands/review.md", "---\ndescription: review the diff\n---\nReview $ARGUMENTS.")
	writeCommandFile(t, dir, ".claude/commands/git/fixup.md", "# Squash fixups\nSquash them.")
	writeCommandFile(t, dir, ".opencode/command/review.md", "the opencode one")
	writeCommandFile(t, dir, ".opencode/command/bench.md", "Run $1 for $2 rounds.")
	writeCommandFile(t, dir, ".github/prompts/explain.prompt.md", "Explain it.")
	writeCommandFile(t, dir, ".github/prompts/notes.md", "not a prompt file")

	var got []string
	for _, c := range LoadProjectCommands(dir) {
		got = append(got, c.Name+"="+c.Description+"@"+c.Source)
	}
	want := []string{
		"bench=Run $1 for $2 rounds.@.opencode/command/bench.md",
		"explain=Explain it.@.github/prompts/explain.prompt.md",
		"git:fixup=Squash fixups@.claude/commands/git/fixup.md",
		"review=review the diff@.claude/commands/review.md",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestProjectCommandExpansion(t *testing.T) {
	cmds := []ProjectCommand{
		{Name: "review", body: "Review $ARGUMENTS."},
		{Name: "bench", body: "Run $1 for $2 rounds."},
		{Name: "explain", body: "Explain it."},
	}
	for _, tc := range []struct{ in, want string }{
		{"/review the parser", "Review the parser."},
		{"/Bench go 3", "Run go for 3 rounds."},
		{"/explain the lock", "Explain it.\n\nARGUMENTS: the lock"},
		{"/explain", "Explain it."},
		{"/unknown x", "/unknown x"},
		{"see /review", "see /review"},
		{"fix it\n\n/review the fix", "fix it\n\nReview the fix."},
	} {
		if got := expandProjectCommands(cmds, "", tc.in); got != tc.want {
			t.Errorf("expand(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestProjectCommandReferences: "@path" inlines a file the worktree holds
// and nothing outside it; "!`cmd`" is never run by gummi, only handed to
// the agent to run under its own confinement.
func TestProjectCommandReferences(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("hush"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "STYLE.md"), []byte("tabs, not spaces\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(dir, "leak")); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ran")
	cmds := []ProjectCommand{{Name: "check", body: "Follow @docs/STYLE.md, not @leak or @missing.md, given !`touch " + marker + "`."}}
	got := expandProjectCommands(cmds, dir, "/check")

	if !strings.Contains(got, "Contents of docs/STYLE.md:\n```\ntabs, not spaces\n```") {
		t.Errorf("the worktree's file was not inlined:\n%s", got)
	}
	if strings.Contains(got, "hush") || strings.Contains(got, "Contents of leak") || strings.Contains(got, "Contents of missing.md") {
		t.Errorf("a reference outside the worktree, or to nothing, was inlined:\n%s", got)
	}
	if !strings.HasPrefix(got, "Before anything else, run these commands") || !strings.Contains(got, "- `touch "+marker+"`") ||
		!strings.Contains(got, "given the output of `touch "+marker+"`.") {
		t.Errorf("the shell line was not handed to the agent:\n%s", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("gummi ran the command file's shell line itself")
	}
}

// TestAFreeformTurnExpandsAProjectCommand: the agent hears the command's
// prompt; the transcript keeps the line as it was typed.
func TestAFreeformTurnExpandsAProjectCommand(t *testing.T) {
	var heard []string
	ag := &agent.Fake{Responder: func(_ agent.SessionOpts, msg string) []agent.Event {
		heard = append(heard, msg)
		return []agent.Event{{Kind: agent.EventMessage, Text: "ok"}, {Kind: agent.EventIdle}}
	}}
	ws, store, wt := newRepo(t)
	e := New(Config{Agents: singleAgent(ag), Store: store, Worktrees: wt, Workspace: ws, Model: "m"})
	t.Cleanup(func() { e.Close() })
	ctx := context.Background()
	f := freeformCard(1, "tidy the parser")
	createFeature(t, store, f)
	ff, err := e.OpenFreeform(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if err := ff.Send(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)
	writeCommandFile(t, ff.workDir, ".claude/commands/review.md", "Review $ARGUMENTS for leaks.")
	if err := ff.Send(ctx, "/review the parser"); err != nil {
		t.Fatal(err)
	}
	waitFreeformIdle(t, ff)

	if len(heard) != 2 || heard[1] != "Review the parser for leaks." {
		t.Errorf("agent heard %q, want the expanded prompt second", heard)
	}
	var users []string
	for _, m := range ff.Session().Snapshot().Transcript {
		if m.Author == AuthorUser {
			users = append(users, m.Content)
		}
	}
	if len(users) != 2 || users[1] != "/review the parser" {
		t.Errorf("transcript users = %q, want the line as typed", users)
	}
}
