package spec

import (
	"strings"
	"testing"
)

const anchorDoc = `## Considered approaches
**A. Subcommand-scoped flag set.** Parse args[1:] with its own FlagSet.
**B. Global top-level flag.** Add -format beside -file.

%% @architect: which seam should own ` + "`-format`" + ` — per-subcommand (A) or global (B)?
## Chosen approach
`

// TestFindAnchorResolvesAMarkerToItsThread: an agent answering its own
// question hands back the question's text, and that text lives on a %%
// marker line. Searching content lines alone missed it, the answer was
// appended at the foot of the document, and the thread it answered stayed
// open — so the next session read a spec where nobody had answered.
func TestFindAnchorResolvesAMarkerToItsThread(t *testing.T) {
	line, ok := FindAnchor(anchorDoc, "which seam should own")
	if !ok {
		t.Fatal("a marker's own text found no anchor")
	}
	// the answer lands on the marker's own line, so AddComment appends it
	// directly below the question rather than into some other thread
	if got := splitLine(anchorDoc, line); got != "%% @architect: which seam should own `-format` — per-subcommand (A) or global (B)?" {
		t.Fatalf("anchor line = %q, want the question marker itself", got)
	}

	// and the resolution written there closes the thread
	out, err := AddComment(anchorDoc, line, "user", "2026-09-14", "resolved — A. Subcommand-scoped")
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range Parse(out).Threads() {
		if !th.Resolved {
			t.Errorf("thread at line %d still open after the answer:\n%s", th.Anchor, out)
		}
	}
}

// A content line still wins over a marker that also contains the snippet:
// the marker pass is a fallback, not a competitor.
func TestFindAnchorPrefersContentOverMarker(t *testing.T) {
	doc := "the seam is here\n%% @architect: the seam is here?\n"
	line, ok := FindAnchor(doc, "the seam is here")
	if !ok || line != 1 {
		t.Fatalf("line=%d ok=%v, want the content line 1", line, ok)
	}
}

// Two markers carrying the snippet are as ambiguous as two content lines.
func TestFindAnchorRefusesAmbiguousMarkers(t *testing.T) {
	doc := "anchor one\n%% @architect: pick a seam\nanchor two\n%% @architect: pick a seam\n"
	if _, ok := FindAnchor(doc, "pick a seam"); ok {
		t.Error("two matching markers resolved anyway; it must fail closed")
	}
}

// A marker opening the document has no content line above it to attach to.
func TestFindAnchorRefusesAMarkerWithNoAnchor(t *testing.T) {
	if _, ok := FindAnchor("%% @architect: orphaned question\nbody\n", "orphaned question"); ok {
		t.Error("a marker with nothing above it resolved anyway")
	}
}

// splitLine returns the 1-based nth line of doc.
func splitLine(doc string, n int) string {
	lines := strings.Split(doc, "\n")
	if n < 1 || n > len(lines) {
		return ""
	}
	return lines[n-1]
}
