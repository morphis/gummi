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

func TestThreadsAndOpenQuestions(t *testing.T) {
	d := Parse(sample)
	threads := d.Threads()
	if len(threads) != 2 {
		t.Fatalf("got %d threads, want 2: %+v", len(threads), threads)
	}
	if !threads[0].Resolved {
		t.Error("thread 0 has a resolution marker; should be resolved")
	}
	if threads[1].Resolved {
		t.Error("thread 1 has no resolution; should be open")
	}
	open := d.OpenQuestions()
	if len(open) != 1 || open[0].Anchor != 9 {
		t.Fatalf("open questions = %+v, want the anchor-9 thread", open)
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
	if scope < 0 || !(problem < scope && scope < considered) {
		t.Errorf("Out of scope section misplaced: problem=%d scope=%d considered=%d", problem, scope, considered)
	}
	if DraftFilename(f) != "FD-007-search.md" {
		t.Errorf("draft filename = %s", DraftFilename(f))
	}
}
