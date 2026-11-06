package engine

import (
	"context"
	"strings"
	"testing"
)

// TestRenameCommandRetitlesTheSessionAndMovesItsBranch: /rename alone
// says the session's name and branch; with a title it retitles the
// session, moves its branch and keeps the words it was opened with.
func TestRenameCommandRetitlesTheSessionAndMovesItsBranch(t *testing.T) {
	ctx := context.Background()
	ff, e, _, _ := triageSessionTitled(t, false, true, "", "add spec states")
	say(t, ff, "/rename")
	if out := lastToolOutput(ff); out != `Titled "add spec states", on branch ff/add-spec-states.` {
		t.Fatalf("/rename alone says %q", out)
	}
	if err := e.RenameCommand(ctx, ff.id, "Session spec states"); err != nil {
		t.Fatal(err)
	}
	f, err := e.feature(ctx, ff.id)
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Session spec states" || f.BranchName() != "ff/session-spec-states" || f.OneLiner != "add spec states" {
		t.Fatalf("the session is %q on %s (one-liner %q)", f.Title, f.BranchName(), f.OneLiner)
	}
	wt, _ := e.mgr(ctx, &f)
	if !wt.HasBranch(ctx, "ff/session-spec-states") || wt.HasBranch(ctx, "ff/add-spec-states") {
		t.Fatal("the branch did not move")
	}
}

// TestRenameKeepsAPushedBranch: once the branch has been pushed its name
// is someone else's to know, so only the title changes, and the thread
// says why.
func TestRenameKeepsAPushedBranch(t *testing.T) {
	ctx := context.Background()
	ff, e, _, _ := triageSessionTitled(t, false, true, "", "add spec states")
	say(t, ff, "/rename")
	f, _ := e.feature(ctx, ff.id)
	wt, _ := e.mgr(ctx, &f)
	root := wt.RepoRoot()
	gitIn(t, root, "remote", "add", "origin", root)
	gitIn(t, root, "update-ref", "refs/remotes/origin/"+f.BranchName(), f.BranchName())
	if err := e.RenameCommand(ctx, ff.id, "Session spec states"); err != nil {
		t.Fatal(err)
	}
	f, _ = e.feature(ctx, ff.id)
	if f.Title != "Session spec states" || f.BranchName() != "ff/add-spec-states" {
		t.Fatalf("the session is %q on %s", f.Title, f.BranchName())
	}
	if out := lastToolOutput(ff); !strings.HasSuffix(out, "Its branch stays ff/add-spec-states: it has been pushed.") {
		t.Fatalf("the thread says %q", out)
	}
}

// lastToolOutput is what the session's last tool line on the thread says.
func lastToolOutput(ff *FreeformSession) string {
	tr := ff.Snapshot().Transcript
	for i := len(tr) - 1; i >= 0; i-- {
		if tr[i].Author == AuthorTool {
			return tr[i].ToolOutput
		}
	}
	return ""
}
