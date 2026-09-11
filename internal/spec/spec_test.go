package spec

import (
	"strings"
	"testing"

	"github.com/morphis/gummi/internal/domain"
)

const sample = `# FD-042: Dark mode

The toggle persists via localStorage.
%% @user(2026-07-03): per-device or synced to the account?
%% @architect: resolved — per-device; sync deferred to FD-051.

## Approaches

Use CSS variables.
%% plain unattributed question
%% @reviewer(2026-07-04): does this cover embedded webviews?

Text after.
`

func TestParseMarkers(t *testing.T) {
	d := Parse(sample)
	if len(d.Markers) != 4 {
		t.Fatalf("got %d markers, want 4: %+v", len(d.Markers), d.Markers)
	}
	m0 := d.Markers[0]
	if m0.Author != "user" || m0.Date != "2026-07-03" || m0.Resolved ||
		m0.Text != "per-device or synced to the account?" || m0.Line != 4 || m0.Anchor != 3 {
		t.Errorf("marker 0 parsed wrong: %+v", m0)
	}
	m1 := d.Markers[1]
	if m1.Author != "architect" || m1.Date != "" || !m1.Resolved || m1.Anchor != 3 {
		t.Errorf("marker 1 parsed wrong: %+v", m1)
	}
	m2 := d.Markers[2]
	if m2.Author != "" || m2.Text != "plain unattributed question" || m2.Resolved || m2.Anchor != 9 {
		t.Errorf("marker 2 parsed wrong: %+v", m2)
	}
	m3 := d.Markers[3]
	if m3.Author != "reviewer" || m3.Anchor != 9 || m3.Resolved {
		t.Errorf("marker 3 parsed wrong: %+v", m3)
	}
}

// TestThreadsAndOpenQuestions used to assert that thread 0 (the `sample`
// fixture's @user question, answered only by "@architect: resolved") came
// back resolved. That was the old, and wrong, rule: any author's
// resolution closed every marker above it on the shared anchor, human
// comments included. That is exactly the bug the 2026-09-10 round-2 UX
// drive hit live (REVIEW §1.2) — an architect's "resolved —" on a
// reviewer's finding silently closed a human's untouched comment sharing
// the anchor, and the gate opened unanswered. The rule now is: only a
// @user resolution closes a @user marker, so thread 0 must stay open
// until the user (or the `x` key, which writes a @user resolution)
// closes it themselves.
func TestThreadsAndOpenQuestions(t *testing.T) {
	d := Parse(sample)
	threads := d.Threads()
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2: %+v", len(threads), threads)
	}
	if threads[0].Resolved {
		t.Error("thread 0's @user marker is only answered by @architect, not @user; should stay open")
	}
	if threads[1].Resolved {
		t.Error("thread 1 has no resolution; should be open")
	}
	open := d.OpenQuestions()
	if len(open) != 2 {
		t.Fatalf("open questions = %+v, want both threads (anchor 3's unanswered @user comment + anchor 9)", open)
	}
}

func TestThreadsGroupByAnchor(t *testing.T) {
	// distinct anchors → distinct threads
	doc := Parse("alpha\n%% q1\nbeta\n%% q2\n")
	threads := doc.Threads()
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2", len(threads))
	}
	if threads[0].Anchor != 1 || threads[1].Anchor != 3 {
		t.Errorf("anchors = %d,%d want 1,3", threads[0].Anchor, threads[1].Anchor)
	}
	// same anchor with a blank line between markers → ONE thread, and a
	// late resolution closes it
	doc = Parse("alpha\n%% q1\n\n%% @a: resolved — late answer\n")
	threads = doc.Threads()
	if len(threads) != 1 {
		t.Fatalf("blank-separated same-anchor markers: got %d threads, want 1", len(threads))
	}
	if !threads[0].Resolved || len(doc.OpenQuestions()) != 0 {
		t.Error("late resolution did not close the thread")
	}
}

func TestResolvedDetection(t *testing.T) {
	cases := map[string]bool{
		"%% @a: resolved — done":              true,
		"%% @a: Resolved: yep":                true,
		"%% @a: resolved":                     true,
		"%% resolved - fine":                  true,
		"%% resolved by design":               false, // prose, not a resolution marker
		"%% resolved ordering still unclear?": false,
		"%% @a: unresolved worry":             false,
		"%% is this resolved?":                false,
		"%% @a: resolved-ish, still broken":   false, // hyphen joins a word, not a separator
		"%% resolvedness unclear":             false,
	}
	for line, want := range cases {
		d := Parse("anchor\n" + line)
		if got := d.Markers[0].Resolved; got != want {
			t.Errorf("%q resolved = %v, want %v", line, got, want)
		}
	}
}

func TestDocLevelMarker(t *testing.T) {
	d := Parse("%% floating question before any content\nBody.")
	if len(d.Markers) != 1 || d.Markers[0].Anchor != 0 {
		t.Fatalf("doc-level marker parsed wrong: %+v", d.Markers)
	}
}

func TestAddComment(t *testing.T) {
	content := "line one\n%% @a: existing\nline three"
	out, err := AddComment(content, 1, "user", "2026-07-04", "my note")
	if err != nil {
		t.Fatal(err)
	}
	want := "line one\n%% @a: existing\n%% @user(2026-07-04): my note\nline three"
	if out != want {
		t.Errorf("AddComment = %q, want %q", out, want)
	}
	// round-trips through the parser as one thread of two markers
	d := Parse(out)
	threads := d.Threads()
	if len(threads) != 1 || len(threads[0].Markers) != 2 {
		t.Fatalf("threads after add = %+v", threads)
	}

	// newlines flattened, empty rejected, out of range rejected
	if _, err := AddComment(content, 1, "u", "d", "  \n "); err == nil {
		t.Error("empty comment accepted")
	}
	if _, err := AddComment(content, 99, "u", "d", "x"); err == nil {
		t.Error("out-of-range line accepted")
	}
	multi, err := AddComment(content, 3, "u", "d", "a\nb")
	if err != nil || strings.Count(multi, "%% @u(d): a b") != 1 {
		t.Errorf("multiline flattening failed: %v %q", err, multi)
	}
}

func TestResolveComment(t *testing.T) {
	content := "anchor\n%% @gummi: first\n%% @user: open question\n%% @gummi: another\nbody\n%% @gummi: separate thread"
	// resolve the @user marker in the first thread: the resolution splices
	// in immediately after THAT marker, not the thread's last one, so the
	// trailing @gummi marker below stays open
	out, err := ResolveComment(content, 3, "user", "2026-07-03")
	if err != nil {
		t.Fatal(err)
	}
	want := "anchor\n%% @gummi: first\n%% @user: open question\n%% @user(2026-07-03): resolved\n%% @gummi: another\nbody\n%% @gummi: separate thread"
	if out != want {
		t.Errorf("ResolveComment = %q, want %q", out, want)
	}
	// the resolution closed the @user marker and everything above it; the
	// trailing @gummi marker stays open, so no user thread blocks the gate
	// even though the thread still has an open (non-user) marker
	d := Parse(out)
	if len(d.UserOpenThreads()) != 0 {
		t.Fatalf("open user threads after resolve: %+v", d.UserOpenThreads())
	}
	if len(d.OpenQuestions()) != 2 {
		t.Fatalf("open questions after resolve = %d, want 2 (the trailing marker + separate thread)", len(d.OpenQuestions()))
	}

	// multi-marker thread: resolving the FIRST marker leaves the ones below
	// it open (per-marker model) — the @user question must stay open
	multi, err := ResolveComment(content, 2, "u", "d")
	if err != nil {
		t.Fatal(err)
	}
	wantMulti := "anchor\n%% @gummi: first\n%% @u(d): resolved\n%% @user: open question\n%% @gummi: another\nbody\n%% @gummi: separate thread"
	if multi != wantMulti {
		t.Errorf("multi-marker thread = %q, want %q", multi, wantMulti)
	}
	dm := Parse(multi)
	if len(dm.UserOpenThreads()) != 1 {
		t.Fatalf("user open threads after resolving first marker = %d, want 1 (the @user below stays open)", len(dm.UserOpenThreads()))
	}

	// single-marker thread: the common case is unaffected — the resolution
	// empties the user-open threads
	single, err := ResolveComment("anchor\n%% @user: a question\n", 2, "user", "2026-07-03")
	if err != nil {
		t.Fatal(err)
	}
	if single != "anchor\n%% @user: a question\n%% @user(2026-07-03): resolved\n" {
		t.Errorf("single-marker thread = %q", single)
	}
	if got := len(Parse(single).UserOpenThreads()); got != 0 {
		t.Errorf("user open threads after resolving single marker = %d, want 0", got)
	}

	// errors on a non-marker line and out of range
	if _, err := ResolveComment(content, 1, "u", "d"); err == nil {
		t.Error("non-marker line accepted")
	}
	if _, err := ResolveComment(content, 99, "u", "d"); err == nil {
		t.Error("out-of-range line accepted")
	}
}

func TestFindAnchor(t *testing.T) {
	content := "## Problem\nThe toggle persists via localStorage.\n%% @gummi: note\nAnother line about storage.\nThe toggle persists via localStorage.\n"
	// unique content line
	if line, ok := FindAnchor(content, "Another line about storage"); !ok || line != 4 {
		t.Errorf("unique anchor = %d,%v; want 4,true", line, ok)
	}
	// duplicated line → not unique → fail closed
	if _, ok := FindAnchor(content, "persists via localStorage"); ok {
		t.Error("duplicated snippet should not resolve to an anchor")
	}
	// snippet only on a marker line is not an anchor
	if _, ok := FindAnchor(content, "@gummi: note"); ok {
		t.Error("marker line should never be an anchor")
	}
	// missing snippet
	if _, ok := FindAnchor(content, "nonexistent"); ok {
		t.Error("missing snippet resolved")
	}
	// empty snippet
	if _, ok := FindAnchor(content, "   "); ok {
		t.Error("empty snippet resolved")
	}
}

func TestTemplateParsesWithOpenQuestions(t *testing.T) {
	f := &domain.Feature{ID: "FD-007", Num: 7, Title: "Search", OneLiner: "find things", Slug: "search", Stage: domain.StageTodo}
	tpl := Template(f)
	if !strings.Contains(tpl, "# FD-007: Search") || !strings.Contains(tpl, "> find things") {
		t.Error("template missing header bits")
	}
	d := Parse(tpl)
	if got := len(d.OpenQuestions()); got != 7 {
		t.Errorf("template has %d open questions, want 7", got)
	}
	// scope boundaries live between the problem and the approaches
	problem := strings.Index(tpl, "## Problem")
	scope := strings.Index(tpl, "## Out of scope")
	considered := strings.Index(tpl, "## Considered approaches")
	if scope < 0 || (problem >= scope || scope >= considered) {
		t.Errorf("Out of scope section misplaced: problem=%d scope=%d considered=%d", problem, scope, considered)
	}
	if DraftFilename(f) != "FD-007-search.md" {
		t.Errorf("draft filename = %s", DraftFilename(f))
	}
}

// TestResolutionDoesNotCloseLaterComment covers two user comments on one
// anchor line, an agent resolution between them, and a later comment
// below the resolution reopening the thread. It used to also demonstrate
// (without asserting) that the architect's resolution closed the FIRST
// comment, per-marker, "above it in the run" — that was the old rule.
// Under the current rule a non-@user resolution never closes a @user
// marker at all, so neither user comment is closed here: both the
// "per-device or synced?" question and the later "what about SSR?" one
// stay open. What this test actually pins — that a single resolution
// must not unblock the gate while a later, un-addressed comment sits
// below it — still holds, and is asserted explicitly for both markers
// below.
func TestResolutionDoesNotCloseLaterComment(t *testing.T) {
	doc := "## Problem\n\nThe toggle persists via localStorage.\n" +
		"%% @user(2026-08-16): per-device or synced?\n" +
		"%% @architect: resolved — per-device.\n" +
		"%% @user(2026-08-16): what about SSR?\n"
	d := Parse(doc)

	threads := d.Threads()
	if got := len(threads); got != 1 {
		t.Fatalf("Threads() = %d, want 1 (one anchor line)", got)
	}
	if threads[0].Resolved {
		t.Error("thread reports resolved, but neither @user comment was answered by a human")
	}
	for _, mk := range threads[0].Markers {
		if mk.Author == "user" && mk.Resolved {
			t.Errorf("marker %q closed by a non-@user resolution", mk.Text)
		}
	}

	open := d.UserOpenThreads()
	if len(open) != 1 {
		t.Fatalf("UserOpenThreads() = %d, want 1 (both questions unanswered, gate must stay shut)", len(open))
	}

	// UnresolvedUserMarker reports the LAST unresolved @user marker in the
	// thread — the SSR question — even though the per-device question is
	// also still open; callers that need every open comment use the
	// thread's Markers directly.
	m := UnresolvedUserMarker(open[0])
	if m == nil || m.Text != "what about SSR?" {
		t.Fatalf("unresolved user marker = %+v, want the SSR comment", m)
	}
}

// TestArchitectResolutionDoesNotCloseUserComment reproduces the exact
// live failure from the 2026-09-10 round-2 UX drive (REVIEW §1.2): the
// user commented on the spec ("Name the flag -n, not --limit"), a
// reviewer later filed its own finding on the SAME anchor line, and the
// architect's "resolved —" for the reviewer's finding closed the human's
// comment too, because every marker sharing an anchor is one thread and
// (under the old rule) any author's resolution closed everything above
// it. The gate then opened with the user's comment never addressed by
// anyone. The thread must stay open and UserOpenThreads must still
// report it; the reviewer's own finding, being agent-to-agent, is still
// correctly closed by the architect — agents keep the old behaviour
// among themselves.
func TestArchitectResolutionDoesNotCloseUserComment(t *testing.T) {
	doc := "## Verification plan\n\n" +
		"- the default is still ten when -n is not given\n" +
		"%% @user(2026-09-10): Name the flag -n, not --limit — short flags fit the rest of this CLI.\n" +
		"%% @reviewer(2026-09-10): blocking — add a machine-run `go test ./...` check to this section.\n" +
		"%% @architect: resolved — added `go build ./...` and `go test ./...` as machine-run checks below.\n"
	d := Parse(doc)

	threads := d.Threads()
	if len(threads) != 1 {
		t.Fatalf("Threads() = %d, want 1 (one anchor line)", len(threads))
	}
	if threads[0].Resolved {
		t.Error("thread reports resolved, but the @user comment was never answered by a human")
	}

	open := d.UserOpenThreads()
	if len(open) != 1 {
		t.Fatalf("UserOpenThreads() = %d, want 1 (the gate must stay shut)", len(open))
	}
	m := UnresolvedUserMarker(open[0])
	if m == nil || !strings.Contains(m.Text, "-n, not --limit") {
		t.Fatalf("unresolved user marker = %+v, want the flag-naming comment", m)
	}

	for _, mk := range threads[0].Markers {
		if mk.Author == "reviewer" && !mk.Resolved {
			t.Error("reviewer's finding is agent-to-agent and should still be closed by the architect's resolution")
		}
	}
}

// TestIndentedContinuationDoesNotResplitAThread pins Parse's one
// exception to anchoring, and the live defect that earned it.
//
// A user commented and then resolved their own comment — one closed
// thread. The verify pass later appended its evidence UNDER the first
// marker, indented. That plain line used to become an anchor, splitting
// the resolution away from the comment it resolved, so the comment
// reopened and the gate shut again over a decision the human had already
// closed. Thread membership must not move because of what an agent
// appended near the thread afterwards.
func TestIndentedContinuationDoesNotResplitAThread(t *testing.T) {
	const anchorLine = "- the default is still ten when -n is not given\n"
	closed := anchorLine +
		"%% @user: Name the flag -n, not --limit\n" +
		"%% @user: resolved\n"
	withEvidence := anchorLine +
		"%% @user: Name the flag -n, not --limit\n" +
		"  RESULT: PASS. printed exactly 10 rows.\n" +
		"%% @user: resolved\n"

	for _, tc := range []struct {
		name string
		doc  string
	}{
		{"as the user left it", closed},
		{"after verify appended its evidence", withEvidence},
	} {
		d := Parse(tc.doc)
		if got := len(d.Threads()); got != 1 {
			t.Errorf("%s: %d threads, want 1 — the continuation line re-anchored", tc.name, got)
		}
		if got := len(d.UserOpenThreads()); got != 0 {
			t.Errorf("%s: %d open user threads, want 0 — a resolved comment reopened", tc.name, got)
		}
	}
}

// TestUnindentedContentStillAnchors is the other half of the rule: only
// an INDENTED line is a continuation. New content at column 0 starts a
// new anchor as it always has, so a marker on the next paragraph is not
// swallowed into the previous conversation.
func TestUnindentedContentStillAnchors(t *testing.T) {
	d := Parse("first line\n%% @user: about the first\nsecond line\n%% @user: about the second\n")
	threads := d.Threads()
	if len(threads) != 2 {
		t.Fatalf("%d threads, want 2 — unindented content must still anchor", len(threads))
	}
	if threads[0].Anchor == threads[1].Anchor {
		t.Errorf("both markers anchored to line %d; they annotate different lines", threads[0].Anchor)
	}
}

// TestContinuationRunEndsAtTheNextContentLine keeps the exception
// bounded: once unindented content ends the run, a later indented line
// (a nested list item, say) is ordinary content again and anchors.
func TestContinuationRunEndsAtTheNextContentLine(t *testing.T) {
	d := Parse("parent\n%% @user: q\n  continuation of the marker\nnext paragraph\n  - an indented child bullet\n%% @user: about the child\n")
	threads := d.Threads()
	if len(threads) != 2 {
		t.Fatalf("%d threads, want 2", len(threads))
	}
	if want := 5; threads[1].Anchor != want {
		t.Errorf("second thread anchored to line %d, want %d (the child bullet)", threads[1].Anchor, want)
	}
}
