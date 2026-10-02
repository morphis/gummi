package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
	"github.com/morphis/gummi/internal/engine"
	"github.com/morphis/gummi/internal/pr"
	"github.com/morphis/gummi/internal/spec"
	"github.com/morphis/gummi/internal/state"
	"github.com/morphis/gummi/internal/ui/theme"
	"github.com/morphis/gummi/internal/webapi"
)

// docsWorkspace is a board with one feature card at implement, a
// worktree cut for it and one committed file on its branch.
func docsWorkspace(t *testing.T) (*WebDocs, *Shell, string) {
	t.Helper()
	m, root := newWorkspace(t)
	ctx := context.Background()
	f := domain.Feature{
		ID: "FD-001", Num: 1, Title: "Dark mode", Slug: "dark-mode",
		Stage: domain.StageImplement, Profile: "thrifty",
		CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	if _, err := m.wt.Create(ctx, &f); err != nil {
		t.Fatal(err)
	}
	commitWork(t, root, "FD-001")
	m = pump(t, m, m.loadRows)
	d, err := m.WebDocs("FD-001")
	if err != nil {
		t.Fatal(err)
	}
	return d, m, root
}

func TestWebDocsRefusesWhatTheBoardLacks(t *testing.T) {
	m, _ := newWorkspace(t)
	if _, err := m.WebDocs("FD-404"); !errors.Is(err, ErrNoCard) {
		t.Fatalf("an unknown card: err = %v, want ErrNoCard", err)
	}
	if _, err := m.WebThreadDocs("FD-404"); !errors.Is(err, ErrNoCard) {
		t.Fatalf("an unknown card's thread: err = %v, want ErrNoCard", err)
	}
	detached := NewShell(theme.GummiDark(), "v0-test")
	if _, err := detached.WebDocs("FD-001"); !errors.Is(err, ErrNoCard) {
		t.Fatalf("a board with no rows: err = %v, want ErrNoCard", err)
	}
}

func TestWebDiffCommentsLifecycle(t *testing.T) {
	d, _, _ := docsWorkspace(t)
	ctx := context.Background()

	diff, err := d.Diff(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Files) != 1 || diff.Files[0].Path != "work.txt" || diff.Files[0].Add != 1 {
		t.Fatalf("diff files = %+v, want the one added work.txt", diff.Files)
	}
	if diff.Rev == "" || diff.BaseRev == "" || diff.Why != "" {
		t.Fatalf("a diff with a change should carry its revs and no reason: %+v", diff)
	}
	var idx int
	var text string
	for _, l := range diff.Files[0].Hunks[0].Lines {
		if l.T == "+" {
			idx, text = l.Idx, l.Text
		}
	}
	if text != "work" {
		t.Fatalf("added line text = %q, want %q", text, "work")
	}

	if _, err := d.AddAnnotation(ctx, idx, "   ", text, "sam"); err == nil {
		t.Fatal("a blank comment was accepted")
	} else if ie := (*InvalidError)(nil); !errors.As(err, &ie) {
		t.Fatalf("a blank comment: err = %T, want *InvalidError", err)
	}
	if _, err := d.AddAnnotation(ctx, 9999, "hi", "", "sam"); !errors.Is(err, ErrMoved) {
		t.Fatalf("a line past the diff: err = %v, want ErrMoved", err)
	}
	if _, err := d.AddAnnotation(ctx, idx, "hi", "not the line", "sam"); !errors.Is(err, ErrMoved) {
		t.Fatalf("a line the page saw differently: err = %v, want ErrMoved", err)
	}

	diff, err = d.AddAnnotation(ctx, idx, " rename this ", text, " sam ")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Annotations) != 1 || diff.PendingComments != 1 {
		t.Fatalf("after commenting: annotations=%d pending=%d, want 1/1", len(diff.Annotations), diff.PendingComments)
	}
	a := diff.Annotations[0]
	if a.Comment != "rename this" || a.By != "sam" || a.Source != "gummi" || a.File != "work.txt" || a.Idx != idx {
		t.Fatalf("annotation = %+v", a)
	}

	diff, err = d.ResolveAnnotation(ctx, a.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Annotations[0].Resolved || diff.PendingComments != 0 {
		t.Fatalf("resolved: %+v pending=%d", diff.Annotations[0], diff.PendingComments)
	}
	diff, err = d.ResolveAnnotation(ctx, a.ID, false)
	if err != nil || diff.PendingComments != 1 {
		t.Fatalf("reopened: err=%v pending=%d, want 1", err, diff.PendingComments)
	}

	if _, err := d.ResolveAnnotation(ctx, a.ID+100, true); !errors.Is(err, ErrNoCard) {
		t.Fatalf("resolving a stranger's comment: err = %v, want ErrNoCard", err)
	}
	if _, err := d.DeleteAnnotation(ctx, a.ID+100); !errors.Is(err, ErrNoCard) {
		t.Fatalf("deleting a stranger's comment: err = %v, want ErrNoCard", err)
	}
	diff, err = d.DeleteAnnotation(ctx, a.ID)
	if err != nil || len(diff.Annotations) != 0 {
		t.Fatalf("deleted: err=%v annotations=%d", err, len(diff.Annotations))
	}
}

func TestWebDiffSinceMarksLaterChanges(t *testing.T) {
	d, _, root := docsWorkspace(t)
	ctx := context.Background()
	first, err := d.Diff(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Diff(ctx, "no-such-commit"); err == nil {
		t.Fatal("a since that names no commit was accepted")
	} else if ie := (*InvalidError)(nil); !errors.As(err, &ie) {
		t.Fatalf("bad since: err = %T, want *InvalidError", err)
	}

	wt := filepath.Join(root, ".gummi", "worktrees", "FD-001")
	if err := os.WriteFile(filepath.Join(wt, "more.txt"), []byte("more\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "more.txt"}, {"commit", "-q", "-m", "more"}} {
		git(t, wt, args...)
	}
	diff, err := d.Diff(ctx, first.Rev)
	if err != nil {
		t.Fatal(err)
	}
	if diff.Since != first.Rev {
		t.Fatalf("Since = %q, want %q", diff.Since, first.Rev)
	}
	for _, f := range diff.Files {
		switch f.Path {
		case "more.txt":
			if !f.Since {
				t.Errorf("more.txt changed after the mark and is not flagged")
			}
			for _, l := range f.Hunks[0].Lines {
				if l.T == "+" && !l.Since {
					t.Errorf("added line %q after the mark is not flagged", l.Text)
				}
			}
		case "work.txt":
			if f.Since {
				t.Errorf("work.txt was already there at the mark and is flagged")
			}
		}
	}
}

func TestWebDiffSaysWhyThereIsNone(t *testing.T) {
	m, _ := newWorkspace(t)
	ctx := context.Background()
	f := domain.Feature{
		ID: "FD-002", Num: 2, Title: "No tree", Slug: "no-tree",
		Stage: domain.StageTodo, Profile: "thrifty", CreatedAt: fixedTime, UpdatedAt: fixedTime,
	}
	if err := m.store.CreateFeature(ctx, &f); err != nil {
		t.Fatal(err)
	}
	m = pump(t, m, m.loadRows)
	d, err := m.WebDocs("FD-002")
	if err != nil {
		t.Fatal(err)
	}
	diff, err := d.Diff(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if diff.Why == "" || len(diff.Files) != 0 || diff.Files == nil {
		t.Fatalf("a card with no worktree: %+v, want a reason and an empty (non-nil) file list", diff)
	}
	if _, err := d.AddAnnotation(ctx, 0, "hi", "", "sam"); err == nil {
		t.Fatal("commenting on a diff that does not exist was accepted")
	}
}

func TestWebSpecNotes(t *testing.T) {
	d, _, root := docsWorkspace(t)
	ctx := context.Background()

	sp, err := d.Spec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sp.None || sp.Why == "" {
		t.Fatalf("before any document: %+v, want None with a reason", sp)
	}
	if _, err := d.AddSpecNote(ctx, 1, "hi", "sam", nil); !errors.Is(err, ErrNoCard) {
		t.Fatalf("a note on a missing document: err = %v, want ErrNoCard", err)
	}

	f := d.f
	path := filepath.Join(root, f.WorktreePath(), f.ArtifactPath())
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	body := "# Dark mode\n\n## Goal\n\nMake it dark.\n\n## Design\n\nUse a theme.\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	sp, err = d.Spec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sp.None || sp.Draft || sp.Title != "Dark mode" || sp.Rev == "" || sp.Markdown != body {
		t.Fatalf("spec = %+v", sp)
	}
	if len(sp.Sections) < 2 || sp.Sections[0].Name != "Dark mode" && sp.Sections[0].Name != "Goal" {
		t.Fatalf("sections = %+v", sp.Sections)
	}
	if sp.Notes == nil || len(sp.Notes) != 0 || sp.Checks == nil {
		t.Fatalf("a clean document should carry empty, non-nil lists: %+v", sp)
	}

	if _, err := d.AddSpecNote(ctx, 5, "  ", "sam", nil); err == nil {
		t.Fatal("a blank note was accepted")
	} else if ie := (*InvalidError)(nil); !errors.As(err, &ie) {
		t.Fatalf("blank note: err = %T, want *InvalidError", err)
	}
	if _, err := d.AddSpecNote(ctx, 999, "hi", "sam", nil); err == nil {
		t.Fatal("a note past the end of the document was accepted")
	}

	sp, err = d.AddSpecNote(ctx, 5, "why dark?", "sam", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.Notes) != 1 {
		t.Fatalf("notes = %+v, want 1", sp.Notes)
	}
	n := sp.Notes[0]
	if n.Author != "user" || n.By != "sam" || n.Text != "why dark?" || n.Resolved {
		t.Fatalf("note = %+v", n)
	}

	stale := webapi.SpecResolveRequest{Line: n.Line, Author: "someone-else", Date: n.Date, Reason: "done"}
	if _, err := d.ResolveSpecNote(ctx, stale, "sam"); !errors.Is(err, ErrMoved) {
		t.Fatalf("resolving a note the page misremembers: err = %v, want ErrMoved", err)
	}
	empty := webapi.SpecResolveRequest{Line: 1, Author: "user", Date: n.Date, Reason: "done"}
	if _, err := d.ResolveSpecNote(ctx, empty, "sam"); !errors.Is(err, ErrMoved) {
		t.Fatalf("resolving a line with no note: err = %v, want ErrMoved", err)
	}
	ok := webapi.SpecResolveRequest{Line: n.Line, Author: n.Author, Date: n.Date, Reason: "because"}
	sp, err = d.ResolveSpecNote(ctx, ok, "sam")
	if err != nil {
		t.Fatal(err)
	}
	if len(sp.Notes) == 0 || !sp.Notes[0].Resolved {
		t.Fatalf("after resolving: %+v", sp.Notes)
	}
	if !strings.Contains(sp.Markdown, "because") {
		t.Fatalf("the resolution's reason is not in the document:\n%s", sp.Markdown)
	}
}

func TestWebSpecReportsDraftAndChecks(t *testing.T) {
	d, m, _ := docsWorkspace(t)
	ctx := context.Background()
	f := d.f
	// a draft lives in the workspace's drafts dir until its worktree has one
	if err := os.MkdirAll(m.ws.DraftsDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	draft := filepath.Join(m.ws.DraftsDir(), spec.DraftFilename(&f))
	body := "# Drafty\n\n## Verification\n\n```gummi-checks\n- name: build\n  cmd: make build\n```\n"
	if err := os.WriteFile(draft, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sp, err := d.Spec(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !sp.Draft || sp.Title != "Drafty" {
		t.Fatalf("draft spec = draft:%v title:%q", sp.Draft, sp.Title)
	}
	if len(sp.Checks) != 1 || sp.Checks[0].Name != "build" || sp.Checks[0].Last != nil || sp.Checks[0].Excused {
		t.Fatalf("checks = %+v, want one unrun, unexcused build", sp.Checks)
	}
}

func TestWebPRUnlinkedOffersThePushCommand(t *testing.T) {
	d, _, _ := docsWorkspace(t)
	ctx := context.Background()
	if got := d.PRLink(ctx); got != "" {
		t.Fatalf("PRLink of an unlinked card = %q", got)
	}
	out := d.PR(ctx)
	if out.Linked {
		t.Fatal("an unlinked card reads as linked")
	}
	if want := "git push -u origin " + d.f.BranchName(); out.PushCommand != want {
		t.Fatalf("push command = %q, want %q", out.PushCommand, want)
	}
}

func TestWebPRLinkedReadsThreadsAndSurvivesGHFailing(t *testing.T) {
	d, m, _ := docsWorkspace(t)
	ctx := context.Background()
	ref := domain.PullRequestRef{Repo: "o/r", Number: 7, URL: "https://github.com/o/r/pull/7"}
	if err := m.store.SetPullRequest(ctx, d.f.ID, ref); err != nil {
		t.Fatal(err)
	}
	d.threads = func(context.Context, domain.PullRequestRef) ([]pr.ReviewThread, []pr.TopLevelComment, string, error) {
		return []pr.ReviewThread{{
			Path: "work.txt", IsOutdated: true,
			DiffHunk: "@@ -0,0 +1,2 @@\n+a\n+b",
			Comments: []pr.ThreadComment{{AuthorLogin: "rev", Body: "nit"}},
		}}, []pr.TopLevelComment{{AuthorLogin: "bot", Body: "hello"}}, "", nil
	}
	if got := d.PRLink(ctx); got != "o/r#7" {
		t.Fatalf("PRLink = %q, want o/r#7", got)
	}
	t.Setenv("PATH", t.TempDir()) // no gh: the live state cannot be read
	out := d.PR(ctx)
	if !out.Linked || out.Ref != "o/r#7" || out.URL == "" {
		t.Fatalf("PR = %+v", out)
	}
	if out.PushCommand == "" {
		t.Fatal("the push command must not depend on GitHub answering")
	}
	if out.Error == "" {
		t.Fatal("a missing gh should be reported in Error, not swallowed")
	}
	if len(out.Threads) != 1 || out.Threads[0].Line != 2 || !out.Threads[0].Outdated ||
		len(out.Threads[0].Notes) != 1 || out.Threads[0].Notes[0].Author != "rev" {
		t.Fatalf("threads = %+v", out.Threads)
	}
	if len(out.Comments) != 1 || out.Comments[0].Author != "bot" {
		t.Fatalf("comments = %+v", out.Comments)
	}
}

func TestWebDocsSmallReaders(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"@alice: looks off", "alice"},
		{"[outdated]\n\n@bob: stale", "bob"},
		{"no at sign", ""},
		{"@no colon here", ""},
		{"@two words: x", ""},
	} {
		if got := prCommentAuthor(c.in); got != c.want {
			t.Errorf("prCommentAuthor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"+added", "added"}, {"-gone", "gone"}, {" ctx", "ctx"}, {"", ""}, {"@@ hunk", "@@ hunk"},
	} {
		if got := diffPayload(c.in); got != c.want {
			t.Errorf("diffPayload(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := docTitle("intro\n# Real title \n## sub", "fb"); got != "Real title" {
		t.Errorf("docTitle = %q", got)
	}
	if got := docTitle("no heading", "fb"); got != "fb" {
		t.Errorf("docTitle fallback = %q", got)
	}
	if got := hunkTailLine("not a hunk"); got != 0 {
		t.Errorf("hunkTailLine(garbage) = %d, want 0", got)
	}
	if got := hunkTailLine("@@ -1,2 +1,3 @@\n a\n+b\n+c"); got != 3 {
		t.Errorf("hunkTailLine = %d, want 3", got)
	}
	ms := []spec.Marker{{Line: 9}, {Line: 2}, {Line: 5}}
	sortMarkers(ms)
	if ms[0].Line != 2 || ms[1].Line != 5 || ms[2].Line != 9 {
		t.Errorf("sortMarkers = %+v", ms)
	}
	if liveWorthSending(webapi.Live{}) || !liveWorthSending(webapi.Live{Busy: true}) {
		t.Error("liveWorthSending should hold back only an entirely idle block")
	}
}

func TestWebThreadFoldsTheLogAndServesOnlyWhatIsNew(t *testing.T) {
	_, m, _ := docsWorkspace(t)
	ctx := context.Background()
	enter, _ := json.Marshal(map[string]string{"role": "implementer", "model": "m"})
	msg, _ := json.Marshal(map[string]string{"author": string(engine.AuthorAssistant), "content": "did the thing"})
	at := fixedTime
	for _, ev := range []state.CardEvent{
		{Feature: "FD-001", Stage: domain.StageImplement, Kind: state.EventStageEnter, At: at, Payload: string(enter)},
		{Feature: "FD-001", Stage: domain.StageImplement, Kind: state.EventMessage, At: at, Payload: string(msg)},
		{Feature: "FD-001", Stage: domain.StageImplement, Kind: state.EventTool, Status: state.StatusOK, At: at, Payload: `{"label":"edit work.txt"}`},
	} {
		if err := m.store.AppendEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	d, err := m.WebThreadDocs("FD-001")
	if err != nil {
		t.Fatal(err)
	}
	th, err := d.Thread(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Items) < 2 || th.LastSeq == 0 {
		t.Fatalf("thread = %+v, want the stage, message and tool folded in", th)
	}
	if th.Items[0].T != webapi.ItemStage || th.Items[0].Role != "implementer" {
		t.Fatalf("first item = %+v, want the implementer's stage opener", th.Items[0])
	}
	var said bool
	for _, it := range th.Items {
		if strings.Contains(it.Text, "did the thing") {
			said = true
		}
	}
	if !said {
		t.Fatalf("the assistant's message is missing from %+v", th.Items)
	}

	again, err := d.Thread(ctx, th.LastSeq)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Items) != 0 || again.Items == nil {
		t.Fatalf("a read after the newest seq returned %d items (nil=%v), want an empty non-nil list", len(again.Items), again.Items == nil)
	}
	if again.LastSeq != th.LastSeq {
		t.Fatalf("LastSeq moved from %d to %d with nothing new", th.LastSeq, again.LastSeq)
	}
}
